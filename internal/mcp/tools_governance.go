// Package mcp — tools_governance.go implements the 4 governance MCP tools for
// bulk-op snapshot management (Milestone-F TG6 T043).
//
// Tools:
//   - list_snapshots    — list recent bulk_op_snapshots with optional op_type/actor filter
//   - rollback_snapshot — roll back a committed snapshot (EC-F3 conflict detection)
//   - pin_snapshot      — pin a snapshot to exempt it from auto-prune (T049)
//   - redaction_rules_status — report redaction rules load status (EC-F9)
//
// All 4 tools are admin-gated: non-admin callers receive error_code='admin_required'.
// Tools are advertised only when snapshotStore is non-nil (ENGRAM_VNEXT_F_ENABLED=true).
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/bulkops"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// governanceTools returns the 4 governance tool definitions.
func governanceTools() []Tool {
	return []Tool{
		{
			Name:        "list_snapshots",
			Description: "List recent bulk_op_snapshots. Admin-only. Supports optional op_type and actor filters.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"op_type": map[string]any{
						"type":        "string",
						"description": "Filter by op_type (ingest_doc, bulk_promote, bulk_delete, bulk_supersede, candidate_review_action, forgetting_review_action). Omit for all.",
						"enum":        []string{"ingest_doc", "bulk_promote", "bulk_delete", "bulk_supersede", "candidate_review_action", "forgetting_review_action"},
					},
					"actor": map[string]any{
						"type":        "string",
						"description": "Filter by actor (KeycardID or 'master'). Omit for all.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Max results (default 20, max 100).",
						"minimum":     1,
						"maximum":     100,
					},
				},
			},
		},
		{
			Name:        "rollback_snapshot",
			Description: "Roll back a committed bulk_op_snapshot. Admin-only. Conflict-safe: refuses if any affected memory was modified after snapshot capture (EC-F3).",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"snapshot_id"},
				"properties": map[string]any{
					"snapshot_id": map[string]any{
						"type":        "string",
						"description": "Snapshot ID (from list_snapshots or bulk-op response).",
					},
				},
			},
		},
		{
			Name:        "pin_snapshot",
			Description: "Pin a snapshot to exempt it from the 30-day auto-prune cycle (T049). Admin-only.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"snapshot_id"},
				"properties": map[string]any{
					"snapshot_id": map[string]any{
						"type":        "string",
						"description": "Snapshot ID to pin.",
					},
				},
			},
		},
		{
			Name:        "redaction_rules_status",
			Description: "Report loaded redaction rules status: rule count, load checksum, source path. Changes require server restart (EC-F9). Admin-only.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

// handleListSnapshots lists recent bulk_op_snapshots.
// Admin-gated: non-admin callers receive "admin_required" error.
func (s *Server) handleListSnapshots(ctx context.Context, args json.RawMessage) (string, error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok || !id.IsAdmin() {
		return "", fmt.Errorf("admin_required: list_snapshots requires admin identity")
	}
	if s.snapshotStore == nil {
		return "", fmt.Errorf("list_snapshots: snapshot store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	opType := models.SnapshotOpType(coerceString(m["op_type"], ""))
	actor := coerceString(m["actor"], "")
	limit := coerceInt(m["limit"], 20)
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	snaps, err := s.snapshotStore.List(ctx, opType, actor, limit)
	if err != nil {
		return "", fmt.Errorf("list_snapshots: %w", err)
	}

	type snapshotSummary struct {
		ID            int64                 `json:"id"`
		SnapshotID    string                `json:"snapshot_id"`
		OpType        models.SnapshotOpType `json:"op_type"`
		Actor         string                `json:"actor"`
		Status        models.SnapshotStatus `json:"status"`
		Pinned        bool                  `json:"pinned"`
		AffectedCount int                   `json:"affected_count"`
		CreatedAt     string                `json:"created_at"`
		RolledBackAt  *string               `json:"rolled_back_at,omitempty"`
	}

	summaries := make([]snapshotSummary, 0, len(snaps))
	for _, snap := range snaps {
		sum := snapshotSummary{
			ID:            snap.ID,
			SnapshotID:    snap.SnapshotID,
			OpType:        snap.OpType,
			Actor:         snap.Actor,
			Status:        snap.Status,
			Pinned:        snap.Pinned,
			AffectedCount: len(snap.AffectedMemoryIDs),
			CreatedAt:     snap.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
		if snap.RolledBackAt != nil {
			ts := snap.RolledBackAt.UTC().Format("2006-01-02T15:04:05Z")
			sum.RolledBackAt = &ts
		}
		summaries = append(summaries, sum)
	}

	out := map[string]any{
		"snapshots": summaries,
		"count":     len(summaries),
	}
	return marshalJSON(out)
}

// handleRollbackSnapshot rolls back a committed snapshot.
// Conflict detection per EC-F3: refuses if any affected memory was modified after snapshot.
func (s *Server) handleRollbackSnapshot(ctx context.Context, args json.RawMessage) (string, error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok || !id.IsAdmin() {
		return "", fmt.Errorf("admin_required: rollback_snapshot requires admin identity")
	}
	if s.snapshotStore == nil {
		return "", fmt.Errorf("rollback_snapshot: snapshot store not available")
	}
	if s.memoryStore == nil {
		return "", fmt.Errorf("rollback_snapshot: memory store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	snapshotID := coerceString(m["snapshot_id"], "")
	if snapshotID == "" {
		return "", fmt.Errorf("snapshot_id is required")
	}

	result, rollErr := bulkops.Rollback(ctx, id, snapshotID, s.snapshotStore, s.memoryStore, s.auditStore, s.candidateStore)
	if rollErr != nil {
		if errors.Is(rollErr, bulkops.ErrRollbackConflict) {
			conflictOut := map[string]any{
				"error":        "rollback_conflict",
				"description":  "One or more affected memories were modified after the snapshot was captured. Rollback refused per EC-F3.",
				"conflict_ids": result.ConflictIDs,
				"snapshot_id":  snapshotID,
			}
			return marshalJSON(conflictOut)
		}
		if errors.Is(rollErr, bulkops.ErrSnapshotNotRollbackable) {
			return "", fmt.Errorf("rollback_snapshot: snapshot %q is not in 'committed' state", snapshotID)
		}
		return "", fmt.Errorf("rollback_snapshot: %w", rollErr)
	}

	out := map[string]any{
		"snapshot_id":    result.SnapshotID,
		"restored_count": result.RestoredCount,
		"status":         "rolled_back",
	}
	return marshalJSON(out)
}

// handlePinSnapshot pins a snapshot to exempt it from auto-prune (T049).
func (s *Server) handlePinSnapshot(ctx context.Context, args json.RawMessage) (string, error) {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok || !identity.IsAdmin() {
		return "", fmt.Errorf("admin_required: pin_snapshot requires admin identity")
	}
	if s.snapshotStore == nil {
		return "", fmt.Errorf("pin_snapshot: snapshot store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	snapshotID := coerceString(m["snapshot_id"], "")
	if snapshotID == "" {
		return "", fmt.Errorf("snapshot_id is required")
	}

	if err := s.snapshotStore.Pin(ctx, snapshotID); err != nil {
		return "", fmt.Errorf("pin_snapshot: %w", err)
	}

	// Audit pin action.
	if s.auditStore != nil {
		actor := resolveGovernanceActor(identity)
		_ = s.auditStore.Log(ctx, gormdb.AuditLogEntry{
			Action: "pin_snapshot",
			Actor:  actor,
			Reason: fmt.Sprintf("snapshot=%s pinned", snapshotID),
		})
	}

	out := map[string]any{
		"snapshot_id": snapshotID,
		"pinned":      true,
	}
	return marshalJSON(out)
}

// handleRedactionRulesStatus reports the current redaction rules load status.
// Per EC-F9: rule file changes require a server restart (no hot-reload).
// The response reflects what was loaded at startup.
func (s *Server) handleRedactionRulesStatus(ctx context.Context, args json.RawMessage) (string, error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok || !id.IsAdmin() {
		return "", fmt.Errorf("admin_required: redaction_rules_status requires admin identity")
	}

	// Redaction rules are loaded at startup from ENGRAM_REDACTION_RULES_PATH (T036 / ADR-F-004).
	// EC-F9: rule file changes require a server restart (no hot-reload).
	// TG5 is now present; report the actual loaded state from s.redactionRules.
	ruleCount := len(s.redactionRules)
	out := map[string]any{
		"restart_required": true, // EC-F9: rule changes always require restart
	}
	if ruleCount == 0 {
		out["status"] = "inactive"
		out["rule_count"] = 0
		out["description"] = "No redaction rules loaded. Set ENGRAM_REDACTION_RULES_PATH to a rules file and restart to activate."
	} else {
		out["status"] = "active"
		out["rule_count"] = ruleCount
		out["description"] = "Redaction rules active. Rules were loaded at startup from ENGRAM_REDACTION_RULES_PATH."
	}
	return marshalJSON(out)
}

// resolveGovernanceActor extracts the actor string from an identity for audit entries.
func resolveGovernanceActor(identity auth.Identity) string {
	if identity.KeycardID != "" {
		return identity.KeycardID
	}
	if identity.Source == auth.SourceMaster {
		return "master"
	}
	return string(identity.Source)
}
