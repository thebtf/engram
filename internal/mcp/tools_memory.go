package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	gormlib "gorm.io/gorm"

	"github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/lifecycle"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/internal/writegate"
	"github.com/thebtf/engram/pkg/models"
)

// vnextFEnabled reports whether the engram vNext Milestone F flag is on per
// spec FR-F1 / RI-F1. Centralised so all flag-gated branches share the exact
// truthy check ("true" — string equality with the env var).
func vnextFEnabled() bool {
	return os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true"
}

// isValidPrivacyScope validates the 4-tier privacy_scope enum added by
// migration 125 (T001) and consumed by scope.Resolve (T003/T003b). Mirrors
// the memories_privacy_scope_chk CHECK constraint exactly.
func isValidPrivacyScope(s string) bool {
	switch s {
	case "private", "project", "shared", "global":
		return true
	default:
		return false
	}
}

// derivePrivacyScopeFromLegacy maps the legacy 2-tier scope tag value to the
// 4-tier privacy_scope enum for the dual-field deprecation window (RI-F2).
// Empty input returns empty (caller decides default handling).
func derivePrivacyScopeFromLegacy(legacy string) string {
	switch legacy {
	case "project":
		return "project"
	case "global":
		return "global"
	default:
		return ""
	}
}

// deriveLegacyScopeFromPrivacy is the inverse of derivePrivacyScopeFromLegacy:
// when a caller supplies only the new 4-tier `privacy_scope` field, the legacy
// 2-tier `scope` value used for downstream tagging + responses must be
// back-derived so legacy consumers parsing `scope:project` / `scope:global`
// (or the legacy `scope` JSON field) see a value consistent with the 4-tier
// intent. Mapping per ADR-F-005 / RI-F2:
//
//	private -> project (legacy 2-tier has no `private`; collapse to the
//	                    more conservative `project`)
//	project -> project
//	shared  -> global  (legacy 2-tier collapses `shared` into `global`)
//	global  -> global
//
// Codex P2 cycle-7 fix on 209a06e: without this mapping, callers using only
// the new field would see legacy `scope:project` tags / responses even when
// they wrote `privacy_scope="shared"`, under-sharing data for legacy 2-tier
// consumers.
func deriveLegacyScopeFromPrivacy(privacy string) string {
	switch privacy {
	case "private", "project":
		return "project"
	case "shared", "global":
		return "global"
	default:
		return ""
	}
}

func isValidStoreObservationType(obsType models.ObservationType) bool {
	switch obsType {
	case models.ObsTypeDecision,
		models.ObsTypeBugfix,
		models.ObsTypeFeature,
		models.ObsTypeRefactor,
		models.ObsTypeDiscovery,
		models.ObsTypeChange,
		models.ObsTypeGuidance,
		models.ObsTypeCredential,
		models.ObsTypeEntity,
		models.ObsTypeWiki,
		models.ObsTypePitfall,
		models.ObsTypeOperational,
		models.ObsTypeTimeline:
		return true
	default:
		return false
	}
}

// handleStoreMemory explicitly stores a memory in the v5 memories table.
func (s *Server) handleStoreMemory(ctx context.Context, args json.RawMessage) (string, error) {
	if s.memoryStore == nil {
		return "", fmt.Errorf("memory store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Tags         []string
		Rejected     []string
		Supersedes   []int64
		Content      string
		Title        string
		Type         string
		Scope        string
		PrivacyScope string // T004 — vNext F, 4-tier enum
		SessionID    string // T004 — vNext F, caller's session for SourceSessions
		Project      string
		AgentSource  string
		Importance   *float64
		TtlDays      *int
		AlwaysInject bool
	}
	params.Tags = coerceStringSlice(m["tags"])
	params.Rejected = coerceStringSlice(m["rejected"])
	params.Supersedes = coerceInt64Slice(m["supersedes"])
	params.Content = coerceString(m["content"], "")
	params.Title = coerceString(m["title"], "")
	params.Type = coerceString(m["type"], "")
	params.Scope = coerceString(m["scope"], "")
	params.PrivacyScope = coerceString(m["privacy_scope"], "")
	params.SessionID = coerceString(m["session_id"], "")
	params.AgentSource = coerceString(m["agent_source"], "")
	if config.Get().EnforceSourceProject {
		params.Project = projectFromContext(ctx)
		if params.Project == "" {
			params.Project = coerceString(m["project"], "")
		}
	} else {
		params.Project = coerceString(m["project"], "")
	}
	params.AlwaysInject = coerceBool(m["always_inject"], false)
	if v, ok := m["importance"]; ok && v != nil {
		f := coerceFloat64(v, 0)
		params.Importance = &f
	}
	if v, ok := m["ttl_days"]; ok && v != nil {
		d := coerceInt(v, 0)
		if d > 0 {
			params.TtlDays = &d
		}
	}
	if params.Content == "" {
		return "", fmt.Errorf("content is required for store_memory")
	}
	if params.Importance != nil && (*params.Importance < 0 || *params.Importance > 1) {
		return "", fmt.Errorf("importance must be between 0 and 1")
	}

	cfg := config.Get()
	hardLimit := cfg.StoreMemoryHardLimit
	if hardLimit <= 0 {
		hardLimit = 10000
	}
	softLimit := cfg.StoreMemorySoftLimit
	if softLimit <= 0 {
		softLimit = 1000
	}
	if utf8.RuneCountInString(params.Content) > hardLimit {
		return "", fmt.Errorf("content exceeds maximum length of %d characters", hardLimit)
	}
	if utf8.RuneCountInString(params.Content) > softLimit {
		params.Content = string([]rune(params.Content)[:softLimit])
		log.Debug().
			Int("soft_limit", softLimit).
			Msg("store_memory: content truncated to soft limit")
	}

	if privacy.ContainsSecrets(params.Content) {
		log.Warn().Msg("store_memory: content contains secrets — redacting before storage")
		params.Content = privacy.RedactSecrets(params.Content)
	}

	resolvedScope := params.Scope
	if resolvedScope == "" {
		resolvedScope = string(models.ScopeProject)
	}
	if resolvedScope != string(models.ScopeProject) && resolvedScope != string(models.ScopeGlobal) {
		return "", fmt.Errorf("invalid scope %q: must be one of project, global", resolvedScope)
	}

	// T004 (engram vNext Milestone F TG1) — resolve the 4-tier privacy_scope.
	// Flag OFF: leave empty so the DB DEFAULT 'project' from migration 125
	// applies on the column. Flag ON: prefer explicit privacy_scope param;
	// fall back to deriving from the legacy 2-tier scope (RI-F2 bridge).
	// Validate against the migration 125 CHECK constraint enum.
	var resolvedPrivacyScope string
	if vnextFEnabled() {
		resolvedPrivacyScope = params.PrivacyScope
		if resolvedPrivacyScope == "" {
			resolvedPrivacyScope = derivePrivacyScopeFromLegacy(resolvedScope)
		}
		if resolvedPrivacyScope == "" {
			resolvedPrivacyScope = "project"
		}
		if !isValidPrivacyScope(resolvedPrivacyScope) {
			// Structured error per spec FR-F1 AMEND (T005): clients parse the
			// 'invalid_privacy_scope:' prefix as an error_code; the trailing
			// message names the offending value + accepted enum.
			return "", fmt.Errorf("invalid_privacy_scope: %q must be one of private, project, shared, global", resolvedPrivacyScope)
		}
		// Codex P2 cycle-7 fix on 209a06e: when the caller used only the new
		// 4-tier `privacy_scope` field (legacy `scope` not provided), back-
		// derive the legacy 2-tier `scope` from the resolved privacy tier so
		// downstream tag synthesis (`scope:project` / `scope:global`) and
		// response `scope` field stay consistent with the 4-tier intent.
		// Without this, `privacy_scope="shared"` would still tag/report as
		// `scope:project` because of the early default at line 167-170,
		// under-sharing data for legacy 2-tier consumers (RI-F2 bridge).
		if params.Scope == "" {
			if derived := deriveLegacyScopeFromPrivacy(resolvedPrivacyScope); derived != "" {
				resolvedScope = derived
			}
		}
	}

	if params.Project == "" && !(params.AlwaysInject && resolvedScope == string(models.ScopeGlobal)) {
		return "", fmt.Errorf("project is required for store_memory in v5 unless always_inject=true with scope=global")
	}

	obsTypeStr := params.Type
	if obsTypeStr == "" {
		cl := strings.ToLower(params.Content)
		switch {
		case strings.Contains(cl, "decided") || strings.Contains(cl, "decision") || strings.Contains(cl, "chose"):
			obsTypeStr = "decision"
		case strings.Contains(cl, "bug") || strings.Contains(cl, "fix") || strings.Contains(cl, "error"):
			obsTypeStr = "bugfix"
		case strings.Contains(cl, "pattern") || strings.Contains(cl, "practice") || strings.Contains(cl, "convention"):
			obsTypeStr = "discovery"
		case strings.Contains(cl, "refactor") || strings.Contains(cl, "rename") || strings.Contains(cl, "move"):
			obsTypeStr = "refactor"
		default:
			obsTypeStr = "feature"
		}
	}
	obsType := models.ObservationType(obsTypeStr)
	if !isValidStoreObservationType(obsType) {
		return "", fmt.Errorf("invalid type %q: must be one of decision, bugfix, feature, refactor, discovery, change, guidance, credential, entity, wiki, pitfall, operational, timeline", obsTypeStr)
	}

	seen := make(map[string]bool)
	tags := make([]string, 0, len(params.Tags)+3)
	for _, tag := range params.Tags {
		for _, part := range expandTagHierarchy(tag) {
			if !seen[part] {
				seen[part] = true
				tags = append(tags, part)
			}
		}
	}

	if !seen["type:"+obsTypeStr] {
		tags = append(tags, "type:"+obsTypeStr)
		seen["type:"+obsTypeStr] = true
	}
	if !seen["scope:"+resolvedScope] {
		tags = append(tags, "scope:"+resolvedScope)
		seen["scope:"+resolvedScope] = true
	}
	if params.TtlDays != nil && !seen[fmt.Sprintf("ttl:%d", *params.TtlDays)] {
		ttlTag := fmt.Sprintf("ttl:%d", *params.TtlDays)
		tags = append(tags, ttlTag)
		seen[ttlTag] = true
	}

	ttlDays := computeTTLDays(params.TtlDays, tags)
	ttlApplied := ttlDays > 0
	if ttlApplied {
		ttlTag := fmt.Sprintf("ttl:%d", ttlDays)
		if !seen[ttlTag] {
			tags = append(tags, ttlTag)
			seen[ttlTag] = true
		}
	}

	if params.AlwaysInject {
		if s.behavioralRulesStore == nil {
			return "", fmt.Errorf("always_inject=true requires behavioral rules store")
		}
		var project *string
		if resolvedScope != string(models.ScopeGlobal) {
			p := params.Project
			project = &p
		}
		rule := &models.BehavioralRule{
			Project:  project,
			Content:  params.Content,
			Priority: 0,
		}
		created, err := s.behavioralRulesStore.Create(ctx, rule)
		if err != nil {
			return "", fmt.Errorf("store behavioral rule: %w", err)
		}

		result := map[string]any{
			"id":            created.ID,
			"title":         truncateTitle(created.Content, 80),
			"type":          string(models.ObsTypeGuidance),
			"scope":         resolvedScope,
			"storage":       "behavioral_rules",
			"always_inject": true,
			"message":       "Behavioral rule stored successfully",
		}
		out, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal result: %w", err)
		}
		return string(out), nil
	}

	agentSource := string(models.AgentUnknown)
	if params.AgentSource != "" {
		if models.IsValidAgentSource(params.AgentSource) {
			agentSource = params.AgentSource
		} else {
			return "", fmt.Errorf("invalid agent_source %q: must be one of claude-code, codex, gemini, other, unknown", params.AgentSource)
		}
	}

	// --- Supersession: mark old memories and compute inherited importance ---
	// importanceBase starts at 0.5. When superseding, it is raised to
	// max(0.5, oldImportance * 0.7) based on the first superseded memory.
	inheritedImportance := 0.5
	var primarySupersededID *int64
	var supersededIDs []int64
	for _, sid := range params.Supersedes {
		if sid <= 0 {
			continue
		}
		oldImportance, supErr := s.memoryStore.Supersede(ctx, sid)
		if supErr != nil {
			log.Warn().Err(supErr).Int64("superseded_id", sid).Msg("store_memory: supersede failed")
			continue
		}
		supersededIDs = append(supersededIDs, sid)
		if primarySupersededID == nil {
			primarySupersededID = &sid
			inherited := oldImportance * 0.7
			if inherited > inheritedImportance {
				inheritedImportance = inherited
			}
		}
	}

	// --- Write gate (vNext Phase A) ---
	// When ENGRAM_VNEXT_ENABLED=true, evaluate novelty before creation.
	// Low-novelty memories are stored with Status="flagged" so callers can
	// detect duplicates; the quality_signals key is added to the response.
	var gateResult writegate.GateResult
	vnextEnabled := os.Getenv("ENGRAM_VNEXT_ENABLED") == "true"
	if vnextEnabled && params.Project != "" {
		existing, listErr := s.memoryStore.List(ctx, params.Project, 100)
		if listErr != nil {
			log.Warn().Err(listErr).Msg("store_memory: write gate could not load existing memories, skipping gate")
		} else {
			gateResult = writegate.Check(ctx, params.Content, existing)
		}
	}

	memory := &models.Memory{
		Project:        params.Project,
		Content:        params.Content,
		Tags:           tags,
		SourceAgent:    agentSource,
		ImportanceBase: inheritedImportance,
		SupersedesID:   primarySupersededID,
	}

	// T004 — vNext F TG1: populate the new lifecycle/identity fields when the
	// flag is ON. PrivacyScope falls through to DB DEFAULT 'project' when
	// flag is OFF (empty Go string lets the column default apply). Workstation
	// id is derived from the caller's keycard via auth.Identity.WorkstationID
	// added in T003b. SourceSessions is populated from the explicit session_id
	// param when supplied — the MCP layer has no implicit session-id ctx key
	// in v6.4.x, so callers (e.g., the engram client proxy) advertise their
	// session via the param. When absent, SourceSessions stays empty and
	// scope.Resolve falls back to the workstation-only-suffices branch per
	// spec FR-F1 AMEND 2026-05-25.
	if vnextFEnabled() {
		memory.PrivacyScope = resolvedPrivacyScope
		if id, ok := auth.IdentityFrom(ctx); ok {
			memory.SourceWorkstationID = id.WorkstationID()
		}
		if params.SessionID != "" {
			memory.SourceSessions = []string{params.SessionID}
		}
		// Codex P1 cycle-4 fix on 783c0be: reject private-scope writes when
		// the caller has no non-empty workstation identity. scope.Resolve
		// fail-closes private memories whose source_workstation_id is empty
		// (`internal/scope/filter.go:85-87` — "if memorySource.WorkstationID
		// == \"\" { return false }"), so persisting such a row would make
		// it permanently unreadable to every caller including the writer
		// itself. Master and bare-session sources cannot produce a
		// non-empty WorkstationID (auth/identity.go:111-116 — returns
		// KeycardID only when Source == SourceClient).
		if resolvedPrivacyScope == "private" && memory.SourceWorkstationID == "" {
			return "", fmt.Errorf("invalid_privacy_scope: private requires a non-empty workstation identity from a SourceClient keycard (master/session sources cannot write private-scope memories)")
		}
	}

	// Lifecycle fields (Milestone B): only set when lifecycle is enabled
	if os.Getenv("ENGRAM_LIFECYCLE_ENABLED") == "true" {
		if tier := coerceString(m["tier"], ""); tier != "" {
			if lifecycle.ValidTier(tier) {
				memory.Tier = tier
			}
		}
		if et := coerceString(m["epistemic_type"], ""); et != "" {
			if lifecycle.ValidEpistemicType(et) {
				memory.EpistemicType = et
			}
		}
		if def := coerceString(m["defeasibility"], ""); def != "" {
			if lifecycle.ValidDefeasibility(def) {
				memory.Defeasibility = def
			}
		}
		if memory.Stability == 0 {
			memory.Stability = lifecycle.ComputeStability(30.0, memory.Tier, memory.EpistemicType, 0)
		}
		memory.Confidence = lifecycle.ComputeConfidence(lifecycle.ConfidenceInputs{})
		memory.Retrievability = 1.0
	}
	if vnextEnabled && gateResult.Decision == "flag" {
		memory.Status = "flagged"
	}

	created, err := s.memoryStore.Create(ctx, memory)
	if err != nil {
		return "", fmt.Errorf("store memory: %w", err)
	}

	// Async embedding: fire-and-forget goroutine so the MCP response is not blocked
	// by the embedding HTTP call. Captures local copies of created fields and store
	// pointers to avoid capturing the mutable request-scoped variables.
	if s.embeddingClient != nil && s.embeddingStore != nil {
		memID := created.ID
		memContent := created.Content
		embClient := s.embeddingClient
		embStore := s.embeddingStore
		go func() {
			gCtx := context.Background()
			vectors, embErr := embClient.Embed(gCtx, []string{memContent})
			if embErr != nil {
				log.Error().Err(embErr).Int64("memory_id", memID).Msg("async embedding failed")
				return
			}
			if len(vectors) == 0 || len(vectors[0]) == 0 {
				return
			}
			chunk := embedding.Chunk{
				MemoryID:  memID,
				Seq:       0,
				Text:      memContent,
				Embedding: pgvector.NewVector(vectors[0]),
				Model:     embClient.Model(),
			}
			if storeErr := embStore.StoreChunks(gCtx, []embedding.Chunk{chunk}); storeErr != nil {
				log.Error().Err(storeErr).Int64("memory_id", memID).Msg("async embedding store failed")
				return // don't run cosine check if store failed
			}
			// Async cosine duplicate guard
			writegate.CheckCosine(gCtx, memID, memContent, embClient, embStore, s.memoryStore)
		}()
	}

	result := map[string]any{
		"id":      created.ID,
		"title":   truncateTitle(created.Content, 80),
		"type":    obsTypeStr,
		"scope":   resolvedScope,
		"storage": "memories",
		"message": "Memory stored successfully",
	}
	// T004 — vNext F TG1: dual-field response per RI-F2. Legacy `scope`
	// (2-tier) continues to appear above for backward compat; add new
	// `privacy_scope` (4-tier) alongside when flag is ON.
	if vnextFEnabled() {
		result["privacy_scope"] = resolvedPrivacyScope
		if created.SourceWorkstationID != "" {
			result["source_workstation_id"] = created.SourceWorkstationID
		}
		if len(created.SourceSessions) > 0 {
			result["source_sessions"] = created.SourceSessions
		}
	}
	if vnextEnabled {
		result["quality_signals"] = map[string]any{
			"gate_result":      gateResult.Decision,
			"novelty_score":    gateResult.NoveltyScore,
			"max_jaccard":      gateResult.MaxJaccard,
			"similar_existing": gateResult.SimilarExisting,
		}
	}
	if len(supersededIDs) > 0 {
		result["superseded_ids"] = supersededIDs
	}
	if ttlApplied {
		result["ttl_days"] = ttlDays
	}
	if params.Importance != nil {
		result["importance_note"] = "importance metadata is not stored in v5 memories schema"
	}
	if len(params.Rejected) > 0 {
		result["rejected_note"] = "rejected alternatives are not stored in v5 memories schema"
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// computeTTLDays determines the TTL for an observation based on explicit override or auto-TTL from tags.
// Returns 0 if no TTL should be applied.
func computeTTLDays(explicit *int, concepts []string) int {
	// 1. Explicit override takes priority
	if explicit != nil && *explicit > 0 {
		return *explicit
	}

	// 2. Auto-TTL only applies to observations with "verified" tag
	hasVerified := false
	for _, c := range concepts {
		if c == "verified" {
			hasVerified = true
			break
		}
	}
	if !hasVerified {
		return 0
	}

	// 3. Auto-TTL by concept tags (exact match) — use minimum TTL from all matching tags
	autoTTL := map[string]int{
		"api": 7, "endpoint": 7,
		"library": 30, "framework": 30,
		"language-feature": 90,
		"architecture":     180, "pattern": 180,
	}
	minTTL := 0
	for _, c := range concepts {
		if days, ok := autoTTL[c]; ok && (minTTL == 0 || days < minTTL) {
			minTTL = days
		}
	}
	if minTTL > 0 {
		return minTTL
	}

	// 4. Default for verified facts with no matching tag
	return 30
}

// truncateTitle creates a short title from content, truncating at a word boundary.
func truncateTitle(content string, maxLen int) string {
	content = strings.TrimSpace(content)
	if utf8.RuneCountInString(content) <= maxLen {
		return content
	}
	truncated := string([]rune(content)[:maxLen])
	if i := strings.LastIndexAny(truncated, " \t\n"); i > 0 {
		truncated = truncated[:i]
	}
	return truncated + "..."
}

// handleRecallMemory retrieves memories from the v5 memories table using list + in-memory filtering.
func (s *Server) handleRecallMemory(ctx context.Context, args json.RawMessage) (string, error) {
	if s.memoryStore == nil {
		return "", fmt.Errorf("recall_memory: memory store not configured")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	query := strings.TrimSpace(coerceString(m["query"], ""))
	obsType := strings.TrimSpace(coerceString(m["type"], ""))
	format := coerceString(m["format"], "")
	limit := coerceInt(m["limit"], 0)
	project := strings.TrimSpace(coerceString(m["project"], ""))
	tags := coerceStringSlice(m["tags"])

	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	if format == "" {
		format = "text"
	}
	if project == "" {
		project = strings.TrimSpace(projectFromContext(ctx))
	}
	if project == "" {
		return "", fmt.Errorf("project is required for recall_memory in v5")
	}

	// T004 + T005 + codex P1 cycle-6 fix on c6006f7: the recall_memory MCP
	// tool advertises session_id + include_scopes in its schema (see
	// server.go) and dispatches here. Without scope.Resolve enforcement, a
	// caller knowing the project + query under ENGRAM_VNEXT_F_ENABLED=true
	// could retrieve `privacy_scope="private"` rows owned by another
	// workstation — bypassing the same visibility model handleRecallSearch
	// enforces on the sibling `recall` tool. Mirror the gating + caller
	// identity build, validate include_scopes behind the flag (T005
	// contract: runtime behavior env-gated), and use the batch-loop
	// ListWithOffset pattern when scope is active so invisible newest rows
	// do not truncate visible recall.
	callerSessionID := strings.TrimSpace(coerceString(m["session_id"], ""))
	scopeEnabled := os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true"
	includeScopes := make(map[string]bool)
	if scopeEnabled {
		for _, sc := range coerceStringSlice(m["include_scopes"]) {
			switch sc {
			case "private", "project", "shared", "global":
				includeScopes[sc] = true
			default:
				return "", fmt.Errorf("invalid_include_scopes: %q must be one of private, project, shared, global", sc)
			}
		}
	}
	var caller scope.KeycardContext
	if scopeEnabled {
		caller.SessionID = callerSessionID
		if id, ok := auth.IdentityFrom(ctx); ok {
			caller.WorkstationID = id.WorkstationID()
		}
	}

	queryLower := strings.ToLower(query)
	tagSet := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tagSet[strings.ToLower(tag)] = struct{}{}
	}

	// keepMemory applies the existing query/type/tags filters AND the new
	// vNext-F scope filter to a single memory. Returns true when it should
	// be included in the response.
	keepMemory := func(mem *models.Memory) bool {
		contentLower := strings.ToLower(mem.Content)
		if queryLower != "" && !strings.Contains(contentLower, queryLower) {
			matchedTag := false
			for _, tag := range mem.Tags {
				if strings.Contains(strings.ToLower(tag), queryLower) {
					matchedTag = true
					break
				}
			}
			if !matchedTag {
				return false
			}
		}
		if obsType != "" {
			typeTag := strings.ToLower("type:" + obsType)
			typeMatched := false
			for _, tag := range mem.Tags {
				if strings.ToLower(tag) == typeTag {
					typeMatched = true
					break
				}
			}
			if !typeMatched {
				return false
			}
		}
		if len(tagSet) > 0 {
			tagMatched := false
			for _, tag := range mem.Tags {
				if _, ok := tagSet[strings.ToLower(tag)]; ok {
					tagMatched = true
					break
				}
			}
			if !tagMatched {
				return false
			}
		}
		if scopeEnabled {
			memScope := mem.PrivacyScope
			if memScope == "" {
				memScope = "project"
			}
			if len(includeScopes) > 0 && !includeScopes[memScope] {
				return false
			}
			meta := scope.SourceMeta{
				WorkstationID: mem.SourceWorkstationID,
				Sessions:      mem.SourceSessions,
			}
			if !scope.Resolve(caller, memScope, meta) {
				return false
			}
		}
		return true
	}

	filtered := make([]*models.Memory, 0, limit)
	if scopeEnabled {
		// Batch-loop via ListWithOffset (codex P1 cycle-3 + cycle-4 pattern)
		// so scope-invisible newest rows do not truncate visible recall
		// before older eligible rows reach the requested limit.
		const batchSize = 500
		offset := 0
		for len(filtered) < limit {
			batch, err := s.memoryStore.ListWithOffset(ctx, project, batchSize, offset)
			if err != nil {
				return "", fmt.Errorf("recall_memory: %w", err)
			}
			if len(batch) == 0 {
				break
			}
			for _, mem := range batch {
				if keepMemory(mem) {
					filtered = append(filtered, mem)
					if len(filtered) >= limit {
						break
					}
				}
			}
			offset += len(batch)
			if len(batch) < batchSize {
				break
			}
		}
	} else {
		// Flag-OFF — original single-fetch shape preserves v6.4.x
		// byte-identity for legacy callers using the recall_memory tool.
		fetchLimit := limit
		if query != "" || obsType != "" || len(tags) > 0 {
			const candidateMultiplier = 10
			const minCandidatePool = 1000
			fetchLimit = limit * candidateMultiplier
			if fetchLimit < minCandidatePool {
				fetchLimit = minCandidatePool
			}
		}
		memories, err := s.memoryStore.List(ctx, project, fetchLimit)
		if err != nil {
			return "", fmt.Errorf("recall_memory: %w", err)
		}
		for _, mem := range memories {
			if keepMemory(mem) {
				filtered = append(filtered, mem)
				if len(filtered) >= limit {
					break
				}
			}
		}
	}

	// Reconsolidation: every retrieval updates access_count, last_retrieved_at,
	// and recalculates stability (Nader 2000). Fire-and-forget to avoid blocking.
	if os.Getenv("ENGRAM_LIFECYCLE_ENABLED") == "true" && len(filtered) > 0 {
		go func() {
			for _, mem := range filtered {
				fields := map[string]any{
					"access_count":      gormlib.Expr("access_count + 1"),
					"last_retrieved_at": gormlib.Expr("now()"),
				}
				if mem.Stability > 0 {
					newStability := lifecycle.Reconsolidate(mem.Stability, mem.Retrievability)
					if newStability != mem.Stability {
						fields["stability"] = newStability
					}
				}
				_ = s.memoryStore.UpdateLifecycleFields(context.Background(), mem.ID, fields)
			}
		}()
	}

	switch format {
	case "items":
		type item struct {
			Tags        []string `json:"tags,omitempty"`
			Title       string   `json:"title"`
			Type        string   `json:"type,omitempty"`
			Content     string   `json:"content"`
			SourceAgent string   `json:"source_agent,omitempty"`
			Project     string   `json:"project"`
			ID          int64    `json:"id"`
		}
		items := make([]item, 0, len(filtered))
		for _, mem := range filtered {
			memoryType := ""
			for _, tag := range mem.Tags {
				if strings.HasPrefix(tag, "type:") {
					memoryType = strings.TrimPrefix(tag, "type:")
					break
				}
			}
			items = append(items, item{
				ID:          mem.ID,
				Title:       truncateTitle(mem.Content, 80),
				Type:        memoryType,
				Content:     mem.Content,
				Tags:        mem.Tags,
				SourceAgent: mem.SourceAgent,
				Project:     mem.Project,
			})
		}
		out, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal result: %w", err)
		}
		return string(out), nil

	case "detailed":
		out, err := json.MarshalIndent(filtered, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal result: %w", err)
		}
		return string(out), nil

	default:
		if len(filtered) == 0 {
			return "No memories found matching the query.", nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d memories for query: %q\n\n", len(filtered), query))
		for i, mem := range filtered {
			typeLabel := "MEMORY"
			for _, tag := range mem.Tags {
				if strings.HasPrefix(tag, "type:") {
					typeLabel = strings.ToUpper(strings.TrimPrefix(tag, "type:"))
					break
				}
			}
			sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, typeLabel, truncateTitle(mem.Content, 80)))
			content := mem.Content
			if len(content) > 300 {
				content = content[:300] + "..."
			}
			sb.WriteString(fmt.Sprintf("   %s\n", content))
			if len(mem.Tags) > 0 {
				sb.WriteString(fmt.Sprintf("   tags: %s\n", strings.Join(mem.Tags, ", ")))
			}
			sb.WriteString("\n")
		}
		return sb.String(), nil
	}
}

// handleRateMemory is kept explicit in v5: memories do not have a rating field yet.
func (s *Server) handleRateMemory(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	id := coerceInt64(m["id"], 0)
	rating := coerceString(m["rating"], "")
	if rating == "" {
		if usefulRaw, ok := m["useful"]; ok && usefulRaw != nil {
			if coerceBool(usefulRaw, false) {
				rating = "useful"
			} else {
				rating = "not_useful"
			}
		}
	}

	if id == 0 {
		return "", fmt.Errorf("id required")
	}
	if rating != "useful" && rating != "not_useful" {
		return "", fmt.Errorf("rating must be 'useful' or 'not_useful'")
	}

	return "", fmt.Errorf("rate_memory removed in v5 (US3): memories table has no rating field yet")
}

// handleSuppressMemory suppresses a v5 memory via soft-delete in the memories table.
func (s *Server) handleSuppressMemory(ctx context.Context, args json.RawMessage) (string, error) {
	if s.memoryStore == nil {
		return "", fmt.Errorf("memory store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	id := coerceInt64(m["id"], 0)
	if id == 0 {
		return "", fmt.Errorf("id required")
	}

	if err := s.memoryStore.Delete(ctx, id); err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			return "", fmt.Errorf("suppress_memory: memory %d not found", id)
		}
		return "", fmt.Errorf("suppress_memory: %w", err)
	}

	return fmt.Sprintf("Memory %d suppressed", id), nil
}
