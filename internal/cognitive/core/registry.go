package core

import (
	"context"
	"fmt"
	"sync"

	"github.com/thebtf/engram/pkg/cognitive"
)

// subsystemEntry is the internal per-subsystem record held by registry.
type subsystemEntry struct {
	sub    Subsystem
	health SubsystemHealth
}

// registry is the unexported implementation of SubsystemRegistry per ADR-002.
// It is constructor-injected into worker.Service; tests get a fresh instance
// per test case. All mutable state is guarded by mu.
//
// Lifecycle states per ADR-009: registered → enabled ↔ disabled; any → failed.
// The failed state is written externally by the panic-isolating dispatcher
// (T015); registry itself only drives registered/enabled/disabled.
type registry struct {
	mu      sync.RWMutex
	entries map[string]*subsystemEntry
}

// newRegistry returns a ready-to-use SubsystemRegistry backed by *registry.
func newRegistry() SubsystemRegistry {
	return &registry{
		entries: make(map[string]*subsystemEntry),
	}
}

// Register stores s under s.Name(). Duplicate names return an error; the
// original registration is preserved (EC-2). Registration sets state to
// "registered" without activating the subsystem.
func (r *registry) Register(s Subsystem) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := s.Name()
	if _, exists := r.entries[name]; exists {
		return fmt.Errorf("subsystem %q already registered", name)
	}

	r.entries[name] = &subsystemEntry{
		sub: s,
		health: SubsystemHealth{
			State: "registered",
		},
	}
	return nil
}

// Enable transitions a registered or disabled subsystem to "enabled" by
// calling Subsystem.Start with an empty Dependencies value. Enabling an
// already-enabled subsystem is a no-op. Returns an error if the subsystem is
// unknown or if Start returns an error.
func (r *registry) Enable(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, exists := r.entries[name]
	if !exists {
		return fmt.Errorf("subsystem %q not registered", name)
	}

	if err := stateTransition(entry.health.State, "enabled"); err != nil {
		return fmt.Errorf("Enable %q: %w", name, err)
	}

	// No-op: already enabled.
	if entry.health.State == "enabled" {
		return nil
	}

	// Call Start before updating state; on error the state is not changed.
	if err := entry.sub.Start(context.Background(), Dependencies{}); err != nil {
		return fmt.Errorf("subsystem %q Start: %w", name, err)
	}

	entry.health.State = "enabled"
	return nil
}

// Disable transitions an enabled subsystem to "disabled" by calling
// Subsystem.Stop. Disabling an already-disabled subsystem is a no-op.
// Returns an error if the subsystem is unknown or if Stop returns an error.
func (r *registry) Disable(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, exists := r.entries[name]
	if !exists {
		return fmt.Errorf("subsystem %q not registered", name)
	}

	if err := stateTransition(entry.health.State, "disabled"); err != nil {
		return fmt.Errorf("Disable %q: %w", name, err)
	}

	// No-op: already disabled (or registered, which is effectively inactive).
	if entry.health.State != "enabled" {
		return nil
	}

	if err := entry.sub.Stop(); err != nil {
		return fmt.Errorf("subsystem %q Stop: %w", name, err)
	}

	entry.health.State = "disabled"
	return nil
}

// Get returns the Subsystem registered under name and true. Disabled and
// failed subsystems are also returned — callers must consult Health for
// lifecycle state. Returns nil, false when the name is unknown.
func (r *registry) Get(name string) (Subsystem, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, exists := r.entries[name]
	if !exists {
		return nil, false
	}
	return entry.sub, true
}

// List returns a snapshot of every registered subsystem's identity. The slice
// is independent of internal storage.
func (r *registry) List() []SubsystemInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]SubsystemInfo, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, SubsystemInfo{
			Name:       e.sub.Name(),
			Version:    e.sub.Version(),
			State:      e.health.State,
			Implements: append([]string(nil), e.sub.Implements()...),
		})
	}
	return out
}

// Health returns the lifecycle health of every registered subsystem. The map
// is independent of internal storage.
func (r *registry) Health() map[string]SubsystemHealth {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string]SubsystemHealth, len(r.entries))
	for name, e := range r.entries {
		out[name] = e.health
	}
	return out
}

// interfacePolicies is the compile-time immutable map that encodes Clarify C2:
// CandidateProposer is the single fan-out interface; every other declared
// cross-subsystem interface uses single-primary dispatch.
var interfacePolicies = map[string]cognitive.ResolutionPolicy{
	"CandidateProposer": cognitive.PolicyFanOut,
	// The 5 single-primary interfaces are listed explicitly so that typos in
	// caller code return PolicySinglePrimary (safe default) rather than silently
	// misrouting. Unknown names also fall through to the default below.
	"HintEmitter":          cognitive.PolicySinglePrimary,
	"StateWriter":          cognitive.PolicySinglePrimary,
	"AttentionEventWriter": cognitive.PolicySinglePrimary,
	"DirectiveDistiller":   cognitive.PolicySinglePrimary,
	"AttentionEventSource": cognitive.PolicySinglePrimary,
}

// ResolvePolicy reports the canonical ResolutionPolicy for the named
// cross-subsystem interface. Returns PolicyFanOut for "CandidateProposer";
// PolicySinglePrimary for all other names including unknown ones.
func (r *registry) ResolvePolicy(interfaceName string) cognitive.ResolutionPolicy {
	if p, ok := interfacePolicies[interfaceName]; ok {
		return p
	}
	return cognitive.PolicySinglePrimary
}

// ResolveImpls returns the enabled subsystems whose Implements() slice
// contains interfaceName. The returned slice is independent of internal
// storage and may be empty. This is a concrete method on *registry; it is
// not part of the SubsystemRegistry interface because callers that only need
// lifecycle management should not need dispatch.
func (r *registry) ResolveImpls(interfaceName string) []Subsystem {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Subsystem
	for _, e := range r.entries {
		if e.health.State != "enabled" {
			continue
		}
		for _, iface := range e.sub.Implements() {
			if iface == interfaceName {
				out = append(out, e.sub)
				break
			}
		}
	}
	return out
}

// stateTransition validates and permits a lifecycle state change from current
// to target. It returns an error for transitions that violate the ADR-009
// state machine. Valid transitions:
//
//	registered → enabled
//	disabled   → enabled
//	enabled    → enabled  (no-op; caller checks and returns early)
//	enabled    → disabled
//	disabled   → disabled (no-op; caller checks and returns early)
//	any        → failed   (set by dispatcher T015 via direct field write)
//
// Transitions not listed here are illegal and return an error.
func stateTransition(current, target string) error {
	switch target {
	case "enabled":
		switch current {
		case "registered", "disabled", "enabled":
			return nil
		default:
			return fmt.Errorf("cannot transition from %q to %q", current, target)
		}
	case "disabled":
		switch current {
		case "enabled", "disabled", "registered":
			return nil
		default:
			return fmt.Errorf("cannot transition from %q to %q", current, target)
		}
	case "failed":
		// Any state may transition to failed (written by the dispatcher).
		return nil
	default:
		return fmt.Errorf("unknown target state %q", target)
	}
}
