package gorm

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	gormlib "gorm.io/gorm"
)

func TestProjectAliasPhysicalDatabaseBinding(t *testing.T) {
	sourceDSN, restoreDSN := os.Getenv("DATABASE_DSN"), os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN")
	if sourceDSN == "" || restoreDSN == "" {
		t.Skip("two dedicated disposable PostgreSQL databases required")
	}
	sourceConfig := consolidationTestDatabaseConfig(t, sourceDSN, "source")
	targetConfig := consolidationTestDatabaseConfig(t, restoreDSN, "restore")
	require.Equal(t, sourceConfig.Host, targetConfig.Host)
	require.Equal(t, sourceConfig.Port, targetConfig.Port)
	require.Equal(t, strings.TrimSuffix(sourceConfig.Database, "_source"), strings.TrimSuffix(targetConfig.Database, "_restore"))
	require.NotEqual(t, sourceConfig.Database, targetConfig.Database)
	schema := "alias_identity_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	source := openMigratedTestDBInSchema(t, sourceDSN, schema, "engram_alias_identity_source")
	target := openMigratedTestDBInSchema(t, restoreDSN, schema, "engram_alias_identity_target")
	sourceIdentity, err := consolidationDatabaseIdentity(source)
	require.NoError(t, err)
	targetIdentity, err := consolidationDatabaseIdentity(target)
	require.NoError(t, err)
	require.NotEqual(t, sourceIdentity, targetIdentity, "fixture databases must have distinct native physical identities")
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	origin := NewProjectConsolidationStore(source, vault, nil)
	restored := NewProjectConsolidationStore(target, vault, nil)
	ctx := t.Context()
	seed := func(t *testing.T) ProjectAliasAttachRequest {
		t.Helper()
		req := ProjectAliasAttachRequest{Target: uuid.NewString(), Aliases: []ProjectAliasExpected{{Source: uuid.NewString()}}, Reason: "synthetic physical identity fixture"}
		for _, db := range []*gormlib.DB{source, target} {
			for _, id := range []string{req.Target, req.Aliases[0].Source} {
				require.NoError(t, db.Create(&Project{ID: id, CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}).Error)
			}
		}
		sourceProjects, sourceRedirects, err := aliasSnapshots(source, req)
		require.NoError(t, err)
		targetProjects, targetRedirects, err := aliasSnapshots(target, req)
		require.NoError(t, err)
		require.Equal(t, sourceProjects, targetProjects, "synthetic project snapshots must match")
		require.Equal(t, sourceRedirects, targetRedirects, "synthetic redirect snapshots must match")
		return req
	}
	t.Run("same_database_apply_status_replay_rollback", func(t *testing.T) {
		req := seed(t)
		plan, err := origin.PrepareProjectAliases(ctx, req)
		require.NoError(t, err)
		apply := ProjectAliasApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, IdempotencyKey: uuid.NewString()}
		applied, err := origin.ApplyProjectAliases(ctx, apply, "synthetic")
		require.NoError(t, err)
		require.Equal(t, sourceIdentity, applied.Database)
		replay, err := origin.ApplyProjectAliases(ctx, apply, "synthetic")
		require.NoError(t, err)
		require.Equal(t, applied, replay)
		status, err := origin.ProjectAliasStatus(ctx, apply.IdempotencyKey)
		require.NoError(t, err)
		require.Equal(t, "applied", status.Status)
		rollback := ProjectAliasRollbackRequest{ApplyKey: apply.IdempotencyKey, IdempotencyKey: uuid.NewString()}
		_, err = origin.RollbackProjectAliases(ctx, rollback, "synthetic")
		require.NoError(t, err)
		_, err = origin.RollbackProjectAliases(ctx, rollback, "synthetic")
		require.NoError(t, err)
		status, err = origin.ProjectAliasStatus(ctx, apply.IdempotencyKey)
		require.NoError(t, err)
		require.Equal(t, "rolled_back", status.Status)
	})
	t.Run("prepared_plan_cannot_apply_to_other_database", func(t *testing.T) {
		req := seed(t)
		plan, err := origin.PrepareProjectAliases(ctx, req)
		require.NoError(t, err)
		apply := ProjectAliasApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, IdempotencyKey: uuid.NewString()}
		_, err = restored.ApplyProjectAliases(ctx, apply, "synthetic")
		assert.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN", "a plan sealed for the source physical database must be refused on the restored database")
		var count int64
		require.NoError(t, target.Table("project_consolidation_redirects").Where("selector = ?", req.Aliases[0].Source).Count(&count).Error)
		assert.Zero(t, count, "wrong-database apply must not create a redirect")
		require.NoError(t, target.Model(&consolidationOperation{}).Where("idempotency_key = ?", apply.IdempotencyKey).Count(&count).Error)
		assert.Zero(t, count, "wrong-database apply must not record an operation")
	})
	t.Run("copied_operation_status_and_rollback_cannot_rebind", func(t *testing.T) {
		req := seed(t)
		plan, err := origin.PrepareProjectAliases(ctx, req)
		require.NoError(t, err)
		apply := ProjectAliasApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, IdempotencyKey: uuid.NewString()}
		_, err = origin.ApplyProjectAliases(ctx, apply, "synthetic")
		require.NoError(t, err)
		var operation consolidationOperation
		require.NoError(t, source.Where("idempotency_key = ?", apply.IdempotencyKey).First(&operation).Error)
		require.NoError(t, target.Create(&operation).Error)
		require.NoError(t, target.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', ?, ?)`, req.Aliases[0].Source, req.Target, plan.Fingerprint).Error)
		_, err = restored.ApplyProjectAliases(ctx, apply, "synthetic")
		assert.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN", "a copied operation must not authorize replay on another database")
		_, err = restored.ProjectAliasStatus(ctx, apply.IdempotencyKey)
		assert.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN", "a copied operation is not evidence of an operation in this physical database")
		_, err = restored.RollbackProjectAliases(ctx, ProjectAliasRollbackRequest{ApplyKey: apply.IdempotencyKey, IdempotencyKey: uuid.NewString()}, "synthetic")
		assert.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN", "a copied operation must not authorize rollback in another physical database")
		var count int64
		require.NoError(t, target.Table("project_consolidation_redirects").Where("selector = ?", req.Aliases[0].Source).Count(&count).Error)
		assert.EqualValues(t, 1, count, "refused copied rollback must leave the target redirect unchanged")
	})
	t.Run("unbound_legacy_receipt_fails_closed", func(t *testing.T) {
		req := seed(t)
		plan, err := origin.PrepareProjectAliases(ctx, req)
		require.NoError(t, err)
		apply := ProjectAliasApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, IdempotencyKey: uuid.NewString()}
		_, err = origin.ApplyProjectAliases(ctx, apply, "synthetic")
		require.NoError(t, err)
		require.NoError(t, source.Exec(`UPDATE project_consolidation_operations SET receipt = receipt - 'database' WHERE idempotency_key = ?`, apply.IdempotencyKey).Error)
		_, err = origin.ProjectAliasStatus(ctx, apply.IdempotencyKey)
		assert.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
		_, err = origin.RollbackProjectAliases(ctx, ProjectAliasRollbackRequest{ApplyKey: apply.IdempotencyKey, IdempotencyKey: uuid.NewString()}, "synthetic")
		assert.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
		var count int64
		require.NoError(t, source.Table("project_consolidation_redirects").Where("selector = ?", req.Aliases[0].Source).Count(&count).Error)
		assert.EqualValues(t, 1, count)
	})
	t.Run("unavailable_database_identity_fails_closed", func(t *testing.T) {
		tx := source.Begin()
		require.NoError(t, tx.Error)
		defer tx.Rollback()
		require.Error(t, tx.Exec("SELECT 1 / 0").Error)
		_, err := consolidationDatabaseIdentity(tx)
		require.EqualError(t, err, "PROJECT_ADOPTION_SOURCE_IDENTITY_UNAVAILABLE")
	})
}
