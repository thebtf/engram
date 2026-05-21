// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/lib/pq"
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
		Project:        mem.Project,
		Content:        mem.Content,
		Tags:           models.JSONStringArray(mem.Tags),
		SourceAgent:    mem.SourceAgent,
		EditedBy:       mem.EditedBy,
		Status:         "active",
		ImportanceBase: 0.5,
		TsAlpha:        1.0,
		TsBeta:         1.0,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if mem.Version > 0 {
		row.Version = mem.Version
	}
	if mem.Status != "" {
		row.Status = mem.Status
	}
	if mem.ImportanceBase > 0 {
		row.ImportanceBase = mem.ImportanceBase
	}
	if mem.SupersedesID != nil {
		row.SupersedesID = mem.SupersedesID
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
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).
		Where("project = ? AND status = 'active' AND deleted_at IS NULL", project).
		Where("valid_from IS NULL OR valid_from <= ?", now).
		Where("valid_until IS NULL OR valid_until >= ?", now).
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

// ListForInjection returns active memories for the given project ordered by
// importance_base DESC, created_at DESC — suitable for context injection.
// project must not be empty. limit defaults to 50 when <= 0.
func (s *MemoryStore) ListForInjection(ctx context.Context, project string, limit int) ([]*models.Memory, error) {
	if project == "" {
		return nil, fmt.Errorf("project must not be empty")
	}
	if limit <= 0 {
		limit = 50
	}
	lifecycleEnabled := os.Getenv("ENGRAM_LIFECYCLE_ENABLED") == "true"
	var rows []Memory
	now := time.Now().UTC()
	q := s.db.WithContext(ctx).
		Where("project = ? AND status = 'active' AND deleted_at IS NULL", project).
		Where("valid_from IS NULL OR valid_from <= ?", now).
		Where("valid_until IS NULL OR valid_until >= ?", now)

	if lifecycleEnabled {
		q = q.Where("tier != 'working'").
			Where("retrievability > ?", 0.3)
	}

	err := q.Order("importance_base DESC, created_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list memories for injection project=%q: %w", project, err)
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

// Supersede marks an existing memory as superseded and returns the memory's importance_base
// BEFORE the penalty was applied (for the caller to compute the new memory's importance).
//
// The old memory receives status='superseded' and importance_base *= 0.1.
// Returns an error when the memory is not found or is already superseded/deleted.
func (s *MemoryStore) Supersede(ctx context.Context, id int64) (oldImportance float64, err error) {
	if id == 0 {
		return 0, fmt.Errorf("memory id must be non-zero")
	}
	// Read current importance before update.
	var row Memory
	if err := s.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", id).First(&row).Error; err != nil {
		return 0, fmt.Errorf("supersede memory id=%d: %w", id, err)
	}
	oldImportance = row.ImportanceBase

	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&Memory{}).
		Where("id = ? AND deleted_at IS NULL AND status = 'active'", id).
		Updates(map[string]any{
			"status":          "superseded",
			"importance_base": row.ImportanceBase * 0.1,
			"updated_at":      now,
		})
	if result.Error != nil {
		return 0, fmt.Errorf("supersede memory id=%d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return 0, fmt.Errorf("supersede memory id=%d: not found or already superseded", id)
	}
	return oldImportance, nil
}

// UpdateLifecycleFields updates specific lifecycle fields on a memory without
// touching content, tags, or version. Used by feedback and injection pipelines.
func (s *MemoryStore) UpdateLifecycleFields(ctx context.Context, id int64, fields map[string]any) error {
	if id == 0 {
		return fmt.Errorf("memory id must be non-zero")
	}
	if fields == nil {
		return fmt.Errorf("fields must not be nil")
	}
	fields["updated_at"] = time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&Memory{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(fields)
	if result.Error != nil {
		return fmt.Errorf("update lifecycle fields memory id=%d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("update lifecycle fields: memory id=%d not found", id)
	}
	return nil
}

// IncrementInjectionCount atomically increments injection_count for a memory.
func (s *MemoryStore) IncrementInjectionCount(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Model(&Memory{}).
		Where("id = ? AND deleted_at IS NULL", id).
		UpdateColumn("injection_count", gorm.Expr("injection_count + 1")).Error
}

// BatchIncrementCited atomically increments ts_alpha, citation_count, and recalculates
// importance_base for the given memory IDs. All updates are applied in a single SQL statement.
func (s *MemoryStore) BatchIncrementCited(ctx context.Context, ids []int64) error {
	return s.db.WithContext(ctx).Exec(
		"UPDATE memories SET ts_alpha = ts_alpha + 1, citation_count = citation_count + 1, importance_base = LEAST(1.0, GREATEST(importance_base, importance_base * ln(2.0 + citation_count))), updated_at = now() WHERE id = ANY(?)",
		pq.Array(ids),
	).Error
}

// BatchIncrementUncited atomically increments ts_beta for the given memory IDs.
// All updates are applied in a single SQL statement.
func (s *MemoryStore) BatchIncrementUncited(ctx context.Context, ids []int64) error {
	return s.db.WithContext(ctx).Exec(
		"UPDATE memories SET ts_beta = ts_beta + 1, updated_at = now() WHERE id = ANY(?)",
		pq.Array(ids),
	).Error
}

// GetProjectCitationRate returns the aggregate citation rate for a project:
// sum(citation_count) / max(sum(injection_count), 1). Returns 0.5 when fewer
// than minSamples memories exist (insufficient data for an informed prior).
func (s *MemoryStore) GetProjectCitationRate(ctx context.Context, project string, minSamples int) (float64, error) {
	var result struct {
		TotalCitations  float64
		TotalInjections float64
		MemoryCount     int64
	}
	err := s.db.WithContext(ctx).Raw(`
		SELECT COALESCE(SUM(citation_count), 0) AS total_citations,
		       COALESCE(SUM(injection_count), 0) AS total_injections,
		       COUNT(*) AS memory_count
		FROM memories
		WHERE project = ? AND deleted_at IS NULL AND status != 'flagged'`,
		project,
	).Scan(&result).Error
	if err != nil {
		return 0.5, err
	}
	if result.MemoryCount < int64(minSamples) || result.TotalInjections < 1 {
		return 0.5, nil
	}
	rate := result.TotalCitations / result.TotalInjections
	if rate > 1.0 {
		rate = 1.0
	}
	return rate, nil
}

// BatchIncrementCitedN increments ts_alpha by a damped amount for the given memory IDs.
// The actual boost is: n / (1 + consecutive_citation_count * damping_factor).
// Also increments consecutive_citation_count for diminishing returns tracking.
func (s *MemoryStore) BatchIncrementCitedN(ctx context.Context, ids []int64, n float64) error {
	return s.db.WithContext(ctx).Exec(
		`UPDATE memories SET
			ts_alpha = ts_alpha + ? / (1.0 + consecutive_citation_count * 0.1),
			citation_count = citation_count + 1,
			consecutive_citation_count = consecutive_citation_count + 1,
			importance_base = LEAST(1.0, GREATEST(importance_base, importance_base * ln(2.0 + citation_count))),
			updated_at = now()
		WHERE id = ANY(?)`,
		n, pq.Array(ids),
	).Error
}

// BatchIncrementUncitedN increments ts_beta by n and resets the consecutive
// citation counter for the given memory IDs.
func (s *MemoryStore) BatchIncrementUncitedN(ctx context.Context, ids []int64, n float64) error {
	return s.db.WithContext(ctx).Exec(
		"UPDATE memories SET ts_beta = ts_beta + ?, consecutive_citation_count = 0, updated_at = now() WHERE id = ANY(?)",
		n, pq.Array(ids),
	).Error
}

// BatchIncrementViolated applies a strong ts_beta penalty for violated memories.
func (s *MemoryStore) BatchIncrementViolated(ctx context.Context, ids []int64, n float64) error {
	return s.db.WithContext(ctx).Exec(
		"UPDATE memories SET ts_beta = ts_beta + ?, updated_at = now() WHERE id = ANY(?)",
		n, pq.Array(ids),
	).Error
}

// memoryRowToModel converts an internal GORM Memory row to the pkg/models.Memory type.
func memoryRowToModel(row *Memory) *models.Memory {
	return &models.Memory{
		ID:              row.ID,
		Project:         row.Project,
		Content:         row.Content,
		Tags:            []string(row.Tags),
		SourceAgent:     row.SourceAgent,
		EditedBy:        row.EditedBy,
		Status:          row.Status,
		Tier:            row.Tier,
		EpistemicType:   row.EpistemicType,
		Defeasibility:   row.Defeasibility,
		PromotionTarget: row.PromotionTarget,
		Version:         row.Version,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
		DeletedAt:       row.DeletedAt,
		LastRetrievedAt: row.LastRetrievedAt,
		LastConfirmed:   row.LastConfirmed,
		ReviewAfter:     row.ReviewAfter,
		ValidFrom:       row.ValidFrom,
		ValidUntil:      row.ValidUntil,
		SupersedesID:    row.SupersedesID,
		SupersededBy:    row.SupersededBy,
		ImportanceBase:  row.ImportanceBase,
		TsAlpha:         row.TsAlpha,
		TsBeta:          row.TsBeta,
		Confidence:      row.Confidence,
		Stability:       row.Stability,
		Retrievability:  row.Retrievability,
		CitationCount:   row.CitationCount,
		InjectionCount:  row.InjectionCount,
		AccessCount:     row.AccessCount,
		RecurrenceCount:          row.RecurrenceCount,
		ConsecutiveCitationCount: row.ConsecutiveCitationCount,
	}
}

// ListAllActive returns a batch of active memories for sleep cycle processing.
func (s *MemoryStore) ListAllActive(ctx context.Context, batchSize int, offset int) ([]*models.Memory, error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	var rows []Memory
	err := s.db.WithContext(ctx).
		Where("status = 'active' AND deleted_at IS NULL").
		Order("id ASC").
		Limit(batchSize).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list all active memories: %w", err)
	}
	result := make([]*models.Memory, len(rows))
	for i := range rows {
		result[i] = memoryRowToModel(&rows[i])
	}
	return result, nil
}
