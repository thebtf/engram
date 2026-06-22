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
	engramgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/lifecycle"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/redaction"
	"github.com/thebtf/engram/internal/reranking"
	"github.com/thebtf/engram/internal/retrieval"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/internal/staleness"
	"github.com/thebtf/engram/internal/writegate"
	"github.com/thebtf/engram/internal/writelint"
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

func applyPrincipalMemoryMetadata(ctx context.Context, mem *models.Memory, agentVisibility, domain string) error {
	visibility := strings.TrimSpace(agentVisibility)
	if visibility != "" && !models.IsValidAgentVisibility(visibility) {
		return fmt.Errorf("invalid_agent_visibility: %q must be one of private, shared", visibility)
	}

	mem.Domain = strings.TrimSpace(domain)
	if id, ok := auth.IdentityFrom(ctx); ok {
		if principal, principalKind, hasOwner := id.MemoryOwner(); hasOwner {
			mem.OwnerPrincipal = principal
			mem.OwnerPrincipalKind = principalKind
		}
	}
	if visibility != "" {
		if mem.OwnerPrincipal == "" {
			return fmt.Errorf("invalid_agent_visibility: principal is required for agent_visibility")
		}
		mem.AgentVisibility = visibility
	} else if mem.OwnerPrincipal != "" {
		mem.AgentVisibility = models.AgentVisibilityShared
	}
	return nil
}

func addPrincipalMemoryFields(result map[string]any, mem *models.Memory) {
	if mem.OwnerPrincipal != "" {
		result["owner_principal"] = mem.OwnerPrincipal
	}
	if mem.OwnerPrincipalKind != "" {
		result["owner_principal_kind"] = mem.OwnerPrincipalKind
	}
	if mem.AgentVisibility != "" {
		result["agent_visibility"] = mem.AgentVisibility
	}
	if mem.Domain != "" {
		result["domain"] = mem.Domain
	}
}

// memoryEditor is the minimal interface over *gorm.MemoryStore that
// handleEditMemory uses. Kept narrow so tests can inject a mock without
// wiring a full database (mirrors the auditWriter test-injection pattern).
type memoryEditor interface {
	Get(ctx context.Context, id int64) (*models.Memory, error)
	Update(ctx context.Context, m *models.Memory) (*models.Memory, error)
}

// effectiveMemoryEditor returns the active memoryEditor for s.
// Precedence: testMemoryEditor (set in tests) → concrete memoryStore.
func (s *Server) effectiveMemoryEditor() memoryEditor {
	if s.testMemoryEditor != nil {
		return s.testMemoryEditor
	}
	if s.memoryStore != nil {
		return s.memoryStore
	}
	return nil
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
// T044 (FR-F6.b): dry_run=true returns a legacy-path preview JSON without any DB writes.
// The nil-store guard is deferred until after dry_run is parsed so that dry_run=true
// always succeeds (TG5-absent nil-safe seam).
func (s *Server) handleStoreMemory(ctx context.Context, args json.RawMessage) (string, error) {
	// T035: write-lint orchestrator has its own MemoryStoreInterface; nil memoryStore
	// is only fatal on the legacy create path. writeLintEnabled and dry_run are both
	// exemptions; the nil-store guard is deferred until after params are parsed so both
	// exemptions can be evaluated together.
	writeLintEnabled := s.writeLint != nil && vnextFEnabled()

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Tags            []string
		Rejected        []string
		Supersedes      []int64
		Content         string
		Title           string
		Type            string
		Scope           string
		PrivacyScope    string // T004 — vNext F, 4-tier enum
		SessionID       string // T004 — vNext F, caller's session for SourceSessions
		AgentVisibility string
		Domain          string
		Project         string
		AgentSource     string
		Importance      *float64
		TtlDays         *int
		AlwaysInject    bool
		DryRun          bool // T044 — FR-F6.b dry-run preview
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
	params.AgentVisibility = coerceString(m["agent_visibility"], "")
	params.Domain = coerceString(m["domain"], "")
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
	params.DryRun = coerceBool(m["dry_run"], false)
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

	// Deferred nil-store guard: runs after params are parsed so all exemptions are known.
	// Exemptions: writeLintEnabled (T035 — orchestrator manages its own store) and
	// dry_run=true (T044 — preview exits before any DB access; no store required).
	if s.memoryStore == nil && !writeLintEnabled && !params.DryRun {
		return "", fmt.Errorf("memory store not available")
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

	// ADR-F-004: operator redaction layer — runs AFTER privacy.RedactSecrets,
	// BEFORE write-lint gate. Rules are pre-compiled at startup from
	// ENGRAM_REDACTION_RULES_PATH (EC-F9: no hot-reload; restart required).
	// ScrubCompiled is the hot path: no per-call regexp.Compile (finding 7 fix).
	if len(s.redactionRules) > 0 {
		scrubbed, _, scrubErr := redaction.ScrubCompiled(params.Content, s.redactionRules)
		if scrubErr != nil {
			if errors.Is(scrubErr, redaction.ErrContentFullyRedacted) {
				// EC-F5: all content was removed by operator redaction rules.
				return "", fmt.Errorf("content_fully_redacted: all content was removed by operator redaction rules")
			}
			return "", fmt.Errorf("redaction: %w", scrubErr)
		}
		params.Content = scrubbed
	}

	// T044 dry-run return (FR-F6.b): after content size limits AND redaction so
	// would_store reflects the post-redacted content (honest preview per ADR-F-004).
	//
	// Composition with TG5 write-lint (ADR: dry_run×write-lint design):
	//   - write-lint active (writeLintEnabled=true): Phase1 would run signal detection
	//     (duplicate / conflict / supersession) before committing. dry_run does NOT
	//     invoke Phase1 because that would either commit on the no-signal path or mint
	//     a resolution token that expires unused — both are write side-effects
	//     incompatible with preview semantics. Instead, the preview declares that
	//     lint signals are "deferred" — the caller knows lint would intercept but
	//     must use a live (non-dry-run) call to see actual signals.
	//   - write-lint absent (writeLintEnabled=false / flag OFF): legacy-path preview
	//     with no lint signals (original TG6 T044 behavior).
	//
	// No DB write, no token mint, no snapshot row for dry_run in either case.
	if params.DryRun {
		dryRunNote := "dry_run preview — no memory row written; redaction applied above"
		if writeLintEnabled {
			dryRunNote = "dry_run preview — no memory row written; redaction applied; write-lint Phase1 (duplicate/conflict detection) would run on live call — signals deferred"
		}
		preview := map[string]any{
			"dry_run":     true,
			"would_store": params.Content,
			"project":     params.Project,
			"type":        params.Type,
			"scope":       params.Scope,
			"tags":        params.Tags,
			"write_lint":  writeLintEnabled,
			"note":        dryRunNote,
		}
		out, jsonErr := json.MarshalIndent(preview, "", "  ")
		if jsonErr != nil {
			return "", fmt.Errorf("store_memory dry_run marshal: %w", jsonErr)
		}
		return string(out), nil
	}

	// T035 (engram vNext Milestone F TG5) — two-phase write-lint protocol.
	//
	// Gate conditions:
	//   1. writeLint orchestrator is wired (non-nil).
	//   2. ENGRAM_VNEXT_F_ENABLED=true.
	//   3. force=true → bypass write-lint, proceed to legacy create path + audit.
	//   4. resolution_token non-empty → Phase2 delegation; return result directly.
	//   5. Otherwise → Phase1; if not stored, return Phase1 JSON; if stored, format as success.
	if s.writeLint != nil && vnextFEnabled() {
		// finding 12 fix: declare write-lint locals inside the gate block so they
		// are not allocated on the common non-writelint path.
		wlForce := coerceBool(m["force"], false)
		wlResolutionToken := coerceString(m["resolution_token"], "")
		wlOption := coerceString(m["option"], "")
		// round-3 finding 1 fix: always_inject=true routes to behavioralRulesStore
		// (legacy path below). Write-lint must not intercept it — Phase1 would run
		// against an empty-project memory list (global scope → Project=="") and fail,
		// and even for project-scoped rules Phase1 would store a plain memory instead
		// of a behavioral_rule. Treat always_inject the same as force: bypass write-lint
		// and fall through to the legacy path which handles behavioralRulesStore routing.
		wlAlwaysInject := params.AlwaysInject
		if !wlForce && !wlAlwaysInject {
			wlActor := actorFromContext(ctx)
			// Use the already-normalized params.Project (which respects
			// EnforceSourceProject) instead of reading raw m["project"]. This
			// prevents a client from bypassing source-project isolation by
			// supplying a different project in the MCP args.
			wlProject := params.Project

			// Build a memory model carrying available metadata so Phase1/Phase2
			// persist the full record (Tags, PrivacyScope, AgentSource, etc.) on
			// the no-signal path and on Phase2 create paths. SourceWorkstationID
			// and the fully-resolved privacy/scope values are not yet computed here
			// (they require the legacy normalization below), so we populate what we
			// have. The vnextF guard ensures PrivacyScope is relevant only when ON.
			wlMem := &models.Memory{
				Content:      params.Content,
				Project:      wlProject,
				Tags:         params.Tags,
				PrivacyScope: params.PrivacyScope,
				SourceAgent:  params.AgentSource,
			}
			if params.SessionID != "" {
				wlMem.SourceSessions = []string{params.SessionID}
			}
			if id, ok := auth.IdentityFrom(ctx); ok {
				wlMem.SourceWorkstationID = id.WorkstationID()
			}
			if err := applyPrincipalMemoryMetadata(ctx, wlMem, params.AgentVisibility, params.Domain); err != nil {
				return "", err
			}

			// round-4 finding 2 fix: reject private-scope writes that lack a
			// workstation identity before reaching Phase1/Phase2. The legacy
			// path performs this check at line 597 (Codex P1 cycle-4 fix).
			// Without mirroring it here, a no-signal write-lint path (Phase1
			// returns stored=true) or a Phase2 create path (ignore_signals /
			// supersede) persists a private memory with empty
			// source_workstation_id. scope.Resolve fail-closes such rows
			// (internal/scope/filter.go:85-87), making them permanently
			// unreadable — including to the writer itself.
			if wlMem.PrivacyScope == "private" && wlMem.SourceWorkstationID == "" {
				return "", fmt.Errorf("invalid_privacy_scope: private requires a non-empty workstation identity from a SourceClient keycard (master/session sources cannot write private-scope memories)")
			}

			if wlResolutionToken != "" {
				// Phase2: caller is committing with a previously minted token.
				p2req := writelint.Phase2Request{
					Token:          wlResolutionToken,
					Option:         wlOption,
					Content:        params.Content,
					Project:        wlProject,
					Actor:          wlActor,
					TargetMemoryID: nil,
					Mem:            wlMem,
				}
				if tv, ok := m["target_memory_id"]; ok && tv != nil {
					tid := coerceInt(tv, 0)
					if tid > 0 {
						tid64 := int64(tid)
						p2req.TargetMemoryID = &tid64
					}
				}
				p2resp, p2err := s.writeLint.Phase2(ctx, p2req)
				if p2err != nil {
					return "", fmt.Errorf("write_lint_phase2: %w", p2err)
				}
				// Phase2 also COMMITS the content (ignore_signals / supersede /
				// merge_with / link_contradiction), so the rank-3 staleness advisory must
				// fire here too — otherwise relative-time content that required conflict
				// resolution commits without the nudge (Codex review). p2resp is a typed
				// struct; round-trip it to a map to attach the advisory key when relevant.
				if terms := staleness.DetectRelativeTime(params.Content); len(terms) > 0 {
					out, marshalErr := marshalWithStaleAdvisory(p2resp, terms)
					if marshalErr != nil {
						return "", fmt.Errorf("write_lint_phase2: marshal: %w", marshalErr)
					}
					return out, nil
				}
				out, marshalErr := json.MarshalIndent(p2resp, "", "  ")
				if marshalErr != nil {
					return "", fmt.Errorf("write_lint_phase2: marshal: %w", marshalErr)
				}
				return string(out), nil
			}

			// Phase1: inspect for duplicates/conflicts/supersessions.
			p1resp, p1err := s.writeLint.Phase1(ctx, wlMem, wlActor)
			if p1err != nil {
				return "", fmt.Errorf("write_lint_phase1: %w", p1err)
			}
			if !p1resp.Stored {
				// Signals detected — return Phase1 response to caller for resolution.
				out, marshalErr := json.MarshalIndent(p1resp, "", "  ")
				if marshalErr != nil {
					return "", fmt.Errorf("write_lint_phase1: marshal: %w", marshalErr)
				}
				return string(out), nil
			}
			// finding 6 fix: no-signal stored=true carries the same fields as the
			// legacy store_memory response (NFR-F1): id, storage, scope, privacy_scope,
			// quality_signals. Phase1 returns MemoryID when Stored=true.
			wlResult := map[string]any{
				"stored":          true,
				"id":              p1resp.MemoryID,
				"storage":         "memories",
				"scope":           params.Scope,
				"privacy_scope":   params.PrivacyScope,
				"quality_signals": []any{},
				"message":         "Memory stored successfully via write-lint (no conflicts detected)",
			}
			addPrincipalMemoryFields(wlResult, wlMem)
			// Rank-3 staleness advisory must also fire on the write-lint success path —
			// this is the primary store path when ENGRAM_VNEXT_F_ENABLED=true, the same
			// config that activates the serve-time hint, so the advisory cannot be
			// legacy-path-only (Codex review). Keyed on params.Content (post-redaction).
			if terms := staleness.DetectRelativeTime(params.Content); len(terms) > 0 {
				wlResult["staleness_advisory"] = staleAdvisory(terms)
			}
			out, marshalErr := json.MarshalIndent(wlResult, "", "  ")
			if marshalErr != nil {
				return "", fmt.Errorf("write_lint_phase1: marshal: %w", marshalErr)
			}
			return string(out), nil
		}
		// wlForce=true: fall through to legacy create path; audit "legacy_force_write" below.
	}
	// force=true OR writeLint not wired: fall through to legacy create path.
	// Require a live memoryStore for the legacy create path.
	if s.memoryStore == nil {
		return "", fmt.Errorf("memory store not available")
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
		// Codex P2 cycle-7 fix on 209a06e + cycle-9 fix on 69398d9: under
		// flag ON, the 4-tier `privacy_scope` is the authoritative field;
		// the legacy 2-tier `scope` must be a derived view of it so the two
		// representations cannot disagree on a single write. Cycle-7
		// originally only back-derived when `params.Scope` was empty, but
		// that left conflicting explicit pairs intact (e.g.,
		// `scope="project"` + `privacy_scope="shared"` would store shared
		// visibility while emitting legacy `scope:project` — RI-F2 bridge
		// gap). Always recompute `resolvedScope` from the resolved privacy
		// tier when `params.PrivacyScope` was explicitly provided OR
		// `params.Scope` was omitted. Mapping per ADR-F-005:
		//   private/project -> project
		//   shared/global   -> global
		// If the caller provided ONLY legacy `scope` (no `privacy_scope`),
		// the early derivation at line 184 already produced a consistent
		// pair, so this block is a no-op in that case.
		if params.PrivacyScope != "" || params.Scope == "" {
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
		if candidate, err := s.captureActiveRuleIntent(ctx, ruleIntentCapture{
			Content:     params.Content,
			Project:     params.Project,
			Scope:       resolvedScope,
			Audience:    "developer",
			SessionID:   params.SessionID,
			Actor:       params.AgentSource,
			SourceTool:  "store_memory",
			EvidenceTag: "always_inject",
		}); err != nil {
			return "", fmt.Errorf("store rule candidate: %w", err)
		} else if candidate != nil {
			return marshalRuleCandidateIntentResponse(candidate, map[string]any{
				"title":         truncateTitle(candidate.ProposedContent, 80),
				"type":          string(models.ObsTypeGuidance),
				"scope":         resolvedScope,
				"always_inject": true,
			})
		}
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
		// Read before-state for audit before calling Supersede (which mutates the row).
		var beforeMem *models.Memory
		if bm, getErr := s.memoryStore.Get(ctx, sid); getErr == nil {
			beforeMem = bm
		}
		oldImportance, supErr := s.memoryStore.Supersede(ctx, sid)
		if supErr != nil {
			log.Warn().Err(supErr).Int64("superseded_id", sid).Msg("store_memory: supersede failed")
			continue
		}
		// Audit: fire-and-forget supersede event.
		if beforeMem == nil {
			tmp := &models.Memory{ID: sid}
			beforeMem = tmp
		}
		logAuditSupersede(ctx, s, beforeMem, actorFromContext(ctx))
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
	if err := applyPrincipalMemoryMetadata(ctx, memory, params.AgentVisibility, params.Domain); err != nil {
		return "", err
	}

	// Lifecycle fields (Milestone B): only set when lifecycle is enabled
	if os.Getenv("ENGRAM_LIFECYCLE_ENABLED") == "true" {
		if tier := coerceString(m["tier"], ""); tier != "" {
			if lifecycle.ValidTier(tier) {
				memory.Tier = tier
			}
		} else {
			// B4 resolution (2026-06-11): spec FR-B2 specifies 'episodic' as the default
			// tier for new memories — fresh, unverified knowledge is episodic until promoted.
			// DB column default is now 'episodic' (migration 131), but memoryRowForCreate
			// only copies Tier when it is non-empty. Set the explicit default here so
			// CreateWithLifecycle persists 'episodic' rather than relying on the DB default
			// (belt-and-suspenders: explicit intent is clearer than relying on DB default).
			memory.Tier = lifecycle.TierEpisodic
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

	// Use CreateWithLifecycle when lifecycle fields are present (ENGRAM_LIFECYCLE_ENABLED
	// path sets Tier/EpistemicType/Defeasibility on the memory struct above).
	var created *models.Memory
	var createErr error
	if os.Getenv("ENGRAM_LIFECYCLE_ENABLED") == "true" {
		created, createErr = s.memoryStore.CreateWithLifecycle(ctx, memory)
	} else {
		created, createErr = s.memoryStore.Create(ctx, memory)
	}
	if createErr != nil {
		return "", fmt.Errorf("store memory: %w", createErr)
	}

	// Audit: fire-and-forget create event (FR-D2 / NFR-D4). Actor derived from
	// session context when available, else "agent".
	actor := actorFromContext(ctx)
	logAuditCreate(ctx, s, created, actor)

	// T035: when write-lint is wired AND force=true was set, emit an additional
	// "legacy_force_write" audit entry to record that the lint gate was bypassed.
	// finding 12 fix: wlForce is now scoped inside the gate block; re-derive here.
	if s.writeLint != nil && vnextFEnabled() && coerceBool(m["force"], false) {
		logAuditGeneric(ctx, s, created, actor, "legacy_force_write")
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
	addPrincipalMemoryFields(result, created)
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
	// Rank-3 staleness advisory (non-blocking): if the content uses relative-time
	// language, nudge the author toward an absolute date/version anchor. "currently X"
	// read months later looks like a current fact — this is the silent-staleness
	// friction caught at the source. Advisory only; the memory is already stored.
	if terms := staleness.DetectRelativeTime(created.Content); len(terms) > 0 {
		result["staleness_advisory"] = staleAdvisory(terms)
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// handleEditMemory updates an existing memory's content and/or narrative.
// Introduced in Milestone D T003 to fill the audit trail gap for the edit path.
// Before-state is captured, Update is called, then logAuditEdit fires async.
func (s *Server) handleEditMemory(ctx context.Context, args json.RawMessage) (string, error) {
	ms := s.effectiveMemoryEditor()
	if ms == nil {
		return "", fmt.Errorf("memory store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	id := coerceInt64(m["id"], 0)
	if id == 0 {
		return "", fmt.Errorf("id required for store_memory action=edit")
	}
	narrative := coerceString(m["narrative"], "")
	tags := coerceStringSlice(m["tags"])

	// Read before-state.
	before, err := ms.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("edit_memory: memory %d not found: %w", id, err)
	}
	if before == nil {
		return "", fmt.Errorf("edit_memory: memory %d not found", id)
	}

	// Finding 2 (first review) + CRIT (second review): enforce project scope so a
	// caller cannot edit another project's memory by id. Return not-found (don't
	// leak existence) on mismatch or on empty caller project.
	//
	// When EnforceSourceProject=true an empty ctxProject must DENY — the same
	// treatment as handleStoreMemory — not silently skip the check.  A caller
	// arriving without a project context (no ContextWithProject injection) cannot
	// be authorised to edit an arbitrary memory by id.
	if config.Get().EnforceSourceProject {
		ctxProject := projectFromContext(ctx)
		if ctxProject == "" || before.Project != ctxProject {
			return "", fmt.Errorf("edit_memory: memory %d not found", id)
		}
	}

	// Build updated memory (preserve all existing fields, override only provided ones).
	updated := *before
	if narrative != "" {
		// Finding 1: apply the same hard-limit, soft-truncation, and secret-redaction
		// as the create path (tools_memory.go:104-126) before assigning content.
		cfg := config.Get()
		hardLimit := cfg.StoreMemoryHardLimit
		if hardLimit <= 0 {
			hardLimit = 10000
		}
		softLimit := cfg.StoreMemorySoftLimit
		if softLimit <= 0 {
			softLimit = 1000
		}
		if utf8.RuneCountInString(narrative) > hardLimit {
			return "", fmt.Errorf("edit_memory: content exceeds maximum length of %d characters", hardLimit)
		}
		if utf8.RuneCountInString(narrative) > softLimit {
			narrative = string([]rune(narrative)[:softLimit])
			log.Debug().
				Int("soft_limit", softLimit).
				Msg("edit_memory: content truncated to soft limit")
		}
		if privacy.ContainsSecrets(narrative) {
			log.Warn().Msg("edit_memory: content contains secrets — redacting before storage")
			narrative = privacy.RedactSecrets(narrative)
		}
		updated.Content = narrative
	}
	// Finding 6: distinguish absent "tags" key from explicit empty [].
	// absent → keep existing tags; explicit [] → clear all tags.
	// coerceStringSlice returns nil for both cases, so we use the raw map.
	if _, tagsPresent := m["tags"]; tagsPresent {
		updated.Tags = tags // tags is nil when [] was passed — clears the field
	}
	if updated.Content == "" {
		return "", fmt.Errorf("edit_memory: content must not be empty after edit")
	}

	after, err := ms.Update(ctx, &updated)
	if err != nil {
		return "", fmt.Errorf("edit_memory: %w", err)
	}

	// Audit: fire-and-forget update event.
	logAuditEdit(ctx, s, before, after, actorFromContext(ctx))

	result := map[string]any{
		"id":      after.ID,
		"title":   truncateTitle(after.Content, 80),
		"storage": "memories",
		"message": "Memory updated successfully",
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

// handleRecallMemory retrieves memories from the v5 memories table.
//
// Flag-OFF (ENGRAM_VNEXT_ENABLED != "true"): byte-identical to the previous
// List-based in-memory-filter path. No schema change; no new parameters accepted.
//
// Flag-ON (ENGRAM_VNEXT_ENABLED == "true"): uses the HybridSearch pipeline
// (FR-C4: FTS+vector RRF + FR-C4 scoring + optional Tier2 graph expansion).
// New parameters: expand_graph, min_confidence, tier_filter, explain.
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

	// B4 resolution (2026-06-11): tier_filter for recall_memory — spec FR-B2 requires
	// recall_memory to accept an optional tier filter. Gated behind ENGRAM_LIFECYCLE_ENABLED.
	tierFilterEnabled := os.Getenv("ENGRAM_LIFECYCLE_ENABLED") == "true"
	tierFilterSet := make(map[string]bool)
	if tierFilterEnabled {
		for _, tf := range coerceStringSlice(m["tier_filter"]) {
			if lifecycle.ValidTier(tf) {
				tierFilterSet[tf] = true
			} else {
				return "", fmt.Errorf("invalid_tier_filter: %q must be one of working, episodic, semantic, procedural", tf)
			}
		}
	}

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

	// ── Scope params — parsed unconditionally so both hybrid and legacy paths
	// can enforce privacy_scope visibility (T004+T005 / F-TG1 / d9eea82 contract).
	// The hybrid path passes caller+scopeEnabled+includeScopes into
	// handleRecallMemoryHybrid so HybridSearch results get the SAME scope
	// predicate the legacy batch-loop applies below. Without this wiring the
	// hybrid path would bypass scope.Resolve and re-introduce the cross-
	// workstation privacy leak fixed in d9eea82.
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

	// ── T018 (engram vNext Milestone F TG3) — new filter + rationale params ──
	// Parsed unconditionally from the args map; gated on vnextFEnabled() at
	// the dispatch point below.  Default values (false / 0.0) produce
	// byte-identical behaviour to v6.4.x — no response-shape change, no
	// ListWithFilters dispatch — satisfying NFR-F1 backward-compat invariant.
	tg3ConfidenceMin := coerceFloat64(m["confidence_min"], 0.0)
	tg3IncludeSuperseded := coerceBool(m["include_superseded"], false)
	tg3IncludeRationale := coerceBool(m["include_rationale"], false)

	// tg3Active is true only when at least one TG3 param is non-default AND the
	// vNext-F flag is on.  Flag-OFF callers that accidentally pass the new params
	// still get the legacy path — a defensive gate that mirrors the include_scopes
	// flag-gate pattern in T005.
	tg3Active := vnextFEnabled() && (tg3ConfidenceMin > 0 || tg3IncludeSuperseded || tg3IncludeRationale)

	// ── vnext hybrid path ───────────────────────────────────────────────────
	// Caller identity + scope context are fully built above; pass them into
	// handleRecallMemoryHybrid so hybrid results are subject to the same
	// privacy_scope visibility predicate as the legacy List path below.
	vnextEnabled := os.Getenv("ENGRAM_VNEXT_ENABLED") == "true"
	if vnextEnabled {
		return s.handleRecallMemoryHybrid(ctx, m, query, project, format, limit, obsType, tags, caller, scopeEnabled, includeScopes, tg3Active, tg3ConfidenceMin, tg3IncludeSuperseded, tg3IncludeRationale)
	}
	// ── legacy List-based path (flag-OFF; byte-identical behaviour when both
	// flags are OFF; scope-aware batch-loop when ENGRAM_VNEXT_F_ENABLED=true) ─

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
		// B4 resolution: tier_filter (ENGRAM_LIFECYCLE_ENABLED path).
		// Empty tierFilterSet means no tier restriction — all tiers pass.
		if len(tierFilterSet) > 0 {
			tier := mem.Tier
			if tier == "" {
				tier = lifecycle.TierEpisodic // treat unset as episodic (migration 131 default)
			}
			if !tierFilterSet[tier] {
				return false
			}
		}
		// T018 TG3 (MINOR fix): confidence floor applied in keepMemory so that
		// the tg3Active+scopeEnabled batch-loop path honours confidence_min
		// even when paging via ListWithOffset (which has no SQL confidence filter).
		if tg3Active && tg3ConfidenceMin > 0 && mem.Confidence < tg3ConfidenceMin {
			return false
		}
		return true
	}

	filtered := make([]*models.Memory, 0, limit)
	if tg3Active {
		// T018 (Milestone F TG3) + MINOR fix (review hardening): when any
		// non-default TG3 filter is set AND scope is enabled, use the same
		// batch-loop pattern as the scopeEnabled-only branch so that
		// scope-invisible rows do not truncate visible recall before older
		// eligible rows reach the requested limit.
		//
		// When scope is NOT enabled, a single-fetch candidate pool is safe
		// (no invisible rows), so we keep the efficient over-fetch path.
		if scopeEnabled {
			// T018 MAJOR fix (review hardening): include_superseded cannot be
			// honoured by the scoped batch-loop because ListWithOffset is
			// hardcoded to status='active'. Silently returning only active rows
			// when the caller requested superseded rows is the same degradation
			// fixed in the hybrid path (line ~1274). Return a structured error
			// so the caller knows to use the non-scope path or omit the flag.
			//
			// Design choice: structured error > silent no-op (option B from
			// coderabbit MAJOR review; mirrors hybrid-path treatment).
			if tg3IncludeSuperseded {
				return "", fmt.Errorf("include_superseded is not supported with scope-enabled recall; omit include_superseded or disable ENGRAM_VNEXT_F_ENABLED scope")
			}
			// Batch-loop: scope-invisible rows must not truncate visible recall
			// (same guarantee as the scopeEnabled-only branch below).
			// keepMemory applies all in-memory predicates including the TG3
			// confidence floor (added in keepMemory above). SQL-layer push of
			// confidence_min is an optimisation reserved for the non-scope path
			// where ListWithFilters can be used without offset.
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
			// Non-scope path: single over-fetch via ListWithFilters (SQL-layer
			// confidence_min + include_superseded push; no invisible rows).
			const (
				tg3CandidateMultiplier = 10
				tg3MinPool             = 1000
			)
			fetchLimit := limit * tg3CandidateMultiplier
			if fetchLimit < tg3MinPool {
				fetchLimit = tg3MinPool
			}
			tg3Opts := engramgorm.ListOptions{
				ConfidenceMin:     tg3ConfidenceMin,
				IncludeSuperseded: tg3IncludeSuperseded,
				Limit:             fetchLimit,
			}
			candidates, err := s.memoryStore.ListWithFilters(ctx, project, tg3Opts)
			if err != nil {
				return "", fmt.Errorf("recall_memory: %w", err)
			}
			for _, mem := range candidates {
				if keepMemory(mem) {
					filtered = append(filtered, mem)
					if len(filtered) >= limit {
						break
					}
				}
			}
		}
	} else if scopeEnabled {
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

	// T018 (TG3): when include_rationale=true, emit a structured JSON response
	// regardless of format — the rationale block only makes sense in structured
	// output. This path overrides the format switch below.
	//
	// The response shape matches spec §FR-F3 (REVISE 2026-05-25):
	//   { "memories": [..., { ..., "ranking_rationale": { 6 fields } }], "count": N,
	//     "query_metadata": { "candidates_before_filter", "candidates_after_filter",
	//                         "elapsed_ms" } }
	//
	// Backward-compat: this branch is only entered when tg3IncludeRationale=true
	// AND tg3Active=true (vnextFEnabled() check is inside tg3Active). Zero-flags
	// callers never reach this branch — their response is byte-identical v6.4.x.
	if tg3IncludeRationale && tg3Active {
		// Build active filter descriptors for rationale.filters_applied.
		var filterDescs []string
		filterDescs = append(filterDescs, "project="+project)
		if tg3ConfidenceMin > 0 {
			filterDescs = append(filterDescs, fmt.Sprintf("confidence_min=%.4g", tg3ConfidenceMin))
		}
		if tg3IncludeSuperseded {
			filterDescs = append(filterDescs, "include_superseded=true")
		}

		type memWithRationale struct {
			*models.Memory
			RankingRationale retrieval.RankingRationale `json:"ranking_rationale"`
		}
		results := make([]memWithRationale, 0, len(filtered))
		for _, mem := range filtered {
			// substring_match: true when query found in content (mirrors keepMemory logic).
			contentMatched := queryLower != "" && strings.Contains(strings.ToLower(mem.Content), queryLower)
			rat := retrieval.AssembleRationale(mem, query, contentMatched, filterDescs)
			results = append(results, memWithRationale{Memory: mem, RankingRationale: rat})
		}

		response := map[string]any{
			"memories": results,
			"count":    len(results),
		}
		if query != "" {
			response["query"] = query
		}
		out, err := json.Marshal(response)
		if err != nil {
			return "", fmt.Errorf("recall_memory rationale marshal: %w", err)
		}
		return string(out), nil
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

// handleRecallMemoryHybrid is the vnext path for recall_memory.
// Called only when ENGRAM_VNEXT_ENABLED == "true".
// It accepts the vnext-gated parameters (expand_graph, min_confidence,
// tier_filter, explain) in addition to the base recall_memory params.
//
// staleAdvisory builds the non-blocking rank-3 write-time staleness advisory shared
// by every store path (legacy create, write-lint no-signal success, write-lint
// Phase2 commit), so the advisory shape cannot drift between them.
func staleAdvisory(terms []string) map[string]any {
	return map[string]any{
		"relative_time_terms": terms,
		"note":                "content uses relative-time language; prefer an absolute date or version anchor (e.g. 'as of 2026-06-17' / 'in v6.16.0') so the fact stays interpretable when recalled later",
	}
}

// marshalWithStaleAdvisory marshals a typed Phase2 response with the staleness
// advisory attached. The response is a struct, so it is round-tripped through a
// generic map to add the advisory key without coupling to the orchestrator's type.
func marshalWithStaleAdvisory(resp any, terms []string) (string, error) {
	raw, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		// Response was not a JSON object (unexpected); fall back to the plain form
		// rather than dropping the commit result.
		return string(raw), nil
	}
	m["staleness_advisory"] = staleAdvisory(terms)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// rerankAdapter bridges the concrete reranking.Client to the retrieval.CrossEncoder
// interface, keeping internal/retrieval free of the reranking package import (the
// same package-boundary discipline used for the store interfaces). It adapts the
// returned []reranking.PassageScore into []retrieval.RerankResult.
type rerankAdapter struct {
	client *reranking.Client
}

func (a rerankAdapter) Rank(ctx context.Context, query string, passages []string) ([]retrieval.RerankResult, error) {
	// Defensive: the adapter is only constructed under an s.rerankClient != nil guard,
	// so this is unreachable in the live path — but a nil client returns no results
	// (not an error), keeping the caller on fusion order per the failure-silent contract.
	if a.client == nil {
		return nil, nil
	}
	scores, err := a.client.Rank(ctx, query, passages)
	if err != nil {
		return nil, err
	}
	out := make([]retrieval.RerankResult, len(scores))
	for i, s := range scores {
		out[i] = retrieval.RerankResult{Index: s.Index, RelevanceScore: s.RelevanceScore}
	}
	return out, nil
}

// caller, scopeEnabled, and includeScopes are forwarded from handleRecallMemory
// so that privacy_scope visibility (scope.Resolve) is enforced on hybrid results.
// This is required by the integration contract established in d9eea82: the hybrid
// path MUST NOT bypass scope filtering (same predicate as the legacy List path).
// Scope filter runs BEFORE final limit truncation so the limit reflects visible
// memories only, consistent with d9eea82 fix #4.
func (s *Server) handleRecallMemoryHybrid(
	ctx context.Context,
	m map[string]any,
	query, project, format string,
	limit int,
	obsType string,
	tags []string,
	caller scope.KeycardContext,
	scopeEnabled bool,
	includeScopes map[string]bool,
	tg3Active bool,
	tg3ConfidenceMin float64,
	tg3IncludeSuperseded bool,
	tg3IncludeRationale bool,
) (string, error) {
	expandGraph := coerceBool(m["expand_graph"], false)
	minConfidence := coerceFloat64(m["min_confidence"], 0.0)
	vecThreshold := coerceFloat64(m["vec_threshold"], 0.0) // translated from min_similarity by recall(similar)
	tierFilter := coerceStringSlice(m["tier_filter"])
	explain := coerceBool(m["explain"], false)

	// Attempt to embed the query for Tier1 vector search.
	// When the embedding client is nil (ENGRAM_EMBEDDING_URL not set),
	// HybridSearch degrades gracefully to FTS-only.
	var queryVec []float32
	if s.embeddingClient != nil {
		vecs, embErr := s.embeddingClient.Embed(ctx, []string{query})
		if embErr == nil && len(vecs) > 0 {
			queryVec = vecs[0]
		}
		// Non-fatal: log at debug level and continue without vector.
		if embErr != nil {
			log.Debug().Err(embErr).Msg("recall_memory: embedding unavailable, falling back to FTS-only")
		}
	}

	// T018 MAJOR fix (review hardening): include_superseded cannot be honored by
	// the hybrid path because HybridSearch legs (FTS, vector, graph) query only
	// active rows and the retrieval package is explicitly filter-agnostic (see
	// SkipTier0 comment in HybridOptions). Silently no-oping the param was the
	// bug — explicit rejection is better than silent degradation.
	//
	// Design choice: structured error > transparent downgrade.
	// Rationale: the caller passed include_superseded=true expecting superseded
	// memories to be included; silently dropping the flag returns a smaller,
	// inconsistent result set with no indication of the gap. An error tells the
	// caller to use the legacy path (ENGRAM_VNEXT_ENABLED=false) or omit the flag.
	if tg3IncludeSuperseded {
		return "", fmt.Errorf("include_superseded is not supported with ENGRAM_VNEXT_ENABLED hybrid retrieval; disable ENGRAM_VNEXT_ENABLED or omit include_superseded")
	}

	opts := retrieval.HybridOptions{
		QueryVec:      queryVec,
		TierFilter:    tierFilter,
		MinConfidence: minConfidence,
		VecThreshold:  vecThreshold,
		ExpandGraph:   expandGraph,
		Explain:       explain,
	}

	var gStore retrieval.GraphStoreInterface
	if s.graphStore != nil {
		gStore = s.graphStore
	}

	var embStore retrieval.EmbeddingStoreInterface
	if s.embeddingStore != nil {
		embStore = s.embeddingStore
	}

	// Rank-4: thread the cross-encoder reranker when configured. nil-guarded exactly
	// like embStore above — when ENGRAM_RERANK_URL is unset s.rerankClient is nil and
	// opts.Reranker stays nil (recall keeps the fusion order). The adapter bridges the
	// reranking.Client to the retrieval.CrossEncoder interface (package-boundary clean).
	if s.rerankClient != nil {
		opts.Reranker = rerankAdapter{client: s.rerankClient}
	}

	// When type/tag filters are active, request a wider candidate pool so that
	// post-filter truncation does not hide matching memories. The pool is capped
	// at limit*4 to bound over-fetching while ensuring filters have material to
	// work with. HybridSearch respects this as its hard limit, then we filter below.
	fetchLimit := limit
	if obsType != "" || len(tags) > 0 {
		const filterCandidateMultiplier = 4
		fetchLimit = limit * filterCandidateMultiplier
		if fetchLimit > 200 {
			fetchLimit = 200
		}
	}

	scored, explanations, err := retrieval.HybridSearch(
		ctx,
		project, query,
		fetchLimit,
		s.memoryStore,
		embStore,
		gStore,
		opts,
	)
	if err != nil {
		return "", fmt.Errorf("recall_memory hybrid: %w", err)
	}

	// Build result: filter by obsType / tags post-scoring (same semantics as legacy).
	// Filters run BEFORE reconsolidation so lifecycle updates only touch memories
	// returned to the caller (not the wider candidate set).
	queryLower := strings.ToLower(query)
	tagSet := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tagSet[strings.ToLower(tag)] = struct{}{}
	}

	type hybridResult struct {
		RankingExplanation *retrieval.RankingExplanation `json:"ranking_explanation,omitempty"`
		Tags               []string                      `json:"tags,omitempty"`
		Title              string                        `json:"title"`
		Type               string                        `json:"type,omitempty"`
		Content            string                        `json:"content"`
		SourceAgent        string                        `json:"source_agent,omitempty"`
		Project            string                        `json:"project"`
		ID                 int64                         `json:"id"`
		Score              float64                       `json:"score"`
	}

	explByID := make(map[int64]retrieval.RankingExplanation, len(explanations))
	for _, e := range explanations {
		explByID[e.MemoryID] = e
	}

	items := make([]hybridResult, 0, limit)
	for _, sm := range scored {
		mem := sm.Memory

		// Post-score obsType filter.
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
				continue
			}
		}

		// Post-score tag filter.
		if len(tagSet) > 0 {
			tagMatched := false
			for _, tag := range mem.Tags {
				if _, ok := tagSet[strings.ToLower(tag)]; ok {
					tagMatched = true
					break
				}
			}
			if !tagMatched {
				continue
			}
		}

		// ── Privacy-scope filter (d9eea82 integration contract) ──────────────
		// Runs BEFORE limit truncation so the returned limit reflects only
		// memories visible to the caller — same semantics as the legacy
		// batch-loop in handleRecallMemory. Without this check the hybrid path
		// would re-introduce the cross-workstation leak that d9eea82 fixed.
		if scopeEnabled {
			memScope := mem.PrivacyScope
			if memScope == "" {
				memScope = "project"
			}
			if len(includeScopes) > 0 && !includeScopes[memScope] {
				continue
			}
			meta := scope.SourceMeta{
				WorkstationID: mem.SourceWorkstationID,
				Sessions:      mem.SourceSessions,
			}
			if !scope.Resolve(caller, memScope, meta) {
				continue
			}
		}

		// ── TG3 confidence_min post-fetch filter ──────────────────────────────
		// Applied BEFORE limit truncation, consistent with the filter-before-limit
		// contract. HybridOptions.MinConfidence filters on the fused hybrid score,
		// not the raw confidence column; tg3ConfidenceMin filters on the stored
		// memory.Confidence field (the v5-surface TG3 semantic). Both can coexist.
		if tg3ConfidenceMin > 0 && mem.Confidence < tg3ConfidenceMin {
			continue
		}

		// Suppress unused-variable warning; queryLower reserved for future FTS safety net.
		_ = queryLower

		memoryType := ""
		for _, tag := range mem.Tags {
			if strings.HasPrefix(tag, "type:") {
				memoryType = strings.TrimPrefix(tag, "type:")
				break
			}
		}

		r := hybridResult{
			ID:          mem.ID,
			Title:       truncateTitle(mem.Content, 80),
			Type:        memoryType,
			Content:     mem.Content,
			Tags:        mem.Tags,
			SourceAgent: mem.SourceAgent,
			Project:     mem.Project,
			Score:       sm.Score,
		}
		if explain {
			if e, ok := explByID[mem.ID]; ok {
				r.RankingExplanation = &e
			}
		}
		items = append(items, r)
		if len(items) == limit {
			break
		}
	}

	// Tier0 fall-through: if HybridSearch returned results but every candidate
	// was filtered out above (by scope, obsType, or tag predicates) AND the
	// initial call did NOT already skip Tier0, the Tier0 exact-hash hit may be
	// the only reason Tier1 never ran. Re-run with SkipTier0=true so FTS+vector
	// Tier1 candidates get a chance to pass the same filter pipeline.
	// This is a rare path (Tier0 hit + invisible to caller) so the extra
	// HybridSearch call is acceptable. The SkipTier0 flag on the opts struct
	// prevents a second re-run if Tier1 results are also fully filtered.
	if len(scored) > 0 && len(items) == 0 && !opts.SkipTier0 {
		opts.SkipTier0 = true
		scored, explanations, err = retrieval.HybridSearch(
			ctx,
			project, query,
			fetchLimit,
			s.memoryStore,
			embStore,
			gStore,
			opts,
		)
		if err != nil {
			return "", fmt.Errorf("recall_memory hybrid (tier0 fallthrough): %w", err)
		}
		// Re-build the explanation lookup for the new result set.
		explByID = make(map[int64]retrieval.RankingExplanation, len(explanations))
		for _, e := range explanations {
			explByID[e.MemoryID] = e
		}
		// Re-apply all post-score filters over the new candidate set.
		items = make([]hybridResult, 0, limit)
		for _, sm := range scored {
			mem := sm.Memory
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
					continue
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
					continue
				}
			}
			if scopeEnabled {
				memScope := mem.PrivacyScope
				if memScope == "" {
					memScope = "project"
				}
				if len(includeScopes) > 0 && !includeScopes[memScope] {
					continue
				}
				meta := scope.SourceMeta{
					WorkstationID: mem.SourceWorkstationID,
					Sessions:      mem.SourceSessions,
				}
				if !scope.Resolve(caller, memScope, meta) {
					continue
				}
			}
			// TG3 confidence_min post-fetch filter (Tier0 fall-through path).
			if tg3ConfidenceMin > 0 && mem.Confidence < tg3ConfidenceMin {
				continue
			}
			memoryType := ""
			for _, tag := range mem.Tags {
				if strings.HasPrefix(tag, "type:") {
					memoryType = strings.TrimPrefix(tag, "type:")
					break
				}
			}
			r := hybridResult{
				ID:          mem.ID,
				Title:       truncateTitle(mem.Content, 80),
				Type:        memoryType,
				Content:     mem.Content,
				Tags:        mem.Tags,
				SourceAgent: mem.SourceAgent,
				Project:     mem.Project,
				Score:       sm.Score,
			}
			if explain {
				if e, ok := explByID[mem.ID]; ok {
					r.RankingExplanation = &e
				}
			}
			items = append(items, r)
			if len(items) == limit {
				break
			}
		}
	}

	// Build a lookup from memory ID → ScoredMemory used by both the
	// reconsolidation block (lifecycle) and the detailed format serializer.
	scoredByID := make(map[int64]retrieval.ScoredMemory, len(scored))
	for _, sm := range scored {
		scoredByID[sm.Memory.ID] = sm
	}

	// Reconsolidation: fire-and-forget lifecycle updates on the FINAL response set only
	// (not the wider candidate pool). This ensures access_count reflects actual retrieval.
	if os.Getenv("ENGRAM_LIFECYCLE_ENABLED") == "true" && len(items) > 0 {
		// Snapshot item IDs before the goroutine runs to avoid data races.
		type reconItem struct {
			id             int64
			stability      float64
			retrievability float64
		}
		toRecon := make([]reconItem, 0, len(items))
		for _, item := range items {
			if sm, ok := scoredByID[item.ID]; ok {
				toRecon = append(toRecon, reconItem{
					id:             sm.Memory.ID,
					stability:      sm.Memory.Stability,
					retrievability: sm.Memory.Retrievability,
				})
			}
		}
		go func() {
			for _, ri := range toRecon {
				fields := map[string]any{
					"access_count":      gormlib.Expr("access_count + 1"),
					"last_retrieved_at": gormlib.Expr("now()"),
				}
				if ri.stability > 0 {
					newStability := lifecycle.Reconsolidate(ri.stability, ri.retrievability)
					if newStability != ri.stability {
						fields["stability"] = newStability
					}
				}
				_ = s.memoryStore.UpdateLifecycleFields(context.Background(), ri.id, fields)
			}
		}()
	}

	// T018+T019 TG3: hybrid path include_rationale — when set, emit v5-surface
	// RankingRationale alongside W3's existing ranking_explanation. The two
	// fields coexist: ranking_explanation carries W3 FR-C4 scores (relevance,
	// recency, importance, fused_score, source_tier); ranking_rationale carries
	// the 6 TG3 v5-surface fields (recency_days, confidence, citation_count,
	// tier, substring_match, filters_applied). See T019 schema description.
	if tg3IncludeRationale && tg3Active {
		var filterDescs []string
		filterDescs = append(filterDescs, "project="+project)
		if tg3ConfidenceMin > 0 {
			filterDescs = append(filterDescs, fmt.Sprintf("confidence_min=%.4g", tg3ConfidenceMin))
		}
		// Note: include_superseded is rejected before reaching here (structured
		// error at hybrid-path entry), so it is never listed as applied.

		type hybridRationaleResult struct {
			RankingExplanation *retrieval.RankingExplanation `json:"ranking_explanation,omitempty"`
			RankingRationale   *retrieval.RankingRationale   `json:"ranking_rationale"`
			Tags               []string                      `json:"tags,omitempty"`
			Title              string                        `json:"title"`
			Type               string                        `json:"type,omitempty"`
			Content            string                        `json:"content"`
			SourceAgent        string                        `json:"source_agent,omitempty"`
			Project            string                        `json:"project"`
			ID                 int64                         `json:"id"`
			Score              float64                       `json:"score"`
		}
		rationaleItems := make([]hybridRationaleResult, 0, len(items))
		for _, item := range items {
			sm, ok := scoredByID[item.ID]
			if !ok {
				continue
			}
			contentMatched := strings.Contains(strings.ToLower(sm.Memory.Content), queryLower)
			rat := retrieval.AssembleRationale(sm.Memory, query, contentMatched, filterDescs)
			r := hybridRationaleResult{
				ID:               item.ID,
				Title:            item.Title,
				Type:             item.Type,
				Content:          item.Content,
				Tags:             item.Tags,
				SourceAgent:      item.SourceAgent,
				Project:          item.Project,
				Score:            item.Score,
				RankingRationale: &rat,
			}
			if explain {
				if e, ok := explByID[item.ID]; ok {
					r.RankingExplanation = &e
				}
			}
			rationaleItems = append(rationaleItems, r)
		}
		response := map[string]any{
			"memories": rationaleItems,
			"count":    len(rationaleItems),
			"query":    query,
		}
		out, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return "", fmt.Errorf("marshal hybrid rationale result: %w", marshalErr)
		}
		return string(out), nil
	}

	switch format {
	case "items":
		out, marshalErr := json.MarshalIndent(items, "", "  ")
		if marshalErr != nil {
			return "", fmt.Errorf("marshal hybrid result: %w", marshalErr)
		}
		return string(out), nil

	case "detailed":
		// detailed returns full models.Memory records (matching legacy flag-OFF
		// behaviour) plus per-memory score and optional ranking explanation.
		// items + scoredByID + explByID are already built by the reconsolidation
		// block above, so we can reconstruct the ordered detailed slice here
		// without duplicating the filter pipeline.
		type detailedHybridResult struct {
			*models.Memory
			Score              float64                       `json:"score"`
			RankingExplanation *retrieval.RankingExplanation `json:"ranking_explanation,omitempty"`
		}
		detailed := make([]detailedHybridResult, 0, len(items))
		for _, item := range items {
			sm, ok := scoredByID[item.ID]
			if !ok {
				continue
			}
			dr := detailedHybridResult{
				Memory: sm.Memory,
				Score:  sm.Score,
			}
			if explain {
				if e, ok := explByID[item.ID]; ok {
					dr.RankingExplanation = &e
				}
			}
			detailed = append(detailed, dr)
		}
		out, marshalErr := json.MarshalIndent(detailed, "", "  ")
		if marshalErr != nil {
			return "", fmt.Errorf("marshal hybrid detailed result: %w", marshalErr)
		}
		return string(out), nil
	default: // "text"
		if len(items) == 0 {
			return "No memories found matching the query.", nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d memories for query: %q\n\n", len(items), query))
		for i, r := range items {
			typeLabel := "MEMORY"
			if r.Type != "" {
				typeLabel = strings.ToUpper(r.Type)
			}
			sb.WriteString(fmt.Sprintf("%d. [%s] %s (score: %.3f)\n", i+1, typeLabel, r.Title, r.Score))
			content := r.Content
			if len(content) > 300 {
				content = content[:300] + "..."
			}
			sb.WriteString(fmt.Sprintf("   %s\n", content))
			if len(r.Tags) > 0 {
				sb.WriteString(fmt.Sprintf("   tags: %s\n", strings.Join(r.Tags, ", ")))
			}
			if explain && r.RankingExplanation != nil {
				e := r.RankingExplanation
				sb.WriteString(fmt.Sprintf("   explanation: relevance=%.3f recency=%.3f importance=%.3f fused=%.3f tier=%s\n",
					e.Relevance, e.Recency, e.Importance, e.FusedScore, e.SourceTier))
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

	// Read before-state for audit before deletion.
	var beforeMem *models.Memory
	if bm, getErr := s.memoryStore.Get(ctx, id); getErr == nil {
		beforeMem = bm
	}

	if err := s.memoryStore.Delete(ctx, id); err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			return "", fmt.Errorf("suppress_memory: memory %d not found", id)
		}
		return "", fmt.Errorf("suppress_memory: %w", err)
	}

	// Audit: fire-and-forget delete event.
	if beforeMem == nil {
		beforeMem = &models.Memory{ID: id}
	}
	logAuditDelete(ctx, s, beforeMem, actorFromContext(ctx))

	return fmt.Sprintf("Memory %d suppressed", id), nil
}
