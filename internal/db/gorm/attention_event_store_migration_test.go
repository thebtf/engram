package gorm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttentionEventsMigrationCreatesBoundedTableAndIndexes(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	var tableCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'attention_events'
	`).Scan(&tableCount).Error)
	require.Equal(t, 1, tableCount, "attention_events table must exist after migration 158")

	for _, col := range []string{"id", "project", "session_id", "source_turn_hash", "derived_intent", "agent_confirmed", "horizon", "privacy_class", "created_at", "updated_at"} {
		var colCount int
		require.NoError(t, db.Raw(`
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'attention_events' AND column_name = ?
		`, col).Scan(&colCount).Error)
		require.Equal(t, 1, colCount, "column %q must exist in attention_events", col)
	}

	var rawColumnCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'attention_events'
		  AND column_name IN ('text', 'raw_text', 'source_turn', 'raw_source_turn', 'prompt', 'user_prompt')
	`).Scan(&rawColumnCount).Error)
	require.Zero(t, rawColumnCount, "attention_events must not contain raw prompt/source-turn columns")

	var indexCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) FROM pg_indexes
		WHERE schemaname = 'public'
		  AND tablename = 'attention_events'
		  AND indexname IN ('idx_attention_events_project_created', 'idx_attention_events_session_created')
	`).Scan(&indexCount).Error)
	require.Equal(t, 2, indexCount, "attention_events must have project/time and session/time indexes")
}

func TestAttentionEventsMigrationEnforcesConfirmedHashHorizonAndPrivacyConstraints(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM attention_events WHERE project = 'test-attention-constraint'`)

	baseInsert := `
		INSERT INTO attention_events (project, session_id, source_turn_hash, derived_intent, agent_confirmed, horizon, privacy_class)
		VALUES (?, 'session-1', ?, 'bounded directive', ?, ?, ?)
	`

	validHash := validAttentionHash("a")
	require.NoError(t, db.Exec(baseInsert, "test-attention-constraint", validHash, true, "project", "secret").Error)
	require.Error(t, db.Exec(baseInsert, "test-attention-constraint", validHash, false, "project", "secret").Error, "agent_confirmed=false must be rejected by the database constraint")
	require.Error(t, db.Exec(baseInsert, "test-attention-constraint", "RAW_SOURCE_TURN_NEVER_STORE", true, "project", "secret").Error, "non-canonical source_turn_hash must be rejected by the database constraint")
	require.Error(t, db.Exec(baseInsert, "test-attention-constraint", validHash, true, "forever", "secret").Error, "invalid horizon must be rejected by the database constraint")
	require.Error(t, db.Exec(baseInsert, "test-attention-constraint", validHash, true, "project", "private-ish").Error, "invalid privacy class must be rejected by the database constraint")
}

func TestAttentionEventsMigrationRollbackDropsTable(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()

	migration := attentionEventsMigration158()
	require.NoError(t, migration.Rollback(tx))
	require.False(t, tx.Migrator().HasTable("attention_events"), "rollback must drop attention_events")

	require.NoError(t, migration.Migrate(tx))
	require.True(t, tx.Migrator().HasTable("attention_events"), "migration must recreate attention_events after rollback")
}
