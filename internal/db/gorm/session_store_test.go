// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/pkg/models"
)

// openSessionTestDB opens a test PostgreSQL connection for SessionStore tests.
// Skips the test when DATABASE_DSN is not set.
func openSessionTestDB(t *testing.T) (*SessionStore, *gorm.DB, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping session store integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "open test DB")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
	require.NoError(t, runMigrations(db), "runMigrations")

	ss := &SessionStore{db: db}
	cleanup := func() { sqlDB.Close() }
	return ss, db, cleanup
}

// TestSessionStore_CreateSDKSession verifies a new session is created and retrievable.
func TestSessionStore_CreateSDKSession(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-create-01'")

	ctx := context.Background()

	id, err := ss.CreateSDKSession(ctx, "sst-create-01", "test-project", "initial prompt")
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	sess, err := ss.GetSessionByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "sst-create-01", sess.ClaudeSessionID)
	assert.Equal(t, "test-project", sess.Project)
	assert.Equal(t, models.SessionStatusActive, sess.Status)
	assert.True(t, sess.UserPrompt.Valid)
	assert.Equal(t, "initial prompt", sess.UserPrompt.String)
}

// TestSessionStore_CreateSDKSession_Idempotent verifies calling twice with the same
// claude_session_id returns the same DB ID and updates project/user_prompt.
func TestSessionStore_CreateSDKSession_Idempotent(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-idempotent-01'")

	ctx := context.Background()

	id1, err := ss.CreateSDKSession(ctx, "sst-idempotent-01", "project-a", "prompt 1")
	require.NoError(t, err)

	id2, err := ss.CreateSDKSession(ctx, "sst-idempotent-01", "project-b", "prompt 2")
	require.NoError(t, err)
	assert.Equal(t, id1, id2, "idempotent call must return the same ID")

	// Project should be updated to "project-b".
	sess, err := ss.GetSessionByID(ctx, id1)
	require.NoError(t, err)
	assert.Equal(t, "project-b", sess.Project)
	assert.Equal(t, "prompt 2", sess.UserPrompt.String)
}

// TestSessionStore_CreateSDKSession_EmptyPrompt verifies that an empty prompt
// is stored as a NULL user_prompt.
func TestSessionStore_CreateSDKSession_EmptyPrompt(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-noprompt-01'")

	ctx := context.Background()

	id, err := ss.CreateSDKSession(ctx, "sst-noprompt-01", "test-project", "")
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	sess, err := ss.GetSessionByID(ctx, id)
	require.NoError(t, err)
	assert.False(t, sess.UserPrompt.Valid, "empty prompt must be stored as NULL")
}

// TestSessionStore_FindAnySDKSession verifies lookup by claude_session_id.
func TestSessionStore_FindAnySDKSession(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-find-01'")

	ctx := context.Background()
	_, err := ss.CreateSDKSession(ctx, "sst-find-01", "test-project", "")
	require.NoError(t, err)

	sess, err := ss.FindAnySDKSession(ctx, "sst-find-01")
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "sst-find-01", sess.ClaudeSessionID)

	missing, err := ss.FindAnySDKSession(ctx, "sst-nonexistent-999")
	require.NoError(t, err)
	assert.Nil(t, missing, "not-found must return nil without error")
}

// TestSessionStore_GetSessionByID_NotFound verifies that a missing ID returns nil.
func TestSessionStore_GetSessionByID_NotFound(t *testing.T) {
	ss, _, cleanup := openSessionTestDB(t)
	defer cleanup()

	ctx := context.Background()
	sess, err := ss.GetSessionByID(ctx, 9999999999)
	require.NoError(t, err)
	assert.Nil(t, sess)
}

// TestSessionStore_IncrementPromptCounter verifies counter increments atomically.
func TestSessionStore_IncrementPromptCounter(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-counter-01'")

	ctx := context.Background()
	id, err := ss.CreateSDKSession(ctx, "sst-counter-01", "test-project", "")
	require.NoError(t, err)

	counter, err := ss.GetPromptCounter(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 0, counter, "initial counter must be 0")

	counter, err = ss.IncrementPromptCounter(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 1, counter)

	counter, err = ss.IncrementPromptCounter(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 2, counter)

	counter, err = ss.GetPromptCounter(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 2, counter)
}

// TestSessionStore_GetSessionsToday verifies today's session count.
func TestSessionStore_GetSessionsToday(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id IN ('sst-today-01', 'sst-today-02')")

	ctx := context.Background()
	before, err := ss.GetSessionsToday(ctx)
	require.NoError(t, err)

	_, _ = ss.CreateSDKSession(ctx, "sst-today-01", "project-1", "")
	_, _ = ss.CreateSDKSession(ctx, "sst-today-02", "project-2", "")

	after, err := ss.GetSessionsToday(ctx)
	require.NoError(t, err)
	assert.Equal(t, before+2, after)
}

// TestSessionStore_GetAllProjects verifies distinct project listing.
func TestSessionStore_GetAllProjects(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id IN ('sst-proj-01', 'sst-proj-02', 'sst-proj-03')")

	ctx := context.Background()

	_, _ = ss.CreateSDKSession(ctx, "sst-proj-01", "alpha-project", "")
	_, _ = ss.CreateSDKSession(ctx, "sst-proj-02", "beta-project", "")
	_, _ = ss.CreateSDKSession(ctx, "sst-proj-03", "alpha-project", "") // duplicate

	projects, err := ss.GetAllProjects(ctx)
	require.NoError(t, err)

	// alpha-project and beta-project must be present; duplicates collapsed.
	has := func(s string) bool {
		for _, p := range projects {
			if p == s {
				return true
			}
		}
		return false
	}
	assert.True(t, has("alpha-project"))
	assert.True(t, has("beta-project"))
}

// TestSessionStore_UpdateSessionOutcome_ByClaudeSessionID verifies recording outcome via claude_session_id.
func TestSessionStore_UpdateSessionOutcome_ByClaudeSessionID(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-outcome-01'")

	ctx := context.Background()
	id, err := ss.CreateSDKSession(ctx, "sst-outcome-01", "test-project", "prompt")
	require.NoError(t, err)

	err = ss.UpdateSessionOutcome(ctx, "sst-outcome-01", "success", "done")
	require.NoError(t, err)

	sess, err := ss.GetSessionByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.True(t, sess.Outcome.Valid)
	assert.Equal(t, "success", sess.Outcome.String)
	require.True(t, sess.OutcomeReason.Valid)
	assert.Equal(t, "done", sess.OutcomeReason.String)
	require.True(t, sess.OutcomeRecordedAt.Valid)
	assert.NotEmpty(t, sess.OutcomeRecordedAt.String)
}

// TestSessionStore_UpdateSessionOutcome_ByNumericDBIDString verifies recording outcome via numeric ID.
func TestSessionStore_UpdateSessionOutcome_ByNumericDBIDString(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-outcome-02'")

	ctx := context.Background()
	id, err := ss.CreateSDKSession(ctx, "sst-outcome-02", "test-project", "prompt")
	require.NoError(t, err)

	err = ss.UpdateSessionOutcome(ctx, strconv.FormatInt(id, 10), "partial", "some progress")
	require.NoError(t, err)

	canonicalID, err := ss.ResolveClaudeSessionID(ctx, strconv.FormatInt(id, 10))
	require.NoError(t, err)
	assert.Equal(t, "sst-outcome-02", canonicalID)

	sess, err := ss.GetSessionByID(ctx, id)
	require.NoError(t, err)
	require.True(t, sess.Outcome.Valid)
	assert.Equal(t, "partial", sess.Outcome.String)
}

// TestSessionStore_UpdateSessionOutcome_AutoCreatesMissingClaudeSession verifies
// that recording an outcome for an unknown claude_session_id auto-creates the row.
func TestSessionStore_UpdateSessionOutcome_AutoCreatesMissingClaudeSession(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-missing-01'")

	ctx := context.Background()

	err := ss.UpdateSessionOutcome(ctx, "sst-missing-01", "failure", "init missing")
	require.NoError(t, err)

	sess, err := ss.FindAnySDKSession(ctx, "sst-missing-01")
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "sst-missing-01", sess.ClaudeSessionID)
	assert.Equal(t, "", sess.Project)
	assert.False(t, sess.UserPrompt.Valid)
	require.True(t, sess.Outcome.Valid)
	assert.Equal(t, "failure", sess.Outcome.String)
	require.True(t, sess.OutcomeReason.Valid)
	assert.Equal(t, "init missing", sess.OutcomeReason.String)
	require.True(t, sess.OutcomeRecordedAt.Valid)
}

// TestSessionStore_UpdateSessionOutcome_AutoCreateConcurrentFirstWrite verifies that
// concurrent auto-creates for the same unknown session converge to a single row.
func TestSessionStore_UpdateSessionOutcome_AutoCreateConcurrentFirstWrite(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-race-01'")

	ctx := context.Background()

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- ss.UpdateSessionOutcome(ctx, "sst-race-01", "success", "race")
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	var rows []SDKSession
	err := db.Where("claude_session_id = ?", "sst-race-01").Find(&rows).Error
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Outcome.Valid)
	assert.Equal(t, "success", rows[0].Outcome.String)
}

// TestSessionStore_UpdateSessionOutcome_IdempotentRepeatedWrite verifies that writing
// the same outcome twice is a no-op (the second call succeeds without error).
func TestSessionStore_UpdateSessionOutcome_IdempotentRepeatedWrite(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-outcome-03'")

	ctx := context.Background()
	id, err := ss.CreateSDKSession(ctx, "sst-outcome-03", "test-project", "prompt")
	require.NoError(t, err)

	require.NoError(t, ss.UpdateSessionOutcome(ctx, "sst-outcome-03", "success", "first reason"))
	require.NoError(t, ss.UpdateSessionOutcome(ctx, "sst-outcome-03", "success", "second reason ignored"))

	sess, err := ss.GetSessionByID(ctx, id)
	require.NoError(t, err)
	require.True(t, sess.Outcome.Valid)
	assert.Equal(t, "success", sess.Outcome.String)
	// Idempotent repeated write must not overwrite the original reason.
	assert.Equal(t, "first reason", sess.OutcomeReason.String)
}

// TestSessionStore_UpdateSessionOutcome_ConflictingSecondOutcome verifies that
// writing a different outcome returns ErrSessionOutcomeConflict.
func TestSessionStore_UpdateSessionOutcome_ConflictingSecondOutcome(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-outcome-04'")

	ctx := context.Background()
	_, err := ss.CreateSDKSession(ctx, "sst-outcome-04", "test-project", "prompt")
	require.NoError(t, err)

	require.NoError(t, ss.UpdateSessionOutcome(ctx, "sst-outcome-04", "partial", "first"))

	err = ss.UpdateSessionOutcome(ctx, "sst-outcome-04", "success", "conflict")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSessionOutcomeConflict), "got: %v", err)
	assert.Contains(t, err.Error(), "existing=partial")
	assert.Contains(t, err.Error(), "requested=success")
}

// TestSessionStore_UpdateSessionOutcome_NumericIDNotFound verifies that a numeric
// ID that does not exist returns ErrSessionNotFound.
func TestSessionStore_UpdateSessionOutcome_NumericIDNotFound(t *testing.T) {
	ss, _, cleanup := openSessionTestDB(t)
	defer cleanup()

	ctx := context.Background()
	err := ss.UpdateSessionOutcome(ctx, "9999999999", "success", "should not exist")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSessionNotFound), "got: %v", err)
}

// TestSessionStore_GetOutcome verifies GetOutcome returns the recorded value.
func TestSessionStore_GetOutcome(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-getoutcome-01'")

	ctx := context.Background()

	_, err := ss.CreateSDKSession(ctx, "sst-getoutcome-01", "project", "")
	require.NoError(t, err)

	outcome, err := ss.GetOutcome(ctx, "sst-getoutcome-01")
	require.NoError(t, err)
	assert.Empty(t, outcome, "no outcome set yet")

	require.NoError(t, ss.UpdateSessionOutcome(ctx, "sst-getoutcome-01", "success", "all good"))

	outcome, err = ss.GetOutcome(ctx, "sst-getoutcome-01")
	require.NoError(t, err)
	assert.Equal(t, "success", outcome)
}

// TestSessionStore_GetOutcome_EmptySessionID verifies empty session ID returns empty string.
func TestSessionStore_GetOutcome_EmptySessionID(t *testing.T) {
	ss, _, cleanup := openSessionTestDB(t)
	defer cleanup()

	ctx := context.Background()
	outcome, err := ss.GetOutcome(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, outcome)
}

// TestSessionStore_SessionFields verifies all fields populated on a created session.
func TestSessionStore_SessionFields(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-fields-01'")

	ctx := context.Background()

	id, err := ss.CreateSDKSession(ctx, "sst-fields-01", "test-project", "test prompt")
	require.NoError(t, err)

	sess, err := ss.GetSessionByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, sess)

	assert.Equal(t, id, sess.ID)
	assert.Equal(t, "sst-fields-01", sess.ClaudeSessionID)
	assert.True(t, sess.SDKSessionID.Valid)
	assert.Equal(t, "sst-fields-01", sess.SDKSessionID.String)
	assert.Equal(t, "test-project", sess.Project)
	assert.True(t, sess.UserPrompt.Valid)
	assert.Equal(t, "test prompt", sess.UserPrompt.String)
	assert.Equal(t, int64(0), sess.PromptCounter)
	assert.Equal(t, models.SessionStatusActive, sess.Status)
	assert.NotEmpty(t, sess.StartedAt)
	assert.Greater(t, sess.StartedAtEpoch, int64(0))
	assert.False(t, sess.CompletedAt.Valid)
	assert.False(t, sess.CompletedAtEpoch.Valid)
}

// TestSessionStore_ListSDKSessions verifies paginated listing.
func TestSessionStore_ListSDKSessions(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id IN ('sst-list-01', 'sst-list-02', 'sst-list-03')")

	ctx := context.Background()
	_, _ = ss.CreateSDKSession(ctx, "sst-list-01", "list-project", "")
	_, _ = ss.CreateSDKSession(ctx, "sst-list-02", "list-project", "")
	_, _ = ss.CreateSDKSession(ctx, "sst-list-03", "other-project", "")

	// Project filter
	sessions, total, err := ss.ListSDKSessions(ctx, "list-project", 10, 0, 0, 0, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, total, int64(2))
	for _, s := range sessions {
		assert.Equal(t, "list-project", s.Project)
	}

	// No filter
	_, totalAll, err := ss.ListSDKSessions(ctx, "", 100, 0, 0, 0, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, totalAll, int64(3))
}

// TestSessionStore_UpdateInjectionStrategy verifies strategy field update.
func TestSessionStore_UpdateInjectionStrategy(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-strategy-01'")

	ctx := context.Background()
	id, err := ss.CreateSDKSession(ctx, "sst-strategy-01", "test-project", "")
	require.NoError(t, err)

	err = ss.UpdateInjectionStrategy(ctx, "sst-strategy-01", "unified")
	require.NoError(t, err)

	sess, err := ss.GetSessionByID(ctx, id)
	require.NoError(t, err)
	assert.True(t, sess.InjectionStrategy.Valid)
	assert.Equal(t, "unified", sess.InjectionStrategy.String)
}

// TestSessionStore_UpdateUtilityPropagatedAt verifies propagation timestamp update.
func TestSessionStore_UpdateUtilityPropagatedAt(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-util-01'")

	ctx := context.Background()
	_, err := ss.CreateSDKSession(ctx, "sst-util-01", "test-project", "")
	require.NoError(t, err)

	err = ss.UpdateUtilityPropagatedAt(ctx, "sst-util-01")
	require.NoError(t, err)

	// Non-existent session should return an error.
	err = ss.UpdateUtilityPropagatedAt(ctx, "sst-nonexistent-util")
	require.Error(t, err)
}

// TestSessionStore_UpdateUtilityPropagatedAtIfStale verifies atomic TOCTOU-free claim.
func TestSessionStore_UpdateUtilityPropagatedAtIfStale(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-stale-01'")

	ctx := context.Background()
	_, err := ss.CreateSDKSession(ctx, "sst-stale-01", "test-project", "")
	require.NoError(t, err)

	// First call: session has no propagation timestamp → should claim.
	claimed, err := ss.UpdateUtilityPropagatedAtIfStale(ctx, "sst-stale-01")
	require.NoError(t, err)
	assert.True(t, claimed, "first call must claim the propagation slot")

	// Immediate second call: now rate-limited → should not claim.
	claimed, err = ss.UpdateUtilityPropagatedAtIfStale(ctx, "sst-stale-01")
	require.NoError(t, err)
	assert.False(t, claimed, "second immediate call must be rate-limited")
}

// TestSessionStore_ClearUtilityPropagatedAt verifies reset to NULL.
func TestSessionStore_ClearUtilityPropagatedAt(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-clear-util-01'")

	ctx := context.Background()
	_, err := ss.CreateSDKSession(ctx, "sst-clear-util-01", "test-project", "")
	require.NoError(t, err)

	require.NoError(t, ss.UpdateUtilityPropagatedAt(ctx, "sst-clear-util-01"))
	require.NoError(t, ss.ClearUtilityPropagatedAt(ctx, "sst-clear-util-01"))

	// After clearing, UpdateUtilityPropagatedAtIfStale must claim again.
	claimed, err := ss.UpdateUtilityPropagatedAtIfStale(ctx, "sst-clear-util-01")
	require.NoError(t, err)
	assert.True(t, claimed, "after ClearUtilityPropagatedAt, slot should be reclaimable")
}

// TestSessionStore_GetStrategyStats verifies strategy stats aggregation.
func TestSessionStore_GetStrategyStats(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id IN ('sst-stats-01', 'sst-stats-02')")

	ctx := context.Background()
	id1, _ := ss.CreateSDKSession(ctx, "sst-stats-01", "test-project", "")
	id2, _ := ss.CreateSDKSession(ctx, "sst-stats-02", "test-project", "")
	_ = ss.UpdateInjectionStrategy(ctx, "sst-stats-01", "unified")
	_ = ss.UpdateInjectionStrategy(ctx, "sst-stats-02", "unified")
	_ = ss.UpdateSessionOutcome(ctx, "sst-stats-01", "success", "ok")
	_ = id1
	_ = id2

	stats, err := ss.GetStrategyStats(ctx)
	require.NoError(t, err)
	for _, s := range stats {
		if s.Strategy == "unified" {
			assert.GreaterOrEqual(t, s.Sessions, int64(2))
			assert.GreaterOrEqual(t, s.Successes, int64(1))
		}
	}
}

// TestSessionStore_GetLearningCurve verifies learning curve data is returned.
func TestSessionStore_GetLearningCurve(t *testing.T) {
	ss, db, cleanup := openSessionTestDB(t)
	defer cleanup()
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = 'sst-curve-01'")

	ctx := context.Background()
	_, _ = ss.CreateSDKSession(ctx, "sst-curve-01", "test-project", "")
	_ = ss.UpdateSessionOutcome(ctx, "sst-curve-01", "success", "done")

	rows, err := ss.GetLearningCurve(ctx, 30, "")
	require.NoError(t, err)
	// Result may be empty if today's row hasn't accumulated yet, but no error.
	_ = rows

	// Negative days defaults to 30.
	rows2, err := ss.GetLearningCurve(ctx, -1, "test-project")
	require.NoError(t, err)
	_ = rows2
}

// TestResolveClaudeSessionID_NotFound verifies the error wraps ErrSessionNotFound.
func TestResolveClaudeSessionID_NotFound(t *testing.T) {
	ss, _, cleanup := openSessionTestDB(t)
	defer cleanup()

	ctx := context.Background()
	_, err := ss.ResolveClaudeSessionID(ctx, "sst-resolve-nonexistent-9999")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSessionNotFound), "got: %v", err)
}
