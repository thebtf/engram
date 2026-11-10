package gorm_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/lifecycle"
)

func TestPruneSnapshots_RealStoreOwnedDatabase(t *testing.T) {
	t.Setenv("ENGRAM_SNAPSHOT_RETENTION_DAYS", "")
	db := dbgorm.OpenSnapshotPruneFixture(t)
	ctx := db.Statement.Context
	old := time.Now().UTC().Add(-31 * 24 * time.Hour)
	for _, fixture := range []struct {
		id     string
		when   time.Time
		pinned bool
	}{
		{"prune-old", old, false},
		{"prune-pinned", old, true},
		{"prune-fresh", time.Now().UTC(), false},
	} {
		require.NoError(t, db.Exec(`INSERT INTO bulk_op_snapshots (snapshot_id, op_type, actor, before_state, status, pinned, created_at)
			VALUES (?, 'bulk_delete', 'test', '{"fixture":"retention"}'::jsonb, 'committed', ?, ?)`, fixture.id, fixture.pinned, fixture.when).Error)
	}

	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM bulk_op_snapshots`).Scan(&count).Error)
	require.Equal(t, int64(3), count, "owned fixture must contain only test snapshots")
	state := func(id string) string {
		t.Helper()
		var row string
		require.NoError(t, db.Raw(`SELECT to_jsonb(bulk_op_snapshots)::text FROM bulk_op_snapshots WHERE snapshot_id = ?`, id).Scan(&row).Error)
		require.NotEmpty(t, row)
		return row
	}
	pinnedBefore, freshBefore := state("prune-pinned"), state("prune-fresh")
	store := dbgorm.NewSnapshotStore(db)
	pruned, err := lifecycle.PruneSnapshots(ctx, store)
	require.NoError(t, err)
	require.Equal(t, int64(1), pruned)
	_, err = store.Get(ctx, "prune-old")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.JSONEq(t, pinnedBefore, state("prune-pinned"), "pinned snapshot must be unchanged")
	require.JSONEq(t, freshBefore, state("prune-fresh"), "fresh snapshot must be unchanged")
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM bulk_op_snapshots`).Scan(&count).Error)
	require.Equal(t, int64(2), count)
}
