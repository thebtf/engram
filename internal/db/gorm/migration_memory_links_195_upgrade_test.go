package gorm

import (
	"net"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Builds the real registered migration chain through 190, rather than a
// projection of selected tables. The database must be an isolated loopback fixture.
func TestMemoryLinks195Actual190Upgrade(t *testing.T) {
	dsn := os.Getenv("ENGRAM_SEARCH_PRIVACY_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_SEARCH_PRIVACY_TEST_DSN not set; explicitly select a disposable loopback PostgreSQL database")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, u.Scheme)
	ip := net.ParseIP(u.Hostname())
	require.NotNil(t, ip)
	require.True(t, ip.IsLoopback())
	require.NotEmpty(t, u.Port())
	require.NotEmpty(t, strings.Trim(u.Path, "/"))
	options, err := url.ParseQuery(u.RawQuery)
	require.NoError(t, err)
	for k := range options {
		require.Contains(t, []string{"sslmode", "connect_timeout", "application_name"}, strings.ToLower(k))
	}
	for _, key := range []string{"PGSERVICE", "PGSERVICEFILE", "PGOPTIONS"} {
		t.Setenv(key, "")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	require.Equal(t, u.Hostname(), config.Host)
	require.Equal(t, strings.TrimPrefix(u.Path, "/"), config.Database)
	for _, fallback := range config.Fallbacks {
		require.Equal(t, config.Host, fallback.Host)
		require.Equal(t, config.Port, fallback.Port)
	}
	schema := "memory_links_195_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	config.RuntimeParams = map[string]string{"application_name": scopedTestApplicationName("engram_links195_admin", schema), "search_path": "pg_catalog", "timezone": "UTC"}
	adminSQL := stdlib.OpenDB(*config)
	t.Cleanup(func() { require.NoError(t, adminSQL.Close()) })
	admin, err := gormlib.Open(postgres.New(postgres.Config{Conn: adminSQL}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	var safe bool
	require.NoError(t, admin.Raw(`SELECT to_regclass('public.memories') IS NULL AND to_regclass('public.migrations') IS NULL AND EXISTS (SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector' AND n.nspname='public')`).Scan(&safe).Error)
	require.True(t, safe, "fixture requires empty public application schema and existing public vector")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Logf("owned schema: %s", schema)
	t.Cleanup(func() { require.NoError(t, admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error) })
	config.RuntimeParams["application_name"] = scopedTestApplicationName("engram_links195_test", schema)
	config.RuntimeParams["search_path"] = schema + ", public"
	open := func() (*gormlib.DB, func()) {
		sqlDB := stdlib.OpenDB(*config)
		db, openErr := gormlib.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, openErr)
		return db, func() { require.NoError(t, sqlDB.Close()) }
	}
	db, closeDB := open()
	defer func() { closeDB() }()
	var current string
	require.NoError(t, db.Raw(`SELECT current_schema()`).Scan(&current).Error)
	require.Equal(t, schema, current)
	require.NoError(t, runMigrationsTo(db, "190_browser_session_public_handle"))
	ledger := memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_agg(id ORDER BY id)::text FROM migrations`)
	require.Contains(t, ledger, "190_browser_session_public_handle")
	require.NotContains(t, ledger, "191_memory_owning_context")
	require.NotContains(t, ledger, "195_memory_links")
	require.NoError(t, db.Exec(`INSERT INTO memories (id, project, content) VALUES (19101, '', 'actual-190-first'), (19102, '', 'actual-190-second')`).Error)
	before := memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_agg(to_jsonb(m) ORDER BY id)::text FROM memories m WHERE id IN (19101,19102)`)
	require.NoError(t, runMigrations(db))
	require.Equal(t, before, memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_agg(to_jsonb(m) - 'context_kind' - 'context_principal' - 'context_principal_kind' ORDER BY id)::text FROM memories m WHERE id IN (19101,19102)`))
	memoryLinksRecoverySeedLink(t, db)
	require.NoError(t, db.Exec(`INSERT INTO memory_link_receipts (principal_kind, principal, request_key, request_digest, link_id) VALUES ('agent','agent/recovery','retry-key','original-digest',7123)`).Error)
	rows := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryRows)
	receipts := memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryReceipts)
	identities := memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array('memory_links'::regclass::oid, 'memory_link_receipts'::regclass::oid, 'memory_links_id_seq'::regclass::oid)::text`)
	after := memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_agg(id ORDER BY id)::text FROM migrations`)
	require.Contains(t, after, "191_memory_owning_context")
	require.Contains(t, after, "195_memory_links")
	closeDB()
	db, closeDB = open()
	require.NoError(t, runMigrations(db))
	require.Equal(t, after, memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_agg(id ORDER BY id)::text FROM migrations`))
	require.Equal(t, rows, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryRows))
	require.Equal(t, receipts, memoryLinksRecoverySnapshot(t, db, memoryLinksRecoveryReceipts))
	require.Equal(t, identities, memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_build_array('memory_links'::regclass::oid, 'memory_link_receipts'::regclass::oid, 'memory_links_id_seq'::regclass::oid)::text`))
	require.Equal(t, before, memoryLinksRecoverySnapshot(t, db, `SELECT jsonb_agg(to_jsonb(m) - 'context_kind' - 'context_principal' - 'context_principal_kind' ORDER BY id)::text FROM memories m WHERE id IN (19101,19102)`))
}
