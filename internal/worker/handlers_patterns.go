// Package worker provides the main worker service for engram.
package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/pkg/models"
)

// DefaultPatternsLimit is the default number of patterns to return.
const DefaultPatternsLimit = 500

// PatternsListResponse is the envelope returned by GET /api/patterns.
type PatternsListResponse struct {
	Patterns []*models.Pattern `json:"patterns"`
	Total    int64             `json:"total"`
}

// handleGetPatterns godoc
// @Summary List patterns
// @Description Returns active patterns with server-side pagination and sorting.
// @Tags Patterns
// @Produce json
// @Security ApiKeyAuth
// @Param type query string false "Filter by pattern type"
// @Param project query string false "Filter by project"
// @Param limit query int false "Number of results (default 500)"
// @Param offset query int false "Pagination offset (default 0)"
// @Param sort query string false "Sort order: frequency (default), confidence, last_seen"
// @Success 200 {object} PatternsListResponse
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "pattern store not initialized"
// @Router /api/patterns [get]
func (s *Service) handleGetPatterns(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.patternStore
	s.initMu.RUnlock()

	if store == nil {
		http.Error(w, "pattern store not initialized", http.StatusServiceUnavailable)
		return
	}

	// Parse query parameters
	limit := DefaultPatternsLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	sort := r.URL.Query().Get("sort") // "frequency", "confidence", "last_seen"
	patternType := r.URL.Query().Get("type")
	project := r.URL.Query().Get("project")

	var patterns []*models.Pattern
	var total int64
	var err error

	if patternType != "" {
		// Filter by type — returns up to limit results, no offset pagination.
		// Total reflects the returned set (type/project filters are not paginated).
		patterns, err = store.GetPatternsByType(r.Context(), models.PatternType(patternType), limit)
		if err == nil {
			total = int64(len(patterns))
		}
	} else if project != "" {
		// Filter by project — same: up to limit, no offset pagination.
		patterns, err = store.GetPatternsByProject(r.Context(), project, limit)
		if err == nil {
			total = int64(len(patterns))
		}
	} else {
		// Count total for pagination metadata
		total, err = store.CountActivePatterns(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		patterns, err = store.GetActivePatterns(r.Context(), limit, offset, sort)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, PatternsListResponse{
		Patterns: patterns,
		Total:    total,
	})
}

// handleGetPatternStats godoc
// @Summary Get pattern statistics
// @Description Returns aggregate statistics about patterns including counts by type and status.
// @Tags Patterns
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "pattern store not initialized"
// @Router /api/patterns/stats [get]
func (s *Service) handleGetPatternStats(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.patternStore
	s.initMu.RUnlock()

	if store == nil {
		http.Error(w, "pattern store not initialized", http.StatusServiceUnavailable)
		return
	}

	stats, err := store.GetPatternStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, stats)
}

// handleGetPatternByID godoc
// @Summary Get pattern by ID
// @Description Returns a single pattern by its ID.
// @Tags Patterns
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Pattern ID"
// @Success 200 {object} models.Pattern
// @Failure 400 {string} string "invalid pattern ID"
// @Failure 404 {string} string "pattern not found"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "pattern store not initialized"
// @Router /api/patterns/{id} [get]
func (s *Service) handleGetPatternByID(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.patternStore
	s.initMu.RUnlock()

	if store == nil {
		http.Error(w, "pattern store not initialized", http.StatusServiceUnavailable)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid pattern ID", http.StatusBadRequest)
		return
	}

	pattern, err := store.GetPatternByID(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if pattern == nil {
		http.Error(w, "pattern not found", http.StatusNotFound)
		return
	}

	writeJSON(w, pattern)
}

// handleGetPatternInsight godoc
// @Summary Get pattern insight
// @Description Returns a formatted insight string for a pattern, generated by the pattern detector.
// @Tags Patterns
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Pattern ID"
// @Success 200 {object} map[string]string
// @Failure 400 {string} string "invalid pattern ID"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "pattern detector not initialized"
// @Router /api/patterns/{id}/insight [get]
func (s *Service) handleGetPatternInsight(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	detector := s.patternDetector
	s.initMu.RUnlock()

	if detector == nil {
		http.Error(w, "pattern detector not initialized", http.StatusServiceUnavailable)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid pattern ID", http.StatusBadRequest)
		return
	}

	insight, err := detector.GetPatternInsight(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"insight": insight})
}

// handleDeletePattern godoc
// @Summary Delete pattern
// @Description Deletes a pattern by its ID.
// @Tags Patterns
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Pattern ID"
// @Success 200 {object} map[string]string
// @Failure 400 {string} string "invalid pattern ID"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "pattern store not initialized"
// @Router /api/patterns/{id} [delete]
func (s *Service) handleDeletePattern(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.patternStore
	s.initMu.RUnlock()

	if store == nil {
		http.Error(w, "pattern store not initialized", http.StatusServiceUnavailable)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid pattern ID", http.StatusBadRequest)
		return
	}

	if err := store.DeletePattern(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"status": "deleted"})
}

// handleDeprecatePattern godoc
// @Summary Deprecate pattern
// @Description Marks a pattern as deprecated.
// @Tags Patterns
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Pattern ID"
// @Success 200 {object} map[string]string
// @Failure 400 {string} string "invalid pattern ID"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "pattern store not initialized"
// @Router /api/patterns/{id}/deprecate [post]
func (s *Service) handleDeprecatePattern(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.patternStore
	s.initMu.RUnlock()

	if store == nil {
		http.Error(w, "pattern store not initialized", http.StatusServiceUnavailable)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid pattern ID", http.StatusBadRequest)
		return
	}

	if err := store.MarkPatternDeprecated(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"status": "deprecated"})
}

// MergePatternsRequest is the request body for merging patterns.
type MergePatternsRequest struct {
	SourceID int64 `json:"source_id"`
	TargetID int64 `json:"target_id"`
}

// handleSearchPatterns godoc
// @Summary Search patterns
// @Description Performs full-text search on patterns.
// @Tags Patterns
// @Produce json
// @Security ApiKeyAuth
// @Param q query string true "Search query"
// @Param limit query int false "Number of results (default 100)"
// @Success 200 {array} models.Pattern
// @Failure 400 {string} string "query parameter 'q' is required"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "pattern store not initialized"
// @Router /api/patterns/search [get]
func (s *Service) handleSearchPatterns(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.patternStore
	s.initMu.RUnlock()

	if store == nil {
		http.Error(w, "pattern store not initialized", http.StatusServiceUnavailable)
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "query parameter 'q' is required", http.StatusBadRequest)
		return
	}

	limit := DefaultPatternsLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	patterns, err := store.SearchPatternsFTS(r.Context(), query, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, patterns)
}

// handleGetPatternByName godoc
// @Summary Get pattern by name
// @Description Returns a pattern by its unique name.
// @Tags Patterns
// @Produce json
// @Security ApiKeyAuth
// @Param name query string true "Pattern name"
// @Success 200 {object} models.Pattern
// @Failure 400 {string} string "query parameter 'name' is required"
// @Failure 404 {string} string "pattern not found"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "pattern store not initialized"
// @Router /api/patterns/by-name [get]
func (s *Service) handleGetPatternByName(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.patternStore
	s.initMu.RUnlock()

	if store == nil {
		http.Error(w, "pattern store not initialized", http.StatusServiceUnavailable)
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "query parameter 'name' is required", http.StatusBadRequest)
		return
	}

	pattern, err := store.GetPatternByName(r.Context(), name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if pattern == nil {
		http.Error(w, "pattern not found", http.StatusNotFound)
		return
	}

	writeJSON(w, pattern)
}

// PatternObservationsResponse is the envelope for GET /api/patterns/{id}/observations.
type PatternObservationsResponse struct {
	Observations []*models.Observation `json:"observations"`
	Total        int                   `json:"total"`
}

// PatternInsightResponse is the envelope for POST /api/patterns/{id}/insight.
type PatternInsightResponse struct {
	Summary            string                `json:"summary"`
	SourceObservations []*models.Observation `json:"source_observations"`
	Cached             bool                  `json:"cached"`
}

// handleGetPatternObservations godoc
// @Summary Get source observations for a pattern
// @Description Returns the observations that contributed to this pattern.
// @Tags Patterns
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Pattern ID"
// @Success 200 {object} PatternObservationsResponse
// @Failure 400 {string} string "invalid pattern ID"
// @Failure 404 {string} string "pattern not found"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "stores not initialized"
// @Router /api/patterns/{id}/observations [get]
func (s *Service) handleGetPatternObservations(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	patStore := s.patternStore
	obsStore := s.observationStore
	s.initMu.RUnlock()

	if patStore == nil || obsStore == nil {
		http.Error(w, "stores not initialized", http.StatusServiceUnavailable)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid pattern ID", http.StatusBadRequest)
		return
	}

	pattern, err := patStore.GetPatternByID(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if pattern == nil {
		http.Error(w, "pattern not found", http.StatusNotFound)
		return
	}

	ids := []int64(pattern.ObservationIDs)
	var observations []*models.Observation
	if len(ids) > 0 {
		observations, err = obsStore.GetObservationsByIDs(r.Context(), ids, "date_desc", 0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if observations == nil {
		observations = []*models.Observation{}
	}

	writeJSON(w, PatternObservationsResponse{
		Observations: observations,
		Total:        len(observations),
	})
}

// handlePostPatternInsight godoc
// @Summary Generate or retrieve LLM-based insight for a pattern
// @Description Generates a 2-3 sentence LLM summary from source observations.
// Returns cached description when it is already a non-generic summary.
// @Tags Patterns
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Pattern ID"
// @Success 200 {object} PatternInsightResponse
// @Failure 400 {string} string "invalid pattern ID"
// @Failure 404 {string} string "pattern not found"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "stores not initialized"
// @Router /api/patterns/{id}/insight [post]
func (s *Service) handlePostPatternInsight(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	patStore := s.patternStore
	obsStore := s.observationStore
	llm := s.llmClient
	s.initMu.RUnlock()

	if patStore == nil || obsStore == nil {
		http.Error(w, "stores not initialized", http.StatusServiceUnavailable)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid pattern ID", http.StatusBadRequest)
		return
	}

	pattern, err := patStore.GetPatternByID(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if pattern == nil {
		http.Error(w, "pattern not found", http.StatusNotFound)
		return
	}

	// Return cached summary when it is already a real (non-generic) description.
	if pattern.Description.Valid && !learning.IsGenericDescription(pattern.Description.String) {
		ids := []int64(pattern.ObservationIDs)
		var observations []*models.Observation
		if len(ids) > 0 {
			observations, _ = obsStore.GetObservationsByIDs(r.Context(), ids, "date_desc", 0)
		}
		if observations == nil {
			observations = []*models.Observation{}
		}
		writeJSON(w, PatternInsightResponse{
			Summary:            pattern.Description.String,
			SourceObservations: observations,
			Cached:             true,
		})
		return
	}

	// Fetch source observations for LLM input.
	ids := []int64(pattern.ObservationIDs)
	var observations []*models.Observation
	if len(ids) > 0 {
		observations, err = obsStore.GetObservationsByIDs(r.Context(), ids, "date_desc", 0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if observations == nil {
		observations = []*models.Observation{}
	}

	// Generate summary — failures are non-fatal: return source observations with empty summary.
	// 120s timeout: Ollama cold start for 9B models takes 30-60s (model loading from disk).
	var summary string
	var llmErr error
	if llm != nil {
		insightCtx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()
		summary, llmErr = learning.GeneratePatternInsight(insightCtx, llm, observations)
	}
	if llmErr == nil && summary != "" {
		// Persist generated summary so subsequent calls return cached.
		updated := *pattern
		updated.Description = sql.NullString{String: summary, Valid: true}
		_ = patStore.UpdatePattern(r.Context(), &updated)
	}

	writeJSON(w, PatternInsightResponse{
		Summary:            summary,
		SourceObservations: observations,
		Cached:             false,
	})
}

// PatternCleanupRequest is the JSON body for POST /api/maintenance/patterns/cleanup.
type PatternCleanupRequest struct {
	ConfidenceThreshold float64 `json:"confidence_threshold"`
	DryRun              bool    `json:"dry_run"`
}

// PatternCleanupResponse is returned by POST /api/maintenance/patterns/cleanup.
type PatternCleanupResponse struct {
	OrphansFound           int `json:"orphans_found"`
	OrphansArchived        int `json:"orphans_archived"`
	LowConfidenceFound     int `json:"low_confidence_found"`
	LowConfidenceArchived  int `json:"low_confidence_archived"`
	ConfidenceRecalculated int `json:"confidence_recalculated"`
}

// handlePatternCleanupAdvanced godoc
// @Summary Advanced pattern cleanup
// @Description Detects orphan patterns, recalculates confidence, and archives low-confidence patterns.
// @Tags Patterns
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body PatternCleanupRequest false "Cleanup options"
// @Success 200 {object} PatternCleanupResponse
// @Failure 400 {string} string "bad request"
// @Failure 503 {string} string "stores not initialized"
// @Failure 500 {string} string "internal error"
// @Router /api/maintenance/patterns/cleanup [post]
func (s *Service) handlePatternCleanupAdvanced(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.patternStore
	detector := s.patternDetector
	maintSvc := s.maintenanceService
	s.initMu.RUnlock()

	if store == nil {
		http.Error(w, "pattern store not initialized", http.StatusServiceUnavailable)
		return
	}

	var req PatternCleanupRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	// Default confidence threshold.
	if req.ConfidenceThreshold == 0 {
		req.ConfidenceThreshold = 0.6
	}

	resp := PatternCleanupResponse{}

	// Step 1: Orphan detection (and pruning if not dry run).
	if maintSvc != nil {
		orphanResult, err := maintSvc.CleanupOrphanPatterns(r.Context(), req.DryRun)
		if err != nil {
			http.Error(w, "orphan cleanup failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		resp.OrphansFound = orphanResult.OrphansFound
		resp.OrphansArchived = orphanResult.OrphansArchived
	}

	// Step 2: Batch confidence recalculation (skip in dry-run — no mutations).
	if !req.DryRun && detector != nil {
		recalculated, err := detector.BatchRecalculateConfidence(r.Context())
		if err != nil {
			http.Error(w, "confidence recalculation failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		resp.ConfidenceRecalculated = recalculated
	}

	// Step 3: Low-confidence pattern detection (and archiving if not dry run).
	const maxPatterns = 10000
	patterns, err := store.GetActivePatterns(r.Context(), maxPatterns, 0, "")
	if err != nil {
		http.Error(w, "fetch active patterns failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	for _, p := range patterns {
		if p.Confidence < req.ConfidenceThreshold {
			resp.LowConfidenceFound++
			if !req.DryRun {
				if err := store.MarkPatternDeprecated(r.Context(), p.ID); err == nil {
					resp.LowConfidenceArchived++
				}
			}
		}
	}

	writeJSON(w, resp)
}

// handleMergePatterns godoc
// @Summary Merge patterns
// @Description Merges a source pattern into a target pattern. Source pattern is consumed.
// @Tags Patterns
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body MergePatternsRequest true "Source and target pattern IDs"
// @Success 200 {object} map[string]string
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "pattern store not initialized"
// @Router /api/patterns/merge [post]
func (s *Service) handleMergePatterns(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.patternStore
	s.initMu.RUnlock()

	if store == nil {
		http.Error(w, "pattern store not initialized", http.StatusServiceUnavailable)
		return
	}

	var req MergePatternsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.SourceID == 0 || req.TargetID == 0 {
		http.Error(w, "source_id and target_id are required", http.StatusBadRequest)
		return
	}

	if req.SourceID == req.TargetID {
		http.Error(w, "source_id and target_id cannot be the same", http.StatusBadRequest)
		return
	}

	if err := store.MergePatterns(r.Context(), req.SourceID, req.TargetID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"status": "merged"})
}
