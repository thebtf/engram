// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

// MemoryStore provides memory-related database operations using GORM.
// It targets the dedicated memories table created by migration 088.
//
// Immutability contract: Create and Update return NEW *models.Memory values populated
// from the database row. The caller's input struct is never mutated.
type MemoryStore struct {
	db *gorm.DB
}

// NewMemoryStore creates a new MemoryStore backed by the given Store.
func NewMemoryStore(store *Store) *MemoryStore {
	return &MemoryStore{db: store.DB}
}

// Create inserts a new memory row. Returns a new *models.Memory populated with the
// database-assigned ID and timestamps. The caller's input is never mutated.
func (s *MemoryStore) Create(ctx context.Context, mem *models.Memory) (*models.Memory, error) {
	if mem == nil {
		return nil, fmt.Errorf("memory must not be nil")
	}
	if mem.Project == "" {
		return nil, fmt.Errorf("memory.Project must not be empty")
	}
	if mem.Content == "" {
		return nil, fmt.Errorf("memory.Content must not be empty")
	}

	now := time.Now().UTC()
	row := &Memory{
		Project:     mem.Project,
		Content:     mem.Content,
		Tags:        models.JSONStringArray(mem.Tags),
		SourceAgent: mem.SourceAgent,
		EditedBy:    mem.EditedBy,
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if mem.Version > 0 {
		row.Version = mem.Version
	}

	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("create memory for project %q: %w", mem.Project, err)
	}
	return memoryRowToModel(row), nil
}

// Get returns the active (non-soft-deleted) memory with the given ID.
// Returns a wrapped gorm.ErrRecordNotFound if no active row exists.
func (s *MemoryStore) Get(ctx context.Context, id int64) (*models.Memory, error) {
	if id == 0 {
		return nil, fmt.Errorf("id must be non-zero")
	}
	var row Memory
	err := s.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).
		First(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get memory id=%d: %w", id, err)
	}
	return memoryRowToModel(&row), nil
}

// List returns active (non-soft-deleted) memories for the given project,
// ordered by created_at DESC, limited to limit rows.
// project must not be empty.
func (s *MemoryStore) List(ctx context.Context, project string, limit int) ([]*models.Memory, error) {
	if project == "" {
		return nil, fmt.Errorf("project: must not be empty")
	}
	if limit <= 0 {
		limit = 50
	}

	var rows []Memory
	err := s.db.WithContext(ctx).
		Where("project = ? AND deleted_at IS NULL", project).
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list memories for project %q: %w", project, err)
	}
	result := make([]*models.Memory, len(rows))
	for i := range rows {
		result[i] = memoryRowToModel(&rows[i])
	}
	return result, nil
}

// Update updates an existing memory row by ID.
// Bumps version and sets updated_at. Returns a NEW populated model.
// The caller's input struct is never mutated.
func (s *MemoryStore) Update(ctx context.Context, mem *models.Memory) (*models.Memory, error) {
	if mem == nil {
		return nil, fmt.Errorf("memory must not be nil")
	}
	if mem.ID == 0 {
		return nil, fmt.Errorf("memory.ID must be set for Update")
	}
	if mem.Content == "" {
		return nil, fmt.Errorf("memory.Content must not be empty")
	}

	now := time.Now().UTC()

	// Perform the update using a map to avoid GORM zero-value omission issues.
	updates := map[string]any{
		"content":      mem.Content,
		"tags":         models.JSONStringArray(mem.Tags),
		"source_agent": mem.SourceAgent,
		"edited_by":    mem.EditedBy,
		"updated_at":   now,
		"version":      gorm.Expr("version + 1"),
	}

	result := s.db.WithContext(ctx).
		Model(&Memory{}).
		Where("id = ? AND deleted_at IS NULL", mem.ID).
		Updates(updates)
	if result.Error != nil {
		return nil, fmt.Errorf("update memory id=%d: %w", mem.ID, result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("update memory id=%d: %w", mem.ID, gorm.ErrRecordNotFound)
	}

	// Re-fetch to return the fully-populated model.
	return s.Get(ctx, mem.ID)
}

// Delete soft-deletes the memory by setting deleted_at = NOW().
// Returns gorm.ErrRecordNotFound if no active row exists.
func (s *MemoryStore) Delete(ctx context.Context, id int64) error {
	if id == 0 {
		return fmt.Errorf("memory id must be non-zero")
	}
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).
		Model(&Memory{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(map[string]any{
			"deleted_at": now,
			"updated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("delete memory id=%d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("delete memory id=%d: %w", id, gorm.ErrRecordNotFound)
	}
	return nil
}

// memoryRowToModel converts an internal GORM Memory row to the pkg/models.Memory type.
func memoryRowToModel(row *Memory) *models.Memory {
	return &models.Memory{
		ID:          row.ID,
		Project:     row.Project,
		Content:     row.Content,
		Tags:        []string(row.Tags),
		SourceAgent: row.SourceAgent,
		EditedBy:    row.EditedBy,
		Version:     row.Version,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
		DeletedAt:   row.DeletedAt,
	}
}
