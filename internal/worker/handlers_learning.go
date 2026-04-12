// Package worker provides learning-related HTTP handlers.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	gormstorage "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/internal/scoring"
)

// handleGetEffectivenessDistribution godoc
// @Summary Get effectiveness distribution
// @Description Returns aggregated counts of observations grouped by effectiveness tier (high/medium/low/insufficient).
// Uses SQL aggregation server-side — does not page through observations.
// @Tags Learning
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} gormstorage.EffectivenessDistribution
// @Failure 503 {string} string "service not ready"
// @Failure 500 {string} string "internal error"
// @Router /api/learning/effectiveness-distribution [get]
func (s *Service) handleGetEffectivenessDistribution(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	obsStore := s.observationStore
	s.initMu.RUnlock()

	if obsStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	dist, err := obsStore.GetEffectivenessDistribution(r.Context())
	if err != nil {
		log.Warn().Err(err).Msg("Failed to query effectiveness distribution")
		http.Error(w, "failed to query effectiveness distribution: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, dist)
}

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
		if errors.Is(err, gormstorage.ErrSessionOutcomeConflict) {
			http.Error(w, "session outcome already recorded with a different value", http.StatusConflict)
			return
		}
		http.Error(w, "failed to update session outcome: "+err.Error(), http.StatusInternalServerError)
		return
	}

	canonicalSessionID, err := sessionStore.ResolveClaudeSessionID(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "failed to resolve canonical session id: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Count injected observations synchronously (count only, no blocking updates).
	s.initMu.RLock()
	injStore := s.injectionStore
	obsStore := s.observationStore
	s.initMu.RUnlock()

	var injectedCount int64
	if injStore != nil {
		injectedCount, _ = injStore.CountInjectionsBySession(r.Context(), canonicalSessionID)
	}

	// Launch outcome propagation asynchronously — updates utility and effectiveness scores.
	if injStore != nil && obsStore != nil {
		s.initMu.RLock()
		agentStatsStore := s.agentStatsStore
		s.initMu.RUnlock()

		capturedSessionID := canonicalSessionID
		capturedOutcome := learning.Outcome(req.Outcome)

		// Resolve agent_id from the session record so agent-specific stats can be updated.
		// The session's Project field stores the agent_id when no explicit project was provided.
		var capturedAgentID string
		if sess, err := sessionStore.FindAnySDKSession(r.Context(), canonicalSessionID); err == nil && sess != nil {
			capturedAgentID = sess.Project
		}

		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

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
		"session_id":            canonicalSessionID,
		"outcome":               req.Outcome,
		"observations_affected": injectedCount,
	})
}

func (s *Service) handlePropagateOutcome(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	if sessionID == "" {
		http.Error(w, "session id required", http.StatusBadRequest)
		return
	}

	s.initMu.RLock()
	sessionStore := s.sessionStore
	injStore := s.injectionStore
	obsStore := s.observationStore
	s.initMu.RUnlock()

	if sessionStore == nil || injStore == nil || obsStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	sess, err := sessionStore.FindAnySDKSession(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "failed to load session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	outcome := learning.Outcome(sess.Outcome.String)
	if !sess.Outcome.Valid || !learning.IsValidOutcome(outcome) {
		http.Error(w, "session outcome not recorded", http.StatusBadRequest)
		return
	}

	// Atomically claim the propagation slot. This is TOCTOU-free: the WHERE clause
	// ensures only one concurrent caller wins the slot; others see zero rows affected.
	claimed, err := sessionStore.UpdateUtilityPropagatedAtIfStale(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "failed to claim propagation slot: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !claimed {
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, map[string]interface{}{
			"error":   "rate_limited",
			"message": "propagation already triggered within the last 60 seconds",
		})
		return
	}

	capturedSessionID := sessionID
	capturedOutcome := outcome
	capturedSessionStore := sessionStore
	capturedInjStore := injStore
	capturedObsStore := obsStore
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := learning.PropagateOutcome(bgCtx, capturedInjStore, capturedObsStore, capturedSessionID, capturedOutcome); err != nil {
			log.Warn().Err(err).Str("session", capturedSessionID).Msg("manual outcome propagation failed")
			// Revert the timestamp claim so the next caller can retry.
			if clearErr := capturedSessionStore.ClearUtilityPropagatedAt(context.Background(), capturedSessionID); clearErr != nil {
				log.Warn().Err(clearErr).Str("session", capturedSessionID).Msg("failed to clear utility_propagated_at after propagation failure")
			}
		}
	}()

	// Return 202 Accepted: propagation is dispatched asynchronously.
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]interface{}{
		"session_id": sessionID,
		"status":     "accepted",
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

// handleGetLearningCurve godoc
// @Summary Get daily learning curve
// @Description Returns daily session outcome rates for the past N days.
// @Tags Learning
// @Produce json
// @Security ApiKeyAuth
// @Param days  query int    false "Number of days (default 30)"
// @Param project query string false "Filter by project"
// @Success 200 {object} map[string]interface{}
// @Failure 503 {string} string "service not ready"
// @Failure 500 {string} string "internal error"
// @Router /api/learning/curve [get]
func (s *Service) handleGetLearningCurve(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	sessionStore := s.sessionStore
	s.initMu.RUnlock()

	if sessionStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	daysStr := r.URL.Query().Get("days")
	days, _ := strconv.Atoi(daysStr)
	if days <= 0 {
		days = 30
	}

	project := r.URL.Query().Get("project")

	rows, err := sessionStore.GetLearningCurve(r.Context(), days, project)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to query learning curve")
		http.Error(w, "failed to query learning curve: "+err.Error(), http.StatusInternalServerError)
		return
	}

	type dataPoint struct {
		Date        string  `json:"date"`
		Sessions    int64   `json:"sessions"`
		Successes   int64   `json:"successes"`
		OutcomeRate float64 `json:"outcome_rate"`
	}

	points := make([]dataPoint, 0, len(rows))
	for _, row := range rows {
		points = append(points, dataPoint{
			Date:        row.Date,
			Sessions:    row.Sessions,
			Successes:   row.Successes,
			OutcomeRate: row.OutcomeRate,
		})
	}

	writeJSON(w, map[string]interface{}{
		"data_points": points,
	})
}

// handleAPORewrite godoc
// @Summary APO rewrite — generate an LLM-rewritten guidance observation
// @Description Generates a rewritten version of a guidance observation narrative using APO-lite.
// In dry_run mode, returns the proposed rewrite without storing it.
// When dry_run is false, stores the rewrite as a new ObservationVersion and activates it.
// @Tags Learning
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body object true "Body: {observation_id, dry_run}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Failure 404 {string} string "observation not found"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "service not ready"
// @Router /api/maintenance/apo/rewrite [post]
func (s *Service) handleAPORewrite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ObservationID int64 `json:"observation_id"`
		DryRun        bool  `json:"dry_run"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ObservationID <= 0 {
		http.Error(w, "observation_id is required", http.StatusBadRequest)
		return
	}

	s.initMu.RLock()
	obsStore := s.observationStore
	versionStore := s.versionStore
	llmClient := s.llmClient
	s.initMu.RUnlock()

	if obsStore == nil || llmClient == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	obs, err := obsStore.GetObservationByID(r.Context(), req.ObservationID)
	if err != nil {
		http.Error(w, "observation not found", http.StatusNotFound)
		return
	}

	if !obs.Narrative.Valid || obs.Narrative.String == "" {
		http.Error(w, "observation has no narrative to rewrite", http.StatusBadRequest)
		return
	}

	original := obs.Narrative.String
	effectivenessData := learning.APOEffectivenessData{
		Injections: obs.EffectivenessInjections,
		Successes:  obs.EffectivenessSuccesses,
	}

	rewritten, err := learning.RewriteGuidance(r.Context(), llmClient, original, effectivenessData)
	if err != nil {
		log.Warn().Err(err).Int64("observation_id", req.ObservationID).Msg("APO rewrite failed")
		http.Error(w, "rewrite failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if req.DryRun || versionStore == nil {
		writeJSON(w, map[string]interface{}{
			"observation_id": req.ObservationID,
			"original":       original,
			"rewrite":        rewritten,
			"applied":        false,
		})
		return
	}

	versionID, err := versionStore.CreateVersion(r.Context(), req.ObservationID, rewritten, gormstorage.VersionSourceAPORewrite)
	if err != nil {
		log.Warn().Err(err).Int64("observation_id", req.ObservationID).Msg("Failed to store APO rewrite version")
		http.Error(w, "failed to store rewrite: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"observation_id": req.ObservationID,
		"original":       original,
		"rewrite":        rewritten,
		"applied":        true,
		"version_id":     versionID,
	})
}

// handleGetSessionInjections returns all observations injected into a session
// with their effectiveness metrics — enables retrospective analysis of what was
// injected, how useful it was, and what was noise.
func (s *Service) handleGetSessionInjections(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	injStore := s.injectionStore
	s.initMu.RUnlock()

	if injStore == nil {
		http.Error(w, "service not ready", http.StatusServiceUnavailable)
		return
	}

	sessionID := chi.URLParam(r, "sessionId")
	if sessionID == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}

	details, err := injStore.GetSessionInjectionDetails(r.Context(), sessionID)
	if err != nil {
		log.Warn().Err(err).Str("session", sessionID).Msg("Failed to get session injection details")
		http.Error(w, "failed to query injections: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Compute summary metrics
	var totalInjections int
	var highEff, medEff, lowEff, noData int
	var sumEffectiveness float64
	for _, d := range details {
		totalInjections++
		if d.EffectivenessInj >= 10 {
			sumEffectiveness += d.EffectivenessScore
			if d.EffectivenessScore >= 0.7 {
				highEff++
			} else if d.EffectivenessScore >= 0.4 {
				medEff++
			} else {
				lowEff++
			}
		} else {
			noData++
		}
	}

	var avgEffectiveness float64
	evaluated := highEff + medEff + lowEff
	if evaluated > 0 {
		avgEffectiveness = sumEffectiveness / float64(evaluated)
	}

	// Group by section
	sections := map[string]int{}
	for _, d := range details {
		sections[d.InjectionSection]++
	}

	writeJSON(w, map[string]any{
		"session_id": sessionID,
		"injections": details,
		"total":      totalInjections,
		"sections":   sections,
		"summary": map[string]any{
			"high_effectiveness":   highEff,
			"medium_effectiveness": medEff,
			"low_effectiveness":    lowEff,
			"insufficient_data":    noData,
			"avg_effectiveness":    avgEffectiveness,
			"evaluated":            evaluated,
		},
	})
}
