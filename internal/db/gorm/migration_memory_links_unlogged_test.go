package gorm

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryLinksMigrationRejectUnloggedPostgres(t *testing.T) {
	t.Run("retained_unlogged_receipts", func(t *testing.T) {
		db, _ := openMemoryContext191UpgradeDB(t)
		require.NoError(t, memoryLinksRecoveryRunner(db).Migrate())
		memoryLinksRecoverySeedLink(t, db)
		require.NoError(t, db.Exec(`UPDATE memory_links SET removed_at = '2025-01-02T03:04:05Z' WHERE id = 7123`).Error)
		require.NoError(t, db.Exec(`INSERT INTO memory_link_receipts (principal_kind, principal, request_key, request_digest, link_id) VALUES ('agent','agent/recovery','retained-key','retained-digest',7123)`).Error)
		require.NoError(t, db.Exec(`ALTER TABLE memory_link_receipts SET UNLOGGED`).Error)
		require.NoError(t, db.Exec(`DELETE FROM migrations WHERE id = ?`, memoryLinksMigration195().ID).Error)
		require.Zero(t, memoryLinksRecoveryMarker(t, db))
		rows := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryRows)
		receipts := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryReceipts)
		identities := memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array('memory_links'::regclass::oid, 'memory_link_receipts'::regclass::oid, 'memory_links_id_seq'::regclass::oid)::text`)
		constraints := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryConstraints)
		indexes := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryIndexes)
		var persistence string
		require.NoError(t, db.Raw(`SELECT relpersistence::text FROM pg_class WHERE oid = 'memory_link_receipts'::regclass`).Scan(&persistence).Error)
		require.Equal(t, "u", persistence)
		assert.Error(t, memoryLinksRecoveryRunner(db).Migrate(), "an unlogged receipt table must not be adopted")
		assert.Zero(t, memoryLinksRecoveryMarker(t, db), "refusal must not record a migration marker")
		assert.Equal(t, rows, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryRows))
		assert.Equal(t, receipts, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryReceipts))
		assert.Equal(t, identities, memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array('memory_links'::regclass::oid, 'memory_link_receipts'::regclass::oid, 'memory_links_id_seq'::regclass::oid)::text`))
		assert.Equal(t, constraints, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryConstraints))
		assert.Equal(t, indexes, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryIndexes))
		require.NoError(t, db.Raw(`SELECT relpersistence::text FROM pg_class WHERE oid = 'memory_link_receipts'::regclass`).Scan(&persistence).Error)
		assert.Equal(t, "u", persistence)
	})

	t.Run("partial_unlogged_links", func(t *testing.T) {
		db, _ := openMemoryContext191UpgradeDB(t)
		require.NoError(t, db.Exec(`CREATE UNLOGGED TABLE memory_links `+fmt.Sprintf(memoryLinksTableDDL, "memories", "memories")).Error)
		memoryLinksRecoverySeedLink(t, db)
		rows := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryRows)
		identities := memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array('memory_links'::regclass::oid, 'memory_links_id_seq'::regclass::oid)::text`)
		constraints := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryConstraints)
		seq := memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array(last_value, is_called)::text FROM memory_links_id_seq`)
		var persistence string
		require.NoError(t, db.Raw(`SELECT relpersistence::text FROM pg_class WHERE oid = 'memory_links'::regclass`).Scan(&persistence).Error)
		require.Equal(t, "u", persistence)
		require.Zero(t, memoryLinksRecoveryMarker(t, db))
		assert.Error(t, memoryLinksRecoveryRunner(db).Migrate(), "an unlogged link table must not be adopted")
		assert.Zero(t, memoryLinksRecoveryMarker(t, db), "refusal must not record a migration marker")
		assert.Equal(t, rows, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryRows))
		assert.Equal(t, identities, memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array('memory_links'::regclass::oid, 'memory_links_id_seq'::regclass::oid)::text`))
		assert.Equal(t, constraints, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryConstraints))
		assert.Equal(t, seq, memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array(last_value, is_called)::text FROM memory_links_id_seq`))
		var receiptsAbsent bool
		require.NoError(t, db.Raw(`SELECT to_regclass('memory_link_receipts') IS NULL`).Scan(&receiptsAbsent).Error)
		assert.True(t, receiptsAbsent, "refusal must roll back added objects")
		require.NoError(t, db.Raw(`SELECT relpersistence::text FROM pg_class WHERE oid = 'memory_links'::regclass`).Scan(&persistence).Error)
		assert.Equal(t, "u", persistence)
	})
}
