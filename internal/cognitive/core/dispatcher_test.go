package core

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thebtf/engram/pkg/cognitive"
)

// --- Mock subsystems used by dispatcher tests --------------------------------

// proposerStub is a CandidateProposer implementation that also satisfies the
// Subsystem interface so the registry can register it.
type proposerStub struct {
	name       string
	implements []string
	propose    func(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error)
}

func (p *proposerStub) Name() string                                       { return p.name }
func (p *proposerStub) Version() string                                    { return "v1.0.0" }
func (p *proposerStub) Start(ctx context.Context, deps Dependencies) error { return nil }
func (p *proposerStub) Stop() error                                        { return nil }
func (p *proposerStub) Implements() []string                               { return p.implements }
func (p *proposerStub) Propose(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
	return p.propose(ctx, event, limit)
}

// emitterStub satisfies HintEmitter + Subsystem; used for single-primary tests.
type emitterStub struct {
	name       string
	implements []string
	render     func(ctx context.Context, surface cognitive.HintSurface, sessionID string, hints []cognitive.HintProposal) (cognitive.HintDelivery, error)
}

func (e *emitterStub) Name() string                                       { return e.name }
func (e *emitterStub) Version() string                                    { return "v1.0.0" }
func (e *emitterStub) Start(ctx context.Context, deps Dependencies) error { return nil }
func (e *emitterStub) Stop() error                                        { return nil }
func (e *emitterStub) Implements() []string                               { return e.implements }
func (e *emitterStub) Render(ctx context.Context, surface cognitive.HintSurface, sessionID string, hints []cognitive.HintProposal) (cognitive.HintDelivery, error) {
	return e.render(ctx, surface, sessionID, hints)
}

// registerAndEnable is a test helper that registers + enables a subsystem in
// one shot. Failures bubble via t.Fatalf so individual tests stay terse.
func registerAndEnable(t *testing.T, reg SubsystemRegistry, sub Subsystem) {
	t.Helper()
	if err := reg.Register(sub); err != nil {
		t.Fatalf("Register(%s): %v", sub.Name(), err)
	}
	if err := reg.Enable(sub.Name()); err != nil {
		t.Fatalf("Enable(%s): %v", sub.Name(), err)
	}
}

// --- TestDispatch_FanOut_AllCalled ------------------------------------------

func TestDispatch_FanOut_AllCalled(t *testing.T) {
	reg := newRegistry()
	meter := NewLocalMeter()

	var hits atomic.Int32
	for i, name := range []string{"p1", "p2", "p3"} {
		_ = i
		registerAndEnable(t, reg, &proposerStub{
			name:       name,
			implements: []string{"CandidateProposer"},
			propose: func(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
				hits.Add(1)
				return nil, nil
			},
		})
	}

	d := NewSubsystemDispatcher(reg, meter)
	err := Dispatch[cognitive.CandidateProposer](
		context.Background(), d, "CandidateProposer",
		func(p cognitive.CandidateProposer) error {
			_, err := p.Propose(context.Background(), cognitive.AttentionEvent{}, 10)
			return err
		},
	)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("hit count: got %d, want 3", got)
	}
}

// --- TestDispatch_SinglePrimary_LastWins ------------------------------------

func TestDispatch_SinglePrimary_LastWins(t *testing.T) {
	reg := newRegistry()
	meter := NewLocalMeter()

	var noopHits, s3Hits atomic.Int32

	// "noop-emitter" registers first.
	registerAndEnable(t, reg, &emitterStub{
		name:       "noop-emitter",
		implements: []string{"HintEmitter"},
		render: func(ctx context.Context, surface cognitive.HintSurface, sessionID string, hints []cognitive.HintProposal) (cognitive.HintDelivery, error) {
			noopHits.Add(1)
			return cognitive.HintDelivery{}, nil
		},
	})

	// "s3-emitter" registers second — should win SinglePrimary.
	registerAndEnable(t, reg, &emitterStub{
		name:       "s3-emitter",
		implements: []string{"HintEmitter"},
		render: func(ctx context.Context, surface cognitive.HintSurface, sessionID string, hints []cognitive.HintProposal) (cognitive.HintDelivery, error) {
			s3Hits.Add(1)
			return cognitive.HintDelivery{Surface: cognitive.HintSurfaceUserPromptSubmit}, nil
		},
	})

	d := NewSubsystemDispatcher(reg, meter)
	err := Dispatch[cognitive.HintEmitter](
		context.Background(), d, "HintEmitter",
		func(e cognitive.HintEmitter) error {
			_, err := e.Render(context.Background(), cognitive.HintSurfaceUserPromptSubmit, "sess-1", nil)
			return err
		},
	)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if got := s3Hits.Load(); got != 1 {
		t.Errorf("s3-emitter hit count: got %d, want 1", got)
	}
	if got := noopHits.Load(); got != 0 {
		t.Errorf("noop-emitter hit count: got %d, want 0 (SinglePrimary should bypass)", got)
	}
}

// --- TestDispatch_PanicIsolation_OthersUnaffected ---------------------------

func TestDispatch_PanicIsolation_OthersUnaffected(t *testing.T) {
	silenceZerolog(t)
	reg := newRegistry()
	meter := NewLocalMeter()

	var leftHits, rightHits atomic.Int32

	registerAndEnable(t, reg, &proposerStub{
		name:       "left",
		implements: []string{"CandidateProposer"},
		propose: func(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
			leftHits.Add(1)
			return nil, nil
		},
	})
	registerAndEnable(t, reg, &proposerStub{
		name:       "middle-panics",
		implements: []string{"CandidateProposer"},
		propose: func(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
			panic("intentional test panic")
		},
	})
	registerAndEnable(t, reg, &proposerStub{
		name:       "right",
		implements: []string{"CandidateProposer"},
		propose: func(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
			rightHits.Add(1)
			return nil, nil
		},
	})

	d := NewSubsystemDispatcher(reg, meter)
	err := Dispatch[cognitive.CandidateProposer](
		context.Background(), d, "CandidateProposer",
		func(p cognitive.CandidateProposer) error {
			_, err := p.Propose(context.Background(), cognitive.AttentionEvent{}, 10)
			return err
		},
	)
	if err == nil {
		t.Fatal("Dispatch should return joined error for panicked impl, got nil")
	}
	if !strings.Contains(err.Error(), "middle-panics") {
		t.Errorf("error missing offending subsystem name: %v", err)
	}
	if got := leftHits.Load(); got != 1 {
		t.Errorf("left hit count: got %d, want 1 (panic must not abort siblings)", got)
	}
	if got := rightHits.Load(); got != 1 {
		t.Errorf("right hit count: got %d, want 1", got)
	}

	// Panicked subsystem must be transitioned to "failed".
	healths := reg.Health()
	if got := healths["middle-panics"].State; got != "failed" {
		t.Errorf("middle-panics state: got %q, want %q", got, "failed")
	}
	if got := healths["left"].State; got == "failed" {
		t.Errorf("left state: got %q, want NOT failed", got)
	}
	if got := healths["right"].State; got == "failed" {
		t.Errorf("right state: got %q, want NOT failed", got)
	}

	// subsystem_panic_total{subsystem=middle-panics} must be incremented.
	snap := meter.Snapshot()
	wantKey := "subsystem_panic_total{subsystem=middle-panics}"
	if got := snap.Counters[wantKey]; got != 1 {
		t.Errorf("panic counter %q: got %d, want 1", wantKey, got)
	}
}

// --- TestDispatch_NoImpls_ReturnsNil ----------------------------------------

func TestDispatch_NoImpls_ReturnsNil(t *testing.T) {
	reg := newRegistry()
	meter := NewLocalMeter()
	d := NewSubsystemDispatcher(reg, meter)

	called := false
	err := Dispatch[cognitive.CandidateProposer](
		context.Background(), d, "CandidateProposer",
		func(cognitive.CandidateProposer) error {
			called = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("empty registry should return nil, got %v", err)
	}
	if called {
		t.Errorf("closure should not be called when there are no impls")
	}
}

// --- TestDispatch_HandlerError_Propagated ----------------------------------

func TestDispatch_HandlerError_Propagated(t *testing.T) {
	reg := newRegistry()
	meter := NewLocalMeter()

	wantErr := errors.New("handler said no")
	registerAndEnable(t, reg, &proposerStub{
		name:       "errorful",
		implements: []string{"CandidateProposer"},
		propose: func(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
			return nil, wantErr
		},
	})

	d := NewSubsystemDispatcher(reg, meter)
	err := Dispatch[cognitive.CandidateProposer](
		context.Background(), d, "CandidateProposer",
		func(p cognitive.CandidateProposer) error {
			_, err := p.Propose(context.Background(), cognitive.AttentionEvent{}, 10)
			return err
		},
	)
	if !errors.Is(err, wantErr) {
		t.Errorf("error: got %v, want chain containing %v", err, wantErr)
	}
}

// --- TestDispatch_RecoverStackTrace_Logged ---------------------------------

// We do not capture zerolog output here (would require an io.Writer plumb
// through the global logger); instead we verify the panic produces an error
// whose message contains the recovered reason, which proves the recover
// boundary ran and serialized the reason exactly once.
func TestDispatch_RecoverStackTrace_Logged(t *testing.T) {
	silenceZerolog(t)
	reg := newRegistry()
	meter := NewLocalMeter()

	registerAndEnable(t, reg, &proposerStub{
		name:       "boomer",
		implements: []string{"CandidateProposer"},
		propose: func(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
			panic("specific-panic-reason")
		},
	})

	d := NewSubsystemDispatcher(reg, meter)
	err := Dispatch[cognitive.CandidateProposer](
		context.Background(), d, "CandidateProposer",
		func(p cognitive.CandidateProposer) error {
			_, err := p.Propose(context.Background(), cognitive.AttentionEvent{}, 10)
			return err
		},
	)
	if err == nil {
		t.Fatal("expected error from panicked impl, got nil")
	}
	if !strings.Contains(err.Error(), "specific-panic-reason") {
		t.Errorf("error must surface recovered reason verbatim: %v", err)
	}
}
