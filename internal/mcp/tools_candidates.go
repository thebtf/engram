// Package mcp — crystallization candidate tools (Milestone-F TG4 T026).
// Exposed when ENGRAM_VNEXT_F_ENABLED=true and candidateStore is non-nil.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/reviewpacket"
	"github.com/thebtf/engram/pkg/models"
)

type candidateItem struct {
	ReviewPacket            reviewpacket.CandidateReviewPacket `json:"review_packet"`
	ID                      int64                              `json:"id"`
	Status                  string                             `json:"status"`
	ProposedContent         string                             `json:"proposed_content"`
	ProposedPromotionTarget string                             `json:"proposed_promotion_target"`
	ProposedTier            string                             `json:"proposed_tier"`
	ProposedEpistemicType   string                             `json:"proposed_epistemic_type"`
	SourceSessionID         string                             `json:"source_session_id"`
	Confidence              float32                            `json:"confidence"`
	RecurrenceCount         int                                `json:"recurrence_count"`
	Fingerprint             string                             `json:"fingerprint,omitempty"`
	CreatedAt               string                             `json:"created_at"`
}

func candidateItemFromDomain(c *models.CrystallizationCandidate) candidateItem {
	if c == nil {
		return candidateItem{}
	}
	return candidateItem{
		ReviewPacket:            reviewpacket.FromCandidate(c),
		ID:                      c.ID,
		Status:                  string(c.Status),
		ProposedContent:         c.ProposedContent,
		ProposedPromotionTarget: c.ProposedPromotionTarget,
		ProposedTier:            c.ProposedTier,
		ProposedEpistemicType:   c.ProposedEpistemicType,
		SourceSessionID:         c.SourceSessionID,
		Confidence:              c.Confidence,
		RecurrenceCount:         c.RecurrenceCount,
		Fingerprint:             c.Fingerprint,
		CreatedAt:               c.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func (s *Server) newCandidateReviewSnapshot(action string, candidate *models.CrystallizationCandidate) (*models.BulkOpSnapshot, error) {
	packet := reviewpacket.FromCandidate(candidate)
	if !packet.MutationRequirements.SnapshotRequired {
		return nil, nil
	}
	return reviewpacket.NewCandidateReviewActionSnapshot(action, candidate, "system")
}

// candidateTools returns the 5 crystallization candidate MCP tool definitions.
// Only registered when ENGRAM_VNEXT_F_ENABLED=true.
func candidateTools() []Tool {
	return []Tool{
		{
			Name:        "list_candidates",
			Description: "List crystallization candidates. Requires ENGRAM_VNEXT_F_ENABLED=true. Returns candidates filtered by project and optional status. Use promote_candidate / reject_candidate to act on results.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"project"},
				"properties": map[string]any{
					"project": map[string]any{
						"type":        "string",
						"description": "REQUIRED. Filter by project slug.",
					},
					"status": map[string]any{
						"type":        "string",
						"enum":        []string{"pending", "promoted", "rejected", "superseded", "decayed"},
						"description": "Filter by candidate status. Defaults to 'pending'.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     100,
						"description": "Max results to return (default 20, max 100).",
					},
				},
			},
		},
		{
			Name:        "get_candidate",
			Description: "Read one crystallization candidate review packet by id. Requires ENGRAM_VNEXT_F_ENABLED=true. Returns the same packet-centric payload shape used by list_candidates.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "integer",
						"description": "REQUIRED. Candidate ID to read.",
					},
				},
			},
		},
		{
			Name:        "promote_candidate",
			Description: "Promote a crystallization candidate to a full memory (epistemic_type=decision). Requires ENGRAM_VNEXT_F_ENABLED=true. Transitions the candidate to status='promoted' and creates a new memory row with a back-reference to the candidate.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "integer",
						"description": "REQUIRED. Candidate ID to promote.",
					},
				},
			},
		},
		{
			Name:        "reject_candidate",
			Description: "Reject a crystallization candidate, marking it as not suitable for promotion. Requires ENGRAM_VNEXT_F_ENABLED=true. Transitions the candidate to status='rejected'.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "integer",
						"description": "REQUIRED. Candidate ID to reject.",
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Optional rejection reason stored in the audit log.",
					},
				},
			},
		},
		{
			Name:        "supersede_candidate",
			Description: "Mark a crystallization candidate as superseded by a newer candidate. Requires ENGRAM_VNEXT_F_ENABLED=true. Transitions the candidate to status='superseded'.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "integer",
						"description": "REQUIRED. Candidate ID to supersede.",
					},
				},
			},
		},
	}
}

// handleListCandidates implements the list_candidates MCP tool.
func (s *Server) handleListCandidates(ctx context.Context, args json.RawMessage) (string, error) {
	if !vnextFEnabled() || s.candidateStore == nil {
		return "", fmt.Errorf("list_candidates requires ENGRAM_VNEXT_F_ENABLED=true")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	project := coerceString(m["project"], "")
	if project == "" {
		return "", fmt.Errorf("list_candidates: project is required")
	}
	status := coerceString(m["status"], "pending")
	limit := coerceInt(m["limit"], 20)
	if limit > 100 {
		limit = 100
	}

	candidates, err := s.candidateStore.ListByStatus(ctx, project, models.CandidateStatus(status), limit)
	if err != nil {
		return "", fmt.Errorf("list_candidates: %w", err)
	}

	items := make([]candidateItem, 0, len(candidates))
	for _, c := range candidates {
		if c == nil {
			continue
		}
		items = append(items, candidateItemFromDomain(c))
	}

	out := map[string]any{
		"candidates": items,
		"count":      len(items),
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("list_candidates marshal: %w", err)
	}
	return string(b), nil
}

// handleGetCandidate implements the get_candidate MCP tool.
func (s *Server) handleGetCandidate(ctx context.Context, args json.RawMessage) (string, error) {
	if !vnextFEnabled() || s.candidateStore == nil {
		return "", fmt.Errorf("get_candidate requires ENGRAM_VNEXT_F_ENABLED=true")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	id := coerceInt64(m["id"], 0)
	if id <= 0 {
		return "", fmt.Errorf("get_candidate: id is required")
	}

	candidate, err := s.candidateStore.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get_candidate %d: %w", id, err)
	}
	if candidate == nil {
		return "", fmt.Errorf("get_candidate: candidate %d not found", id)
	}

	b, err := json.Marshal(candidateItemFromDomain(candidate))
	if err != nil {
		return "", fmt.Errorf("get_candidate marshal: %w", err)
	}
	return string(b), nil
}

// handlePromoteCandidate implements the promote_candidate MCP tool.
// It creates a memory with epistemic_type=decision from the candidate content,
// then transitions the candidate to status='promoted' with a back-reference to the new memory.
func (s *Server) handlePromoteCandidate(ctx context.Context, args json.RawMessage) (string, error) {
	if !vnextFEnabled() {
		return "", fmt.Errorf("promote_candidate requires ENGRAM_VNEXT_F_ENABLED=true")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	id := coerceInt64(m["id"], 0)
	if id <= 0 {
		return "", fmt.Errorf("promote_candidate: id is required")
	}

	// T044 dry-run early return (FR-F6.b): before any DB access.
	// When candidateStore is nil, dry_run returns a preview with the id only
	// (TG5-absent nil-safe seam — no live candidate data available).
	dryRun := coerceBool(m["dry_run"], false)
	if dryRun {
		preview := map[string]any{
			"dry_run":      true,
			"candidate_id": id,
			"note":         "dry_run preview — no promotion executed",
		}
		// If we have a live candidate store, enrich the preview.
		if s.candidateStore != nil {
			if c, getErr := s.candidateStore.Get(ctx, id); getErr == nil && c != nil {
				preview["proposed_content"] = c.ProposedContent
				preview["status"] = string(c.Status)
				preview["proposed_tier"] = c.ProposedTier
				preview["review_packet"] = reviewpacket.FromCandidate(c)
			}
		}
		b, jsonErr := json.Marshal(preview)
		if jsonErr != nil {
			return "", fmt.Errorf("promote_candidate dry_run marshal: %w", jsonErr)
		}
		return string(b), nil
	}

	// Non-dry-run requires candidateStore.
	if s.candidateStore == nil {
		return "", fmt.Errorf("promote_candidate requires candidateStore to be wired")
	}

	// Load candidate.
	candidate, err := s.candidateStore.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("promote_candidate get %d: %w", id, err)
	}
	if candidate == nil {
		return "", fmt.Errorf("promote_candidate: candidate %d not found", id)
	}
	if candidate.Status != models.CandidateStatusPending {
		return "", fmt.Errorf("promote_candidate: candidate %d is not pending (status=%s)", id, candidate.Status)
	}
	if err := reviewpacket.ValidateCandidateMutation(candidate); err != nil {
		return "", fmt.Errorf("promote_candidate: %w", err)
	}
	snapshot, err := s.newCandidateReviewSnapshot("promote", candidate)
	if err != nil {
		return "", fmt.Errorf("promote_candidate: %w", err)
	}

	// Build a memory from the candidate's proposed content.
	// epistemic_type="decision" per spec §FR-F4 promotion semantics.
	// source_agent="crystallization" per FR-F4 (NIT-8 review finding).
	// Back-reference to the candidate via tag "candidate:<id>".
	project := ""
	if len(candidate.AffectedProjects) > 0 {
		project = candidate.AffectedProjects[0]
	}
	mem := &models.Memory{
		Content:       candidate.ProposedContent,
		Project:       project,
		Tier:          candidate.ProposedTier,
		EpistemicType: "decision",
		Tags:          []string{fmt.Sprintf("candidate:%d", id), "crystallized"},
		SourceAgent:   "crystallization",
	}

	// PromoteWithMemoryAndSnapshot creates the snapshot, creates the memory,
	// transitions the candidate, and amends the snapshot in one DB transaction.
	// A snapshot-amend failure rolls back the committed promotion as well, so the
	// rollback snapshot cannot be left incomplete after an error.
	updated, created, _, err := s.candidateStore.PromoteWithMemoryAndSnapshot(ctx, s.snapshotStore, id, mem, snapshot, "system")
	if err != nil {
		if errors.Is(err, gormdb.ErrInvalidTransition) {
			return "", fmt.Errorf("promote_candidate: %w", err)
		}
		return "", fmt.Errorf("promote_candidate %d: %w", id, err)
	}
	if updated == nil || created == nil {
		return "", fmt.Errorf("promote_candidate %d: promotion returned nil candidate or memory", id)
	}

	out := map[string]any{
		"candidate_id":       updated.ID,
		"candidate_status":   string(updated.Status),
		"memory_id":          created.ID,
		"promoted_memory_id": updated.PromotedMemoryID,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("promote_candidate marshal: %w", err)
	}
	return string(b), nil
}

// handleRejectCandidate implements the reject_candidate MCP tool.
func (s *Server) handleRejectCandidate(ctx context.Context, args json.RawMessage) (string, error) {
	if !vnextFEnabled() || s.candidateStore == nil {
		return "", fmt.Errorf("reject_candidate requires ENGRAM_VNEXT_F_ENABLED=true")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	id := coerceInt64(m["id"], 0)
	if id <= 0 {
		return "", fmt.Errorf("reject_candidate: id is required")
	}
	reason := coerceString(m["reason"], "")
	candidate, err := s.candidateStore.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("reject_candidate get %d: %w", id, err)
	}
	if candidate == nil {
		return "", fmt.Errorf("reject_candidate: candidate %d not found", id)
	}
	if err := reviewpacket.ValidateCandidateMutation(candidate); err != nil {
		return "", fmt.Errorf("reject_candidate: %w", err)
	}
	snapshot, err := s.newCandidateReviewSnapshot("reject", candidate)
	if err != nil {
		return "", fmt.Errorf("reject_candidate: %w", err)
	}

	updated, _, err := s.candidateStore.TransitionToRejectedWithSnapshot(ctx, s.snapshotStore, id, reason, snapshot, "system")
	if err != nil {
		if errors.Is(err, gormdb.ErrInvalidTransition) {
			return "", fmt.Errorf("reject_candidate: %w", err)
		}
		return "", fmt.Errorf("reject_candidate %d: %w", id, err)
	}
	if updated == nil {
		return "", fmt.Errorf("reject_candidate %d: transition returned nil candidate", id)
	}

	out := map[string]any{
		"candidate_id":     updated.ID,
		"candidate_status": string(updated.Status),
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("reject_candidate marshal: %w", err)
	}
	return string(b), nil
}

// handleSupersedeCandidate implements the supersede_candidate MCP tool.
func (s *Server) handleSupersedeCandidate(ctx context.Context, args json.RawMessage) (string, error) {
	if !vnextFEnabled() || s.candidateStore == nil {
		return "", fmt.Errorf("supersede_candidate requires ENGRAM_VNEXT_F_ENABLED=true")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	id := coerceInt64(m["id"], 0)
	if id <= 0 {
		return "", fmt.Errorf("supersede_candidate: id is required")
	}
	candidate, err := s.candidateStore.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("supersede_candidate get %d: %w", id, err)
	}
	if candidate == nil {
		return "", fmt.Errorf("supersede_candidate: candidate %d not found", id)
	}
	if err := reviewpacket.ValidateCandidateMutation(candidate); err != nil {
		return "", fmt.Errorf("supersede_candidate: %w", err)
	}
	snapshot, err := s.newCandidateReviewSnapshot("supersede", candidate)
	if err != nil {
		return "", fmt.Errorf("supersede_candidate: %w", err)
	}

	updated, _, err := s.candidateStore.TransitionToSupersededWithSnapshot(ctx, s.snapshotStore, id, snapshot, "system")
	if err != nil {
		if errors.Is(err, gormdb.ErrInvalidTransition) {
			return "", fmt.Errorf("supersede_candidate: %w", err)
		}
		return "", fmt.Errorf("supersede_candidate %d: %w", id, err)
	}
	if updated == nil {
		return "", fmt.Errorf("supersede_candidate %d: transition returned nil candidate", id)
	}

	out := map[string]any{
		"candidate_id":     updated.ID,
		"candidate_status": string(updated.Status),
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("supersede_candidate marshal: %w", err)
	}
	return string(b), nil
}
