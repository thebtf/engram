// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/thebtf/engram/internal/learning"
)

// InjectionRecord represents a single observation injection event.
type InjectionRecord struct {
	ObservationID    int64  `gorm:"column:observation_id"`
	SessionID        string `gorm:"column:session_id"`
	InjectionSection string `gorm:"column:injection_section"`
}

// InjectionStore handles observation injection tracking.
type InjectionStore struct {
	db *gorm.DB
}

// NewInjectionStore creates a new InjectionStore.
func NewInjectionStore(db *gorm.DB) *InjectionStore {
	return &InjectionStore{db: db}
}

// RecordInjections batch-inserts injection records for a session.
func (s *InjectionStore) RecordInjections(ctx context.Context, records []InjectionRecord) error {
	if len(records) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Table("observation_injections").Create(&records).Error
}

// GetInjectionsBySession returns injection records for a session as learning.InjectionRecord values.
// Implements learning.InjectionSource so InjectionStore can be passed directly to the propagator.
func (s *InjectionStore) GetInjectionsBySession(ctx context.Context, sessionID string) ([]learning.InjectionRecord, error) {
	var rows []InjectionRecord
	err := s.db.WithContext(ctx).Table("observation_injections").
		Where("session_id = ?", sessionID).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]learning.InjectionRecord, len(rows))
	for i, r := range rows {
		result[i] = learning.InjectionRecord{
			ObservationID:    r.ObservationID,
			InjectionSection: r.InjectionSection,
		}
	}
	return result, nil
}

// CountInjectionsBySession returns the number of injection records for a session.
func (s *InjectionStore) CountInjectionsBySession(ctx context.Context, sessionID string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Table("observation_injections").
		Where("session_id = ?", sessionID).
		Count(&count).Error
	return count, err
}

// SessionInjectionDetail represents an observation injected into a session with its metadata.
type SessionInjectionDetail struct {
	ObservationID       int64   `json:"observation_id"`
	InjectionSection    string  `json:"injection_section"`
	Title               string  `json:"title"`
	Type                string  `json:"type"`
	EffectivenessScore  float64 `json:"effectiveness_score"`
	EffectivenessInj    int     `json:"effectiveness_injections"`
	EffectivenessSuc    int     `json:"effectiveness_successes"`
	ImportanceScore     float64 `json:"importance_score"`
	UtilityScore        float64 `json:"utility_score"`
}

// GetSessionInjectionDetails returns enriched injection records for a session,
// joining with observations to include title, type, and effectiveness metrics.
func (s *InjectionStore) GetSessionInjectionDetails(ctx context.Context, sessionID string) ([]SessionInjectionDetail, error) {
	var results []SessionInjectionDetail
	err := s.db.WithContext(ctx).Raw(`
		SELECT DISTINCT ON (oi.observation_id)
			oi.observation_id,
			oi.injection_section,
			COALESCE(o.title, '') AS title,
			COALESCE(o.type, '') AS type,
			COALESCE(o.effectiveness_score, 0) AS effectiveness_score,
			COALESCE(o.effectiveness_injections, 0) AS effectiveness_injections,
			COALESCE(o.effectiveness_successes, 0) AS effectiveness_successes,
			COALESCE(o.importance_score, 0) AS importance_score,
			COALESCE(o.utility_score, 0.5) AS utility_score
		FROM observation_injections oi
		LEFT JOIN observations o ON o.id = oi.observation_id
		WHERE oi.session_id = ?
		ORDER BY oi.observation_id, oi.injected_at DESC
	`, sessionID).Scan(&results).Error
	return results, err
}

// CleanupOldInjections removes records older than the given time.
func (s *InjectionStore) CleanupOldInjections(ctx context.Context, olderThan time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Table("observation_injections").
		Where("injected_at < ?", olderThan).
		Delete(nil)
	return result.RowsAffected, result.Error
}
