package gorm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
)

func TestProjectConsolidationMigration186WriteScopeMatrix(t *testing.T) {
	consolidationTestDatabaseConfig(t, os.Getenv("DATABASE_DSN"), "source")
	db, _ := openInterventionReceiptMigrationTestDB(t)
	require.NoError(t, projectConsolidationMigration186().Migrate(db))
	a, b := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Create(&Project{ID: a}).Error)
	require.NoError(t, db.Create(&Project{ID: b}).Error)
	for _, redirect := range []struct{ selector, session, target string }{
		{"mixed", "session-a", a},
		{"mixed", "session-a2", a},
		{"mixed", "session-b", b},
		{"global-alias", "", a},
		{"conflict", "", a},
		{"conflict", "session-b", b},
	} {
		require.NoError(t, consolidationRedirect(db, redirect.selector, redirect.session, redirect.target, "matrix"))
	}
	require.NoError(t, db.Exec(`INSERT INTO project_consolidation_memberships
		(reference,unresolved_reference,table_name,row_key,field_path,selector_reference,plan_fingerprint)
		VALUES ('unrelated','unrelated','memories','{"id":-1}','["project"]','unrelated','matrix')`).Error)

	for _, tc := range []struct{ name, selector, session, target, code string }{
		{"no-session", "mixed", "", "", "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP"},
		{"unknown-session", "mixed", "unknown", "", "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP"},
		{"exact-a", "mixed", "session-a", a, ""},
		{"exact-b", "mixed", "session-b", b, ""},
		{"conflicting-global-exact", "conflict", "session-b", "", "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP"},
		{"global-redirect", "global-alias", "unknown", a, ""},
		{"missing-historical", "historical-only", "", "", "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP"},
		{"nil-uuid", uuid.Nil.String(), "", "", "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP"},
		{"canonical", b, "unknown", b, ""},
		{"empty-global", "", "unknown", "", ""},
	} {
		t.Run("scope/"+tc.name, func(t *testing.T) {
			var actual string
			err := db.Raw(`SELECT engram_consolidation_scope(?,?)`, tc.selector, tc.session).Row().Scan(&actual)
			if tc.code != "" {
				assertConsolidationSQLRefusal(t, err, tc.code)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.target, actual)
		})
	}

	for _, tc := range []struct {
		name, selector, target, code string
		sessions                     pq.StringArray
	}{
		{"no-session", "mixed", "", "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP", pq.StringArray{}},
		{"unknown", "mixed", "", "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP", pq.StringArray{"unknown"}},
		{"exact-a", "mixed", a, "", pq.StringArray{"session-a"}},
		{"exact-b", "mixed", b, "", pq.StringArray{"session-b"}},
		{"all-agree", "mixed", a, "", pq.StringArray{"session-a2", "session-a", "session-a"}},
		{"known-and-unknown", "mixed", "", "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP", pq.StringArray{"session-a", "unknown"}},
		{"conflicting-sessions", "mixed", "", "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP", pq.StringArray{"session-a", "session-b"}},
		{"global-exact-conflict", "conflict", "", "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP", pq.StringArray{"session-b"}},
		{"canonical", b, b, "", pq.StringArray{"session-a", "unknown"}},
		{"empty-global", "", "", "", pq.StringArray{"session-a", "session-b"}},
		{"missing-historical", "historical-only", "", "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP", pq.StringArray{}},
	} {
		t.Run("scalar/"+tc.name, func(t *testing.T) {
			row := Memory{
				Project: tc.selector, Content: "insert-" + tc.name, SourceSessions: tc.sessions,
				SourceAgent: "matrix-agent", EditedBy: "matrix-actor", PrivacyScope: "private", SourceWorkstationID: "matrix-workstation",
			}
			err := db.Create(&row).Error
			if tc.code != "" {
				assertConsolidationSQLRefusal(t, err, tc.code)
				var count int64
				require.NoError(t, db.Model(&Memory{}).Where("content = ?", row.Content).Count(&count).Error)
				require.Zero(t, count, "rejected insert must leave no row")
			} else {
				require.NoError(t, err)
				var actual Memory
				require.NoError(t, db.First(&actual, row.ID).Error)
				require.Equal(t, tc.target, actual.Project)
				require.Equal(t, tc.sessions, actual.SourceSessions)
				require.Equal(t, row.SourceAgent, actual.SourceAgent)
				require.Equal(t, row.EditedBy, actual.EditedBy)
				require.Equal(t, row.PrivacyScope, actual.PrivacyScope)
				require.Equal(t, row.SourceWorkstationID, actual.SourceWorkstationID)
			}

			baseline := Memory{Project: a, Content: "baseline-" + tc.name, SourceSessions: pq.StringArray{"baseline-session"}, EditedBy: "retained-actor"}
			require.NoError(t, db.Create(&baseline).Error)
			before := consolidationTestRowJSON(t, db, "memories", baseline.ID)
			err = db.Model(&Memory{}).Where("id = ?", baseline.ID).Updates(map[string]any{
				"project": tc.selector, "content": "updated-" + tc.name, "source_sessions": tc.sessions,
			}).Error
			if tc.code != "" {
				assertConsolidationSQLRefusal(t, err, tc.code)
				require.JSONEq(t, before, consolidationTestRowJSON(t, db, "memories", baseline.ID), "rejected update must change no field")
			} else {
				require.NoError(t, err)
				var actual Memory
				require.NoError(t, db.First(&actual, baseline.ID).Error)
				require.Equal(t, baseline.ID, actual.ID)
				require.Equal(t, tc.target, actual.Project)
				require.Equal(t, tc.sessions, actual.SourceSessions)
				require.Equal(t, baseline.EditedBy, actual.EditedBy)
			}
			// A refusal must not poison the connection or make a subsequent valid write fail.
			require.NoError(t, db.Model(&Memory{}).Where("id = ?", baseline.ID).Update("project", b).Error)
		})
	}

	for _, tc := range []struct {
		name, session, code string
		values, targets     pq.StringArray
	}{
		{"no-session", "", "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP", pq.StringArray{b, "mixed"}, nil},
		{"unknown", "unknown", "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP", pq.StringArray{b, "mixed"}, nil},
		{"exact-a", "session-a", "", pq.StringArray{"", b, "mixed", "mixed", a}, pq.StringArray{"", b, a, a, a}},
		{"exact-b", "session-b", "", pq.StringArray{a, "mixed", "", "mixed"}, pq.StringArray{a, b, "", b}},
		{"global-conflict", "session-b", "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP", pq.StringArray{a, "conflict"}, nil},
		{"missing-member", "session-a", "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP", pq.StringArray{"mixed", "historical-only"}, nil},
		{"canonical-global", "unknown", "", pq.StringArray{b, "", a, b}, pq.StringArray{b, "", a, b}},
		{"empty-array", "unknown", "", pq.StringArray{}, pq.StringArray{}},
	} {
		t.Run("array/"+tc.name, func(t *testing.T) {
			row := candidateRow{SourceSessionID: tc.session, ProposedContent: "array-insert-" + tc.name, AffectedProjects: tc.values}
			err := db.Create(&row).Error
			if tc.code != "" {
				assertConsolidationSQLRefusal(t, err, tc.code)
				var count int64
				require.NoError(t, db.Model(&candidateRow{}).Where("proposed_content = ?", row.ProposedContent).Count(&count).Error)
				require.Zero(t, count)
			} else {
				require.NoError(t, err)
				var actual candidateRow
				require.NoError(t, db.First(&actual, row.ID).Error)
				require.Equal(t, tc.targets, actual.AffectedProjects)
				require.Equal(t, tc.session, actual.SourceSessionID)
			}
			baseline := candidateRow{SourceSessionID: "retained-session", ProposedContent: "array-baseline-" + tc.name, AffectedProjects: pq.StringArray{a}}
			require.NoError(t, db.Create(&baseline).Error)
			before := consolidationTestRowJSON(t, db, "crystallization_candidates", baseline.ID)
			err = db.Model(&candidateRow{}).Where("id = ?", baseline.ID).Updates(map[string]any{
				"source_session_id": tc.session, "proposed_content": "array-updated-" + tc.name, "affected_projects": tc.values,
			}).Error
			if tc.code != "" {
				assertConsolidationSQLRefusal(t, err, tc.code)
				require.JSONEq(t, before, consolidationTestRowJSON(t, db, "crystallization_candidates", baseline.ID))
			} else {
				require.NoError(t, err)
				var actual candidateRow
				require.NoError(t, db.First(&actual, baseline.ID).Error)
				require.Equal(t, baseline.ID, actual.ID)
				require.Equal(t, tc.targets, actual.AffectedProjects)
				require.Equal(t, tc.session, actual.SourceSessionID)
			}
			require.NoError(t, db.Model(&candidateRow{}).Where("id = ?", baseline.ID).Update("affected_projects", pq.StringArray{b}).Error)
		})
	}

	conflicting := SDKSession{Project: "mixed", ClaudeSessionID: "session-a", SDKSessionID: sql.NullString{String: "session-b", Valid: true}}
	assertConsolidationSQLRefusal(t, db.Create(&conflicting).Error, "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP")
	var sessionCount int64
	require.NoError(t, db.Model(&SDKSession{}).Count(&sessionCount).Error)
	require.Zero(t, sessionCount)
	agreeing := SDKSession{Project: "mixed", ClaudeSessionID: "session-a", SDKSessionID: sql.NullString{String: "session-a2", Valid: true}, PromptCounter: 17}
	require.NoError(t, db.Create(&agreeing).Error)
	var actual SDKSession
	require.NoError(t, db.First(&actual, agreeing.ID).Error)
	require.Equal(t, a, actual.Project)
	require.Equal(t, agreeing.ClaudeSessionID, actual.ClaudeSessionID)
	require.Equal(t, agreeing.SDKSessionID, actual.SDKSessionID)
	require.Equal(t, agreeing.PromptCounter, actual.PromptCounter)
}

func TestProjectConsolidationMigration186RetainedRestoreAndPartialArray(t *testing.T) {
	sourceDSN, restoreDSN := os.Getenv("DATABASE_DSN"), os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN")
	if sourceDSN == "" || restoreDSN == "" {
		t.Skip("DATABASE_DSN and ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN must name two dedicated empty PostgreSQL databases")
	}
	sourceConfig := consolidationTestDatabaseConfig(t, sourceDSN, "source")
	targetConfig := consolidationTestDatabaseConfig(t, restoreDSN, "restore")
	require.Equal(t, sourceConfig.Host, targetConfig.Host, "fixture pair must use the same admitted loopback node")
	require.Equal(t, sourceConfig.Port, targetConfig.Port)
	require.Equal(t, strings.TrimSuffix(sourceConfig.Database, "_source"), strings.TrimSuffix(targetConfig.Database, "_restore"))
	sourceStore, closeSource := openIntegrationTestDB(t)
	t.Cleanup(closeSource)
	t.Setenv("DATABASE_DSN", restoreDSN)
	targetStore, closeTarget := openIntegrationTestDB(t)
	t.Cleanup(closeTarget)
	source, target := sourceStore.GetDB(), targetStore.GetDB()
	ctx := context.Background()
	vault, err := vaultcrypto.NewVault(&config.Config{EncryptionKey: "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"})
	require.NoError(t, err)
	store := NewProjectConsolidationStore(source, vault, target)
	require.NoError(t, store.distinctTarget(ctx), "restore must never use the source database")
	for _, db := range []*gormlib.DB{source, target} {
		var schema string
		require.NoError(t, db.Raw(`SELECT current_schema()`).Scan(&schema).Error)
		require.Equal(t, "public", schema, "schema-qualified index definitions must match across the two isolated databases")
		tables, err := consolidationDataTables(db)
		require.NoError(t, err)
		for _, table := range tables {
			var count int64
			require.NoError(t, db.Table(table.Name).Count(&count).Error)
			require.Zero(t, count, "retention fixture requires empty dedicated data table %s", table.Name)
		}
	}
	memory := Memory{Project: "historical-only", Content: "retained-memory", SourceAgent: "retained-agent", EditedBy: "retained-actor", SourceSessions: pq.StringArray{"retained-session"}}
	candidate := candidateRow{SourceSessionID: "retained-session", ProposedContent: "retained-candidate", AffectedProjects: pq.StringArray{"mapped", "retained", "mapped"}}
	// Seed pre-cutover data without changing any normal client write path.
	require.NoError(t, source.Transaction(func(tx *gormlib.DB) error {
		restore, err := consolidationSuspendWriteGuards(tx)
		if err != nil {
			return err
		}
		if err := tx.Create(&memory).Error; err != nil {
			return err
		}
		candidate.PromotedMemoryID = &memory.ID
		if err := tx.Create(&candidate).Error; err != nil {
			return err
		}
		return restore()
	}))
	originalMemory := consolidationTestRowJSON(t, source, "memories", memory.ID)
	originalCandidate := consolidationTestRowJSON(t, source, "crystallization_candidates", candidate.ID)
	require.NoError(t, projectConsolidationMigration186().Migrate(source), "installation retry must retain unresolved historical rows")
	require.JSONEq(t, originalMemory, consolidationTestRowJSON(t, source, "memories", memory.ID))
	require.JSONEq(t, originalCandidate, consolidationTestRowJSON(t, source, "crystallization_candidates", candidate.ID))

	for _, db := range []*gormlib.DB{source, target} {
		for _, mode := range []struct{ table, action string }{
			{"sdk_sessions", "ENABLE REPLICA"}, {"memories", "ENABLE ALWAYS"}, {"behavioral_rules", "DISABLE"},
		} {
			require.NoError(t, db.Exec("ALTER TABLE "+quoted(mode.table)+" "+mode.action+" TRIGGER engram_consolidation_scope_write").Error)
		}
		// This independent guard must still run during the historical restore.
		require.NoError(t, db.Exec(`CREATE FUNCTION consolidation_test_copy_refusal() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'fixture-copy-refused'; END $$`).Error)
		require.NoError(t, db.Exec(`CREATE TRIGGER consolidation_test_copy_guard BEFORE INSERT ON crystallization_candidates
			FOR EACH ROW EXECUTE FUNCTION consolidation_test_copy_refusal()`).Error)
	}
	guardModes := func(db *gormlib.DB) []struct{ TableName, Mode string } {
		var modes []struct{ TableName, Mode string }
		require.NoError(t, db.Raw(`SELECT c.relname AS table_name,t.tgenabled::text AS mode FROM pg_trigger t
			JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=current_schema() AND t.tgname='engram_consolidation_scope_write' ORDER BY c.relname`).Scan(&modes).Error)
		return modes
	}
	beforeModes := guardModes(target)
	canonical := uuid.NewString()
	census, err := store.Census(ctx, "migration186-retention")
	require.NoError(t, err)
	var censusState consolidationCensusState
	require.NoError(t, store.open(census.Reference, "census", &censusState))
	var mappedReference string
	for _, row := range censusState.Rows {
		if row.Table == "crystallization_candidates" && row.Column == "affected_projects" && len(row.Path) == 1 && row.Path[0] == "0" {
			mappedReference = row.Reference
		}
	}
	require.NotEmpty(t, mappedReference)
	plan, err := store.Preflight(ctx, ConsolidationPreflightRequest{
		CensusReference: census.Reference, CensusFingerprint: census.Fingerprint,
		Evidence: []ConsolidationEvidence{{Reference: "urn:engram:test:marker", Kind: "portable_marker", Group: "urn:engram:test:group", ProjectID: canonical, Rows: []string{mappedReference}}},
	})
	require.NoError(t, err)
	backup, err := store.Backup(ctx, plan.Reference, plan.Fingerprint)
	require.NoError(t, err)
	backupPlaintext, err := vault.Decrypt(backup)
	require.NoError(t, err)
	var backupState consolidationBackupState
	require.NoError(t, json.Unmarshal([]byte(backupPlaintext), &backupState))
	var sequenceNames []string
	require.NoError(t, source.Raw(`SELECT sequencename FROM pg_sequences WHERE schemaname=current_schema() ORDER BY sequencename`).Scan(&sequenceNames).Error)
	require.Len(t, backupState.Sequences, len(sequenceNames))
	for i, sequence := range backupState.Sequences {
		require.Equal(t, sequenceNames[i], sequence.Name, "sealed backup must retain the actual sequence identity")
	}
	t.Logf("sealed backup sequence names: %v", sequenceNames)
	_, err = store.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, backup)
	var hold *ConsolidationError
	require.ErrorAs(t, err, &hold)
	require.Equal(t, "PROJECT_ADOPTION_RESTORE_CONSTRAINT_FAILED", hold.Code)
	require.Equal(t, beforeModes, guardModes(target), "failed copy must roll back exact trigger modes")
	for _, table := range []string{"memories", "crystallization_candidates", "project_consolidation_backups"} {
		var count int64
		require.NoError(t, target.Table(table).Count(&count).Error)
		require.Zero(t, count, "failed copy must retain no partial rows or receipt")
	}
	var otherMode string
	require.NoError(t, target.Raw(`SELECT t.tgenabled::text FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
		JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND t.tgname='consolidation_test_copy_guard'`).Scan(&otherMode).Error)
	require.Equal(t, "O", otherMode, "owned suspension must never disable the independent guard")
	for _, db := range []*gormlib.DB{source, target} {
		require.NoError(t, db.Exec(`ALTER TABLE crystallization_candidates DISABLE TRIGGER consolidation_test_copy_guard`).Error)
	}
	backupReference, err := store.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, backup)
	require.NoError(t, err, "isolated restore must preserve unresolved historical data and actual FK references")
	require.Equal(t, beforeModes, guardModes(target))
	restoredSequences, err := consolidationSequences(target)
	require.NoError(t, err)
	require.Equal(t, backupState.Sequences, restoredSequences, "verified restore must conserve sequence identities and state")
	require.JSONEq(t, originalMemory, consolidationTestRowJSON(t, target, "memories", memory.ID))
	require.JSONEq(t, originalCandidate, consolidationTestRowJSON(t, target, "crystallization_candidates", candidate.ID))
	assertConsolidationSQLRefusal(t, target.Create(&Memory{Project: "historical-only", Content: "forbidden-new", SourceSessions: pq.StringArray{}}).Error, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")

	request := ConsolidationApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, BackupReference: backupReference, IdempotencyKey: uuid.NewString()}
	_, err = store.DryRun(ctx, request)
	require.NoError(t, err)
	require.JSONEq(t, originalCandidate, consolidationTestRowJSON(t, target, "crystallization_candidates", candidate.ID), "dry-run must roll back mapped occurrence")
	require.Equal(t, beforeModes, guardModes(target))

	// Force a real mapping failure after its row UPDATE, not before the guarded transaction.
	require.NoError(t, target.Create(&ConsolidationMembership{Reference: mappedReference, Table: "crystallization_candidates", RowKey: JSONRaw(`{"id":0}`), FieldPath: JSONRaw(`["affected_projects","0"]`), PlanFingerprint: "conflicting-fixture-plan"}).Error)
	_, err = store.Apply(ctx, request)
	require.ErrorAs(t, err, &hold)
	require.Equal(t, "PROJECT_ADOPTION_STALE_MAPPING", hold.Code)
	require.JSONEq(t, originalCandidate, consolidationTestRowJSON(t, target, "crystallization_candidates", candidate.ID))
	require.Equal(t, beforeModes, guardModes(target), "failed mapping must roll back trigger DDL as well as row UPDATE")
	var partialProjects, receipts int64
	require.NoError(t, target.Model(&Project{}).Where("id = ?", canonical).Count(&partialProjects).Error)
	require.NoError(t, target.Model(&consolidationOperation{}).Count(&receipts).Error)
	require.Zero(t, partialProjects)
	require.Zero(t, receipts)
	require.NoError(t, target.Where("reference = ?", mappedReference).Delete(&ConsolidationMembership{}).Error)

	_, err = store.Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, beforeModes, guardModes(target), "successful apply must restore exact modes before receipt")
	var expected map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(originalCandidate), &expected))
	expected["affected_projects"], err = json.Marshal([]string{canonical, "retained", "mapped"})
	require.NoError(t, err)
	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err)
	require.JSONEq(t, string(expectedJSON), consolidationTestRowJSON(t, target, "crystallization_candidates", candidate.ID), "only the proven occurrence may change; preserve row key, timestamps, FK and session provenance")
	require.JSONEq(t, originalMemory, consolidationTestRowJSON(t, target, "memories", memory.ID))
	var retained []ConsolidationMembership
	require.NoError(t, target.Where("table_name = ? AND project_id IS NULL", "crystallization_candidates").Find(&retained).Error)
	require.Len(t, retained, 2, "unresolved array occurrences remain separately addressable")
	assertConsolidationSQLRefusal(t, target.Model(&candidateRow{}).Where("id = ?", candidate.ID).Update("proposed_content", "forbidden-update").Error, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
	require.JSONEq(t, string(expectedJSON), consolidationTestRowJSON(t, target, "crystallization_candidates", candidate.ID))
	assertConsolidationSQLRefusal(t, target.Create(&candidateRow{ProposedContent: "forbidden-array", SourceSessionID: "retained-session", AffectedProjects: pq.StringArray{"retained"}}).Error, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
	require.NoError(t, target.Create(&candidateRow{ProposedContent: "canonical-after-refusal", SourceSessionID: "unknown", AffectedProjects: pq.StringArray{canonical}}).Error)
	invalidFK := candidateRow{ProposedContent: "invalid-fk", AffectedProjects: pq.StringArray{canonical}}
	missingMemoryID := memory.ID + 1000000
	invalidFK.PromotedMemoryID = &missingMemoryID
	var fkError *pgconn.PgError
	require.ErrorAs(t, target.Create(&invalidFK).Error, &fkError)
	require.Equal(t, "23503", fkError.Code, "ordinary FK protection stays active")
}

func assertConsolidationSQLRefusal(t *testing.T, err error, code string) {
	t.Helper()
	var sqlError *pgconn.PgError
	require.True(t, errors.As(err, &sqlError), "expected a PostgreSQL machine error, got %T: %v", err, err)
	require.Equal(t, "P0001", sqlError.Code)
	require.Equal(t, code, sqlError.Message)
}

func consolidationTestRowJSON(t *testing.T, db *gormlib.DB, table string, id int64) string {
	t.Helper()
	var row string
	require.NoError(t, db.Raw("SELECT to_jsonb(t)::text FROM "+quoted(table)+" t WHERE id=?", id).Row().Scan(&row))
	return row
}

func consolidationTestDatabaseConfig(t *testing.T, dsn, role string) *pgx.ConnConfig {
	t.Helper()
	if dsn == "" {
		t.Skip("migration186 PostgreSQL fixture DSN is not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid migration186 fixture DSN")
	}
	require.True(t, cfg.Host == "127.0.0.1" || cfg.Host == "localhost" || cfg.Host == "::1", "migration186 fixtures require an admitted loopback PostgreSQL node")
	const prefix = "engram_migration186_scope_test_"
	suffix := "_" + role
	require.True(t, strings.HasPrefix(cfg.Database, prefix) && strings.HasSuffix(cfg.Database, suffix) && len(cfg.Database) > len(prefix)+len(suffix), "refusing a database outside the migration186 dedicated test namespace")
	require.True(t, cfg.RuntimeParams["search_path"] == "" || cfg.RuntimeParams["search_path"] == "public", "migration186 fixtures require the public schema")
	require.Empty(t, cfg.Fallbacks, "fixture DSN must not fall back to another PostgreSQL target")
	return cfg
}
