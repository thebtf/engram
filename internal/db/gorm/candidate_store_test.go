package gorm

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

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

	// Build a valid candidate.
	candidate, err := models.NewCrystallizationCandidate(
		"session-roundtrip",
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
		name      string
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
			// Create a fresh pending candidate for each sub-test.
			candidate, err := models.NewCrystallizationCandidate(
				"session-sm-"+tc.name,
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

	candidate, err := models.NewCrystallizationCandidate("session-illegal", "content", "rule", models.CandidateOptions{})
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
	candidate, err := models.NewCrystallizationCandidate("session-race", "race content", "rule", models.CandidateOptions{})
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

	c, err := models.NewCrystallizationCandidate("session-fp", "idempotent content", "rule", models.CandidateOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, c.Fingerprint, "fingerprint must be non-empty for session+content pair")

	// First insert succeeds.
	_, err = cs.Create(ctx, c)
	require.NoError(t, err)

	// Second insert with same fingerprint must fail (unique constraint on pending+fingerprint).
	c2, _ := models.NewCrystallizationCandidate("session-fp", "idempotent content", "rule", models.CandidateOptions{})
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

	candidate, err := models.NewCrystallizationCandidate("session-audit", "content for audit", "rule", models.CandidateOptions{})
	require.NoError(t, err)
	created, err := cs.Create(ctx, candidate)
	require.NoError(t, err)

	beforeCount := countAuditRows(t, db, "decay_candidate")
	_, err = cs.TransitionToDecayed(ctx, created.ID)
	require.NoError(t, err)

	afterCount := countAuditRows(t, db, "decay_candidate")
	require.Greater(t, afterCount, beforeCount, "audit_log must have a new decay_candidate entry")
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
	expired, err := models.NewCrystallizationCandidate("session-decay", "expired content", "rule", models.CandidateOptions{})
	require.NoError(t, err)
	expired.ReviewAfter = &past

	createdExpired, err := cs.Create(ctx, expired)
	require.NoError(t, err)

	// Insert a candidate whose review_after is in the future.
	future, err := models.NewCrystallizationCandidate("session-future", "future content", "rule", models.CandidateOptions{})
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
		"session-reject-preserve",
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
		"session-promote-rollback",
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

// containsStr is a helper for checking error message content without importing strings.
func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
