package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	gormlib "gorm.io/gorm"
)

// projectIdentityV3Migration162 adds dormant V3 identity storage without
// assigning a canonical key or changing any V2 project read path.
func projectIdentityV3Migration162() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "162_project_identity_v3",
		Migrate: func(tx *gormlib.DB) error {
			stmts := []string{
				`ALTER TABLE projects ADD COLUMN IF NOT EXISTS project_key UUID`,
				`ALTER TABLE projects ADD COLUMN IF NOT EXISTS anchor_project_id UUID`,
				`ALTER TABLE projects ADD COLUMN IF NOT EXISTS identity_scope TEXT`,
				`ALTER TABLE projects ADD COLUMN IF NOT EXISTS identity_status TEXT`,
				`DO $$ BEGIN
					IF NOT EXISTS (
						SELECT 1 FROM pg_constraint
						WHERE conname = 'projects_identity_scope_chk' AND conrelid = 'projects'::regclass
					) THEN
						ALTER TABLE projects ADD CONSTRAINT projects_identity_scope_chk
							CHECK (identity_scope IS NULL OR identity_scope IN ('repository', 'directory'));
					END IF;
				END $$`,
				`DO $$ BEGIN
					IF NOT EXISTS (
						SELECT 1 FROM pg_constraint
						WHERE conname = 'projects_identity_status_chk' AND conrelid = 'projects'::regclass
					) THEN
						ALTER TABLE projects ADD CONSTRAINT projects_identity_status_chk
							CHECK (identity_status IS NULL OR identity_status IN ('active', 'merged', 'retired'));
					END IF;
				END $$`,
				`DO $$ BEGIN
					IF NOT EXISTS (
						SELECT 1 FROM pg_constraint
						WHERE conname = 'projects_project_key_key' AND conrelid = 'projects'::regclass
					) THEN
						ALTER TABLE projects ADD CONSTRAINT projects_project_key_key UNIQUE (project_key);
					END IF;
				END $$`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_anchor_project_id
					ON projects (anchor_project_id) WHERE anchor_project_id IS NOT NULL`,
				`CREATE TABLE IF NOT EXISTS project_identifiers (
					identifier_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
					project_key UUID NOT NULL,
					scheme TEXT NOT NULL,
					normalized_value TEXT NOT NULL,
					source TEXT NOT NULL,
					provenance JSONB NOT NULL DEFAULT '{}'::jsonb,
					status TEXT NOT NULL DEFAULT 'active',
					first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
					last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
					CONSTRAINT project_identifiers_scheme_chk CHECK (scheme IN (
						'anchor_v3', 'binding_v2', 'git_remote_relative_v2', 'git_hash_v2',
						'path_hash_v1', 'legacy_slug', 'non_git_anchor_v2', 'manual_alias'
					)),
					CONSTRAINT project_identifiers_status_chk CHECK (status IN ('active', 'redirected', 'retired')),
					CONSTRAINT project_identifiers_normalized_value_not_blank CHECK (btrim(normalized_value) <> '')
				)`,
				`DO $$ BEGIN
					IF NOT EXISTS (
						SELECT 1 FROM pg_constraint
						WHERE conname = 'project_identifiers_project_key_fkey' AND conrelid = 'project_identifiers'::regclass
					) THEN
						ALTER TABLE project_identifiers ADD CONSTRAINT project_identifiers_project_key_fkey
							FOREIGN KEY (project_key) REFERENCES projects(project_key) ON DELETE RESTRICT;
					END IF;
				END $$`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_project_identifiers_live_scheme_value
					ON project_identifiers (scheme, normalized_value)
					WHERE status IN ('active', 'redirected')`,
				`CREATE INDEX IF NOT EXISTS idx_project_identifiers_project_key
					ON project_identifiers (project_key)`,
				`CREATE TABLE IF NOT EXISTS project_merge_audits (
					merge_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
					target_project_key UUID NOT NULL,
					evidence_class TEXT NOT NULL,
					conflict_policy TEXT NOT NULL,
					before_table_counts JSONB NOT NULL DEFAULT '{}'::jsonb,
					after_table_counts JSONB NOT NULL DEFAULT '{}'::jsonb,
					fingerprints JSONB NOT NULL DEFAULT '{}'::jsonb,
					privacy_result TEXT NOT NULL,
					actor TEXT NOT NULL,
					started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
					completed_at TIMESTAMPTZ,
					rollback_boundary TEXT NOT NULL,
					migration_receipt_ref TEXT,
					created_at TIMESTAMPTZ NOT NULL DEFAULT now()
				)`,
				`DO $$ BEGIN
					IF NOT EXISTS (
						SELECT 1 FROM pg_constraint
						WHERE conname = 'project_merge_audits_target_project_key_fkey' AND conrelid = 'project_merge_audits'::regclass
					) THEN
						ALTER TABLE project_merge_audits ADD CONSTRAINT project_merge_audits_target_project_key_fkey
							FOREIGN KEY (target_project_key) REFERENCES projects(project_key) ON DELETE RESTRICT;
					END IF;
				END $$`,
				// PostgreSQL cannot enforce a foreign key on UUID array elements, so each
				// source relationship is normalized. AR-2 does not backfill or apply merges.
				`CREATE TABLE IF NOT EXISTS project_merge_audit_sources (
					merge_id UUID NOT NULL,
					source_project_key UUID NOT NULL,
					PRIMARY KEY (merge_id, source_project_key)
				)`,
				`DO $$ BEGIN
					IF NOT EXISTS (
						SELECT 1 FROM pg_constraint
						WHERE conname = 'project_merge_audit_sources_merge_id_fkey' AND conrelid = 'project_merge_audit_sources'::regclass
					) THEN
						ALTER TABLE project_merge_audit_sources ADD CONSTRAINT project_merge_audit_sources_merge_id_fkey
							FOREIGN KEY (merge_id) REFERENCES project_merge_audits(merge_id) ON DELETE RESTRICT;
					END IF;
				END $$`,
				`DO $$ BEGIN
					IF NOT EXISTS (
						SELECT 1 FROM pg_constraint
						WHERE conname = 'project_merge_audit_sources_project_key_fkey' AND conrelid = 'project_merge_audit_sources'::regclass
					) THEN
						ALTER TABLE project_merge_audit_sources ADD CONSTRAINT project_merge_audit_sources_project_key_fkey
							FOREIGN KEY (source_project_key) REFERENCES projects(project_key) ON DELETE RESTRICT;
					END IF;
				END $$`,
				`CREATE INDEX IF NOT EXISTS idx_project_merge_audits_target_project_key
					ON project_merge_audits (target_project_key)`,
				`CREATE INDEX IF NOT EXISTS idx_project_merge_audit_sources_project_key
					ON project_merge_audit_sources (source_project_key)`,
			}
			for _, stmt := range stmts {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 162: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gormlib.DB) error {
			// V3 evidence is durable. The additive schema leaves every V2 column and
			// read path intact, so the compatible V2 read boundary is already available
			// without deleting V3 identifiers, audits, provenance, or bindings.
			return nil
		},
	}
}
