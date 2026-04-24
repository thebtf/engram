// Package worker provides the main worker service for engram.
package worker

import (
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog/log"
)

// IngestRequest is the request body for the event ingest endpoint.
type IngestRequest struct {
	ToolInput     any    `json:"tool_input"`
	ToolResult    any    `json:"tool_result"`
	SessionID     string `json:"session_id"`
	Project       string `json:"project"`
	ToolName      string `json:"tool_name"`
	WorkstationID string `json:"workstation_id"`
}

// handleIngestEvent godoc
// @Summary Ingest tool event
// @Description Receives raw tool events from Claude Code hooks. Stores in raw_events, runs deterministic Level 0 pipeline, creates an observation, and triggers async embedding.
// @Tags Events
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body IngestRequest true "Tool event data"
// @Success 202 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Router /api/events/ingest [post]
func (s *Service) handleIngestEvent(w http.ResponseWriter, r *http.Request) {
	log.Info().Msg("/api/events/ingest removed in v5; rejecting ingest request without reading payload")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":         "removed_in_v5",
		"error":          "event ingest endpoint was removed in v5",
		"tool_name":      "",
		"session_id":     "",
		"project":        "",
		"workstation_id": "",
	})
}

