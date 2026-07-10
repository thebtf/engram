// Package mcp — tools_bulkops.go implements bulk_promote, bulk_delete,
// bulk_supersede MCP tools (Milestone-F TG6 T044).
//
// All 3 tools are admin-gated and support dry_run=true for zero-side-effect
// previews per spec §FR-F6.b.
//
// Dry-run nil-safe seam (TG5 absent): when bulkFacade is nil AND dry_run=true,
// the handler computes would_affect from normalized candidate IDs and returns
// immediately — no DB read, no write. When dry_run=false and facade is nil,
// an error is returned (operation not available).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/bulkops"
	"github.com/thebtf/engram/pkg/models"
)

// bulkOpsTools returns MCP tool definitions for bulk_promote, bulk_delete, bulk_supersede.
// These are admin-only tools advertised only when ENGRAM_VNEXT_F_ENABLED=true.
func bulkOpsTools() []Tool {
	return []Tool{
		{
			Name:        "bulk_promote",
			Description: "Bulk-promote crystallization candidates to memories. Admin-only. Captures a pre-op snapshot for rollback. dry_run=true returns would_affect count without any DB writes.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"candidate_ids"},
				"properties": map[string]any{
					"candidate_ids": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "integer"},
						"description": "List of candidate IDs to promote.",
					},
					"dry_run": map[string]any{
						"type":        "boolean",
						"description": "If true, return would_affect count without any writes (default false).",
					},
				},
			},
		},
		{
			Name:        "bulk_delete",
			Description: "Bulk-delete memories. Admin-only. Captures a pre-op snapshot for rollback. dry_run=true returns would_affect count without any DB writes.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"memory_ids"},
				"properties": map[string]any{
					"memory_ids": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "integer"},
						"description": "List of memory IDs to delete.",
					},
					"dry_run": map[string]any{
						"type":        "boolean",
						"description": "If true, return would_affect count without any writes (default false).",
					},
				},
			},
		},
		{
			Name:        "bulk_supersede",
			Description: "Bulk-supersede memories. Admin-only. Captures a pre-op snapshot for rollback. dry_run=true returns would_affect count without any DB writes.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"memory_ids"},
				"properties": map[string]any{
					"memory_ids": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "integer"},
						"description": "List of memory IDs to supersede.",
					},
					"dry_run": map[string]any{
						"type":        "boolean",
						"description": "If true, return would_affect count without any writes (default false).",
					},
				},
			},
		},
	}
}

// handleBulkPromote promotes a list of crystallization candidates to memories.
//
// Admin gate: non-admin callers receive admin_required error.
// Dry-run nil-safe seam: when bulkFacade is nil and dry_run=true, returns the
// same sorted unique non-zero candidate count as the facade — zero DB reads or writes.
func (s *Server) handleBulkPromote(ctx context.Context, args json.RawMessage) (string, error) {
	if !vnextFEnabled() {
		return "", fmt.Errorf("bulk_promote: requires ENGRAM_VNEXT_F_ENABLED=true")
	}
	identity, ok := auth.IdentityFrom(ctx)
	if !ok || !identity.IsAdmin() {
		return "", fmt.Errorf("admin_required: bulk_promote requires admin identity")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	candidateIDs := bulkops.NormalizeCandidateIDs(coerceInt64Slice(m["candidate_ids"]))
	dryRun := coerceBool(m["dry_run"], false)

	// Nil-safe TG5-absent dry-run seam: when facade is nil and dry_run=true,
	// return a preview using the facade's normalized ID contract — no DB access.
	if dryRun && s.bulkFacade == nil {
		out := map[string]any{
			"dry_run":      true,
			"would_affect": len(candidateIDs),
			"note":         "bulk_promote preview (facade not wired — normalized input only)",
		}
		return marshalJSON(out)
	}

	if s.bulkFacade == nil {
		return "", fmt.Errorf("bulk_promote: facade not available — set ENGRAM_VNEXT_F_ENABLED=true and wire stores")
	}

	op := bulkops.BulkOp{
		Type:         models.SnapshotOpBulkPromote,
		CandidateIDs: candidateIDs,
		DryRun:       dryRun,
		Actor:        resolveGovernanceActor(identity),
	}
	result, err := s.bulkFacade.Execute(ctx, identity, op)
	if err != nil {
		return "", fmt.Errorf("bulk_promote: %w", err)
	}

	out := map[string]any{
		"dry_run":        result.DryRun,
		"would_affect":   result.WouldAffect,
		"affected_count": result.AffectedCount,
		"snapshot_id":    result.SnapshotID,
		"promoted":       result.Promoted,
		"errors":         result.Errors,
	}
	return marshalJSON(out)
}

// handleBulkDelete deletes a list of memories.
//
// Admin gate: non-admin callers receive admin_required error.
// Dry-run nil-safe seam: when bulkFacade is nil and dry_run=true, returns
// would_affect from len(memory_ids) — zero DB reads or writes.
func (s *Server) handleBulkDelete(ctx context.Context, args json.RawMessage) (string, error) {
	if !vnextFEnabled() {
		return "", fmt.Errorf("bulk_delete: requires ENGRAM_VNEXT_F_ENABLED=true")
	}
	identity, ok := auth.IdentityFrom(ctx)
	if !ok || !identity.IsAdmin() {
		return "", fmt.Errorf("admin_required: bulk_delete requires admin identity")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	memoryIDs := coerceInt64Slice(m["memory_ids"])
	dryRun := coerceBool(m["dry_run"], false)

	// Nil-safe TG5-absent dry-run seam.
	if dryRun && s.bulkFacade == nil {
		out := map[string]any{
			"dry_run":      true,
			"would_affect": len(memoryIDs),
			"note":         "bulk_delete preview (facade not wired — would_affect from input only)",
		}
		return marshalJSON(out)
	}

	if s.bulkFacade == nil {
		return "", fmt.Errorf("bulk_delete: facade not available — set ENGRAM_VNEXT_F_ENABLED=true and wire stores")
	}

	op := bulkops.BulkOp{
		Type:      models.SnapshotOpBulkDelete,
		MemoryIDs: memoryIDs,
		DryRun:    dryRun,
		Actor:     resolveGovernanceActor(identity),
	}
	result, err := s.bulkFacade.Execute(ctx, identity, op)
	if err != nil {
		return "", fmt.Errorf("bulk_delete: %w", err)
	}

	out := map[string]any{
		"dry_run":        result.DryRun,
		"would_affect":   result.WouldAffect,
		"affected_count": result.AffectedCount,
		"snapshot_id":    result.SnapshotID,
		"errors":         result.Errors,
	}
	return marshalJSON(out)
}

// handleBulkSupersede supersedes a list of memories.
//
// Admin gate: non-admin callers receive admin_required error.
// Dry-run nil-safe seam: when bulkFacade is nil and dry_run=true, returns
// would_affect from len(memory_ids) — zero DB reads or writes.
func (s *Server) handleBulkSupersede(ctx context.Context, args json.RawMessage) (string, error) {
	if !vnextFEnabled() {
		return "", fmt.Errorf("bulk_supersede: requires ENGRAM_VNEXT_F_ENABLED=true")
	}
	identity, ok := auth.IdentityFrom(ctx)
	if !ok || !identity.IsAdmin() {
		return "", fmt.Errorf("admin_required: bulk_supersede requires admin identity")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	memoryIDs := coerceInt64Slice(m["memory_ids"])
	dryRun := coerceBool(m["dry_run"], false)

	// Nil-safe TG5-absent dry-run seam.
	if dryRun && s.bulkFacade == nil {
		out := map[string]any{
			"dry_run":      true,
			"would_affect": len(memoryIDs),
			"note":         "bulk_supersede preview (facade not wired — would_affect from input only)",
		}
		return marshalJSON(out)
	}

	if s.bulkFacade == nil {
		return "", fmt.Errorf("bulk_supersede: facade not available — set ENGRAM_VNEXT_F_ENABLED=true and wire stores")
	}

	op := bulkops.BulkOp{
		Type:      models.SnapshotOpBulkSupersede,
		MemoryIDs: memoryIDs,
		DryRun:    dryRun,
		Actor:     resolveGovernanceActor(identity),
	}
	result, err := s.bulkFacade.Execute(ctx, identity, op)
	if err != nil {
		return "", fmt.Errorf("bulk_supersede: %w", err)
	}

	out := map[string]any{
		"dry_run":        result.DryRun,
		"would_affect":   result.WouldAffect,
		"affected_count": result.AffectedCount,
		"snapshot_id":    result.SnapshotID,
		"errors":         result.Errors,
	}
	return marshalJSON(out)
}
