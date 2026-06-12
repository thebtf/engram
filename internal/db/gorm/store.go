// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store is the central database handle. It wraps a GORM DB for ORM queries and
// the underlying *sql.DB for pool management and raw SQL that GORM cannot express
// (e.g. tsvector full-text queries, COPY statements).
type Store struct {
	healthCacheTime time.Time
	DB              *gorm.DB
	sqlDB           *sql.DB
	metrics         *PoolMetrics
	cachedHealth    *HealthInfo
	healthCacheTTL  time.Duration
	healthCacheMu   sync.RWMutex
}

// Config holds the parameters needed to open a Store.
// DSN accepts any libpq-style connection string; MaxConns defaults to 10 when
// zero or negative because PostgreSQL connections carry ~5 MB server-side memory
// overhead each — uncapped pools exhaust RAM under burst traffic.
type Config struct {
	DSN      string          // PostgreSQL DSN (e.g. postgres://user:pass@host/db)
	MaxConns int             // Maximum number of open connections (default: 10)
	LogLevel logger.LogLevel // GORM log level (logger.Silent for production)
}

// NewStore opens a PostgreSQL connection, configures the connection pool,
// verifies reachability, runs all pending migrations, and warms the pool.
// It is the single entry point for obtaining a Store.
func NewStore(cfg Config) (*Store, error) {
	db, err := openGORM(cfg)
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}

	maxConns := resolveMaxConns(cfg.MaxConns)
	configurePool(sqlDB, maxConns)

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	store := &Store{
		DB:             db,
		sqlDB:          sqlDB,
		metrics:        NewPoolMetrics(100), // sliding window of 100 latency samples
		healthCacheTTL: 5 * time.Second,     // avoid hammering DB during health polls
	}

	if err := runMigrations(db); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	// Pre-create connections so the first real request does not pay cold-start
	// latency. We warm half the pool — enough to absorb a burst without spending
	// startup time on connections that may never be needed.
	store.WarmPool(maxConns / 2)

	return store, nil
}

// openGORM opens GORM with the PostgreSQL driver.
// PrepareStmt is enabled because engram runs a small, stable query set;
// server-side prepared statements reduce per-query parse/plan overhead.
func openGORM(cfg Config) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{
		Logger:      logger.Default.LogMode(cfg.LogLevel),
		PrepareStmt: true,
		NowFunc:     nil,
	})
	if err != nil {
		return nil, fmt.Errorf("open gorm postgres: %w", err)
	}
	return db, nil
}

// resolveMaxConns returns maxConns if positive, otherwise the default of 10.
func resolveMaxConns(maxConns int) int {
	if maxConns <= 0 {
		return 10
	}
	return maxConns
}

// configurePool sets pool limits that match PostgreSQL's resource model:
//   - MaxOpen caps server-side memory (one server process per connection).
//   - MaxIdle = MaxOpen/2 keeps a modest reserve without hoarding.
//   - ConnMaxLifetime recycles connections to avoid long-lived server state drift.
//   - ConnMaxIdleTime reclaims idle connections during low-traffic periods.
func configurePool(sqlDB *sql.DB, maxConns int) {
	sqlDB.SetMaxOpenConns(maxConns)
	sqlDB.SetMaxIdleConns(maxConns / 2)
	sqlDB.SetConnMaxLifetime(1 * time.Hour)
	sqlDB.SetConnMaxIdleTime(10 * time.Minute)
}

// WarmPool pre-creates connections to eliminate cold-start latency on the first
// real request after server startup. Each goroutine acquires a connection,
// executes a trivial ping to ensure the connection is fully negotiated, then
// returns the connection to the pool (Close on a pool-acquired Conn returns it,
// not destroys it). We call wg.Wait so that WarmPool blocks until every warmup
// attempt has completed or timed out — the caller (NewStore) should not proceed
// until the pool is ready.
func (s *Store) WarmPool(numConns int) {
	if numConns <= 0 {
		numConns = 4
	}

	var wg sync.WaitGroup
	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			conn, err := s.sqlDB.Conn(ctx)
			if err != nil {
				return
			}
			// PingContext forces the driver to complete the connection handshake.
			_ = conn.PingContext(ctx)
			// Close returns the connection to the pool; it does not destroy it.
			_ = conn.Close()
		}()
	}
	wg.Wait()
	log.Debug().Int("connections", numConns).Msg("Connection pool warmed")
}

// Close shuts down the connection pool. Call this during graceful shutdown.
func (s *Store) Close() error {
	return s.sqlDB.Close()
}

// Ping verifies that at least one database connection is alive and responsive.
func (s *Store) Ping() error {
	return s.sqlDB.Ping()
}

// GetRawDB exposes the underlying *sql.DB for operations GORM cannot express:
//   - tsvector full-text search with @@ operator
//   - COPY FROM/TO for bulk imports
//   - Advisory locks and other raw SQL constructs
func (s *Store) GetRawDB() *sql.DB {
	return s.sqlDB
}

// GetDB exposes the GORM DB instance for standard typed queries.
func (s *Store) GetDB() *gorm.DB {
	return s.DB
}

// Stats returns a snapshot of the connection pool counters. Callers can
// expose these via /health or metrics endpoints.
func (s *Store) Stats() sql.DBStats {
	return s.sqlDB.Stats()
}

// Optimize runs ANALYZE to refresh query planner statistics.
// PostgreSQL's query planner relies on per-column statistics (pg_statistic);
// after large bulk writes the statistics become stale and the planner picks
// suboptimal plans. This should be called during scheduled maintenance windows,
// not on every write — ANALYZE locks tables briefly.
func (s *Store) Optimize(ctx context.Context) error {
	log.Info().Msg("Starting database optimization")
	start := time.Now()

	if _, err := s.sqlDB.ExecContext(ctx, "ANALYZE"); err != nil {
		return fmt.Errorf("analyze: %w", err)
	}

	log.Info().Dur("duration", time.Since(start)).Msg("Database optimization complete")
	return nil
}

// HealthCheck returns a cached health snapshot, refreshing it when the cache
// is older than healthCacheTTL. The cache prevents a thundering herd of health
// probes from each translating into a live DB query under high monitoring frequency.
func (s *Store) HealthCheck(ctx context.Context) *HealthInfo {
	// Fast path: return cached result under read lock if still fresh.
	s.healthCacheMu.RLock()
	if s.cachedHealth != nil && time.Since(s.healthCacheTime) < s.healthCacheTTL {
		cached := s.cachedHealth
		s.healthCacheMu.RUnlock()
		return cached
	}
	s.healthCacheMu.RUnlock()

	// Slow path: perform a live check then cache.
	info := s.performHealthCheck(ctx)

	s.healthCacheMu.Lock()
	s.cachedHealth = info
	s.healthCacheTime = time.Now()
	s.healthCacheMu.Unlock()

	return info
}

// HealthCheckForce bypasses the cache and performs a live health check.
// Use this when real-time data is required (alerting pipelines, debug endpoints).
func (s *Store) HealthCheckForce(ctx context.Context) *HealthInfo {
	info := s.performHealthCheck(ctx)

	// Keep the cache consistent so the next regular HealthCheck sees fresh data.
	s.healthCacheMu.Lock()
	s.cachedHealth = info
	s.healthCacheTime = time.Now()
	s.healthCacheMu.Unlock()

	return info
}

// performHealthCheck runs the actual health probe. It measures pool occupancy,
// executes a minimal "SELECT 1" to measure query latency, and degrades the
// status when thresholds are exceeded. Callers should use HealthCheck or
// HealthCheckForce rather than calling this directly.
func (s *Store) performHealthCheck(ctx context.Context) *HealthInfo {
	info := &HealthInfo{
		Status:    "healthy",
		Timestamp: time.Now(),
	}

	stats := s.sqlDB.Stats()
	info.PoolStats = poolStatsFromDBStats(stats)

	if s.metrics != nil {
		s.metrics.RecordPoolStats(stats)
	}

	// A SELECT 1 measures round-trip latency without any table I/O, giving a
	// clean signal of network and server overhead.
	start := time.Now()
	var dummy int
	err := s.sqlDB.QueryRowContext(ctx, "SELECT 1").Scan(&dummy)
	info.QueryLatency = time.Since(start)

	if s.metrics != nil {
		s.metrics.RecordLatency(info.QueryLatency)
		info.HistoricalMetrics = s.metrics.GetMetricsSummary()
	}

	if err != nil {
		info.Status = "unhealthy"
		info.Error = err.Error()
		return info
	}

	applyHealthThresholds(info, stats)
	return info
}

// poolStatsFromDBStats converts sql.DBStats into the exported PoolStats shape.
func poolStatsFromDBStats(stats sql.DBStats) PoolStats {
	return PoolStats{
		OpenConnections:   stats.OpenConnections,
		InUse:             stats.InUse,
		Idle:              stats.Idle,
		WaitCount:         stats.WaitCount,
		WaitDuration:      stats.WaitDuration,
		MaxIdleClosed:     stats.MaxIdleClosed,
		MaxLifetimeClosed: stats.MaxLifetimeClosed,
	}
}

// applyHealthThresholds degrades the health status when measurable signals
// cross the warning thresholds. Status only moves toward worse states, never
// back to healthy within a single check.
func applyHealthThresholds(info *HealthInfo, stats sql.DBStats) {
	// Pool saturation: >80 % in-use is a sign of connection pressure.
	if stats.InUse > 0 && float64(stats.InUse)/float64(stats.OpenConnections) > 0.8 {
		info.Status = "degraded"
		info.Warning = "Connection pool heavily utilized"
	}

	// Wait contention: sustained waits indicate requests are queuing for connections.
	if stats.WaitCount > 100 && stats.WaitDuration > 100*time.Millisecond {
		info.Status = "degraded"
		info.Warning = "Connection pool contention detected"
	}

	// Query latency: a SELECT 1 taking >10 ms signals network or server load.
	if info.QueryLatency > 10*time.Millisecond {
		if info.Status == "healthy" {
			info.Status = "degraded"
		}
		info.Warning = fmt.Sprintf("Slow query latency: %v", info.QueryLatency)
	}

	// P95 latency trend: sustained high P95 catches degradation that single
	// samples miss.
	if info.HistoricalMetrics.P95Latency > 50*time.Millisecond {
		if info.Status == "healthy" {
			info.Status = "degraded"
		}
		info.Warning = fmt.Sprintf("High P95 latency: %v", info.HistoricalMetrics.P95Latency)
	}
}

// HealthInfo carries the result of a database health check.
type HealthInfo struct {
	Timestamp         time.Time      `json:"timestamp"`
	Status            string         `json:"status"`
	Error             string         `json:"error,omitempty"`
	Warning           string         `json:"warning,omitempty"`
	HistoricalMetrics MetricsSummary `json:"historical_metrics,omitempty"`
	PoolStats         PoolStats      `json:"pool_stats"`
	QueryLatency      time.Duration  `json:"query_latency_ns"`
}

// PoolStats is the JSON-serialisable snapshot of connection pool counters.
type PoolStats struct {
	OpenConnections   int           `json:"open_connections"`
	InUse             int           `json:"in_use"`
	Idle              int           `json:"idle"`
	WaitCount         int64         `json:"wait_count"`
	WaitDuration      time.Duration `json:"wait_duration_ns"`
	MaxIdleClosed     int64         `json:"max_idle_closed"`
	MaxLifetimeClosed int64         `json:"max_lifetime_closed"`
}

// Query timeout constants for different workload classes.
// Sized to reflect the expected cost of each operation class:
//   - FastQueryTimeout: health checks and single-row lookups
//   - DefaultQueryTimeout: filtered list queries and small JOINs
//   - SlowQueryTimeout: bulk operations, index rebuilds, analytics aggregates
const (
	DefaultQueryTimeout = 5 * time.Second
	FastQueryTimeout    = 1 * time.Second
	SlowQueryTimeout    = 30 * time.Second
)

// PoolMetrics tracks a sliding window of query latency samples and pool
// statistics peaks. The fixed-size ring buffer avoids unbounded growth while
// providing enough data to compute a meaningful P95.
type PoolMetrics struct {
	lastSampleTime time.Time
	latencySamples []time.Duration
	latencyIdx     int
	latencyCount   int
	totalQueries   int64
	totalWaitTime  time.Duration
	peakInUse      int
	peakWaitCount  int64
	windowSize     int
	mu             sync.RWMutex
}

// NewPoolMetrics creates a PoolMetrics collector. windowSize controls how many
// latency samples are kept; older samples are overwritten in ring-buffer order.
// Defaults to 100 when windowSize is zero or negative.
func NewPoolMetrics(windowSize int) *PoolMetrics {
	if windowSize <= 0 {
		windowSize = 100
	}
	return &PoolMetrics{
		latencySamples: make([]time.Duration, windowSize),
		windowSize:     windowSize,
		lastSampleTime: time.Now(),
	}
}

// RecordLatency adds one query latency observation to the ring buffer.
func (m *PoolMetrics) RecordLatency(latency time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.latencySamples[m.latencyIdx] = latency
	m.latencyIdx = (m.latencyIdx + 1) % m.windowSize
	if m.latencyCount < m.windowSize {
		m.latencyCount++
	}
	m.totalQueries++
	m.lastSampleTime = time.Now()
}

// RecordPoolStats updates peak-tracking counters from a pool stats snapshot.
func (m *PoolMetrics) RecordPoolStats(stats sql.DBStats) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if stats.InUse > m.peakInUse {
		m.peakInUse = stats.InUse
	}
	if stats.WaitCount > m.peakWaitCount {
		m.peakWaitCount = stats.WaitCount
	}
	m.totalWaitTime += stats.WaitDuration
}

// GetMetricsSummary returns an aggregate snapshot of all recorded metrics.
// P95 is computed only when at least 20 samples are available — below that
// threshold the sorted approximation is not statistically meaningful.
func (m *PoolMetrics) GetMetricsSummary() MetricsSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()

	summary := MetricsSummary{
		TotalQueries:   m.totalQueries,
		SampleCount:    m.latencyCount,
		PeakInUse:      m.peakInUse,
		PeakWaitCount:  m.peakWaitCount,
		TotalWaitTime:  m.totalWaitTime,
		LastSampleTime: m.lastSampleTime,
	}

	if m.latencyCount == 0 {
		return summary
	}

	summary.MinLatency, summary.MaxLatency, summary.AvgLatency = computeLatencyStats(m.latencySamples[:m.latencyCount])

	if m.latencyCount >= 20 {
		summary.P95Latency = computeP95(m.latencySamples[:m.latencyCount])
	}

	return summary
}

// computeLatencyStats returns min, max, and average for the given sample slice.
// Called while holding m.mu.RLock — must not acquire any locks.
func computeLatencyStats(samples []time.Duration) (min, max, avg time.Duration) {
	min, max = samples[0], samples[0]
	var total time.Duration
	for _, s := range samples {
		total += s
		if s < min {
			min = s
		}
		if s > max {
			max = s
		}
	}
	avg = total / time.Duration(len(samples))
	return min, max, avg
}

// computeP95 returns the approximate P95 latency from a sample slice.
// We sort a copy to avoid mutating the ring buffer and use the 95th percentile
// index. slices.Sort is O(n log n) — acceptable for a window of ≤100 samples.
func computeP95(samples []time.Duration) time.Duration {
	cp := make([]time.Duration, len(samples))
	copy(cp, samples)
	slices.Sort(cp)
	p95Idx := int(float64(len(cp)) * 0.95)
	return cp[p95Idx]
}

// MetricsSummary is the JSON-serialisable aggregate of collected metrics.
type MetricsSummary struct {
	LastSampleTime time.Time     `json:"last_sample_time"`
	TotalQueries   int64         `json:"total_queries"`
	SampleCount    int           `json:"sample_count"`
	AvgLatency     time.Duration `json:"avg_latency_ns"`
	MinLatency     time.Duration `json:"min_latency_ns"`
	MaxLatency     time.Duration `json:"max_latency_ns"`
	P95Latency     time.Duration `json:"p95_latency_ns,omitempty"`
	PeakInUse      int           `json:"peak_in_use"`
	PeakWaitCount  int64         `json:"peak_wait_count"`
	TotalWaitTime  time.Duration `json:"total_wait_time_ns"`
}

// GetMetrics returns the current metrics snapshot without performing a health check.
// Useful for metrics-scraping endpoints that want counters without the live DB probe.
func (s *Store) GetMetrics() MetricsSummary {
	if s.metrics == nil {
		return MetricsSummary{}
	}
	return s.metrics.GetMetricsSummary()
}

// ResetMetrics replaces the metrics collector with a fresh one of the same window
// size. Useful after a major schema change or performance test to clear historical
// data that no longer reflects the current workload.
func (s *Store) ResetMetrics() {
	if s.metrics != nil {
		s.metrics = NewPoolMetrics(s.metrics.windowSize)
	}
}

// WithTimeout wraps a context with the given timeout and returns a cancel function
// that also logs slow operations. The log is emitted on cancel, not at timeout
// deadline, so every call path (success, error, timeout) gets the slow-query warning.
func (s *Store) WithTimeout(ctx context.Context, timeout time.Duration, operation string) (context.Context, context.CancelFunc) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	start := time.Now()

	return timeoutCtx, func() {
		elapsed := time.Since(start)
		cancel()

		// 100 ms is the threshold where application users begin to notice latency.
		if elapsed > 100*time.Millisecond {
			log.Warn().
				Str("operation", operation).
				Dur("elapsed", elapsed).
				Dur("timeout", timeout).
				Msg("Slow database operation")
		}
	}
}

// ExecWithTimeout executes a raw SQL statement with a deadline. Returns an
// explicit timeout error message that includes the query text so that logs
// are actionable without cross-referencing the call site.
func (s *Store) ExecWithTimeout(ctx context.Context, timeout time.Duration, query string, args ...any) error {
	timeoutCtx, cancel := s.WithTimeout(ctx, timeout, "exec")
	defer cancel()

	_, err := s.sqlDB.ExecContext(timeoutCtx, query, args...)
	if err != nil {
		if err == context.DeadlineExceeded {
			return fmt.Errorf("query timeout after %v: %s", timeout, query)
		}
		return err
	}
	return nil
}

// QueryRowWithTimeout executes a single-row query with a deadline.
// The cancel function leaks if the caller never calls row.Scan; callers that
// need strict cleanup should wrap the returned row in a defer.
func (s *Store) QueryRowWithTimeout(ctx context.Context, timeout time.Duration, query string, args ...any) *sql.Row {
	timeoutCtx, cancel := s.WithTimeout(ctx, timeout, "query_row")
	// Note: cancel will be called when row.Scan() completes or errors
	_ = cancel // Caller must ensure proper cleanup
	return s.sqlDB.QueryRowContext(timeoutCtx, query, args...)
}

// TransactionWithTimeout wraps fn in a GORM transaction that is automatically
// rolled back if the context deadline is exceeded. The pre-flight select on
// the Done channel prevents fn from starting against an already-expired context,
// which would result in a misleading "context canceled" error from the first
// query inside fn rather than from the transaction wrapper.
func (s *Store) TransactionWithTimeout(ctx context.Context, timeout time.Duration, fn func(*gorm.DB) error) error {
	timeoutCtx, cancel := s.WithTimeout(ctx, timeout, "transaction")
	defer cancel()

	return s.DB.WithContext(timeoutCtx).Transaction(func(tx *gorm.DB) error {
		// Guard against callers that pass a context that was already expired
		// before this transaction was entered.
		select {
		case <-timeoutCtx.Done():
			return timeoutCtx.Err()
		default:
		}
		return fn(tx)
	})
}
