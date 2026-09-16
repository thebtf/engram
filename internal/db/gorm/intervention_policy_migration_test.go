package gorm

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

const interventionPolicyMigrationID = "169_intervention_evidence_policies"

type interventionPolicyMigrationColumn struct {
	Name     string `gorm:"column:column_name"`
	DataType string `gorm:"column:data_type"`
	Nullable string `gorm:"column:is_nullable"`
}

type interventionPolicyMigrationConstraint struct {
	Name       string `gorm:"column:conname"`
	Definition string `gorm:"column:definition"`
}

type interventionPolicyMigrationIndex struct {
	Name       string `gorm:"column:indexname"`
	Definition string `gorm:"column:indexdef"`
}

func TestInterventionPolicyMigration169Schema(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)

	var tableCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = current_schema()
		  AND table_name = 'intervention_evidence_policies'
	`).Scan(&tableCount).Error)
	require.Equal(t, 1, tableCount, "migration 169 must create immutable intervention policies in the isolated schema")

	var columns []interventionPolicyMigrationColumn
	require.NoError(t, db.Raw(`
		SELECT column_name, data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'intervention_evidence_policies'
		ORDER BY ordinal_position
	`).Scan(&columns).Error)
	actualColumns := make(map[string]interventionPolicyMigrationColumn, len(columns))
	for _, column := range columns {
		actualColumns[column.Name] = column
	}
	for name, expected := range map[string]struct {
		dataType string
		nullable string
	}{
		"policy_id":             {"bytea", "NO"},
		"policy_version":        {"bytea", "NO"},
		"scope_commitment":      {"bytea", "NO"},
		"descriptor_commitment": {"bytea", "NO"},
		"memory_id":             {"bigint", "NO"},
		"memory_version":        {"integer", "NO"},
		"canonical_project":     {"uuid", "NO"},
		"descriptor":            {"jsonb", "NO"},
		"descriptor_status":     {"text", "NO"},
		"descriptor_origin":     {"text", "NO"},
		"compiler_version":      {"text", "NO"},
		"algorithm_version":     {"text", "NO"},
		"normalization_version": {"text", "NO"},
		"parameter_version":     {"text", "NO"},
		"created_from_event":    {"bytea", "NO"},
		"created_at":            {"timestamp with time zone", "NO"},
	} {
		actual, ok := actualColumns[name]
		require.Truef(t, ok, "policy column %q must exist", name)
		require.Equalf(t, expected.dataType, actual.DataType, "policy column %q type", name)
		require.Equalf(t, expected.nullable, actual.Nullable, "policy column %q nullability", name)
	}

	for _, forbidden := range []string{"status", "retired_at", "policy_body", "memory_fk", "memory_content", "source_content"} {
		_, exists := actualColumns[forbidden]
		require.Falsef(t, exists, "immutable policy schema must not contain mutable or source-body column %q", forbidden)
	}

	var constraints []interventionPolicyMigrationConstraint
	require.NoError(t, db.Raw(`
		SELECT conname, pg_get_constraintdef(oid) AS definition
		FROM pg_constraint
		WHERE conrelid = 'intervention_evidence_policies'::regclass
		ORDER BY conname
	`).Scan(&constraints).Error)
	constraintDefinitions := make(map[string]string, len(constraints))
	for _, constraint := range constraints {
		constraintDefinitions[constraint.Name] = strings.ToLower(constraint.Definition)
	}
	for _, required := range []string{
		"intervention_evidence_policies_pkey",
		"intervention_evidence_policies_policy_version_unique",
		"intervention_evidence_policies_source_semantic_unique",
		"intervention_evidence_policies_commitment_width",
		"intervention_evidence_policies_source_pointer",
		"intervention_evidence_policies_descriptor_shape",
		"intervention_evidence_policies_descriptor_status",
		"intervention_evidence_policies_descriptor_origin",
		"intervention_evidence_policies_semantic_versions",
		"intervention_evidence_policies_created_at_precision",
	} {
		require.Containsf(t, constraintDefinitions, required, "required migration 169 constraint %q", required)
	}
	require.Contains(t, constraintDefinitions["intervention_evidence_policies_pkey"], "primary key (policy_id)")
	require.Contains(t, constraintDefinitions["intervention_evidence_policies_policy_version_unique"], "unique (policy_version)")
	require.Contains(t, constraintDefinitions["intervention_evidence_policies_source_semantic_unique"], "unique (memory_id, memory_version, scope_commitment, descriptor_commitment, compiler_version, algorithm_version, normalization_version, parameter_version)")
	for _, commitment := range []string{"policy_id", "policy_version", "scope_commitment", "descriptor_commitment", "created_from_event"} {
		require.Containsf(t, constraintDefinitions["intervention_evidence_policies_commitment_width"], commitment, "full-width policy constraint must cover %s", commitment)
	}
	require.Contains(t, constraintDefinitions["intervention_evidence_policies_descriptor_status"], "descriptor_status = any")
	require.Contains(t, constraintDefinitions["intervention_evidence_policies_descriptor_status"], "valid")
	require.Contains(t, constraintDefinitions["intervention_evidence_policies_descriptor_status"], "insufficient")
	require.Contains(t, constraintDefinitions["intervention_evidence_policies_descriptor_origin"], "deterministic")

	var indexes []interventionPolicyMigrationIndex
	require.NoError(t, db.Raw(`
		SELECT indexname, indexdef
		FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND tablename = 'intervention_evidence_policies'
	`).Scan(&indexes).Error)
	indexDefinitions := make(map[string]string, len(indexes))
	for _, index := range indexes {
		indexDefinitions[index.Name] = strings.ToLower(index.Definition)
	}
	for _, required := range []string{
		"idx_intervention_evidence_policies_reader_current",
		"idx_intervention_evidence_policies_project_status",
		"idx_intervention_evidence_policies_created_at",
	} {
		require.Containsf(t, indexDefinitions, required, "required migration 169 index %q", required)
	}
	require.Contains(t, indexDefinitions["idx_intervention_evidence_policies_reader_current"], "(memory_id, memory_version, compiler_version, algorithm_version, normalization_version, parameter_version, descriptor_status)")
	require.Contains(t, indexDefinitions["idx_intervention_evidence_policies_project_status"], "(canonical_project, descriptor_status)")
	require.Contains(t, indexDefinitions["idx_intervention_evidence_policies_created_at"], "(created_at)")

	var foreignKeys int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM pg_constraint
		WHERE conrelid = 'intervention_evidence_policies'::regclass
		  AND contype = 'f'
	`).Scan(&foreignKeys).Error)
	require.Zero(t, foreignKeys, "retained immutable policy history must have no memory FK or cascade")

	var triggerNames []string
	require.NoError(t, db.Raw(`
		SELECT tgname
		FROM pg_trigger
		WHERE tgrelid = 'intervention_evidence_policies'::regclass
		  AND NOT tgisinternal
		ORDER BY tgname
	`).Scan(&triggerNames).Error)
	require.ElementsMatch(t, []string{
		"intervention_evidence_policies_reject_mutation",
		"intervention_evidence_policies_reject_truncate",
	}, triggerNames)
	require.Equal(t, 1, interventionReceiptImmutableFunctionCount(t, db), "migration 169 must reuse the migration 168 immutable function")
}

func TestInterventionPolicyMigration169RejectsMalformedShape(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)

	wrongCommitmentWidth := newInterventionPolicyMigrationFixture()
	wrongCommitmentWidth.PolicyID = []byte{1}
	require.Error(t, wrongCommitmentWidth.insert(db), "policy commitments must be exactly 32 bytes")

	invalidStatus := newInterventionPolicyMigrationFixture()
	invalidStatus.DescriptorStatus = "retired"
	require.Error(t, invalidStatus.insert(db), "policy schema must reject a mutable retirement status")

	invalidDescriptor := newInterventionPolicyMigrationFixture()
	invalidDescriptor.Descriptor = `[]`
	require.Error(t, invalidDescriptor.insert(db), "policy descriptor must remain a typed object")
}

func TestInterventionPolicyMigration169RejectsMutation(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	fixture := newInterventionPolicyMigrationFixture()
	require.NoError(t, fixture.insert(db))

	require.Error(t, db.Exec(`UPDATE intervention_evidence_policies SET descriptor_status = 'insufficient' WHERE policy_id = ?`, fixture.PolicyID).Error)
	require.Error(t, db.Exec(`DELETE FROM intervention_evidence_policies WHERE policy_id = ?`, fixture.PolicyID).Error)
	require.Error(t, db.Exec(`TRUNCATE TABLE intervention_evidence_policies`).Error)

	var count int64
	require.NoError(t, db.Table("intervention_evidence_policies").Count(&count).Error)
	require.EqualValues(t, 1, count, "all immutable operations must leave the policy intact")
}

func TestInterventionPolicyMigration169RollbackEmptyReappliesAndAbsentSucceeds(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)

	require.NoError(t, rollbackInterventionPolicyMigration169(db), "empty policy table must roll back")
	assertInterventionPolicyTableAbsent(t, db)
	require.Equal(t, 1, interventionReceiptImmutableFunctionCount(t, db), "policy rollback must not drop the shared migration 168 function")

	require.NoError(t, db.Exec(`DELETE FROM migrations WHERE id = ?`, interventionPolicyMigrationID).Error)
	require.NoError(t, runMigrations(db), "removing migration 169 marker must exercise the inline reapply path")
	assertInterventionPolicyTablePresent(t, db)

	require.NoError(t, rollbackInterventionPolicyMigration169(db), "empty re-created policy table must roll back")
	require.NoError(t, rollbackInterventionPolicyMigration169(db), "absent policy table rollback must succeed")
	assertInterventionPolicyTableAbsent(t, db)
	require.Equal(t, 1, interventionReceiptImmutableFunctionCount(t, db), "absent policy rollback must not drop the shared migration 168 function")
}

func TestInterventionPolicyMigration169RollbackRefusesRetainedRows(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	fixture := newInterventionPolicyMigrationFixture()
	// This is a DDL-retention fixture only. Behavioral tests create policies
	// exclusively through MemoryStore and PolicyReconciler.
	require.NoError(t, fixture.insert(db))

	err := rollbackInterventionPolicyMigration169(db)
	require.EqualError(t, err, "migration 169 rollback blocked: 1 retained intervention_evidence_policies rows")

	var count int64
	require.NoError(t, db.Table("intervention_evidence_policies").Count(&count).Error)
	require.EqualValues(t, 1, count, "blocked rollback must retain immutable policy evidence")
}

func assertInterventionPolicyTablePresent(t *testing.T, db *gormlib.DB) {
	t.Helper()
	var tableCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = 'intervention_evidence_policies'
	`).Scan(&tableCount).Error)
	require.Equal(t, 1, tableCount)
}

func assertInterventionPolicyTableAbsent(t *testing.T, db *gormlib.DB) {
	t.Helper()
	var tableCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = 'intervention_evidence_policies'
	`).Scan(&tableCount).Error)
	require.Zero(t, tableCount)
}

type interventionPolicyMigrationFixture struct {
	PolicyID             []byte
	PolicyVersion        []byte
	ScopeCommitment      []byte
	DescriptorCommitment []byte
	MemoryID             int64
	MemoryVersion        int
	CanonicalProject     string
	Descriptor           string
	DescriptorStatus     string
	DescriptorOrigin     string
	CompilerVersion      string
	AlgorithmVersion     string
	NormalizationVersion string
	ParameterVersion     string
	CreatedFromEvent     []byte
	CreatedAt            time.Time
}

func newInterventionPolicyMigrationFixture() interventionPolicyMigrationFixture {
	return interventionPolicyMigrationFixture{
		PolicyID:             interventionPolicyMigrationFixtureDigest(1),
		PolicyVersion:        interventionPolicyMigrationFixtureDigest(2),
		ScopeCommitment:      interventionPolicyMigrationFixtureDigest(3),
		DescriptorCommitment: interventionPolicyMigrationFixtureDigest(4),
		MemoryID:             1,
		MemoryVersion:        1,
		CanonicalProject:     uuid.NewString(),
		Descriptor:           `{"action":"inspect","origin":"deterministic","trigger":"agent_turn_start"}`,
		DescriptorStatus:     "valid",
		DescriptorOrigin:     "deterministic",
		CompilerVersion:      "intervention-compiler/1",
		AlgorithmVersion:     "intervention-algorithm/1",
		NormalizationVersion: "intervention-normalization/1",
		ParameterVersion:     "intervention-parameters/1:0000000000000000000000000000000000000000000000000000000000000000",
		CreatedFromEvent:     interventionPolicyMigrationFixtureDigest(5),
		CreatedAt:            time.Now().UTC().Truncate(time.Microsecond),
	}
}

func interventionPolicyMigrationFixtureDigest(seed byte) []byte {
	return bytes.Repeat([]byte{seed}, 32)
}

func (f interventionPolicyMigrationFixture) insert(db *gormlib.DB) error {
	return db.Exec(`
		INSERT INTO intervention_evidence_policies (
			policy_id,
			policy_version,
			scope_commitment,
			descriptor_commitment,
			memory_id,
			memory_version,
			canonical_project,
			descriptor,
			descriptor_status,
			descriptor_origin,
			compiler_version,
			algorithm_version,
			normalization_version,
			parameter_version,
			created_from_event,
			created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`,
		f.PolicyID,
		f.PolicyVersion,
		f.ScopeCommitment,
		f.DescriptorCommitment,
		f.MemoryID,
		f.MemoryVersion,
		f.CanonicalProject,
		f.Descriptor,
		f.DescriptorStatus,
		f.DescriptorOrigin,
		f.CompilerVersion,
		f.AlgorithmVersion,
		f.NormalizationVersion,
		f.ParameterVersion,
		f.CreatedFromEvent,
		f.CreatedAt,
	).Error
}
