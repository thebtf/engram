package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	gormlib "gorm.io/gorm"
)

// projectIdentityV3ComparisonsMigration165 adds durable, redacted comparison
// telemetry. It is additive: no V2 row, project binding, merge, redirect, or
// read path is changed.
func projectIdentityV3ComparisonsMigration165() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "165_project_identity_v3_comparisons",
		Migrate: func(tx *gormlib.DB) error {
			for _, stmt := range []string{
				`CREATE TABLE IF NOT EXISTS project_identity_comparisons (
					comparison_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
					idempotency_key TEXT NOT NULL UNIQUE CHECK (idempotency_key ~ '^sha256:[0-9a-f]{64}$'),
					correlation TEXT NOT NULL CHECK (correlation ~ '^[^[:space:]@/\\]+$'),
					v3_outcome TEXT NOT NULL CHECK (v3_outcome IN (
						'PROJECT_RESOLVED', 'PROJECT_REDIRECTED', 'PROJECT_ONBOARDING_REQUIRED',
						'PROJECT_ANCHOR_INVALID', 'PROJECT_SCOPE_MISMATCH',
						'PROJECT_NESTED_REPOSITORY_UNRESOLVED', 'PROJECT_ANCHOR_DECISION_REQUIRED',
						'PROJECT_IDENTITY_AMBIGUOUS', 'PROJECT_DESCRIPTOR_UNSUPPORTED',
						'PROJECT_DESCRIPTOR_INVALID', 'PROJECT_KEY_CLIENT_ASSERTION_FORBIDDEN'
					)),
					legacy_outcome TEXT NOT NULL CHECK (legacy_outcome IN ('resolved', 'refusal', 'unavailable')),
					classification TEXT NOT NULL CHECK (classification IN ('equal', 'mismatch', 'refusal', 'unavailable')),
					client_instance_id TEXT NOT NULL CHECK (client_instance_id ~ '^[^[:space:]@/\\]+$'),
					transport TEXT NOT NULL CHECK (transport IN ('grpc', 'http', 'hook', 'daemon', 'openclaw')),
					scope TEXT NOT NULL CHECK (scope IN ('repository', 'directory')),
					freshness TEXT NOT NULL CHECK (freshness IN ('fresh', 'stale', 'unknown')),
					evidence_fingerprint TEXT NOT NULL CHECK (evidence_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
					created_at TIMESTAMPTZ NOT NULL DEFAULT now()
				)`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_project_identity_comparisons_idempotency_key
					ON project_identity_comparisons (idempotency_key)`,
				`CREATE INDEX IF NOT EXISTS idx_project_identity_comparisons_correlation
					ON project_identity_comparisons (correlation)`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 165: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gormlib.DB) error {
			// Comparison evidence remains durable while V2 read compatibility remains unchanged.
			return nil
		},
	}
}
