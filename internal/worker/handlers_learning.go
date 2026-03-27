// Package worker provides learning-related HTTP handlers.
package worker

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/internal/learning"
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

	writeJSON(w, map[string]interface{}{
		"session_id":            sessionID,
		"outcome":               req.Outcome,
		"observations_affected": 0, // Propagation is Phase 2
	})
}
