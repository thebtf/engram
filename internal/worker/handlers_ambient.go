package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/cognitive/s3ambient"
	"github.com/thebtf/engram/pkg/cognitive"
)

const ambientHookTimeout = 200 * time.Millisecond

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

func buildAmbientAttentionEvent(req ambientHookRequest) cognitive.AttentionEvent {
	return cognitive.AttentionEvent{
		Type:      "user_prompt_submit",
		SessionID: req.SessionID,
		Project:   req.Project,
		Payload: map[string]interface{}{
			"text": req.PromptText,
		},
		Timestamp: time.Now().UTC(),
	}
}

func (s *Service) handleAmbientCandidates(w http.ResponseWriter, r *http.Request) {
	if !ambientHookEnabled(s.flagConfig) {
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

	ctx, cancel := context.WithTimeout(r.Context(), ambientHookTimeout)
	defer cancel()

	proposers := resolveAmbientCandidateProposers(s.cognitiveRegistry)
	fusion := s3ambient.NewFusion(true, proposers)
	proposals, err := fusion.Propose(ctx, buildAmbientAttentionEvent(req), normalizeAmbientHintLimit(req.Limit))
	if err != nil {
		writeJSON(w, emptyAmbientHookResponse(false, ""))
		return
	}

	delivery, ok := renderAmbientHookDelivery(ctx, s.cognitiveRegistry, s.cognitiveMeter, req.SessionID, proposals)
	if !ok {
		writeJSON(w, emptyAmbientHookResponse(false, ""))
		return
	}
	_ = s3ambient.DrainQueuedProposals(s.cognitiveQueue, req.SessionID, time.Now().UTC())

	writeJSON(w, ambientHookResponse{
		Hints:             delivery.Hints,
		AdditionalContext: delivery.AdditionalContext,
	})
}

func ambientHookEnabled(flagCfg cognitivecore.FlagConfig) bool {
	return flagCfg.IsPlugEnabled() && flagCfg.IsSubsystemEnabled("s3")
}

func normalizeAmbientHintLimit(limit int) int {
	if limit <= 0 {
		return 3
	}
	if limit > 3 {
		return 3
	}
	return limit
}

func resolveAmbientCandidateProposers(registry cognitivecore.SubsystemRegistry) []cognitive.CandidateProposer {
	type implsResolver interface {
		ResolveImpls(interfaceName string) []cognitivecore.Subsystem
	}
	resolver, ok := registry.(implsResolver)
	if !ok {
		return nil
	}
	impls := resolver.ResolveImpls("CandidateProposer")
	if len(impls) == 0 {
		return nil
	}
	out := make([]cognitive.CandidateProposer, 0, len(impls))
	for _, impl := range impls {
		proposer, ok := any(impl).(cognitive.CandidateProposer)
		if !ok {
			continue
		}
		out = append(out, proposer)
	}
	return out
}

func renderAmbientHookDelivery(ctx context.Context, registry cognitivecore.SubsystemRegistry, meter cognitivecore.SubsystemMeter, sessionID string, proposals []cognitive.HintProposal) (cognitive.HintDelivery, bool) {
	if registry == nil || meter == nil {
		return cognitive.HintDelivery{}, false
	}
	dispatcher := cognitivecore.NewSubsystemDispatcher(registry, meter)
	var delivery cognitive.HintDelivery
	if err := cognitivecore.Dispatch[cognitive.HintEmitter](ctx, dispatcher, "HintEmitter", func(emitter cognitive.HintEmitter) error {
		var err error
		delivery, err = emitter.Render(ctx, cognitive.HintSurfaceMCPPoll, sessionID, proposals)
		return err
	}); err != nil {
		return cognitive.HintDelivery{}, false
	}
	return delivery, true
}
