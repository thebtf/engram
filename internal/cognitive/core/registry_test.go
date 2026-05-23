package core

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/thebtf/engram/pkg/cognitive"
)

// mockSubsystem is a minimal test double for Subsystem. Name and version are
// configurable; Implements returns the supplied slice.
type mockSubsystem struct {
	name       string
	version    string
	implements []string
	startErr   error
	stopErr    error
}

func (m *mockSubsystem) Name() string    { return m.name }
func (m *mockSubsystem) Version() string { return m.version }
func (m *mockSubsystem) Implements() []string {
	if m.implements == nil {
		return []string{}
	}
	return m.implements
}
func (m *mockSubsystem) Start(_ context.Context, _ Dependencies) error { return m.startErr }
func (m *mockSubsystem) Stop() error                                   { return m.stopErr }

// newMock builds a named mock with no interface declarations.
func newMock(name string) *mockSubsystem {
	return &mockSubsystem{name: name, version: "1.0.0"}
}

// newMockImpls builds a named mock that claims to implement the given
// cross-subsystem interface names.
func newMockImpls(name string, implements ...string) *mockSubsystem {
	return &mockSubsystem{name: name, version: "1.0.0", implements: implements}
}

// --- TestRegister_NewSubsystem -----------------------------------------------

// TestRegister_NewSubsystem verifies that a freshly registered subsystem is
// visible via Get and that its initial lifecycle state is "registered".
func TestRegister_NewSubsystem(t *testing.T) {
	r := newRegistry()

	sub := newMock("alpha")
	if err := r.Register(sub); err != nil {
		t.Fatalf("Register returned unexpected error: %v", err)
	}

	got, ok := r.Get("alpha")
	if !ok {
		t.Fatal("Get returned false for just-registered subsystem")
	}
	if got.Name() != "alpha" {
		t.Fatalf("Get returned wrong subsystem: name=%q", got.Name())
	}

	h := r.Health()
	entry, exists := h["alpha"]
	if !exists {
		t.Fatal("Health map missing 'alpha' entry")
	}
	if entry.State != "registered" {
		t.Fatalf("initial state = %q, want %q", entry.State, "registered")
	}
}

// --- TestRegister_Duplicate_FirstWinsErrorReturned ---------------------------

// TestRegister_Duplicate_FirstWinsErrorReturned verifies that registering a
// second subsystem under the same name returns an error and the original
// registration is preserved (EC-2).
func TestRegister_Duplicate_FirstWinsErrorReturned(t *testing.T) {
	r := newRegistry()

	first := newMock("beta")
	first.version = "1.0.0"
	if err := r.Register(first); err != nil {
		t.Fatalf("first Register failed: %v", err)
	}

	second := newMock("beta")
	second.version = "2.0.0"
	err := r.Register(second)
	if err == nil {
		t.Fatal("second Register with duplicate name should return error, got nil")
	}

	// First registration must be preserved.
	got, ok := r.Get("beta")
	if !ok {
		t.Fatal("Get returned false after duplicate Register attempt")
	}
	if got.Version() != "1.0.0" {
		t.Fatalf("first registration was overwritten: version=%q, want %q", got.Version(), "1.0.0")
	}
}

// --- TestEnable_StateTransition ----------------------------------------------

// TestEnable_StateTransition verifies that Enable transitions a subsystem from
// "registered" to "enabled".
func TestEnable_StateTransition(t *testing.T) {
	r := newRegistry()

	sub := newMock("gamma")
	if err := r.Register(sub); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := r.Enable("gamma"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	h := r.Health()
	entry, exists := h["gamma"]
	if !exists {
		t.Fatal("Health map missing 'gamma' after Enable")
	}
	if entry.State != "enabled" {
		t.Fatalf("state after Enable = %q, want %q", entry.State, "enabled")
	}
}

// TestEnable_AlreadyEnabled verifies that calling Enable on an already-enabled
// subsystem is a no-op (no error returned).
func TestEnable_AlreadyEnabled(t *testing.T) {
	r := newRegistry()

	sub := newMock("delta")
	if err := r.Register(sub); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Enable("delta"); err != nil {
		t.Fatalf("first Enable: %v", err)
	}
	// Second Enable on an already-enabled subsystem must not return an error.
	if err := r.Enable("delta"); err != nil {
		t.Fatalf("second Enable (no-op expected): %v", err)
	}
}

// --- TestDisable ------------------------------------------------------------

// TestDisable verifies that Disable transitions a subsystem from "enabled" to
// "disabled".
func TestDisable(t *testing.T) {
	r := newRegistry()

	sub := newMock("epsilon")
	if err := r.Register(sub); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Enable("epsilon"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := r.Disable("epsilon"); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	h := r.Health()
	entry, exists := h["epsilon"]
	if !exists {
		t.Fatal("Health map missing 'epsilon' after Disable")
	}
	if entry.State != "disabled" {
		t.Fatalf("state after Disable = %q, want %q", entry.State, "disabled")
	}
}

// --- TestHealth_ReportsAllRegistered ----------------------------------------

// TestHealth_ReportsAllRegistered verifies that Health returns an entry for
// every registered subsystem, all in state "registered" before any Enable.
func TestHealth_ReportsAllRegistered(t *testing.T) {
	r := newRegistry()

	names := []string{"s1", "s2", "s3"}
	for _, n := range names {
		if err := r.Register(newMock(n)); err != nil {
			t.Fatalf("Register %q: %v", n, err)
		}
	}

	h := r.Health()
	if len(h) != len(names) {
		t.Fatalf("Health returned %d entries, want %d", len(h), len(names))
	}
	for _, n := range names {
		entry, exists := h[n]
		if !exists {
			t.Errorf("Health missing entry for %q", n)
			continue
		}
		if entry.State != "registered" {
			t.Errorf("Health[%q].State = %q, want %q", n, entry.State, "registered")
		}
	}
}

// --- TestResolvePolicy_CandidateProposer_FanOut ------------------------------

// TestResolvePolicy_CandidateProposer_FanOut verifies that
// ResolvePolicy("CandidateProposer") returns PolicyFanOut per Clarify C2.
func TestResolvePolicy_CandidateProposer_FanOut(t *testing.T) {
	r := newRegistry()

	got := r.ResolvePolicy("CandidateProposer")
	if got != cognitive.PolicyFanOut {
		t.Fatalf("ResolvePolicy(%q) = %q, want %q", "CandidateProposer", got, cognitive.PolicyFanOut)
	}
}

// --- TestResolvePolicy_AllOthersSinglePrimary --------------------------------

// TestResolvePolicy_AllOthersSinglePrimary verifies that all 5 non-FanOut
// interface names plus an unknown name return PolicySinglePrimary (table-driven).
func TestResolvePolicy_AllOthersSinglePrimary(t *testing.T) {
	r := newRegistry()

	cases := []struct {
		interfaceName string
	}{
		{"HintEmitter"},
		{"StateWriter"},
		{"AttentionEventWriter"},
		{"DirectiveDistiller"},
		{"AttentionEventSource"},
		{"Unknown"},
	}

	for _, tc := range cases {
		t.Run(tc.interfaceName, func(t *testing.T) {
			got := r.ResolvePolicy(tc.interfaceName)
			if got != cognitive.PolicySinglePrimary {
				t.Fatalf("ResolvePolicy(%q) = %q, want %q",
					tc.interfaceName, got, cognitive.PolicySinglePrimary)
			}
		})
	}
}

// --- TestResolveImpls_ReturnsEnabledOnly -------------------------------------

// TestResolveImpls_ReturnsEnabledOnly verifies that ResolveImpls returns only
// enabled subsystems that declare the requested interface.
func TestResolveImpls_ReturnsEnabledOnly(t *testing.T) {
	r := newRegistry()

	// s1: implements CandidateProposer, enabled
	s1 := newMockImpls("s1", "CandidateProposer")
	// s2: implements CandidateProposer, registered-only (not enabled)
	s2 := newMockImpls("s2", "CandidateProposer")
	// s3: does NOT implement CandidateProposer, enabled
	s3 := newMockImpls("s3", "HintEmitter")

	for _, sub := range []Subsystem{s1, s2, s3} {
		if err := r.Register(sub); err != nil {
			t.Fatalf("Register %q: %v", sub.Name(), err)
		}
	}

	// Enable s1 and s3; leave s2 in registered state.
	if err := r.Enable("s1"); err != nil {
		t.Fatalf("Enable s1: %v", err)
	}
	if err := r.Enable("s3"); err != nil {
		t.Fatalf("Enable s3: %v", err)
	}

	// ResolveImpls is a method on *registry, not on SubsystemRegistry interface.
	// Access it via type assertion.
	concreteR, ok := r.(*registry)
	if !ok {
		t.Fatal("newRegistry() did not return *registry")
	}

	impls := concreteR.ResolveImpls("CandidateProposer")
	if len(impls) != 1 {
		t.Fatalf("ResolveImpls returned %d impls, want 1; got: %v", len(impls), names(impls))
	}
	if impls[0].Name() != "s1" {
		t.Fatalf("ResolveImpls returned %q, want %q", impls[0].Name(), "s1")
	}
}

// names extracts Name() from a Subsystem slice for readable test messages.
func names(ss []Subsystem) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name()
	}
	return out
}

// --- TestRace_ConcurrentRegisterRead ----------------------------------------

// TestRace_ConcurrentRegisterRead verifies absence of data races under
// concurrent Register and List calls. Run with -race flag to catch violations.
func TestRace_ConcurrentRegisterRead(t *testing.T) {
	r := newRegistry()

	const writers = 10
	const readers = 10

	var wg sync.WaitGroup
	wg.Add(writers + readers)

	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("race-sub-%d", i)
			_ = r.Register(newMock(name))
		}()
	}

	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			_ = r.List()
		}()
	}

	wg.Wait()
}
