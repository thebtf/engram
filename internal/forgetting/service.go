package forgetting

import (
	"context"
	"fmt"
	"strings"

	"github.com/thebtf/engram/internal/reviewpacket"
	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
)

const (
	ForgettingReviewPacketKind     = "forgetting_review"
	ForgettingReviewAuditAction    = "forgetting_review"
	ForgettingAutomaticAuditAction = "forgetting_auto"
	ForgettingAuditExportPath      = "audit_log.after_state.forgetting_export_proof"
	maxPacketEvidence              = 5
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
			"hide low-value or noisy memory from hot retrieval while preserving audit evidence and archive reachability",
			"suppress keeps the original memory reachable for audit/rollback and can fall back to archive before irreversible loss",
			false,
			[]string{"archive", "suppress"}), nil
	case cognitive.ForgettingReasonRetentionExpired:
		return newDecision(request, cognitive.ForgettingOperationExpire, cognitive.ForgettingDecisionAutoResolvable,
			"apply retention policy to low-value episodic traces without reclassifying them as hard deletes",
			"expire is retention-governed, archive-first, and must retain audit evidence for the retention decision",
			false,
			[]string{"archive", "expire"}), nil
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
			[]string{"archive", "suppress", "consolidate"}), nil
	case cognitive.ForgettingReasonOperatorDestroy:
		return newDecision(request, cognitive.ForgettingOperationDestroy, cognitive.ForgettingDecisionBlocked,
			"block destructive removal until explicit operator review and audit/export proof exist",
			"destroy is an operator-approved hard-delete class and is never automatic in this classifier",
			true,
			[]string{"archive", "destroy"}), nil
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
		rationale = rationale + "; structural loss guard triggered: " + structuralLossRationale(request.StructuralLoss)
	}
	if request.Risky && state == cognitive.ForgettingDecisionAutoResolvable {
		state = cognitive.ForgettingDecisionReviewRequired
		reviewRequired = true
		rationale = rationale + "; risky request context requires review"
	}

	policyOwner := policyOwnerFor(request, operation)
	decision := cognitive.ForgettingDecision{
		Operation:      operation,
		State:          state,
		Rationale:      rationale,
		PolicyOwner:    policyOwner,
		PolicyBoundary: boundary,
		ArchiveFirst:   true,
		Audit: cognitive.ForgettingAuditSurface{
			Required:      true,
			SnapshotStore: reviewpacket.SnapshotStore,
			AuditStore:    reviewpacket.AuditStore,
			ExportPath:    ForgettingAuditExportPath,
			Evidence:      append([]string(nil), request.Evidence...),
		},
		Review: cognitive.ForgettingReviewPolicy{
			Required:       reviewRequired,
			PacketKind:     ForgettingReviewPacketKind,
			AllowedActions: append([]string(nil), allowedActions...),
		},
		StructuralLoss:           request.StructuralLoss,
		DataDestructionByDefault: false,
	}
	if reviewRequired || state == cognitive.ForgettingDecisionBlocked {
		decision.Review.Packet = buildReviewPacket(request, decision)
	}
	return decision
}

func hasStructuralLoss(loss cognitive.ForgettingStructuralLoss) bool {
	return loss.UniqueMeaning || loss.Provenance || loss.Scope || loss.HistoricalValue || strings.TrimSpace(loss.Rationale) != ""
}

func structuralLossRationale(loss cognitive.ForgettingStructuralLoss) string {
	rationale := strings.TrimSpace(loss.Rationale)
	if rationale != "" {
		return rationale
	}
	reasons := make([]string, 0, 4)
	if loss.UniqueMeaning {
		reasons = append(reasons, "unique meaning")
	}
	if loss.Provenance {
		reasons = append(reasons, "provenance")
	}
	if loss.Scope {
		reasons = append(reasons, "scope")
	}
	if loss.HistoricalValue {
		reasons = append(reasons, "historical value")
	}
	if len(reasons) == 0 {
		return "unspecified structural loss"
	}
	return strings.Join(reasons, ", ")
}

func policyOwnerFor(request cognitive.ForgettingClassificationRequest, operation cognitive.ForgettingOperation) string {
	if owner := strings.TrimSpace(request.PolicyOwner); owner != "" {
		return owner
	}
	switch operation {
	case cognitive.ForgettingOperationConsolidate, cognitive.ForgettingOperationDestroy:
		return "operator_governance_policy"
	default:
		return "system_retention_policy"
	}
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

	previewAction := string(decision.Operation)
	return cognitive.ForgettingReviewPacket{
		PacketID:       packetID(decision.Operation, request.MemoryID),
		Kind:           ForgettingReviewPacketKind,
		Operation:      decision.Operation,
		State:          decision.State,
		Rationale:      decision.Rationale,
		AllowedActions: append([]string(nil), decision.Review.AllowedActions...),
		PolicyOwner:    decision.PolicyOwner,
		Scope: cognitive.ForgettingPacketScope{
			Project:      request.Project,
			PrivacyScope: request.PrivacyScope,
			MemoryIDs:    memoryIDs,
		},
		Evidence: evidence,
		Preview: cognitive.ForgettingPacketPreview{
			BeforeRefs:        append([]string(nil), memoryIDs...),
			AfterPlan:         previewAfterPlan(decision.Operation),
			Recommendation:    previewAction,
			Action:            previewAction,
			ApprovalRequired:  true,
			MutationSeparated: true,
		},
		Snapshot: cognitive.ForgettingSnapshotPolicy{
			Store:     reviewpacket.SnapshotStore,
			Operation: string(models.SnapshotOpForgettingReviewAction),
			Status:    "pre_action_required",
			Required:  true,
		},
		Audit: cognitive.ForgettingAuditPolicy{
			Store:  reviewpacket.AuditStore,
			Action: ForgettingReviewAuditAction,
			Status: "pending_on_action",
		},
		MutationRequirements: cognitive.ForgettingMutationRequirements{
			StructuralLossCheckRequired: true,
			PrivacyScopeRequired:        true,
			AuditWriteBeforeMutation:    true,
			SnapshotRequired:            true,
			ReviewApprovalRequired:      true,
		},
		StructuralLoss: decision.StructuralLoss,
		ReadOnly:       true,
	}
}

func previewAfterPlan(operation cognitive.ForgettingOperation) string {
	switch operation {
	case cognitive.ForgettingOperationConsolidate:
		return "review approved consolidate may merge redundant content only after snapshot and audit write; unique meaning remains preserved or the action stays blocked"
	case cognitive.ForgettingOperationDestroy:
		return "review approved destroy remains blocked by default; archive must be considered before any irreversible delete path"
	case cognitive.ForgettingOperationArchive:
		return "move memory out of hot retrieval while preserving bounded historical/export reachability"
	case cognitive.ForgettingOperationExpire:
		return "apply retention expiry without hard delete and keep audit/export proof"
	default:
		return "suppress from hot retrieval while preserving original evidence and archive fallback"
	}
}

func packetID(operation cognitive.ForgettingOperation, memoryID string) string {
	suffix := strings.TrimSpace(memoryID)
	if suffix == "" {
		suffix = "unscoped"
	}
	return fmt.Sprintf("forgetting:%s:%s", operation, suffix)
}
