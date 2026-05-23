package core

import (
	"context"
	"reflect"
	"testing"

	"github.com/thebtf/engram/pkg/cognitive"
)

// fakeRegistry is a minimal SubsystemRegistry test double that records every
// Register call. The other six methods return safe zero values; none of the
// T011 tests exercise them.
type fakeRegistry struct {
	registered []Subsystem
}

func (f *fakeRegistry) Register(s Subsystem) error {
	f.registered = append(f.registered, s)
	return nil
}

func (f *fakeRegistry) Enable(_ string) error              { return nil }
func (f *fakeRegistry) Disable(_ string) error             { return nil }
func (f *fakeRegistry) Get(_ string) (Subsystem, bool)     { return nil, false }
func (f *fakeRegistry) List() []SubsystemInfo              { return nil }
func (f *fakeRegistry) Health() map[string]SubsystemHealth { return nil }
func (f *fakeRegistry) ResolvePolicy(_ string) cognitive.ResolutionPolicy {
	return cognitive.PolicySinglePrimary
}

// TestNoOps_FiveRegistered_CanonicalNames verifies that RegisterNoOps invokes
// Register exactly 5 times and that each registered subsystem carries the
// canonical name defined in the AC.
func TestNoOps_FiveRegistered_CanonicalNames(t *testing.T) {
	reg := &fakeRegistry{}
	if err := RegisterNoOps(reg); err != nil {
		t.Fatalf("RegisterNoOps returned unexpected error: %v", err)
	}

	wantNames := []string{
		"core.noop.candidate_proposer",
		"core.noop.hint_emitter",
		"core.noop.state_writer",
		"core.noop.attention_event_writer",
		"core.noop.directive_distiller",
	}

	if got := len(reg.registered); got != len(wantNames) {
		t.Fatalf("RegisterNoOps registered %d subsystems; want %d", got, len(wantNames))
	}

	// Build a name→bool presence map from actual registrations.
	gotNames := make(map[string]bool, len(reg.registered))
	for _, s := range reg.registered {
		gotNames[s.Name()] = true
	}

	for _, name := range wantNames {
		if !gotNames[name] {
			t.Errorf("canonical name %q not registered; got names: %v", name, reg.registered)
		}
	}
}

// TestNoOpCandidateProposer_ReturnsEmptyList verifies Propose returns an
// empty (non-nil) slice and nil error, without side effects.
func TestNoOpCandidateProposer_ReturnsEmptyList(t *testing.T) {
	p := &noopCandidateProposer{}
	proposals, err := p.Propose(context.Background(), cognitive.AttentionEvent{}, 10)
	if err != nil {
		t.Fatalf("Propose returned unexpected error: %v", err)
	}
	if proposals == nil {
		t.Fatal("Propose returned nil slice; want empty non-nil slice")
	}
	if len(proposals) != 0 {
		t.Fatalf("Propose returned %d proposals; want 0", len(proposals))
	}
}

// TestNoOpHintEmitter_ReturnsEmptyDelivery verifies Render returns the zero
// HintDelivery value and nil error.
func TestNoOpHintEmitter_ReturnsEmptyDelivery(t *testing.T) {
	e := &noopHintEmitter{}
	delivery, err := e.Render(
		context.Background(),
		cognitive.HintSurfaceMCPPoll,
		"session-1",
		nil,
	)
	if err != nil {
		t.Fatalf("Render returned unexpected error: %v", err)
	}
	want := cognitive.HintDelivery{}
	if !reflect.DeepEqual(delivery, want) {
		t.Fatalf("Render returned %+v; want zero HintDelivery %+v", delivery, want)
	}
}

// TestNoOpStateWriter_NilErrorBothMethods verifies both WriteSessionState and
// WriteProjectState return nil without side effects.
func TestNoOpStateWriter_NilErrorBothMethods(t *testing.T) {
	w := &noopStateWriter{}

	if err := w.WriteSessionState(
		context.Background(),
		"session-1",
		cognitive.SessionStateSlots{},
	); err != nil {
		t.Fatalf("WriteSessionState returned unexpected error: %v", err)
	}

	if err := w.WriteProjectState(
		context.Background(),
		"project-1",
		cognitive.ProjectStateRecord{},
	); err != nil {
		t.Fatalf("WriteProjectState returned unexpected error: %v", err)
	}
}

// TestNoOpAttentionEventWriter_NilError verifies WriteAttentionEvent returns
// nil without side effects.
func TestNoOpAttentionEventWriter_NilError(t *testing.T) {
	aw := &noopAttentionEventWriter{}
	if err := aw.WriteAttentionEvent(
		context.Background(),
		cognitive.AttentionEventRecord{},
	); err != nil {
		t.Fatalf("WriteAttentionEvent returned unexpected error: %v", err)
	}
}

// TestNoOpDirectiveDistiller_ReturnsZeroDistilled verifies Distill returns
// the zero Distilled value and nil error.
func TestNoOpDirectiveDistiller_ReturnsZeroDistilled(t *testing.T) {
	d := &noopDirectiveDistiller{}
	distilled, err := d.Distill(context.Background(), cognitive.RawSignal{})
	if err != nil {
		t.Fatalf("Distill returned unexpected error: %v", err)
	}
	var want cognitive.Distilled
	if distilled != want {
		t.Fatalf("Distill returned %+v; want zero Distilled %+v", distilled, want)
	}
}
