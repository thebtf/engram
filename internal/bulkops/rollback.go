// Package bulkops — rollback.go implements snapshot rollback with conflict detection.
//
// Rollback restores the before_state of a bulk_op_snapshot to the memories table.
// Typed restore entries use their expected post-mutation version to reject later edits;
// legacy entries fall back to updated_at > snapshot.created_at. The rollback is refused
// (not partially applied) and returns ErrRollbackConflict on any conflict.
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

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	gormpkg "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrRollbackConflict is returned when at least one affected memory has been modified
// after the snapshot was captured. Per spec EC-F3.
var ErrRollbackConflict = errors.New("rollback_conflict")

// ErrSnapshotNotRollbackable is returned when the snapshot status is not 'committed'
// (e.g., already rolled back or in preview state).
var ErrSnapshotNotRollbackable = errors.New("snapshot_not_rollbackable")

// ErrLegacySnapshotAmbiguous is returned when a legacy unprefixed promotion
// snapshot collides across candidate and memory identities.
var ErrLegacySnapshotAmbiguous = errors.New("legacy_snapshot_ambiguous")

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
	candidateStore *gormdb.CandidateStore,
) (*RollbackResult, error) {
	if identity.Role != auth.RoleAdmin {
		return nil, ErrAdminRequired
	}
	if snapshotStore == nil {
		return nil, fmt.Errorf("rollback: snapshotStore is required")
	}
	if memoryStore == nil {
		return nil, fmt.Errorf("rollback: memoryStore is required")
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
	if auditStore == nil {
		return nil, errors.New("rollback: audit store required")
	}

	actor := resolveActor(identity)

	// Conflict detection and mutations must share one transaction. Locking each target
	// row makes a concurrent edit commit before the check or wait behind this rollback.
	db := memoryStore.GetDB()
	result := &RollbackResult{SnapshotID: snapshotID}

	txErr := db.WithContext(ctx).Transaction(func(tx *gormpkg.DB) error {
		lockedSnap, err := snapshotStore.GetForUpdateTx(ctx, tx, snapshotID)
		if err != nil {
			return fmt.Errorf("rollback: lock snapshot %q: %w", snapshotID, err)
		}
		if lockedSnap.Status != models.SnapshotStatusCommitted {
			return fmt.Errorf("rollback: snapshot %q has status %q, expected 'committed': %w", snapshotID, lockedSnap.Status, ErrSnapshotNotRollbackable)
		}
		typedEntries, err := decodeTypedBeforeState(lockedSnap.BeforeState)
		if err != nil {
			return fmt.Errorf("rollback: decode before_state: %w", err)
		}
		if err := rejectAmbiguousLegacyPromoteEntriesTx(ctx, tx, lockedSnap.OpType, typedEntries); err != nil {
			return err
		}

		var legacyTimestampIDs []int64
		var createdIDsToCheck []int64
		expectedVersions := make(map[int64]int)
		for _, id := range lockedSnap.AffectedMemoryIDs {
			entry, ok := snapshotEntryForMemoryID(typedEntries, id)
			if ok && entry.ExpectedVersion != nil {
				expectedVersions[id] = *entry.ExpectedVersion
				continue
			}
			if ok && entry.Kind == models.EntryKindDelete {
				createdIDsToCheck = append(createdIDsToCheck, id)
				continue
			}
			legacyTimestampIDs = append(legacyTimestampIDs, id)
		}
		conflictIDs, err := detectConflictsTx(ctx, tx, legacyTimestampIDs, lockedSnap.CreatedAt, lockedSnap.OpType == models.SnapshotOpBulkDelete)
		if err != nil {
			return fmt.Errorf("rollback: conflict detection: %w", err)
		}
		versionConflictIDs, err := detectExpectedVersionConflictsTx(ctx, tx, expectedVersions)
		if err != nil {
			return fmt.Errorf("rollback: version conflict detection: %w", err)
		}
		conflictIDs = append(conflictIDs, versionConflictIDs...)
		createdConflictIDs, err := detectCreatedRowConflictsTx(ctx, tx, createdIDsToCheck)
		if err != nil {
			return fmt.Errorf("rollback: created-row conflict detection: %w", err)
		}
		conflictIDs = append(conflictIDs, createdConflictIDs...)
		if len(conflictIDs) > 0 {
			result.ConflictIDs = conflictIDs
			return ErrRollbackConflict
		}

		var restored int

		for key, entry := range typedEntries {
			entity, id, parseErr := parseSnapshotEntryKey(key)
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
				if entity == snapshotEntryEntityCandidate || (entity == "" && (lockedSnap.OpType == models.SnapshotOpBulkPromote || lockedSnap.OpType == models.SnapshotOpCandidateReviewAction)) {
					// bulk_promote and candidate_review_action store candidate JSON in restore entries.
					// Rollback must revert the candidate row to its pre-op state (e.g., pending),
					// NOT write candidate data into the memories table.
					if candidateStore == nil {
						return fmt.Errorf("rollback: candidateStore required to roll back candidate %d", id)
					}
					var c models.CrystallizationCandidate
					if unmarshalErr := json.Unmarshal(entry.Before, &c); unmarshalErr != nil {
						return fmt.Errorf("rollback: unmarshal candidate %d: %w", id, unmarshalErr)
					}
					c.ID = id
					if revertErr := candidateStore.RevertRawTx(ctx, tx, &c); revertErr != nil {
						return fmt.Errorf("rollback: revert candidate %d: %w", id, revertErr)
					}
				} else {
					var mem models.Memory
					if unmarshalErr := json.Unmarshal(entry.Before, &mem); unmarshalErr != nil {
						return fmt.Errorf("rollback: unmarshal memory %d: %w", id, unmarshalErr)
					}
					mem.ID = id
					if restoreErr := memoryStore.RestoreRawTx(ctx, tx, &mem); restoreErr != nil {
						return fmt.Errorf("rollback: restore memory %d: %w", id, restoreErr)
					}
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
		if err := auditStore.LogTx(ctx, tx, gormdb.AuditLogEntry{
			Action: "rollback",
			Actor:  actor,
			Reason: fmt.Sprintf("snapshot=%s restored=%d", snapshotID, result.RestoredCount),
		}); err != nil {
			return fmt.Errorf("rollback: audit: %w", err)
		}
		return nil
	})

	if errors.Is(txErr, ErrRollbackConflict) {
		if err := auditStore.Log(ctx, gormdb.AuditLogEntry{
			Action: "rollback_attempted_with_conflict",
			Actor:  actor,
			Reason: fmt.Sprintf("snapshot=%s conflict_ids=%v", snapshotID, result.ConflictIDs),
		}); err != nil {
			return result, errors.Join(ErrRollbackConflict, fmt.Errorf("rollback conflict audit: %w", err))
		}
		return result, ErrRollbackConflict
	}
	if txErr != nil {
		return nil, txErr
	}

	return result, nil

}

const (
	snapshotEntryEntityCandidate = "candidate"
	snapshotEntryEntityMemory    = "memory"
)

func parseSnapshotEntryKey(key string) (string, int64, error) {
	const candidatePrefix = "candidate:"
	const memoryPrefix = "memory:"
	if len(key) > len(candidatePrefix) && key[:len(candidatePrefix)] == candidatePrefix {
		id, err := strconv.ParseInt(key[len(candidatePrefix):], 10, 64)
		return snapshotEntryEntityCandidate, id, err
	}
	if len(key) > len(memoryPrefix) && key[:len(memoryPrefix)] == memoryPrefix {
		id, err := strconv.ParseInt(key[len(memoryPrefix):], 10, 64)
		return snapshotEntryEntityMemory, id, err
	}
	id, err := strconv.ParseInt(key, 10, 64)
	return "", id, err
}

func snapshotEntryForMemoryID(entries map[string]models.SnapshotEntry, id int64) (models.SnapshotEntry, bool) {
	if entry, ok := entries[fmt.Sprintf("memory:%d", id)]; ok {
		return entry, true
	}
	entry, ok := entries[fmt.Sprintf("%d", id)]
	return entry, ok
}

// rejectAmbiguousLegacyPromoteEntriesTx refuses legacy bulk-promote and
// candidate-review entries whose unprefixed ID names both a candidate and memory.
// Entity-prefixed entries carry their identity and are always safe to process.
func rejectAmbiguousLegacyPromoteEntriesTx(ctx context.Context, tx *gormpkg.DB, opType models.SnapshotOpType, entries map[string]models.SnapshotEntry) error {
	if opType != models.SnapshotOpBulkPromote && opType != models.SnapshotOpCandidateReviewAction {
		return nil
	}
	for key := range entries {
		entity, id, err := parseSnapshotEntryKey(key)
		if err != nil {
			return fmt.Errorf("rollback: parse entry key %q: %w", key, err)
		}
		if entity != "" {
			continue
		}

		var candidates, memories int64
		if err := tx.WithContext(ctx).Table("crystallization_candidates").Where("id = ?", id).Count(&candidates).Error; err != nil {
			return fmt.Errorf("rollback: inspect legacy candidate %d: %w", id, err)

		}
		if err := tx.WithContext(ctx).Table("memories").Where("id = ?", id).Count(&memories).Error; err != nil {
			return fmt.Errorf("rollback: inspect legacy memory %d: %w", id, err)
		}
		if candidates > 0 && memories > 0 {
			return fmt.Errorf("rollback: legacy unprefixed %s snapshot entry %d is candidate/memory ambiguous: %w", opType, id, ErrLegacySnapshotAmbiguous)
		}
	}
	return nil
}

// detectExpectedVersionConflictsTx locks each row, including soft-deleted rows,
// and requires its version to equal the version produced by the forward mutation.
func detectExpectedVersionConflictsTx(ctx context.Context, tx *gormpkg.DB, expectedVersions map[int64]int) ([]int64, error) {
	var conflicts []int64
	for id, expectedVersion := range expectedVersions {
		var mem gormdb.Memory
		err := tx.WithContext(ctx).Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&mem).Error
		if err != nil {
			if errors.Is(err, gormpkg.ErrRecordNotFound) {
				conflicts = append(conflicts, id)
				continue
			}
			return nil, fmt.Errorf("detectExpectedVersionConflicts: lock memory %d: %w", id, err)
		}
		if mem.Version != expectedVersion {
			conflicts = append(conflicts, id)
		}
	}
	return conflicts, nil
}

// detectConflictsTx locks each row, including soft-deleted rows, before checking
// its timestamp. Missing rows conflict for legacy bulk-delete snapshots, where a
// restore cannot safely distinguish deletion from a later hard-delete.
func detectConflictsTx(ctx context.Context, tx *gormpkg.DB, ids []int64, snapshotTime time.Time, missingIsConflict bool) ([]int64, error) {
	var conflicts []int64
	for _, id := range ids {
		var mem gormdb.Memory
		err := tx.WithContext(ctx).Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&mem).Error
		if err != nil {
			if errors.Is(err, gormpkg.ErrRecordNotFound) {
				if missingIsConflict {
					conflicts = append(conflicts, id)
				}
				continue
			}
			return nil, fmt.Errorf("detectConflicts: lock memory %d: %w", id, err)
		}
		if mem.UpdatedAt.After(snapshotTime) {
			conflicts = append(conflicts, id)
		}
	}
	return conflicts, nil
}

// detectCreatedRowConflictsTx locks promoted rows before deciding whether rollback
// may hard-delete them.
func detectCreatedRowConflictsTx(ctx context.Context, tx *gormpkg.DB, ids []int64) ([]int64, error) {
	var conflicts []int64
	for _, id := range ids {
		var mem gormdb.Memory
		err := tx.WithContext(ctx).Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&mem).Error
		if err != nil {
			if errors.Is(err, gormpkg.ErrRecordNotFound) {
				continue
			}
			return nil, fmt.Errorf("detectCreatedRowConflicts: lock memory %d: %w", id, err)
		}
		if mem.UpdatedAt.After(mem.CreatedAt) {
			conflicts = append(conflicts, id)
		}
	}
	return conflicts, nil
}

// decodeTypedBeforeState parses the JSONB before_state into typed SnapshotEntry values.
//
//  1. Typed: map[id-or-entity-key]{"kind":"restore"|"delete","before":<raw>}.
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
