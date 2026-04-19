package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/pkg/models"
)

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

// handleStoreMemory explicitly stores a memory/observation.
func (s *Server) handleStoreMemory(ctx context.Context, args json.RawMessage) (string, error) {
	if s.observationStore == nil {
		return "", fmt.Errorf("observation store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Tags         []string
		Rejected     []string
		Content      string
		Title        string
		Type         string
		Scope        string
		Project      string
		AgentSource  string
		Importance   *float64
		TtlDays      *int
		AlwaysInject bool
	}
	params.Tags = coerceStringSlice(m["tags"])
	params.Rejected = coerceStringSlice(m["rejected"])
	params.Content = coerceString(m["content"], "")
	params.Title = coerceString(m["title"], "")
	params.Type = coerceString(m["type"], "")
	params.Scope = coerceString(m["scope"], "")
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

	// Redact secrets from content before storing — warn and continue rather than reject.
	if privacy.ContainsSecrets(params.Content) {
		log.Warn().Msg("store_memory: content contains secrets — redacting before storage")
		params.Content = privacy.RedactSecrets(params.Content)
	}

	// Classify observation type from content keywords when not provided.
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

	// Expand hierarchical tags: "lang:go:concurrency" -> ["lang", "lang:go", "lang:go:concurrency"]
	seen := make(map[string]bool)
	var concepts []string
	for _, tag := range params.Tags {
		for _, part := range expandTagHierarchy(tag) {
			if !seen[part] {
				seen[part] = true
				concepts = append(concepts, part)
			}
		}
	}

	// Add always-inject concept when requested — observations with this concept
	// are injected into every agent context regardless of query relevance.
	if params.AlwaysInject && !seen["always-inject"] {
		concepts = append(concepts, "always-inject")
		seen["always-inject"] = true
	}

	// Determine scope from explicit param or auto-detect from concepts.
	var scope models.ObservationScope
	if params.Scope != "" {
		scope = models.ObservationScope(params.Scope)
	} else {
		scope = models.DetermineScope(concepts)
	}

	if scope == models.ScopeGlobal {
		log.Warn().
			Str("project", params.Project).
			Msg("store_memory: storing global-scoped observation")
	}

	// Vector-based dedup removed in v5; every store_memory call creates a new observation.
	const contradictionAction = "ADD"

	title := params.Title
	if title == "" {
		title = truncateTitle(params.Content, 80)
	}

	// Validate agent_source: coerce to 'unknown' if unrecognized
	agentSource := models.AgentUnknown
	if params.AgentSource != "" {
		if models.IsValidAgentSource(params.AgentSource) {
			agentSource = models.AgentSource(params.AgentSource)
		} else {
			return "", fmt.Errorf("invalid agent_source %q: must be one of claude-code, codex, gemini, other, unknown", params.AgentSource)
		}
	}

	obs := &models.ParsedObservation{
		Type:        obsType,
		SourceType:  models.SourceManual,
		MemoryType:  models.ClassifyMemoryType(&models.ParsedObservation{Type: obsType, Narrative: params.Content, Concepts: concepts}),
		Title:       title,
		Narrative:   params.Content,
		Concepts:    concepts,
		Rejected:    params.Rejected,
		Scope:       scope,
		AgentSource: agentSource,
	}

	// Generate a unique session ID for manual memories to avoid
	// duplicate key violations on idx_sdk_sessions_claude_session_id.
	// Empty string causes conflicts because PostgreSQL NULLs are always unique
	// (ON CONFLICT on sdk_session_id won't fire) but claude_session_id="" collides.
	manualSessionID := "manual-" + uuid.NewString()
	id, _, err := s.observationStore.StoreObservation(ctx, manualSessionID, params.Project, obs, 0, 0)
	if err != nil {
		return "", fmt.Errorf("store observation: %w", err)
	}
	if params.Importance != nil {
		if err := s.observationStore.UpdateImportanceScore(ctx, id, *params.Importance); err != nil {
			return "", fmt.Errorf("set importance: %w", err)
		}
	}

	// Apply TTL for verified facts.
	ttlDays := computeTTLDays(params.TtlDays, concepts)
	ttlApplied := false
	if ttlDays > 0 {
		if err := s.observationStore.SetObservationTTL(ctx, id, ttlDays); err != nil {
			log.Warn().Err(err).Int64("id", id).Int("ttl_days", ttlDays).Msg("failed to set observation TTL")
		} else {
			ttlApplied = true
		}
	}

	result := map[string]any{
		"id":      id,
		"title":   title,
		"type":    string(obsType),
		"scope":   string(scope),
		"action":  contradictionAction,
		"message": "Memory stored successfully",
	}
	if ttlApplied {
		result["ttl_days"] = ttlDays
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

// handleRecallMemory retrieves observations via FTS (v5: vector search removed).
// Falls back to a simple substring filter when observationStore is unavailable.
func (s *Server) handleRecallMemory(ctx context.Context, args json.RawMessage) (string, error) {
	if s.observationStore == nil {
		return "", fmt.Errorf("recall_memory: observation store not configured")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	query := coerceString(m["query"], "")
	obsType := coerceString(m["type"], "")
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

	observations, err := s.observationStore.SearchObservationsFTS(ctx, query, project, limit*4)
	if err != nil {
		return "", fmt.Errorf("recall_memory: %w", err)
	}

	// Filter by type when requested.
	if obsType != "" {
		filtered := make([]*models.Observation, 0, len(observations))
		for _, obs := range observations {
			if string(obs.Type) == obsType {
				filtered = append(filtered, obs)
			}
		}
		observations = filtered
	}

	// Filter by concept tags when requested.
	if len(tags) > 0 {
		tagSet := make(map[string]struct{}, len(tags))
		for _, t := range tags {
			tagSet[t] = struct{}{}
		}
		filtered := make([]*models.Observation, 0, len(observations))
		for _, obs := range observations {
			for _, c := range obs.Concepts {
				if _, ok := tagSet[c]; ok {
					filtered = append(filtered, obs)
					break
				}
			}
		}
		observations = filtered
	}

	if len(observations) > limit {
		observations = observations[:limit]
	}

	switch format {
	case "items":
		type item struct {
			Concepts  []string `json:"concepts,omitempty"`
			Title     string   `json:"title"`
			Type      string   `json:"type"`
			Scope     string   `json:"scope"`
			Narrative string   `json:"narrative,omitempty"`
			ID        int64    `json:"id"`
		}
		items := make([]item, 0, len(observations))
		for _, obs := range observations {
			items = append(items, item{
				ID:        obs.ID,
				Title:     obs.Title.String,
				Type:      string(obs.Type),
				Scope:     string(obs.Scope),
				Narrative: obs.Narrative.String,
				Concepts:  []string(obs.Concepts),
			})
		}
		out, _ := json.MarshalIndent(items, "", "  ")
		return string(out), nil

	case "detailed":
		out, err := json.MarshalIndent(observations, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal result: %w", err)
		}
		return string(out), nil

	default: // "text"
		if len(observations) == 0 {
			return "No memories found matching the query.", nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d memories for query: %q\n\n", len(observations), query))
		for i, obs := range observations {
			scopeTag := ""
			if obs.Scope == models.ScopeGlobal {
				scopeTag = " [GLOBAL]"
			}
			sb.WriteString(fmt.Sprintf("%d. [%s]%s %s\n", i+1, strings.ToUpper(string(obs.Type)), scopeTag, obs.Title.String))
			if obs.Narrative.String != "" {
				content := obs.Narrative.String
				if len(content) > 300 {
					content = content[:300] + "..."
				}
				sb.WriteString(fmt.Sprintf("   %s\n", content))
			}
			sb.WriteString("\n")
		}
		return sb.String(), nil
	}
}

// handleRateMemory allows agents to rate observation usefulness.
// A "useful" rating increments user_feedback by 1; "not_useful" decrements by 1.
func (s *Server) handleRateMemory(ctx context.Context, args json.RawMessage) (string, error) {
	if s.observationStore == nil {
		return "", fmt.Errorf("observation store not available")
	}

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

	delta := 1
	if rating == "not_useful" {
		delta = -1
	}

	if err := s.observationStore.GetDB().WithContext(ctx).
		Exec("UPDATE observations SET user_feedback = COALESCE(user_feedback, 0) + ? WHERE id = ?", delta, id).Error; err != nil {
		return "", fmt.Errorf("update feedback: %w", err)
	}

	return fmt.Sprintf("Rated observation %d as %s", id, rating), nil
}

// handleSuppressMemory marks an observation as suppressed, excluding it from future search results.
// The observation remains in the database but is hidden from all FTS and LIKE search queries.
func (s *Server) handleSuppressMemory(ctx context.Context, args json.RawMessage) (string, error) {
	if s.observationStore == nil {
		return "", fmt.Errorf("observation store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	id := coerceInt64(m["id"], 0)
	if id == 0 {
		return "", fmt.Errorf("id required")
	}

	if err := s.observationStore.GetDB().WithContext(ctx).
		Exec("UPDATE observations SET is_suppressed = TRUE WHERE id = ?", id).Error; err != nil {
		return "", fmt.Errorf("suppress: %w", err)
	}

	return fmt.Sprintf("Observation %d suppressed", id), nil
}
