package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestProjectPartitionRepairCLIRequiresDistinctExplicitStages(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"apply", "--source-dsn-file", "S", "--target-dsn-file", "T", "--plan-file", "P"},
		{"plan", "--source-dsn-file", "S", "--plan-file", "P"},
		{"plan", "--source-dsn-file", "S", "--target-dsn-file", "T", "--plan-file", "P", "--parent-receipt-digest", strings.Repeat("a", 64)},
		{"dry-run", "--source-dsn-file", "S", "--plan-file", "P"},
		{"dry-run", "--source-dsn-file", "S", "--target-dsn-file", "T", "--plan-file", "P", "--commit"},
		{"apply", "--source-dsn-file", "S", "--target-dsn-file", "T", "--plan-file", "P", "--plan-id", "ID", "extra"},
	} {
		_, err := parseProjectPartitionRepairArgs(args)
		require.Error(t, err)
	}
	plan, err := parseProjectPartitionRepairArgs([]string{"plan", "--source-dsn-file", "S", "--plan-file", "P", "--parent-receipt-digest", strings.Repeat("a", 64)})
	require.NoError(t, err)
	require.Empty(t, plan.targetFile)
	for _, stage := range []string{"dry-run", "apply"} {
		options, err := parseProjectPartitionRepairArgs([]string{stage, "--source-dsn-file", "S", "--target-dsn-file", "T", "--plan-file", "P", "--plan-id", "ID"})
		require.NoError(t, err)
		require.Equal(t, stage, options.stage)
		require.Equal(t, "ID", options.planID)
	}
}

func TestProjectPartitionRepairCLIHelpAndRefusalNeverStartService(t *testing.T) {
	var stdout, stderr bytes.Buffer
	require.Zero(t, runProjectPartitionRepairOutput([]string{"plan", "--help"}, &stdout, &stderr))
	require.Contains(t, stdout.String(), "repair-project-partitions plan|dry-run|apply")
	require.Empty(t, stderr.String())
	stdout.Reset()
	require.NotZero(t, runProjectPartitionRepairOutput([]string{"apply", "--source-dsn-file", "missing-source", "--target-dsn-file", "missing-target", "--plan-file", "missing-plan"}, &stdout, &stderr))
	require.Empty(t, stdout.String())
	require.Contains(t, stderr.String(), "PROJECT_ADOPTION_PLAN_ID_REQUIRED")
}

func TestProjectPartitionRepairCLIRealStoreComposition(t *testing.T) {
	sourceDSN, targetDSN := os.Getenv("DATABASE_DSN"), os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN")
	if sourceDSN == "" || targetDSN == "" {
		t.Skip("separate migration186 PostgreSQL fixtures are required")
	}
	for i, dsn := range []string{sourceDSN, targetDSN} {
		cfg, err := pgx.ParseConfig(dsn)
		require.NoError(t, err)
		require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, cfg.Host)
		require.True(t, strings.HasPrefix(cfg.Database, "engram_migration186_scope_test_"))
		require.True(t, strings.HasSuffix(cfg.Database, []string{"_source", "_restore"}[i]))
		require.Empty(t, cfg.Fallbacks)
		require.True(t, cfg.RuntimeParams["search_path"] == "" || cfg.RuntimeParams["search_path"] == "public")
	}
	require.NotEqual(t, sourceDSN, targetDSN)
	key := config.Get()
	oldKey := key.EncryptionKey
	key.EncryptionKey = strings.Repeat("ab", 32)
	t.Cleanup(func() { key.EncryptionKey = oldKey })
	stamp := time.Date(2026, 10, 19, 12, 0, 0, 123456000, time.UTC)
	const project = "30000000-0000-4000-8000-000000000001"
	const parent = "30000000-0000-4000-8000-000000000002"
	parentReceipt, err := json.Marshal(gormdb.ConsolidationReceipt{OperationID: parent, Reference: "urn:engram:migration:receipt:" + parent, ProjectIDs: []string{project}})
	require.NoError(t, err)
	stores := make([]*gormdb.Store, 0, 2)
	for _, dsn := range []string{sourceDSN, targetDSN} {
		precheck, closePrecheck, err := openConsolidationDatabase(dsn, true)
		require.NoError(t, err)
		var tables int64
		err = precheck.Raw("SELECT count(*) FROM pg_tables WHERE schemaname='public'").Scan(&tables).Error
		require.NoError(t, closePrecheck())
		require.NoError(t, err)
		require.Zero(t, tables, "CLI composition owns only freshly created empty fixtures")
		store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, MaxConns: 2, LogLevel: logger.Silent})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		stores = append(stores, store)
		require.NoError(t, store.DB.Transaction(func(tx *gormlib.DB) error {
			if err := tx.Exec("SET LOCAL session_replication_role=replica").Error; err != nil {
				return err
			}
			if err := tx.Create(&gormdb.Project{ID: project, DisplayName: sql.NullString{String: "CLI fixture", Valid: true}, CreatedAt: stamp}).Error; err != nil {
				return err
			}
			memory := gormdb.Memory{Content: "CLI unassigned fixture", CreatedAt: stamp, UpdatedAt: stamp, ValidFrom: &stamp}
			if err := tx.Create(&memory).Error; err != nil {
				return err
			}
			return tx.Exec("INSERT INTO project_consolidation_operations (idempotency_key,operation_id,plan_id,plan_fingerprint,receipt,completed_at) VALUES (?,?,?,?,?::jsonb,?)", parent, parent, parent, "synthetic-B7-plan", string(parentReceipt), stamp).Error
		}))
	}
	snapshot := func(db *gormlib.DB) map[string]string {
		t.Helper()
		var tables []string
		require.NoError(t, db.Raw("SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename").Scan(&tables).Error)
		result := map[string]string{}
		for _, table := range tables {
			var rows string
			quoted := pgx.Identifier{table}.Sanitize()
			require.NoError(t, db.Raw("SELECT coalesce(jsonb_agg(to_jsonb(t)||jsonb_build_object('fixture_xmin',xmin::text) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM "+quoted+" t").Row().Scan(&rows))
			result[table] = rows
		}
		return result
	}
	var parentText string
	require.NoError(t, stores[0].DB.Raw("SELECT receipt::text FROM project_consolidation_operations WHERE operation_id=?", parent).Row().Scan(&parentText))
	digest := sha256.Sum256([]byte(parentText))
	dir := t.TempDir()
	sourceFile, targetFile, planFile := filepath.Join(dir, "source.dsn"), filepath.Join(dir, "target.dsn"), filepath.Join(dir, "repair.json")
	require.NoError(t, os.WriteFile(sourceFile, []byte(sourceDSN), 0o600))
	require.NoError(t, os.WriteFile(targetFile, []byte(targetDSN), 0o600))
	sourceBefore, targetBefore := snapshot(stores[0].DB), snapshot(stores[1].DB)
	t.Run("invalid_parent_receipt_is_a_private_typed_refusal", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		require.Equal(t, 1, runProjectPartitionRepairOutput([]string{"plan", "--source-dsn-file", sourceFile, "--plan-file", planFile, "--parent-receipt-digest", strings.Repeat("0", 64)}, &stdout, &stderr))
		require.Empty(t, stdout.String())
		require.JSONEq(t, `{"code":"PROJECT_ADOPTION_PARENT_RECEIPT_INVALID"}`, stderr.String())
		require.NoFileExists(t, planFile)
		require.Equal(t, sourceBefore, snapshot(stores[0].DB))
		require.Equal(t, targetBefore, snapshot(stores[1].DB))
	})
	call := func(args ...string) []byte {
		t.Helper()
		var stdout, stderr bytes.Buffer
		require.Zero(t, runProjectPartitionRepairOutput(args, &stdout, &stderr), stderr.String())
		require.Empty(t, stderr.String())
		return stdout.Bytes()
	}
	var plan gormdb.ProjectPartitionRepairPlan
	require.NoError(t, json.Unmarshal(call("plan", "--source-dsn-file", sourceFile, "--plan-file", planFile, "--parent-receipt-digest", hex.EncodeToString(digest[:])), &plan))
	require.Empty(t, plan.Reference, "stdout must not reveal the private sealed reference")
	require.Equal(t, 1, plan.UnassignedAdditions)
	var sealed gormdb.ProjectPartitionRepairPlan
	raw, err := os.ReadFile(planFile)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &sealed))
	require.NotEmpty(t, sealed.Reference)
	var stdout, stderr bytes.Buffer
	require.NotZero(t, runProjectConsolidationOutput([]string{"--source-dsn-file", sourceFile, "--target-dsn-file", targetFile, "--plan-file", planFile}, &stdout, &stderr), "a repair plan cannot be consumed as an adoption plan")
	require.Equal(t, targetBefore, snapshot(stores[1].DB))
	stdout.Reset()
	stderr.Reset()
	require.NotZero(t, runProjectPartitionRepairOutput([]string{"dry-run", "--source-dsn-file", sourceFile, "--target-dsn-file", sourceFile, "--plan-file", planFile}, &stdout, &stderr))
	require.Contains(t, stderr.String(), "PROJECT_ADOPTION_TARGET_IS_SOURCE")
	var dry, applied, replay gormdb.ProjectPartitionRepairResult
	require.NoError(t, json.Unmarshal(call("dry-run", "--source-dsn-file", sourceFile, "--target-dsn-file", targetFile, "--plan-file", planFile), &dry))
	require.True(t, dry.DryRun)
	require.Equal(t, targetBefore, snapshot(stores[1].DB))
	require.NoError(t, json.Unmarshal(call("apply", "--source-dsn-file", sourceFile, "--target-dsn-file", targetFile, "--plan-file", planFile, "--plan-id", dry.Receipt.PlanID), &applied))
	require.False(t, applied.DryRun)
	require.False(t, applied.Replay)
	require.Equal(t, "project-partition-repair/v1", applied.Receipt.Kind)
	require.Equal(t, 1, applied.Receipt.UnassignedAdditions)
	after := snapshot(stores[1].DB)
	require.NoError(t, json.Unmarshal(call("apply", "--source-dsn-file", sourceFile, "--target-dsn-file", targetFile, "--plan-file", planFile, "--plan-id", dry.Receipt.PlanID), &replay))
	require.True(t, replay.Replay)
	require.Equal(t, applied.Receipt, replay.Receipt)
	require.Equal(t, after, snapshot(stores[1].DB))
	require.Equal(t, sourceBefore, snapshot(stores[0].DB))
	t.Log("actual typed CLI plan/private-file/dry-run/apply/replay, separate plan kind and source refusal passed on synthetic PostgreSQL fixtures")
}
