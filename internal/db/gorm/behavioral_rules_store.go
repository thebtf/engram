// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

// BehavioralRulesStore provides behavioral-rule database operations using GORM.
// It targets the dedicated behavioral_rules table created by migration 089.
//
// Global rules (project IS NULL) are always included in List results regardless of the
// project filter — this ensures every session receives globally-applicable guidance.
//
// Immutability contract: Create and Update return NEW *models.BehavioralRule values
// populated from the database row. The caller's input struct is never mutated.
type BehavioralRulesStore struct {
	db *gorm.DB
}

// NewBehavioralRulesStore creates a new BehavioralRulesStore backed by the given Store.
func NewBehavioralRulesStore(store *Store) *BehavioralRulesStore {
	return &BehavioralRulesStore{db: store.DB}
}

// Create inserts a new behavioral rule. Returns a new *models.BehavioralRule populated with
// the database-assigned ID and timestamps. The caller's input is never mutated.
func (s *BehavioralRulesStore) Create(ctx context.Context, rule *models.BehavioralRule) (*models.BehavioralRule, error) {
	if rule == nil {
		return nil, fmt.Errorf("behavioral rule must not be nil")
	}
	if rule.Content == "" {
		return nil, fmt.Errorf("behavioral rule Content must not be empty")
	}

	now := time.Now().UTC()
	row := &BehavioralRule{
		Project:   rule.Project,
		Content:   rule.Content,
		Priority:  rule.Priority,
		EditedBy:  rule.EditedBy,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if rule.Version > 0 {
		row.Version = rule.Version
	}

	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("create behavioral rule: %w", err)
	}
	return behavioralRuleRowToModel(row), nil
}

// Get returns the active (non-soft-deleted) behavioral rule with the given ID.
// Returns a wrapped gorm.ErrRecordNotFound if no active row exists.
func (s *BehavioralRulesStore) Get(ctx context.Context, id int64) (*models.BehavioralRule, error) {
	if id == 0 {
		return nil, fmt.Errorf("id must be non-zero")
	}
	var row BehavioralRule
	err := s.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).
		First(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get behavioral rule id=%d: %w", id, err)
	}
	return behavioralRuleRowToModel(&row), nil
}

// List returns active behavioral rules.
//
// Scoping rules:
//   - project == nil  → returns only global rules (WHERE project IS NULL AND deleted_at IS NULL)
//   - project != nil  → returns project-scoped AND global rules
//     (WHERE (project = ? OR project IS NULL) AND deleted_at IS NULL)
//
// Results are ordered by priority DESC, created_at DESC.
// limit must be > 0; if ≤ 0 it is clamped to 50.
func (s *BehavioralRulesStore) List(ctx context.Context, project *string, limit int) ([]*models.BehavioralRule, error) {
	if limit <= 0 {
		limit = 50
	}

	q := s.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Order("priority DESC, created_at DESC").
		Limit(limit)

	if project == nil {
		q = q.Where("project IS NULL")
	} else {
		q = q.Where("project = ? OR project IS NULL", *project)
	}

	var rows []BehavioralRule
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list behavioral rules: %w", err)
	}
	result := make([]*models.BehavioralRule, len(rows))
	for i := range rows {
		result[i] = behavioralRuleRowToModel(&rows[i])
	}
	return result, nil
}

// Update updates an existing behavioral rule by ID.
// Bumps version and sets updated_at. Returns a NEW populated model.
// The caller's input struct is never mutated.
func (s *BehavioralRulesStore) Update(ctx context.Context, rule *models.BehavioralRule) (*models.BehavioralRule, error) {
	if rule == nil {
		return nil, fmt.Errorf("behavioral rule must not be nil")
	}
	if rule.ID == 0 {
		return nil, fmt.Errorf("behavioral rule ID must be set for Update")
	}
	if rule.Content == "" {
		return nil, fmt.Errorf("behavioral rule Content must not be empty")
	}

	now := time.Now().UTC()
	updates := map[string]any{
		"content":   rule.Content,
		"priority":  rule.Priority,
		"edited_by": rule.EditedBy,
		"updated_at": now,
		"version":   gorm.Expr("version + 1"),
	}
	// project is intentionally excluded from partial updates — changing a rule's scope
	// (global → project-scoped or vice versa) is a design-time concern, not a runtime one.

	result := s.db.WithContext(ctx).
		Model(&BehavioralRule{}).
		Where("id = ? AND deleted_at IS NULL", rule.ID).
		Updates(updates)
	if result.Error != nil {
		return nil, fmt.Errorf("update behavioral rule id=%d: %w", rule.ID, result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("update behavioral rule id=%d: %w", rule.ID, gorm.ErrRecordNotFound)
	}

	return s.Get(ctx, rule.ID)
}

// Delete soft-deletes the behavioral rule by setting deleted_at = NOW().
// Returns gorm.ErrRecordNotFound if no active row exists.
func (s *BehavioralRulesStore) Delete(ctx context.Context, id int64) error {
	if id == 0 {
		return fmt.Errorf("behavioral rule id must be non-zero")
	}
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).
		Model(&BehavioralRule{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(map[string]any{
			"deleted_at": now,
			"updated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("delete behavioral rule id=%d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("delete behavioral rule id=%d: %w", id, gorm.ErrRecordNotFound)
	}
	return nil
}

// behavioralRuleRowToModel converts a GORM BehavioralRule row to the pkg/models.BehavioralRule type.
func behavioralRuleRowToModel(row *BehavioralRule) *models.BehavioralRule {
	return &models.BehavioralRule{
		ID:        row.ID,
		Project:   row.Project,
		Content:   row.Content,
		Priority:  row.Priority,
		EditedBy:  row.EditedBy,
		Version:   row.Version,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
		DeletedAt: row.DeletedAt,
	}
}
