package reviewpacket

import (
	"testing"

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
