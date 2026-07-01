package forgetting

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
)

func TestClassifier_ClassifiesFiveOperationsWithoutDestroyingByDefault(t *testing.T) {
	service := NewClassifier()
	cases := []struct {
		name      string
		request   cognitive.ForgettingClassificationRequest
		operation cognitive.ForgettingOperation
		state     cognitive.ForgettingDecisionState
	}{
		{
			name: "low value noise is suppressed",
			request: cognitive.ForgettingClassificationRequest{
				Reason:       cognitive.ForgettingReasonLowValue,
				MemoryID:     "memory-low-value",
				Evidence:     []string{"candidate:low-value"},
				Project:      "engram",
				PrivacyScope: "project",
			},
			operation: cognitive.ForgettingOperationSuppress,
			state:     cognitive.ForgettingDecisionAutoResolvable,
		},
		{
			name: "retention aged trace expires",
			request: cognitive.ForgettingClassificationRequest{
				Reason:       cognitive.ForgettingReasonRetentionExpired,
				MemoryID:     "memory-aged",
				Evidence:     []string{"retention:30d"},
				Project:      "engram",
				PrivacyScope: "project",
			},
			operation: cognitive.ForgettingOperationExpire,
			state:     cognitive.ForgettingDecisionAutoResolvable,
		},
		{
			name: "cold retention archives",
			request: cognitive.ForgettingClassificationRequest{
				Reason:       cognitive.ForgettingReasonColdStorage,
				MemoryID:     "memory-cold",
				Evidence:     []string{"operator:archive"},
				Project:      "engram",
				PrivacyScope: "project",
			},
			operation: cognitive.ForgettingOperationArchive,
			state:     cognitive.ForgettingDecisionAutoResolvable,
		},
		{
			name: "duplicate evidence consolidates through review",
			request: cognitive.ForgettingClassificationRequest{
				Reason:       cognitive.ForgettingReasonDuplicate,
				MemoryID:     "memory-duplicate-a",
				RelatedIDs:   []string{"memory-duplicate-b"},
				Evidence:     []string{"candidate:duplicate"},
				Project:      "engram",
				PrivacyScope: "project",
			},
			operation: cognitive.ForgettingOperationConsolidate,
			state:     cognitive.ForgettingDecisionReviewRequired,
		},
		{
			name: "destructive request is blocked by default",
			request: cognitive.ForgettingClassificationRequest{
				Reason:       cognitive.ForgettingReasonOperatorDestroy,
				MemoryID:     "memory-destroy",
				Evidence:     []string{"operator:destroy"},
				Project:      "engram",
				PrivacyScope: "project",
				Risky:        true,
			},
			operation: cognitive.ForgettingOperationDestroy,
			state:     cognitive.ForgettingDecisionBlocked,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := service.ClassifyForgetting(context.Background(), tc.request)
			require.NoError(t, err)
			require.Equal(t, tc.operation, decision.Operation)
			require.Equal(t, tc.state, decision.State)
			require.False(t, decision.DataDestructionByDefault)
			require.True(t, decision.Audit.Required)
			require.Equal(t, "audit_log", decision.Audit.AuditStore)
			require.Equal(t, "bulk_op_snapshots", decision.Audit.SnapshotStore)
			require.True(t, decision.ArchiveFirst)
			require.NotEmpty(t, decision.PolicyOwner)
			require.Equal(t, ForgettingAuditExportPath, decision.Audit.ExportPath)
			require.NotEmpty(t, decision.PolicyBoundary)
			require.NotEmpty(t, decision.Rationale)
		})
	}
}

func TestClassifier_RejectsUnclassifiedRequests(t *testing.T) {
	service := NewClassifier()

	_, err := service.ClassifyForgetting(context.Background(), cognitive.ForgettingClassificationRequest{
		MemoryID: "memory-unknown",
		Reason:   "unknown",
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "unsupported forgetting reason")
}

func TestClassifier_StructuralLossEscalatesConsolidationWithRationale(t *testing.T) {
	service := NewClassifier()

	decision, err := service.ClassifyForgetting(context.Background(), cognitive.ForgettingClassificationRequest{
		Reason:     cognitive.ForgettingReasonDuplicate,
		MemoryID:   "memory-a",
		RelatedIDs: []string{"memory-b"},
		Evidence:   []string{"candidate:duplicate"},
		StructuralLoss: cognitive.ForgettingStructuralLoss{
			UniqueMeaning: true,
			Provenance:    true,
			Rationale:     "source-specific caveat would disappear",
		},
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.ForgettingOperationConsolidate, decision.Operation)
	require.Equal(t, cognitive.ForgettingDecisionReviewRequired, decision.State)
	require.True(t, decision.StructuralLoss.UniqueMeaning)
	require.True(t, decision.StructuralLoss.Provenance)
	require.Contains(t, decision.StructuralLoss.Rationale, "source-specific caveat")
	require.Contains(t, decision.Rationale, "structural loss")
	require.True(t, decision.Review.Required)
}

func TestClassifier_StructuralLossBlocksDestroyWithRationale(t *testing.T) {
	service := NewClassifier()

	decision, err := service.ClassifyForgetting(context.Background(), cognitive.ForgettingClassificationRequest{
		Reason:   cognitive.ForgettingReasonOperatorDestroy,
		MemoryID: "memory-danger",
		Evidence: []string{"operator:destroy"},
		StructuralLoss: cognitive.ForgettingStructuralLoss{
			Scope:     true,
			Rationale: "global policy memory would be narrowed to one project",
		},
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.ForgettingOperationDestroy, decision.Operation)
	require.Equal(t, cognitive.ForgettingDecisionBlocked, decision.State)
	require.True(t, decision.StructuralLoss.Scope)
	require.Contains(t, decision.StructuralLoss.Rationale, "global policy")
	require.Contains(t, decision.Rationale, "structural loss")
	require.True(t, decision.Review.Required)
	require.False(t, decision.DataDestructionByDefault)
}

func TestClassifier_RiskyCasesEmitBoundedReviewPackets(t *testing.T) {
	service := NewClassifier()

	decision, err := service.ClassifyForgetting(context.Background(), cognitive.ForgettingClassificationRequest{
		Reason:     cognitive.ForgettingReasonDuplicate,
		MemoryID:   "memory-a",
		RelatedIDs: []string{"memory-b", "memory-c"},
		Evidence: []string{
			"candidate:1",
			"candidate:2",
			"candidate:3",
			"candidate:4",
			"candidate:5",
			"candidate:6",
		},
		StructuralLoss: cognitive.ForgettingStructuralLoss{UniqueMeaning: true, Rationale: "meaning loss"},
		Project:        "engram",
		PrivacyScope:   "project",
	})

	require.NoError(t, err)
	require.True(t, decision.Review.Required)
	require.Equal(t, "forgetting_review", decision.Review.Packet.Kind)
	require.NotEmpty(t, decision.Review.Packet.PacketID)
	require.Equal(t, cognitive.ForgettingOperationConsolidate, decision.Review.Packet.Operation)
	require.Equal(t, cognitive.ForgettingDecisionReviewRequired, decision.Review.Packet.State)
	require.LessOrEqual(t, len(decision.Review.Packet.Evidence), 5)
	require.Equal(t, []string{"memory-a", "memory-b", "memory-c"}, decision.Review.Packet.Scope.MemoryIDs)
	require.True(t, decision.Review.Packet.Snapshot.Required)
	require.Equal(t, "bulk_op_snapshots", decision.Review.Packet.Snapshot.Store)
	require.Equal(t, "audit_log", decision.Review.Packet.Audit.Store)
	require.Equal(t, "forgetting_review_action", decision.Review.Packet.Snapshot.Operation)
	require.Equal(t, "pending_on_action", decision.Review.Packet.Audit.Status)
	require.True(t, decision.Review.Packet.Preview.MutationSeparated)
	require.True(t, decision.Review.Packet.Preview.ApprovalRequired)
	require.True(t, decision.Review.Packet.MutationRequirements.AuditWriteBeforeMutation)
	require.True(t, decision.Review.Packet.ReadOnly)
	require.Equal(t, "archive", decision.Review.Packet.Preview.Action)
	require.Equal(t, "archive", decision.Review.Packet.Preview.Recommendation)
	require.Contains(t, decision.Review.Packet.Preview.AfterPlan, "move memory out of hot retrieval")
}

func TestClassifier_SafeLowRiskActionsDoNotEmitPackets(t *testing.T) {
	service := NewClassifier()
	cases := []cognitive.ForgettingClassificationRequest{
		{Reason: cognitive.ForgettingReasonLowValue, MemoryID: "low"},
		{Reason: cognitive.ForgettingReasonRetentionExpired, MemoryID: "expired"},
		{Reason: cognitive.ForgettingReasonColdStorage, MemoryID: "cold"},
	}

	for _, request := range cases {
		decision, err := service.ClassifyForgetting(context.Background(), request)
		require.NoError(t, err)
		require.Equal(t, cognitive.ForgettingDecisionAutoResolvable, decision.State)
		require.False(t, decision.Review.Required)
		require.Empty(t, decision.Review.Packet.PacketID)
	}
}

func TestClassifier_RiskyAutoResolvableActionsRouteToReviewPackets(t *testing.T) {
	service := NewClassifier()
	cases := []cognitive.ForgettingClassificationRequest{
		{Reason: cognitive.ForgettingReasonLowValue, MemoryID: "low", Risky: true},
		{Reason: cognitive.ForgettingReasonRetentionExpired, MemoryID: "expired", Risky: true},
		{Reason: cognitive.ForgettingReasonColdStorage, MemoryID: "cold", Risky: true},
	}

	for _, request := range cases {
		decision, err := service.ClassifyForgetting(context.Background(), request)
		require.NoError(t, err)
		require.Equal(t, cognitive.ForgettingDecisionReviewRequired, decision.State)
		require.True(t, decision.Review.Required)
		require.Equal(t, "forgetting_review", decision.Review.Packet.Kind)
		require.NotEmpty(t, decision.Review.Packet.PacketID)
		require.Contains(t, decision.Rationale, "risky")
	}
}

func TestClassifier_ArchiveFirstAllowedActionsPrecedeDestructiveOrRiskyActions(t *testing.T) {
	service := NewClassifier()
	cases := []cognitive.ForgettingClassificationRequest{
		{Reason: cognitive.ForgettingReasonLowValue, MemoryID: "low"},
		{Reason: cognitive.ForgettingReasonRetentionExpired, MemoryID: "expired"},
		{Reason: cognitive.ForgettingReasonColdStorage, MemoryID: "cold"},
		{Reason: cognitive.ForgettingReasonDuplicate, MemoryID: "dup", RelatedIDs: []string{"dup-b"}, PrivacyScope: "project"},
		{Reason: cognitive.ForgettingReasonOperatorDestroy, MemoryID: "destroy", PrivacyScope: "project"},
	}

	for _, request := range cases {
		decision, err := service.ClassifyForgetting(context.Background(), request)
		require.NoError(t, err)
		require.True(t, decision.ArchiveFirst)
		require.NotEmpty(t, decision.Review.AllowedActions)
		require.Equal(t, "archive", decision.Review.AllowedActions[0], "archive must be considered before shrinking mode %s", decision.Operation)
		require.False(t, decision.DataDestructionByDefault)
	}
}

func TestClassifier_StructuralLossIncludesHistoricalValueWithoutRationale(t *testing.T) {
	service := NewClassifier()

	decision, err := service.ClassifyForgetting(context.Background(), cognitive.ForgettingClassificationRequest{
		Reason:         cognitive.ForgettingReasonDuplicate,
		MemoryID:       "memory-a",
		RelatedIDs:     []string{"memory-b"},
		StructuralLoss: cognitive.ForgettingStructuralLoss{HistoricalValue: true},
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.ForgettingDecisionReviewRequired, decision.State)
	require.True(t, decision.StructuralLoss.HistoricalValue)
	require.Contains(t, decision.Rationale, "historical value")
	require.True(t, decision.Review.Required)
}

func TestPreviewReviewAction_DoesNotMutateAndRequiresBoundary(t *testing.T) {
	service := NewClassifier()
	decision, err := service.ClassifyForgetting(context.Background(), cognitive.ForgettingClassificationRequest{
		Reason:       cognitive.ForgettingReasonDuplicate,
		MemoryID:     "memory-a",
		RelatedIDs:   []string{"memory-b"},
		Project:      "engram",
		PrivacyScope: "project",
		StructuralLoss: cognitive.ForgettingStructuralLoss{
			UniqueMeaning: true,
			Rationale:     "would lose source caveat",
		},
	})
	require.NoError(t, err)
	packetID := decision.Review.Packet.PacketID

	preview, err := PreviewReviewAction(decision, packetID, "archive", time.Time{})
	require.NoError(t, err)
	require.Equal(t, packetID, preview.PacketID)
	require.Equal(t, "archive", preview.ActionType)
	require.Equal(t, "archive", preview.ReviewPacket.Preview.Action)
	require.Equal(t, "archive", preview.ReviewPacket.Preview.Recommendation)
	require.Contains(t, preview.ReviewPacket.Preview.AfterPlan, "move memory out of hot retrieval")
	require.Equal(t, cognitive.ForgettingDecisionReviewRequired, decision.State, "preview must not mutate the decision")
	require.Contains(t, preview.AuditExpectation, "snapshot before archive mutation")
	require.True(t, preview.ConfirmationRequired)
	require.NoError(t, ValidateForgettingMutationBoundary(decision.Review.Packet))
	preview.ReviewPacket.AllowedActions[0] = "mutated"
	preview.ReviewPacket.Evidence = append(preview.ReviewPacket.Evidence, "preview-only")
	preview.ReviewPacket.Scope.MemoryIDs[0] = "mutated-memory"
	preview.ReviewPacket.Preview.BeforeRefs[0] = "mutated-before"
	require.Equal(t, []string{"archive", "suppress", "consolidate"}, decision.Review.Packet.AllowedActions)
	require.NotContains(t, decision.Review.Packet.Evidence, "preview-only")
	require.Equal(t, "memory-a", decision.Review.Packet.Scope.MemoryIDs[0])
	require.Equal(t, "memory-a", decision.Review.Packet.Preview.BeforeRefs[0])

	tampered := decision.Review.Packet
	tampered.ReadOnly = false
	require.ErrorContains(t, ValidateForgettingMutationBoundary(tampered), "read-only")
	tampered = decision.Review.Packet
	tampered.MutationRequirements.PrivacyScopeRequired = false
	require.ErrorContains(t, ValidateForgettingMutationBoundary(tampered), "non-optional privacy_scope gate")
	tampered = decision.Review.Packet
	tampered.MutationRequirements.SnapshotRequired = false
	require.ErrorContains(t, ValidateForgettingMutationBoundary(tampered), "non-optional snapshot gate")
	tampered = decision.Review.Packet
	tampered.MutationRequirements.ReviewApprovalRequired = false
	require.ErrorContains(t, ValidateForgettingMutationBoundary(tampered), "explicit review approval requirement")
}

func TestNewForgettingReviewActionSnapshot_BindsPacketToSnapshotOpType(t *testing.T) {
	service := NewClassifier()
	decision, err := service.ClassifyForgetting(context.Background(), cognitive.ForgettingClassificationRequest{
		Reason:       cognitive.ForgettingReasonDuplicate,
		MemoryID:     "101",
		RelatedIDs:   []string{"102"},
		PrivacyScope: "project",
		StructuralLoss: cognitive.ForgettingStructuralLoss{
			Provenance: true,
			Rationale:  "source trace would disappear",
		},
	})
	require.NoError(t, err)

	snapshot, err := NewForgettingReviewActionSnapshot(decision.Review.Packet, "archive", "agent/reviewer", json.RawMessage(`{"memory:101":{"kind":"restore","before":{"id":101}}}`))
	require.NoError(t, err)
	require.Equal(t, models.SnapshotOpForgettingReviewAction, snapshot.OpType)
	require.Equal(t, "agent/reviewer", snapshot.Actor)
	require.Contains(t, string(snapshot.Parameters), `"operation":"forgetting_review_action"`)
	require.Contains(t, string(snapshot.Parameters), `"action":"archive"`)
	require.Contains(t, string(snapshot.Parameters), decision.Review.Packet.PacketID)
	require.Equal(t, []int64{101, 102}, snapshot.AffectedMemoryIDs)

	_, err = NewForgettingReviewActionSnapshot(decision.Review.Packet, "archive", "agent/reviewer", json.RawMessage(`{}`))
	require.ErrorContains(t, err, "non-empty before_state")

	_, err = NewForgettingReviewActionSnapshot(decision.Review.Packet, "consolidate", "agent/reviewer", json.RawMessage(`{"memory:101":{"kind":"restore","before":{"id":101}}}`))
	require.ErrorContains(t, err, "does not match approved preview action")
}

func TestValidateForgettingMutationBoundary_BlocksDestroyPacket(t *testing.T) {
	service := NewClassifier()
	decision, err := service.ClassifyForgetting(context.Background(), cognitive.ForgettingClassificationRequest{
		Reason:       cognitive.ForgettingReasonOperatorDestroy,
		MemoryID:     "memory-danger",
		PrivacyScope: "project",
	})
	require.NoError(t, err)

	err = ValidateForgettingMutationBoundary(decision.Review.Packet)
	require.ErrorContains(t, err, "blocked")
}

func TestAuditExportProof_AutomaticAndReviewedActionsRoundTripFromAuditEntry(t *testing.T) {
	service := NewClassifier()
	automatic, err := service.ClassifyForgetting(context.Background(), cognitive.ForgettingClassificationRequest{
		Reason:      cognitive.ForgettingReasonLowValue,
		MemoryID:    "memory-low",
		Evidence:    []string{"candidate:low"},
		PolicyOwner: "retention-policy:v1",
	})
	require.NoError(t, err)

	automaticProof, err := BuildAuditExportProof(automatic, ActionReceipt{Actor: "agent/system", Result: cognitive.ForgettingActionResultApplied, ExportRef: "cr-010:auto"})
	require.NoError(t, err)
	require.Equal(t, cognitive.ForgettingActionPathAutomatic, automaticProof.Path)
	require.Equal(t, ForgettingAutomaticAuditAction, automaticProof.AuditAction)
	require.Equal(t, "retention-policy:v1", automaticProof.PolicyOwner)
	require.Contains(t, automaticProof.Evidence, "candidate:low")

	automaticEntry, err := AuditLogEntryFromProof(automaticProof)
	require.NoError(t, err)
	require.Equal(t, ForgettingAutomaticAuditAction, automaticEntry.Action)
	restoredAutomatic, err := ExportProofFromAuditLogEntry(automaticEntry)
	require.NoError(t, err)
	require.Equal(t, automaticProof, restoredAutomatic)
	require.Equal(t, ForgettingAuditExportPath, "audit_log.after_state")

	reviewed, err := service.ClassifyForgetting(context.Background(), cognitive.ForgettingClassificationRequest{
		Reason:         cognitive.ForgettingReasonDuplicate,
		MemoryID:       "memory-a",
		RelatedIDs:     []string{"memory-b"},
		PrivacyScope:   "project",
		StructuralLoss: cognitive.ForgettingStructuralLoss{Provenance: true, Rationale: "source trace would disappear"},
	})
	require.NoError(t, err)
	reviewedProof, err := BuildAuditExportProof(reviewed, ActionReceipt{
		Actor:      "agent/reviewer",
		Action:     "archive",
		Path:       cognitive.ForgettingActionPathReviewed,
		Result:     cognitive.ForgettingActionResultPreviewed,
		SnapshotID: "forgetting-review-snapshot-1",
		AuditRef:   "forgetting_review:memory-a",
	})
	require.NoError(t, err)
	require.Equal(t, cognitive.ForgettingActionPathReviewed, reviewedProof.Path)
	require.Equal(t, ForgettingReviewAuditAction, reviewedProof.AuditAction)
	require.Equal(t, "archive", reviewedProof.Action)
	require.Equal(t, reviewed.Review.Packet.PacketID, reviewedProof.PacketID)
	require.Equal(t, "forgetting-review-snapshot-1", reviewedProof.SnapshotID)

	reviewedEntry, err := AuditLogEntryFromProof(reviewedProof)
	require.NoError(t, err)
	restoredReviewed, err := ExportProofFromAuditLogEntry(reviewedEntry)
	require.NoError(t, err)
	require.Equal(t, reviewedProof, restoredReviewed)

	reviewedAltProof, err := BuildAuditExportProof(reviewed, ActionReceipt{
		Actor:      "agent/reviewer",
		Action:     "consolidate",
		Path:       cognitive.ForgettingActionPathReviewed,
		Result:     cognitive.ForgettingActionResultApplied,
		SnapshotID: "forgetting-review-snapshot-2",
		AuditRef:   "forgetting_review:memory-a:consolidate",
	})
	require.NoError(t, err)
	require.Equal(t, "consolidate", reviewedAltProof.Action)

	_, err = BuildAuditExportProof(reviewed, ActionReceipt{Actor: "agent/reviewer", Path: cognitive.ForgettingActionPathReviewed})
	require.ErrorContains(t, err, "explicit action")

	_, err = BuildAuditExportProof(reviewed, ActionReceipt{Actor: "agent/reviewer", Action: "archive", Path: cognitive.ForgettingActionPathReviewed})
	require.ErrorContains(t, err, "snapshot_id")

	_, err = BuildAuditExportProof(automatic, ActionReceipt{Actor: "agent/system", Action: "destroy", Result: cognitive.ForgettingActionResultApplied})
	require.ErrorContains(t, err, "automatic proof")
}
