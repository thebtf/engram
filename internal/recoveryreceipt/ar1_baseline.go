// Package recoveryreceipt produces bounded AR-1 evidence receipts.
package recoveryreceipt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

// ScenarioEvidence is the exact fixture-only scenario input accepted by AR-1.
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
	FixtureID                   string                        `json:"fixture_id"`
	FixtureRoot                 string                        `json:"fixture_root"`
	RunID                       string                        `json:"run_id"`
	ManifestFingerprint         string                        `json:"manifest_fingerprint"`
	DatabaseIdentityFingerprint string                        `json:"database_identity_fingerprint"`
	ExportReference             string                        `json:"export_reference"`
	ExportFingerprint           string                        `json:"export_fingerprint"`
	RestoreReference            string                        `json:"restore_reference"`
	SelectorInventoryCount      int                           `json:"selector_inventory_count"`
	StructuralFingerprints      FixtureStructuralFingerprints `json:"structural_fingerprints"`
	ServerMarkerFingerprint     string                        `json:"server_marker_fingerprint"`
}

// FixtureStructuralFingerprints bind both synthetic fixture record families.
type FixtureStructuralFingerprints struct {
	Projects       string `json:"projects"`
	LegacyPayloads string `json:"legacy_payloads"`
}

// HealthProvenance binds a scenario to its checked staged server payload.
type HealthProvenance struct {
	ReceiptFingerprint   string `json:"receipt_fingerprint"`
	ServerFingerprint    string `json:"server_fingerprint"`
	SourceCommit         string `json:"source_commit"`
	Status               string `json:"status"`
	RunID                string `json:"run_id"`
	ProcessID            int64  `json:"process_id"`
	ProcessStartUTCTicks int64  `json:"process_start_utc_ticks"`
	Port                 int64  `json:"port"`
}

// ScenarioCandidate identifies the source and staged payload checked by the scenario.
type ScenarioCandidate struct {
	SourceCommit             string `json:"source_commit"`
	BuiltPayloadFingerprint  string `json:"built_payload_fingerprint"`
	StagedPayloadFingerprint string `json:"staged_payload_fingerprint"`
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
	StatusCode   int    `json:"status_code"`
	Status       string `json:"status"`
	SourceCommit string `json:"source_commit"`
}

type ScenarioObservations struct {
	FixtureContainment          string `json:"fixture_containment"`
	SyntheticRestore            string `json:"synthetic_restore"`
	FixtureDatabaseBinding      string `json:"fixture_database_binding"`
	OwnedLiveProcess            string `json:"owned_live_process"`
	StagedPayloadProvenance     string `json:"staged_payload_provenance"`
	RuntimeHealthProvenance     string `json:"runtime_health_provenance"`
	FixtureServerHealth         string `json:"fixture_server_health"`
	LiveDataObserved            bool   `json:"live_data_observed"`
	InstalledReleaseAuthority   string `json:"installed_release_authority"`
	AR1BaselineReceiptAuthority string `json:"ar1_baseline_receipt_authority"`
}

// AR1BaselineInput is the source inventory plus raw scenario authority required for a receipt.
type AR1BaselineInput struct {
	SourceReports []recoveryinventory.Report
	RawScenario   []byte
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
	if err := rejectDuplicateJSONMembers(data); err != nil {
		return ScenarioEvidence{}, "", fmt.Errorf("scenario envelope: %w", err)
	}

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
		{"fixture", []string{"fixture_id", "fixture_root", "run_id", "manifest_fingerprint", "database_identity_fingerprint", "export_reference", "export_fingerprint", "restore_reference", "selector_inventory_count", "structural_fingerprints", "server_marker_fingerprint"}},
		{"health", []string{"receipt_fingerprint", "server_fingerprint", "source_commit", "status", "run_id", "process_id", "process_start_utc_ticks", "port"}},
		{"candidate", []string{"source_commit", "built_payload_fingerprint", "staged_payload_fingerprint"}},
		{"behavior", []string{"retired_outcome_callbacks", "selector_only_context_inject", "health_after_behavior"}},
		{"observations", []string{"fixture_containment", "synthetic_restore", "fixture_database_binding", "owned_live_process", "staged_payload_provenance", "runtime_health_provenance", "fixture_server_health", "live_data_observed", "installed_release_authority", "ar1_baseline_receipt_authority"}},
	} {
		if err := requireRawObject(raw[nested.name], nested.fields...); err != nil {
			return ScenarioEvidence{}, "", fmt.Errorf("scenario %s: %w", nested.name, err)
		}
	}
	if err := requireRawObjectField(raw["fixture"], "structural_fingerprints", "projects", "legacy_payloads"); err != nil {
		return ScenarioEvidence{}, "", fmt.Errorf("scenario structural fingerprints: %w", err)
	}
	if err := requireRawObjectField(raw["behavior"], "selector_only_context_inject", "path", "status_code", "error_code", "upgrade_action", "canonical_project_returned"); err != nil {
		return ScenarioEvidence{}, "", fmt.Errorf("scenario selector behavior: %w", err)
	}
	if err := requireRawObjectField(raw["behavior"], "health_after_behavior", "status_code", "status", "source_commit"); err != nil {
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

// BuildAR1BaselineReceipt re-decodes raw scenario evidence and builds a deterministic receipt.
func BuildAR1BaselineReceipt(input AR1BaselineInput) (AR1BaselineReceipt, error) {
	scenario, scenarioFingerprint, err := DecodeScenarioEvidence(input.RawScenario)
	if err != nil {
		return AR1BaselineReceipt{}, err
	}
	summaries, err := summarizeSourceReports(input.SourceReports)
	if err != nil {
		return AR1BaselineReceipt{}, err
	}
	processMetric, err := scenarioProcessMetric(scenario)
	if err != nil {
		return AR1BaselineReceipt{}, err
	}
	return AR1BaselineReceipt{
		SchemaVersion:            AR1BaselineSchemaVersion,
		ReceiptAuthority:         AR1BaselineAuthority,
		Scope:                    scenario.Scope,
		SourceCommit:             scenario.Candidate.SourceCommit,
		StagedPayloadFingerprint: scenario.Candidate.StagedPayloadFingerprint,
		ScenarioFingerprint:      scenarioFingerprint,
		Health:                   scenario.Health,
		SourceInventories:        summaries,
		DurableMetrics:           []operability.Metric{},
		ProcessMetrics:           []operability.Metric{processMetric},
	}, nil
}

// writeAR1BaselineReceiptFromTestEnvironment is deliberately test-harness-only.
// It scans only a clean candidate Git worktree linked to the primary repository.
func writeAR1BaselineReceiptFromTestEnvironment() (AR1BaselineReceipt, error) {
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
	if err := requireCandidateWorktreeRoot(candidate); err != nil {
		return AR1BaselineReceipt{}, err
	}
	if err := sameGitCommonDirectory(primary, candidate); err != nil {
		return AR1BaselineReceipt{}, err
	}
	scenarioFile, err := openContainedRegularFile(primary, scenarioPath)
	if err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("test scenario receipt: %w", err)
	}
	data, readErr := io.ReadAll(scenarioFile)
	closeErr := scenarioFile.Close()
	if readErr != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("read test scenario receipt: %w", readErr)
	}
	if closeErr != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("close test scenario receipt: %w", closeErr)
	}
	scenario, _, err := DecodeScenarioEvidence(data)
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
	if err := requireCleanGitWorktree(candidate); err != nil {
		return AR1BaselineReceipt{}, err
	}
	reports, err := recoveryinventory.ScanAll(candidate)
	if err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("scan candidate source root: %w", err)
	}
	receipt, err := BuildAR1BaselineReceipt(AR1BaselineInput{SourceReports: reports, RawScenario: data})
	if err != nil {
		return AR1BaselineReceipt{}, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("marshal baseline receipt: %w", err)
	}
	outputFile, err := createContainedRegularFile(primary, outputPath)
	if err != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("test output file: %w", err)
	}
	_, writeErr := outputFile.Write(encoded)
	closeErr = outputFile.Close()
	if writeErr != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("write baseline receipt: %w", writeErr)
	}
	if closeErr != nil {
		return AR1BaselineReceipt{}, fmt.Errorf("close baseline receipt: %w", closeErr)
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

func scenarioProcessMetric(s ScenarioEvidence) (operability.Metric, error) {
	checks := int64(len(s.Behavior.RetiredOutcomeCallbacks) + 2)
	report, err := operability.BuildBaselineReport(nil, []operability.MetricInput{{
		Name:             "scenario-behavior-coverage",
		NumeratorName:    "validated_behavior_checks",
		NumeratorValue:   checks,
		DenominatorName:  "observed_behavior_checks",
		DenominatorValue: checks,
		Scope:            "fixture-server",
		Window:           "scenario",
		Freshness:        "post-behavior",
		Source:           operability.ProcessCounterSource,
	}})
	if err != nil {
		return operability.Metric{}, fmt.Errorf("derive scenario process metric: %w", err)
	}
	return report.ProcessCounters[0], nil
}

func validateScenario(s ScenarioEvidence) error {
	if s.SchemaVersion != "engram.recovery.scenario-evidence.v2" || s.EvidenceKind != "fixture_scenario" || s.Release != "AR-1" || s.Scenario != "baseline" || s.Scope != "synthetic_fixture_only" {
		return fmt.Errorf("scenario envelope is not the AR-1 synthetic baseline")
	}
	if _, err := time.Parse(time.RFC3339, s.ObservedAtUTC); err != nil {
		return fmt.Errorf("scenario observed_at_utc: %w", err)
	}
	f := s.Fixture
	if f.FixtureID != "synthetic-redacted-legacy" || !safeRelativeReference(f.FixtureRoot) || !validRunID(f.RunID) || !validFingerprint(f.ManifestFingerprint) || !validFingerprint(f.DatabaseIdentityFingerprint) || !safeRelativeReference(f.ExportReference) || !validFingerprint(f.ExportFingerprint) || !safeRelativeReference(f.RestoreReference) || f.SelectorInventoryCount < 0 || !validFingerprint(f.StructuralFingerprints.Projects) || !validFingerprint(f.StructuralFingerprints.LegacyPayloads) || !validFingerprint(f.ServerMarkerFingerprint) {
		return fmt.Errorf("scenario fixture provenance is invalid")
	}
	h := s.Health
	if h.Status != "ready" || !validFingerprint(h.ReceiptFingerprint) || !validFingerprint(h.ServerFingerprint) || !validCommit(h.SourceCommit) || h.RunID != f.RunID || h.ProcessID < 1 || h.ProcessStartUTCTicks < 1 || h.Port < 1024 || h.Port > 65535 {
		return fmt.Errorf("scenario health provenance is invalid")
	}
	c := s.Candidate
	if !validCommit(c.SourceCommit) || c.SourceCommit != h.SourceCommit || !validFingerprint(c.BuiltPayloadFingerprint) || c.BuiltPayloadFingerprint != h.ServerFingerprint || !validFingerprint(c.StagedPayloadFingerprint) || c.StagedPayloadFingerprint != h.ServerFingerprint {
		return fmt.Errorf("scenario candidate provenance is invalid")
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
	postBehavior := s.Behavior.HealthAfterBehavior
	if postBehavior.StatusCode != 200 || postBehavior.Status != "ready" || postBehavior.SourceCommit != c.SourceCommit {
		return fmt.Errorf("scenario does not prove post-behavior health provenance")
	}
	o := s.Observations
	if o.FixtureContainment != "validated" || o.SyntheticRestore != "validated" || o.FixtureDatabaseBinding != "validated" || o.OwnedLiveProcess != "validated" || o.StagedPayloadProvenance != "validated" || o.RuntimeHealthProvenance != "validated" || o.FixtureServerHealth != "ready" || o.LiveDataObserved || o.InstalledReleaseAuthority != "not_claimed" || o.AR1BaselineReceiptAuthority != "not_claimed" {
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

func rejectDuplicateJSONMembers(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := rejectDuplicateJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("scenario envelope has trailing content")
	}
	return nil
}

func rejectDuplicateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		members := make(map[string]struct{})
		for decoder.More() {
			name, err := decoder.Token()
			if err != nil {
				return err
			}
			member, ok := name.(string)
			if !ok {
				return fmt.Errorf("JSON object member is not a string")
			}
			if _, duplicate := members[member]; duplicate {
				return fmt.Errorf("duplicate JSON object member %q", member)
			}
			members[member] = struct{}{}
			if err := rejectDuplicateJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := rejectDuplicateJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
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

func openContainedRegularFile(root, path string) (*os.File, error) {
	candidate, err := absoluteContainedPath(root, path)
	if err != nil {
		return nil, err
	}
	before, err := verifyContainedRegularFile(root, candidate)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(candidate)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && (!info.Mode().IsRegular() || !os.SameFile(before, info)) {
		err = fmt.Errorf("opened file does not match verified regular file")
	}
	if err == nil {
		after, afterErr := verifyContainedRegularFile(root, candidate)
		if afterErr != nil {
			err = afterErr
		} else if !os.SameFile(after, info) {
			err = fmt.Errorf("opened file changed after verification")
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func createContainedRegularFile(root, path string) (*os.File, error) {
	candidate, err := absoluteContainedPath(root, path)
	if err != nil {
		return nil, err
	}
	if err := verifyContainedDirectories(root, candidate); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(candidate); err == nil {
		return nil, fmt.Errorf("output path already exists")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("created output is not a regular file")
	}
	if err == nil {
		after, afterErr := verifyContainedRegularFile(root, candidate)
		if afterErr != nil {
			err = afterErr
		} else if !os.SameFile(after, info) {
			err = fmt.Errorf("created output changed after verification")
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func absoluteContainedPath(root, path string) (string, error) {
	candidate, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := containedPath(root, candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

func verifyContainedRegularFile(root, path string) (os.FileInfo, error) {
	components, err := containedPathComponents(root, path)
	if err != nil {
		return nil, err
	}
	current := root
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if linkOrReparse(info) {
			return nil, fmt.Errorf("path contains a link or reparse component")
		}
		if index < len(components)-1 && !info.IsDir() {
			return nil, fmt.Errorf("path component is not a directory")
		}
		if index == len(components)-1 {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("path is not a regular file")
			}
			return info, nil
		}
	}
	return nil, fmt.Errorf("path is not a regular file")
}

func verifyContainedDirectories(root, path string) error {
	components, err := containedPathComponents(root, path)
	if err != nil {
		return err
	}
	current := root
	for _, component := range components[:len(components)-1] {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if linkOrReparse(info) {
			return fmt.Errorf("path contains a link or reparse component")
		}
		if !info.IsDir() {
			return fmt.Errorf("path component is not a directory")
		}
	}
	return nil
}

func containedPathComponents(root, path string) ([]string, error) {
	if err := containedPath(root, path); err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	return strings.Split(relative, string(filepath.Separator)), nil
}

func linkOrReparse(info os.FileInfo) bool {
	return info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0
}

func containedPath(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path is outside root")
	}
	return nil
}

func candidateCommit(root string) (string, error) {
	output, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve candidate source commit: %w", err)
	}
	commit := strings.TrimSpace(output)
	if !validCommit(commit) {
		return "", fmt.Errorf("candidate source commit is invalid")
	}
	return commit, nil
}

func requireCandidateWorktreeRoot(root string) error {
	output, err := gitOutput(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("resolve candidate Git worktree root: %w", err)
	}
	topLevel, err := absoluteDirectory(strings.TrimSpace(output))
	if err != nil {
		return fmt.Errorf("resolve candidate Git worktree root: %w", err)
	}
	if root != topLevel {
		return fmt.Errorf("candidate source root must equal its Git worktree root")
	}
	return nil
}

func sameGitCommonDirectory(primary, candidate string) error {
	primaryCommon, err := gitCommonDirectory(primary)
	if err != nil {
		return fmt.Errorf("resolve primary Git common directory: %w", err)
	}
	candidateCommon, err := gitCommonDirectory(candidate)
	if err != nil {
		return fmt.Errorf("resolve candidate Git common directory: %w", err)
	}
	primaryInfo, err := os.Stat(primaryCommon)
	if err != nil {
		return fmt.Errorf("stat primary Git common directory: %w", err)
	}
	candidateInfo, err := os.Stat(candidateCommon)
	if err != nil {
		return fmt.Errorf("stat candidate Git common directory: %w", err)
	}
	if !os.SameFile(primaryInfo, candidateInfo) {
		return fmt.Errorf("candidate source worktree has a different Git common directory")
	}
	return nil
}

func gitCommonDirectory(root string) (string, error) {
	output, err := gitOutput(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return absoluteDirectory(strings.TrimSpace(output))
}

type ignoredScanRelevantSourceAllowance func(relative string) bool

func requireCleanGitWorktree(root string) error {
	return requireCleanGitWorktreeWithIgnoredScanRelevantSourceAllowance(root, nil)
}

func requireCleanGitWorktreeWithIgnoredScanRelevantSourceAllowance(root string, allowance ignoredScanRelevantSourceAllowance) error {
	output, err := gitOutput(root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("read candidate source status: %w", err)
	}
	if output != "" {
		return fmt.Errorf("candidate source worktree is dirty")
	}
	if err := requireNoHiddenTrackedScanRelevantSource(root); err != nil {
		return err
	}
	if err := requireNoIgnoredScanRelevantSource(root, allowance); err != nil {
		return err
	}
	return nil
}

// requireNoHiddenTrackedScanRelevantSource rejects index flags that can make
// Git report changed scanned source as clean.
func requireNoHiddenTrackedScanRelevantSource(root string) error {
	output, err := gitOutput(root, "ls-files", "-v", "-z")
	if err != nil {
		return fmt.Errorf("list candidate tracked index entries: %w", err)
	}
	for _, entry := range strings.Split(output, "\x00") {
		if entry == "" {
			continue
		}
		if len(entry) < 3 || entry[1] != ' ' {
			return fmt.Errorf("parse candidate tracked index entry")
		}
		if entry[0] != 'S' && (entry[0] < 'a' || entry[0] > 'z') {
			continue
		}
		if scanRelevantSourcePath(entry[2:]) {
			return fmt.Errorf("candidate source worktree has index-hidden scan-relevant source %q", filepath.ToSlash(entry[2:]))
		}
	}
	return nil
}

func requireNoIgnoredScanRelevantSource(root string, allowance ignoredScanRelevantSourceAllowance) error {
	output, err := gitOutput(root, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return fmt.Errorf("list ignored candidate source paths: %w", err)
	}
	for _, ignored := range strings.Split(output, "\x00") {
		if ignored == "" {
			continue
		}
		relevant, err := ignoredScanRelevantSource(root, ignored, allowance)
		if err != nil {
			return fmt.Errorf("inspect ignored candidate source path %q: %w", ignored, err)
		}
		if relevant != "" {
			return fmt.Errorf("candidate source worktree has ignored scan-relevant source %q", relevant)
		}
	}
	return nil
}

func ignoredScanRelevantSource(root, ignored string, allowance ignoredScanRelevantSourceAllowance) (string, error) {
	relative := strings.TrimSuffix(filepath.ToSlash(ignored), "/")
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := containedPath(root, path); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() && skippedScanDirectory(info.Name()) {
		return "", nil
	}
	if skippedPathComponent(root, path) {
		return "", nil
	}
	if allowance != nil && allowance(relative) {
		return "", nil
	}
	if !info.IsDir() {
		if scanRelevantSourceFile(info) {
			return filepath.ToSlash(relative), nil
		}
		return "", nil
	}
	var relevant string
	err = filepath.Walk(path, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if path != filepath.Join(root, filepath.FromSlash(relative)) && skippedScanDirectory(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !scanRelevantSourceFile(info) {
			return nil
		}
		found, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if allowance != nil && allowance(filepath.ToSlash(found)) {
			return nil
		}
		relevant = filepath.ToSlash(found)
		return io.EOF
	})
	if err == io.EOF {
		return relevant, nil
	}
	if err != nil {
		return "", err
	}
	return "", nil
}

func scanRelevantSourceFile(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink == 0 && scanRelevantSourcePath(info.Name())
}

func scanRelevantSourcePath(relative string) bool {
	components := strings.Split(filepath.ToSlash(relative), "/")
	for _, component := range components[:len(components)-1] {
		if skippedScanDirectory(component) {
			return false
		}
	}
	name := components[len(components)-1]
	if strings.HasPrefix(name, ".env") {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".vue", ".proto", ".json", ".yaml", ".yml", ".md", ".sh", ".ps1", ".py":
		return true
	default:
		return false
	}
}

func skippedPathComponent(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	components := strings.Split(relative, string(filepath.Separator))
	for _, component := range components[:len(components)-1] {
		if skippedScanDirectory(component) {
			return true
		}
	}
	return false
}

func skippedScanDirectory(name string) bool {
	switch name {
	case ".git", ".agent", ".serena", "node_modules", "vendor":
		return true
	default:
		return false
	}
}

func gitOutput(root string, args ...string) (string, error) {
	output, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func validRunID(value string) bool {
	if len(value) != 32 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
