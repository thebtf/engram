package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func memoryPrincipalConstraintsMigration188() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "188_memory_principal_constraints",
		Migrate: func(tx *gorm.DB) error {
			// gormigrate defaults to no transaction; either both required CHECKs validate or neither is added.
			if err := tx.Transaction(memoryPrincipalsMigration149().Migrate); err != nil {
				return fmt.Errorf("migration 188: %w", err)
			}
			return nil
		},
		Rollback: func(*gorm.DB) error {
			return fmt.Errorf("migration 188 rollback refused: principal checks are required by migration 149")
		},
	}
}
