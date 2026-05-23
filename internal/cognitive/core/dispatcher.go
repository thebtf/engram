package core

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/thebtf/engram/pkg/cognitive"
)

// maxStackTraceBytes caps the panic stack-trace size written to the structured
// log. Most panic stacks are well below this; the cap prevents a runaway
// recursive panic from blowing log volume.
const maxStackTraceBytes = 4096

// SubsystemDispatcher routes Dispatch calls to enabled subsystems implementing
// a named cross-subsystem interface, with per-impl panic isolation per PR-5.
// FanOut and SinglePrimary semantics are picked from
// SubsystemRegistry.ResolvePolicy. Panics are recovered, recorded via
// SubsystemMeter, and the offending subsystem is moved to the ADR-009
// "failed" state via the registry.
type SubsystemDispatcher struct {
	registry SubsystemRegistry
	meter    SubsystemMeter
}

// NewSubsystemDispatcher constructs a SubsystemDispatcher wired against the
// supplied registry + meter. Both must be non-nil; passing nil panics
// immediately (NewService is the canonical caller and supplies both).
func NewSubsystemDispatcher(registry SubsystemRegistry, meter SubsystemMeter) *SubsystemDispatcher {
	if registry == nil {
		panic("core.NewSubsystemDispatcher: registry must be non-nil")
	}
	if meter == nil {
		panic("core.NewSubsystemDispatcher: meter must be non-nil")
	}
	return &SubsystemDispatcher{registry: registry, meter: meter}
}

// Dispatch invokes every enabled implementation of interfaceName (per
// SubsystemRegistry.ResolveImpls) using the supplied per-impl function. The
// dispatch semantics are determined by registry.ResolvePolicy(interfaceName):
//
//   - PolicyFanOut: every enabled impl is called in parallel. fn errors are
//     aggregated via errors.Join; individual panics are recovered + recorded
//   - the impl is transitioned to failed without aborting siblings.
//   - PolicySinglePrimary: only the LAST registered enabled impl is invoked.
//     This matches T011 NoOps' "last-registered-wins" registration order so
//     real subsystems (S1, S3, S4a, etc.) override the NoOp baseline once
//     they Enable.
//
// Implementation reflection: Dispatch is parameterised on T to give callers
// a static type check at the call site (no `interface{}` round-trips). The
// caller's fn receives the concrete impl value as T; if a registered
// subsystem does not satisfy T, the wrapper returns a typed error (treated
// as a misconfiguration rather than a panic).
//
// Dispatch returns nil when every called impl returns nil; otherwise the
// joined error chain via errors.Join.
func Dispatch[T any](
	ctx context.Context,
	d *SubsystemDispatcher,
	interfaceName string,
	fn func(impl T) error,
) error {
	if d == nil {
		return errors.New("core.Dispatch: dispatcher is nil")
	}

	// ResolveImpls lives on the concrete *registry, not on the SubsystemRegistry
	// interface (interface stays minimal so unrelated callers do not depend on
	// dispatch). Type-assert here; an interface substitute that lacks the
	// method is a contract violation surfaced as an explicit error.
	type implsResolver interface {
		ResolveImpls(interfaceName string) []Subsystem
	}
	resolver, ok := d.registry.(implsResolver)
	if !ok {
		return errors.New("core.Dispatch: registry does not implement ResolveImpls")
	}
	impls := resolver.ResolveImpls(interfaceName)
	if len(impls) == 0 {
		return nil
	}

	// Bridge the type-parameterised closure to a Subsystem-typed wrapper so
	// the helper machinery below stays generic-agnostic. The assertion
	// happens once per impl inside the safeCall recover boundary.
	wrapper := func(impl Subsystem) error {
		typed, ok := any(impl).(T)
		if !ok {
			return fmt.Errorf(
				"core.Dispatch: subsystem %q registered for %q does not satisfy the requested type",
				impl.Name(), interfaceName,
			)
		}
		return fn(typed)
	}

	policy := d.registry.ResolvePolicy(interfaceName)
	switch policy {
	case cognitive.PolicyFanOut:
		return d.dispatchFanOut(ctx, interfaceName, impls, wrapper)
	default:
		// Unknown policy defaults to SinglePrimary — the safest least-impact
		// choice (matches ResolvePolicy's documented fallback).
		return d.dispatchSinglePrimary(ctx, interfaceName, impls, wrapper)
	}
}

// dispatchFanOut runs wrapper concurrently against every impl. Each call is
// wrapped in safeCall's panic-recovery boundary; sibling errors are joined.
func (d *SubsystemDispatcher) dispatchFanOut(
	ctx context.Context,
	interfaceName string,
	impls []Subsystem,
	wrapper func(Subsystem) error,
) error {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, impl := range impls {
		impl := impl
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.safeCall(ctx, interfaceName, impl, wrapper); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// dispatchSinglePrimary picks the last-registered enabled impl. ResolveImpls
// preserves registration order; the tail entry is the latest registration.
func (d *SubsystemDispatcher) dispatchSinglePrimary(
	ctx context.Context,
	interfaceName string,
	impls []Subsystem,
	wrapper func(Subsystem) error,
) error {
	primary := impls[len(impls)-1]
	return d.safeCall(ctx, interfaceName, primary, wrapper)
}

// safeCall invokes wrapper on impl with a deferred recover boundary. On
// panic: emits a structured log event with the recovered reason and a
// truncated stack trace, increments subsystem_panic_total, transitions the
// subsystem to "failed", and returns a wrapped error so the caller learns
// the failure happened (PR-5 isolation does not equal silence).
func (d *SubsystemDispatcher) safeCall(
	ctx context.Context,
	interfaceName string,
	impl Subsystem,
	wrapper func(Subsystem) error,
) (err error) {
	defer func() {
		if r := recover(); r != nil {
			reason := fmt.Sprintf("%v", r)
			stack := truncateStack(debug.Stack(), maxStackTraceBytes)
			log.Error().
				Str("subsystem", impl.Name()).
				Str("interface", interfaceName).
				Str("panic_reason", reason).
				Str("stack", string(stack)).
				Msg("subsystem panic recovered in Dispatch")
			d.meter.IncrCounter("subsystem_panic_total", 1, map[string]string{
				"subsystem": impl.Name(),
			})
			if terr := d.registry.TransitionToFailed(impl.Name(), reason); terr != nil {
				log.Warn().
					Err(terr).
					Str("subsystem", impl.Name()).
					Msg("TransitionToFailed returned error")
			}
			err = fmt.Errorf("subsystem %q panicked: %s", impl.Name(), reason)
		}
	}()

	// ctx kept available for handler use; we explicitly do not enforce a
	// timeout here — the caller (S3 ambient handler, etc.) owns the deadline.
	_ = ctx
	return wrapper(impl)
}

// truncateStack returns at most maxBytes of stack so log volumes stay bounded.
func truncateStack(stack []byte, maxBytes int) []byte {
	if len(stack) <= maxBytes {
		return stack
	}
	return stack[:maxBytes]
}
