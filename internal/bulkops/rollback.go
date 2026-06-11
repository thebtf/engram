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
	"gorm.io/gorm"
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("rollback: snapshot %q not found: %w", snapshotID, err)
		}
		return nil, fmt.Errorf("rollback: get snapshot: %w", err)
	}

	if snap.Status != models.SnapshotStatusCommitted {
		return nil, fmt.Errorf("rollback: snapshot %q has status %q, expected 'committed': %w",
			snapshotID, snap.Status, ErrSnapshotNotRollbackable)
	}

	actor := resolveActor(identity)

	// Decode before_state: map[string(memoryID)]json.RawMessage.
	beforeState, err := decodeBeforeState(snap.BeforeState)
	if err != nil {
		return nil, fmt.Errorf("rollback: decode before_state: %w", err)
	}

	// Conflict check (EC-F3): for every affected memory, verify updated_at <= snapshot.created_at.
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

	// Restore each memory from before_state.
	result := &RollbackResult{SnapshotID: snapshotID}
	for _, id := range snap.AffectedMemoryIDs {
		key := strconv.FormatInt(id, 10)
		rawMem, ok := beforeState[key]
		if !ok || rawMem == nil {
			// Memory was not in before_state (e.g., was created by the op); skip.
			continue
		}

		var mem models.Memory
		if err := json.Unmarshal(rawMem, &mem); err != nil {
			return nil, fmt.Errorf("rollback: unmarshal memory %d: %w", id, err)
		}
		mem.ID = id

		// Restore via Update. Update only touches content/tags/source_agent/edited_by/updated_at.
		// For deletions (deleted_at was nil before), we also need to clear deleted_at.
		if err := restoreMemory(ctx, memoryStore, &mem); err != nil {
			return nil, fmt.Errorf("rollback: restore memory %d: %w", id, err)
		}
		result.RestoredCount++
	}

	// Mark snapshot as rolled back.
	if err := snapshotStore.MarkRolledBack(ctx, snapshotID); err != nil {
		return nil, fmt.Errorf("rollback: mark_rolled_back: %w", err)
	}

	// Audit success.
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
			if errors.Is(err, gorm.ErrRecordNotFound) {
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

// decodeBeforeState parses the JSONB before_state into a map[memoryID]raw.
// The before_state was written by captureMemoryBeforeState as map[string]*models.Memory.
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

// restoreMemory writes back a memory row captured in before_state.
// Uses GORM raw update to restore content, tags, status, deleted_at, and updated_at
// to their pre-op values without triggering version bumps.
func restoreMemory(ctx context.Context, memoryStore *gormdb.MemoryStore, mem *models.Memory) error {
	if mem.ID == 0 {
		return fmt.Errorf("restoreMemory: memory ID is zero")
	}
	// RestoreRaw performs a direct UPDATE bypassing the normal Update() validations
	// (which require non-empty Content and reject deleted rows).
	return memoryStore.RestoreRaw(ctx, mem)
}
