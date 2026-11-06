package gorm

import (
	"fmt"
	"testing"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

func memoryLinksRecoveryRunner(db *gormlib.DB) *gormigrate.Gormigrate {
	return gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{memoryLinksMigration192()})
}

func memoryLinksRecoveryMarker(t *testing.T, db *gormlib.DB) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM migrations WHERE id = ?`, memoryLinksMigration192().ID).Scan(&count).Error)
	return count
}

func memoryLinksRecoverySnapshot(t *testing.T, db *gormlib.DB, query string) string {
	t.Helper()
	var value string
	require.NoError(t, db.Raw(query).Scan(&value).Error)
	return value
}

const memoryLinksRecoveryRows = `SELECT COALESCE(jsonb_agg(to_jsonb(l) ORDER BY id), '[]'::jsonb)::text FROM memory_links l`
const memoryLinksRecoveryReceipts = `SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY principal_kind, principal, request_key), '[]'::jsonb)::text FROM memory_link_receipts r`
const memoryLinksRecoveryConstraints = `SELECT COALESCE(jsonb_agg(jsonb_build_array(c.conrelid::regclass::text, c.conname, c.contype::text, pg_get_constraintdef(c.oid)) ORDER BY c.conrelid::regclass::text, c.conname), '[]'::jsonb)::text FROM pg_constraint c WHERE c.conrelid IN (to_regclass('memory_links'), to_regclass('memory_link_receipts'))`
const memoryLinksRecoveryIndexes = `SELECT COALESCE(jsonb_agg(jsonb_build_array(c.relname, i.relname, ix.indisunique, pg_get_indexdef(i.oid)) ORDER BY c.relname, i.relname), '[]'::jsonb)::text FROM pg_index ix JOIN pg_class c ON c.oid = ix.indrelid JOIN pg_class i ON i.oid = ix.indexrelid WHERE c.oid IN (to_regclass('memory_links'), to_regclass('memory_link_receipts'))`

func memoryLinksRecoverySeedLink(t *testing.T, db *gormlib.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO memory_links (id, source_id, target_id, relation, target_version, author_principal_kind, author_principal, source_session_id) VALUES (7123, 19101, 19102, 'references', 7, 'agent', 'agent/recovery', 'untrusted-session-α')`).Error)
	require.NoError(t, db.Exec(`SELECT setval(pg_get_serial_sequence('memory_links', 'id'), 7123, true)`).Error)
}

func TestMemoryLinksMigrationRecoveryPostgres(t *testing.T) {
	t.Run("interrupted_ddl_recovers_retained_link", func(t *testing.T) {
		db, _ := openMemoryContext191UpgradeDB(t)
		require.NoError(t, db.Exec(`CREATE TABLE memory_links `+fmt.Sprintf(memoryLinksTableDDL, "memories", "memories")).Error)
		require.Zero(t, memoryLinksRecoveryMarker(t, db))
		memoryLinksRecoverySeedLink(t, db)
		rows := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryRows)
		constraints := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryConstraints)
		tableOID := memoryLinksRecoverySnapshot(t, db, `SELECT 'memory_links'::regclass::oid::text`)
		seq := memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array(last_value, is_called)::text FROM memory_links_id_seq`)
		require.NoError(t, memoryLinksRecoveryRunner(db).Migrate(), "restart must finish partially committed DDL")
		require.EqualValues(t, 1, memoryLinksRecoveryMarker(t, db))
		require.Equal(t, rows, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryRows))
		require.Equal(t, tableOID, memoryLinksRecoverySnapshot(t, db, `SELECT 'memory_links'::regclass::oid::text`))
		require.Equal(t, seq, memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array(last_value, is_called)::text FROM memory_links_id_seq`))
		require.Equal(t, constraints, memoryLinksRecoverySnapshot(t, db, `SELECT COALESCE(jsonb_agg(jsonb_build_array(c.conrelid::regclass::text, c.conname, c.contype::text, pg_get_constraintdef(c.oid)) ORDER BY c.conrelid::regclass::text, c.conname), '[]'::jsonb)::text FROM pg_constraint c WHERE c.conrelid = 'memory_links'::regclass`))
		var indexes, receiptTables int
		require.NoError(t, db.Raw(`SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname IN ('memory_links_active_assertion','memory_links_source','memory_links_target')`).Scan(&indexes).Error)
		require.Equal(t, 3, indexes)
		require.NoError(t, db.Raw(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='memory_link_receipts'`).Scan(&receiptTables).Error)
		require.Equal(t, 1, receiptTables)
	})

	t.Run("missing_marker_preserves_complete_schema_and_data", func(t *testing.T) {
		db, _ := openMemoryContext191UpgradeDB(t)
		require.NoError(t, memoryLinksRecoveryRunner(db).Migrate())
		memoryLinksRecoverySeedLink(t, db)
		require.NoError(t, db.Exec(`INSERT INTO memory_link_receipts (principal_kind, principal, request_key, request_digest, link_id) VALUES ('agent','agent/recovery','retry-key','original-digest',7123)`).Error)
		rows := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryRows)
		receipts := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryReceipts)
		constraints := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryConstraints)
		indexes := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryIndexes)
		identities := memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array('memory_links'::regclass::oid, 'memory_link_receipts'::regclass::oid, 'memory_links_id_seq'::regclass::oid)::text`)
		seq := memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array(last_value, is_called)::text FROM memory_links_id_seq`)
		require.NoError(t, db.Exec(`DELETE FROM migrations WHERE id = ?`, memoryLinksMigration192().ID).Error)
		require.Zero(t, memoryLinksRecoveryMarker(t, db))
		require.NoError(t, memoryLinksRecoveryRunner(db).Migrate(), "restart after DDL and before marker must preserve retained data")
		require.EqualValues(t, 1, memoryLinksRecoveryMarker(t, db))
		require.Equal(t, rows, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryRows))
		require.Equal(t, receipts, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryReceipts))
		require.Equal(t, constraints, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryConstraints))
		require.Equal(t, indexes, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryIndexes))
		require.Equal(t, identities, memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array('memory_links'::regclass::oid, 'memory_link_receipts'::regclass::oid, 'memory_links_id_seq'::regclass::oid)::text`))
		require.Equal(t, seq, memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array(last_value, is_called)::text FROM memory_links_id_seq`))
	})

	t.Run("incompatible_object_fails_closed", func(t *testing.T) {
		db, _ := openMemoryContext191UpgradeDB(t)
		require.NoError(t, db.Exec(`CREATE TABLE memory_links (id BIGINT PRIMARY KEY, sentinel TEXT NOT NULL)`).Error)
		require.NoError(t, db.Exec(`INSERT INTO memory_links VALUES (941, 'incompatible object must survive')`).Error)
		rows := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryRows)
		constraints := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryConstraints)
		oid := memoryLinksRecoverySnapshot(t, db, `SELECT 'memory_links'::regclass::oid::text`)
		require.Error(t, memoryLinksRecoveryRunner(db).Migrate(), "incompatible objects must not be adopted")
		require.Zero(t, memoryLinksRecoveryMarker(t, db))
		require.Equal(t, rows, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryRows))
		require.Equal(t, constraints, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryConstraints))
		require.Equal(t, oid, memoryLinksRecoverySnapshot(t, db, `SELECT 'memory_links'::regclass::oid::text`))
		var absent bool
		require.NoError(t, db.Raw(`SELECT to_regclass('memory_link_receipts') IS NULL`).Scan(&absent).Error)
		require.True(t, absent, "failure must not create a receipt table")
	})
}
