package forgetting

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/reviewpacket"
	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
)

// ReviewActionPreview is the read-only packet/action preview returned before a
// reviewed forgetting mutation may be applied.
type ReviewActionPreview struct {
	ReviewPacket         cognitive.ForgettingReviewPacket   `json:"review_packet"`
	Snapshot             cognitive.ForgettingSnapshotPolicy `json:"snapshot"`
	Audit                cognitive.ForgettingAuditPolicy    `json:"audit"`
	ActionType           string                             `json:"action_type"`
	PacketID             string                             `json:"packet_id"`
	AuditExpectation     string                             `json:"audit_expectation"`
	State                string                             `json:"state"`
	ConfirmationRequired bool                               `json:"confirmation_required"`
}

// ActionReceipt carries post-action evidence used to build audit/export proof.
type ActionReceipt struct {
	Actor      string
	Action     string
	Path       cognitive.ForgettingActionPath
	Result     cognitive.ForgettingActionResult
	PacketID   string
	SnapshotID string
	AuditRef   string
	ExportRef  string
	Evidence   []string
}

// PreviewReviewAction validates a forgetting review packet and returns a
// no-mutation preview. It intentionally produces no store writes.
func PreviewReviewAction(decision cognitive.ForgettingDecision, packetID string, action string, now time.Time) (ReviewActionPreview, error) {
	packet := decision.Review.Packet
	if err := validateReviewPacketIdentity(packet, packetID); err != nil {
		return ReviewActionPreview{}, err
	}
	normalized, err := normalizePacketAction(packet, action)
	if err != nil {
		return ReviewActionPreview{}, err
	}
	state := reviewpacket.ReviewStateLive
	if decision.State == cognitive.ForgettingDecisionBlocked || decision.Operation == cognitive.ForgettingOperationDestroy {
		state = reviewpacket.ReviewStateRiskyConfirm
	}
	if hasStructuralLoss(decision.StructuralLoss) {
		state = reviewpacket.ReviewStateRiskyConfirm
	}
	previewPacket := packet
	previewPacket.AllowedActions = append([]string(nil), packet.AllowedActions...)
	previewPacket.Evidence = append([]string(nil), packet.Evidence...)
	previewPacket.Scope.MemoryIDs = append([]string(nil), packet.Scope.MemoryIDs...)
	previewPacket.Preview.BeforeRefs = append([]string(nil), packet.Preview.BeforeRefs...)
	previewPacket.Preview.Action = normalized
	previewPacket.Preview.Recommendation = normalized
	previewPacket.Preview.AfterPlan = previewAfterPlan(cognitive.ForgettingOperation(normalized))
	return ReviewActionPreview{
		ReviewPacket:         previewPacket,
		Snapshot:             packet.Snapshot,
		Audit:                packet.Audit,
		ActionType:           normalized,
		PacketID:             packet.PacketID,
		AuditExpectation:     fmt.Sprintf("%s audit after %s snapshot before %s mutation", packet.Audit.Action, packet.Snapshot.Store, normalized),
		State:                state,
		ConfirmationRequired: true,
	}, nil
}

// ValidateForgettingMutationBoundary enforces the packet/action separation used
// by CR-008: a risky forgetting mutation requires a read-only packet, snapshot,
// structural-loss check, privacy scope, and audit-before-mutate policy.
func ValidateForgettingMutationBoundary(packet cognitive.ForgettingReviewPacket) error {
	if packet.Kind != ForgettingReviewPacketKind {
		return fmt.Errorf("forgetting mutation requires %s packet", ForgettingReviewPacketKind)
	}
	if strings.TrimSpace(packet.PacketID) == "" {
		return fmt.Errorf("forgetting mutation requires packet_id")
	}
	if !packet.ReadOnly {
		return fmt.Errorf("forgetting mutation requires a read-only review packet")
	}
	if packet.State == cognitive.ForgettingDecisionBlocked {
		return fmt.Errorf("forgetting mutation blocked by structural/destructive guard")
	}
	if len(packet.AllowedActions) == 0 {
		return fmt.Errorf("forgetting mutation requires allowed review actions")
	}
	req := packet.MutationRequirements
	if !req.StructuralLossCheckRequired {
		return fmt.Errorf("forgetting mutation requires structural-loss check")
	}
	if req.PrivacyScopeRequired && strings.TrimSpace(packet.Scope.PrivacyScope) == "" {
		return fmt.Errorf("forgetting mutation requires privacy_scope")
	}
	if !req.AuditWriteBeforeMutation {
		return fmt.Errorf("forgetting mutation requires audit-before-mutate policy")
	}
	if req.SnapshotRequired {
		if packet.Snapshot.Store != reviewpacket.SnapshotStore || packet.Snapshot.Operation != string(models.SnapshotOpForgettingReviewAction) || !packet.Snapshot.Required || packet.Snapshot.Status != "pre_action_required" {
			return fmt.Errorf("forgetting mutation requires pre-action structural snapshot policy")
		}
	}
	if packet.Audit.Store != reviewpacket.AuditStore || packet.Audit.Action != ForgettingReviewAuditAction || packet.Audit.Status != "pending_on_action" {
		return fmt.Errorf("forgetting mutation requires pending forgetting audit policy")
	}
	if !packet.Preview.MutationSeparated || !packet.Preview.ApprovalRequired {
		return fmt.Errorf("forgetting mutation requires separate approved preview")
	}
	return nil
}

// NewForgettingReviewActionSnapshot builds the required pre-action snapshot for
// an approved forgetting review mutation. The caller supplies the concrete
// before_state rows; this helper binds them to the CR-010 packet identity and
// the bulk_op_snapshots op_type allowed by migration 154.
func NewForgettingReviewActionSnapshot(packet cognitive.ForgettingReviewPacket, action string, actor string, beforeState json.RawMessage) (*models.BulkOpSnapshot, error) {
	if err := ValidateForgettingMutationBoundary(packet); err != nil {
		return nil, err
	}
	normalized, err := normalizePacketAction(packet, action)
	if err != nil {
		return nil, err
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "system"
	}
	snapshot, err := models.NewBulkOpSnapshot("forgetting-review-"+uuid.NewString(), models.SnapshotOpForgettingReviewAction, actor, beforeState)
	if err != nil {
		return nil, err
	}
	params, err := json.Marshal(map[string]any{
		"action":     normalized,
		"memory_ids": packet.Scope.MemoryIDs,
		"operation":  string(models.SnapshotOpForgettingReviewAction),
		"packet_id":  packet.PacketID,
	})
	if err != nil {
		return nil, fmt.Errorf("forgetting review snapshot marshal parameters: %w", err)
	}
	snapshot.AffectedMemoryIDs = parseAffectedMemoryIDs(packet.Scope.MemoryIDs)
	snapshot.Parameters = params
	return snapshot, nil
}

// BuildAuditExportProof creates the self-describing export payload stored in
// audit_log.after_state for automatic or reviewed forgetting actions.
func BuildAuditExportProof(decision cognitive.ForgettingDecision, receipt ActionReceipt) (cognitive.ForgettingAuditExportProof, error) {
	if decision.Operation == "" {
		return cognitive.ForgettingAuditExportProof{}, fmt.Errorf("forgetting export proof requires operation")
	}
	action := strings.TrimSpace(receipt.Action)
	if action == "" {
		action = string(decision.Operation)
	}
	path := receipt.Path
	if path == "" {
		if decision.Review.Required {
			path = cognitive.ForgettingActionPathReviewed
		} else {
			path = cognitive.ForgettingActionPathAutomatic
		}
	}
	result := receipt.Result
	if result == "" {
		if decision.State == cognitive.ForgettingDecisionBlocked {
			result = cognitive.ForgettingActionResultBlocked
		} else if path == cognitive.ForgettingActionPathReviewed {
			result = cognitive.ForgettingActionResultPreviewed
		} else {
			result = cognitive.ForgettingActionResultApplied
		}
	}
	actor := strings.TrimSpace(receipt.Actor)
	if actor == "" {
		actor = "system"
	}
	packetID := strings.TrimSpace(receipt.PacketID)
	if packetID == "" && decision.Review.Packet.PacketID != "" {
		packetID = decision.Review.Packet.PacketID
	}
	auditAction := ForgettingAutomaticAuditAction
	if path == cognitive.ForgettingActionPathReviewed {
		auditAction = ForgettingReviewAuditAction
	}
	evidence := append([]string(nil), decision.Audit.Evidence...)
	evidence = append(evidence, receipt.Evidence...)
	return cognitive.ForgettingAuditExportProof{
		Operation:                decision.Operation,
		Action:                   action,
		State:                    decision.State,
		Path:                     path,
		Actor:                    actor,
		Result:                   result,
		PolicyOwner:              decision.PolicyOwner,
		PolicyBoundary:           decision.PolicyBoundary,
		PacketID:                 packetID,
		SnapshotID:               strings.TrimSpace(receipt.SnapshotID),
		AuditAction:              auditAction,
		AuditRef:                 strings.TrimSpace(receipt.AuditRef),
		ExportRef:                strings.TrimSpace(receipt.ExportRef),
		Evidence:                 evidence,
		DataDestructionByDefault: decision.DataDestructionByDefault,
		StructuralLoss:           decision.StructuralLoss,
	}, nil
}

// AuditLogEntryFromProof encodes proof into after_state so exported audit rows
// can be reconstructed without transcript archaeology.
func AuditLogEntryFromProof(proof cognitive.ForgettingAuditExportProof) (gormdb.AuditLogEntry, error) {
	if proof.Operation == "" || proof.AuditAction == "" {
		return gormdb.AuditLogEntry{}, fmt.Errorf("forgetting audit entry requires operation and audit_action")
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		return gormdb.AuditLogEntry{}, fmt.Errorf("forgetting audit proof marshal: %w", err)
	}
	afterState := json.RawMessage(raw)
	return gormdb.AuditLogEntry{
		Action:     proof.AuditAction,
		Actor:      proof.Actor,
		AfterState: &afterState,
		Reason:     fmt.Sprintf("forgetting %s %s via %s", proof.Operation, proof.Result, proof.Path),
	}, nil
}

// ExportProofFromAuditLogEntry reconstructs CR-010 proof from audit_log.after_state.
func ExportProofFromAuditLogEntry(entry gormdb.AuditLogEntry) (cognitive.ForgettingAuditExportProof, error) {
	if entry.AfterState == nil || len(*entry.AfterState) == 0 {
		return cognitive.ForgettingAuditExportProof{}, fmt.Errorf("forgetting audit proof missing after_state")
	}
	var proof cognitive.ForgettingAuditExportProof
	if err := json.Unmarshal(*entry.AfterState, &proof); err != nil {
		return cognitive.ForgettingAuditExportProof{}, fmt.Errorf("forgetting audit proof unmarshal: %w", err)
	}

	if proof.Operation == "" || proof.AuditAction == "" {
		return cognitive.ForgettingAuditExportProof{}, fmt.Errorf("forgetting audit proof incomplete")
	}
	return proof, nil
}

func parseAffectedMemoryIDs(memoryIDs []string) []int64 {
	affected := make([]int64, 0, len(memoryIDs))
	for _, raw := range memoryIDs {
		id := strings.TrimSpace(raw)
		id = strings.TrimPrefix(id, "memory:")
		if id == "" {
			continue
		}
		parsed, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			continue
		}
		affected = append(affected, parsed)
	}
	return affected
}

func validateReviewPacketIdentity(packet cognitive.ForgettingReviewPacket, packetID string) error {
	if strings.TrimSpace(packetID) == "" {
		return fmt.Errorf("forgetting review preview requires packet_id")
	}
	if packet.PacketID != strings.TrimSpace(packetID) {
		return fmt.Errorf("%w: current packet_id is %s", reviewpacket.ErrStaleReviewPacket, packet.PacketID)
	}
	return nil
}

func normalizePacketAction(packet cognitive.ForgettingReviewPacket, action string) (string, error) {
	normalized := strings.TrimSpace(action)
	if normalized == "" {
		return "", reviewpacket.ErrUnsupportedReviewAction
	}
	for _, allowed := range packet.AllowedActions {
		if normalized == allowed {
			return normalized, nil
		}
	}
	return "", reviewpacket.ErrUnsupportedReviewAction
}
