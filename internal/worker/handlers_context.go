// Package worker provides context and search-related HTTP handlers.
package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"github.com/thebtf/engram/internal/db/gorm"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"github.com/thebtf/engram/internal/worker/sdk"
	"github.com/thebtf/engram/pkg/models"
)

type sessionStartContextProvider interface {
	GetSessionStartContext(context.Context, *pb.GetSessionStartContextRequest) (*pb.GetSessionStartContextResponse, error)
}

func behavioralRulesToObservations(rules []*models.BehavioralRule) []*models.Observation {
	result := make([]*models.Observation, 0, len(rules))
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		project := ""
		scope := models.ScopeGlobal
		if rule.Project != nil {
			project = *rule.Project
			scope = models.ScopeProject
		}
		result = append(result, &models.Observation{
			ID:              rule.ID,
			Project:         project,
			Scope:           scope,
			Type:            models.ObsTypeGuidance,
			MemoryType:      models.MemTypeGuidance,
			SourceType:      models.SourceManual,
			CreatedAt:       rule.CreatedAt.Format(time.RFC3339),
			CreatedAtEpoch:  rule.CreatedAt.UnixMilli(),
			Title:           sql.NullString{String: rule.Content, Valid: rule.Content != ""},
			Narrative:       sql.NullString{String: rule.Content, Valid: rule.Content != ""},
			Concepts:        models.JSONStringArray{"behavioral-rule", "always-inject"},
			ImportanceScore: 1,
		})
	}
	return result
}

func memoriesToObservations(mems []*models.Memory) []*models.Observation {
	result := make([]*models.Observation, 0, len(mems))
	for _, mem := range mems {
		if mem == nil {
			continue
		}
		result = append(result, &models.Observation{
			ID:              mem.ID,
			Project:         mem.Project,
			Scope:           models.ScopeProject,
			Type:            models.ObsTypeDiscovery,
			MemoryType:      models.MemTypeContext,
			SourceType:      models.SourceManual,
			CreatedAt:       mem.CreatedAt.Format(time.RFC3339),
			CreatedAtEpoch:  mem.CreatedAt.UnixMilli(),
			Title:           sql.NullString{String: mem.Content, Valid: mem.Content != ""},
			Narrative:       sql.NullString{String: mem.Content, Valid: mem.Content != ""},
			Concepts:        models.JSONStringArray(mem.Tags),
			ImportanceScore: 1,
		})
	}
	return result
}

// handleSearchByPrompt godoc
// @Summary Search observations by prompt
// @Description Searches observations relevant to a user prompt using hybrid vector + FTS search with query expansion, cross-encoder reranking, and clustering. Supports both GET (query params) and POST (JSON body) to avoid URL length limits.
// @Tags Search
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Project filter"
// @Param query query string false "Search query"
// @Param cwd query string false "Working directory (ignored server-side)"
// @Param agent_id query string false "Agent ID (acts as project scope if project empty)"
// @Param limit query int false "Number of results (default 50, max 200)"
// @Param body body object false "POST body: {project, query, agent_id, cwd, limit}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "project and query required"
// @Failure 500 {string} string "internal error"
// @Router /api/context/search [get]
// @Router /api/context/search [post]
func (s *Service) handleSearchByPrompt(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	query := r.URL.Query().Get("query")
	cwd := r.URL.Query().Get("cwd")
	agentID := r.URL.Query().Get("agent_id")
	filesBeingEdited := r.URL.Query()["files_being_edited"]

	// For POST requests, allow JSON body to override query params.
	var obsTypeFilter string
	if r.Method == http.MethodPost && r.Body != nil {
		var body struct {
			Project         string `json:"project"`
			Query           string `json:"query"`
			Cwd             string `json:"cwd"`
			AgentID         string `json:"agent_id"`
			ObsType         string `json:"obs_type"`
			FilesBeingEdited []string `json:"files_being_edited"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			if body.Project != "" {
				project = body.Project
			}
			if body.Query != "" {
				query = body.Query
			}
			if body.Cwd != "" {
				cwd = body.Cwd
			}
			if body.AgentID != "" {
				agentID = body.AgentID
			}
			if body.ObsType != "" {
				obsTypeFilter = body.ObsType
			}
			if len(body.FilesBeingEdited) > 0 {
				filesBeingEdited = body.FilesBeingEdited
			}
			// agent_id acts as project scope for OpenClaw agents without filesystem context
			if project == "" && agentID != "" {
				project = agentID
			}
		}
	}

	// Also accept agent_id as query param fallback for project
	if project == "" && agentID != "" {
		project = agentID
	}

	if project == "" || query == "" {
		http.Error(w, "project and query required", http.StatusBadRequest)
		return
	}

	// Server-side: ignore client-provided cwd to prevent filesystem probing (S9-003).
	// File mtime staleness checks are only meaningful on the client; the server has no
	// access to client filesystems.
	cwd = ""

	// Validate project name to prevent path traversal
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	limit := gorm.ParseLimitParamWithMax(r, DefaultSearchLimit, 200)
	searchStart := time.Now()
	maxResults := s.config.ContextMaxPromptResults
	if limit > 0 && (maxResults <= 0 || limit < maxResults) {
		maxResults = limit
	}
	retrievalMeta := &retrievalMetadata{}
	retrievalCtx := withRetrievalRequest(r.Context(), agentID, cwd, retrievalMeta)
	clusteredObservations, similarityScores, err := s.RetrieveRelevant(retrievalCtx, project, query, RetrievalOptions{
		MaxResults: maxResults,
		FilePaths:  filesBeingEdited,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	threshold := retrievalMeta.threshold
	expandedQueries := retrievalMeta.expandedQueries
	detectedIntent := retrievalMeta.detectedIntent
	staleCount := retrievalMeta.staleCount
	freshCount := retrievalMeta.freshCount
	duplicatesRemoved := retrievalMeta.duplicatesRemoved
	totalResults := retrievalMeta.totalResults
	// Filter by observation type if requested (e.g., obs_type=guidance for behavioral rules)
	if obsTypeFilter != "" {
		filtered := make([]*models.Observation, 0, len(clusteredObservations))
		for _, obs := range clusteredObservations {
			if string(obs.Type) == obsTypeFilter {
				filtered = append(filtered, obs)
			}
		}
		clusteredObservations = filtered
	}
	// Record retrieval stats with staleness metrics
	s.recordRetrievalStatsExtended(project, int64(len(clusteredObservations)), 0, 0,
		int64(staleCount), int64(freshCount), int64(duplicatesRemoved), true)

	log.Info().
		Str("project", project).
		Str("query", query).
		Str("intent", detectedIntent).
		Int("expansions", len(expandedQueries)).
		Int("found", len(clusteredObservations)).
		Int("stale_excluded", staleCount).
		Float64("threshold", threshold).
		Msg("Prompt-based observation search")

	// Build response with similarity scores
	obsWithScores := make([]map[string]any, len(clusteredObservations))
	for i, obs := range clusteredObservations {
		obsMap := obs.ToMap()
		if score, ok := similarityScores[obs.ID]; ok {
			obsMap["similarity"] = score
		}
		obsWithScores[i] = obsMap
	}

	// Build expansion info for response.
	// v5 (US9): query expansion removed — expandedQueries is always a single-element
	// []string containing the original query. Weight and source fields are omitted.
	expansionInfo := make([]map[string]any, len(expandedQueries))
	for i, eq := range expandedQueries {
		expansionInfo[i] = map[string]any{
			"query":  eq,
			"weight": 1.0,
			"source": "original",
		}
	}

	// Track search misses for self-tuning analytics (inline — avoids unbounded goroutine spawn)
	if len(clusteredObservations) == 0 && query != "" {
		s.trackSearchMiss(project, query)
	}

	// Track this search for analytics
	s.trackSearchQuery(query, project, "observations", len(clusteredObservations), float32(time.Since(searchStart).Milliseconds()))

	// Always-inject tier: backed by behavioral_rules in v5.
	alwaysInjectLimit := s.config.AlwaysInjectLimit
	if alwaysInjectLimit <= 0 {
		alwaysInjectLimit = 20
	}
	var alwaysInjectObs []*models.Observation
	if s.behavioralRulesStore != nil {
		projectPtr := &project
		if project == "" {
			projectPtr = nil
		}
		rules, aiErr := s.behavioralRulesStore.List(r.Context(), projectPtr, alwaysInjectLimit)
		if aiErr != nil {
			log.Debug().Err(aiErr).Msg("Failed to fetch always-inject behavioral rules for search")
		} else {
			alwaysInjectObs = behavioralRulesToObservations(rules)
		}
	}

	writeJSON(w, map[string]any{
		"project":       project,
		"query":         query,
		"intent":        detectedIntent,
		"expansions":    expansionInfo,
		"observations":  obsWithScores,
		"always_inject": alwaysInjectObs,
		"threshold":     threshold,
		"max_results":   maxResults,
		"total_results": totalResults,
	})
}

// handleFileContext godoc
// @Summary Get file context
// @Description Returns observations relevant to specific files being worked on, using vector similarity search.
// @Tags Context
// @Produce json
// @Security ApiKeyAuth
// @Param project query string true "Project name"
// @Param files query string true "Comma-separated file paths (max 20)"
// @Param limit query int false "Results per file (default 10, max 50)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Router /api/context/files [get]
func (s *Service) handleFileContext(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project == "" {
		http.Error(w, "project required", http.StatusBadRequest)
		return
	}

	filesParam := r.URL.Query().Get("files")
	if filesParam == "" {
		http.Error(w, "files required", http.StatusBadRequest)
		return
	}

	// Parse comma-separated file paths
	files := strings.Split(filesParam, ",")
	if len(files) == 0 {
		http.Error(w, "at least one file required", http.StatusBadRequest)
		return
	}

	// Limit to reasonable number of files
	maxFiles := 20
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}

	// Vector search removed in v5 (content_chunks table dropped). Return empty results.
	// "deprecated" and "message" allow clients to distinguish feature-removal
	// from a genuine empty-result search, preventing silent degradation.
	writeJSON(w, map[string]any{
		"files":      files,
		"results":    map[string]any{},
		"count":      0,
		"deprecated": true,
		"message":    "file-context vector search removed in v5; results are intentionally empty",
	})
}

// estimateObsTokens estimates the token count for a single observation (full detail).
// Uses ~4 chars per token heuristic for English text.
func estimateObsTokens(obs *models.Observation) int {
	chars := len(obs.Title.String) + len(obs.Subtitle.String) + len(obs.Narrative.String)
	for _, fact := range obs.Facts {
		chars += len(fact)
	}
	// Add overhead for type tag, formatting, bullet points (~50 chars)
	chars += 50
	return (chars + 3) / 4 // ceil(chars/4)
}

// estimateObsTokensCondensed estimates tokens for condensed format (title + subtitle only).
func estimateObsTokensCondensed(obs *models.Observation) int {
	chars := len(obs.Title.String) + len(obs.Subtitle.String) + 30 // type tag + formatting
	return (chars + 3) / 4
}

// estimateTokens estimates total tokens for a slice of observations.
func estimateTokens(observations []*models.Observation) int {
	return estimateTokensWithLimit(observations, -1)
}

// estimateTokensWithLimit estimates tokens accounting for condensed format.
// First `fullCount` observations use full detail, the rest use condensed (title+subtitle).
// If fullCount < 0, all observations use full detail.
func estimateTokensWithLimit(observations []*models.Observation, fullCount int) int {
	total := 0
	for i, obs := range observations {
		if fullCount >= 0 && i >= fullCount {
			total += estimateObsTokensCondensed(obs)
		} else {
			total += estimateObsTokens(obs)
		}
	}
	return total
}

// trimToTokenBudget trims observations to fit within a token budget.
// Returns the trimmed slice, number of observations removed, and estimated token count.
func trimToTokenBudget(observations []*models.Observation, budget int) ([]*models.Observation, int, int) {
	if budget <= 0 || len(observations) == 0 {
		return observations, 0, estimateTokens(observations)
	}

	var totalTokens int
	for i, obs := range observations {
		tokens := estimateObsTokens(obs)
		if totalTokens+tokens > budget {
			return observations[:i], len(observations) - i, totalTokens
		}
		totalTokens += tokens
	}
	return observations, 0, totalTokens
}

// filterByIDs filters observations to only include those with IDs in the set.
func filterByIDs(observations []*models.Observation, ids map[int64]struct{}) []*models.Observation {
	result := make([]*models.Observation, 0, len(observations))
	for _, obs := range observations {
		if _, ok := ids[obs.ID]; ok {
			result = append(result, obs)
		}
	}
	return result
}

// compactObservation returns only the fields needed by the session-start hook.
func compactObservation(obs *models.Observation) map[string]any {
	m := map[string]any{
		"id":    obs.ID,
		"type":  obs.Type,
		"title": obs.Title.String,
	}
	if obs.Subtitle.Valid && obs.Subtitle.String != "" {
		m["subtitle"] = obs.Subtitle.String
	}
	if obs.Narrative.Valid && obs.Narrative.String != "" {
		m["narrative"] = obs.Narrative.String
	}
	if len(obs.Facts) > 0 {
		m["facts"] = obs.Facts
	}
	return m
}

// compactObservations converts a slice of observations to compact format.
// Uses compactObservationsWithLimit with fullCount=-1 (all full detail).
func compactObservations(observations []*models.Observation) []map[string]any {
	return compactObservationsWithLimit(observations, -1)
}

func projectBriefingNarrative(enabled bool, briefing *models.Observation) any {
	if !enabled {
		return nil
	}
	if briefing == nil || !briefing.Narrative.Valid || strings.TrimSpace(briefing.Narrative.String) == "" {
		return nil
	}
	return briefing.Narrative.String
}

// compactObservationsWithLimit converts observations to compact format.
// First `fullCount` observations get full detail (narrative + facts).
// Remaining observations get condensed format (title + subtitle only).
// If fullCount < 0, all observations get full detail.
func compactObservationsWithLimit(observations []*models.Observation, fullCount int) []map[string]any {
	result := make([]map[string]any, len(observations))
	for i, obs := range observations {
		if fullCount >= 0 && i >= fullCount {
			// Condensed: only id, type, title, subtitle
			m := map[string]any{
				"id":    obs.ID,
				"type":  obs.Type,
				"title": obs.Title.String,
			}
			if obs.Subtitle.Valid && obs.Subtitle.String != "" {
				m["subtitle"] = obs.Subtitle.String
			}
			result[i] = m
		} else {
			result[i] = compactObservation(obs)
		}
	}
	return result
}

// applyActiveVersions replaces each observation's narrative with its active ObservationVersion
// narrative when one exists. Returns a new slice; original observation pointers are not mutated.
// Errors from the version store are silently logged — the original narrative is used as fallback.
func applyActiveVersions(ctx context.Context, vs *gorm.VersionStore, observations []*models.Observation) []*models.Observation {
	if len(observations) == 0 || vs == nil {
		return observations
	}

	result := make([]*models.Observation, len(observations))
	for i, obs := range observations {
		active, err := vs.GetActiveVersion(ctx, obs.ID)
		if err != nil {
			log.Debug().Err(err).Int64("obs_id", obs.ID).Msg("Failed to fetch active observation version; using original narrative")
			result[i] = obs
			continue
		}
		if active == nil {
			result[i] = obs
			continue
		}
		// Shallow copy — only swap the narrative field so the original model is not mutated.
		copy := *obs
		copy.Narrative.String = active.Narrative
		copy.Narrative.Valid = true
		result[i] = &copy
	}

	return result
}

type sessionStartCompatibilityResponse struct {
	Issues      []map[string]any `json:"issues"`
	Rules       []map[string]any `json:"rules"`
	Memories    []map[string]any `json:"memories"`
	GeneratedAt string           `json:"generated_at"`
}

func sessionStartIssuesToMaps(issues []*pb.SessionStartIssue) []map[string]any {
	result := make([]map[string]any, 0, len(issues))
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		entry := map[string]any{
			"id":             issue.GetId(),
			"title":          issue.GetTitle(),
			"body":           issue.GetBody(),
			"status":         issue.GetStatus(),
			"priority":       issue.GetPriority(),
			"type":           issue.GetType(),
			"source_project": issue.GetSourceProject(),
			"target_project": issue.GetTargetProject(),
			"source_agent":   issue.GetSourceAgent(),
			"labels":         append([]string(nil), issue.GetLabels()...),
			"comment_count":  issue.GetCommentCount(),
		}
		if ts := issue.GetAcknowledgedAt(); ts != nil {
			entry["acknowledged_at"] = ts.AsTime().UTC().Format(time.RFC3339)
		}
		if ts := issue.GetResolvedAt(); ts != nil {
			entry["resolved_at"] = ts.AsTime().UTC().Format(time.RFC3339)
		}
		if ts := issue.GetReopenedAt(); ts != nil {
			entry["reopened_at"] = ts.AsTime().UTC().Format(time.RFC3339)
		}
		if ts := issue.GetClosedAt(); ts != nil {
			entry["closed_at"] = ts.AsTime().UTC().Format(time.RFC3339)
		}
		if ts := issue.GetCreatedAt(); ts != nil {
			entry["created_at"] = ts.AsTime().UTC().Format(time.RFC3339)
		}
		if ts := issue.GetUpdatedAt(); ts != nil {
			entry["updated_at"] = ts.AsTime().UTC().Format(time.RFC3339)
		}
		result = append(result, entry)
	}
	return result
}

func sessionStartRulesToMaps(rules []*pb.SessionStartRule) []map[string]any {
	result := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		entry := map[string]any{
			"id":         rule.GetId(),
			"project":    rule.GetProject(),
			"content":    rule.GetContent(),
			"edited_by":  rule.GetEditedBy(),
			"priority":   rule.GetPriority(),
			"version":    rule.GetVersion(),
			"narrative":  rule.GetContent(),
			"title":      rule.GetContent(),
			"facts":      []string{},
		}
		if ts := rule.GetCreatedAt(); ts != nil {
			entry["created_at"] = ts.AsTime().UTC().Format(time.RFC3339)
		}
		if ts := rule.GetUpdatedAt(); ts != nil {
			entry["updated_at"] = ts.AsTime().UTC().Format(time.RFC3339)
		}
		result = append(result, entry)
	}
	return result
}

func sessionStartMemoriesToMaps(memories []*pb.SessionStartMemory) []map[string]any {
	result := make([]map[string]any, 0, len(memories))
	for _, memory := range memories {
		if memory == nil {
			continue
		}
		entry := map[string]any{
			"id":           memory.GetId(),
			"project":      memory.GetProject(),
			"content":      memory.GetContent(),
			"tags":         append([]string(nil), memory.GetTags()...),
			"source_agent": memory.GetSourceAgent(),
			"edited_by":    memory.GetEditedBy(),
			"version":      memory.GetVersion(),
		}
		if ts := memory.GetCreatedAt(); ts != nil {
			entry["created_at"] = ts.AsTime().UTC().Format(time.RFC3339)
		}
		if ts := memory.GetUpdatedAt(); ts != nil {
			entry["updated_at"] = ts.AsTime().UTC().Format(time.RFC3339)
		}
		result = append(result, entry)
	}
	return result
}

// handleSessionStartContextStatic godoc
// @Summary Get static session-start context
// @Description Returns static session-start context sourced from the server gRPC implementation: active issues, behavioral rules, recent memories, and generated_at.
// @Tags Context
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Project slug (required)"
// @Param body body object false "POST body: {project, memories_limit, issues_limit}"
// @Success 200 {object} sessionStartCompatibilityResponse
// @Failure 400 {string} string "project required"
// @Failure 500 {string} string "internal error"
// @Router /api/context/session-start [post]
// @Router /api/context/session-start [get]
func (s *Service) handleSessionStartContextStatic(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	memoriesLimit := int32(0)
	issuesLimit := int32(0)

	if r.Method == http.MethodPost && r.Body != nil {
		var body struct {
			Project       string `json:"project"`
			MemoriesLimit int32  `json:"memories_limit"`
			IssuesLimit   int32  `json:"issues_limit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.Project) != "" {
			project = strings.TrimSpace(body.Project)
		}
		memoriesLimit = body.MemoriesLimit
		issuesLimit = body.IssuesLimit
	}

	if project == "" {
		http.Error(w, "project required", http.StatusBadRequest)
		return
	}
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.initMu.RLock()
	grpcSrv := s.grpcInternalServer
	s.initMu.RUnlock()
	if grpcSrv == nil {
		http.Error(w, "session-start service unavailable", http.StatusServiceUnavailable)
		return
	}

	resp, err := grpcSrv.GetSessionStartContext(r.Context(), &pb.GetSessionStartContextRequest{
		Project:       project,
		MemoriesLimit: memoriesLimit,
		IssuesLimit:   issuesLimit,
	})
	if err != nil {
		if st, ok := grpcstatus.FromError(err); ok {
			http.Error(w, st.Message(), grpcCodeToHTTP(st.Code()))
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	generatedAt := ""
	if ts := resp.GetGeneratedAt(); ts != nil {
		generatedAt = ts.AsTime().UTC().Format(time.RFC3339)
	}

	writeJSON(w, sessionStartCompatibilityResponse{
		Issues:      sessionStartIssuesToMaps(resp.GetIssues()),
		Rules:       sessionStartRulesToMaps(resp.GetRules()),
		Memories:    sessionStartMemoriesToMaps(resp.GetMemories()),
		GeneratedAt: generatedAt,
	})
}

func grpcCodeToHTTP(code codes.Code) int {
	switch code {
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.NotFound:
		return http.StatusNotFound
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}

// handleContextInject godoc
// @Summary Inject context for session start
// @Description Returns context for injection at session start. Response includes recent (last 5), relevant (top 10 semantic), and guidance sections. Supports GET (deprecated) and POST. Critical startup path — optimized for speed.
// @Tags Context
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Project name (required)"
// @Param agent_id query string false "Agent ID (acts as project scope if project empty)"
// @Param format query string false "Response format: 'compact' for minimal payload"
// @Param body body object false "POST body: {project, agent_id, cwd, legacy_project, git_remote, relative_path}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "project required"
// @Failure 500 {string} string "internal error"
// @Router /api/context/inject [post]
// @Router /api/context/inject [get]
func (s *Service) handleContextInject(w http.ResponseWriter, r *http.Request) {
	var project, agentID, cwd, legacyProject, gitRemote, relativePath, sessionID string
	var filesBeingEdited []string

	if r.Method == http.MethodPost {
		var req struct {
			Project          string   `json:"project"`
			AgentID          string   `json:"agent_id"`
			Cwd              string   `json:"cwd"`
			LegacyProject    string   `json:"legacy_project"`
			GitRemote        string   `json:"git_remote"`
			RelativePath     string   `json:"relative_path"`
			SessionID        string   `json:"session_id"`
			FilesBeingEdited []string `json:"files_being_edited"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		project = req.Project
		agentID = req.AgentID
		cwd = req.Cwd
		legacyProject = req.LegacyProject
		gitRemote = req.GitRemote
		relativePath = req.RelativePath
		sessionID = req.SessionID
		filesBeingEdited = req.FilesBeingEdited
	} else {
		// GET (deprecated — use POST)
		project = r.URL.Query().Get("project")
		agentID = r.URL.Query().Get("agent_id")
		cwd = r.URL.Query().Get("cwd")
		legacyProject = r.URL.Query().Get("legacy_project")
		gitRemote = r.URL.Query().Get("git_remote")
		relativePath = r.URL.Query().Get("relative_path")
		sessionID = r.URL.Query().Get("session_id")
		filesBeingEdited = r.URL.Query()["files_being_edited"]
	}

	// Fall back to agent_id as session proxy when no explicit session_id provided
	if sessionID == "" {
		sessionID = agentID
	}

	// agent_id acts as project scope for OpenClaw agents without filesystem context
	if project == "" && agentID != "" {
		project = agentID
	}
	if project == "" {
		http.Error(w, "project required", http.StatusBadRequest)
		return
	}

	// Validate project name to prevent path traversal
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Server-side: ignore client-provided cwd to prevent filesystem probing (S9-003).
	// File mtime staleness checks are only meaningful on the client; the server has no
	// access to client filesystems.
	cwd = ""

	if legacyProject != "" && legacyProject != project {
		displayName := project
		if idx := strings.Index(project, "_"); idx > 0 {
			displayName = project[:idx]
		}
		go func() {
			if err := gorm.UpsertProject(context.Background(), s.store.DB, project, legacyProject, gitRemote, relativePath, displayName); err != nil {
				log.Warn().Err(err).Str("project", project).Str("legacy", legacyProject).Msg("project upsert failed")
			}
		}()
	}

	// Limit observations for fast startup (configurable, default 100)
	limit := s.config.ContextObservations
	if limit <= 0 {
		limit = DefaultContextLimit
	}

	// Full count determines how many observations get full detail (configurable, default 25)
	fullCount := s.config.ContextFullCount
	if fullCount <= 0 {
		fullCount = 25
	}

	ctx := r.Context()

	// --- Recent section: last 5 observations by created_at ---
	scopeFilter := retrievalScope{Project: project, AgentID: agentID}
	recentRaw, err := s.searchFallbackObservations(ctx, "", scopeFilter, 5)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Apply staleness filter to recent observations
	var staleCount int
	recentFresh := make([]*models.Observation, 0, len(recentRaw))
	for _, obs := range recentRaw {
		if len(obs.FileMtimes) > 0 {
			var paths []string
			for path := range obs.FileMtimes {
				paths = append(paths, path)
			}
			currentMtimes := sdk.GetFileMtimes(paths, cwd)
			if obs.CheckStaleness(currentMtimes) {
				staleCount++
				s.queueStaleVerification(obs.ID, cwd)
				continue
			}
		}
		recentFresh = append(recentFresh, obs)
	}

	// Build a set of IDs already in the recent section for deduplication
	recentIDs := make(map[int64]struct{}, len(recentFresh))
	for _, obs := range recentFresh {
		recentIDs[obs.ID] = struct{}{}
	}

	// --- Relevant section: unified hybrid search via RetrieveRelevant (FR-3) ---
	// Query is derived from the last user prompt for this specific session (session-scoped),
	// so session A is never seeded by session B's last prompt. Falls back to the most-recent
	// project-wide prompt when session_id is empty (cold-start), and ultimately to the project
	// name when no prompt history exists. Uses the same pipeline as prompt-search.
	// When InjectUnified=false (ENGRAM_INJECT_UNIFIED=false), the legacy path is used instead.
	var relevantObservations []*models.Observation
	if s.config == nil || s.config.InjectUnified {
		// Unified path: derive query from the last user prompt for this session.
		injectQuery := project
		if prompt, pErr := s.loadLastUserPromptBySession(ctx, project, sessionID, 20); pErr == nil && prompt != nil {
			if prompt.PromptText != "" {
				injectQuery = prompt.PromptText
			}
		}
		opts := RetrievalOptions{MaxResults: 10, SessionID: sessionID, FilePaths: filesBeingEdited}
		retrieved, _, retrieveErr := s.RetrieveRelevant(ctx, project, injectQuery, opts)
		if retrieveErr != nil {
			log.Debug().Err(retrieveErr).Str("project", project).Msg("RetrieveRelevant failed for context inject relevant section")
		} else {
			for _, obs := range retrieved {
				if _, alreadyInRecent := recentIDs[obs.ID]; !alreadyInRecent {
					relevantObservations = append(relevantObservations, obs)
				}
			}
		}
	} else {
		// Legacy path (ENGRAM_INJECT_UNIFIED=false): observation-era fallback removed in PR-B.
		// Keep HTTP contract stable by returning an empty relevant section instead of erroring.
		relevantObservations = []*models.Observation{}
	}

	// --- Guidance section: top behavioral rules in v5 ---
	var guidanceObservations []*models.Observation
	if s.behavioralRulesStore != nil {
		projectPtr := &project
		if project == "" {
			projectPtr = nil
		}
		rules, guidanceErr := s.behavioralRulesStore.List(ctx, projectPtr, 5)
		if guidanceErr != nil {
			log.Debug().Err(guidanceErr).Str("project", project).Msg("Failed to fetch behavioral rules guidance")
		} else {
			guidanceObservations = behavioralRulesToObservations(rules)
		}
	}

	// Add guidance IDs to recent dedup set
	for _, obs := range guidanceObservations {
		recentIDs[obs.ID] = struct{}{}
	}

	// Project briefing was removed in v5 (ProjectBriefingEnabled config field deleted).
	var projectBriefing *models.Observation

	// --- Always-inject section: backed by behavioral_rules in v5 ---
	var alwaysInjectObservations []*models.Observation
	alwaysInjectLimit := s.config.AlwaysInjectLimit
	if alwaysInjectLimit <= 0 {
		alwaysInjectLimit = 20
	}
	if s.behavioralRulesStore != nil {
		projectPtr := &project
		if project == "" {
			projectPtr = nil
		}
		rules, aiErr := s.behavioralRulesStore.List(ctx, projectPtr, alwaysInjectLimit)
		if aiErr != nil {
			log.Debug().Err(aiErr).Msg("Failed to fetch always-inject behavioral rules")
		} else {
			for _, obs := range behavioralRulesToObservations(rules) {
				if _, already := recentIDs[obs.ID]; !already {
					alwaysInjectObservations = append(alwaysInjectObservations, obs)
					recentIDs[obs.ID] = struct{}{}
				}
			}
		}
	}

	// Injection floor was removed in v5 (InjectionFloor config field deleted).

	// --- Backward-compat observations field: use v5 memory fallback where available ---
	allRecentRaw, err := s.searchFallbackObservations(ctx, "", scopeFilter, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if allRecentRaw == nil && s.memoryStore != nil && project != "" {
		mems, memErr := s.memoryStore.List(ctx, project, limit)
		if memErr != nil {
			http.Error(w, memErr.Error(), http.StatusInternalServerError)
			return
		}
		allRecentRaw = memoriesToObservations(mems)
	}
	if allRecentRaw == nil {
		allRecentRaw = []*models.Observation{}
	}

	var allFreshObservations []*models.Observation
	for _, obs := range allRecentRaw {
		if len(obs.FileMtimes) > 0 {
			var paths []string
			for path := range obs.FileMtimes {
				paths = append(paths, path)
			}
			currentMtimes := sdk.GetFileMtimes(paths, cwd)
			if obs.CheckStaleness(currentMtimes) {
				staleCount++
				s.queueStaleVerification(obs.ID, cwd)
				continue
			}
		}
		allFreshObservations = append(allFreshObservations, obs)
	}

	// Merge relevant observations into the union (those not already in allFreshObservations)
	allFreshIDs := make(map[int64]struct{}, len(allFreshObservations))
	for _, obs := range allFreshObservations {
		allFreshIDs[obs.ID] = struct{}{}
	}
	unionObservations := make([]*models.Observation, len(allFreshObservations))
	copy(unionObservations, allFreshObservations)
	for _, obs := range relevantObservations {
		if _, exists := allFreshIDs[obs.ID]; !exists {
			unionObservations = append(unionObservations, obs)
		}
	}

	// Cluster the union to remove duplicates (clustering threshold removed in v5)
	clusteredObservations := unionObservations
	duplicatesRemoved := 0

	// Record retrieval stats with staleness metrics
	s.recordRetrievalStatsExtended(project, int64(len(clusteredObservations)), 0, 0,
		int64(staleCount), int64(len(allFreshObservations)), int64(duplicatesRemoved), false)

	// Apply token budget: estimate tokens and trim observations to fit
	tokenBudget := s.config.ContextMaxTokens
	var tokenEstimate int
	var budgetTrimmed int

	if tokenBudget > 0 {
		// Estimate tokens per observation (~4 chars per token for English)
		// Reserve 20% of budget for guidance
		guidanceBudget := tokenBudget / 5
		mainBudget := tokenBudget - guidanceBudget

		// Trim guidance first
		guidanceObservations, _, _ = trimToTokenBudget(guidanceObservations, guidanceBudget)

		// Trim main observations
		var mainTrimmed int
		clusteredObservations, mainTrimmed, tokenEstimate = trimToTokenBudget(clusteredObservations, mainBudget)
		budgetTrimmed = mainTrimmed

		// Also trim recent and relevant sections to not exceed what's in clustered
		clusteredIDs := make(map[int64]struct{}, len(clusteredObservations))
		for _, obs := range clusteredObservations {
			clusteredIDs[obs.ID] = struct{}{}
		}
		recentFresh = filterByIDs(recentFresh, clusteredIDs)
		relevantObservations = filterByIDs(relevantObservations, clusteredIDs)
	} else {
		tokenEstimate = estimateTokens(clusteredObservations) + estimateTokens(guidanceObservations)
	}

	log.Info().
		Str("project", project).
		Int("total", len(allRecentRaw)).
		Int("fresh", len(allFreshObservations)).
		Int("clustered", len(clusteredObservations)).
		Int("duplicates", duplicatesRemoved).
		Int("stale_excluded", staleCount).
		Int("budget_trimmed", budgetTrimmed).
		Int("token_estimate", tokenEstimate).
		Int("recent_section", len(recentFresh)).
		Int("relevant_section", len(relevantObservations)).
		Int("guidance_section", len(guidanceObservations)).
		Msg("Context injection with clustering")

	// Agent stats fetch + A/B injection strategy selector were removed in v5.
	var selectedStrategy string

	// Apply active version substitution (APO-lite, Phase 5).
	// For each observation in guidance and always-inject sections, check whether an active
	// ObservationVersion exists. When one does, replace the narrative in a shallow copy so
	// the original model record is not mutated.
	s.initMu.RLock()
	versionStore := s.versionStore
	s.initMu.RUnlock()
	if versionStore != nil {
		guidanceObservations = applyActiveVersions(ctx, versionStore, guidanceObservations)
		alwaysInjectObservations = applyActiveVersions(ctx, versionStore, alwaysInjectObservations)
	}

	// Record injection events asynchronously (closed-loop learning Phase 1).
	// Fire-and-forget: injection tracking is non-critical; errors are silently dropped.
	if sessionID != "" && s.injectionStore != nil {
		capturedAlwaysInject := alwaysInjectObservations
		capturedRecent := recentFresh
		capturedRelevant := relevantObservations
		capturedSessionID := sessionID
		injStore := s.injectionStore
		go func() {
			var records []gorm.InjectionRecord
			for _, obs := range capturedAlwaysInject {
				records = append(records, gorm.InjectionRecord{ObservationID: obs.ID, SessionID: capturedSessionID, InjectionSection: "always_inject"})
			}
			for _, obs := range capturedRecent {
				records = append(records, gorm.InjectionRecord{ObservationID: obs.ID, SessionID: capturedSessionID, InjectionSection: "recent"})
			}
			for _, obs := range capturedRelevant {
				records = append(records, gorm.InjectionRecord{ObservationID: obs.ID, SessionID: capturedSessionID, InjectionSection: "relevant"})
			}
			if len(records) > 0 {
				_ = injStore.RecordInjections(context.Background(), records)
			}
		}()
	}

	// Check if compact format is requested
	compact := r.URL.Query().Get("format") == "compact"

	if compact {
		// Compact format: only fields the hook actually uses.
		// Main observations use fullCount limit — condensed entries skip narrative/facts.
		// Recalculate token estimate accounting for condensed format savings.
		compactTokenEstimate := estimateTokensWithLimit(clusteredObservations, fullCount) +
			estimateTokens(guidanceObservations)
		writeJSON(w, map[string]any{
			"strategy":           selectedStrategy,
			"project":            project,
			"observations":       compactObservationsWithLimit(clusteredObservations, fullCount),
			"recent":             compactObservations(recentFresh),
			"relevant":           compactObservations(relevantObservations),
			"guidance":           compactObservations(guidanceObservations),
			"always_inject":      compactObservations(alwaysInjectObservations),
			"project_briefing":   projectBriefingNarrative(false, projectBriefing),
			"full_count":         fullCount,
			"stale_excluded":     staleCount,
			"duplicates_removed": duplicatesRemoved,
			"token_estimate":     compactTokenEstimate,
			"budget_trimmed":     budgetTrimmed,
		})
	} else {
		writeJSON(w, map[string]any{
			"project":            project,
			"strategy":           selectedStrategy,
			"observations":       clusteredObservations,
			"recent":             recentFresh,
			"relevant":           relevantObservations,
			"guidance":           guidanceObservations,
			"always_inject":      alwaysInjectObservations,
			"project_briefing":   projectBriefingNarrative(false, projectBriefing),
			"full_count":         fullCount,
			"stale_excluded":     staleCount,
			"duplicates_removed": duplicatesRemoved,
			"token_estimate":     tokenEstimate,
			"budget_trimmed":     budgetTrimmed,
		})
	}
}

// handleSearchDecisions godoc
// @Summary Search decisions
// @Description Searches observations using decision-optimized semantic search. Thin REST wrapper over the search manager's Decisions method.
// @Tags Search
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body object true "Search params: query, project (required), limit (optional)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "query and project required"
// @Failure 500 {string} string "internal error"
// @Router /api/decisions/search [post]
func (s *Service) handleSearchDecisions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query   string `json:"query"`
		Project string `json:"project"`
		Limit   int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if body.Query == "" || body.Project == "" {
		http.Error(w, "query and project required", http.StatusBadRequest)
		return
	}
	if err := ValidateProjectName(body.Project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Decisions preset search was backed by search.Manager, dropped in v5 (US9).
	// Return 501 Not Implemented so clients can distinguish "feature removed" from
	// "no results". Use recall(action="search") via MCP instead.
	_ = body.Limit
	w.WriteHeader(http.StatusNotImplemented)
	writeJSON(w, map[string]any{
		"project":      body.Project,
		"query":        body.Query,
		"observations": []any{},
		"total_count":  0,
		"deprecated":   "decisions preset search removed in v5 (US9); use recall(action=\"search\") via MCP",
	})
}

// handleContextCount godoc
// @Summary Get observation count
// @Description Returns the count of observations for a project (cached).
// @Tags Context
// @Produce json
// @Security ApiKeyAuth
// @Param project query string true "Project name"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "project required"
// @Failure 500 {string} string "internal error"
// @Router /api/context/count [get]
func (s *Service) handleContextCount(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project == "" {
		http.Error(w, "project required", http.StatusBadRequest)
		return
	}

	count, err := s.getCachedObservationCount(r.Context(), project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"project": project,
		"count":   count,
	})
}

// trackSearchMiss records a search query that returned zero results for analytics.
// Observation-era search miss persistence was removed in v5; keep the hook as a no-op so callers stay stable.
func (s *Service) trackSearchMiss(project, query string) {
	_ = project
	_ = query
}

// handleSearchMissAnalytics godoc
// @Summary Get search miss analytics
// @Description Search miss analytics persistence was removed in v5; this endpoint remains for compatibility and returns an explicit deprecation payload.
// @Tags Search
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body object true "Params: project (optional — omit to aggregate across all projects), limit (optional)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "invalid project name"
// @Router /api/analytics/search-misses [post]
func (s *Service) handleSearchMissAnalytics(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string `json:"project"`
		Limit   int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Project != "" {
		if err := ValidateProjectName(body.Project); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if body.Limit <= 0 {
		body.Limit = 50
	}

	writeJSON(w, map[string]any{
		"project":      body.Project,
		"limit":        body.Limit,
		"miss_stats":   []any{},
		"total_misses": 0,
		"deprecated":   "search miss analytics persistence removed in v5",
	})
}
