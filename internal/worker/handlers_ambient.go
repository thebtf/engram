package worker

import (
	"encoding/json"
	"net/http"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/worker/ambientcore"
	"github.com/thebtf/engram/pkg/cognitive"
)

type ambientHookRequest struct {
	SessionID  string `json:"session_id"`
	Project    string `json:"project"`
	PromptText string `json:"prompt_text"`
	Limit      int    `json:"limit,omitempty"`
}

type ambientHookResponse struct {
	Hints             []cognitive.HintProposal `json:"hints,omitempty"`
	AdditionalContext string                   `json:"additional_context,omitempty"`
	Disabled          bool                     `json:"disabled,omitempty"`
	Reason            string                   `json:"reason,omitempty"`
}

func emptyAmbientHookResponse(disabled bool, reason string) ambientHookResponse {
	return ambientHookResponse{Disabled: disabled, Reason: reason}
}

func (s *Service) handleAmbientCandidates(w http.ResponseWriter, r *http.Request) {
	if !ambientcore.Enabled(s.flagConfig) && !config.HAP01BLegacyDirectEnforcementEnabled() {
		writeJSON(w, emptyAmbientHookResponse(true, "s3 disabled"))
		return
	}

	var req ambientHookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" || req.Project == "" || req.PromptText == "" {
		http.Error(w, "session_id, project, and prompt_text required", http.StatusBadRequest)
		return
	}
	if s.rejectLegacyDirectDelivery(w, r.Context(), req.Project) {
		return
	}

	result := ambientcore.Deliver(r.Context(), ambientcore.Dependencies{
		Registry: s.cognitiveRegistry,
		Meter:    s.cognitiveMeter,
		Queue:    s.cognitiveQueue,
		Flags:    s.flagConfig,
	}, ambientcore.Request{
		SessionID:  req.SessionID,
		Project:    req.Project,
		PromptText: req.PromptText,
		Limit:      req.Limit,
		Surface:    cognitive.HintSurfaceMCPPoll,
	})
	writeJSON(w, ambientHookResponse{
		Hints:             result.Hints,
		AdditionalContext: result.AdditionalContext,
		Disabled:          result.Disabled,
		Reason:            result.Reason,
	})
}
