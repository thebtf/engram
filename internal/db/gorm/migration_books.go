// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// booksJobsMigration155 creates the book-ingestion job table (US13, CR-002
// Task Group 5). The table tracks the lifecycle of an uploaded book/document
// source as it is processed into VersionedDocumentStore entries by the
// books.Pipeline (T019). This migration is additive-only: it creates a new
// table and indexes, and never alters or drops existing schema.
func booksJobsMigration155() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "155_books_jobs",
		Migrate: func(tx *gorm.DB) error {
			sqls := []string{
				`CREATE TABLE IF NOT EXISTS books_jobs (
					id          BIGSERIAL PRIMARY KEY,
					status      TEXT NOT NULL DEFAULT 'pending',
					source_ref  TEXT NOT NULL,
					error       TEXT,
					created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
					updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
					CONSTRAINT books_jobs_status_chk
						CHECK (status IN ('pending','processing','done','failed')),
					CONSTRAINT books_jobs_source_ref_not_blank
						CHECK (btrim(source_ref) <> '')
				)`,
				`CREATE INDEX IF NOT EXISTS idx_books_jobs_status_created
					ON books_jobs (status, created_at DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_books_jobs_source_ref
					ON books_jobs (source_ref)`,
			}
			for _, stmt := range sqls {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 155: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS books_jobs`).Error
		},
	}
}
