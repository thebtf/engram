package gorm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/internal/reviewpacket"
	"github.com/thebtf/engram/pkg/models"
)

// openCandidateTestDB opens a test PostgreSQL connection or skips the test.
func openCandidateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
	require.NoError(t, err, "open test DB")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())

	// Ensure migration chain is applied.
	require.NoError(t, runMigrations(db), "runMigrations")
	return db
}

// TestCandidateStore_CRUDRoundtrip creates, retrieves, and lists a candidate.
func TestCandidateStore_CRUDRoundtrip(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	cs := NewCandidateStore(db, auditStore)
	ctx := context.Background()

	// Build a valid candidate. Unique session ID prevents fingerprint collisions on re-run.
	candidate, err := models.NewCrystallizationCandidate(
		fmt.Sprintf("session-roundtrip-%d", time.Now().UnixNano()),
		"we decided to use Redis because of cluster mode",
		"rule",
		models.CandidateOptions{
			AffectedProjects: []string{"test-project"},
		},
	)
	require.NoError(t, err)

	// Create.
	created, err := cs.Create(ctx, candidate)
	require.NoError(t, err)
	require.NotZero(t, created.ID, "created candidate must have a DB-assigned ID")
	require.Equal(t, models.CandidateStatusPending, created.Status)
	require.Equal(t, "we decided to use Redis because of cluster mode", created.ProposedContent)
	require.NotNil(t, created.ReviewAfter, "review_after must be set")

	// Get by ID.
	got, err := cs.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, got.ID)
	require.Equal(t, created.Fingerprint, got.Fingerprint)

	// List by status.
	list, err := cs.ListByStatus(ctx, "test-project", models.CandidateStatusPending, 10)
	require.NoError(t, err)
	found := false
	for _, c := range list {
		if c.ID == created.ID {
			found = true
			break
		}
	}
	require.True(t, found, "created candidate must appear in ListByStatus(pending)")
}

// TestCandidateStore_StateMachine_AllTransitionsFromPending verifies all 4 terminal transitions.
func TestCandidateStore_StateMachine_AllTransitionsFromPending(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	cs := NewCandidateStore(db, auditStore)
	ctx := context.Background()

	for _, tc := range []struct {
		name       string
		transition func(id int64) error
		wantStatus models.CandidateStatus
	}{
		{
			name: "promoted",
			transition: func(id int64) error {
				// We need a real memory ID for promote; insert a minimal memory first.
				var memID int64
				err := db.Raw(`INSERT INTO memories (project, content, tags, status, version)
					VALUES ('test-project', 'mem for promote test', '[]', 'active', 1)
					RETURNING id`).Scan(&memID).Error
				if err != nil {
					return err
				}
				_, err = cs.TransitionToPromoted(ctx, id, memID)
				return err
			},
			wantStatus: models.CandidateStatusPromoted,
		},
		{
			name: "rejected",
			transition: func(id int64) error {
				_, err := cs.TransitionToRejected(ctx, id, "not relevant enough")
				return err
			},
			wantStatus: models.CandidateStatusRejected,
		},
		{
			name: "superseded",
			transition: func(id int64) error {
				_, err := cs.TransitionToSuperseded(ctx, id)
				return err
			},
			wantStatus: models.CandidateStatusSuperseded,
		},
		{
			name: "decayed",
			transition: func(id int64) error {
				_, err := cs.TransitionToDecayed(ctx, id)
				return err
			},
			wantStatus: models.CandidateStatusDecayed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Create a fresh pending candidate for each sub-test. Unique session ID prevents
			// fingerprint collisions on repeated test runs against the same database.
			candidate, err := models.NewCrystallizationCandidate(
				fmt.Sprintf("session-sm-%s-%d", tc.name, time.Now().UnixNano()),
				"content for "+tc.name,
				"rule",
				models.CandidateOptions{AffectedProjects: []string{"test-project"}},
			)
			require.NoError(t, err)
			created, err := cs.Create(ctx, candidate)
			require.NoError(t, err)

			// Perform the transition.
			require.NoError(t, tc.transition(created.ID))

			// Verify status in DB.
			got, err := cs.Get(ctx, created.ID)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, got.Status, "status after %s transition", tc.name)
		})
	}
}

// TestCandidateStore_StateMachine_IllegalTransition verifies that terminal→any is rejected.
func TestCandidateStore_StateMachine_IllegalTransition(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	cs := NewCandidateStore(db, auditStore)
	ctx := context.Background()

	candidate, err := models.NewCrystallizationCandidate(fmt.Sprintf("session-illegal-%d", time.Now().UnixNano()), "content", "rule", models.CandidateOptions{})
	require.NoError(t, err)
	created, err := cs.Create(ctx, candidate)
	require.NoError(t, err)

	// Transition to rejected (legal).
	_, err = cs.TransitionToRejected(ctx, created.ID, "first rejection")
	require.NoError(t, err)

	// Attempt promoted from rejected (illegal — terminal state).
	_, err = cs.TransitionToPromoted(ctx, created.ID, 999)
	require.Error(t, err, "transition from rejected to promoted must be rejected")
	require.True(t, errors.Is(err, ErrInvalidTransition) || containsStr(err.Error(), "invalid_transition"),
		"error must indicate invalid_transition, got: %v", err)

	// Attempt decayed from rejected (also illegal).
	_, err = cs.TransitionToDecayed(ctx, created.ID)
	require.Error(t, err, "transition from rejected to decayed must be rejected")
}

// TestCandidateStore_StateMachine_ConcurrentRace verifies EC-F10: SELECT...FOR UPDATE
// serialises concurrent transitions so only one goroutine wins a given ID.
func TestCandidateStore_StateMachine_ConcurrentRace(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	cs := NewCandidateStore(db, auditStore)
	ctx := context.Background()

	// Create a shared pending candidate.
	candidate, err := models.NewCrystallizationCandidate(fmt.Sprintf("session-race-%d", time.Now().UnixNano()), "race content", "rule", models.CandidateOptions{})
	require.NoError(t, err)
	created, err := cs.Create(ctx, candidate)
	require.NoError(t, err)

	// 10 goroutines all attempt to reject the same candidate concurrently.
	const n = 10
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			_, errs[i] = cs.TransitionToRejected(ctx, created.ID, "concurrent rejection")
		}()
	}
	wg.Wait()

	// Exactly 1 goroutine must have succeeded; the rest must have failed with invalid_transition.
	successCount := 0
	for _, e := range errs {
		if e == nil {
			successCount++
		}
	}
	require.Equal(t, 1, successCount, "exactly one goroutine must win the transition")

	// Final status must be rejected.
	got, err := cs.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, models.CandidateStatusRejected, got.Status)
}

// TestCandidateStore_FingerprintIdempotency verifies that duplicate pending candidates
// for the same fingerprint cannot be inserted (partial unique index).
func TestCandidateStore_FingerprintIdempotency(t *testing.T) {
	db := openCandidateTestDB(t)
	cs := NewCandidateStore(db, nil)
	ctx := context.Background()

	// Use a fixed session ID here intentionally: the test exercises fingerprint collision
	// between two candidates with the SAME session+content, so both must share one session ID.
	// The cleanup before the test (t.Cleanup) is not available without a DB truncate, so we
	// use a run-unique prefix to isolate this test's fingerprint from prior runs.
	sessionFP := fmt.Sprintf("session-fp-%d", time.Now().UnixNano())
	c, err := models.NewCrystallizationCandidate(sessionFP, "idempotent content", "rule", models.CandidateOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, c.Fingerprint, "fingerprint must be non-empty for session+content pair")

	// First insert succeeds.
	_, err = cs.Create(ctx, c)
	require.NoError(t, err)

	// Second insert with same fingerprint must fail (unique constraint on pending+fingerprint).
	c2, _ := models.NewCrystallizationCandidate(sessionFP, "idempotent content", "rule", models.CandidateOptions{})
	_, err = cs.Create(ctx, c2)
	require.Error(t, err, "duplicate pending fingerprint must be rejected by unique partial index")
}

// TestCandidateStore_AuditLogWrittenOnTransition verifies that the audit log
// receives an entry for each state-machine transition (spec §FR-F5 enum).
func TestCandidateStore_AuditLogWrittenOnTransition(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	cs := NewCandidateStore(db, auditStore)
	ctx := context.Background()

	candidate, err := models.NewCrystallizationCandidate(fmt.Sprintf("session-audit-%d", time.Now().UnixNano()), "content for audit", "rule", models.CandidateOptions{})
	require.NoError(t, err)
	created, err := cs.Create(ctx, candidate)
	require.NoError(t, err)

	beforeCount := countAuditRows(t, db, "decay_candidate")
	_, err = cs.TransitionToDecayed(ctx, created.ID)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return countAuditRows(t, db, "decay_candidate") > beforeCount
	}, 2*time.Second, 25*time.Millisecond, "audit_log must have a new decay_candidate entry")
}

func countAuditRows(t *testing.T, db *gorm.DB, action string) int {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&AuditLogEntry{}).Where("action = ?", action).Count(&count).Error)
	return int(count)
}

// TestCandidateStore_ListExpiredPending verifies decay-batch candidate selection.
func TestCandidateStore_ListExpiredPending(t *testing.T) {
	db := openCandidateTestDB(t)
	cs := NewCandidateStore(db, nil)
	ctx := context.Background()

	// Insert a candidate whose review_after is in the past.
	past := time.Now().UTC().Add(-48 * time.Hour)
	expired, err := models.NewCrystallizationCandidate(fmt.Sprintf("session-decay-%d", time.Now().UnixNano()), "expired content", "rule", models.CandidateOptions{})
	require.NoError(t, err)
	expired.ReviewAfter = &past

	createdExpired, err := cs.Create(ctx, expired)
	require.NoError(t, err)

	// Insert a candidate whose review_after is in the future.
	future, err := models.NewCrystallizationCandidate(fmt.Sprintf("session-future-%d", time.Now().UnixNano()), "future content", "rule", models.CandidateOptions{})
	require.NoError(t, err)
	_, err = cs.Create(ctx, future)
	require.NoError(t, err)

	list, err := cs.ListExpiredPending(ctx, 5, 100)
	require.NoError(t, err)

	foundExpired := false
	for _, c := range list {
		if c.ID == createdExpired.ID {
			foundExpired = true
		}
	}
	require.True(t, foundExpired, "expired pending candidate must appear in ListExpiredPending")
}

// TestCandidateStore_TransitionToRejected_PreservesProposedContent verifies
// MAJOR finding 1: rejecting a candidate must not overwrite proposed_content.
// The reason lands in the audit log; the original decision text is untouched.
func TestCandidateStore_TransitionToRejected_PreservesProposedContent(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	cs := NewCandidateStore(db, auditStore)
	ctx := context.Background()

	originalContent := "decided to use PostgreSQL for transactional workloads"
	candidate, err := models.NewCrystallizationCandidate(
		fmt.Sprintf("session-reject-preserve-%d", time.Now().UnixNano()),
		originalContent,
		"rule",
		models.CandidateOptions{AffectedProjects: []string{"test-project"}},
	)
	require.NoError(t, err)
	created, err := cs.Create(ctx, candidate)
	require.NoError(t, err)

	reason := "not aligned with current architecture decisions"
	rejected, err := cs.TransitionToRejected(ctx, created.ID, reason)
	require.NoError(t, err)
	require.Equal(t, models.CandidateStatusRejected, rejected.Status)

	// The proposed_content must be the ORIGINAL text, not the rejection reason.
	require.Equal(t, originalContent, rejected.ProposedContent,
		"TransitionToRejected must not overwrite proposed_content with the reason")

	// Verify from DB (not just the returned struct).
	got, err := cs.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, originalContent, got.ProposedContent,
		"proposed_content must be preserved in the database after rejection")
}

// TestCandidateStore_PromoteWithMemory_AtomicRollback verifies MAJOR finding 2:
// when the candidate status update is forced to fail (invalid transition from a
// pre-transitioned terminal state), the entire PromoteWithMemory call returns an
// error and no orphan memory row is committed.
//
// This is the closest approximation to a "Step-B transient failure" test without
// sqlmock: we set the candidate to 'rejected' (terminal) before calling
// PromoteWithMemory, so the SELECT...FOR UPDATE / validTransitions check fails,
// rolling back the whole transaction — including any memory that was inserted.
func TestCandidateStore_PromoteWithMemory_AtomicRollback(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	cs := NewCandidateStore(db, auditStore)
	ctx := context.Background()

	// Create a candidate and immediately reject it (terminal state).
	candidate, err := models.NewCrystallizationCandidate(
		fmt.Sprintf("session-promote-rollback-%d", time.Now().UnixNano()),
		"content for atomic promote test",
		"rule",
		models.CandidateOptions{AffectedProjects: []string{"test-project"}},
	)
	require.NoError(t, err)
	created, err := cs.Create(ctx, candidate)
	require.NoError(t, err)

	_, err = cs.TransitionToRejected(ctx, created.ID, "pre-rejected for rollback test")
	require.NoError(t, err)

	// Count memory rows before the attempted promote.
	var memCountBefore int64
	require.NoError(t, db.Model(&Memory{}).Count(&memCountBefore).Error)

	// PromoteWithMemory must fail because the candidate is in a terminal state.
	mem := &models.Memory{
		Content:       "content for atomic promote test",
		Project:       "test-project",
		EpistemicType: "decision",
		Tier:          "episodic",
		SourceAgent:   "crystallization",
	}
	_, _, err = cs.PromoteWithMemory(ctx, created.ID, mem)
	require.Error(t, err, "PromoteWithMemory must fail for a terminal candidate")
	require.True(t, errors.Is(err, ErrInvalidTransition) || containsStr(err.Error(), "invalid_transition"),
		"error must be ErrInvalidTransition, got: %v", err)

	// No orphan memory row should have been created.
	var memCountAfter int64
	require.NoError(t, db.Model(&Memory{}).Count(&memCountAfter).Error)
	require.Equal(t, memCountBefore, memCountAfter,
		"transaction rollback must not leave an orphan memory row")
}

func TestCandidateStore_PromoteWithMemory_InheritsCandidatePrivacyScope(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	cs := NewCandidateStore(db, auditStore)
	ctx := context.Background()

	candidate, err := models.NewCrystallizationCandidate(
		fmt.Sprintf("session-promote-private-%d", time.Now().UnixNano()),
		"content for private candidate promotion test",
		"rule",
		models.CandidateOptions{
			AffectedProjects: []string{"test-project"},
			PrivacyScope:     "private",
		},
	)
	require.NoError(t, err)
	createdCandidate, err := cs.Create(ctx, candidate)
	require.NoError(t, err)
	require.Equal(t, "private", createdCandidate.PrivacyScope)

	mem := &models.Memory{
		Content:       candidate.ProposedContent,
		Project:       "test-project",
		EpistemicType: "decision",
		Tier:          "episodic",
		SourceAgent:   "crystallization",
	}
	updatedCandidate, promotedMemory, err := cs.PromoteWithMemory(ctx, createdCandidate.ID, mem)
	require.NoError(t, err)
	require.Equal(t, models.CandidateStatusPromoted, updatedCandidate.Status)
	require.NotNil(t, promotedMemory)
	require.NotZero(t, promotedMemory.ID)

	var promotedRow Memory
	require.NoError(t, db.First(&promotedRow, promotedMemory.ID).Error)
	require.Equal(t, "private", promotedRow.PrivacyScope,
		"promoted memory must inherit the locked candidate privacy_scope instead of defaulting to project")
}

func TestCandidateStore_PromoteWithMemoryAndSnapshot_AmendFailureRollsBackPromotion(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	cs := NewCandidateStore(db, auditStore)
	snapshotStore := NewSnapshotStore(db)
	ctx := context.Background()

	candidate, err := models.NewCrystallizationCandidate(
		fmt.Sprintf("session-promote-snapshot-rollback-%d", time.Now().UnixNano()),
		"content for snapshot amend rollback test",
		"rule",
		models.CandidateOptions{AffectedProjects: []string{"test-project"}},
	)
	require.NoError(t, err)
	createdCandidate, err := cs.Create(ctx, candidate)
	require.NoError(t, err)

	var memCountBefore int64
	require.NoError(t, db.Model(&Memory{}).Count(&memCountBefore).Error)

	snapshot, err := reviewpacket.NewCandidateReviewActionSnapshot("promote", createdCandidate, "agent/tester")
	require.NoError(t, err)

	suffix := time.Now().UnixNano()
	triggerName := fmt.Sprintf("test_fail_snapshot_amend_%d", suffix)
	functionName := fmt.Sprintf("test_fail_snapshot_amend_fn_%d", suffix)
	require.NoError(t, db.Exec(fmt.Sprintf(`
CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS $$
BEGIN
	RAISE EXCEPTION 'forced snapshot amend failure';
END;
$$ LANGUAGE plpgsql`, functionName)).Error)
	t.Cleanup(func() {
		_ = db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON bulk_op_snapshots", triggerName)).Error
		_ = db.Exec(fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName)).Error
	})
	require.NoError(t, db.Exec(fmt.Sprintf(`
CREATE TRIGGER %s
BEFORE UPDATE ON bulk_op_snapshots
FOR EACH ROW
WHEN (OLD.snapshot_id = '%s')
EXECUTE FUNCTION %s()`, triggerName, snapshot.SnapshotID, functionName)).Error)

	mem := &models.Memory{
		Content:       "content for snapshot amend rollback test",
		Project:       "test-project",
		EpistemicType: "decision",
		Tier:          "episodic",
		SourceAgent:   "crystallization",
	}
	_, _, _, err = cs.PromoteWithMemoryAndSnapshot(ctx, snapshotStore, createdCandidate.ID, mem, snapshot, "agent/tester")
	require.Error(t, err, "forced snapshot amend failure must occur after snapshot create and promotion")
	require.Contains(t, err.Error(), "forced snapshot amend failure")

	gotCandidate, err := cs.Get(ctx, createdCandidate.ID)
	require.NoError(t, err)
	require.Equal(t, models.CandidateStatusPending, gotCandidate.Status,
		"snapshot amend failure must roll back the candidate promotion")
	require.Nil(t, gotCandidate.PromotedMemoryID,
		"snapshot amend failure must not leave a promoted memory pointer")

	var memCountAfter int64
	require.NoError(t, db.Model(&Memory{}).Count(&memCountAfter).Error)
	require.Equal(t, memCountBefore, memCountAfter,
		"snapshot amend failure must roll back the created memory row")

	var snapshotCount int64
	require.NoError(t, db.Model(&snapshotRow{}).Where("snapshot_id = ?", snapshot.SnapshotID).Count(&snapshotCount).Error)
	require.Zero(t, snapshotCount,
		"snapshot create must roll back with the failed promotion transaction")
}

func TestCandidateStore_PromoteWithMemoryAndSnapshot_WritesCandidateReviewAudit(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	cs := NewCandidateStore(db, auditStore)
	snapshotStore := NewSnapshotStore(db)
	ctx := context.Background()

	candidate := createCandidateReviewStoreTestCandidate(t, cs, ctx, "promote-audit")
	snapshot := newCandidateReviewStoreTestSnapshot(t, candidate, "promote", "agent/promoter")
	mem := &models.Memory{
		Content:       candidate.ProposedContent,
		Project:       "test-project",
		EpistemicType: "decision",
		Tier:          "episodic",
		SourceAgent:   "crystallization",
	}

	updated, created, _, err := cs.PromoteWithMemoryAndSnapshot(ctx, snapshotStore, candidate.ID, mem, snapshot, "agent/promoter")
	require.NoError(t, err)
	require.Equal(t, models.CandidateStatusPromoted, updated.Status)
	require.NotNil(t, created)

	var entry AuditLogEntry
	require.NoError(t, db.Where("action = ? AND actor = ? AND source_session_id = ?", "candidate_review", "agent/promoter", candidate.SourceSessionID).
		Order("id DESC").
		First(&entry).Error)
	require.NotNil(t, entry.BeforeState)
	require.NotNil(t, entry.AfterState)
	require.Contains(t, entry.Reason, "review action promote")
}

func TestCandidateStore_PreserveWithMemoryAndSnapshot_RequiresCandidateReviewSnapshotBeforeMutation(t *testing.T) {
	cs := NewCandidateStore(nil, nil)
	mem := &models.Memory{Content: "preserve requires snapshot", Project: "test-project", EpistemicType: "decision", Tier: "episodic", SourceAgent: "crystallization"}

	updated, created, snapshot, err := cs.PreserveWithMemoryAndSnapshot(context.Background(), nil, 42, mem, nil, "agent/reviewer")

	require.Error(t, err)
	require.Contains(t, err.Error(), "candidate_review snapshot is required")
	require.Nil(t, updated)
	require.Nil(t, created)
	require.Nil(t, snapshot)
}

func TestCandidateStore_TransitionToRejectedWithSnapshot_WritesCandidateReviewAudit(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	cs := NewCandidateStore(db, auditStore)
	snapshotStore := NewSnapshotStore(db)
	ctx := context.Background()

	candidate := createCandidateReviewStoreTestCandidate(t, cs, ctx, "reject-audit")
	snapshot := newCandidateReviewStoreTestSnapshot(t, candidate, "reject", "agent/reviewer")

	updated, createdSnapshot, err := cs.TransitionToRejectedWithSnapshot(ctx, snapshotStore, candidate.ID, "not durable enough", snapshot, "agent/reviewer")
	require.NoError(t, err)
	require.Equal(t, models.CandidateStatusRejected, updated.Status)
	require.NotNil(t, createdSnapshot)

	var entry AuditLogEntry
	require.NoError(t, db.Where("action = ? AND actor = ? AND source_session_id = ?", "candidate_review", "agent/reviewer", candidate.SourceSessionID).
		Order("id DESC").
		First(&entry).Error)
	require.Contains(t, entry.Reason, "review action reject")
	require.Contains(t, entry.Reason, "not durable enough")
}

func TestCandidateStore_TransitionWithSnapshotRequiresAuditStore(t *testing.T) {
	db := openCandidateTestDB(t)
	cs := NewCandidateStore(db, nil)
	snapshotStore := NewSnapshotStore(db)
	ctx := context.Background()

	candidate := createCandidateReviewStoreTestCandidate(t, cs, ctx, "missing-audit")
	snapshot := newCandidateReviewStoreTestSnapshot(t, candidate, "reject", "agent/reviewer")

	_, _, err := cs.TransitionToRejectedWithSnapshot(ctx, snapshotStore, candidate.ID, "missing audit store", snapshot, "agent/reviewer")
	require.Error(t, err)
	require.Contains(t, err.Error(), "candidate_review audit store")

	got, getErr := cs.Get(ctx, candidate.ID)
	require.NoError(t, getErr)
	require.Equal(t, models.CandidateStatusPending, got.Status)

	var snapshotCount int64
	require.NoError(t, db.Model(&snapshotRow{}).Where("snapshot_id = ?", snapshot.SnapshotID).Count(&snapshotCount).Error)
	require.Zero(t, snapshotCount)
}

func TestCandidateStore_TransitionToSupersededWithSnapshot_AuditFailureRollsBack(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	cs := NewCandidateStore(db, auditStore)
	snapshotStore := NewSnapshotStore(db)
	ctx := context.Background()

	candidate := createCandidateReviewStoreTestCandidate(t, cs, ctx, "supersede-audit-rollback")
	snapshot := newCandidateReviewStoreTestSnapshot(t, candidate, "supersede", "agent/fail")

	suffix := time.Now().UnixNano()
	triggerName := fmt.Sprintf("test_fail_candidate_review_audit_%d", suffix)
	functionName := fmt.Sprintf("test_fail_candidate_review_audit_fn_%d", suffix)
	require.NoError(t, db.Exec(fmt.Sprintf(`
CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS $$
BEGIN
	RAISE EXCEPTION 'forced candidate_review audit failure';
END;
$$ LANGUAGE plpgsql`, functionName)).Error)
	t.Cleanup(func() {
		_ = db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON audit_log", triggerName)).Error
		_ = db.Exec(fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName)).Error
	})
	require.NoError(t, db.Exec(fmt.Sprintf(`
CREATE TRIGGER %s
BEFORE INSERT ON audit_log
FOR EACH ROW
WHEN (NEW.action = 'candidate_review' AND NEW.actor = 'agent/fail')
EXECUTE FUNCTION %s()`, triggerName, functionName)).Error)

	_, _, err := cs.TransitionToSupersededWithSnapshot(ctx, snapshotStore, candidate.ID, snapshot, "agent/fail")
	require.Error(t, err)
	require.Contains(t, err.Error(), "forced candidate_review audit failure")

	got, getErr := cs.Get(ctx, candidate.ID)
	require.NoError(t, getErr)
	require.Equal(t, models.CandidateStatusPending, got.Status)

	var snapshotCount int64
	require.NoError(t, db.Model(&snapshotRow{}).Where("snapshot_id = ?", snapshot.SnapshotID).Count(&snapshotCount).Error)
	require.Zero(t, snapshotCount)
}

type candidateReviewSnapshotSeamCase struct {
	name          string
	action        string
	wantStatus    models.CandidateStatus
	createsMemory bool
}

func candidateReviewSnapshotSeamCases() []candidateReviewSnapshotSeamCase {
	return []candidateReviewSnapshotSeamCase{
		{name: "promote", action: "promote", wantStatus: models.CandidateStatusPromoted, createsMemory: true},
		{name: "preserve", action: "preserve", wantStatus: models.CandidateStatusPromoted, createsMemory: true},
		{name: "reject", action: "reject", wantStatus: models.CandidateStatusRejected},
		{name: "suppress", action: "suppress", wantStatus: models.CandidateStatusRejected},
		{name: "supersede", action: "supersede", wantStatus: models.CandidateStatusSuperseded},
	}
}

func candidateReviewStoreTestMemory(candidate *models.CrystallizationCandidate) *models.Memory {
	return &models.Memory{
		Content:       candidate.ProposedContent,
		Project:       "test-project",
		EpistemicType: "decision",
		Tier:          "episodic",
		SourceAgent:   "crystallization",
	}
}

func callCandidateReviewSnapshotSeam(
	ctx context.Context,
	cs *CandidateStore,
	snapshotStore *SnapshotStore,
	seam candidateReviewSnapshotSeamCase,
	candidate *models.CrystallizationCandidate,
	snapshot *models.BulkOpSnapshot,
	actor string,
) error {
	switch seam.name {
	case "promote":
		_, _, _, err := cs.PromoteWithMemoryAndSnapshot(ctx, snapshotStore, candidate.ID, candidateReviewStoreTestMemory(candidate), snapshot, actor)
		return err
	case "preserve":
		_, _, _, err := cs.PreserveWithMemoryAndSnapshot(ctx, snapshotStore, candidate.ID, candidateReviewStoreTestMemory(candidate), snapshot, actor)
		return err
	case "reject":
		_, _, err := cs.TransitionToRejectedWithSnapshot(ctx, snapshotStore, candidate.ID, "not durable enough", snapshot, actor)
		return err
	case "suppress":
		_, _, err := cs.TransitionToSuppressedWithSnapshot(ctx, snapshotStore, candidate.ID, "too noisy", snapshot, actor)
		return err
	case "supersede":
		_, _, err := cs.TransitionToSupersededWithSnapshot(ctx, snapshotStore, candidate.ID, snapshot, actor)
		return err
	default:
		return fmt.Errorf("unknown candidate-review seam %q", seam.name)
	}
}

func setCandidateReviewSnapshotParameter(t *testing.T, snapshot *models.BulkOpSnapshot, key string, value any) {
	t.Helper()
	var parameters map[string]any
	require.NoError(t, json.Unmarshal(snapshot.Parameters, &parameters))
	parameters[key] = value
	updated, err := json.Marshal(parameters)
	require.NoError(t, err)
	snapshot.Parameters = updated
}

func candidateReviewSnapshotEntries(t *testing.T, snapshot *models.BulkOpSnapshot) map[string]models.SnapshotEntry {
	t.Helper()
	var entries map[string]models.SnapshotEntry
	require.NoError(t, json.Unmarshal(snapshot.BeforeState, &entries))
	return entries
}

func setCandidateReviewSnapshotEntries(t *testing.T, snapshot *models.BulkOpSnapshot, entries map[string]models.SnapshotEntry) {
	t.Helper()
	updated, err := json.Marshal(entries)
	require.NoError(t, err)
	snapshot.BeforeState = updated
}

func countCandidateReviewTestRows(t *testing.T, db *gorm.DB) (memories int64, snapshots int64) {
	t.Helper()
	require.NoError(t, db.Model(&Memory{}).Count(&memories).Error)
	require.NoError(t, db.Model(&snapshotRow{}).Count(&snapshots).Error)
	return memories, snapshots
}

func TestCandidateStore_AllCandidateReviewSnapshotSeamsRejectInvalidBindingsWithoutWrites(t *testing.T) {
	db := openCandidateTestDB(t)
	ctx := context.Background()

	invalidCases := []string{
		"nil_snapshot",
		"nil_snapshot_store",
		"nil_audit_store",
		"wrong_op_type",
		"wrong_operation_parameter",
		"wrong_action_parameter",
		"wrong_candidate_parameter",
		"wrong_actor",
		"wrong_before_key",
		"wrong_before_payload_id",
		"prepopulated_after",
		"extra_before_entry",
		"prepopulated_affected_memory_ids",
		"wrong_source_session",
		"forged_payload_and_source_session",
	}

	for _, seam := range candidateReviewSnapshotSeamCases() {
		seam := seam
		for _, invalidCase := range invalidCases {
			invalidCase := invalidCase
			t.Run(seam.name+"/"+invalidCase, func(t *testing.T) {
				auditStore := NewAuditStore(db)
				candidateStore := NewCandidateStore(db, auditStore)
				snapshotStore := NewSnapshotStore(db)
				candidate := createCandidateReviewStoreTestCandidate(t, candidateStore, ctx, seam.name+"-"+invalidCase)
				actor := "agent/tester"
				snapshot := newCandidateReviewStoreTestSnapshot(t, candidate, seam.action, actor)

				invokeStore := candidateStore
				invokeSnapshotStore := snapshotStore
				invokeSnapshot := snapshot
				invokeActor := actor

				switch invalidCase {
				case "nil_snapshot":
					invokeSnapshot = nil
				case "nil_snapshot_store":
					invokeSnapshotStore = nil
				case "nil_audit_store":
					invokeStore = NewCandidateStore(db, nil)
				case "wrong_op_type":
					snapshot.OpType = models.SnapshotOpBulkPromote
				case "wrong_operation_parameter":
					setCandidateReviewSnapshotParameter(t, snapshot, "operation", "bulk_promote")
				case "wrong_action_parameter":
					wrongAction := "reject"
					if seam.action == wrongAction {
						wrongAction = "promote"
					}
					setCandidateReviewSnapshotParameter(t, snapshot, "action", wrongAction)
				case "wrong_candidate_parameter":
					setCandidateReviewSnapshotParameter(t, snapshot, "candidate_id", candidate.ID+1)
				case "wrong_actor":
					snapshot.Actor = "agent/other"
				case "wrong_before_key":
					entries := candidateReviewSnapshotEntries(t, snapshot)
					entry := entries[fmt.Sprintf("candidate:%d", candidate.ID)]
					delete(entries, fmt.Sprintf("candidate:%d", candidate.ID))
					entries[fmt.Sprintf("candidate:%d", candidate.ID+1)] = entry
					setCandidateReviewSnapshotEntries(t, snapshot, entries)
				case "wrong_before_payload_id":
					entries := candidateReviewSnapshotEntries(t, snapshot)
					key := fmt.Sprintf("candidate:%d", candidate.ID)
					entry := entries[key]
					var beforeCandidate models.CrystallizationCandidate
					require.NoError(t, json.Unmarshal(entry.Before, &beforeCandidate))
					beforeCandidate.ID++
					entry.Before, _ = json.Marshal(&beforeCandidate)
					entries[key] = entry
					setCandidateReviewSnapshotEntries(t, snapshot, entries)
				case "prepopulated_after":
					entries := candidateReviewSnapshotEntries(t, snapshot)
					key := fmt.Sprintf("candidate:%d", candidate.ID)
					entry := entries[key]
					entry.After = append(json.RawMessage(nil), entry.Before...)
					entries[key] = entry
					setCandidateReviewSnapshotEntries(t, snapshot, entries)
				case "extra_before_entry":
					entries := candidateReviewSnapshotEntries(t, snapshot)
					entries["memory:999"] = models.SnapshotEntry{Kind: models.EntryKindDelete}
					setCandidateReviewSnapshotEntries(t, snapshot, entries)
				case "prepopulated_affected_memory_ids":
					snapshot.AffectedMemoryIDs = []int64{999}
				case "wrong_source_session":
					snapshot.SourceSessionID = "session-for-another-candidate"
				case "forged_payload_and_source_session":
					entries := candidateReviewSnapshotEntries(t, snapshot)
					key := fmt.Sprintf("candidate:%d", candidate.ID)
					entry := entries[key]
					var beforeCandidate models.CrystallizationCandidate
					require.NoError(t, json.Unmarshal(entry.Before, &beforeCandidate))
					beforeCandidate.ProposedContent = "forged rollback content"
					beforeCandidate.SourceSessionID = "forged-source-session"
					entry.Before, _ = json.Marshal(&beforeCandidate)
					entries[key] = entry
					setCandidateReviewSnapshotEntries(t, snapshot, entries)
					snapshot.SourceSessionID = beforeCandidate.SourceSessionID
				default:
					t.Fatalf("unhandled invalid case %q", invalidCase)
				}

				memoriesBefore, snapshotsBefore := countCandidateReviewTestRows(t, db)
				auditsBefore := countAuditRows(t, db, "candidate_review")

				err := callCandidateReviewSnapshotSeam(ctx, invokeStore, invokeSnapshotStore, seam, candidate, invokeSnapshot, invokeActor)

				memoriesAfter, snapshotsAfter := countCandidateReviewTestRows(t, db)
				auditsAfter := countAuditRows(t, db, "candidate_review")
				storedCandidate, getErr := candidateStore.Get(ctx, candidate.ID)
				require.NoError(t, getErr)

				require.Error(t, err, "invalid candidate-review snapshot binding must fail closed")
				require.Equal(t, models.CandidateStatusPending, storedCandidate.Status)
				require.Nil(t, storedCandidate.PromotedMemoryID)
				require.Equal(t, memoriesBefore, memoriesAfter, "invalid binding must not create memory rows")
				require.Equal(t, snapshotsBefore, snapshotsAfter, "invalid binding must not create snapshot rows")
				require.Equal(t, auditsBefore, auditsAfter, "invalid binding must not create candidate_review audit rows")
			})
		}
	}
}

func TestCandidateStore_AllCandidateReviewSnapshotSeamsCommitExactlyOneAudit(t *testing.T) {
	db := openCandidateTestDB(t)
	ctx := context.Background()
	auditStore := NewAuditStore(db)
	candidateStore := NewCandidateStore(db, auditStore)
	snapshotStore := NewSnapshotStore(db)

	for _, seam := range candidateReviewSnapshotSeamCases() {
		seam := seam
		t.Run(seam.name, func(t *testing.T) {
			candidate := createCandidateReviewStoreTestCandidate(t, candidateStore, ctx, "valid-"+seam.name)
			actor := "  agent/tester  "
			snapshot := newCandidateReviewStoreTestSnapshot(t, candidate, seam.action, actor)
			memoriesBefore, snapshotsBefore := countCandidateReviewTestRows(t, db)
			auditsBefore := countAuditRows(t, db, "candidate_review")

			err := callCandidateReviewSnapshotSeam(ctx, candidateStore, snapshotStore, seam, candidate, snapshot, actor)

			require.NoError(t, err)
			storedCandidate, getErr := candidateStore.Get(ctx, candidate.ID)
			require.NoError(t, getErr)
			require.Equal(t, seam.wantStatus, storedCandidate.Status)
			memoriesAfter, snapshotsAfter := countCandidateReviewTestRows(t, db)
			auditsAfter := countAuditRows(t, db, "candidate_review")
			require.Equal(t, snapshotsBefore+1, snapshotsAfter)
			require.Equal(t, auditsBefore+1, auditsAfter, "valid candidate-review seam must write exactly one synchronous audit row")
			if seam.createsMemory {
				require.Equal(t, memoriesBefore+1, memoriesAfter)
				require.NotNil(t, storedCandidate.PromotedMemoryID)
			} else {
				require.Equal(t, memoriesBefore, memoriesAfter)
				require.Nil(t, storedCandidate.PromotedMemoryID)
			}
		})
	}
}

func TestCandidateStore_AllCandidateReviewSnapshotSeamsRejectTimestampDurationOverflowWithoutWrites(t *testing.T) {
	db := openCandidateTestDB(t)
	ctx := context.Background()
	auditStore := NewAuditStore(db)
	candidateStore := NewCandidateStore(db, auditStore)
	snapshotStore := NewSnapshotStore(db)

	for _, seam := range candidateReviewSnapshotSeamCases() {
		seam := seam
		t.Run(seam.name, func(t *testing.T) {
			candidate := createCandidateReviewStoreTestCandidate(t, candidateStore, ctx, "timestamp-overflow-"+seam.name)
			actor := "agent/tester"
			snapshot := newCandidateReviewStoreTestSnapshot(t, candidate, seam.action, actor)

			entries := candidateReviewSnapshotEntries(t, snapshot)
			key := fmt.Sprintf("candidate:%d", candidate.ID)
			entry := entries[key]
			var forgedCandidate models.CrystallizationCandidate
			require.NoError(t, json.Unmarshal(entry.Before, &forgedCandidate))
			forgedCandidate.CreatedAt = time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC)
			forgedCandidate.UpdatedAt = forgedCandidate.CreatedAt
			var err error
			entry.Before, err = json.Marshal(&forgedCandidate)
			require.NoError(t, err)
			entries[key] = entry
			setCandidateReviewSnapshotEntries(t, snapshot, entries)

			persistedBefore, err := candidateStore.Get(ctx, candidate.ID)
			require.NoError(t, err)
			memoriesBefore, snapshotsBefore := countCandidateReviewTestRows(t, db)
			auditsBefore := countAuditRows(t, db, "candidate_review")

			err = callCandidateReviewSnapshotSeam(ctx, candidateStore, snapshotStore, seam, candidate, snapshot, actor)

			memoriesAfter, snapshotsAfter := countCandidateReviewTestRows(t, db)
			auditsAfter := countAuditRows(t, db, "candidate_review")
			storedCandidate, getErr := candidateStore.Get(ctx, candidate.ID)
			require.NoError(t, getErr)

			require.Error(t, err, "timestamp values outside time.Duration range must fail closed")
			require.Equal(t, persistedBefore, storedCandidate, "rejected snapshot must leave the candidate unchanged")
			require.Equal(t, memoriesBefore, memoriesAfter, "rejected snapshot must not create memory rows")
			require.Equal(t, snapshotsBefore, snapshotsAfter, "rejected snapshot must not create snapshot rows")
			require.Equal(t, auditsBefore, auditsAfter, "rejected snapshot must not create candidate_review audit rows")
		})
	}
}

type candidateReviewInvalidInitialSnapshotCase struct {
	name   string
	mutate func(*models.BulkOpSnapshot)
}

func candidateReviewInvalidInitialSnapshotCases() []candidateReviewInvalidInitialSnapshotCase {
	return []candidateReviewInvalidInitialSnapshotCase{
		{name: "empty_status", mutate: func(snapshot *models.BulkOpSnapshot) {
			snapshot.Status = ""
		}},
		{name: "preview_status", mutate: func(snapshot *models.BulkOpSnapshot) {
			snapshot.Status = models.SnapshotStatusPreview
		}},
		{name: "rolled_back_status", mutate: func(snapshot *models.BulkOpSnapshot) {
			snapshot.Status = models.SnapshotStatusRolledBack
		}},
		{name: "rolled_back_at_set", mutate: func(snapshot *models.BulkOpSnapshot) {
			rolledBackAt := time.Now().UTC().Add(-time.Minute)
			snapshot.RolledBackAt = &rolledBackAt
		}},
		{name: "pinned", mutate: func(snapshot *models.BulkOpSnapshot) {
			snapshot.Pinned = true
		}},
		{name: "zero_created_at", mutate: func(snapshot *models.BulkOpSnapshot) {
			snapshot.CreatedAt = time.Time{}
		}},
		{name: "persisted_id", mutate: func(snapshot *models.BulkOpSnapshot) {
			snapshot.ID = 99
		}},
		{name: "blank_snapshot_id", mutate: func(snapshot *models.BulkOpSnapshot) {
			snapshot.SnapshotID = "   "
		}},
	}
}

func TestCandidateStore_CandidateReviewInitialSnapshotShapeIsRejectedAtEveryValidationBoundary(t *testing.T) {
	ctx := context.Background()

	t.Run("before_database_access", func(t *testing.T) {
		candidate, err := models.NewCrystallizationCandidate(
			"session-initial-shape-preflight",
			"candidate snapshot shape must fail before database access",
			"rule",
			models.CandidateOptions{AffectedProjects: []string{"test-project"}},
		)
		require.NoError(t, err)
		candidate.ID = 42
		candidateStore := NewCandidateStore(nil, NewAuditStore(nil))
		snapshotStore := NewSnapshotStore(nil)

		for _, seam := range candidateReviewSnapshotSeamCases() {
			seam := seam
			for _, invalidCase := range candidateReviewInvalidInitialSnapshotCases() {
				invalidCase := invalidCase
				t.Run(seam.name+"/"+invalidCase.name, func(t *testing.T) {
					actor := "agent/tester"
					snapshot := newCandidateReviewStoreTestSnapshot(t, candidate, seam.action, actor)
					invalidCase.mutate(snapshot)
					var callErr error
					require.NotPanics(t, func() {
						callErr = callCandidateReviewSnapshotSeam(ctx, candidateStore, snapshotStore, seam, candidate, snapshot, actor)
					}, "invalid initial snapshot shape must be rejected before dereferencing the database")
					require.Error(t, callErr)
				})
			}
		}
	})

	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	candidateStore := NewCandidateStore(db, auditStore)
	snapshotStore := NewSnapshotStore(db)

	t.Run("public_seams_without_writes", func(t *testing.T) {
		for _, seam := range candidateReviewSnapshotSeamCases() {
			seam := seam
			for _, invalidCase := range candidateReviewInvalidInitialSnapshotCases() {
				invalidCase := invalidCase
				t.Run(seam.name+"/"+invalidCase.name, func(t *testing.T) {
					candidate := createCandidateReviewStoreTestCandidate(t, candidateStore, ctx, "initial-shape-"+seam.name+"-"+invalidCase.name)
					actor := "agent/tester"
					snapshot := newCandidateReviewStoreTestSnapshot(t, candidate, seam.action, actor)
					invalidCase.mutate(snapshot)

					persistedBefore, err := candidateStore.Get(ctx, candidate.ID)
					require.NoError(t, err)
					memoriesBefore, snapshotsBefore := countCandidateReviewTestRows(t, db)
					auditsBefore := countAuditRows(t, db, "candidate_review")

					err = callCandidateReviewSnapshotSeam(ctx, candidateStore, snapshotStore, seam, candidate, snapshot, actor)

					memoriesAfter, snapshotsAfter := countCandidateReviewTestRows(t, db)
					auditsAfter := countAuditRows(t, db, "candidate_review")
					storedCandidate, getErr := candidateStore.Get(ctx, candidate.ID)
					require.NoError(t, getErr)

					require.Error(t, err, "non-canonical initial candidate-review snapshots must fail closed")
					require.Equal(t, persistedBefore, storedCandidate, "rejected snapshot must leave the candidate unchanged")
					require.Equal(t, memoriesBefore, memoriesAfter, "rejected snapshot must not create memory rows")
					require.Equal(t, snapshotsBefore, snapshotsAfter, "rejected snapshot must not create snapshot rows")
					require.Equal(t, auditsBefore, auditsAfter, "rejected snapshot must not create candidate_review audit rows")
				})
			}
		}
	})

	t.Run("transaction_revalidation", func(t *testing.T) {
		for _, seam := range candidateReviewSnapshotSeamCases() {
			seam := seam
			for _, invalidCase := range candidateReviewInvalidInitialSnapshotCases() {
				invalidCase := invalidCase
				t.Run(seam.name+"/"+invalidCase.name, func(t *testing.T) {
					candidate := createCandidateReviewStoreTestCandidate(t, candidateStore, ctx, "tx-revalidation-"+seam.name+"-"+invalidCase.name)
					actor := "agent/tester"
					snapshot := newCandidateReviewStoreTestSnapshot(t, candidate, seam.action, actor)
					operation := seam.action + "_with_snapshot"
					require.NoError(t, candidateStore.validateCandidateReviewSnapshotBinding(ctx, nil, snapshotStore, snapshot, seam.action, candidate.ID, actor, operation))
					invalidCase.mutate(snapshot)

					persistedBefore, err := candidateStore.Get(ctx, candidate.ID)
					require.NoError(t, err)
					memoriesBefore, snapshotsBefore := countCandidateReviewTestRows(t, db)
					auditsBefore := countAuditRows(t, db, "candidate_review")

					err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
						return candidateStore.validateCandidateReviewSnapshotBinding(ctx, tx, snapshotStore, snapshot, seam.action, candidate.ID, actor, operation)
					})

					memoriesAfter, snapshotsAfter := countCandidateReviewTestRows(t, db)
					auditsAfter := countAuditRows(t, db, "candidate_review")
					storedCandidate, getErr := candidateStore.Get(ctx, candidate.ID)
					require.NoError(t, getErr)

					require.Error(t, err, "transaction-bound validation must reject shape drift after preflight")
					require.Equal(t, persistedBefore, storedCandidate, "transaction revalidation must leave the candidate unchanged")
					require.Equal(t, memoriesBefore, memoriesAfter)
					require.Equal(t, snapshotsBefore, snapshotsAfter)
					require.Equal(t, auditsBefore, auditsAfter)
				})
			}
		}
	})
}

func createCandidateReviewStoreTestCandidate(t *testing.T, cs *CandidateStore, ctx context.Context, suffix string) *models.CrystallizationCandidate {
	t.Helper()
	candidate, err := models.NewCrystallizationCandidate(
		fmt.Sprintf("session-candidate-review-%s-%d", suffix, time.Now().UnixNano()),
		"content for candidate review transaction test",
		"rule",
		models.CandidateOptions{AffectedProjects: []string{"test-project"}},
	)
	require.NoError(t, err)
	candidate.PrivacyScope = "project"
	created, err := cs.Create(ctx, candidate)
	require.NoError(t, err)
	return created
}

func newCandidateReviewStoreTestSnapshot(t *testing.T, candidate *models.CrystallizationCandidate, action string, actor string) *models.BulkOpSnapshot {
	t.Helper()
	snapshot, err := reviewpacket.NewCandidateReviewActionSnapshot(action, candidate, actor)
	require.NoError(t, err)
	return snapshot
}

// containsStr is a helper for checking error message content without importing strings.
func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
