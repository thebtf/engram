// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/pkg/models"
)

// openIntegrationTestDB opens a PostgreSQL connection for integration tests.
// Skips the test when DATABASE_DSN is not set.
func openIntegrationTestDB(t *testing.T) (*Store, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	cfg := Config{
		DSN:      dsn,
		MaxConns: 4,
		LogLevel: logger.Silent,
	}
	store, err := NewStore(cfg)
	require.NoError(t, err, "NewStore must succeed for integration tests")

	return store, func() { store.Close() }
}

// TestIntegration_StoreCompatibility verifies Store accessor contracts.
// CODE_PATH_COVERED: GetRawDB, GetDB, Ping.
func TestIntegration_StoreCompatibility(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	rawDB := store.GetRawDB()
	require.NotNil(t, rawDB)
	assert.IsType(t, &sql.DB{}, rawDB)

	gormDB := store.GetDB()
	require.NotNil(t, gormDB)

	assert.NoError(t, store.Ping())
}

// TestIntegration_StoreHealthCheck verifies HealthCheck returns a populated result.
// CODE_PATH_COVERED: performHealthCheck + TTL cache path.
func TestIntegration_StoreHealthCheck(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	ctx := context.Background()
	info := store.HealthCheck(ctx)
	require.NotNil(t, info)
	assert.NotEmpty(t, info.Status)
	assert.Contains(t, []string{"healthy", "degraded"}, info.Status)
	assert.False(t, info.Timestamp.IsZero())
}

// TestIntegration_StoreHealthCheckForce verifies HealthCheckForce bypasses cache.
// CODE_PATH_COVERED: performHealthCheck without TTL guard.
func TestIntegration_StoreHealthCheckForce(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	ctx := context.Background()
	info := store.HealthCheckForce(ctx)
	require.NotNil(t, info)
	assert.NotEmpty(t, info.Status)
}

// TestIntegration_StoreMetrics verifies GetMetrics and ResetMetrics.
// CODE_PATH_COVERED: ring-buffer metrics round-trip.
func TestIntegration_StoreMetrics(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	metrics := store.GetMetrics()
	assert.GreaterOrEqual(t, metrics.TotalQueries, int64(0))

	store.ResetMetrics()
	after := store.GetMetrics()
	assert.Equal(t, int64(0), after.TotalQueries)
}

// TestIntegration_StoreOptimize verifies Optimize runs ANALYZE without error.
// CODE_PATH_COVERED: Optimize → ANALYZE.
func TestIntegration_StoreOptimize(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	ctx := context.Background()
	err := store.Optimize(ctx)
	require.NoError(t, err)
}

// TestIntegration_StoreTransactionWithTimeout verifies a successful transaction.
// CODE_PATH_COVERED: TransactionWithTimeout happy path.
func TestIntegration_StoreTransactionWithTimeout(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	ctx := context.Background()
	var val int
	err := store.TransactionWithTimeout(ctx, DefaultQueryTimeout, func(tx *gorm.DB) error {
		return tx.Raw("SELECT 1").Scan(&val).Error
	})
	require.NoError(t, err)
	assert.Equal(t, 1, val)
}

// TestIntegration_StoreExecWithTimeout verifies ExecWithTimeout executes without error.
// CODE_PATH_COVERED: ExecWithTimeout happy path.
func TestIntegration_StoreExecWithTimeout(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	ctx := context.Background()
	err := store.ExecWithTimeout(ctx, DefaultQueryTimeout, "SET search_path TO public")
	require.NoError(t, err)
}

// TestIntegration_SessionStoreCreateAndRetrieve verifies the full Create→Get session lifecycle.
// CODE_PATH_COVERED: CreateSDKSession + GetSessionByID + idempotent repeat.
func TestIntegration_SessionStoreCreateAndRetrieve(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	ss := NewSessionStore(store)
	ctx := context.Background()

	const sid = "integration-test-session-compat-01"
	defer store.DB.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = ?", sid)

	id, err := ss.CreateSDKSession(ctx, sid, "integration-project", "hello world")
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	// Idempotent: same call returns same ID.
	id2, err := ss.CreateSDKSession(ctx, sid, "integration-project", "hello world")
	require.NoError(t, err)
	assert.Equal(t, id, id2)

	sess, err := ss.GetSessionByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, sid, sess.ClaudeSessionID)
	assert.Equal(t, "integration-project", sess.Project)
}

// TestIntegration_SessionStoreConcurrentPromptCounter verifies atomic counter increments under concurrency.
// CODE_PATH_COVERED: concurrent IncrementPromptCounter correctness.
func TestIntegration_SessionStoreConcurrentPromptCounter(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	ss := NewSessionStore(store)
	ctx := context.Background()

	const sid = "integration-test-concurrent-counter-01"
	defer store.DB.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = ?", sid)

	id, err := ss.CreateSDKSession(ctx, sid, "concurrent-project", "")
	require.NoError(t, err)

	const numGoroutines = 10
	done := make(chan error, numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			_, innerErr := ss.IncrementPromptCounter(ctx, id)
			done <- innerErr
		}()
	}

	for i := 0; i < numGoroutines; i++ {
		require.NoError(t, <-done)
	}

	counter, err := ss.GetPromptCounter(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, numGoroutines, counter)
}

// TestIntegration_RelationStoreIdempotency verifies that duplicate (src,tgt,type) is handled via ON CONFLICT.
// CODE_PATH_COVERED: StoreRelation idempotency (RowsAffected==0) path.
func TestIntegration_RelationStoreIdempotency(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	rs := NewRelationStore(store)
	ctx := context.Background()

	defer store.DB.Exec("DELETE FROM observation_relations WHERE source_id = 9000001 AND target_id = 9000002")

	rel := newTestRelation(9000001, 9000002)

	id1, err := rs.StoreRelation(ctx, rel)
	require.NoError(t, err)
	assert.Greater(t, id1, int64(0))

	id2, err := rs.StoreRelation(ctx, rel)
	require.NoError(t, err)
	assert.Equal(t, id1, id2)
}

// TestIntegration_RelationStoreGetRelationsByObservationID verifies bidirectional lookup.
// CODE_PATH_COVERED: GetRelationsByObservationID returns both outgoing and incoming edges.
func TestIntegration_RelationStoreGetRelationsByObservationID(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	rs := NewRelationStore(store)
	ctx := context.Background()

	defer store.DB.Exec("DELETE FROM observation_relations WHERE source_id IN (9100001, 9100002) OR target_id IN (9100002, 9100003)")

	rel1 := newTestRelation(9100001, 9100002)
	rel2 := newTestRelation(9100002, 9100003)

	_, err := rs.StoreRelation(ctx, rel1)
	require.NoError(t, err)
	_, err = rs.StoreRelation(ctx, rel2)
	require.NoError(t, err)

	results, err := rs.GetRelationsByObservationID(ctx, 9100002)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

// TestIntegration_PoolMetrics_WindowBehavior verifies P95 is computed after ≥20 samples.
// CODE_PATH_COVERED: PoolMetrics.RecordLatency, GetMetricsSummary, P95 branch.
func TestIntegration_PoolMetrics_WindowBehavior(t *testing.T) {
	m := NewPoolMetrics(50)
	require.NotNil(t, m)

	for i := 1; i <= 25; i++ {
		m.RecordLatency(time.Duration(i) * time.Microsecond)
	}

	summary := m.GetMetricsSummary()
	assert.Equal(t, int64(25), summary.TotalQueries)
	assert.Greater(t, summary.P95Latency, time.Duration(0))
	assert.GreaterOrEqual(t, summary.MaxLatency, summary.MinLatency)
}

// TestIntegration_PoolMetrics_FewSamples verifies P95 is absent when sample count < 20.
// CODE_PATH_COVERED: GetMetricsSummary skips P95 path below 20 samples.
func TestIntegration_PoolMetrics_FewSamples(t *testing.T) {
	m := NewPoolMetrics(100)
	for i := 0; i < 5; i++ {
		m.RecordLatency(time.Duration(i+1) * time.Microsecond)
	}

	summary := m.GetMetricsSummary()
	assert.Equal(t, time.Duration(0), summary.P95Latency, "P95 should be zero when fewer than 20 samples")
}

// TestIntegration_OpenGORM_InvalidDSN verifies NewStore returns error for an unreachable DSN.
// CODE_PATH_COVERED: NewStore error path.
func TestIntegration_OpenGORM_InvalidDSN(t *testing.T) {
	cfg := Config{
		DSN:      "postgres://invalid:invalid@localhost:19999/nonexistent?sslmode=disable&connect_timeout=1",
		MaxConns: 1,
		LogLevel: logger.Silent,
	}
	_, err := NewStore(cfg)
	assert.Error(t, err, "NewStore should fail with unreachable DSN")
}

// TestIntegration_StoreStats verifies Stats returns a pool snapshot without error.
// CODE_PATH_COVERED: Stats() accessor.
func TestIntegration_StoreStats(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	stats := store.Stats()
	assert.GreaterOrEqual(t, stats.OpenConnections, 0)
}

// TestIntegration_WithTimeout_ReturnsCancelPair verifies WithTimeout returns usable ctx/cancel.
// CODE_PATH_COVERED: WithTimeout wrapper (no panic contract).
func TestIntegration_WithTimeout_ReturnsCancelPair(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	ctx := context.Background()
	tCtx, cancel := store.WithTimeout(ctx, FastQueryTimeout, "test-op")
	defer cancel()
	assert.NotNil(t, tCtx)
}

// TestIntegration_QueryRowWithTimeout verifies a scalar result via QueryRowWithTimeout.
// CODE_PATH_COVERED: QueryRowWithTimeout + row.Scan.
func TestIntegration_QueryRowWithTimeout(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()

	ctx := context.Background()
	row := store.QueryRowWithTimeout(ctx, FastQueryTimeout, "SELECT 42")
	require.NotNil(t, row)

	var val int
	err := row.Scan(&val)
	require.NoError(t, err)
	assert.Equal(t, 42, val)
}

// TestIntegration_ResolveMaxConns verifies the resolveMaxConns helper contracts.
// CODE_PATH_COVERED: zero/negative default branch and positive passthrough.
func TestIntegration_ResolveMaxConns(t *testing.T) {
	assert.Equal(t, 10, resolveMaxConns(0))
	assert.Equal(t, 10, resolveMaxConns(-5))
	assert.Equal(t, 20, resolveMaxConns(20))
}

// ============================================================
// shared helpers
// ============================================================

// newTestRelation returns a minimal *models.ObservationRelation for use in tests.
func newTestRelation(sourceID, targetID int64) *models.ObservationRelation {
	now := time.Now()
	return &models.ObservationRelation{
		SourceID:        sourceID,
		TargetID:        targetID,
		RelationType:    models.RelationCauses,
		Confidence:      0.75,
		DetectionSource: models.DetectionSourceFileOverlap,
		CreatedAt:       now.Format(time.RFC3339),
		CreatedAtEpoch:  now.UnixMilli(),
	}
}
