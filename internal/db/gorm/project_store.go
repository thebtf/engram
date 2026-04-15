// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UpsertProject registers or updates a project identity record.
//
// newID is the canonical git-remote-based project ID.
// legacyID is the old path-based ID (may be empty on first git-based registration).
// gitRemote and relativePath are the git metadata used to derive newID.
// displayName is the human-readable project name (typically the directory name).
//
// When legacyID is non-empty, this function:
//  1. Upserts the project row (idempotent by primary key).
//  2. Appends legacyID to legacy_ids if not already present.
//  3. Launches a background goroutine to re-associate observations from legacyID to newID.
func UpsertProject(ctx context.Context, db *gorm.DB, newID, legacyID, gitRemote, relativePath, displayName string) error {
	if newID == "" {
		return fmt.Errorf("project newID must not be empty")
	}

	proj := Project{
		ID:           newID,
		GitRemote:    sql.NullString{String: gitRemote, Valid: gitRemote != ""},
		RelativePath: sql.NullString{String: relativePath, Valid: relativePath != ""},
		DisplayName:  sql.NullString{String: displayName, Valid: displayName != ""},
	}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&proj).Error; err != nil {
		return fmt.Errorf("upsert project %s: %w", newID, err)
	}

	if legacyID != "" {
		// Append legacyID only if not already present in the array.
		appendSQL := `UPDATE projects
		              SET legacy_ids = array_append(legacy_ids, ?)
		              WHERE id = ?
		                AND NOT (COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[])`
		if err := db.WithContext(ctx).Exec(appendSQL, legacyID, newID, legacyID).Error; err != nil {
			log.Warn().Err(err).Str("project", newID).Str("legacy_id", legacyID).Msg("failed to append legacy_id to project")
		}

		// Re-associate observations in the background with a timeout. Idempotent and non-blocking.
		go func(oldID, canonicalID string) {
			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := reassociateObservations(bgCtx, db, oldID, canonicalID); err != nil {
				log.Warn().Err(err).Str("from", oldID).Str("to", canonicalID).Msg("observation re-association failed")
			}
		}(legacyID, newID)
	}

	return nil
}

// reassociateObservations migrates observations from an old project ID to the canonical one.
// Safe to call multiple times — idempotent via WHERE project = oldID.
func reassociateObservations(ctx context.Context, db *gorm.DB, oldID, newID string) error {
	res := db.WithContext(ctx).Exec("UPDATE observations SET project = ? WHERE project = ?", newID, oldID)
	if res.Error != nil {
		return fmt.Errorf("reassociate observations %s -> %s: %w", oldID, newID, res.Error)
	}
	if res.RowsAffected > 0 {
		log.Info().
			Str("from", oldID).
			Str("to", newID).
			Int64("count", res.RowsAffected).
			Msg("re-associated observations to canonical project ID")
	}
	return nil
}

// ResolveProjectID checks if projectID is a legacy alias in the projects table.
// Returns the canonical project ID when a matching alias is found,
// otherwise returns the input projectID unchanged.
func ResolveProjectID(ctx context.Context, db *gorm.DB, projectID string) string {
	if projectID == "" {
		return projectID
	}
	var canonicalID string
	if err := db.WithContext(ctx).
		Raw(`SELECT id FROM projects WHERE removed_at IS NULL AND COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[] LIMIT 1`, projectID).
		Scan(&canonicalID).Error; err != nil || canonicalID == "" {
		return projectID
	}
	return canonicalID
}
