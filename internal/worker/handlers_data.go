// Package worker provides data retrieval HTTP handlers.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/pkg/models"
)

// handleGetObservations godoc
// @Summary List observations
// @Description Returns recent observations with optional semantic search via vector store. Supports pagination.
// @Tags Observations
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param query query string false "Semantic search query"
// @Param limit query int false "Number of results (default 100)"
// @Param offset query int false "Pagination offset"
// @Param concept query string false "Filter by concept (LIKE match on concepts JSON column)"
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
	concept := r.URL.Query().Get("concept")

	// Validate project name to prevent path traversal
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var observations []*models.Observation
	var total int64
	var err error
	var usedVector bool
	searchStart := time.Now()

	// Use vector search if query is provided and vector client is available
	if query != "" && s.vectorClient != nil && s.vectorClient.IsConnected() {
		where := vector.BuildWhereFilter(vector.DocTypeObservation, "", false, nil)
		vectorResults, vecErr := s.vectorClient.Query(r.Context(), query, pagination.Limit*2, where)
		if vecErr == nil && len(vectorResults) > 0 {
			obsIDs := vector.ExtractObservationIDs(vectorResults, project)
			if len(obsIDs) > 0 {
				observations, err = s.observationStore.GetObservationsByIDs(r.Context(), obsIDs, "date_desc", pagination.Limit)
				if err == nil {
					usedVector = true
					total = int64(len(observations)) // Vector search doesn't have total, use returned count
				}
			}
		}
	}

	// Fall back to database query if vector search not used
	if !usedVector {
		if project != "" {
			// Strict project filtering for dashboard - only observations from this project
			observations, total, err = s.observationStore.GetObservationsByProjectStrictPaginated(r.Context(), project, obsType, status, memoryType, concept, pagination.Limit, pagination.Offset)
		} else {
			// All projects
			observations, total, err = s.observationStore.GetAllRecentObservationsPaginated(r.Context(), obsType, status, memoryType, concept, pagination.Limit, pagination.Offset)
		}
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Ensure we return empty array, not null
	if observations == nil {
		observations = []*models.Observation{}
	}

	// Track search if query was provided
	if query != "" {
		s.trackSearchQuery(query, project, "observations", len(observations), usedVector, float32(time.Since(searchStart).Milliseconds()))
	}

	// Return paginated response
	writeJSON(w, map[string]any{
		"observations": observations,
		"total":        total,
		"limit":        pagination.Limit,
		"offset":       pagination.Offset,
		"hasMore":      int64(pagination.Offset)+int64(len(observations)) < total,
	})
}

// handleGetSummaries godoc
// @Summary List summaries
// @Description Returns recent session summaries with optional semantic search via vector store.
// @Tags Observations
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param query query string false "Semantic search query"
// @Param limit query int false "Number of results (default 50)"
// @Success 200 {array} models.SessionSummary
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
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

	var summaries []*models.SessionSummary
	var err error
	var usedVector bool

	// Use vector search if query is provided and vector client is available
	if query != "" && s.vectorClient != nil && s.vectorClient.IsConnected() {
		where := vector.BuildWhereFilter(vector.DocTypeSessionSummary, "", false, nil)
		vectorResults, vecErr := s.vectorClient.Query(r.Context(), query, limit*2, where)
		if vecErr == nil && len(vectorResults) > 0 {
			summaryIDs := vector.ExtractSummaryIDs(vectorResults, project)
			if len(summaryIDs) > 0 {
				summaries, err = s.summaryStore.GetSummariesByIDs(r.Context(), summaryIDs, "date_desc", limit)
				if err == nil {
					usedVector = true
				}
			}
		}
	}

	// Fall back to database query if vector search not used
	if !usedVector {
		if project != "" {
			summaries, err = s.summaryStore.GetRecentSummaries(r.Context(), project, limit)
		} else {
			summaries, err = s.summaryStore.GetAllRecentSummaries(r.Context(), limit)
		}
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Ensure we return empty array, not null
	if summaries == nil {
		summaries = []*models.SessionSummary{}
	}
	writeJSON(w, summaries)
}

// handleGetPrompts godoc
// @Summary List user prompts
// @Description Returns recent user prompts with optional semantic search via vector store.
// @Tags Observations
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param query query string false "Semantic search query"
// @Param limit query int false "Number of results (default 100)"
// @Success 200 {array} models.UserPromptWithSession
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
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

	var prompts []*models.UserPromptWithSession
	var err error
	var usedVector bool

	// Use vector search if query is provided and vector client is available
	if query != "" && s.vectorClient != nil && s.vectorClient.IsConnected() {
		where := vector.BuildWhereFilter(vector.DocTypeUserPrompt, "", false, nil)
		vectorResults, vecErr := s.vectorClient.Query(r.Context(), query, limit*2, where)
		if vecErr == nil && len(vectorResults) > 0 {
			promptIDs := vector.ExtractPromptIDs(vectorResults, project)
			if len(promptIDs) > 0 {
				prompts, err = s.promptStore.GetPromptsByIDs(r.Context(), promptIDs, "date_desc", limit)
				if err == nil {
					usedVector = true
				}
			}
		}
	}

	// Fall back to database query if vector search not used
	if !usedVector {
		if project != "" {
			prompts, err = s.promptStore.GetRecentUserPromptsByProject(r.Context(), project, limit)
		} else {
			prompts, err = s.promptStore.GetAllRecentUserPrompts(r.Context(), limit)
		}
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Ensure we return empty array, not null
	if prompts == nil {
		prompts = []*models.UserPromptWithSession{}
	}
	writeJSON(w, prompts)
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

	models := embedding.ListModels()
	defaultModel := embedding.GetDefaultModel()

	writeJSON(w, map[string]any{
		"models":  models,
		"default": defaultModel,
		"current": s.embedSvc.Version(),
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
		"uptime":            time.Since(s.startTime).String(),
		"uptimeSeconds":     time.Since(s.startTime).Seconds(),
		"activeSessions":    s.sessionManager.GetActiveSessionCount(),
		"queueDepth":        s.sessionManager.GetTotalQueueDepth(),
		"isProcessing":      s.sessionManager.IsAnySessionProcessing(),
		"connectedClients":  s.sseBroadcaster.ClientCount(),
		"sessionsToday":     sessionsToday,
		"retrieval":         retrievalStats,
		"ready":             s.ready.Load(),
		"vectorSyncDropped": s.vectorSyncDropped.Load(),
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

	// Add embedding model info
	if s.embedSvc != nil {
		response["embeddingModel"] = map[string]any{
			"name":       s.embedSvc.Name(),
			"version":    s.embedSvc.Version(),
			"dimensions": s.embedSvc.Dimensions(),
		}
	}

	// Add vector cache stats
	if s.vectorClient != nil {
		if count, err := s.vectorClient.Count(r.Context()); err == nil {
			response["vectorCount"] = count
		}
		cacheStats := s.vectorClient.GetCacheStats()
		response["vectorCache"] = map[string]any{
			"hit_rate": cacheStats.HitRate(),
		}
	}

	// Include total observation count (active, non-archived, non-superseded)
	if s.observationStore != nil {
		obsCount, err := s.observationStore.GetTotalObservationCount(r.Context(), project)
		if err == nil {
			response["observationCount"] = obsCount
		}
	}

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

	// Add graph store stats
	if s.graphStore != nil {
		gs, err := s.graphStore.Stats(r.Context())
		if err == nil {
			response["graph"] = map[string]any{
				"provider":   gs.Provider,
				"connected":  gs.Connected,
				"node_count": gs.NodeCount,
				"edge_count": gs.EdgeCount,
			}
		} else {
			response["graph"] = map[string]any{
				"provider":  gs.Provider,
				"connected": false,
				"error":     err.Error(),
			}
		}
		if s.graphWriter != nil {
			enqueued, written, dropped := s.graphWriter.Stats()
			response["graphWriter"] = map[string]any{
				"enqueued": enqueued,
				"written":  written,
				"dropped":  dropped,
			}
		}
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
// @Description Returns comprehensive health information about the vector database including health score, warnings, cache hit rate, and rebuild status.
// @Tags Vectors
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "vector client not initialized"
// @Router /api/vectors/health [get]
func (s *Service) handleVectorHealth(w http.ResponseWriter, r *http.Request) {
	if s.vectorClient == nil {
		http.Error(w, "vector client not initialized", http.StatusServiceUnavailable)
		return
	}

	stats, err := s.vectorClient.GetHealthStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Add additional computed metrics
	healthScore := 100.0
	var warnings []string

	// Penalize for stale vectors
	if stats.TotalVectors > 0 {
		staleRatio := float64(stats.StaleVectors) / float64(stats.TotalVectors)
		if staleRatio > 0 {
			healthScore -= staleRatio * 50 // Up to 50 points off for stale vectors
			warnings = append(warnings, formatWarning("%.1f%% vectors need rebuild", staleRatio*100))
		}
	}

	// Check cache effectiveness
	cacheStats := s.vectorClient.GetCacheStats()
	cacheHitRate := cacheStats.HitRate()
	if cacheHitRate < 20 && (cacheStats.EmbeddingHits+cacheStats.EmbeddingMisses) > 100 {
		healthScore -= 10
		warnings = append(warnings, formatWarning("Low cache hit rate: %.1f%%", cacheHitRate))
	}

	// Penalize if rebuild is needed
	if stats.NeedsRebuild {
		healthScore -= 20
		warnings = append(warnings, "Vector rebuild recommended: "+stats.RebuildReason)
	}

	if healthScore < 0 {
		healthScore = 0
	}

	status := "healthy"
	if healthScore < 50 {
		status = "unhealthy"
	} else if healthScore < 80 {
		status = "degraded"
	}

	writeJSON(w, map[string]any{
		"status":         status,
		"health_score":   healthScore,
		"warnings":       warnings,
		"stats":          stats,
		"cache_hit_rate": cacheHitRate,
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
// @Description Updates an existing observation's fields. Only provided fields are updated.
// @Tags Observations
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Param body body UpdateObservationRequest true "Fields to update"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Failure 404 {string} string "observation not found"
// @Failure 500 {string} string "internal error"
// @Router /api/observations/{id} [put]
func (s *Service) handleUpdateObservation(w http.ResponseWriter, r *http.Request) {
	// Parse observation ID from URL
	id, ok := parseIDParam(w, r.PathValue("id"), "observation")
	if !ok {
		return
	}

	// Parse request body
	var req UpdateObservationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Build update struct - only include fields that were provided
	update := &gorm.ObservationUpdate{}

	if req.Title != nil {
		update.Title = req.Title
	}
	if req.Subtitle != nil {
		update.Subtitle = req.Subtitle
	}
	if req.Narrative != nil {
		update.Narrative = req.Narrative
	}
	if req.Facts != nil {
		update.Facts = &req.Facts
	}
	if req.Concepts != nil {
		update.Concepts = &req.Concepts
	}
	if req.FilesRead != nil {
		update.FilesRead = &req.FilesRead
	}
	if req.FilesModified != nil {
		update.FilesModified = &req.FilesModified
	}
	if req.Scope != nil {
		// Validate scope
		if *req.Scope != "project" && *req.Scope != "global" {
			http.Error(w, "scope must be 'project' or 'global'", http.StatusBadRequest)
			return
		}
		update.Scope = req.Scope
	}

	// Update the observation
	updatedObs, err := s.observationStore.UpdateObservation(r.Context(), id, update)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, "failed to update observation: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Trigger vector resync for the updated observation
	if s.vectorSync != nil {
		s.asyncVectorSync(func() {
			if err := s.vectorSync.SyncObservation(s.ctx, updatedObs); err != nil {
				log.Warn().Err(err).Int64("id", id).Msg("Failed to resync observation vectors after update")
			}
		})
	}

	// Broadcast update event
	s.sseBroadcaster.Broadcast(map[string]any{
		"type": "observation_updated",
		"id":   id,
	})

	writeJSON(w, map[string]any{
		"observation": updatedObs,
		"message":     "observation updated successfully",
	})
}

// handleGetObservationByID godoc
// @Summary Get observation by ID
// @Description Returns a single observation by its ID.
// @Tags Observations
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Success 200 {object} models.Observation
// @Failure 400 {string} string "bad request"
// @Failure 404 {string} string "observation not found"
// @Failure 500 {string} string "internal error"
// @Router /api/observations/{id} [get]
func (s *Service) handleGetObservationByID(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r.PathValue("id"), "observation")
	if !ok {
		return
	}

	obs, err := s.observationStore.GetObservationByID(r.Context(), id)
	if err != nil {
		http.Error(w, "failed to get observation: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if obs == nil {
		http.Error(w, "observation not found", http.StatusNotFound)
		return
	}

	writeJSON(w, obs)
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
// @Description Returns vector database metrics for the dashboard including queries, latency, storage, and cache stats. Returns enabled: false if vector features are not available.
// @Tags Vectors
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/vector/metrics [get]
func (s *Service) handleVectorMetrics(w http.ResponseWriter, r *http.Request) {
	if s.vectorClient == nil {
		writeJSON(w, map[string]any{
			"enabled": false,
			"message": "Vector database not initialized",
		})
		return
	}

	metrics := s.vectorClient.GetMetrics(r.Context())

	writeJSON(w, map[string]any{
		"enabled":         true,
		"query_count":     metrics.QueryCount,
		"avg_latency_ms":  metrics.AvgLatencyMs,
		"p50_latency_ms":  metrics.P50LatencyMs,
		"p95_latency_ms":  metrics.P95LatencyMs,
		"p99_latency_ms":  metrics.P99LatencyMs,
		"total_documents": metrics.TotalDocs,
		"uptime":          time.Since(s.startTime).Round(time.Second).String(),
	})
}

// handleGraphSync godoc
// @Summary Sync graph from relations
// @Description Triggers a manual re-sync of relations from PostgreSQL to FalkorDB. Runs in background.
// @Tags Graph
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Failure 503 {string} string "graph backend not connected"
// @Router /api/graph/sync [post]
func (s *Service) handleGraphSync(w http.ResponseWriter, r *http.Request) {
	if s.graphStore == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{"error": "graph backend not configured"})
		return
	}

	if err := s.graphStore.Ping(r.Context()); err != nil {
		http.Error(w, "graph backend not connected: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Run sync in background.
	go func() {
		s.syncGraphFromRelations()
	}()

	writeJSON(w, map[string]any{"status": "sync started in background"})
}

// bulkDeleteRequest is the JSON body for DELETE /api/observations/bulk.
type bulkDeleteRequest struct {
	IDs []int64 `json:"ids"`
}

// handleBulkDeleteREST godoc
// @Summary Bulk delete observations
// @Description Deletes multiple observations by ID. Maximum 100 per request.
// @Tags Observations
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body bulkDeleteRequest true "Observation IDs to delete"
// @Success 200 {object} object
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Router /api/observations/bulk [delete]
func (s *Service) handleBulkDeleteREST(w http.ResponseWriter, r *http.Request) {
	if s.observationStore == nil {
		http.Error(w, "observation store not available", http.StatusServiceUnavailable)
		return
	}

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

	deleted, err := s.observationStore.DeleteObservations(r.Context(), req.IDs)
	if err != nil {
		log.Error().Err(err).Int("count", len(req.IDs)).Msg("bulk-delete: delete observations failed")
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"deleted": deleted,
	})
}

// bulkScopeChangeRequest is the JSON body for PATCH /api/observations/bulk-scope.
type bulkScopeChangeRequest struct {
	IDs   []int64 `json:"ids"`
	Scope string  `json:"scope"` // "global" or "project"
}

// handleBulkScopeChange godoc
// @Summary Bulk update observation scope
// @Description Changes the scope of multiple observations to either "global" or "project".
// @Tags Observations
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body bulkScopeChangeRequest true "IDs and new scope"
// @Success 200 {object} object
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Router /api/observations/bulk-scope [patch]
func (s *Service) handleBulkScopeChange(w http.ResponseWriter, r *http.Request) {
	if s.observationStore == nil {
		http.Error(w, "observation store not available", http.StatusServiceUnavailable)
		return
	}

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

	ctx := r.Context()
	var updated int64

	scope := req.Scope
	update := &gorm.ObservationUpdate{Scope: &scope}

	for _, id := range req.IDs {
		if _, err := s.observationStore.UpdateObservation(ctx, id, update); err != nil {
			log.Error().Err(err).Int64("id", id).Msg("bulk-scope: update observation failed")
			continue
		}
		updated++
	}

	writeJSON(w, map[string]any{
		"updated": updated,
	})
}

// handleGetSimilarityTelemetry godoc
// @Summary Get similarity telemetry
// @Description Returns the latest similarity telemetry data. Optionally filter by project to get a single snapshot.
// @Tags Analytics
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Success 200 {object} map[string]interface{}
// @Failure 500 {string} string "internal error"
// @Router /api/telemetry/similarity [get]
func (s *Service) handleMCPHitRateAnalytics(ctx context.Context, project string, limit int) (string, error) {
	if s.observationStore == nil {
		return "", nil
	}
	db := s.observationStore.GetDB().WithContext(ctx)
	type hitRateObs struct {
		ID    int64  `gorm:"column:id"`
		Title string `gorm:"column:title"`
		Type  string `gorm:"column:type"`
		Flag  string `gorm:"column:flag"`
	}
	var results []hitRateObs
	sql := `
		SELECT id, COALESCE(title, '') as title, type,
			CASE
				WHEN concepts::text LIKE '%noise_candidate%' THEN 'noise_candidate'
				WHEN concepts::text LIKE '%high_value%' THEN 'high_value'
			END as flag
		FROM observations
		WHERE (concepts::text LIKE '%noise_candidate%' OR concepts::text LIKE '%high_value%')
		AND status = 'active'`
	params := []any{}
	if project != "" {
		sql += " AND project = ?"
		params = append(params, project)
	}
	sql += " ORDER BY importance_score DESC LIMIT ?"
	params = append(params, limit)
	if err := db.Raw(sql, params...).Scan(&results).Error; err != nil {
		return "", fmt.Errorf("hit_rate query: %w", err)
	}
	if len(results) == 0 {
		return "No hit rate analytics data yet. Hit rate flags are computed during maintenance cycles and require 50+ injection_log entries.", nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Hit Rate Analytics (%d observations)\n\n", len(results)))
	noiseCount, starCount := 0, 0
	sb.WriteString("### Noise Candidates (injected 10+ times, never cited)\n")
	for _, r := range results {
		if r.Flag == "noise_candidate" {
			sb.WriteString(fmt.Sprintf("- [%d] %s (%s)\n", r.ID, r.Title, r.Type))
			noiseCount++
		}
	}
	if noiseCount == 0 {
		sb.WriteString("None found.\n")
	}
	sb.WriteString("\n### High Value (injected 5+ times, >50% citation rate)\n")
	for _, r := range results {
		if r.Flag == "high_value" {
			sb.WriteString(fmt.Sprintf("- [%d] %s (%s)\n", r.ID, r.Title, r.Type))
			starCount++
		}
	}
	if starCount == 0 {
		sb.WriteString("None found.\n")
	}
	return sb.String(), nil
}

func parseHitRateAnalyticsText(text string) ([]map[string]any, error) {
	observations := make([]map[string]any, 0)
	section := ""

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "### Noise Candidates"):
			section = "noise_candidate"
			continue
		case strings.HasPrefix(line, "### High Value"):
			section = "high_value"
			continue
		case !strings.HasPrefix(line, "- ["):
			continue
		}

		if section != "noise_candidate" && section != "high_value" {
			continue
		}

		tail := strings.TrimPrefix(line, "- ")
		opening := strings.Index(tail, "[")
		closing := strings.Index(tail, "]")
		if opening != 0 || closing <= opening+1 {
			continue
		}

		idText := strings.TrimSpace(tail[opening+1 : closing])
		id, err := strconv.Atoi(idText)
		if err != nil {
			continue
		}

		rest := tail[closing+1:]
		openParen := strings.LastIndex(rest, "(")
		closeParen := strings.LastIndex(rest, ")")
		if openParen == -1 || closeParen <= openParen {
			continue
		}

		title := strings.TrimSpace(rest[:openParen])
		typeVal := strings.TrimSpace(rest[openParen+1 : closeParen])
		if title == "" || typeVal == "" {
			continue
		}

		observations = append(observations, map[string]any{
			"id":    id,
			"title": title,
			"type":  typeVal,
			"flag":  section,
		})
	}

	return observations, nil
}

func (s *Service) handleGetHitRateAnalytics(w http.ResponseWriter, r *http.Request) {
	if s.observationStore == nil {
		writeJSON(w, map[string]any{"high_value": 0, "noise_candidates": 0, "observations": []any{}, "total": 0})
		return
	}

	text, err := s.handleMCPHitRateAnalytics(r.Context(), r.URL.Query().Get("project"), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	observations, parseErr := parseHitRateAnalyticsText(text)
	if parseErr != nil {
		http.Error(w, parseErr.Error(), http.StatusInternalServerError)
		return
	}

	noiseCount := 0
	valueCount := 0
	for _, obs := range observations {
		switch obs["flag"] {
		case "noise_candidate":
			noiseCount++
		case "high_value":
			valueCount++
		}
	}

	writeJSON(w, map[string]any{
		"high_value":      valueCount,
		"noise_candidates": noiseCount,
		"observations":     observations,
		"total":           len(observations),
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
