package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

const memoryTriggersTimeout = 180 * time.Millisecond

// MemoryTriggerRequest is the request body for POST /api/memory/triggers.
type MemoryTriggerRequest struct {
	Tool      string         `json:"tool"`
	Params    map[string]any `json:"params"`
	Project   string         `json:"project"`
	SessionID string         `json:"session_id"`
}

// MemoryTriggerMatch is the response item shape for POST /api/memory/triggers.
type MemoryTriggerMatch struct {
	Kind          string `json:"kind"`
	ObservationID int64  `json:"observation_id"`
	Blurb         string `json:"blurb"`
}

// handleMemoryTriggers godoc
// @Summary Match memory triggers
// @Description Parses tool trigger input and returns matched memory trigger warnings.
// @Tags Context
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body MemoryTriggerRequest true "Trigger request body"
// @Success 200 {array} MemoryTriggerMatch
// @Failure 400 {string} string "bad request"
// @Failure 504 {string} string "trigger matching timed out"
// @Router /api/memory/triggers [post]
func (s *Service) handleMemoryTriggers(w http.ResponseWriter, r *http.Request) {
	var req MemoryTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Tool == "" {
		http.Error(w, "tool is required", http.StatusBadRequest)
		return
	}
	if req.Project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}
	if err := ValidateProjectName(req.Project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), memoryTriggersTimeout)
	defer cancel()

	matches, err := s.matchMemoryTriggers(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			writeJSON(w, make([]MemoryTriggerMatch, 0))
			return
		}
		http.Error(w, "trigger matching failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if ctx.Err() != nil {
		writeJSON(w, make([]MemoryTriggerMatch, 0))
		return
	}

	writeJSON(w, matches)
}
