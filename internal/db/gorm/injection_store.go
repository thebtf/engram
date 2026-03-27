// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"time"

	"gorm.io/gorm"
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

// GetInjectionsBySession returns all injection records for a session.
func (s *InjectionStore) GetInjectionsBySession(ctx context.Context, sessionID string) ([]InjectionRecord, error) {
	var records []InjectionRecord
	err := s.db.WithContext(ctx).Table("observation_injections").
		Where("session_id = ?", sessionID).
		Find(&records).Error
	return records, err
}

// CleanupOldInjections removes records older than the given time.
func (s *InjectionStore) CleanupOldInjections(ctx context.Context, olderThan time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Table("observation_injections").
		Where("injected_at < ?", olderThan).
		Delete(nil)
	return result.RowsAffected, result.Error
}
