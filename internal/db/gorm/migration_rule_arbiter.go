// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func ruleArbiterBackgroundMigration145() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "145_rule_arbiter_background",
		Migrate: func(tx *gorm.DB) error {
			sqls := []string{
				`ALTER TABLE rule_candidates
					ADD COLUMN IF NOT EXISTS arbiter_action TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE rule_candidates
					ADD COLUMN IF NOT EXISTS arbiter_reason TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE rule_candidates
					ADD COLUMN IF NOT EXISTS arbiter_confidence DOUBLE PRECISION NOT NULL DEFAULT 0`,
				`ALTER TABLE rule_candidates
					ADD COLUMN IF NOT EXISTS arbiter_run_id BIGINT`,
				`ALTER TABLE rule_candidates
					ADD COLUMN IF NOT EXISTS arbiter_evaluation_id BIGINT`,
				`ALTER TABLE rule_candidates
					DROP CONSTRAINT IF EXISTS rule_candidates_arbiter_action_chk`,
				`ALTER TABLE rule_candidates
					ADD CONSTRAINT rule_candidates_arbiter_action_chk
						CHECK (arbiter_action = '' OR arbiter_action IN ('propose','hold','reject','skip','error'))`,
				`CREATE TABLE IF NOT EXISTS rule_arbiter_runs (
					id                       BIGSERIAL PRIMARY KEY,
					trigger                  TEXT NOT NULL,
					status                   TEXT NOT NULL,
					candidates_seen          INTEGER NOT NULL DEFAULT 0,
					candidates_evaluated     INTEGER NOT NULL DEFAULT 0,
					candidates_proposed      INTEGER NOT NULL DEFAULT 0,
					candidates_held          INTEGER NOT NULL DEFAULT 0,
					candidates_rejected      INTEGER NOT NULL DEFAULT 0,
					candidates_skipped       INTEGER NOT NULL DEFAULT 0,
					errors                   INTEGER NOT NULL DEFAULT 0,
					error_summary            TEXT,
					started_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
					finished_at              TIMESTAMPTZ,
					CONSTRAINT rule_arbiter_runs_status_chk
						CHECK (status IN ('started','completed','failed'))
				)`,
				`CREATE TABLE IF NOT EXISTS rule_arbiter_evaluations (
					id                       BIGSERIAL PRIMARY KEY,
					run_id                   BIGINT NOT NULL REFERENCES rule_arbiter_runs(id) ON DELETE CASCADE,
					candidate_id             BIGINT NOT NULL REFERENCES rule_candidates(id) ON DELETE CASCADE,
					evaluator_kind           TEXT NOT NULL,
					action                   TEXT NOT NULL,
					reason                   TEXT NOT NULL,
					confidence               DOUBLE PRECISION NOT NULL DEFAULT 0,
					parse_status             TEXT NOT NULL,
					proposal_json            JSONB NOT NULL DEFAULT '{}'::jsonb,
					raw_response             TEXT,
					error_summary            TEXT,
					created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
					CONSTRAINT rule_arbiter_evaluations_action_chk
						CHECK (action IN ('propose','hold','reject','skip','error')),
					CONSTRAINT rule_arbiter_evaluations_parse_status_chk
						CHECK (parse_status IN ('ok','failed','not_applicable')),
					CONSTRAINT rule_arbiter_evaluations_proposal_object_chk
						CHECK (jsonb_typeof(proposal_json) = 'object')
				)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_arbiter_runs_status_started
					ON rule_arbiter_runs (status, started_at)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_arbiter_evaluations_candidate_created
					ON rule_arbiter_evaluations (candidate_id, created_at)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_arbiter_evaluations_run_created
					ON rule_arbiter_evaluations (run_id, created_at)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_candidates_arbiter_action
					ON rule_candidates (arbiter_action)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_candidates_arbiter_due
					ON rule_candidates (status, review_after, created_at)`,
				`ALTER TABLE rule_candidates
					DROP CONSTRAINT IF EXISTS fk_rule_candidates_arbiter_run`,
				`ALTER TABLE rule_candidates
					ADD CONSTRAINT fk_rule_candidates_arbiter_run
						FOREIGN KEY (arbiter_run_id) REFERENCES rule_arbiter_runs(id) ON DELETE SET NULL`,
				`ALTER TABLE rule_candidates
					DROP CONSTRAINT IF EXISTS fk_rule_candidates_arbiter_evaluation`,
				`ALTER TABLE rule_candidates
					ADD CONSTRAINT fk_rule_candidates_arbiter_evaluation
						FOREIGN KEY (arbiter_evaluation_id) REFERENCES rule_arbiter_evaluations(id) ON DELETE SET NULL`,
			}
			for _, s := range sqls {
				if err := tx.Exec(s).Error; err != nil {
					return fmt.Errorf("migration 145: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			sqls := []string{
				`ALTER TABLE rule_candidates DROP CONSTRAINT IF EXISTS fk_rule_candidates_arbiter_evaluation`,
				`ALTER TABLE rule_candidates DROP CONSTRAINT IF EXISTS fk_rule_candidates_arbiter_run`,
				`ALTER TABLE rule_candidates DROP CONSTRAINT IF EXISTS rule_candidates_arbiter_action_chk`,
				`DROP TABLE IF EXISTS rule_arbiter_evaluations`,
				`DROP TABLE IF EXISTS rule_arbiter_runs`,
				`DROP INDEX IF EXISTS idx_rule_candidates_arbiter_action`,
				`DROP INDEX IF EXISTS idx_rule_candidates_arbiter_due`,
				`ALTER TABLE rule_candidates DROP COLUMN IF EXISTS arbiter_evaluation_id`,
				`ALTER TABLE rule_candidates DROP COLUMN IF EXISTS arbiter_run_id`,
				`ALTER TABLE rule_candidates DROP COLUMN IF EXISTS arbiter_confidence`,
				`ALTER TABLE rule_candidates DROP COLUMN IF EXISTS arbiter_reason`,
				`ALTER TABLE rule_candidates DROP COLUMN IF EXISTS arbiter_action`,
			}
			for _, s := range sqls {
				if err := tx.Exec(s).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}
