package gorm

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// SessionSegment represents a topically coherent portion of a session.
type SessionSegment struct {
	ID           int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID    string     `gorm:"column:session_id;type:text;not null;index:idx_session_segments_session" json:"session_id"`
	Project      string     `gorm:"column:project;type:text;not null" json:"project"`
	SegmentIndex int        `gorm:"column:segment_index;not null;default:0" json:"segment_index"`
	TopicHint    string     `gorm:"column:topic_hint;type:text;not null;default:''" json:"topic_hint"`
	StartedAt    time.Time  `gorm:"column:started_at;type:timestamptz;not null;default:now()" json:"started_at"`
	EndedAt      *time.Time `gorm:"column:ended_at;type:timestamptz" json:"ended_at,omitempty"`
}

func (SessionSegment) TableName() string { return "session_segments" }

// SegmentStore handles CRUD for session_segments.
type SegmentStore struct {
	db *gorm.DB
}

// NewSegmentStore creates a new SegmentStore.
func NewSegmentStore(store *Store) *SegmentStore {
	return &SegmentStore{db: store.DB}
}

// GetCurrentSegment returns the latest (open) segment for a session.
func (s *SegmentStore) GetCurrentSegment(ctx context.Context, sessionID string) (*SessionSegment, error) {
	var seg SessionSegment
	err := s.db.WithContext(ctx).
		Where("session_id = ? AND ended_at IS NULL", sessionID).
		Order("segment_index DESC").
		First(&seg).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("segment_store: get current: %w", err)
	}
	return &seg, nil
}

// CreateSegment creates a new segment, closing the previous one if it exists.
func (s *SegmentStore) CreateSegment(ctx context.Context, sessionID, project, topicHint string) (*SessionSegment, error) {
	now := time.Now().UTC()
	var result *SessionSegment

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Close the previous segment.
		if err := tx.Model(&SessionSegment{}).
			Where("session_id = ? AND ended_at IS NULL", sessionID).
			Update("ended_at", now).Error; err != nil {
			return fmt.Errorf("close previous segment: %w", err)
		}

		// Get next index.
		var maxIndex int
		if err := tx.Model(&SessionSegment{}).
			Where("session_id = ?", sessionID).
			Select("COALESCE(MAX(segment_index), -1)").
			Scan(&maxIndex).Error; err != nil {
			return fmt.Errorf("get max index: %w", err)
		}

		seg := SessionSegment{
			SessionID:    sessionID,
			Project:      project,
			SegmentIndex: maxIndex + 1,
			TopicHint:    topicHint,
			StartedAt:    now,
		}
		if err := tx.Create(&seg).Error; err != nil {
			return err
		}
		result = &seg
		return nil
	})
	return result, err
}

// GetSegments returns all segments for a session, ordered by index.
func (s *SegmentStore) GetSegments(ctx context.Context, sessionID string) ([]SessionSegment, error) {
	var segments []SessionSegment
	err := s.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("segment_index ASC").
		Find(&segments).Error
	return segments, err
}

// CloseAllSegments closes any open segments for a session.
func (s *SegmentStore) CloseAllSegments(ctx context.Context, sessionID string) error {
	return s.db.WithContext(ctx).
		Model(&SessionSegment{}).
		Where("session_id = ? AND ended_at IS NULL", sessionID).
		Update("ended_at", time.Now().UTC()).Error
}
