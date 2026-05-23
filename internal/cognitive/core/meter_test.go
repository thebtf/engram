package core

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestIncrCounter_Atomic verifies that concurrent goroutines incrementing the
// same counter produce the correct sum. Tests atomic safety.
func TestIncrCounter_Atomic(t *testing.T) {
	m := NewLocalMeter()
	const goroutines = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			m.IncrCounter("calls_total", 1, nil)
		}()
	}
	wg.Wait()

	snap := m.Snapshot()
	got := snap.Counters["calls_total"]
	if got != goroutines {
		t.Fatalf("calls_total = %d, want %d", got, goroutines)
	}
}

// TestObserveHistogram_PercentilesCorrect inserts 10 known values and checks
// that P50, P95, P99 match the nearest-rank expectations.
func TestObserveHistogram_PercentilesCorrect(t *testing.T) {
	m := NewLocalMeter()
	// Insert values 1.0 through 10.0 in shuffled order to verify sort.
	for _, v := range []float64{7, 3, 1, 9, 5, 2, 8, 4, 6, 10} {
		m.ObserveHistogram("latency_ns_histogram", v, nil)
	}

	snap := m.Snapshot()
	h, ok := snap.Histograms["latency_ns_histogram"]
	if !ok {
		t.Fatal("histogram series missing from snapshot")
	}
	if h.Count != 10 {
		t.Fatalf("Count = %d, want 10", h.Count)
	}

	// Nearest-rank method on sorted [1,2,3,4,5,6,7,8,9,10] (n=10):
	//   P50: ceil(0.50*10)=5 → index 4 → value 5
	//   P95: ceil(0.95*10)=10 → index 9 → value 10
	//   P99: ceil(0.99*10)=10 → index 9 → value 10
	if h.P50 != 5.0 {
		t.Errorf("P50 = %f, want 5.0", h.P50)
	}
	if h.P95 != 10.0 {
		t.Errorf("P95 = %f, want 10.0", h.P95)
	}
	if h.P99 != 10.0 {
		t.Errorf("P99 = %f, want 10.0", h.P99)
	}
}

// TestSnapshot_DeepCopy verifies that mutating a returned MetricsSnapshot does
// not affect subsequent snapshots from the same meter.
func TestSnapshot_DeepCopy(t *testing.T) {
	m := NewLocalMeter()
	m.IncrCounter("calls_total", 5, nil)

	snap1 := m.Snapshot()
	original := snap1.Counters["calls_total"]
	if original != 5 {
		t.Fatalf("pre-mutation value = %d, want 5", original)
	}

	// Mutate the returned map.
	snap1.Counters["calls_total"] = 999

	// A fresh snapshot must reflect the real stored value (5), not 999.
	snap2 := m.Snapshot()
	got := snap2.Counters["calls_total"]
	if got != 5 {
		t.Fatalf("post-mutation snapshot = %d, want 5 (deep copy violated)", got)
	}
}

// TestRaceUnderConcurrentWrites is a -race test: N goroutines mix IncrCounter,
// ObserveHistogram, and Snapshot calls concurrently. The test passes when no
// data race is detected by the race detector.
func TestRaceUnderConcurrentWrites(t *testing.T) {
	m := NewLocalMeter()
	const workers = 32

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				m.IncrCounter("event_emitted", 1, nil)
				m.ObserveHistogram("latency_ns_histogram", float64(j), nil)
				_ = m.Snapshot()
			}
		}(i)
	}
	wg.Wait()
}

// forbiddenVocabPattern matches any of the 5 S5-owned product metric terms
// that must never appear in CORE source files.
var forbiddenVocabPattern = regexp.MustCompile(
	`\b(precision|miss_rate|burden|freshness|accepted_hint_action)\b`,
)

// TestForbiddenVocabulary_StdlibGate walks all .go files in the
// internal/cognitive/core package directory (excluding _test.go files and
// interfaces.go), reads each file, and asserts that none contain any of the 5
// forbidden S5-owned vocabulary terms. Uses only stdlib: filepath.Walk,
// os.ReadFile, regexp — no exec.Command, no shell-out.
//
// interfaces.go is explicitly excluded: it is the boundary declaration file
// that defines ProductMetricsProvider and must name S5 product metric terms in
// its documentation to describe the boundary. Implementation files (meter.go,
// registry.go, event_bus.go, hint_queue.go, flag_config.go, noop.go,
// dispatcher.go, …) must not carry those terms per ADR-008.
func TestForbiddenVocabulary_StdlibGate(t *testing.T) {
	// Derive package directory from the test file's own path.
	_, selfFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot determine package directory")
	}
	packageDir := filepath.Dir(selfFile)

	var violations []string

	walkErr := filepath.Walk(packageDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Only descend into the package dir itself, not subdirs.
			if path != packageDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip test files — gate applies to non-test CORE source only.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Skip interfaces.go — it is the ADR-008 boundary declaration file.
		// It intentionally names S5 product metric terms in doc comments for
		// ProductMetricsProvider to document what belongs to S5. That naming
		// is required context, not a leak.
		if filepath.Base(path) == "interfaces.go" {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		matches := forbiddenVocabPattern.FindAll(data, -1)
		for _, m := range matches {
			violations = append(violations,
				filepath.Base(path)+": forbidden term \""+string(m)+"\"",
			)
		}
		return nil
	})

	if walkErr != nil {
		t.Fatalf("filepath.Walk failed: %v", walkErr)
	}

	if len(violations) > 0 {
		t.Errorf("CORE source files contain forbidden S5 vocabulary (%d violation(s)):", len(violations))
		for _, v := range violations {
			t.Errorf("  %s", v)
		}
	}
}
