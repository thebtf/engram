// Package bulkops — rollback.go implements snapshot rollback with conflict detection.
//
// Rollback restores the before_state of a bulk_op_snapshot to memories and, for
// candidate operations, crystallization candidates. Per spec EC-F3, rollback is
// refused atomically when a memory changed after the operation or a candidate no
// longer exactly matches the persisted operation-owned after-state. The caller
// receives ErrRollbackConflict and an audit entry with
// action='rollback_attempted_with_conflict'.
//
// Successful rollback writes action='rollback' to the audit log and marks the snapshot
// status='rolled_back' via SnapshotStore.MarkRolledBack.
package bulkops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	gormpkg "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrRollbackConflict is returned when at least one affected memory or candidate no
// longer matches the state owned by the snapshot operation. Per spec EC-F3.
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
	// ConflictIDs contains memory or candidate IDs whose current state no longer
	// matches the operation-owned post-state (populated when ErrRollbackConflict is returned).
	ConflictIDs []int64 `json:"conflict_ids,omitempty"`
}

// Rollback rolls back a committed bulk_op_snapshot.
//
// Admin gate: identity.Role must be auth.RoleAdmin.
// Conflict check (EC-F3): memory changes and exact candidate after-state mismatches
// refuse rollback atomically (no partial restore). An audit entry with
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
	actor := resolveActor(identity)
	db := memoryStore.GetDB()
	result := &RollbackResult{SnapshotID: snapshotID}
	var conflictIDs []int64

	txErr := db.WithContext(ctx).Transaction(func(tx *gormpkg.DB) error {
		// Lock order is deliberate: snapshot first, then candidate rows, then memory
		// rows, with each entity class locked in sorted ID order. The conflict decision,
		// restore, and status CAS therefore observe one transactional state with no
		// read-to-write TOCTOU window.
		snap, err := snapshotStore.GetForUpdateTx(ctx, tx, snapshotID)
		if err != nil {
			if errors.Is(err, gormpkg.ErrRecordNotFound) {
				return fmt.Errorf("rollback: snapshot %q not found: %w", snapshotID, err)
			}
			return fmt.Errorf("rollback: get snapshot for update: %w", err)
		}
		if snap.Status != models.SnapshotStatusCommitted {
			return fmt.Errorf("rollback: snapshot %q has status %q, expected 'committed': %w",
				snapshotID, snap.Status, ErrSnapshotNotRollbackable)
		}

		// Decode before_state only after the snapshot row is locked, so a concurrent
		// amend/status transition cannot change the rollback contract underneath us.
		typedEntries, err := decodeTypedBeforeState(snap.BeforeState)
		if err != nil {
			return fmt.Errorf("rollback: decode before_state: %w", err)
		}

		// Derive the lock/conflict set from the entries that the restore loop will
		// actually mutate. AffectedMemoryIDs is metadata and can also contain
		// candidate IDs; using it alone can both lock unrelated memories and miss a
		// restore entry if metadata drifts.
		var idsToCheck []int64
		var createdIDsToCheck []int64
		var candidateIDsToLock []int64
		candidateEntriesToCheck := make(map[int64]models.SnapshotEntry)
		for key, entry := range typedEntries {
			entity, id, parseErr := parseSnapshotEntryKey(key)
			if parseErr != nil {
				return fmt.Errorf("rollback: parse entry key %q: %w", key, parseErr)
			}
			if entry.Kind == models.EntryKindDelete {
				createdIDsToCheck = append(createdIDsToCheck, id)
				continue
			}
			if entity == snapshotEntryEntityCandidate ||
				(entity == "" && (snap.OpType == models.SnapshotOpBulkPromote || snap.OpType == models.SnapshotOpCandidateReviewAction)) {
				candidateIDsToLock = append(candidateIDsToLock, id)
				// Candidate rollback is safe only when the snapshot persists the exact
				// operation-owned post-state. Legacy entries without After are still
				// locked, but detectCandidateConflicts fails them closed instead of
				// guessing from snapshot timestamps and overwriting a later edit.
				candidateEntriesToCheck[id] = entry
				continue
			}
			idsToCheck = append(idsToCheck, id)
		}
		candidateIDsToLock = sortedUniqueIDs(candidateIDsToLock)
		idsToCheck = sortedUniqueIDs(idsToCheck)
		createdIDsToCheck = sortedUniqueIDs(createdIDsToCheck)
		if len(candidateIDsToLock) > 0 && candidateStore == nil {
			return fmt.Errorf("rollback: candidateStore required to roll back candidates %v", candidateIDsToLock)
		}
		lockedCandidates, err := lockCandidateRowsForRollbackTx(ctx, tx, candidateIDsToLock)
		if err != nil {
			return fmt.Errorf("rollback: lock affected candidates: %w", err)
		}
		candidateConflicts, err := detectCandidateConflicts(lockedCandidates, candidateEntriesToCheck)
		if err != nil {
			return fmt.Errorf("rollback: candidate conflict detection: %w", err)
		}
		allMemoryIDs := make([]int64, 0, len(idsToCheck)+len(createdIDsToCheck))
		allMemoryIDs = append(allMemoryIDs, idsToCheck...)
		allMemoryIDs = append(allMemoryIDs, createdIDsToCheck...)
		lockedRows, err := memoryStore.LockRawByIDsTx(ctx, tx, allMemoryIDs)
		if err != nil {
			return fmt.Errorf("rollback: lock affected memories: %w", err)
		}

		conflictIDs = append(conflictIDs, candidateConflicts...)
		memoryConflicts, err := detectConflicts(lockedRows, idsToCheck, snap.CreatedAt, snap.OpType, typedEntries)
		if err != nil {
			return fmt.Errorf("rollback: conflict detection: %w", err)
		}
		conflictIDs = append(conflictIDs, memoryConflicts...)
		conflictIDs = append(conflictIDs, detectCreatedRowConflicts(lockedRows, createdIDsToCheck)...)
		conflictIDs = sortedUniqueIDs(conflictIDs)
		if len(conflictIDs) > 0 {
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
				if entity == snapshotEntryEntityCandidate || (entity == "" && (snap.OpType == models.SnapshotOpBulkPromote || snap.OpType == models.SnapshotOpCandidateReviewAction)) {
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

		if err := snapshotStore.MarkRolledBackTx(ctx, tx, snapshotID, time.Now().UTC()); err != nil {
			return fmt.Errorf("rollback: mark snapshot rolled_back: %w", err)
		}

		result.RestoredCount = restored
		return nil
	})

	if txErr != nil {
		if errors.Is(txErr, ErrRollbackConflict) {
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

func sortedUniqueIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })
	return unique
}

func lockCandidateRowsForRollbackTx(
	ctx context.Context,
	tx *gormpkg.DB,
	ids []int64,
) (map[int64]*models.CrystallizationCandidate, error) {
	ids = sortedUniqueIDs(ids)
	locked := make(map[int64]*models.CrystallizationCandidate, len(ids))
	if len(ids) == 0 {
		return locked, nil
	}

	var rows []struct {
		ID int64
	}
	if err := tx.WithContext(ctx).
		Table("crystallization_candidates").
		Select("id").
		Where("id IN ?", ids).
		Order("id ASC").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	found := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		found[row.ID] = struct{}{}
	}
	txCandidateStore := gormdb.NewCandidateStore(tx, nil)
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			continue
		}
		candidate, err := txCandidateStore.Get(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("read locked candidate %d: %w", id, err)
		}
		locked[id] = candidate
	}
	return locked, nil
}

func detectCandidateConflicts(
	currentByID map[int64]*models.CrystallizationCandidate,
	entries map[int64]models.SnapshotEntry,
) ([]int64, error) {
	ids := make([]int64, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	ids = sortedUniqueIDs(ids)

	var conflicts []int64
	for _, id := range ids {
		current, exists := currentByID[id]
		if !exists {
			conflicts = append(conflicts, id)
			continue
		}
		matches, err := matchesExpectedCandidateState(entries[id], current)
		if err != nil {
			return nil, fmt.Errorf("candidate %d: %w", id, err)
		}
		if !matches {
			conflicts = append(conflicts, id)
		}
	}
	return conflicts, nil
}

func matchesExpectedCandidateState(entry models.SnapshotEntry, current *models.CrystallizationCandidate) (bool, error) {
	if current == nil || len(entry.After) == 0 || string(entry.After) == "null" {
		return false, nil
	}

	var expected models.CrystallizationCandidate
	if err := json.Unmarshal(entry.After, &expected); err != nil {
		return false, fmt.Errorf("unmarshal expected candidate after-state: %w", err)
	}
	expected = normalizedCandidateState(expected)
	currentNormalized := normalizedCandidateState(*current)
	return reflect.DeepEqual(expected, currentNormalized), nil
}

func normalizedCandidateState(candidate models.CrystallizationCandidate) models.CrystallizationCandidate {
	candidate.CreatedAt = candidate.CreatedAt.UTC()
	candidate.UpdatedAt = candidate.UpdatedAt.UTC()
	if candidate.ReviewAfter != nil {
		reviewAfter := candidate.ReviewAfter.UTC()
		candidate.ReviewAfter = &reviewAfter
	}
	if len(candidate.EvidenceHandles) == 0 {
		candidate.EvidenceHandles = nil
	}
	if len(candidate.AffectedProjects) == 0 {
		candidate.AffectedProjects = nil
	}
	return candidate
}

// detectConflicts returns the IDs of memories modified after snapshotTime.
// A memory's updated_at > snapshotTime indicates a post-snapshot modification (EC-F3).
func detectConflicts(
	rowsByID map[int64]*gormdb.Memory,
	ids []int64,
	snapshotTime time.Time,
	opType models.SnapshotOpType,
	entries map[string]models.SnapshotEntry,
) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var conflicts []int64
	for _, id := range ids {
		mem, exists := rowsByID[id]
		if !exists {
			// A hard-deleted row has nothing left to restore or overwrite.
			continue
		}
		if entry, ok := snapshotEntryForMemoryID(entries, id); ok {
			expected, matchErr := matchesExpectedOperationMutation(opType, entry, mem)
			if matchErr != nil {
				return nil, fmt.Errorf("detectConflicts: memory %d: %w", id, matchErr)
			}
			if expected {
				continue
			}
		}
		if mem.UpdatedAt.After(snapshotTime) {
			conflicts = append(conflicts, id)
		}
	}
	return conflicts, nil
}

// matchesExpectedOperationMutation separates the bulk operation's own write
// from a later conflicting edit. A plain updated_at > snapshot comparison
// misclassifies every successful delete/supersede as a conflict because those
// operations intentionally update updated_at after capturing before_state.
func matchesExpectedOperationMutation(opType models.SnapshotOpType, entry models.SnapshotEntry, currentRow *gormdb.Memory) (bool, error) {
	if currentRow == nil || len(entry.Before) == 0 || string(entry.Before) == "null" {
		return false, nil
	}

	var before models.Memory
	if err := json.Unmarshal(entry.Before, &before); err != nil {
		return false, fmt.Errorf("unmarshal memory before_state: %w", err)
	}
	currentJSON, err := marshalMemoryRowSnapshot(currentRow)
	if err != nil {
		return false, fmt.Errorf("marshal current memory state: %w", err)
	}
	var current models.Memory
	if err := json.Unmarshal(currentJSON, &current); err != nil {
		return false, fmt.Errorf("unmarshal current memory state: %w", err)
	}

	expected := before
	switch opType {
	case models.SnapshotOpBulkDelete:
		if current.DeletedAt == nil || !current.UpdatedAt.Equal(*current.DeletedAt) {
			return false, nil
		}
		expected.DeletedAt = current.DeletedAt
		expected.UpdatedAt = current.UpdatedAt

	case models.SnapshotOpBulkSupersede:
		if current.DeletedAt != nil || current.Status != "superseded" {
			return false, nil
		}
		if math.Abs(current.ImportanceBase-before.ImportanceBase*0.1) > 0.000001 {
			return false, nil
		}
		expected.Status = current.Status
		expected.ImportanceBase = current.ImportanceBase
		expected.UpdatedAt = current.UpdatedAt

	default:
		return false, nil
	}

	return reflect.DeepEqual(expected, current), nil
}

// detectCreatedRowConflicts returns op-created memory IDs that were modified after creation.
// EntryKindDelete rollback hard-deletes rows that did not exist before the operation, so
// snapshot.created_at is not a valid conflict boundary for them. Instead, the safe-delete
// invariant is created_at == updated_at; any later update means rollback must refuse to
// destroy user-visible edits.
func detectCreatedRowConflicts(rowsByID map[int64]*gormdb.Memory, ids []int64) []int64 {
	var conflicts []int64
	for _, id := range ids {
		mem, exists := rowsByID[id]
		if !exists {
			// Already hard-deleted rows have nothing left for rollback to destroy.
			continue
		}
		if mem.UpdatedAt.After(mem.CreatedAt) {
			conflicts = append(conflicts, id)
		}
	}
	return conflicts
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
