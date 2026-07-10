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
	"math/big"
	"strings"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/bulkops"
	"github.com/thebtf/engram/pkg/models"
)

var executeBulkFacade = func(facade *bulkops.Facade, ctx context.Context, identity auth.Identity, op bulkops.BulkOp) (*bulkops.ExecuteResult, error) {
	return facade.Execute(ctx, identity, op)
}

func parseBulkStructuredArgs(args json.RawMessage, idField string, operation string) ([]int64, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil || fields == nil {
		return nil, false, fmt.Errorf("%s: arguments must be a JSON object", operation)
	}

	rawIDs, ok := fields[idField]
	if !ok {
		return nil, false, fmt.Errorf("%s: %s is required", operation, idField)
	}
	var encodedIDs []json.RawMessage
	if err := json.Unmarshal(rawIDs, &encodedIDs); err != nil || encodedIDs == nil {
		return nil, false, fmt.Errorf("%s: %s must be an array of integral int64 JSON numbers", operation, idField)
	}
	ids := make([]int64, 0, len(encodedIDs))
	for index, encodedID := range encodedIDs {
		numberText := strings.TrimSpace(string(encodedID))
		var exact big.Rat
		if _, ok := exact.SetString(numberText); !ok || !exact.IsInt() || !exact.Num().IsInt64() {
			return nil, false, fmt.Errorf("%s: %s[%d] must be an integral int64 JSON number", operation, idField, index)
		}
		ids = append(ids, exact.Num().Int64())
	}

	dryRun := false
	if rawDryRun, ok := fields["dry_run"]; ok {
		switch strings.TrimSpace(string(rawDryRun)) {
		case "true":
			dryRun = true
		case "false":
			dryRun = false
		default:
			return nil, false, fmt.Errorf("%s: dry_run must be a JSON boolean", operation)
		}
	}
	return ids, dryRun, nil
}

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

	candidateIDs, dryRun, err := parseBulkStructuredArgs(args, "candidate_ids", "bulk_promote")
	if err != nil {
		return "", err
	}
	candidateIDs = bulkops.NormalizeCandidateIDs(candidateIDs)

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
	result, err := executeBulkFacade(s.bulkFacade, ctx, identity, op)
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

	memoryIDs, dryRun, err := parseBulkStructuredArgs(args, "memory_ids", "bulk_delete")
	if err != nil {
		return "", err
	}
	memoryIDs = bulkops.NormalizeCandidateIDs(memoryIDs)

	// Nil-safe TG5-absent dry-run seam.
	if dryRun && s.bulkFacade == nil {
		out := map[string]any{
			"dry_run":      true,
			"would_affect": len(memoryIDs),
			"note":         "bulk_delete preview (facade not wired — normalized input only)",
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
	result, err := executeBulkFacade(s.bulkFacade, ctx, identity, op)
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

	memoryIDs, dryRun, err := parseBulkStructuredArgs(args, "memory_ids", "bulk_supersede")
	if err != nil {
		return "", err
	}
	memoryIDs = bulkops.NormalizeCandidateIDs(memoryIDs)

	// Nil-safe TG5-absent dry-run seam.
	if dryRun && s.bulkFacade == nil {
		out := map[string]any{
			"dry_run":      true,
			"would_affect": len(memoryIDs),
			"note":         "bulk_supersede preview (facade not wired — normalized input only)",
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
	result, err := executeBulkFacade(s.bulkFacade, ctx, identity, op)
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
