// Package worker provides data retrieval HTTP handlers.
package worker

import (
	"context"
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
