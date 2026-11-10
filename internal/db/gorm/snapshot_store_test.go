package gorm

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/pkg/models"
)

// openSnapshotTestDB creates only the store's table in an owned schema.
// It does not run migrations or install extensions in the target database.
func openSnapshotTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	if config.RuntimeParams == nil {
		config.RuntimeParams = make(map[string]string)
	}
	config.RuntimeParams["search_path"] = "pg_catalog"
	adminSQL := stdlib.OpenDB(*config)
	t.Cleanup(func() { require.NoError(t, adminSQL.Close()) })
	admin, err := gorm.Open(postgres.New(postgres.Config{Conn: adminSQL}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	schema := "snapshot_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() { require.NoError(t, admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error) })

	config.RuntimeParams["search_path"] = schema
	sqlDB := stdlib.OpenDB(*config)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	var actualSchema string
	require.NoError(t, db.Raw(`SELECT current_schema()`).Scan(&actualSchema).Error)
	require.Equal(t, schema, actualSchema)
	require.NoError(t, db.AutoMigrate(&snapshotRow{}))
	return db
}

// TestSnapshotStore_CRUD tests Create/Get/List/MarkRolledBack/Pin/DeleteOlderThan.
// Requires DATABASE_DSN. Skips in CI without it.
// Engram vNext Milestone F TG6 / T040.
func TestSnapshotStore_CRUD(t *testing.T) {
	db := openSnapshotTestDB(t)

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
	snap2, err := models.NewBulkOpSnapshot("test-store-002", models.SnapshotOpBulkDelete, "test-actor", json.RawMessage(`{}`))
	require.NoError(t, err)
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

	// --- DeleteOlderThan: old unpinned snapshot is deleted; old pinned snapshot survives ---
	snap3, err := models.NewBulkOpSnapshot("test-store-003", models.SnapshotOpIngestDoc, "actor2", nil)
	require.NoError(t, err)
	_, err = store.Create(ctx, snap3)
	require.NoError(t, err)

	require.NoError(t, db.Exec(`UPDATE bulk_op_snapshots SET created_at = '2000-01-01T00:00:00Z' WHERE snapshot_id IN ('test-store-002', 'test-store-003')`).Error)
	cutoff := time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)
	deleted, err := store.DeleteOlderThan(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted, "only the old unpinned snapshot must be deleted")
	_, err = store.Get(ctx, "test-store-003")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Pinned snap must still exist.
	_, err = store.Get(ctx, "test-store-002")
	require.NoError(t, err, "pinned snapshot must survive DeleteOlderThan")
}

// TestSnapshotStore_NilSnapshot ensures nil input is rejected.
func TestSnapshotStore_NilSnapshot(t *testing.T) {
	store := NewSnapshotStore(nil)
	_, err := store.Create(context.Background(), nil)
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
