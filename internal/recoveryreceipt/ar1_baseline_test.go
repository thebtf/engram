package recoveryreceipt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/operability"
	"github.com/thebtf/engram/internal/recoveryinventory"
)

func TestBuildAR1BaselineReceiptIsDeterministicAndBounded(t *testing.T) {
	input := baselineInput(t)

	first, err := BuildAR1BaselineReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildAR1BaselineReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("receipt changed for identical input\nfirst: %s\nsecond: %s", firstJSON, secondJSON)
	}
	if first.SchemaVersion != AR1BaselineSchemaVersion || first.ReceiptAuthority != AR1BaselineAuthority {
		t.Fatalf("receipt authority = %#v", first)
	}
	if first.SourceCommit != strings.Repeat("a", 40) || first.StagedPayloadFingerprint != fingerprintOf("b") || first.ScenarioFingerprint != input.ScenarioFingerprint {
		t.Fatalf("candidate binding = %#v", first)
	}
	if len(first.SourceInventories) != 4 || first.SourceInventories[0].Inventory != "current-source" {
		t.Fatalf("source summaries = %#v", first.SourceInventories)
	}
	if len(first.DurableMetrics) != 1 || first.DurableMetrics[0].ResultStatus != operability.NotComputable || first.DurableMetrics[0].Ratio != nil {
		t.Fatalf("zero denominator was not preserved: %#v", first.DurableMetrics)
	}
	for _, forbidden := range []string{"fixtures/ar1", "secret-token", "https://private.example", "C:\\Users"} {
		if strings.Contains(string(firstJSON), forbidden) {
			t.Fatalf("receipt leaked %q: %s", forbidden, firstJSON)
		}
	}
}

func TestBuildAR1BaselineReceiptRejectsInvalidEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AR1BaselineInput)
	}{
		{
			name: "missing source inventory",
			mutate: func(input *AR1BaselineInput) {
				input.SourceReports = input.SourceReports[:3]
			},
		},
		{
			name: "mismatched payload fingerprint",
			mutate: func(input *AR1BaselineInput) {
				input.Scenario.Candidate.BuiltPayloadFingerprint = fingerprintOf("f")
			},
		},
		{
			name: "retired callback false success",
			mutate: func(input *AR1BaselineInput) {
				input.Scenario.Behavior.RetiredOutcomeCallbacks[0].StatusCode = 200
			},
		},
		{
			name: "selector returns canonical project",
			mutate: func(input *AR1BaselineInput) {
				input.Scenario.Behavior.SelectorOnlyContextInject.CanonicalProjectReturned = true
			},
		},
		{
			name: "zero denominator marked computed",
			mutate: func(input *AR1BaselineInput) {
				input.Metrics.DurableFacts[0].ResultStatus = operability.Computed
			},
		},
		{
			name: "fixture claims receipt authority",
			mutate: func(input *AR1BaselineInput) {
				input.Scenario.Observations.AR1BaselineReceiptAuthority = "claimed"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := baselineInput(t)
			test.mutate(&input)
			if _, err := BuildAR1BaselineReceipt(input); err == nil {
				t.Fatal("BuildAR1BaselineReceipt accepted invalid evidence")
			}
		})
	}
}

func TestDecodeScenarioEvidenceRejectsUnknownOrMalformedInput(t *testing.T) {
	input := baselineInput(t)
	data, err := json.Marshal(input.Scenario)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["raw_fixture_payload"] = "forbidden"
	withExtra, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeScenarioEvidence(withExtra); err == nil {
		t.Fatal("accepted scenario with an unbounded property")
	}
	if _, _, err := DecodeScenarioEvidence([]byte("not json")); err == nil {
		t.Fatal("accepted malformed scenario")
	}
}

func TestWriteAR1BaselineReceiptFromTestEnvironment(t *testing.T) {
	root := sourceRoot(t)
	scenario := validScenario()
	encodedScenario, err := json.Marshal(scenario)
	if err != nil {
		t.Fatal(err)
	}
	scenarioPath := filepath.Join(root, "owned", "scenario.json")
	outputPath := filepath.Join(root, "owned", "ar1-baseline.json")
	writeFile(t, scenarioPath, string(encodedScenario))
	t.Setenv(testScenarioReceiptEnv, scenarioPath)
	t.Setenv(testSourceRootEnv, root)
	t.Setenv(testOutputFileEnv, outputPath)

	receipt, err := writeAR1BaselineReceiptFromTestEnvironment(baselineMetrics(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded AR1BaselineReceipt
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(receipt, decoded) {
		t.Fatalf("written receipt differs\nwant: %#v\ngot: %#v", receipt, decoded)
	}
	if decoded.ReceiptAuthority != AR1BaselineAuthority || decoded.ScenarioFingerprint != fingerprint(encodedScenario) {
		t.Fatalf("written receipt is not bound to the explicit scenario: %#v", decoded)
	}
}

func baselineInput(t *testing.T) AR1BaselineInput {
	t.Helper()
	scenario := validScenario()
	encoded, err := json.Marshal(scenario)
	if err != nil {
		t.Fatal(err)
	}
	decoded, scenarioFingerprint, err := DecodeScenarioEvidence(encoded)
	if err != nil {
		t.Fatal(err)
	}
	reports, err := recoveryinventory.ScanAll(sourceRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	return AR1BaselineInput{
		SourceReports:       reports,
		Metrics:             baselineMetrics(t),
		Scenario:            decoded,
		ScenarioFingerprint: scenarioFingerprint,
	}
}

func baselineMetrics(t *testing.T) operability.BaselineReport {
	t.Helper()
	durable, err := operability.BuildBaselineReport([]operability.MetricInput{{
		Name:             "durable-coverage",
		NumeratorName:    "covered_records",
		NumeratorValue:   0,
		DenominatorName:  "eligible_records",
		DenominatorValue: 0,
		Scope:            "fixture-source",
		Window:           "baseline",
		Freshness:        "scenario-bound",
		Source:           operability.DurableRecordSource,
	}}, []operability.MetricInput{{
		Name:             "process-coverage",
		NumeratorName:    "checked_requests",
		NumeratorValue:   2,
		DenominatorName:  "attempted_requests",
		DenominatorValue: 4,
		Scope:            "fixture-server",
		Window:           "scenario",
		Freshness:        "post-behavior",
		Source:           operability.ProcessCounterSource,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return durable
}

func validScenario() ScenarioEvidence {
	return ScenarioEvidence{
		SchemaVersion: "engram.recovery.scenario-evidence.v1",
		EvidenceKind:  "fixture_scenario",
		Release:       "AR-1",
		Scenario:      "baseline",
		ObservedAtUTC: "2026-08-22T00:00:00Z",
		Scope:         "synthetic_fixture_only",
		Fixture: ScenarioFixture{
			FixtureID:              "synthetic-redacted-legacy",
			FixtureRoot:            "fixtures/ar1",
			ManifestFingerprint:    fingerprintOf("c"),
			ExportReference:        "fixtures/ar1/exports/legacy.json",
			ExportFingerprint:      fingerprintOf("d"),
			RestoreReference:       "fixtures/ar1/restored/legacy.json",
			SelectorInventoryCount: 0,
		},
		Health: HealthProvenance{
			ReceiptFingerprint: fingerprintOf("e"),
			ServerFingerprint:  fingerprintOf("b"),
			Status:             "ready",
		},
		Candidate: ScenarioCandidate{
			SourceCommit:            strings.Repeat("a", 40),
			BuiltPayloadFingerprint: fingerprintOf("b"),
		},
		Behavior: ScenarioBehavior{
			RetiredOutcomeCallbacks: []RetiredOutcomeCallback{
				{Path: "/api/sessions/claude-session/propagate-outcome", StatusCode: 410, ContentType: "application/json", ContractVersion: "engram.outcome-retirement.v1", Code: "OUTCOME_CALLBACK_RETIRED", Action: "upgrade_outcome_adapter"},
				{Path: "/api/sessions/openclaw-session/outcome", StatusCode: 410, ContentType: "application/json", ContractVersion: "engram.outcome-retirement.v1", Code: "OUTCOME_CALLBACK_RETIRED", Action: "upgrade_outcome_adapter"},
			},
			SelectorOnlyContextInject: SelectorOnlyContextInject{
				Path: "/api/context/inject", StatusCode: 409, ErrorCode: "PROJECT_IDENTITY_AMBIGUOUS", UpgradeAction: "send_project_identity_v2", CanonicalProjectReturned: false,
			},
			HealthAfterBehavior: PostBehaviorHealth{StatusCode: 200, Status: "ready"},
		},
		Observations: ScenarioObservations{
			FixtureContainment:          "validated",
			SyntheticRestore:            "validated",
			FixtureServerHealth:         "ready",
			LiveDataObserved:            false,
			InstalledReleaseAuthority:   "not_claimed",
			AR1BaselineReceiptAuthority: "not_claimed",
		},
	}
}

func sourceRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "internal", "fixture.go"), "package fixture\n\ntype ProjectRecord struct { ID string }\n\nconst raw = \"secret-token https://private.example\"\n")
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fingerprintOf(hexDigit string) string {
	return "sha256:" + strings.Repeat(hexDigit, 64)
}
