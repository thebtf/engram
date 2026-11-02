package gorm

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	interventionReceiptMigrationID                 = "168_task_memory_intervention_receipts"
	interventionReceiptContextReferenceMigrationID = "170_task_memory_context_reference_receipts"
)

const migratedTestAdminApplicationName = "engram_fixture_admin"

// openInterventionReceiptMigrationTestDB creates a schema that is private to one
// test. Immutable receipt rows are never deleted individually: the test pool is
// closed first, then the dedicated admin connection drops this schema CASCADE.
func openInterventionReceiptMigrationTestDB(t *testing.T) (*gormlib.DB, string) {
	t.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping intervention receipt PostgreSQL integration test")
	}
	schema := "intervention_receipt_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	return openMigratedTestDBInSchema(t, dsn, schema, "engram_intervention_receipt_test"), schema
}

func scopedTestApplicationName(applicationName, schema string) string {
	// PostgreSQL limits application_name to 63 bytes. Preserve the unique schema
	// suffix so cleanup observes only sessions belonging to this fixture.
	return applicationName[:min(len(applicationName), 30)] + ":" + schema[max(0, len(schema)-32):]
}

func waitForMigratedTestSessions(ctx context.Context, adminDB *gormlib.DB, fixtureApplicationName string) error {
	var ticker *time.Ticker
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var count int64
		if err := adminDB.WithContext(ctx).Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND application_name = ?`, fixtureApplicationName).Scan(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		if ticker == nil {
			ticker = time.NewTicker(10 * time.Millisecond)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func openMigratedTestDBInSchema(t *testing.T, dsn, schema, applicationName string) *gormlib.DB {
	t.Helper()

	adminConfig, err := pgx.ParseConfig(dsn)
	require.NoError(t, err, "parse test database DSN")
	if adminConfig.RuntimeParams == nil {
		adminConfig.RuntimeParams = make(map[string]string)
	}
	adminApplicationName := scopedTestApplicationName(migratedTestAdminApplicationName, schema)
	adminConfig.RuntimeParams["application_name"] = adminApplicationName
	adminSQL := stdlib.OpenDB(*adminConfig)
	adminSQL.SetMaxOpenConns(2)
	adminSQL.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := adminSQL.Close(); err != nil {
			t.Errorf("close receipt admin pool: %v", err)
		}
	})
	adminDB, err := gormlib.Open(postgres.New(postgres.Config{Conn: adminSQL}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "open dedicated admin connection")
	var actualAdminApplicationName string
	require.NoError(t, adminDB.Raw(`SELECT current_setting('application_name')`).Scan(&actualAdminApplicationName).Error)
	require.Equal(t, adminApplicationName, actualAdminApplicationName, "the driver must apply the exact fixture admin identity")
	require.NoError(t, adminDB.Exec(`CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public`).Error, "install pgvector in stable public schema")

	require.NoError(t, adminDB.Exec("CREATE SCHEMA "+schema).Error, "create isolated receipt schema")
	drained := true
	t.Cleanup(func() {
		if !drained {
			t.Errorf("retain isolated receipt schema %s: fixture sessions did not drain", schema)
			return
		}
		if err := adminDB.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop isolated receipt schema %s: %v", schema, err)
		}
	})

	config := adminConfig.Copy()
	fixtureApplicationName := scopedTestApplicationName(applicationName, schema)
	config.RuntimeParams["application_name"] = fixtureApplicationName
	config.RuntimeParams["search_path"] = schema + ", public"

	testSQL := stdlib.OpenDB(*config)
	testSQL.SetMaxOpenConns(2)
	testSQL.SetMaxIdleConns(1)
	drained = false
	// Register after the schema cleanup so LIFO cleanup closes this pool before
	// DROP SCHEMA CASCADE releases immutable fixture rows.
	t.Cleanup(func() {
		if err := testSQL.Close(); err != nil {
			t.Errorf("close receipt test pool: %v", err)
		}
		// Close can return while a driver call is still using a connection. An
		// async audit INSERT can hold its sequence while acquiring the memories FK
		// lock, in the opposite order to DROP SCHEMA CASCADE.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := waitForMigratedTestSessions(ctx, adminDB, fixtureApplicationName)
		drained = err == nil
		require.NoError(t, err, "closed fixture sessions must finish before schema cleanup")
	})

	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: testSQL}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "open isolated receipt test pool")
	require.NoError(t, testSQL.Ping(), "ping isolated receipt test pool")
	require.NoError(t, runMigrations(db), "run migration chain in isolated receipt schema")

	var actualSchema string
	require.NoError(t, db.Raw(`SELECT current_schema()`).Scan(&actualSchema).Error)
	require.Equal(t, schema, actualSchema, "test pool must resolve its isolated schema before public")
	return db
}

func TestMigratedTestSessionPollCancellationAndErrors(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set; session-drain proof requires PostgreSQL")
	}
	adminDB, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	adminSQL, err := adminDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, adminSQL.Close()) })
	adminSQL.SetMaxOpenConns(1)
	adminSQL.SetMaxIdleConns(1)
	scope := "drain_poll_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	openSession := func(t *testing.T, applicationName string) *sql.DB {
		t.Helper()
		config, err := pgx.ParseConfig(dsn)
		require.NoError(t, err)
		if config.RuntimeParams == nil {
			config.RuntimeParams = make(map[string]string)
		}
		config.RuntimeParams["application_name"] = applicationName
		pool := stdlib.OpenDB(*config)
		pool.SetMaxOpenConns(1)
		pool.SetMaxIdleConns(1)
		t.Cleanup(func() { require.NoError(t, pool.Close()) })
		require.NoError(t, pool.PingContext(t.Context()))
		return pool
	}
	t.Run("exact application name excludes another own fixture", func(t *testing.T) {
		other := openSession(t, scope+"-other")
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		require.NoError(t, waitForMigratedTestSessions(ctx, adminDB, scope))
		require.NoError(t, other.PingContext(t.Context()), "polling must not terminate another fixture's session")
	})
	t.Run("live own session remains intact on deadline", func(t *testing.T) {
		own := openSession(t, scope)
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		require.ErrorIs(t, waitForMigratedTestSessions(ctx, adminDB, scope), context.DeadlineExceeded)
		require.NoError(t, own.PingContext(t.Context()), "a drain deadline must not kill the retained session")
	})
	t.Run("actual query acquisition obeys cancellation", func(t *testing.T) {
		held, err := adminSQL.Conn(t.Context())
		require.NoError(t, err)
		waitsBefore := adminSQL.Stats().WaitCount
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		released := make(chan error, 1)
		stop := context.AfterFunc(ctx, func() { released <- held.Close() })
		defer func() {
			if stop() {
				require.NoError(t, held.Close())
			} else {
				require.NoError(t, <-released)
			}
		}()
		require.ErrorIs(t, waitForMigratedTestSessions(ctx, adminDB, scope), context.DeadlineExceeded)
		require.Greater(t, adminSQL.Stats().WaitCount, waitsBefore, "the real metadata query must have waited for its held connection")
	})
	t.Run("actual query error returns instead of polling", func(t *testing.T) {
		require.NoError(t, adminSQL.Close())
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		require.ErrorContains(t, waitForMigratedTestSessions(ctx, adminDB, scope), "database is closed")
		require.NoError(t, ctx.Err(), "the query error must return before its deadline")
	})
}

func TestMigratedSchemaDrainRetainsSchemaOnDeadline(t *testing.T) {
	const childSchemaEnv = "ENGRAM_TEST_DRAIN_RETENTION_SCHEMA"
	const applicationName = "drain_retention_test"
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set; retained-schema proof requires PostgreSQL")
	}
	if schema := os.Getenv(childSchemaEnv); schema != "" {
		id, err := uuid.Parse(strings.TrimPrefix(schema, "drain_retention_"))
		require.NoError(t, err)
		require.Equal(t, "drain_retention_"+strings.ReplaceAll(id.String(), "-", ""), schema)
		db := openMigratedTestDBInSchema(t, dsn, schema, applicationName)
		require.NoError(t, db.Exec(`CREATE TABLE drain_retention_marker (value TEXT NOT NULL)`).Error)
		require.NoError(t, db.Exec(`INSERT INTO drain_retention_marker VALUES ('retained')`).Error)
		return
	}
	schema := "drain_retention_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	adminDB, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	adminSQL, err := adminDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, adminSQL.Close()) })
	t.Cleanup(func() { require.NoError(t, adminDB.Exec("DROP SCHEMA IF EXISTS "+schema+" CASCADE").Error) })
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	if config.RuntimeParams == nil {
		config.RuntimeParams = make(map[string]string)
	}
	config.RuntimeParams["application_name"] = scopedTestApplicationName(applicationName, schema)
	held := stdlib.OpenDB(*config)
	held.SetMaxOpenConns(1)
	held.SetMaxIdleConns(1)
	t.Cleanup(func() { require.NoError(t, held.Close()) })
	require.NoError(t, held.PingContext(t.Context()))
	adminApplicationName := scopedTestApplicationName(migratedTestAdminApplicationName, schema)
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestMigratedSchemaDrainRetainsSchemaOnDeadline$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), childSchemaEnv+"="+schema, "DATABASE_DSN="+dsn)
	output, childErr := command.CombinedOutput()
	require.NoError(t, ctx.Err(), "the child must return its own bounded drain failure")
	require.Error(t, childErr, "the child fixture must fail rather than drop a live schema")
	require.Contains(t, string(output), "context deadline exceeded")
	require.Contains(t, string(output), "retain isolated receipt schema "+schema)
	var retained int64
	require.NoError(t, adminDB.Raw("SELECT count(*) FROM "+schema+".drain_retention_marker WHERE value = 'retained'").Scan(&retained).Error)
	require.EqualValues(t, 1, retained, "the failed child must preserve its schema and durable marker")
	require.NoError(t, held.PingContext(t.Context()), "the child must not kill a retained fixture session")
	var adminSessions int64
	require.NoError(t, adminDB.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND application_name = ?`, adminApplicationName).Scan(&adminSessions).Error)
	require.Zero(t, adminSessions, "the child must join its poll and close its admin pool before returning")
	t.Log("expected child drain deadline preserved the schema and marker; live fixture session survived; child admin sessions=0")
}

type interventionReceiptColumn struct {
	Name     string `gorm:"column:column_name"`
	DataType string `gorm:"column:data_type"`
	Nullable string `gorm:"column:is_nullable"`
}

type interventionReceiptConstraint struct {
	Name       string `gorm:"column:conname"`
	Definition string `gorm:"column:definition"`
}

type interventionReceiptIndex struct {
	Name       string `gorm:"column:indexname"`
	Definition string `gorm:"column:indexdef"`
}

func TestInterventionReceiptMigration168Schema(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)

	var tableCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = current_schema()
		  AND table_name = 'task_memory_intervention_receipts'
	`).Scan(&tableCount).Error)
	require.Equal(t, 1, tableCount, "migration 168 must create the v2 receipt table in the isolated schema")

	var columns []interventionReceiptColumn
	require.NoError(t, db.Raw(`
		SELECT column_name, data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'task_memory_intervention_receipts'
		ORDER BY ordinal_position
	`).Scan(&columns).Error)
	actualColumns := make(map[string]interventionReceiptColumn, len(columns))
	for _, column := range columns {
		actualColumns[column.Name] = column
	}
	for name, expected := range map[string]struct {
		dataType string
		nullable string
	}{
		"receipt_id":                     {"uuid", "NO"},
		"operation_id":                   {"uuid", "NO"},
		"key_epoch_commitment":           {"bytea", "NO"},
		"integrity_digest":               {"bytea", "NO"},
		"channel_key":                    {"bytea", "NO"},
		"host_family":                    {"text", "NO"},
		"canonical_project":              {"uuid", "NO"},
		"actor_principal":                {"text", "NO"},
		"actor_kind":                     {"text", "NO"},
		"workstation":                    {"text", "NO"},
		"session_key":                    {"bytea", "NO"},
		"occurrence_key":                 {"bytea", "NO"},
		"content_commitment":             {"bytea", "NO"},
		"capability_snapshot_commitment": {"bytea", "NO"},
		"outcome":                        {"text", "NO"},
		"closed_reason":                  {"text", "YES"},
		"decision_mode":                  {"text", "NO"},
		"evaluated_count":                {"smallint", "NO"},
		"eligible_count":                 {"smallint", "NO"},
		"snapshot_refs":                  {"jsonb", "NO"},
		"selected_memory_id":             {"bigint", "YES"},
		"selected_memory_version":        {"integer", "YES"},
		"selected_source_project":        {"uuid", "YES"},
		"selected_source_tier":           {"smallint", "YES"},
		"selected_policy_id":             {"bytea", "YES"},
		"selected_snapshot_id":           {"uuid", "YES"},
		"selected_snapshot_version":      {"bigint", "YES"},
		"selected_text_digest":           {"bytea", "YES"},
		"created_at":                     {"timestamp with time zone", "NO"},
		"expires_at":                     {"timestamp with time zone", "NO"},
	} {
		actual, ok := actualColumns[name]
		require.Truef(t, ok, "receipt column %q must exist", name)
		require.Equalf(t, expected.dataType, actual.DataType, "receipt column %q type", name)
		require.Equalf(t, expected.nullable, actual.Nullable, "receipt column %q nullability", name)
	}

	for _, forbidden := range []string{
		"host_adapter", "adapter_id", "adapter_version", "host_version",
		"runtime_instance", "runtime_id", "host_session_ref", "session_ref",
		"query", "task_query", "body", "packet", "project_evidence",
		"project_descriptor", "raw_project_evidence",
	} {
		_, exists := actualColumns[forbidden]
		require.Falsef(t, exists, "v2 receipt schema must not persist raw %q", forbidden)
	}

	for _, legacy := range []string{"task_memory_delivery_receipts", "legacy_task_memory_receipts"} {
		var count int
		require.NoError(t, db.Raw(`
			SELECT COUNT(*)
			FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = ?
		`, legacy).Scan(&count).Error)
		require.Zerof(t, count, "isolated v2 schema must not create legacy receipt table %q", legacy)
	}

	var constraints []interventionReceiptConstraint
	require.NoError(t, db.Raw(`
		SELECT conname, pg_get_constraintdef(oid) AS definition
		FROM pg_constraint
		WHERE conrelid = 'task_memory_intervention_receipts'::regclass
		ORDER BY conname
	`).Scan(&constraints).Error)
	constraintDefinitions := make(map[string]string, len(constraints))
	for _, constraint := range constraints {
		constraintDefinitions[constraint.Name] = strings.ToLower(constraint.Definition)
	}
	for _, required := range []string{
		"task_memory_intervention_receipts_occurrence_unique",
		"task_memory_intervention_receipts_operation_unique",
		"task_memory_intervention_receipts_commitment_width",
		"task_memory_intervention_receipts_actor_pair",
		"task_memory_intervention_receipts_counts",
		"task_memory_intervention_receipts_snapshot_refs",
		"task_memory_intervention_receipts_selected_version",
		"task_memory_intervention_receipts_outcome_shape",
		"task_memory_intervention_receipts_selected_source",
		"task_memory_intervention_receipts_expiry",
	} {
		require.Containsf(t, constraintDefinitions, required, "required migration 168 constraint %q", required)
	}
	require.Contains(t, constraintDefinitions["task_memory_intervention_receipts_occurrence_unique"], "unique (channel_key, canonical_project, actor_principal, actor_kind, workstation, occurrence_key)")
	for _, commitment := range []string{
		"key_epoch_commitment", "integrity_digest", "channel_key", "session_key",
		"occurrence_key", "content_commitment", "capability_snapshot_commitment",
	} {
		require.Containsf(t, constraintDefinitions["task_memory_intervention_receipts_commitment_width"], commitment, "full-width receipt constraint must cover %s", commitment)
	}
	require.Contains(t, constraintDefinitions["task_memory_intervention_receipts_selected_version"], "octet_length(selected_policy_id) = 32")
	require.Contains(t, constraintDefinitions["task_memory_intervention_receipts_selected_version"], "octet_length(selected_text_digest) = 32")
	require.Contains(t, constraintDefinitions["task_memory_intervention_receipts_outcome_shape"], "outcome = 'emit'")
	require.Contains(t, constraintDefinitions["task_memory_intervention_receipts_outcome_shape"], "outcome = 'abstain'")
	require.Contains(t, constraintDefinitions["task_memory_intervention_receipts_outcome_shape"], "context_reference")
	require.Contains(t, constraintDefinitions["task_memory_intervention_receipts_outcome_shape"], "selected_source_project = canonical_project")

	var indexes []interventionReceiptIndex
	require.NoError(t, db.Raw(`
		SELECT indexname, indexdef
		FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND tablename = 'task_memory_intervention_receipts'
	`).Scan(&indexes).Error)
	indexDefinitions := make(map[string]string, len(indexes))
	for _, index := range indexes {
		indexDefinitions[index.Name] = strings.ToLower(index.Definition)
	}
	for _, required := range []string{
		"idx_task_memory_intervention_receipts_session_visibility",
		"idx_task_memory_intervention_receipts_canary_policy",
		"idx_task_memory_intervention_receipts_expires_at",
	} {
		require.Containsf(t, indexDefinitions, required, "required migration 168 index %q", required)
	}
	require.Contains(t, indexDefinitions["idx_task_memory_intervention_receipts_session_visibility"], "where")
	require.Contains(t, indexDefinitions["idx_task_memory_intervention_receipts_session_visibility"], "outcome")
	require.Contains(t, indexDefinitions["idx_task_memory_intervention_receipts_session_visibility"], "selected_memory_id")
	require.Contains(t, indexDefinitions["idx_task_memory_intervention_receipts_canary_policy"], "where")
	require.Contains(t, indexDefinitions["idx_task_memory_intervention_receipts_canary_policy"], "decision_mode")
	require.Contains(t, indexDefinitions["idx_task_memory_intervention_receipts_canary_policy"], "selected_policy_id")
	require.Contains(t, indexDefinitions["idx_task_memory_intervention_receipts_expires_at"], "(expires_at)")

	var triggerNames []string
	require.NoError(t, db.Raw(`
		SELECT tgname
		FROM pg_trigger
		WHERE tgrelid = 'task_memory_intervention_receipts'::regclass
		  AND NOT tgisinternal
		ORDER BY tgname
	`).Scan(&triggerNames).Error)
	require.ElementsMatch(t, []string{
		"task_memory_intervention_receipts_reject_mutation",
		"task_memory_intervention_receipts_reject_truncate",
	}, triggerNames)
	require.Equal(t, 1, interventionReceiptImmutableFunctionCount(t, db), "migration 168 must create its reusable immutable trigger function")
}

func TestInterventionReceiptMigration168RejectsMalformedShape(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)

	malformedCommitment := newInterventionReceiptFixture()
	malformedCommitment.KeyEpochCommitment = []byte{1}
	require.Error(t, malformedCommitment.insert(db), "all receipt commitments must be exactly 32 bytes")

	invalidEmit := newInterventionReceiptFixture()
	invalidEmit.Outcome = "emit"
	invalidEmit.ClosedReason = ""
	require.Error(t, invalidEmit.insert(db), "an emit must have a complete selection and no closed reason")
}

func TestInterventionReceiptMigration168RejectsMutation(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	fixture := newInterventionReceiptFixture()
	require.NoError(t, fixture.insert(db))

	require.Error(t, db.Exec(`UPDATE task_memory_intervention_receipts SET eligible_count = 1 WHERE receipt_id = ?`, fixture.ReceiptID).Error)
	require.Error(t, db.Exec(`DELETE FROM task_memory_intervention_receipts WHERE receipt_id = ?`, fixture.ReceiptID).Error)
	require.Error(t, db.Exec(`TRUNCATE TABLE task_memory_intervention_receipts`).Error)

	var count int64
	require.NoError(t, db.Table("task_memory_intervention_receipts").Count(&count).Error)
	require.EqualValues(t, 1, count, "all three immutable operations must leave the receipt intact")
}

func TestInterventionReceiptMigration170ReappliesWithoutRewritingLegacyRows(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	legacy := newInterventionReceiptFixture()
	require.NoError(t, legacy.insert(db))

	var before struct {
		ReceiptID      string `gorm:"column:receipt_id"`
		DecisionMode   string `gorm:"column:decision_mode"`
		Outcome        string `gorm:"column:outcome"`
		ClosedReason   string `gorm:"column:closed_reason"`
		EvaluatedCount int    `gorm:"column:evaluated_count"`
	}
	require.NoError(t, db.Raw(`
		SELECT receipt_id, decision_mode, outcome, closed_reason, evaluated_count
		FROM task_memory_intervention_receipts
		WHERE receipt_id = ?
	`, legacy.ReceiptID).Scan(&before).Error)

	require.NoError(t, rollbackInterventionContextReferenceMigration170(db), "legacy-only ledger must roll back migration 170")
	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", interventionReceiptContextReferenceMigrationID).Error)
	require.NoError(t, runMigrations(db), "migration 170 must reapply over retained legacy rows")

	var after struct {
		ReceiptID      string `gorm:"column:receipt_id"`
		DecisionMode   string `gorm:"column:decision_mode"`
		Outcome        string `gorm:"column:outcome"`
		ClosedReason   string `gorm:"column:closed_reason"`
		EvaluatedCount int    `gorm:"column:evaluated_count"`
	}
	require.NoError(t, db.Raw(`
		SELECT receipt_id, decision_mode, outcome, closed_reason, evaluated_count
		FROM task_memory_intervention_receipts
		WHERE receipt_id = ?
	`, legacy.ReceiptID).Scan(&after).Error)
	require.Equal(t, before, after, "migration 170 must extend—not rewrite—the retained legacy ledger")

	var sourced int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM task_memory_intervention_receipts
		WHERE receipt_id = ? AND selected_source_project IS NOT NULL
	`, legacy.ReceiptID).Scan(&sourced).Error)
	require.Zero(t, sourced, "legacy rows must remain free of M1 source fields")
}

func TestInterventionReceiptMigration170RejectsMismatchedSourceAndBlocksRollback(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	mismatched := newInterventionReceiptFixture()
	require.Error(t, insertContextReferenceReceiptFixture(db, mismatched, uuid.NewString()), "context source project must match the ledger project")

	contextReceipt := newInterventionReceiptFixture()
	require.NoError(t, insertContextReferenceReceiptFixture(db, contextReceipt, contextReceipt.CanonicalProject))
	err := rollbackInterventionContextReferenceMigration170(db)
	require.ErrorContains(t, err, "1 retained context_reference task_memory_intervention_receipts rows")
}

func TestInterventionReceiptMigration168RollbackEmptyReappliesAndAbsentSucceeds(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)

	require.NoError(t, rollbackInterventionContextReferenceMigration170(db), "empty context-reference migration must roll back before its receipt-table owner")
	require.NoError(t, rollbackInterventionPolicyMigration169(db), "empty policy table must roll back before its shared function owner")
	require.NoError(t, rollbackInterventionReceiptMigration168(db), "empty receipt table must roll back")
	assertInterventionReceiptTableAbsent(t, db)
	require.Zero(t, interventionReceiptImmutableFunctionCount(t, db), "ordered empty rollback must remove the immutable trigger function")

	require.NoError(t, db.Exec(`DELETE FROM migrations WHERE id IN (?, ?, ?)`, interventionReceiptMigrationID, interventionPolicyMigrationID, interventionReceiptContextReferenceMigrationID).Error)
	require.NoError(t, runMigrations(db), "removing migration 168-170 markers must exercise ordered inline reapply")
	var tableCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = current_schema()
		  AND table_name IN ('task_memory_intervention_receipts', 'intervention_evidence_policies')
	`).Scan(&tableCount).Error)
	require.Equal(t, 2, tableCount, "migrations 168-170 must recreate both additive tables")

	require.NoError(t, rollbackInterventionContextReferenceMigration170(db), "empty re-created context-reference migration must roll back")
	require.NoError(t, rollbackInterventionPolicyMigration169(db), "empty re-created policy table must roll back")
	require.NoError(t, rollbackInterventionReceiptMigration168(db), "empty re-created receipt table must roll back")
	require.NoError(t, rollbackInterventionReceiptMigration168(db), "absent receipt table rollback must succeed")
	assertInterventionReceiptTableAbsent(t, db)
}

func TestInterventionReceiptMigration168RollbackRefusesRetainedRows(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	fixture := newInterventionReceiptFixture()
	require.NoError(t, fixture.insert(db))

	err := rollbackInterventionReceiptMigration168(db)
	require.Error(t, err)
	require.ErrorContains(t, err, "1 retained task_memory_intervention_receipts rows")

	var count int64
	require.NoError(t, db.Table("task_memory_intervention_receipts").Count(&count).Error)
	require.EqualValues(t, 1, count, "blocked rollback must retain immutable evidence")
}

func assertInterventionReceiptTableAbsent(t *testing.T, db *gormlib.DB) {
	t.Helper()
	var tableCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = 'task_memory_intervention_receipts'
	`).Scan(&tableCount).Error)
	require.Zero(t, tableCount)
}

func interventionReceiptImmutableFunctionCount(t *testing.T, db *gormlib.DB) int {
	t.Helper()
	var count int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM pg_proc AS p
		JOIN pg_namespace AS n ON n.oid = p.pronamespace
		WHERE n.nspname = current_schema()
		  AND p.proname = 'task_memory_reject_immutable_mutation'
	`).Scan(&count).Error)
	return count
}

type interventionReceiptFixture struct {
	ReceiptID                    string
	OperationID                  string
	KeyEpochCommitment           []byte
	IntegrityDigest              []byte
	ChannelKey                   []byte
	HostFamily                   string
	CanonicalProject             string
	ActorPrincipal               string
	ActorKind                    string
	Workstation                  string
	SessionKey                   []byte
	OccurrenceKey                []byte
	ContentCommitment            []byte
	CapabilitySnapshotCommitment []byte
	Outcome                      string
	ClosedReason                 string
	DecisionMode                 string
	EvaluatedCount               int
	EligibleCount                int
	SnapshotRefs                 []byte
	CreatedAt                    time.Time
	ExpiresAt                    time.Time
}

func newInterventionReceiptFixture() interventionReceiptFixture {
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	return interventionReceiptFixture{
		ReceiptID:                    uuid.NewString(),
		OperationID:                  uuid.NewString(),
		KeyEpochCommitment:           interventionReceiptFixtureDigest(1),
		IntegrityDigest:              interventionReceiptFixtureDigest(2),
		ChannelKey:                   interventionReceiptFixtureDigest(3),
		HostFamily:                   "omp",
		CanonicalProject:             uuid.NewString(),
		ActorPrincipal:               "receipt-test-agent",
		ActorKind:                    "agent",
		Workstation:                  "receipt-test-workstation",
		SessionKey:                   interventionReceiptFixtureDigest(4),
		OccurrenceKey:                interventionReceiptFixtureDigest(5),
		ContentCommitment:            interventionReceiptFixtureDigest(6),
		CapabilitySnapshotCommitment: interventionReceiptFixtureDigest(7),
		Outcome:                      "abstain",
		ClosedReason:                 "no_candidates",
		DecisionMode:                 "none",
		EvaluatedCount:               0,
		EligibleCount:                0,
		SnapshotRefs:                 []byte("[]"),
		CreatedAt:                    createdAt,
		ExpiresAt:                    createdAt.Add(time.Minute),
	}
}

func insertContextReferenceReceiptFixture(db *gormlib.DB, fixture interventionReceiptFixture, sourceProject string) error {
	return db.Exec(`
		INSERT INTO task_memory_intervention_receipts (
			receipt_id,
			operation_id,
			key_epoch_commitment,
			integrity_digest,
			channel_key,
			host_family,
			canonical_project,
			actor_principal,
			actor_kind,
			workstation,
			session_key,
			occurrence_key,
			content_commitment,
			capability_snapshot_commitment,
			outcome,
			closed_reason,
			decision_mode,
			evaluated_count,
			eligible_count,
			snapshot_refs,
			selected_memory_id,
			selected_memory_version,
			selected_source_project,
			selected_source_tier,
			selected_policy_id,
			selected_snapshot_id,
			selected_snapshot_version,
			selected_text_digest,
			created_at,
			expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'emit', NULL, 'context_reference', 1, 0, '[]', 1, 1, ?, 1, NULL, NULL, NULL, ?, ?, ?)
	`,
		fixture.ReceiptID,
		fixture.OperationID,
		fixture.KeyEpochCommitment,
		fixture.IntegrityDigest,
		fixture.ChannelKey,
		fixture.HostFamily,
		fixture.CanonicalProject,
		fixture.ActorPrincipal,
		fixture.ActorKind,
		fixture.Workstation,
		fixture.SessionKey,
		fixture.OccurrenceKey,
		fixture.ContentCommitment,
		fixture.CapabilitySnapshotCommitment,
		sourceProject,
		fixture.IntegrityDigest,
		fixture.CreatedAt,
		fixture.ExpiresAt,
	).Error
}

func interventionReceiptFixtureDigest(seed byte) []byte {
	return bytes.Repeat([]byte{seed}, 32)
}

func (f interventionReceiptFixture) insert(db *gormlib.DB) error {
	return db.Exec(`
		INSERT INTO task_memory_intervention_receipts (
			receipt_id,
			operation_id,
			key_epoch_commitment,
			integrity_digest,
			channel_key,
			host_family,
			canonical_project,
			actor_principal,
			actor_kind,
			workstation,
			session_key,
			occurrence_key,
			content_commitment,
			capability_snapshot_commitment,
			outcome,
			closed_reason,
			decision_mode,
			evaluated_count,
			eligible_count,
			snapshot_refs,
			created_at,
			expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		f.ReceiptID,
		f.OperationID,
		f.KeyEpochCommitment,
		f.IntegrityDigest,
		f.ChannelKey,
		f.HostFamily,
		f.CanonicalProject,
		f.ActorPrincipal,
		f.ActorKind,
		f.Workstation,
		f.SessionKey,
		f.OccurrenceKey,
		f.ContentCommitment,
		f.CapabilitySnapshotCommitment,
		f.Outcome,
		f.ClosedReason,
		f.DecisionMode,
		f.EvaluatedCount,
		f.EligibleCount,
		string(f.SnapshotRefs),
		f.CreatedAt,
		f.ExpiresAt,
	).Error
}
