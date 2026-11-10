package serverevents

import (
	"fmt"
	"sync"
	"testing"
)

func TestLRU_NewKey(t *testing.T) {
	t.Parallel()
	l := newLRU(dedupCapacity)

	seen := l.Mark("removed", "proj-a")
	if seen {
		t.Error("expected false for a new key, got true")
	}
}

func TestLRU_DuplicateKey(t *testing.T) {
	t.Parallel()
	l := newLRU(dedupCapacity)

	l.Mark("removed", "proj-dup")
	seen := l.Mark("removed", "proj-dup")
	if !seen {
		t.Error("expected true for duplicate key, got false")
	}
}

func TestLRU_Eviction_AtCapacity(t *testing.T) {
	t.Parallel()
	l := newLRU(4)

	// Fill to capacity.
	for i := 0; i < 4; i++ {
		l.Mark("removed", fmt.Sprintf("proj-%d", i))
	}
	// Cache is now: [proj-3, proj-2, proj-1, proj-0] (front → back)
	// All 4 should be seen.
	for i := 0; i < 4; i++ {
		if !l.Mark("removed", fmt.Sprintf("proj-%d", i)) {
			t.Errorf("expected proj-%d to be seen (duplicate), got false", i)
		}
	}
	// Adding a 5th entry must succeed (not panic, not return wrong value).
	seen := l.Mark("removed", "proj-new")
	if seen {
		t.Error("expected false for brand-new entry beyond capacity, got true")
	}
	if l.Mark("removed", "proj-0") {
		t.Error("expected least-recent proj-0 to be evicted at capacity")
	}
}

func TestLRU_LeastRecentEvicted(t *testing.T) {
	t.Parallel()
	l := newLRU(3)

	// Insert three entries in order: a, b, c
	l.Mark("removed", "a")
	l.Mark("removed", "b")
	l.Mark("removed", "c")
	// Access "a" so it moves to front; LRU order becomes c(front), a, b(back)
	// Wait — actually after inserting: c(front), b, a(back)
	// Then refreshing "a": a(front), c, b(back)
	l.Mark("removed", "a") // refresh a → not evicted next

	// Insert "d" → evicts LRU = b
	l.Mark("removed", "d")

	// "b" should now be evictable (not in cache → Mark returns false)
	if l.Mark("removed", "b") {
		t.Error("expected b to have been evicted (Mark should return false), got true")
	}
	// "a" should still be in cache
	if !l.Mark("removed", "a") {
		t.Error("expected a to still be in cache (Mark should return true), got false")
	}
}

func TestLRU_ConcurrentMark(t *testing.T) {
	t.Parallel()
	l := newLRU(dedupCapacity)

	var wg sync.WaitGroup
	const goroutines = 100

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// Each goroutine marks a unique key and then the shared key.
			l.Mark("removed", fmt.Sprintf("unique-%d", n))
			l.Mark("removed", "shared-key")
		}(i)
	}
	wg.Wait()

	// After all goroutines, the shared key must be in the cache.
	if !l.Mark("removed", "shared-key") {
		t.Error("shared-key should still be in cache after concurrent access")
	}
}
