package gorm

import (
	"net"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const memoryContext191Project = "19100000-0000-4000-8000-000000000001"

// This fixture is a pre-191 projection of the historical users/memories DDL
// (080, 088, 105, 110, 125, 130), plus the actual migration 149. It deliberately
// does not use today's Memory model, run the full startup chain, rewind 191, or
// claim to reproduce every unrelated table/migration through 190.
// Like the privacy fixtures, it requires an explicitly selected disposable
// loopback database with no public application tables. Unlike the full-chain
// fixture, it needs no extensions and never executes DDL in public.
func openMemoryContext191UpgradeDB(t *testing.T) (*gormlib.DB, string) {
	t.Helper()
	dsn := os.Getenv("ENGRAM_SEARCH_PRIVACY_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_SEARCH_PRIVACY_TEST_DSN not set; explicitly select a disposable loopback PostgreSQL database")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed == nil {
		t.Fatal("fixture DSN must be a PostgreSQL URL; value and parse error withheld")
	}
	require.Contains(t, []string{"postgres", "postgresql"}, parsed.Scheme)
	ip := net.ParseIP(parsed.Hostname())
	require.NotNil(t, ip, "fixture target must use an explicit loopback IP")
	require.True(t, ip.IsLoopback(), "fixture target must stay on loopback")
	require.NotEmpty(t, parsed.Port(), "fixture must explicitly select its disposable port")
	require.NotEmpty(t, strings.Trim(parsed.Path, "/"), "fixture must explicitly select its disposable database")
	options, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		t.Fatal("fixture DSN options are invalid; values withheld")
	}
	for key := range options {
		switch strings.ToLower(key) {
		case "sslmode", "connect_timeout", "application_name":
		default:
			t.Fatal("fixture DSN must not override its explicit target or schema")
		}
	}
	for _, key := range []string{"PGSERVICE", "PGSERVICEFILE", "PGOPTIONS"} {
		t.Setenv(key, "")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("fixture driver configuration is invalid; value and parse error withheld")
	}
	require.Equal(t, parsed.Hostname(), config.Host)
	require.Equal(t, strings.TrimPrefix(parsed.Path, "/"), config.Database)
	for _, fallback := range config.Fallbacks {
		require.Equal(t, config.Host, fallback.Host, "TLS fallback must not change the target")
		require.Equal(t, config.Port, fallback.Port)
	}
	schema := "memory_context_191_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	config.RuntimeParams = map[string]string{
		"application_name":  scopedTestApplicationName("engram_memory_context_191_admin", schema),
		"search_path":       "pg_catalog",
		"timezone":          "UTC",
		"statement_timeout": "15000",
		"lock_timeout":      "5000",
	}
	adminSQL := stdlib.OpenDB(*config)
	adminSQL.SetMaxOpenConns(1)
	adminSQL.SetMaxIdleConns(1)
	t.Cleanup(func() { require.NoError(t, adminSQL.Close()) })
	admin, err := gormlib.Open(postgres.New(postgres.Config{Conn: adminSQL}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	var publicUnused bool
	require.NoError(t, admin.Raw(`SELECT to_regclass('public.memories') IS NULL
		AND to_regclass('public.users') IS NULL AND to_regclass('public.migrations') IS NULL
		AND to_regclass('public.memory_principals') IS NULL
		AND to_regclass('public.memory_request_keys') IS NULL`).Scan(&publicUnused).Error)
	require.True(t, publicUnused, "refusing a populated public application schema; do not use a mirror or production target")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error, "never replace an existing schema")
	t.Cleanup(func() { require.NoError(t, admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error) })

	fixtureConfig := config.Copy()
	fixtureConfig.RuntimeParams["application_name"] = scopedTestApplicationName("engram_memory_context_191_test", schema)
	// No public fallback: an absent fixture dependency must fail closed.
	fixtureConfig.RuntimeParams["search_path"] = schema
	fixtureSQL := stdlib.OpenDB(*fixtureConfig)
	fixtureSQL.SetMaxOpenConns(2)
	fixtureSQL.SetMaxIdleConns(1)
	// LIFO cleanup closes the fixture before dropping only its owned schema.
	t.Cleanup(func() { require.NoError(t, fixtureSQL.Close()) })
	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: fixtureSQL}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	var actualSchema, searchPath string
	require.NoError(t, db.Raw(`SELECT current_schema(), current_setting('search_path')`).Row().Scan(&actualSchema, &searchPath))
	require.Equal(t, schema, actualSchema)
	require.Equal(t, schema, searchPath)

	for _, statement := range []string{
		`CREATE TABLE users (
			id SERIAL PRIMARY KEY, email VARCHAR(255) NOT NULL UNIQUE,
			password_hash VARCHAR(255) NOT NULL DEFAULT '',
			role VARCHAR(20) NOT NULL DEFAULT 'operator', disabled BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMP NOT NULL DEFAULT NOW(), last_login_at TIMESTAMP
		)`,
		`CREATE TABLE memories (
			id BIGSERIAL PRIMARY KEY, project TEXT NOT NULL, content TEXT NOT NULL,
			tags JSONB NOT NULL DEFAULT '[]', source_agent TEXT, version INTEGER NOT NULL DEFAULT 1,
			edited_by TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), deleted_at TIMESTAMPTZ,
			search_vector tsvector GENERATED ALWAYS AS (
				to_tsvector('english', COALESCE(content, '')) ||
				to_tsvector('simple', COALESCE(content, ''))
			) STORED,
			importance_base REAL DEFAULT 0.5, ts_alpha REAL DEFAULT 1.0, ts_beta REAL DEFAULT 1.0,
			citation_count INTEGER DEFAULT 0, injection_count INTEGER DEFAULT 0,
			status TEXT DEFAULT 'active', supersedes_id BIGINT REFERENCES memories(id),
			access_count INTEGER DEFAULT 0, last_retrieved_at TIMESTAMPTZ,
			valid_from TIMESTAMPTZ DEFAULT now(), valid_until TIMESTAMPTZ DEFAULT 'infinity',
			tier TEXT DEFAULT 'semantic', epistemic_type TEXT DEFAULT 'observation',
			confidence REAL DEFAULT 0.5, stability REAL DEFAULT 30.0, retrievability REAL DEFAULT 1.0,
			recurrence_count INT DEFAULT 0, last_confirmed TIMESTAMPTZ, review_after TIMESTAMPTZ,
			defeasibility TEXT DEFAULT 'slow', superseded_by BIGINT, promotion_target TEXT DEFAULT 'none',
			privacy_scope TEXT NOT NULL DEFAULT 'project', source_sessions TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
			source_workstation_id TEXT NOT NULL DEFAULT '',
			CONSTRAINT chk_memories_status CHECK (status IN ('active','superseded','flagged','archived')),
			CONSTRAINT memories_privacy_scope_chk CHECK (privacy_scope IN ('private','project','shared','global'))
		)`,
		`CREATE INDEX idx_memories_project_created ON memories (project, created_at DESC) WHERE deleted_at IS NULL`,
		`CREATE INDEX idx_memories_fts ON memories USING GIN (search_vector)`,
		`CREATE INDEX idx_memories_tags ON memories USING GIN (tags)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	require.NoError(t, gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{memoryPrincipalsMigration149()}).Migrate())
	require.NoError(t, db.Exec(`INSERT INTO users (id, email, role, created_at)
		VALUES (17, 'migration-191@example.invalid', 'admin', '2025-01-02 03:04:05')`).Error)
	// Owner/author metadata already exists, including on the historical blank
	// project row. It is evidence, not permission to infer an owning self context.
	require.NoError(t, db.Exec(`INSERT INTO memories (
		id, project, content, tags, source_agent, edited_by, version, created_at, updated_at,
		deleted_at, status, privacy_scope, source_workstation_id, source_sessions,
		owner_principal, owner_principal_kind, agent_visibility, domain,
		valid_from, valid_until, last_retrieved_at, last_confirmed, review_after,
		citation_count, injection_count, access_count, recurrence_count, tier, epistemic_type,
		confidence, importance_base, ts_alpha, ts_beta, stability, retrievability, defeasibility, promotion_target
	) VALUES
		(19101, ?, E'project sentinel: retain café / память\nversion seven',
		 '["keep:project","scope:project","unicode:память"]'::jsonb,
		 'legacy-import/project', 'human/editor-project', 7,
		 '2025-01-02 03:04:05.123456+00', '2025-02-03 04:05:06.654321+00',
		 NULL, 'active', 'project', 'workstation-project', ARRAY['session-project-a','session-project-b'],
		 'agent/existing-project-author', 'agent', 'shared', 'migration/project-domain',
		 '2024-12-01 00:00:00+00', 'infinity', '2025-02-04 00:00:00+00', '2025-02-05 00:00:00+00', '2026-12-31 00:00:00+00',
		 13, 21, 34, 5, 'semantic', 'decision', 0.75, 0.625, 3.25, 1.5, 48.5, 0.875, 'slow', 'none'),
		(19102, '', E'blank-project sentinel: not self, not global\nprivate archived history',
		 '["keep:blank","scope:global","punctuation:[]{}"]'::jsonb,
		 'legacy-import/blank', 'human/editor-blank', 11,
		 '2023-03-04 05:06:07.123456+00', '2024-04-05 06:07:08.654321+00',
		 '2024-05-06 07:08:09+00', 'archived', 'private', 'workstation-blank', ARRAY['session-blank-b','session-blank-a'],
		 'agent/existing-blank-author', 'agent', 'private', 'migration/blank-domain',
		 '2023-03-01 00:00:00+00', '2024-05-06 00:00:00+00', NULL, '2024-04-06 00:00:00+00', NULL,
		 8, 9, 10, 4, 'episodic', 'observation', 0.25, 0.375, 2.5, 4.25, 12.5, 0.125, 'fast', 'none')`, memoryContext191Project).Error)
	require.NoError(t, db.Exec(`SELECT setval(pg_get_serial_sequence('memories', 'id'), 19102, true)`).Error)
	var contextColumns int
	require.NoError(t, db.Raw(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'memories'
		AND column_name IN ('context_kind','context_principal','context_principal_kind')`).Scan(&contextColumns).Error)
	require.Zero(t, contextColumns, "the fixture must really precede 191")
	require.Equal(t, "[\"149_memory_principals\"]", memoryContext191Snapshot(t, db, `SELECT jsonb_agg(id ORDER BY id)::text FROM migrations`))
	return db, schema
}

func memoryContext191Snapshot(t *testing.T, db *gormlib.DB, query string) string {
	t.Helper()
	var value string
	require.NoError(t, db.Raw(query).Row().Scan(&value))
	return value
}

const memoryContext191LegacyRowsSQL = `SELECT jsonb_agg(
	to_jsonb(m) - 'context_kind' - 'context_principal' - 'context_principal_kind' ORDER BY id)::text
	FROM memories m WHERE id IN (19101,19102)`

const memoryContext191SchemaSQL = `SELECT jsonb_build_object(
	'relations', (SELECT jsonb_agg(jsonb_build_array(c.oid, c.relname, c.relkind) ORDER BY c.relname)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema()),
	'columns', (SELECT jsonb_agg(jsonb_build_array(c.relname, a.attnum, a.attname, a.atttypid,
		a.attnotnull, pg_get_expr(d.adbin,d.adrelid)) ORDER BY c.relname,a.attnum)
		FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE n.nspname = current_schema() AND a.attnum > 0 AND NOT a.attisdropped),
	'constraints', (SELECT jsonb_agg(jsonb_build_array(k.oid, k.conname, k.convalidated,
		pg_get_constraintdef(k.oid)) ORDER BY k.oid)
		FROM pg_constraint k JOIN pg_namespace n ON n.oid = k.connamespace WHERE n.nspname = current_schema()),
	'indexes', (SELECT jsonb_agg(jsonb_build_array(indexname,indexdef) ORDER BY indexname)
		FROM pg_indexes WHERE schemaname = current_schema())
	)::text`

const memoryContext191DurableSQL = `SELECT jsonb_build_object(
	'memories', (SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM memories m),
	'users', (SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),
	'principals', (SELECT jsonb_agg(to_jsonb(p) ORDER BY principal_kind,principal) FROM memory_principals p),
	'requests', (SELECT jsonb_agg(to_jsonb(r) ORDER BY principal_kind,principal,context_kind,project,request_key) FROM memory_request_keys r),
	'revisions', (SELECT jsonb_agg(to_jsonb(r) ORDER BY memory_id,version) FROM memory_revisions r),
	'migrations', (SELECT jsonb_agg(id ORDER BY id) FROM migrations),
	'sequence', (SELECT jsonb_build_object('last_value', last_value, 'is_called', is_called) FROM memories_id_seq)
	)::text`

func TestMemoryContext191PostgresUpgradePreservesLegacyAndRollbackEvidence(t *testing.T) {
	db, schema := openMemoryContext191UpgradeDB(t)
	legacyBefore := memoryContext191Snapshot(t, db, memoryContext191LegacyRowsSQL)
	usersBefore := memoryContext191Snapshot(t, db, `SELECT jsonb_agg(to_jsonb(u) ORDER BY id)::text FROM users u`)
	sequenceBefore := memoryContext191Snapshot(t, db, `SELECT jsonb_build_object('last_value', last_value, 'is_called', is_called)::text FROM memories_id_seq`)
	migration := memoryContextMigration191()
	runner := gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{memoryPrincipalsMigration149(), migration})
	require.NoError(t, runner.Migrate(), "apply actual 191 to already-existing rows")
	require.Equal(t, legacyBefore, memoryContext191Snapshot(t, db, memoryContext191LegacyRowsSQL), "every prior value, including generated FTS, IDs, tags, provenance, privacy and lifecycle, must survive")
	require.Equal(t, usersBefore, memoryContext191Snapshot(t, db, `SELECT jsonb_agg(to_jsonb(u) ORDER BY id)::text FROM users u`))
	require.Equal(t, sequenceBefore, memoryContext191Snapshot(t, db, `SELECT jsonb_build_object('last_value', last_value, 'is_called', is_called)::text FROM memories_id_seq`), "migration must not consume or reset memory IDs")
	var legacyCount, principalCount, requestCount int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM memories WHERE context_kind = 'legacy'
		AND context_principal = '' AND context_principal_kind = ''`).Scan(&legacyCount).Error)
	require.EqualValues(t, 2, legacyCount, "neither canonical project nor blank project implies a new typed context")
	require.NoError(t, db.Table("memory_principals").Count(&principalCount).Error)
	require.NoError(t, db.Table("memory_request_keys").Count(&requestCount).Error)
	require.Zero(t, principalCount, "historical author/owner metadata must not auto-enroll principals")
	require.Zero(t, requestCount, "migration must not invent retry receipts")
	var revisionCount int64
	require.NoError(t, db.Table("memory_revisions").Count(&revisionCount).Error)
	require.Zero(t, revisionCount, "migration must not invent prior history for legacy records")

	// Deliberate enrollment uses the actual user FK, followed by all three typed
	// contexts. Direct SQL checks persistence/DDL only, not browser authorization.
	require.NoError(t, db.Exec(`INSERT INTO memory_principals (principal_kind, principal, enrolled_by, created_at)
		VALUES ('agent', 'agent/deliberately-enrolled', 17, '2025-06-01 00:00:00+00')`).Error)
	for _, tc := range []struct{ kind, project string }{
		{"self", ""}, {"global", ""}, {"project", memoryContext191Project},
	} {
		principalKind, principal := "", ""
		if tc.kind == "self" {
			principalKind, principal = "agent", "agent/deliberately-enrolled"
		}
		var id int64
		require.NoError(t, db.Raw(`INSERT INTO memories
			(project, content, tags, owner_principal_kind, owner_principal, agent_visibility,
			context_kind, context_principal_kind, context_principal)
			VALUES (?, ?, '["typed:191"]'::jsonb, 'agent', 'agent/deliberately-enrolled', 'private', ?, ?, ?)
			RETURNING id`, tc.project, "typed sentinel "+tc.kind, tc.kind, principalKind, principal).Row().Scan(&id))
		require.Greater(t, id, int64(19102), "existing IDs must not be reused")
		require.NoError(t, db.Exec(`INSERT INTO memory_request_keys
			(principal_kind, principal, context_kind, project, request_key, request_digest, memory_id, created_at)
			VALUES ('agent', 'agent/deliberately-enrolled', ?, ?, ?, ?, ?, '2025-06-02 00:00:00+00')`,
			tc.kind, tc.project, "retry-191-"+tc.kind, strings.Repeat("a", 64), id).Error)
	}
	require.NoError(t, db.Raw(`SELECT count(*) FROM memories`).Scan(&legacyCount).Error)
	require.EqualValues(t, 5, legacyCount)
	require.NoError(t, db.Table("memory_request_keys").Count(&requestCount).Error)
	require.EqualValues(t, 3, requestCount)
	require.NoError(t, db.Exec(`INSERT INTO memory_revisions (memory_id, version, content, changed_by_principal, changed_by_principal_kind) SELECT id, 1, 'retained synthetic version', 'agent/deliberately-enrolled', 'agent' FROM memories WHERE context_kind = 'self'`).Error)
	beforeReplay := memoryContext191Snapshot(t, db, memoryContext191DurableSQL)
	schemaBeforeReplay := memoryContext191Snapshot(t, db, memoryContext191SchemaSQL)
	require.NoError(t, runner.Migrate(), "already-recorded startup replay must be a no-op")
	require.NoError(t, migration.Migrate(db), "DDL itself must be idempotent with useful typed rows and receipts")
	require.Equal(t, beforeReplay, memoryContext191Snapshot(t, db, memoryContext191DurableSQL))
	require.Equal(t, schemaBeforeReplay, memoryContext191Snapshot(t, db, memoryContext191SchemaSQL), "replay must not replace constraints, indexes or relations")
	var applied191 int64
	require.NoError(t, db.Table("migrations").Where("id = ?", migration.ID).Count(&applied191).Error)
	require.EqualValues(t, 1, applied191)

	// Exercise the real constraint, not an incidental missing dependency. UPDATE
	// avoids changing a sequence even when PostgreSQL rejects the write.
	err := db.Exec(`UPDATE memories SET context_kind = 'self' WHERE id = 19102`).Error
	var pgError *pgconn.PgError
	require.ErrorAs(t, err, &pgError)
	require.Equal(t, "23514", pgError.Code)
	require.Equal(t, schema, pgError.SchemaName)
	require.Equal(t, "memories_owning_context_check", pgError.ConstraintName)
	require.ErrorContains(t, runner.RollbackLast(), "rollback refused", "ordinary migration rollback must refuse before removing its ledger entry")
	require.ErrorContains(t, migration.Rollback(db), "rollback refused")
	require.Equal(t, beforeReplay, memoryContext191Snapshot(t, db, memoryContext191DurableSQL), "refusal must preserve all five memories, enrollment, three retry receipts, users, sequence and migration history")
	require.Equal(t, schemaBeforeReplay, memoryContext191Snapshot(t, db, memoryContext191SchemaSQL), "refusal must preserve the complete owned schema")
	require.Equal(t, legacyBefore, memoryContext191Snapshot(t, db, memoryContext191LegacyRowsSQL))
}

func TestMemoryContext191PostgresFailureRollsBackDDLAndCanRetry(t *testing.T) {
	db, _ := openMemoryContext191UpgradeDB(t)
	migration := memoryContextMigration191()
	runner := gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{memoryPrincipalsMigration149(), migration})
	legacyBefore := memoryContext191Snapshot(t, db, memoryContext191LegacyRowsSQL)
	ledgerBefore := memoryContext191Snapshot(t, db, `SELECT jsonb_agg(id ORDER BY id)::text FROM migrations`)
	sequenceBefore := memoryContext191Snapshot(t, db, `SELECT jsonb_build_object('last_value', last_value, 'is_called', is_called)::text FROM memories_id_seq`)
	// Hide only this owned schema's existing FK dependency. The first three
	// ALTERs succeed before CREATE memory_principals fails; search_path excludes
	// public, so it cannot accidentally bind to a different users table.
	require.NoError(t, db.Exec(`ALTER TABLE users RENAME TO users_unavailable`).Error)
	usersBefore := memoryContext191Snapshot(t, db, `SELECT jsonb_agg(to_jsonb(u) ORDER BY id)::text FROM users_unavailable u`)
	schemaBefore := memoryContext191Snapshot(t, db, memoryContext191SchemaSQL)
	var pgError *pgconn.PgError
	require.ErrorAs(t, runner.Migrate(), &pgError)
	require.Equal(t, "42P01", pgError.Code, "fail on the deliberately unavailable users dependency")
	require.Contains(t, pgError.Message, "users")
	require.Equal(t, schemaBefore, memoryContext191Snapshot(t, db, memoryContext191SchemaSQL), "failed 191 must undo added columns and leave no partial enrollment, ledger, index or CHECK")
	require.Equal(t, legacyBefore, memoryContext191Snapshot(t, db, memoryContext191LegacyRowsSQL))
	require.Equal(t, usersBefore, memoryContext191Snapshot(t, db, `SELECT jsonb_agg(to_jsonb(u) ORDER BY id)::text FROM users_unavailable u`))
	require.Equal(t, ledgerBefore, memoryContext191Snapshot(t, db, `SELECT jsonb_agg(id ORDER BY id)::text FROM migrations`), "failed 191 must not be marked applied")
	require.Equal(t, sequenceBefore, memoryContext191Snapshot(t, db, `SELECT jsonb_build_object('last_value', last_value, 'is_called', is_called)::text FROM memories_id_seq`))
	require.NoError(t, db.Exec(`ALTER TABLE users_unavailable RENAME TO users`).Error)
	require.NoError(t, runner.Migrate(), "restoring the exact dependency must allow a clean retry")
	require.Equal(t, legacyBefore, memoryContext191Snapshot(t, db, memoryContext191LegacyRowsSQL))
	require.Equal(t, usersBefore, memoryContext191Snapshot(t, db, `SELECT jsonb_agg(to_jsonb(u) ORDER BY id)::text FROM users u`))
	var legacyCount, applied191 int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM memories WHERE context_kind = 'legacy'
		AND context_principal = '' AND context_principal_kind = ''`).Scan(&legacyCount).Error)
	require.EqualValues(t, 2, legacyCount)
	require.NoError(t, db.Table("migrations").Where("id = ?", migration.ID).Count(&applied191).Error)
	require.EqualValues(t, 1, applied191)
	require.Equal(t, "0", memoryContext191Snapshot(t, db, `SELECT count(*)::text FROM memory_principals`))
	require.Equal(t, "0", memoryContext191Snapshot(t, db, `SELECT count(*)::text FROM memory_request_keys`))
}
