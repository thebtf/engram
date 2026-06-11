package gorm

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/pkg/models"
)

// TestSnapshotStore_CRUD tests Create/Get/List/MarkRolledBack/Pin/DeleteOlderThan.
// Requires DATABASE_DSN. Skips in CI without it.
// Engram vNext Milestone F TG6 / T040.
func TestSnapshotStore_CRUD(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, sqlDB.Ping())
	require.NoError(t, runMigrations(db))

	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM bulk_op_snapshots WHERE snapshot_id LIKE 'test-store-%'`).Error
	})

	store := NewSnapshotStore(db)
	ctx := context.Background()

	// --- Create ---
	bs := json.RawMessage(`{"42":{"content":"original memory content"}}`)
	snap, err := models.NewBulkOpSnapshot("test-store-001", models.SnapshotOpBulkPromote, "test-actor", bs)
	require.NoError(t, err)
	snap.AffectedMemoryIDs = []int64{42, 99}
	snap.SourceSessionID = "sess-test"

	created, err := store.Create(ctx, snap)
	require.NoError(t, err)
	require.NotZero(t, created.ID)
	require.Equal(t, "test-store-001", created.SnapshotID)
	require.Equal(t, models.SnapshotOpBulkPromote, created.OpType)
	require.Equal(t, models.SnapshotStatusCommitted, created.Status)
	require.Equal(t, []int64{42, 99}, created.AffectedMemoryIDs)

	// --- Get ---
	got, err := store.Get(ctx, "test-store-001")
	require.NoError(t, err)
	require.Equal(t, created.ID, got.ID)
	require.Equal(t, "test-actor", got.Actor)
	require.Equal(t, "sess-test", got.SourceSessionID)
	require.False(t, got.Pinned)

	// --- GetByID ---
	got2, err := store.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, "test-store-001", got2.SnapshotID)

	// --- Create a second snapshot for list test ---
	snap2, _ := models.NewBulkOpSnapshot("test-store-002", models.SnapshotOpBulkDelete, "test-actor", json.RawMessage(`{}`))
	_, err = store.Create(ctx, snap2)
	require.NoError(t, err)

	// --- List (no filter) ---
	list, err := store.List(ctx, "", "", 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(list), 2, "must return at least 2 test snapshots")

	// --- List (filtered by op_type) ---
	filtered, err := store.List(ctx, models.SnapshotOpBulkDelete, "", 10)
	require.NoError(t, err)
	for _, s := range filtered {
		require.Equal(t, models.SnapshotOpBulkDelete, s.OpType, "all returned snaps must have bulk_delete op_type")
	}

	// --- MarkRolledBack ---
	err = store.MarkRolledBack(ctx, "test-store-001")
	require.NoError(t, err)

	rolledBack, err := store.Get(ctx, "test-store-001")
	require.NoError(t, err)
	require.Equal(t, models.SnapshotStatusRolledBack, rolledBack.Status)
	require.NotNil(t, rolledBack.RolledBackAt)

	// Idempotency: marking rolled-back twice must fail (not committed).
	err = store.MarkRolledBack(ctx, "test-store-001")
	require.Error(t, err, "marking already-rolled-back snapshot must return error")

	// --- Pin ---
	err = store.Pin(ctx, "test-store-002")
	require.NoError(t, err)
	pinned, err := store.Get(ctx, "test-store-002")
	require.NoError(t, err)
	require.True(t, pinned.Pinned, "snapshot must be pinned after Pin()")

	// --- DeleteOlderThan: pinned snapshot must survive ---
	// Use a far-future cutoff to delete all non-pinned test rows.
	snap3, _ := models.NewBulkOpSnapshot("test-store-003", models.SnapshotOpIngestDoc, "actor2", nil)
	_, _ = store.Create(ctx, snap3)

	// Force created_at to be very old for test-store-003 by updating directly.
	_ = db.Exec(`UPDATE bulk_op_snapshots SET created_at = '2000-01-01T00:00:00Z' WHERE snapshot_id = 'test-store-003'`).Error

	deleted, err := store.DeleteOlderThan(ctx, time.Now())
	require.NoError(t, err)
	require.GreaterOrEqual(t, deleted, int64(1), "at least 1 old non-pinned snap must be deleted")

	// Pinned snap must still exist.
	_, err = store.Get(ctx, "test-store-002")
	require.NoError(t, err, "pinned snapshot must survive DeleteOlderThan")
}

// TestSnapshotStore_NilSnapshot ensures nil input is rejected.
func TestSnapshotStore_NilSnapshot(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	require.NoError(t, runMigrations(db))

	store := NewSnapshotStore(db)
	_, err = store.Create(context.Background(), nil)
	require.Error(t, err, "Create with nil must return error")
}

// TestInt64Array_Roundtrip verifies the Int64Array Value/Scan cycle.
func TestInt64Array_Roundtrip(t *testing.T) {
	cases := []struct {
		name  string
		input Int64Array
		pgStr string
	}{
		{"empty", Int64Array{}, "{}"},
		{"single", Int64Array{42}, "{42}"},
		{"multi", Int64Array{1, 2, 3}, "{1,2,3}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			val, err := c.input.Value()
			require.NoError(t, err)
			require.Equal(t, c.pgStr, val)

			var decoded Int64Array
			require.NoError(t, decoded.Scan(c.pgStr))
			require.Equal(t, []int64(c.input), []int64(decoded))
		})
	}
}
