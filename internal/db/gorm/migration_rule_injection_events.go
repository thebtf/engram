package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func ruleInjectionEventsMigration146() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "146_rule_injection_events",
		Migrate: func(tx *gorm.DB) error {
			sqls := []string{
				`CREATE TABLE IF NOT EXISTS rule_injection_events (
					id                         BIGSERIAL PRIMARY KEY,
					session_id                 TEXT NOT NULL,
					project                    TEXT NOT NULL,
					surface                    TEXT NOT NULL,
					rule_version_id            BIGINT REFERENCES rule_versions(id) ON DELETE SET NULL,
					legacy_behavioral_rule_id  BIGINT REFERENCES behavioral_rules(id) ON DELETE SET NULL,
					event_type                 TEXT NOT NULL,
					reason                     TEXT NOT NULL DEFAULT '',
					budget_position            INTEGER NOT NULL DEFAULT 0,
					created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
					CONSTRAINT rule_injection_events_type_chk
						CHECK (event_type IN (
							'emitted_kernel',
							'emitted_contextual',
							'deferred_budget',
							'suppressed_state',
							'suppressed_predicate',
							'suppressed_prompt_safety',
							'fallback_legacy',
							'router_error'
						))
				)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_injection_events_project_created
					ON rule_injection_events (project, created_at DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_injection_events_session_created
					ON rule_injection_events (session_id, created_at DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_rule_injection_events_rule_version_created
					ON rule_injection_events (rule_version_id, created_at DESC)
					WHERE rule_version_id IS NOT NULL`,
				`CREATE INDEX IF NOT EXISTS idx_rule_injection_events_legacy_rule_created
					ON rule_injection_events (legacy_behavioral_rule_id, created_at DESC)
					WHERE legacy_behavioral_rule_id IS NOT NULL`,
				`CREATE INDEX IF NOT EXISTS idx_rule_injection_events_event_created
					ON rule_injection_events (event_type, created_at DESC)`,
			}
			for _, stmt := range sqls {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 146: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS rule_injection_events`).Error
		},
	}
}
