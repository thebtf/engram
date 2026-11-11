// Package bulkops — conflict_test.go: T050 acceptance test for EC-F3 rollback conflict detection.
//
// AC per tasks.md T050:
//   - Capture a snapshot, delete the memory, then alter its content.
//   - Refuse rollback and preserve the conflicting deleted state.
//   - Create a new exact snapshot and verify a successful rollback.
//
// Anti-stub: the conflict check must fire before any memory row is modified.
// Unit tests cover detectConflicts without a real DB using a mock memory store.
// Integration tests require an explicitly designated disposable test database.
package bulkops

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// --- Unit: detectConflicts via rollback admin gate (no DB) ---

// TestEC_F3_AdminGate_NoConflictCheck verifies that non-admin callers are rejected
// before any conflict detection (guard ordering: admin check first).
func TestEC_F3_AdminGate_NoConflictCheck(t *testing.T) {
	ctx := context.Background()
	// Non-admin — Rollback must return ErrAdminRequired without reaching detectConflicts.
	_, err := Rollback(ctx, readOnlyIdentity(), "snap-ec-f3-unit", nil, nil, nil, nil)
	require.ErrorIs(t, err, ErrAdminRequired,
		"non-admin must be rejected before conflict detection")
}

// TestEC_F3_ErrRollbackConflict_IsDistinct verifies the sentinel error is well-formed
// and distinct from other rollback errors.
func TestEC_F3_ErrRollbackConflict_IsDistinct(t *testing.T) {
	assert.NotErrorIs(t, ErrRollbackConflict, ErrAdminRequired)
	assert.NotErrorIs(t, ErrRollbackConflict, ErrSnapshotNotRollbackable)
	assert.Equal(t, "rollback_conflict", ErrRollbackConflict.Error())
}

// --- Integration: full EC-F3 conflict → clear → retry flow ---

func openConflictTestDB(t *testing.T) *gormdb.Store {
	t.Helper()
	dsn := bulkDisposableTestDSN(t)
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestEC_F3_ConflictDetected_Integration is the primary EC-F3 acceptance test.
//
// Flow:
//  1. Create memory M.
//  2. Capture before_state snapshot S and delete M.
//  3. Change M's content after deletion.
//  4. Rollback(S) must report M as conflicting and preserve its state.
//  5. Verify the conflict audit belongs to S.
//  6. Create a new delete snapshot S2 and verify its rollback restores M.
func TestEC_F3_ConflictDetected_Integration(t *testing.T) {
	store := openConflictTestDB(t)
	db := store.DB
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	ctx := context.Background()
	admin := auth.Identity{Role: auth.RoleAdmin, Source: auth.SourceMaster}
	snapshotID := "ec-f3-test-" + uuid.NewString()
	retrySnapshotID := "ec-f3-test-" + uuid.NewString()
	var snapshotCreated, retrySnapshotCreated bool

	// Step 1: Create memory.
	mem := &models.Memory{
		Content:     "EC-F3 original content",
		Project:     newBulkTestProject(t, store.DB),
		SourceAgent: "claude-code",
	}
	created, err := memStore.Create(ctx, mem)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM memories WHERE id = ?", created.ID).Error
		for _, snapshot := range []struct {
			id      string
			created bool
		}{{snapshotID, snapshotCreated}, {retrySnapshotID, retrySnapshotCreated}} {
			if snapshot.created {
				_ = db.Exec(`DELETE FROM audit_log WHERE action IN ('rollback','rollback_attempted_with_conflict') AND actor = 'master' AND reason LIKE ?`, "snapshot="+snapshot.id+" %").Error
				_ = db.Exec(`DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?`, snapshot.id).Error
			}
		}
	})

	// Capture the pre-delete state in a legacy snapshot.
	beforeStateMap := map[string]any{
		fmt.Sprintf("%d", created.ID): created,
	}
	beforeStateBytes, err := json.Marshal(beforeStateMap)
	require.NoError(t, err)

	snap, err := models.NewBulkOpSnapshot(
		snapshotID,
		models.SnapshotOpBulkDelete,
		"master",
		json.RawMessage(beforeStateBytes),
	)
	require.NoError(t, err)
	snap.AffectedMemoryIDs = []int64{created.ID}
	createdSnap, err := snapStore.Create(ctx, snap)
	require.NoError(t, err)
	snapshotCreated = true

	// Produce the expected legacy delete state, then independently change content.
	require.NoError(t, memStore.Delete(ctx, created.ID))
	require.NoError(t, db.Exec(
		`UPDATE memories SET updated_at = NOW(), content = 'post-snapshot edit' WHERE id = ?`,
		created.ID,
	).Error)

	// Step 4: Rollback → must return ErrRollbackConflict.
	result, rollbackErr := Rollback(ctx, admin, createdSnap.SnapshotID, snapStore, memStore, auditStore, nil)
	require.Error(t, rollbackErr)
	require.ErrorIs(t, rollbackErr, ErrRollbackConflict,
		"EC-F3: rollback of a post-snapshot-modified memory must return ErrRollbackConflict")
	require.NotNil(t, result)
	assert.Contains(t, result.ConflictIDs, created.ID,
		"EC-F3: conflicting memory ID must appear in result.ConflictIDs")

	// The deleted row must retain the conflicting content after refusal.
	notRestored, err := memStore.GetForRollbackTx(ctx, db, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "post-snapshot edit", notRestored.Content,
		"EC-F3: atomic refusal — memory must not be rolled back when conflict detected")
	assert.NotNil(t, notRestored.DeletedAt)

	// Snapshot must still be 'committed' (rollback was refused).
	snapshotAfterConflict, err := snapStore.Get(ctx, createdSnap.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, models.SnapshotStatusCommitted, snapshotAfterConflict.Status,
		"EC-F3: snapshot must remain 'committed' when rollback is refused")

	// Step 6: Verify audit_log has action='rollback_attempted_with_conflict'.
	var conflictAuditCount int64
	require.NoError(t, db.Raw(
		`SELECT COUNT(*) FROM audit_log WHERE action = ? AND actor = ? AND reason LIKE ?`,
		"rollback_attempted_with_conflict", "master", "snapshot="+snapshotID+" %",
	).Scan(&conflictAuditCount).Error)
	assert.GreaterOrEqual(t, conflictAuditCount, int64(1),
		"EC-F3: audit_log must record rollback_attempted_with_conflict")

	// Step 7: Capture a fresh exact pre-state, perform the real delete mutation,
	// and bind S2 to the immutable post-state token produced by that mutation.
	require.NoError(t, db.Exec(
		`UPDATE memories SET deleted_at = NULL, content = 'retry pre-state' WHERE id = ?`,
		created.ID,
	).Error)
	retryBefore, err := memStore.Get(ctx, created.ID)
	require.NoError(t, err)
	retryBeforeBytes, err := json.Marshal(retryBefore)
	require.NoError(t, err)
	require.NoError(t, memStore.Delete(ctx, created.ID))
	retryPostState, err := memStore.GetForRollbackTx(ctx, db, created.ID)
	require.NoError(t, err)
	retryPostStateToken, err := models.SnapshotStateToken(retryPostState)
	require.NoError(t, err)
	retryStateBytes, err := json.Marshal(map[string]models.SnapshotEntry{
		fmt.Sprintf("memory:%d", created.ID): {
			Kind:           models.EntryKindRestore,
			Before:         retryBeforeBytes,
			PostStateToken: retryPostStateToken,
		},
	})
	require.NoError(t, err)

	// Step 8: New committed snapshot S2 represents the exact retry mutation.
	snap2, err := models.NewBulkOpSnapshot(
		retrySnapshotID,
		models.SnapshotOpBulkDelete,
		"master",
		retryStateBytes,
	)
	require.NoError(t, err)
	snap2.AffectedMemoryIDs = []int64{created.ID}
	createdSnap2, err := snapStore.Create(ctx, snap2)
	require.NoError(t, err)
	retrySnapshotCreated = true

	// Step 9: Rollback S2 succeeds because the locked row still matches its token.
	result2, err := Rollback(ctx, admin, createdSnap2.SnapshotID, snapStore, memStore, auditStore, nil)
	require.NoError(t, err, "EC-F3: rollback must succeed for the exact post-state")
	require.NotNil(t, result2)
	assert.Empty(t, result2.ConflictIDs,
		"EC-F3: no conflict IDs when modification is cleared before rollback")
	restored, err := memStore.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, retryBefore.Content, restored.Content)
	assert.Equal(t, retryBefore.Version, restored.Version)
	assert.Nil(t, restored.DeletedAt)

	// Snapshot S2 must be rolled_back.
	snapshotAfterSuccess, err := snapStore.Get(ctx, createdSnap2.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, models.SnapshotStatusRolledBack, snapshotAfterSuccess.Status,
		"EC-F3: snapshot must be marked rolled_back on successful rollback")
}
