package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	gormlib "gorm.io/gorm"
)

// temporalTruthRecordsMigration157 creates the dedicated additive admission
// substrate for CR-011 selective temporal truth. A row exists iff a fact chain
// was admitted into the bounded temporal slice.
func temporalTruthRecordsMigration157() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "157_temporal_truth_records",
		Migrate: func(tx *gormlib.DB) error {
			stmts := []string{
				`CREATE TABLE IF NOT EXISTS temporal_truth_records (
					id BIGSERIAL PRIMARY KEY,
					fact_id TEXT NOT NULL,
					fact_class TEXT NOT NULL,
					project TEXT NOT NULL,
					value TEXT NOT NULL,
					valid_from TIMESTAMPTZ NOT NULL DEFAULT now(),
					valid_until TIMESTAMPTZ NOT NULL DEFAULT '9999-12-31T23:59:59Z',
					invalidated_at TIMESTAMPTZ,
					invalidation_rationale TEXT NOT NULL DEFAULT '',
					source_memory_ids BIGINT[] NOT NULL DEFAULT ARRAY[]::BIGINT[],
					created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
					updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
					CONSTRAINT temporal_truth_records_fact_id_not_blank CHECK (btrim(fact_id) <> ''),
					CONSTRAINT temporal_truth_records_fact_class_not_blank CHECK (btrim(fact_class) <> ''),
					CONSTRAINT temporal_truth_records_project_not_blank CHECK (btrim(project) <> ''),
					CONSTRAINT temporal_truth_records_value_not_blank CHECK (btrim(value) <> ''),
					CONSTRAINT temporal_truth_records_valid_window_chk CHECK (valid_until >= valid_from)
				)`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_temporal_truth_records_fact_valid_from
					ON temporal_truth_records (fact_id, valid_from)`,
				`CREATE INDEX IF NOT EXISTS idx_temporal_truth_records_fact_project_valid_from
					ON temporal_truth_records (fact_id, project, valid_from DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_temporal_truth_records_fact_class_project
					ON temporal_truth_records (fact_class, project)`,
			}
			for _, stmt := range stmts {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 157: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gormlib.DB) error {
			// Additive-only production migration: temporal admission evidence must
			// remain reversible through forward migrations, not destructive rollback.
			return nil
		},
	}
}
