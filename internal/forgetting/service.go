package forgetting

import (
	"context"
	"fmt"
	"strings"

	"github.com/thebtf/engram/internal/reviewpacket"
	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	forgettingReviewPacketKind = "forgetting_review"
	maxPacketEvidence          = 5
)

// Classifier maps memory-quality signals onto the explicit forgetting
// taxonomy. It is classification-only and intentionally has no store handle.
type Classifier struct{}

// NewClassifier returns the bounded forgetting classifier.
func NewClassifier() *Classifier {
	return &Classifier{}
}

// ClassifyForgetting returns the safe operation envelope for request without
// mutating memory storage.
func (c *Classifier) ClassifyForgetting(ctx context.Context, request cognitive.ForgettingClassificationRequest) (cognitive.ForgettingDecision, error) {
	if err := ctx.Err(); err != nil {
		return cognitive.ForgettingDecision{}, err
	}

	switch request.Reason {
	case cognitive.ForgettingReasonLowValue:
		return newDecision(request, cognitive.ForgettingOperationSuppress, cognitive.ForgettingDecisionAutoResolvable,
			"hide low-value or noisy memory from hot retrieval while preserving audit evidence",
			"suppress keeps the original memory reachable for audit/rollback and does not erase provenance",
			false,
			[]string{"suppress", "archive"}), nil
	case cognitive.ForgettingReasonRetentionExpired:
		return newDecision(request, cognitive.ForgettingOperationExpire, cognitive.ForgettingDecisionAutoResolvable,
			"apply retention policy to low-value episodic traces without reclassifying them as hard deletes",
			"expire is retention-governed and must retain audit evidence for the retention decision",
			false,
			[]string{"expire", "archive"}), nil
	case cognitive.ForgettingReasonColdStorage:
		return newDecision(request, cognitive.ForgettingOperationArchive, cognitive.ForgettingDecisionAutoResolvable,
			"move cold but still useful memory out of hot retrieval into bounded archive reachability",
			"archive keeps the memory reachable through explicit historical/export paths",
			false,
			[]string{"archive", "suppress"}), nil
	case cognitive.ForgettingReasonDuplicate:
		return newDecision(request, cognitive.ForgettingOperationConsolidate, cognitive.ForgettingDecisionReviewRequired,
			"merge duplicate evidence only through a reviewable consolidation decision",
			"consolidate may replace redundant rows with a stronger semantic record but cannot silently drop unique meaning",
			true,
			[]string{"consolidate", "archive", "suppress"}), nil
	case cognitive.ForgettingReasonOperatorDestroy:
		return newDecision(request, cognitive.ForgettingOperationDestroy, cognitive.ForgettingDecisionBlocked,
			"block destructive removal until explicit operator review and audit/export proof exist",
			"destroy is an operator-approved hard-delete class and is never automatic in this classifier",
			true,
			[]string{"destroy", "archive"}), nil
	default:
		return cognitive.ForgettingDecision{}, fmt.Errorf("unsupported forgetting reason %q", request.Reason)
	}
}

func newDecision(
	request cognitive.ForgettingClassificationRequest,
	operation cognitive.ForgettingOperation,
	state cognitive.ForgettingDecisionState,
	rationale string,
	boundary string,
	reviewRequired bool,
	allowedActions []string,
) cognitive.ForgettingDecision {
	if hasStructuralLoss(request.StructuralLoss) {
		if operation == cognitive.ForgettingOperationDestroy {
			state = cognitive.ForgettingDecisionBlocked
		} else {
			state = cognitive.ForgettingDecisionReviewRequired
		}
		reviewRequired = true
		rationale = rationale + "; structural loss guard triggered: " + request.StructuralLoss.Rationale
	}

	decision := cognitive.ForgettingDecision{
		Operation:      operation,
		State:          state,
		Rationale:      rationale,
		PolicyBoundary: boundary,
		Audit: cognitive.ForgettingAuditSurface{
			Required:      true,
			SnapshotStore: reviewpacket.SnapshotStore,
			AuditStore:    reviewpacket.AuditStore,
			Evidence:      append([]string(nil), request.Evidence...),
		},
		Review: cognitive.ForgettingReviewPolicy{
			Required:       reviewRequired,
			PacketKind:     forgettingReviewPacketKind,
			AllowedActions: append([]string(nil), allowedActions...),
		},
		StructuralLoss: request.StructuralLoss,
	}
	if reviewRequired || state == cognitive.ForgettingDecisionBlocked {
		decision.Review.Packet = buildReviewPacket(request, decision)
	}
	return decision
}

func hasStructuralLoss(loss cognitive.ForgettingStructuralLoss) bool {
	return loss.UniqueMeaning || loss.Provenance || loss.Scope || strings.TrimSpace(loss.Rationale) != ""
}

func buildReviewPacket(request cognitive.ForgettingClassificationRequest, decision cognitive.ForgettingDecision) cognitive.ForgettingReviewPacket {
	memoryIDs := make([]string, 0, 1+len(request.RelatedIDs))
	if strings.TrimSpace(request.MemoryID) != "" {
		memoryIDs = append(memoryIDs, request.MemoryID)
	}
	memoryIDs = append(memoryIDs, request.RelatedIDs...)
	evidence := append([]string(nil), request.Evidence...)
	if len(evidence) > maxPacketEvidence {
		evidence = evidence[:maxPacketEvidence]
	}

	return cognitive.ForgettingReviewPacket{
		PacketID:       packetID(decision.Operation, request.MemoryID),
		Kind:           forgettingReviewPacketKind,
		Operation:      decision.Operation,
		State:          decision.State,
		Rationale:      decision.Rationale,
		AllowedActions: append([]string(nil), decision.Review.AllowedActions...),
		Scope: cognitive.ForgettingPacketScope{
			Project:      request.Project,
			PrivacyScope: request.PrivacyScope,
			MemoryIDs:    memoryIDs,
		},
		Evidence: evidence,
		Snapshot: cognitive.ForgettingSnapshotPolicy{
			Store:     reviewpacket.SnapshotStore,
			Operation: "forgetting_review_action",
			Status:    "pre_action_required",
			Required:  true,
		},
		Audit: cognitive.ForgettingAuditPolicy{
			Store:  reviewpacket.AuditStore,
			Action: "forgetting_review",
			Status: "pending_on_action",
		},
		StructuralLoss: decision.StructuralLoss,
	}
}

func packetID(operation cognitive.ForgettingOperation, memoryID string) string {
	suffix := strings.TrimSpace(memoryID)
	if suffix == "" {
		suffix = "unscoped"
	}
	return fmt.Sprintf("forgetting:%s:%s", operation, suffix)
}
