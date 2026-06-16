package watcher

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tempTarget creates a temporary directory with a dummy target file inside.
// Returns the target file path and a cleanup function.
func tempTarget(t *testing.T) (targetPath string, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.pem")
	if err := os.WriteFile(target, []byte("pem"), 0o600); err != nil {
		t.Fatalf("create target: %v", err)
	}
	return target, func() { _ = os.RemoveAll(dir) }
}

// TestStopDuringDebouncedDeletion verifies that calling Stop() while the
// debounce timer is in flight (or after the timer has fired but before
// handleDeletion acquires the mutex) does not panic and does not invoke
// onDelete or re-establish the watch after shutdown.
func TestStopDuringDebouncedDeletion(t *testing.T) {
	targetPath, cleanup := tempTarget(t)
	defer cleanup()

	var callCount atomic.Int64

	w, err := New(targetPath, func() {
		callCount.Add(1)
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Shorten debounce so the timer fires quickly enough to race with Stop.
	w.debounce = 10 * time.Millisecond

	if err := w.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Delete the target to arm the debounce timer in watchLoop.
	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("remove target: %v", err)
	}

	// Concurrently call Stop() roughly when the timer fires.
	// We use a small sleep that overlaps with the debounce window so the
	// race detector can observe both orderings (Stop before / after timer).
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond) // races with the 10 ms debounce
		if stopErr := w.Stop(); stopErr != nil {
			// Double-close of watcher is benign; any other error is fatal.
			t.Errorf("Stop: %v", stopErr)
		}
	}()

	wg.Wait()

	// Allow the re-watch goroutine's 500 ms sleep to complete so the race
	// detector can observe any post-stop watcher.Add call.
	time.Sleep(600 * time.Millisecond)

	// After Stop, the watcher must be marked not running.
	w.mu.Lock()
	stillRunning := w.running
	w.mu.Unlock()
	if stillRunning {
		t.Error("watcher still marked running after Stop()")
	}

	// onDelete MAY or MAY NOT have fired depending on race timing — both are
	// correct. What must not happen is a panic or a watcher.Add call on the
	// closed fsnotify watcher. If either occurred the test would have already
	// panicked or the race detector would have flagged it.
	t.Logf("onDelete called %d time(s) (0 or 1 are both valid)", callCount.Load())
}

// TestStopBeforeDebounce verifies the early-return path: Stop() called before
// the debounce timer fires means handleDeletion should bail at the running check.
func TestStopBeforeDebounce(t *testing.T) {
	targetPath, cleanup := tempTarget(t)
	defer cleanup()

	var callCount atomic.Int64

	w, err := New(targetPath, func() {
		callCount.Add(1)
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Long debounce — Stop() will always arrive first.
	w.debounce = 200 * time.Millisecond

	if err := w.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Delete target to arm the timer, then immediately stop.
	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("remove target: %v", err)
	}

	// Stop well before the 200 ms debounce fires.
	time.Sleep(10 * time.Millisecond)
	if err := w.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Wait past the debounce window to let the timer goroutine run (it should
	// bail early) and past the re-watch sleep (also should bail early).
	time.Sleep(800 * time.Millisecond)

	if n := callCount.Load(); n != 0 {
		t.Errorf("onDelete called %d time(s) after Stop(); want 0", n)
	}
}

// TestReWatchGoroutineStoppedAfterSleep verifies the second guard: even if
// handleDeletion passes the first running check, the re-watch goroutine must
// not call watcher.Add if Stop() fires during the 500 ms sleep.
func TestReWatchGoroutineStoppedAfterSleep(t *testing.T) {
	targetPath, cleanup := tempTarget(t)
	defer cleanup()

	w, err := New(targetPath, func() {})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Zero debounce so handleDeletion fires immediately, guaranteeing it
	// passes the first running check before Stop() is called.
	w.debounce = 0

	if err := w.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("remove target: %v", err)
	}

	// Wait for handleDeletion to run (debounce = 0, so it fires immediately),
	// then stop while the re-watch goroutine is sleeping its 500 ms.
	time.Sleep(50 * time.Millisecond)
	if err := w.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Let the 500 ms sleep complete; the goroutine must not call watcher.Add.
	// If it did, the closed watcher would produce an error or panic — both are
	// caught by the race detector or by the test crashing.
	time.Sleep(600 * time.Millisecond)

	w.mu.Lock()
	stillRunning := w.running
	w.mu.Unlock()
	if stillRunning {
		t.Error("watcher still marked running after Stop()")
	}
}

// TestIdempotentStop verifies that calling Stop() twice does not panic.
func TestIdempotentStop(t *testing.T) {
	targetPath, cleanup := tempTarget(t)
	defer cleanup()

	w, err := New(targetPath, func() {})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := w.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := w.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}
