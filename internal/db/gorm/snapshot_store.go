// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/thebtf/engram/pkg/models"
)

// snapshotRow is the GORM model for the bulk_op_snapshots table.
// It mirrors the schema created by migration 133.
type snapshotRow struct {
	CreatedAt         time.Time  `gorm:"column:created_at;autoCreateTime"`
	RolledBackAt      *time.Time `gorm:"column:rolled_back_at"`
	SnapshotID        string     `gorm:"column:snapshot_id;not null;uniqueIndex"`
	OpType            string     `gorm:"column:op_type;not null"`
	Actor             string     `gorm:"column:actor;not null"`
	SourceSessionID   string     `gorm:"column:source_session_id;not null;default:''"`
	Parameters        JSONRaw    `gorm:"column:parameters;type:jsonb;not null;default:'{}'"`
	AffectedMemoryIDs Int64Array `gorm:"column:affected_memory_ids;type:bigint[]"`
	BeforeState       JSONRaw    `gorm:"column:before_state;type:jsonb;not null"`
	Status            string     `gorm:"column:status;not null;default:'committed'"`
	Pinned            bool       `gorm:"column:pinned;not null;default:false"`
	ID                int64      `gorm:"primaryKey;autoIncrement"`
}

func (snapshotRow) TableName() string { return "bulk_op_snapshots" }

// Int64Array is a []int64 that stores/retrieves a PostgreSQL BIGINT[] column.
type Int64Array []int64

func (a Int64Array) Value() (driver.Value, error) {
	if len(a) == 0 {
		return "{}", nil
	}
	// Format: {1,2,3}
	s := "{"
	for i, v := range a {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf("%d", v)
	}
	s += "}"
	return s, nil
}

func (a *Int64Array) Scan(src interface{}) error {
	if src == nil {
		*a = []int64{}
		return nil
	}
	// postgres returns int64[] as a []int64 or string representation
	switch v := src.(type) {
	case []byte:
		return a.parsePostgresArray(string(v))
	case string:
		return a.parsePostgresArray(v)
	case []int64:
		*a = append((*a)[:0], v...)
		return nil
	}
	return fmt.Errorf("int64_array: unsupported Scan source type %T", src)
}

// parsePostgresArray parses the {1,2,3} PostgreSQL array format.
func (a *Int64Array) parsePostgresArray(s string) error {
	if s == "{}" || s == "" {
		*a = []int64{}
		return nil
	}
	// Strip braces
	if len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}' {
		s = s[1 : len(s)-1]
	}
	if s == "" {
		*a = []int64{}
		return nil
	}
	// Split on comma and parse each element
	var out []int64
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			elem := s[start:i]
			if elem != "" {
				var v int64
				_, err := fmt.Sscanf(elem, "%d", &v)
				if err != nil {
					return fmt.Errorf("int64_array: parse %q: %w", elem, err)
				}
				out = append(out, v)
			}
			start = i + 1
		}
	}
	*a = out
	return nil
}

// toDomainSnapshot converts a snapshotRow to a models.BulkOpSnapshot.
func toDomainSnapshot(r *snapshotRow) *models.BulkOpSnapshot {
	params := json.RawMessage(r.Parameters)
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	bs := json.RawMessage(r.BeforeState)
	if len(bs) == 0 {
		bs = json.RawMessage(`{}`)
	}
	snap := &models.BulkOpSnapshot{
		ID:                r.ID,
		SnapshotID:        r.SnapshotID,
		OpType:            models.SnapshotOpType(r.OpType),
		Actor:             r.Actor,
		SourceSessionID:   r.SourceSessionID,
		Parameters:        params,
		AffectedMemoryIDs: []int64(r.AffectedMemoryIDs),
		BeforeState:       bs,
		Status:            models.SnapshotStatus(r.Status),
		Pinned:            r.Pinned,
		CreatedAt:         r.CreatedAt,
		RolledBackAt:      r.RolledBackAt,
	}
	if snap.AffectedMemoryIDs == nil {
		snap.AffectedMemoryIDs = []int64{}
	}
	return snap
}

// fromDomainSnapshot converts a models.BulkOpSnapshot to a snapshotRow.
func fromDomainSnapshot(s *models.BulkOpSnapshot) *snapshotRow {
	params := JSONRaw(s.Parameters)
	if len(params) == 0 {
		params = JSONRaw(`{}`)
	}
	bs := JSONRaw(s.BeforeState)
	if len(bs) == 0 {
		bs = JSONRaw(`{}`)
	}
	r := &snapshotRow{
		SnapshotID:        s.SnapshotID,
		OpType:            string(s.OpType),
		Actor:             s.Actor,
		SourceSessionID:   s.SourceSessionID,
		Parameters:        params,
		AffectedMemoryIDs: Int64Array(s.AffectedMemoryIDs),
		BeforeState:       bs,
		Status:            string(s.Status),
		Pinned:            s.Pinned,
		RolledBackAt:      s.RolledBackAt,
	}
	if r.Status == "" {
		r.Status = string(models.SnapshotStatusCommitted)
	}
	return r
}

// SnapshotStore provides CRUD operations for bulk_op_snapshots.
type SnapshotStore struct {
	db *gorm.DB
}

// NewSnapshotStore creates a new SnapshotStore.
func NewSnapshotStore(db *gorm.DB) *SnapshotStore {
	return &SnapshotStore{db: db}
}

// Create inserts a new snapshot.
// The full before_state JSONB row snapshot is stored per ADR-F-003.
// Returns the stored snapshot with its DB-assigned ID and CreatedAt.
func (s *SnapshotStore) Create(ctx context.Context, snap *models.BulkOpSnapshot) (*models.BulkOpSnapshot, error) {
	return s.createTx(ctx, s.db, snap)
}

// CreateTx inserts a snapshot using the caller's transaction.
func (s *SnapshotStore) CreateTx(ctx context.Context, tx *gorm.DB, snap *models.BulkOpSnapshot) (*models.BulkOpSnapshot, error) {
	return s.createTx(ctx, tx, snap)
}

func (s *SnapshotStore) createTx(ctx context.Context, tx *gorm.DB, snap *models.BulkOpSnapshot) (*models.BulkOpSnapshot, error) {
	if snap == nil {
		return nil, fmt.Errorf("snapshot_store create: snapshot must not be nil")
	}
	if snap.SnapshotID == "" {
		return nil, fmt.Errorf("snapshot_store create: snapshot_id is required")
	}
	row := fromDomainSnapshot(snap)
	result := tx.WithContext(ctx).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("snapshot_store create: %w", result.Error)
	}
	return toDomainSnapshot(row), nil
}

// Get retrieves a snapshot by its SnapshotID (the ULID/UUID unique field).
// Returns gorm.ErrRecordNotFound if absent.
func (s *SnapshotStore) Get(ctx context.Context, snapshotID string) (*models.BulkOpSnapshot, error) {
	var row snapshotRow
	if err := s.db.WithContext(ctx).Where("snapshot_id = ?", snapshotID).First(&row).Error; err != nil {
		return nil, fmt.Errorf("snapshot_store get %q: %w", snapshotID, err)
	}
	return toDomainSnapshot(&row), nil
}

// GetForUpdateTx retrieves the current snapshot state while holding its row lock.
func (s *SnapshotStore) GetForUpdateTx(ctx context.Context, tx *gorm.DB, snapshotID string) (*models.BulkOpSnapshot, error) {
	var row snapshotRow
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("snapshot_id = ?", snapshotID).First(&row).Error; err != nil {
		return nil, fmt.Errorf("snapshot_store get_for_update %q: %w", snapshotID, err)
	}
	return toDomainSnapshot(&row), nil
}

// GetByID retrieves a snapshot by its numeric primary key.
// Returns gorm.ErrRecordNotFound if absent.
func (s *SnapshotStore) GetByID(ctx context.Context, id int64) (*models.BulkOpSnapshot, error) {
	var row snapshotRow
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, fmt.Errorf("snapshot_store get_by_id %d: %w", id, err)
	}
	return toDomainSnapshot(&row), nil
}

// List returns recent snapshots filtered by optional op_type and actor.
// limit <= 0 defaults to 20.
func (s *SnapshotStore) List(ctx context.Context, opType models.SnapshotOpType, actor string, limit int) ([]*models.BulkOpSnapshot, error) {
	if limit <= 0 {
		limit = 20
	}
	q := s.db.WithContext(ctx).Order("created_at DESC").Limit(limit)
	if opType != "" {
		q = q.Where("op_type = ?", string(opType))
	}
	if actor != "" {
		q = q.Where("actor = ?", actor)
	}
	var rows []snapshotRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("snapshot_store list: %w", err)
	}
	out := make([]*models.BulkOpSnapshot, len(rows))
	for i := range rows {
		out[i] = toDomainSnapshot(&rows[i])
	}
	return out, nil
}

// MarkRolledBack transitions the snapshot status to rolled_back and sets rolled_back_at.
func (s *SnapshotStore) MarkRolledBack(ctx context.Context, snapshotID string) error {
	now := time.Now().UTC()
	res := s.db.WithContext(ctx).
		Model(&snapshotRow{}).
		Where("snapshot_id = ? AND status = 'committed'", snapshotID).
		Updates(map[string]any{
			"status":         string(models.SnapshotStatusRolledBack),
			"rolled_back_at": now,
		})
	if res.Error != nil {
		return fmt.Errorf("snapshot_store mark_rolled_back %q: %w", snapshotID, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("snapshot_store mark_rolled_back %q: not found or already rolled back", snapshotID)
	}
	return nil
}

// Pin marks the snapshot as pinned (exempt from auto-prune).
func (s *SnapshotStore) Pin(ctx context.Context, snapshotID string) error {
	res := s.db.WithContext(ctx).
		Model(&snapshotRow{}).
		Where("snapshot_id = ?", snapshotID).
		Update("pinned", true)
	if res.Error != nil {
		return fmt.Errorf("snapshot_store pin %q: %w", snapshotID, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("snapshot_store pin %q: not found", snapshotID)
	}
	return nil
}

// AmendPromoteEntries adds EntryKindDelete typed entries for memory IDs that were
// created by a promotion. Promotion callers must run this in the same transaction
// that creates the snapshot and promoted memory.
func (s *SnapshotStore) AmendPromoteEntries(ctx context.Context, snapshotID string, promotedMemoryIDs []int64) error {
	if len(promotedMemoryIDs) == 0 {
		return nil
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.amendPromoteEntriesTx(ctx, tx, snapshotID, promotedMemoryIDs)
	})
}

// AmendPromoteEntriesTx amends a snapshot using the caller's transaction.
func (s *SnapshotStore) AmendPromoteEntriesTx(ctx context.Context, tx *gorm.DB, snapshotID string, promotedMemoryIDs []int64) error {
	return s.amendPromoteEntriesTx(ctx, tx, snapshotID, promotedMemoryIDs)
}

// AmendPromoteEntriesWithCandidatesTx records the exact locked candidate state
// and the memories created by successful promotions in the caller's transaction.
func (s *SnapshotStore) AmendPromoteEntriesWithCandidatesTx(ctx context.Context, tx *gorm.DB, snapshotID string, candidateBefore map[int64]json.RawMessage, promotedMemoryIDs []int64) error {
	return s.amendPromoteEntriesWithCandidatesTx(ctx, tx, snapshotID, candidateBefore, promotedMemoryIDs)
}

func (s *SnapshotStore) amendPromoteEntriesTx(ctx context.Context, tx *gorm.DB, snapshotID string, promotedMemoryIDs []int64) error {
	return s.amendPromoteEntriesWithCandidatesTx(ctx, tx, snapshotID, nil, promotedMemoryIDs)
}

func (s *SnapshotStore) amendPromoteEntriesWithCandidatesTx(ctx context.Context, tx *gorm.DB, snapshotID string, candidateBefore map[int64]json.RawMessage, promotedMemoryIDs []int64) error {
	if len(candidateBefore) == 0 && len(promotedMemoryIDs) == 0 {
		return nil
	}

	// Lock the row for update to prevent concurrent amend races.
	var row snapshotRow
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("snapshot_id = ?", snapshotID).
		First(&row).Error; err != nil {
		return fmt.Errorf("amend_promote_entries: get snapshot %q: %w", snapshotID, err)
	}

	// Decode existing before_state.
	existing := make(map[string]json.RawMessage)
	if len(row.BeforeState) > 0 && string(row.BeforeState) != "{}" {
		if err := json.Unmarshal([]byte(row.BeforeState), &existing); err != nil {
			return fmt.Errorf("amend_promote_entries: decode before_state: %w", err)
		}
	}

	for candidateID, before := range candidateBefore {
		var candidate candidateRow
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&candidate, candidateID).Error; err != nil {
			return fmt.Errorf("amend_promote_entries: lock candidate %d: %w", candidateID, err)
		}
		token, err := models.SnapshotStateToken(toDomainCandidate(&candidate))
		if err != nil {
			return fmt.Errorf("amend_promote_entries: token candidate %d: %w", candidateID, err)
		}
		entry, err := json.Marshal(models.SnapshotEntry{Kind: models.EntryKindRestore, Before: before, PostStateToken: token})
		if err != nil {
			return fmt.Errorf("amend_promote_entries: serialize candidate %d: %w", candidateID, err)
		}
		existing[fmt.Sprintf("candidate:%d", candidateID)] = json.RawMessage(entry)
	}

	// Candidate-review snapshots are created before the candidate mutation. When
	// their caller later amends promoted-memory entries, bind every pre-existing
	// candidate restore entry to its exact post-mutation state as well.
	for key, raw := range existing {
		if len(key) <= len("candidate:") || key[:len("candidate:")] != "candidate:" {
			continue
		}
		var entry models.SnapshotEntry
		if err := json.Unmarshal(raw, &entry); err != nil || entry.Kind != models.EntryKindRestore || entry.PostStateToken != "" {
			continue
		}
		candidateID, err := strconv.ParseInt(key[len("candidate:"):], 10, 64)
		if err != nil {
			return fmt.Errorf("amend_promote_entries: parse candidate key %q: %w", key, err)
		}
		var candidate candidateRow
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&candidate, candidateID).Error; err != nil {
			return fmt.Errorf("amend_promote_entries: lock candidate %d: %w", candidateID, err)
		}
		token, err := models.SnapshotStateToken(toDomainCandidate(&candidate))
		if err != nil {
			return fmt.Errorf("amend_promote_entries: token candidate %d: %w", candidateID, err)
		}
		entry.PostStateToken = token
		encoded, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("amend_promote_entries: serialize candidate %d: %w", candidateID, err)
		}
		existing[key] = encoded
	}

	// Use entity-prefixed keys whenever a snapshot contains both candidate and
	// memory IDs; independent table sequences can otherwise collide.
	for _, memID := range promotedMemoryIDs {
		var memory Memory
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&memory, memID).Error; err != nil {
			return fmt.Errorf("amend_promote_entries: lock memory %d: %w", memID, err)
		}
		token, err := models.SnapshotStateToken(memoryRowToSnapshotModel(&memory))
		if err != nil {
			return fmt.Errorf("amend_promote_entries: token memory %d: %w", memID, err)
		}
		deleteEntry, err := json.Marshal(models.SnapshotEntry{Kind: models.EntryKindDelete, PostStateToken: token})
		if err != nil {
			return fmt.Errorf("amend_promote_entries: serialize memory %d: %w", memID, err)
		}
		key := fmt.Sprintf("%d", memID)
		if row.OpType == string(models.SnapshotOpCandidateReviewAction) || row.OpType == string(models.SnapshotOpBulkPromote) {
			key = fmt.Sprintf("memory:%d", memID)
		}
		existing[key] = json.RawMessage(deleteEntry)
	}

	// Merge promoted memory IDs into AffectedMemoryIDs as a set (no duplicates).
	seen := make(map[int64]struct{}, len(row.AffectedMemoryIDs)+len(promotedMemoryIDs))
	allMemoryIDs := make(Int64Array, 0, len(row.AffectedMemoryIDs)+len(promotedMemoryIDs))
	for _, id := range row.AffectedMemoryIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		allMemoryIDs = append(allMemoryIDs, id)
	}
	for _, id := range promotedMemoryIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		allMemoryIDs = append(allMemoryIDs, id)
	}

	// Serialize and write back within the same transaction.
	amended, err := json.Marshal(existing)
	if err != nil {
		return fmt.Errorf("amend_promote_entries: serialize: %w", err)
	}

	res := tx.WithContext(ctx).Model(&snapshotRow{}).
		Where("snapshot_id = ?", snapshotID).
		Updates(map[string]any{
			"before_state":        JSONRaw(amended),
			"affected_memory_ids": allMemoryIDs,
		})
	if res.Error != nil {
		return fmt.Errorf("amend_promote_entries: update snapshot %q: %w", snapshotID, res.Error)
	}
	return nil
}

// DeleteOlderThan deletes non-pinned snapshots older than the cutoff time.
// Returns the number of rows deleted. Used by the T049 snapshot auto-prune.
func (s *SnapshotStore) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := s.db.WithContext(ctx).
		Where("created_at < ? AND pinned = false", cutoff).
		Delete(&snapshotRow{})
	if res.Error != nil {
		return 0, fmt.Errorf("snapshot_store delete_older_than: %w", res.Error)
	}
	return res.RowsAffected, nil
}
