package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

type continuitySlotMemorySnapshot struct {
	Content   string
	DeletedAt *time.Time
	UpdatedAt time.Time
}

func createContinuitySlotTestMemory(t *testing.T, db *gormlib.DB, project, content string) int64 {
	t.Helper()

	var memoryID int64
	require.NoError(t, db.Raw(
		`INSERT INTO memories (project, content) VALUES (?, ?) RETURNING id`, project, content,
	).Scan(&memoryID).Error)
	require.Positive(t, memoryID)
	return memoryID
}

func continuitySlotTestMemorySnapshot(t *testing.T, db *gormlib.DB, memoryID int64) continuitySlotMemorySnapshot {
	t.Helper()

	var snapshot continuitySlotMemorySnapshot
	require.NoError(t, db.Raw(
		`SELECT content, deleted_at, updated_at FROM memories WHERE id = ?`, memoryID,
	).Scan(&snapshot).Error)
	return snapshot
}

func cleanupContinuitySlotTestProject(t *testing.T, db *gormlib.DB, project string) {
	t.Helper()
	require.NoError(t, db.Exec(`DELETE FROM project_continuity_slots WHERE project = ?`, project).Error)
	require.NoError(t, db.Exec(`DELETE FROM memories WHERE project = ?`, project).Error)
}

func TestContinuitySlotStoreReplacesClearsAndPreservesMemoryTargets(t *testing.T) {
	db, closeDB := openTestDB(t)
	t.Cleanup(closeDB)

	ctx := context.Background()
	project := fmt.Sprintf("test-continuity-slot-%d", time.Now().UnixNano())
	t.Cleanup(func() { cleanupContinuitySlotTestProject(t, db, project) })

	firstMemoryID := createContinuitySlotTestMemory(t, db, project, "first continuity target")
	secondMemoryID := createContinuitySlotTestMemory(t, db, project, "replacement continuity target")
	firstMemoryBefore := continuitySlotTestMemorySnapshot(t, db, firstMemoryID)
	secondMemoryBefore := continuitySlotTestMemorySnapshot(t, db, secondMemoryID)

	store := NewContinuitySlotStore(db)
	firstExpiry := time.Date(2028, time.January, 2, 3, 4, 5, 0, time.FixedZone("test-offset", -7*60*60))
	require.NoError(t, store.Upsert(ctx, ContinuitySlot{
		Project:                     project,
		MemoryID:                    firstMemoryID,
		ExpiresAt:                   firstExpiry,
		AuthorityDomain:             "continuity-domain",
		AuthorityOwnerPrincipal:     "agent:planner",
		AuthorityOwnerPrincipalKind: "agent",
	}))

	got, err := store.Get(ctx, project)
	require.NoError(t, err)
	require.Equal(t, firstMemoryID, got.MemoryID)
	require.Equal(t, firstExpiry.UTC(), got.ExpiresAt)
	require.Equal(t, time.UTC, got.ExpiresAt.Location())
	require.Equal(t, "continuity-domain", got.AuthorityDomain)
	require.Equal(t, "agent:planner", got.AuthorityOwnerPrincipal)
	require.Equal(t, "agent", got.AuthorityOwnerPrincipalKind)

	secondExpiry := firstExpiry.Add(24 * time.Hour)
	require.NoError(t, store.Upsert(ctx, ContinuitySlot{
		Project:                     project,
		MemoryID:                    secondMemoryID,
		ExpiresAt:                   secondExpiry,
		AuthorityDomain:             "replacement-domain",
		AuthorityOwnerPrincipal:     "human:release-owner",
		AuthorityOwnerPrincipalKind: "human",
	}))

	var slotCount int64
	require.NoError(t, db.Model(&ContinuitySlot{}).Where("project = ?", project).Count(&slotCount).Error)
	require.Equal(t, int64(1), slotCount, "one project must have exactly one replaceable slot")

	got, err = store.Get(ctx, project)
	require.NoError(t, err)
	require.Equal(t, secondMemoryID, got.MemoryID)
	require.Equal(t, secondExpiry.UTC(), got.ExpiresAt)
	require.Equal(t, "replacement-domain", got.AuthorityDomain)
	require.Equal(t, "human:release-owner", got.AuthorityOwnerPrincipal)
	require.Equal(t, "human", got.AuthorityOwnerPrincipalKind)

	cleared, err := store.Clear(ctx, project)
	require.NoError(t, err)
	require.True(t, cleared)
	cleared, err = store.Clear(ctx, project)
	require.NoError(t, err)
	require.False(t, cleared, "clearing a missing slot must be idempotent")
	_, err = store.Get(ctx, project)
	require.ErrorIs(t, err, gormlib.ErrRecordNotFound)

	require.NoError(t, store.Upsert(ctx, ContinuitySlot{
		Project:                     project,
		MemoryID:                    secondMemoryID,
		ExpiresAt:                   secondExpiry,
		AuthorityDomain:             "replacement-domain",
		AuthorityOwnerPrincipal:     "human:release-owner",
		AuthorityOwnerPrincipalKind: "human",
	}))
	cleared, err = store.ClearByMemory(ctx, firstMemoryID)
	require.NoError(t, err)
	require.False(t, cleared, "clearing a non-target must leave the slot intact")
	_, err = store.Get(ctx, project)
	require.NoError(t, err)

	cleared, err = store.ClearByMemory(ctx, secondMemoryID)
	require.NoError(t, err)
	require.True(t, cleared)
	_, err = store.Get(ctx, project)
	require.ErrorIs(t, err, gormlib.ErrRecordNotFound)

	require.Equal(t, firstMemoryBefore, continuitySlotTestMemorySnapshot(t, db, firstMemoryID))
	require.Equal(t, secondMemoryBefore, continuitySlotTestMemorySnapshot(t, db, secondMemoryID))
}

func TestContinuitySlotMigrationCreatesSchemaAndRestrictsTargetDeletion(t *testing.T) {
	db, closeDB := openTestDB(t)
	t.Cleanup(closeDB)

	var tableCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'project_continuity_slots'
	`).Scan(&tableCount).Error)
	require.Equal(t, 1, tableCount)

	for _, column := range []string{
		"project", "memory_id", "expires_at", "authority_domain",
		"authority_owner_principal", "authority_owner_principal_kind", "created_at", "updated_at",
	} {
		var columnCount int
		require.NoError(t, db.Raw(`
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'project_continuity_slots' AND column_name = ?
		`, column).Scan(&columnCount).Error)
		require.Equal(t, 1, columnCount, "column %q must exist", column)
	}

	var indexCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) FROM pg_indexes
		WHERE schemaname = 'public'
		  AND tablename = 'project_continuity_slots'
		  AND indexname IN ('idx_project_continuity_slots_memory_id', 'idx_project_continuity_slots_expires_at')
	`).Scan(&indexCount).Error)
	require.Equal(t, 2, indexCount, "memory-target and expiry indexes must exist")

	var deleteRule string
	require.NoError(t, db.Raw(`
		SELECT rc.delete_rule
		FROM information_schema.referential_constraints rc
		JOIN information_schema.table_constraints tc
		  ON tc.constraint_catalog = rc.constraint_catalog
		 AND tc.constraint_schema = rc.constraint_schema
		 AND tc.constraint_name = rc.constraint_name
		WHERE tc.table_schema = 'public'
		  AND tc.table_name = 'project_continuity_slots'
		  AND tc.constraint_type = 'FOREIGN KEY'
	`).Scan(&deleteRule).Error)
	require.Equal(t, "RESTRICT", deleteRule)
}

func TestContinuitySlotMigrationRollbackRefusesNonEmptyTable(t *testing.T) {
	db, closeDB := openTestDB(t)
	t.Cleanup(closeDB)

	ctx := context.Background()
	project := fmt.Sprintf("test-continuity-rollback-%d", time.Now().UnixNano())
	t.Cleanup(func() { cleanupContinuitySlotTestProject(t, db, project) })

	memoryID := createContinuitySlotTestMemory(t, db, project, "rollback continuity target")
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()

	require.NoError(t, NewContinuitySlotStore(db).UpsertTx(ctx, tx, ContinuitySlot{
		Project:                     project,
		MemoryID:                    memoryID,
		ExpiresAt:                   time.Now().UTC().Add(time.Hour),
		AuthorityDomain:             "continuity-domain",
		AuthorityOwnerPrincipal:     "agent:planner",
		AuthorityOwnerPrincipalKind: "agent",
	}))

	err := continuitySlotMigration161().Rollback(tx)
	require.ErrorContains(t, err, "project_continuity_slots rows exist")
	require.True(t, tx.Migrator().HasTable("project_continuity_slots"), "blocked rollback must leave the slot table intact")
}
