package recoveryreceipt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/operability"
	"github.com/thebtf/engram/internal/projectidentity"
)

func TestBuildAR2IdentityExpandReceiptFromControlledFixtureRequiresExactFiveCallableCorrelations(t *testing.T) {
	capture, observations := ar2ControlledFixtureInput(t)
	reader := &persistedComparisonReaderV3{comparisons: observations}
	before := capture.attestation
	expectedRead, err := capture.ordered()
	if err != nil {
		t.Fatal(err)
	}

	receipt, err := buildAR2IdentityExpandReceiptFromControlledFixture(context.Background(), reader, capture)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(capture.attestation, before) || reader.calls != 1 || !reflect.DeepEqual(reader.requested, expectedRead[:]) {
		t.Fatalf("receipt did not exact-read its controlled-fixture input once: capture=%#v reader=%#v", capture, reader)
	}
	if receipt.SchemaVersion != AR2IdentityExpandSchemaVersion || receipt.SchemaVersion == "engram.recovery.ar2-identity-expand-receipt.v1" || receipt.ReceiptAuthority != AR2IdentityExpandAuthority || receipt.EvidenceScope != ar2EvidenceScope || receipt.RuntimeLabelTrust != ar2RuntimeLabelTrust || receipt.CompatibilityWindow != AR2CompatibilityWindow || receipt.Candidate != capture.attestation.candidate || receipt.DescriptorVersion != 3 || receipt.V2Compatibility != AR2V2ReadCompatible || receipt.CapabilityState != AR2CapabilityAvailable {
		t.Fatalf("receipt identity = %#v", receipt)
	}
	if !reflect.DeepEqual(receipt.MigrationIDs, ar2IdentityExpandMigrationIDs) || len(receipt.SupportedAdapters) != len(ar2ControlledFixtureCallables) || len(receipt.AdapterCoverage) != len(ar2ControlledFixtureCallables) {
		t.Fatalf("receipt callable contract = %#v", receipt)
	}
	for index, spec := range ar2ControlledFixtureCallables {
		adapter := receipt.SupportedAdapters[index]
		coverage := receipt.AdapterCoverage[index]
		if adapter.Adapter != spec.adapter || adapter.Callable != spec.callable || adapter.PhysicalChannel != spec.physicalChannel || coverage.Adapter != spec.adapter || coverage.Callable != spec.callable || coverage.Metric.DenominatorValue != 1 || coverage.Metric.NumeratorValue != 1 || coverage.Metric.ResultStatus != operability.Computed || coverage.Metric.Ratio == nil || *coverage.Metric.Ratio != 1 {
			t.Fatalf("callable slot %d = %#v / %#v", index, adapter, coverage)
		}
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"C:private", "https://fixture-user:fixture-password@git.example.test/private/repo", "fixture-password", "ar2-receipt-client"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("receipt leaked raw comparison input %q: %s", forbidden, encoded)
		}
	}
}

func TestBuildAR2IdentityExpandReceiptFromControlledFixtureIgnoresPersistedAdapterLabels(t *testing.T) {
	capture, observations := ar2ControlledFixtureInput(t)
	for index := range observations {
		observations[index].Transport = projectidentity.ComparisonTransportOpenClawV3
	}

	receipt, err := buildAR2IdentityExpandReceiptFromControlledFixture(context.Background(), &persistedComparisonReaderV3{comparisons: observations}, capture)
	if err != nil {
		t.Fatal(err)
	}
	for index, spec := range ar2ControlledFixtureCallables {
		coverage := receipt.AdapterCoverage[index]
		if coverage.Adapter != spec.adapter || coverage.Equal+coverage.Mismatch+coverage.Refusal+coverage.Unavailable != 1 {
			t.Fatalf("persisted label selected coverage bucket at %d: %#v", index, coverage)
		}
	}
}

func TestBuildAR2IdentityExpandReceiptFromControlledFixtureRejectsUnboundPersistedCoverage(t *testing.T) {
	_, observations := ar2ControlledFixtureInput(t)
	capture, err := newAR2ControlledFixtureCapture(ar2FixtureCandidateAttestation())
	if err != nil {
		t.Fatal(err)
	}
	reader := &persistedComparisonReaderV3{comparisons: observations}
	if _, err := buildAR2IdentityExpandReceiptFromControlledFixture(context.Background(), reader, capture); err == nil {
		t.Fatal("five persisted rows with apparent adapter labels built a trusted receipt without controlled fixture capture")
	}
	if reader.calls != 0 {
		t.Fatal("unbound persisted coverage reached durable readback")
	}
}

func TestBuildAR2IdentityExpandReceiptFromControlledFixtureRejectsNonExactReadback(t *testing.T) {
	capture, observations := ar2ControlledFixtureInput(t)
	unrequested := observations[0]
	unrequested.Correlation = ar2Correlation(t, "ar2-unrequested-correlation")
	unrequested.IdempotencyKey = ar2ReceiptFingerprint("ar2-unrequested-idempotency")
	missing := *capture
	missing.correlations[0] = ""
	duplicate := *capture
	duplicate.correlations[1] = duplicate.correlations[0]
	invalid := *capture
	invalid.correlations[0] = "not/a-correlation"
	unrelatedPayload := *capture
	unrelatedPayload.attestation.runtimePayloadFingerprint = ar2ReceiptFingerprint("unrelated-candidate-payload")
	mismatchedHealth := *capture
	mismatchedHealth.attestation.healthSourceCommit = strings.Repeat("b", 40)
	cases := []struct {
		name    string
		capture *ar2ControlledFixtureCapture
		reader  *persistedComparisonReaderV3
	}{
		{name: "missing fixed slot", capture: &missing, reader: &persistedComparisonReaderV3{comparisons: observations}},
		{name: "duplicate fixed slots", capture: &duplicate, reader: &persistedComparisonReaderV3{comparisons: observations}},
		{name: "invalid fixed slot", capture: &invalid, reader: &persistedComparisonReaderV3{comparisons: observations}},
		{name: "missing durable row", capture: capture, reader: &persistedComparisonReaderV3{comparisons: observations[:4]}},
		{name: "duplicate durable row", capture: capture, reader: &persistedComparisonReaderV3{comparisons: append(append([]projectidentity.ComparisonObservationV3(nil), observations...), observations[0])}},
		{name: "unrequested durable row", capture: capture, reader: &persistedComparisonReaderV3{comparisons: append(append([]projectidentity.ComparisonObservationV3(nil), observations[:4]...), unrequested)}},
		{name: "reader failure", capture: capture, reader: &persistedComparisonReaderV3{err: errors.New("fixture read failed")}},
		{name: "unrelated runtime payload", capture: &unrelatedPayload, reader: &persistedComparisonReaderV3{comparisons: observations}},
		{name: "mismatched health commit", capture: &mismatchedHealth, reader: &persistedComparisonReaderV3{comparisons: observations}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := buildAR2IdentityExpandReceiptFromControlledFixture(context.Background(), testCase.reader, testCase.capture); err == nil {
				t.Fatal("builder accepted non-exact controlled-fixture evidence")
			}
		})
	}
}

func TestBuildAR2IdentityExpandReceiptFromControlledFixtureBindsOrderedEvidenceAndRedactsLegacyReadback(t *testing.T) {
	capture, observations := ar2ControlledFixtureInput(t)
	observations[0].ClientInstanceID = "C:private"

	first, err := buildAR2IdentityExpandReceiptFromControlledFixture(context.Background(), &persistedComparisonReaderV3{comparisons: observations}, capture)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildAR2IdentityExpandReceiptFromControlledFixture(context.Background(), &persistedComparisonReaderV3{comparisons: observations}, capture)
	if err != nil || first.FixtureEvidenceFingerprint != second.FixtureEvidenceFingerprint {
		t.Fatalf("fixture evidence fingerprint is not deterministic: %#v / %#v / %v", first, second, err)
	}
	swapped := *capture
	swapped.correlations[0], swapped.correlations[1] = swapped.correlations[1], swapped.correlations[0]
	changed, err := buildAR2IdentityExpandReceiptFromControlledFixture(context.Background(), &persistedComparisonReaderV3{comparisons: observations}, &swapped)
	if err != nil || changed.FixtureEvidenceFingerprint == first.FixtureEvidenceFingerprint {
		t.Fatalf("ordered controlled-fixture correlations do not bind receipt evidence: %#v / %#v / %v", first, changed, err)
	}
	changedEvidence := append([]projectidentity.ComparisonObservationV3(nil), observations...)
	changedEvidence[0].EvidenceFingerprint = ar2ReceiptFingerprint("changed-durable-evidence")
	withChangedEvidence, err := buildAR2IdentityExpandReceiptFromControlledFixture(context.Background(), &persistedComparisonReaderV3{comparisons: changedEvidence}, capture)
	if err != nil || withChangedEvidence.FixtureEvidenceFingerprint == first.FixtureEvidenceFingerprint {
		t.Fatalf("durable evidence fingerprint does not bind receipt evidence: %#v / %#v / %v", first, withChangedEvidence, err)
	}
	changedClassification := append([]projectidentity.ComparisonObservationV3(nil), observations...)
	changedClassification[0].LegacyOutcome = projectidentity.LegacyComparisonRefusalV2
	withChangedClassification, err := buildAR2IdentityExpandReceiptFromControlledFixture(context.Background(), &persistedComparisonReaderV3{comparisons: changedClassification}, capture)
	if err != nil || withChangedClassification.FixtureEvidenceFingerprint == first.FixtureEvidenceFingerprint {
		t.Fatalf("durable classification does not bind receipt evidence: %#v / %#v / %v", first, withChangedClassification, err)
	}
	unrelatedPayload := *capture
	unrelatedPayload.attestation.runtimePayloadFingerprint = ar2ReceiptFingerprint("unrelated-candidate-payload")
	if _, err := buildAR2IdentityExpandReceiptFromControlledFixture(context.Background(), &persistedComparisonReaderV3{comparisons: observations}, &unrelatedPayload); err == nil {
		t.Fatal("receipt accepted a payload that was not attested by the runtime")
	}
	encoded, err := json.Marshal(first)
	if err != nil || strings.Contains(string(encoded), "C:private") {
		t.Fatalf("receipt exposed historical locator: %s / %v", encoded, err)
	}
}

func TestAR2ControlledFixtureCaptureRejectsSwappedCallableSlots(t *testing.T) {
	capture, err := newAR2ControlledFixtureCapture(ar2FixtureCandidateAttestation())
	if err != nil {
		t.Fatal(err)
	}
	if err := capture.record(ar2ControlledFixtureCallables[1].callable, ar2Correlation(t, "ar2-receipt-grpc")); err == nil {
		t.Fatal("swapped callable evidence slot was accepted")
	}
}

func ar2ControlledFixtureInput(t *testing.T) (*ar2ControlledFixtureCapture, []projectidentity.ComparisonObservationV3) {
	t.Helper()
	capture, err := newAR2ControlledFixtureCapture(ar2FixtureCandidateAttestation())
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range ar2ControlledFixtureCallables {
		if err := capture.record(spec.callable, ar2Correlation(t, "ar2-receipt-"+spec.adapter)); err != nil {
			t.Fatal(err)
		}
	}
	ordered, err := capture.ordered()
	if err != nil {
		t.Fatal(err)
	}
	classes := []struct {
		outcome projectidentity.ResolutionOutcomeV3
		legacy  projectidentity.LegacyComparisonOutcomeV2
	}{
		{projectidentity.ProjectResolvedOutcomeV3, projectidentity.LegacyComparisonResolvedV2},
		{projectidentity.ProjectResolvedOutcomeV3, projectidentity.LegacyComparisonRefusalV2},
		{projectidentity.ProjectDescriptorInvalidOutcomeV3, projectidentity.LegacyComparisonResolvedV2},
		{projectidentity.ProjectResolvedOutcomeV3, projectidentity.LegacyComparisonUnavailableV2},
		{projectidentity.ProjectResolvedOutcomeV3, projectidentity.LegacyComparisonResolvedV2},
	}
	observations := make([]projectidentity.ComparisonObservationV3, 0, len(ordered))
	for index, correlation := range ordered {
		observation, err := projectidentity.NewComparisonObservationV3(
			ar2ReceiptFingerprint("idempotency-"+string(correlation)),
			correlation,
			classes[index].outcome,
			classes[index].legacy,
			"ar2-receipt-client-"+ar2ControlledFixtureCallables[index].adapter,
			projectidentity.ComparisonTransportOpenClawV3,
			projectidentity.ComparisonRepositoryScopeV3,
			projectidentity.ComparisonFreshV3,
			ar2ReceiptFingerprint("evidence-"+string(correlation)),
		)
		if err != nil {
			t.Fatal(err)
		}
		observations = append(observations, observation)
	}
	return capture, observations
}

func ar2FixtureCandidateAttestation() ar2CandidateAttestation {
	candidateCommit := strings.Repeat("a", 40)
	payloadFingerprint := ar2ReceiptFingerprint("candidate-payload")
	return ar2CandidateAttestation{
		candidate: ar2CandidateIdentity{
			SourceCommit:                candidateCommit,
			CandidateCommit:             candidateCommit,
			CandidatePayloadFingerprint: payloadFingerprint,
		},
		healthSourceCommit:        candidateCommit,
		runtimePayloadFingerprint: payloadFingerprint,
		v2Compatibility:           AR2V2ReadCompatible,
		capabilityState:           AR2CapabilityAvailable,
	}
}

func ar2Correlation(t *testing.T, value string) projectidentity.CorrelationV3 {
	t.Helper()
	correlation, err := projectidentity.NewCorrelationV3(value)
	if err != nil {
		t.Fatal(err)
	}
	return correlation
}

func ar2ReceiptFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type persistedComparisonReaderV3 struct {
	comparisons []projectidentity.ComparisonObservationV3
	err         error
	calls       int
	requested   []projectidentity.CorrelationV3
}

func (reader *persistedComparisonReaderV3) ReadComparisonsByCorrelationV3(_ context.Context, correlations []projectidentity.CorrelationV3) ([]projectidentity.ComparisonObservationV3, error) {
	reader.calls++
	reader.requested = append([]projectidentity.CorrelationV3(nil), correlations...)
	return append([]projectidentity.ComparisonObservationV3(nil), reader.comparisons...), reader.err
}
