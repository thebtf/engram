package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func ruleGovernanceEscapeConstraintsMigration159() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "159_rule_governance_escape_constraints",
		Migrate: func(tx *gorm.DB) error {
			for _, table := range []string{"rule_candidates", "rule_versions"} {
				for _, column := range []string{"anti_capture_status", "conflict_status", "decay_policy"} {
					constraint := table + "_" + column + "_escape_chk"
					if err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s`, table, constraint)).Error; err != nil {
						return fmt.Errorf("migration 159 drop %s: %w", constraint, err)
					}
					stmt := fmt.Sprintf(`ALTER TABLE %s ADD CONSTRAINT %s CHECK (
						btrim(%s) <> '' AND (
							%s !~ '^(HYPOTHESIS|BLOCKED|NEEDS CLARIFICATION)' OR
							%s ~ '^(HYPOTHESIS|BLOCKED|NEEDS CLARIFICATION): .*[[:graph:]]$'
						)
					)`, table, constraint, column, column, column)
					if err := tx.Exec(stmt).Error; err != nil {
						return fmt.Errorf("migration 159 add %s: %w", constraint, err)
					}
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	}
}
