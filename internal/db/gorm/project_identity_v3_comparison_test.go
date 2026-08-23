package gorm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/recoveryreceipt"
	gormlib "gorm.io/gorm"
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
	readback, err := store.ReadComparisonsByCorrelationV3(context.Background(), []projectidentity.CorrelationV3{observation.Correlation})
	require.NoError(t, err)
	require.Equal(t, []projectidentity.ComparisonObservationV3{observation}, readback)

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

func TestProjectIdentityV3ComparisonClientInstancePrivacyMigration166PreservesHistoryAndRejectsFutureLocators(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	t.Cleanup(cleanup)
	db := store.GetDB()
	schema := "t027_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, db.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Exec("DROP SCHEMA IF EXISTS "+schema+" CASCADE").Error)
	})

	migration165 := projectIdentityV3ComparisonsMigration165()
	migration166 := projectIdentityV3ComparisonClientInstancePrivacyMigration166()
	withProjectIdentityMigrationSchema(t, db, schema, func(tx *gormlib.DB) {
		scopedStore := &Store{DB: tx}
		require.NoError(t, migration165.Migrate(tx))
		insertComparison := func(target *gormlib.DB, clientInstanceID string) (projectidentity.CorrelationV3, error) {
			correlation, err := projectidentity.NewCorrelationV3("migration-166-correlation-" + uuid.NewString())
			if err != nil {
				return "", err
			}
			return correlation, target.Exec(`
				INSERT INTO project_identity_comparisons (
					comparison_id, idempotency_key, correlation, v3_outcome, legacy_outcome,
					classification, client_instance_id, transport, scope, freshness, evidence_fingerprint
				) VALUES (?, ?, ?, 'PROJECT_DESCRIPTOR_INVALID', 'refusal', 'refusal', ?, 'http', 'repository', 'fresh', ?)
			`, uuid.NewString(), comparisonStoreFingerprint("migration-166-"+uuid.NewString()), correlation, clientInstanceID, comparisonStoreFingerprint("migration-166-evidence-"+uuid.NewString())).Error
		}

		const historicalLocator = "C:private"
		historicalCorrelation, err := insertComparison(tx, historicalLocator)
		require.NoError(t, err, "migration 165 must reproduce the pre-166 comparison vocabulary")
		require.NoError(t, migration166.Migrate(tx))
		require.NoError(t, migration166.Migrate(tx), "migration 166 DDL must be idempotent")

		var historicalRows int64
		require.NoError(t, tx.Model(&ProjectIdentityComparison{}).Where("client_instance_id = ?", historicalLocator).Count(&historicalRows).Error)
		require.EqualValues(t, 1, historicalRows, "the forward privacy migration must not rewrite or delete historical evidence")

		readback, err := scopedStore.ReadComparisonsByCorrelationV3(context.Background(), []projectidentity.CorrelationV3{historicalCorrelation})
		require.NoError(t, err)
		require.Len(t, readback, 1)
		require.Equal(t, historicalLocator, readback[0].ClientInstanceID)
		require.False(t, readback[0].Valid(), "strict validation remains unavailable for the preserved locator")

		receipt, err := recoveryreceipt.BuildAR2IdentityExpandReceiptFromPersistedComparisons(context.Background(), scopedStore, recoveryreceipt.AR2IdentityExpandInput{
			Candidate: recoveryreceipt.AR2CandidateIdentity{
				SourceCommit:                strings.Repeat("a", 40),
				CandidateCommit:             strings.Repeat("a", 40),
				CandidatePayloadFingerprint: comparisonStoreFingerprint("migration-166-receipt"),
			},
			MigrationIDs: []string{
				"162_project_identity_v3",
				"163_project_identity_v3_resolution_attempts",
				"164_project_identity_v3_resolution_attempt_admin_audit",
				"165_project_identity_v3_comparisons",
				"166_project_identity_v3_comparison_client_instance_privacy",
			},
			DescriptorVersion: 3,
			SupportedTransports: []projectidentity.ComparisonTransportV3{
				projectidentity.ComparisonTransportGRPCV3,
				projectidentity.ComparisonTransportHTTPV3,
				projectidentity.ComparisonTransportHookV3,
				projectidentity.ComparisonTransportDaemonV3,
				projectidentity.ComparisonTransportOpenClawV3,
			},
			V2Compatibility: recoveryreceipt.AR2V2ReadCompatible,
			CapabilityState: recoveryreceipt.AR2CapabilityAvailable,
		}, []projectidentity.CorrelationV3{historicalCorrelation})
		require.NoError(t, err)
		encodedReceipt, err := json.Marshal(receipt)
		require.NoError(t, err)
		require.NotContains(t, string(encodedReceipt), historicalLocator, "the receipt must count historical evidence without exposing it")

		var constraintRows int64
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*) FROM pg_constraint
			WHERE conrelid = 'project_identity_comparisons'::regclass
				AND conname = 'project_identity_comparisons_client_instance_privacy_chk'
		`).Scan(&constraintRows).Error)
		require.EqualValues(t, 1, constraintRows, "migration 166 must install the privacy check")
		var validated bool
		require.NoError(t, tx.Raw(`
			SELECT convalidated FROM pg_constraint
			WHERE conrelid = 'project_identity_comparisons'::regclass
				AND conname = 'project_identity_comparisons_client_instance_privacy_chk'
		`).Scan(&validated).Error)
		require.False(t, validated, "the new check must preserve pre-existing comparison evidence")

		futureLocatorObservation := projectidentity.ComparisonObservationV3{
			IdempotencyKey:      comparisonStoreFingerprint("migration-166-future-record"),
			Correlation:         historicalCorrelation,
			V3Outcome:           projectidentity.ProjectDescriptorInvalidOutcomeV3,
			LegacyOutcome:       projectidentity.LegacyComparisonRefusalV2,
			ClientInstanceID:    historicalLocator,
			Transport:           projectidentity.ComparisonTransportHTTPV3,
			Scope:               projectidentity.ComparisonRepositoryScopeV3,
			Freshness:           projectidentity.ComparisonFreshV3,
			EvidenceFingerprint: comparisonStoreFingerprint("migration-166-future-record-evidence"),
		}
		_, err = scopedStore.RecordComparisonV3(context.Background(), futureLocatorObservation)
		require.ErrorIs(t, err, errProjectIdentityComparisonInvalid, "store writes must keep strict client-ID validation")
		_, err = projectidentity.RecordComparisonV3(context.Background(), scopedStore, futureLocatorObservation)
		require.Error(t, err, "public recording must keep strict client-ID validation")

		for _, clientInstanceID := range []string{"C:private", "http:private", "ssh:private"} {
			err := tx.Transaction(func(nested *gormlib.DB) error {
				_, err := insertComparison(nested, clientInstanceID)
				return err
			})
			require.Error(t, err, "migration 166 must reject future locator-shaped client IDs: %q", clientInstanceID)
		}
		_, err = insertComparison(tx, "fixture-install-166")
		require.NoError(t, err, "migration 166 must preserve valid opaque IDs")
	})
}

func comparisonStoreFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestObserveLegacyOutcomeV2OnlyCountsValidatedInventoryOwners(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	t.Cleanup(cleanup)
	db := store.GetDB()
	ctx := context.Background()

	projectKeys := []string{uuid.NewString(), uuid.NewString()}
	projectIDs := []string{uuid.NewString(), uuid.NewString()}
	for index := range projectKeys {
		require.NoError(t, db.Create(&Project{
			ID:         projectIDs[index],
			ProjectKey: sql.NullString{String: projectKeys[index], Valid: true},
		}).Error)
	}
	identifierValues := []string{"legacy-observer-one-" + uuid.NewString(), "legacy-observer-two-" + uuid.NewString()}
	identifiers := []ProjectIdentifier{
		{ProjectKey: projectKeys[0], Scheme: "binding_v2", NormalizedValue: identifierValues[0], Source: "comparison-test", Provenance: `{}`, Status: "active"},
		{ProjectKey: projectKeys[1], Scheme: "git_hash_v2", NormalizedValue: identifierValues[1], Source: "comparison-test", Provenance: `{}`, Status: "redirected"},
	}
	for index := range identifiers {
		require.NoError(t, db.Create(&identifiers[index]).Error)
	}
	t.Cleanup(func() {
		_ = db.Where("identifier_id IN ?", []string{identifiers[0].IdentifierID, identifiers[1].IdentifierID}).Delete(&ProjectIdentifier{}).Error
		_ = db.Where("id IN ?", projectIDs).Delete(&Project{}).Error
	})

	var projectsBefore, identifiersBefore, mergesBefore int64
	require.NoError(t, db.Model(&Project{}).Count(&projectsBefore).Error)
	require.NoError(t, db.Model(&ProjectIdentifier{}).Count(&identifiersBefore).Error)
	require.NoError(t, db.Model(&ProjectMergeAudit{}).Count(&mergesBefore).Error)

	legacyDescriptor := func(t *testing.T, legacy []projectidentity.LegacyIdentifierV3) projectidentity.LegacyComparisonDescriptorV3 {
		t.Helper()
		anchor := projectidentity.AnchorV3{Version: 3, ProjectID: uuid.NewString(), Name: "comparison-observer", Scope: "repository"}
		descriptor := projectidentity.DescriptorV3{
			Version:           3,
			AnchorProjectID:   anchor.ProjectID,
			Name:              anchor.Name,
			Scope:             anchor.Scope,
			LegacyIdentifiers: legacy,
			ClientInstanceID:  "comparison-observer-client",
		}
		validated, err := projectidentity.NewLegacyComparisonDescriptorV3(anchor, descriptor)
		require.NoError(t, err)
		return validated
	}
	one := legacyDescriptor(t, []projectidentity.LegacyIdentifierV3{{Scheme: "binding_v2", Value: identifierValues[0], Provenance: "comparison-test"}})
	zero := legacyDescriptor(t, []projectidentity.LegacyIdentifierV3{{Scheme: "binding_v2", Value: "missing-" + uuid.NewString(), Provenance: "comparison-test"}})
	multiple := legacyDescriptor(t, []projectidentity.LegacyIdentifierV3{
		{Scheme: "binding_v2", Value: identifierValues[0], Provenance: "comparison-test"},
		{Scheme: "git_hash_v2", Value: identifierValues[1], Provenance: "comparison-test"},
	})
	noIdentifiers := legacyDescriptor(t, nil)

	for _, testCase := range []struct {
		name       string
		descriptor projectidentity.LegacyComparisonDescriptorV3
		want       projectidentity.LegacyComparisonOutcomeV2
	}{
		{name: "one live owner resolves", descriptor: one, want: projectidentity.LegacyComparisonResolvedV2},
		{name: "zero live owners refuses", descriptor: zero, want: projectidentity.LegacyComparisonRefusalV2},
		{name: "multiple live owners refuses", descriptor: multiple, want: projectidentity.LegacyComparisonRefusalV2},
		{name: "no safe identifiers unavailable", descriptor: noIdentifiers, want: projectidentity.LegacyComparisonUnavailableV2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			outcome, err := store.ObserveLegacyOutcomeV2(ctx, testCase.descriptor)
			require.NoError(t, err)
			require.Equal(t, testCase.want, outcome)
		})
	}

	unavailable, err := (&Store{}).ObserveLegacyOutcomeV2(ctx, one)
	require.Error(t, err)
	require.Equal(t, projectidentity.LegacyComparisonUnavailableV2, unavailable)

	var projectsAfter, identifiersAfter, mergesAfter int64
	require.NoError(t, db.Model(&Project{}).Count(&projectsAfter).Error)
	require.NoError(t, db.Model(&ProjectIdentifier{}).Count(&identifiersAfter).Error)
	require.NoError(t, db.Model(&ProjectMergeAudit{}).Count(&mergesAfter).Error)
	require.Equal(t, projectsBefore, projectsAfter)
	require.Equal(t, identifiersBefore, identifiersAfter)
	require.Equal(t, mergesBefore, mergesAfter)
}
