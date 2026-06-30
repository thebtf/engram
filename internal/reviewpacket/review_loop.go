package reviewpacket

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/thebtf/engram/pkg/models"
)

const (
	ReviewPacketTypeUsefulnessNoise = "usefulness_noise"

	ReviewActionPreserve = "preserve"
	ReviewActionSuppress = "suppress"

	ReviewStateLive              = "live"
	ReviewStateSparse            = "sparse"
	ReviewStateGated             = "gated"
	ReviewStateError             = "error"
	ReviewStateEmpty             = "empty"
	ReviewStateStalePacket       = "stale-packet"
	ReviewStateRiskyConfirm      = "risky-confirm"
	ReviewStateUnsupportedAction = "unsupported-action"
)

var (
	ErrInvalidReviewPacket     = errors.New("invalid_review_packet")
	ErrStaleReviewPacket       = errors.New("stale_review_packet")
	ErrUnsupportedReviewAction = errors.New("unsupported_review_action")
)

type ReviewQueueRead struct {
	Packets   []ReviewPacketSummary `json:"packets"`
	Backlog   ReviewQueueBacklog    `json:"backlog"`
	Metrics   ReviewMetrics         `json:"metrics"`
	Freshness string                `json:"freshness"`
	State     string                `json:"state"`
	Status    string                `json:"status"`
	Limit     int                   `json:"limit"`
}

type ReviewQueueBacklog struct {
	SparseReason string `json:"sparse_reason,omitempty"`
	State        string `json:"state"`
	BoundedTotal int    `json:"bounded_total"`
	ReadyCount   int    `json:"ready_count"`
	Limit        int    `json:"limit"`
}

type ReviewMetrics struct {
	Freshness    string `json:"freshness"`
	SparseReason string `json:"sparse_reason,omitempty"`
	State        string `json:"state"`
	BacklogTotal int    `json:"backlog_total"`
	ReadyCount   int    `json:"ready_count"`
	RiskyCount   int    `json:"risky_count"`
}

type ReviewPacketSummary struct {
	ReviewPacket    CandidateReviewPacket `json:"review_packet"`
	PacketID        string                `json:"packet_id"`
	PacketType      string                `json:"packet_type"`
	Recommendation  string                `json:"recommendation"`
	ConfidenceState string                `json:"confidence_state"`
	QueueStatus     string                `json:"queue_status"`
	CreatedAt       string                `json:"created_at"`
	Freshness       string                `json:"freshness"`
	CandidateRefs   []string              `json:"candidate_refs"`
	ProvenanceCount int                   `json:"provenance_count"`
}

type ReviewPacketDetail struct {
	PacketDetail   CandidateReviewPacket   `json:"packet_detail"`
	Snapshot       CandidateSnapshotPolicy `json:"snapshot"`
	Audit          CandidateAuditPolicy    `json:"audit"`
	Freshness      string                  `json:"freshness"`
	State          string                  `json:"state"`
	CandidateRefs  []string                `json:"candidate_refs"`
	AffectedRefs   []string                `json:"affected_refs"`
	AllowedActions []string                `json:"allowed_actions"`
	MetricNotes    []string                `json:"metric_notes"`
	ProvenanceRefs []CandidateEvidence     `json:"provenance_refs"`
}

type ReviewActionPreview struct {
	ReviewPacket         CandidateReviewPacket   `json:"review_packet"`
	Snapshot             CandidateSnapshotPolicy `json:"snapshot"`
	Audit                CandidateAuditPolicy    `json:"audit"`
	ActionType           string                  `json:"action_type"`
	CandidateAction      string                  `json:"candidate_action"`
	PacketID             string                  `json:"packet_id"`
	AuditExpectation     string                  `json:"audit_expectation"`
	StaleMarker          string                  `json:"stale_marker"`
	State                string                  `json:"state"`
	AffectedRefs         []string                `json:"affected_refs"`
	ConfirmationRequired bool                    `json:"confirmation_required"`
	CandidateID          int64                   `json:"candidate_id"`
}

type ReviewActionReceipt struct {
	ActionType          string `json:"action_type"`
	CandidateAction     string `json:"candidate_action"`
	PacketID            string `json:"packet_id"`
	UpdatedPacketID     string `json:"updated_packet_id"`
	UpdatedPacketStatus string `json:"updated_packet_status"`
	AuditRef            string `json:"audit_ref"`
	SnapshotID          string `json:"snapshot_id,omitempty"`
	State               string `json:"state"`
	QueueDelta          int    `json:"queue_delta"`
	CandidateID         int64  `json:"candidate_id"`
	MemoryID            int64  `json:"memory_id,omitempty"`
}

func BuildReviewQueue(candidates []*models.CrystallizationCandidate, status models.CandidateStatus, limit int, now time.Time) ReviewQueueRead {
	metrics := BuildReviewMetrics(candidates, limit, now)
	return BuildReviewQueueWithMetrics(candidates, status, limit, now, metrics)
}

func BuildReviewQueueWithMetrics(candidates []*models.CrystallizationCandidate, status models.CandidateStatus, limit int, now time.Time, metrics ReviewMetrics) ReviewQueueRead {
	if limit <= 0 {
		limit = 20
	}
	packets := make([]ReviewPacketSummary, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		packets = append(packets, PacketSummaryFromCandidate(candidate, now))
	}

	state := ReviewStateLive
	if len(packets) == 0 {
		state = ReviewStateEmpty
	}
	return ReviewQueueRead{
		Packets:   packets,
		Backlog:   ReviewQueueBacklog{BoundedTotal: metrics.BacklogTotal, ReadyCount: metrics.ReadyCount, Limit: limit, State: metrics.State, SparseReason: metrics.SparseReason},
		Metrics:   metrics,
		Freshness: metrics.Freshness,
		State:     state,
		Status:    string(status),
		Limit:     limit,
	}
}

func BuildReviewMetrics(candidates []*models.CrystallizationCandidate, limit int, now time.Time) ReviewMetrics {
	if limit <= 0 {
		limit = 20
	}
	readyCount := 0
	riskyCount := 0
	unknownConfidence := false
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		if candidate.Status == models.CandidateStatusPending {
			readyCount++
		}
		if candidate.Confidence <= 0 {
			unknownConfidence = true
		} else if candidate.Confidence < 0.5 {
			riskyCount++
		}
	}

	state := ReviewStateLive
	reasons := make([]string, 0, 2)
	if unknownConfidence {
		state = ReviewStateSparse
		reasons = append(reasons, "risk classification is sparse for packets without confidence telemetry")
	}
	if len(candidates) >= limit {
		state = ReviewStateSparse
		reasons = append(reasons, "candidate store returned a bounded page, not a proven total backlog")
	}

	return ReviewMetrics{
		BacklogTotal: len(candidates),
		ReadyCount:   readyCount,
		RiskyCount:   riskyCount,
		Freshness:    freshestCandidateTime(candidates, now),
		SparseReason: strings.Join(reasons, "; "),
		State:        state,
	}
}

func GatedReviewMetrics(reason string, now time.Time) ReviewMetrics {
	return ReviewMetrics{State: ReviewStateGated, Freshness: fallbackFreshness(now), SparseReason: strings.TrimSpace(reason)}
}

func ErrorReviewMetrics(err error, now time.Time) ReviewMetrics {
	reason := "review metrics unavailable"
	if err != nil {
		reason = err.Error()
	}
	return ReviewMetrics{State: ReviewStateError, Freshness: fallbackFreshness(now), SparseReason: reason}
}

func GatedReviewQueue(reason string, limit int, now time.Time) ReviewQueueRead {
	limit = normalizeQueueLimit(limit)
	metrics := GatedReviewMetrics(reason, now)
	return ReviewQueueRead{
		Packets:   []ReviewPacketSummary{},
		Backlog:   ReviewQueueBacklog{State: ReviewStateGated, Limit: limit, SparseReason: metrics.SparseReason},
		Metrics:   metrics,
		Freshness: metrics.Freshness,
		State:     ReviewStateGated,
		Limit:     limit,
	}
}

func ErrorReviewQueue(err error, limit int, now time.Time) ReviewQueueRead {
	limit = normalizeQueueLimit(limit)
	metrics := ErrorReviewMetrics(err, now)
	return ReviewQueueRead{
		Packets:   []ReviewPacketSummary{},
		Backlog:   ReviewQueueBacklog{State: ReviewStateError, Limit: limit, SparseReason: metrics.SparseReason},
		Metrics:   metrics,
		Freshness: metrics.Freshness,
		State:     ReviewStateError,
		Limit:     limit,
	}
}

func normalizeQueueLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	return limit
}

func PacketSummaryFromCandidate(candidate *models.CrystallizationCandidate, now time.Time) ReviewPacketSummary {
	packet := FromCandidate(candidate)
	if candidate == nil {
		return ReviewPacketSummary{ReviewPacket: packet, PacketType: ReviewPacketTypeUsefulnessNoise, ConfidenceState: ReviewStateSparse, CandidateRefs: []string{}, ProvenanceCount: 0}
	}
	return ReviewPacketSummary{
		ReviewPacket:    packet,
		PacketID:        packet.PacketID,
		PacketType:      ReviewPacketTypeUsefulnessNoise,
		Recommendation:  recommendationForCandidate(candidate),
		ConfidenceState: confidenceState(candidate),
		QueueStatus:     queueStatus(candidate),
		CreatedAt:       candidateTime(candidate.CreatedAt, time.Time{}),
		Freshness:       candidateFreshness(candidate, now),
		CandidateRefs:   candidateRefs(candidate),
		ProvenanceCount: len(provenanceRefs(candidate)),
	}
}

func DetailFromCandidate(candidate *models.CrystallizationCandidate, now time.Time) ReviewPacketDetail {
	packet := FromCandidate(candidate)
	state := ReviewStateLive
	allowedActions := []string{ReviewActionPreserve, ReviewActionSuppress}
	if candidate == nil || candidate.Status != models.CandidateStatusPending {
		state = ReviewStateStalePacket
		allowedActions = []string{}
	}
	return ReviewPacketDetail{
		PacketDetail:   packet,
		Snapshot:       packet.Snapshot,
		Audit:          packet.Audit,
		Freshness:      candidateFreshness(candidate, now),
		State:          state,
		CandidateRefs:  candidateRefs(candidate),
		AffectedRefs:   affectedRefs(candidate),
		AllowedActions: allowedActions,
		MetricNotes:    metricNotes(candidate),
		ProvenanceRefs: provenanceRefs(candidate),
	}
}

func PreviewReviewAction(candidate *models.CrystallizationCandidate, packetID string, action string, now time.Time) (ReviewActionPreview, error) {
	candidateAction, err := ValidateReviewAction(candidate, packetID, action)
	if err != nil {
		return ReviewActionPreview{}, err
	}
	packet := FromCandidate(candidate)
	state := ReviewStateLive
	if candidate.Confidence > 0 && candidate.Confidence < 0.5 {
		state = ReviewStateRiskyConfirm
	}
	return ReviewActionPreview{
		ReviewPacket:         packet,
		Snapshot:             packet.Snapshot,
		Audit:                packet.Audit,
		ActionType:           NormalizeReviewActionMust(action),
		CandidateAction:      candidateAction,
		PacketID:             packet.PacketID,
		CandidateID:          candidate.ID,
		AffectedRefs:         affectedRefs(candidate),
		AuditExpectation:     fmt.Sprintf("candidate_review audit plus %s snapshot before %s", SnapshotStore, NormalizeReviewActionMust(action)),
		ConfirmationRequired: true,
		StaleMarker:          packet.PacketID,
		State:                state,
	}, nil
}

func ValidateReviewAction(candidate *models.CrystallizationCandidate, packetID string, action string) (string, error) {
	candidateAction, err := CandidateActionForReviewAction(action)
	if err != nil {
		return "", err
	}
	packetID = strings.TrimSpace(packetID)
	if packetID == "" {
		return "", fmt.Errorf("%w: packet_id is required", ErrInvalidReviewPacket)
	}
	if candidate == nil {
		return "", fmt.Errorf("%w: candidate is required", ErrInvalidReviewPacket)
	}
	packet := FromCandidate(candidate)
	if packet.PacketID != packetID {
		return "", fmt.Errorf("%w: current packet_id is %s", ErrStaleReviewPacket, packet.PacketID)
	}
	if candidate.Status != models.CandidateStatusPending {
		return "", fmt.Errorf("%w: packet is not pending (status=%s)", ErrStaleReviewPacket, candidate.Status)
	}
	if err := ValidateMutationBoundary(packet); err != nil {
		return "", err
	}
	return candidateAction, nil
}

func NewReviewActionReceipt(action string, packetID string, updated *models.CrystallizationCandidate, snapshot *models.BulkOpSnapshot, memory *models.Memory) ReviewActionReceipt {
	normalized, err := NormalizeReviewAction(action)
	if err != nil {
		normalized = strings.TrimSpace(action)
	}
	candidateAction, err := CandidateActionForReviewAction(normalized)
	if err != nil {
		candidateAction = ""
	}
	receipt := ReviewActionReceipt{
		ActionType:      normalized,
		CandidateAction: candidateAction,
		PacketID:        strings.TrimSpace(packetID),
		State:           ReviewStateLive,
		QueueDelta:      -1,
	}
	if updated != nil {
		updatedPacket := FromCandidate(updated)
		receipt.CandidateID = updated.ID
		receipt.UpdatedPacketID = updatedPacket.PacketID
		receipt.UpdatedPacketStatus = updatedPacket.Status
		receipt.AuditRef = fmt.Sprintf("candidate_review:%s:%d", normalized, updated.ID)
	}
	if snapshot != nil {
		receipt.SnapshotID = snapshot.SnapshotID
	}
	if memory != nil {
		receipt.MemoryID = memory.ID
	}
	return receipt
}

func NormalizeReviewAction(action string) (string, error) {
	switch strings.TrimSpace(action) {
	case ReviewActionPreserve:
		return ReviewActionPreserve, nil
	case ReviewActionSuppress:
		return ReviewActionSuppress, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedReviewAction, strings.TrimSpace(action))
	}
}

func NormalizeReviewActionMust(action string) string {
	normalized, err := NormalizeReviewAction(action)
	if err != nil {
		return strings.TrimSpace(action)
	}
	return normalized
}

func CandidateActionForReviewAction(action string) (string, error) {
	normalized, err := NormalizeReviewAction(action)
	if err != nil {
		return "", err
	}
	switch normalized {
	case ReviewActionPreserve:
		return "promote", nil
	case ReviewActionSuppress:
		return "reject", nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedReviewAction, normalized)
	}
}

func CandidateIDFromPacketID(packetID string) (int64, error) {
	parts := strings.Split(strings.TrimSpace(packetID), ":")
	if len(parts) < 3 || parts[0] != "candidate" {
		return 0, fmt.Errorf("%w: candidate packet_id must look like candidate:<id>:<fingerprint>", ErrInvalidReviewPacket)
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%w: invalid candidate id in packet_id", ErrInvalidReviewPacket)
	}
	return id, nil
}

func recommendationForCandidate(candidate *models.CrystallizationCandidate) string {
	if candidate == nil {
		return ""
	}
	switch candidate.Status {
	case models.CandidateStatusPending, models.CandidateStatusPromoted:
		return ReviewActionPreserve
	case models.CandidateStatusRejected, models.CandidateStatusDecayed:
		return ReviewActionSuppress
	default:
		return string(candidate.Status)
	}
}

func confidenceState(candidate *models.CrystallizationCandidate) string {
	if candidate == nil || candidate.Confidence <= 0 {
		return ReviewStateSparse
	}
	if candidate.Confidence < 0.5 {
		return ReviewStateRiskyConfirm
	}
	return "ready"
}

func queueStatus(candidate *models.CrystallizationCandidate) string {
	if candidate == nil {
		return ReviewStateSparse
	}
	if candidate.Status == models.CandidateStatusPending {
		return "ready"
	}
	return string(candidate.Status)
}

func candidateRefs(candidate *models.CrystallizationCandidate) []string {
	if candidate == nil || candidate.ID <= 0 {
		return []string{}
	}
	return []string{fmt.Sprintf("candidate:%d", candidate.ID)}
}

func affectedRefs(candidate *models.CrystallizationCandidate) []string {
	refs := candidateRefs(candidate)
	if candidate != nil && candidate.Status == models.CandidateStatusPending {
		refs = append(refs, "snapshot:"+SnapshotStore, "audit:"+AuditStore)
	}
	return refs
}

func provenanceRefs(candidate *models.CrystallizationCandidate) []CandidateEvidence {
	if candidate == nil {
		return []CandidateEvidence{}
	}
	refs := []CandidateEvidence{{Handle: fmt.Sprintf("candidate:%d", candidate.ID), Kind: "candidate"}}
	if strings.TrimSpace(candidate.SourceSessionID) != "" {
		refs = append(refs, CandidateEvidence{Handle: "session:" + strings.TrimSpace(candidate.SourceSessionID), Kind: "session"})
	}
	refs = append(refs, evidenceFromHandles(candidate.EvidenceHandles)...)
	refs = append(refs, CandidateEvidence{Handle: "snapshot:" + SnapshotStore + ":candidate_review_action", Kind: "snapshot"})
	refs = append(refs, CandidateEvidence{Handle: "audit:" + AuditStore + ":candidate_review", Kind: "audit"})
	return refs
}

func metricNotes(candidate *models.CrystallizationCandidate) []string {
	if candidate == nil {
		return []string{"candidate unavailable"}
	}
	notes := []string{"metrics are packet-local and bounded to candidate/snapshot/audit provenance"}
	if candidate.Confidence <= 0 {
		notes = append(notes, "confidence telemetry is sparse")
	}
	return notes
}

func candidateFreshness(candidate *models.CrystallizationCandidate, now time.Time) string {
	if candidate == nil {
		return fallbackFreshness(now)
	}
	if !candidate.UpdatedAt.IsZero() {
		return candidateTime(candidate.UpdatedAt, now)
	}
	return candidateTime(candidate.CreatedAt, now)
}

func freshestCandidateTime(candidates []*models.CrystallizationCandidate, now time.Time) string {
	var freshest time.Time
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		candidateTime := candidate.UpdatedAt
		if candidateTime.IsZero() {
			candidateTime = candidate.CreatedAt
		}
		if candidateTime.After(freshest) {
			freshest = candidateTime
		}
	}
	return candidateTime(freshest, now)
}

func candidateTime(value time.Time, fallback time.Time) string {
	if value.IsZero() {
		return fallbackFreshness(fallback)
	}
	return value.UTC().Format(time.RFC3339)
}

func fallbackFreshness(now time.Time) string {
	if now.IsZero() {
		return "unknown"
	}
	return now.UTC().Format(time.RFC3339)
}
