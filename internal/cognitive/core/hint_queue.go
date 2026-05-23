package core

// hint_queue.go — bounded FIFO buffer for HintProposalPayload values.
//
// Design decisions (see .agent/tasks/engram-v7-core/T008/implementation-log.md):
//
//   - Per-session buffer; cap = 50; overflow drops the oldest entry (ADR-004).
//   - 30-minute hint expiry enforced by a background sweeper goroutine.
//   - Lifecycle owned by the queue (post-PM-Fix-7): Start/Stop on the concrete
//     type; HintQueue interface (T004) is NOT modified.
//   - Injectable clock (q.clock) and sweepInterval for deterministic testing.
//   - No import of internal/worker — enforced by TestImportBoundary_NoWorkerImport.

import (
	"context"
	"sync"
	"time"
)

const (
	// hintQueueCap is the maximum number of HintProposalPayload entries held
	// per session. Excess entries drop the oldest (drop-oldest eviction per
	// ADR-004).
	hintQueueCap = 50

	// hintExpiry is the default duration after which a hint is considered
	// stale and removed by the sweeper.
	hintExpiry = 30 * time.Minute

	// defaultSweepInterval is how often the sweeper goroutine wakes to prune
	// expired entries in production. Tests override this via sweepInterval.
	defaultSweepInterval = 30 * time.Minute
)

// sessionBuf holds the per-session queue state.
type sessionBuf struct {
	entries   []HintProposalPayload
	overflows uint64
	evicted   uint64
}

// hintQueue is the concrete implementation of HintQueue. The exported
// constructor is NewHintQueue; callers that need Start/Stop use the concrete
// *hintQueue type directly (satisfies HintQueue implicitly).
type hintQueue struct {
	mu            sync.Mutex
	sessions      map[string]*sessionBuf
	clock         func() time.Time
	sweepInterval time.Duration
	expiry        time.Duration

	// lifecycle
	running bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

// NewHintQueue returns a *hintQueue ready to use. Callers may call Start to
// enable the background expiry sweeper, or use Enqueue/Drain/Stats without
// the sweeper for unit testing.
func NewHintQueue() *hintQueue {
	return &hintQueue{
		sessions:      make(map[string]*sessionBuf),
		clock:         time.Now,
		sweepInterval: defaultSweepInterval,
		expiry:        hintExpiry,
		stopCh:        make(chan struct{}),
	}
}

// --- HintQueue interface methods -------------------------------------------

// Enqueue appends hint to sessionID's buffer. If the buffer has reached
// hintQueueCap the oldest entry is dropped and the overflow + evicted
// counters are incremented (EC-4, ADR-004 drop-oldest).
func (q *hintQueue) Enqueue(_ context.Context, sessionID string, hint HintProposalPayload) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	buf := q.getOrCreate(sessionID)
	buf.entries = append(buf.entries, hint)

	if len(buf.entries) > hintQueueCap {
		// Drop-oldest: remove the first (oldest) entry.
		buf.entries = buf.entries[1:]
		buf.overflows++
		buf.evicted++
	}
	return nil
}

// Drain removes up to max hints from sessionID's buffer in queue order
// (oldest first) and returns them. The returned slice is independent of
// internal storage.
func (q *hintQueue) Drain(sessionID string, max int) []HintProposalPayload {
	// Defensive: a negative max would cause `make([]T, n)` to panic below.
	// Treat any non-positive max as "no events requested".
	if max <= 0 {
		return nil
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	buf, ok := q.sessions[sessionID]
	if !ok || len(buf.entries) == 0 {
		return nil
	}

	n := len(buf.entries)
	if max < n {
		n = max
	}

	out := make([]HintProposalPayload, n)
	copy(out, buf.entries[:n])
	buf.entries = buf.entries[n:]
	return out
}

// Stats reports the current depth and lifetime overflow counters for
// sessionID. Unknown sessions return the zero QueueStats value.
func (q *hintQueue) Stats(sessionID string) QueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()

	buf, ok := q.sessions[sessionID]
	if !ok {
		return QueueStats{}
	}
	return QueueStats{
		QueuedNow: len(buf.entries),
		Overflows: buf.overflows,
		Evicted:   buf.evicted,
	}
}

// --- Lifecycle methods (on the concrete *hintQueue, not HintQueue iface) ---

// Start spawns the background expiry sweeper bound to ctx. Calling Start
// while the sweeper is already running is a no-op (idempotent).
func (q *hintQueue) Start(ctx context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.running {
		return nil
	}
	q.running = true
	// Allocate a fresh stop channel each time Start is called after a
	// previous Stop cycle.
	q.stopCh = make(chan struct{})
	q.wg.Add(1)
	go q.runSweeper(ctx)
	return nil
}

// Stop signals the sweeper goroutine to exit and waits for it to do so.
// Calling Stop while the sweeper is not running is a no-op (idempotent).
func (q *hintQueue) Stop() error {
	q.mu.Lock()
	if !q.running {
		q.mu.Unlock()
		return nil
	}
	// Signal the sweeper.
	close(q.stopCh)
	q.running = false
	q.mu.Unlock()

	// Wait outside the lock so the sweeper can acquire mu during its final
	// sweep pass.
	q.wg.Wait()
	return nil
}

// --- Internal helpers -------------------------------------------------------

// getOrCreate returns the sessionBuf for sessionID, creating it if absent.
// Caller must hold q.mu.
func (q *hintQueue) getOrCreate(sessionID string) *sessionBuf {
	buf, ok := q.sessions[sessionID]
	if !ok {
		buf = &sessionBuf{}
		q.sessions[sessionID] = buf
	}
	return buf
}

// runSweeper is the sweeper goroutine body. It wakes every q.sweepInterval,
// calls sweep(), and exits when ctx is done or stopCh is closed.
func (q *hintQueue) runSweeper(ctx context.Context) {
	defer q.wg.Done()
	ticker := time.NewTicker(q.sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			q.sweep()
		case <-ctx.Done():
			return
		case <-q.stopCh:
			return
		}
	}
}

// sweep removes all entries whose CreatedAt is older than q.expiry relative
// to q.clock(). It is safe to call from tests directly (same package).
func (q *hintQueue) sweep() {
	cutoff := q.clock().Add(-q.expiry)

	q.mu.Lock()
	defer q.mu.Unlock()

	for sid, buf := range q.sessions {
		kept := buf.entries[:0]
		for _, e := range buf.entries {
			if !e.CreatedAt.Before(cutoff) {
				kept = append(kept, e)
			}
		}
		if len(kept) != len(buf.entries) {
			// Replace the slice; preserve counters.
			buf.entries = kept
		}
		if len(buf.entries) == 0 {
			delete(q.sessions, sid)
		}
	}
}
