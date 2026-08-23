package recoveryreceipt

import (
	"context"
	"fmt"

	"github.com/thebtf/engram/internal/operability"
	"github.com/thebtf/engram/internal/projectidentity"
)

const (
	AR2IdentityExpandSchemaVersion = "engram.recovery.ar2-identity-expand-receipt.v1"
	AR2IdentityExpandAuthority     = "ar2_compatibility_descriptor_telemetry"
	AR2CompatibilityWindow         = "v3_descriptor_with_v2_read_compatibility"
)

var (
	ar2IdentityExpandMigrationIDs = []string{
		"162_project_identity_v3",
		"163_project_identity_v3_resolution_attempts",
		"164_project_identity_v3_resolution_attempt_admin_audit",
		"165_project_identity_v3_comparisons",
		"166_project_identity_v3_comparison_client_instance_privacy",
	}
	ar2SupportedTransports = []projectidentity.ComparisonTransportV3{
		projectidentity.ComparisonTransportGRPCV3,
		projectidentity.ComparisonTransportHTTPV3,
		projectidentity.ComparisonTransportHookV3,
		projectidentity.ComparisonTransportDaemonV3,
		projectidentity.ComparisonTransportOpenClawV3,
	}
)

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

// AR2CandidateIdentity binds the receipt to the exact source and staged candidate.
type AR2CandidateIdentity struct {
	SourceCommit                string `json:"source_commit"`
	CandidateCommit             string `json:"candidate_commit"`
	CandidatePayloadFingerprint string `json:"candidate_payload_fingerprint"`
}

// AR2SupportedAdapter names one transport and its matching adapter without request content.
type AR2SupportedAdapter struct {
	Transport projectidentity.ComparisonTransportV3 `json:"transport"`
	Adapter   string                                `json:"adapter"`
}

// AR2TransportCoverage renders redacted comparison classes for one supported transport.
type AR2TransportCoverage struct {
	Transport   projectidentity.ComparisonTransportV3 `json:"transport"`
	Equal       int64                                 `json:"equal"`
	Mismatch    int64                                 `json:"mismatch"`
	Refusal     int64                                 `json:"refusal"`
	Unavailable int64                                 `json:"unavailable"`
	Metric      operability.Metric                    `json:"comparison_coverage"`
}

// AR2RollbackBoundary is the fixed additive rollback disposition for this receipt.
type AR2RollbackBoundary struct {
	V3AdapterAction string   `json:"v3_adapter_action"`
	ReadBoundary    string   `json:"read_boundary"`
	Preserve        []string `json:"preserve"`
}

// AR2IdentityExpandInput carries only exact candidate identity and already-redacted comparisons.
type AR2IdentityExpandInput struct {
	Candidate           AR2CandidateIdentity
	MigrationIDs        []string
	DescriptorVersion   int
	SupportedTransports []projectidentity.ComparisonTransportV3
	Comparisons         []projectidentity.ComparisonObservationV3
	V2Compatibility     AR2V2CompatibilityState
	CapabilityState     AR2CapabilityState
}

// AR2IdentityExpandReceipt is a read-only, redacted AR-2 compatibility-window receipt.
type AR2IdentityExpandReceipt struct {
	SchemaVersion       string                  `json:"schema_version"`
	ReceiptAuthority    string                  `json:"receipt_authority"`
	CompatibilityWindow string                  `json:"compatibility_window"`
	Candidate           AR2CandidateIdentity    `json:"candidate"`
	MigrationIDs        []string                `json:"migration_ids"`
	DescriptorVersion   int                     `json:"descriptor_version"`
	SupportedAdapters   []AR2SupportedAdapter   `json:"supported_adapters"`
	TransportCoverage   []AR2TransportCoverage  `json:"transport_coverage"`
	V2Compatibility     AR2V2CompatibilityState `json:"v2_compatibility_state"`
	CapabilityState     AR2CapabilityState      `json:"capability_state"`
	RollbackBoundary    AR2RollbackBoundary     `json:"rollback_boundary"`
}

// BuildAR2IdentityExpandReceipt summarizes only redacted comparison observations.
// It deliberately permits a zero transport denominator so that the receipt remains
// honest through Metric.ResultStatus=not_computable rather than fabricating coverage.
func BuildAR2IdentityExpandReceipt(input AR2IdentityExpandInput) (AR2IdentityExpandReceipt, error) {
	return buildAR2IdentityExpandReceipt(input, false)
}

func buildAR2IdentityExpandReceipt(input AR2IdentityExpandInput, persistedLegacyReadback bool) (AR2IdentityExpandReceipt, error) {
	if err := validateAR2IdentityExpandInput(input, persistedLegacyReadback); err != nil {
		return AR2IdentityExpandReceipt{}, err
	}

	coverage := make([]AR2TransportCoverage, 0, len(ar2SupportedTransports))
	adapters := make([]AR2SupportedAdapter, 0, len(ar2SupportedTransports))
	for _, transport := range ar2SupportedTransports {
		counts := AR2TransportCoverage{Transport: transport}
		for _, comparison := range input.Comparisons {
			if comparison.Transport != transport {
				continue
			}
			switch comparison.Classification() {
			case projectidentity.ComparisonEqualV3:
				counts.Equal++
			case projectidentity.ComparisonMismatchV3:
				counts.Mismatch++
			case projectidentity.ComparisonRefusalV3:
				counts.Refusal++
			case projectidentity.ComparisonUnavailableV3:
				counts.Unavailable++
			}
		}
		denominator := counts.Equal + counts.Mismatch + counts.Refusal + counts.Unavailable
		metric, err := operability.EvaluateMetric(operability.MetricInput{
			Name:             "ar2-" + string(transport) + "-comparison-coverage",
			NumeratorName:    "classified_comparisons",
			NumeratorValue:   denominator,
			DenominatorName:  "observed_comparisons",
			DenominatorValue: denominator,
			Scope:            AR2CompatibilityWindow,
			Window:           AR2CompatibilityWindow,
			Freshness:        "fixture_observation",
			Source:           operability.DurableRecordSource,
		})
		if err != nil {
			return AR2IdentityExpandReceipt{}, fmt.Errorf("%s coverage: %w", transport, err)
		}
		counts.Metric = metric
		coverage = append(coverage, counts)
		adapters = append(adapters, AR2SupportedAdapter{Transport: transport, Adapter: string(transport)})
	}

	return AR2IdentityExpandReceipt{
		SchemaVersion:       AR2IdentityExpandSchemaVersion,
		ReceiptAuthority:    AR2IdentityExpandAuthority,
		CompatibilityWindow: AR2CompatibilityWindow,
		Candidate:           input.Candidate,
		MigrationIDs:        append([]string(nil), ar2IdentityExpandMigrationIDs...),
		DescriptorVersion:   input.DescriptorVersion,
		SupportedAdapters:   adapters,
		TransportCoverage:   coverage,
		V2Compatibility:     input.V2Compatibility,
		CapabilityState:     input.CapabilityState,
		RollbackBoundary: AR2RollbackBoundary{
			V3AdapterAction: "disable_v3_adapter",
			ReadBoundary:    "read_v2",
			Preserve:        []string{"additive_schema", "v3_identifiers", "v3_resolution_audits", "v3_anchors", "comparison_evidence"},
		},
	}, nil
}

// BuildAR2IdentityExpandReceiptFromPersistedComparisons accepts only an exact
// persisted correlation set. It refuses caller-injected comparisons, missing
// records, duplicate records, and records outside the requested set.
func BuildAR2IdentityExpandReceiptFromPersistedComparisons(ctx context.Context, reader projectidentity.ComparisonReaderV3, input AR2IdentityExpandInput, correlations []projectidentity.CorrelationV3) (AR2IdentityExpandReceipt, error) {
	if reader == nil || len(correlations) == 0 || len(input.Comparisons) != 0 {
		return AR2IdentityExpandReceipt{}, fmt.Errorf("invalid persisted comparison receipt input")
	}
	requested := make(map[projectidentity.CorrelationV3]struct{}, len(correlations))
	for _, correlation := range correlations {
		if _, err := projectidentity.NewCorrelationV3(string(correlation)); err != nil {
			return AR2IdentityExpandReceipt{}, fmt.Errorf("invalid requested comparison correlation")
		}
		if _, duplicate := requested[correlation]; duplicate {
			return AR2IdentityExpandReceipt{}, fmt.Errorf("duplicate requested comparison correlation")
		}
		requested[correlation] = struct{}{}
	}
	comparisons, err := reader.ReadComparisonsByCorrelationV3(ctx, correlations)
	if err != nil {
		return AR2IdentityExpandReceipt{}, fmt.Errorf("read persisted comparisons: %w", err)
	}
	found := make(map[projectidentity.CorrelationV3]struct{}, len(comparisons))
	for _, comparison := range comparisons {
		if !comparison.ValidPersistedLegacyReadback() {
			return AR2IdentityExpandReceipt{}, fmt.Errorf("invalid persisted comparison")
		}
		if _, requested := requested[comparison.Correlation]; !requested {
			return AR2IdentityExpandReceipt{}, fmt.Errorf("unrequested persisted comparison correlation")
		}
		if _, duplicate := found[comparison.Correlation]; duplicate {
			return AR2IdentityExpandReceipt{}, fmt.Errorf("duplicate persisted comparison correlation")
		}
		found[comparison.Correlation] = struct{}{}
	}
	if len(found) != len(requested) {
		return AR2IdentityExpandReceipt{}, fmt.Errorf("missing persisted comparison correlation")
	}
	input.Comparisons = comparisons
	return buildAR2IdentityExpandReceipt(input, true)
}

func validateAR2IdentityExpandInput(input AR2IdentityExpandInput, persistedLegacyReadback bool) error {
	if !validCommit(input.Candidate.SourceCommit) || input.Candidate.SourceCommit != input.Candidate.CandidateCommit || !validFingerprint(input.Candidate.CandidatePayloadFingerprint) {
		return fmt.Errorf("invalid exact source/candidate identity")
	}
	if input.DescriptorVersion != 3 {
		return fmt.Errorf("descriptor version %d is not V3", input.DescriptorVersion)
	}
	if !sameStrings(input.MigrationIDs, ar2IdentityExpandMigrationIDs) {
		return fmt.Errorf("AR-2 migration IDs are incomplete or out of order")
	}
	if !sameTransports(input.SupportedTransports, ar2SupportedTransports) {
		return fmt.Errorf("supported transports are incomplete or out of order")
	}
	if input.V2Compatibility != AR2V2ReadCompatible {
		return fmt.Errorf("V2 compatibility state %q is unsupported", input.V2Compatibility)
	}
	switch input.CapabilityState {
	case AR2CapabilityConfigured, AR2CapabilityAvailable, AR2CapabilityDegraded, AR2CapabilityPaused, AR2CapabilityFailed:
	default:
		return fmt.Errorf("capability state %q is unsupported", input.CapabilityState)
	}
	for _, comparison := range input.Comparisons {
		valid := comparison.Valid()
		if persistedLegacyReadback {
			valid = comparison.ValidPersistedLegacyReadback()
		}
		if !valid || !containsTransport(input.SupportedTransports, comparison.Transport) {
			return fmt.Errorf("comparison is not a supported redacted observation")
		}
	}
	return nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameTransports(left, right []projectidentity.ComparisonTransportV3) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func containsTransport(transports []projectidentity.ComparisonTransportV3, target projectidentity.ComparisonTransportV3) bool {
	for _, transport := range transports {
		if transport == target {
			return true
		}
	}
	return false
}
