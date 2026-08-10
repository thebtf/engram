// Package bulkops_test — rollback_test.go tests rollback with conflict detection.
// Engram vNext Milestone F TG6 / T042.
//
// Unit tests (no DB required):
//   - decodeBeforeState edge cases (empty, valid, invalid JSON)
//   - Non-admin caller → ErrAdminRequired without any DB calls
//
// Integration tests (skip when DATABASE_DSN absent):
//   - Happy path: rollback restores memory, audit='rollback', snapshot status='rolled_back'
//   - Conflict path: a durable post-snapshot row mutation returns ErrRollbackConflict,
//     audit='rollback_attempted_with_conflict', memory NOT modified
package bulkops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
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
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: logger.Warn})
	require.NoError(t, err)
	db := store.DB
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db, store
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
		Content:             "original content before bulk delete",
		Project:             "tg6-rollback-test",
		SourceAgent:         "claude-code",
		SourceWorkstationID: "rollback-workstation",
		OwnerPrincipal:      "agent/rollback-owner",
		OwnerPrincipalKind:  "agent",
		AgentVisibility:     models.AgentVisibilityShared,
		Domain:              "rollback-domain",
		SourceSessions:      pq.StringArray{"rollback-session-a", "rollback-session-b"},
	}
	created, err := memStore.Create(ctx, mem)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM memories WHERE id = ?", created.ID).Error
		_ = db.Exec(`DELETE FROM bulk_op_snapshots WHERE snapshot_id LIKE 'rollback-test-%'`).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE action IN ('rollback','rollback_attempted_with_conflict') AND actor = 'master'`).Error
	})

	// Capture the typed before_state used by new bulk delete/supersede snapshots.
	before, err := memStore.GetForSnapshot(ctx, created.ID)
	require.NoError(t, err)
	beforeBytes, err := json.Marshal(before)
	require.NoError(t, err)
	expectedVersion := before.Version + 1
	beforeStateMap := map[string]models.SnapshotEntry{
		fmt.Sprintf("%d", created.ID): {
			Kind:            models.EntryKindRestore,
			Before:          beforeBytes,
			ExpectedVersion: &expectedVersion,
		},
	}
	beforeStateBytes, err := json.Marshal(beforeStateMap)
	require.NoError(t, err)

	snap, err := models.NewBulkOpSnapshot("rollback-test-001", models.SnapshotOpBulkDelete, "master", json.RawMessage(beforeStateBytes))
	require.NoError(t, err)
	snap.AffectedMemoryIDs = []int64{created.ID}
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

	restored, err := memStore.GetForSnapshot(ctx, created.ID)
	require.NoError(t, err)
	assert.Nil(t, restored.DeletedAt, "rollback must clear the soft-delete marker")
	assert.Equal(t, before.PrivacyScope, restored.PrivacyScope)
	assert.Equal(t, before.SourceWorkstationID, restored.SourceWorkstationID)
	assert.Equal(t, before.SourceSessions, restored.SourceSessions)
	assert.Equal(t, before.OwnerPrincipal, restored.OwnerPrincipal)
	assert.Equal(t, before.OwnerPrincipalKind, restored.OwnerPrincipalKind)
	assert.Equal(t, before.AgentVisibility, restored.AgentVisibility)
	assert.Equal(t, before.Domain, restored.Domain)
	assert.Equal(t, before.CreatedAt, restored.CreatedAt)
	assert.Equal(t, before.CitationCount, restored.CitationCount)
	assert.Equal(t, before.AccessCount, restored.AccessCount)

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

func TestLegacyMemoryProofRejectsEqualVersionMutation(t *testing.T) {
	before := &models.Memory{ID: 42, Content: "before", Project: "project", SourceAgent: "test", PrivacyScope: "project", Version: 3}
	beforeJSON, err := json.Marshal(before)
	require.NoError(t, err)
	now := time.Now().UTC()
	current := *before
	current.Version = before.Version + 1
	current.UpdatedAt = now
	current.DeletedAt = &now
	current.PrivacyScope = "global" // Same version, but not the deterministic delete state.
	assert.False(t, legacyMemoryPostStateMatches(models.SnapshotOpBulkDelete, models.SnapshotEntry{Kind: models.EntryKindRestore, Before: beforeJSON, ExpectedVersion: &current.Version}, &current))
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

	// Persist a past boundary; SnapshotStore assigns created_at during insertion.
	snap, err := models.NewBulkOpSnapshot("rollback-conflict-001", models.SnapshotOpBulkDelete, "master", json.RawMessage(beforeStateBytes))
	require.NoError(t, err)
	snap.AffectedMemoryIDs = []int64{created.ID}
	createdSnap, err := snapStore.Create(ctx, snap)
	require.NoError(t, err)
	snapshotTime := time.Now().UTC().Add(-2 * time.Second)
	require.NoError(t, db.Exec("UPDATE bulk_op_snapshots SET created_at = ? WHERE snapshot_id = ?", snapshotTime, createdSnap.SnapshotID).Error)
	entries, err := decodeTypedBeforeState(createdSnap.BeforeState)
	require.NoError(t, err)
	assert.Empty(t, entries[fmt.Sprintf("%d", created.ID)].PostStateToken, "legacy snapshots have no captured post-state token")

	// Simulate a post-snapshot durable mutation.
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

func TestRollback_LegacyBulkDeleteRollsBackWithoutClockComparison(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	ctx := context.Background()

	before, err := memStore.Create(ctx, &models.Memory{Content: "legacy delete source", Project: "legacy-delete-conflict", SourceAgent: "test"})
	require.NoError(t, err)
	snapshotID := fmt.Sprintf("rollback-legacy-delete-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", before.ID).Error
		_ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", snapshotID).Error
	})

	beforeState, err := json.Marshal(map[string]*models.Memory{fmt.Sprintf("%d", before.ID): before})
	require.NoError(t, err)
	snap, err := models.NewBulkOpSnapshot(snapshotID, models.SnapshotOpBulkDelete, "master", beforeState)
	require.NoError(t, err)
	snap.AffectedMemoryIDs = []int64{before.ID}
	created, err := snapStore.Create(ctx, snap)
	require.NoError(t, err)

	require.NoError(t, memStore.Delete(ctx, before.ID))
	// A database clock earlier than the application-side snapshot timestamp must
	// not reject an otherwise exact legacy bulk-delete post-state.
	require.NoError(t, db.Exec("UPDATE memories SET updated_at = ? WHERE id = ?", created.CreatedAt.Add(-time.Hour), before.ID).Error)
	result, err := Rollback(ctx, adminIdentity(), created.SnapshotID, snapStore, memStore, gormdb.NewAuditStore(db), nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.RestoredCount)
	restored, err := memStore.Get(ctx, before.ID)
	require.NoError(t, err)
	assert.Equal(t, before.Content, restored.Content)
	assert.Nil(t, restored.DeletedAt)
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

func TestRollback_CandidateReviewRejectAndSupersedeSnapshots(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	snapshots := gormdb.NewSnapshotStore(db)
	audits := gormdb.NewAuditStore(db)
	candidates := gormdb.NewCandidateStore(db, audits)
	ctx := context.Background()

	for _, test := range []struct {
		name      string
		supersede bool
		postEdit  bool
	}{
		{name: "reject_restores_pending"},
		{name: "supersede_post_transition_edit_conflicts", supersede: true, postEdit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate, err := models.NewCrystallizationCandidate(
				fmt.Sprintf("rollback-%s-%d", test.name, time.Now().UnixNano()),
				"candidate rollback state-token coverage",
				"rule",
				models.CandidateOptions{AffectedProjects: []string{"rollback-candidate-transition"}},
			)
			require.NoError(t, err)
			candidate.PrivacyScope = "project"
			candidate, err = candidates.Create(ctx, candidate)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error)
			})

			before, err := json.Marshal(candidate)
			require.NoError(t, err)
			beforeState, err := json.Marshal(map[string]models.SnapshotEntry{
				fmt.Sprintf("candidate:%d", candidate.ID): {Kind: models.EntryKindRestore, Before: before},
			})
			require.NoError(t, err)
			snapshot, err := models.NewBulkOpSnapshot(
				fmt.Sprintf("rollback-candidate-transition-%s-%d", test.name, time.Now().UnixNano()),
				models.SnapshotOpCandidateReviewAction,
				"master",
				beforeState,
			)
			require.NoError(t, err)

			if test.supersede {
				_, snapshot, err = candidates.TransitionToSupersededWithSnapshot(ctx, snapshots, candidate.ID, snapshot, "master")
			} else {
				_, snapshot, err = candidates.TransitionToRejectedWithSnapshot(ctx, snapshots, candidate.ID, "not durable", snapshot, "master")
			}
			require.NoError(t, err)
			if test.postEdit {
				require.NoError(t, db.Exec("UPDATE crystallization_candidates SET proposed_content = ?, updated_at = NOW() WHERE id = ?", "edited after supersede", candidate.ID).Error)
			}

			result, err := Rollback(ctx, adminIdentity(), snapshot.SnapshotID, snapshots, memories, audits, candidates)
			if test.postEdit {
				require.ErrorIs(t, err, ErrRollbackConflict)
				require.NotNil(t, result)
				require.Contains(t, result.ConflictIDs, candidate.ID)
				stillSuperseded, getErr := candidates.Get(ctx, candidate.ID)
				require.NoError(t, getErr)
				require.Equal(t, models.CandidateStatusSuperseded, stillSuperseded.Status)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, result.RestoredCount)
			restored, getErr := candidates.Get(ctx, candidate.ID)
			require.NoError(t, getErr)
			require.Equal(t, models.CandidateStatusPending, restored.Status)
		})
	}
}

func TestRollback_LegacyTokenlessCandidateReviewEditConflicts(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	snapshots := gormdb.NewSnapshotStore(db)
	audits := gormdb.NewAuditStore(db)
	candidates := gormdb.NewCandidateStore(db, audits)
	ctx := context.Background()
	candidate, err := models.NewCrystallizationCandidate(fmt.Sprintf("legacy-review-reject-%d", time.Now().UnixNano()), "legacy tokenless candidate review rollback", "rule", models.CandidateOptions{AffectedProjects: []string{"legacy-review-reject"}})
	require.NoError(t, err)
	candidate.PrivacyScope = "project"
	candidate, err = candidates.Create(ctx, candidate)
	require.NoError(t, err)
	before, err := json.Marshal(candidate)
	require.NoError(t, err)
	beforeState, err := json.Marshal(map[string]models.SnapshotEntry{fmt.Sprintf("candidate:%d", candidate.ID): {Kind: models.EntryKindRestore, Before: before}})
	require.NoError(t, err)
	snapshot, err := models.NewBulkOpSnapshot(fmt.Sprintf("legacy-review-reject-%d", time.Now().UnixNano()), models.SnapshotOpCandidateReviewAction, "master", beforeState)
	require.NoError(t, err)
	snapshot.Parameters, err = json.Marshal(map[string]any{"action": "reject", "candidate_id": candidate.ID})
	require.NoError(t, err)
	snapshot, err = snapshots.Create(ctx, snapshot)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error
		_ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", snapshot.SnapshotID).Error
	})
	_, err = candidates.TransitionToRejected(ctx, candidate.ID, "legacy rejection")
	require.NoError(t, err)
	require.NoError(t, db.Exec("UPDATE crystallization_candidates SET proposed_content = ? WHERE id = ?", "edited after legacy transition", candidate.ID).Error)
	result, err := Rollback(ctx, adminIdentity(), snapshot.SnapshotID, snapshots, memories, audits, candidates)
	require.ErrorIs(t, err, ErrRollbackConflict)
	require.Equal(t, []int64{candidate.ID}, result.ConflictIDs)
	require.Equal(t, []RollbackConflictRef{{Entity: snapshotEntryEntityCandidate, ID: candidate.ID}}, result.ConflictRefs)
}

func TestRollback_ConflictDetectedAfterStalePreTransactionRead(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	auditStore := gormdb.NewAuditStore(db)
	ctx := context.Background()

	mem, err := memStore.Create(ctx, &models.Memory{
		Content:     "before concurrent edit",
		Project:     "tg6-rollback-stale-read",
		SourceAgent: "claude-code",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM memories WHERE id = ?", mem.ID).Error
		_ = db.Exec(`DELETE FROM bulk_op_snapshots WHERE snapshot_id = 'rollback-stale-read-001'`).Error
	})
	require.NoError(t, db.Exec(`UPDATE memories SET updated_at = NOW() - INTERVAL '2 seconds' WHERE id = ?`, mem.ID).Error)
	beforeState, err := json.Marshal(map[string]any{fmt.Sprintf("%d", mem.ID): &models.Memory{
		ID:          mem.ID,
		CreatedAt:   mem.CreatedAt,
		UpdatedAt:   mem.UpdatedAt,
		Content:     "before concurrent edit",
		Project:     mem.Project,
		SourceAgent: mem.SourceAgent,
		Status:      "active",
		Tags:        []string{},
	}})
	require.NoError(t, err)
	snap, err := models.NewBulkOpSnapshot("rollback-stale-read-001", models.SnapshotOpBulkDelete, "master", beforeState)
	require.NoError(t, err)
	snap.AffectedMemoryIDs = []int64{mem.ID}
	snap.CreatedAt = time.Now().UTC().Add(-time.Second)
	createdSnap, err := snapStore.Create(ctx, snap)
	require.NoError(t, err)

	writer := db.Begin()
	require.NoError(t, writer.Error)
	require.NoError(t, writer.Exec(`UPDATE memories SET content = 'concurrent edit', updated_at = ? WHERE id = ?`, createdSnap.CreatedAt.Add(time.Second), mem.ID).Error)

	type rollbackOutcome struct {
		result *RollbackResult
		err    error
	}
	done := make(chan rollbackOutcome, 1)
	go func() {
		result, rollbackErr := Rollback(ctx, adminIdentity(), createdSnap.SnapshotID, snapStore, memStore, auditStore, nil)
		done <- rollbackOutcome{result: result, err: rollbackErr}
	}()

	select {
	case outcome := <-done:
		t.Fatalf("rollback completed before concurrent writer committed: result=%+v err=%v", outcome.result, outcome.err)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, writer.Commit().Error)

	select {
	case outcome := <-done:
		require.ErrorIs(t, outcome.err, ErrRollbackConflict)
		require.NotNil(t, outcome.result)
		assert.Contains(t, outcome.result.ConflictIDs, mem.ID)
	case <-time.After(5 * time.Second):
		t.Fatal("rollback did not finish after concurrent writer committed")
	}

	after, err := memStore.Get(ctx, mem.ID)
	require.NoError(t, err)
	assert.Equal(t, "concurrent edit", after.Content)
}

func TestRollback_LegacyTokenlessPromotionFailsClosedWithoutMemoryProof(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	snapshots := gormdb.NewSnapshotStore(db)
	candidates := gormdb.NewCandidateStore(db, nil)
	ctx := context.Background()
	project := fmt.Sprintf("legacy-tokenless-promote-%d", time.Now().UnixNano())
	candidate, err := candidates.Create(ctx, &models.CrystallizationCandidate{SourceSessionID: "legacy-tokenless-promote", ProposedContent: "legacy tokenless candidate", ProposedTier: "semantic", ProposedPromotionTarget: "semantic", PrivacyScope: "project", Status: models.CandidateStatusPending, Fingerprint: project, AffectedProjects: []string{project}, Confidence: 0.9, RecurrenceCount: 1})
	require.NoError(t, err)
	memory, err := memories.Create(ctx, &models.Memory{Content: "legacy promoted memory", Project: project, SourceAgent: "test", Status: "active", Tags: models.JSONStringArray{}})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error
		_ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", memory.ID).Error
		_ = db.Exec("DELETE FROM bulk_op_snapshots WHERE source_session_id = ?", "legacy-tokenless-promote").Error
	})
	before, err := json.Marshal(candidate)
	require.NoError(t, err)
	require.NoError(t, db.Exec("UPDATE crystallization_candidates SET status = ?, promoted_memory_id = ? WHERE id = ?", models.CandidateStatusPromoted, memory.ID, candidate.ID).Error)
	beforeState, err := json.Marshal(map[string]models.SnapshotEntry{
		fmt.Sprintf("candidate:%d", candidate.ID): {Kind: models.EntryKindRestore, Before: before},
		fmt.Sprintf("memory:%d", memory.ID):       {Kind: models.EntryKindDelete},
	})
	require.NoError(t, err)
	snapshot, err := models.NewBulkOpSnapshot(fmt.Sprintf("legacy-tokenless-promote-%d", time.Now().UnixNano()), models.SnapshotOpBulkPromote, "master", beforeState)
	require.NoError(t, err)
	snapshot.SourceSessionID = "legacy-tokenless-promote"
	snapshot.Parameters = json.RawMessage(fmt.Sprintf(`{"candidate_ids":[%d]}`, candidate.ID))
	snapshot.AffectedMemoryIDs = []int64{memory.ID}
	created, err := snapshots.Create(ctx, snapshot)
	require.NoError(t, err)
	result, err := Rollback(ctx, adminIdentity(), created.SnapshotID, snapshots, memories, gormdb.NewAuditStore(db), candidates)
	require.ErrorIs(t, err, ErrRollbackConflict)
	require.Equal(t, []int64{memory.ID}, result.ConflictIDs)
	stillPromoted, err := candidates.Get(ctx, candidate.ID)
	require.NoError(t, err)
	require.Equal(t, models.CandidateStatusPromoted, stillPromoted.Status)
	var memoryCount int64
	require.NoError(t, db.Unscoped().Model(&gormdb.Memory{}).Where("id = ?", memory.ID).Count(&memoryCount).Error)
	require.Equal(t, int64(1), memoryCount)
}

func TestRollback_MissingPromotedMemoryIsAnIdempotentDelete(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	snapshots := gormdb.NewSnapshotStore(db)
	candidates := gormdb.NewCandidateStore(db, nil)
	ctx := context.Background()
	project := fmt.Sprintf("missing-promoted-memory-%d", time.Now().UnixNano())
	candidate, err := candidates.Create(ctx, &models.CrystallizationCandidate{
		SourceSessionID:         "missing-promoted-memory",
		ProposedContent:         "candidate with removed promoted memory",
		ProposedTier:            "semantic",
		ProposedPromotionTarget: "semantic",
		PrivacyScope:            "project",
		Status:                  models.CandidateStatusPending,
		Fingerprint:             project,
		AffectedProjects:        []string{project},
		Confidence:              0.9,
		RecurrenceCount:         1,
	})
	require.NoError(t, err)
	memory, err := memories.Create(ctx, &models.Memory{Content: "already removed promoted memory", Project: project, SourceAgent: "test", Status: "active", Tags: models.JSONStringArray{}})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error
		_ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", memory.ID).Error
		_ = db.Exec("DELETE FROM bulk_op_snapshots WHERE source_session_id = ?", "missing-promoted-memory").Error
	})

	before, err := json.Marshal(candidate)
	require.NoError(t, err)
	require.NoError(t, db.Exec("UPDATE crystallization_candidates SET status = ?, promoted_memory_id = ? WHERE id = ?", models.CandidateStatusPromoted, memory.ID, candidate.ID).Error)
	promotedCandidate, err := candidates.Get(ctx, candidate.ID)
	require.NoError(t, err)
	candidateToken, err := models.SnapshotStateToken(promotedCandidate)
	require.NoError(t, err)
	promotedMemory, err := memories.GetForSnapshot(ctx, memory.ID)
	require.NoError(t, err)
	memoryToken, err := models.SnapshotStateToken(promotedMemory)
	require.NoError(t, err)
	beforeState, err := json.Marshal(map[string]models.SnapshotEntry{
		fmt.Sprintf("candidate:%d", candidate.ID): {Kind: models.EntryKindRestore, Before: before, PostStateToken: candidateToken},
		fmt.Sprintf("memory:%d", memory.ID):       {Kind: models.EntryKindDelete, PostStateToken: memoryToken},
	})
	require.NoError(t, err)
	snapshot, err := models.NewBulkOpSnapshot(fmt.Sprintf("missing-promoted-memory-%d", time.Now().UnixNano()), models.SnapshotOpBulkPromote, "master", beforeState)
	require.NoError(t, err)
	snapshot.SourceSessionID = "missing-promoted-memory"
	snapshot.Parameters = json.RawMessage(fmt.Sprintf(`{"candidate_ids":[%d]}`, candidate.ID))
	snapshot.AffectedMemoryIDs = []int64{memory.ID}
	created, err := snapshots.Create(ctx, snapshot)
	require.NoError(t, err)
	require.NoError(t, db.Exec("DELETE FROM memories WHERE id = ?", memory.ID).Error)
	orphaned, err := candidates.Get(ctx, candidate.ID)
	require.NoError(t, err)
	require.Nil(t, orphaned.PromotedMemoryID)

	result, err := Rollback(ctx, adminIdentity(), created.SnapshotID, snapshots, memories, gormdb.NewAuditStore(db), candidates)
	require.NoError(t, err)
	require.Equal(t, 1, result.RestoredCount)
	restored, err := candidates.Get(ctx, candidate.ID)
	require.NoError(t, err)
	require.Equal(t, models.CandidateStatusPending, restored.Status)
	require.Nil(t, restored.PromotedMemoryID)
}

func TestRollback_LegacyUnprefixedPromotionCollisionFailsClosed(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memStore := gormdb.NewMemoryStore(store)
	snapStore := gormdb.NewSnapshotStore(db)
	candidateStore := gormdb.NewCandidateStore(db, nil)
	ctx := context.Background()

	candidate, err := candidateStore.Create(ctx, &models.CrystallizationCandidate{
		SourceSessionID:         "legacy-collision-session",
		ProposedContent:         "candidate must remain untouched",
		ProposedTier:            "semantic",
		ProposedPromotionTarget: "semantic",
		PrivacyScope:            "project",
		Status:                  models.CandidateStatusPending,
		Fingerprint:             fmt.Sprintf("legacy-collision-%d", time.Now().UnixNano()),
		AffectedProjects:        []string{"legacy-collision-project"},
		Confidence:              0.9,
		RecurrenceCount:         1,
	})
	require.NoError(t, err)
	row := &gormdb.Memory{
		ID:          candidate.ID,
		Content:     "memory must remain untouched",
		Project:     "legacy-collision-project",
		SourceAgent: "rollback-test",
		Status:      "active",
		Tags:        models.JSONStringArray{},
	}
	require.NoError(t, db.Create(row).Error)
	memory, err := memStore.Get(ctx, candidate.ID)
	require.NoError(t, err)
	require.Equal(t, candidate.ID, memory.ID, "fixture requires equal candidate and memory IDs")

	snapshotID := fmt.Sprintf("rollback-legacy-collision-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM bulk_op_snapshots WHERE snapshot_id = ?", snapshotID).Error
		_ = db.Exec("DELETE FROM memories WHERE id = ?", memory.ID).Error
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error
	})
	before, err := json.Marshal(candidate)
	require.NoError(t, err)
	snap, err := models.NewBulkOpSnapshot(snapshotID, models.SnapshotOpBulkPromote, "master", json.RawMessage(fmt.Sprintf(`{"%d":%s}`, candidate.ID, before)))
	require.NoError(t, err)
	created, err := snapStore.Create(ctx, snap)
	require.NoError(t, err)

	result, err := Rollback(ctx, adminIdentity(), created.SnapshotID, snapStore, memStore, gormdb.NewAuditStore(db), candidateStore)
	require.ErrorIs(t, err, ErrLegacySnapshotAmbiguous)
	assert.Nil(t, result)

	candidateAfter, err := candidateStore.Get(ctx, candidate.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CandidateStatusPending, candidateAfter.Status)
	assert.Equal(t, "candidate must remain untouched", candidateAfter.ProposedContent)
	memoryAfter, err := memStore.Get(ctx, memory.ID)
	require.NoError(t, err)
	assert.Equal(t, "memory must remain untouched", memoryAfter.Content)
	stillCommitted, err := snapStore.Get(ctx, created.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, models.SnapshotStatusCommitted, stillCommitted.Status)
}

func TestRollback_LegacyPromotionParametersIgnoreUnrelatedEqualMemoryID(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	snapshots := gormdb.NewSnapshotStore(db)
	candidates := gormdb.NewCandidateStore(db, nil)
	ctx := context.Background()
	candidate, err := candidates.Create(ctx, &models.CrystallizationCandidate{SourceSessionID: "legacy-parameter", ProposedContent: "parameter-owned candidate", ProposedTier: "semantic", ProposedPromotionTarget: "semantic", PrivacyScope: "project", Status: models.CandidateStatusPending, Fingerprint: fmt.Sprintf("legacy-parameter-%d", time.Now().UnixNano()), AffectedProjects: []string{"legacy-parameter"}, Confidence: 0.9, RecurrenceCount: 1})
	require.NoError(t, err)
	promotedID := candidate.ID + 1000
	require.NoError(t, db.Create(&gormdb.Memory{ID: candidate.ID, Content: "unrelated equal id", Project: "legacy-parameter", SourceAgent: "test", Status: "active", Tags: models.JSONStringArray{}}).Error)
	require.NoError(t, db.Create(&gormdb.Memory{ID: promotedID, Content: "promoted memory", Project: "legacy-parameter", SourceAgent: "test", Status: "active", Tags: models.JSONStringArray{}}).Error)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error
		_ = db.Unscoped().Delete(&gormdb.Memory{}, "id IN ?", []int64{candidate.ID, promotedID}).Error
	})

	before, err := json.Marshal(candidate)
	require.NoError(t, err)
	promoted, err := memories.GetForSnapshot(ctx, promotedID)
	require.NoError(t, err)
	promotedToken, err := models.SnapshotStateToken(promoted)
	require.NoError(t, err)
	beforeState, err := json.Marshal(map[string]models.SnapshotEntry{
		fmt.Sprintf("%d", candidate.ID):      {Kind: models.EntryKindRestore, Before: before},
		fmt.Sprintf("memory:%d", promotedID): {Kind: models.EntryKindDelete, PostStateToken: promotedToken},
	})
	require.NoError(t, err)
	parameters, err := json.Marshal(map[string]any{"candidate_id": candidate.ID})
	require.NoError(t, err)
	snapshotID := fmt.Sprintf("legacy-parameter-%d", time.Now().UnixNano())
	snap, err := models.NewBulkOpSnapshot(snapshotID, models.SnapshotOpBulkPromote, "master", beforeState)
	require.NoError(t, err)
	snap.Parameters = parameters
	snap.AffectedMemoryIDs = []int64{promotedID}
	created, err := snapshots.Create(ctx, snap)
	require.NoError(t, err)
	require.NoError(t, db.Exec("UPDATE crystallization_candidates SET status = ?, promoted_memory_id = ? WHERE id = ?", models.CandidateStatusPromoted, promotedID, candidate.ID).Error)

	result, err := Rollback(ctx, adminIdentity(), created.SnapshotID, snapshots, memories, gormdb.NewAuditStore(db), candidates)
	require.NoError(t, err)
	assert.Equal(t, 1, result.RestoredCount)
	unrelated, err := memories.Get(ctx, candidate.ID)
	require.NoError(t, err)
	assert.Equal(t, "unrelated equal id", unrelated.Content)
	var promotedCount int64
	require.NoError(t, db.Unscoped().Model(&gormdb.Memory{}).Where("id = ?", promotedID).Count(&promotedCount).Error)
	assert.Zero(t, promotedCount)
}

func TestRollback_PrefixedPromotionSameIDReturnsTypedConflicts(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	snapshots := gormdb.NewSnapshotStore(db)
	candidates := gormdb.NewCandidateStore(db, nil)
	ctx := context.Background()
	candidate, err := candidates.Create(ctx, &models.CrystallizationCandidate{SourceSessionID: "prefixed-same-id", ProposedContent: "prefixed candidate", ProposedTier: "semantic", ProposedPromotionTarget: "semantic", PrivacyScope: "project", Status: models.CandidateStatusPending, Fingerprint: fmt.Sprintf("prefixed-same-id-%d", time.Now().UnixNano()), AffectedProjects: []string{"prefixed-same-id"}, Confidence: 0.9, RecurrenceCount: 1})
	require.NoError(t, err)
	require.NoError(t, db.Create(&gormdb.Memory{ID: candidate.ID, Content: "prefixed promoted memory", Project: "prefixed-same-id", SourceAgent: "test", Status: "active", Tags: models.JSONStringArray{}}).Error)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error
		_ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", candidate.ID).Error
	})
	before, err := json.Marshal(candidate)
	require.NoError(t, err)
	require.NoError(t, db.Exec("UPDATE crystallization_candidates SET status = ?, promoted_memory_id = ? WHERE id = ?", models.CandidateStatusPromoted, candidate.ID, candidate.ID).Error)
	afterCandidate, err := candidates.Get(ctx, candidate.ID)
	require.NoError(t, err)
	candidateToken, err := models.SnapshotStateToken(afterCandidate)
	require.NoError(t, err)
	promoted, err := memories.GetForSnapshot(ctx, candidate.ID)
	require.NoError(t, err)
	promotedToken, err := models.SnapshotStateToken(promoted)
	require.NoError(t, err)
	beforeState, err := json.Marshal(map[string]models.SnapshotEntry{
		fmt.Sprintf("candidate:%d", candidate.ID): {Kind: models.EntryKindRestore, Before: before, PostStateToken: candidateToken},
		fmt.Sprintf("memory:%d", candidate.ID):    {Kind: models.EntryKindDelete, PostStateToken: promotedToken},
	})
	require.NoError(t, err)
	snap, err := models.NewBulkOpSnapshot(fmt.Sprintf("prefixed-same-id-%d", time.Now().UnixNano()), models.SnapshotOpBulkPromote, "master", beforeState)
	require.NoError(t, err)
	snap.Parameters = json.RawMessage(fmt.Sprintf(`{"candidate_ids":[%d]}`, candidate.ID))
	snap.AffectedMemoryIDs = []int64{candidate.ID}
	created, err := snapshots.Create(ctx, snap)
	require.NoError(t, err)

	require.NoError(t, db.Exec("UPDATE crystallization_candidates SET proposed_content = ? WHERE id = ?", "edited candidate", candidate.ID).Error)
	require.NoError(t, db.Exec("UPDATE memories SET content = ? WHERE id = ?", "edited memory", candidate.ID).Error)
	result, err := Rollback(ctx, adminIdentity(), created.SnapshotID, snapshots, memories, gormdb.NewAuditStore(db), candidates)
	require.ErrorIs(t, err, ErrRollbackConflict)
	require.Equal(t, []int64{candidate.ID, candidate.ID}, result.ConflictIDs)
	require.Equal(t, []RollbackConflictRef{{Entity: snapshotEntryEntityCandidate, ID: candidate.ID}, {Entity: snapshotEntryEntityMemory, ID: candidate.ID}}, result.ConflictRefs)
}

func TestRollback_LegacyPromotionParametersSameIDCollisionFailsClosed(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	snapshots := gormdb.NewSnapshotStore(db)
	candidates := gormdb.NewCandidateStore(db, nil)
	ctx := context.Background()
	candidate, err := candidates.Create(ctx, &models.CrystallizationCandidate{SourceSessionID: "legacy-parameter-collision", ProposedContent: "candidate remains promoted", ProposedTier: "semantic", ProposedPromotionTarget: "semantic", PrivacyScope: "project", Status: models.CandidateStatusPending, Fingerprint: fmt.Sprintf("legacy-parameter-collision-%d", time.Now().UnixNano()), AffectedProjects: []string{"legacy-parameter-collision"}, Confidence: 0.9, RecurrenceCount: 1})
	require.NoError(t, err)
	row := &gormdb.Memory{ID: candidate.ID, Content: "promoted memory remains", Project: "legacy-parameter-collision", SourceAgent: "test", Status: "active", Tags: models.JSONStringArray{}}
	require.NoError(t, db.Create(row).Error)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error
		_ = db.Unscoped().Delete(&gormdb.Memory{}, "id = ?", candidate.ID).Error
	})
	before, err := json.Marshal(candidate)
	require.NoError(t, err)
	parameters, err := json.Marshal(map[string]any{"candidate_ids": []int64{candidate.ID}})
	require.NoError(t, err)
	snapshotID := fmt.Sprintf("legacy-parameter-collision-%d", time.Now().UnixNano())
	snap, err := models.NewBulkOpSnapshot(snapshotID, models.SnapshotOpBulkPromote, "master", json.RawMessage(fmt.Sprintf(`{"%d":%s}`, candidate.ID, before)))
	require.NoError(t, err)
	snap.Parameters = parameters
	snap.AffectedMemoryIDs = []int64{candidate.ID}
	created, err := snapshots.Create(ctx, snap)
	require.NoError(t, err)
	require.NoError(t, db.Exec("UPDATE crystallization_candidates SET status = ?, promoted_memory_id = ? WHERE id = ?", models.CandidateStatusPromoted, candidate.ID, candidate.ID).Error)

	result, err := Rollback(ctx, adminIdentity(), created.SnapshotID, snapshots, memories, gormdb.NewAuditStore(db), candidates)
	require.ErrorIs(t, err, ErrLegacySnapshotAmbiguous)
	assert.Nil(t, result)
	stillPromoted, err := candidates.Get(ctx, candidate.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CandidateStatusPromoted, stillPromoted.Status)
	promoted, err := memories.Get(ctx, candidate.ID)
	require.NoError(t, err)
	assert.Equal(t, "promoted memory remains", promoted.Content)
}

func TestRollback_LegacyPromotionMissingPromotedMemoryEntryFailsClosed(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	snapshots := gormdb.NewSnapshotStore(db)
	candidates := gormdb.NewCandidateStore(db, nil)
	candidate, err := candidates.Create(context.Background(), &models.CrystallizationCandidate{SourceSessionID: "legacy-missing-memory", ProposedContent: "candidate stays", ProposedTier: "semantic", ProposedPromotionTarget: "semantic", PrivacyScope: "project", Status: models.CandidateStatusPending, Fingerprint: fmt.Sprintf("legacy-missing-memory-%d", time.Now().UnixNano()), AffectedProjects: []string{"legacy-missing-memory"}, Confidence: 0.9, RecurrenceCount: 1})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", candidate.ID).Error })
	before, err := json.Marshal(candidate)
	require.NoError(t, err)
	snap, err := models.NewBulkOpSnapshot(fmt.Sprintf("legacy-missing-memory-%d", time.Now().UnixNano()), models.SnapshotOpBulkPromote, "master", json.RawMessage(fmt.Sprintf(`{"%d":%s}`, candidate.ID, before)))
	require.NoError(t, err)
	snap.Parameters = json.RawMessage(fmt.Sprintf(`{"candidate_ids":[%d]}`, candidate.ID))
	snap.AffectedMemoryIDs = []int64{candidate.ID + 1000}
	created, err := snapshots.Create(context.Background(), snap)
	require.NoError(t, err)

	result, err := Rollback(context.Background(), adminIdentity(), created.SnapshotID, snapshots, memories, gormdb.NewAuditStore(db), candidates)
	require.ErrorIs(t, err, ErrLegacySnapshotAmbiguous)
	assert.Nil(t, result)
}
