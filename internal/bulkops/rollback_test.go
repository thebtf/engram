// Package bulkops_test — rollback_test.go tests rollback with conflict detection.
// Engram vNext Milestone F TG6 / T042.
//
// Unit tests (no DB required):
//   - decodeBeforeState edge cases (empty, valid, invalid JSON)
//   - Non-admin caller → ErrAdminRequired without any DB calls
//
// Integration tests (skip when DATABASE_DSN absent):
//   - Happy path: rollback restores memory, audit='rollback', snapshot status='rolled_back'
//   - Conflict path (EC-F3): updated_at > snapshot.created_at → ErrRollbackConflict,
//     audit='rollback_attempted_with_conflict', memory NOT modified
package bulkops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// --- Unit: decodeBeforeState ---

func TestDecodeBeforeState_Empty(t *testing.T) {
	m, err := decodeBeforeState(json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Empty(t, m)
}

func TestDecodeBeforeState_ValidEntry(t *testing.T) {
	raw := json.RawMessage(`{"42":{"content":"original","project":"p1","id":42}}`)
	m, err := decodeBeforeState(raw)
	require.NoError(t, err)
	assert.Contains(t, m, "42")
}

func TestDecodeBeforeState_InvalidJSON(t *testing.T) {
	_, err := decodeBeforeState(json.RawMessage(`{bad json}`))
	require.Error(t, err)
}

func TestDecodeBeforeState_NilInput(t *testing.T) {
	m, err := decodeBeforeState(nil)
	require.NoError(t, err)
	assert.Empty(t, m)
}

// --- Unit: admin gate ---

func TestRollback_NonAdmin_ReturnsErrAdminRequired(t *testing.T) {
	// No DB required — admin gate fires before any store access.
	ctx := context.Background()
	_, err := Rollback(ctx, readOnlyIdentity(), "snap-001", nil, nil, nil, nil)
	require.ErrorIs(t, err, ErrAdminRequired)
}

// --- Integration tests ---

func openRollbackTestDB(t *testing.T) (*gorm.DB, *gormdb.Store) {
	t.Helper()
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
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping())
	return db, &gormdb.Store{DB: db}
}

// TestRollback_HappyPath verifies that a committed snapshot can be rolled back:
// - memory is restored to before_state
// - snapshot status becomes 'rolled_back'
// - audit log has action='rollback'
func TestRollback_HappyPath(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	ctx := context.Background()
	admin := adminIdentity()

	// Create a test memory.
	mem := &models.Memory{
		Content:     "original content before bulk delete",
		Project:     "tg6-rollback-test",
		SourceAgent: "claude-code",
	}
	created, err := memStore.Create(ctx, mem)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM memories WHERE id = ?", created.ID).Error
		_ = db.Exec(`DELETE FROM bulk_op_snapshots WHERE snapshot_id LIKE 'rollback-test-%'`).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE action IN ('rollback','rollback_attempted_with_conflict') AND actor = 'master'`).Error
	})

	// Capture before_state manually (simulating what facade.Execute would do).
	beforeStateMap := map[string]any{
		fmt.Sprintf("%d", created.ID): created,
	}
	beforeStateBytes, err := json.Marshal(beforeStateMap)
	require.NoError(t, err)

	// Create a snapshot with created_at slightly in the past.
	snap, err := models.NewBulkOpSnapshot("rollback-test-001", models.SnapshotOpBulkDelete, "master", json.RawMessage(beforeStateBytes))
	require.NoError(t, err)
	snap.AffectedMemoryIDs = []int64{created.ID}
	// Force created_at to be in the past so the memory's updated_at is before it.
	snap.CreatedAt = time.Now().UTC().Add(time.Second) // snapshot is "newer" than memory
	createdSnap, err := snapStore.Create(ctx, snap)
	require.NoError(t, err)

	// Simulate the bulk_delete op: soft-delete the memory.
	require.NoError(t, memStore.Delete(ctx, created.ID))

	// Rollback.
	result, err := Rollback(ctx, admin, createdSnap.SnapshotID, snapStore, memStore, auditStore, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, createdSnap.SnapshotID, result.SnapshotID)
	assert.Equal(t, 1, result.RestoredCount, "expected 1 memory to be restored")
	assert.Empty(t, result.ConflictIDs)

	// Snapshot must be marked rolled_back.
	updatedSnap, err := snapStore.Get(ctx, createdSnap.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, models.SnapshotStatusRolledBack, updatedSnap.Status)

	// Audit must contain 'rollback'.
	var count int64
	db.Model(&gormdb.AuditLogEntry{}).
		Where("action = ? AND actor = ?", "rollback", "master").
		Count(&count)
	assert.GreaterOrEqual(t, count, int64(1), "audit log must have rollback entry")
}

// TestRollback_Conflict_EC_F3 verifies that when a memory is modified after the snapshot,
// the rollback is refused, the memory is unchanged, and audit='rollback_attempted_with_conflict'.
func TestRollback_Conflict_EC_F3(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	ctx := context.Background()
	admin := adminIdentity()

	// Create a test memory.
	mem := &models.Memory{
		Content:     "content to preserve",
		Project:     "tg6-conflict-test",
		SourceAgent: "claude-code",
	}
	created, err := memStore.Create(ctx, mem)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM memories WHERE id = ?", created.ID).Error
		_ = db.Exec(`DELETE FROM bulk_op_snapshots WHERE snapshot_id LIKE 'rollback-conflict-%'`).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE action = 'rollback_attempted_with_conflict' AND actor = 'master'`).Error
	})

	// Build before_state capturing the original memory.
	beforeStateMap := map[string]any{
		fmt.Sprintf("%d", created.ID): created,
	}
	beforeStateBytes, _ := json.Marshal(beforeStateMap)

	// Create a snapshot with created_at in the PAST (memory's updated_at will be after this).
	snap, err := models.NewBulkOpSnapshot("rollback-conflict-001", models.SnapshotOpBulkDelete, "master", json.RawMessage(beforeStateBytes))
	require.NoError(t, err)
	snap.AffectedMemoryIDs = []int64{created.ID}
	snap.CreatedAt = time.Now().UTC().Add(-2 * time.Second) // snapshot is OLD
	createdSnap, err := snapStore.Create(ctx, snap)
	require.NoError(t, err)

	// Simulate a post-snapshot modification: update the memory's updated_at to be after snapshot.created_at.
	require.NoError(t, db.Exec(
		`UPDATE memories SET updated_at = NOW(), content = 'post-snapshot modification' WHERE id = ?`,
		created.ID,
	).Error)

	// Rollback must fail with ErrRollbackConflict (EC-F3).
	result, err := Rollback(ctx, admin, createdSnap.SnapshotID, snapStore, memStore, auditStore, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrRollbackConflict, "expected ErrRollbackConflict for modified memory")
	require.NotNil(t, result)
	assert.Contains(t, result.ConflictIDs, created.ID)

	// Memory must NOT be reverted — still has the post-snapshot content.
	afterAttempt, err := memStore.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "post-snapshot modification", afterAttempt.Content,
		"memory content must not be changed when conflict is detected")

	// Snapshot must still be 'committed' (not rolled back).
	stillCommitted, err := snapStore.Get(ctx, createdSnap.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, models.SnapshotStatusCommitted, stillCommitted.Status)

	// Audit must contain 'rollback_attempted_with_conflict'.
	var count int64
	db.Model(&gormdb.AuditLogEntry{}).
		Where("action = ? AND actor = ?", "rollback_attempted_with_conflict", "master").
		Count(&count)
	assert.GreaterOrEqual(t, count, int64(1))
}

func TestRollback_CandidateReviewPromoteDeletesMemoryAndRestoresPending(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	candidateStore := gormdb.NewCandidateStore(db, nil)
	ctx := context.Background()
	admin := adminIdentity()

	candidate, err := candidateStore.Create(ctx, &models.CrystallizationCandidate{
		SourceSessionID:         "rollback-candidate-session",
		ProposedContent:         "candidate review promote rollback must delete created memory",
		ProposedTier:            "semantic",
		ProposedEpistemicType:   "decision",
		ProposedPromotionTarget: "semantic",
		EvidenceHandles:         []string{"session:rollback-candidate-session"},
		PrivacyScope:            "project",
		Status:                  models.CandidateStatusPending,
		Fingerprint:             fmt.Sprintf("rollback-candidate-review-promote-%d", time.Now().UnixNano()),
		AffectedProjects:        []string{"tg6-candidate-rollback-test"},
		Confidence:              0.9,
		RecurrenceCount:         2,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error
		_ = db.Exec("DELETE FROM memories WHERE project = ?", "tg6-candidate-rollback-test").Error
		_ = db.Exec(`DELETE FROM bulk_op_snapshots WHERE snapshot_id LIKE 'rollback-candidate-review-%'`).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE action = 'rollback' AND actor = 'master'`).Error
	})

	candidateBefore, err := json.Marshal(candidate)
	require.NoError(t, err)
	beforeState, err := json.Marshal(map[string]models.SnapshotEntry{
		fmt.Sprintf("candidate:%d", candidate.ID): {
			Kind:   models.EntryKindRestore,
			Before: candidateBefore,
		},
	})
	require.NoError(t, err)

	snap, err := models.NewBulkOpSnapshot(
		"rollback-candidate-review-promote-001",
		models.SnapshotOpCandidateReviewAction,
		"master",
		json.RawMessage(beforeState),
	)
	require.NoError(t, err)
	snap.SourceSessionID = candidate.SourceSessionID
	snap.CreatedAt = time.Now().UTC().Add(-time.Second)
	createdSnap, err := snapStore.Create(ctx, snap)
	require.NoError(t, err)

	updatedCandidate, createdMemory, err := candidateStore.PromoteWithMemory(ctx, candidate.ID, &models.Memory{
		Content:       candidate.ProposedContent,
		Project:       "tg6-candidate-rollback-test",
		Tier:          "semantic",
		EpistemicType: "decision",
		Tags:          []string{fmt.Sprintf("candidate:%d", candidate.ID), "crystallized"},
		SourceAgent:   "crystallization",
	})
	require.NoError(t, err)
	require.Equal(t, models.CandidateStatusPromoted, updatedCandidate.Status)
	require.NotNil(t, updatedCandidate.PromotedMemoryID)
	require.Equal(t, createdMemory.ID, *updatedCandidate.PromotedMemoryID)
	require.NoError(t, snapStore.AmendPromoteEntries(ctx, createdSnap.SnapshotID, []int64{createdMemory.ID}))
	assert.True(t, createdMemory.CreatedAt.After(createdSnap.CreatedAt), "fixture must create promoted memory after snapshot")
	assert.True(t, createdMemory.UpdatedAt.After(createdSnap.CreatedAt), "fixture must create promoted memory updated timestamp after snapshot")
	assert.False(t, createdMemory.UpdatedAt.After(createdMemory.CreatedAt), "fixture must represent an unmodified promoted memory")

	result, err := Rollback(ctx, admin, createdSnap.SnapshotID, snapStore, memStore, auditStore, candidateStore)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.RestoredCount)

	restoredCandidate, err := candidateStore.Get(ctx, candidate.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CandidateStatusPending, restoredCandidate.Status)
	assert.Nil(t, restoredCandidate.PromotedMemoryID)

	var memoryCount int64
	require.NoError(t, db.Unscoped().Model(&gormdb.Memory{}).Where("id = ?", createdMemory.ID).Count(&memoryCount).Error)
	assert.Equal(t, int64(0), memoryCount, "rollback must hard-delete the memory created by promote")
}

func TestRollback_CandidateReviewPromoteEditedMemoryConflicts(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	candidateStore := gormdb.NewCandidateStore(db, nil)
	ctx := context.Background()
	admin := adminIdentity()

	candidate, err := candidateStore.Create(ctx, &models.CrystallizationCandidate{
		SourceSessionID:         "rollback-candidate-edited-session",
		ProposedContent:         "candidate review promote rollback must preserve edited memory",
		ProposedTier:            "semantic",
		ProposedEpistemicType:   "decision",
		ProposedPromotionTarget: "semantic",
		EvidenceHandles:         []string{"session:rollback-candidate-edited-session"},
		PrivacyScope:            "project",
		Status:                  models.CandidateStatusPending,
		Fingerprint:             fmt.Sprintf("rollback-candidate-review-edited-%d", time.Now().UnixNano()),
		AffectedProjects:        []string{"tg6-candidate-rollback-edited-test"},
		Confidence:              0.9,
		RecurrenceCount:         2,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error
		_ = db.Exec("DELETE FROM memories WHERE project = ?", "tg6-candidate-rollback-edited-test").Error
		_ = db.Exec(`DELETE FROM bulk_op_snapshots WHERE snapshot_id LIKE 'rollback-candidate-review-edited-%'`).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE action IN ('rollback','rollback_attempted_with_conflict') AND actor = 'master'`).Error
	})

	candidateBefore, err := json.Marshal(candidate)
	require.NoError(t, err)
	beforeState, err := json.Marshal(map[string]models.SnapshotEntry{
		fmt.Sprintf("candidate:%d", candidate.ID): {
			Kind:   models.EntryKindRestore,
			Before: candidateBefore,
		},
	})
	require.NoError(t, err)

	snap, err := models.NewBulkOpSnapshot(
		"rollback-candidate-review-edited-001",
		models.SnapshotOpCandidateReviewAction,
		"master",
		json.RawMessage(beforeState),
	)
	require.NoError(t, err)
	snap.SourceSessionID = candidate.SourceSessionID
	snap.CreatedAt = time.Now().UTC().Add(time.Second)
	createdSnap, err := snapStore.Create(ctx, snap)
	require.NoError(t, err)

	updatedCandidate, createdMemory, err := candidateStore.PromoteWithMemory(ctx, candidate.ID, &models.Memory{
		Content:       candidate.ProposedContent,
		Project:       "tg6-candidate-rollback-edited-test",
		Tier:          "semantic",
		EpistemicType: "decision",
		Tags:          []string{fmt.Sprintf("candidate:%d", candidate.ID), "crystallized"},
		SourceAgent:   "crystallization",
	})
	require.NoError(t, err)
	require.Equal(t, models.CandidateStatusPromoted, updatedCandidate.Status)
	require.NotNil(t, updatedCandidate.PromotedMemoryID)
	require.Equal(t, createdMemory.ID, *updatedCandidate.PromotedMemoryID)
	require.NoError(t, snapStore.AmendPromoteEntries(ctx, createdSnap.SnapshotID, []int64{createdMemory.ID}))

	editedAt := createdMemory.CreatedAt.Add(2 * time.Second)
	require.NoError(t, db.Exec(
		`UPDATE memories SET updated_at = ?, content = 'edited promoted memory' WHERE id = ?`,
		editedAt,
		createdMemory.ID,
	).Error)

	result, err := Rollback(ctx, admin, createdSnap.SnapshotID, snapStore, memStore, auditStore, candidateStore)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrRollbackConflict)
	require.NotNil(t, result)
	assert.Contains(t, result.ConflictIDs, createdMemory.ID)

	afterAttempt, err := memStore.Get(ctx, createdMemory.ID)
	require.NoError(t, err)
	assert.Equal(t, "edited promoted memory", afterAttempt.Content)

	stillPromoted, err := candidateStore.Get(ctx, candidate.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CandidateStatusPromoted, stillPromoted.Status)
	require.NotNil(t, stillPromoted.PromotedMemoryID)
	assert.Equal(t, createdMemory.ID, *stillPromoted.PromotedMemoryID)

	stillCommitted, err := snapStore.Get(ctx, createdSnap.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, models.SnapshotStatusCommitted, stillCommitted.Status)
}
