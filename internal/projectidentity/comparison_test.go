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

func TestComparisonOriginV3BindsClaimToPhysicalChannelAndDerivesStableReferences(t *testing.T) {
	testCases := []struct {
		name     string
		physical ComparisonTransportV3
		claim    string
		want     ComparisonTransportV3
	}{
		{name: "grpc absent claim", physical: ComparisonTransportGRPCV3, want: ComparisonTransportGRPCV3},
		{name: "grpc claim", physical: ComparisonTransportGRPCV3, claim: "grpc", want: ComparisonTransportGRPCV3},
		{name: "daemon claim", physical: ComparisonTransportGRPCV3, claim: "daemon", want: ComparisonTransportDaemonV3},
		{name: "grpc rejects cross channel hook", physical: ComparisonTransportGRPCV3, claim: "hook", want: ComparisonTransportGRPCV3},
		{name: "grpc rejects unknown", physical: ComparisonTransportGRPCV3, claim: "unknown", want: ComparisonTransportGRPCV3},
		{name: "http absent claim", physical: ComparisonTransportHTTPV3, want: ComparisonTransportHTTPV3},
		{name: "hook claim", physical: ComparisonTransportHTTPV3, claim: "hook", want: ComparisonTransportHookV3},
		{name: "openclaw claim", physical: ComparisonTransportHTTPV3, claim: "openclaw", want: ComparisonTransportOpenClawV3},
		{name: "http rejects cross channel daemon", physical: ComparisonTransportHTTPV3, claim: "daemon", want: ComparisonTransportHTTPV3},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			origin := NewComparisonOriginV3(testCase.physical, testCase.claim, "request-17")
			require.Equal(t, testCase.want, origin.Transport())
		})
	}

	anchor, descriptor := comparisonTestDescriptorV3(t, "11111111-1111-4111-8111-111111111111", "daemon-install-17", "example.invalid/acme/engram", "legacy-comparison-17")
	origin := NewComparisonOriginV3(ComparisonTransportGRPCV3, "daemon", "request-17")
	first, err := origin.DeriveComparisonReferencesV3(anchor, descriptor, ResolveExistingIntentV3)
	require.NoError(t, err)
	second, err := origin.DeriveComparisonReferencesV3(anchor, descriptor, ResolveExistingIntentV3)
	require.NoError(t, err)
	require.Equal(t, first, second, "a retry with the same request and evidence must reuse all references")

	changedIntent, err := origin.DeriveComparisonReferencesV3(anchor, descriptor, ReadFilterIntentV3)
	require.NoError(t, err)
	require.NotEqual(t, first, changedIntent)
	clientAnchor, clientDescriptor := comparisonTestDescriptorV3(t, "33333333-3333-4333-8333-333333333333", "hook-client-install-17", "example.invalid/acme/engram", "legacy-comparison-17")
	originWithClientLikeClaim := NewComparisonOriginV3(ComparisonTransportGRPCV3, "", "request-17")
	clientLikeHook, err := originWithClientLikeClaim.DeriveComparisonReferencesV3(clientAnchor, clientDescriptor, ResolveExistingIntentV3)
	require.NoError(t, err)
	require.Equal(t, ComparisonTransportGRPCV3, originWithClientLikeClaim.Transport(), "client instance metadata must not infer an adapter")
	require.NotEqual(t, first, clientLikeHook)
}

func TestWithHTTPComparisonOriginV3BindsOnlyHTTPClaims(t *testing.T) {
	origin, ok := ComparisonOriginFromContextV3(WithHTTPComparisonOriginV3(context.Background(), "hook", "http-attempt-17"))
	require.True(t, ok)
	require.Equal(t, ComparisonTransportHookV3, origin.Transport())
	require.Equal(t, "http-attempt-17", origin.AttemptID())

	crossChannel, ok := ComparisonOriginFromContextV3(WithHTTPComparisonOriginV3(context.Background(), "daemon", "http-attempt-17"))
	require.True(t, ok)
	require.Equal(t, ComparisonTransportHTTPV3, crossChannel.Transport())
}

func TestComparisonOriginV3BindsReferencesToRedactedValidatedEvidence(t *testing.T) {
	projectID := "11111111-1111-4111-8111-111111111111"
	anchor, descriptor := comparisonTestDescriptorV3(t, projectID, "daemon-install-17", "example.invalid/acme/private-repository", "legacy-private-comparison")
	origin := NewComparisonOriginV3(ComparisonTransportGRPCV3, "daemon", "request-17")
	first, err := origin.DeriveComparisonReferencesV3(anchor, descriptor, ResolveExistingIntentV3)
	require.NoError(t, err)

	otherAnchor, otherDescriptor := comparisonTestDescriptorV3(t, "33333333-3333-4333-8333-333333333333", "daemon-install-17", "example.invalid/acme/private-repository", "legacy-private-comparison")
	otherProject, err := origin.DeriveComparisonReferencesV3(otherAnchor, otherDescriptor, ResolveExistingIntentV3)
	require.NoError(t, err)
	changedRemoteAnchor, changedRemote := comparisonTestDescriptorV3(t, projectID, "daemon-install-17", "example.invalid/acme/other-repository", "legacy-private-comparison")
	otherRemote, err := origin.DeriveComparisonReferencesV3(changedRemoteAnchor, changedRemote, ResolveExistingIntentV3)
	require.NoError(t, err)
	changedLegacyAnchor, changedLegacy := comparisonTestDescriptorV3(t, projectID, "daemon-install-17", "example.invalid/acme/private-repository", "legacy-other-comparison")
	otherLegacy, err := origin.DeriveComparisonReferencesV3(changedLegacyAnchor, changedLegacy, ResolveExistingIntentV3)
	require.NoError(t, err)
	for _, references := range []ComparisonReferencesV3{otherProject, otherRemote, otherLegacy} {
		require.NotEqual(t, first.IdempotencyKey, references.IdempotencyKey)
	}
	for _, raw := range []string{anchor.ProjectID, anchor.Name, descriptor.NormalizedGitRemotes[0], descriptor.LegacyIdentifiers[0].Value} {
		require.NotContains(t, first.IdempotencyKey, raw)
		require.NotContains(t, string(first.Correlation), raw)
		require.NotContains(t, first.EvidenceFingerprint, raw)
	}
}

func TestComparisonOriginV3SkipsUnstableRequestIdentity(t *testing.T) {
	anchor, descriptor := comparisonTestDescriptorV3(t, "11111111-1111-4111-8111-111111111111", "daemon-install-17", "example.invalid/acme/engram", "legacy-comparison-17")
	for _, requestID := range []string{"", "https://fixture-user:fixture-credential@example.invalid/private/request"} {
		origin := NewComparisonOriginV3(ComparisonTransportGRPCV3, "daemon", requestID)
		require.Empty(t, origin.AttemptID())
		_, err := origin.DeriveComparisonReferencesV3(anchor, descriptor, ResolveExistingIntentV3)
		require.Error(t, err)
	}
}

func comparisonTestDescriptorV3(t *testing.T, projectID, clientInstanceID, remote, legacy string) (AnchorV3, DescriptorV3) {
	t.Helper()
	anchor := AnchorV3{Version: 3, ProjectID: projectID, Name: "private/comparison-project", Scope: "repository"}
	descriptor, err := BuildDescriptorV3(anchor, []string{remote}, []LegacyIdentifierV3{{Scheme: "binding_v2", Value: legacy, Provenance: "comparison-test"}}, clientInstanceID)
	require.NoError(t, err)
	return anchor, descriptor
}
