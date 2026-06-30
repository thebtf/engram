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
	"github.com/thebtf/engram/internal/reviewpacket"
	"github.com/thebtf/engram/pkg/models"
)

// nonNilCandidateStore returns a zero-value *gormdb.CandidateStore so that the
// server's candidateStore != nil check passes. Tests that exercise the project-guard
// return an error before any store method is invoked, so zero-value is safe here.
func nonNilCandidateStore() *gormdb.CandidateStore {
	return &gormdb.CandidateStore{}
}

type fakeReviewLoopCandidateLister struct {
	rows    []*models.CrystallizationCandidate
	project string
	status  models.CandidateStatus
	limit   int
}

func (f *fakeReviewLoopCandidateLister) ListByStatus(ctx context.Context, project string, status models.CandidateStatus, limit int) ([]*models.CrystallizationCandidate, error) {
	f.project = project
	f.status = status
	f.limit = limit
	return f.rows, nil
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

func TestCandidateTools_ExposeCR008ReviewLoopContracts(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range candidateTools() {
		names[tool.Name] = true
	}

	for _, name := range []string{
		"review_metrics.read",
		"review_queue.read",
		"review_packet.detail",
		"review_packet.preview_action",
		"review_packet.apply_action",
	} {
		require.True(t, names[name], "missing CR-008 MCP tool %s", name)
	}
}

func TestHandleReviewQueueRead_UnsupportedPacketTypeReturnsGatedPayload(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	s := NewServer(ServerOptions{Version: "test"})
	s.candidateStore = nonNilCandidateStore()
	args, err := json.Marshal(map[string]any{"packet_type": "raw_memory", "limit": 7})
	require.NoError(t, err)

	result, err := s.handleReviewQueueRead(context.Background(), args)

	require.NoError(t, err)
	var queue reviewpacket.ReviewQueueRead
	require.NoError(t, json.Unmarshal([]byte(result), &queue))
	require.Equal(t, reviewpacket.ReviewStateGated, queue.State)
	require.Contains(t, queue.Metrics.SparseReason, "unsupported packet_type")
	require.Empty(t, queue.Packets)
}

func TestHandleReviewQueueRead_RiskyOnlyKeepsUnfilteredMetricsAndBacklog(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	lister := &fakeReviewLoopCandidateLister{rows: []*models.CrystallizationCandidate{
		{ID: 42, Status: models.CandidateStatusPending, SourceSessionID: "sess-42", AffectedProjects: []string{"engram"}, PrivacyScope: "project", Fingerprint: "abc123", Confidence: 0.4},
		{ID: 43, Status: models.CandidateStatusPending, SourceSessionID: "sess-43", AffectedProjects: []string{"engram"}, PrivacyScope: "project", Fingerprint: "def456", Confidence: 0.8},
	}}
	s := NewServer(ServerOptions{Version: "test"})
	s.candidateStore = nonNilCandidateStore()
	s.reviewLoopCandidateStoreSeam = lister
	args, err := json.Marshal(map[string]any{"project": "engram", "status": "pending", "limit": 5, "risky_only": true})
	require.NoError(t, err)

	result, err := s.handleReviewQueueRead(context.Background(), args)

	require.NoError(t, err)
	var queue reviewpacket.ReviewQueueRead
	require.NoError(t, json.Unmarshal([]byte(result), &queue))
	require.Len(t, queue.Packets, 1)
	require.Equal(t, "candidate:42:abc123", queue.Packets[0].PacketID)
	require.Equal(t, "engram", lister.project)
	require.Equal(t, models.CandidateStatusPending, lister.status)
	require.Equal(t, 5, lister.limit)
	require.Equal(t, 2, queue.Metrics.BacklogTotal)
	require.Equal(t, 2, queue.Metrics.ReadyCount)
	require.Equal(t, 1, queue.Metrics.RiskyCount)
	require.Equal(t, reviewpacket.ReviewStateLive, queue.Metrics.State)
	require.Equal(t, 2, queue.Backlog.BoundedTotal)
	require.Equal(t, 2, queue.Backlog.ReadyCount)
}

func TestHandleReviewPacketPreviewAction_UnsupportedActionRejectedBeforeStoreMutation(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	s := NewServer(ServerOptions{Version: "test"})
	s.candidateStore = nonNilCandidateStore()
	args, err := json.Marshal(map[string]any{"packet_id": "candidate:42:abc123", "action_type": "destroy"})
	require.NoError(t, err)

	_, err = s.handleReviewPacketPreviewAction(context.Background(), args)

	require.ErrorIs(t, err, reviewpacket.ErrUnsupportedReviewAction)
}
