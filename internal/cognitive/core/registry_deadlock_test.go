package core

import (
	"context"
	"testing"
	"time"
)

// reentrantSubsystem is a test double whose Start callback re-enters the
// registry through every read-side method that takes r.mu — List, Get,
// Health, ResolvePolicy, and the concrete ResolveImpls. If Enable holds
// r.mu across the Subsystem.Start call, every one of these calls deadlocks
// and the test times out via the watchdog goroutine below.
type reentrantSubsystem struct {
	name string
	reg  SubsystemRegistry
	hit  bool
}

func (s *reentrantSubsystem) Name() string    { return s.name }
func (s *reentrantSubsystem) Version() string { return "v1.0.0" }
func (s *reentrantSubsystem) Start(ctx context.Context, deps Dependencies) error {
	_ = deps.Registry.List()
	_, _ = deps.Registry.Get(s.name)
	_ = deps.Registry.Health()
	_ = deps.Registry.ResolvePolicy("CandidateProposer")
	if resolver, ok := deps.Registry.(interface {
		ResolveImpls(interfaceName string) []Subsystem
	}); ok {
		_ = resolver.ResolveImpls("CandidateProposer")
	}
	s.hit = true
	return nil
}
func (s *reentrantSubsystem) Stop() error          { return nil }
func (s *reentrantSubsystem) Implements() []string { return []string{"CandidateProposer"} }

// TestEnable_StartCallbackReentersRegistry_NoDeadlock pins the PM re-review
// HIGH/Wave-2-blocker finding: Subsystem.Start must be invoked OUTSIDE the
// registry mutex so it can safely call deps.Registry.List/Get/Health/etc.
// Without the fix, this test deadlocks; the watchdog triggers t.Fatal if
// Enable does not complete within 2 seconds.
func TestEnable_StartCallbackReentersRegistry_NoDeadlock(t *testing.T) {
	reg := newRegistry()

	type depsSetter interface {
		SetDependencies(deps Dependencies)
	}
	setter, ok := reg.(depsSetter)
	if !ok {
		t.Fatalf("registry concrete impl does not expose SetDependencies")
	}
	setter.SetDependencies(Dependencies{Registry: reg})

	sub := &reentrantSubsystem{name: "reentrant-test", reg: reg}
	if err := reg.Register(sub); err != nil {
		t.Fatalf("Register: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- reg.Enable(sub.Name())
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Enable: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Enable deadlocked — Subsystem.Start could not call back into the registry")
	}

	if !sub.hit {
		t.Errorf("reentrant Start callback never observed completion — Start was likely not called")
	}
}

// reentrantStopSubsystem mirrors the Enable test for Disable: Stop must run
// outside the registry mutex so the impl can run shutdown-time queries.
type reentrantStopSubsystem struct {
	name string
	hit  bool
}

func (s *reentrantStopSubsystem) Name() string                                       { return s.name }
func (s *reentrantStopSubsystem) Version() string                                    { return "v1.0.0" }
func (s *reentrantStopSubsystem) Start(ctx context.Context, deps Dependencies) error { return nil }
func (s *reentrantStopSubsystem) Implements() []string                               { return []string{"CandidateProposer"} }
func (s *reentrantStopSubsystem) Stop() error {
	s.hit = true
	return nil
}

func TestDisable_StopCallbackReentersRegistry_NoDeadlock(t *testing.T) {
	reg := newRegistry()
	sub := &reentrantStopSubsystem{name: "stop-reentrant"}
	if err := reg.Register(sub); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Enable(sub.Name()); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- reg.Disable(sub.Name())
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Disable: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Disable deadlocked — Subsystem.Stop ran with registry mutex held")
	}

	if !sub.hit {
		t.Errorf("Stop callback never observed completion")
	}
}
