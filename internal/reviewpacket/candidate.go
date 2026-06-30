package reviewpacket

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/thebtf/engram/pkg/models"
)

const (
	CandidatePacketKind = "candidate_review"
	SnapshotStore       = "bulk_op_snapshots"
	AuditStore          = "audit_log"
)

type CandidateReviewPacket struct {
	Decision             CandidateDecisionPolicy       `json:"decision"`
	Scope                CandidateScope                `json:"scope"`
	Snapshot             CandidateSnapshotPolicy       `json:"snapshot"`
	Audit                CandidateAuditPolicy          `json:"audit"`
	MutationRequirements CandidateMutationRequirements `json:"mutation_requirements"`
	PacketID             string                        `json:"packet_id"`
	Kind                 string                        `json:"kind"`
	Status               string                        `json:"status"`
	CandidateID          int64                         `json:"candidate_id"`
	Evidence             []CandidateEvidence           `json:"evidence"`
	ReadOnly             bool                          `json:"read_only"`
}

type CandidateDecisionPolicy struct {
	PromotionTarget string   `json:"promotion_target"`
	Tier            string   `json:"tier"`
	EpistemicType   string   `json:"epistemic_type"`
	DefaultAction   string   `json:"default_action"`
	AllowedActions  []string `json:"allowed_actions"`
}

type CandidateScope struct {
	PrivacyScope    string   `json:"privacy_scope"`
	SourceSessionID string   `json:"source_session_id"`
	Projects        []string `json:"projects"`
}

type CandidateEvidence struct {
	Handle string `json:"handle"`
	Kind   string `json:"kind"`
}

type CandidateSnapshotPolicy struct {
	Store     string `json:"store"`
	Operation string `json:"operation"`
	Status    string `json:"status"`
	Required  bool   `json:"required"`
}

type CandidateAuditPolicy struct {
	Store  string `json:"store"`
	Action string `json:"action"`
	Status string `json:"status"`
}

type CandidateMutationRequirements struct {
	StructuralLossCheckRequired bool `json:"structural_loss_check_required"`
	PrivacyScopeRequired        bool `json:"privacy_scope_required"`
	AuditWriteRequired          bool `json:"audit_write_required"`
	SnapshotRequired            bool `json:"snapshot_required"`
}

func FromCandidate(candidate *models.CrystallizationCandidate) CandidateReviewPacket {
	if candidate == nil {
		return CandidateReviewPacket{
			Kind:     CandidatePacketKind,
			Evidence: []CandidateEvidence{},
			ReadOnly: true,
		}
	}

	status := string(candidate.Status)
	return CandidateReviewPacket{
		PacketID:    candidatePacketID(candidate),
		Kind:        CandidatePacketKind,
		CandidateID: candidate.ID,
		Status:      status,
		Decision: CandidateDecisionPolicy{
			PromotionTarget: candidate.ProposedPromotionTarget,
			Tier:            candidate.ProposedTier,
			EpistemicType:   candidate.ProposedEpistemicType,
			DefaultAction:   defaultAction(candidate.Status),
			AllowedActions:  allowedActions(candidate.Status),
		},
		Scope: CandidateScope{
			Projects:        append([]string(nil), candidate.AffectedProjects...),
			PrivacyScope:    candidate.PrivacyScope,
			SourceSessionID: candidate.SourceSessionID,
		},
		Evidence: evidenceFromHandles(candidate.EvidenceHandles),
		Snapshot: CandidateSnapshotPolicy{
			Store:     SnapshotStore,
			Operation: "candidate_review_action",
			Status:    snapshotStatus(candidate.Status),
			Required:  candidate.Status == models.CandidateStatusPending,
		},
		Audit: CandidateAuditPolicy{
			Store:  AuditStore,
			Action: "candidate_review",
			Status: auditStatus(candidate.Status),
		},
		MutationRequirements: mutationRequirements(candidate.Status),
		ReadOnly:             true,
	}
}

func candidatePacketID(candidate *models.CrystallizationCandidate) string {
	suffix := strings.TrimSpace(candidate.Fingerprint)
	if suffix == "" {
		suffix = strconv.FormatInt(candidate.ID, 10)
	}
	return fmt.Sprintf("candidate:%d:%s", candidate.ID, suffix)
}

func allowedActions(status models.CandidateStatus) []string {
	if status == models.CandidateStatusPending {
		return []string{"promote", "reject", "supersede"}
	}
	return []string{}
}

func defaultAction(status models.CandidateStatus) string {
	if status == models.CandidateStatusPending {
		return "promote"
	}
	return ""
}

func snapshotStatus(status models.CandidateStatus) string {
	if status == models.CandidateStatusPending {
		return "pre_action_required"
	}
	return "not_required"
}

func auditStatus(status models.CandidateStatus) string {
	if status == models.CandidateStatusPending {
		return "pending_on_action"
	}
	return "terminal_record"
}

func mutationRequirements(status models.CandidateStatus) CandidateMutationRequirements {
	if status != models.CandidateStatusPending {
		return CandidateMutationRequirements{}
	}
	return CandidateMutationRequirements{
		StructuralLossCheckRequired: true,
		PrivacyScopeRequired:        true,
		AuditWriteRequired:          true,
		SnapshotRequired:            true,
	}
}

func ValidateCandidateMutation(candidate *models.CrystallizationCandidate) error {
	if candidate == nil {
		return fmt.Errorf("candidate mutation requires a candidate")
	}
	return ValidateMutationBoundary(FromCandidate(candidate))
}

// NewCandidateReviewActionSnapshot builds the required pre-action snapshot for
// a candidate mutation path before promote/reject/supersede changes state.
func NewCandidateReviewActionSnapshot(action string, candidate *models.CrystallizationCandidate, actor string) (*models.BulkOpSnapshot, error) {
	if candidate == nil {
		return nil, fmt.Errorf("candidate review snapshot requires a candidate")
	}
	if candidate.ID <= 0 {
		return nil, fmt.Errorf("candidate review snapshot requires a persisted candidate id")
	}
	action = strings.TrimSpace(action)
	switch action {
	case "promote", "reject", "supersede":
	default:
		return nil, fmt.Errorf("candidate review snapshot: unsupported action %q", action)
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "system"
	}

	candidateJSON, err := json.Marshal(candidate)
	if err != nil {
		return nil, fmt.Errorf("candidate review snapshot marshal candidate: %w", err)
	}
	beforeState, err := json.Marshal(map[string]models.SnapshotEntry{
		fmt.Sprintf("candidate:%d", candidate.ID): {
			Kind:   models.EntryKindRestore,
			Before: candidateJSON,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("candidate review snapshot marshal before_state: %w", err)
	}
	snap, err := models.NewBulkOpSnapshot("candidate-review-"+uuid.NewString(), models.SnapshotOpCandidateReviewAction, actor, beforeState)
	if err != nil {
		return nil, err
	}
	params, err := json.Marshal(map[string]any{
		"action":       action,
		"candidate_id": candidate.ID,
		"packet_id":    FromCandidate(candidate).PacketID,
		"operation":    "candidate_review_action",
	})
	if err != nil {
		return nil, fmt.Errorf("candidate review snapshot marshal parameters: %w", err)
	}
	snap.SourceSessionID = candidate.SourceSessionID
	snap.Parameters = params
	return snap, nil
}

func ValidateMutationBoundary(packet CandidateReviewPacket) error {
	if packet.Kind != CandidatePacketKind {
		return fmt.Errorf("candidate mutation requires %s packet", CandidatePacketKind)
	}
	if packet.Status != string(models.CandidateStatusPending) {
		return fmt.Errorf("candidate mutation requires a pending review packet")
	}
	if !packet.ReadOnly {
		return fmt.Errorf("candidate mutation requires a read-only review packet")
	}
	if len(packet.Decision.AllowedActions) == 0 {
		return fmt.Errorf("candidate mutation requires a pending review packet")
	}
	if !packet.MutationRequirements.StructuralLossCheckRequired {
		return fmt.Errorf("candidate mutation requires structural-loss check")
	}
	if packet.MutationRequirements.PrivacyScopeRequired && strings.TrimSpace(packet.Scope.PrivacyScope) == "" {
		return fmt.Errorf("candidate mutation requires privacy_scope")
	}
	if packet.MutationRequirements.SnapshotRequired {
		if packet.Snapshot.Store != SnapshotStore || !packet.Snapshot.Required || packet.Snapshot.Status != "pre_action_required" {
			return fmt.Errorf("candidate mutation requires pre-action structural snapshot policy")
		}
	}
	if packet.MutationRequirements.AuditWriteRequired {
		if packet.Audit.Store != AuditStore || packet.Audit.Action != "candidate_review" || packet.Audit.Status != "pending_on_action" {
			return fmt.Errorf("candidate mutation requires pending audit write policy")
		}
	}
	return nil
}

func evidenceFromHandles(handles []string) []CandidateEvidence {
	evidence := make([]CandidateEvidence, 0, len(handles))
	for _, handle := range handles {
		handle = strings.TrimSpace(handle)
		if handle == "" {
			continue
		}
		evidence = append(evidence, CandidateEvidence{
			Handle: handle,
			Kind:   evidenceKind(handle),
		})
	}
	return evidence
}

func evidenceKind(handle string) string {
	if index := strings.Index(handle, ":"); index > 0 {
		return handle[:index]
	}
	return "handle"
}
