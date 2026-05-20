package gorm

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// PromotionLogEntry represents a tier change audit record.
type PromotionLogEntry struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	MemoryID  int64     `gorm:"not null" json:"memory_id"`
	FromTier  string    `gorm:"type:text;not null" json:"from_tier"`
	ToTier    string    `gorm:"type:text;not null" json:"to_tier"`
	Reason    string    `gorm:"type:text;not null;default:''" json:"reason"`
	CreatedAt time.Time `gorm:"type:timestamptz;not null;default:now()" json:"created_at"`
}

func (PromotionLogEntry) TableName() string { return "promotion_log" }

// PromotionStore handles promotion_log CRUD operations.
type PromotionStore struct {
	db *gorm.DB
}

// NewPromotionStore creates a new PromotionStore.
func NewPromotionStore(db *gorm.DB) *PromotionStore {
	return &PromotionStore{db: db}
}

// LogPromotion records a tier change event.
func (s *PromotionStore) LogPromotion(ctx context.Context, memoryID int64, fromTier, toTier, reason string) error {
	entry := PromotionLogEntry{
		MemoryID: memoryID,
		FromTier: fromTier,
		ToTier:   toTier,
		Reason:   reason,
	}
	if err := s.db.WithContext(ctx).Create(&entry).Error; err != nil {
		return fmt.Errorf("log promotion: %w", err)
	}
	return nil
}

// GetHistory returns promotion log entries for a memory, newest first.
func (s *PromotionStore) GetHistory(ctx context.Context, memoryID int64, limit int) ([]PromotionLogEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	var entries []PromotionLogEntry
	err := s.db.WithContext(ctx).
		Where("memory_id = ?", memoryID).
		Order("created_at DESC").
		Limit(limit).
		Find(&entries).Error
	if err != nil {
		return nil, fmt.Errorf("get promotion history: %w", err)
	}
	return entries, nil
}
