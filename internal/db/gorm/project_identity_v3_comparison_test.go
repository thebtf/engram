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

	differentCorrelation, err := projectidentity.NewCorrelationV3("comparison-store-replay-" + uuid.NewString())
	require.NoError(t, err)
	conflictingEvidence := comparisonStoreFingerprint("conflicting-evidence-" + uuid.NewString())
	for _, testCase := range []struct {
		name   string
		mutate func(*projectidentity.ComparisonObservationV3)
	}{
		{name: "correlation", mutate: func(conflicting *projectidentity.ComparisonObservationV3) {
			conflicting.Correlation = differentCorrelation
		}},
		{name: "v3 outcome", mutate: func(conflicting *projectidentity.ComparisonObservationV3) {
			conflicting.V3Outcome = projectidentity.ProjectScopeMismatchOutcomeV3
		}},
		{name: "legacy outcome", mutate: func(conflicting *projectidentity.ComparisonObservationV3) {
			conflicting.LegacyOutcome = projectidentity.LegacyComparisonResolvedV2
		}},
		{name: "client instance", mutate: func(conflicting *projectidentity.ComparisonObservationV3) {
			conflicting.ClientInstanceID = "comparison-client-conflict-" + uuid.NewString()
		}},
		{name: "transport", mutate: func(conflicting *projectidentity.ComparisonObservationV3) {
			conflicting.Transport = projectidentity.ComparisonTransportGRPCV3
		}},
		{name: "scope", mutate: func(conflicting *projectidentity.ComparisonObservationV3) {
			conflicting.Scope = projectidentity.ComparisonDirectoryScopeV3
		}},
		{name: "freshness", mutate: func(conflicting *projectidentity.ComparisonObservationV3) {
			conflicting.Freshness = projectidentity.ComparisonStaleV3
		}},
		{name: "evidence", mutate: func(conflicting *projectidentity.ComparisonObservationV3) {
			conflicting.EvidenceFingerprint = conflictingEvidence
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			conflicting := observation
			testCase.mutate(&conflicting)

			receipt, err := projectidentity.RecordComparisonV3(context.Background(), store, conflicting)
			require.ErrorIs(t, err, errProjectIdentityComparisonReplayConflict)
			require.Zero(t, receipt)
		})
	}

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
	require.Equal(t, first, comparisonReceiptV3(persisted), "conflicting replays must not overwrite the original observation")
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
