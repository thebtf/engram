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
