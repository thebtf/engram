package forgetting

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/cognitive"
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
