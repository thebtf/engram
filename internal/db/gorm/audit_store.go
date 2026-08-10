package gorm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// authSetupCompletedAuditAction permanently proves a surviving admin completed setup.
const authSetupCompletedAuditAction = "auth_setup_completed"

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

// LogTx records an audit event in the caller's transaction.
func (s *AuditStore) LogTx(ctx context.Context, tx *gorm.DB, entry AuditLogEntry) error {
	if err := tx.WithContext(ctx).Create(&entry).Error; err != nil {
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
//
// memoryID == 0 means "no specific memory" — a project-level event (e.g.
// write_lint_signaled, write_lint_aborted, candidate_pending_created all call
// LogAudit(ctx, 0, ...)). It is stored as NULL, NOT 0: migration 136 adds an FK
// audit_log.memory_id → memories(id), and there is no memories.id = 0, so a
// literal 0 would violate the constraint and the insert would fail (callers
// swallow the error, silently dropping the audit event). nil pointer → SQL NULL,
// which the ON DELETE SET NULL FK accepts. This mirrors the project-level
// convention already used by PurgeProject (MemoryID: nil).
func (s *AuditStore) LogAudit(ctx context.Context, memoryID int64, action, actor string) error {
	entry := AuditLogEntry{
		Action: action,
		Actor:  actor,
	}
	if memoryID != 0 {
		entry.MemoryID = &memoryID
	}
	return s.Log(ctx, entry)
}

// DeleteOlderThan removes ordinary audit entries older than the cutoff.
// The initial-admin setup proof is retained because Authentik provisioning
// relies on it to distinguish a completed setup from an uninitialized system.
func (s *AuditStore) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("created_at < ? AND action <> ?", cutoff, authSetupCompletedAuditAction).
		Delete(&AuditLogEntry{})
	return result.RowsAffected, result.Error
}
