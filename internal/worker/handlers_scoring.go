// Package worker provides the main worker service for engram.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	"github.com/rs/zerolog/log"
)

// FeedbackRequest represents a user feedback submission.
type FeedbackRequest struct {
	Feedback int `json:"feedback"` // -1 (thumbs down), 0 (neutral), 1 (thumbs up)
}

// handleObservationFeedback godoc
// @Summary Submit observation feedback
// @Description Records user feedback (thumbs up/down) for an observation and recalculates its importance score.
// @Tags Scoring
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Param body body FeedbackRequest true "Feedback value (-1, 0, or 1)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/observations/{id}/feedback [post]
func (s *Service) handleObservationFeedback(w http.ResponseWriter, r *http.Request) {
	// Parse observation ID
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid observation id", http.StatusBadRequest)
		return
	}

	// Parse request body
	var req FeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate feedback value
	if req.Feedback < -1 || req.Feedback > 1 {
		http.Error(w, "feedback must be -1, 0, or 1", http.StatusBadRequest)
		return
	}

	// Get required components
	s.initMu.RLock()
	observationStore := s.observationStore
	scoreCalculator := s.scoreCalculator
	s.initMu.RUnlock()

	if observationStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	// Update feedback in database
	if err := observationStore.UpdateObservationFeedback(r.Context(), id, req.Feedback); err != nil {
		http.Error(w, "failed to update feedback", http.StatusInternalServerError)
		return
	}

	// Recalculate score immediately if calculator is available
	var newScore float64
	if scoreCalculator != nil {
		obs, err := observationStore.GetObservationByID(r.Context(), id)
		if err == nil && obs != nil {
			obs.UserFeedback = req.Feedback // Apply the new feedback
			newScore = scoreCalculator.Calculate(obs, time.Now())
			if err := observationStore.UpdateImportanceScore(r.Context(), id, newScore); err != nil {
				// Log but don't fail - feedback was recorded
				// Score will be updated on next recalculation cycle
				_ = err // Explicitly ignore - non-critical operation
			}
		}
	}

	// Broadcast update via SSE
	s.sseBroadcaster.Broadcast(map[string]interface{}{
		"type":     "observation_feedback",
		"id":       id,
		"feedback": req.Feedback,
		"score":    newScore,
	})

	writeJSON(w, map[string]interface{}{
		"status":   "ok",
		"id":       id,
		"feedback": req.Feedback,
		"score":    newScore,
	})
}

// handleMarkInjected godoc
// @Summary Mark observations as injected
// @Description Records that observations were injected into Claude Code context by incrementing injection counts.
// @Tags Scoring
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body object true "IDs to mark: {ids: [1,2,3]}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "invalid request body"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/observations/mark-injected [post]
func (s *Service) handleMarkInjected(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		writeJSON(w, map[string]any{"status": "ok", "count": 0})
		return
	}

	s.initMu.RLock()
	observationStore := s.observationStore
	s.initMu.RUnlock()

	if observationStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	if err := observationStore.IncrementInjectionCounts(r.Context(), req.IDs); err != nil {
		http.Error(w, "failed to mark injected", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{"status": "ok", "count": len(req.IDs)})
}

// UtilityRequest represents a utility signal for an observation.
type UtilityRequest struct {
	Signal string `json:"signal"` // "used", "corrected", "ignored"
}

// handleObservationUtility godoc
// @Summary Record utility signal
// @Description Records a utility signal (used, corrected, ignored) for an observation, updating its utility score via EMA.
// @Tags Scoring
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Param body body UtilityRequest true "Utility signal"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/observations/{id}/utility [post]
func (s *Service) handleObservationUtility(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid observation id", http.StatusBadRequest)
		return
	}

	var req UtilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Map signal to numeric value
	var signal float64
	switch req.Signal {
	case "used":
		signal = 1.0
	case "corrected":
		signal = 0.0
	case "ignored":
		signal = 0.3 // Slightly negative — not as bad as correction
	default:
		http.Error(w, "signal must be 'used', 'corrected', or 'ignored'", http.StatusBadRequest)
		return
	}

	s.initMu.RLock()
	observationStore := s.observationStore
	s.initMu.RUnlock()

	if observationStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	// EMA with alpha=0.1, max delta=0.05 per session
	if err := observationStore.UpdateUtilityScore(r.Context(), id, signal, 0.1, 0.05); err != nil {
		http.Error(w, "failed to update utility", http.StatusInternalServerError)
		return
	}

	// Adaptive per-project threshold adjustment was removed in US4 (v5 cleanup) —
	// project_settings table dropped; callers use a global default threshold only.

	writeJSON(w, map[string]any{
		"status": "ok",
		"id":     id,
		"signal": req.Signal,
	})
}

// handleGetScoringStats godoc
// @Summary Get scoring statistics
// @Description Returns scoring statistics including feedback distribution and recalculator stats.
// @Tags Scoring
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Success 200 {object} map[string]interface{}
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/scoring/stats [get]
func (s *Service) handleGetScoringStats(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")

	s.initMu.RLock()
	observationStore := s.observationStore
	recalculator := s.recalculator
	s.initMu.RUnlock()

	if observationStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	// Get feedback statistics
	feedbackStats, err := observationStore.GetObservationFeedbackStats(r.Context(), project)
	if err != nil {
		http.Error(w, "failed to get feedback stats", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"feedback": feedbackStats,
	}

	// Add recalculator stats if available
	if recalculator != nil {
		response["recalculator"] = recalculator.GetStats()
	}

	writeJSON(w, response)
}

// handleGetTopObservations godoc
// @Summary Get top-scoring observations
// @Description Returns the highest-scoring observations by importance score.
// @Tags Scoring
// @Produce json
// @Security ApiKeyAuth
// @Param limit query int false "Number of results (default 10)"
// @Param project query string false "Filter by project"
// @Success 200 {array} models.Observation
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/observations/top [get]
func (s *Service) handleGetTopObservations(w http.ResponseWriter, r *http.Request) {
	limit := parseIntParam(r, "limit", 10)
	project := r.URL.Query().Get("project")

	s.initMu.RLock()
	observationStore := s.observationStore
	s.initMu.RUnlock()

	if observationStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	observations, err := observationStore.GetTopScoringObservations(r.Context(), project, limit)
	if err != nil {
		http.Error(w, "failed to get top observations", http.StatusInternalServerError)
		return
	}

	if observations == nil {
		observations = []*models.Observation{}
	}

	writeJSON(w, observations)
}

// handleGetMostRetrieved godoc
// @Summary Get most retrieved observations
// @Description Returns the most frequently retrieved observations by retrieval count.
// @Tags Scoring
// @Produce json
// @Security ApiKeyAuth
// @Param limit query int false "Number of results (default 10)"
// @Param project query string false "Filter by project"
// @Success 200 {array} models.Observation
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/observations/most-retrieved [get]
func (s *Service) handleGetMostRetrieved(w http.ResponseWriter, r *http.Request) {
	limit := parseIntParam(r, "limit", 10)
	project := r.URL.Query().Get("project")

	s.initMu.RLock()
	observationStore := s.observationStore
	s.initMu.RUnlock()

	if observationStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	observations, err := observationStore.GetMostRetrievedObservations(r.Context(), project, limit)
	if err != nil {
		http.Error(w, "failed to get most retrieved observations", http.StatusInternalServerError)
		return
	}

	if observations == nil {
		observations = []*models.Observation{}
	}

	writeJSON(w, observations)
}

// handleExplainScore godoc
// @Summary Explain observation score
// @Description Returns a breakdown of how an observation's importance score was calculated, including all scoring components.
// @Tags Scoring
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "invalid observation id"
// @Failure 404 {string} string "observation not found"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/observations/{id}/score [get]
func (s *Service) handleExplainScore(w http.ResponseWriter, r *http.Request) {
	// Parse observation ID
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid observation id", http.StatusBadRequest)
		return
	}

	s.initMu.RLock()
	observationStore := s.observationStore
	scoreCalculator := s.scoreCalculator
	s.initMu.RUnlock()

	if observationStore == nil || scoreCalculator == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	// Get observation
	obs, err := observationStore.GetObservationByID(r.Context(), id)
	if err != nil {
		http.Error(w, "failed to get observation", http.StatusInternalServerError)
		return
	}
	if obs == nil {
		http.Error(w, "observation not found", http.StatusNotFound)
		return
	}

	// Calculate score components
	components := scoreCalculator.CalculateComponents(obs, time.Now())

	// Build response matching frontend ScoreBreakdown.vue expectations
	title := ""
	if obs.Title.Valid {
		title = obs.Title.String
	}

	writeJSON(w, map[string]interface{}{
		"id":         id,
		"observation": map[string]interface{}{
			"title": title,
			"type":  string(obs.Type),
		},
		"scoring": map[string]interface{}{
			"final_score":      components.FinalScore,
			"type_weight":      components.TypeWeight,
			"recency_decay":    components.RecencyDecay,
			"core_score":       components.CoreScore,
			"feedback_contrib": components.FeedbackContrib,
			"concept_contrib":  components.ConceptContrib,
			"retrieval_contrib": components.RetrievalContrib,
			"age_days":         components.AgeDays,
		},
		"explanation": map[string]interface{}{
			"type_impact":      fmt.Sprintf("Type '%s' has base weight %.2f", obs.Type, components.TypeWeight),
			"recency_impact":   fmt.Sprintf("%.1f days old, decay factor %.2f", components.AgeDays, components.RecencyDecay),
			"feedback_impact":  fmt.Sprintf("User feedback %+d, contribution %+.3f", obs.UserFeedback, components.FeedbackContrib),
			"concept_impact":   fmt.Sprintf("Concept boost %+.3f from %d concepts", components.ConceptContrib, len(obs.Concepts)),
			"retrieval_impact": fmt.Sprintf("Retrieved %d times, contribution %+.3f", obs.RetrievalCount, components.RetrievalContrib),
		},
		"components": components,
		"config":     scoreCalculator.GetConfig(),
	})
}

// handleUpdateConceptWeight godoc
// @Summary Update concept weight
// @Description Updates the weight for a specific concept used in scoring calculations.
// @Tags Scoring
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param concept path string true "Concept name"
// @Param body body object true "Weight: {weight: 0.5}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/scoring/concepts/{concept} [put]
func (s *Service) handleUpdateConceptWeight(w http.ResponseWriter, r *http.Request) {
	concept := chi.URLParam(r, "concept")
	if concept == "" {
		http.Error(w, "concept is required", http.StatusBadRequest)
		return
	}

	var req struct {
		Weight float64 `json:"weight"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate weight
	if req.Weight < 0 || req.Weight > 1 {
		http.Error(w, "weight must be between 0 and 1", http.StatusBadRequest)
		return
	}

	s.initMu.RLock()
	observationStore := s.observationStore
	recalculator := s.recalculator
	s.initMu.RUnlock()

	if observationStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	// Update in database
	if err := observationStore.UpdateConceptWeight(r.Context(), concept, req.Weight); err != nil {
		http.Error(w, "failed to update concept weight", http.StatusInternalServerError)
		return
	}

	// Refresh concept weights in recalculator
	if recalculator != nil {
		if err := recalculator.RefreshConceptWeights(r.Context()); err != nil {
			// Log but don't fail - weight was saved
			_ = err // Explicitly ignore - non-critical operation
		}
	}

	writeJSON(w, map[string]interface{}{
		"status":  "ok",
		"concept": concept,
		"weight":  req.Weight,
	})
}

// handleGetConceptWeights godoc
// @Summary Get concept weights
// @Description Returns all concept weights used in scoring calculations.
// @Tags Scoring
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/scoring/concepts [get]
func (s *Service) handleGetConceptWeights(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	observationStore := s.observationStore
	s.initMu.RUnlock()

	if observationStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	weights, err := observationStore.GetConceptWeights(r.Context())
	if err != nil {
		http.Error(w, "failed to get concept weights", http.StatusInternalServerError)
		return
	}

	writeJSON(w, weights)
}

// handleGetRecentlyInjected godoc
// @Summary Get recently injected observations
// @Description Returns observations that have been recently injected into Claude Code context.
// @Tags Scoring
// @Produce json
// @Security ApiKeyAuth
// @Param limit query int false "Number of results (default 50)"
// @Param project query string false "Filter by project"
// @Success 200 {object} map[string]interface{}
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/observations/recently-injected [get]
func (s *Service) handleGetRecentlyInjected(w http.ResponseWriter, r *http.Request) {
	limit := parseIntParam(r, "limit", 50)
	project := r.URL.Query().Get("project")

	s.initMu.RLock()
	observationStore := s.observationStore
	s.initMu.RUnlock()

	if observationStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	observations, err := observationStore.GetRecentlyInjectedObservations(r.Context(), project, limit)
	if err != nil {
		http.Error(w, "failed to get recently injected observations", http.StatusInternalServerError)
		return
	}

	if observations == nil {
		observations = []*models.Observation{}
	}

	writeJSON(w, map[string]interface{}{
		"observations": observations,
	})
}

// handleTriggerRecalculation godoc
// @Summary Trigger score recalculation
// @Description Triggers an immediate background recalculation of all observation importance scores.
// @Tags Scoring
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]string
// @Failure 503 {string} string "recalculator not available"
// @Router /api/scoring/recalculate [post]
func (s *Service) handleTriggerRecalculation(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	recalculator := s.recalculator
	s.initMu.RUnlock()

	if recalculator == nil {
		http.Error(w, "recalculator not available", http.StatusServiceUnavailable)
		return
	}

	// Run recalculation in background
	go func() {
		if err := recalculator.RecalculateNow(r.Context()); err != nil {
			log.Warn().Err(err).Msg("Background score recalculation failed")
		}
	}()

	writeJSON(w, map[string]string{"status": "recalculation triggered"})
}

// parseIntParam parses an integer query parameter with a default value.
func parseIntParam(r *http.Request, name string, defaultVal int) int {
	if val := r.URL.Query().Get(name); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultVal
}

// handleSessionMarkInjected godoc
// @Summary Mark observations injected for session
// @Description Records which observations were injected into a specific session. Dual-writes to per-session table and global injection_count counter.
// @Tags Scoring
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param sessionId path int true "Session database ID"
// @Param body body object true "IDs to mark: {ids: [1,2,3]}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/sessions/{sessionId}/mark-injected [post]
func (s *Service) handleSessionMarkInjected(w http.ResponseWriter, r *http.Request) {
	sessionIdStr := chi.URLParam(r, "sessionId")
	sessionID, err := strconv.ParseInt(sessionIdStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}

	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		writeJSON(w, map[string]any{"status": "ok", "count": 0})
		return
	}

	s.initMu.RLock()
	observationStore := s.observationStore
	s.initMu.RUnlock()

	if observationStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	if err := observationStore.IncrementInjectionCounts(r.Context(), req.IDs); err != nil {
		http.Error(w, "failed to increment injection counts", http.StatusInternalServerError)
		return
	}

	// Write injection records to the junction table asynchronously (closed-loop learning Phase 1).
	// Fire-and-forget: resolve the Claude session ID then batch-insert junction rows.
	s.initMu.RLock()
	injStore := s.injectionStore
	sessStore := s.sessionStore
	s.initMu.RUnlock()
	if injStore != nil && sessStore != nil {
		capturedDBSessionID := sessionID
		capturedObsIDs := append([]int64(nil), req.IDs...)
		go func() {
			sess, err := sessStore.GetSessionByID(context.Background(), capturedDBSessionID)
			if err != nil || sess == nil {
				return
			}
			claudeSessionID := sess.ClaudeSessionID
			records := make([]gorm.InjectionRecord, 0, len(capturedObsIDs))
			for _, obsID := range capturedObsIDs {
				records = append(records, gorm.InjectionRecord{
					ObservationID:    obsID,
					SessionID:        claudeSessionID,
					InjectionSection: "mark_injected",
				})
			}
			_ = injStore.RecordInjections(context.Background(), records)
		}()
	}

	writeJSON(w, map[string]any{"status": "ok", "count": len(req.IDs)})
}

// incrementRetrievalCounts increments retrieval counts for observations.
// Called after search results are returned to track popularity.
func (s *Service) incrementRetrievalCounts(ids []int64) {
	if len(ids) == 0 {
		return
	}

	s.initMu.RLock()
	store := s.observationStore
	s.initMu.RUnlock()

	if store == nil {
		return
	}

	// Increment in background to not block response
	// Use service context to respect shutdown signals
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
		defer cancel()

		if err := store.IncrementRetrievalCount(ctx, ids); err != nil {
			// Log but don't fail - this is a background operation
			if s.ctx.Err() == nil { // Don't log during shutdown
				log.Debug().Err(err).Msg("Failed to increment retrieval counts")
			}
		}
	}()
}

