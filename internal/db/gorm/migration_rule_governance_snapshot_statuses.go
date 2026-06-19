package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func ruleGovernanceSnapshotStatusesMigration147() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "147_rule_governance_snapshot_statuses",
		Migrate: func(tx *gorm.DB) error {
			sqls := []string{
				`ALTER TABLE rule_governance_snapshots
					DROP CONSTRAINT IF EXISTS rule_governance_snapshots_status_chk`,
				`ALTER TABLE rule_governance_snapshots
					ADD CONSTRAINT rule_governance_snapshots_status_chk
						CHECK (status IN ('committed','rolled_back','failed','rollback_conflict'))`,
			}
			for _, stmt := range sqls {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 147: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			sqls := []string{
				`UPDATE rule_governance_snapshots
					SET status = 'committed'
					WHERE status IN ('failed','rollback_conflict')`,
				`ALTER TABLE rule_governance_snapshots
					DROP CONSTRAINT IF EXISTS rule_governance_snapshots_status_chk`,
				`ALTER TABLE rule_governance_snapshots
					ADD CONSTRAINT rule_governance_snapshots_status_chk
						CHECK (status IN ('committed','rolled_back'))`,
			}
			for _, stmt := range sqls {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 147 rollback: %w", err)
				}
			}
			return nil
		},
	}
}
