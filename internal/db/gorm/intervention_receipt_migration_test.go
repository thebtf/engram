package gorm

import (
	"bytes"
	"os"
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

// openInterventionReceiptMigrationTestDB creates a schema that is private to one
// test. Immutable receipt rows are never deleted individually: the test pool is
// closed first, then the dedicated admin connection drops this schema CASCADE.
func openInterventionReceiptMigrationTestDB(t *testing.T) (*gormlib.DB, string) {
	t.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping intervention receipt PostgreSQL integration test")
	}

	adminDB, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "open dedicated admin connection")
	adminSQL, err := adminDB.DB()
	require.NoError(t, err, "resolve dedicated admin pool")
	require.NoError(t, adminDB.Exec(`CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public`).Error, "install pgvector in stable public schema")

	schema := "intervention_receipt_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, adminDB.Exec("CREATE SCHEMA "+schema).Error, "create isolated receipt schema")
	t.Cleanup(func() {
		if err := adminDB.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop isolated receipt schema %s: %v", schema, err)
		}
		if err := adminSQL.Close(); err != nil {
			t.Errorf("close receipt admin pool: %v", err)
		}
	})

	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err, "parse test database DSN")
	if config.RuntimeParams == nil {
		config.RuntimeParams = make(map[string]string)
	}
	config.RuntimeParams["application_name"] = "engram_intervention_receipt_test"
	config.RuntimeParams["search_path"] = schema + ", public"

	testSQL := stdlib.OpenDB(*config)
	// Register after the schema cleanup so LIFO cleanup closes this pool before
	// DROP SCHEMA CASCADE releases immutable fixture rows.
	t.Cleanup(func() {
		if err := testSQL.Close(); err != nil {
			t.Errorf("close receipt test pool: %v", err)
		}
	})

	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: testSQL}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "open isolated receipt test pool")
	require.NoError(t, testSQL.Ping(), "ping isolated receipt test pool")
	require.NoError(t, runMigrations(db), "run migration chain in isolated receipt schema")

	var actualSchema string
	require.NoError(t, db.Raw(`SELECT current_schema()`).Scan(&actualSchema).Error)
	require.Equal(t, schema, actualSchema, "test pool must resolve its isolated schema before public")
	return db, schema
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
