package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/reviewpacket"
	"github.com/thebtf/engram/pkg/models"
)

type reviewLoopCandidateLister interface {
	ListByStatus(ctx context.Context, project string, status models.CandidateStatus, limit int) ([]*models.CrystallizationCandidate, error)
}

func (s *Server) currentReviewLoopCandidateLister() reviewLoopCandidateLister {
	if s.reviewLoopCandidateStoreSeam != nil {
		return s.reviewLoopCandidateStoreSeam
	}
	if s.candidateStore == nil {
		return nil
	}
	return s.candidateStore
}

func reviewLoopCandidateTools() []Tool {
	return []Tool{
		{
			Name:        "review_metrics.read",
			Description: "CR-008: read honest usefulness/noise review metrics over candidate/snapshot/audit seams. Sparse telemetry is labeled, not inferred.",
			tier:        tierCore,
			InputSchema: reviewLoopReadSchema(false),
		},
		{
			Name:        "review_queue.read",
			Description: "CR-008: read a bounded packet-centric usefulness/noise review queue. Returns packets, backlog state, freshness, and provenance counts rather than raw candidate rows.",
			tier:        tierCore,
			InputSchema: reviewLoopReadSchema(true),
		},
		{
			Name:        "review_packet.detail",
			Description: "CR-008: read one review packet detail with candidate/snapshot/audit provenance and allowed preserve/suppress actions.",
			tier:        tierCore,
			InputSchema: reviewPacketIDSchema(false),
		},
		{
			Name:        "review_packet.preview_action",
			Description: "CR-008: preview preserve or suppress for a review packet. This tool is read-only and never mutates candidate state.",
			tier:        tierCore,
			InputSchema: reviewPacketIDSchema(true),
		},
		{
			Name:        "review_packet.apply_action",
			Description: "CR-008: explicitly apply a previously previewed preserve/suppress action using snapshot/audit-aware candidate mutation paths.",
			tier:        tierCore,
			InputSchema: reviewPacketIDSchema(true),
		},
	}
}

func reviewLoopReadSchema(includeQueueFilters bool) map[string]any {
	properties := map[string]any{
		"project": map[string]any{"type": "string", "description": "Optional project slug; omit or use all for unscoped review queue."},
		"status":  map[string]any{"type": "string", "enum": []string{"pending", "promoted", "rejected", "superseded", "decayed"}, "description": "Candidate status backing the review packets. Defaults to pending."},
		"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Bounded page size. Defaults to 20, max 100."},
	}
	if includeQueueFilters {
		properties["packet_type"] = map[string]any{"type": "string", "description": "Optional packet type. CR-008 supports usefulness_noise/candidate_review only."}
		properties["risky_only"] = map[string]any{"type": "boolean", "description": "When true, returns risky packets only if confidence telemetry is complete; otherwise returns a gated state."}
	}
	return map[string]any{"type": "object", "properties": properties}
}

func reviewPacketIDSchema(includeAction bool) map[string]any {
	properties := map[string]any{
		"packet_id": map[string]any{"type": "string", "description": "Required review packet id, e.g. candidate:42:abcdef."},
	}
	required := []string{"packet_id"}
	if includeAction {
		properties["action_type"] = map[string]any{"type": "string", "enum": []string{reviewpacket.ReviewActionPreserve, reviewpacket.ReviewActionSuppress}, "description": "Required CR-008 action."}
		properties["reason"] = map[string]any{"type": "string", "description": "Optional operator reason for suppress."}
		required = append(required, "action_type")
	}
	return map[string]any{"type": "object", "required": required, "properties": properties}
}

func (s *Server) handleReviewMetricsRead(ctx context.Context, args json.RawMessage) (string, error) {
	if !vnextFEnabled() {
		return "", fmt.Errorf("review_metrics.read requires ENGRAM_VNEXT_F_ENABLED=true")
	}
	store := s.currentReviewLoopCandidateLister()
	if store == nil {
		return "", fmt.Errorf("review_metrics.read requires candidateStore to be wired")
	}
	project, status, limit, _, _, err := parseReviewLoopReadArgs(args)
	if err != nil {
		return "", err
	}
	candidates, err := store.ListByStatus(ctx, project, status, limit)
	if err != nil {
		return marshalReviewLoop("review_metrics.read", reviewpacket.ErrorReviewMetrics(err, time.Now().UTC()))
	}
	return marshalReviewLoop("review_metrics.read", reviewpacket.BuildReviewMetrics(candidates, limit, time.Now().UTC()))
}

func (s *Server) handleReviewQueueRead(ctx context.Context, args json.RawMessage) (string, error) {
	if !vnextFEnabled() {
		return "", fmt.Errorf("review_queue.read requires ENGRAM_VNEXT_F_ENABLED=true")
	}
	store := s.currentReviewLoopCandidateLister()
	if store == nil {
		return "", fmt.Errorf("review_queue.read requires candidateStore to be wired")
	}
	project, status, limit, packetType, riskyOnly, err := parseReviewLoopReadArgs(args)
	if err != nil {
		return "", err
	}
	if !reviewLoopMCPPacketTypeSupported(packetType) {
		return marshalReviewLoop("review_queue.read", reviewpacket.GatedReviewQueue("unsupported packet_type for CR-008 review queue", limit, time.Now().UTC()))
	}
	candidates, err := store.ListByStatus(ctx, project, status, limit)
	if err != nil {
		return marshalReviewLoop("review_queue.read", reviewpacket.ErrorReviewQueue(err, limit, time.Now().UTC()))
	}
	now := time.Now().UTC()
	metrics := reviewpacket.BuildReviewMetrics(candidates, limit, now)
	if riskyOnly {
		if strings.Contains(metrics.SparseReason, "confidence telemetry") {
			return marshalReviewLoop("review_queue.read", reviewpacket.GatedReviewQueue("risky_only requires complete confidence telemetry", limit, now))
		}
		candidates = filterRiskyMCPReviewCandidates(candidates)
	}
	return marshalReviewLoop("review_queue.read", reviewpacket.BuildReviewQueueWithMetrics(candidates, status, limit, now, metrics))
}

func (s *Server) handleReviewPacketDetail(ctx context.Context, args json.RawMessage) (string, error) {
	candidate, packetID, err := s.loadReviewPacketCandidate(ctx, args)
	if err != nil {
		return "", err
	}
	if reviewpacket.FromCandidate(candidate).PacketID != packetID {
		return "", fmt.Errorf("%w: current packet_id is %s", reviewpacket.ErrStaleReviewPacket, reviewpacket.FromCandidate(candidate).PacketID)
	}
	return marshalReviewLoop("review_packet.detail", reviewpacket.DetailFromCandidate(candidate, time.Now().UTC()))
}

func (s *Server) handleReviewPacketPreviewAction(ctx context.Context, args json.RawMessage) (string, error) {
	action, err := reviewLoopActionFromArgs(args)
	if err != nil {
		return "", err
	}
	candidate, packetID, err := s.loadReviewPacketCandidate(ctx, args)
	if err != nil {
		return "", err
	}
	preview, err := reviewpacket.PreviewReviewAction(candidate, packetID, action, time.Now().UTC())
	if err != nil {
		return "", err
	}
	return marshalReviewLoop("review_packet.preview_action", preview)
}

func (s *Server) handleReviewPacketApplyAction(ctx context.Context, args json.RawMessage) (string, error) {
	action, err := reviewLoopActionFromArgs(args)
	if err != nil {
		return "", err
	}
	candidate, packetID, err := s.loadReviewPacketCandidate(ctx, args)
	if err != nil {
		return "", err
	}
	if _, err := reviewpacket.PreviewReviewAction(candidate, packetID, action, time.Now().UTC()); err != nil {
		return "", err
	}
	switch action {
	case reviewpacket.ReviewActionPreserve:
		return s.applyReviewPacketPreserve(ctx, candidate, packetID)
	case reviewpacket.ReviewActionSuppress:
		return s.applyReviewPacketSuppress(ctx, candidate, packetID, reviewLoopReasonFromArgs(args))
	default:
		return "", fmt.Errorf("%w: %s", reviewpacket.ErrUnsupportedReviewAction, action)
	}
}

func parseReviewLoopReadArgs(args json.RawMessage) (string, models.CandidateStatus, int, string, bool, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", "", 0, "", false, err
	}
	project := coerceString(m["project"], "")
	if strings.EqualFold(project, "all") || project == "*" {
		project = ""
	}
	status := models.CandidateStatus(coerceString(m["status"], "pending"))
	if !status.IsValid() {
		return "", "", 0, "", false, fmt.Errorf("review_loop: invalid status %q", status)
	}
	limit := coerceInt(m["limit"], 20)
	if limit <= 0 {
		return "", "", 0, "", false, fmt.Errorf("review_loop: limit must be positive")
	}
	if limit > 100 {
		return "", "", 0, "", false, fmt.Errorf("review_loop: limit must not exceed 100")
	}
	return project, status, limit, coerceString(m["packet_type"], ""), coerceBool(m["risky_only"], false), nil
}

func reviewLoopMCPPacketTypeSupported(packetType string) bool {
	packetType = strings.TrimSpace(packetType)
	return packetType == "" || packetType == reviewpacket.ReviewPacketTypeUsefulnessNoise || packetType == reviewpacket.CandidatePacketKind
}

func reviewLoopActionFromArgs(args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	return reviewpacket.NormalizeReviewAction(coerceString(m["action_type"], ""))
}

func reviewLoopReasonFromArgs(args json.RawMessage) string {
	m, err := parseArgs(args)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(coerceString(m["reason"], ""))
}

func (s *Server) loadReviewPacketCandidate(ctx context.Context, args json.RawMessage) (*models.CrystallizationCandidate, string, error) {
	if !vnextFEnabled() || s.candidateStore == nil {
		return nil, "", fmt.Errorf("review_packet requires ENGRAM_VNEXT_F_ENABLED=true")
	}
	m, err := parseArgs(args)
	if err != nil {
		return nil, "", err
	}
	packetID := strings.TrimSpace(coerceString(m["packet_id"], ""))
	candidateID, err := reviewpacket.CandidateIDFromPacketID(packetID)
	if err != nil {
		return nil, "", err
	}
	candidate, err := s.candidateStore.Get(ctx, candidateID)
	if err != nil {
		return nil, "", fmt.Errorf("review_packet get candidate %d: %w", candidateID, err)
	}
	if candidate == nil {
		return nil, "", fmt.Errorf("review_packet: candidate %d not found", candidateID)
	}
	return candidate, packetID, nil
}

func (s *Server) applyReviewPacketPreserve(ctx context.Context, candidate *models.CrystallizationCandidate, packetID string) (string, error) {
	memory, err := reviewLoopMemoryFromCandidate(candidate)
	if err != nil {
		return "", err
	}
	snapshot, err := s.newCandidateReviewSnapshot(reviewpacket.ReviewActionPreserve, candidate)
	if err != nil {
		return "", err
	}
	if snapshot != nil && s.snapshotStore == nil {
		return "", fmt.Errorf("review_packet.apply_action preserve: snapshot store not available")
	}
	updated, created, createdSnapshot, err := s.candidateStore.PreserveWithMemoryAndSnapshot(ctx, s.snapshotStore, candidate.ID, memory, snapshot, "system")
	if err != nil {
		if errors.Is(err, gormdb.ErrInvalidTransition) {
			return "", fmt.Errorf("review_packet.apply_action preserve: %w", err)
		}
		return "", fmt.Errorf("review_packet.apply_action preserve %d: %w", candidate.ID, err)
	}
	return marshalReviewLoop("review_packet.apply_action", reviewpacket.NewReviewActionReceipt(reviewpacket.ReviewActionPreserve, packetID, updated, createdSnapshot, created))
}

func (s *Server) applyReviewPacketSuppress(ctx context.Context, candidate *models.CrystallizationCandidate, packetID string, reason string) (string, error) {
	snapshot, err := s.newCandidateReviewSnapshot(reviewpacket.ReviewActionSuppress, candidate)
	if err != nil {
		return "", err
	}
	if snapshot != nil && s.snapshotStore == nil {
		return "", fmt.Errorf("review_packet.apply_action suppress: snapshot store not available")
	}
	updated, createdSnapshot, err := s.candidateStore.TransitionToSuppressedWithSnapshot(ctx, s.snapshotStore, candidate.ID, reason, snapshot, "system")
	if err != nil {
		if errors.Is(err, gormdb.ErrInvalidTransition) {
			return "", fmt.Errorf("review_packet.apply_action suppress: %w", err)
		}
		return "", fmt.Errorf("review_packet.apply_action suppress %d: %w", candidate.ID, err)
	}
	return marshalReviewLoop("review_packet.apply_action", reviewpacket.NewReviewActionReceipt(reviewpacket.ReviewActionSuppress, packetID, updated, createdSnapshot, nil))
}

func reviewLoopMemoryFromCandidate(candidate *models.CrystallizationCandidate) (*models.Memory, error) {
	if candidate == nil {
		return nil, fmt.Errorf("candidate is required")
	}
	project := ""
	for _, affectedProject := range candidate.AffectedProjects {
		project = strings.TrimSpace(affectedProject)
		if project != "" {
			break
		}
	}
	if project == "" {
		return nil, fmt.Errorf("candidate has no affected project")
	}
	return &models.Memory{
		Content:       candidate.ProposedContent,
		Project:       project,
		Tier:          candidate.ProposedTier,
		EpistemicType: "decision",
		Tags:          []string{fmt.Sprintf("candidate:%d", candidate.ID), "crystallized"},
		SourceAgent:   "crystallization",
	}, nil
}

func filterRiskyMCPReviewCandidates(candidates []*models.CrystallizationCandidate) []*models.CrystallizationCandidate {
	filtered := make([]*models.CrystallizationCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate != nil && candidate.Confidence > 0 && candidate.Confidence < 0.5 {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func marshalReviewLoop(operation string, payload any) (string, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%s marshal: %w", operation, err)
	}
	return string(b), nil
}
