// Package worker provides learning-related HTTP handlers.
package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/internal/scoring"
)

// handleSetSessionOutcome godoc
// @Summary Set session outcome
// @Description Records the outcome of a session (success/partial/failure/abandoned).
// @Tags Learning
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param sessionId path string true "Claude session ID"
// @Param body body object true "Outcome: {outcome, reason}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Failure 404 {string} string "session not found"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/sessions/{sessionId}/outcome [post]
func (s *Service) handleSetSessionOutcome(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	if sessionID == "" {
		http.Error(w, "session id required", http.StatusBadRequest)
		return
	}

	var req struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if !learning.IsValidOutcome(learning.Outcome(req.Outcome)) {
		http.Error(w, "outcome must be one of: success, partial, failure, abandoned", http.StatusBadRequest)
		return
	}

	s.initMu.RLock()
	sessionStore := s.sessionStore
	s.initMu.RUnlock()

	if sessionStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	if err := sessionStore.UpdateSessionOutcome(r.Context(), sessionID, req.Outcome, req.Reason); err != nil {
		http.Error(w, "failed to update session outcome: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Count injected observations synchronously (count only, no blocking updates).
	s.initMu.RLock()
	injStore := s.injectionStore
	obsStore := s.observationStore
	s.initMu.RUnlock()

	var injectedCount int64
	if injStore != nil {
		injectedCount, _ = injStore.CountInjectionsBySession(r.Context(), sessionID)
	}

	// Launch outcome propagation asynchronously — updates utility and effectiveness scores.
	if injStore != nil && obsStore != nil {
		s.initMu.RLock()
		agentStatsStore := s.agentStatsStore
		s.initMu.RUnlock()

		capturedSessionID := sessionID
		capturedOutcome := learning.Outcome(req.Outcome)

		// Resolve agent_id from the session record so agent-specific stats can be updated.
		// The session's Project field stores the agent_id when no explicit project was provided.
		var capturedAgentID string
		if sessionStore != nil {
			if sess, err := sessionStore.FindAnySDKSession(r.Context(), sessionID); err == nil && sess != nil {
				capturedAgentID = sess.Project
			}
		}

		go func() {
			bgCtx := context.Background()

			// Propagate global effectiveness scores.
			if _, err := learning.PropagateOutcome(bgCtx, injStore, obsStore, capturedSessionID, capturedOutcome); err != nil {
				log.Warn().Err(err).Str("session", capturedSessionID).Msg("outcome propagation failed")
			}

			// Propagate agent-specific stats when agent_id is available.
			if agentStatsStore != nil && capturedAgentID != "" {
				if _, err := learning.PropagateAgentStats(bgCtx, injStore, agentStatsStore, capturedSessionID, capturedAgentID, capturedOutcome); err != nil {
					log.Warn().Err(err).Str("session", capturedSessionID).Str("agent_id", capturedAgentID).Msg("agent stats propagation failed")
				}
			}
		}()
	}

	writeJSON(w, map[string]interface{}{
		"session_id":            sessionID,
		"outcome":               req.Outcome,
		"observations_affected": injectedCount,
	})
}

// handleGetEffectiveness godoc
// @Summary Get observation effectiveness
// @Description Returns injection effectiveness stats for an observation. When agent_id is provided,
// returns agent-specific stats if available; otherwise returns global stats.
// @Tags Learning
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Param agent_id query string false "Agent ID — returns agent-specific effectiveness when provided"
// @Success 200 {object} scoring.EffectivenessResult
// @Failure 400 {string} string "invalid id"
// @Failure 404 {string} string "observation not found"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/observations/{id}/effectiveness [get]
func (s *Service) handleGetEffectiveness(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid observation id", http.StatusBadRequest)
		return
	}

	agentID := r.URL.Query().Get("agent_id")

	s.initMu.RLock()
	obsStore := s.observationStore
	agentStatsStore := s.agentStatsStore
	s.initMu.RUnlock()

	if obsStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	// When agent_id is provided, return agent-specific effectiveness if a record exists.
	if agentID != "" && agentStatsStore != nil {
		stat, err := agentStatsStore.GetAgentEffectiveness(r.Context(), agentID, id)
		if err != nil {
			log.Warn().Err(err).Str("agent_id", agentID).Int64("observation_id", id).Msg("Failed to fetch agent effectiveness")
			http.Error(w, "failed to fetch agent effectiveness: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if stat != nil {
			result := scoring.ComputeEffectiveness(id, stat.Injections, stat.Successes)
			writeJSON(w, result)
			return
		}
		// No agent-specific record — fall through to global stats below.
	}

	obs, err := obsStore.GetObservationByID(r.Context(), id)
	if err != nil {
		http.Error(w, "observation not found", http.StatusNotFound)
		return
	}

	result := scoring.ComputeEffectiveness(obs.ID, obs.EffectivenessInjections, obs.EffectivenessSuccesses)
	writeJSON(w, result)
}

// handleGetStrategies godoc
// @Summary Get injection strategy comparison
// @Description Returns A/B testing stats for each injection strategy (session count, successes, outcome rate).
// @Tags Learning
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Failure 503 {string} string "service not ready"
// @Failure 500 {string} string "internal error"
// @Router /api/learning/strategies [get]
func (s *Service) handleGetStrategies(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	sessionStore := s.sessionStore
	s.initMu.RUnlock()

	if sessionStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	type strategyRow struct {
		Name        string  `json:"name"`
		Sessions    int64   `json:"sessions"`
		Successes   int64   `json:"successes"`
		OutcomeRate float64 `json:"outcome_rate"`
	}

	rows, err := sessionStore.GetStrategyStats(r.Context())
	if err != nil {
		log.Warn().Err(err).Msg("Failed to query strategy stats")
		http.Error(w, "failed to query strategy stats: "+err.Error(), http.StatusInternalServerError)
		return
	}

	result := make([]strategyRow, 0, len(rows))
	for _, row := range rows {
		var rate float64
		if row.Sessions > 0 {
			rate = float64(row.Successes) / float64(row.Sessions)
		}
		result = append(result, strategyRow{
			Name:        row.Strategy,
			Sessions:    row.Sessions,
			Successes:   row.Successes,
			OutcomeRate: rate,
		})
	}

	writeJSON(w, map[string]interface{}{
		"strategies": result,
	})
}

