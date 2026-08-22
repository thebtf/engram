// Package recoveryreceipt produces bounded AR-1 evidence receipts.
package recoveryreceipt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/operability"
	"github.com/thebtf/engram/internal/recoveryinventory"
)

const (
	AR1BaselineSchemaVersion = "engram.recovery.ar1-baseline-receipt.v1"
	AR1BaselineAuthority     = "sole_ar1_baseline_receipt"

	testScenarioReceiptEnv       = "ENGRAM_AR1_TEST_SCENARIO_RECEIPT"
	testSourceRootEnv            = "ENGRAM_AR1_TEST_SOURCE_ROOT"
	testPrimaryRepositoryRootEnv = "ENGRAM_AR1_TEST_PRIMARY_REPOSITORY_ROOT"
	testOutputFileEnv            = "ENGRAM_AR1_TEST_OUTPUT_FILE"
)

// ScenarioEvidence is the bounded fixture-only scenario input accepted by AR-1.
type ScenarioEvidence struct {
	SchemaVersion string               `json:"schema_version"`
	EvidenceKind  string               `json:"evidence_kind"`
	Release       string               `json:"release"`
	Scenario      string               `json:"scenario"`
	ObservedAtUTC string               `json:"observed_at_utc"`
	Scope         string               `json:"scope"`
	Fixture       ScenarioFixture      `json:"fixture"`
	Health        HealthProvenance     `json:"health"`
	Candidate     ScenarioCandidate    `json:"candidate"`
	Behavior      ScenarioBehavior     `json:"behavior"`
	Observations  ScenarioObservations `json:"observations"`
}

// ScenarioFixture carries only fixture provenance. It is never copied to a baseline receipt.
type ScenarioFixture struct {
	FixtureID              string `json:"fixture_id"`
	FixtureRoot            string `json:"fixture_root"`
	ManifestFingerprint    string `json:"manifest_fingerprint"`
	ExportReference        string `json:"export_reference"`
	ExportFingerprint      string `json:"export_fingerprint"`
	RestoreReference       string `json:"restore_reference"`
	SelectorInventoryCount int    `json:"selector_inventory_count"`
}

// HealthProvenance binds a scenario to its checked staged server payload.
type HealthProvenance struct {
	ReceiptFingerprint string `json:"receipt_fingerprint"`
	ServerFingerprint  string `json:"server_fingerprint"`
	Status             string `json:"status"`
}

// ScenarioCandidate identifies the source and staged payload checked by the scenario.
type ScenarioCandidate struct {
	SourceCommit            string `json:"source_commit"`
	BuiltPayloadFingerprint string `json:"built_payload_fingerprint"`
}

// ScenarioBehavior is the behavior evidence AR-1 must validate, not merely record.
type ScenarioBehavior struct {
	RetiredOutcomeCallbacks   []RetiredOutcomeCallback  `json:"retired_outcome_callbacks"`
	SelectorOnlyContextInject SelectorOnlyContextInject `json:"selector_only_context_inject"`
	HealthAfterBehavior       PostBehaviorHealth        `json:"health_after_behavior"`
}

type RetiredOutcomeCallback struct {
	Path            string `json:"path"`
	StatusCode      int    `json:"status_code"`
	ContentType     string `json:"content_type"`
	ContractVersion string `json:"contract_version"`
	Code            string `json:"code"`
	Action          string `json:"action"`
}

type SelectorOnlyContextInject struct {
	Path                     string `json:"path"`
	StatusCode               int    `json:"status_code"`
	ErrorCode                string `json:"error_code"`
	UpgradeAction            string `json:"upgrade_action"`
	CanonicalProjectReturned bool   `json:"canonical_project_returned"`
}

type PostBehaviorHealth struct {
	StatusCode int    `json:"status_code"`
	Status     string `json:"status"`
}

type ScenarioObservations struct {
	FixtureContainment          string `json:"fixture_containment"`
	SyntheticRestore            string `json:"synthetic_restore"`
	FixtureServerHealth         string `json:"fixture_server_health"`
	LiveDataObserved            bool   `json:"live_data_observed"`
	InstalledReleaseAuthority   string `json:"installed_release_authority"`
	AR1BaselineReceiptAuthority string `json:"ar1_baseline_receipt_authority"`
}

// AR1BaselineInput is the complete, typed evidence required to produce a receipt.
type AR1BaselineInput struct {
	SourceReports       []recoveryinventory.Report
	Metrics             operability.BaselineReport
	Scenario            ScenarioEvidence
	ScenarioFingerprint string
}

// SourceInventorySummary binds source-only inventory shape without re-emitting source records.
type SourceInventorySummary struct {
	Inventory   string `json:"inventory"`
	RecordCount int    `json:"record_count"`
	SourceOnly  bool   `json:"source_only"`
}

// AR1BaselineReceipt is the sole AR-1 baseline receipt authority.
type AR1BaselineReceipt struct {
	SchemaVersion            string                   `json:"schema_version"`
	ReceiptAuthority         string                   `json:"receipt_authority"`
	Scope                    string                   `json:"scope"`
	SourceCommit             string                   `json:"source_commit"`
	StagedPayloadFingerprint string                   `json:"staged_payload_fingerprint"`
	ScenarioFingerprint      string                   `json:"scenario_fingerprint"`
	Health                   HealthProvenance         `json:"health"`
	SourceInventories        []SourceInventorySummary `json:"source_inventories"`
	DurableMetrics           []operability.Metric     `json:"durable_metrics"`
	ProcessMetrics           []operability.Metric     `json:"process_metrics"`
}

// DecodeScenarioEvidence parses the exact scenario envelope and returns its content fingerprint.
func DecodeScenarioEvidence(data []byte) (ScenarioEvidence, string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return ScenarioEvidence{}, "", fmt.Errorf("decode scenario envelope: %w", err)
	}
	if err := requireExactObject(raw, "schema_version", "evidence_kind", "release", "scenario", "observed_at_utc", "scope", "fixture", "health", "candidate", "behavior", "observations"); err != nil {
		return ScenarioEvidence{}, "", fmt.Errorf("scenario envelope: %w", err)
	}
	for _, nested := range []struct {
		name   string
		fields []string
	}{
		{"fixture", []string{"fixture_id", "fixture_root", "manifest_fingerprint", "export_reference", "export_fingerprint", "restore_reference", "selector_inventory_count"}},
		{"health", []string{"receipt_fingerprint", "server_fingerprint", "status"}},
		{"candidate", []string{"source_commit", "built_payload_fingerprint"}},
		{"behavior", []string{"retired_outcome_callbacks", "selector_only_context_inject", "health_after_behavior"}},
		{"observations", []string{"fixture_containment", "synthetic_restore", "fixture_server_health", "live_data_observed", "installed_release_authority", "ar1_baseline_receipt_authority"}},
	} {
		if err := requireRawObject(raw[nested.name], nested.fields...); err != nil {
			return ScenarioEvidence{}, "", fmt.Errorf("scenario %s: %w", nested.name, err)
		}
	}
	if err := requireRawObjectField(raw["behavior"], "selector_only_context_inject", "path", "status_code", "error_code", "upgrade_action", "canonical_project_returned"); err != nil {
		return ScenarioEvidence{}, "", fmt.Errorf("scenario selector behavior: %w", err)
	}
	if err := requireRawObjectField(raw["behavior"], "health_after_behavior", "status_code", "status"); err != nil {
		return ScenarioEvidence{}, "", fmt.Errorf("scenario post-behavior health: %w", err)
	}
	if err := requireRetiredCallbacks(raw["behavior"]); err != nil {
		return ScenarioEvidence{}, "", err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var scenario ScenarioEvidence
	if err := decoder.Decode(&scenario); err != nil {
		return ScenarioEvidence{}, "", fmt.Errorf("decode exact scenario envelope: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ScenarioEvidence{}, "", fmt.Errorf("scenario envelope has trailing content")
	}
	if err := validateScenario(scenario); err != nil {
		return ScenarioEvidence{}, "", err
	}
	return scenario, fingerprint(data), nil
}

// BuildAR1BaselineReceipt validates typed evidence and builds a deterministic receipt.
func BuildAR1BaselineReceipt(input AR1BaselineInput) (AR1BaselineReceipt, error) {
	if err := validateScenario(input.Scenario); err != nil {
		return AR1BaselineReceipt{}, err
	}
	if !validFingerprint(input.ScenarioFingerprint) {
		return AR1BaselineReceipt{}, fmt.Errorf("invalid scenario fingerprint")
	}
	summaries, err := summarizeSourceReports(input.SourceReports)
	if err != nil {
		return AR1BaselineReceipt{}, err
	}
	if err := validateMetrics(input.Metrics.DurableFacts, operability.DurableRecordSource); err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("durable metrics: %w", err)
	}
	if err := validateMetrics(input.Metrics.ProcessCounters, operability.ProcessCounterSource); err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("process metrics: %w", err)
	}
	if len(input.Metrics.DurableFacts)+len(input.Metrics.ProcessCounters) == 0 {
		return AR1BaselineReceipt{}, fmt.Errorf("baseline metrics are missing")
	}

	return AR1BaselineReceipt{
		SchemaVersion:            AR1BaselineSchemaVersion,
		ReceiptAuthority:         AR1BaselineAuthority,
		Scope:                    input.Scenario.Scope,
		SourceCommit:             input.Scenario.Candidate.SourceCommit,
		StagedPayloadFingerprint: input.Scenario.Candidate.BuiltPayloadFingerprint,
		ScenarioFingerprint:      input.ScenarioFingerprint,
		Health:                   input.Scenario.Health,
		SourceInventories:        summaries,
		DurableMetrics:           append([]operability.Metric(nil), input.Metrics.DurableFacts...),
		ProcessMetrics:           append([]operability.Metric(nil), input.Metrics.ProcessCounters...),
	}, nil
}

// writeAR1BaselineReceiptFromTestEnvironment is deliberately test-harness-only.
// It accepts an explicit candidate worktree for source scanning and a distinct
// primary repository root for scenario and receipt ownership.
func writeAR1BaselineReceiptFromTestEnvironment(metrics operability.BaselineReport) (AR1BaselineReceipt, error) {
	scenarioPath := os.Getenv(testScenarioReceiptEnv)
	sourceRoot := os.Getenv(testSourceRootEnv)
	primaryRoot := os.Getenv(testPrimaryRepositoryRootEnv)
	outputPath := os.Getenv(testOutputFileEnv)
	if scenarioPath == "" || sourceRoot == "" || primaryRoot == "" || outputPath == "" {
		return AR1BaselineReceipt{}, fmt.Errorf("%s, %s, %s, and %s are required", testScenarioReceiptEnv, testSourceRootEnv, testPrimaryRepositoryRootEnv, testOutputFileEnv)
	}
	primary, err := absoluteDirectory(primaryRoot)
	if err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("primary repository root: %w", err)
	}
	candidate, err := absoluteDirectory(sourceRoot)
	if err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("candidate source root: %w", err)
	}
	if err := containedPath(primary, candidate); err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("candidate source root: %w", err)
	}
	if err := containedRegularFile(primary, scenarioPath, false); err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("test scenario receipt: %w", err)
	}
	if err := containedRegularFile(primary, outputPath, true); err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("test output file: %w", err)
	}

	data, err := os.ReadFile(scenarioPath)
	if err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("read test scenario receipt: %w", err)
	}
	scenario, scenarioFingerprint, err := DecodeScenarioEvidence(data)
	if err != nil {
		return AR1BaselineReceipt{}, err
	}
	commit, err := candidateCommit(candidate)
	if err != nil {
		return AR1BaselineReceipt{}, err
	}
	if commit != scenario.Candidate.SourceCommit {
		return AR1BaselineReceipt{}, fmt.Errorf("candidate source commit %q does not match scenario commit %q", commit, scenario.Candidate.SourceCommit)
	}
	reports, err := recoveryinventory.ScanAll(candidate)
	if err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("scan candidate source root: %w", err)
	}
	receipt, err := BuildAR1BaselineReceipt(AR1BaselineInput{
		SourceReports:       reports,
		Metrics:             metrics,
		Scenario:            scenario,
		ScenarioFingerprint: scenarioFingerprint,
	})
	if err != nil {
		return AR1BaselineReceipt{}, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("marshal baseline receipt: %w", err)
	}
	if err := os.WriteFile(outputPath, encoded, 0o600); err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("write baseline receipt: %w", err)
	}
	return receipt, nil
}

func summarizeSourceReports(reports []recoveryinventory.Report) ([]SourceInventorySummary, error) {
	expected := map[string]struct{}{
		"current-source": {},
		"feature-flags":  {},
		"package-route-hook-tool-documentation-claims": {},
		"project-bearing-data":                         {},
	}
	if len(reports) != len(expected) {
		return nil, fmt.Errorf("expected %d source inventories, got %d", len(expected), len(reports))
	}
	summaries := make([]SourceInventorySummary, 0, len(reports))
	for _, report := range reports {
		if report.SchemaVersion != "recovery-inventory/v1" || !report.SourceOnly {
			return nil, fmt.Errorf("inventory %q is not a source-only recovery inventory", report.Inventory)
		}
		if _, ok := expected[report.Inventory]; !ok {
			return nil, fmt.Errorf("unexpected or duplicate source inventory %q", report.Inventory)
		}
		delete(expected, report.Inventory)
		summaries = append(summaries, SourceInventorySummary{
			Inventory:   report.Inventory,
			RecordCount: len(report.Records),
			SourceOnly:  report.SourceOnly,
		})
	}
	if len(expected) != 0 {
		return nil, fmt.Errorf("source inventories are incomplete")
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Inventory < summaries[j].Inventory })
	return summaries, nil
}

func validateMetrics(metrics []operability.Metric, source operability.MetricSource) error {
	for _, metric := range metrics {
		if metric.Source != source {
			return fmt.Errorf("metric %q has source %q, want %q", metric.Name, metric.Source, source)
		}
		for _, value := range []string{metric.Name, metric.NumeratorName, metric.DenominatorName, metric.Scope, metric.Window, metric.Freshness} {
			if !safeReceiptText(value) {
				return fmt.Errorf("metric %q contains receipt-unsafe text", metric.Name)
			}
		}
		expected, err := operability.EvaluateMetric(operability.MetricInput{
			Name:             metric.Name,
			NumeratorName:    metric.NumeratorName,
			NumeratorValue:   metric.NumeratorValue,
			DenominatorName:  metric.DenominatorName,
			DenominatorValue: metric.DenominatorValue,
			Scope:            metric.Scope,
			Window:           metric.Window,
			Freshness:        metric.Freshness,
			Source:           metric.Source,
		})
		if err != nil {
			return err
		}
		if metric.ResultStatus != expected.ResultStatus || !sameRatio(metric.Ratio, expected.Ratio) {
			return fmt.Errorf("metric %q does not preserve %q denominator semantics", metric.Name, expected.ResultStatus)
		}
	}
	return nil
}

func sameRatio(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return !math.IsNaN(*a) && !math.IsInf(*a, 0) && *a == *b
}

func safeReceiptText(value string) bool {
	lower := strings.ToLower(value)
	if filepath.IsAbs(value) || strings.Contains(lower, "://") {
		return false
	}
	for _, forbidden := range []string{"credential", "password", "secret", "token", "api_key", "apikey", "private_key", "profile"} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	return true
}

func validateScenario(s ScenarioEvidence) error {
	if s.SchemaVersion != "engram.recovery.scenario-evidence.v1" || s.EvidenceKind != "fixture_scenario" || s.Release != "AR-1" || s.Scenario != "baseline" || s.Scope != "synthetic_fixture_only" {
		return fmt.Errorf("scenario envelope is not the AR-1 synthetic baseline")
	}
	if _, err := time.Parse(time.RFC3339, s.ObservedAtUTC); err != nil {
		return fmt.Errorf("scenario observed_at_utc: %w", err)
	}
	if s.Fixture.FixtureID != "synthetic-redacted-legacy" || !safeRelativeReference(s.Fixture.FixtureRoot) || !safeRelativeReference(s.Fixture.ExportReference) || !safeRelativeReference(s.Fixture.RestoreReference) || s.Fixture.SelectorInventoryCount < 0 || !validFingerprint(s.Fixture.ManifestFingerprint) || !validFingerprint(s.Fixture.ExportFingerprint) {
		return fmt.Errorf("scenario fixture provenance is invalid")
	}
	if s.Health.Status != "ready" || !validFingerprint(s.Health.ReceiptFingerprint) || !validFingerprint(s.Health.ServerFingerprint) || !validCommit(s.Candidate.SourceCommit) || s.Candidate.BuiltPayloadFingerprint != s.Health.ServerFingerprint {
		return fmt.Errorf("scenario health or candidate provenance is invalid")
	}
	callbacks := []string{"/api/sessions/claude-session/propagate-outcome", "/api/sessions/openclaw-session/outcome"}
	if len(s.Behavior.RetiredOutcomeCallbacks) != len(callbacks) {
		return fmt.Errorf("scenario lacks both retired outcome callback contracts")
	}
	for i, callback := range s.Behavior.RetiredOutcomeCallbacks {
		if callback.Path != callbacks[i] || callback.StatusCode != 410 || callback.ContentType != "application/json" || callback.ContractVersion != "engram.outcome-retirement.v1" || callback.Code != "OUTCOME_CALLBACK_RETIRED" || callback.Action != "upgrade_outcome_adapter" {
			return fmt.Errorf("scenario retired callback %d is invalid", i)
		}
	}
	selector := s.Behavior.SelectorOnlyContextInject
	if selector.Path != "/api/context/inject" || selector.StatusCode != 409 || selector.ErrorCode != "PROJECT_IDENTITY_AMBIGUOUS" || selector.UpgradeAction != "send_project_identity_v2" || selector.CanonicalProjectReturned {
		return fmt.Errorf("scenario selector-only behavior is invalid")
	}
	if s.Behavior.HealthAfterBehavior.StatusCode != 200 || s.Behavior.HealthAfterBehavior.Status != "ready" {
		return fmt.Errorf("scenario does not prove post-behavior health")
	}
	o := s.Observations
	if o.FixtureContainment != "validated" || o.SyntheticRestore != "validated" || o.FixtureServerHealth != "ready" || o.LiveDataObserved || o.InstalledReleaseAuthority != "not_claimed" || o.AR1BaselineReceiptAuthority != "not_claimed" {
		return fmt.Errorf("scenario fixture-only authority boundary is invalid")
	}
	return nil
}

func requireExactObject(object map[string]json.RawMessage, fields ...string) error {
	if len(object) != len(fields) {
		return fmt.Errorf("expected exactly %d properties, got %d", len(fields), len(object))
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return fmt.Errorf("missing property %q", field)
		}
	}
	return nil
}

func requireRawObject(raw json.RawMessage, fields ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	return requireExactObject(object, fields...)
}

func requireRawObjectField(raw json.RawMessage, field string, fields ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	return requireRawObject(object[field], fields...)
}

func requireRetiredCallbacks(raw json.RawMessage) error {
	var behavior map[string]json.RawMessage
	if err := json.Unmarshal(raw, &behavior); err != nil {
		return err
	}
	var callbacks []json.RawMessage
	if err := json.Unmarshal(behavior["retired_outcome_callbacks"], &callbacks); err != nil {
		return err
	}
	if len(callbacks) != 2 {
		return fmt.Errorf("expected two retired outcome callbacks")
	}
	for _, callback := range callbacks {
		if err := requireRawObject(callback, "path", "status_code", "content_type", "contract_version", "code", "action"); err != nil {
			return fmt.Errorf("retired outcome callback: %w", err)
		}
	}
	return nil
}

func validFingerprint(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil && value == strings.ToLower(value)
}

func validCommit(value string) bool {
	if len(value) != 40 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func safeRelativeReference(value string) bool {
	if strings.TrimSpace(value) == "" || filepath.IsAbs(value) || strings.Contains(value, "://") {
		return false
	}
	for _, component := range strings.FieldsFunc(filepath.ToSlash(value), func(r rune) bool { return r == '/' }) {
		if component == ".." {
			return false
		}
	}
	return true
}

func fingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func absoluteDirectory(path string) (string, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve directory links: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}
	return root, nil
}

func containedRegularFile(root, path string, mayNotExist bool) error {
	candidate, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		if !mayNotExist || !os.IsNotExist(err) {
			return err
		}
		parent, err := absoluteDirectory(filepath.Dir(candidate))
		if err != nil {
			return err
		}
		candidate = filepath.Join(parent, filepath.Base(candidate))
	} else {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("path is not a regular file")
		}
		candidate, err = filepath.EvalSymlinks(candidate)
		if err != nil {
			return err
		}
	}
	return containedPath(root, candidate)
}

func containedPath(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path is outside root")
	}
	return nil
}

func candidateCommit(root string) (string, error) {
	output, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("resolve candidate source commit: %w", err)
	}
	commit := strings.TrimSpace(string(output))
	if !validCommit(commit) {
		return "", fmt.Errorf("candidate source commit is invalid")
	}
	return commit, nil
}
