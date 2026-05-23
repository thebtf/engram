package worker

import (
	"context"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/cognitive/core"
)

// TestServiceFields_All4CoreAccessible reflects on the Service struct and
// asserts that the four explicit CORE fields plus the flagConfig field exist
// with their expected types (post-tasks-review Fix #3). Catches accidental
// removal during downstream refactors.
func TestServiceFields_All4CoreAccessible(t *testing.T) {
	// Use the pointer-to-T pattern to avoid the lock-copy vet warning that
	// fires on reflect.TypeOf(Service{}) — Service embeds sync.WaitGroup.
	typ := reflect.TypeOf((*Service)(nil)).Elem()

	wantFields := map[string]string{
		"cognitiveRegistry":       "core.SubsystemRegistry",
		"cognitiveMeter":          "core.SubsystemMeter",
		"cognitiveQueue":          "core.HintQueue",
		"cognitiveBus":            "core.AttentionEventBus",
		"cognitiveQueueLifecycle": "worker.lifecycleQueue",
		"flagConfig":              "core.FlagConfig",
	}

	for fieldName, wantTypeSuffix := range wantFields {
		f, ok := typ.FieldByName(fieldName)
		if !ok {
			t.Errorf("Service missing field %q", fieldName)
			continue
		}
		got := f.Type.String()
		// reflect prints package-qualified type names like
		// "cognitivecore.SubsystemRegistry" because we aliased the import.
		// Match by suffix so the assertion stays robust against alias changes.
		if !hasTypeSuffix(got, wantTypeSuffix) {
			t.Errorf("field %q: got type %q, want suffix %q", fieldName, got, wantTypeSuffix)
		}
	}
}

// hasTypeSuffix compares the type-name part (everything after the LAST '.')
// of actual to the type-name part of suffix. Both arguments are expected to
// be of the form `pkg.TypeName` or just `TypeName`. The strict last-dot
// split avoids false positives from substring matches against unrelated
// types whose names happen to share a tail with the target.
func hasTypeSuffix(actual, suffix string) bool {
	return lastDotPart(actual) == lastDotPart(suffix)
}

// lastDotPart returns the substring after the final '.' in s, or s itself
// when no '.' is present. Equivalent to filepath.Ext-style splitting but
// without importing the path package for a one-line helper.
func lastDotPart(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return s[i+1:]
		}
	}
	return s
}

// TestPlatformWiring_NoOpsRegisteredWithCanonicalNames exercises the wiring
// shape NewService applies: build the four CORE primitives, RegisterNoOps,
// then assert the registry lists exactly the five canonical NoOp names.
// This is the unit-level proof for T014 wiring without spinning up the full
// Service (which requires DB, config, and ENGRAM_AUTH env state).
func TestPlatformWiring_NoOpsRegisteredWithCanonicalNames(t *testing.T) {
	meter := core.NewLocalMeter()
	bus := core.NewAttentionEventBus(meter)
	queue := core.NewHintQueue()
	registry := core.NewRegistry()

	if err := core.RegisterNoOps(registry); err != nil {
		t.Fatalf("RegisterNoOps: %v", err)
	}

	infos := registry.List()
	got := make([]string, 0, len(infos))
	for _, info := range infos {
		got = append(got, info.Name)
	}
	sort.Strings(got)

	want := []string{
		"core.noop.attention_event_writer",
		"core.noop.candidate_proposer",
		"core.noop.directive_distiller",
		"core.noop.hint_emitter",
		"core.noop.state_writer",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NoOp names:\n  got:  %v\n  want: %v", got, want)
	}

	// Keep bus/queue alive references so the test exercises full wiring shape.
	_ = bus
	_ = queue
}

// TestPlatformWiring_HintQueueLifecycle exercises the Start/Stop discipline
// the worker package depends on via the unexported lifecycleQueue interface.
// Runs through the same conversion NewService applies and verifies the
// goroutine count returns to baseline after Stop.
func TestPlatformWiring_HintQueueLifecycle(t *testing.T) {
	queue := core.NewHintQueue()
	lq, ok := any(queue).(lifecycleQueue)
	if !ok {
		t.Fatal("core.NewHintQueue() does not satisfy worker.lifecycleQueue")
	}

	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := lq.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := lq.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Grace window for the sweeper goroutine to drain.
	time.Sleep(100 * time.Millisecond)
	runtime.GC()

	got := runtime.NumGoroutine()
	// Allow ±3 slack for test-runner and GC goroutines.
	if got > baseline+3 {
		t.Errorf("goroutine leak: baseline=%d after-stop=%d (slack ±3)", baseline, got)
	}
}

// TestPlatformWiring_FlagOff_NoOpsRegisteredButDisabled verifies that with
// ENGRAM_V7_PLUG_ENABLED unset, NoOps still appear in the registry (state
// "registered") and FlagConfig.IsPlugEnabled returns false. This proves the
// Enable-gating discipline: registration is unconditional, but flipping
// subsystems to "enabled" depends on the flag.
func TestPlatformWiring_FlagOff_NoOpsRegisteredButDisabled(t *testing.T) {
	// Ensure no master flag in effect during this test.
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "")

	cfg := core.LoadFlagConfigFromEnv()
	if cfg.IsPlugEnabled() {
		t.Fatalf("IsPlugEnabled: got true, want false (env unset)")
	}

	registry := core.NewRegistry()
	if err := core.RegisterNoOps(registry); err != nil {
		t.Fatalf("RegisterNoOps: %v", err)
	}

	infos := registry.List()
	if got := len(infos); got != 5 {
		t.Fatalf("registered count: got %d, want 5", got)
	}
	for _, info := range infos {
		if info.State == "enabled" {
			t.Errorf("subsystem %q is enabled despite master flag off; state=%q",
				info.Name, info.State)
		}
	}
}
