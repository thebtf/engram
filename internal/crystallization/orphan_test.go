// Package crystallization_test contains EC-F4 orphan behavior tests.
//
// T030 — EC-F4: promoted candidate whose memory is hard-deleted must have
// promoted_memory_id set to NULL (ON DELETE SET NULL FK) while status stays 'promoted'.
// This verifies the ON DELETE SET NULL constraint defined in migration 132.
package crystallization_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// TestEC_F4_OrphanBehavior verifies that deleting a promoted memory nulls
// the candidate's promoted_memory_id while preserving status='promoted'.
//
// This exercises the ON DELETE SET NULL FK on crystallization_candidates.promoted_memory_id
// defined in migration 132.
func TestEC_F4_OrphanBehavior(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()

	auditStore := gormdb.NewAuditStore(db)
	cs := gormdb.NewCandidateStore(db, auditStore)
	storeWrapper := &gormdb.Store{DB: db}
	ms := gormdb.NewMemoryStore(storeWrapper)

	// Step 1: create a pending candidate.
	candidate, err := models.NewCrystallizationCandidate(
		"session-orphan-test-t030",
		"we decided to hard-delete memories to verify EC-F4 ON DELETE SET NULL",
		"rule",
		models.CandidateOptions{
			AffectedProjects: []string{"orphan-test-project"},
		},
	)
	require.NoError(t, err)
	created, err := cs.Create(ctx, candidate)
	require.NoError(t, err)
	require.Greater(t, created.ID, int64(0))

	// Step 2: create a memory (the "promoted memory").
	mem := &models.Memory{
		Content:       created.ProposedContent,
		Project:       "orphan-test-project",
		EpistemicType: "decision",
		Tier:          "episodic",
		SourceAgent:   "crystallization",
		Tags:          []string{"crystallized"},
	}
	createdMem, err := ms.CreateWithLifecycle(ctx, mem)
	require.NoError(t, err)
	require.Greater(t, createdMem.ID, int64(0))

	// Step 3: TransitionToPromoted — links candidate to memory.
	promoted, err := cs.TransitionToPromoted(ctx, created.ID, createdMem.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CandidateStatusPromoted, promoted.Status)
	require.NotNil(t, promoted.PromotedMemoryID)
	assert.Equal(t, createdMem.ID, *promoted.PromotedMemoryID, "promoted_memory_id must be set after transition")

	// Step 4: hard-delete the memory row directly via raw SQL.
	// MemoryStore.Delete is a soft-delete; we use a raw DELETE to trigger the FK.
	err = db.WithContext(ctx).Exec("DELETE FROM memories WHERE id = ?", createdMem.ID).Error
	require.NoError(t, err, "hard-delete memory must succeed")

	// Step 5: re-read the candidate and assert EC-F4 behavior:
	//   - status still 'promoted' (ON DELETE SET NULL does not change status)
	//   - promoted_memory_id is NULL
	reloaded, err := cs.Get(ctx, created.ID)
	require.NoError(t, err, "Get candidate after memory deletion must succeed")
	assert.Equal(t, models.CandidateStatusPromoted, reloaded.Status,
		"status must remain 'promoted' after promoted memory is deleted (EC-F4)")
	assert.Nil(t, reloaded.PromotedMemoryID,
		"promoted_memory_id must be NULL after promoted memory is deleted (ON DELETE SET NULL per EC-F4)")
}

// TestEC_F4_OrphanBehavior_SoftDelete verifies that a soft-deleted memory
// does NOT null promoted_memory_id (only a hard DELETE triggers the FK).
// This documents the boundary between soft and hard delete semantics.
func TestEC_F4_OrphanBehavior_SoftDelete(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()

	auditStore := gormdb.NewAuditStore(db)
	cs := gormdb.NewCandidateStore(db, auditStore)
	storeWrapper := &gormdb.Store{DB: db}
	ms := gormdb.NewMemoryStore(storeWrapper)

	candidate, err := models.NewCrystallizationCandidate(
		"session-orphan-softdelete-t030",
		"we decided to test soft-delete boundary for EC-F4 null propagation",
		"rule",
		models.CandidateOptions{
			AffectedProjects: []string{"orphan-soft-project"},
		},
	)
	require.NoError(t, err)
	created, err := cs.Create(ctx, candidate)
	require.NoError(t, err)

	mem := &models.Memory{
		Content:       created.ProposedContent,
		Project:       "orphan-soft-project",
		EpistemicType: "decision",
		Tier:          "episodic",
		SourceAgent:   "crystallization",
		Tags:          []string{"crystallized"},
	}
	createdMem, err := ms.CreateWithLifecycle(ctx, mem)
	require.NoError(t, err)

	promoted, err := cs.TransitionToPromoted(ctx, created.ID, createdMem.ID)
	require.NoError(t, err)
	require.NotNil(t, promoted.PromotedMemoryID)

	// Soft-delete the memory (sets deleted_at, row still exists).
	err = ms.Delete(ctx, createdMem.ID)
	require.NoError(t, err, "soft-delete memory must succeed")

	// Soft-delete does NOT trigger the FK; promoted_memory_id remains set.
	reloaded, err := cs.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CandidateStatusPromoted, reloaded.Status)
	require.NotNil(t, reloaded.PromotedMemoryID,
		"promoted_memory_id must NOT be nulled by soft-delete (FK only fires on hard DELETE)")
	assert.Equal(t, createdMem.ID, *reloaded.PromotedMemoryID)

	// Cleanup: hard-delete to avoid polluting other tests.
	_ = db.WithContext(ctx).Exec("DELETE FROM memories WHERE id = ?", createdMem.ID).Error
}

// TestEC_F4_StateAfterPromotion_InvalidTransition verifies that a promoted candidate
// cannot be transitioned again (terminal state enforcement per EC-F10 state machine).
func TestEC_F4_StateAfterPromotion_InvalidTransition(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()

	auditStore := gormdb.NewAuditStore(db)
	cs := gormdb.NewCandidateStore(db, auditStore)
	storeWrapper := &gormdb.Store{DB: db}
	ms := gormdb.NewMemoryStore(storeWrapper)

	candidate, err := models.NewCrystallizationCandidate(
		"session-terminal-t030",
		"we decided to test that terminal state rejects further transitions",
		"rule",
		models.CandidateOptions{
			AffectedProjects: []string{"terminal-test-project"},
		},
	)
	require.NoError(t, err)
	created, err := cs.Create(ctx, candidate)
	require.NoError(t, err)

	mem := &models.Memory{
		Content: created.ProposedContent,
		Project: "terminal-test-project",
		SourceAgent: "crystallization",
		Tags:    []string{"crystallized"},
	}
	createdMem, err := ms.CreateWithLifecycle(ctx, mem)
	require.NoError(t, err)

	_, err = cs.TransitionToPromoted(ctx, created.ID, createdMem.ID)
	require.NoError(t, err)

	// Attempt a second transition — must fail with ErrInvalidTransition.
	_, err = cs.TransitionToRejected(ctx, created.ID, "should fail: already promoted")
	require.Error(t, err, "TransitionToRejected on a promoted candidate must return an error")
	assert.ErrorIs(t, err, gormdb.ErrInvalidTransition,
		"error must be ErrInvalidTransition for promoted→rejected attempt")

	// Cleanup.
	_ = db.WithContext(ctx).Exec("DELETE FROM memories WHERE id = ?", createdMem.ID).Error
}

// helperDeleteCandidate is used by tests to clean up candidate rows from the DB.
// It uses a raw DELETE to bypass soft-delete logic.
func helperDeleteCandidate(t *testing.T, db *gorm.DB, id int64) {
	t.Helper()
	_ = db.Exec("DELETE FROM crystallization_candidates WHERE id = ?", id).Error
}
