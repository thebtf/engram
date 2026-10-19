package gorm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	streamIntegrityLease      = "r1-stream-integrity-20261017-01a0db09"
	streamIntegrityBaseline   = "6109befb6cba841d9aaabd34fb99eb0826aa40d5"
	streamIntegrityOriginal   = "original"
	streamIntegrityOracle     = "oracle"
	streamIntegrityHistorical = "historical-reader"
)

var streamIntegrityBaselineFiles = map[string]string{
	"project_consolidation.go":         "4b0e47614e7589356b5bc1a732e87a8a2c1bbd8b92d2985abee21db907518732",
	"project_consolidation_census.go":  "a2767f39d41b2c4a6f861c4c12f154fb1b99c406feeadcd7e4ea7bed61847e81",
	"project_consolidation_stream.go":  "71f10adde7cb0cbae93fe1f3a5791b2fc214137ae6c3165b0110ecf829ab26eb",
	"project_consolidation_restore.go": "c8ae3141ed2889b19f3d07163bae7dc9c4c0039a9b1eb31e8d4e85cfb3f5cb3a",
	"project_consolidation_native.go":  "3df11a30cc0e09d17174fed383dc863bd719d380de30d59e0539f3d0941a9d97",
}

type streamIntegrityFixture struct {
	store          *ProjectConsolidationStore
	source, target *gormlib.DB
	ctx            context.Context
	directory      string
	schema         string
}

type streamIntegritySequence struct {
	Name      string
	LastValue int64
	IsCalled  bool
}

type streamIntegrityState struct {
	Rows        map[string][]string
	Versions    map[string][]string
	Sequences   []streamIntegritySequence
	Objects     []string
	Definitions []string
}

func streamIntegrityFixtureRoot(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	require.NoError(t, err, "resolve the primary fixture owner through the worktree's native Git common directory")
	commonDirectory := filepath.Clean(strings.TrimSpace(string(output)))
	require.True(t, filepath.IsAbs(commonDirectory) && filepath.Base(commonDirectory) == ".git", "fixture requires an absolute primary repository Git directory")
	owner := "pr568-stream-integrity-20261017"
	if os.Getenv("ENGRAM_CONSOLIDATION_PREPARED_FIXTURE") != "" {
		owner = "r1-census-prepared-20261018"
	}
	return filepath.Join(filepath.Dir(commonDirectory), ".agent", "artifacts", "data", owner)
}

// No ambient DATABASE_DSN, old fixture admission or cleanup is inherited here.
func openStreamIntegrityFixture(t *testing.T, binding, schema string, create bool) *streamIntegrityFixture {
	t.Helper()
	path, deadlineText := os.Getenv("ENGRAM_STREAM_INTEGRITY_FIXTURE"), os.Getenv("ENGRAM_STREAM_INTEGRITY_DEADLINE")
	isolated := os.Getenv("ENGRAM_CONSOLIDATION_PREPARED_FIXTURE") != ""
	if isolated {
		require.Equal(t, streamIntegrityOracle, binding, "prepared fixtures cannot replace retained historical inputs")
		path, deadlineText = os.Getenv("ENGRAM_CONSOLIDATION_PREPARED_FIXTURE"), os.Getenv("ENGRAM_CONSOLIDATION_PREPARED_DEADLINE")
	}
	if path == "" && deadlineText == "" {
		t.Skip("no assigned stream-integrity fixture; this is not RED or conservation proof")
	}
	if path == "" || deadlineText == "" {
		t.Fatal("explicit stream-integrity assignment is incomplete")
	}
	var configurationName, identityName, sourceDatabase, targetDatabase, sourceOID, targetOID string
	switch binding {
	case streamIntegrityOriginal:
		configurationName, identityName = "fixture.json", "fixture-identity.json"
		sourceDatabase, targetDatabase = "engram_migration186_scope_test_stream_20261017_source", "engram_migration186_scope_test_stream_20261017_restore"
		sourceOID, targetOID = "16389", "16390"
	case streamIntegrityOracle:
		configurationName, identityName = "oracle-fixture.json", "oracle-fixture-identity.json"
		sourceDatabase, targetDatabase = "engram_migration186_scope_test_stream_oracle_20261017_source", "engram_migration186_scope_test_stream_oracle_20261017_restore"
		sourceOID, targetOID = "22326", "22327"
	case streamIntegrityHistorical:
		configurationName, identityName = "fixture.json", "fixture-identity.json"
		sourceDatabase, targetDatabase = "engram_migration186_scope_test_stream_20261017_source", "engram_migration186_scope_test_stream_20261017_restore"
		sourceOID, targetOID = "16389", "16390"
	default:
		t.Fatal("unknown stream-integrity fixture binding")
	}
	lease, system, user, port := streamIntegrityLease, "7697507119866282022", "stream_fixture", uint16(22417)
	if isolated {
		configurationName, identityName = "fixture.json", "fixture-identity.json"
		sourceDatabase, targetDatabase = "engram_migration186_scope_test_prepared_source", "engram_migration186_scope_test_prepared_restore"
		lease, user, port = "r1-census-prepared-20261018", "prepared_fixture", 22479
	}
	absolute, err := filepath.Abs(path)
	require.NoError(t, err)
	root := streamIntegrityFixtureRoot(t)
	require.Equal(t, filepath.Join(root, ".private", configurationName), absolute, "only the exact selected fixture is admitted")
	deadline, err := time.Parse(time.RFC3339, deadlineText)
	require.NoError(t, err)
	require.True(t, time.Now().Before(deadline), "explicit fixture execution deadline expired")
	require.LessOrEqual(t, time.Until(deadline), 30*time.Minute, "bounded fixture execution only")
	var private struct{ Lease, SourceDSN, TargetDSN, KeyPath, Artifacts string }
	raw, err := os.ReadFile(absolute)
	if err != nil || json.Unmarshal(raw, &private) != nil {
		t.Fatal("private stream-integrity fixture configuration unavailable or invalid")
	}
	require.Equal(t, lease, private.Lease)
	require.Equal(t, root, filepath.Clean(private.Artifacts))
	require.Equal(t, filepath.Join(root, ".private", "vault.key"), filepath.Clean(private.KeyPath))
	var identity struct {
		Lease, SystemIdentifier, SourceOID, TargetOID string
		DatabaseIdentity                              []string
	}
	raw, err = os.ReadFile(filepath.Join(private.Artifacts, identityName))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &identity))
	require.Equal(t, lease, identity.Lease)
	if isolated {
		require.NotEmpty(t, identity.SystemIdentifier)
		require.NotEmpty(t, identity.SourceOID)
		require.NotEmpty(t, identity.TargetOID)
		require.NotEqual(t, identity.SourceOID, identity.TargetOID)
		system, sourceOID, targetOID = identity.SystemIdentifier, identity.SourceOID, identity.TargetOID
	}
	require.Len(t, identity.DatabaseIdentity, 2)
	require.ElementsMatch(t, []string{system + "|" + sourceOID + "|" + sourceDatabase, system + "|" + targetOID + "|" + targetDatabase}, identity.DatabaseIdentity)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)
	open := func(dsn, role, database, oid string) *gormlib.DB {
		cfg := consolidationTestDatabaseConfig(t, dsn, role)
		require.Equal(t, "127.0.0.1", cfg.Host)
		require.EqualValues(t, port, cfg.Port)
		require.Equal(t, database, cfg.Database)
		require.Equal(t, user, cfg.User)
		cfg.RuntimeParams["application_name"] = lease
		cfg.RuntimeParams["row_security"] = "off"
		cfg.RuntimeParams["TimeZone"] = "UTC"
		cfg.RuntimeParams["statement_timeout"] = "60000"
		cfg.RuntimeParams["lock_timeout"] = "5000"
		if binding == streamIntegrityHistorical && role == "source" {
			cfg.RuntimeParams["default_transaction_read_only"] = "on"
		}
		admin := stdlib.OpenDB(*cfg)
		admin.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, admin.Close()) })
		var actual string
		err := admin.QueryRowContext(ctx, `SELECT (SELECT system_identifier::text FROM pg_control_system())||'|'||oid::text||'|'||datname FROM pg_database WHERE datname=current_database()`).Scan(&actual)
		if err != nil {
			t.Fatal("assigned fixture identity connection failed; not a protected regression failure")
		}
		require.Equal(t, system+"|"+oid+"|"+database, actual, "physical fixture identity changed")
		var maintenance bool
		require.NoError(t, admin.QueryRowContext(ctx, `SELECT rolsuper AND rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&maintenance))
		require.True(t, maintenance, "fixture needs full maintenance visibility, not permission-dependent refusal")
		createSchema := create
		if createSchema {
			_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+quoted(schema))
			require.NoError(t, err, "never replace an existing fixture schema")
		} else {
			var exists bool
			require.NoError(t, admin.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists))
			require.True(t, exists, "required retained source or separately admitted matching historical target schema is missing; never recreate original inputs")
		}
		copy := cfg.Copy()
		copy.RuntimeParams["search_path"] = schema + ", public"
		pool := stdlib.OpenDB(*copy)
		pool.SetMaxOpenConns(2)
		pool.SetMaxIdleConns(1)
		pool.SetConnMaxLifetime(time.Minute)
		t.Cleanup(func() { require.NoError(t, pool.Close()) })
		db, err := gormlib.Open(postgres.New(postgres.Config{Conn: pool}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
		require.NoError(t, err)
		db = db.WithContext(ctx)
		if createSchema {
			require.NoError(t, runMigrations(db))
		}
		return db
	}
	source := open(private.SourceDSN, "source", sourceDatabase, sourceOID)
	target := open(private.TargetDSN, "restore", targetDatabase, targetOID)
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKeyFile: private.KeyPath})
	require.NoError(t, err)
	directory := filepath.Join(private.Artifacts, ".private", schema)
	if create {
		require.NoError(t, os.Mkdir(directory, 0o700), "retain every run, including failed runs")
	}
	return &streamIntegrityFixture{NewProjectConsolidationStore(source, vault, target), source, target, ctx, directory, schema}
}

func streamIntegrityJSON(t *testing.T, raw string) string {
	t.Helper()
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &object))
	canonical, err := json.Marshal(object)
	require.NoError(t, err)
	return string(canonical)
}

// The denominator is every base table from PostgreSQL, never the exporter's list.
func streamIntegrityRead(t *testing.T, db *gormlib.DB) streamIntegrityState {
	t.Helper()
	state := streamIntegrityState{Rows: map[string][]string{}, Versions: map[string][]string{}}
	require.NoError(t, db.Transaction(func(tx *gormlib.DB) error {
		if err := tx.Exec("SET LOCAL row_security=off; SET LOCAL TimeZone='UTC'; SET LOCAL extra_float_digits=3").Error; err != nil {
			return err
		}
		var tables []string
		if err := tx.Raw(`SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relkind IN ('r','p') ORDER BY c.relname`).Scan(&tables).Error; err != nil {
			return err
		}
		for _, table := range tables {
			state.Rows[table], state.Versions[table] = []string{}, []string{}
			rows, err := tx.Raw("SELECT to_jsonb(t)::text,t.xmin::text FROM " + quoted(table) + " t ORDER BY to_jsonb(t)::text COLLATE \"C\",t.xmin::text").Rows()
			if err != nil {
				return err
			}
			for rows.Next() {
				var body, version string
				if err := rows.Scan(&body, &version); err != nil {
					rows.Close()
					return err
				}
				body = streamIntegrityJSON(t, body)
				state.Rows[table] = append(state.Rows[table], body)
				state.Versions[table] = append(state.Versions[table], body+"@xmin="+version)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			sort.Strings(state.Rows[table])
			sort.Strings(state.Versions[table])
		}
		var names []string
		if err := tx.Raw(`SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relkind='S' ORDER BY c.relname`).Scan(&names).Error; err != nil {
			return err
		}
		for _, name := range names {
			sequence := streamIntegritySequence{Name: name}
			if err := tx.Raw("SELECT last_value,is_called FROM "+quoted(name)).Row().Scan(&sequence.LastValue, &sequence.IsCalled); err != nil {
				return err
			}
			state.Sequences = append(state.Sequences, sequence)
		}
		if err := tx.Raw(`SELECT jsonb_build_object('table',c.relname,'owner',pg_get_userbyid(c.relowner),'acl',(SELECT array_agg(a::text ORDER BY a::text) FROM unnest(c.relacl)a),'rls',c.relrowsecurity,'force_rls',c.relforcerowsecurity)::text FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() ORDER BY c.relname`).Scan(&state.Objects).Error; err != nil {
			return err
		}
		var triggers, policies []string
		if err := tx.Raw(`SELECT jsonb_build_object('table',c.relname,'trigger',t.tgname,'enabled',t.tgenabled::text,'definition',pg_get_triggerdef(t.oid))::text FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND NOT t.tgisinternal ORDER BY c.relname,t.tgname`).Scan(&triggers).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT jsonb_build_object('table',c.relname,'policy',p.polname,'command',p.polcmd,'permissive',p.polpermissive,'roles',(SELECT array_agg(CASE WHEN r=0 THEN 'PUBLIC' ELSE pg_get_userbyid(r) END ORDER BY r) FROM unnest(p.polroles)r),'using',pg_get_expr(p.polqual,p.polrelid),'check',pg_get_expr(p.polwithcheck,p.polrelid))::text FROM pg_policy p JOIN pg_class c ON c.oid=p.polrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() ORDER BY c.relname,p.polname`).Scan(&policies).Error; err != nil {
			return err
		}
		state.Objects = append(state.Objects, triggers...)
		state.Objects = append(state.Objects, policies...)
		return tx.Raw(`SELECT c.relname||':'||t.tgname||':'||pg_get_triggerdef(t.oid) AS definition FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND NOT t.tgisinternal
		 UNION ALL SELECT cl.relname||':'||c.conname||':'||pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class cl ON cl.oid=c.conrelid JOIN pg_namespace n ON n.oid=cl.relnamespace WHERE n.nspname=current_schema()
		 UNION ALL SELECT tablename||':'||indexname||':'||indexdef FROM pg_indexes WHERE schemaname=current_schema() ORDER BY 1`).Scan(&state.Definitions).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}))
	require.NotEmpty(t, state.Rows, "an empty denominator is not proof")
	return state
}

func streamIntegritySave(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	streamIntegrityWrite(t, path, raw)
}

func streamIntegrityWrite(t *testing.T, path string, raw []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	require.NoError(t, err, "never overwrite retained baseline inputs or failed-run evidence")
	_, err = file.Write(raw)
	require.NoError(t, err)
	require.NoError(t, file.Sync())
	require.NoError(t, file.Close())
}

func streamIntegrityCiphers(t *testing.T, raw []byte) [][]byte {
	t.Helper()
	reader := bytes.NewReader(raw)
	var frames [][]byte
	for reader.Len() != 0 {
		var length uint32
		require.NoError(t, binary.Read(reader, binary.BigEndian, &length))
		require.GreaterOrEqual(t, length, uint32(28))
		require.LessOrEqual(t, length, uint32(512<<10))
		cipher := make([]byte, int(length))
		_, err := io.ReadFull(reader, cipher)
		require.NoError(t, err)
		frames = append(frames, cipher)
	}
	require.GreaterOrEqual(t, len(frames), 2)
	return frames
}

func streamIntegrityPlan(t *testing.T, f *streamIntegrityFixture, db *gormlib.DB) (ConsolidationPlan, []byte) {
	t.Helper()
	store := NewProjectConsolidationStore(db, f.store.vault, nil)
	census, err := store.Census(f.ctx, streamIntegrityBaseline)
	require.NoError(t, err)
	plan, err := store.Preflight(f.ctx, ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint})
	require.NoError(t, err)
	var sealed bytes.Buffer
	require.NoError(t, store.Backup(f.ctx, plan.Reference, plan.Fingerprint, &sealed))
	return plan, sealed.Bytes()
}

func streamIntegritySeed(t *testing.T, f *streamIntegrityFixture, history bool) string {
	t.Helper()
	project := uuid.NewString()
	require.NoError(t, f.source.Create(&Project{ID: project}).Error)
	secret, err := f.store.vault.Encrypt("private synthetic stream-integrity credential")
	require.NoError(t, err)
	credential, err := NewCredentialStore(&Store{DB: f.source}).Create(f.ctx, &models.Credential{Project: project, Key: "stream-integrity", EncryptedSecret: secret, EncryptionKeyFingerprint: f.store.vault.Fingerprint(), Scope: "project", Version: 7, EditedBy: "synthetic-original-actor"})
	require.NoError(t, err)
	historical := json.RawMessage(`{"project":"legacy-stream-history","actor":"synthetic-original-actor","version":3}`)
	audit := AuditLogEntry{Action: "retained-stream-history", Actor: "synthetic-original-actor", SourceSessionID: "synthetic-original-session", BeforeState: &historical, Reason: "retain exact serialized provenance"}
	require.NoError(t, f.source.Create(&audit).Error)
	if !history {
		return project
	}
	oldPlan, oldBytes := streamIntegrityPlan(t, f, f.source)
	state, err := f.store.plan(oldPlan.Reference, oldPlan.Fingerprint)
	require.NoError(t, err)
	for _, row := range state.Census.Rows {
		if !((row.Table == "credentials" && row.Column == "project") || (row.Table == "audit_log" && row.Column == "")) {
			continue
		}
		path, err := json.Marshal(append([]string{row.Column}, row.Path...))
		require.NoError(t, err)
		member := ConsolidationMembership{Reference: row.Reference, Table: row.Table, RowKey: row.Key, SourceRowKey: row.Key, FieldPath: JSONRaw(path), SelectorReference: row.SelectorReference, ProjectID: &project, Reason: "retained_original_evidence", PlanFingerprint: oldPlan.Fingerprint, NativeVersion: row.NativeVersion, SourceSnapshot: state.Census.NativeSnapshot}
		require.NoError(t, f.source.Create(&member).Error)
	}
	require.NoError(t, f.source.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES('legacy-stream-history','',?,?)`, project, oldPlan.Fingerprint).Error)
	streamIntegrityRetainArchive(t, f, f.source, oldPlan, oldBytes)
	operationID := uuid.NewString()
	receipt, err := json.Marshal(ConsolidationReceipt{OperationID: operationID, Snapshot: state.Census.Snapshot, RollbackBoundary: consolidationRollbackBoundary})
	require.NoError(t, err)
	require.NoError(t, f.source.Create(&consolidationOperation{IdempotencyKey: uuid.NewString(), OperationID: operationID, PlanID: state.ID, PlanFingerprint: state.Fingerprint, Receipt: JSONRaw(receipt), CompletedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}).Error)
	read, err := NewCredentialStore(&Store{DB: f.source}).GetByID(f.ctx, credential.ID)
	require.NoError(t, err)
	require.True(t, bytes.Equal(secret, read.EncryptedSecret), "original synthetic ciphertext remains byte-identical")
	return project
}

func streamIntegrityRetainArchive(t *testing.T, f *streamIntegrityFixture, db *gormlib.DB, plan ConsolidationPlan, raw []byte) {
	t.Helper()
	frames := streamIntegrityCiphers(t, raw)
	state, err := f.store.plan(plan.Reference, plan.Fingerprint)
	require.NoError(t, err)
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(consolidationCipherHash(frames[0]))).String()
	identity, err := consolidationDatabaseIdentity(db)
	require.NoError(t, err)
	require.NoError(t, db.Create(&consolidationBackupRow{BackupID: id, PlanID: state.ID, PlanFingerprint: state.Fingerprint, Ciphertext: frames[0], RestoreIdentity: identity, RestoreSnapshot: state.Census.Snapshot, RestoreVerifiedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}).Error)
	for ordinal, cipher := range frames {
		require.NoError(t, db.Create(&consolidationStoredFrame{BackupID: id, Ordinal: uint64(ordinal), Ciphertext: cipher}).Error)
	}
}

func streamIntegrityUnchanged(t *testing.T, expected streamIntegrityState, db *gormlib.DB, label string) {
	t.Helper()
	require.True(t, reflect.DeepEqual(expected, streamIntegrityRead(t, db)), "%s changed rows/xmin/sequences/effective objects", label)
}

// Only the exact current backup ID and its authenticated ordinals are additions.
func streamIntegrityConserved(t *testing.T, f *streamIntegrityFixture, source, local streamIntegrityState, plan ConsolidationPlan, sealed []byte, id string, declared map[string]bool) {
	t.Helper()
	frames := streamIntegrityCiphers(t, sealed)
	require.Equal(t, uuid.NewSHA1(uuid.NameSpaceOID, []byte(consolidationCipherHash(frames[0]))).String(), id)
	state, err := f.store.plan(plan.Reference, plan.Fingerprint)
	require.NoError(t, err)
	var envelope consolidationBackupRow
	require.NoError(t, f.target.Where("backup_id=?", id).First(&envelope).Error)
	require.Equal(t, state.ID, envelope.PlanID)
	require.Equal(t, plan.Fingerprint, envelope.PlanFingerprint)
	require.True(t, bytes.Equal(frames[0], envelope.Ciphertext), "current header ciphertext changed")
	identity, err := consolidationDatabaseIdentity(f.target)
	require.NoError(t, err)
	require.Equal(t, identity, envelope.RestoreIdentity)
	require.NotEmpty(t, envelope.RestoreSnapshot)
	require.NotEqual(t, "pending", envelope.RestoreSnapshot)
	require.False(t, envelope.RestoreVerifiedAt.IsZero())
	actual := streamIntegrityRead(t, f.target)
	expected := make(map[string][]string, len(source.Rows))
	for table, rows := range source.Rows {
		if declared != nil && !declared[table] {
			expected[table] = append([]string{}, local.Rows[table]...)
		} else {
			expected[table] = append([]string{}, rows...)
			if table == "project_consolidation_backups" || table == "project_consolidation_frames" {
				expected[table] = append(expected[table], local.Rows[table]...)
			}
		}
	}
	var currentRow string
	require.NoError(t, f.target.Raw(`SELECT to_jsonb(t)::text FROM project_consolidation_backups t WHERE backup_id=?`, id).Row().Scan(&currentRow))
	expected["project_consolidation_backups"] = append(expected["project_consolidation_backups"], streamIntegrityJSON(t, currentRow))
	for ordinal, cipher := range frames {
		row, err := json.Marshal(map[string]any{"backup_id": id, "ordinal": ordinal, "ciphertext": "\\x" + hex.EncodeToString(cipher)})
		require.NoError(t, err)
		expected["project_consolidation_frames"] = append(expected["project_consolidation_frames"], string(row))
	}
	require.Equal(t, len(expected), len(actual.Rows), "all-base-table denominator changed")
	for table, rows := range expected {
		sort.Strings(rows)
		if !reflect.DeepEqual(rows, actual.Rows[table]) {
			t.Errorf("STREAM_HISTORY_CONSERVATION: accepted VerifyBackup but table=%s expected_rows=%d actual_rows=%d; exact source and target projections retained privately", table, len(rows), len(actual.Rows[table]))
		}
	}
	require.True(t, reflect.DeepEqual(source.Sequences, actual.Sequences), "all actual sequence states must survive restore")
	require.True(t, reflect.DeepEqual(source.Objects, actual.Objects), "effective DDL/access state must remain identical")
}

func TestProjectConsolidationStreamPreservesDurableHistory(t *testing.T) {
	f := openStreamIntegrityFixture(t, streamIntegrityOracle, "stream_history_"+strings.ReplaceAll(uuid.NewString(), "-", ""), true)
	project := streamIntegritySeed(t, f, true)
	localPlan, localBytes := streamIntegrityPlan(t, f, f.target)
	streamIntegrityRetainArchive(t, f, f.target, localPlan, localBytes)
	before, local := streamIntegrityRead(t, f.source), streamIntegrityRead(t, f.target)
	for _, table := range []string{"project_consolidation_memberships", "project_consolidation_redirects", "project_consolidation_operations", "project_consolidation_backups", "project_consolidation_frames"} {
		require.NotEmpty(t, before.Rows[table], "history oracle must exercise nonempty %s", table)
	}
	plan, sealed := streamIntegrityPlan(t, f, f.source)
	streamIntegritySave(t, filepath.Join(f.directory, "source-before.json"), before)
	streamIntegritySave(t, filepath.Join(f.directory, "target-local-before.json"), local)
	streamIntegritySave(t, filepath.Join(f.directory, "plan.json"), plan)
	streamIntegrityWrite(t, filepath.Join(f.directory, "backup.sealed"), sealed)
	id, err := f.store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed))
	require.NoError(t, err, "RED requires successful old verification, not a setup/connection/codec failure")
	streamIntegritySave(t, filepath.Join(f.directory, "target-after.json"), streamIntegrityRead(t, f.target))
	streamIntegrityUnchanged(t, before, f.source, "read-only source")
	streamIntegrityConserved(t, f, before, local, plan, sealed, id, nil)
	reader := NewProjectConsolidationStore(f.target, f.store.vault, nil)
	resolved, err := reader.ResolveLegacy(f.ctx, "legacy-stream-history", "")
	require.NoError(t, err, "restored redirects remain available to native readers")
	require.Equal(t, project, resolved)
	for _, table := range []string{"credentials", "audit_log"} {
		original, err := NewProjectConsolidationStore(f.source, f.store.vault, nil).CanonicalMembership(f.ctx, project, table)
		require.NoError(t, err)
		require.NotEmpty(t, original)
		restored, err := reader.CanonicalMembership(f.ctx, project, table)
		require.NoError(t, err)
		require.True(t, reflect.DeepEqual(original, restored), "native history reader lost original membership/provenance for %s", table)
	}
	credential, err := NewCredentialStore(&Store{DB: f.target}).Get(f.ctx, project, "stream-integrity")
	require.NoError(t, err)
	_, err = f.store.vault.Decrypt(credential.EncryptedSecret)
	require.NoError(t, err, "restored ciphertext must remain readable with the original private key")
	beforeReplay := streamIntegrityRead(t, f.target)
	replayed, err := f.store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed))
	require.NoError(t, err)
	require.Equal(t, id, replayed)
	streamIntegrityUnchanged(t, beforeReplay, f.target, "verified replay")
	streamIntegrityUnchanged(t, before, f.source, "source after replay")
}

func TestProjectConsolidationStreamRejectsEffectiveObjectDrift(t *testing.T) {
	for _, drift := range []struct{ name, sql string }{
		{"disabled_trigger", `ALTER TABLE credentials DISABLE TRIGGER engram_consolidation_scope_write`},
		{"acl", `GRANT SELECT ON credentials TO PUBLIC`},
		{"rls", `ALTER TABLE credentials ENABLE ROW LEVEL SECURITY; ALTER TABLE credentials FORCE ROW LEVEL SECURITY`},
		{"policy", `CREATE POLICY stream_integrity_policy ON credentials AS RESTRICTIVE FOR ALL TO PUBLIC USING (true) WITH CHECK (true)`},
	} {
		for _, stage := range []string{"before_verify", "before_apply"} {
			t.Run(drift.name+"/"+stage, func(t *testing.T) {
				f := openStreamIntegrityFixture(t, streamIntegrityOracle, "stream_drift_"+strings.ReplaceAll(uuid.NewString(), "-", ""), true)
				streamIntegritySeed(t, f, false)
				source := streamIntegrityRead(t, f.source)
				plan, sealed := streamIntegrityPlan(t, f, f.source)
				var backup string
				var err error
				if stage == "before_apply" {
					backup, err = f.store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed))
					require.NoError(t, err, "Apply oracle requires a real verified backup")
				}
				beforeDDL := streamIntegrityRead(t, f.target)
				require.NoError(t, f.target.Exec(drift.sql).Error)
				before := streamIntegrityRead(t, f.target)
				require.True(t, reflect.DeepEqual(beforeDDL.Rows, before.Rows) && reflect.DeepEqual(beforeDDL.Versions, before.Versions) && reflect.DeepEqual(beforeDDL.Sequences, before.Sequences), "drift setup must not hide or change data")
				require.True(t, reflect.DeepEqual(beforeDDL.Definitions, before.Definitions), "old trigger/constraint/index definitions remain identical")
				require.False(t, reflect.DeepEqual(beforeDDL.Objects, before.Objects), "effective drift must be real")
				streamIntegritySave(t, filepath.Join(f.directory, "source-before.json"), source)
				streamIntegritySave(t, filepath.Join(f.directory, "target-before.json"), before)
				if stage == "before_verify" {
					_, err = f.store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed))
				} else {
					_, err = f.store.Apply(f.ctx, ConsolidationApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, BackupReference: backup, IdempotencyKey: uuid.NewString()})
				}
				streamIntegritySave(t, filepath.Join(f.directory, "target-after.json"), streamIntegrityRead(t, f.target))
				streamIntegrityUnchanged(t, source, f.source, "source after refused drift")
				if err == nil {
					t.Errorf("STREAM_EFFECTIVE_DRIFT_ACCEPTED: %s/%s accepted effective target mismatch under full maintenance visibility", drift.name, stage)
				} else {
					var hold *ConsolidationError
					require.ErrorAs(t, err, &hold, "permission/hidden-row/SQL failure is not the required schema refusal")
					require.Contains(t, []string{"PROJECT_ADOPTION_SCHEMA_MISMATCH", "PROJECT_ADOPTION_STALE_PLAN"}, hold.Code)
				}
				streamIntegrityUnchanged(t, before, f.target, "refused target including frames, receipt, sequence state and exact DDL modes")
			})
		}
	}
}

type streamIntegrityBaselineArchive struct {
	SourceVersion string
	Schema        string
	Plan          ConsolidationPlan
	Manifest      consolidationBackupState
	Expected      streamIntegrityState
	EmptyTarget   streamIntegrityState
	StreamSHA256  string
	SourceFiles   map[string]string
}

// Run on unchanged 6109 production before any repair; this never re-encodes an old fixture.
func TestProjectConsolidationStreamBaseline6109Capture(t *testing.T) {
	if os.Getenv("ENGRAM_STREAM_INTEGRITY_FIXTURE") == "" && os.Getenv("ENGRAM_STREAM_INTEGRITY_DEADLINE") == "" {
		t.Skip("no assigned baseline capture fixture")
	}
	for name, expected := range streamIntegrityBaselineFiles {
		raw, err := os.ReadFile(name)
		require.NoError(t, err)
		require.Equal(t, expected, fmt.Sprintf("%x", sha256.Sum256(raw)), "old artifact must be captured by exact baseline production, never the fixed writer")
	}
	f := openStreamIntegrityFixture(t, streamIntegrityOriginal, "stream_baseline6109", true)
	streamIntegritySeed(t, f, true)
	before := streamIntegrityRead(t, f.source)
	plan, sealed := streamIntegrityPlan(t, f, f.source)
	frame, _, err := f.store.readFrame(bytes.NewReader(sealed))
	require.NoError(t, err)
	require.NotNil(t, frame.Manifest)
	require.Equal(t, "engram.owner-stream/2", frame.Manifest.Format)
	declared := map[string]bool{}
	for _, table := range frame.Manifest.Tables {
		declared[table.Name] = true
		require.Len(t, before.Rows[table.Name], int(table.Count), "independent original declared row denominator")
	}
	require.Len(t, frame.Manifest.Sequences, len(before.Sequences))
	for i, sequence := range before.Sequences {
		require.Equal(t, sequence.Name, frame.Manifest.Sequences[i].Name)
		require.Equal(t, sequence.LastValue, frame.Manifest.Sequences[i].LastValue)
		require.Equal(t, sequence.IsCalled, frame.Manifest.Sequences[i].IsCalled)
	}
	for _, table := range []string{"project_consolidation_memberships", "project_consolidation_redirects", "project_consolidation_operations", "project_consolidation_backups", "project_consolidation_frames"} {
		require.NotEmpty(t, before.Rows[table])
		require.False(t, declared[table], "this capture specifically preserves the actual old partial manifest")
	}
	archive := streamIntegrityBaselineArchive{SourceVersion: streamIntegrityBaseline, Schema: f.schema, Plan: plan, Manifest: *frame.Manifest, Expected: before, EmptyTarget: streamIntegrityRead(t, f.target), StreamSHA256: fmt.Sprintf("%x", sha256.Sum256(sealed)), SourceFiles: streamIntegrityBaselineFiles}
	streamIntegrityWrite(t, filepath.Join(f.directory, "baseline6109.sealed"), sealed)
	streamIntegritySave(t, filepath.Join(f.directory, "baseline6109.json"), archive)
	streamIntegrityUnchanged(t, before, f.source, "retained available original baseline source")
	t.Logf("genuine old /2 capture retained: schema=%s declared_tables=%d all_base_tables=%d bytes=%d sha256=%s; original plan/key/source retained privately", f.schema, len(declared), len(before.Rows), len(sealed), archive.StreamSHA256)
}

func TestProjectConsolidationStreamHistorical6109Reader(t *testing.T) {
	f := openStreamIntegrityFixture(t, streamIntegrityHistorical, "stream_baseline6109", false)
	raw, err := os.ReadFile(filepath.Join(f.directory, "baseline6109.json"))
	require.NoError(t, err, "Root-authorized actual baseline capture must run before repair")
	var archive streamIntegrityBaselineArchive
	require.NoError(t, json.Unmarshal(raw, &archive))
	require.Equal(t, streamIntegrityBaseline, archive.SourceVersion)
	require.Equal(t, f.schema, archive.Schema)
	require.True(t, reflect.DeepEqual(streamIntegrityBaselineFiles, archive.SourceFiles))
	sealed, err := os.ReadFile(filepath.Join(f.directory, "baseline6109.sealed"))
	require.NoError(t, err)
	require.Equal(t, archive.StreamSHA256, fmt.Sprintf("%x", sha256.Sum256(sealed)))
	streamIntegrityUnchanged(t, archive.Expected, f.source, "available unchanged original /2 source")
	local := streamIntegrityRead(t, f.target)
	require.Equal(t, archive.EmptyTarget, local, "original matching target must still have the exact preserved rows/xmin/sequences/effective objects; never reset a changed target")
	id, err := f.store.VerifyBackup(f.ctx, archive.Plan.Reference, archive.Plan.Fingerprint, bytes.NewReader(sealed))
	require.NoError(t, err, "original authenticated backup+plan must remain restorable through existing VerifyBackup")
	declared := map[string]bool{}
	for _, table := range archive.Manifest.Tables {
		declared[table.Name] = true
	}
	streamIntegrityConserved(t, f, archive.Expected, local, archive.Plan, sealed, id, declared)
	var credentials []Credential
	require.NoError(t, f.target.Find(&credentials).Error)
	require.NotEmpty(t, credentials)
	for _, credential := range credentials {
		_, err := f.store.vault.Decrypt(credential.EncryptedSecret)
		require.NoError(t, err)
	}
	beforeApply := streamIntegrityRead(t, f.target)
	_, err = f.store.Apply(f.ctx, ConsolidationApplyRequest{PlanReference: archive.Plan.Reference, PlanFingerprint: archive.Plan.Fingerprint, BackupReference: id, IdempotencyKey: uuid.NewString()})
	var hold *ConsolidationError
	require.ErrorAs(t, err, &hold, "historical partial proof must never authorize a new Apply")
	streamIntegrityUnchanged(t, beforeApply, f.target, "refused old partial Apply")
	_, err = f.store.DryRun(f.ctx, ConsolidationApplyRequest{PlanReference: archive.Plan.Reference, PlanFingerprint: archive.Plan.Fingerprint, BackupReference: id, IdempotencyKey: uuid.NewString()})
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
	oldState, err := f.store.plan(archive.Plan.Reference, archive.Plan.Fingerprint)
	require.NoError(t, err)
	oldCensus, err := f.store.seal("census", oldState.Census)
	require.NoError(t, err)
	_, err = f.store.Preflight(f.ctx, ConsolidationPreflightRequest{CensusReference: oldCensus, CensusFingerprint: oldState.Census.Fingerprint})
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
	_, err = f.store.DryRunNative(f.ctx, ConsolidationNativeRequest{PlanReference: archive.Plan.Reference, PlanFingerprint: archive.Plan.Fingerprint})
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
	wrongSource := NewProjectConsolidationStore(f.target, f.store.vault, f.source)
	_, err = wrongSource.VerifyBackup(f.ctx, archive.Plan.Reference, archive.Plan.Fingerprint, bytes.NewReader(sealed))
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN", "a different physical source cannot reuse the original historical recipe")
	streamIntegrityUnchanged(t, beforeApply, f.target, "all old partial refusal paths")
	targetTx := f.target.Begin()
	require.NoError(t, targetTx.Exec(`GRANT SELECT ON credentials TO PUBLIC`).Error)
	_, err = NewProjectConsolidationStore(f.source, f.store.vault, targetTx).VerifyBackup(f.ctx, archive.Plan.Reference, archive.Plan.Fingerprint, bytes.NewReader(sealed))
	require.EqualError(t, err, "PROJECT_ADOPTION_SCHEMA_MISMATCH", "historical recipe still requires current effective source/target correspondence")
	require.NoError(t, targetTx.Rollback().Error)
	streamIntegrityUnchanged(t, beforeApply, f.target, "historical target drift refusal")
	streamIntegrityUnchanged(t, archive.Expected, f.source, "original source after historical reader")
	retainedBytes, err := os.ReadFile(filepath.Join(f.directory, "baseline6109.sealed"))
	require.NoError(t, err)
	require.Equal(t, archive.StreamSHA256, fmt.Sprintf("%x", sha256.Sum256(retainedBytes)), "historical bytes were not rewritten")
}
