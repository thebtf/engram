// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// CitationRecord represents a single row in the citation_log table.
// The table is created by migration 107.
type CitationRecord struct {
	ID        int64     `gorm:"primaryKey;autoIncrement"`
	SessionID string    `gorm:"column:session_id;type:text;not null;index:idx_citation_log_session"`
	MemoryID  int64     `gorm:"column:memory_id;not null;index:idx_citation_log_memory"`
	Cited     bool      `gorm:"column:cited;not null"`
	MatchType string    `gorm:"column:match_type;type:text"`
	CreatedAt time.Time `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
}

// TableName returns the database table name for CitationRecord.
func (CitationRecord) TableName() string { return "citation_log" }

// CitationLogStore handles append-only recording of memory citation events.
type CitationLogStore struct {
	db *gorm.DB
}

// NewCitationLogStore creates a new CitationLogStore backed by the given Store.
func NewCitationLogStore(store *Store) *CitationLogStore {
	return &CitationLogStore{db: store.DB}
}

// RecordBatch inserts a batch of CitationRecord rows in a single statement.
// records must not be empty.
func (s *CitationLogStore) RecordBatch(ctx context.Context, records []CitationRecord) error {
	if len(records) == 0 {
		return fmt.Errorf("citation_log: records must not be empty")
	}
	if err := s.db.WithContext(ctx).Create(&records).Error; err != nil {
		return fmt.Errorf("citation_log: record batch (n=%d): %w", len(records), err)
	}
	return nil
}

// GetBySession returns all CitationRecord rows for the given sessionID,
// ordered by created_at ASC. Returns an empty (non-nil) slice when none exist.
func (s *CitationLogStore) GetBySession(ctx context.Context, sessionID string) ([]CitationRecord, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("citation_log: sessionID must not be empty")
	}
	var rows []CitationRecord
	err := s.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("created_at ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("citation_log: get by session=%q: %w", sessionID, err)
	}
	if rows == nil {
		rows = []CitationRecord{}
	}
	return rows, nil
}

// GetByMemory returns all CitationRecord rows for the given memoryID,
// ordered by created_at ASC. Returns an empty (non-nil) slice when none exist.
func (s *CitationLogStore) GetByMemory(ctx context.Context, memoryID int64) ([]CitationRecord, error) {
	if memoryID == 0 {
		return nil, fmt.Errorf("citation_log: memoryID must be non-zero")
	}
	var rows []CitationRecord
	err := s.db.WithContext(ctx).
		Where("memory_id = ?", memoryID).
		Order("created_at ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("citation_log: get by memory_id=%d: %w", memoryID, err)
	}
	if rows == nil {
		rows = []CitationRecord{}
	}
	return rows, nil
}

// DeleteOlderThan removes all citation_log rows whose created_at timestamp
// is strictly before olderThan. Returns the number of rows deleted.
func (s *CitationLogStore) DeleteOlderThan(ctx context.Context, olderThan time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("created_at < ?", olderThan).
		Delete(&CitationRecord{})
	if result.Error != nil {
		return 0, fmt.Errorf("citation_log: delete older than %v: %w", olderThan, result.Error)
	}
	return result.RowsAffected, nil
}
