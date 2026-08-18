package gorm

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ContinuitySlotStore owns persistence of the one current continuity slot per
// project. Callers provide already-authorized, server-derived authority
// snapshots; this store does not make authorization decisions.
type ContinuitySlotStore struct {
	db *gorm.DB
}

func NewContinuitySlotStore(db *gorm.DB) *ContinuitySlotStore {
	return &ContinuitySlotStore{db: db}
}

// Get returns the current slot for project, or a wrapped gorm.ErrRecordNotFound.
func (s *ContinuitySlotStore) Get(ctx context.Context, project string) (*ContinuitySlot, error) {
	return s.get(ctx, s.db, project, false)
}

// GetTx returns the current slot while holding its row lock in the caller's transaction.
func (s *ContinuitySlotStore) GetTx(ctx context.Context, tx *gorm.DB, project string) (*ContinuitySlot, error) {
	return s.get(ctx, tx, project, true)
}

func (s *ContinuitySlotStore) get(ctx context.Context, db *gorm.DB, project string, forUpdate bool) (*ContinuitySlot, error) {
	if project == "" {
		return nil, fmt.Errorf("continuity slot get: project is required")
	}
	query := db.WithContext(ctx).Where("project = ?", project)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var slot ContinuitySlot
	if err := query.First(&slot).Error; err != nil {
		return nil, fmt.Errorf("continuity slot get %q: %w", project, err)
	}
	slot.ExpiresAt = slot.ExpiresAt.UTC()
	return &slot, nil
}

// Upsert atomically replaces the slot for its project.
func (s *ContinuitySlotStore) Upsert(ctx context.Context, slot ContinuitySlot) error {
	return s.UpsertTx(ctx, s.db, slot)
}

// UpsertTx atomically replaces the slot for its project in the caller's transaction.
func (s *ContinuitySlotStore) UpsertTx(ctx context.Context, tx *gorm.DB, slot ContinuitySlot) error {
	if slot.Project == "" || slot.MemoryID <= 0 || slot.ExpiresAt.IsZero() {
		return fmt.Errorf("continuity slot upsert: project, positive memory_id, and expires_at are required")
	}
	now := time.Now().UTC()
	slot.ExpiresAt, slot.UpdatedAt = slot.ExpiresAt.UTC(), now
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "project"}},
		DoUpdates: clause.Assignments(map[string]any{
			"memory_id": slot.MemoryID, "expires_at": slot.ExpiresAt,
			"authority_domain":               slot.AuthorityDomain,
			"authority_owner_principal":      slot.AuthorityOwnerPrincipal,
			"authority_owner_principal_kind": slot.AuthorityOwnerPrincipalKind,
			"updated_at":                     now,
		}),
	}).Create(&slot).Error; err != nil {
		return fmt.Errorf("continuity slot upsert %q: %w", slot.Project, err)
	}
	return nil
}

// ClearTx removes the slot for project in the caller's transaction and reports whether a row was removed.
func (s *ContinuitySlotStore) ClearTx(ctx context.Context, tx *gorm.DB, project string) (bool, error) {
	if project == "" {
		return false, fmt.Errorf("continuity slot clear: project is required")
	}
	result := tx.WithContext(ctx).Where("project = ?", project).Delete(&ContinuitySlot{})
	if result.Error != nil {
		return false, fmt.Errorf("continuity slot clear %q: %w", project, result.Error)
	}
	return result.RowsAffected > 0, nil
}

// Clear removes the slot for project and reports whether a row was removed.
func (s *ContinuitySlotStore) Clear(ctx context.Context, project string) (bool, error) {
	return s.ClearTx(ctx, s.db, project)
}

// ClearByMemory removes a slot targeting memoryID and reports whether it existed.
func (s *ContinuitySlotStore) ClearByMemory(ctx context.Context, memoryID int64) (bool, error) {
	if memoryID <= 0 {
		return false, fmt.Errorf("continuity slot clear_by_memory: memory_id must be positive")
	}
	result := s.db.WithContext(ctx).Where("memory_id = ?", memoryID).Delete(&ContinuitySlot{})
	if result.Error != nil {
		return false, fmt.Errorf("continuity slot clear_by_memory %d: %w", memoryID, result.Error)
	}
	return result.RowsAffected > 0, nil
}
