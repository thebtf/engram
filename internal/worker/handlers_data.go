// Package worker provides data retrieval HTTP handlers.
package worker

import (
	"context"
	"net/http"
	"os"
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

	// matchesFilters is evaluated per-observation after the DB/search fetch.
	// Inline closure avoids threading multiple filter params through helper signatures.
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

	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	searchStart := time.Now()
	scopeFilter := retrievalScope{Project: project}

	// requestedCount is the minimum we need to correctly slice the page.
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

	// Overfetch loop: because filtering happens in-memory after the DB call, we
	// may not get enough matching rows on the first attempt. Increase the fetch limit
	// by overfetchStep each iteration until we have enough or the source is exhausted.
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
			// Source returned fewer rows than requested — nothing more to fetch.
			exhausted = true
			break
		}
		if len(filtered) > requestedCount {
			// We have more than enough; stop early to avoid unnecessary work.
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

	// total and hasMore carry different meanings depending on whether we exhausted
	// the source. When exhausted we know the exact total; otherwise we know a lower bound.
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

	// Project list changes infrequently; 5-minute cache avoids repeated DB hits
	// during dashboard load-bursts.
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
	// These values are compile-time constants; 24-hour cache is safe.
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
	// Model list is static at runtime; 1-hour cache prevents dashboard spam.
	w.Header().Set("Cache-Control", "public, max-age=3600")

	// The server-side embedding-model registry was removed in v5 (embeddings are now
	// computed via external API, not on-box), so this list is intentionally empty.
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

	// Memory stats — cheap runtime call, always included for diagnostics.
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

	// Database health — only when the store is initialized.
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

	if project != "" {
		count, err := s.getCachedObservationCount(r.Context(), project)
		if err == nil {
			response["projectObservations"] = count
			response["project"] = project
		}
	}

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

	// Prefer persistent DB stats for accurate time-range queries.
	// Fall back to in-memory when the log store is unavailable (e.g. during init).
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

	// In-memory fallback (no time range support).
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
		// Return a zero-value response rather than 500 — the store may be initializing.
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
// @Description Reports live pgvector subsystem health. Message reflects whether vNext hybrid retrieval is active.
// @Tags Vectors
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/vectors/health [get]
func (s *Service) handleVectorHealth(w http.ResponseWriter, _ *http.Request) {
	msg := "pgvector available; vNext retrieval gated by ENGRAM_VNEXT_ENABLED"
	if os.Getenv("ENGRAM_VNEXT_ENABLED") == "true" {
		msg = "pgvector hybrid retrieval active"
	}
	writeJSON(w, map[string]any{
		"enabled": true,
		"message": msg,
	})
}

// handleVectorMetrics godoc
// @Summary Get vector database metrics
// @Description Returns live pgvector metrics from the embedding store (chunk_count, memories_with_chunks, etc.).
// @Tags Vectors
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/vector/metrics [get]
func (s *Service) handleVectorMetrics(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	embStore := s.embeddingStore
	s.initMu.RUnlock()

	if embStore == nil {
		msg := "pgvector subsystem available; embedding store not yet initialised"
		enabled := true
		if s.ready.Load() {
			msg = "embedding store unavailable (embedding disabled or failed to initialize)"
			enabled = false
		}
		writeJSON(w, map[string]any{
			"enabled": enabled,
			"message": msg,
		})
		return
	}

	stats, err := embStore.Stats(r.Context())
	if err != nil {
		http.Error(w, "embedding stats unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"enabled": true,
		"message": "pgvector subsystem active",
		"stats":   stats,
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
