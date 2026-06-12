// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

// openStoreForStoreTest opens a full Store via DATABASE_DSN.
// Skips the test when DATABASE_DSN is absent.
func openStoreForStoreTest(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping Store integration test")
	}
	store, err := NewStore(Config{DSN: dsn, LogLevel: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// ---------------------------------------------------------------------------
// resolveMaxConns
// ---------------------------------------------------------------------------

func TestResolveMaxConns_Zero(t *testing.T) {
	assert.Equal(t, 10, resolveMaxConns(0))
}

func TestResolveMaxConns_Negative(t *testing.T) {
	assert.Equal(t, 10, resolveMaxConns(-1))
	assert.Equal(t, 10, resolveMaxConns(-100))
}

func TestResolveMaxConns_Positive(t *testing.T) {
	assert.Equal(t, 1, resolveMaxConns(1))
	assert.Equal(t, 20, resolveMaxConns(20))
	assert.Equal(t, 100, resolveMaxConns(100))
}

// ---------------------------------------------------------------------------
// PoolMetrics — pure in-memory, no DB needed
// ---------------------------------------------------------------------------

func TestPoolMetrics_NewDefaults(t *testing.T) {
	m := NewPoolMetrics(0)
	require.NotNil(t, m)
	summary := m.GetMetricsSummary()
	assert.Equal(t, int64(0), summary.TotalQueries)
	assert.Equal(t, 0, summary.SampleCount)
	assert.Equal(t, time.Duration(0), summary.P95Latency, "P95 must be zero when no samples")
}

func TestPoolMetrics_RecordLatency_SingleSample(t *testing.T) {
	m := NewPoolMetrics(100)
	m.RecordLatency(5 * time.Millisecond)
	s := m.GetMetricsSummary()
	assert.Equal(t, int64(1), s.TotalQueries)
	assert.Equal(t, 1, s.SampleCount)
	assert.Equal(t, 5*time.Millisecond, s.MinLatency)
	assert.Equal(t, 5*time.Millisecond, s.MaxLatency)
	assert.Equal(t, 5*time.Millisecond, s.AvgLatency)
	assert.Equal(t, time.Duration(0), s.P95Latency, "P95 needs ≥20 samples")
}

func TestPoolMetrics_P95_RequiresTwentySamples(t *testing.T) {
	m := NewPoolMetrics(100)

	// 19 samples — P95 must still be zero.
	for i := 0; i < 19; i++ {
		m.RecordLatency(time.Duration(i+1) * time.Millisecond)
	}
	s := m.GetMetricsSummary()
	assert.Equal(t, 19, s.SampleCount)
	assert.Equal(t, time.Duration(0), s.P95Latency, "P95 must be 0 with <20 samples")

	// 20th sample — P95 must now be computed.
	m.RecordLatency(20 * time.Millisecond)
	s = m.GetMetricsSummary()
	assert.Equal(t, 20, s.SampleCount)
	assert.Greater(t, s.P95Latency, time.Duration(0), "P95 must be non-zero with ≥20 samples")
}

func TestPoolMetrics_RingBuffer_Wraps(t *testing.T) {
	m := NewPoolMetrics(5)
	for i := 0; i < 10; i++ {
		m.RecordLatency(time.Duration(i+1) * time.Millisecond)
	}
	s := m.GetMetricsSummary()
	// Ring buffer of size 5: after 10 inserts count stays at 5.
	assert.Equal(t, 5, s.SampleCount)
	assert.Equal(t, int64(10), s.TotalQueries)
}

func TestPoolMetrics_RecordPoolStats_PeakTracking(t *testing.T) {
	m := NewPoolMetrics(100)

	m.RecordPoolStats(sql.DBStats{InUse: 3, WaitCount: 5, WaitDuration: 10 * time.Millisecond})
	m.RecordPoolStats(sql.DBStats{InUse: 7, WaitCount: 3, WaitDuration: 5 * time.Millisecond})
	m.RecordPoolStats(sql.DBStats{InUse: 2, WaitCount: 9, WaitDuration: 1 * time.Millisecond})

	s := m.GetMetricsSummary()
	assert.Equal(t, 7, s.PeakInUse, "peak InUse must be 7")
	assert.Equal(t, int64(9), s.PeakWaitCount, "peak WaitCount must be 9")
	assert.Equal(t, 16*time.Millisecond, s.TotalWaitTime, "TotalWaitTime accumulates")
}

func TestPoolMetrics_MinMaxAvg(t *testing.T) {
	m := NewPoolMetrics(100)
	durations := []time.Duration{
		1 * time.Millisecond,
		3 * time.Millisecond,
		5 * time.Millisecond,
		7 * time.Millisecond,
		9 * time.Millisecond,
	}
	for _, d := range durations {
		m.RecordLatency(d)
	}
	s := m.GetMetricsSummary()
	assert.Equal(t, 1*time.Millisecond, s.MinLatency)
	assert.Equal(t, 9*time.Millisecond, s.MaxLatency)
	assert.Equal(t, 5*time.Millisecond, s.AvgLatency)
}

// ---------------------------------------------------------------------------
// computeLatencyStats
// ---------------------------------------------------------------------------

func TestComputeLatencyStats_SingleValue(t *testing.T) {
	min, max, avg := computeLatencyStats([]time.Duration{42 * time.Microsecond})
	assert.Equal(t, 42*time.Microsecond, min)
	assert.Equal(t, 42*time.Microsecond, max)
	assert.Equal(t, 42*time.Microsecond, avg)
}

func TestComputeLatencyStats_MultipleValues(t *testing.T) {
	samples := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
	}
	min, max, avg := computeLatencyStats(samples)
	assert.Equal(t, 10*time.Millisecond, min)
	assert.Equal(t, 30*time.Millisecond, max)
	assert.Equal(t, 20*time.Millisecond, avg)
}

// ---------------------------------------------------------------------------
// computeP95
// ---------------------------------------------------------------------------

func TestComputeP95_Sorted(t *testing.T) {
	// 20 values 1ms..20ms; p95 index = floor(20*0.95)=19 → 20ms on sorted copy.
	samples := make([]time.Duration, 20)
	for i := range samples {
		samples[i] = time.Duration(i+1) * time.Millisecond
	}
	p95 := computeP95(samples)
	assert.Equal(t, 20*time.Millisecond, p95)
}

func TestComputeP95_Unsorted(t *testing.T) {
	// Same 20 values shuffled — sort copy inside; result must be the same.
	samples := []time.Duration{
		20, 1, 15, 3, 18, 7, 11, 4, 16, 2,
		19, 5, 14, 8, 13, 6, 17, 9, 12, 10,
	}
	for i := range samples {
		samples[i] *= time.Millisecond
	}
	p95 := computeP95(samples)
	assert.Equal(t, 20*time.Millisecond, p95)
}

// ---------------------------------------------------------------------------
// applyHealthThresholds
// ---------------------------------------------------------------------------

func TestApplyHealthThresholds_Healthy(t *testing.T) {
	info := &HealthInfo{Status: "healthy", QueryLatency: 1 * time.Millisecond}
	stats := sql.DBStats{InUse: 2, OpenConnections: 10, WaitCount: 0}
	applyHealthThresholds(info, stats)
	assert.Equal(t, "healthy", info.Status)
	assert.Empty(t, info.Warning)
}

func TestApplyHealthThresholds_PoolSaturation(t *testing.T) {
	info := &HealthInfo{Status: "healthy", QueryLatency: 1 * time.Millisecond}
	// 9/10 = 90% in-use → degraded.
	stats := sql.DBStats{InUse: 9, OpenConnections: 10}
	applyHealthThresholds(info, stats)
	assert.Equal(t, "degraded", info.Status)
	assert.Contains(t, info.Warning, "Connection pool heavily utilized")
}

func TestApplyHealthThresholds_WaitContention(t *testing.T) {
	info := &HealthInfo{Status: "healthy", QueryLatency: 1 * time.Millisecond}
	stats := sql.DBStats{
		InUse:           1,
		OpenConnections: 10,
		WaitCount:       200,
		WaitDuration:    500 * time.Millisecond,
	}
	applyHealthThresholds(info, stats)
	assert.Equal(t, "degraded", info.Status)
	assert.Contains(t, info.Warning, "contention")
}

func TestApplyHealthThresholds_SlowQueryLatency(t *testing.T) {
	info := &HealthInfo{Status: "healthy", QueryLatency: 15 * time.Millisecond}
	stats := sql.DBStats{}
	applyHealthThresholds(info, stats)
	assert.Equal(t, "degraded", info.Status)
	assert.Contains(t, info.Warning, "Slow query latency")
}

func TestApplyHealthThresholds_HighP95(t *testing.T) {
	info := &HealthInfo{
		Status:       "healthy",
		QueryLatency: 1 * time.Millisecond,
		HistoricalMetrics: MetricsSummary{
			P95Latency: 60 * time.Millisecond,
		},
	}
	stats := sql.DBStats{}
	applyHealthThresholds(info, stats)
	assert.Equal(t, "degraded", info.Status)
	assert.Contains(t, info.Warning, "P95")
}

func TestApplyHealthThresholds_AlreadyDegraded(t *testing.T) {
	// An already-degraded status must not regress back to healthy.
	info := &HealthInfo{Status: "degraded", QueryLatency: 1 * time.Millisecond}
	stats := sql.DBStats{}
	applyHealthThresholds(info, stats)
	assert.Equal(t, "degraded", info.Status)
}

// ---------------------------------------------------------------------------
// Store integration tests (skip without DATABASE_DSN)
// ---------------------------------------------------------------------------

func TestStore_WithTimeout_CancelIdempotent(t *testing.T) {
	store := openStoreForStoreTest(t)

	ctx, cancel := store.WithTimeout(context.Background(), 100*time.Millisecond, "test-op")
	cancel()
	cancel() // second call must not panic
	_ = ctx
}

func TestStore_ExecWithTimeout(t *testing.T) {
	store := openStoreForStoreTest(t)
	ctx := context.Background()
	err := store.ExecWithTimeout(ctx, 5*time.Second, "SET search_path TO public")
	require.NoError(t, err)
}

func TestStore_QueryRowWithTimeout(t *testing.T) {
	store := openStoreForStoreTest(t)
	ctx := context.Background()
	row := store.QueryRowWithTimeout(ctx, 5*time.Second, "SELECT 42")
	var val int
	require.NoError(t, row.Scan(&val))
	assert.Equal(t, 42, val)
}

func TestStore_TransactionWithTimeout_Commit(t *testing.T) {
	store := openStoreForStoreTest(t)
	ctx := context.Background()
	err := store.TransactionWithTimeout(ctx, 5*time.Second, func(tx *gormlib.DB) error {
		var n int
		return tx.Raw("SELECT 1").Scan(&n).Error
	})
	require.NoError(t, err)
}

func TestStore_TransactionWithTimeout_RollbackOnError(t *testing.T) {
	store := openStoreForStoreTest(t)
	ctx := context.Background()
	sentinel := errors.New("intentional rollback")
	err := store.TransactionWithTimeout(ctx, 5*time.Second, func(tx *gormlib.DB) error {
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel)
}

func TestStore_GetMetrics_ResetMetrics(t *testing.T) {
	store := openStoreForStoreTest(t)
	ctx := context.Background()
	// Health check forces a SELECT 1, which records a latency sample.
	_ = store.HealthCheckForce(ctx)

	s := store.GetMetrics()
	assert.GreaterOrEqual(t, s.TotalQueries, int64(1))

	store.ResetMetrics()
	s2 := store.GetMetrics()
	assert.Equal(t, int64(0), s2.TotalQueries, "metrics must be zero after reset")
}

func TestStore_Stats(t *testing.T) {
	store := openStoreForStoreTest(t)
	stats := store.Stats()
	assert.GreaterOrEqual(t, stats.OpenConnections, 1)
}

func TestStore_Ping(t *testing.T) {
	store := openStoreForStoreTest(t)
	require.NoError(t, store.Ping())
}

func TestStore_GetRawDB_GetDB(t *testing.T) {
	store := openStoreForStoreTest(t)
	assert.NotNil(t, store.GetRawDB())
	assert.NotNil(t, store.GetDB())
}

func TestStore_HealthCheck_Caching(t *testing.T) {
	store := openStoreForStoreTest(t)
	ctx := context.Background()
	h1 := store.HealthCheck(ctx)
	h2 := store.HealthCheck(ctx)
	require.NotNil(t, h1)
	require.NotNil(t, h2)
	assert.Contains(t, []string{"healthy", "degraded"}, h1.Status)
	assert.Contains(t, []string{"healthy", "degraded"}, h2.Status)
}

func TestStore_HealthCheckForce_BypassesCache(t *testing.T) {
	store := openStoreForStoreTest(t)
	ctx := context.Background()
	h := store.HealthCheckForce(ctx)
	require.NotNil(t, h)
	assert.Contains(t, []string{"healthy", "degraded"}, h.Status)
	assert.Greater(t, h.QueryLatency, time.Duration(0))
	assert.False(t, h.Timestamp.IsZero())
}

func TestStore_Optimize(t *testing.T) {
	store := openStoreForStoreTest(t)
	ctx := context.Background()
	err := store.Optimize(ctx)
	require.NoError(t, err)
}
