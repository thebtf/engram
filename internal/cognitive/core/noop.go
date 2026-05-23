package core

import (
	"context"

	"github.com/thebtf/engram/pkg/cognitive"
)

// RegisterNoOps creates and registers all five NoOp subsystems against
// registry. Callers (typically worker.Service during boot) call this to
// populate the SubsystemRegistry with safe-default no-operation
// implementations before real Wave-2 subsystems are wired in.
//
// AttentionEventSource is intentionally excluded: it is a declarative marker
// interface, not a dispatch-invoked contract, so a NoOp provides no value.
//
// Any registration error is returned immediately; subsequent subsystems are
// not registered.
func RegisterNoOps(registry SubsystemRegistry) error {
	noops := []Subsystem{
		&noopCandidateProposer{},
		&noopHintEmitter{},
		&noopStateWriter{},
		&noopAttentionEventWriter{},
		&noopDirectiveDistiller{},
	}
	for _, s := range noops {
		if err := registry.Register(s); err != nil {
			return err
		}
	}
	return nil
}

// --- noopCandidateProposer --------------------------------------------------

// noopCandidateProposer is the no-operation implementation of both
// pkg/cognitive.CandidateProposer and the CORE Subsystem interface. Propose
// always returns an empty non-nil slice with no side effects.
type noopCandidateProposer struct{}

func (n *noopCandidateProposer) Name() string                                  { return "core.noop.candidate_proposer" }
func (n *noopCandidateProposer) Version() string                               { return "v1.0.0" }
func (n *noopCandidateProposer) Start(_ context.Context, _ Dependencies) error { return nil }
func (n *noopCandidateProposer) Stop() error                                   { return nil }
func (n *noopCandidateProposer) Implements() []string                          { return []string{"CandidateProposer"} }

// Propose returns an empty non-nil slice and nil error. The empty-not-nil
// contract lets callers safely range over the result without nil checks.
func (n *noopCandidateProposer) Propose(_ context.Context, _ cognitive.AttentionEvent, _ int) ([]cognitive.HintProposal, error) {
	return []cognitive.HintProposal{}, nil
}

// --- noopHintEmitter --------------------------------------------------------

// noopHintEmitter is the no-operation implementation of both
// pkg/cognitive.HintEmitter and the CORE Subsystem interface. Render always
// returns the zero HintDelivery value with no side effects.
type noopHintEmitter struct{}

func (n *noopHintEmitter) Name() string                                  { return "core.noop.hint_emitter" }
func (n *noopHintEmitter) Version() string                               { return "v1.0.0" }
func (n *noopHintEmitter) Start(_ context.Context, _ Dependencies) error { return nil }
func (n *noopHintEmitter) Stop() error                                   { return nil }
func (n *noopHintEmitter) Implements() []string                          { return []string{"HintEmitter"} }

// Render returns the zero HintDelivery value and nil error.
func (n *noopHintEmitter) Render(_ context.Context, _ cognitive.HintSurface, _ string, _ []cognitive.HintProposal) (cognitive.HintDelivery, error) {
	return cognitive.HintDelivery{}, nil
}

// --- noopStateWriter --------------------------------------------------------

// noopStateWriter is the no-operation implementation of both
// pkg/cognitive.StateWriter and the CORE Subsystem interface. Both write
// methods return nil without side effects.
type noopStateWriter struct{}

func (n *noopStateWriter) Name() string                                  { return "core.noop.state_writer" }
func (n *noopStateWriter) Version() string                               { return "v1.0.0" }
func (n *noopStateWriter) Start(_ context.Context, _ Dependencies) error { return nil }
func (n *noopStateWriter) Stop() error                                   { return nil }
func (n *noopStateWriter) Implements() []string                          { return []string{"StateWriter"} }

// WriteSessionState is a no-op; returns nil.
func (n *noopStateWriter) WriteSessionState(_ context.Context, _ string, _ cognitive.SessionStateSlots) error {
	return nil
}

// WriteProjectState is a no-op; returns nil.
func (n *noopStateWriter) WriteProjectState(_ context.Context, _ string, _ cognitive.ProjectStateRecord) error {
	return nil
}

// --- noopAttentionEventWriter -----------------------------------------------

// noopAttentionEventWriter is the no-operation implementation of both
// pkg/cognitive.AttentionEventWriter and the CORE Subsystem interface.
// WriteAttentionEvent returns nil without side effects.
type noopAttentionEventWriter struct{}

func (n *noopAttentionEventWriter) Name() string                                  { return "core.noop.attention_event_writer" }
func (n *noopAttentionEventWriter) Version() string                               { return "v1.0.0" }
func (n *noopAttentionEventWriter) Start(_ context.Context, _ Dependencies) error { return nil }
func (n *noopAttentionEventWriter) Stop() error                                   { return nil }
func (n *noopAttentionEventWriter) Implements() []string {
	return []string{"AttentionEventWriter"}
}

// WriteAttentionEvent is a no-op; returns nil.
func (n *noopAttentionEventWriter) WriteAttentionEvent(_ context.Context, _ cognitive.AttentionEventRecord) error {
	return nil
}

// --- noopDirectiveDistiller -------------------------------------------------

// noopDirectiveDistiller is the no-operation implementation of both
// pkg/cognitive.DirectiveDistiller and the CORE Subsystem interface. Distill
// always returns the zero Distilled value with no side effects.
type noopDirectiveDistiller struct{}

func (n *noopDirectiveDistiller) Name() string                                  { return "core.noop.directive_distiller" }
func (n *noopDirectiveDistiller) Version() string                               { return "v1.0.0" }
func (n *noopDirectiveDistiller) Start(_ context.Context, _ Dependencies) error { return nil }
func (n *noopDirectiveDistiller) Stop() error                                   { return nil }
func (n *noopDirectiveDistiller) Implements() []string                          { return []string{"DirectiveDistiller"} }

// Distill returns the zero Distilled value and nil error.
func (n *noopDirectiveDistiller) Distill(_ context.Context, _ cognitive.RawSignal) (cognitive.Distilled, error) {
	return cognitive.Distilled{}, nil
}
