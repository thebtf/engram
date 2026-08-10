package gorm

import (
	"fmt"
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func issueCreatorKeycardMigration160() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "160_issue_creator_keycard",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range []string{
				`ALTER TABLE issues ADD COLUMN IF NOT EXISTS creator_keycard_id TEXT NOT NULL DEFAULT ''`,
				`CREATE INDEX IF NOT EXISTS idx_issues_creator_keycard ON issues (creator_keycard_id) WHERE creator_keycard_id <> ''`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 160: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(`DROP INDEX IF EXISTS idx_issues_creator_keycard`).Error; err != nil {
				return fmt.Errorf("migration 160 rollback: %w", err)
			}
			if err := tx.Exec(`ALTER TABLE issues DROP COLUMN IF EXISTS creator_keycard_id`).Error; err != nil {
				return fmt.Errorf("migration 160 rollback: %w", err)
			}
			return nil
		},
	}
}
