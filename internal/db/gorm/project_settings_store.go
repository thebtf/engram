// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProjectSettings holds per-project adaptive threshold configuration.
type ProjectSettings struct {
	Project            string    `gorm:"column:project;primaryKey"`
	RelevanceThreshold float64   `gorm:"column:relevance_threshold;default:0.3"`
	FeedbackCount      int       `gorm:"column:feedback_count;default:0"`
	UpdatedAt          time.Time `gorm:"column:updated_at;not null"`
}

// TableName returns the table name for GORM.
func (ProjectSettings) TableName() string {
	return "project_settings"
}

// ProjectSettingsStore provides access to per-project settings.
type ProjectSettingsStore struct {
	db *gorm.DB
}

// NewProjectSettingsStore creates a new ProjectSettingsStore.
func NewProjectSettingsStore(db *gorm.DB) *ProjectSettingsStore {
	return &ProjectSettingsStore{db: db}
}

// GetThreshold returns the relevance threshold for a project.
// If no entry exists, returns the default threshold of 0.3.
func (s *ProjectSettingsStore) GetThreshold(ctx context.Context, project string) (float64, error) {
	var ps ProjectSettings
	err := s.db.WithContext(ctx).
		Where("project = ?", project).
		First(&ps).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return 0.3, nil
		}
		return 0, err
	}
	return ps.RelevanceThreshold, nil
}

// AdjustThreshold atomically adjusts the relevance threshold for a project by delta.
// The threshold is clamped to the range [0.1, 0.8].
// Also increments feedback_count.
// Uses UPSERT (INSERT ON CONFLICT UPDATE) to auto-create the entry if it does not exist.
func (s *ProjectSettingsStore) AdjustThreshold(ctx context.Context, project string, delta float64) error {
	// Use raw SQL for atomic UPSERT with clamped arithmetic.
	sql := `INSERT INTO project_settings (project, relevance_threshold, feedback_count, updated_at)
	        VALUES (?, 0.3, 0, NOW())
	        ON CONFLICT (project) DO UPDATE
	        SET relevance_threshold = GREATEST(0.1, LEAST(0.8, project_settings.relevance_threshold + ?)),
	            feedback_count      = project_settings.feedback_count + 1,
	            updated_at          = NOW()`
	return s.db.WithContext(ctx).Exec(sql, project, delta).Error
}

// UpsertSettings creates or replaces settings for a project.
// Exported for testing; normal callers use GetThreshold and AdjustThreshold.
func (s *ProjectSettingsStore) UpsertSettings(ctx context.Context, ps *ProjectSettings) error {
	ps.UpdatedAt = time.Now()
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "project"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"relevance_threshold",
				"feedback_count",
				"updated_at",
			}),
		}).
		Create(ps).Error
}
