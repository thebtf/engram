// Package mcp — crystallization candidate tools (Milestone-F TG4 T026).
// Exposed when ENGRAM_VNEXT_F_ENABLED=true and candidateStore is non-nil.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// candidateTools returns the 4 crystallization candidate MCP tool definitions.
// Only registered when ENGRAM_VNEXT_F_ENABLED=true.
func candidateTools() []Tool {
	return []Tool{
		{
			Name:        "list_candidates",
			Description: "List crystallization candidates. Requires ENGRAM_VNEXT_F_ENABLED=true. Returns candidates filtered by project and optional status. Use promote_candidate / reject_candidate to act on results.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{},
				"properties": map[string]any{
					"project": map[string]any{
						"type":        "string",
						"description": "Filter by project slug. Empty returns all projects.",
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
	status := coerceString(m["status"], "pending")
	limit := coerceInt(m["limit"], 20)
	if limit > 100 {
		limit = 100
	}

	candidates, err := s.candidateStore.ListByStatus(ctx, project, models.CandidateStatus(status), limit)
	if err != nil {
		return "", fmt.Errorf("list_candidates: %w", err)
	}

	type candidateItem struct {
		ID                      int64   `json:"id"`
		Status                  string  `json:"status"`
		ProposedContent         string  `json:"proposed_content"`
		ProposedPromotionTarget string  `json:"proposed_promotion_target"`
		ProposedTier            string  `json:"proposed_tier"`
		ProposedEpistemicType   string  `json:"proposed_epistemic_type"`
		SourceSessionID         string  `json:"source_session_id"`
		Confidence              float32 `json:"confidence"`
		RecurrenceCount         int     `json:"recurrence_count"`
		Fingerprint             string  `json:"fingerprint,omitempty"`
		CreatedAt               string  `json:"created_at"`
	}

	items := make([]candidateItem, len(candidates))
	for i, c := range candidates {
		items[i] = candidateItem{
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

// handlePromoteCandidate implements the promote_candidate MCP tool.
// It creates a memory with epistemic_type=decision from the candidate content,
// then transitions the candidate to status='promoted' with a back-reference to the new memory.
func (s *Server) handlePromoteCandidate(ctx context.Context, args json.RawMessage) (string, error) {
	if !vnextFEnabled() || s.candidateStore == nil {
		return "", fmt.Errorf("promote_candidate requires ENGRAM_VNEXT_F_ENABLED=true")
	}
	if s.memoryStore == nil {
		return "", fmt.Errorf("promote_candidate: memory store not ready")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	id := coerceInt64(m["id"], 0)
	if id <= 0 {
		return "", fmt.Errorf("promote_candidate: id is required")
	}

	// Load candidate.
	candidate, err := s.candidateStore.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("promote_candidate get %d: %w", id, err)
	}
	if candidate.Status != models.CandidateStatusPending {
		return "", fmt.Errorf("promote_candidate: candidate %d is not pending (status=%s)", id, candidate.Status)
	}

	// Build a memory from the candidate's proposed content.
	// epistemic_type="decision" per spec §FR-F4 promotion semantics.
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
		SourceAgent:   "promote_candidate",
	}

	// Use CreateWithLifecycle (gated path; this tool is only callable when ENGRAM_VNEXT_F_ENABLED=true).
	created, err := s.memoryStore.CreateWithLifecycle(ctx, mem)
	if err != nil {
		return "", fmt.Errorf("promote_candidate create memory: %w", err)
	}

	// Transition candidate to promoted.
	updated, err := s.candidateStore.TransitionToPromoted(ctx, id, created.ID)
	if err != nil {
		// Memory was created but transition failed — log and surface the error.
		// Caller can retry; idempotent: TransitionToPromoted with row-lock will reject
		// if already promoted.
		return "", fmt.Errorf("promote_candidate transition %d: %w (memory %d created)", id, err, created.ID)
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

	updated, err := s.candidateStore.TransitionToRejected(ctx, id, reason)
	if err != nil {
		if errors.Is(err, gormdb.ErrInvalidTransition) {
			return "", fmt.Errorf("reject_candidate: %w", err)
		}
		return "", fmt.Errorf("reject_candidate %d: %w", id, err)
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

	updated, err := s.candidateStore.TransitionToSuperseded(ctx, id)
	if err != nil {
		if errors.Is(err, gormdb.ErrInvalidTransition) {
			return "", fmt.Errorf("supersede_candidate: %w", err)
		}
		return "", fmt.Errorf("supersede_candidate %d: %w", id, err)
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
