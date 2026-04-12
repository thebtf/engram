//go:build fts5

// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/pkg/models"
)

// testSessionStore creates a SessionStore with a temporary database for testing.
func testSessionStore(t *testing.T) (*SessionStore, *Store, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "gorm_session_test_*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	cfg := Config{
		Path:     dbPath,
		MaxConns: 4,
		LogLevel: logger.Silent,
	}

	store, err := NewStore(cfg)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("NewStore failed: %v", err)
	}

	sessionStore := NewSessionStore(store)

	cleanup := func() {
		store.Close()
		os.RemoveAll(tmpDir)
	}

	return sessionStore, store, cleanup
}

func TestSessionStore_CreateSDKSession(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create a new session
	id, err := sessionStore.CreateSDKSession(ctx, "claude-1", "test-project", "initial prompt")
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	// Retrieve and verify
	sess, err := sessionStore.GetSessionByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "claude-1", sess.ClaudeSessionID)
	assert.Equal(t, "test-project", sess.Project)
	assert.Equal(t, models.SessionStatusActive, sess.Status)
	assert.True(t, sess.UserPrompt.Valid)
	assert.Equal(t, "initial prompt", sess.UserPrompt.String)
}

func TestSessionStore_CreateSDKSession_Idempotent(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create first session
	id1, err := sessionStore.CreateSDKSession(ctx, "claude-1", "project-a", "prompt 1")
	require.NoError(t, err)

	// Create again with same claude_session_id but different project
	id2, err := sessionStore.CreateSDKSession(ctx, "claude-1", "project-b", "prompt 2")
	require.NoError(t, err)

	// Should return same ID (idempotent)
	assert.Equal(t, id1, id2)

	// Should have updated project to project-b
	sess, err := sessionStore.GetSessionByID(ctx, id1)
	require.NoError(t, err)
	assert.Equal(t, "project-b", sess.Project)
	assert.Equal(t, "prompt 2", sess.UserPrompt.String)
}

func TestSessionStore_CreateSDKSession_EmptyPrompt(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create session with empty prompt
	id, err := sessionStore.CreateSDKSession(ctx, "claude-2", "test-project", "")
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	// Verify prompt is NULL
	sess, err := sessionStore.GetSessionByID(ctx, id)
	require.NoError(t, err)
	assert.False(t, sess.UserPrompt.Valid)
}

func TestSessionStore_FindAnySDKSession(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create a session
	_, err := sessionStore.CreateSDKSession(ctx, "claude-1", "test-project", "")
	require.NoError(t, err)

	// Find it
	sess, err := sessionStore.FindAnySDKSession(ctx, "claude-1")
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "claude-1", sess.ClaudeSessionID)

	// Try to find non-existent
	sess, err = sessionStore.FindAnySDKSession(ctx, "claude-nonexistent")
	require.NoError(t, err)
	assert.Nil(t, sess)
}

func TestSessionStore_GetSessionByID_NotFound(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()

	// Try to get non-existent session
	sess, err := sessionStore.GetSessionByID(ctx, 99999)
	require.NoError(t, err)
	assert.Nil(t, sess)
}

func TestSessionStore_IncrementPromptCounter(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create a session
	id, err := sessionStore.CreateSDKSession(ctx, "claude-1", "test-project", "")
	require.NoError(t, err)

	// Initial counter should be 0
	counter, err := sessionStore.GetPromptCounter(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 0, counter)

	// Increment
	counter, err = sessionStore.IncrementPromptCounter(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 1, counter)

	// Increment again
	counter, err = sessionStore.IncrementPromptCounter(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 2, counter)

	// Verify via GetPromptCounter
	counter, err = sessionStore.GetPromptCounter(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 2, counter)
}

func TestSessionStore_GetSessionsToday(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()

	// Initially should be 0
	count, err := sessionStore.GetSessionsToday(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Create some sessions
	_, err = sessionStore.CreateSDKSession(ctx, "claude-1", "project-1", "")
	require.NoError(t, err)

	_, err = sessionStore.CreateSDKSession(ctx, "claude-2", "project-2", "")
	require.NoError(t, err)

	// Should now have 2 sessions today
	count, err = sessionStore.GetSessionsToday(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestSessionStore_GetAllProjects(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()

	// Initially should be empty
	projects, err := sessionStore.GetAllProjects(ctx)
	require.NoError(t, err)
	assert.Empty(t, projects)

	// Create sessions with different projects
	_, err = sessionStore.CreateSDKSession(ctx, "claude-1", "project-a", "")
	require.NoError(t, err)

	_, err = sessionStore.CreateSDKSession(ctx, "claude-2", "project-b", "")
	require.NoError(t, err)

	_, err = sessionStore.CreateSDKSession(ctx, "claude-3", "project-a", "") // Duplicate project
	require.NoError(t, err)

	// Should get distinct projects in alphabetical order
	projects, err = sessionStore.GetAllProjects(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"project-a", "project-b"}, projects)
}

func TestSessionStore_UpdateSessionOutcome_ByClaudeSessionID(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()
	id, err := sessionStore.CreateSDKSession(ctx, "claude-outcome-1", "test-project", "prompt")
	require.NoError(t, err)

	err = sessionStore.UpdateSessionOutcome(ctx, "claude-outcome-1", "success", "done")
	require.NoError(t, err)

	sess, err := sessionStore.GetSessionByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.True(t, sess.Outcome.Valid)
	assert.Equal(t, "success", sess.Outcome.String)
	require.True(t, sess.OutcomeReason.Valid)
	assert.Equal(t, "done", sess.OutcomeReason.String)
	require.True(t, sess.OutcomeRecordedAt.Valid)
	assert.NotEmpty(t, sess.OutcomeRecordedAt.String)
}

func TestSessionStore_UpdateSessionOutcome_ByNumericDBIDString(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()
	id, err := sessionStore.CreateSDKSession(ctx, "claude-outcome-2", "test-project", "prompt")
	require.NoError(t, err)

	err = sessionStore.UpdateSessionOutcome(ctx, strconv.FormatInt(id, 10), "partial", "some progress")
	require.NoError(t, err)

	canonicalID, err := sessionStore.ResolveClaudeSessionID(ctx, strconv.FormatInt(id, 10))
	require.NoError(t, err)
	assert.Equal(t, "claude-outcome-2", canonicalID)

	sess, err := sessionStore.GetSessionByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.True(t, sess.Outcome.Valid)
	assert.Equal(t, "partial", sess.Outcome.String)
	require.True(t, sess.OutcomeReason.Valid)
	assert.Equal(t, "some progress", sess.OutcomeReason.String)
}

func TestSessionStore_UpdateSessionOutcome_AutoCreatesMissingClaudeSession(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()
	const claudeSessionID = "claude-missing-1"

	err := sessionStore.UpdateSessionOutcome(ctx, claudeSessionID, "failure", "init missing")
	require.NoError(t, err)

	sess, err := sessionStore.FindAnySDKSession(ctx, claudeSessionID)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, claudeSessionID, sess.ClaudeSessionID)
	assert.Equal(t, "", sess.Project)
	assert.False(t, sess.UserPrompt.Valid)
	require.True(t, sess.Outcome.Valid)
	assert.Equal(t, "failure", sess.Outcome.String)
	require.True(t, sess.OutcomeReason.Valid)
	assert.Equal(t, "init missing", sess.OutcomeReason.String)
	require.True(t, sess.OutcomeRecordedAt.Valid)
}

func TestSessionStore_UpdateSessionOutcome_AutoCreateConcurrentFirstWrite(t *testing.T) {
	sessionStore, store, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()
	const claudeSessionID = "claude-missing-race-1"

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- sessionStore.UpdateSessionOutcome(ctx, claudeSessionID, "success", "race")
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	var rows []SDKSession
	err := store.DB.WithContext(ctx).
		Where("claude_session_id = ?", claudeSessionID).
		Find(&rows).Error
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Outcome.Valid)
	assert.Equal(t, "success", rows[0].Outcome.String)
}

func TestSessionStore_UpdateSessionOutcome_IdempotentRepeatedWrite(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()
	id, err := sessionStore.CreateSDKSession(ctx, "claude-outcome-3", "test-project", "prompt")
	require.NoError(t, err)

	err = sessionStore.UpdateSessionOutcome(ctx, "claude-outcome-3", "success", "first reason")
	require.NoError(t, err)
	err = sessionStore.UpdateSessionOutcome(ctx, "claude-outcome-3", "success", "second reason ignored")
	require.NoError(t, err)

	sess, err := sessionStore.GetSessionByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.True(t, sess.Outcome.Valid)
	assert.Equal(t, "success", sess.Outcome.String)
	// Idempotent repeated write should not overwrite original reason.
	require.True(t, sess.OutcomeReason.Valid)
	assert.Equal(t, "first reason", sess.OutcomeReason.String)
}

func TestSessionStore_UpdateSessionOutcome_ConflictingSecondOutcome(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()
	_, err := sessionStore.CreateSDKSession(ctx, "claude-outcome-4", "test-project", "prompt")
	require.NoError(t, err)

	err = sessionStore.UpdateSessionOutcome(ctx, "claude-outcome-4", "partial", "first")
	require.NoError(t, err)

	err = sessionStore.UpdateSessionOutcome(ctx, "claude-outcome-4", "success", "conflict")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSessionOutcomeConflict), "expected ErrSessionOutcomeConflict, got %v", err)
	assert.Contains(t, err.Error(), "existing=partial")
	assert.Contains(t, err.Error(), "requested=success")
}

func TestSessionStore_SessionFields(t *testing.T) {
	sessionStore, _, cleanup := testSessionStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create a session
	id, err := sessionStore.CreateSDKSession(ctx, "claude-1", "test-project", "test prompt")
	require.NoError(t, err)

	// Retrieve and verify all fields
	sess, err := sessionStore.GetSessionByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, sess)

	// Verify all fields
	assert.Equal(t, id, sess.ID)
	assert.Equal(t, "claude-1", sess.ClaudeSessionID)
	assert.True(t, sess.SDKSessionID.Valid)
	assert.Equal(t, "claude-1", sess.SDKSessionID.String) // Should be same as ClaudeSessionID
	assert.Equal(t, "test-project", sess.Project)
	assert.True(t, sess.UserPrompt.Valid)
	assert.Equal(t, "test prompt", sess.UserPrompt.String)
	assert.Equal(t, int64(0), sess.PromptCounter)
	assert.Equal(t, models.SessionStatusActive, sess.Status)
	assert.NotEmpty(t, sess.StartedAt)
	assert.Greater(t, sess.StartedAtEpoch, int64(0))
	assert.False(t, sess.CompletedAt.Valid)      // Should be NULL
	assert.False(t, sess.CompletedAtEpoch.Valid) // Should be NULL
}
