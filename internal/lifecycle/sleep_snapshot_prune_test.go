// Package lifecycle — sleep_snapshot_prune_test.go: T049 snapshot auto-prune tests.
// RED phase: verifies 30-day retention prune in the sleep cycle.
// AC: delete bulk_op_snapshots older than ENGRAM_SNAPSHOT_RETENTION_DAYS (default 30);
//     pinned=true rows are exempt.
// Anti-stub: row count verified after prune.
package lifecycle

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "github.com/lib/pq" // postgres driver for integration tests
)

// mockSnapshotPruner is a minimal in-memory SnapshotPruner for unit tests.
type mockSnapshotPruner struct {
	snapshots []mockSnapshot
	pruned    int
}

type mockSnapshot struct {
	id        int
	createdAt time.Time
	pinned    bool
}

func (m *mockSnapshotPruner) DeleteOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	var deleted int64
	remaining := m.snapshots[:0]
	for _, s := range m.snapshots {
		if s.createdAt.Before(cutoff) && !s.pinned {
			deleted++
		} else {
			remaining = append(remaining, s)
		}
	}
	m.snapshots = remaining
	m.pruned += int(deleted)
	return deleted, nil
}

// seedSnapshots adds n snapshots with the given age and pinned state.
func (m *mockSnapshotPruner) seedSnapshots(count int, age time.Duration, pinned bool) {
	base := len(m.snapshots)
	for i := 0; i < count; i++ {
		m.snapshots = append(m.snapshots, mockSnapshot{
			id:        base + i + 1,
			createdAt: time.Now().UTC().Add(-age),
			pinned:    pinned,
		})
	}
}

// TestPruneSnapshots_DefaultRetention verifies the default 30-day retention.
// Snapshots older than 30 days and NOT pinned must be pruned.
func TestPruneSnapshots_DefaultRetention(t *testing.T) {
	t.Setenv("ENGRAM_SNAPSHOT_RETENTION_DAYS", "") // use default
	pruner := &mockSnapshotPruner{}
	pruner.seedSnapshots(3, 31*24*time.Hour, false) // 3 old, non-pinned
	pruner.seedSnapshots(2, 29*24*time.Hour, false) // 2 recent, non-pinned

	pruned, err := PruneSnapshots(context.Background(), pruner)
	require.NoError(t, err)
	assert.Equal(t, int64(3), pruned, "must prune 3 snapshots older than 30 days")
	assert.Equal(t, 2, len(pruner.snapshots), "2 recent snapshots must survive")
}

// TestPruneSnapshots_PinnedExempt verifies pinned snapshots survive pruning
// regardless of age.
func TestPruneSnapshots_PinnedExempt(t *testing.T) {
	t.Setenv("ENGRAM_SNAPSHOT_RETENTION_DAYS", "") // use default
	pruner := &mockSnapshotPruner{}
	pruner.seedSnapshots(2, 31*24*time.Hour, false) // 2 old, non-pinned → pruned
	pruner.seedSnapshots(3, 31*24*time.Hour, true)  // 3 old, pinned → survive

	pruned, err := PruneSnapshots(context.Background(), pruner)
	require.NoError(t, err)
	assert.Equal(t, int64(2), pruned, "must prune 2 non-pinned old snapshots")
	assert.Equal(t, 3, len(pruner.snapshots), "3 pinned snapshots must survive")
}

// TestPruneSnapshots_RetentionDaysOverride verifies ENGRAM_SNAPSHOT_RETENTION_DAYS.
func TestPruneSnapshots_RetentionDaysOverride(t *testing.T) {
	t.Setenv("ENGRAM_SNAPSHOT_RETENTION_DAYS", "7") // 7-day override
	pruner := &mockSnapshotPruner{}
	pruner.seedSnapshots(2, 8*24*time.Hour, false) // older than 7 days → pruned
	pruner.seedSnapshots(2, 6*24*time.Hour, false) // within 7 days → survive

	pruned, err := PruneSnapshots(context.Background(), pruner)
	require.NoError(t, err)
	assert.Equal(t, int64(2), pruned, "with 7-day override, snapshots older than 7 days must be pruned")
	assert.Equal(t, 2, len(pruner.snapshots))
}

// TestPruneSnapshots_ZeroCount verifies no error when no snapshots qualify.
func TestPruneSnapshots_ZeroCount(t *testing.T) {
	t.Setenv("ENGRAM_SNAPSHOT_RETENTION_DAYS", "")
	pruner := &mockSnapshotPruner{}
	pruner.seedSnapshots(5, 20*24*time.Hour, false) // all recent

	pruned, err := PruneSnapshots(context.Background(), pruner)
	require.NoError(t, err)
	assert.Equal(t, int64(0), pruned, "no snapshots should be pruned when all are recent")
	assert.Equal(t, 5, len(pruner.snapshots))
}

// TestPruneSnapshots_InvalidRetentionDays verifies that invalid env value falls back to default.
func TestPruneSnapshots_InvalidRetentionDays(t *testing.T) {
	t.Setenv("ENGRAM_SNAPSHOT_RETENTION_DAYS", "not-a-number")
	pruner := &mockSnapshotPruner{}
	pruner.seedSnapshots(1, 31*24*time.Hour, false) // should be pruned by 30-day default

	pruned, err := PruneSnapshots(context.Background(), pruner)
	require.NoError(t, err, "invalid env value must fall back to default (no error)")
	assert.Equal(t, int64(1), pruned, "invalid env must fall back to 30-day default")
}

// TestSleepResult_SnapshotsPruned verifies SleepResult.SnapshotsPruned field.
func TestSleepResult_SnapshotsPruned(t *testing.T) {
	result := SleepResult{SnapshotsPruned: 5}
	assert.Equal(t, 5, result.SnapshotsPruned)
	_ = fmt.Sprintf("%v", result) // ensure field is part of struct without panic
}

// TestPruneSnapshots_Integration is the anti-stub integration test.
// Requires DATABASE_DSN (postgres DSN). Skipped when absent.
// AC: a 31-day-old non-pinned row is pruned; a pinned row of the same age survives;
//     a recent non-pinned row survives.
func TestPruneSnapshots_Integration(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set — skipping integration snapshot prune test")
	}

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err, "open DATABASE_DSN")
	require.NoError(t, db.PingContext(context.Background()), "ping DATABASE_DSN")
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	cutoff31Days := time.Now().UTC().Add(-31 * 24 * time.Hour)

	oldID := integInsertSnapshot(t, db, ctx, cutoff31Days.Add(-time.Hour), false) // must be pruned
	pinnedID := integInsertSnapshot(t, db, ctx, cutoff31Days.Add(-time.Hour), true) // must survive
	freshID := integInsertSnapshot(t, db, ctx, time.Now().UTC(), false)             // must survive (recent)

	pruner := &integrationSnapshotPruner{db: db}
	pruned, err := PruneSnapshots(ctx, pruner)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, pruned, int64(1), "at least the 31-day-old non-pinned snapshot must be pruned")

	assert.False(t, integSnapshotExists(t, db, ctx, oldID), "31-day-old non-pinned snapshot must be pruned")
	assert.True(t, integSnapshotExists(t, db, ctx, pinnedID), "pinned snapshot must survive pruning")
	assert.True(t, integSnapshotExists(t, db, ctx, freshID), "recent snapshot must survive pruning")
}

// integrationSnapshotPruner implements SnapshotPruner via raw database/sql.
// Kept in the test file so the lifecycle package itself has no DB dependency.
type integrationSnapshotPruner struct {
	db *sql.DB
}

func (p *integrationSnapshotPruner) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := p.db.ExecContext(ctx,
		`DELETE FROM bulk_op_snapshots WHERE created_at < $1 AND pinned = false`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("integration prune: %w", err)
	}
	return res.RowsAffected()
}

func integInsertSnapshot(t *testing.T, db *sql.DB, ctx context.Context, createdAt time.Time, pinned bool) string {
	t.Helper()
	id := fmt.Sprintf("prune-integ-%d-%v", time.Now().UnixNano(), pinned)
	_, err := db.ExecContext(ctx,
		`INSERT INTO bulk_op_snapshots (snapshot_id, op_type, actor, before_state, status, pinned, created_at)
		 VALUES ($1, 'bulk_delete', 'test', '{}', 'committed', $2, $3)`,
		id, pinned, createdAt,
	)
	require.NoError(t, err, "insert integration snapshot")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(),
			`DELETE FROM bulk_op_snapshots WHERE snapshot_id = $1`, id)
	})
	return id
}

func integSnapshotExists(t *testing.T, db *sql.DB, ctx context.Context, snapshotID string) bool {
	t.Helper()
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM bulk_op_snapshots WHERE snapshot_id = $1`, snapshotID,
	).Scan(&count)
	require.NoError(t, err)
	return count > 0
}
