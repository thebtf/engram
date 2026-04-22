// Package worker provides data retrieval HTTP handlers.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// handleGetObservations godoc
// @Summary List observations
// @Description Returns recent observations with optional fallback search. Filters by type, status, memory type, and concept, then applies corrected pagination after in-memory filtering.
// @Tags Observations
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param query query string false "Search query"
// @Param type query string false "Filter by observation type"
// @Param status query string false "Filter by status (only active is currently supported)"
// @Param memory_type query string false "Filter by memory type"
// @Param limit query int false "Number of results (default 100)"
// @Param offset query int false "Pagination offset"
// @Param concept query string false "Filter by concept substring match"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Router /api/observations [get]
func (s *Service) handleGetObservations(w http.ResponseWriter, r *http.Request) {
	pagination := gorm.ParsePaginationParams(r, DefaultObservationsLimit)
	project := r.URL.Query().Get("project")
	query := r.URL.Query().Get("query")
	obsType := r.URL.Query().Get("type")
	status := r.URL.Query().Get("status")
	memoryType := r.URL.Query().Get("memory_type")
	concept := strings.TrimSpace(r.URL.Query().Get("concept"))

	matchesFilters := func(observation *models.Observation) bool {
		if observation == nil {
			return false
		}
		if obsType != "" && string(observation.Type) != obsType {
			return false
		}
		// searchFallbackObservations only returns active observations in v5.
		// That means status filtering is capability-based rather than row-based:
		// - empty status: accept the active-only fallback result set as-is
		// - status=active: explicitly accept that same active-only result set
		// - any other explicit status: return no matches because those states are unavailable here
		if status != "" && status != "active" {
			return false
		}
		if memoryType != "" && string(observation.MemoryType) != memoryType {
			return false
		}
		if concept == "" {
			return true
		}
		needle := strings.ToLower(concept)
		for _, candidate := range observation.Concepts {
			if strings.Contains(strings.ToLower(candidate), needle) {
				return true
			}
		}
		return false
	}

	// Validate project name to prevent path traversal
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	searchStart := time.Now()
	scopeFilter := retrievalScope{Project: project}
	requestedCount := pagination.Offset + pagination.Limit
	if requestedCount <= 0 {
		requestedCount = pagination.Limit
	}
	if requestedCount <= 0 {
		requestedCount = DefaultObservationsLimit
	}

	overfetchStep := pagination.Limit
	if overfetchStep <= 0 {
		overfetchStep = DefaultObservationsLimit
	}

	fetchLimit := requestedCount
	filtered := make([]*models.Observation, 0, fetchLimit)
	exhausted := false

	for {
		observations, err := s.searchFallbackObservations(r.Context(), query, scopeFilter, fetchLimit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if observations == nil {
			observations = []*models.Observation{}
		}

		filtered = filtered[:0]
		for _, observation := range observations {
			if matchesFilters(observation) {
				filtered = append(filtered, observation)
			}
		}

		if len(observations) < fetchLimit {
			exhausted = true
			break
		}
		if len(filtered) > requestedCount {
			break
		}

		nextFetchLimit := fetchLimit + overfetchStep
		if nextFetchLimit <= fetchLimit {
			exhausted = true
			break
		}
		fetchLimit = nextFetchLimit
	}

	page := 1
	if pagination.Limit > 0 {
		page = (pagination.Offset / pagination.Limit) + 1
	}

	total := int64(len(filtered))
	hasMore := false
	if exhausted {
		hasMore = int64(pagination.Offset)+int64(pagination.Limit) < total
	} else {
		hasMore = true
		if lowerBoundTotal := requestedCount + 1; lowerBoundTotal > len(filtered) {
			total = int64(lowerBoundTotal)
		}
	}

	pageItems := []*models.Observation{}
	if pagination.Offset < len(filtered) {
		end := pagination.Offset + pagination.Limit
		if end > len(filtered) {
			end = len(filtered)
		}
		pageItems = filtered[pagination.Offset:end]
	}

	if query != "" {
		s.trackSearchQuery(query, project, "observations", len(pageItems), float32(time.Since(searchStart).Milliseconds()))
	}

	resp := map[string]any{
		"observations": pageItems,
		"total":        total,
		"limit":        pagination.Limit,
		"offset":       pagination.Offset,
		"page":         page,
		"hasMore":      hasMore,
	}
	if project != "" {
		resp["project_display_name"] = s.getProjectDisplayName(r.Context(), project)
	}
	writeJSON(w, resp)
}

// handleGetSummaries godoc
// @Summary List summaries
// @Description Session summaries endpoint removed in v5. Returns 501 Not Implemented after request validation.
// @Tags Observations
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param query query string false "Semantic search query"
// @Param limit query int false "Number of results (default 50)"
// @Success 501 {string} string "session summaries endpoint removed in v5; session_summaries persistence was dropped in US3-PR-B"
// @Failure 400 {string} string "bad request"
// @Router /api/summaries [get]
func (s *Service) handleGetSummaries(w http.ResponseWriter, r *http.Request) {
	limit := gorm.ParseLimitParam(r, DefaultSummariesLimit)
	project := r.URL.Query().Get("project")
	query := r.URL.Query().Get("query")

	// Validate project name to prevent path traversal
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	_ = limit
	_ = query

	http.Error(w, "session summaries endpoint removed in v5; session_summaries persistence was dropped in US3-PR-B", http.StatusNotImplemented)
}

// handleGetPrompts godoc
// @Summary List user prompts
// @Description User prompts endpoint removed in v5. Returns 501 Not Implemented after request validation.
// @Tags Observations
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param query query string false "Semantic search query"
// @Param limit query int false "Number of results (default 100)"
// @Success 501 {string} string "user prompts endpoint removed in v5; prompt persistence was dropped in US3-PR-B"
// @Failure 400 {string} string "bad request"
// @Router /api/prompts [get]
func (s *Service) handleGetPrompts(w http.ResponseWriter, r *http.Request) {
	limit := gorm.ParseLimitParam(r, DefaultPromptsLimit)
	project := r.URL.Query().Get("project")
	query := r.URL.Query().Get("query")

	// Validate project name to prevent path traversal
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	_ = limit
	_ = query

	http.Error(w, "user prompts endpoint removed in v5; prompt persistence was dropped in US3-PR-B", http.StatusNotImplemented)
}

// handleGetProjects godoc
// @Summary List projects
// @Description Returns all known projects. Response is cacheable for 5 minutes.
// @Tags Observations
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {array} object
// @Failure 500 {string} string "internal error"
// @Router /api/projects [get]
func (s *Service) handleGetProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.sessionStore.GetAllProjects(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Cache for 5 minutes - project list changes infrequently
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, projects)
}

// handleGetTypes godoc
// @Summary List observation and concept types
// @Description Returns the canonical list of observation and concept types. Cacheable for 24 hours.
// @Tags Observations
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/types [get]
func (s *Service) handleGetTypes(w http.ResponseWriter, r *http.Request) {
	// Cache for 24 hours - these values are compile-time constants
	w.Header().Set("Cache-Control", "public, max-age=86400")
	writeJSON(w, map[string]any{
		"observation_types": ObservationTypes,
		"concept_types":     ConceptTypes,
	})
}

// handleGetModels godoc
// @Summary List embedding models
// @Description Returns available embedding models with default and current model info. Cacheable for 1 hour.
// @Tags Observations
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/models [get]
func (s *Service) handleGetModels(w http.ResponseWriter, _ *http.Request) {
	// Cache for 1 hour - model list is static during runtime
	w.Header().Set("Cache-Control", "public, max-age=3600")

	// Embedding models removed in v5 (content_chunks table dropped)
	writeJSON(w, map[string]any{
		"models":  []any{},
		"default": nil,
		"current": "",
	})
}

// handleGetStats godoc
// @Summary Get worker statistics
// @Description Returns comprehensive worker statistics including uptime, memory, database health, vector cache, graph, and rate limiter stats.
// @Tags Analytics
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter stats by project"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Router /api/stats [get]
func (s *Service) handleGetStats(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")

	// Validate project name to prevent path traversal
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	retrievalStats := s.GetRetrievalStats(project)
	sessionsToday, _ := s.sessionStore.GetSessionsToday(r.Context())

	response := map[string]any{
		"uptime":           time.Since(s.startTime).String(),
		"uptimeSeconds":    time.Since(s.startTime).Seconds(),
		"activeSessions":   s.sessionManager.GetActiveSessionCount(),
		"queueDepth":       s.sessionManager.GetTotalQueueDepth(),
		"isProcessing":     s.sessionManager.IsAnySessionProcessing(),
		"connectedClients": s.sseBroadcaster.ClientCount(),
		"sessionsToday":    sessionsToday,
		"retrieval":        retrievalStats,
		"ready":            s.ready.Load(),
	}

	// Add memory stats
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	response["memory"] = map[string]any{
		"alloc_mb":          float64(memStats.Alloc) / 1024 / 1024,
		"total_alloc_mb":    float64(memStats.TotalAlloc) / 1024 / 1024,
		"sys_mb":            float64(memStats.Sys) / 1024 / 1024,
		"heap_alloc_mb":     float64(memStats.HeapAlloc) / 1024 / 1024,
		"heap_inuse_mb":     float64(memStats.HeapInuse) / 1024 / 1024,
		"heap_objects":      memStats.HeapObjects,
		"goroutines":        runtime.NumGoroutine(),
		"gc_cycles":         memStats.NumGC,
		"gc_pause_total_ms": float64(memStats.PauseTotalNs) / 1e6,
	}

	// Add database health if available
	if s.store != nil {
		dbHealth := s.store.HealthCheck(r.Context())
		response["database"] = map[string]any{
			"status":           dbHealth.Status,
			"query_latency_ms": float64(dbHealth.QueryLatency) / 1e6,
			"pool":             dbHealth.PoolStats,
			"warning":          dbHealth.Warning,
		}
	}

	// observationCount was backed by the removed observations store in v5.
	// Keep only projectObservations, which now comes from v5 stores via cache.

	// Include project-specific observation count if project is specified
	if project != "" {
		count, err := s.getCachedObservationCount(r.Context(), project)
		if err == nil {
			response["projectObservations"] = count
			response["project"] = project
		}
	}

	// Add rate limiter stats
	if s.rateLimiter != nil {
		response["rateLimiter"] = s.rateLimiter.Stats()
	}

	// Add circuit breaker metrics
	if s.processor != nil {
		response["circuitBreaker"] = s.processor.CircuitBreakerMetrics()
	}

	writeJSON(w, response)
}

// handleGetRetrievalStats godoc
// @Summary Get retrieval statistics
// @Description Returns detailed retrieval statistics including hit rates and latency.
// @Tags Analytics
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param since query string false "ISO8601 timestamp for time range filter"
// @Success 200 {object} map[string]interface{}
// @Router /api/stats/retrieval [get]
func (s *Service) handleGetRetrievalStats(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")

	// Try persistent DB stats first, fall back to in-memory.
	s.initMu.RLock()
	logStore := s.retrievalStatsLogStore
	s.initMu.RUnlock()

	if logStore != nil {
		var since time.Time
		if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
			if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
				since = t
			}
		}
		dbStats, err := logStore.GetStats(r.Context(), project, since)
		if err == nil {
			writeJSON(w, dbStats)
			return
		}
		log.Warn().Err(err).Msg("failed to get retrieval stats from DB, falling back to in-memory")
	}

	// Fallback to in-memory stats (no time range support).
	stats := s.GetRetrievalStats(project)
	writeJSON(w, stats)
}

// handleGetRecentQueries godoc
// @Summary Get recent search queries
// @Description Returns recent search queries for analytics purposes.
// @Tags Analytics
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param limit query int false "Number of results (default 20)"
// @Success 200 {object} map[string]interface{}
// @Router /api/search/recent [get]
func (s *Service) handleGetRecentQueries(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.searchQueryLogStore
	s.initMu.RUnlock()

	project := r.URL.Query().Get("project")
	limit := gorm.ParseLimitParam(r, 20)

	if store == nil {
		writeJSON(w, map[string]any{"queries": []any{}, "count": 0, "project": project})
		return
	}

	queries, err := store.GetRecent(r.Context(), project, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"queries": queries,
		"count":   len(queries),
		"project": project,
	})
}

// handleGetSearchAnalytics godoc
// @Summary Get search analytics
// @Description Returns aggregated search analytics from the persistent search query log, including vector search counts, latency, and zero-result rate. Supports optional time-range filtering via the 'since' parameter.
// @Tags Analytics
// @Produce json
// @Security ApiKeyAuth
// @Param since query string false "ISO8601 timestamp to filter results (e.g. 2024-01-01T00:00:00Z)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "invalid 'since' parameter"
// @Failure 500 {string} string "internal error"
// @Router /api/search/analytics [get]
func (s *Service) handleGetSearchAnalytics(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.searchQueryLogStore
	s.initMu.RUnlock()

	if store == nil {
		writeJSON(w, map[string]any{
			"total_searches":   0,
			"searches_today":   0,
			"avg_latency_ms":   0,
			"zero_result_rate": 0,
			"vector_searches":  0,
			"filter_searches":  0,
			"cache_hits":       0,
			"search_errors":    0,
		})
		return
	}

	var since time.Time
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		var err error
		since, err = time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			http.Error(w, "invalid 'since' parameter: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	analytics, err := store.GetAnalytics(r.Context(), since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, analytics)
}

// handleVectorHealth godoc
// @Summary Get vector database health
// @Description Vector storage was removed in v5. Always returns disabled status.
// @Tags Vectors
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/vectors/health [get]
func (s *Service) handleVectorHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"enabled": false,
		"message": "Vector storage removed in v5 (content_chunks table dropped)",
	})
}

// UpdateObservationRequest is the request body for updating an observation.
type UpdateObservationRequest struct {
	Title         *string  `json:"title,omitempty"`
	Subtitle      *string  `json:"subtitle,omitempty"`
	Narrative     *string  `json:"narrative,omitempty"`
	Scope         *string  `json:"scope,omitempty"`
	Facts         []string `json:"facts,omitempty"`
	Concepts      []string `json:"concepts,omitempty"`
	FilesRead     []string `json:"files_read,omitempty"`
	FilesModified []string `json:"files_modified,omitempty"`
}

// handleUpdateObservation godoc
// @Summary Update an observation
// @Description Observation update endpoint removed in v5. Returns 501 Not Implemented after request validation.
// @Tags Observations
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Param body body UpdateObservationRequest true "Fields to update"
// @Success 501 {string} string "observation update endpoint removed in v5; observations persistence was dropped in US3-PR-B"
// @Failure 400 {string} string "bad request"
// @Router /api/observations/{id} [put]
func (s *Service) handleUpdateObservation(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r.PathValue("id"), "observation")
	if !ok {
		return
	}

	var req UpdateObservationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	_ = id
	_ = req

	http.Error(w, "observation update endpoint removed in v5; observations persistence was dropped in US3-PR-B", http.StatusNotImplemented)
}

// handleGetObservationByID godoc
// @Summary Get observation by ID
// @Description Observation lookup endpoint removed in v5. Returns 501 Not Implemented after request validation.
// @Tags Observations
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Success 501 {string} string "observation lookup endpoint removed in v5; observations persistence was dropped in US3-PR-B"
// @Failure 400 {string} string "bad request"
// @Router /api/observations/{id} [get]
func (s *Service) handleGetObservationByID(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r.PathValue("id"), "observation")
	if !ok {
		return
	}

	_ = id

	http.Error(w, "observation lookup endpoint removed in v5; observations persistence was dropped in US3-PR-B", http.StatusNotImplemented)
}

// handleGraphStats godoc
// @Summary Get graph statistics
// @Description Returns graph statistics for the dashboard, using relation data to compute knowledge graph metrics.
// @Tags Graph
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/graph/stats [get]
func (s *Service) handleGraphStats(w http.ResponseWriter, r *http.Request) {
	// Get relation count (edges) - this represents the knowledge graph
	edgeCount, err := s.relationStore.GetTotalRelationCount(r.Context())
	if err != nil {
		edgeCount = 0
	}

	// Count by relation type
	edgeTypes := make(map[string]int)
	for _, t := range models.AllRelationTypes {
		relations, err := s.relationStore.GetRelationsByType(r.Context(), t, 10000)
		if err == nil {
			edgeTypes[string(t)] = len(relations)
		}
	}

	// Get unique observation IDs involved in relations (real node count)
	nodeCount, err := s.relationStore.GetDistinctNodeCount(r.Context())
	if err != nil {
		nodeCount = 0
	}

	// Calculate average degree (each edge contributes to 2 nodes)
	var avgDegree float64
	if nodeCount > 0 {
		avgDegree = float64(edgeCount*2) / float64(nodeCount)
	}

	// Max degree from SQL
	maxDegree, err := s.relationStore.GetMaxDegree(r.Context())
	if err != nil {
		maxDegree = 0
	}

	// Graph is enabled if we have any edges (relations)
	enabled := edgeCount > 0

	writeJSON(w, map[string]any{
		"enabled":      enabled,
		"nodeCount":    nodeCount,
		"edgeCount":    edgeCount,
		"avgDegree":    avgDegree,
		"maxDegree":    maxDegree,
		"minDegree":    0,
		"medianDegree": 0.0,
		"edgeTypes":    edgeTypes,
		"config": map[string]any{
			"maxHops":            2,
			"branchFactor":       10,
			"edgeWeight":         0.3,
			"rebuildIntervalMin": 30,
		},
	})
}

// handleVectorMetrics godoc
// @Summary Get vector database metrics
// @Description Vector storage was removed in v5. Always returns disabled status.
// @Tags Vectors
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/vector/metrics [get]
func (s *Service) handleVectorMetrics(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"enabled": false,
		"message": "Vector storage removed in v5 (content_chunks table dropped)",
	})
}

// bulkDeleteRequest is the JSON body for DELETE /api/observations/bulk.
type bulkDeleteRequest struct {
	IDs []int64 `json:"ids"`
}

// handleBulkDeleteREST godoc
// @Summary Bulk delete observations
// @Description Bulk observation delete endpoint removed in v5. Returns 501 Not Implemented after request validation.
// @Tags Observations
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body bulkDeleteRequest true "Observation IDs to delete"
// @Success 501 {string} string "bulk observation delete endpoint removed in v5; observations persistence was dropped in US3-PR-B"
// @Failure 400 {string} string "bad request"
// @Router /api/observations/bulk [delete]
func (s *Service) handleBulkDeleteREST(w http.ResponseWriter, r *http.Request) {
	var req bulkDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		http.Error(w, "ids is required and must not be empty", http.StatusBadRequest)
		return
	}
	if len(req.IDs) > 100 {
		http.Error(w, "ids must not exceed 100 entries per request", http.StatusBadRequest)
		return
	}

	http.Error(w, "bulk observation delete endpoint removed in v5; observations persistence was dropped in US3-PR-B", http.StatusNotImplemented)
}

// bulkScopeChangeRequest is the JSON body for PATCH /api/observations/bulk-scope.
type bulkScopeChangeRequest struct {
	IDs   []int64 `json:"ids"`
	Scope string  `json:"scope"` // "global" or "project"
}

// handleBulkScopeChange godoc
// @Summary Bulk update observation scope
// @Description Bulk observation scope endpoint removed in v5. Returns 501 Not Implemented after request validation.
// @Tags Observations
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body bulkScopeChangeRequest true "IDs and new scope"
// @Success 501 {string} string "bulk observation scope endpoint removed in v5; observations persistence was dropped in US3-PR-B"
// @Failure 400 {string} string "bad request"
// @Router /api/observations/bulk-scope [patch]
func (s *Service) handleBulkScopeChange(w http.ResponseWriter, r *http.Request) {
	var req bulkScopeChangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		http.Error(w, "ids is required and must not be empty", http.StatusBadRequest)
		return
	}
	switch req.Scope {
	case "global", "project":
		// valid
	default:
		http.Error(w, "scope must be 'global' or 'project'", http.StatusBadRequest)
		return
	}

	http.Error(w, "bulk observation scope endpoint removed in v5; observations persistence was dropped in US3-PR-B", http.StatusNotImplemented)
}

const noHitRateAnalyticsDataMessage = "No hit rate analytics data available. Hit rate analytics is disabled in v5 (injection_log was dropped in US1)."

type hitRateAnalyticsRow struct {
	ID    int64  `gorm:"column:id"`
	Title string `gorm:"column:title"`
	Type  string `gorm:"column:type"`
	Flag  string `gorm:"column:flag"`
}

func (s *Service) queryHitRateAnalyticsRows(ctx context.Context, project string, limit int) ([]hitRateAnalyticsRow, error) {
	_ = ctx
	_ = project
	_ = limit
	return nil, nil
}

func formatHitRateAnalyticsMarkdown(rows []hitRateAnalyticsRow) string {
	if len(rows) == 0 {
		return noHitRateAnalyticsDataMessage
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Hit Rate Analytics (%d observations)\n\n", len(rows)))
	noiseCount, starCount := 0, 0
	sb.WriteString("### Noise Candidates (injected 10+ times, never cited)\n")
	for _, row := range rows {
		if row.Flag == "noise_candidate" {
			sb.WriteString(fmt.Sprintf("- [%d] %s (%s)\n", row.ID, row.Title, row.Type))
			noiseCount++
		}
	}
	if noiseCount == 0 {
		sb.WriteString("None found.\n")
	}

	sb.WriteString("\n### High Value (injected 5+ times, >50% citation rate)\n")
	for _, row := range rows {
		if row.Flag == "high_value" {
			sb.WriteString(fmt.Sprintf("- [%d] %s (%s)\n", row.ID, row.Title, row.Type))
			starCount++
		}
	}
	if starCount == 0 {
		sb.WriteString("None found.\n")
	}
	return sb.String()
}

func buildHitRateAnalyticsResponse(rows []hitRateAnalyticsRow) map[string]any {
	observations := make([]map[string]any, 0, len(rows))
	noiseCount := 0
	valueCount := 0

	for _, row := range rows {
		if row.Flag == "noise_candidate" {
			noiseCount++
		}
		if row.Flag == "high_value" {
			valueCount++
		}

		observations = append(observations, map[string]any{
			"id":    row.ID,
			"title": row.Title,
			"type":  row.Type,
			"flag":  row.Flag,
		})
	}

	return map[string]any{
		"high_value":       valueCount,
		"noise_candidates": noiseCount,
		"observations":     observations,
		"total":            len(observations),
	}
}

func (s *Service) handleGetHitRateAnalytics(w http.ResponseWriter, r *http.Request) {
	rows, err := s.queryHitRateAnalyticsRows(r.Context(), r.URL.Query().Get("project"), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, buildHitRateAnalyticsResponse(rows))
}

func (s *Service) handleGetSimilarityTelemetry(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	st := s.similarityTelemetry
	s.initMu.RUnlock()

	if st == nil {
		writeJSON(w, map[string]any{
			"enabled": false,
			"message": "Similarity telemetry not initialized",
		})
		return
	}

	project := r.URL.Query().Get("project")

	if project != "" {
		snapshot, err := st.GetLatestSnapshot(r.Context(), project)
		if err != nil {
			http.Error(w, "failed to get telemetry: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"enabled":  true,
			"project":  project,
			"snapshot": snapshot,
		})
		return
	}

	snapshots, err := st.GetAllLatestSnapshots(r.Context())
	if err != nil {
		http.Error(w, "failed to get telemetry: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"enabled":   true,
		"snapshots": snapshots,
	})
}

// getProjectDisplayName looks up the display_name for a project ID from the projects table.
// It checks both the primary ID and the legacy_ids array.
// Returns the display name if found, or falls back to the raw project ID.
// Returns empty string (triggering caller fallback) on DB error.
func (s *Service) getProjectDisplayName(ctx context.Context, projectID string) string {
	if projectID == "" || s.store == nil {
		return projectID
	}
	var displayName string
	if err := s.store.GetDB().WithContext(ctx).
		Raw("SELECT display_name FROM projects WHERE removed_at IS NULL AND (id = ? OR ? = ANY(COALESCE(legacy_ids, ARRAY[]::TEXT[])))", projectID, projectID).
		Scan(&displayName).Error; err != nil {
		return ""
	}
	if displayName == "" {
		return projectID
	}
	return displayName
}
