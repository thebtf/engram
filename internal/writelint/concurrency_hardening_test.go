// Package writelint — finding 8 and finding 9 hardening tests.
// Finding 8: 50-goroutine independent writes asserts final store state (N distinct IDs).
// Finding 9: Token expiry contract — first call after expiry → resolution_token_expired;
//             subsequent call after token is consumed/purged → resolution_token_not_found.
package writelint_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/writelint"
)

// TestConcurrency_50Goroutines_IndependentWrites_StateAssertions is the
// finding 8 hardening of the existing 50-goroutine test: asserts that
// the final memory store contains exactly N entries with distinct IDs.
func TestConcurrency_50Goroutines_IndependentWrites_StateAssertions(t *testing.T) {
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
			content := uniqueContent(i)
			resp, err := orch.Phase1(ctx, content, "testproj", "agent")
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
		t.Errorf("finding 8: %d errors during concurrent Phase1", errCount)
	}
	if stored != N {
		t.Errorf("finding 8: expected %d stored, got %d", N, stored)
	}

	// finding 8 fix: assert final store state has exactly N memories with distinct IDs.
	ms.mu.Lock()
	finalCount := len(ms.memories)
	seenIDs := make(map[int64]int)
	for _, m := range ms.memories {
		seenIDs[m.ID]++
	}
	ms.mu.Unlock()

	if finalCount != N {
		t.Errorf("finding 8: expected %d entries in memory store, got %d", N, finalCount)
	}
	for id, count := range seenIDs {
		if count > 1 {
			t.Errorf("finding 8: duplicate ID %d appears %d times in final store state", id, count)
		}
	}
}

// TestConcurrency_50Goroutines_SameContent_DistinctTokens is the finding 8
// hardening of the same-content test: asserts that all N tokens are distinct.
func TestConcurrency_50Goroutines_SameContent_DistinctTokens(t *testing.T) {
	ms := newConcMemStore(makeDupMemory())
	al := &concurrentAuditLogger{}
	orch, closer := buildConcOrchestrator(ms, al)
	defer closer()

	ctx := context.Background()
	const N = 50

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		tokens   []string
		errCount int
	)

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			resp, err := orch.Phase1(ctx, dupContent, "testproj", "agent")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errCount++
				return
			}
			if !resp.Stored && resp.ResolutionToken != "" {
				tokens = append(tokens, resp.ResolutionToken)
			}
		}()
	}
	wg.Wait()

	if errCount > 0 {
		t.Errorf("finding 8 same-content: %d errors", errCount)
	}

	// All tokens minted for concurrent same-content writes must be distinct.
	seen := make(map[string]bool)
	for _, tok := range tokens {
		if seen[tok] {
			t.Errorf("finding 8 same-content: duplicate token %q", tok)
		}
		seen[tok] = true
	}
}

// TestToken_ExpiryContract verifies the two-state expiry contract (finding 9):
//
//   State 1: First Phase2 call after TTL expires (but before janitor purge)
//            → error contains "resolution_token_expired".
//
//   State 2: After Consume deletes the entry (Consume atomically removes it),
//            a subsequent Phase2 call for the same key cannot find it
//            → error contains "resolution_token_not_found".
//
// Token expiry contract (finding 9, as documented in orchestrator.go package comment):
// - resolution_token_expired: token exists in store but TTL elapsed.
// - resolution_token_not_found: token never existed, already consumed, or
//   purged by janitor after expiry. Distinct from expired.
func TestToken_ExpiryContract(t *testing.T) {
	ms := newConcMemStore(makeDupMemory())
	al := &concurrentAuditLogger{}
	ts := writelint.NewTokenStore(writelint.TokenStoreConfig{
		TTL: 50 * time.Millisecond,
		// Janitor runs every 10 minutes — will not purge during this test.
		JanitorInterval: 10 * time.Minute,
	})
	defer ts.Close()

	orch := writelint.NewOrchestrator(writelint.OrchestratorConfig{
		MemoryStore:  ms,
		AuditLogger:  al,
		TokenStore:   ts,
		DupThreshold: 0.85,
		TokenTTL:     50 * time.Millisecond,
	})

	ctx := context.Background()
	p1, err := orch.Phase1(ctx, dupContent, "testproj", "agent")
	if err != nil {
		t.Fatalf("Phase1: %v", err)
	}
	if p1.Stored {
		t.Skip("no signal fired — expiry contract test requires a token")
	}
	token := p1.ResolutionToken

	// Wait for TTL to elapse; janitor is on 10-minute interval so entry is
	// still in the map (expired but not purged).
	time.Sleep(100 * time.Millisecond)

	// State 1: First call after expiry → resolution_token_expired.
	_, err1 := orch.Phase2(ctx, writelint.Phase2Request{
		Token:   token,
		Option:  "abort",
		Content: dupContent,
		Project: "testproj",
		Actor:   "agent",
	})
	if err1 == nil {
		t.Fatal("expiry contract State1: expected error for expired token, got nil")
	}
	if !strings.Contains(err1.Error(), "resolution_token_expired") {
		t.Errorf("expiry contract State1: expected 'resolution_token_expired', got: %v", err1)
	}

	// State 2: Consume atomically deleted the expired entry in State1.
	// The next call for the same token cannot find it → resolution_token_not_found.
	_, err2 := orch.Phase2(ctx, writelint.Phase2Request{
		Token:   token,
		Option:  "abort",
		Content: dupContent,
		Project: "testproj",
		Actor:   "agent",
	})
	if err2 == nil {
		t.Fatal("expiry contract State2: expected error for consumed/not-found token, got nil")
	}
	if !strings.Contains(err2.Error(), "resolution_token_not_found") {
		t.Errorf("expiry contract State2: expected 'resolution_token_not_found', got: %v", err2)
	}
}
