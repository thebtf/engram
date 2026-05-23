package core

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newTestQueue returns a hintQueue wired for testing:
//   - injectable clock so expiry tests don't sleep
//   - short sweepInterval so the sweeper fires quickly in tests
func newTestQueue(clk func() time.Time, sweepInterval time.Duration) *hintQueue {
	q := NewHintQueue()
	if clk != nil {
		q.clock = clk
	}
	if sweepInterval > 0 {
		q.sweepInterval = sweepInterval
	}
	return q
}

// makeHint builds a HintProposalPayload with a given id and CreatedAt.
func makeHint(id string, createdAt time.Time) HintProposalPayload {
	return HintProposalPayload{
		ID:        id,
		Title:     "hint-" + id,
		CreatedAt: createdAt,
	}
}

// --------------------------------------------------------------------------
// TestEnqueueDrain_FIFO
// Enqueue 3 entries, drain 2 → get first 2; drain 5 → get the remaining 1.
// --------------------------------------------------------------------------

func TestEnqueueDrain_FIFO(t *testing.T) {
	q := newTestQueue(nil, 0)
	ctx := context.Background()
	sid := "session-fifo"
	now := time.Now()

	h1 := makeHint("h1", now)
	h2 := makeHint("h2", now)
	h3 := makeHint("h3", now)

	if err := q.Enqueue(ctx, sid, h1); err != nil {
		t.Fatalf("Enqueue h1: %v", err)
	}
	if err := q.Enqueue(ctx, sid, h2); err != nil {
		t.Fatalf("Enqueue h2: %v", err)
	}
	if err := q.Enqueue(ctx, sid, h3); err != nil {
		t.Fatalf("Enqueue h3: %v", err)
	}

	// First drain: max=2 → must return h1, h2 in order.
	got := q.Drain(sid, 2)
	if len(got) != 2 {
		t.Fatalf("Drain(2): got %d entries, want 2", len(got))
	}
	if got[0].ID != "h1" || got[1].ID != "h2" {
		t.Fatalf("Drain(2): got IDs [%s %s], want [h1 h2]", got[0].ID, got[1].ID)
	}

	// Second drain: max=5 → must return the remaining h3 only.
	got = q.Drain(sid, 5)
	if len(got) != 1 {
		t.Fatalf("Drain(5) after partial drain: got %d entries, want 1", len(got))
	}
	if got[0].ID != "h3" {
		t.Fatalf("Drain(5): got ID %q, want h3", got[0].ID)
	}

	// Queue is now empty.
	got = q.Drain(sid, 10)
	if len(got) != 0 {
		t.Fatalf("Drain after empty: got %d entries, want 0", len(got))
	}
}

// --------------------------------------------------------------------------
// TestOverflow_DropsOldest (EC-4)
// Enqueue 51 entries into a cap=50 queue. The oldest entry (first enqueued)
// must be evicted. Overflows and Evicted counters must each equal 1.
// --------------------------------------------------------------------------

func TestOverflow_DropsOldest(t *testing.T) {
	q := newTestQueue(nil, 0)
	ctx := context.Background()
	sid := "session-overflow"
	now := time.Now()

	const cap = 50

	// Enqueue cap+1 entries; the first one should be evicted.
	for i := 0; i < cap+1; i++ {
		id := string(rune('a'+i%26)) + string(rune('a'+i/26))
		h := makeHint(id, now.Add(time.Duration(i)*time.Millisecond))
		if err := q.Enqueue(ctx, sid, h); err != nil {
			t.Fatalf("Enqueue[%d]: %v", i, err)
		}
	}

	stats := q.Stats(sid)
	if stats.QueuedNow != cap {
		t.Errorf("QueuedNow = %d, want %d", stats.QueuedNow, cap)
	}
	if stats.Overflows != 1 {
		t.Errorf("Overflows = %d, want 1", stats.Overflows)
	}
	if stats.Evicted != 1 {
		t.Errorf("Evicted = %d, want 1", stats.Evicted)
	}

	// The first enqueued entry (index 0 = "aa") must have been evicted.
	drained := q.Drain(sid, cap)
	if len(drained) != cap {
		t.Fatalf("Drain returned %d entries, want %d", len(drained), cap)
	}
	for _, e := range drained {
		if e.ID == "aa" {
			t.Error("oldest entry 'aa' survived but should have been evicted")
		}
	}
}

// --------------------------------------------------------------------------
// TestExpiry_RemovesAged
// Uses injectable clock: entries created at baseTime are stale when
// clock() returns baseTime + 31 minutes. After a manual sweep, those
// entries are removed.
// --------------------------------------------------------------------------

func TestExpiry_RemovesAged(t *testing.T) {
	baseTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	// Clock that always returns baseTime + 31 min: all entries look stale.
	advancedClock := func() time.Time { return baseTime.Add(31 * time.Minute) }

	q := newTestQueue(advancedClock, 10*time.Millisecond)
	ctx := context.Background()
	sid := "session-expiry"

	// Enqueue entries with CreatedAt = baseTime (31 min ago from clock).
	for i := 0; i < 3; i++ {
		h := makeHint(string(rune('a'+i)), baseTime)
		if err := q.Enqueue(ctx, sid, h); err != nil {
			t.Fatalf("Enqueue[%d]: %v", i, err)
		}
	}

	// Verify entries present before sweep.
	stats := q.Stats(sid)
	if stats.QueuedNow != 3 {
		t.Fatalf("before sweep: QueuedNow = %d, want 3", stats.QueuedNow)
	}

	// Trigger sweep manually via exported-test helper (we call the private method
	// directly since the test is in the same package).
	q.sweep()

	stats = q.Stats(sid)
	if stats.QueuedNow != 0 {
		t.Errorf("after sweep: QueuedNow = %d, want 0", stats.QueuedNow)
	}
}

// --------------------------------------------------------------------------
// TestStop_StopsSweeper
// Start the sweeper, wait briefly, then Stop. The sweeper goroutine must
// exit within a reasonable timeout (200ms).
// --------------------------------------------------------------------------

func TestStop_StopsSweeper(t *testing.T) {
	q := newTestQueue(nil, 10*time.Millisecond)

	ctx := context.Background()
	if err := q.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Idempotent second Start must return nil without spawning another goroutine.
	if err := q.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}

	// Give the sweeper time to run at least once.
	time.Sleep(25 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Stop() //nolint:errcheck
	}()

	select {
	case <-done:
		// Good — Stop returned.
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Stop did not return within 200ms — sweeper goroutine leak suspected")
	}

	// Idempotent second Stop must return nil.
	if err := q.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// --------------------------------------------------------------------------
// TestImportBoundary_NoWorkerImport
// Parses hint_queue.go via go/parser, walks all ast.ImportSpec nodes, and
// asserts that none of the import paths equal
// "github.com/thebtf/engram/internal/worker".
// --------------------------------------------------------------------------

func TestImportBoundary_NoWorkerImport(t *testing.T) {
	// Locate the package directory via this test file's own source location.
	// The boundary applies to EVERY non-test CORE source file, not just
	// hint_queue.go — a future file under internal/cognitive/core/ that
	// imports internal/worker would equally break the substrate boundary, so
	// the test walks the whole directory rather than naming hint_queue.go.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	packageDir := filepath.Dir(thisFile)

	const forbidden = "github.com/thebtf/engram/internal/worker"

	checkFile := func(t *testing.T, srcFile string) {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, srcFile, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", srcFile, err)
		}
		base := filepath.Base(srcFile)
		for _, imp := range f.Imports {
			if imp.Path == nil {
				continue
			}
			path := strings.Trim(imp.Path.Value, `"`)
			if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
				t.Errorf("%s imports %q — CORE package MUST NOT import internal/worker", base, path)
			}
		}
		// Defensive AST walk in case a future Go release changes ImportSpec
		// placement (e.g., nested in conditional compilation blocks).
		ast.Inspect(f, func(n ast.Node) bool {
			spec, ok := n.(*ast.ImportSpec)
			if !ok || spec.Path == nil {
				return true
			}
			path := strings.Trim(spec.Path.Value, `"`)
			if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
				t.Errorf("ast walk: %s imports %q", base, path)
			}
			return true
		})
	}

	walkErr := filepath.Walk(packageDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if path != packageDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			checkFile(t, path)
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("filepath.Walk: %v", walkErr)
	}
}

// --------------------------------------------------------------------------
// TestHintQueue_GoroutineLeak_None
// Records runtime.NumGoroutine() before Start. Starts, then Stops the queue.
// After a 100ms grace period, goroutine count must equal the baseline.
// (Named with HintQueue prefix to avoid collision with TestGoroutineLeak_None
// in event_bus_test.go, which tests the same property for AttentionEventBus.)
// --------------------------------------------------------------------------

func TestHintQueue_GoroutineLeak_None(t *testing.T) {
	// Establish baseline before creating the queue under test.
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	q := newTestQueue(nil, 10*time.Millisecond)
	ctx := context.Background()

	if err := q.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Let the sweeper tick a couple of times.
	time.Sleep(25 * time.Millisecond)

	if err := q.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Allow goroutine runtime to quiesce.
	time.Sleep(100 * time.Millisecond)
	runtime.GC()

	after := runtime.NumGoroutine()
	if after > baseline {
		t.Errorf("goroutine leak: before Start=%d, after Stop=%d (delta=%d)",
			baseline, after, after-baseline)
	}
}
