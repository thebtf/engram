package gorm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTemporalTruthMigrationCreatesDedicatedTable(t *testing.T) {
	db := openCandidateTestDB(t)

	var tableCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'temporal_truth_records'
	`).Scan(&tableCount).Error)
	require.Equal(t, 1, tableCount, "temporal_truth_records table must exist after migration 157")

	for _, col := range []string{"fact_id", "fact_class", "project", "value", "valid_from", "valid_until", "invalidated_at", "invalidation_rationale", "source_memory_ids"} {
		var colCount int
		require.NoError(t, db.Raw(`
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'temporal_truth_records' AND column_name = ?
		`, col).Scan(&colCount).Error)
		require.Equal(t, 1, colCount, "column %q must exist in temporal_truth_records", col)
	}
}
