package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func behavioralRulesEnabledMigration151() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "151_behavioral_rules_enabled",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`ALTER TABLE behavioral_rules ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT true`).Error; err != nil {
				return fmt.Errorf("migration 151 add behavioral_rules.enabled: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE behavioral_rules DROP COLUMN IF EXISTS enabled`).Error
		},
	}
}
