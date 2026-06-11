// Package writelint — T037: EC-F2 concurrent two-phase serialization tests.
// Two-goroutine race and 50-goroutine stress on the same project target.
// All races must not produce data corruption; only one goroutine per conflicting
// Phase2 token may succeed (token is single-use — consumed on first Phase2 call).
package writelint_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/writelint"
	"github.com/thebtf/engram/pkg/models"
)

// ---------------------------------------------------------------------------
// Concurrent MemoryStore stub — thread-safe stub for EC-F2 tests
// ---------------------------------------------------------------------------

// concurrentMemStore is a thread-safe stub implementing MemoryStoreInterface.
type concurrentMemStore struct {
	mu       sync.Mutex
	memories []*models.Memory
	nextID   int64
}

func newConcMemStore(initial ...*models.Memory) *concurrentMemStore {
	s := &concurrentMemStore{nextID: 100}
	s.memories = append(s.memories, initial...)
	return s
}

func (s *concurrentMemStore) List(_ context.Context, _ string, limit int) ([]*models.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*models.Memory, len(s.memories))
	copy(out, s.memories)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *concurrentMemStore) Get(_ context.Context, id int64) (*models.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.memories {
		if m.ID == id {
			cp := *m
			return &cp, nil
		}
	}
	return &models.Memory{ID: id, Content: "stub"}, nil
}

func (s *concurrentMemStore) Create(_ context.Context, m *models.Memory) (*models.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	created := *m
	created.ID = s.nextID
	s.memories = append(s.memories, &created)
	return &created, nil
}

func (s *concurrentMemStore) Update(_ context.Context, m *models.Memory) (*models.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, mem := range s.memories {
		if mem.ID == m.ID {
			cp := *m
			s.memories[i] = &cp
			return &cp, nil
		}
	}
	return m, nil
}

func (s *concurrentMemStore) MarkSuperseded(_ context.Context, olderID, newID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.memories {
		if m.ID == olderID {
			m.Status = "superseded"
			m.SupersededBy = &newID
			return nil
		}
	}
	return nil
}

// concurrentAuditLogger is a thread-safe stub for EC-F2 tests.
type concurrentAuditLogger struct {
	mu      sync.Mutex
	entries []string
}

func (a *concurrentAuditLogger) LogAudit(_ context.Context, _ int64, action, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, action)
	return nil
}

func (a *concurrentAuditLogger) count(action string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, e := range a.entries {
		if e == action {
			n++
		}
	}
	return n
}

// buildConcOrchestrator returns an orchestrator with short-lived tokens for EC-F2 tests.
func buildConcOrchestrator(ms *concurrentMemStore, al *concurrentAuditLogger) (*writelint.Orchestrator, func()) {
	ts := writelint.NewTokenStore(writelint.TokenStoreConfig{
		TTL:             30 * time.Second,
		JanitorInterval: 60 * time.Second,
	})
	orch := writelint.NewOrchestrator(writelint.OrchestratorConfig{
		MemoryStore:  ms,
		AuditLogger:  al,
		TokenStore:   ts,
		DupThreshold: 0.85,
	})
	return orch, ts.Close
}

// ---------------------------------------------------------------------------
// EC-F2: Two-goroutine race on the same token
// ---------------------------------------------------------------------------

// TestConcurrency_TwoGoroutines_SameToken verifies that two concurrent Phase2
// calls with the same token produce exactly one success and one "token already
// used" or "token not found" error. The token is single-use.
func TestConcurrency_TwoGoroutines_SameToken(t *testing.T) {
	ms := newConcMemStore(makeDupMemory())
	al := &concurrentAuditLogger{}
	orch, closer := buildConcOrchestrator(ms, al)
	defer closer()

	ctx := context.Background()

	// Phase1 to get a token — use the same project as makeDupMemory() ("test")
	// so the project-binding check introduced in round-4 passes on Phase2.
	p1, err := orch.Phase1(ctx, &models.Memory{Content: dupContent, Project: "test"}, "agent")
	if err != nil {
		t.Fatalf("Phase1: %v", err)
	}
	if p1.Stored {
		t.Skip("no duplicate detected — concurrency test needs signal; check dupContent")
	}
	token := p1.ResolutionToken

	var (
		wg      sync.WaitGroup
		results [2]error
	)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			_, results[i] = orch.Phase2(ctx, writelint.Phase2Request{
				Token:          token,
				Option:         "merge_with",
				Content:        dupContent,
				Project:        "test",
				Actor:          "agent",
				TargetMemoryID: func() *int64 { v := int64(42); return &v }(), // makeDupMemory() ID
			})
		}()
	}
	wg.Wait()

	// Exactly one success and one error
	var successes, failures int
	for _, e := range results {
		if e == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Errorf("EC-F2: expected 1 success + 1 failure for same-token concurrent Phase2; got successes=%d failures=%d errors=%v",
			successes, failures, results)
	}
}

// ---------------------------------------------------------------------------
// EC-F2: 50-goroutine stress — independent writes to same project
// ---------------------------------------------------------------------------

// TestConcurrency_50Goroutines_IndependentWrites verifies that 50 concurrent
// Phase1 writes to the same project with unique content do not corrupt state
// or race on the token store. Each goroutine writes unique content so no
// duplicates fire; each should store independently and return stored=true.
func TestConcurrency_50Goroutines_IndependentWrites(t *testing.T) {
	ms := newConcMemStore()
	al := &concurrentAuditLogger{}
	orch, closer := buildConcOrchestrator(ms, al)
	defer closer()

	ctx := context.Background()
	const N = 50

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		stored   int
		errCount int
	)

	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			// Each goroutine uses a content string unique enough to avoid Jaccard >= 0.85
			content := uniqueContent(i)
			resp, err := orch.Phase1(ctx, &models.Memory{Content: content, Project: "testproj"}, "agent")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errCount++
				return
			}
			if resp.Stored {
				stored++
			}
		}()
	}
	wg.Wait()

	if errCount > 0 {
		t.Errorf("EC-F2 50-goroutine stress: %d errors during concurrent Phase1", errCount)
	}
	if stored != N {
		t.Errorf("EC-F2 50-goroutine stress: expected %d stored, got %d (content may not be unique enough)", N, stored)
	}
}

// uniqueContent generates content strings with low pairwise Jaccard similarity.
// Each string is produced by using i as a unique distinguishing element so each
// token set is disjoint from others (Jaccard well below 0.85).
func uniqueContent(i int) string {
	// "goroutine N has unique job X with tag T" — each i produces a distinct set
	return "goroutineTask" + itoa(i) + " uniqueJobDescription tagValue" + itoa(i*7+3)
}

// itoa is a minimal integer-to-string helper to avoid importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// ---------------------------------------------------------------------------
// EC-F2: 50-goroutine stress — same-content writes (all Phase1 signals)
// ---------------------------------------------------------------------------

// TestConcurrency_50Goroutines_SameContent verifies that 50 concurrent Phase1
// writes with identical content (high Jaccard with the pre-seeded duplicate) all
// receive signals and tokens without panicking or racing.
func TestConcurrency_50Goroutines_SameContent(t *testing.T) {
	ms := newConcMemStore(makeDupMemory())
	al := &concurrentAuditLogger{}
	orch, closer := buildConcOrchestrator(ms, al)
	defer closer()

	ctx := context.Background()
	const N = 50

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		signaled  int
		errCount  int
	)

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			resp, err := orch.Phase1(ctx, &models.Memory{Content: dupContent, Project: "testproj"}, "agent")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errCount++
				return
			}
			if !resp.Stored {
				signaled++
			}
		}()
	}
	wg.Wait()

	if errCount > 0 {
		t.Errorf("EC-F2 50-goroutine same-content: %d errors", errCount)
	}
	// All N goroutines see the seeded duplicate → all should get signals
	if signaled != N {
		t.Errorf("EC-F2 50-goroutine same-content: expected %d signaled, got %d", N, signaled)
	}
}
