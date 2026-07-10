// Package worker provides context and search-related HTTP handlers.
package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/injection"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/internal/worker/sdk"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type sessionStartContextProvider interface {
	GetSessionStartContext(context.Context, *pb.GetSessionStartContextRequest) (*pb.GetSessionStartContextResponse, error)
}

// behavioralRulesToObservations converts behavioral rules into the observation shape
// expected by context-inject callers. Rules use guidance type so downstream consumers
// can distinguish them from user-authored observations.
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

// memoriesToObservations converts memory records into the observation shape.
// Used in the backward-compat observations field when the v5 memory store is the
// source of truth and no observation rows exist.
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

	// POST body overrides query params — allows large payloads that would exceed URL limits.
	var obsTypeFilter string
	if r.Method == http.MethodPost && r.Body != nil {
		var body struct {
			Project          string   `json:"project"`
			Query            string   `json:"query"`
			Cwd              string   `json:"cwd"`
			AgentID          string   `json:"agent_id"`
			ObsType          string   `json:"obs_type"`
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

	// Query-param fallback for agent_id → project mapping.
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

	// Optional post-filter by observation type (e.g., obs_type=guidance for behavioral rules).
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

	// Attach similarity scores to each observation for caller ranking.
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
		rules, aiErr := s.behavioralRulesStore.ListEnabled(r.Context(), projectPtr, alwaysInjectLimit)
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

	files := strings.Split(filesParam, ",")
	if len(files) == 0 {
		http.Error(w, "at least one file required", http.StatusBadRequest)
		return
	}

	// Cap to a reasonable number to prevent excessive query expansion.
	maxFiles := 20
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}

	// The file-context vector-search feature was removed in v5; this endpoint is a
	// stable no-op. (The content_chunks table itself was later restored at migration
	// 108 for vNext memory embeddings, but this file-search path was not rewired.)
	// "deprecated" and "message" let clients distinguish feature-removal from a
	// genuine empty-result search, preventing silent degradation.
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
			// Condensed: only id, type, title, subtitle — omit narrative and facts to save tokens.
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
			"id":        rule.GetId(),
			"project":   rule.GetProject(),
			"content":   rule.GetContent(),
			"edited_by": rule.GetEditedBy(),
			"priority":  rule.GetPriority(),
			"version":   rule.GetVersion(),
			"narrative": rule.GetContent(),
			"title":     rule.GetContent(),
			"facts":     []string{},
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
	// session_id enables injection-event recording on this primary injection path
	// (CR-001: revive feedback loop). GET query value is the fallback; a POST body
	// value wins. Empty session_id => recording is skipped (delivery is unaffected).
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	memoriesLimit := int32(0)
	issuesLimit := int32(0)

	if r.Method == http.MethodPost && r.Body != nil {
		var body struct {
			Project       string `json:"project"`
			SessionID     string `json:"session_id"`
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
		if strings.TrimSpace(body.SessionID) != "" {
			sessionID = strings.TrimSpace(body.SessionID)
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

	// grpcInternalServer is set during init; guard before use.
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

	// CR-001 (revive feedback loop): this is the PRIMARY live injection event for
	// Claude Code. Record the injected memory IDs to injection_log and increment
	// memories.injection_count so session-end citation detection has rows to match
	// against. Without this, injection_log stays empty, processCitationsAsync
	// early-returns ("no injection records found"), and injection_count/citation_count
	// are 0 forever. Fire-and-forget, mirroring handleContextInject's legacy-path
	// recorder. Memory IDs ONLY (not rule IDs) to keep injection_count semantics clean.
	if sessionID != "" {
		s.initMu.RLock()
		injLogStore := s.injectionLogStore
		memStore := s.memoryStore
		s.initMu.RUnlock()
		if injLogStore != nil {
			ids := collectSessionStartMemoryIDs(resp.GetMemories())
			if len(ids) > 0 {
				capturedSessionID := sessionID
				capturedProject := project
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					recCtx, cancel := s.detachedContext(30 * time.Second)
					defer cancel()
					if err := injLogStore.Record(recCtx, capturedSessionID, capturedProject, ids); err != nil {
						log.Warn().Err(err).Str("session_id", capturedSessionID).Msg("injection_log: session-start record failed")
					}
					if memStore != nil {
						if err := memStore.BatchIncrementInjected(recCtx, ids); err != nil {
							log.Warn().Err(err).Str("session_id", capturedSessionID).Msg("injection_count: session-start increment failed")
						}
					}
				}()
			}
		}
	}

	writeJSON(w, sessionStartCompatibilityResponse{
		Issues:      sessionStartIssuesToMaps(resp.GetIssues()),
		Rules:       sessionStartRulesToMaps(resp.GetRules()),
		Memories:    sessionStartMemoriesToMaps(resp.GetMemories()),
		GeneratedAt: generatedAt,
	})
}

// detachedContext returns a timeout context for fire-and-forget background work
// spawned from a request handler (injection recording, citation tracking). It falls
// back to context.Background() when s.ctx is nil — a half-initialized Service shape
// that occurs in unit tests and early init — so a detached goroutine can never panic
// in context.WithTimeout and crash the process. All in-file recorders route through
// this single chokepoint (CR-001 review: gemini flagged the unguarded s.ctx pattern).
func (s *Service) detachedContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, timeout)
}

// collectSessionStartMemoryIDs extracts the deduplicated, non-zero memory IDs from
// a session-start gRPC response, for recording the injection event to injection_log
// (CR-001: revive feedback loop). It is pure (no I/O) so the ID-selection logic —
// the part that must be correct for injection_count semantics — is unit-testable
// without a database. Rule IDs are intentionally NOT collected here: only memory
// IDs feed injection_count, matching handleContextInject.
func collectSessionStartMemoryIDs(memories []*pb.SessionStartMemory) []int64 {
	if len(memories) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(memories))
	seen := make(map[int64]struct{}, len(memories))
	for _, m := range memories {
		if m == nil {
			continue
		}
		id := m.GetId()
		if id == 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// grpcCodeToHTTP maps gRPC status codes to HTTP status codes for error forwarding.
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
// @Param body body object false "POST body: {project, agent_id, cwd, legacy_project, project_identity, identity_only}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "project required"
// @Failure 409 {object} map[string]interface{} "ambiguous legacy project identity"
// @Failure 503 {object} map[string]interface{} "project identity registration unavailable"
// @Failure 500 {string} string "internal error"
// @Router /api/context/inject [post]
// @Router /api/context/inject [get]
func (s *Service) handleContextInject(w http.ResponseWriter, r *http.Request) {
	var project, agentID, cwd, legacyProject, sessionID string
	var filesBeingEdited []string
	var projectIdentity *gorm.ProjectIdentityV2
	var identityOnly bool

	if r.Method == http.MethodPost {
		var req struct {
			Project          string                  `json:"project"`
			AgentID          string                  `json:"agent_id"`
			Cwd              string                  `json:"cwd"`
			LegacyProject    string                  `json:"legacy_project"`
			GitRemote        string                  `json:"git_remote"`
			RelativePath     string                  `json:"relative_path"`
			SessionID        string                  `json:"session_id"`
			FilesBeingEdited []string                `json:"files_being_edited"`
			ProjectIdentity  *gorm.ProjectIdentityV2 `json:"project_identity"`
			IdentityOnly     bool                    `json:"identity_only"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		project = req.Project
		agentID = req.AgentID
		cwd = req.Cwd
		legacyProject = req.LegacyProject
		sessionID = req.SessionID
		filesBeingEdited = req.FilesBeingEdited
		projectIdentity = req.ProjectIdentity
		identityOnly = req.IdentityOnly
	} else {
		// GET (deprecated — use POST)
		project = r.URL.Query().Get("project")
		agentID = r.URL.Query().Get("agent_id")
		cwd = r.URL.Query().Get("cwd")
		legacyProject = r.URL.Query().Get("legacy_project")
		sessionID = r.URL.Query().Get("session_id")
		filesBeingEdited = r.URL.Query()["files_being_edited"]
	}

	// Fall back to agent_id as session proxy when no explicit session_id provided.
	if sessionID == "" {
		sessionID = agentID
	}

	// agent_id acts as project scope for OpenClaw agents without filesystem context.
	if project == "" && agentID != "" {
		project = agentID
	}
	if project == "" {
		http.Error(w, "project required", http.StatusBadRequest)
		return
	}

	// Resolve/register synchronously before any retrieval or tenant mutation.
	// Identity metadata selects a namespace; bearer/principal authorization is
	// still enforced independently by the HTTP middleware.
	if s.store != nil {
		resolution, resolveErr := gorm.RegisterAndResolve(r.Context(), s.store.DB, project, projectIdentity)
		if resolveErr != nil {
			writeProjectIdentityHTTPError(w, resolveErr)
			return
		}
		project = resolution.CanonicalProjectID
		// Preserve the old HTTP contract: project is the canonical outer selector
		// and legacy_project is only an alias. Never reverse them on a fresh DB.
		if projectIdentity == nil && legacyProject != "" && legacyProject != project {
			if err := gorm.AttachLegacyAlias(r.Context(), s.store.DB, project, legacyProject); err != nil {
				writeProjectIdentityHTTPError(w, err)
				return
			}
		}
	} else if identityOnly || projectIdentity != nil {
		writeProjectIdentityHTTPError(w, &gorm.ProjectIdentityError{Code: gorm.ProjectIdentityUnavailable, UpgradeAction: gorm.UpgradeActionRetryProjectRegistration, Err: fmt.Errorf("project identity database is not ready")})
		return
	}

	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if identityOnly {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"canonical_project": project})
		return
	}

	// Server-side: ignore client-provided cwd to prevent filesystem probing (S9-003).
	// File mtime staleness checks are only meaningful on the client; the server has no
	// access to client filesystems.
	cwd = ""

	// Observation limits come from config; fall back to constants when config is absent.
	limit := s.config.ContextObservations
	if limit <= 0 {
		limit = DefaultContextLimit
	}

	// fullCount determines how many observations get full detail (narrative + facts).
	// Observations beyond this index get condensed format to save tokens.
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

	// Apply staleness filter to recent observations.
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

	// Build a set of IDs already in the recent section for deduplication across sections.
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
		rules, guidanceErr := s.behavioralRulesStore.ListEnabled(ctx, projectPtr, 5)
		if guidanceErr != nil {
			log.Debug().Err(guidanceErr).Str("project", project).Msg("Failed to fetch behavioral rules guidance")
		} else {
			guidanceObservations = behavioralRulesToObservations(rules)
		}
	}

	// Add guidance IDs to the dedup set so always-inject doesn't repeat them.
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
		rules, aiErr := s.behavioralRulesStore.ListEnabled(ctx, projectPtr, alwaysInjectLimit)
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
		// W4 P2: apply privacy-scope filter when ENGRAM_VNEXT_F_ENABLED=true so
		// private-scope rows from other workstations are not included in the
		// backward-compat fallback. Flag-OFF: path is byte-identical to pre-fix
		// behavior (scope.FilterMemories is not called).
		if os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true" {
			var callerCtx scope.KeycardContext
			if id, ok := auth.IdentityFrom(ctx); ok {
				callerCtx.WorkstationID = id.WorkstationID()
			}
			mems = scope.FilterMemories(callerCtx, mems)
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

	// Merge relevant observations into the union (those not already in allFreshObservations).
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

	// Cluster the union to remove duplicates (clustering threshold removed in v5).
	clusteredObservations := unionObservations
	duplicatesRemoved := 0

	s.recordRetrievalStatsExtended(project, int64(len(clusteredObservations)), 0, 0,
		int64(staleCount), int64(len(allFreshObservations)), int64(duplicatesRemoved), false)

	// Apply token budget: trim observations to fit within the configured limit.
	tokenBudget := s.config.ContextMaxTokens
	var tokenEstimate int
	var budgetTrimmed int

	if tokenBudget > 0 {
		// Reserve 20% of the budget for guidance; main observations get the remainder.
		guidanceBudget := tokenBudget / 5
		mainBudget := tokenBudget - guidanceBudget

		guidanceObservations, _, _ = trimToTokenBudget(guidanceObservations, guidanceBudget)

		var mainTrimmed int
		clusteredObservations, mainTrimmed, tokenEstimate = trimToTokenBudget(clusteredObservations, mainBudget)
		budgetTrimmed = mainTrimmed

		// Sync recent and relevant sections to only include what survived the budget trim.
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

	// (Active-version narrative substitution — APO-lite Phase 5, backed by the
	// observation_versions table — was removed in CR-2a of provenance-cleanup: the
	// table is unpopulated post-v5 demolition and its store/readers are gone.)

	// Snapshot the vNext stores + flag once. CR-1 (provenance-cleanup): injection_log
	// (mig 106) is now the SOLE injection-record sink for BOTH response strategies —
	// the legacy observation_injections write (InjectionStore.RecordInjections) is
	// gone, so observation_injections is no longer written and CR-3 can drop it.
	s.initMu.RLock()
	vnextMemStore := s.memoryStore
	vnextTracker := s.injectionTracker
	injLogStore := s.injectionLogStore
	s.initMu.RUnlock()

	vnextEnabled := os.Getenv("ENGRAM_VNEXT_ENABLED") == "true"

	// --- vNext Thompson Sampling path (ENGRAM_VNEXT_ENABLED=true) ---
	// When enabled AND scoring succeeds, replaces the response with a Thompson-sampled
	// memory selection and records the scored set to injection_log, then returns. When
	// scoring fails it falls through to the legacy clustering response below, which
	// records its OWN injection set after this block (so a fallback session is never
	// left without injection_log rows — PR #272 review).
	if vnextEnabled && vnextMemStore != nil {
		topK := 15
		const maxTopK = 100
		if v := os.Getenv("ENGRAM_INJECTION_TOP_K"); v != "" {
			if n, parseErr := strconv.Atoi(v); parseErr == nil && n > 0 {
				topK = n
			}
		}
		if topK > maxTopK {
			topK = maxTopK
		}

		vnextMems, vnextErr := listVisibleForInjection(ctx, vnextMemStore, project, topK*3)
		if vnextErr != nil {
			log.Warn().Err(vnextErr).Str("project", project).Msg("vnext: listVisibleForInjection failed, falling back to legacy path")
		} else {
			var scoreOpts injection.ScoreOpts
			citRate, crErr := vnextMemStore.GetProjectCitationRate(ctx, project, 10)
			if crErr == nil && citRate != 0.5 {
				scoreOpts.DynamicPrior = true
				scoreOpts.ProjectCitationRate = citRate
			}
			scored := injection.Score(vnextMems, topK, scoreOpts)

			// Build the selected memory slice for response.
			selectedMems := make([]*models.Memory, 0, topK)
			for _, sm := range scored {
				if sm.Selected && sm.Memory != nil {
					selectedMems = append(selectedMems, sm.Memory)
				}
			}

			// Fire-and-forget injection tracking (CR-1): Tracker.Track records the
			// scored selection to injection_log; BatchIncrementInjected wires the
			// injection_count denominator for the citation-rate signal (drift T1).
			if sessionID != "" && vnextTracker != nil {
				capturedScored := scored
				capturedSID := sessionID
				capturedProj := project
				tracker := vnextTracker
				capturedSelected := selectedMems
				capturedMemStore := vnextMemStore
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					trkCtx, cancel := s.detachedContext(30 * time.Second)
					defer cancel()
					tracker.Track(trkCtx, capturedSID, capturedProj, capturedScored)
					if capturedMemStore != nil && len(capturedSelected) > 0 {
						ids := make([]int64, 0, len(capturedSelected))
						for _, m := range capturedSelected {
							ids = append(ids, m.ID)
						}
						if err := capturedMemStore.BatchIncrementInjected(trkCtx, ids); err != nil {
							log.Warn().Err(err).Str("session_id", capturedSID).Msg("injection_count: thompson-path increment failed")
						}
					}
				}()
			}

			explorationRatio := injection.ExplorationRatio(scored)
			vnextObs := memoriesToObservations(selectedMems)

			injectionMetadata := map[string]any{
				"strategy":          "thompson_sampling",
				"injected_count":    len(selectedMems),
				"candidate_pool":    len(vnextMems),
				"exploration_ratio": explorationRatio,
			}

			log.Info().
				Str("event", "vnext_inject").
				Str("project", project).
				Int("injected_count", len(selectedMems)).
				Int("candidate_pool", len(vnextMems)).
				Float64("exploration_ratio", explorationRatio).
				Msg("vnext Thompson Sampling injection")

			writeJSON(w, map[string]any{
				"strategy":           "thompson_sampling",
				"project":            project,
				"observations":       vnextObs,
				"recent":             compactObservations(recentFresh),
				"relevant":           compactObservations(relevantObservations),
				"guidance":           compactObservations(guidanceObservations),
				"always_inject":      compactObservations(alwaysInjectObservations),
				"project_briefing":   projectBriefingNarrative(false, projectBriefing),
				"full_count":         fullCount,
				"stale_excluded":     staleCount,
				"duplicates_removed": duplicatesRemoved,
				"token_estimate":     tokenEstimate,
				"budget_trimmed":     budgetTrimmed,
				"injection_metadata": injectionMetadata,
			})
			return
		}
	}

	// Legacy (clustering) response path — reached when vNext is disabled OR when the
	// Thompson scoring path errored and fell through (the success path returned above).
	// Record the clustering injection set to injection_log + increment injection_count.
	// This is the ONLY recording for this response, so it runs for BOTH the flag-off and
	// the vnext-fallback cases (PR #272 review: a fallback session must not be left
	// without injection_log rows, or session-end citation detection silently skips it).
	if sessionID != "" && injLogStore != nil {
		capturedAlwaysInject := alwaysInjectObservations
		capturedRecent := recentFresh
		capturedRelevant := relevantObservations
		capturedSessionID := sessionID
		capturedProject := project
		capturedMemStore := vnextMemStore
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			recCtx, cancel := s.detachedContext(30 * time.Second)
			defer cancel()
			seen := make(map[int64]struct{})
			var ids []int64
			for _, group := range [][]*models.Observation{capturedAlwaysInject, capturedRecent, capturedRelevant} {
				for _, obs := range group {
					if obs == nil {
						continue
					}
					if _, dup := seen[obs.ID]; dup {
						continue
					}
					seen[obs.ID] = struct{}{}
					ids = append(ids, obs.ID)
				}
			}
			if len(ids) == 0 {
				return
			}
			if err := injLogStore.Record(recCtx, capturedSessionID, capturedProject, ids); err != nil {
				log.Warn().Err(err).Str("session_id", capturedSessionID).Msg("injection_log: legacy-path record failed")
			}
			if capturedMemStore != nil {
				if err := capturedMemStore.BatchIncrementInjected(recCtx, ids); err != nil {
					log.Warn().Err(err).Str("session_id", capturedSessionID).Msg("injection_count: legacy-path increment failed")
				}
			}
		}()
	}

	// Check if compact format is requested.
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

func writeProjectIdentityHTTPError(w http.ResponseWriter, err error) {
	statusCode := http.StatusServiceUnavailable
	code := gorm.ProjectIdentityUnavailable
	action := gorm.UpgradeActionRetryProjectRegistration
	message := gorm.ProjectIdentityPublicMessage(err)
	var identityErr *gorm.ProjectIdentityError
	if errors.As(err, &identityErr) {
		code = identityErr.Code
		action = identityErr.UpgradeAction
		switch identityErr.Code {
		case gorm.ProjectIdentityInvalid:
			statusCode = http.StatusBadRequest
		case gorm.ProjectIdentityAmbiguous:
			statusCode = http.StatusConflict
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":           code,
			"message":        message,
			"upgrade_action": action,
		},
	})
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
