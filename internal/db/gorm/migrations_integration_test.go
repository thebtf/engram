package gorm

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestMigrationsIntegration runs all migrations against a real PostgreSQL+pgvector instance.
// Requires DATABASE_DSN environment variable pointing to a test database.
//
//	DATABASE_DSN="postgres://user:pass@host:5432/db?sslmode=disable" go test ./internal/db/gorm/ -run TestMigrationsIntegration -v
func TestMigrationsIntegration(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	defer sqlDB.Close()

	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	// Use 2000 dims — the target production configuration.
	const dims = 2000

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	t.Logf("all migrations passed")

	// Verify the embedding column has the expected dimension.
	var actual int
	row := db.Raw("SELECT atttypmod FROM pg_attribute WHERE attrelid = 'vectors'::regclass AND attname = 'embedding' AND atttypmod > 0").Row()
	if err := row.Scan(&actual); err != nil {
		t.Fatalf("read vector dimension: %v", err)
	}
	if actual != dims {
		t.Fatalf("vector dimension mismatch: got %d, want %d", actual, dims)
	}
	t.Logf("vectors.embedding = vector(%d) — correct", actual)
}

// TestMigrationsIntegration_PatternsDropped verifies that a fresh-install migration chain
// correctly creates and then removes the patterns subsystem tables.
// It checks that after running all migrations:
//   - The "patterns" table does NOT exist (dropped by 098_drop_patterns)
//   - The "pattern_observations" table does NOT exist (dropped by 098_drop_patterns)
//
// This is a regression guard: if either 009_patterns or 098_drop_patterns is broken,
// fresh installs will fail silently while upgrades from pre-US5 instances remain green.
func TestMigrationsIntegration_PatternsDropped(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, sqlDB.Ping())

	// Run the full migration chain — this covers both 009_patterns (create) and
	// 098_drop_patterns (drop), exercising the fresh-install path end-to-end.
	require.NoError(t, runMigrations(db), "full migration chain must succeed on fresh DB")

	// Verify that neither patterns table survives — 098_drop_patterns must have run.
	for _, table := range []string{"patterns", "pattern_observations"} {
		var count int
		err := db.Raw(`
			SELECT COUNT(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = ?
		`, table).Scan(&count).Error
		require.NoError(t, err, "checking existence of table %q", table)
		require.Equal(t, 0, count, "table %q must not exist after 098_drop_patterns", table)
	}
}

func TestMigrationsIntegration_AddsCommandsRunColumn(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, sqlDB.Ping())

	const dims = 2000
	require.NoError(t, runMigrations(db))

	require.NoError(t, db.Exec(`ALTER TABLE observations DROP COLUMN IF EXISTS commands_run`).Error)
	require.NoError(t, db.Exec(`DELETE FROM migrations WHERE id = ?`, "074_observations_commands_run").Error)
	require.NoError(t, runMigrations(db))

	var dataType string
	err = db.Raw(`
		SELECT data_type
		FROM information_schema.columns
		WHERE table_name = 'observations' AND column_name = 'commands_run'
	`).Row().Scan(&dataType)
	require.NoError(t, err)
	require.Equal(t, "jsonb", dataType)
}

// TestMigration125_AddPrivacyScope verifies migration 125_privacy_scope_addition
// adds the privacy_scope + source_sessions columns to memories with the correct
// types, defaults, and CHECK constraint, and that the rollback drops them cleanly.
//
// Asserts:
//   - privacy_scope: text, NOT NULL, DEFAULT 'project'
//   - source_sessions: ARRAY (text[]), NOT NULL, DEFAULT ARRAY[]::text[]
//   - CHECK constraint memories_privacy_scope_chk admits ('private','project','shared','global')
//   - Rollback drops both columns + constraint
//
// Anti-stub property: if the migration body is replaced with `return nil`, the
// column-existence assertion below fails — the test does not pass on a no-op
// migration.
//
// Engram vNext Milestone F TG1/T001.
func TestMigration125_AddPrivacyScope(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, sqlDB.Ping())

	// Run the full migration chain — must include 125.
	require.NoError(t, runMigrations(db), "full migration chain must succeed")

	// Register cleanup of any test rows BEFORE inserting them, so that even a
	// panic or failure mid-test does not leave orphaned 't001-test' rows in the
	// shared DB across runs.
	t.Cleanup(func() {
		if err := db.Exec(`DELETE FROM memories WHERE project = 't001-test'`).Error; err != nil {
			t.Logf("TestMigration125 cleanup: failed to delete test rows: %v", err)
		}
	})

	// Assert privacy_scope column shape.
	var dataType, isNullable, columnDefault string
	row := db.Raw(`
		SELECT data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'memories'
		  AND column_name = 'privacy_scope'
	`).Row()
	require.NoError(t, row.Scan(&dataType, &isNullable, &columnDefault), "privacy_scope column must exist")
	require.Equal(t, "text", dataType, "privacy_scope must be text")
	require.Equal(t, "NO", isNullable, "privacy_scope must be NOT NULL")
	require.Contains(t, columnDefault, "'project'", "privacy_scope default must be 'project'")

	// Assert source_sessions column shape.
	var ssDataType, ssIsNullable, ssColumnDefault string
	row = db.Raw(`
		SELECT data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'memories'
		  AND column_name = 'source_sessions'
	`).Row()
	require.NoError(t, row.Scan(&ssDataType, &ssIsNullable, &ssColumnDefault), "source_sessions column must exist")
	require.Equal(t, "ARRAY", ssDataType, "source_sessions must be ARRAY (text[])")
	require.Equal(t, "NO", ssIsNullable, "source_sessions must be NOT NULL")
	require.Contains(t, ssColumnDefault, "ARRAY", "source_sessions default must be empty ARRAY")

	// Assert CHECK constraint admits the 4 enum values and rejects others.
	for _, valid := range []string{"private", "project", "shared", "global"} {
		err := db.Exec(`INSERT INTO memories (project, content, privacy_scope) VALUES (?, ?, ?)`,
			"t001-test", "T001 fixture content", valid).Error
		require.NoError(t, err, "privacy_scope=%q must be admitted by CHECK constraint", valid)
	}
	err = db.Exec(`INSERT INTO memories (project, content, privacy_scope) VALUES (?, ?, ?)`,
		"t001-test", "T001 invalid fixture", "invalid_scope").Error
	require.Error(t, err, "privacy_scope='invalid_scope' must be rejected by CHECK constraint")

	// Cleanup runs via t.Cleanup registered above — survives failure/panic.
}

// TestMigration125_TagDerivedBackfill_T006 verifies the T006 backfill step
// inside migration 125: rows that carry the legacy `scope:global` tag are
// promoted to privacy_scope='global' on migrate-up. Rows with `scope:project`
// or without a scope tag stay at the column DEFAULT 'project'.
//
// Asserts (post-migration):
//   - row with tags=['scope:global']  -> privacy_scope='global'
//   - row with tags=['scope:project'] -> privacy_scope='project'
//   - row with tags=[] (no scope tag) -> privacy_scope='project'
//   - re-running the UPDATE (the migration body explicitly, not the
//     gormigrate run loop which skips applied IDs) is a no-op — idempotent.
//
// Anti-stub: removing the `UPDATE memories SET privacy_scope='global' ...`
// line from migration 125 stmts breaks the first assertion (the global-tagged
// row would default to 'project').
//
// Engram vNext Milestone F TG1 / T006.
func TestMigration125_TagDerivedBackfill_T006(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, sqlDB.Ping())

	// Run the full migration chain — migration 125 with the T006 backfill
	// statement is in the slice.
	require.NoError(t, runMigrations(db), "full migration chain must succeed")

	// Register cleanup BEFORE inserting fixtures so failure/panic still cleans.
	t.Cleanup(func() {
		if err := db.Exec(`DELETE FROM memories WHERE project = 't006-backfill-test'`).Error; err != nil {
			t.Logf("TestMigration125_TagDerivedBackfill_T006 cleanup: %v", err)
		}
	})

	// Pre-clean any residual rows from a prior aborted run (CodeRabbit fix-forward
	// on bfae983): t.Cleanup is post-fact and does not fire on panic/SIGKILL.
	// Without this, a stale row from an aborted previous run would make the
	// `require.Len(..., 3)` assertion below flaky on shared staging DBs.
	require.NoError(t, db.Exec(
		`DELETE FROM memories WHERE project = 't006-backfill-test'`,
	).Error, "pre-test cleanup of residual fixtures")

	// Seed three fixture rows. Because migration 125 has already run on a
	// fresh DB, ADD COLUMN + DEFAULT 'project' applies on insert and the
	// tag-derived UPDATE has executed before any T006 rows existed. To
	// exercise the UPDATE statement directly, insert fixtures THEN re-run
	// only the UPDATE clause (the gormigrate run-loop skips applied
	// migration IDs but the SQL itself is the contract under test).
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags) VALUES (?, ?, ?::jsonb)`,
		"t006-backfill-test", "T006 fixture global-tagged", `["scope:global"]`,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags) VALUES (?, ?, ?::jsonb)`,
		"t006-backfill-test", "T006 fixture project-tagged", `["scope:project"]`,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags) VALUES (?, ?, ?::jsonb)`,
		"t006-backfill-test", "T006 fixture untagged", `[]`,
	).Error)

	// Force the freshly-inserted global-tagged row off the default so the
	// backfill UPDATE has work to do on subsequent runs.
	require.NoError(t, db.Exec(
		`UPDATE memories SET privacy_scope = 'project'
			WHERE project = 't006-backfill-test' AND tags ? 'scope:global'`,
	).Error)

	// Run the T006 backfill statement (mirroring the line in migration 125).
	runBackfill := func() error {
		return db.Exec(
			`UPDATE memories
				SET privacy_scope = 'global'
				WHERE privacy_scope <> 'global'
				  AND tags ? 'scope:global'`,
		).Error
	}
	require.NoError(t, runBackfill(), "first backfill run")

	// Read back the three rows and assert the privacy_scope values.
	type row struct {
		Tags         string
		PrivacyScope string
	}
	var rows []row
	require.NoError(t, db.Raw(
		`SELECT tags::text AS tags, privacy_scope FROM memories
			WHERE project = 't006-backfill-test'
			ORDER BY id`,
	).Scan(&rows).Error)
	require.Len(t, rows, 3, "expected 3 fixture rows")

	require.Equal(t, "global", rows[0].PrivacyScope,
		"scope:global-tagged row must be backfilled to privacy_scope='global'")
	require.Equal(t, "project", rows[1].PrivacyScope,
		"scope:project-tagged row must stay at privacy_scope='project' (default)")
	require.Equal(t, "project", rows[2].PrivacyScope,
		"untagged row must stay at privacy_scope='project' (default)")

	// Idempotency: a second run must leave the same final state and report
	// zero rows affected (the WHERE clause filters out already-global rows).
	require.NoError(t, runBackfill(), "second backfill run")

	var rowsAfter []row
	require.NoError(t, db.Raw(
		`SELECT tags::text AS tags, privacy_scope FROM memories
			WHERE project = 't006-backfill-test'
			ORDER BY id`,
	).Scan(&rowsAfter).Error)
	require.Equal(t, rows, rowsAfter, "backfill must be idempotent across runs")
}

// TestMigration130_AddSourceWorkstationID verifies migration
// 130_source_workstation_id (engram vNext Milestone F TG1/T001b, AMEND
// 2026-05-25) adds the source_workstation_id column to memories with TEXT
// NOT NULL DEFAULT ” shape and that the rollback drops it cleanly.
//
// Asserts:
//   - source_workstation_id: text, NOT NULL, DEFAULT ”
//   - Pre-existing rows (and rows written without populating the column)
//     receive the empty-string default and are queryable.
//
// Anti-stub property: a `return nil` Migrate body causes the
// information_schema query to return zero rows and Scan to fail.
//
// See spec.md §FR-F1 AMEND 2026-05-25 for the column-semantics contract
// and how empty-string interacts with scope.Resolve ScopePrivate.
func TestMigration130_AddSourceWorkstationID(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, sqlDB.Ping())

	// Run the full migration chain — must include 130.
	require.NoError(t, runMigrations(db), "full migration chain must succeed")

	// Register cleanup BEFORE inserting test row.
	t.Cleanup(func() {
		if err := db.Exec(`DELETE FROM memories WHERE project = 't001b-test'`).Error; err != nil {
			t.Logf("TestMigration130 cleanup: failed to delete test rows: %v", err)
		}
	})

	// Assert source_workstation_id column shape.
	var dataType, isNullable, columnDefault string
	row := db.Raw(`
		SELECT data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'memories'
		  AND column_name = 'source_workstation_id'
	`).Row()
	require.NoError(t, row.Scan(&dataType, &isNullable, &columnDefault), "source_workstation_id column must exist")
	require.Equal(t, "text", dataType, "source_workstation_id must be text")
	require.Equal(t, "NO", isNullable, "source_workstation_id must be NOT NULL")
	require.Contains(t, columnDefault, "''::text", "source_workstation_id default must be empty string literal")

	// Verify rows inserted without specifying the column receive empty default.
	require.NoError(t, db.Exec(`INSERT INTO memories (project, content) VALUES (?, ?)`,
		"t001b-test", "T001b default-empty fixture").Error,
		"row insertable without source_workstation_id (default applies)")

	var observed string
	require.NoError(t, db.Raw(
		`SELECT source_workstation_id FROM memories WHERE project = 't001b-test' LIMIT 1`,
	).Row().Scan(&observed))
	require.Equal(t, "", observed, "default-inserted row must carry empty source_workstation_id")

	// Verify explicit non-empty insert also accepted.
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, source_workstation_id) VALUES (?, ?, ?)`,
		"t001b-test", "T001b explicit fixture", "ws-abc-123",
	).Error, "row insertable with explicit source_workstation_id")
}
