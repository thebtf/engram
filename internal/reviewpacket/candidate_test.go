package reviewpacket

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

func TestFromCandidate_PendingPacketCarriesDecisionEvidenceSnapshotAndAudit(t *testing.T) {
	packet := FromCandidate(&models.CrystallizationCandidate{
		ID:                      42,
		Status:                  models.CandidateStatusPending,
		ProposedPromotionTarget: "semantic",
		ProposedTier:            "semantic",
		ProposedEpistemicType:   "decision",
		SourceSessionID:         "sess-42",
		Fingerprint:             "abc123",
		EvidenceHandles:         []string{"session:sess-42"},
		AffectedProjects:        []string{"engram"},
		PrivacyScope:            "project",
	})

	require.Equal(t, "candidate:42:abc123", packet.PacketID)
	require.Equal(t, int64(42), packet.CandidateID)
	require.Equal(t, CandidatePacketKind, packet.Kind)
	require.Equal(t, []string{"promote", "reject", "supersede"}, packet.Decision.AllowedActions)
	require.Equal(t, "semantic", packet.Decision.PromotionTarget)
	require.Equal(t, []string{"engram"}, packet.Scope.Projects)
	require.Equal(t, "project", packet.Scope.PrivacyScope)
	require.Equal(t, []CandidateEvidence{{Handle: "session:sess-42", Kind: "session"}}, packet.Evidence)
	require.True(t, packet.Snapshot.Required)
	require.Equal(t, SnapshotStore, packet.Snapshot.Store)
	require.Equal(t, "pre_action_required", packet.Snapshot.Status)
	require.Equal(t, AuditStore, packet.Audit.Store)
	require.Equal(t, "pending_on_action", packet.Audit.Status)
	require.True(t, packet.ReadOnly)
	require.True(t, packet.MutationRequirements.StructuralLossCheckRequired)
	require.True(t, packet.MutationRequirements.PrivacyScopeRequired)
	require.True(t, packet.MutationRequirements.AuditWriteRequired)
	require.True(t, packet.MutationRequirements.SnapshotRequired)
	require.NoError(t, ValidateMutationBoundary(packet))
}

func TestFromCandidate_TerminalPacketHasNoPendingActions(t *testing.T) {
	packet := FromCandidate(&models.CrystallizationCandidate{
		ID:     77,
		Status: models.CandidateStatusRejected,
	})

	require.Empty(t, packet.Decision.AllowedActions)
	require.False(t, packet.Snapshot.Required)
	require.Equal(t, "not_required", packet.Snapshot.Status)
	require.Equal(t, "terminal_record", packet.Audit.Status)
}

func TestValidateCandidateMutation_BlocksMissingPrivacyScope(t *testing.T) {
	packet := FromCandidate(&models.CrystallizationCandidate{
		ID:              42,
		Status:          models.CandidateStatusPending,
		EvidenceHandles: []string{"session:sess-42"},
	})

	require.ErrorContains(t, ValidateMutationBoundary(packet), "privacy_scope")
}

func TestValidateCandidateMutation_BlocksTerminalPacket(t *testing.T) {
	packet := FromCandidate(&models.CrystallizationCandidate{
		ID:           77,
		Status:       models.CandidateStatusRejected,
		PrivacyScope: "project",
	})

	require.ErrorContains(t, ValidateMutationBoundary(packet), "pending review packet")
}

func TestValidateCandidateMutation_BlocksManualBoundaryBypass(t *testing.T) {
	packet := FromCandidate(&models.CrystallizationCandidate{
		ID:           42,
		Status:       models.CandidateStatusPending,
		PrivacyScope: "project",
	})
	packet.ReadOnly = false
	require.ErrorContains(t, ValidateMutationBoundary(packet), "read-only")

	packet = FromCandidate(&models.CrystallizationCandidate{
		ID:           42,
		Status:       models.CandidateStatusPending,
		PrivacyScope: "project",
	})
	packet.Status = string(models.CandidateStatusRejected)
	require.ErrorContains(t, ValidateMutationBoundary(packet), "pending review packet")
}

func TestNewCandidateReviewActionSnapshot_CapturesCandidateBeforeState(t *testing.T) {
	snapshot, err := NewCandidateReviewActionSnapshot("promote", &models.CrystallizationCandidate{
		ID:              42,
		Status:          models.CandidateStatusPending,
		SourceSessionID: "sess-42",
		Fingerprint:     "abc123",
		PrivacyScope:    "project",
	}, "agent:developer")

	require.NoError(t, err)
	require.Equal(t, models.SnapshotOpCandidateReviewAction, snapshot.OpType)
	require.Equal(t, "agent:developer", snapshot.Actor)
	require.Equal(t, "sess-42", snapshot.SourceSessionID)
	require.Contains(t, string(snapshot.BeforeState), `"status":"pending"`)
	require.Contains(t, string(snapshot.BeforeState), `"candidate:42"`)
	require.Contains(t, string(snapshot.Parameters), `"operation":"candidate_review_action"`)
}

func TestBuildReviewQueue_PacketCentricPayloadIncludesProvenanceAndSparseMetrics(t *testing.T) {
	now := time.Date(2026, time.June, 30, 22, 0, 0, 0, time.UTC)
	candidate := &models.CrystallizationCandidate{
		ID:                      42,
		Status:                  models.CandidateStatusPending,
		ProposedContent:         "keep this useful operating rule",
		ProposedPromotionTarget: "semantic",
		ProposedTier:            "semantic",
		ProposedEpistemicType:   "decision",
		SourceSessionID:         "sess-42",
		EvidenceHandles:         []string{"session:sess-42", "file:notes.md"},
		AffectedProjects:        []string{"engram"},
		PrivacyScope:            "project",
		Fingerprint:             "abc123",
		CreatedAt:               now.Add(-time.Hour),
		UpdatedAt:               now,
	}

	queue := BuildReviewQueue([]*models.CrystallizationCandidate{candidate}, models.CandidateStatusPending, 1, now)

	require.Equal(t, ReviewStateLive, queue.State)
	require.Equal(t, ReviewStateSparse, queue.Metrics.State)
	require.Contains(t, queue.Metrics.SparseReason, "bounded page")
	require.Len(t, queue.Packets, 1)
	require.Equal(t, "candidate:42:abc123", queue.Packets[0].PacketID)
	require.Equal(t, ReviewPacketTypeUsefulnessNoise, queue.Packets[0].PacketType)
	require.Equal(t, ReviewActionPreserve, queue.Packets[0].Recommendation)
	require.Equal(t, []string{"candidate:42"}, queue.Packets[0].CandidateRefs)
	require.GreaterOrEqual(t, queue.Packets[0].ProvenanceCount, 4)
	require.Equal(t, SnapshotStore, queue.Packets[0].ReviewPacket.Snapshot.Store)
	require.Equal(t, AuditStore, queue.Packets[0].ReviewPacket.Audit.Store)
}

func TestPreviewReviewAction_DoesNotMutateAndRejectsStaleOrUnsupportedPackets(t *testing.T) {
	candidate := &models.CrystallizationCandidate{
		ID:               42,
		Status:           models.CandidateStatusPending,
		PrivacyScope:     "project",
		AffectedProjects: []string{"engram"},
		Fingerprint:      "abc123",
		Confidence:       0.4,
	}
	packetID := FromCandidate(candidate).PacketID

	preview, err := PreviewReviewAction(candidate, packetID, ReviewActionSuppress, time.Time{})

	require.NoError(t, err)
	require.Equal(t, ReviewActionSuppress, preview.ActionType)
	require.Equal(t, "reject", preview.CandidateAction)
	require.Equal(t, ReviewStateRiskyConfirm, preview.State)
	require.Equal(t, models.CandidateStatusPending, candidate.Status, "preview must not mutate candidate status")

	_, err = PreviewReviewAction(candidate, packetID, "destroy", time.Time{})
	require.ErrorIs(t, err, ErrUnsupportedReviewAction)

	candidate.Status = models.CandidateStatusRejected
	_, err = PreviewReviewAction(candidate, packetID, ReviewActionSuppress, time.Time{})
	require.ErrorIs(t, err, ErrStaleReviewPacket)
}

func TestReviewMetrics_HonestGatedAndErrorStatesDoNotClaimLivePrecision(t *testing.T) {
	now := time.Date(2026, time.June, 30, 22, 5, 0, 0, time.UTC)
	sparse := BuildReviewMetrics([]*models.CrystallizationCandidate{{ID: 7, Status: models.CandidateStatusPending}}, 20, now)
	require.Equal(t, ReviewStateSparse, sparse.State)
	require.Contains(t, sparse.SparseReason, "confidence telemetry")

	gated := GatedReviewMetrics("protected scope", now)
	require.Equal(t, ReviewStateGated, gated.State)
	require.Equal(t, 0, gated.BacklogTotal)
	require.Contains(t, gated.SparseReason, "protected scope")

	failed := ErrorReviewMetrics(assertiveError("store unavailable"), now)
	require.Equal(t, ReviewStateError, failed.State)
	require.Equal(t, 0, failed.ReadyCount)
	require.Contains(t, failed.SparseReason, "store unavailable")
}

func TestReviewQueueStatePayloadsNormalizeDefaultLimit(t *testing.T) {
	gated := GatedReviewQueue("flag disabled", 0, time.Time{})
	require.Equal(t, 20, gated.Limit)
	require.Equal(t, 20, gated.Backlog.Limit)

	failed := ErrorReviewQueue(assertiveError("store unavailable"), -5, time.Time{})
	require.Equal(t, 20, failed.Limit)
	require.Equal(t, 20, failed.Backlog.Limit)
}

type assertiveError string

func (e assertiveError) Error() string { return string(e) }

func TestReviewActionReceipt_CarriesSnapshotAuditBackedTransition(t *testing.T) {
	memoryID := int64(77)
	updated := &models.CrystallizationCandidate{ID: 42, Status: models.CandidateStatusPromoted, Fingerprint: "abc123"}
	snapshot := &models.BulkOpSnapshot{SnapshotID: "snap-42", OpType: models.SnapshotOpCandidateReviewAction}
	memory := &models.Memory{ID: memoryID}

	receipt := NewReviewActionReceipt(ReviewActionPreserve, "candidate:42:abc123", updated, snapshot, memory)

	require.Equal(t, ReviewActionPreserve, receipt.ActionType)
	require.Equal(t, "promote", receipt.CandidateAction)
	require.Equal(t, "candidate:42:abc123", receipt.UpdatedPacketID)
	require.Equal(t, "promoted", receipt.UpdatedPacketStatus)
	require.Equal(t, "snap-42", receipt.SnapshotID)
	require.Equal(t, "candidate_review:preserve:42", receipt.AuditRef)
	require.Equal(t, memoryID, receipt.MemoryID)
}
