package core

import (
	"context"
	"fmt"
	"sync"
	"time"

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
//
// order keeps insertion order so SinglePrimary dispatch can deterministically
// pick the last-registered enabled impl. A map alone gives random iteration
// which would make ResolvePolicy/PolicySinglePrimary nondeterministic — the
// ADR-010 "last-registered-wins" rule requires explicit ordering tracking.
type registry struct {
	mu      sync.RWMutex
	entries map[string]*subsystemEntry
	order   []string
	deps    Dependencies
}

// newRegistry returns a ready-to-use SubsystemRegistry backed by *registry.
func newRegistry() SubsystemRegistry {
	return &registry{
		entries: make(map[string]*subsystemEntry),
	}
}

// NewRegistry is the exported constructor for the canonical CORE-internal
// SubsystemRegistry implementation. Worker-side wiring (T014) uses this to
// build the per-service registry; tests inside the core package keep using
// newRegistry directly when they want a freshly-allocated registry.
//
// The returned value also satisfies the optional RegistryWithDependencies
// helper interface; callers that need to inject CORE-wide dependencies
// before any Enable call can type-assert and invoke SetDependencies.
func NewRegistry() SubsystemRegistry {
	return newRegistry()
}

// SetDependencies installs the CORE-wide Dependencies bundle the registry
// passes to Subsystem.Start at Enable time. Without this call, Enable invokes
// Subsystem.Start with the zero Dependencies value — adequate for NoOps but
// insufficient for real subsystems that need Bus/Queue/Meter/DB/Logger.
// NewService is the canonical caller; it sets Dependencies after building
// the platform pieces and before activating any subsystem.
//
// The method takes a value (not pointer): Dependencies is a small bundle
// of interface handles, and copy semantics make it safe to share across
// goroutines without locking.
func (r *registry) SetDependencies(deps Dependencies) {
	r.mu.Lock()
	r.deps = deps
	r.mu.Unlock()
}

// Register stores s under s.Name(). Duplicate names return an error; the
// original registration is preserved (EC-2). Registration sets state to
// "registered" without activating the subsystem.
//
// A nil Subsystem returns an error rather than panicking on the Name() call —
// the caller is expected to have constructed a real Subsystem value, but the
// defensive check catches an accidental zero-pointer assignment cleanly.
func (r *registry) Register(s Subsystem) error {
	if s == nil {
		return fmt.Errorf("Register: subsystem is nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	name := s.Name()
	if name == "" {
		return fmt.Errorf("Register: subsystem returned empty Name()")
	}
	if _, exists := r.entries[name]; exists {
		return fmt.Errorf("subsystem %q already registered", name)
	}

	r.entries[name] = &subsystemEntry{
		sub: s,
		health: SubsystemHealth{
			State: "registered",
		},
	}
	r.order = append(r.order, name)
	return nil
}

// Enable transitions a registered or disabled subsystem to "enabled" by
// calling Subsystem.Start with the Dependencies bundle installed via
// SetDependencies. Enabling an already-enabled subsystem is a no-op.
// Returns an error if the subsystem is unknown or if Start returns an error.
//
// Callers that need real Dependencies (Bus/Queue/Meter/DB/Logger) MUST
// install them via SetDependencies before invoking Enable. NoOps tolerate
// the zero Dependencies value — see noop.go Start methods — so a test that
// only exercises NoOps may skip SetDependencies safely.
// Enable runs Subsystem.Start OUTSIDE the registry lock so the subsystem
// can call back into deps.Registry (List/Get/Health/ResolveImpls/ResolvePolicy)
// during its Start without deadlocking. The lock is held only long enough to
// validate the state transition and snapshot the impl + deps; after Start
// returns, the lock is re-acquired to commit the state. A concurrent
// Register/Disable between these two critical sections is tolerated — the
// commit re-fetches the entry by name and silently skips if the subsystem
// was removed in flight.
func (r *registry) Enable(name string) error {
	r.mu.Lock()
	entry, exists := r.entries[name]
	if !exists {
		r.mu.Unlock()
		return fmt.Errorf("subsystem %q not registered", name)
	}
	if err := stateTransition(entry.health.State, "enabled"); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("Enable %q: %w", name, err)
	}
	if entry.health.State == "enabled" {
		// Already enabled — no Start, no state change.
		r.mu.Unlock()
		return nil
	}
	sub := entry.sub
	deps := r.deps
	r.mu.Unlock()

	// Call Start without the lock held. Subsystem.Start may now safely
	// invoke deps.Registry.List/Get/Health/ResolvePolicy etc.
	if err := sub.Start(context.Background(), deps); err != nil {
		return fmt.Errorf("subsystem %q Start: %w", name, err)
	}

	// Commit the state transition under the lock. Re-fetch in case the
	// subsystem was unregistered or transitioned to "failed" concurrently.
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.entries[name]; ok && cur == entry {
		// Only commit if state still allows enable. A racing TransitionToFailed
		// must win — operator must Disable+Enable to recover from failed.
		if cur.health.State == "registered" || cur.health.State == "disabled" {
			cur.health.State = "enabled"
		}
	}
	return nil
}

// Disable runs Subsystem.Stop OUTSIDE the registry lock for the same reason
// Enable does: Stop may legitimately query deps.Registry to inspect sibling
// subsystem state during teardown. The lock is held only for state-transition
// validation and final commit.
func (r *registry) Disable(name string) error {
	r.mu.Lock()
	entry, exists := r.entries[name]
	if !exists {
		r.mu.Unlock()
		return fmt.Errorf("subsystem %q not registered", name)
	}
	if err := stateTransition(entry.health.State, "disabled"); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("Disable %q: %w", name, err)
	}
	// failed → disabled is the operator-driven recovery transition: the
	// subsystem already panicked and the dispatcher has flipped it to
	// "failed", so Stop must NOT run a second time. Update the state
	// directly under the lock and return.
	if entry.health.State == "failed" {
		entry.health.State = "disabled"
		r.mu.Unlock()
		return nil
	}
	if entry.health.State != "enabled" {
		// registered or disabled — nothing to Stop.
		r.mu.Unlock()
		return nil
	}
	sub := entry.sub
	r.mu.Unlock()

	if err := sub.Stop(); err != nil {
		return fmt.Errorf("subsystem %q Stop: %w", name, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.entries[name]; ok && cur == entry {
		if cur.health.State == "enabled" {
			cur.health.State = "disabled"
		}
	}
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
// TransitionToFailed flips the named subsystem into the ADR-009 "failed"
// state and records the supplied reason on SubsystemHealth.PanicReason +
// LastPanic timestamp + ErrorsTotal counter. Once failed, ResolveImpls and
// Dispatch treat the subsystem as NoOp until the operator runs
// Disable + Enable to recover it. Calling on an unknown name is a no-op
// (returns nil) so concurrent Unregister/Failed sequences stay idempotent
// at the caller.
func (r *registry) TransitionToFailed(name string, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.entries[name]
	if !ok {
		return nil
	}
	e.health.State = "failed"
	e.health.PanicReason = reason
	e.health.LastPanic = time.Now().UTC()
	e.health.ErrorsTotal++
	return nil
}

// not part of the SubsystemRegistry interface because callers that only need
// lifecycle management should not need dispatch.
func (r *registry) ResolveImpls(interfaceName string) []Subsystem {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Iterate via the registration-order slice so PolicySinglePrimary's
	// "last-registered enabled wins" semantics stay deterministic (map
	// iteration order is intentionally randomized in Go).
	var out []Subsystem
	for _, name := range r.order {
		e, ok := r.entries[name]
		if !ok {
			continue
		}
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
		case "enabled", "disabled", "registered", "failed":
			// "failed" → "disabled" is the operator-driven recovery path
			// per ADR-009: a failed subsystem returns to "disabled" via
			// Disable, then to "enabled" via Enable. Without this
			// transition operators have no way to recover from panics
			// short of restarting the process.
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
