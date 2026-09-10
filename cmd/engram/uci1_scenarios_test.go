package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	UCI1ScenarioReceiptSchemaVersion = "engram.uci1-scenarios-receipt/v1"

	uci1ScenarioPlanSchema              = "engram.uci.acceptance-plan/1"
	uci1ScenarioRecordPathEnv           = "ENGRAM_UCI1_SCENARIOS_RECORD_PATH"
	uci1ScenarioOperationTimeout        = 50 * time.Minute
	uci1ScenarioStatusPass              = "pass"
	uci1ScenarioStatusMissing           = "missing_installed_evidence"
	uci1ScenarioModeInstalled           = "installed_observed"
	uci1ScenarioModeInstalledProbe      = "installed_probe_observed"
	uci1ScenarioModeExactCandidate      = "exact_candidate_behavior"
	uci1ScenarioModeHistoricalInstalled = "historical_installed_revalidated"
	uci1ScenarioModeMissing             = "missing_installed_evidence"
	uci1ScenarioCodeObserved            = uciInstalledAcceptanceScenarioCodeObserved
	uci1ScenarioCodeExactCandidate      = "EXACT_CANDIDATE_BEHAVIOR"
	uci1ScenarioCodeHistoricalInstalled = "HISTORICAL_INSTALLED_REVALIDATED"
	uci1ScenarioCodeUnavailable         = "MISSING_INSTALLED_EVIDENCE"
)

var uci1RequiredScenarioIDs = [...]string{
	"U01", "U02", "U03", "U04", "U05", "U06", "U07", "U08", "U09", "U10",
	"U11", "U12", "U13", "U14", "U15", "U16", "U17", "U18", "U19", "U20",
	"U21", "U22", "U23", "U24", "U25", "U29", "U36", "U37", "U38", "U39",
	"U40", "U42", "U43", "U44", "U45", "U46",
}

// uciRunUCI1InstalledScenarioProbes is set by the T083 probe owner. Keeping
// its default nil preserves the base installed lifecycle for every other caller.
var uciRunUCI1InstalledScenarioProbes uciInstalledAcceptanceScenarioProbe = nil

type uci1ScenarioPlan struct {
	Schema    string                   `json:"schema"`
	Scenarios []uci1ScenarioDefinition `json:"scenarios"`
}

type uci1ScenarioDefinition struct {
	ID        string `json:"id"`
	Milestone string `json:"milestone"`
	Area      string `json:"area"`
}

type uci1ScenarioEvidence struct {
	Mode   string
	Code   string
	Digest string
}

// UCI1ScenarioReceipt is a deterministic, redacted projection of the current
// installed lifecycle. It binds the candidate Git identity and installed
// artifact hashes, but never retains source bodies, queries, locators, secrets,
// tool output, or raw UCI identifiers.
type UCI1ScenarioReceipt struct {
	SchemaVersion string                   `json:"schema_version"`
	Scope         UCI1ScenarioReceiptScope `json:"scope"`
	Candidate     UCI1ScenarioCandidate    `json:"candidate"`
	Runtime       UCI1ScenarioRuntime      `json:"runtime"`
	Scenarios     []UCI1ScenarioResult     `json:"scenarios"`
	Digest        string                   `json:"digest"`
}

type UCI1ScenarioReceiptScope struct {
	FixtureClass    string `json:"fixture_class"`
	Production      string `json:"production"`
	SourceBodies    string `json:"source_bodies"`
	Queries         string `json:"queries"`
	PrivateLocators string `json:"private_locators"`
	Secrets         string `json:"secrets"`
	ToolOutput      string `json:"tool_output"`
	RawIdentifiers  string `json:"raw_identifiers"`
}

type UCI1ScenarioCandidate struct {
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
}

type UCI1ScenarioRuntime struct {
	AcceptanceVersion string                 `json:"acceptance_version"`
	HarnessVersion    string                 `json:"harness_version"`
	Artifacts         []UCI1ScenarioArtifact `json:"artifacts"`
}

type UCI1ScenarioArtifact struct {
	Role            string `json:"role"`
	CandidateSHA256 string `json:"candidate_sha256"`
	InstalledSHA256 string `json:"installed_sha256"`
}

type UCI1ScenarioResult struct {
	ID           string `json:"id"`
	Area         string `json:"area"`
	Status       string `json:"status"`
	EvidenceMode string `json:"evidence_mode"`
	Code         string `json:"code"`
	Digest       string `json:"digest"`
}

// TestUCI1Scenarios is deliberately RED until each assigned scenario is added
// to the installed lifecycle result. It performs exactly one lifecycle run and
// writes an optional caller-owned receipt only after the driver's cleanup.
func TestUCI1Scenarios(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows installed UCI-1 scenario acceptance")
	}

	request := uciInstalledAcceptanceConfiguredRequest(t)
	request.EmbeddingProvider = &uciInstalledAcceptanceEmbeddingProvider{
		URL:   strings.TrimSpace(os.Getenv("ENGRAM_EMBEDDING_URL")),
		Model: strings.TrimSpace(os.Getenv("ENGRAM_EMBEDDING_MODEL")),
		Key:   strings.TrimSpace(os.Getenv("ENGRAM_EMBEDDING_API_KEY")),
	}
	request.ScenarioProbe = uciRunUCI1InstalledScenarioProbes
	request.OperationTimeout = uci1ScenarioOperationTimeout
	scenarios, err := uci1LoadScenarioPlan(filepath.Join(request.CandidateSourceRoot, "specs", "010-unified-code-intelligence", "acceptance", "scenarios.json"))
	if err != nil {
		t.Fatalf("load authoritative UCI-1 scenario plan: %v", err)
	}
	recordPath, err := uci1ScenarioRecordPath(request.CandidateSourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := uci1ReadCandidateRevision(context.Background(), request.CandidateSourceRoot)
	if err != nil {
		t.Fatalf("bind UCI-1 candidate revision: %v", err)
	}

	uciInstalledAcceptanceRequireCleanup(t, request)
	ctx, cancel := context.WithTimeout(context.Background(), request.OperationTimeout+time.Minute)
	defer cancel()
	result, err := runUCIInstalledAcceptance(ctx, request)
	if err != nil {
		t.Fatalf("run installed UCI-1 lifecycle: %v", err)
	}
	uci1RequireInstalledCleanup(t, request, result)

	installedReceipt, err := BuildUCIInstalledReceipt(result)
	if err != nil {
		t.Fatalf("assemble current installed lifecycle receipt: %v", err)
	}
	installedEvidence, err := uci1InstalledScenarioEvidence(result, installedReceipt)
	if err != nil {
		t.Fatalf("derive current installed UCI-1 scenario evidence: %v", err)
	}
	liveProbeEvidence, err := uci1InstalledScenarioProbeEvidence(result)
	if err != nil {
		t.Fatalf("derive live UCI-1 scenario probe evidence: %v", err)
	}
	evidenceCtx, evidenceCancel := context.WithTimeout(context.Background(), request.OperationTimeout+5e9)
	defer evidenceCancel()
	exactCandidateEvidence, err := uci1CollectExactCandidateEvidence(evidenceCtx, request.CandidateSourceRoot)
	if err != nil {
		t.Fatalf("collect exact candidate UCI-1 scenario evidence: %v", err)
	}
	historicalInstalledEvidence, err := uci1CollectHistoricalInstalledEvidence(evidenceCtx, request.CandidateSourceRoot)
	if err != nil {
		t.Fatalf("collect historical installed UCI-1 scenario evidence: %v", err)
	}
	historicalInstalledEvidence, err = uci1AllowedHistoricalInstalledEvidence(historicalInstalledEvidence)
	if err != nil {
		t.Fatalf("filter historical installed UCI-1 scenario evidence: %v", err)
	}
	evidence, err := uci1MergeScenarioEvidence(installedEvidence, liveProbeEvidence, exactCandidateEvidence, historicalInstalledEvidence)
	if err != nil {
		t.Fatalf("merge UCI-1 scenario evidence: %v", err)
	}
	receipt, err := BuildUCI1ScenarioReceipt(candidate, installedReceipt, scenarios, evidence)
	if err != nil {
		t.Fatalf("assemble UCI-1 scenario receipt: %v", err)
	}
	encoded, err := EncodeUCI1ScenarioReceipt(receipt)
	if err != nil {
		t.Fatalf("encode UCI-1 scenario receipt: %v", err)
	}
	if recordPath != "" {
		if err := uci1WriteScenarioReceiptAtomically(recordPath, append(encoded, '\n')); err != nil {
			t.Fatalf("write UCI-1 scenario receipt: %v", err)
		}
	}

	missing := uci1MissingScenarioIDs(receipt)
	if len(missing) > 0 {
		t.Fatalf("missing installed evidence for UCI-1 scenarios: %s", strings.Join(missing, ","))
	}
}

func uci1LoadScenarioPlan(path string) ([]uci1ScenarioDefinition, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("read authoritative UCI-1 scenario plan")
	}
	var plan uci1ScenarioPlan
	if err := json.Unmarshal(encoded, &plan); err != nil {
		return nil, errors.New("decode authoritative UCI-1 scenario plan")
	}
	if plan.Schema != uci1ScenarioPlanSchema {
		return nil, errors.New("authoritative UCI-1 scenario plan schema is not accepted")
	}

	byID := make(map[string]uci1ScenarioDefinition, len(uci1RequiredScenarioIDs))
	for _, scenario := range plan.Scenarios {
		if scenario.Milestone != "UCI-1" {
			continue
		}
		if !uci1RequiredScenarioID(scenario.ID) || !uci1SafeScenarioArea(scenario.Area) {
			return nil, errors.New("authoritative UCI-1 scenario plan has an unsafe scenario")
		}
		if _, duplicate := byID[scenario.ID]; duplicate {
			return nil, errors.New("authoritative UCI-1 scenario plan has a duplicate scenario")
		}
		byID[scenario.ID] = scenario
	}
	if len(byID) != len(uci1RequiredScenarioIDs) {
		return nil, errors.New("authoritative UCI-1 scenario plan does not contain the exact assigned set")
	}

	selected := make([]uci1ScenarioDefinition, 0, len(uci1RequiredScenarioIDs))
	for _, id := range uci1RequiredScenarioIDs {
		scenario, found := byID[id]
		if !found {
			return nil, errors.New("authoritative UCI-1 scenario plan is missing an assigned scenario")
		}
		selected = append(selected, scenario)
	}
	return selected, nil
}

func uci1RequiredScenarioID(id string) bool {
	for _, required := range uci1RequiredScenarioIDs {
		if id == required {
			return true
		}
	}
	return false
}

func uci1SafeScenarioArea(area string) bool {
	if len(area) == 0 || len(area) > 64 {
		return false
	}
	for _, value := range area {
		if (value >= 'a' && value <= 'z') || value == '_' {
			continue
		}
		return false
	}
	return true
}

func uci1ScenarioRecordPath(sourceRoot string) (string, error) {
	recordPath := strings.TrimSpace(os.Getenv(uci1ScenarioRecordPathEnv))
	if recordPath == "" {
		return "", nil
	}
	if !filepath.IsAbs(recordPath) || uciInstalledAcceptancePathOverlaps(sourceRoot, recordPath) {
		return "", errors.New("UCI-1 scenario record path must be absolute and outside the candidate source root")
	}
	parent, err := os.Stat(filepath.Dir(recordPath))
	if err != nil || !parent.IsDir() {
		return "", errors.New("UCI-1 scenario record path parent must already exist and be caller-owned")
	}
	return filepath.Clean(recordPath), nil
}

func uci1ReadCandidateRevision(ctx context.Context, root string) (UCI1ScenarioCandidate, error) {
	commit, err := uci1GitRevision(ctx, root, "HEAD")
	if err != nil {
		return UCI1ScenarioCandidate{}, err
	}
	tree, err := uci1GitRevision(ctx, root, "HEAD^{tree}")
	if err != nil {
		return UCI1ScenarioCandidate{}, err
	}
	return UCI1ScenarioCandidate{Commit: commit, Tree: tree}, nil
}

func uci1GitRevision(ctx context.Context, root, revision string) (string, error) {
	output, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--verify", revision).Output()
	if err != nil {
		return "", errors.New("read UCI-1 candidate Git revision")
	}
	value := strings.TrimSpace(string(output))
	if !uci1ValidGitObjectID(value) {
		return "", errors.New("UCI-1 candidate Git revision is unsafe")
	}
	return value, nil
}

func uci1ValidGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, value := range value {
		if (value >= '0' && value <= '9') || (value >= 'a' && value <= 'f') {
			continue
		}
		return false
	}
	return true
}

// BuildUCI1ScenarioReceipt projects exactly one current installed lifecycle
// result into every authoritative UCI-1 scenario. An unobserved behavior is
// explicitly missing evidence, never an inferred pass.
func BuildUCI1ScenarioReceipt(candidate UCI1ScenarioCandidate, installed UCIInstalledReceipt, scenarios []uci1ScenarioDefinition, evidence map[string]uci1ScenarioEvidence) (UCI1ScenarioReceipt, error) {
	if err := ValidateUCIInstalledReceipt(installed); err != nil {
		return UCI1ScenarioReceipt{}, fmt.Errorf("validate installed lifecycle input: %w", err)
	}
	if len(scenarios) != len(uci1RequiredScenarioIDs) {
		return UCI1ScenarioReceipt{}, errors.New("UCI-1 scenario receipt requires the exact assigned plan")
	}
	if err := uci1ValidateScenarioEvidenceSet(installed, evidence); err != nil {
		return UCI1ScenarioReceipt{}, fmt.Errorf("validate UCI-1 scenario evidence: %w", err)
	}

	receipt := UCI1ScenarioReceipt{
		SchemaVersion: UCI1ScenarioReceiptSchemaVersion,
		Scope: UCI1ScenarioReceiptScope{
			FixtureClass:    "synthetic_redacted",
			Production:      "not_claimed",
			SourceBodies:    "not_retained",
			Queries:         "not_retained",
			PrivateLocators: "not_retained",
			Secrets:         "not_retained",
			ToolOutput:      "not_retained",
			RawIdentifiers:  "not_retained",
		},
		Candidate: candidate,
		Runtime: UCI1ScenarioRuntime{
			AcceptanceVersion: installed.Candidate.AcceptanceVersion,
			HarnessVersion:    installed.Candidate.HarnessVersion,
			Artifacts:         make([]UCI1ScenarioArtifact, 0, len(installed.Candidate.Artifacts)),
		},
		Scenarios: make([]UCI1ScenarioResult, 0, len(scenarios)),
	}
	for _, artifact := range installed.Candidate.Artifacts {
		receipt.Runtime.Artifacts = append(receipt.Runtime.Artifacts, UCI1ScenarioArtifact{
			Role:            artifact.Role,
			CandidateSHA256: artifact.CandidateSHA256,
			InstalledSHA256: artifact.InstalledSHA256,
		})
	}
	for index, scenario := range scenarios {
		if scenario.ID != uci1RequiredScenarioIDs[index] || !uci1SafeScenarioArea(scenario.Area) {
			return UCI1ScenarioReceipt{}, errors.New("UCI-1 scenario receipt plan order is unsafe")
		}
		result := UCI1ScenarioResult{
			ID:   scenario.ID,
			Area: scenario.Area,
		}
		if scenarioEvidence, found := evidence[scenario.ID]; found {
			result.Status = uci1ScenarioStatusPass
			result.EvidenceMode = scenarioEvidence.Mode
			result.Code = scenarioEvidence.Code
			result.Digest = scenarioEvidence.Digest
		} else {
			result.Status = uci1ScenarioStatusMissing
			result.EvidenceMode = uci1ScenarioModeMissing
			result.Code = uci1ScenarioCodeUnavailable
		}
		receipt.Scenarios = append(receipt.Scenarios, result)
	}
	receipt.Digest = uci1ScenarioReceiptDigest(receipt)
	if err := ValidateUCI1ScenarioReceipt(receipt); err != nil {
		return UCI1ScenarioReceipt{}, err
	}
	return receipt, nil
}

// ValidateUCI1ScenarioReceipt rejects a receipt that is not candidate-bound,
// does not contain all 36 assigned rows, or attempts to encode an unsupported
// evidence state.
func ValidateUCI1ScenarioReceipt(receipt UCI1ScenarioReceipt) error {
	if receipt.SchemaVersion != UCI1ScenarioReceiptSchemaVersion {
		return errors.New("UCI-1 scenario receipt schema is not accepted")
	}
	if receipt.Scope != (UCI1ScenarioReceiptScope{
		FixtureClass:    "synthetic_redacted",
		Production:      "not_claimed",
		SourceBodies:    "not_retained",
		Queries:         "not_retained",
		PrivateLocators: "not_retained",
		Secrets:         "not_retained",
		ToolOutput:      "not_retained",
		RawIdentifiers:  "not_retained",
	}) {
		return errors.New("UCI-1 scenario receipt redaction scope is invalid")
	}
	if !uci1ValidGitObjectID(receipt.Candidate.Commit) || !uci1ValidGitObjectID(receipt.Candidate.Tree) {
		return errors.New("UCI-1 scenario receipt candidate binding is incomplete")
	}
	if receipt.Runtime.AcceptanceVersion != uciInstalledAcceptanceVersionV1 || receipt.Runtime.HarnessVersion != uciInstallHarnessVersionV1 || len(receipt.Runtime.Artifacts) != len(uciInstalledReceiptRoles) {
		return errors.New("UCI-1 scenario receipt installed runtime binding is incomplete")
	}
	for index, role := range uciInstalledReceiptRoles {
		artifact := receipt.Runtime.Artifacts[index]
		if artifact.Role != role || !uciInstalledReceiptValidSHA256(artifact.CandidateSHA256) || artifact.CandidateSHA256 != artifact.InstalledSHA256 {
			return errors.New("UCI-1 scenario receipt installed artifact binding is incomplete")
		}
	}
	if len(receipt.Scenarios) != len(uci1RequiredScenarioIDs) {
		return errors.New("UCI-1 scenario receipt does not contain every assigned scenario")
	}
	for index, scenario := range receipt.Scenarios {
		if scenario.ID != uci1RequiredScenarioIDs[index] || !uci1SafeScenarioArea(scenario.Area) {
			return errors.New("UCI-1 scenario receipt scenario identity is invalid")
		}
		switch scenario.Status {
		case uci1ScenarioStatusPass:
			if !uci1ValidScenarioPassEvidence(uci1ScenarioEvidence{
				Mode:   scenario.EvidenceMode,
				Code:   scenario.Code,
				Digest: scenario.Digest,
			}) {
				return errors.New("UCI-1 scenario receipt pass evidence is invalid")
			}
		case uci1ScenarioStatusMissing:
			if scenario.EvidenceMode != uci1ScenarioModeMissing || scenario.Code != uci1ScenarioCodeUnavailable || scenario.Digest != "" {
				return errors.New("UCI-1 scenario receipt missing evidence is unsafe")
			}
		default:
			return errors.New("UCI-1 scenario receipt status is not accepted")
		}
	}
	if !uciInstalledReceiptValidSHA256(receipt.Digest) || receipt.Digest != uci1ScenarioReceiptDigest(receipt) {
		return errors.New("UCI-1 scenario receipt digest does not bind its rows")
	}
	return nil
}

// EncodeUCI1ScenarioReceipt validates and encodes a map-free deterministic
// receipt. Its field set intentionally has no source, query, locator, secret,
// tool-output, or raw UCI-ID channel.
func EncodeUCI1ScenarioReceipt(receipt UCI1ScenarioReceipt) ([]byte, error) {
	if err := ValidateUCI1ScenarioReceipt(receipt); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return nil, errors.New("marshal UCI-1 scenario receipt")
	}
	return encoded, nil
}

func uci1ScenarioReceiptDigest(receipt UCI1ScenarioReceipt) string {
	values := []string{
		receipt.SchemaVersion,
		receipt.Scope.FixtureClass,
		receipt.Scope.Production,
		receipt.Scope.SourceBodies,
		receipt.Scope.Queries,
		receipt.Scope.PrivateLocators,
		receipt.Scope.Secrets,
		receipt.Scope.ToolOutput,
		receipt.Scope.RawIdentifiers,
		receipt.Candidate.Commit,
		receipt.Candidate.Tree,
		receipt.Runtime.AcceptanceVersion,
		receipt.Runtime.HarnessVersion,
	}
	for _, artifact := range receipt.Runtime.Artifacts {
		values = append(values, artifact.Role, artifact.CandidateSHA256, artifact.InstalledSHA256)
	}
	for _, scenario := range receipt.Scenarios {
		values = append(values, scenario.ID, scenario.Area, scenario.Status, scenario.EvidenceMode, scenario.Code, scenario.Digest)
	}
	return uciInstalledReceiptDigestStrings(values...)
}

func uci1ValidateScenarioEvidenceSet(installed UCIInstalledReceipt, evidence map[string]uci1ScenarioEvidence) error {
	installedEvidenceSeed := ""
	for scenarioID, scenarioEvidence := range evidence {
		if !uci1RequiredScenarioID(scenarioID) {
			return errors.New("UCI-1 scenario evidence contains an unknown scenario")
		}
		if !uci1ValidScenarioPassEvidence(scenarioEvidence) {
			return errors.New("UCI-1 scenario evidence is invalid")
		}
		if scenarioEvidence.Mode != uci1ScenarioModeInstalled {
			continue
		}
		if installedEvidenceSeed == "" {
			var err error
			installedEvidenceSeed, err = uci1InstalledScenarioEvidenceSeed(installed)
			if err != nil {
				return fmt.Errorf("encode installed lifecycle evidence: %w", err)
			}
		}
		if scenarioEvidence.Digest != uci1InstalledScenarioEvidenceDigest(installedEvidenceSeed, scenarioID) {
			return errors.New("UCI-1 scenario installed evidence digest is invalid")
		}
	}
	return nil
}

func uci1ValidScenarioPassEvidence(evidence uci1ScenarioEvidence) bool {
	if !uciInstalledReceiptValidSHA256(evidence.Digest) {
		return false
	}
	switch evidence.Mode {
	case uci1ScenarioModeInstalled, uci1ScenarioModeInstalledProbe:
		return evidence.Code == uci1ScenarioCodeObserved
	case uci1ScenarioModeExactCandidate:
		return evidence.Code == uci1ScenarioCodeExactCandidate
	case uci1ScenarioModeHistoricalInstalled:
		return evidence.Code == uci1ScenarioCodeHistoricalInstalled
	default:
		return false
	}
}

func uci1MergeScenarioEvidence(installed, installedProbe, exactCandidate, historicalInstalled map[string]uci1ScenarioEvidence) (map[string]uci1ScenarioEvidence, error) {
	sources := [...]struct {
		mode     string
		evidence map[string]uci1ScenarioEvidence
	}{
		{mode: uci1ScenarioModeHistoricalInstalled, evidence: historicalInstalled},
		{mode: uci1ScenarioModeExactCandidate, evidence: exactCandidate},
		{mode: uci1ScenarioModeInstalledProbe, evidence: installedProbe},
		{mode: uci1ScenarioModeInstalled, evidence: installed},
	}
	merged := make(map[string]uci1ScenarioEvidence, len(uci1RequiredScenarioIDs))
	for _, source := range sources {
		for scenarioID, scenarioEvidence := range source.evidence {
			if !uci1RequiredScenarioID(scenarioID) {
				return nil, errors.New("UCI-1 scenario evidence contains an unknown scenario")
			}
			if scenarioEvidence.Mode != source.mode || !uci1ValidScenarioPassEvidence(scenarioEvidence) {
				return nil, errors.New("UCI-1 scenario evidence source is invalid")
			}
			if existing, found := merged[scenarioID]; found {
				existingPrecedence := uci1ScenarioEvidencePrecedence(existing.Mode)
				candidatePrecedence := uci1ScenarioEvidencePrecedence(scenarioEvidence.Mode)
				if existingPrecedence == candidatePrecedence {
					if existing != scenarioEvidence {
						return nil, errors.New("UCI-1 scenario evidence conflicts at the same precedence")
					}
					continue
				}
				if existingPrecedence > candidatePrecedence {
					continue
				}
			}
			merged[scenarioID] = scenarioEvidence
		}
	}
	return merged, nil
}

func uci1AllowedHistoricalInstalledEvidence(input map[string]uci1ScenarioEvidence) (map[string]uci1ScenarioEvidence, error) {
	allowed := make(map[string]uci1ScenarioEvidence, 3)
	for scenarioID, scenarioEvidence := range input {
		if !uci1RequiredScenarioID(scenarioID) || scenarioEvidence.Mode != uci1ScenarioModeHistoricalInstalled || !uci1ValidScenarioPassEvidence(scenarioEvidence) {
			return nil, errors.New("historical UCI-1 scenario evidence is invalid")
		}
		if scenarioID == "U16" || scenarioID == "U19" || scenarioID == "U25" {
			allowed[scenarioID] = scenarioEvidence
		}
	}
	return allowed, nil
}

func uci1ScenarioEvidencePrecedence(mode string) int {
	switch mode {
	case uci1ScenarioModeInstalled:
		return 4
	case uci1ScenarioModeInstalledProbe:
		return 3
	case uci1ScenarioModeExactCandidate:
		return 2
	case uci1ScenarioModeHistoricalInstalled:
		return 1
	default:
		return 0
	}
}

func uci1MissingScenarioIDs(receipt UCI1ScenarioReceipt) []string {
	missing := make([]string, 0, len(receipt.Scenarios))
	for _, scenario := range receipt.Scenarios {
		if scenario.Status == uci1ScenarioStatusMissing {
			missing = append(missing, scenario.ID)
		}
	}
	return missing
}

func uci1InstalledScenarioObservations(result uciInstalledAcceptanceResult) map[string]bool {
	return map[string]bool{
		"U01": uci1ObservesDirtyViewIsolation(result),
		"U02": uci1ObservesThirdClientDefaultIsolation(result),
		"U03": uci1ObservesWatcherPublicationIsolation(result),
		"U08": uci1ObservesRestartContinuity(result),
		"U40": uci1ObservesPostSaveBarrier(result),
		"U43": uci1ObservesExposureUnavailable(result),
		"U44": uci1ObservesExposureMismatch(result),
	}
}

func uci1InstalledScenarioEvidence(result uciInstalledAcceptanceResult, installed UCIInstalledReceipt) (map[string]uci1ScenarioEvidence, error) {
	installedEvidenceSeed, err := uci1InstalledScenarioEvidenceSeed(installed)
	if err != nil {
		return nil, err
	}
	evidence := make(map[string]uci1ScenarioEvidence, len(uci1RequiredScenarioIDs))
	for scenarioID, observed := range uci1InstalledScenarioObservations(result) {
		if !observed {
			continue
		}
		evidence[scenarioID] = uci1ScenarioEvidence{
			Mode:   uci1ScenarioModeInstalled,
			Code:   uci1ScenarioCodeObserved,
			Digest: uci1InstalledScenarioEvidenceDigest(installedEvidenceSeed, scenarioID),
		}
	}
	return evidence, nil
}

func uci1InstalledScenarioProbeEvidence(result uciInstalledAcceptanceResult) (map[string]uci1ScenarioEvidence, error) {
	if err := uciValidateInstalledAcceptanceScenarioEvidence(result.ScenarioEvidence); err != nil {
		return nil, err
	}
	evidence := make(map[string]uci1ScenarioEvidence, len(result.ScenarioEvidence))
	for scenarioID, scenarioEvidence := range result.ScenarioEvidence {
		if !uci1RequiredScenarioID(scenarioID) {
			return nil, errors.New("installed scenario probe evidence contains an unknown scenario")
		}
		evidence[scenarioID] = uci1ScenarioEvidence{
			Mode:   uci1ScenarioModeInstalledProbe,
			Code:   scenarioEvidence.Code,
			Digest: scenarioEvidence.Digest,
		}
	}
	return evidence, nil
}

func uci1InstalledScenarioEvidenceSeed(installed UCIInstalledReceipt) (string, error) {
	encoded, err := EncodeUCIInstalledReceipt(installed)
	if err != nil {
		return "", err
	}
	return uciInstalledReceiptDigestStrings("engram.uci1-scenarios/installed-observed/v1", string(encoded)), nil
}

func uci1InstalledScenarioEvidenceDigest(installedEvidenceSeed, scenarioID string) string {
	return uciInstalledReceiptDigestStrings("engram.uci1-scenarios/installed-observed/v1", scenarioID, installedEvidenceSeed)
}

func uci1ObservesDirtyViewIsolation(result uciInstalledAcceptanceResult) bool {
	primary, primaryOK := result.ClientContexts[uciInstalledAcceptanceClientA]
	linked, linkedOK := result.ClientContexts[uciInstalledAcceptanceClientB]
	return primaryOK && linkedOK &&
		result.Worktrees.Registered[uciInstalledAcceptanceClientA] &&
		result.Worktrees.Registered[uciInstalledAcceptanceClientB] &&
		result.Worktrees.UsedGitArgumentVectors && result.Worktrees.LinkedGitFile && result.Worktrees.SameHead &&
		result.Worktrees.PrimaryDirty && result.Worktrees.LinkedDirty &&
		primary.SourceDigest == linked.SourceDigest && primary.CheckoutDigest != linked.CheckoutDigest && primary.ViewDigest != linked.ViewDigest &&
		uci1MatchesSingleDigest(result.Observations[uciInstalledAcceptanceClientA].SearchArtifactDigests, result.Fixture.PrimarySourceDigest) &&
		uci1MatchesSingleDigest(result.Observations[uciInstalledAcceptanceClientA].GraphCalleeDigests, result.Fixture.PrimaryCalleeDigest) &&
		uci1MatchesSingleDigest(result.Observations[uciInstalledAcceptanceClientA].ReadArtifactDigests, result.Fixture.PrimarySourceDigest) &&
		uci1MatchesSingleDigest(result.Observations[uciInstalledAcceptanceClientB].SearchArtifactDigests, result.Fixture.LinkedSourceDigest) &&
		uci1MatchesSingleDigest(result.Observations[uciInstalledAcceptanceClientB].GraphCalleeDigests, result.Fixture.LinkedCalleeDigest) &&
		uci1MatchesSingleDigest(result.Observations[uciInstalledAcceptanceClientB].ReadArtifactDigests, result.Fixture.LinkedSourceDigest)
}

func uci1ObservesThirdClientDefaultIsolation(result uciInstalledAcceptanceResult) bool {
	third, thirdOK := result.ClientTranscripts[uciInstalledAcceptanceClientC]
	if !thirdOK || !third.UsedStdio {
		return false
	}
	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB} {
		contextRef, found := result.ClientContexts[client]
		if !found || result.Defaults.BeforeThirdClientViewDigests[client] != contextRef.ViewDigest || result.Defaults.AfterThirdClientViewDigests[client] != contextRef.ViewDigest {
			return false
		}
	}
	return true
}

func uci1ObservesWatcherPublicationIsolation(result uciInstalledAcceptanceResult) bool {
	initialA, initialAOK := result.ClientContexts[uciInstalledAcceptanceClientA]
	initialB, initialBOK := result.ClientContexts[uciInstalledAcceptanceClientB]
	writeA, deleteA := result.Watcher.AfterWriteA, result.Watcher.AfterDeleteA
	writeB, deleteB := result.Watcher.AfterWriteB, result.Watcher.AfterDeleteB
	return initialAOK && initialBOK &&
		writeA.SourceDigest == initialA.SourceDigest && writeA.CheckoutDigest == initialA.CheckoutDigest && writeA.ViewDigest != initialA.ViewDigest &&
		deleteA.SourceDigest == initialA.SourceDigest && deleteA.CheckoutDigest == initialA.CheckoutDigest && deleteA.ViewDigest != writeA.ViewDigest &&
		writeA.Generation >= 2 && deleteA.Generation > writeA.Generation &&
		writeA.FreshnessState == "observed_current" && writeA.BarrierState == "satisfied" &&
		deleteA.FreshnessState == "observed_current" && deleteA.BarrierState == "satisfied" &&
		writeB.SourceDigest == initialB.SourceDigest && writeB.CheckoutDigest == initialB.CheckoutDigest && writeB.ViewDigest == initialB.ViewDigest &&
		deleteB == writeB && writeB.Generation >= 1 && writeB.FreshnessState == "observed_current"
}

func uci1ObservesRestartContinuity(result uciInstalledAcceptanceResult) bool {
	before, after := result.Restart.BeforeProcesses, result.Restart.AfterProcesses
	if before.ServerPID <= 0 || before.DaemonPID <= 0 || before.ParserPID <= 0 ||
		after.ServerPID <= 0 || after.DaemonPID <= 0 || after.ParserPID <= 0 ||
		before.ServerPID == after.ServerPID || before.DaemonPID == after.DaemonPID || before.ParserPID == after.ParserPID ||
		result.Restart.UnchangedInputReembedded || result.Restart.BeforeProjectionCounts != result.Restart.AfterProjectionCounts {
		return false
	}
	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB} {
		beforeContext, beforeContextOK := result.Restart.BeforeClientContexts[client]
		afterContext, afterContextOK := result.Restart.ClientContexts[client]
		beforeObservation, beforeObservationOK := result.Restart.BeforeObservations[client]
		afterObservation, afterObservationOK := result.Restart.Observations[client]
		if !beforeContextOK || !afterContextOK || beforeContext != afterContext || !beforeObservationOK || !afterObservationOK || !uciInstalledAcceptanceSameObservations(beforeObservation, afterObservation) {
			return false
		}
	}
	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB, uciInstalledAcceptanceClientC} {
		transcript, found := result.Restart.ClientTranscripts[client]
		if !found || !transcript.UsedStdio {
			return false
		}
	}
	return result.Restart.ClientContexts[uciInstalledAcceptanceClientC] == result.Restart.BeforeClientContexts[uciInstalledAcceptanceClientA]
}

func uci1ObservesPostSaveBarrier(result uciInstalledAcceptanceResult) bool {
	return result.Watcher.AfterWriteA.FreshnessState == "observed_current" && result.Watcher.AfterWriteA.BarrierState == "satisfied" &&
		result.Watcher.AfterDeleteA.FreshnessState == "observed_current" && result.Watcher.AfterDeleteA.BarrierState == "satisfied"
}

func uci1ObservesExposureUnavailable(result uciInstalledAcceptanceResult) bool {
	return uci1ClosedOutcomeWithoutContext(result.Recorder.InitialUnavailable, "unavailable", "EXPOSURE_UNAVAILABLE") && result.Recorder.HealthAfterInitialUnavailable == "unavailable"
}

func uci1ObservesExposureMismatch(result uciInstalledAcceptanceResult) bool {
	return uci1ClosedOutcomeWithoutContext(result.Recorder.Mismatch, "unavailable", "IDEMPOTENCY_MISMATCH") &&
		uciInstalledReceiptValidSHA256(result.Recorder.FirstExposureDigest) &&
		result.Recorder.FirstExposureDigest == result.Recorder.ExactRetryExposureDigest &&
		result.Recorder.HealthBeforeMismatch == "healthy" && result.Recorder.HealthAfterMismatch == "healthy"
}

func uci1ClosedOutcomeWithoutContext(outcome uciInstalledAcceptanceClosedOutcome, status, code string) bool {
	return outcome.Status == status && outcome.ErrorCode == code && outcome.ContextDigest == "" &&
		len(outcome.ContentDigests) == 0 && len(outcome.GraphDigests) == 0 && outcome.ExposureDigest == ""
}

func uci1MatchesSingleDigest(values []string, want string) bool {
	return len(values) == 1 && uciInstalledReceiptValidSHA256(want) && values[0] == want
}

func uci1RequireInstalledCleanup(t *testing.T, request uciInstalledAcceptanceRequest, result uciInstalledAcceptanceResult) {
	t.Helper()
	if !result.Cleanup.ProcessTreeClosed || !result.Cleanup.InstallRootRemoved || !result.Cleanup.FixtureRootRemoved || !result.Cleanup.LocalStateRootRemoved {
		t.Fatal("installed UCI-1 lifecycle did not finish cleanup before scenario receipt")
	}
	for _, root := range []string{request.InstallRoot, request.FixtureRoot, request.LocalStateRoot} {
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("installed UCI-1 lifecycle retained disposable state before scenario receipt")
		}
	}
}

func uci1WriteScenarioReceiptAtomically(path string, encoded []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".uci1-scenarios-")
	if err != nil {
		return errors.New("create UCI-1 scenario receipt temporary file")
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return errors.New("protect UCI-1 scenario receipt temporary file")
	}
	if _, err := temporary.Write(encoded); err != nil {
		return errors.New("write UCI-1 scenario receipt temporary file")
	}
	if err := temporary.Sync(); err != nil {
		return errors.New("sync UCI-1 scenario receipt temporary file")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close UCI-1 scenario receipt temporary file")
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return errors.New("atomically replace UCI-1 scenario receipt")
	}
	return nil
}
