package gorm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	// Equal private schema names preserve schema-qualified fingerprints in distinct databases.
	schema := "migration186_retention_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	source := openMigratedTestDBInSchema(t, sourceDSN, schema, "engram_migration186_retention_test")
	target := openMigratedTestDBInSchema(t, restoreDSN, schema, "engram_migration186_retention_test")
	ctx := context.Background()
	vault, err := vaultcrypto.NewVault(&config.Config{EncryptionKey: "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"})
	require.NoError(t, err)
	store := NewProjectConsolidationStore(source, vault, target)
	require.NoError(t, store.distinctTarget(ctx), "restore must never use the source database")
	for _, db := range []*gormlib.DB{source, target} {
		var actualSchema string
		require.NoError(t, db.Raw(`SELECT current_schema()`).Scan(&actualSchema).Error)
		require.Equal(t, schema, actualSchema, "the isolated databases must use matching private schemas")
		tables, err := consolidationDataTables(db)
		require.NoError(t, err)
		for _, table := range tables {
			if table.Name == "migrations" {
				continue
			}
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
	backup, err := os.OpenFile(filepath.Join(t.TempDir(), "retained-backup.sealed"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	require.NoError(t, err)
	originalBackup := backup
	t.Cleanup(func() { _ = originalBackup.Close() })
	require.NoError(t, store.Backup(ctx, plan.Reference, plan.Fingerprint, backup))
	_, err = backup.Seek(0, io.SeekStart)
	require.NoError(t, err)
	_, header, err := store.readFrame(backup)
	require.NoError(t, err)
	boundPlan, err := store.plan(plan.Reference, plan.Fingerprint)
	require.NoError(t, err)
	backupState, err := store.backupManifest(header, boundPlan)
	require.NoError(t, err)
	var sequenceNames []string
	require.NoError(t, source.Raw(`SELECT sequencename FROM pg_sequences WHERE schemaname=current_schema() ORDER BY sequencename`).Scan(&sequenceNames).Error)
	require.Len(t, backupState.Sequences, len(sequenceNames))
	for i, sequence := range backupState.Sequences {
		require.Equal(t, sequenceNames[i], sequence.Name, "sealed backup must retain the actual sequence identity")
	}
	t.Logf("sealed backup sequence names: %v", sequenceNames)
	_, err = backup.Seek(0, io.SeekStart)
	require.NoError(t, err)
	_, err = store.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, backup)
	var hold *ConsolidationError
	require.ErrorAs(t, err, &hold)
	require.Equal(t, "PROJECT_ADOPTION_RESTORE_CONSTRAINT_FAILED", hold.Code)
	require.Equal(t, beforeModes, guardModes(target), "failed copy must roll back exact trigger modes")
	for _, table := range []string{"memories", "crystallization_candidates", "project_consolidation_backups", "project_consolidation_frames"} {
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
	// Trigger modes are signed schema inputs; deliberate fixture DDL needs a fresh plan and backup.
	census, err = store.Census(ctx, "migration186-retention-retry")
	require.NoError(t, err)
	plan, err = store.Preflight(ctx, ConsolidationPreflightRequest{
		CensusReference: census.Reference, CensusFingerprint: census.Fingerprint,
		Evidence: []ConsolidationEvidence{{Reference: "urn:engram:test:marker", Kind: "portable_marker", Group: "urn:engram:test:group", ProjectID: canonical, Rows: []string{mappedReference}}},
	})
	require.NoError(t, err)
	retryBackup, err := os.OpenFile(filepath.Join(t.TempDir(), "retained-retry-backup.sealed"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = retryBackup.Close() })
	backup = retryBackup
	require.NoError(t, store.Backup(ctx, plan.Reference, plan.Fingerprint, backup))
	_, err = backup.Seek(0, io.SeekStart)
	require.NoError(t, err)
	backupReference, err := store.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, backup)
	require.NoError(t, err, "isolated restore must preserve unresolved historical data and actual FK references")
	require.Equal(t, beforeModes, guardModes(target))
	restoredSequences, err := consolidationSequences(target)
	require.NoError(t, err)
	require.Equal(t, backupState.Sequences, restoredSequences, "verified restore must conserve sequence identities and state")
	require.JSONEq(t, originalMemory, consolidationTestRowJSON(t, target, "memories", memory.ID))
	require.JSONEq(t, originalCandidate, consolidationTestRowJSON(t, target, "crystallization_candidates", candidate.ID))
	var refusedIDCount int64
	require.NoError(t, target.Model(&Memory{}).Where("id = ?", memory.ID+1).Count(&refusedIDCount).Error)
	require.Zero(t, refusedIDCount, "refused probe ID must not collide with a restored row")
	assertConsolidationSQLRefusal(t, target.Create(&Memory{ID: memory.ID + 1, Project: "historical-only", Content: "forbidden-new", SourceSessions: pq.StringArray{}}).Error, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
	restoredSequences, err = consolidationSequences(target)
	require.NoError(t, err)
	require.Equal(t, backupState.Sequences, restoredSequences, "refused row probe must preserve the verified restore's sequence state")

	request := ConsolidationApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, BackupReference: backupReference, IdempotencyKey: uuid.NewString()}
	_, err = store.DryRun(ctx, request)
	require.NoError(t, err)
	require.JSONEq(t, originalCandidate, consolidationTestRowJSON(t, target, "crystallization_candidates", candidate.ID), "dry-run must roll back mapped occurrence")
	require.Equal(t, beforeModes, guardModes(target))

	// Inject only after freshness checks: pre-Apply metadata edits correctly invalidate the verified snapshot.
	mappingMutationReached := false
	func() {
		callbacks := target.Callback().Raw()
		const callbackName = "retention_mapping_rollback"
		armed := true
		require.NoError(t, callbacks.Before("gorm:raw").Register(callbackName, func(tx *gormlib.DB) {
			if !armed || !strings.HasPrefix(tx.Statement.SQL.String(), `UPDATE "crystallization_candidates" t SET`) {
				return
			}
			armed = false
			probe := tx.Session(&gormlib.Session{NewDB: true})
			updated := probe.Exec(`UPDATE crystallization_candidates SET affected_projects[1]=? WHERE id=?`, canonical, candidate.ID)
			mappingMutationReached = updated.Error == nil && updated.RowsAffected == 1
			tx.AddError(updated.Error)
			tx.AddError(probe.Exec(`TRUNCATE pg_temp.engram_owner_row`).Error)
		}))
		defer func() { require.NoError(t, callbacks.Remove(callbackName)) }()
		_, err = store.Apply(ctx, request)
	}()
	require.True(t, mappingMutationReached, "failure must follow a real mapped row UPDATE inside guarded Apply")
	require.ErrorAs(t, err, &hold)
	require.Equal(t, "PROJECT_ADOPTION_STALE_MAPPING", hold.Code)
	require.JSONEq(t, originalCandidate, consolidationTestRowJSON(t, target, "crystallization_candidates", candidate.ID))
	require.Equal(t, beforeModes, guardModes(target), "failed mapping must roll back trigger DDL as well as row UPDATE")
	var partialProjects, receipts int64
	require.NoError(t, target.Model(&Project{}).Where("id = ?", canonical).Count(&partialProjects).Error)
	require.NoError(t, target.Model(&consolidationOperation{}).Count(&receipts).Error)
	require.Zero(t, partialProjects)
	require.Zero(t, receipts)

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

func TestIssueProjectWriteScopeMigration189UpgradeAndReplay(t *testing.T) {
	db, schema := openInterventionReceiptMigrationTestDB(t)
	snapshot := func(query string, args ...any) string {
		t.Helper()
		var value string
		require.NoError(t, db.Raw(query, args...).Row().Scan(&value))
		return value
	}
	project := uuid.NewString()
	require.NoError(t, db.Create(&Project{ID: project}).Error)
	issue := Issue{Title: "preserved forward migration issue", SourceProject: project, TargetProject: project, SourceAgent: "original-agent"}
	require.NoError(t, db.Create(&issue).Error)
	require.NoError(t, db.Create(&IssueComment{IssueID: issue.ID, AuthorProject: project, AuthorAgent: "original-agent", Body: "preserved comment"}).Error)
	// Reconstruct the already-applied 188 trigger state through its original owner.
	require.NoError(t, projectConsolidationMigration186().Migrate(db))
	migration := issueProjectWriteScopeMigration189()
	deleted := db.Exec(`DELETE FROM migrations WHERE id = ?`, migration.ID)
	require.NoError(t, deleted.Error)
	require.EqualValues(t, 1, deleted.RowsAffected)
	functionsSQL := `SELECT jsonb_agg(jsonb_build_object('oid', p.oid, 'definition', pg_get_functiondef(p.oid)) ORDER BY p.proname)::text
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = current_schema() AND p.proname IN ('engram_consolidation_scope', 'engram_consolidation_write_guard')`
	otherTriggersSQL := `SELECT jsonb_agg(jsonb_build_object('oid', t.oid, 'mode', t.tgenabled, 'definition', pg_get_triggerdef(t.oid)) ORDER BY c.relname, t.tgname)::text
		FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relname <> 'issues' AND NOT t.tgisinternal`
	issueTriggerSQL := `SELECT pg_get_triggerdef(oid) FROM pg_trigger WHERE tgrelid = 'issues'::regclass AND tgname = 'engram_consolidation_scope_write'`
	dataSQL := `SELECT jsonb_build_object(
		'issues', (SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM issues i),
		'comments', (SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM issue_comments c),
		'memberships', (SELECT jsonb_agg(to_jsonb(m) ORDER BY reference) FROM project_consolidation_memberships m),
		'redirects', (SELECT jsonb_agg(to_jsonb(r) ORDER BY selector, session_id) FROM project_consolidation_redirects r)
	)::text`
	functionsBefore, otherTriggersBefore, dataBefore := snapshot(functionsSQL), snapshot(otherTriggersSQL), snapshot(dataSQL)
	require.NotContains(t, snapshot(issueTriggerSQL), "UPDATE OF")
	require.NoError(t, runMigrations(db), "normal startup must apply the pending forward migration after 188")
	var applied186, applied188, applied189 int
	require.NoError(t, db.Raw(`SELECT count(*) FROM migrations WHERE id = '186_project_consolidation'`).Scan(&applied186).Error)
	require.NoError(t, db.Raw(`SELECT count(*) FROM migrations WHERE id = '188_memory_principal_constraints'`).Scan(&applied188).Error)
	require.NoError(t, db.Raw(`SELECT count(*) FROM migrations WHERE id = ?`, migration.ID).Scan(&applied189).Error)
	require.Equal(t, 1, applied186)
	require.Equal(t, 1, applied188)
	require.Equal(t, 1, applied189)
	triggerAfter := snapshot(issueTriggerSQL)
	require.Contains(t, triggerAfter, "BEFORE INSERT OR UPDATE OF source_project, target_project")
	require.Contains(t, triggerAfter, "engram_consolidation_write_guard('source_project', 'target_project')")
	require.Contains(t, triggerAfter, "ON "+schema+".issues")
	require.JSONEq(t, functionsBefore, snapshot(functionsSQL), "both scope functions must retain their exact OIDs and definitions")
	require.JSONEq(t, otherTriggersBefore, snapshot(otherTriggersSQL), "comment-author and all other triggers must remain unchanged")
	require.JSONEq(t, dataBefore, snapshot(dataSQL), "forward DDL must not rewrite rows, comments or memberships")
	ledgerAfter := snapshot(`SELECT jsonb_agg(id ORDER BY id)::text FROM migrations`)
	require.NoError(t, runMigrations(db), "completed startup replay must be a no-op")
	require.NoError(t, migration.Migrate(db), "the one trigger replacement must be idempotent")
	require.ErrorContains(t, migration.Rollback(db), "retained issue metadata writes must remain independent")
	require.Equal(t, triggerAfter, snapshot(issueTriggerSQL))
	require.JSONEq(t, ledgerAfter, snapshot(`SELECT jsonb_agg(id ORDER BY id)::text FROM migrations`))
	require.JSONEq(t, functionsBefore, snapshot(functionsSQL))
	require.JSONEq(t, otherTriggersBefore, snapshot(otherTriggersSQL))
	require.JSONEq(t, dataBefore, snapshot(dataSQL))
	t.Log("migration 189: original 188 trigger upgraded by normal startup; scope functions, other triggers and durable data unchanged; replay idempotent; rollback refused")
}
