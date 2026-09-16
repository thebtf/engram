package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	gormlib "gorm.io/gorm"
)

// projectIdentityV3ResolutionAttemptAdminAuditMigration164 adds administrative
// audit fields after immutable migration 163 has already been applied.
func projectIdentityV3ResolutionAttemptAdminAuditMigration164() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "164_project_identity_v3_resolution_attempt_admin_audit",
		Migrate: func(tx *gormlib.DB) error {
			for _, stmt := range []string{
				`ALTER TABLE project_resolution_attempts
					ADD COLUMN IF NOT EXISTS admin_target_reference TEXT,
					ADD COLUMN IF NOT EXISTS admin_actor TEXT,
					ADD COLUMN IF NOT EXISTS admin_purpose TEXT,
					ADD COLUMN IF NOT EXISTS admin_decision TEXT,
					ADD COLUMN IF NOT EXISTS admin_retention_or_rollback TEXT`,
				`DO $$
				BEGIN
					IF NOT EXISTS (
						SELECT 1 FROM pg_constraint
						WHERE conname = 'project_resolution_attempts_admin_audit_chk'
						  AND conrelid = 'project_resolution_attempts'::regclass
					) THEN
						ALTER TABLE project_resolution_attempts
							ADD CONSTRAINT project_resolution_attempts_admin_audit_chk CHECK (
								(admin_target_reference IS NULL AND admin_actor IS NULL AND admin_purpose IS NULL AND admin_decision IS NULL AND admin_retention_or_rollback IS NULL)
								OR (admin_target_reference IS NOT NULL AND admin_actor IS NOT NULL AND admin_purpose IS NOT NULL AND admin_decision IS NOT NULL AND admin_retention_or_rollback IS NOT NULL)
							);
					END IF;
				END
				$$`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 164: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gormlib.DB) error {
			// Resolution audit evidence must survive a V2-boundary rollback.
			return nil
		},
	}
}
