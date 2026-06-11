// Package bulkops — rollback.go implements snapshot rollback with conflict detection.
//
// Rollback restores the before_state of a bulk_op_snapshot to the memories table.
// Per spec EC-F3: if any affected memory's updated_at > snapshot.created_at, the
// rollback is refused (not partially applied). The caller receives ErrRollbackConflict
// and an audit entry with action='rollback_attempted_with_conflict'.
//
// Successful rollback writes action='rollback' to the audit log and marks the snapshot
// status='rolled_back' via SnapshotStore.MarkRolledBack.
package bulkops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/pkg/models"
	gormpkg "gorm.io/gorm"
)

// ErrRollbackConflict is returned when at least one affected memory has been modified
// after the snapshot was captured. Per spec EC-F3.
var ErrRollbackConflict = errors.New("rollback_conflict")

// ErrSnapshotNotRollbackable is returned when the snapshot status is not 'committed'
// (e.g., already rolled back or in preview state).
var ErrSnapshotNotRollbackable = errors.New("snapshot_not_rollbackable")

// RollbackResult is the outcome of a Rollback call.
type RollbackResult struct {
	// SnapshotID is the snapshot that was rolled back.
	SnapshotID string `json:"snapshot_id"`
	// RestoredCount is the number of memory rows restored.
	RestoredCount int `json:"restored_count"`
	// ConflictIDs contains memory IDs that were modified after the snapshot (populated
	// when ErrRollbackConflict is returned).
	ConflictIDs []int64 `json:"conflict_ids,omitempty"`
}

// Rollback rolls back a committed bulk_op_snapshot.
//
// Admin gate: identity.Role must be auth.RoleAdmin.
// Conflict check (EC-F3): if any affected memory's current updated_at > snapshot.created_at,
// the rollback is refused atomically (no partial restore). An audit entry with
// action='rollback_attempted_with_conflict' is written.
// On success: memory rows are restored, audit action='rollback' is written, snapshot
// status is set to 'rolled_back'.
func Rollback(
	ctx context.Context,
	identity auth.Identity,
	snapshotID string,
	snapshotStore *gormdb.SnapshotStore,
	memoryStore *gormdb.MemoryStore,
	auditStore *gormdb.AuditStore,
) (*RollbackResult, error) {
	if identity.Role != auth.RoleAdmin {
		return nil, ErrAdminRequired
	}

	snap, err := snapshotStore.Get(ctx, snapshotID)
	if err != nil {
		if errors.Is(err, gormpkg.ErrRecordNotFound) {
			return nil, fmt.Errorf("rollback: snapshot %q not found: %w", snapshotID, err)
		}
		return nil, fmt.Errorf("rollback: get snapshot: %w", err)
	}

	if snap.Status != models.SnapshotStatusCommitted {
		return nil, fmt.Errorf("rollback: snapshot %q has status %q, expected 'committed': %w",
			snapshotID, snap.Status, ErrSnapshotNotRollbackable)
	}

	actor := resolveActor(identity)

	// Decode before_state. Supports two formats:
	//  - Typed entries: map[id]{"kind":"restore"|"delete","before":<raw>} (bulk_promote fix)
	//  - Legacy flat format: map[id]<memory JSON> (bulk_delete, bulk_supersede)
	// decodeTypedBeforeState transparently handles both.
	typedEntries, err := decodeTypedBeforeState(snap.BeforeState)
	if err != nil {
		return nil, fmt.Errorf("rollback: decode before_state: %w", err)
	}

	// Conflict check (EC-F3): for every affected memory ID, verify updated_at <= snapshot.created_at.
	// AffectedMemoryIDs contains actual memory IDs (post-amend for bulk_promote).
	conflictIDs, err := detectConflicts(ctx, memoryStore, snap.AffectedMemoryIDs, snap.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("rollback: conflict detection: %w", err)
	}
	if len(conflictIDs) > 0 {
		// Write conflict audit entry and return error — no restore occurs.
		if auditStore != nil {
			_ = auditStore.Log(ctx, gormdb.AuditLogEntry{
				Action: "rollback_attempted_with_conflict",
				Actor:  actor,
				Reason: fmt.Sprintf("snapshot=%s conflict_ids=%v", snapshotID, conflictIDs),
			})
		}
		return &RollbackResult{
			SnapshotID:  snapshotID,
			ConflictIDs: conflictIDs,
		}, ErrRollbackConflict
	}

	// MAJOR fix: all restore mutations + MarkRolledBack run inside ONE transaction.
	// A mid-loop failure previously left partially-restored state with the snapshot
	// still committed — re-rollback would double-write already-restored rows.
	// With a single transaction: either everything is applied or nothing is.
	db := memoryStore.GetDB()
	result := &RollbackResult{SnapshotID: snapshotID}

	txErr := db.WithContext(ctx).Transaction(func(tx *gormpkg.DB) error {
		var restored int

		for key, entry := range typedEntries {
			id, parseErr := strconv.ParseInt(key, 10, 64)
			if parseErr != nil {
				return fmt.Errorf("rollback: parse entry key %q: %w", key, parseErr)
			}

			switch entry.Kind {
			case models.EntryKindDelete:
				// Row was CREATED by the op; hard-delete it within the transaction.
				// candidate.promoted_memory_id is SET NULL by FK (ON DELETE SET NULL, EC-F4).
				if delErr := memoryStore.HardDeleteTx(ctx, tx, id); delErr != nil {
					return fmt.Errorf("rollback: hard-delete created memory %d: %w", id, delErr)
				}

			case models.EntryKindRestore, "": // empty kind = legacy flat format (treated as restore)
				if len(entry.Before) == 0 || string(entry.Before) == "null" {
					// Empty before-state: row didn't exist pre-op; skip (same as legacy skip).
					continue
				}
				var mem models.Memory
				if unmarshalErr := json.Unmarshal(entry.Before, &mem); unmarshalErr != nil {
					return fmt.Errorf("rollback: unmarshal memory %d: %w", id, unmarshalErr)
				}
				mem.ID = id
				if restoreErr := memoryStore.RestoreRawTx(ctx, tx, &mem); restoreErr != nil {
					return fmt.Errorf("rollback: restore memory %d: %w", id, restoreErr)
				}
				restored++

			default:
				return fmt.Errorf("rollback: unknown entry kind %q for id %d", entry.Kind, id)
			}
		}

		// Mark snapshot rolled_back inside the same transaction.
		now := time.Now().UTC()
		res := tx.WithContext(ctx).
			Model(&struct{ TableName string }{}).
			Table("bulk_op_snapshots").
			Where("snapshot_id = ? AND status = 'committed'", snapshotID).
			Updates(map[string]any{
				"status":         string(models.SnapshotStatusRolledBack),
				"rolled_back_at": now,
			})
		if res.Error != nil {
			return fmt.Errorf("rollback: mark_rolled_back in tx: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("rollback: snapshot %q not found or already rolled back", snapshotID)
		}

		result.RestoredCount = restored
		return nil
	})

	if txErr != nil {
		return nil, txErr
	}

	// Audit success (outside tx — non-fatal if it fails).
	if auditStore != nil {
		_ = auditStore.Log(ctx, gormdb.AuditLogEntry{
			Action: "rollback",
			Actor:  actor,
			Reason: fmt.Sprintf("snapshot=%s restored=%d", snapshotID, result.RestoredCount),
		})
	}

	return result, nil
}

// detectConflicts returns the IDs of memories modified after snapshotTime.
// A memory's updated_at > snapshotTime indicates a post-snapshot modification (EC-F3).
func detectConflicts(ctx context.Context, memoryStore *gormdb.MemoryStore, ids []int64, snapshotTime time.Time) ([]int64, error) {
	if memoryStore == nil || len(ids) == 0 {
		return nil, nil
	}
	var conflicts []int64
	for _, id := range ids {
		mem, err := memoryStore.Get(ctx, id)
		if err != nil {
			if errors.Is(err, gormpkg.ErrRecordNotFound) {
				// Deleted memories don't conflict — skip.
				continue
			}
			return nil, fmt.Errorf("detectConflicts: get memory %d: %w", id, err)
		}
		if mem.UpdatedAt.After(snapshotTime) {
			conflicts = append(conflicts, id)
		}
	}
	return conflicts, nil
}

// decodeTypedBeforeState parses the JSONB before_state into typed SnapshotEntry values.
//
// Supports two wire formats transparently:
//  1. Typed: map[id]{"kind":"restore"|"delete","before":<raw>} — written by bulk_promote fix.
//  2. Legacy flat: map[id]<memory JSON object> — written by bulk_delete/bulk_supersede.
//
// The distinguishing heuristic: if the top-level value has a "kind" field that equals
// "restore" or "delete", it is a typed entry. Otherwise it is treated as a legacy
// flat memory row and wrapped as EntryKindRestore with the full value as Before.
func decodeTypedBeforeState(raw json.RawMessage) (map[string]models.SnapshotEntry, error) {
	if len(raw) == 0 || string(raw) == "{}" {
		return map[string]models.SnapshotEntry{}, nil
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		return nil, fmt.Errorf("parse before_state JSON: %w", err)
	}

	out := make(map[string]models.SnapshotEntry, len(rawMap))
	for k, v := range rawMap {
		if v == nil || string(v) == "null" {
			// nil entry = legacy missing row; treat as restore with empty before.
			out[k] = models.SnapshotEntry{Kind: models.EntryKindRestore}
			continue
		}

		// Try typed format first.
		var typed models.SnapshotEntry
		if err := json.Unmarshal(v, &typed); err == nil &&
			(typed.Kind == models.EntryKindRestore || typed.Kind == models.EntryKindDelete) {
			out[k] = typed
			continue
		}

		// Legacy flat format: the value is a raw memory object. Wrap as restore entry.
		out[k] = models.SnapshotEntry{Kind: models.EntryKindRestore, Before: v}
	}
	return out, nil
}

// decodeBeforeState parses the JSONB before_state into a map[memoryID]raw.
// Kept for backward compatibility with existing tests.
func decodeBeforeState(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "{}" {
		return map[string]json.RawMessage{}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse before_state JSON: %w", err)
	}
	return m, nil
}
