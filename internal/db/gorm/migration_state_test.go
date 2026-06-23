package gorm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSortAppliedMigrationIDsUsesNumericSequence(t *testing.T) {
	ids := []string{
		"151_behavioral_rules_enabled",
		"abc_manual_unknown",
		"010_middle",
		"002_user_prompts",
		"1000_future_width",
		"001_core_tables",
	}

	assert.Equal(t, []string{
		"001_core_tables",
		"002_user_prompts",
		"010_middle",
		"151_behavioral_rules_enabled",
		"1000_future_width",
		"abc_manual_unknown",
	}, sortAppliedMigrationIDs(ids))
}
