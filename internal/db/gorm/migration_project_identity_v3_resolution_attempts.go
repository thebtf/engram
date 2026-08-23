package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	gormlib "gorm.io/gorm"
)

// projectIdentityV3ResolutionAttemptsMigration163 adds immutable redacted
// resolver audit records without changing the compatible V2 read boundary.
func projectIdentityV3ResolutionAttemptsMigration163() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "163_project_identity_v3_resolution_attempts",
		Migrate: func(tx *gormlib.DB) error {
			for _, stmt := range []string{
				`CREATE TABLE IF NOT EXISTS project_resolution_attempts (
					attempt_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
					correlation TEXT NOT NULL,
					intent TEXT NOT NULL CHECK (intent IN (
						'resolve_existing', 'register_anchor', 'read_filter', 'admin_target'
					)),
					outcome TEXT NOT NULL CHECK (outcome IN (
						'PROJECT_RESOLVED', 'PROJECT_REDIRECTED', 'PROJECT_ONBOARDING_REQUIRED',
						'PROJECT_ANCHOR_INVALID', 'PROJECT_SCOPE_MISMATCH',
						'PROJECT_NESTED_REPOSITORY_UNRESOLVED', 'PROJECT_ANCHOR_DECISION_REQUIRED',
						'PROJECT_IDENTITY_AMBIGUOUS', 'PROJECT_DESCRIPTOR_UNSUPPORTED',
						'PROJECT_DESCRIPTOR_INVALID', 'PROJECT_KEY_CLIENT_ASSERTION_FORBIDDEN'
					)),
					anchor_project_id UUID,
					descriptor_version INTEGER NOT NULL CHECK (descriptor_version >= 0),
					provenance TEXT NOT NULL CHECK (provenance = 'anchor_v3'),
					redirect_reference TEXT,
					created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
					CONSTRAINT project_resolution_attempts_redirect_chk CHECK (
						(outcome = 'PROJECT_REDIRECTED' AND redirect_reference IS NOT NULL)
						OR (outcome <> 'PROJECT_REDIRECTED' AND redirect_reference IS NULL)
					)
				)`,
				`CREATE INDEX IF NOT EXISTS idx_project_resolution_attempts_correlation
					ON project_resolution_attempts (correlation)`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 163: %w", err)
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
