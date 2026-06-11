package gorm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// AuditLogEntry represents a single audit trail record.
type AuditLogEntry struct {
	ID              int64            `gorm:"primaryKey;autoIncrement" json:"id"`
	MemoryID        *int64           `gorm:"type:bigint" json:"memory_id,omitempty"`
	Action          string           `gorm:"type:text;not null" json:"action"`
	Actor           string           `gorm:"type:text;not null;default:'system'" json:"actor"`
	SourceSessionID string           `gorm:"type:text;not null;default:''" json:"source_session_id"`
	BeforeState     *json.RawMessage `gorm:"type:jsonb" json:"before_state,omitempty"`
	AfterState      *json.RawMessage `gorm:"type:jsonb" json:"after_state,omitempty"`
	Reason          string           `gorm:"type:text;not null;default:''" json:"reason"`
	CreatedAt       time.Time        `gorm:"type:timestamptz;not null;default:now()" json:"created_at"`
}

func (AuditLogEntry) TableName() string { return "audit_log" }

// AuditStore handles audit_log CRUD operations.
type AuditStore struct {
	db *gorm.DB
}

// NewAuditStore creates a new AuditStore.
func NewAuditStore(db *gorm.DB) *AuditStore {
	return &AuditStore{db: db}
}

// Log records an audit event.
func (s *AuditStore) Log(ctx context.Context, entry AuditLogEntry) error {
	if err := s.db.WithContext(ctx).Create(&entry).Error; err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	return nil
}

// GetByMemory returns audit entries for a memory, newest first.
func (s *AuditStore) GetByMemory(ctx context.Context, memoryID int64, limit int) ([]AuditLogEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	var entries []AuditLogEntry
	err := s.db.WithContext(ctx).
		Where("memory_id = ?", memoryID).
		Order("created_at DESC").
		Limit(limit).
		Find(&entries).Error
	if err != nil {
		return nil, fmt.Errorf("get audit by memory: %w", err)
	}
	return entries, nil
}

// LogAudit is a convenience method that satisfies the lifecycle.AuditLogger interface.
// It records a minimal audit entry with only action, actor, and optional memory_id.
func (s *AuditStore) LogAudit(ctx context.Context, memoryID int64, action, actor string) error {
	entry := AuditLogEntry{
		MemoryID: &memoryID,
		Action:   action,
		Actor:    actor,
	}
	return s.Log(ctx, entry)
}

// DeleteOlderThan removes audit entries older than the cutoff.
func (s *AuditStore) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("created_at < ?", cutoff).
		Delete(&AuditLogEntry{})
	return result.RowsAffected, result.Error
}
