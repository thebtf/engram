package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	gormlib "gorm.io/gorm"

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

	if privacy.ContainsSecrets(params.Content) {
		log.Warn().Msg("store_memory: content contains secrets — redacting before storage")
		params.Content = privacy.RedactSecrets(params.Content)
	}

	resolvedScope := params.Scope
	if resolvedScope == "" {
		resolvedScope = string(models.ScopeProject)
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

	memory := &models.Memory{
		Project:     params.Project,
		Content:     params.Content,
		Tags:        tags,
		SourceAgent: agentSource,
	}
	created, err := s.memoryStore.Create(ctx, memory)
	if err != nil {
		return "", fmt.Errorf("store memory: %w", err)
	}

	result := map[string]any{
		"id":      created.ID,
		"title":   truncateTitle(created.Content, 80),
		"type":    obsTypeStr,
		"scope":   params.Scope,
		"storage": "memories",
		"message": "Memory stored successfully",
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

	queryLower := strings.ToLower(query)
	tagSet := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tagSet[strings.ToLower(tag)] = struct{}{}
	}

	filtered := make([]*models.Memory, 0, min(limit, len(memories)))
	for _, mem := range memories {
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
				continue
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

		filtered = append(filtered, mem)
		if len(filtered) == limit {
			break
		}
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
			items = append(items, item{
				ID:          mem.ID,
				Title:       truncateTitle(mem.Content, 80),
				Type:        obsType,
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
