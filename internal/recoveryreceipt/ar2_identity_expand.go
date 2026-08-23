package recoveryreceipt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/thebtf/engram/internal/operability"
	"github.com/thebtf/engram/internal/projectidentity"
)

const (
	AR2IdentityExpandSchemaVersion = "engram.recovery.ar2-identity-expand-receipt.v2"
	AR2IdentityExpandAuthority     = "ar2_controlled_fixture_callable_provenance"
	AR2CompatibilityWindow         = "v3_descriptor_with_v2_read_compatibility"
	ar2EvidenceScope               = "controlled_fixture_only"
	ar2RuntimeLabelTrust           = "advisory_untrusted_for_coverage"
)

var ar2IdentityExpandMigrationIDs = []string{
	"162_project_identity_v3",
	"163_project_identity_v3_resolution_attempts",
	"164_project_identity_v3_resolution_attempt_admin_audit",
	"165_project_identity_v3_comparisons",
	"166_project_identity_v3_comparison_client_instance_privacy",
}

type ar2CallableSpec struct {
	adapter         string
	callable        string
	physicalChannel projectidentity.ComparisonTransportV3
}

// ar2ControlledFixtureCallables is the only source of AR-2 adapter coverage.
// Durable comparison Transport remains advisory telemetry and must never select
// an adapter bucket here.
var ar2ControlledFixtureCallables = [...]ar2CallableSpec{
	{adapter: "grpc", callable: "EngramService.Initialize", physicalChannel: projectidentity.ComparisonTransportGRPCV3},
	{adapter: "http", callable: "POST /api/context/inject", physicalChannel: projectidentity.ComparisonTransportHTTPV3},
	{adapter: "hook", callable: "registerProjectIdentityV3", physicalChannel: projectidentity.ComparisonTransportHTTPV3},
	{adapter: "daemon", callable: "Module.ProxyTools Initialize", physicalChannel: projectidentity.ComparisonTransportGRPCV3},
	{adapter: "openclaw", callable: "EngramRestClient.registerAndResolveProject", physicalChannel: projectidentity.ComparisonTransportHTTPV3},
}

// AR2V2CompatibilityState states whether the preserved V2 read boundary remains usable.
type AR2V2CompatibilityState string

const AR2V2ReadCompatible AR2V2CompatibilityState = "v2_read_compatible"

// AR2CapabilityState is the declared non-authoritative state of the V3 adapter set.
type AR2CapabilityState string

const (
	AR2CapabilityConfigured AR2CapabilityState = "configured"
	AR2CapabilityAvailable  AR2CapabilityState = "available"
	AR2CapabilityDegraded   AR2CapabilityState = "degraded"
	AR2CapabilityPaused     AR2CapabilityState = "paused"
	AR2CapabilityFailed     AR2CapabilityState = "failed"
)

// ar2CandidateIdentity binds the receipt to the exact source and staged candidate.
type ar2CandidateIdentity struct {
	SourceCommit                string `json:"source_commit"`
	CandidateCommit             string `json:"candidate_commit"`
	CandidatePayloadFingerprint string `json:"candidate_payload_fingerprint"`
}

// ar2CandidateAttestation is private runtime evidence. A receipt accepts it only
// when the served health commit and built runtime payload agree with its candidate.
type ar2CandidateAttestation struct {
	candidate                 ar2CandidateIdentity
	healthSourceCommit        string
	runtimePayloadFingerprint string
	v2Compatibility           AR2V2CompatibilityState
	capabilityState           AR2CapabilityState
}

// ar2ControlledFixtureCapture is a fixed-order capability held by the fixture
// wrappers. Durable rows alone cannot construct or complete it.
type ar2ControlledFixtureCapture struct {
	attestation  ar2CandidateAttestation
	correlations [len(ar2ControlledFixtureCallables)]projectidentity.CorrelationV3
	next         int
}

func newAR2ControlledFixtureCapture(attestation ar2CandidateAttestation) (*ar2ControlledFixtureCapture, error) {
	if err := validateAR2CandidateAttestation(attestation); err != nil {
		return nil, err
	}
	return &ar2ControlledFixtureCapture{attestation: attestation}, nil
}

func (capture *ar2ControlledFixtureCapture) record(callable string, correlation projectidentity.CorrelationV3) error {
	if capture == nil || capture.next >= len(ar2ControlledFixtureCallables) {
		return fmt.Errorf("controlled-fixture capture cannot accept another callable")
	}
	spec := ar2ControlledFixtureCallables[capture.next]
	if callable != spec.callable {
		return fmt.Errorf("controlled-fixture callable slot %d is not %s", capture.next, spec.callable)
	}
	if _, err := projectidentity.NewCorrelationV3(string(correlation)); err != nil {
		return fmt.Errorf("invalid %s controlled-fixture correlation", spec.adapter)
	}
	capture.correlations[capture.next] = correlation
	capture.next++
	return nil
}

func (capture *ar2ControlledFixtureCapture) ordered() ([len(ar2ControlledFixtureCallables)]projectidentity.CorrelationV3, error) {
	if capture == nil || capture.next != len(capture.correlations) {
		return [len(ar2ControlledFixtureCallables)]projectidentity.CorrelationV3{}, fmt.Errorf("controlled-fixture capture is incomplete")
	}
	return capture.correlations, nil
}

// AR2SupportedAdapter names an immutable fixture callable and its actual
// physical channel. Adapter labels persisted with comparison rows are advisory.
type AR2SupportedAdapter struct {
	Adapter         string                                `json:"adapter"`
	Callable        string                                `json:"callable"`
	PhysicalChannel projectidentity.ComparisonTransportV3 `json:"physical_channel"`
}

// AR2CallableCoverage renders one exact-read durable observation under its
// controlled-fixture callable slot, never under its persisted Transport label.
type AR2CallableCoverage struct {
	Adapter     string             `json:"adapter"`
	Callable    string             `json:"callable"`
	Equal       int64              `json:"equal"`
	Mismatch    int64              `json:"mismatch"`
	Refusal     int64              `json:"refusal"`
	Unavailable int64              `json:"unavailable"`
	Metric      operability.Metric `json:"comparison_coverage"`
}

// AR2RollbackBoundary is the fixed additive rollback disposition for this receipt.
type AR2RollbackBoundary struct {
	V3AdapterAction string   `json:"v3_adapter_action"`
	ReadBoundary    string   `json:"read_boundary"`
	Preserve        []string `json:"preserve"`
}

// ar2IdentityExpandReceipt is a read-only, redacted AR-2 controlled-fixture receipt.
type ar2IdentityExpandReceipt struct {
	SchemaVersion              string                  `json:"schema_version"`
	ReceiptAuthority           string                  `json:"receipt_authority"`
	EvidenceScope              string                  `json:"evidence_scope"`
	RuntimeLabelTrust          string                  `json:"runtime_label_trust"`
	CompatibilityWindow        string                  `json:"compatibility_window"`
	Candidate                  ar2CandidateIdentity    `json:"candidate"`
	FixtureEvidenceFingerprint string                  `json:"fixture_evidence_fingerprint"`
	MigrationIDs               []string                `json:"migration_ids"`
	DescriptorVersion          int                     `json:"descriptor_version"`
	SupportedAdapters          []AR2SupportedAdapter   `json:"supported_adapters"`
	AdapterCoverage            []AR2CallableCoverage   `json:"adapter_coverage"`
	V2Compatibility            AR2V2CompatibilityState `json:"v2_compatibility_state"`
	CapabilityState            AR2CapabilityState      `json:"capability_state"`
	RollbackBoundary           AR2RollbackBoundary     `json:"rollback_boundary"`
}

// buildAR2IdentityExpandReceiptFromControlledFixture exact-reads the five
// correlations held by the fixed-order wrapper capture. The capture cannot be
// reconstructed from persisted comparison labels or durable rows alone.
func buildAR2IdentityExpandReceiptFromControlledFixture(ctx context.Context, reader projectidentity.ComparisonReaderV3, capture *ar2ControlledFixtureCapture) (ar2IdentityExpandReceipt, error) {
	if reader == nil {
		return ar2IdentityExpandReceipt{}, fmt.Errorf("controlled-fixture comparison reader is required")
	}
	if capture == nil {
		return ar2IdentityExpandReceipt{}, fmt.Errorf("controlled-fixture capture is required")
	}
	if err := validateAR2CandidateAttestation(capture.attestation); err != nil {
		return ar2IdentityExpandReceipt{}, err
	}
	ordered, err := capture.ordered()
	if err != nil {
		return ar2IdentityExpandReceipt{}, err
	}
	requested := make(map[projectidentity.CorrelationV3]struct{}, len(ordered))
	for _, correlation := range ordered {
		if _, err := projectidentity.NewCorrelationV3(string(correlation)); err != nil {
			return ar2IdentityExpandReceipt{}, fmt.Errorf("invalid controlled-fixture correlation")
		}
		requested[correlation] = struct{}{}
	}
	observations, err := reader.ReadComparisonsByCorrelationV3(ctx, ordered[:])
	if err != nil {
		return ar2IdentityExpandReceipt{}, fmt.Errorf("read controlled-fixture comparisons: %w", err)
	}
	if len(observations) != len(ordered) {
		return ar2IdentityExpandReceipt{}, fmt.Errorf("controlled-fixture comparison readback is not exact")
	}
	byCorrelation := make(map[projectidentity.CorrelationV3]projectidentity.ComparisonObservationV3, len(observations))
	for _, observation := range observations {
		if !observation.ValidPersistedLegacyReadback() {
			return ar2IdentityExpandReceipt{}, fmt.Errorf("invalid persisted controlled-fixture comparison")
		}
		if _, ok := requested[observation.Correlation]; !ok {
			return ar2IdentityExpandReceipt{}, fmt.Errorf("unrequested persisted controlled-fixture comparison")
		}
		if _, duplicate := byCorrelation[observation.Correlation]; duplicate {
			return ar2IdentityExpandReceipt{}, fmt.Errorf("duplicate persisted controlled-fixture comparison")
		}
		byCorrelation[observation.Correlation] = observation
	}
	if len(byCorrelation) != len(requested) {
		return ar2IdentityExpandReceipt{}, fmt.Errorf("missing persisted controlled-fixture comparison")
	}

	adapters := make([]AR2SupportedAdapter, 0, len(ar2ControlledFixtureCallables))
	coverage := make([]AR2CallableCoverage, 0, len(ar2ControlledFixtureCallables))
	for index, spec := range ar2ControlledFixtureCallables {
		observation, ok := byCorrelation[ordered[index]]
		if !ok {
			return ar2IdentityExpandReceipt{}, fmt.Errorf("missing %s controlled-fixture comparison", spec.adapter)
		}
		counts := AR2CallableCoverage{Adapter: spec.adapter, Callable: spec.callable}
		switch observation.Classification() {
		case projectidentity.ComparisonEqualV3:
			counts.Equal = 1
		case projectidentity.ComparisonMismatchV3:
			counts.Mismatch = 1
		case projectidentity.ComparisonRefusalV3:
			counts.Refusal = 1
		case projectidentity.ComparisonUnavailableV3:
			counts.Unavailable = 1
		default:
			return ar2IdentityExpandReceipt{}, fmt.Errorf("invalid %s controlled-fixture classification", spec.adapter)
		}
		metric, err := operability.EvaluateMetric(operability.MetricInput{
			Name:             "ar2-" + spec.adapter + "-comparison-coverage",
			NumeratorName:    "classified_comparisons",
			NumeratorValue:   1,
			DenominatorName:  "controlled_fixture_callable_returns",
			DenominatorValue: 1,
			Scope:            ar2EvidenceScope,
			Window:           AR2CompatibilityWindow,
			Freshness:        "fixture_observation",
			Source:           operability.DurableRecordSource,
		})
		if err != nil {
			return ar2IdentityExpandReceipt{}, fmt.Errorf("%s coverage: %w", spec.adapter, err)
		}
		counts.Metric = metric
		coverage = append(coverage, counts)
		adapters = append(adapters, AR2SupportedAdapter{Adapter: spec.adapter, Callable: spec.callable, PhysicalChannel: spec.physicalChannel})
	}
	fixtureEvidenceFingerprint, err := ar2FixtureEvidenceFingerprint(capture.attestation.candidate, ordered, byCorrelation)
	if err != nil {
		return ar2IdentityExpandReceipt{}, err
	}

	return ar2IdentityExpandReceipt{
		SchemaVersion:              AR2IdentityExpandSchemaVersion,
		ReceiptAuthority:           AR2IdentityExpandAuthority,
		EvidenceScope:              ar2EvidenceScope,
		RuntimeLabelTrust:          ar2RuntimeLabelTrust,
		CompatibilityWindow:        AR2CompatibilityWindow,
		Candidate:                  capture.attestation.candidate,
		FixtureEvidenceFingerprint: fixtureEvidenceFingerprint,
		MigrationIDs:               append([]string(nil), ar2IdentityExpandMigrationIDs...),
		DescriptorVersion:          3,
		SupportedAdapters:          adapters,
		AdapterCoverage:            coverage,
		V2Compatibility:            capture.attestation.v2Compatibility,
		CapabilityState:            capture.attestation.capabilityState,
		RollbackBoundary: AR2RollbackBoundary{
			V3AdapterAction: "disable_v3_adapter",
			ReadBoundary:    "read_v2",
			Preserve:        []string{"additive_schema", "v3_identifiers", "v3_resolution_audits", "v3_anchors", "comparison_evidence"},
		},
	}, nil
}

func validateAR2CandidateAttestation(attestation ar2CandidateAttestation) error {
	candidate := attestation.candidate
	if !validCommit(candidate.SourceCommit) || candidate.SourceCommit != candidate.CandidateCommit || !validFingerprint(candidate.CandidatePayloadFingerprint) {
		return fmt.Errorf("invalid exact source/candidate identity")
	}
	if attestation.healthSourceCommit != candidate.CandidateCommit || attestation.runtimePayloadFingerprint != candidate.CandidatePayloadFingerprint {
		return fmt.Errorf("candidate attestation does not match runtime health and payload")
	}
	if attestation.v2Compatibility != AR2V2ReadCompatible {
		return fmt.Errorf("V2 compatibility state %q is unsupported", attestation.v2Compatibility)
	}
	switch attestation.capabilityState {
	case AR2CapabilityConfigured, AR2CapabilityAvailable, AR2CapabilityDegraded, AR2CapabilityPaused, AR2CapabilityFailed:
		return nil
	default:
		return fmt.Errorf("capability state %q is unsupported", attestation.capabilityState)
	}
}

func ar2FixtureEvidenceFingerprint(candidate ar2CandidateIdentity, correlations [len(ar2ControlledFixtureCallables)]projectidentity.CorrelationV3, observations map[projectidentity.CorrelationV3]projectidentity.ComparisonObservationV3) (string, error) {
	hash := sha256.New()
	for _, value := range []string{candidate.SourceCommit, candidate.CandidateCommit, candidate.CandidatePayloadFingerprint} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, correlation := range correlations {
		observation, ok := observations[correlation]
		if !ok {
			return "", fmt.Errorf("missing persisted controlled-fixture comparison fingerprint evidence")
		}
		for _, value := range []string{string(correlation), string(observation.V3Outcome), string(observation.LegacyOutcome), string(observation.Classification()), observation.EvidenceFingerprint} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func correlationStrings(correlations [len(ar2ControlledFixtureCallables)]projectidentity.CorrelationV3) []string {
	values := make([]string, len(correlations))
	for index, correlation := range correlations {
		values[index] = string(correlation)
	}
	return values
}
