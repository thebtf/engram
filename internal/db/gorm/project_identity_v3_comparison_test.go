package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/projectidentity"
)

func TestProjectIdentityV3ComparisonStoreRedactsAndReplaysReceipt(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	t.Cleanup(cleanup)
	db := store.GetDB()
	require.NoError(t, projectIdentityV3ComparisonsMigration165().Migrate(db))

	idempotencyKey := comparisonStoreFingerprint("idempotency-" + uuid.NewString())
	correlation, err := projectidentity.NewCorrelationV3("comparison-store-" + uuid.NewString())
	require.NoError(t, err)
	refusal, err := projectidentity.NewRefusalResultV3(projectidentity.ResolveExistingIntentV3, projectidentity.ProjectDescriptorInvalidOutcomeV3, correlation)
	require.NoError(t, err)
	observation, err := projectidentity.NewComparisonObservationV3(
		idempotencyKey,
		correlation,
		refusal.Outcome(),
		projectidentity.LegacyComparisonRefusalV2,
		"comparison-client-"+uuid.NewString(),
		projectidentity.ComparisonTransportHTTPV3,
		projectidentity.ComparisonRepositoryScopeV3,
		projectidentity.ComparisonFreshV3,
		comparisonStoreFingerprint("evidence-"+uuid.NewString()),
	)
	require.NoError(t, err)

	var projectsBefore, mergesBefore int64
	require.NoError(t, db.Model(&Project{}).Count(&projectsBefore).Error)
	require.NoError(t, db.Model(&ProjectMergeAudit{}).Count(&mergesBefore).Error)
	t.Cleanup(func() {
		_ = db.Where("idempotency_key = ?", idempotencyKey).Delete(&ProjectIdentityComparison{}).Error
	})

	first, err := projectidentity.RecordComparisonV3(context.Background(), store, observation)
	require.NoError(t, err)
	second, err := projectidentity.RecordComparisonV3(context.Background(), store, observation)
	require.NoError(t, err)
	require.Equal(t, first, second, "a repeated observation must return the original receipt")
	require.Equal(t, projectidentity.ComparisonRefusalV3, first.Classification)

	var rows int64
	require.NoError(t, db.Model(&ProjectIdentityComparison{}).Where("idempotency_key = ?", idempotencyKey).Count(&rows).Error)
	require.EqualValues(t, 1, rows, "idempotency must leave one durable record")

	var projectsAfter, mergesAfter int64
	require.NoError(t, db.Model(&Project{}).Count(&projectsAfter).Error)
	require.NoError(t, db.Model(&ProjectMergeAudit{}).Count(&mergesAfter).Error)
	require.Equal(t, projectsBefore, projectsAfter, "comparison must not create or bind a project")
	require.Equal(t, mergesBefore, mergesAfter, "comparison must not create a merge audit")

	var persisted ProjectIdentityComparison
	require.NoError(t, db.Where("idempotency_key = ?", idempotencyKey).First(&persisted).Error)
	persistedText := strings.Join([]string{
		persisted.IdempotencyKey,
		persisted.Correlation,
		persisted.V3Outcome,
		persisted.LegacyOutcome,
		persisted.Classification,
		persisted.ClientInstanceID,
		persisted.Transport,
		persisted.Scope,
		persisted.Freshness,
		persisted.EvidenceFingerprint,
	}, "|")
	for _, raw := range []string{
		"https://fixture-user:fixture-credential@example.invalid/private/repo",
		`C:\\private\\repo`,
		"fixture-credential",
		uuid.NewString(),
	} {
		require.NotContains(t, persistedText, raw, "refusal telemetry must retain only safe metadata")
	}

	var sensitiveColumns int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'project_identity_comparisons'
		  AND column_name IN ('descriptor', 'remote', 'path', 'credential', 'canonical_key', 'project_key', 'target_project_key')
	`).Scan(&sensitiveColumns).Error)
	require.Zero(t, sensitiveColumns, "comparison storage must not offer raw identity or authority columns")

	invalid := observation
	invalid.EvidenceFingerprint = "https://fixture-user:fixture-credential@example.invalid/private/repo"
	_, err = store.RecordComparisonV3(context.Background(), invalid)
	require.Error(t, err, "direct storage calls must reject writes before a valid comparison boundary")
}

func comparisonStoreFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
