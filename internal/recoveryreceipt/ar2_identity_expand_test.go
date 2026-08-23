package recoveryreceipt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/operability"
	"github.com/thebtf/engram/internal/projectidentity"
)

func TestBuildAR2IdentityExpandReceiptCoversEveryTransportAndRedactsRefusals(t *testing.T) {
	input := ar2IdentityExpandInput(t)
	before := input
	before.MigrationIDs = append([]string(nil), input.MigrationIDs...)
	before.SupportedTransports = append([]projectidentity.ComparisonTransportV3(nil), input.SupportedTransports...)
	before.Comparisons = append([]projectidentity.ComparisonObservationV3(nil), input.Comparisons...)

	receipt, err := BuildAR2IdentityExpandReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatalf("receipt construction mutated its authority-free input: before=%#v after=%#v", before, input)
	}
	if receipt.SchemaVersion != AR2IdentityExpandSchemaVersion || receipt.ReceiptAuthority != AR2IdentityExpandAuthority || receipt.CompatibilityWindow != AR2CompatibilityWindow || receipt.Candidate != input.Candidate || !reflect.DeepEqual(receipt.MigrationIDs, input.MigrationIDs) || receipt.DescriptorVersion != 3 || receipt.V2Compatibility != AR2V2ReadCompatible || receipt.CapabilityState != AR2CapabilityAvailable {
		t.Fatalf("receipt identity = %#v", receipt)
	}
	if len(receipt.SupportedAdapters) != len(input.SupportedTransports) || len(receipt.TransportCoverage) != len(input.SupportedTransports) {
		t.Fatalf("supported transports/adapters = %#v / %#v", receipt.SupportedAdapters, receipt.TransportCoverage)
	}

	classes := map[projectidentity.ComparisonClassV3]int64{}
	for _, coverage := range receipt.TransportCoverage {
		if coverage.Metric.DenominatorValue == 0 || coverage.Metric.ResultStatus != operability.Computed || coverage.Metric.Ratio == nil || *coverage.Metric.Ratio != 1 {
			t.Fatalf("%s coverage = %#v, want nonzero computed coverage", coverage.Transport, coverage)
		}
		classes[projectidentity.ComparisonEqualV3] += coverage.Equal
		classes[projectidentity.ComparisonMismatchV3] += coverage.Mismatch
		classes[projectidentity.ComparisonRefusalV3] += coverage.Refusal
		classes[projectidentity.ComparisonUnavailableV3] += coverage.Unavailable
	}
	for _, class := range []projectidentity.ComparisonClassV3{
		projectidentity.ComparisonEqualV3,
		projectidentity.ComparisonMismatchV3,
		projectidentity.ComparisonRefusalV3,
		projectidentity.ComparisonUnavailableV3,
	} {
		if classes[class] == 0 {
			t.Fatalf("receipt omitted explicit %s class: %#v", class, receipt.TransportCoverage)
		}
	}
	if receipt.RollbackBoundary.V3AdapterAction != "disable_v3_adapter" || receipt.RollbackBoundary.ReadBoundary != "read_v2" || !reflect.DeepEqual(receipt.RollbackBoundary.Preserve, []string{"additive_schema", "v3_identifiers", "v3_resolution_audits", "v3_anchors", "comparison_evidence"}) {
		t.Fatalf("rollback boundary = %#v", receipt.RollbackBoundary)
	}

	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		`{"version":3,"anchor_project_id":"11111111-1111-4111-8111-111111111111"}`,
		"https://fixture-user:fixture-password@git.example.test/private/repo",
		"C:/private/repo",
		"fixture-password",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("redacted refusal receipt leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestBuildAR2IdentityExpandReceiptReportsZeroDenominatorAsNotComputable(t *testing.T) {
	input := ar2IdentityExpandInput(t)
	input.Comparisons = input.Comparisons[1:]

	receipt, err := BuildAR2IdentityExpandReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	var grpc AR2TransportCoverage
	for _, coverage := range receipt.TransportCoverage {
		if coverage.Transport == projectidentity.ComparisonTransportGRPCV3 {
			grpc = coverage
			break
		}
	}
	if grpc.Metric.DenominatorValue != 0 || grpc.Metric.NumeratorValue != 0 || grpc.Metric.ResultStatus != operability.NotComputable || grpc.Metric.Ratio != nil {
		t.Fatalf("zero denominator must be not_computable, never green: %#v", grpc)
	}
}

func TestBuildAR2IdentityExpandReceiptRejectsIncompleteSupportContract(t *testing.T) {
	input := ar2IdentityExpandInput(t)
	input.SupportedTransports = input.SupportedTransports[:len(input.SupportedTransports)-1]
	if _, err := BuildAR2IdentityExpandReceipt(input); err == nil {
		t.Fatal("receipt accepted an incomplete supported transport contract")
	}
}

func ar2IdentityExpandInput(t *testing.T) AR2IdentityExpandInput {
	t.Helper()
	comparisons := make([]projectidentity.ComparisonObservationV3, 0, len(ar2SupportedTransports))
	cases := []struct {
		outcome projectidentity.ResolutionOutcomeV3
		legacy  projectidentity.LegacyComparisonOutcomeV2
	}{
		{projectidentity.ProjectResolvedOutcomeV3, projectidentity.LegacyComparisonResolvedV2},
		{projectidentity.ProjectResolvedOutcomeV3, projectidentity.LegacyComparisonRefusalV2},
		{projectidentity.ProjectDescriptorInvalidOutcomeV3, projectidentity.LegacyComparisonResolvedV2},
		{projectidentity.ProjectResolvedOutcomeV3, projectidentity.LegacyComparisonUnavailableV2},
		{projectidentity.ProjectResolvedOutcomeV3, projectidentity.LegacyComparisonResolvedV2},
	}
	for index, transport := range ar2SupportedTransports {
		correlation, err := projectidentity.NewCorrelationV3("ar2-receipt-correlation-" + string(transport))
		if err != nil {
			t.Fatal(err)
		}
		comparison, err := projectidentity.NewComparisonObservationV3(
			ar2ReceiptFingerprint("idempotency-"+string(transport)),
			correlation,
			cases[index].outcome,
			cases[index].legacy,
			"ar2-receipt-client-"+string(transport),
			transport,
			projectidentity.ComparisonRepositoryScopeV3,
			projectidentity.ComparisonFreshV3,
			ar2ReceiptFingerprint("evidence-"+string(transport)),
		)
		if err != nil {
			t.Fatal(err)
		}
		comparisons = append(comparisons, comparison)
	}
	return AR2IdentityExpandInput{
		Candidate: AR2CandidateIdentity{
			SourceCommit:                strings.Repeat("a", 40),
			CandidateCommit:             strings.Repeat("a", 40),
			CandidatePayloadFingerprint: ar2ReceiptFingerprint("candidate-payload"),
		},
		MigrationIDs:        append([]string(nil), ar2IdentityExpandMigrationIDs...),
		DescriptorVersion:   3,
		SupportedTransports: append([]projectidentity.ComparisonTransportV3(nil), ar2SupportedTransports...),
		Comparisons:         comparisons,
		V2Compatibility:     AR2V2ReadCompatible,
		CapabilityState:     AR2CapabilityAvailable,
	}
}

func ar2ReceiptFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestBuildAR2IdentityExpandReceiptFromPersistedComparisonsRequiresExactCorrelations(t *testing.T) {
	input := ar2IdentityExpandInput(t)
	persisted := append([]projectidentity.ComparisonObservationV3(nil), input.Comparisons...)
	input.Comparisons = nil
	correlations := make([]projectidentity.CorrelationV3, 0, len(persisted))
	for _, comparison := range persisted {
		correlations = append(correlations, comparison.Correlation)
	}

	receipt, err := BuildAR2IdentityExpandReceiptFromPersistedComparisons(context.Background(), persistedComparisonReaderV3{comparisons: persisted}, input, correlations)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.TransportCoverage) != len(ar2SupportedTransports) {
		t.Fatalf("coverage=%#v", receipt.TransportCoverage)
	}

	for _, testCase := range []struct {
		name         string
		reader       persistedComparisonReaderV3
		correlations []projectidentity.CorrelationV3
	}{
		{name: "missing", reader: persistedComparisonReaderV3{comparisons: persisted[:len(persisted)-1]}, correlations: correlations},
		{name: "duplicate row", reader: persistedComparisonReaderV3{comparisons: append(append([]projectidentity.ComparisonObservationV3(nil), persisted...), persisted[0])}, correlations: correlations},
		{name: "unrequested", reader: persistedComparisonReaderV3{comparisons: persisted}, correlations: correlations[:len(correlations)-1]},
		{name: "duplicate request", reader: persistedComparisonReaderV3{comparisons: persisted}, correlations: append(append([]projectidentity.CorrelationV3(nil), correlations...), correlations[0])},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := BuildAR2IdentityExpandReceiptFromPersistedComparisons(context.Background(), testCase.reader, input, testCase.correlations); err == nil {
				t.Fatal("persisted comparison readback accepted a non-exact correlation set")
			}
		})
	}
}

func TestBuildAR2IdentityExpandReceiptFromPersistedComparisonsAcceptsLegacyLocatorReadbackOnly(t *testing.T) {
	input := ar2IdentityExpandInput(t)
	persisted := append([]projectidentity.ComparisonObservationV3(nil), input.Comparisons...)
	persisted[0].ClientInstanceID = "C:private"
	correlations := make([]projectidentity.CorrelationV3, 0, len(persisted))
	for _, comparison := range persisted {
		correlations = append(correlations, comparison.Correlation)
	}

	direct := input
	direct.Comparisons = persisted
	if _, err := BuildAR2IdentityExpandReceipt(direct); err == nil {
		t.Fatal("direct receipt input accepted a legacy locator")
	}

	input.Comparisons = nil
	receipt, err := BuildAR2IdentityExpandReceiptFromPersistedComparisons(context.Background(), persistedComparisonReaderV3{comparisons: persisted}, input, correlations)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), persisted[0].ClientInstanceID) {
		t.Fatalf("receipt exposed historical locator: %s", encoded)
	}

	persisted[0].EvidenceFingerprint = "invalid"
	if _, err := BuildAR2IdentityExpandReceiptFromPersistedComparisons(context.Background(), persistedComparisonReaderV3{comparisons: persisted}, input, correlations); err == nil {
		t.Fatal("persisted legacy readback accepted an invalid non-client field")
	}
}

type persistedComparisonReaderV3 struct {
	comparisons []projectidentity.ComparisonObservationV3
	err         error
}

func (reader persistedComparisonReaderV3) ReadComparisonsByCorrelationV3(_ context.Context, _ []projectidentity.CorrelationV3) ([]projectidentity.ComparisonObservationV3, error) {
	return append([]projectidentity.ComparisonObservationV3(nil), reader.comparisons...), reader.err
}
