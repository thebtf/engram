// Package ambientcore contains the bounded fail-open ambient delivery core
// shared by the legacy HTTP hook and the private bridge gRPC facade.
package ambientcore

import (
	"context"
	"time"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/cognitive/s3ambient"
	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	Timeout                   = 200 * time.Millisecond
	MaxAdditionalContextBytes = 4 * 1024
)

// Request contains the accepted callback facts for one ambient attempt.
type Request struct {
	SessionID  string
	Project    string
	PromptText string
	Limit      int
	Surface    cognitive.HintSurface
}

// Result is a fully formed attempted-delivery payload. A successful non-empty
// result drains the fallback queue before this value is returned, so a lost
// response cannot cause a duplicate later fallback delivery.
type Result struct {
	Hints             []cognitive.HintProposal
	AdditionalContext string
	Disabled          bool
	Reason            string
}

// Dependencies are the worker-owned ambient substrate dependencies.
type Dependencies struct {
	Registry cognitivecore.SubsystemRegistry
	Meter    cognitivecore.SubsystemMeter
	Queue    cognitivecore.HintQueue
	Flags    cognitivecore.FlagConfig
}

// Enabled reports whether the existing S3 ambient subsystem is enabled.
func Enabled(flags cognitivecore.FlagConfig) bool {
	return flags.IsPlugEnabled() && flags.IsSubsystemEnabled("s3")
}

// BoundedAdditionalContext caps private bridge text without splitting a UTF-8
// rune. Existing HTTP delivery keeps its established renderer payload intact.
func BoundedAdditionalContext(value string) string {
	if len(value) <= MaxAdditionalContextBytes {
		return value
	}
	end := MaxAdditionalContextBytes
	for end > 0 && value[end]&0xc0 == 0x80 {
		end--
	}
	return value[:end]
}

// Deliver runs the existing S3 ambient pipeline under its fixed 200 ms budget.
// Any proposer or renderer failure becomes an empty result, preserving the
// established fail-open behavior.
func Deliver(parent context.Context, deps Dependencies, req Request) Result {
	if !Enabled(deps.Flags) {
		return Result{Disabled: true, Reason: "s3 disabled"}
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, Timeout)
	defer cancel()

	proposers := candidateProposers(deps.Registry)
	fusion := s3ambient.NewFusion(true, proposers)
	proposals, err := fusion.Propose(ctx, attentionEvent(req), normalizedLimit(req.Limit))
	if err != nil {
		return Result{}
	}

	delivery, ok := render(ctx, deps.Registry, deps.Meter, req.SessionID, req.Surface, proposals)
	if !ok {
		return Result{}
	}
	if len(delivery.Hints) > 0 || delivery.AdditionalContext != "" {
		// This is the attempted-delivery commit point: the caller now has a
		// fully formed payload even if its transport later loses the response.
		_ = s3ambient.DrainQueuedProposals(deps.Queue, req.SessionID, time.Now().UTC())
	}
	return Result{
		Hints:             delivery.Hints,
		AdditionalContext: delivery.AdditionalContext,
	}
}

func attentionEvent(req Request) cognitive.AttentionEvent {
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

func normalizedLimit(limit int) int {
	if limit <= 0 {
		return 3
	}
	if limit > 3 {
		return 3
	}
	return limit
}

func candidateProposers(registry cognitivecore.SubsystemRegistry) []cognitive.CandidateProposer {
	type implementationsResolver interface {
		ResolveImpls(interfaceName string) []cognitivecore.Subsystem
	}
	resolver, ok := registry.(implementationsResolver)
	if !ok {
		return nil
	}
	implementations := resolver.ResolveImpls("CandidateProposer")
	if len(implementations) == 0 {
		return nil
	}
	proposers := make([]cognitive.CandidateProposer, 0, len(implementations))
	for _, implementation := range implementations {
		proposer, ok := any(implementation).(cognitive.CandidateProposer)
		if !ok {
			continue
		}
		proposers = append(proposers, proposer)
	}
	return proposers
}

func render(ctx context.Context, registry cognitivecore.SubsystemRegistry, meter cognitivecore.SubsystemMeter, sessionID string, surface cognitive.HintSurface, proposals []cognitive.HintProposal) (cognitive.HintDelivery, bool) {
	if registry == nil || meter == nil {
		return cognitive.HintDelivery{}, false
	}
	dispatcher := cognitivecore.NewSubsystemDispatcher(registry, meter)
	var delivery cognitive.HintDelivery
	if err := cognitivecore.Dispatch[cognitive.HintEmitter](ctx, dispatcher, "HintEmitter", func(emitter cognitive.HintEmitter) error {
		var err error
		delivery, err = emitter.Render(ctx, surface, sessionID, proposals)
		return err
	}); err != nil {
		return cognitive.HintDelivery{}, false
	}
	return delivery, true
}
