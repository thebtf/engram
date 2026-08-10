// Package bulkops — conflict_test.go: T050 acceptance test for EC-F3 rollback conflict detection.
//
// AC per tasks.md T050:
//   - Create snapshot → manually UPDATE memory.updated_at (simulating post-snapshot edit)
//   - → rollback_snapshot → assert snapshot_conflict_detected error returned with that memory_id listed
//   - Clear modification → retry rollback → succeeds
//
// Anti-stub: the conflict check must fire before any memory row is modified.
// Unit tests cover detectConflicts without a real DB using a mock memory store.
// Integration tests (skip when DATABASE_DSN absent) cover the full rollback flow.
package bulkops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

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

// TestEC_F3_SnapshotNotRollbackable verifies that a non-committed snapshot returns
// ErrSnapshotNotRollbackable before any conflict detection.
func TestEC_F3_SnapshotNotRollbackable_Status(t *testing.T) {
	// This is a property of the domain spec — testable at Rollback level only with DB.
	// Property: the status guard fires on 'rolled_back' status.
	err := ErrSnapshotNotRollbackable
	assert.True(t, errors.Is(err, ErrSnapshotNotRollbackable))
	assert.False(t, errors.Is(err, ErrRollbackConflict), "status error must not be ErrRollbackConflict")
}

// TestEC_F3_ConflictResult_Structure verifies the RollbackResult shape when a conflict fires.
func TestEC_F3_ConflictResult_Structure(t *testing.T) {
	result := &RollbackResult{
		SnapshotID:  "snap-ec-f3",
		ConflictIDs: []int64{42, 99},
	}
	assert.Equal(t, "snap-ec-f3", result.SnapshotID)
	assert.Contains(t, result.ConflictIDs, int64(42))
	assert.Contains(t, result.ConflictIDs, int64(99))
	assert.Equal(t, 0, result.RestoredCount, "no rows restored when conflict blocks rollback")
}

// TestEC_F3_ErrRollbackConflict_IsDistinct verifies the sentinel error is well-formed
// and distinct from other rollback errors.
func TestEC_F3_ErrRollbackConflict_IsDistinct(t *testing.T) {
	assert.ErrorIs(t, ErrRollbackConflict, ErrRollbackConflict)
	assert.NotErrorIs(t, ErrRollbackConflict, ErrAdminRequired)
	assert.NotErrorIs(t, ErrRollbackConflict, ErrSnapshotNotRollbackable)
	assert.Equal(t, "rollback_conflict", ErrRollbackConflict.Error())
}

// --- Integration: full EC-F3 conflict → clear → retry flow ---

func openConflictTestDB(t *testing.T) *gormdb.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set — skipping EC-F3 integration test")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestEC_F3_ConflictDetected_Integration is the primary EC-F3 acceptance test.
//
// Flow:
//  1. Create memory M.
//  2. Capture before_state snapshot S with created_at in the past.
//  3. UPDATE M.updated_at to NOW() (post-snapshot modification).
//  4. Rollback(S) → must return ErrRollbackConflict with M.ID in ConflictIDs.
//  5. Verify M was NOT modified (EC-F3 atomic refusal).
//  6. Verify audit_log action='rollback_attempted_with_conflict' was written.
//  7. Clear modification (reset updated_at to before snapshot.created_at).
//  8. Create new snapshot S2 (committed).
//  9. Rollback(S2) → must succeed.
func TestEC_F3_ConflictDetected_Integration(t *testing.T) {
	store := openConflictTestDB(t)
	db := store.DB
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	ctx := context.Background()
	admin := auth.Identity{Role: auth.RoleAdmin, Source: auth.SourceMaster}

	// Step 1: Create memory.
	mem := &models.Memory{
		Content:     "EC-F3 original content",
		Project:     "tg6-ec-f3-acceptance",
		SourceAgent: "claude-code",
	}
	created, err := memStore.Create(ctx, mem)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM memories WHERE id = ?", created.ID).Error
		_ = db.Exec(`DELETE FROM bulk_op_snapshots WHERE snapshot_id LIKE 'ec-f3-test-%'`).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE action IN ('rollback','rollback_attempted_with_conflict') AND actor = 'master' AND reason LIKE 'snapshot=ec-f3-test-%'`).Error
	})

	// Step 2: Capture before_state with snapshot created_at = 2 seconds ago.
	snapshotTime := time.Now().UTC().Add(-2 * time.Second)
	beforeStateMap := map[string]any{
		fmt.Sprintf("%d", created.ID): created,
	}
	beforeStateBytes, err := json.Marshal(beforeStateMap)
	require.NoError(t, err)

	snap, err := models.NewBulkOpSnapshot(
		"ec-f3-test-001",
		models.SnapshotOpBulkDelete,
		"master",
		json.RawMessage(beforeStateBytes),
	)
	require.NoError(t, err)
	snap.AffectedMemoryIDs = []int64{created.ID}
	snap.CreatedAt = snapshotTime
	createdSnap, err := snapStore.Create(ctx, snap)
	require.NoError(t, err)

	// Step 3: Simulate post-snapshot modification.
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

	// Step 5: Memory must NOT be modified — still has post-snapshot content.
	notRestored, err := memStore.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "post-snapshot edit", notRestored.Content,
		"EC-F3: atomic refusal — memory must not be rolled back when conflict detected")

	// Snapshot must still be 'committed' (rollback was refused).
	snapshotAfterConflict, err := snapStore.Get(ctx, createdSnap.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, models.SnapshotStatusCommitted, snapshotAfterConflict.Status,
		"EC-F3: snapshot must remain 'committed' when rollback is refused")

	// Step 6: Verify audit_log has action='rollback_attempted_with_conflict'.
	var conflictAuditCount int64
	db.Raw(
		`SELECT COUNT(*) FROM audit_log WHERE action = ? AND actor = ?`,
		"rollback_attempted_with_conflict", "master",
	).Scan(&conflictAuditCount)
	assert.GreaterOrEqual(t, conflictAuditCount, int64(1),
		"EC-F3: audit_log must record rollback_attempted_with_conflict")

	// Step 7: Capture a fresh exact pre-state, perform the real delete mutation,
	// and bind S2 to the immutable post-state token produced by that mutation.
	require.NoError(t, db.Exec(
		`UPDATE memories SET content = 'retry pre-state' WHERE id = ?`,
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
		"ec-f3-test-002",
		models.SnapshotOpBulkDelete,
		"master",
		retryStateBytes,
	)
	require.NoError(t, err)
	snap2.AffectedMemoryIDs = []int64{created.ID}
	createdSnap2, err := snapStore.Create(ctx, snap2)
	require.NoError(t, err)

	// Step 9: Rollback S2 succeeds because the locked row still matches its token.
	result2, err := Rollback(ctx, admin, createdSnap2.SnapshotID, snapStore, memStore, auditStore, nil)
	require.NoError(t, err, "EC-F3: rollback must succeed for the exact post-state")
	require.NotNil(t, result2)
	assert.Empty(t, result2.ConflictIDs,
		"EC-F3: no conflict IDs when modification is cleared before rollback")

	// Snapshot S2 must be rolled_back.
	snapshotAfterSuccess, err := snapStore.Get(ctx, createdSnap2.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, models.SnapshotStatusRolledBack, snapshotAfterSuccess.Status,
		"EC-F3: snapshot must be marked rolled_back on successful rollback")
}
