// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func ruleGovernanceMigration144() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "144_rule_governance_core",
		Migrate: func(tx *gorm.DB) error {
			sqls := []string{
				`CREATE TABLE IF NOT EXISTS rule_candidates (
					id                         BIGSERIAL PRIMARY KEY,
					source_signal_type         TEXT NOT NULL,
					source_session_id          TEXT,
					source_project             TEXT,
					source_actor               TEXT NOT NULL,
					proposed_content           TEXT NOT NULL,
					proposed_scope             TEXT NOT NULL,
					proposed_audience          TEXT NOT NULL,
					activation_predicate_json  JSONB NOT NULL DEFAULT '{}'::jsonb,
					evidence_handles_json      JSONB NOT NULL DEFAULT '[]'::jsonb,
					confidence                 DOUBLE PRECISION NOT NULL DEFAULT 0,
					recurrence_count           INTEGER NOT NULL DEFAULT 0,
					anti_capture_status        TEXT NOT NULL,
					anti_capture_reason        TEXT,
					conflict_status            TEXT NOT NULL,
					status                     TEXT NOT NULL DEFAULT 'pending',
					fingerprint                TEXT NOT NULL DEFAULT '',
					review_after               TIMESTAMPTZ,
					last_evaluated_at          TIMESTAMPTZ,
					decay_policy               TEXT NOT NULL,
					created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
					updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
					CONSTRAINT rule_candidates_status_chk
						CHECK (status IN ('pending','drafted','rejected','duplicate','superseded')),
					CONSTRAINT rule_candidates_activation_object_chk
						CHECK (jsonb_typeof(activation_predicate_json) = 'object'),
					CONSTRAINT rule_candidates_evidence_array_chk
						CHECK (jsonb_typeof(evidence_handles_json) = 'array')
				)`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_rule_candidates_fingerprint_mutable
					ON rule_candidates (fingerprint)
					WHERE status IN ('pending','drafted') AND fingerprint <> ''`,
				`CREATE INDEX IF NOT EXISTS idx_rule_candidates_project_status
					ON rule_candidates (source_project, status)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_candidates_status_review
					ON rule_candidates (status, review_after)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_candidates_anti_capture_reason
					ON rule_candidates (anti_capture_reason)`,
				`CREATE TABLE IF NOT EXISTS rule_families (
					id                         BIGSERIAL PRIMARY KEY,
					family_key                 TEXT NOT NULL UNIQUE,
					created_from_candidate_id  BIGINT REFERENCES rule_candidates(id) ON DELETE SET NULL,
					created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
					updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
				)`,
				`CREATE TABLE IF NOT EXISTS rule_versions (
					id                         BIGSERIAL PRIMARY KEY,
					family_id                  BIGINT NOT NULL REFERENCES rule_families(id) ON DELETE CASCADE,
					source_candidate_id         BIGINT REFERENCES rule_candidates(id) ON DELETE SET NULL,
					active_behavioral_rule_id   BIGINT REFERENCES behavioral_rules(id) ON DELETE SET NULL,
					content                    TEXT NOT NULL,
					summary                    TEXT,
					scope                      TEXT NOT NULL,
					owner                      TEXT NOT NULL,
					audience                   TEXT NOT NULL,
					activation_predicate_json  JSONB NOT NULL DEFAULT '{}'::jsonb,
					evidence_handles_json      JSONB NOT NULL DEFAULT '[]'::jsonb,
					state                      TEXT NOT NULL,
					protected                  BOOLEAN NOT NULL DEFAULT false,
					pinned                     BOOLEAN NOT NULL DEFAULT false,
					priority                   INTEGER NOT NULL DEFAULT 0,
					budget_class               TEXT NOT NULL DEFAULT 'contextual',
					anti_capture_status        TEXT NOT NULL,
					conflict_status            TEXT NOT NULL,
					decay_policy               TEXT NOT NULL,
					last_evaluated_at          TIMESTAMPTZ,
					supersedes_version_id      BIGINT REFERENCES rule_versions(id) ON DELETE SET NULL,
					effective_from             TIMESTAMPTZ,
					effective_until            TIMESTAMPTZ,
					created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
					updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
					archived_at                TIMESTAMPTZ,
					CONSTRAINT rule_versions_state_chk
						CHECK (state IN ('draft','shadow','canary','active_project','active_shared','active_global','kernel','superseded','archived','rejected')),
					CONSTRAINT rule_versions_activation_object_chk
						CHECK (jsonb_typeof(activation_predicate_json) = 'object'),
					CONSTRAINT rule_versions_evidence_array_chk
						CHECK (jsonb_typeof(evidence_handles_json) = 'array')
				)`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_rule_versions_source_candidate_once
					ON rule_versions (source_candidate_id)
					WHERE source_candidate_id IS NOT NULL`,
				`CREATE INDEX IF NOT EXISTS idx_rule_versions_family_state
					ON rule_versions (family_id, state)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_versions_source_candidate
					ON rule_versions (source_candidate_id)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_versions_scope_state_priority
					ON rule_versions (scope, state, priority)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_versions_active_behavioral_rule
					ON rule_versions (active_behavioral_rule_id)`,
				`CREATE TABLE IF NOT EXISTS rule_transition_log (
					id                         BIGSERIAL PRIMARY KEY,
					rule_version_id            BIGINT REFERENCES rule_versions(id) ON DELETE SET NULL,
					candidate_id               BIGINT REFERENCES rule_candidates(id) ON DELETE SET NULL,
					actor                      TEXT NOT NULL,
					actor_kind                 TEXT NOT NULL,
					action                     TEXT NOT NULL,
					from_state                 TEXT,
					to_state                   TEXT NOT NULL,
					reason                     TEXT NOT NULL,
					evidence_handles_json      JSONB NOT NULL DEFAULT '[]'::jsonb,
					snapshot_id                TEXT,
					created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
					CONSTRAINT rule_transition_evidence_array_chk
						CHECK (jsonb_typeof(evidence_handles_json) = 'array')
				)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_transition_log_version_created
					ON rule_transition_log (rule_version_id, created_at)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_transition_log_candidate_created
					ON rule_transition_log (candidate_id, created_at)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_transition_log_actor_action
					ON rule_transition_log (actor_kind, action)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_transition_log_snapshot
					ON rule_transition_log (snapshot_id)`,
				`CREATE TABLE IF NOT EXISTS rule_governance_snapshots (
					id                         BIGSERIAL PRIMARY KEY,
					snapshot_id                TEXT NOT NULL UNIQUE,
					op_type                    TEXT NOT NULL,
					actor                      TEXT NOT NULL,
					before_state_json          JSONB NOT NULL DEFAULT '{}'::jsonb,
					after_state_json           JSONB,
					status                     TEXT NOT NULL DEFAULT 'committed',
					created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
					rolled_back_at             TIMESTAMPTZ,
					pinned                     BOOLEAN NOT NULL DEFAULT false,
					CONSTRAINT rule_governance_snapshots_status_chk
						CHECK (status IN ('committed','rolled_back')),
					CONSTRAINT rule_governance_snapshots_before_object_chk
						CHECK (jsonb_typeof(before_state_json) = 'object'),
					CONSTRAINT rule_governance_snapshots_after_object_chk
						CHECK (after_state_json IS NULL OR jsonb_typeof(after_state_json) = 'object')
				)`,
			}
			for _, s := range sqls {
				if err := tx.Exec(s).Error; err != nil {
					return fmt.Errorf("migration 144: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			sqls := []string{
				`DROP TABLE IF EXISTS rule_transition_log`,
				`DROP TABLE IF EXISTS rule_governance_snapshots`,
				`DROP TABLE IF EXISTS rule_versions`,
				`DROP TABLE IF EXISTS rule_families`,
				`DROP TABLE IF EXISTS rule_candidates`,
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
