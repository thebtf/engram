package serverevents

import (
	"container/list"
	"sync"
)

const dedupCapacity = 256

// dedupKey is the composite key used by the LRU dedup cache.
type dedupKey struct {
	eventType string
	projectID string
}

// lru is a fixed-capacity LRU cache keyed on (eventType, projectID).
// Mark returns true if the key was already seen (duplicate); false if it is
// new (the key is then recorded as seen). Thread-safe via a single Mutex.
//
// Capacity is 256 entries. When full, the least-recently-seen entry is
// evicted to make room for the new one. The evicted entry can be re-marked
// as new after eviction, which is the correct behaviour for a long-running
// bridge: a project that was removed and recreated weeks later should trigger
// OnProjectRemoved again.
type lru struct {
	mu       sync.Mutex
	order    *list.List              // front = most recently seen
	entries  map[dedupKey]*list.Element
	capacity int
}

// newLRU returns an initialized LRU with the given capacity.
func newLRU(capacity int) *lru {
	return &lru{
		order:    list.New(),
		entries:  make(map[dedupKey]*list.Element, capacity),
		capacity: capacity,
	}
}

// Mark reports whether key (eventType, projectID) was already seen.
// If the key is new it is recorded; if it already exists its LRU position
// is refreshed (moved to front).
//
// Returns true  → duplicate (caller should suppress fan-out).
// Returns false → new event (caller should proceed with fan-out).
func (l *lru) Mark(eventType, projectID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	k := dedupKey{eventType: eventType, projectID: projectID}

	if elem, ok := l.entries[k]; ok {
		// Already seen — move to front (refresh LRU position).
		l.order.MoveToFront(elem)
		return true
	}

	// New key — evict LRU entry if at capacity.
	if l.order.Len() >= l.capacity {
		back := l.order.Back()
		if back != nil {
			l.order.Remove(back)
			delete(l.entries, back.Value.(dedupKey))
		}
	}

	// Insert new entry at front.
	elem := l.order.PushFront(k)
	l.entries[k] = elem
	return false
}
