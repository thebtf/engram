// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// ReasoningTraceStore provides reasoning trace database operations using GORM.
type ReasoningTraceStore struct {
	db *gorm.DB
}

// NewReasoningTraceStore creates a new reasoning trace store.
func NewReasoningTraceStore(store *Store) *ReasoningTraceStore {
	return &ReasoningTraceStore{
		db: store.DB,
	}
}

// Create stores a new reasoning trace and returns its ID.
func (s *ReasoningTraceStore) Create(ctx context.Context, trace *ReasoningTrace) (int64, error) {
	if trace.CreatedAtEpoch == 0 {
		trace.CreatedAtEpoch = time.Now().UnixMilli()
	}
	if err := s.db.WithContext(ctx).Create(trace).Error; err != nil {
		return 0, err
	}
	return trace.ID, nil
}

// GetBySession retrieves reasoning traces for a given session, ordered by recency.
func (s *ReasoningTraceStore) GetBySession(ctx context.Context, sessionID string, limit int) ([]ReasoningTrace, error) {
	if limit <= 0 {
		limit = 10
	}
	var traces []ReasoningTrace
	err := s.db.WithContext(ctx).
		Where("sdk_session_id = ?", sessionID).
		Order("created_at_epoch DESC").
		Limit(limit).
		Find(&traces).Error
	return traces, err
}

// SearchByProject retrieves high-quality reasoning traces for a project.
func (s *ReasoningTraceStore) SearchByProject(ctx context.Context, project string, limit int) ([]ReasoningTrace, error) {
	if limit <= 0 {
		limit = 5
	}
	var traces []ReasoningTrace
	err := s.db.WithContext(ctx).
		Where("project = ? AND quality_score >= 0.5", project).
		Order("quality_score DESC, created_at_epoch DESC").
		Limit(limit).
		Find(&traces).Error
	return traces, err
}

// GetDB returns the underlying GORM DB for advanced queries.
func (s *ReasoningTraceStore) GetDB() *gorm.DB {
	return s.db
}
