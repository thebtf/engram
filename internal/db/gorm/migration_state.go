package gorm

import (
	"context"
	"fmt"
)

const (
	migrationEngine = "gormigrate"
	migrationTable  = "migrations"
)

// MigrationState is the operator-visible snapshot of the actual migration
// substrate. Engram uses gormigrate, whose table stores applied IDs only; it has
// no dirty flag or applied_at timestamp like goose.
type MigrationState struct {
	Engine             string   `json:"engine"`
	Table              string   `json:"table"`
	CurrentVersion     string   `json:"current_version"`
	AppliedCount       int      `json:"applied_count"`
	AppliedIDs         []string `json:"applied_ids"`
	DirtySupported     bool     `json:"dirty_supported"`
	AppliedAtSupported bool     `json:"applied_at_supported"`
}

// GetMigrationState reads the gormigrate bookkeeping table. NewStore runs
// migrations before the worker serves requests, so a missing table is a real
// database-readiness problem rather than an empty clean state.
func (s *Store) GetMigrationState(ctx context.Context) (*MigrationState, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("store is not initialized")
	}

	var ids []string
	if err := s.DB.WithContext(ctx).
		Table(migrationTable).
		Select("id").
		Order("id ASC").
		Scan(&ids).Error; err != nil {
		return nil, fmt.Errorf("read migration state: %w", err)
	}

	current := ""
	if len(ids) > 0 {
		current = ids[len(ids)-1]
	}

	return &MigrationState{
		Engine:             migrationEngine,
		Table:              migrationTable,
		CurrentVersion:     current,
		AppliedCount:       len(ids),
		AppliedIDs:         ids,
		DirtySupported:     false,
		AppliedAtSupported: false,
	}, nil
}
