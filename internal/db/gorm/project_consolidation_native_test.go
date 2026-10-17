package gorm

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestProjectConsolidationNativeExpectedEditsPreserveOtherFields(t *testing.T) {
	raw := JSONRaw(`{"id":4,"project":"mixed","history":{"project":"mixed"},"names":["mixed","mixed"]}`)
	row := consolidationRow{Reference: "one", Table: "memories", Column: "project", Kind: "tenant", Selector: "mixed", Key: JSONRaw(`{"id":4}`)}
	changed, key, err := consolidationNativeEdit(raw, []string{"id"}, []consolidationRowEdit{{Row: row, Mapping: ConsolidationMapping{ProjectID: "canonical"}}})
	require.NoError(t, err)
	require.JSONEq(t, `{"id":4,"project":"canonical","history":{"project":"mixed"},"names":["mixed","mixed"]}`, string(changed))
	require.JSONEq(t, `{"id":4}`, string(key))
	require.JSONEq(t, `{"id":4,"project":"mixed","history":{"project":"mixed"},"names":["mixed","mixed"]}`, string(raw))
	encoded, err := json.Marshal([]consolidationNativeChange{{Key: json.RawMessage(key), Object: json.RawMessage(changed)}})
	require.NoError(t, err)
	var batch []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &batch))
	require.Len(t, batch, 1)
	require.JSONEq(t, `{"id":4}`, string(batch[0]["key"]))
	require.JSONEq(t, `{"id":4,"project":"canonical","history":{"project":"mixed"},"names":["mixed","mixed"]}`, string(batch[0]["object"]))
	row.Selector = "other"
	_, _, err = consolidationNativeEdit(raw, []string{"id"}, []consolidationRowEdit{{Row: row, Mapping: ConsolidationMapping{ProjectID: "canonical"}}})
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_MAPPING")
}

func TestProjectConsolidationNativeMetadataJSONShapes(t *testing.T) {
	native := &consolidationNativeState{
		source: consolidationNativeSnapshot{Tables: map[string]consolidationNativeTable{
			"project_consolidation_memberships": {Rows: map[string]consolidationNativeRow{}},
			"project_consolidation_redirects":   {Rows: map[string]consolidationNativeRow{}},
		}},
		expected: consolidationNativeSnapshot{Tables: map[string]consolidationNativeTable{
			"project_consolidation_memberships": {Rows: map[string]consolidationNativeRow{}},
			"project_consolidation_operations":  {Rows: map[string]consolidationNativeRow{}},
		}},
		pending: map[string]map[string]JSONRaw{},
	}
	state := consolidationPlanState{
		Fingerprint: "plan-fingerprint",
		Census: consolidationCensusState{
			NativeSnapshot: "source-lineage",
			Rows:           []consolidationRow{{Reference: "row", Table: "memories", Column: "project", Kind: "tenant", Key: JSONRaw(`{"id":4}`), NativeVersion: "source-version"}},
		},
		Mapping: []ConsolidationMapping{{Reference: "row", UnresolvedReference: "unresolved"}},
	}
	require.NoError(t, native.expectMetadata(state))
	var membership map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(native.pending["project_consolidation_memberships"][`{"reference":"row"}`], &membership))
	require.JSONEq(t, `{"id":4}`, string(membership["row_key"]))
	require.JSONEq(t, `{"id":4}`, string(membership["source_row_key"]))
	require.JSONEq(t, `["project"]`, string(membership["field_path"]))
	operation := consolidationOperation{
		IdempotencyKey:  "operation",
		OperationID:     "operation",
		PlanID:          "plan",
		PlanFingerprint: "plan-fingerprint",
		Receipt:         JSONRaw(`{"operation_id":"operation","roles":[]}`),
		CompletedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, native.operation(operation))
	var recorded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(native.pending["project_consolidation_operations"][`{"idempotency_key":"operation"}`], &recorded))
	require.JSONEq(t, `{"operation_id":"operation","roles":[]}`, string(recorded["receipt"]))
}

func TestProjectConsolidationNativeMultiplicityAndIndependentVersions(t *testing.T) {
	row := consolidationNativeRow{Digest: "digest", Version: "source"}
	a := consolidationNativeTable{Rows: map[string]consolidationNativeRow{"source-a": row, "source-b": row}}
	b := consolidationNativeTable{Rows: map[string]consolidationNativeRow{"target-a": {Digest: "digest", Version: "target"}, "target-b": {Digest: "digest", Version: "target"}}}
	require.NoError(t, consolidationNativeCompareTable(a, b, false))
	delete(b.Rows, "target-b")
	require.EqualError(t, consolidationNativeCompareTable(a, b, false), "PROJECT_ADOPTION_CONSERVATION_FAILED")
	a.PrimaryKey, b.PrimaryKey = []string{"id"}, []string{"id"}
	a.Rows = map[string]consolidationNativeRow{"same": row}
	b.Rows = map[string]consolidationNativeRow{"same": {Digest: "digest", Version: "target"}}
	require.NoError(t, consolidationNativeCompareTable(a, b, false))
	require.EqualError(t, consolidationNativeCompareTable(a, b, true), "PROJECT_ADOPTION_CONSERVATION_FAILED")
}

func TestProjectConsolidationNativePlanIDIsDecodedNotCallerState(t *testing.T) {
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	store := NewProjectConsolidationStore(nil, vault, nil)
	state := consolidationPlanState{Census: consolidationCensusState{Fingerprint: "census"}}
	state.Mapping, err = store.buildMapping(state.Census.Rows, state.Evidence)
	require.NoError(t, err)
	state.Fingerprint = consolidationPlanFingerprint(state)
	state.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(state.Fingerprint)).String()
	token, err := store.seal("plan", state)
	require.NoError(t, err)
	request := ConsolidationNativeRequest{PlanReference: token, PlanFingerprint: state.Fingerprint}
	decoded, err := store.nativePlan(request, false)
	require.NoError(t, err)
	require.Equal(t, state.ID, decoded.ID)
	_, err = store.nativePlan(request, true)
	require.EqualError(t, err, "PROJECT_ADOPTION_PLAN_ID_REQUIRED")
	request.ExpectedPlanID = uuid.NewString()
	_, err = store.nativePlan(request, false)
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
	request.ExpectedPlanID = state.ID
	_, err = store.nativePlan(request, true)
	require.NoError(t, err)
}

func TestProjectConsolidationNativePhysicalDatabaseIdentity(t *testing.T) {
	dsnPath := os.Getenv("ENGRAM_CONSOLIDATION_FIXTURE_DSN_FILE")
	if dsnPath == "" {
		t.Skip("exact admitted fixture binding absent; no physical database identity proof")
	}
	deadline, err := time.Parse(time.RFC3339, os.Getenv("ENGRAM_CONSOLIDATION_FIXTURE_DEADLINE"))
	require.NoError(t, err)
	require.True(t, time.Now().Before(deadline), "admitted fixture deadline expired")
	private, err := os.ReadFile(dsnPath)
	require.NoError(t, err, "private fixture DSN unavailable")
	fixture, err := pgx.ParseConfig(strings.TrimSpace(string(private)))
	require.NoError(t, err, "invalid admitted fixture DSN")
	require.Equal(t, "127.0.0.1", fixture.Host)
	require.EqualValues(t, 22366, fixture.Port)
	require.Equal(t, "identity_fixture", fixture.Database)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	open := func(dsn, role string) *gormlib.DB {
		configuration := consolidationTestDatabaseConfig(t, dsn, role)
		require.Equal(t, fixture.Host, configuration.Host)
		require.Equal(t, fixture.Port, configuration.Port)
		configuration.RuntimeParams["default_transaction_read_only"] = "on"
		configuration.RuntimeParams["application_name"] = "engram_consolidation_physical_identity_test"
		pool := stdlib.OpenDB(*configuration)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, pool.Close()) })
		db, err := gormlib.Open(postgres.New(postgres.Config{Conn: pool}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		return db.WithContext(ctx)
	}
	source := open(os.Getenv("DATABASE_DSN"), "source")
	target := open(os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN"), "restore")
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	store := NewProjectConsolidationStore(source, vault, target)
	for _, db := range []*gormlib.DB{source, target} {
		pool, err := db.DB()
		require.NoError(t, err)
		var physical struct {
			Database, SystemIdentifier string
			OID                        int64
		}
		require.NoError(t, pool.QueryRowContext(ctx, `SELECT current_database(),system_identifier::text,(SELECT oid::bigint FROM pg_database WHERE datname=current_database()) FROM pg_control_system()`).Scan(&physical.Database, &physical.SystemIdentifier, &physical.OID))
		require.NotEmpty(t, physical.Database)
		require.NotEmpty(t, physical.SystemIdentifier)
		require.Positive(t, physical.OID)
		reader := NewProjectConsolidationStore(db, vault, nil)
		census, err := reader.Census(ctx, "native-physical-identity-regression")
		require.NoError(t, err)
		require.Equal(t, structuralDigest(physical), census.Database, "Census must bind the independently read native physical tuple")
		var state consolidationCensusState
		require.NoError(t, reader.open(census.Reference, "census", &state))
		require.NoError(t, reader.fresh(db, state))
		physical.OID++
		state.Database = structuralDigest(physical)
		require.EqualError(t, reader.fresh(db, state), "PROJECT_ADOPTION_STALE_PLAN", "a different physical OID must invalidate the census even when rows and schema are unchanged")
	}
	require.NoError(t, store.distinctTarget(ctx))
	require.EqualError(t, NewProjectConsolidationStore(source, vault, source).distinctTarget(ctx), "PROJECT_ADOPTION_TARGET_IS_SOURCE")
}

func TestProjectConsolidationNativeStoreWorkflow(t *testing.T) {
	f := consolidationFixtureStore(t)
	store, source, target, ctx := f.store, f.source, f.target, f.ctx
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, db := range []*ProjectConsolidationStore{NewProjectConsolidationStore(source, store.vault, nil), NewProjectConsolidationStore(target, store.vault, nil)} {
		tx := db.db.Begin()
		require.NoError(t, tx.Error)
		restore, err := consolidationSuspendWriteGuards(tx)
		require.NoError(t, err)
		require.NoError(t, tx.Create(&Project{ID: "native-mixed", CreatedAt: created, DisplayName: sql.NullString{String: "retained", Valid: true}}).Error)
		require.NoError(t, tx.Create(&Memory{ID: 810001, Project: "native-mixed", Content: "native preserved content", Version: 1, CreatedAt: created, UpdatedAt: created, ValidFrom: &created}).Error)
		require.NoError(t, restore())
		require.NoError(t, tx.Commit().Error)
	}
	census, err := store.Census(ctx, "native-fixture")
	require.NoError(t, err)
	var state consolidationCensusState
	require.NoError(t, store.open(census.Reference, "census", &state))
	canonical := uuid.NewString()
	var references []string
	for _, row := range state.Rows {
		if row.Table == "memories" && row.Column == "project" {
			references = append(references, row.Reference)
		}
	}
	require.NotEmpty(t, references)
	plan, err := store.Preflight(ctx, ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint, Evidence: []ConsolidationEvidence{{Reference: "urn:engram:proof:native", Kind: "portable_marker", Group: "urn:engram:group:native", ProjectID: canonical, Rows: references}}})
	require.NoError(t, err)
	beforeSource, err := consolidationNativeRead(source.WithContext(ctx), nil)
	require.NoError(t, err)
	beforeTarget, err := consolidationNativeRead(target.WithContext(ctx), nil)
	require.NoError(t, err)
	require.NoError(t, consolidationNativeCompare(beforeSource, beforeTarget, false), "native fixture source and target must match before migration")
	request := ConsolidationNativeRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint}
	var mode string
	require.NoError(t, target.Raw("SELECT tgenabled::text FROM pg_trigger WHERE tgrelid='memories'::regclass AND tgname='engram_consolidation_scope_write' AND NOT tgisinternal").Scan(&mode).Error)
	require.Contains(t, []string{"O", "R", "A"}, mode)
	require.NoError(t, target.Exec("ALTER TABLE memories DISABLE TRIGGER engram_consolidation_scope_write").Error)
	_, err = store.DryRunNative(ctx, request)
	require.EqualError(t, err, "PROJECT_ADOPTION_SCHEMA_MISMATCH")
	restoreMode := "ENABLE"
	if mode == "R" {
		restoreMode = "ENABLE REPLICA"
	} else if mode == "A" {
		restoreMode = "ENABLE ALWAYS"
	}
	require.NoError(t, target.Exec("ALTER TABLE memories "+restoreMode+" TRIGGER engram_consolidation_scope_write").Error)
	dry, err := store.DryRunNative(ctx, request)
	require.NoError(t, err)
	require.True(t, dry.DryRun)
	afterDry, err := consolidationNativeRead(target.WithContext(ctx), nil)
	require.NoError(t, err)
	require.NoError(t, consolidationNativeCompare(beforeTarget, afterDry, true))
	request.ExpectedPlanID = dry.PlanID
	applied, err := store.ApplyNative(ctx, request)
	require.NoError(t, err)
	require.False(t, applied.Replay)
	require.Equal(t, dry.PlanID, applied.PlanID)
	replayed, err := store.ApplyNative(ctx, request)
	require.NoError(t, err)
	require.True(t, replayed.Replay)
	require.Equal(t, applied.Receipt, replayed.Receipt)
	afterSource, err := consolidationNativeRead(source.WithContext(ctx), nil)
	require.NoError(t, err)
	require.NoError(t, consolidationNativeCompare(beforeSource, afterSource, true))
	var memory Memory
	require.NoError(t, target.First(&memory, "id=?", 810001).Error)
	require.Equal(t, canonical, memory.Project)
	require.Equal(t, "native preserved content", memory.Content)
	encoded, err := json.Marshal(applied)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), plan.Reference)
}
