package gorm

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProjectIdentityV3Migration162(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	defer cleanup()
	db := store.GetDB()

	// NewStore ran the migration chain. Running this migration again proves its
	// forward DDL is safe when an install retries after a completed migration.
	migration := projectIdentityV3Migration162()
	require.NoError(t, migration.Migrate(db))

	for column, dataType := range map[string]string{
		"project_key":       "uuid",
		"anchor_project_id": "uuid",
		"identity_scope":    "text",
		"identity_status":   "text",
	} {
		var actualType, nullable string
		require.NoError(t, db.Raw(`
			SELECT data_type, is_nullable
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'projects' AND column_name = ?
		`, column).Row().Scan(&actualType, &nullable))
		require.Equal(t, dataType, actualType, "%s data type", column)
		require.Equal(t, "YES", nullable, "%s must remain additive and nullable", column)
	}

	for _, table := range []string{"project_identifiers", "project_merge_audits", "project_merge_audit_sources"} {
		var count int
		require.NoError(t, db.Raw(`
			SELECT COUNT(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = ?
		`, table).Scan(&count).Error)
		require.Equal(t, 1, count, "%s must exist", table)
	}

	var projectKeyConstraintCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) FROM pg_constraint
		WHERE conname = 'projects_project_key_key'
		  AND conrelid = 'projects'::regclass
		  AND contype = 'u'
	`).Scan(&projectKeyConstraintCount).Error)
	require.Equal(t, 1, projectKeyConstraintCount, "nullable project_key must be a foreign-key target")

	for index, predicate := range map[string]string{
		"idx_projects_anchor_project_id": "where (anchor_project_id is not null)",
	} {
		var definition string
		require.NoError(t, db.Raw(`SELECT indexdef FROM pg_indexes WHERE indexname = ?`, index).Row().Scan(&definition))
		require.Contains(t, strings.ToLower(definition), predicate, "%s must be partial", index)
	}

	for _, foreignKey := range []struct {
		table, name, column, targetTable, targetColumn string
	}{
		{"project_identifiers", "project_identifiers_project_key_fkey", "project_key", "projects", "project_key"},
		{"project_merge_audits", "project_merge_audits_target_project_key_fkey", "target_project_key", "projects", "project_key"},
		{"project_merge_audit_sources", "project_merge_audit_sources_merge_id_fkey", "merge_id", "project_merge_audits", "merge_id"},
		{"project_merge_audit_sources", "project_merge_audit_sources_project_key_fkey", "source_project_key", "projects", "project_key"},
	} {
		var count int
		require.NoError(t, db.Raw(`
			SELECT COUNT(*)
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage kcu
			  ON kcu.constraint_catalog = tc.constraint_catalog
			 AND kcu.constraint_schema = tc.constraint_schema
			 AND kcu.constraint_name = tc.constraint_name
			JOIN information_schema.constraint_column_usage ccu
			  ON ccu.constraint_catalog = tc.constraint_catalog
			 AND ccu.constraint_schema = tc.constraint_schema
			 AND ccu.constraint_name = tc.constraint_name
			WHERE tc.table_schema = 'public'
			  AND tc.table_name = ?
			  AND tc.constraint_name = ?
			  AND tc.constraint_type = 'FOREIGN KEY'
			  AND kcu.column_name = ?
			  AND ccu.table_name = ?
			  AND ccu.column_name = ?
		`, foreignKey.table, foreignKey.name, foreignKey.column, foreignKey.targetTable, foreignKey.targetColumn).Scan(&count).Error)
		require.Equal(t, 1, count, "%s must constrain %s", foreignKey.name, foreignKey.column)
	}

	v2ID := "migration-162-v2-" + uuid.NewString()
	sourceID := "migration-162-source-" + uuid.NewString()
	targetID := "migration-162-target-" + uuid.NewString()
	duplicateKeyID := "migration-162-duplicate-key-" + uuid.NewString()
	duplicateAnchorID := "migration-162-duplicate-anchor-" + uuid.NewString()
	sourceKey := uuid.NewString()
	targetKey := uuid.NewString()
	anchorID := uuid.NewString()
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM project_identifiers WHERE project_key IN (?, ?)`, sourceKey, targetKey).Error
		_ = db.Exec(`DELETE FROM project_merge_audit_sources WHERE source_project_key IN (?, ?)`, sourceKey, targetKey).Error
		_ = db.Exec(`DELETE FROM project_merge_audits WHERE target_project_key = ?`, targetKey).Error
		_ = db.Exec(`DELETE FROM projects WHERE id IN (?, ?, ?, ?, ?)`, v2ID, sourceID, targetID, duplicateKeyID, duplicateAnchorID).Error
	})

	// Existing V2 rows keep their original identity and have no V3 key assigned.
	require.NoError(t, db.Create(&Project{ID: v2ID}).Error)
	var v2Project Project
	require.NoError(t, db.First(&v2Project, "id = ?", v2ID).Error)
	require.False(t, v2Project.ProjectKey.Valid)
	require.False(t, v2Project.AnchorProjectID.Valid)

	require.NoError(t, db.Create(&Project{
		ID:              sourceID,
		ProjectKey:      sql.NullString{String: sourceKey, Valid: true},
		AnchorProjectID: sql.NullString{String: anchorID, Valid: true},
		IdentityScope:   sql.NullString{String: "repository", Valid: true},
		IdentityStatus:  sql.NullString{String: "active", Valid: true},
	}).Error)
	require.NoError(t, db.Create(&Project{
		ID:              targetID,
		ProjectKey:      sql.NullString{String: targetKey, Valid: true},
		AnchorProjectID: sql.NullString{String: uuid.NewString(), Valid: true},
		IdentityScope:   sql.NullString{String: "repository", Valid: true},
		IdentityStatus:  sql.NullString{String: "active", Valid: true},
	}).Error)

	require.Error(t, db.Create(&Project{
		ID:              duplicateKeyID,
		ProjectKey:      sql.NullString{String: sourceKey, Valid: true},
		AnchorProjectID: sql.NullString{String: uuid.NewString(), Valid: true},
	}).Error, "a canonical key may have only one bound project")
	require.Error(t, db.Create(&Project{
		ID:              duplicateAnchorID,
		ProjectKey:      sql.NullString{String: uuid.NewString(), Valid: true},
		AnchorProjectID: sql.NullString{String: anchorID, Valid: true},
	}).Error, "an anchor may have only one bound project")

	identifier := ProjectIdentifier{
		ProjectKey:      sourceKey,
		Scheme:          "anchor_v3",
		NormalizedValue: "anchor-" + uuid.NewString(),
		Source:          "migration-test",
		Provenance:      `{"fixture":true}`,
		Status:          "active",
	}
	require.NoError(t, db.Create(&identifier).Error)
	require.NotEmpty(t, identifier.IdentifierID)
	require.Error(t, db.Create(&ProjectIdentifier{
		ProjectKey:      uuid.NewString(),
		Scheme:          "anchor_v3",
		NormalizedValue: "missing-project-" + uuid.NewString(),
		Source:          "migration-test",
		Provenance:      `{"fixture":true}`,
		Status:          "active",
	}).Error, "identifier project key must resolve to a project")
	require.Error(t, db.Create(&ProjectIdentifier{
		ProjectKey:      targetKey,
		Scheme:          identifier.Scheme,
		NormalizedValue: identifier.NormalizedValue,
		Source:          "migration-test",
		Provenance:      `{"fixture":true}`,
		Status:          "active",
	}).Error, "one active or redirected owner is allowed per identifier")
	require.NoError(t, db.Create(&ProjectIdentifier{
		ProjectKey:      targetKey,
		Scheme:          identifier.Scheme,
		NormalizedValue: identifier.NormalizedValue,
		Source:          "migration-test",
		Provenance:      `{"fixture":true}`,
		Status:          "retired",
	}).Error, "retired evidence remains preservable")

	audit := ProjectMergeAudit{
		TargetProjectKey:  targetKey,
		EvidenceClass:     "migration-test",
		ConflictPolicy:    "quarantine",
		BeforeTableCounts: `{"projects":1}`,
		AfterTableCounts:  `{"projects":1}`,
		Fingerprints:      `{"fixture":"sha256:test"}`,
		PrivacyResult:     "preserved",
		Actor:             "migration-test",
		RollbackBoundary:  "v2-read-boundary",
	}
	require.NoError(t, db.Create(&audit).Error)
	require.NotEmpty(t, audit.MergeID)
	require.Error(t, db.Create(&ProjectMergeAudit{
		TargetProjectKey:  uuid.NewString(),
		EvidenceClass:     "migration-test",
		ConflictPolicy:    "quarantine",
		BeforeTableCounts: `{"projects":1}`,
		AfterTableCounts:  `{"projects":1}`,
		Fingerprints:      `{"fixture":"sha256:test"}`,
		PrivacyResult:     "preserved",
		Actor:             "migration-test",
		RollbackBoundary:  "v2-read-boundary",
	}).Error, "merge audit target must resolve to a project")

	auditSource := ProjectMergeAuditSource{MergeID: audit.MergeID, SourceProjectKey: sourceKey}
	require.NoError(t, db.Create(&auditSource).Error)
	require.Error(t, db.Create(&ProjectMergeAuditSource{
		MergeID:          audit.MergeID,
		SourceProjectKey: uuid.NewString(),
	}).Error, "merge audit source key must resolve to a project")
	require.Error(t, db.Create(&ProjectMergeAuditSource{
		MergeID:          uuid.NewString(),
		SourceProjectKey: sourceKey,
	}).Error, "merge audit source must resolve to an audit")

	// Rollback intentionally preserves all V3 evidence. The unaffected V2 read
	// path remains available without destructive schema reversal.
	require.NoError(t, migration.Rollback(db))
	var persistedV2 Project
	require.NoError(t, db.First(&persistedV2, "id = ?", v2ID).Error)
	require.Equal(t, v2ID, persistedV2.ID)
	var identifierCount, auditCount, auditSourceCount int64
	require.NoError(t, db.Model(&ProjectIdentifier{}).Where("project_key = ?", sourceKey).Count(&identifierCount).Error)
	require.NoError(t, db.Model(&ProjectMergeAudit{}).Where("merge_id = ?", audit.MergeID).Count(&auditCount).Error)
	require.NoError(t, db.Model(&ProjectMergeAuditSource{}).Where("merge_id = ? AND source_project_key = ?", audit.MergeID, sourceKey).Count(&auditSourceCount).Error)
	require.EqualValues(t, 1, identifierCount)
	require.EqualValues(t, 1, auditCount)
	require.EqualValues(t, 1, auditSourceCount)
}
