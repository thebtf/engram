// Package bulkops — rollback.go implements snapshot rollback with conflict detection.
//
// Rollback restores the typed memory and crystallization-candidate rows captured
// in a bulk_op_snapshot. Typed entries carry an immutable hash of the exact
// post-mutation row state; rollback locks and compares every target before applying
// a restore. Legacy entries reconstruct only deterministic operation output and
// fail closed when that proof is unavailable.
//
// Successful rollback writes action='rollback' to the audit log and transitions the
// locked committed snapshot to 'rolled_back' with one guarded in-transaction update.
package bulkops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	gormpkg "gorm.io/gorm"
)

// ErrRollbackConflict is returned when at least one affected memory or candidate
// row no longer matches the snapshot's captured post-operation state. Per spec EC-F3.
var ErrRollbackConflict = errors.New("rollback_conflict")

// ErrSnapshotNotRollbackable is returned when the snapshot status is not 'committed'
// (e.g., already rolled back or in preview state).
var ErrSnapshotNotRollbackable = errors.New("snapshot_not_rollbackable")

// ErrLegacySnapshotAmbiguous is returned when a legacy unprefixed promotion
// snapshot collides across candidate and memory identities.
var ErrLegacySnapshotAmbiguous = errors.New("legacy_snapshot_ambiguous")

// RollbackConflictRef identifies a conflicted rollback row without conflating
// independent memory and candidate numeric ID sequences.
type RollbackConflictRef struct {
	Entity string `json:"entity"`
	ID     int64  `json:"id"`
}

// RollbackResult is the outcome of a Rollback call.
type RollbackResult struct {
	// SnapshotID is the snapshot that was rolled back.
	SnapshotID string `json:"snapshot_id"`
	// RestoredCount is the number of memory or candidate rows restored.
	RestoredCount int `json:"restored_count"`
	// ConflictIDs preserves the legacy numeric response.
	ConflictIDs []int64 `json:"conflict_ids,omitempty"`
	// ConflictRefs distinguishes conflicts with identical numeric IDs.
	ConflictRefs []RollbackConflictRef `json:"conflict_refs,omitempty"`
}

// Rollback rolls back a committed bulk_op_snapshot.
//
// Admin gate: identity.Role must be auth.RoleAdmin.
// Conflict detection locks every affected row and compares its immutable post-state
// token before any mutation. A conflict refuses the entire restore atomically and
// writes action='rollback_attempted_with_conflict'.
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
		legacyCandidates := parseLegacyCandidateParameters(lockedSnap.Parameters)
		if err := validateLegacyCandidateEntries(lockedSnap, typedEntries, legacyCandidates); err != nil {
			return err
		}
		resolvedEntries, err := resolveSnapshotEntries(lockedSnap, typedEntries, legacyCandidates)
		if err != nil {
			return fmt.Errorf("rollback: resolve snapshot entries: %w", err)
		}
		conflicts, err := detectSnapshotEntryConflictsTx(ctx, tx, lockedSnap, resolvedEntries, legacyCandidates, memoryStore, candidateStore)
		if err != nil {
			return fmt.Errorf("rollback: conflict detection: %w", err)
		}
		if len(conflicts) > 0 {
			result.ConflictIDs = make([]int64, len(conflicts))
			result.ConflictRefs = make([]RollbackConflictRef, len(conflicts))
			for i, conflict := range conflicts {
				result.ConflictIDs[i] = conflict.id
				result.ConflictRefs[i] = RollbackConflictRef{Entity: conflict.entity, ID: conflict.id}
			}
			return ErrRollbackConflict
		}

		var restored int

		for _, resolved := range resolvedEntries {
			entry := resolved.entry
			switch entry.Kind {
			case models.EntryKindDelete:
				// Row was CREATED by the op; hard-delete it within the transaction.
				// candidate.promoted_memory_id is SET NULL by FK (ON DELETE SET NULL, EC-F4).
				if delErr := memoryStore.HardDeleteTx(ctx, tx, resolved.id); delErr != nil {
					return fmt.Errorf("rollback: hard-delete created memory %d: %w", resolved.id, delErr)
				}

			case models.EntryKindRestore, "": // empty kind = legacy flat format (treated as restore)
				if len(entry.Before) == 0 || string(entry.Before) == "null" {
					// Empty before-state: row didn't exist pre-op; skip (same as legacy skip).
					continue
				}
				if resolved.entity == snapshotEntryEntityCandidate {
					// Rollback must revert the candidate row to its pre-op state (e.g., pending),
					// NOT write candidate data into the memories table.
					if candidateStore == nil {
						return fmt.Errorf("rollback: candidateStore required to roll back candidate %d", resolved.id)
					}
					var c models.CrystallizationCandidate
					if unmarshalErr := json.Unmarshal(entry.Before, &c); unmarshalErr != nil {
						return fmt.Errorf("rollback: unmarshal candidate %d: %w", resolved.id, unmarshalErr)
					}
					normalizeLegacyCandidateBeforeState(&c)
					c.ID = resolved.id
					if revertErr := candidateStore.RevertRawTx(ctx, tx, &c); revertErr != nil {
						return fmt.Errorf("rollback: revert candidate %d: %w", resolved.id, revertErr)
					}
				} else {
					var mem models.Memory
					if unmarshalErr := json.Unmarshal(entry.Before, &mem); unmarshalErr != nil {
						return fmt.Errorf("rollback: unmarshal memory %d: %w", resolved.id, unmarshalErr)
					}
					normalizeLegacyMemoryBeforeState(&mem)
					mem.ID = resolved.id
					if restoreErr := memoryStore.RestoreRawTx(ctx, tx, &mem); restoreErr != nil {
						return fmt.Errorf("rollback: restore memory %d: %w", resolved.id, restoreErr)
					}
				}
				restored++

			default:
				return fmt.Errorf("rollback: unknown entry kind %q for id %d", entry.Kind, resolved.id)
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

type legacyCandidateParameters struct {
	ids    []int64
	byID   map[int64]struct{}
	action string
	valid  bool
}

type resolvedSnapshotEntry struct {
	entity string
	id     int64
	entry  models.SnapshotEntry
}

func parseLegacyCandidateParameters(parameters json.RawMessage) legacyCandidateParameters {
	var raw map[string]json.RawMessage
	if len(parameters) == 0 || json.Unmarshal(parameters, &raw) != nil {
		return legacyCandidateParameters{}
	}
	plural, hasPlural := raw["candidate_ids"]
	singular, hasSingular := raw["candidate_id"]
	if hasPlural == hasSingular {
		return legacyCandidateParameters{}
	}
	var ids []int64
	if hasPlural {
		if json.Unmarshal(plural, &ids) != nil {
			return legacyCandidateParameters{}
		}
	} else {
		var id int64
		if json.Unmarshal(singular, &id) != nil {
			return legacyCandidateParameters{}
		}
		ids = []int64{id}
	}
	action := ""
	if encodedAction, ok := raw["action"]; ok && json.Unmarshal(encodedAction, &action) != nil {
		return legacyCandidateParameters{}
	}
	byID := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		byID[id] = struct{}{}
	}
	return legacyCandidateParameters{ids: ids, byID: byID, action: action, valid: true}
}

func resolveSnapshotEntries(snap *models.BulkOpSnapshot, entries map[string]models.SnapshotEntry, legacyCandidates legacyCandidateParameters) ([]resolvedSnapshotEntry, error) {
	resolved := make([]resolvedSnapshotEntry, 0, len(entries))
	for key, entry := range entries {
		entity, id, err := parseSnapshotEntryKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse entry key %q: %w", key, err)
		}
		if entity == "" {
			if snap.OpType == models.SnapshotOpBulkPromote || snap.OpType == models.SnapshotOpCandidateReviewAction {
				if !legacyCandidates.valid {
					return nil, ErrLegacySnapshotAmbiguous
				}
				if _, ok := legacyCandidates.byID[id]; ok {
					entity = snapshotEntryEntityCandidate
				} else if containsSnapshotID(snap.AffectedMemoryIDs, id) {
					entity = snapshotEntryEntityMemory
				} else {
					return nil, ErrLegacySnapshotAmbiguous
				}
			} else {
				entity = snapshotEntryEntityMemory
			}
		}
		resolved = append(resolved, resolvedSnapshotEntry{entity: entity, id: id, entry: entry})
	}
	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].entity != resolved[j].entity {
			return resolved[i].entity < resolved[j].entity
		}
		return resolved[i].id < resolved[j].id
	})
	return resolved, nil
}

// validateLegacyCandidateEntries resolves old unprefixed promotion entries from
// their immutable operation parameters, never from coincident table row IDs.
func validateLegacyCandidateEntries(snap *models.BulkOpSnapshot, entries map[string]models.SnapshotEntry, legacyCandidates legacyCandidateParameters) error {
	if (snap.OpType != models.SnapshotOpBulkPromote && snap.OpType != models.SnapshotOpCandidateReviewAction) || !hasUnprefixedSnapshotEntry(entries) {
		return nil
	}
	if !legacyCandidates.valid {
		return ErrLegacySnapshotAmbiguous
	}
	if snap.OpType == models.SnapshotOpCandidateReviewAction {
		switch legacyCandidates.action {
		case "reject", "suppress", "supersede":
			if len(legacyCandidates.ids) != 1 || len(snap.AffectedMemoryIDs) != 0 || !hasExactlyOneSnapshotEntry(entries, fmt.Sprintf("candidate:%d", legacyCandidates.ids[0]), fmt.Sprintf("%d", legacyCandidates.ids[0]), models.EntryKindRestore) {
				return fmt.Errorf("rollback: legacy %s snapshot lacks a complete candidate mapping: %w", snap.OpType, ErrLegacySnapshotAmbiguous)
			}
			return nil
		case "promote", "preserve":
		default:
			return ErrLegacySnapshotAmbiguous
		}
	}
	return validateLegacyPromotionMapping(snap, entries, legacyCandidates.ids)
}

func validateLegacyPromotionMapping(snap *models.BulkOpSnapshot, entries map[string]models.SnapshotEntry, candidateIDs []int64) error {
	if len(candidateIDs) != len(snap.AffectedMemoryIDs) {
		return ErrLegacySnapshotAmbiguous
	}
	seenCandidates := make(map[int64]struct{}, len(candidateIDs))
	seenMemories := make(map[int64]struct{}, len(snap.AffectedMemoryIDs))
	for index, candidateID := range candidateIDs {
		memoryID := snap.AffectedMemoryIDs[index]
		if _, duplicate := seenCandidates[candidateID]; duplicate || containsSnapshotID(snap.AffectedMemoryIDs, candidateID) {
			return fmt.Errorf("rollback: legacy %s candidate/memory ID %d overlaps: %w", snap.OpType, candidateID, ErrLegacySnapshotAmbiguous)
		}
		if _, duplicate := seenMemories[memoryID]; duplicate {
			return ErrLegacySnapshotAmbiguous
		}
		seenCandidates[candidateID] = struct{}{}
		seenMemories[memoryID] = struct{}{}
		if !hasExactlyOneSnapshotEntry(entries, fmt.Sprintf("candidate:%d", candidateID), fmt.Sprintf("%d", candidateID), models.EntryKindRestore) || !hasExactlyOneSnapshotEntry(entries, fmt.Sprintf("memory:%d", memoryID), fmt.Sprintf("%d", memoryID), models.EntryKindDelete) {
			return fmt.Errorf("rollback: legacy %s snapshot lacks a complete candidate/memory mapping: %w", snap.OpType, ErrLegacySnapshotAmbiguous)
		}
	}
	return nil
}

func hasExactlyOneSnapshotEntry(entries map[string]models.SnapshotEntry, prefixed, unprefixed string, kind models.SnapshotEntryKind) bool {
	prefixedEntry, hasPrefixed := entries[prefixed]
	unprefixedEntry, hasUnprefixed := entries[unprefixed]
	if hasPrefixed == hasUnprefixed {
		return false
	}
	if hasPrefixed {
		return prefixedEntry.Kind == kind
	}
	return unprefixedEntry.Kind == kind
}

func hasUnprefixedSnapshotEntry(entries map[string]models.SnapshotEntry) bool {
	for key := range entries {
		entity, _, err := parseSnapshotEntryKey(key)
		if err == nil && entity == "" {
			return true
		}
	}
	return false
}

func containsSnapshotID(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func detectSnapshotEntryConflictsTx(ctx context.Context, tx *gormpkg.DB, snap *models.BulkOpSnapshot, entries []resolvedSnapshotEntry, legacyCandidates legacyCandidateParameters, memoryStore *gormdb.MemoryStore, candidateStore *gormdb.CandidateStore) ([]resolvedSnapshotEntry, error) {
	lockedCandidates, lockedMemories, err := lockRollbackRowsTx(ctx, tx, snap, entries, legacyCandidates, memoryStore, candidateStore)
	if err != nil {
		return nil, err
	}
	conflicts := make([]resolvedSnapshotEntry, 0)
	for _, resolved := range entries {
		entry := resolved.entry
		switch resolved.entity {
		case snapshotEntryEntityCandidate:
			candidate := lockedCandidates[resolved.id]
			if candidate == nil {
				conflicts = append(conflicts, resolved)
				continue
			}
			if entry.PostStateToken == "" {
				memoryID, _ := mappedPromotedMemoryID(snap, legacyCandidates.ids, resolved.id)
				if !legacyCandidatePostStateMatches(snap, legacyCandidates, resolved.id, entry, candidate, lockedMemories[memoryID]) {
					conflicts = append(conflicts, resolved)
				}
				continue
			}
			token, tokenErr := models.SnapshotStateToken(candidate)
			if tokenErr != nil {
				return nil, tokenErr
			}
			if token != entry.PostStateToken && !candidateTokenMatchesMissingPromotedMemory(snap, legacyCandidates, resolved.id, entry.PostStateToken, candidate, lockedMemories) {
				conflicts = append(conflicts, resolved)
			}
		case snapshotEntryEntityMemory:
			memory := lockedMemories[resolved.id]
			if memory == nil {
				if entry.Kind != models.EntryKindDelete {
					conflicts = append(conflicts, resolved)
				}
				continue
			}
			if entry.PostStateToken != "" {
				token, tokenErr := models.SnapshotStateToken(memory)
				if tokenErr != nil {
					return nil, tokenErr
				}
				if token != entry.PostStateToken {
					conflicts = append(conflicts, resolved)
				}
				continue
			}
			if entry.ExpectedVersion != nil && memory.Version != *entry.ExpectedVersion {
				conflicts = append(conflicts, resolved)
				continue
			}
			if entry.Kind == models.EntryKindDelete || !legacyMemoryPostStateMatches(snap.OpType, entry, memory) {
				conflicts = append(conflicts, resolved)
			}
		default:
			return nil, fmt.Errorf("unknown snapshot entity %q", resolved.entity)
		}
	}
	return conflicts, nil
}

// lockRollbackRowsTx acquires every rollback target before any semantic check.
func lockRollbackRowsTx(ctx context.Context, tx *gormpkg.DB, snap *models.BulkOpSnapshot, entries []resolvedSnapshotEntry, legacyCandidates legacyCandidateParameters, memoryStore *gormdb.MemoryStore, candidateStore *gormdb.CandidateStore) (map[int64]*models.CrystallizationCandidate, map[int64]*models.Memory, error) {
	candidateIDs := make(map[int64]struct{})
	memoryIDs := make(map[int64]struct{})
	for _, entry := range entries {
		if entry.entity == snapshotEntryEntityCandidate {
			candidateIDs[entry.id] = struct{}{}
			if memoryID, ok := mappedPromotedMemoryID(snap, legacyCandidates.ids, entry.id); ok {
				memoryIDs[memoryID] = struct{}{}
			}
		} else {
			memoryIDs[entry.id] = struct{}{}
		}
	}
	lockedCandidates := make(map[int64]*models.CrystallizationCandidate, len(candidateIDs))
	if len(candidateIDs) > 0 && candidateStore == nil {
		return nil, nil, errors.New("candidate store required to inspect rollback candidates")
	}
	for _, id := range sortedSnapshotIDs(candidateIDs) {
		candidate, err := candidateStore.GetForUpdateTx(ctx, tx, id)
		if err != nil && !errors.Is(err, gormpkg.ErrRecordNotFound) {
			return nil, nil, err
		}
		lockedCandidates[id] = candidate
	}
	lockedMemories := make(map[int64]*models.Memory, len(memoryIDs))
	for _, id := range sortedSnapshotIDs(memoryIDs) {
		memory, err := memoryStore.GetForRollbackTx(ctx, tx, id)
		if err != nil && !errors.Is(err, gormpkg.ErrRecordNotFound) {
			return nil, nil, err
		}
		lockedMemories[id] = memory
	}
	return lockedCandidates, lockedMemories, nil
}

func sortedSnapshotIDs(ids map[int64]struct{}) []int64 {
	ordered := make([]int64, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered
}

// legacyMemoryPostStateMatches reconstructs only the deterministic output of
// old bulk mutations. It deliberately never compares application and DB clocks.
func legacyMemoryPostStateMatches(opType models.SnapshotOpType, entry models.SnapshotEntry, current *models.Memory) bool {
	if len(entry.Before) == 0 || string(entry.Before) == "null" || current == nil {
		return false
	}
	var before models.Memory
	if json.Unmarshal(entry.Before, &before) != nil {
		return false
	}
	expected := before
	normalizeLegacyMemoryBeforeState(&expected)
	switch opType {
	case models.SnapshotOpBulkDelete:
		if current.DeletedAt == nil {
			return false
		}
		expected.DeletedAt = current.DeletedAt
	case models.SnapshotOpBulkSupersede:
		if current.Status != "superseded" || current.ImportanceBase != before.ImportanceBase*0.1 {
			return false
		}
		expected.Status = current.Status
		expected.ImportanceBase = current.ImportanceBase
	default:
		return false
	}
	expected.UpdatedAt = current.UpdatedAt
	expected.Version = before.Version + 1
	expectedToken, err := models.SnapshotStateToken(&expected)
	if err != nil {
		return false
	}
	currentToken, err := models.SnapshotStateToken(current)
	return err == nil && expectedToken == currentToken
}

func normalizeLegacyMemoryBeforeState(memory *models.Memory) {
	if memory != nil && memory.PrivacyScope == "" {
		// privacy_scope was added with a deterministic PostgreSQL default. Older
		// snapshots can omit it even though the persisted row was project-scoped.
		memory.PrivacyScope = "project"
	}
}

func legacyCandidatePostStateMatches(snap *models.BulkOpSnapshot, legacyCandidates legacyCandidateParameters, candidateID int64, entry models.SnapshotEntry, current *models.CrystallizationCandidate, promotedMemory *models.Memory) bool {
	if len(entry.Before) == 0 || current == nil {
		return false
	}
	var before models.CrystallizationCandidate
	if json.Unmarshal(entry.Before, &before) != nil {
		return false
	}
	normalizeLegacyCandidateBeforeState(&before)
	expected := before
	if snap.OpType == models.SnapshotOpCandidateReviewAction {
		switch legacyCandidates.action {
		case "reject", "suppress":
			if current.Status != models.CandidateStatusRejected {
				return false
			}
			expected.Status = models.CandidateStatusRejected
		case "supersede":
			if current.Status != models.CandidateStatusSuperseded {
				return false
			}
			expected.Status = models.CandidateStatusSuperseded
		case "promote", "preserve":
			if !legacyPromotedCandidateMatches(snap, legacyCandidates, candidateID, current, promotedMemory) {
				return false
			}
			expected.Status, expected.PromotedMemoryID = models.CandidateStatusPromoted, current.PromotedMemoryID
		default:
			return false
		}
	} else if snap.OpType == models.SnapshotOpBulkPromote {
		if !legacyPromotedCandidateMatches(snap, legacyCandidates, candidateID, current, promotedMemory) {
			return false
		}
		expected.Status, expected.PromotedMemoryID = models.CandidateStatusPromoted, current.PromotedMemoryID
	} else {
		return false
	}
	expected.UpdatedAt = current.UpdatedAt
	expectedToken, err := models.SnapshotStateToken(&expected)
	if err != nil {
		return false
	}
	currentToken, err := models.SnapshotStateToken(current)
	return err == nil && expectedToken == currentToken
}

func legacyPromotedCandidateMatches(snap *models.BulkOpSnapshot, legacyCandidates legacyCandidateParameters, candidateID int64, current *models.CrystallizationCandidate, promotedMemory *models.Memory) bool {
	memoryID, ok := mappedPromotedMemoryID(snap, legacyCandidates.ids, candidateID)
	if !ok || current.Status != models.CandidateStatusPromoted {
		return false
	}
	return (current.PromotedMemoryID == nil && promotedMemory == nil) || (current.PromotedMemoryID != nil && *current.PromotedMemoryID == memoryID)
}

func mappedPromotedMemoryID(snap *models.BulkOpSnapshot, candidateIDs []int64, candidateID int64) (int64, bool) {
	if len(candidateIDs) != len(snap.AffectedMemoryIDs) {
		return 0, false
	}
	for index, id := range candidateIDs {
		if id == candidateID {
			return snap.AffectedMemoryIDs[index], true
		}
	}
	return 0, false
}

func candidateTokenMatchesMissingPromotedMemory(snap *models.BulkOpSnapshot, legacyCandidates legacyCandidateParameters, candidateID int64, expectedToken string, current *models.CrystallizationCandidate, lockedMemories map[int64]*models.Memory) bool {
	if current == nil || current.Status != models.CandidateStatusPromoted || current.PromotedMemoryID != nil {
		return false
	}
	if snap.OpType == models.SnapshotOpCandidateReviewAction && legacyCandidates.action != "promote" && legacyCandidates.action != "preserve" {
		return false
	}
	if snap.OpType != models.SnapshotOpCandidateReviewAction && snap.OpType != models.SnapshotOpBulkPromote {
		return false
	}
	memoryID, ok := mappedPromotedMemoryID(snap, legacyCandidates.ids, candidateID)
	if !ok || lockedMemories[memoryID] != nil {
		return false
	}
	relinked := *current
	relinked.PromotedMemoryID = &memoryID
	token, err := models.SnapshotStateToken(&relinked)
	return err == nil && token == expectedToken
}

func normalizeLegacyCandidateBeforeState(candidate *models.CrystallizationCandidate) {
	if candidate == nil {
		return
	}
	if candidate.PrivacyScope == "" {
		candidate.PrivacyScope = "project"
	}
	candidate.CreatedAt = candidate.CreatedAt.Truncate(time.Microsecond)
	if candidate.ReviewAfter != nil {
		reviewAfter := candidate.ReviewAfter.Truncate(time.Microsecond)
		candidate.ReviewAfter = &reviewAfter
	}
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
