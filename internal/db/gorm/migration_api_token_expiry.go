package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func apiTokenExpiryMigration167() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "167_api_tokens_expires_at",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ`).Error; err != nil {
				return fmt.Errorf("migration 167: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(`ALTER TABLE api_tokens DROP COLUMN IF EXISTS expires_at`).Error; err != nil {
				return fmt.Errorf("migration 167 rollback: %w", err)
			}
			return nil
		},
	}
}
