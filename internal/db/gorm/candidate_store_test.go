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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/pkg/models"
)

const candidateTestDBApplicationName = "engram_candidate_store_test"

// openCandidateTestDB opens a test PostgreSQL connection or skips the test.
func openCandidateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err, "parse test DB config")
	if config.RuntimeParams == nil {
		config.RuntimeParams = make(map[string]string)
	}
	config.RuntimeParams["application_name"] = candidateTestDBApplicationName

	sqlDB := stdlib.OpenDB(*config)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close candidate test DB pool: %v", err)
		}
	})

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
	require.NoError(t, err, "open test DB")
	require.NoError(t, sqlDB.Ping())

	// Ensure migration chain is applied.
	require.NoError(t, runMigrations(db), "runMigrations")
	return db
}

// TestOpenCandidateTestDB_SubtestOwnerClosesPoolWithoutPrematureClose protects
// the test-process connection budget. Each child owns the pool it opens: the
// pool must remain usable for the child's body, then disappear before t.Run
// returns control to the parent.
func TestOpenCandidateTestDB_SubtestOwnerClosesPoolWithoutPrematureClose(t *testing.T) {
	observer := openCandidateTestDB(t)
	observerSQL, err := observer.DB()
	require.NoError(t, err, "resolve observer SQL pool")
	observerSQL.SetMaxIdleConns(0)
	baseline := candidateTestDBOtherSessionCount(t, observer)

	for i := 0; i < 4; i++ {
		t.Run(fmt.Sprintf("owner-%d", i), func(t *testing.T) {
			child := openCandidateTestDB(t)

			var one int
			require.NoError(t, child.Raw("SELECT 1").Scan(&one).Error)
			require.Equal(t, 1, one, "owner pool must remain usable during the subtest")

			live := candidateTestDBOtherSessionCount(t, observer)
			t.Logf("baseline_sessions=%d child_live_sessions=%d", baseline, live)
			require.Greater(t, live, baseline, "child pool must be observable before owner cleanup")
		})

		deadline := time.Now().Add(time.Second)
		after := candidateTestDBOtherSessionCount(t, observer)
		for after > baseline && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
			after = candidateTestDBOtherSessionCount(t, observer)
		}
		t.Logf("baseline_sessions=%d sessions_after_owner_cleanup=%d", baseline, after)
		if after > baseline {
			t.Errorf("child pool leaked past owner cleanup: baseline=%d after=%d", baseline, after)
		}
	}
}

func candidateTestDBOtherSessionCount(t *testing.T, observer *gorm.DB) int64 {
	t.Helper()

	var count int64
	require.NoError(t, observer.Raw(`
		SELECT count(*)
		FROM pg_stat_activity
		WHERE datname = current_database()
		  AND pid <> pg_backend_pid()
		  AND application_name = ?
	`, candidateTestDBApplicationName).Scan(&count).Error)
	return count
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

	snapshot, err := models.NewBulkOpSnapshot(
		fmt.Sprintf("candidate-promote-amend-failure-%d", time.Now().UnixNano()),
		models.SnapshotOpCandidateReviewAction,
		"system",
		json.RawMessage(`{}`),
	)
	require.NoError(t, err)
	snapshot.SourceSessionID = createdCandidate.SourceSessionID

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
	snapshot, err := models.NewBulkOpSnapshot(
		fmt.Sprintf("candidate-review-%s-%d", action, time.Now().UnixNano()),
		models.SnapshotOpCandidateReviewAction,
		actor,
		json.RawMessage(`{}`),
	)
	require.NoError(t, err)
	snapshot.SourceSessionID = candidate.SourceSessionID
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
