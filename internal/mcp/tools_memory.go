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
	"github.com/thebtf/engram/internal/search"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/pkg/models"
)

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
		Tags       []string
		Content    string
		Title      string
		Type       string
		Scope      string
		Project    string
		Importance *float64
	}
	params.Tags = coerceStringSlice(m["tags"])
	params.Content = coerceString(m["content"], "")
	params.Title = coerceString(m["title"], "")
	params.Type = coerceString(m["type"], "")
	params.Scope = coerceString(m["scope"], "")
	params.Project = coerceString(m["project"], "")
	if v, ok := m["importance"]; ok && v != nil {
		f := coerceFloat64(v, 0)
		params.Importance = &f
	}
	if params.Content == "" {
		return "", fmt.Errorf("content is required")
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
	dedupThreshold := cfg.StoreMemoryDedupThreshold
	if dedupThreshold <= 0 {
		dedupThreshold = 0.92
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

	// Dedup check: skip if very similar observation already exists.
	// includeGlobal=true so that global observations are considered during dedup.
	if s.vectorClient != nil && s.vectorClient.IsConnected() {
		where := vector.BuildWhereFilter(vector.DocTypeObservation, params.Project, true)
		similar, err := s.vectorClient.Query(ctx, params.Content, 1, where)
		if err == nil && len(similar) > 0 && similar[0].Similarity >= dedupThreshold {
			existingID := vector.ExtractRowID(similar[0].Metadata)
			result := map[string]any{
				"id":        existingID,
				"duplicate": true,
				"message":   fmt.Sprintf("Similar observation already exists (similarity: %.2f)", similar[0].Similarity),
			}
			out, _ := json.MarshalIndent(result, "", "  ")
			return string(out), nil
		}
	}

	title := params.Title
	if title == "" {
		title = truncateTitle(params.Content, 80)
	}

	obs := &models.ParsedObservation{
		Type:       obsType,
		SourceType: models.SourceManual,
		Title:      title,
		Narrative:  params.Content,
		Concepts:   concepts,
		Scope:      scope,
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

	result := map[string]any{
		"id":      id,
		"title":   title,
		"type":    string(obsType),
		"scope":   string(scope),
		"message": "Memory stored successfully",
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
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

// handleRecallMemory retrieves observations by semantic search.
func (s *Server) handleRecallMemory(ctx context.Context, args json.RawMessage) (string, error) {
	if s.searchMgr == nil {
		return "", fmt.Errorf("search manager not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Tags    []string
		Query   string
		Type    string
		Format  string
		Limit   int
		Project string
	}
	params.Tags = coerceStringSlice(m["tags"])
	params.Query = coerceString(m["query"], "")
	params.Type = coerceString(m["type"], "")
	params.Format = coerceString(m["format"], "")
	params.Limit = coerceInt(m["limit"], 0)
	params.Project = coerceString(m["project"], "")
	if params.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	if params.Limit <= 0 {
		params.Limit = 10
	}
	if params.Limit > 50 {
		params.Limit = 50
	}
	if params.Format == "" {
		params.Format = "text"
	}

	searchParams := search.SearchParams{
		Query:         params.Query,
		Project:       strings.TrimSpace(params.Project),
		Limit:         params.Limit,
		IncludeGlobal: true,
		Format:        "full",
		Type:          "observations",
	}
	if len(params.Tags) > 0 {
		searchParams.Concepts = strings.Join(params.Tags, ",")
	}
	if params.Type != "" {
		searchParams.ObsType = params.Type
	}

	result, err := s.searchMgr.UnifiedSearch(ctx, searchParams)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}

	switch params.Format {
	case "items":
		type item struct {
			Concepts  []string `json:"concepts,omitempty"`
			Title     string   `json:"title"`
			Type      string   `json:"type"`
			Scope     string   `json:"scope"`
			Narrative string   `json:"narrative,omitempty"`
			ID        int64    `json:"id"`
		}
		items := make([]item, 0, len(result.Results))
		for _, r := range result.Results {
			var concepts []string
			if c, ok := r.Metadata["concepts"]; ok {
				switch cv := c.(type) {
				case []string:
					concepts = cv
				case []any:
					for _, v := range cv {
						if sv, ok := v.(string); ok {
							concepts = append(concepts, sv)
						}
					}
				}
			}
			items = append(items, item{
				ID:        r.ID,
				Title:     r.Title,
				Type:      r.Type,
				Scope:     r.Scope,
				Narrative: r.Content,
				Concepts:  concepts,
			})
		}
		out, _ := json.MarshalIndent(items, "", "  ")
		return string(out), nil

	case "detailed":
		out, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal result: %w", err)
		}
		return string(out), nil

	default: // "text"
		if len(result.Results) == 0 {
			return "No memories found matching the query.", nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d memories for query: %q\n\n", len(result.Results), params.Query))
		for i, r := range result.Results {
			scopeTag := ""
			if r.Scope == "global" {
				scopeTag = " [GLOBAL]"
			}
			sb.WriteString(fmt.Sprintf("%d. [%s]%s %s\n", i+1, strings.ToUpper(r.Type), scopeTag, r.Title))
			if r.Content != "" {
				content := r.Content
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
