package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	gormlib "gorm.io/gorm"
)

// workspaceCatalogMigration182 adds optional owner-managed display metadata.
// Existing checkouts remain unnamed until their exact owner supplies a safe label.
func workspaceCatalogMigration182() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "182_workspace_catalog_display_metadata",
		Migrate: func(tx *gormlib.DB) error {
			for _, stmt := range []string{
				`ALTER TABLE ci_checkouts ADD COLUMN IF NOT EXISTS display_name TEXT`,
				`DO $$
				BEGIN
					IF NOT EXISTS (
						SELECT 1 FROM pg_constraint
						WHERE conname = 'ci_checkouts_display_name_chk'
						  AND conrelid = 'ci_checkouts'::regclass
					) THEN
						ALTER TABLE ci_checkouts
							ADD CONSTRAINT ci_checkouts_display_name_chk CHECK (
								display_name IS NULL OR (
									btrim(display_name) <> ''
									AND display_name = btrim(display_name)
									AND display_name !~ '[[:cntrl:]]'
								)
							);
					END IF;
				END $$`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 182: %w", err)
				}
			}
			return nil
		},
		Rollback: func(_ *gormlib.DB) error {
			// Historical owner display choices remain readable after binary rollback.
			return nil
		},
	}
}
