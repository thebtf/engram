package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/registry"
)

// ---------------------------------------------------------------------------
// Fake module for tests
// ---------------------------------------------------------------------------

type fakeMod struct {
	name           string
	initErr        error
	shutdownCalled *int32 // atomic counter — nil means "don't track"
	initOrder      *[]string
	shutOrder      *[]string
	panicOnInit    bool
	blockCtx       bool // sleep until ctx.Done or 35 s
}

func (f *fakeMod) Name() string { return f.name }

func (f *fakeMod) Init(ctx context.Context, _ module.ModuleDeps) error {
	if f.initOrder != nil {
		*f.initOrder = append(*f.initOrder, f.name)
	}
	if f.panicOnInit {
		panic("deliberate test panic in " + f.name)
	}
	if f.blockCtx {
		// Block until context is cancelled or 35 s — whichever comes first.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(35 * time.Second):
			return nil
		}
	}
	return f.initErr
}

func (f *fakeMod) Shutdown(_ context.Context) error {
	if f.shutdownCalled != nil {
		atomic.AddInt32(f.shutdownCalled, 1)
	}
	if f.shutOrder != nil {
		*f.shutOrder = append(*f.shutOrder, f.name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helper: build a registry with n sequential fake modules.
// ---------------------------------------------------------------------------

func buildRegistry(t *testing.T, mods ...module.EngramModule) *registry.Registry {
	t.Helper()
	r := registry.New()
	for _, m := range mods {
		if err := r.Register(m); err != nil {
			t.Fatalf("Register %q: %v", m.Name(), err)
		}
	}
	r.Freeze()
	return r
}

func noopDeps(_ string) module.ModuleDeps {
	return module.ModuleDeps{
		Logger:    slog.Default(),
		DaemonCtx: context.Background(),
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestStart_CallsInitInRegistrationOrder(t *testing.T) {
	var order []string
	m1 := &fakeMod{name: "m1", initOrder: &order}
	m2 := &fakeMod{name: "m2", initOrder: &order}
	m3 := &fakeMod{name: "m3", initOrder: &order}

	r := buildRegistry(t, m1, m2, m3)
	p := New(r, slog.Default())

	if err := p.Start(context.Background(), noopDeps); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}
	want := []string{"m1", "m2", "m3"}
	if !sliceEqual(order, want) {
		t.Errorf("init order: got %v, want %v", order, want)
	}
}

func TestStart_InitErrorOnModule3_Shutdowns1And2InReverse(t *testing.T) {
	var shutOrder []string
	var sc1, sc2 int32
	m1 := &fakeMod{name: "m1", shutdownCalled: &sc1, shutOrder: &shutOrder}
	m2 := &fakeMod{name: "m2", shutdownCalled: &sc2, shutOrder: &shutOrder}
	m3 := &fakeMod{name: "m3", initErr: errors.New("m3 failed")}
	m4 := &fakeMod{name: "m4"}
	m5 := &fakeMod{name: "m5"}

	r := buildRegistry(t, m1, m2, m3, m4, m5)
	p := New(r, slog.Default())

	err := p.Start(context.Background(), noopDeps)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// m1 and m2 must have been shut down, m4 and m5 must NOT.
	if atomic.LoadInt32(&sc1) != 1 {
		t.Errorf("m1 Shutdown: called %d times, want 1", sc1)
	}
	if atomic.LoadInt32(&sc2) != 1 {
		t.Errorf("m2 Shutdown: called %d times, want 1", sc2)
	}
	// Shutdown order should be reverse: m2, m1.
	want := []string{"m2", "m1"}
	if !sliceEqual(shutOrder, want) {
		t.Errorf("shutdown order: got %v, want %v", shutOrder, want)
	}
}

func TestShutdownAll_ReverseOrder(t *testing.T) {
	var shutOrder []string
	m1 := &fakeMod{name: "m1", shutOrder: &shutOrder}
	m2 := &fakeMod{name: "m2", shutOrder: &shutOrder}
	m3 := &fakeMod{name: "m3", shutOrder: &shutOrder}

	r := buildRegistry(t, m1, m2, m3)
	p := New(r, slog.Default())

	if err := p.Start(context.Background(), noopDeps); err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutOrder = shutOrder[:0] // reset after Start (no shutdown during Start in success path)

	if err := p.ShutdownAll(context.Background()); err != nil {
		t.Fatalf("ShutdownAll: unexpected error: %v", err)
	}
	want := []string{"m3", "m2", "m1"}
	if !sliceEqual(shutOrder, want) {
		t.Errorf("shutdown order: got %v, want %v", shutOrder, want)
	}
}

func TestStart_PanicInInit_ConvertedToError_NoCrash(t *testing.T) {
	m := &fakeMod{name: "panicker", panicOnInit: true}
	r := buildRegistry(t, m)
	p := New(r, slog.Default())

	err := p.Start(context.Background(), noopDeps)
	if err == nil {
		t.Fatal("expected error from panicking module, got nil")
	}
}

// TestNFR2_InitTimeout_30s verifies FR-14 / NFR-2: a module that would block
// for 35 s is aborted by a 30 s context deadline well before 35 s.
//
// Note: this test uses a real 30 s timeout in production mode. For fast CI we
// instead verify with a 200 ms timeout and a module that blocks for 1 s. The
// structural invariant is the same: the pipeline propagates ctx cancellation
// to Init. The 30 s constant lives in cmd/engram/wiring.go (Phase 5).
func TestNFR2_InitTimeout_ContextCancelledBeforeModuleUnblocks(t *testing.T) {
	m := &fakeMod{name: "slow", blockCtx: true}
	r := buildRegistry(t, m)
	p := New(r, slog.Default())

	deadline := 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	start := time.Now()
	err := p.Start(ctx, noopDeps)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	// Must complete well before the 1 s blocking sleep — allow 3× the deadline.
	if elapsed > 3*deadline {
		t.Errorf("Start took %v; expected < %v (3× deadline)", elapsed, 3*deadline)
	}
}

// panicOnShutdownMod panics in its Shutdown method.
type panicOnShutdownMod struct {
	name string
}

func (f *panicOnShutdownMod) Name() string                                       { return f.name }
func (f *panicOnShutdownMod) Init(_ context.Context, _ module.ModuleDeps) error { return nil }
func (f *panicOnShutdownMod) Shutdown(_ context.Context) error {
	panic("deliberate shutdown panic in " + f.name)
}

func TestShutdownAll_PanicInShutdown_DoesNotAbortFanout(t *testing.T) {
	var shutOrder []string
	good1 := &fakeMod{name: "good1", shutOrder: &shutOrder}
	panicker := &panicOnShutdownMod{name: "panicker"}
	good2 := &fakeMod{name: "good2", shutOrder: &shutOrder}

	r := buildRegistry(t, good1, panicker, good2)
	p := New(r, slog.Default())

	if err := p.Start(context.Background(), noopDeps); err != nil {
		t.Fatalf("Start: %v", err)
	}
	shutOrder = shutOrder[:0]

	// ShutdownAll should not panic even though panicker.Shutdown panics.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("ShutdownAll should not propagate panic, got: %v", r)
			}
		}()
		_ = p.ShutdownAll(context.Background())
	}()

	// good1 and good2 should still have been shut down despite panicker.
	if len(shutOrder) != 2 {
		t.Errorf("expected 2 shutdown calls (good1 + good2), got %d: %v", len(shutOrder), shutOrder)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
