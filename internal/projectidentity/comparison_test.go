package projectidentity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestComparisonObservationV3Classification(t *testing.T) {
	correlation, err := NewCorrelationV3("comparison-correlation-" + uuid.NewString())
	require.NoError(t, err)
	key := comparisonTestFingerprint("idempotency-key-" + uuid.NewString())
	evidence := comparisonTestFingerprint("evidence-" + uuid.NewString())
	projectKey, err := NewProjectKeyV3(uuid.NewString())
	require.NoError(t, err)

	resolved, err := NewSuccessResultV3(ResolveExistingIntentV3, ProjectResolvedOutcomeV3, projectKey, RepositoryResolvedScopeV3, "", correlation)
	require.NoError(t, err)
	refused, err := NewRefusalResultV3(ResolveExistingIntentV3, ProjectDescriptorInvalidOutcomeV3, correlation)
	require.NoError(t, err)

	for _, test := range []struct {
		name      string
		v3Outcome ResolutionOutcomeV3
		legacy    LegacyComparisonOutcomeV2
		wantClass ComparisonClassV3
	}{
		{name: "equal resolved", v3Outcome: resolved.Outcome(), legacy: LegacyComparisonResolvedV2, wantClass: ComparisonEqualV3},
		{name: "mismatch legacy refusal", v3Outcome: resolved.Outcome(), legacy: LegacyComparisonRefusalV2, wantClass: ComparisonMismatchV3},
		{name: "v3 refusal", v3Outcome: refused.Outcome(), legacy: LegacyComparisonResolvedV2, wantClass: ComparisonRefusalV3},
		{name: "legacy unavailable", v3Outcome: resolved.Outcome(), legacy: LegacyComparisonUnavailableV2, wantClass: ComparisonUnavailableV3},
	} {
		t.Run(test.name, func(t *testing.T) {
			observation, err := NewComparisonObservationV3(key, correlation, test.v3Outcome, test.legacy, "comparison-client-"+uuid.NewString(), ComparisonTransportGRPCV3, ComparisonRepositoryScopeV3, ComparisonFreshV3, evidence)
			require.NoError(t, err)
			require.Equal(t, test.wantClass, observation.Classification())
		})
	}
}

func TestRecordComparisonV3RecordsOnlySafeTelemetry(t *testing.T) {
	correlation, err := NewCorrelationV3("comparison-refusal-" + uuid.NewString())
	require.NoError(t, err)
	refusal, err := NewRefusalResultV3(ResolveExistingIntentV3, ProjectDescriptorInvalidOutcomeV3, correlation)
	require.NoError(t, err)
	observation, err := NewComparisonObservationV3(
		comparisonTestFingerprint("idempotency-"+uuid.NewString()),
		correlation,
		refusal.Outcome(),
		LegacyComparisonRefusalV2,
		"comparison-client-"+uuid.NewString(),
		ComparisonTransportHTTPV3,
		ComparisonRepositoryScopeV3,
		ComparisonFreshV3,
		comparisonTestFingerprint("evidence-"+uuid.NewString()),
	)
	require.NoError(t, err)

	store := &comparisonStoreSpy{}
	receipt, err := RecordComparisonV3(context.Background(), store, observation)
	require.NoError(t, err)
	require.Equal(t, ComparisonRefusalV3, receipt.Classification)
	require.Equal(t, observation.IdempotencyKey, receipt.IdempotencyKey)
	require.Equal(t, observation.Correlation, receipt.Correlation)
	require.Equal(t, observation.V3Outcome, receipt.V3Outcome)
	require.Equal(t, observation.LegacyOutcome, receipt.LegacyOutcome)
	require.Len(t, store.observations, 1)
	require.Equal(t, observation, store.observations[0])
}

func TestComparisonObservationV3RejectsUnsafeInputs(t *testing.T) {
	correlation, err := NewCorrelationV3("comparison-invalid-" + uuid.NewString())
	require.NoError(t, err)

	for _, test := range []struct {
		name        string
		idempotency string
		client      string
		evidence    string
	}{
		{name: "raw idempotency key", idempotency: "raw-idempotency", client: "comparison-client", evidence: comparisonTestFingerprint("evidence")},
		{name: "raw descriptor fingerprint", idempotency: comparisonTestFingerprint("idempotency"), client: "comparison-client", evidence: "https://user:credential@example.invalid/private/repo"},
		{name: "path client", idempotency: comparisonTestFingerprint("idempotency"), client: "C:/private/repo", evidence: comparisonTestFingerprint("evidence")},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewComparisonObservationV3(test.idempotency, correlation, ProjectDescriptorInvalidOutcomeV3, LegacyComparisonRefusalV2, test.client, ComparisonTransportHookV3, ComparisonDirectoryScopeV3, ComparisonFreshV3, test.evidence)
			require.Error(t, err)
		})
	}
}

type comparisonStoreSpy struct {
	observations []ComparisonObservationV3
}

func (store *comparisonStoreSpy) RecordComparisonV3(_ context.Context, observation ComparisonObservationV3) (ComparisonReceiptV3, error) {
	store.observations = append(store.observations, observation)
	return ComparisonReceiptV3{
		RecordID:            uuid.NewString(),
		IdempotencyKey:      observation.IdempotencyKey,
		Correlation:         observation.Correlation,
		V3Outcome:           observation.V3Outcome,
		LegacyOutcome:       observation.LegacyOutcome,
		Classification:      observation.Classification(),
		ClientInstanceID:    observation.ClientInstanceID,
		Transport:           observation.Transport,
		Scope:               observation.Scope,
		Freshness:           observation.Freshness,
		EvidenceFingerprint: observation.EvidenceFingerprint,
		RecordedAt:          time.Unix(1, 0).UTC(),
	}, nil
}

func comparisonTestFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
