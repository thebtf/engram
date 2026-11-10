// Package lifecycle — retention cutoff policy tests for snapshot pruning.
package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
