// Package core — SubsystemMeter implementation.
//
// LocalMeter is the default in-process implementation of SubsystemMeter.
// It holds atomic uint64 counters and append-only float64 observation slices,
// both guarded by a single sync.RWMutex for consistent snapshot reads.
//
// ADR-008 vocabulary constraint: this file operates exclusively on generic
// substrate terms — calls_total, errors_total, latency_ns_histogram,
// event_emitted, event_dropped. S5-owned product metric names are declared
// only in internal/cognitive/s5.
package core

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// LocalMeter is a goroutine-safe implementation of SubsystemMeter backed by
// in-process maps. Counters use atomic.AddUint64 on *uint64 pointers stored in
// a map; histograms store raw float64 observations appended under a RWMutex.
// Snapshot returns a deep copy — callers may mutate the returned maps without
// affecting stored state.
type LocalMeter struct {
	mu         sync.RWMutex
	counters   map[string]*uint64
	histograms map[string][]float64
}

// NewLocalMeter allocates an empty LocalMeter ready for use.
func NewLocalMeter() *LocalMeter {
	return &LocalMeter{
		counters:   make(map[string]*uint64),
		histograms: make(map[string][]float64),
	}
}

// IncrCounter adds delta to the named counter identified by (name, tags).
// Tags are folded into the key deterministically; concurrent calls are safe.
func (m *LocalMeter) IncrCounter(name string, delta uint64, tags map[string]string) {
	key := foldKey(name, tags)
	ptr := m.loadOrCreateCounter(key)
	atomic.AddUint64(ptr, delta)
}

// ObserveHistogram appends value to the histogram series identified by
// (name, tags). The series grows unboundedly; percentiles are computed lazily
// in Snapshot.
func (m *LocalMeter) ObserveHistogram(name string, value float64, tags map[string]string) {
	key := foldKey(name, tags)
	m.mu.Lock()
	m.histograms[key] = append(m.histograms[key], value)
	m.mu.Unlock()
}

// Snapshot returns a deep copy of all counter and histogram summary data.
// Mutating the returned MetricsSnapshot does not affect the LocalMeter's
// internal state.
func (m *LocalMeter) Snapshot() MetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snap := MetricsSnapshot{
		Counters:   make(map[string]uint64, len(m.counters)),
		Histograms: make(map[string]HistogramSummary, len(m.histograms)),
	}

	for key, ptr := range m.counters {
		snap.Counters[key] = atomic.LoadUint64(ptr)
	}

	for key, observations := range m.histograms {
		snap.Histograms[key] = computeSummary(observations)
	}

	return snap
}

// loadOrCreateCounter returns the *uint64 for key, creating it under the
// write lock if absent. The pointer is stable after creation; subsequent
// increments use only atomic operations without holding the mutex.
func (m *LocalMeter) loadOrCreateCounter(key string) *uint64 {
	m.mu.RLock()
	ptr, ok := m.counters[key]
	m.mu.RUnlock()
	if ok {
		return ptr
	}

	m.mu.Lock()
	// Re-check under write lock to avoid double-init.
	if ptr, ok = m.counters[key]; !ok {
		var zero uint64
		ptr = &zero
		m.counters[key] = ptr
	}
	m.mu.Unlock()
	return ptr
}

// foldKey produces a stable string key from a metric name and an optional tag
// map. Tags are sorted by key for determinism. An empty tag map returns the
// name unchanged.
func foldKey(name string, tags map[string]string) string {
	if len(tags) == 0 {
		return name
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString(name)
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%s=%s", k, tags[k])
	}
	sb.WriteByte('}')
	return sb.String()
}

// computeSummary builds a HistogramSummary from a copy of the observation slice
// using the nearest-rank percentile method. The input slice is copied before
// sorting so the stored observations remain unsorted.
func computeSummary(obs []float64) HistogramSummary {
	n := uint64(len(obs))
	if n == 0 {
		return HistogramSummary{}
	}
	sorted := make([]float64, n)
	copy(sorted, obs)
	sort.Float64s(sorted)

	return HistogramSummary{
		Count: n,
		P50:   percentile(sorted, 0.50),
		P95:   percentile(sorted, 0.95),
		P99:   percentile(sorted, 0.99),
	}
}

// percentile computes the nearest-rank percentile p (0 < p <= 1) from a
// pre-sorted slice. Returns 0 for empty input.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}
