package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	gormlib "gorm.io/gorm"
)

// projectIdentityV3ComparisonClientInstancePrivacyMigration166 preserves
// historical comparison evidence while refusing locator-shaped client IDs on
// all future comparison writes.
func projectIdentityV3ComparisonClientInstancePrivacyMigration166() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "166_project_identity_v3_comparison_client_instance_privacy",
		Migrate: func(tx *gormlib.DB) error {
			if err := tx.Exec(`DO $$
				BEGIN
					IF NOT EXISTS (
						SELECT 1 FROM pg_constraint
						WHERE conrelid = 'project_identity_comparisons'::regclass
							AND conname = 'project_identity_comparisons_client_instance_privacy_chk'
					) THEN
						ALTER TABLE project_identity_comparisons
							ADD CONSTRAINT project_identity_comparisons_client_instance_privacy_chk CHECK (
								char_length(client_instance_id) BETWEEN 1 AND 256
								AND client_instance_id ~ '^[^[:space:]@/\\]+$'
								AND client_instance_id !~ '[[:cntrl:]]'
								AND client_instance_id !~ '^[A-Za-z][A-Za-z0-9+.-]*:'
							) NOT VALID;
					END IF;
				END
			$$`).Error; err != nil {
				return fmt.Errorf("migration 166: %w", err)
			}
			return nil
		},
		Rollback: func(tx *gormlib.DB) error {
			// Future migrations, not destructive rollback, preserve comparison evidence.
			return nil
		},
	}
}
