package gorm

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/internal/issuefixture"
	gormlib "gorm.io/gorm"
)

// Historical migrations 148 and 156 check constraint names across the entire
// database. Independently created private schemas must contain the same actual
// constraints before they can represent an identical-schema restore pair.
func issue513CompletePrivateConstraintFixture(t *testing.T, db *gormlib.DB) {
	t.Helper()
	tables, err := consolidationDataTables(db)
	require.NoError(t, err)
	for _, table := range tables {
		if table.Name == "migrations" {
			continue
		}
		var count int64
		require.NoError(t, db.Table(table.Name).Count(&count).Error)
		require.Zero(t, count, "normalize only empty synthetic fixture schemas")
	}
	for _, statement := range []string{
		`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='api_tokens'::regclass AND conname='api_tokens_principal_kind_chk') THEN ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_principal_kind_chk CHECK (principal_kind IN ('human','agent','service')); END IF; END $$`,
		`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='invitations'::regclass AND conname='invitations_role_chk') THEN ALTER TABLE invitations ADD CONSTRAINT invitations_role_chk CHECK (role IN ('admin','operator')); END IF; END $$`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
}

func issue513LogRestoreError(t *testing.T, stage string, err error) {
	t.Helper()
	if err == nil {
		t.Logf("restore diagnostic stage=%s error_code=none", stage)
		return
	}
	var failure *ConsolidationError
	if errors.As(err, &failure) {
		t.Logf("restore diagnostic stage=%s error_code=%s", stage, failure.Code)
		return
	}
	t.Logf("restore diagnostic stage=%s error_type=%T", stage, err)
}

// Only an unexpected failure or unequal fingerprints invokes these diagnostics.
// Compare the two explicitly owned schemas, never sibling namespace metadata.
// Entry names/digests are bounded to 12 per category; no rows/keys are read.
func issue513LogRestoreStructure(t *testing.T, stage, ownedSchema string, source, target *gormlib.DB, sourceSchema, targetSchema string) {
	t.Helper()
	if ownedSchema == "" {
		t.Logf("restore diagnostic stage=%s skipped=missing_owned_schema", stage)
		return
	}
	for _, db := range []*gormlib.DB{source, target} {
		var actual string
		if err := db.Raw("SELECT current_schema()").Scan(&actual).Error; err != nil {
			issue513LogRestoreError(t, stage+".owned_schema", err)
			return
		}
		if actual != ownedSchema {
			t.Logf("restore diagnostic stage=%s skipped=owned_schema_mismatch", stage)
			return
		}
	}
	if sourceSchema == "" {
		var err error
		sourceSchema, _, _, err = consolidationSchema(source)
		if err != nil {
			issue513LogRestoreError(t, stage+".source_schema", err)
			return
		}
	}
	if targetSchema == "" {
		var err error
		targetSchema, _, _, err = consolidationSchema(target)
		if err != nil {
			issue513LogRestoreError(t, stage+".target_schema", err)
			return
		}
	}
	t.Logf("restore diagnostic stage=%s source_schema_fingerprint=%s target_schema_fingerprint=%s equal=%t", stage, sourceSchema, targetSchema, sourceSchema == targetSchema)
	compare := func(kind string, left, right map[string]string) {
		keys := make(map[string]struct{}, len(left)+len(right))
		for key := range left {
			keys[key] = struct{}{}
		}
		for key := range right {
			keys[key] = struct{}{}
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		differences := 0
		for _, key := range ordered {
			if left[key] != right[key] {
				differences++
				if differences <= 12 {
					t.Logf("restore diagnostic stage=%s kind=%s entry=%s source_digest=%s target_digest=%s", stage, kind, key, left[key], right[key])
				}
			}
		}
		t.Logf("restore diagnostic stage=%s kind=%s source_count=%d target_count=%d differences=%d omitted=%d", stage, kind, len(left), len(right), differences, max(0, differences-12))
	}
	readColumns := func(db *gormlib.DB) ([]consolidationColumn, error) {
		var items []consolidationColumn
		err := db.Raw(`SELECT c.table_name,c.column_name,c.data_type,c.udt_name,c.is_generated,c.is_nullable,c.column_default,
		 format_type(a.atttypid,a.atttypmod) AS type_definition,COALESCE(c.collation_name,'') AS collation_name
		 FROM information_schema.columns c JOIN information_schema.tables t USING(table_schema,table_name)
		 JOIN pg_attribute a ON a.attrelid=to_regclass(quote_ident(c.table_schema)||'.'||quote_ident(c.table_name)) AND a.attname=c.column_name
		 WHERE c.table_schema=? AND t.table_type='BASE TABLE' ORDER BY c.table_name,c.ordinal_position`, ownedSchema).Scan(&items).Error
		return items, err
	}
	sourceColumns, err := readColumns(source)
	if err != nil {
		issue513LogRestoreError(t, stage+".source_columns", err)
		return
	}
	targetColumns, err := readColumns(target)
	if err != nil {
		issue513LogRestoreError(t, stage+".target_columns", err)
		return
	}
	columns := func(items []consolidationColumn) map[string]string {
		values := make(map[string]string, len(items))
		for _, item := range items {
			values[item.TableName+"."+item.ColumnName] = structuralDigest(item)
		}
		return values
	}
	compare("columns", columns(sourceColumns), columns(targetColumns))
	t.Logf("restore diagnostic stage=%s column_order_equal=%t", stage, structuralDigest(sourceColumns) == structuralDigest(targetColumns))
	type definition struct{ TableName, Name, Kind, Definition string }
	definitions := func(db *gormlib.DB) (map[string]string, error) {
		var entries []definition
		err := db.Raw(`SELECT cl.relname AS table_name,c.conname AS name,c.contype::text AS kind,pg_get_constraintdef(c.oid) AS definition
		 FROM pg_constraint c JOIN pg_class cl ON cl.oid=c.conrelid JOIN pg_namespace n ON n.oid=cl.relnamespace WHERE n.nspname=?
		 UNION ALL SELECT tablename,indexname,'index',indexdef FROM pg_indexes WHERE schemaname=?
		 UNION ALL SELECT cl.relname,t.tgname,'trigger',pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN pg_class cl ON cl.oid=t.tgrelid JOIN pg_namespace n ON n.oid=cl.relnamespace WHERE n.nspname=? AND NOT t.tgisinternal
		 UNION ALL SELECT '',p.proname||'('||pg_get_function_identity_arguments(p.oid)||')','routine',pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=? AND p.prokind IN ('f','p')
		 UNION ALL SELECT '',sequencename,'sequence',concat_ws(':',start_value,min_value,max_value,increment_by,cycle,cache_size) FROM pg_sequences WHERE schemaname=?
		 UNION ALL SELECT viewname,viewname,'view',definition FROM pg_views WHERE schemaname=? ORDER BY 1,2,3`, ownedSchema, ownedSchema, ownedSchema, ownedSchema, ownedSchema, ownedSchema).Scan(&entries).Error
		values := make(map[string]string, len(entries))
		for _, item := range entries {
			values[item.TableName+"/"+item.Kind+"/"+item.Name] = structuralDigest(item.Definition)
		}
		return values, err
	}
	sourceDefinitions, err := definitions(source)
	if err != nil {
		issue513LogRestoreError(t, stage+".source_definitions", err)
		return
	}
	targetDefinitions, err := definitions(target)
	if err != nil {
		issue513LogRestoreError(t, stage+".target_definitions", err)
		return
	}
	compare("definitions", sourceDefinitions, targetDefinitions)
	readCatalog := func(db *gormlib.DB) ([]consolidationCatalogObject, error) {
		var items []consolidationCatalogObject
		err := db.Raw(`SELECT 'relation' AS kind,c.relname AS name,jsonb_build_object('owner',pg_get_userbyid(c.relowner),'acl',(SELECT array_agg(a::text ORDER BY a::text) FROM unnest(c.relacl)a),'rls',c.relrowsecurity,'force_rls',c.relforcerowsecurity,'persistence',c.relpersistence,'replica_identity',c.relreplident)::text AS definition
		 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=?
		 UNION ALL SELECT 'trigger',c.relname||'.'||t.tgname,jsonb_build_object('definition',pg_get_triggerdef(t.oid),'enabled',t.tgenabled::text)::text FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=? AND NOT t.tgisinternal
		 UNION ALL SELECT 'policy',c.relname||'.'||p.polname,jsonb_build_object('command',p.polcmd,'permissive',p.polpermissive,'roles',(SELECT array_agg(CASE WHEN r=0 THEN 'PUBLIC' ELSE pg_get_userbyid(r) END ORDER BY CASE WHEN r=0 THEN 'PUBLIC' ELSE pg_get_userbyid(r) END) FROM unnest(p.polroles)r),'using',pg_get_expr(p.polqual,p.polrelid),'check',pg_get_expr(p.polwithcheck,p.polrelid))::text FROM pg_policy p JOIN pg_class c ON c.oid=p.polrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=?
		 UNION ALL SELECT 'enum',t.typname,jsonb_build_object('owner',pg_get_userbyid(t.typowner),'labels',(SELECT array_agg(e.enumlabel ORDER BY e.enumsortorder) FROM pg_enum e WHERE e.enumtypid=t.oid))::text FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname=? AND t.typtype='e'
		 UNION ALL SELECT 'domain',t.typname,jsonb_build_object('owner',pg_get_userbyid(t.typowner),'base',format_type(t.typbasetype,t.typtypmod),'not_null',t.typnotnull,'default',t.typdefault,'constraints',(SELECT array_agg(pg_get_constraintdef(c.oid) ORDER BY c.conname) FROM pg_constraint c WHERE c.contypid=t.oid))::text FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname=? AND t.typtype='d'
		 ORDER BY 1,2,3`, ownedSchema, ownedSchema, ownedSchema, ownedSchema, ownedSchema).Scan(&items).Error
		return items, err
	}
	sourceCatalog, err := readCatalog(source)
	if err != nil {
		issue513LogRestoreError(t, stage+".source_catalog", err)
		return
	}
	targetCatalog, err := readCatalog(target)
	if err != nil {
		issue513LogRestoreError(t, stage+".target_catalog", err)
		return
	}
	catalog := func(items []consolidationCatalogObject) map[string]string {
		values := make(map[string]string, len(items))
		for _, item := range items {
			values[item.Kind+"/"+item.Name] = structuralDigest(item.Definition)
		}
		return values
	}
	compare("catalog", catalog(sourceCatalog), catalog(targetCatalog))
}

// Exercise the supported sealed Census -> Preflight -> Backup -> VerifyBackup
// workflow, not a hand-built INSERT or forged frame. The single-domain cases
// discriminate the two new guards; the combined case is the full deleted-card
// state. The live receipt case is only a restore-writer/storage control.
func TestIssueInternalTasksRetainedRestore(t *testing.T) {
	guards := []struct{ table, name string }{
		{"issue_changes", "engram_issue_change_append_only"},
		{"issue_comments", "engram_issue_insert_actor_required"},
		{"issue_create_receipts", "engram_issue_receipt_immutable"},
		{"issues", "engram_issue_insert_actor_required"},
	}
	for _, test := range []struct {
		name              string
		history           bool
		receipt           bool
		deleted           bool
		alwaysGuards      bool
		guardMode         string
		invalidIdentity   string
		failRestore       bool
		failModeRestore   bool
		invalidTable      string
		failRestoreTable  string
		modeRestoreInsert bool
	}{
		{name: "live_receipt_storage_control", receipt: true},
		{name: "retained_orphan_history", history: true, deleted: true},
		{name: "retained_receipt_tombstone", receipt: true, deleted: true},
		{name: "deleted_card_history_and_tombstone", history: true, receipt: true, deleted: true},
		{name: "live_history_and_receipt_order", history: true, receipt: true, alwaysGuards: true},
		{name: "replica_modes_preserved", history: true, receipt: true, deleted: true, guardMode: "R"},
		{name: "disabled_modes_preserved", history: true, receipt: true, deleted: true, guardMode: "D"},
		{name: "missing_guard_refuses_before_suspension", history: true, receipt: true, deleted: true, invalidIdentity: "missing"},
		{name: "unexpected_guard_identity_refuses_before_suspension", history: true, receipt: true, deleted: true, invalidIdentity: "unexpected"},
		{name: "missing_issue_insert_guard_refuses", history: true, receipt: true, invalidIdentity: "missing", invalidTable: "issues"},
		{name: "unexpected_issue_insert_guard_refuses", history: true, receipt: true, invalidIdentity: "unexpected", invalidTable: "issues"},
		{name: "missing_comment_insert_guard_refuses", history: true, receipt: true, invalidIdentity: "missing", invalidTable: "issue_comments"},
		{name: "unexpected_comment_insert_guard_refuses", history: true, receipt: true, invalidIdentity: "unexpected", invalidTable: "issue_comments"},
		{name: "independent_guard_failure_rolls_back_modes_and_rows", history: true, receipt: true, deleted: true, alwaysGuards: true, failRestore: true},
		{name: "mode_restoration_failure_rolls_back_modes_and_rows", history: true, receipt: true, deleted: true, alwaysGuards: true, failModeRestore: true},
		{name: "insert_mode_restoration_failure_rolls_back", history: true, receipt: true, alwaysGuards: true, failModeRestore: true, modeRestoreInsert: true},
		{name: "independent_issue_insert_guard_remains_active", history: true, receipt: true, alwaysGuards: true, failRestore: true, failRestoreTable: "issues"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sourceDSN := os.Getenv("DATABASE_DSN")
			restoreDSN := os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN")
			if sourceDSN == "" || restoreDSN == "" {
				t.Skip("requires the existing two-database migration186 fixture: DATABASE_DSN and ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN")
			}
			sourceConfig := consolidationTestDatabaseConfig(t, sourceDSN, "source")
			restoreConfig := consolidationTestDatabaseConfig(t, restoreDSN, "restore")
			require.Equal(t, sourceConfig.Host, restoreConfig.Host)
			require.Equal(t, sourceConfig.Port, restoreConfig.Port)
			require.Equal(t, sourceConfig.User, restoreConfig.User, "schema ownership must match the supported restore recipe")
			require.Equal(t, strings.TrimSuffix(sourceConfig.Database, "_source"), strings.TrimSuffix(restoreConfig.Database, "_restore"))
			schema := "issue513_restore_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			source := openMigratedTestDBInSchema(t, sourceDSN, schema, "engram_issue513_restore_test")
			target := openMigratedTestDBInSchema(t, restoreDSN, schema, "engram_issue513_restore_test")
			issue513CompletePrivateConstraintFixture(t, source)
			issue513CompletePrivateConstraintFixture(t, target)
			var key [32]byte
			_, err := rand.Read(key[:])
			require.NoError(t, err)
			vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: hex.EncodeToString(key[:])})
			require.NoError(t, err)
			store := NewProjectConsolidationStore(source, vault, target)
			ctx := t.Context()
			require.NoError(t, store.distinctTarget(ctx), "source and restore must be different real databases")
			for _, db := range []*gormlib.DB{source, target} {
				tables, err := consolidationDataTables(db)
				require.NoError(t, err)
				for _, table := range tables {
					if table.Name == "migrations" {
						continue
					}
					var count int64
					require.NoError(t, db.Table(table.Name).Count(&count).Error)
					require.Zero(t, count, "restore setup requires empty private fixture table %s", table.Name)
				}
			}
			const project = "51300000-0000-4000-8000-000000000097"
			require.NoError(t, source.Create(&Project{ID: project}).Error)
			var issueID int64
			require.NoError(t, issuefixture.SeedHistorical(source, func(tx *gormlib.DB) error {
				var err error
				issueID, err = NewIssueStore(tx).CreateIssue(ctx, &Issue{
					Title: "Synthetic recoverable card", Body: "Synthetic retained body", Type: "task",
					SourceProject: project, TargetProject: project, SourceAgent: "synthetic-restore-agent",
					CreatorKeycardID: "synthetic-restore-creator",
				})
				if err != nil {
					return err
				}
				return tx.Create(&IssueComment{IssueID: issueID, AuthorProject: project, Body: "Synthetic historical unknown-actor comment"}).Error
			}))
			if test.history {
				require.NoError(t, source.Exec(`INSERT INTO issue_changes (issue_id,revision,action,attribution_source_project,attribution_source_agent)
					VALUES (?,1,'created',?,'synthetic-restore-agent')`, issueID, project).Error)
			}
			if test.receipt {
				binding := sha256.Sum256([]byte("synthetic original issue intent"))
				receipt := issueCreateReceipt{
					ActorIdentityKey: "client:synthetic-restore-creator", CanonicalSourceProjectID: project,
					RequestKey: "synthetic-recoverable-intent", IssueID: &issueID, PayloadVersion: 1, PayloadHash: binding[:],
				}
				require.NoError(t, source.Create(&receipt).Error)
			}
			if test.deleted {
				require.NoError(t, NewIssueStore(source).DeleteIssue(ctx, issueID, nil), "accepted explicit store deletion must create the retained state without disabling guards")
				var issues int64
				require.NoError(t, source.Model(&Issue{}).Where("id=?", issueID).Count(&issues).Error)
				require.Zero(t, issues)
				if test.history {
					var changes int64
					require.NoError(t, source.Table("issue_changes").Where("issue_id=?", issueID).Count(&changes).Error)
					require.EqualValues(t, 1, changes)
				}
				if test.receipt {
					var tombstones int64
					require.NoError(t, source.Table("issue_create_receipts").Where("request_key=? AND issue_id IS NULL", "synthetic-recoverable-intent").Count(&tombstones).Error)
					require.EqualValues(t, 1, tombstones)
				}
			}
			if test.alwaysGuards {
				for _, db := range []*gormlib.DB{source, target} {
					for _, guard := range guards {
						require.NoError(t, db.Exec("ALTER TABLE "+quoted(guard.table)+" ENABLE ALWAYS TRIGGER "+quoted(guard.name)).Error)
					}
				}
			}
			if test.guardMode != "" {
				mode := "ENABLE REPLICA"
				if test.guardMode == "D" {
					mode = "DISABLE"
				}
				for _, db := range []*gormlib.DB{source, target} {
					for _, guard := range guards {
						require.NoError(t, db.Exec("ALTER TABLE "+quoted(guard.table)+" "+mode+" TRIGGER "+quoted(guard.name)).Error)
					}
				}
			}
			if test.invalidIdentity != "" {
				for _, db := range []*gormlib.DB{source, target} {
					table, name, events := "issue_changes", "engram_issue_change_append_only", "INSERT OR UPDATE OR DELETE"
					if test.invalidTable != "" {
						table, name, events = test.invalidTable, "engram_issue_insert_actor_required", "INSERT"
					}
					require.NoError(t, db.Exec("DROP TRIGGER "+quoted(name)+" ON "+quoted(table)).Error)
					if test.invalidIdentity == "unexpected" {
						require.NoError(t, db.Exec(`CREATE FUNCTION synthetic_issue513_unexpected() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`).Error)
						require.NoError(t, db.Exec("CREATE TRIGGER "+quoted(name)+" BEFORE "+events+" ON "+quoted(table)+" FOR EACH ROW EXECUTE FUNCTION synthetic_issue513_unexpected()").Error)
					}
				}
			}
			failRestoreTable := test.failRestoreTable
			if failRestoreTable == "" {
				failRestoreTable = "issue_create_receipts"
			}
			if test.failRestore {
				// Install the same independent guard in both signed schemas only
				// after seeding. Exact issue-owner suspension must not disable it.
				for _, db := range []*gormlib.DB{source, target} {
					require.NoError(t, db.Exec(`CREATE FUNCTION synthetic_issue513_restore_refuse() RETURNS trigger LANGUAGE plpgsql AS $$
						BEGIN RAISE EXCEPTION 'synthetic_issue513_restore_refused'; END $$`).Error)
					require.NoError(t, db.Exec("CREATE TRIGGER synthetic_issue513_restore_refusal BEFORE INSERT ON "+quoted(failRestoreTable)+" FOR EACH ROW EXECUTE FUNCTION synthetic_issue513_restore_refuse()").Error)
				}
			}
			snapshot := func(db *gormlib.DB, table string) string {
				t.Helper()
				var value string
				require.NoError(t, db.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+quoted(table)+` t`).Row().Scan(&value))
				return value
			}
			guardModes := func(db *gormlib.DB) string {
				t.Helper()
				var modes string
				require.NoError(t, db.Raw(`SELECT jsonb_agg(jsonb_build_object('table',c.relname,'name',t.tgname,'mode',t.tgenabled,'definition',pg_get_triggerdef(t.oid)) ORDER BY c.relname,t.tgname)::text
					FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
					WHERE n.nspname=current_schema() AND t.tgname IN ('engram_issue_receipt_immutable','engram_issue_change_append_only','engram_issue_insert_actor_required','engram_issue_revision_monotonic','engram_issue_target_session_revision_guard','synthetic_issue513_restore_refusal')`).Row().Scan(&modes))
				if test.guardMode == "" && test.invalidIdentity == "" {
					var enabled int64
					require.NoError(t, db.Raw(`SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
						WHERE n.nspname=current_schema() AND t.tgname IN ('engram_issue_receipt_immutable','engram_issue_change_append_only','engram_issue_insert_actor_required','engram_issue_revision_monotonic','engram_issue_target_session_revision_guard') AND t.tgenabled IN ('O','A')`).Scan(&enabled).Error)
					require.EqualValues(t, 6, enabled, "all issue guards must remain enabled")
				}
				return modes
			}
			assertOrdinaryGuards := func(db *gormlib.DB, history, receipt bool) {
				t.Helper()
				if test.invalidIdentity != "" {
					return
				}
				if test.guardMode == "" {
					issue513AssertOrdinaryRestoreGuards(t, db, project, history, receipt)
					return
				}
				// R/D must remain R/D after restore. Check ordinary enabled-mode
				// admission in a rolled-back test transaction, not by changing them.
				rollback := errors.New("synthetic enabled-mode guard probe rollback")
				err := db.Transaction(func(tx *gormlib.DB) error {
					for _, guard := range guards {
						require.NoError(t, tx.Exec("ALTER TABLE "+quoted(guard.table)+" ENABLE TRIGGER "+quoted(guard.name)).Error)
					}
					issue513AssertOrdinaryRestoreGuards(t, tx, project, history, receipt)
					return rollback
				})
				require.ErrorIs(t, err, rollback)
			}
			original := make(map[string]string)
			for _, table := range []string{"projects", "issues", "issue_comments", "issue_changes", "issue_create_receipts"} {
				original[table] = snapshot(source, table)
			}
			sourceGuards, targetGuards := guardModes(source), guardModes(target)
			assertOrdinaryGuards(source, test.history, test.receipt)
			census, err := store.Census(ctx, "synthetic-issue513-retained-restore")
			require.NoError(t, err, "precondition: actual supported census must accept the synthetic retained state")
			var receiptRoleObserved bool
			for _, row := range census.Rows {
				if row.Family == "issue_create_receipts" && row.Role == "canonical_source_project_id" {
					receiptRoleObserved = true
					t.Logf("receipt canonical-source census classification: %s", row.Reason)
				}
			}
			if test.receipt {
				require.True(t, receiptRoleObserved, "census must inventory the actual receipt source field, not infer its role")
				var receiptReferences []string
				for _, row := range census.Rows {
					if row.Family == "issue_create_receipts" && row.Role == "canonical_source_project_id" {
						receiptReferences = append(receiptReferences, row.Reference)
					}
				}
				claimed, err := store.Preflight(ctx, ConsolidationPreflightRequest{
					CensusReference: census.Reference, CensusFingerprint: census.Fingerprint,
					Evidence: []ConsolidationEvidence{{Reference: "urn:engram:test:receipt-claim", Kind: "portable_marker", Group: "urn:engram:test:receipt-group", ProjectID: uuid.NewString(), Rows: receiptReferences}},
				})
				require.NoError(t, err)
				for _, mapping := range claimed.Mapping {
					if mapping.Family == "issue_create_receipts" && mapping.Role == "canonical_source_project_id" {
						assert.Empty(t, mapping.ProjectID, "unclassified receipt source must remain retained, not gain remapping authority from caller evidence")
					}
				}
			}
			plan, err := store.Preflight(ctx, ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint})
			require.NoError(t, err, "precondition: actual no-evidence retained plan must be admitted")
			for _, mapping := range plan.Mapping {
				if mapping.Family == "issue_create_receipts" && mapping.Role == "canonical_source_project_id" {
					assert.Empty(t, mapping.ProjectID, "this restore probe supplies no authority to remap a receipt namespace")
					t.Logf("receipt canonical-source preflight disposition: %s", mapping.Reason)
				}
			}
			var sealed bytes.Buffer
			require.NoError(t, store.Backup(ctx, plan.Reference, plan.Fingerprint, &sealed), "precondition: use actual encrypted backup writer")
			require.Positive(t, sealed.Len())
			type attemptedInsert struct{ table, code, cause string }
			var attempts []attemptedInsert
			type restoreObservation struct {
				stage, transaction, database string
				backend, locks, disabled     int
			}
			var observations []restoreObservation
			observe := func(tx *gormlib.DB, stage string) {
				t.Helper()
				_, transactional := tx.Statement.ConnPool.(gormlib.TxCommitter)
				require.True(t, transactional, "real restore statements must use one transaction connection")
				sample := restoreObservation{stage: stage}
				require.NoError(t, tx.Statement.ConnPool.QueryRowContext(ctx, `SELECT pg_backend_pid(),txid_current()::text,current_database(),
					(SELECT count(DISTINCT l.relation) FROM pg_locks l JOIN pg_class c ON c.oid=l.relation JOIN pg_namespace n ON n.oid=c.relnamespace
					 WHERE l.pid=pg_backend_pid() AND l.locktype='relation' AND l.mode='AccessExclusiveLock' AND l.granted
					 AND n.nspname=$1 AND c.relname IN ('issue_changes','issue_comments','issue_create_receipts','issues')),
					(SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
					 WHERE n.nspname=$1 AND t.tgenabled='D' AND ((c.relname='issue_changes' AND t.tgname='engram_issue_change_append_only')
					 OR (c.relname='issue_create_receipts' AND t.tgname='engram_issue_receipt_immutable')
					 OR (c.relname IN ('issues','issue_comments') AND t.tgname='engram_issue_insert_actor_required')))`, schema).
					Scan(&sample.backend, &sample.transaction, &sample.database, &sample.locks, &sample.disabled))
				observations = append(observations, sample)
			}
			callback := "synthetic_issue513_restore_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			explicitLock := "LOCK TABLE " + quoted(schema) + "." + quoted("issue_changes") + ", " + quoted(schema) + "." + quoted("issue_comments") + ", " + quoted(schema) + "." + quoted("issue_create_receipts") + ", " + quoted(schema) + "." + quoted("issues") + " IN ACCESS EXCLUSIVE MODE"
			var explicitLockObserved bool
			var guardDDLs int
			modeFailure := errors.New("synthetic issue restore mode restoration failure")
			modeFailureGuard := "engram_issue_change_append_only"
			if test.modeRestoreInsert {
				modeFailureGuard = "engram_issue_insert_actor_required"
			}
			require.NoError(t, target.Callback().Row().Before("gorm:row").Register(callback, func(tx *gormlib.DB) {
				if strings.Contains(tx.Statement.SQL.String(), "t.tgname AS trigger_name,t.tgenabled::text AS mode") {
					require.True(t, explicitLockObserved, "lock all four exact target tables before reading any trigger mode")
					observe(tx, "before_mode_read")
				}
			}))
			require.NoError(t, target.Callback().Raw().Before("gorm:raw").Register(callback+"_before", func(tx *gormlib.DB) {
				statement := tx.Statement.SQL.String()
				if strings.HasPrefix(statement, "ALTER TABLE "+quoted(schema)+".") &&
					(strings.Contains(statement, " TRIGGER "+quoted("engram_issue_change_append_only")) || strings.Contains(statement, " TRIGGER "+quoted("engram_issue_receipt_immutable")) || strings.Contains(statement, " TRIGGER "+quoted("engram_issue_insert_actor_required"))) {
					guardDDLs++
					observe(tx, "guard_ddl")
					if test.failModeRestore && strings.Contains(statement, " ENABLE ALWAYS TRIGGER "+quoted(modeFailureGuard)) {
						tx.AddError(modeFailure)
					}
				}
				for _, table := range []string{"issue_changes", "issue_comments", "issue_create_receipts", "issues"} {
					if strings.HasPrefix(statement, "INSERT INTO "+quoted(table)+" (") {
						observe(tx, table+"_insert")
					}
				}
			}))
			require.NoError(t, target.Callback().Raw().After("gorm:raw").Register(callback, func(tx *gormlib.DB) {
				if tx.Statement.SQL.String() == explicitLock {
					require.NoError(t, tx.Error)
					explicitLockObserved = true
					observe(tx, "locked_tables")
				}
				for _, table := range []string{"issue_changes", "issue_comments", "issue_create_receipts", "issues"} {
					if !strings.HasPrefix(tx.Statement.SQL.String(), "INSERT INTO "+quoted(table)+" (") {
						continue
					}
					attempt := attemptedInsert{table: table}
					var failure *pgconn.PgError
					if errors.As(tx.Error, &failure) {
						attempt.code = failure.Code
						switch failure.Message {
						case "issue receipt requires a live issue on creation":
							attempt.cause = "receipt insert rejects retained tombstone"
						case "issue change requires a live issue on append":
							attempt.cause = "history insert requires missing parent or earlier restore order"
						case "synthetic_issue513_restore_refused":
							attempt.cause = "independent guard remains active"
						default:
							attempt.cause = "other PostgreSQL constraint"
						}
					}
					attempts = append(attempts, attempt)
				}
			}))
			defer func() {
				require.NoError(t, target.Callback().Row().Remove(callback))
				require.NoError(t, target.Callback().Raw().Remove(callback+"_before"))
				require.NoError(t, target.Callback().Raw().Remove(callback))
			}()
			reference, restoreErr := store.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed.Bytes()))
			issue513LogRestoreError(t, "retained.after_verify", restoreErr)
			var expectedConstraint *ConsolidationError
			expectedFailure := test.failModeRestore && errors.Is(restoreErr, modeFailure)
			if errors.As(restoreErr, &expectedConstraint) && expectedConstraint.Code == "PROJECT_ADOPTION_RESTORE_CONSTRAINT_FAILED" {
				if test.invalidIdentity != "" {
					expectedFailure = guardDDLs == 0 && len(attempts) == 0
				} else if test.failRestore {
					var historyInserted, independentRefusal bool
					for _, attempt := range attempts {
						historyInserted = historyInserted || (attempt.table == "issue_changes" && attempt.code == "")
						independentRefusal = independentRefusal || (attempt.table == failRestoreTable && attempt.code == "P0001" && attempt.cause == "independent guard remains active")
					}
					expectedFailure = historyInserted && independentRefusal
				}
			}
			if restoreErr != nil && !expectedFailure {
				issue513LogRestoreStructure(t, "retained.unexpected_failure", schema, source, target, "", "")
			}
			for _, attempt := range attempts {
				t.Logf("actual restore INSERT: table=%s SQLSTATE=%s cause=%s", attempt.table, attempt.code, attempt.cause)
			}
			require.NotEmpty(t, observations, "observe the real restore catalog read before any issue guard is suspended")
			assert.Equal(t, "locked_tables", observations[0].stage)
			for _, sample := range observations {
				assert.Equal(t, restoreConfig.Database, sample.database, "restore guard operations must never use the source database")
				assert.Equal(t, observations[0].backend, sample.backend, "mode reads, DDL and inserts must use one target backend")
				assert.Equal(t, observations[0].transaction, sample.transaction, "mode reads, DDL and inserts must use one transaction")
				assert.Equal(t, 4, sample.locks, "all four exact tables need granted ACCESS EXCLUSIVE locks before mode reads through restore")
				if strings.HasSuffix(sample.stage, "_insert") {
					assert.Equal(t, 4, sample.disabled, "all four exact issue guards must be suspended on the insert backend")
				}
			}
			originalMode := test.guardMode
			if originalMode == "" {
				originalMode = "O"
				if test.alwaysGuards {
					originalMode = "A"
				}
			}
			t.Logf("restore owner proof: observations=%d target_only=true backends=1 transactions=1 access_exclusive_tables=4 original_mode=%s guard_ddl=%d", len(observations), originalMode, guardDDLs)
			assert.Equal(t, sourceGuards, guardModes(source))
			assert.Equal(t, targetGuards, guardModes(target), "restore cannot bypass or leave changed issue guards")
			for table, value := range original {
				assert.True(t, value == snapshot(source, table), "backup/restore must not mutate source %s or dump retained bytes", table)
			}
			if restoreErr != nil {
				var hold *ConsolidationError
				if errors.As(restoreErr, &hold) {
					t.Logf("actual supported restore stop: %s", hold.Code)
				}
				for _, table := range census.Tables {
					if table.Name == "migrations" {
						continue
					}
					var count int64
					require.NoError(t, target.Table(table.Name).Count(&count).Error)
					assert.Zero(t, count, "failed restore must roll back partial rows and recovery receipts in %s", table.Name)
				}
			}
			assertOrdinaryGuards(source, test.history, test.receipt)
			if test.invalidIdentity != "" {
				var hold *ConsolidationError
				require.ErrorAs(t, restoreErr, &hold)
				assert.Equal(t, "PROJECT_ADOPTION_RESTORE_CONSTRAINT_FAILED", hold.Code)
				assert.Zero(t, guardDDLs, "a missing or unexpected guard must refuse before suspending either guard")
				assert.Empty(t, attempts, "invalid guard identity must refuse before inserting retained rows")
				return
			}
			if test.failModeRestore {
				require.ErrorIs(t, restoreErr, modeFailure)
				var historyInserted, receiptInserted bool
				for _, attempt := range attempts {
					historyInserted = historyInserted || (attempt.table == "issue_changes" && attempt.code == "")
					receiptInserted = receiptInserted || (attempt.table == "issue_create_receipts" && attempt.code == "")
				}
				assert.True(t, historyInserted)
				assert.True(t, receiptInserted, "mode restoration failure must roll back already inserted history and receipt rows")
				assertOrdinaryGuards(target, false, false)
				return
			}
			if test.failRestore {
				var hold *ConsolidationError
				require.ErrorAs(t, restoreErr, &hold)
				assert.Equal(t, "PROJECT_ADOPTION_RESTORE_CONSTRAINT_FAILED", hold.Code)
				var historyInserted, independentRefusal bool
				for _, attempt := range attempts {
					historyInserted = historyInserted || (attempt.table == "issue_changes" && attempt.code == "")
					independentRefusal = independentRefusal || (attempt.table == failRestoreTable && attempt.code == "P0001" && attempt.cause == "independent guard remains active")
				}
				assert.True(t, historyInserted, "failure must happen after real retained history insertion, not an earlier issue guard")
				assert.True(t, independentRefusal, "restore must not disable an independent trigger on a guarded table")
				assertOrdinaryGuards(target, false, false)
				return
			}
			require.NoError(t, restoreErr, "real sealed backup restore must conserve legitimate retained issue state with issue guards enabled")
			require.NotEmpty(t, reference)
			for table, value := range original {
				assert.True(t, value == snapshot(target, table), "restore must preserve every %s value without dumping private rows/bindings", table)
			}
			assertOrdinaryGuards(target, test.history, test.receipt)
			assert.Equal(t, sourceGuards, guardModes(source), "ordinary enabled-mode probes must preserve original source trigger modes")
			assert.Equal(t, targetGuards, guardModes(target), "ordinary enabled-mode probes must preserve original target trigger modes")
			t.Log("source retained rows and trigger definitions/modes unchanged; target retained rows conserved; normal enabled-mode forged writes denied")
		})
	}
}

func issue513AssertOrdinaryRestoreGuards(t *testing.T, db *gormlib.DB, project string, history, receipt bool) {
	t.Helper()
	reject := func(code, message string, operation func(*gormlib.DB) error) {
		t.Helper()
		unexpectedSuccess := errors.New("ordinary issue guard probe unexpectedly succeeded")
		err := db.Transaction(func(tx *gormlib.DB) error {
			if err := operation(tx); err != nil {
				return err
			}
			return unexpectedSuccess
		})
		var failure *pgconn.PgError
		if assert.True(t, errors.As(err, &failure), "ordinary probe must be rejected by PostgreSQL, not bypass issue guards") {
			assert.Equal(t, code, failure.Code)
			assert.Equal(t, message, failure.Message)
		}
	}
	const insertRefusal = "new issue and comment rows require a compatible actor-bearing writer"
	reject("23514", insertRefusal, func(tx *gormlib.DB) error {
		if err := tx.Exec(`INSERT INTO projects(id) VALUES (?) ON CONFLICT DO NOTHING`, project).Error; err != nil {
			return err
		}
		_, err := NewIssueStore(tx).CreateIssue(t.Context(), &Issue{Title: "Synthetic incompatible restored-schema create", SourceProject: project, TargetProject: project})
		return err
	})
	reject("23514", insertRefusal, func(tx *gormlib.DB) error {
		if err := tx.Exec(`INSERT INTO projects(id) VALUES (?) ON CONFLICT DO NOTHING`, project).Error; err != nil {
			return err
		}
		// A separate issues INSERT guard must not prevent this comment probe.
		// The absent parent would cause an FK error without the actor guard;
		// reject requires that guard's exact SQLSTATE and message instead.
		// The frozen AddComment regression covers the real writer transaction.
		return tx.Exec(`INSERT INTO issue_comments (issue_id,author_project,body)
			VALUES (9223372036854775807,?,'Synthetic incompatible comment')`, project).Error
	})
	reject("23514", "issue receipt requires a live issue on creation", func(tx *gormlib.DB) error {
		return tx.Exec(`INSERT INTO issue_create_receipts (actor_identity_key,canonical_source_project_id,request_key,payload_version,payload_hash)
			VALUES ('client:synthetic-forged',?,'synthetic-forged-tombstone',1,?)`, project, bytes.Repeat([]byte{0x51}, 32)).Error
	})
	reject("23503", "issue change requires a live issue on append", func(tx *gormlib.DB) error {
		return tx.Exec(`INSERT INTO issue_changes (issue_id,revision,action) VALUES (9223372036854775807,1,'created')`).Error
	})
	if receipt {
		for _, statement := range []string{
			`UPDATE issue_create_receipts SET request_key=request_key WHERE request_key='synthetic-recoverable-intent'`,
			`DELETE FROM issue_create_receipts WHERE request_key='synthetic-recoverable-intent'`,
		} {
			reject("55000", "issue receipt binding and tombstone must be retained", func(tx *gormlib.DB) error {
				return tx.Exec(statement).Error
			})
		}
	}
	if history {
		for _, statement := range []string{
			`UPDATE issue_changes SET action=action`,
			`DELETE FROM issue_changes`,
		} {
			reject("55000", "issue change history must be retained", func(tx *gormlib.DB) error {
				return tx.Exec(statement).Error
			})
		}
	}
}

// A complete, current census can describe a pre-190 schema: absence of the new
// pair is not the older five-control-table inventory omission classification.
func TestIssueInternalTasksPre190Restore(t *testing.T) {
	for _, test := range []struct {
		name             string
		history, receipt bool
		retainedInsert   string
		retainedRevision bool
	}{
		{name: "authenticated_full_inventory_without_issue_tables"},
		{name: "partial_history_only_refuses", history: true},
		{name: "partial_receipt_only_refuses", receipt: true},
		{name: "issue_insert_guard_without_history_pair_refuses", retainedInsert: "issues"},
		{name: "comment_insert_guard_without_history_pair_refuses", retainedInsert: "issue_comments"},
		{name: "both_insert_guards_without_history_pair_refuse", retainedInsert: "both"},
		{name: "revision_guard_without_history_pair_refuses", retainedRevision: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sourceDSN := os.Getenv("DATABASE_DSN")
			restoreDSN := os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN")
			if sourceDSN == "" || restoreDSN == "" {
				t.Skip("requires the two dedicated disposable migration186 fixture databases")
			}
			sourceConfig := consolidationTestDatabaseConfig(t, sourceDSN, "source")
			restoreConfig := consolidationTestDatabaseConfig(t, restoreDSN, "restore")
			require.Equal(t, sourceConfig.Host, restoreConfig.Host)
			require.Equal(t, sourceConfig.Port, restoreConfig.Port)
			require.Equal(t, sourceConfig.User, restoreConfig.User)
			require.Equal(t, strings.TrimSuffix(sourceConfig.Database, "_source"), strings.TrimSuffix(restoreConfig.Database, "_restore"))
			schema := "issue513_pre190_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			source := openMigratedTestDBInSchema(t, sourceDSN, schema, "engram_issue513_pre190_test")
			target := openMigratedTestDBInSchema(t, restoreDSN, schema, "engram_issue513_pre190_test")
			issue513CompletePrivateConstraintFixture(t, source)
			issue513CompletePrivateConstraintFixture(t, target)
			for _, db := range []*gormlib.DB{source, target} {
				require.NoError(t, db.Exec("DROP TRIGGER engram_issue_target_session_revision_guard ON "+quoted(schema)+".issues").Error)
				require.NoError(t, db.Exec("DROP FUNCTION "+quoted(schema)+".engram_issue_target_session_revision_guard()").Error)
				// These are newly created private synthetic schemas only. Rebuild
				// the pre-feature projection, never alter a retained fixture/source.
				for _, table := range []string{"issues", "issue_comments"} {
					if test.retainedInsert != table && test.retainedInsert != "both" {
						require.NoError(t, db.Exec("DROP TRIGGER engram_issue_insert_actor_required ON "+quoted(schema)+"."+quoted(table)).Error)
					}
				}
				if test.retainedInsert == "" {
					require.NoError(t, db.Exec("DROP FUNCTION "+quoted(schema)+".engram_issue_insert_actor_required()").Error)
				}
				if !test.history {
					require.NoError(t, db.Exec("DROP TABLE "+quoted(schema)+"."+quoted("issue_changes")).Error)
				}
				if !test.receipt {
					require.NoError(t, db.Exec("DROP TABLE "+quoted(schema)+"."+quoted("issue_create_receipts")).Error)
				}
				if !test.history && !test.receipt {
					if !test.retainedRevision {
						require.NoError(t, db.Exec("DROP TRIGGER engram_issue_revision_monotonic ON "+quoted(schema)+"."+quoted("issues")).Error)
						require.NoError(t, db.Exec("DROP FUNCTION "+quoted(schema)+".engram_issue_revision_monotonic()").Error)
					}
					for _, function := range []string{"engram_issue_receipt_immutable", "engram_issue_change_append_only"} {
						require.NoError(t, db.Exec("DROP FUNCTION "+quoted(schema)+"."+quoted(function)+"()").Error)
					}
					if test.retainedInsert == "" && !test.retainedRevision {
						for table, columns := range map[string][]string{
							"issues":         {"metadata", "revision", "owner_label", "recipient_label", "actor_kind", "actor_reference"},
							"issue_comments": {"metadata", "actor_kind", "actor_reference"},
						} {
							for _, column := range columns {
								require.NoError(t, db.Exec("ALTER TABLE "+quoted(schema)+"."+quoted(table)+" DROP COLUMN "+quoted(column)).Error)
							}
						}
					}
				}
				require.NoError(t, db.Exec(`DELETE FROM migrations WHERE id=?`, issueInternalTasksMigrationID).Error)
			}
			const project = "51300000-0000-4000-8000-000000000095"
			require.NoError(t, source.Exec(`INSERT INTO projects(id) VALUES (?)`, project).Error)
			if test.retainedInsert == "" && !test.retainedRevision {
				require.NoError(t, source.Exec(`INSERT INTO issues(id,title,body,source_project,target_project,source_agent)
					VALUES (8514,'Synthetic pre-feature card','Synthetic pre-feature body',?,?,'synthetic-pre190-agent')`, project, project).Error)
				require.NoError(t, source.Exec(`INSERT INTO issue_comments(id,issue_id,author_project,author_agent,body)
					VALUES (9514,8514,?,'synthetic-pre190-agent','Synthetic pre-feature comment')`, project).Error)
			}
			// Corrupt-schema cases deliberately contain no issue rows: without the
			// classifier refusal, matching sealed catalogs would restore successfully.
			if test.history {
				require.NoError(t, source.Exec(`INSERT INTO issue_changes(issue_id,revision,action) VALUES (8514,1,'created')`).Error)
			}
			if test.receipt {
				require.NoError(t, source.Exec(`INSERT INTO issue_create_receipts(actor_identity_key,canonical_source_project_id,request_key,issue_id,payload_version,payload_hash)
					VALUES ('client:synthetic-pre190',?,'synthetic-pre190-request',8514,1,?)`, project, bytes.Repeat([]byte{0x51}, 32)).Error)
			}
			var key [32]byte
			_, err := rand.Read(key[:])
			require.NoError(t, err)
			vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: hex.EncodeToString(key[:])})
			require.NoError(t, err)
			store := NewProjectConsolidationStore(source, vault, target)
			ctx := t.Context()
			require.NoError(t, store.distinctTarget(ctx))
			sourceSchema, _, _, err := consolidationSchema(source)
			require.NoError(t, err)
			targetSchema, _, _, err := consolidationSchema(target)
			require.NoError(t, err)
			if sourceSchema != targetSchema {
				issue513LogRestoreStructure(t, "pre190.schema_mismatch", schema, source, target, sourceSchema, targetSchema)
			}
			require.Equal(t, sourceSchema, targetSchema, "real source/target schemas must match before sealing")
			census, err := store.Census(ctx, "synthetic-issue513-pre190-restore")
			require.NoError(t, err)
			var inventory consolidationCensusState
			require.NoError(t, store.open(census.Reference, "census", &inventory))
			historical, err := consolidationHistoricalCensus(inventory)
			require.NoError(t, err)
			require.False(t, historical, "a full authenticated pre-190 inventory is not the old omitted-control-table recipe")
			var historyPresent, receiptPresent bool
			var metadataTables int
			for _, table := range inventory.Tables {
				historyPresent = historyPresent || table.Name == "issue_changes"
				receiptPresent = receiptPresent || table.Name == "issue_create_receipts"
				if consolidationMetadataTable(table.Name) {
					metadataTables++
				}
			}
			require.Equal(t, test.history, historyPresent)
			require.Equal(t, test.receipt, receiptPresent)
			require.Equal(t, 5, metadataTables, "the real signed census must contain the full control inventory")
			plan, err := store.Preflight(ctx, ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint})
			require.NoError(t, err)
			var sealed bytes.Buffer
			require.NoError(t, store.Backup(ctx, plan.Reference, plan.Fingerprint, &sealed))
			snapshot := func(db *gormlib.DB, table string) string {
				t.Helper()
				var rows string
				require.NoError(t, db.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+quoted(table)+` t`).Row().Scan(&rows))
				return rows
			}
			original := make(map[string]string)
			for _, table := range inventory.Tables {
				original[table.Name] = snapshot(source, table.Name)
			}
			var guardDDLs int
			var triggerDDLs int
			callback := "synthetic_issue513_pre190_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			require.NoError(t, target.Callback().Raw().After("gorm:raw").Register(callback, func(tx *gormlib.DB) {
				statement := tx.Statement.SQL.String()
				if strings.HasPrefix(statement, "ALTER TABLE ") && strings.Contains(statement, " TRIGGER ") {
					triggerDDLs++
				}
				if strings.HasPrefix(statement, "ALTER TABLE "+quoted(schema)+".") &&
					(strings.Contains(statement, " TRIGGER "+quoted("engram_issue_change_append_only")) || strings.Contains(statement, " TRIGGER "+quoted("engram_issue_receipt_immutable")) || strings.Contains(statement, " TRIGGER "+quoted("engram_issue_insert_actor_required")) || strings.Contains(statement, " TRIGGER "+quoted("engram_issue_revision_monotonic"))) {
					guardDDLs++
				}
				if strings.HasPrefix(statement, "LOCK TABLE "+quoted(schema)+".") {
					var failure *pgconn.PgError
					if errors.As(tx.Error, &failure) {
						t.Logf("actual restore issue-owner lock failure: SQLSTATE=%s", failure.Code)
					}
				}
			}))
			defer func() { require.NoError(t, target.Callback().Raw().Remove(callback)) }()
			reference, restoreErr := store.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed.Bytes()))
			t.Logf("authenticated pre-feature proof: full_control_inventory=%d legacy_omission_recipe=%t history_present=%t receipt_present=%t issue_guard_ddl=%d all_trigger_ddl=%d", metadataTables, historical, historyPresent, receiptPresent, guardDDLs, triggerDDLs)
			assert.Zero(t, guardDDLs, "absent or partial issue-table pairs must never cause guard suspension")
			for _, table := range inventory.Tables {
				assert.True(t, original[table.Name] == snapshot(source, table.Name), "restore must not mutate any source table or disclose its rows: %s", table.Name)
				if restoreErr != nil && table.Name != "migrations" {
					var count int64
					require.NoError(t, target.Table(table.Name).Count(&count).Error)
					assert.Zero(t, count, "a refused partial restore must roll back all target data and control records: %s", table.Name)
				}
			}
			afterSourceSchema, _, _, err := consolidationSchema(source)
			require.NoError(t, err)
			afterTargetSchema, _, _, err := consolidationSchema(target)
			require.NoError(t, err)
			assert.Equal(t, sourceSchema, afterSourceSchema)
			assert.Equal(t, targetSchema, afterTargetSchema, "success or rollback must preserve every target guard definition/mode")
			if test.history || test.receipt || test.retainedInsert != "" || test.retainedRevision {
				require.Error(t, restoreErr, "an authenticated partial feature must fail closed, never fall back to an absent-feature recipe")
				var hold *ConsolidationError
				require.ErrorAs(t, restoreErr, &hold, "a partial feature must have an explicit owner refusal, not a missing-table SQL error")
				assert.Equal(t, "PROJECT_ADOPTION_RESTORE_CONSTRAINT_FAILED", hold.Code)
				assert.Zero(t, triggerDDLs, "partial issue feature must refuse before any restore trigger DDL")
				return
			}
			require.NoError(t, restoreErr, "real sealed backup of a matching full pre-190 schema with neither issue table must remain restorable")
			require.NotEmpty(t, reference)
			for _, table := range inventory.Tables {
				if table.Name != "migrations" && !consolidationMetadataTable(table.Name) {
					assert.True(t, original[table.Name] == snapshot(target, table.Name), "pre-feature restore must conserve all actual data: %s", table.Name)
				}
			}
		})
	}
}
