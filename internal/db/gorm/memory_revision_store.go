package gorm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

var ErrMemoryVersionConflict = errors.New("memory version conflict")

type MemoryRevision struct {
	MemoryID               int64     `json:"memory_id" gorm:"primaryKey"`
	Version                int       `json:"version" gorm:"primaryKey"`
	Content                string    `json:"content"`
	ChangedByPrincipal     string    `json:"changed_by_principal"`
	ChangedByPrincipalKind string    `json:"changed_by_principal_kind"`
	ChangedAt              time.Time `json:"changed_at"`
}

func (MemoryRevision) TableName() string { return "memory_revisions" }

// ReviseInContext retains the prior content and advances the single current
// memory body in one transaction. The caller reconciles a stale version by read.
func (s *MemoryStore) ReviseInContext(ctx context.Context, id int64, owning models.MemoryContext, project string, caller scope.KeycardContext, expectedVersion int, content string) (*models.Memory, error) {
	if expectedVersion <= 0 || content == "" {
		return nil, fmt.Errorf("version and content are required")
	}
	var result *models.Memory
	err := s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		// Match consolidation's relation order before locking the target memory.
		if err := tx.Exec(`LOCK TABLE "audit_log", "content_chunks", "memories" IN ROW EXCLUSIVE MODE`).Error; err != nil {
			return err
		}
		current, err := memoryInContext(ctx, tx, id, owning, project, caller, true)
		if err != nil {
			return err
		}
		if current.OwnerPrincipal != caller.Principal || current.OwnerPrincipalKind != caller.PrincipalKind || !scope.ResolveMemoryManage(caller, current) {
			return gormlib.ErrRecordNotFound
		}
		if current.Context == nil || current.Context.Kind == models.MemoryContextLegacy {
			return gormlib.ErrRecordNotFound
		}
		if current.Status != "active" {
			return gormlib.ErrRecordNotFound
		}
		if current.Version != expectedVersion {
			return ErrMemoryVersionConflict
		}
		previous := MemoryRevision{MemoryID: id, Version: current.Version, Content: current.Content, ChangedByPrincipal: caller.Principal, ChangedByPrincipalKind: caller.PrincipalKind, ChangedAt: time.Now().UTC()}
		if err := tx.Create(&previous).Error; err != nil {
			return err
		}
		updated := tx.Model(&Memory{}).Where("id = ? AND version = ?", id, expectedVersion).Updates(map[string]any{"content": content, "version": expectedVersion + 1, "edited_by": caller.Principal, "updated_at": time.Now().UTC()})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrMemoryVersionConflict
		}
		if err := tx.Exec("DELETE FROM content_chunks WHERE memory_id = ?", id).Error; err != nil {
			return err
		}
		if err := NewAuditStore(tx).Log(ctx, AuditLogEntry{MemoryID: &id, Action: "update", Actor: caller.Principal, SourceSessionID: caller.SessionID}); err != nil {
			return err
		}
		result, err = memoryInContext(ctx, tx, id, owning, project, caller, false)
		return err
	})
	return result, err
}

// HistoryInContext authorizes the current card before exposing prior bodies.
func (s *MemoryStore) HistoryInContext(ctx context.Context, id int64, owning models.MemoryContext, project string, caller scope.KeycardContext, afterVersion, limit int) ([]MemoryRevision, error) {
	if afterVersion < 0 || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid revision page")
	}
	var revisions []MemoryRevision
	err := s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		if _, err := memoryInContext(ctx, tx, id, owning, project, caller, true); err != nil {
			return err
		}
		return tx.Where("memory_id = ? AND version > ?", id, afterVersion).Order("version ASC").Limit(limit).Find(&revisions).Error
	})
	return revisions, err
}
