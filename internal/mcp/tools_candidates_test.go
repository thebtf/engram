// Package mcp — unit tests for crystallization candidate MCP tools (TG4 review hardening).
package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// nonNilCandidateStore returns a zero-value *gormdb.CandidateStore so that the
// server's candidateStore != nil check passes. Tests that exercise the project-guard
// return an error before any store method is invoked, so zero-value is safe here.
func nonNilCandidateStore() *gormdb.CandidateStore {
	return &gormdb.CandidateStore{}
}

func TestRequireCandidateReviewSnapshotRejectsNil(t *testing.T) {
	for _, operation := range []string{"reject_candidate", "supersede_candidate"} {
		t.Run(operation, func(t *testing.T) {
			err := requireCandidateReviewSnapshot(operation, nil)
			require.Error(t, err, "nil snapshot must fail before snapshot-based transition")
			require.Contains(t, err.Error(), operation)
			require.Contains(t, err.Error(), "candidate review snapshot is required")
		})
	}
}

func TestRequireCandidateReviewSnapshotAllowsNonNil(t *testing.T) {
	require.NoError(t, requireCandidateReviewSnapshot("reject_candidate", &models.BulkOpSnapshot{}))
}

// TestHandleListCandidates_EmptyProjectReturnsError verifies MAJOR finding 3:
// list_candidates with an empty project must return an error matching the deny pattern
// used by other read-path tools.
func TestHandleListCandidates_EmptyProjectReturnsError(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	s := NewServer(ServerOptions{Version: "test"})
	s.candidateStore = nonNilCandidateStore()

	args, err := json.Marshal(map[string]any{
		// project intentionally omitted → empty string default
		"status": "pending",
	})
	require.NoError(t, err)

	_, callErr := s.handleListCandidates(context.Background(), args)
	require.Error(t, callErr, "empty project must return an error")
	require.True(t,
		strings.Contains(callErr.Error(), "project is required"),
		"error must mention 'project is required', got: %v", callErr)
}

// TestHandleListCandidates_FlagOffReturnsError verifies that list_candidates is unavailable
// when ENGRAM_VNEXT_F_ENABLED is not set.
func TestHandleListCandidates_FlagOffReturnsError(t *testing.T) {
	os.Unsetenv("ENGRAM_VNEXT_F_ENABLED")

	s := NewServer(ServerOptions{Version: "test"})
	// candidateStore is nil (flag is off)

	args, _ := json.Marshal(map[string]any{"project": "some-project"})
	_, err := s.handleListCandidates(context.Background(), args)
	require.Error(t, err, "must error when flag is off")
	require.True(t, strings.Contains(err.Error(), "ENGRAM_VNEXT_F_ENABLED"),
		"error must mention the feature flag, got: %v", err)
}

func TestHandleGetCandidate_EmptyIDReturnsError(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	s := NewServer(ServerOptions{Version: "test"})
	s.candidateStore = nonNilCandidateStore()

	args, err := json.Marshal(map[string]any{})
	require.NoError(t, err)

	_, callErr := s.handleGetCandidate(context.Background(), args)
	require.Error(t, callErr, "missing id must return an error")
	require.True(t,
		strings.Contains(callErr.Error(), "id is required"),
		"error must mention 'id is required', got: %v", callErr)
}
