package recoveryreceipt

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/recoveryinventory"
)

func TestBuildAR1BaselineReceiptIsDeterministicAndScenarioDerived(t *testing.T) {
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
	if first.SourceCommit != strings.Repeat("a", 40) || first.StagedPayloadFingerprint != fingerprintOf("b") || first.ScenarioFingerprint != fingerprint(input.RawScenario) {
		t.Fatalf("candidate binding = %#v", first)
	}
	if len(first.SourceInventories) != 4 || first.SourceInventories[0].Inventory != "current-source" {
		t.Fatalf("source summaries = %#v", first.SourceInventories)
	}
	if len(first.DurableMetrics) != 0 {
		t.Fatalf("invented durable metrics: %#v", first.DurableMetrics)
	}
	scenario, _, err := DecodeScenarioEvidence(input.RawScenario)
	if err != nil {
		t.Fatal(err)
	}
	checks := int64(len(scenario.Behavior.RetiredOutcomeCallbacks) + 2)
	if len(first.ProcessMetrics) != 1 {
		t.Fatalf("process metrics = %#v", first.ProcessMetrics)
	}
	metric := first.ProcessMetrics[0]
	if metric.Name != "scenario-behavior-coverage" || metric.NumeratorName != "validated_behavior_checks" || metric.NumeratorValue != checks || metric.DenominatorName != "observed_behavior_checks" || metric.DenominatorValue != checks || metric.Source != "process-counter" || metric.ResultStatus != "computed" || metric.Ratio == nil || *metric.Ratio != 1 {
		t.Fatalf("scenario-derived process metric = %#v", metric)
	}
	for _, forbidden := range []string{"fixtures/ar1", "secret-token", "https://private.example", "C:\\Users"} {
		if strings.Contains(string(firstJSON), forbidden) {
			t.Fatalf("receipt leaked %q: %s", forbidden, firstJSON)
		}
	}
}

func TestBuildAR1BaselineReceiptRecomputesRawScenarioFingerprint(t *testing.T) {
	input := baselineInput(t)
	tamperedRaw := append([]byte("\n"), input.RawScenario...)
	input.RawScenario = tamperedRaw

	receipt, err := BuildAR1BaselineReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ScenarioFingerprint != fingerprint(tamperedRaw) {
		t.Fatalf("fingerprint = %q, want raw evidence fingerprint %q", receipt.ScenarioFingerprint, fingerprint(tamperedRaw))
	}
}

func TestBuildAR1BaselineReceiptRejectsInvalidEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, input *AR1BaselineInput)
	}{
		{
			name: "missing source inventory",
			mutate: func(_ *testing.T, input *AR1BaselineInput) {
				input.SourceReports = input.SourceReports[:3]
			},
		},
		{
			name: "mismatched staged payload provenance",
			mutate: func(t *testing.T, input *AR1BaselineInput) {
				mutateRawScenario(t, input, func(s *ScenarioEvidence) {
					s.Candidate.StagedPayloadFingerprint = fingerprintOf("f")
				})
			},
		},
		{
			name: "mismatched source provenance",
			mutate: func(t *testing.T, input *AR1BaselineInput) {
				mutateRawScenario(t, input, func(s *ScenarioEvidence) {
					s.Health.SourceCommit = strings.Repeat("b", 40)
				})
			},
		},
		{
			name: "retired callback false success",
			mutate: func(t *testing.T, input *AR1BaselineInput) {
				mutateRawScenario(t, input, func(s *ScenarioEvidence) {
					s.Behavior.RetiredOutcomeCallbacks[0].StatusCode = 200
				})
			},
		},
		{
			name: "selector returns canonical project",
			mutate: func(t *testing.T, input *AR1BaselineInput) {
				mutateRawScenario(t, input, func(s *ScenarioEvidence) {
					s.Behavior.SelectorOnlyContextInject.CanonicalProjectReturned = true
				})
			},
		},
		{
			name: "post behavior source differs",
			mutate: func(t *testing.T, input *AR1BaselineInput) {
				mutateRawScenario(t, input, func(s *ScenarioEvidence) {
					s.Behavior.HealthAfterBehavior.SourceCommit = strings.Repeat("b", 40)
				})
			},
		},
		{
			name: "fixture claims receipt authority",
			mutate: func(t *testing.T, input *AR1BaselineInput) {
				mutateRawScenario(t, input, func(s *ScenarioEvidence) {
					s.Observations.AR1BaselineReceiptAuthority = "claimed"
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := baselineInput(t)
			test.mutate(t, &input)
			if _, err := BuildAR1BaselineReceipt(input); err == nil {
				t.Fatal("BuildAR1BaselineReceipt accepted invalid evidence")
			}
		})
	}
}

func TestDecodeScenarioEvidenceUsesExactV2Schema(t *testing.T) {
	raw := rawScenario(t, validScenario())
	decoded, scenarioFingerprint, err := DecodeScenarioEvidence(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != "engram.recovery.scenario-evidence.v2" || decoded.Fixture.RunID != strings.Repeat("c", 32) || decoded.Health.SourceCommit != decoded.Candidate.SourceCommit || decoded.Behavior.HealthAfterBehavior.SourceCommit != decoded.Candidate.SourceCommit || scenarioFingerprint != fingerprint(raw) {
		t.Fatalf("v2 decode = %#v, %q", decoded, scenarioFingerprint)
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(envelope["fixture"], &fixture); err != nil {
		t.Fatal(err)
	}
	fixture["unbounded"] = json.RawMessage(`true`)
	envelope["fixture"], err = json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	withExtra, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeScenarioEvidence(withExtra); err == nil {
		t.Fatal("accepted v2 scenario with an unbounded fixture property")
	}
	if _, _, err := DecodeScenarioEvidence([]byte("not json")); err == nil {
		t.Fatal("accepted malformed scenario")
	}
	duplicateTopLevel := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"schema_version":"engram.recovery.scenario-evidence.v2"}`)...)
	duplicateNestedProvenance := []byte(strings.Replace(string(raw), `"health":{`, `"health":{"source_commit":"`+strings.Repeat("a", 40)+`",`, 1))
	for name, duplicate := range map[string][]byte{
		"top-level":                duplicateTopLevel,
		"nested health provenance": duplicateNestedProvenance,
	} {
		t.Run("rejects duplicate "+name+" member", func(t *testing.T) {
			if _, _, err := DecodeScenarioEvidence(duplicate); err == nil {
				t.Fatal("accepted scenario with a duplicate JSON object member")
			}
		})
	}
}

func TestWriteAR1BaselineReceiptAcceptsCleanLinkedWorktree(t *testing.T) {
	primaryRoot, candidateRoot, sourceCommit := candidateWorktree(t)
	raw, outputPath := configureWriter(t, primaryRoot, candidateRoot, sourceCommit)

	receipt, err := writeAR1BaselineReceiptFromTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	assertWrittenReceipt(t, outputPath, receipt, fingerprint(raw))
}

func TestWriteAR1BaselineReceiptRejectsForeignDirtyOrMismatchedCandidate(t *testing.T) {
	tests := []struct {
		name      string
		candidate func(t *testing.T, primaryRoot, candidateRoot string) string
		commit    func(sourceCommit string) string
	}{
		{
			name: "foreign nested repository",
			candidate: func(t *testing.T, primaryRoot, _ string) string {
				return foreignRepository(t, primaryRoot)
			},
			commit: func(sourceCommit string) string { return sourceCommit },
		},
		{
			name: "dirty candidate file",
			candidate: func(t *testing.T, _, candidateRoot string) string {
				writeFile(t, filepath.Join(candidateRoot, "untracked.txt"), "dirty\n")
				return candidateRoot
			},
			commit: func(sourceCommit string) string { return sourceCommit },
		},
		{
			name: "candidate worktree subdirectory",
			candidate: func(_ *testing.T, _, candidateRoot string) string {
				return filepath.Join(candidateRoot, "internal")
			},
			commit: func(sourceCommit string) string { return sourceCommit },
		},
		{
			name: "ignored scan-relevant source",
			candidate: func(t *testing.T, _, candidateRoot string) string {
				writeFile(t, filepath.Join(candidateRoot, "build", "forged.go"), "package forged\n")
				return candidateRoot
			},
			commit: func(sourceCommit string) string { return sourceCommit },
		},
		{
			name:      "candidate head differs from scenario",
			candidate: func(_ *testing.T, _, candidateRoot string) string { return candidateRoot },
			commit:    func(string) string { return strings.Repeat("b", 40) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primaryRoot, candidateRoot, sourceCommit := candidateWorktree(t)
			candidateRoot = test.candidate(t, primaryRoot, candidateRoot)
			configureWriter(t, primaryRoot, candidateRoot, test.commit(sourceCommit))
			if _, err := writeAR1BaselineReceiptFromTestEnvironment(); err == nil {
				t.Fatal("accepted invalid candidate source worktree")
			}
		})
	}
}

func TestWriteAR1BaselineReceiptRejectsIndexHiddenScanRelevantSource(t *testing.T) {
	primaryRoot, candidateRoot, sourceCommit := candidateWorktree(t)
	const sourcePath = "internal/fixture.go"
	gitCommand(t, "-C", candidateRoot, "update-index", "--assume-unchanged", "--", sourcePath)
	writeFile(t, filepath.Join(candidateRoot, filepath.FromSlash(sourcePath)), "package fixture\n\nconst changed = true\n")
	configureWriter(t, primaryRoot, candidateRoot, sourceCommit)

	_, err := writeAR1BaselineReceiptFromTestEnvironment()
	if err == nil {
		t.Fatal("accepted an index-hidden scan-relevant source change")
	}
	if !strings.Contains(err.Error(), "index-hidden scan-relevant source") {
		t.Fatalf("refusal = %v", err)
	}
}

func TestWriteAR1BaselineReceiptRejectsIndexHiddenScanRelevantScriptSource(t *testing.T) {
	for _, sourcePath := range []string{"scripts/fixture.cjs", "scripts/fixture.sh", "scripts/fixture.ps1", "scripts/fixture.py"} {
		t.Run(sourcePath, func(t *testing.T) {
			primaryRoot, candidateRoot, sourceCommit := candidateWorktree(t)
			gitCommand(t, "-C", candidateRoot, "update-index", "--assume-unchanged", "--", sourcePath)
			writeFile(t, filepath.Join(candidateRoot, filepath.FromSlash(sourcePath)), "changed\n")
			configureWriter(t, primaryRoot, candidateRoot, sourceCommit)

			if _, err := writeAR1BaselineReceiptFromTestEnvironment(); err == nil {
				t.Fatal("accepted an index-hidden scan-relevant script source change")
			}
		})
	}
}

func TestWriteAR1BaselineReceiptAcceptsIgnoredUnscannedArtifacts(t *testing.T) {
	primaryRoot, candidateRoot, sourceCommit := candidateWorktree(t)
	raw, outputPath := configureWriter(t, primaryRoot, candidateRoot, sourceCommit)
	for _, path := range []string{
		"build/forged.bin",
		".agent/forged.go",
		"node_modules/forged.go",
		"vendor/forged.go",
	} {
		writeFile(t, filepath.Join(candidateRoot, filepath.FromSlash(path)), "ignored\n")
	}

	receipt, err := writeAR1BaselineReceiptFromTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	assertWrittenReceipt(t, outputPath, receipt, fingerprint(raw))
}

func TestWriteAR1BaselineReceiptRejectsExistingOutputFile(t *testing.T) {
	primaryRoot, candidateRoot, sourceCommit := candidateWorktree(t)
	_, outputPath := configureWriter(t, primaryRoot, candidateRoot, sourceCommit)
	const existing = "do not overwrite\n"
	writeFile(t, outputPath, existing)

	if _, err := writeAR1BaselineReceiptFromTestEnvironment(); err == nil {
		t.Fatal("accepted an existing output file")
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != existing {
		t.Fatalf("existing output was overwritten: %q", got)
	}
}

func TestWriteAR1BaselineReceiptRejectsLinkedScenarioAndOutput(t *testing.T) {
	t.Run("scenario", func(t *testing.T) {
		primaryRoot, candidateRoot, sourceCommit := candidateWorktree(t)
		_, _ = configureWriter(t, primaryRoot, candidateRoot, sourceCommit)
		scenarioPath := filepath.Join(primaryRoot, "owned", "scenario.json")
		linkedPath := filepath.Join(primaryRoot, "owned", "scenario-link.json")
		symlinkFile(t, scenarioPath, linkedPath)
		t.Setenv(testScenarioReceiptEnv, linkedPath)

		if _, err := writeAR1BaselineReceiptFromTestEnvironment(); err == nil {
			t.Fatal("accepted a linked scenario input")
		}
	})
	t.Run("output", func(t *testing.T) {
		primaryRoot, candidateRoot, sourceCommit := candidateWorktree(t)
		_, outputPath := configureWriter(t, primaryRoot, candidateRoot, sourceCommit)
		targetPath := filepath.Join(primaryRoot, "owned", "output-target.json")
		writeFile(t, targetPath, "do not overwrite\n")
		symlinkFile(t, targetPath, outputPath)

		if _, err := writeAR1BaselineReceiptFromTestEnvironment(); err == nil {
			t.Fatal("accepted a linked output path")
		}
		got, err := os.ReadFile(targetPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "do not overwrite\n" {
			t.Fatalf("linked output target was overwritten: %q", got)
		}
	})
}

func TestWriteAR1BaselineReceiptFromConfiguredTestEnvironment(t *testing.T) {
	for _, env := range []string{testScenarioReceiptEnv, testSourceRootEnv, testPrimaryRepositoryRootEnv, testOutputFileEnv} {
		if os.Getenv(env) == "" {
			t.Skipf("configured receipt writer requires %s", env)
		}
	}

	receipt, err := writeAR1BaselineReceiptFromTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	assertWrittenReceipt(t, os.Getenv(testOutputFileEnv), receipt, receipt.ScenarioFingerprint)
	if len(receipt.DurableMetrics) != 0 || len(receipt.ProcessMetrics) != 1 || receipt.ProcessMetrics[0].Source != "process-counter" {
		t.Fatalf("configured receipt metrics = %#v", receipt)
	}
}

func assertWrittenReceipt(t *testing.T, outputPath string, receipt AR1BaselineReceipt, scenarioFingerprint string) {
	t.Helper()
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
	if decoded.ReceiptAuthority != AR1BaselineAuthority || decoded.ScenarioFingerprint != scenarioFingerprint {
		t.Fatalf("written receipt is not bound to the explicit scenario: %#v", decoded)
	}
}

func baselineInput(t *testing.T) AR1BaselineInput {
	t.Helper()
	reports, err := recoveryinventory.ScanAll(sourceRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	return AR1BaselineInput{SourceReports: reports, RawScenario: rawScenario(t, validScenario())}
}

func mutateRawScenario(t *testing.T, input *AR1BaselineInput, mutate func(*ScenarioEvidence)) {
	t.Helper()
	scenario, _, err := DecodeScenarioEvidence(input.RawScenario)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&scenario)
	input.RawScenario = rawScenario(t, scenario)
}

func rawScenario(t *testing.T, scenario ScenarioEvidence) []byte {
	t.Helper()
	encoded, err := json.Marshal(scenario)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func configureWriter(t *testing.T, primaryRoot, candidateRoot, sourceCommit string) ([]byte, string) {
	t.Helper()
	scenario := validScenario()
	scenario.Health.SourceCommit = sourceCommit
	scenario.Candidate.SourceCommit = sourceCommit
	scenario.Behavior.HealthAfterBehavior.SourceCommit = sourceCommit
	raw := rawScenario(t, scenario)
	scenarioPath := filepath.Join(primaryRoot, "owned", "scenario.json")
	outputPath := filepath.Join(primaryRoot, "owned", "ar1-baseline.json")
	writeFile(t, scenarioPath, string(raw))
	t.Setenv(testScenarioReceiptEnv, scenarioPath)
	t.Setenv(testSourceRootEnv, candidateRoot)
	t.Setenv(testPrimaryRepositoryRootEnv, primaryRoot)
	t.Setenv(testOutputFileEnv, outputPath)
	return raw, outputPath
}

func validScenario() ScenarioEvidence {
	return ScenarioEvidence{
		SchemaVersion: "engram.recovery.scenario-evidence.v2",
		EvidenceKind:  "fixture_scenario",
		Release:       "AR-1",
		Scenario:      "baseline",
		ObservedAtUTC: "2026-08-22T00:00:00Z",
		Scope:         "synthetic_fixture_only",
		Fixture: ScenarioFixture{
			FixtureID:                   "synthetic-redacted-legacy",
			FixtureRoot:                 "fixtures/ar1",
			RunID:                       strings.Repeat("c", 32),
			ManifestFingerprint:         fingerprintOf("d"),
			DatabaseIdentityFingerprint: fingerprintOf("e"),
			ExportReference:             "urn:engram:fixture-export:synthetic",
			ExportFingerprint:           fingerprintOf("f"),
			RestoreReference:            "urn:engram:fixture-restore:synthetic",
			SelectorInventoryCount:      2,
			StructuralFingerprints: FixtureStructuralFingerprints{
				Projects:       fingerprintOf("1"),
				LegacyPayloads: fingerprintOf("2"),
			},
			ServerMarkerFingerprint: fingerprintOf("3"),
		},
		Health: HealthProvenance{
			ReceiptFingerprint:   fingerprintOf("4"),
			ServerFingerprint:    fingerprintOf("b"),
			SourceCommit:         strings.Repeat("a", 40),
			Status:               "ready",
			RunID:                strings.Repeat("c", 32),
			ProcessID:            1234,
			ProcessStartUTCTicks: 123456,
			Port:                 37777,
		},
		Candidate: ScenarioCandidate{
			SourceCommit:             strings.Repeat("a", 40),
			BuiltPayloadFingerprint:  fingerprintOf("b"),
			StagedPayloadFingerprint: fingerprintOf("b"),
		},
		Behavior: ScenarioBehavior{
			RetiredOutcomeCallbacks: []RetiredOutcomeCallback{
				{Path: "/api/sessions/claude-session/propagate-outcome", StatusCode: 410, ContentType: "application/json", ContractVersion: "engram.outcome-retirement.v1", Code: "OUTCOME_CALLBACK_RETIRED", Action: "upgrade_outcome_adapter"},
				{Path: "/api/sessions/openclaw-session/outcome", StatusCode: 410, ContentType: "application/json", ContractVersion: "engram.outcome-retirement.v1", Code: "OUTCOME_CALLBACK_RETIRED", Action: "upgrade_outcome_adapter"},
			},
			SelectorOnlyContextInject: SelectorOnlyContextInject{
				Path: "/api/context/inject", StatusCode: 409, ErrorCode: "PROJECT_IDENTITY_AMBIGUOUS", UpgradeAction: "send_project_identity_v2", CanonicalProjectReturned: false,
			},
			HealthAfterBehavior: PostBehaviorHealth{StatusCode: 200, Status: "ready", SourceCommit: strings.Repeat("a", 40)},
		},
		Observations: ScenarioObservations{
			FixtureContainment:          "validated",
			SyntheticRestore:            "validated",
			FixtureDatabaseBinding:      "validated",
			OwnedLiveProcess:            "validated",
			StagedPayloadProvenance:     "validated",
			RuntimeHealthProvenance:     "validated",
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

func candidateWorktree(t *testing.T) (string, string, string) {
	t.Helper()
	primaryRoot := t.TempDir()
	writeFile(t, filepath.Join(primaryRoot, ".gitignore"), "build/\n.agent/\nnode_modules/\nvendor/\n")
	writeFile(t, filepath.Join(primaryRoot, "internal", "fixture.go"), "package fixture\n\ntype ProjectRecord struct { ID string }\n\nconst raw = \"secret-token https://private.example\"\n")
	writeFile(t, filepath.Join(primaryRoot, "scripts", "fixture.cjs"), "console.log(\"fixture\")\n")
	writeFile(t, filepath.Join(primaryRoot, "scripts", "fixture.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(primaryRoot, "scripts", "fixture.ps1"), "Write-Output fixture\n")
	writeFile(t, filepath.Join(primaryRoot, "scripts", "fixture.py"), "print(\"fixture\")\n")
	gitCommand(t, "-C", primaryRoot, "init")
	gitCommand(t, "-C", primaryRoot, "add", ".")
	gitCommand(t, "-C", primaryRoot, "-c", "user.name=AR1 Test", "-c", "user.email=ar1@example.invalid", "commit", "-m", "fixture")
	candidateRoot := filepath.Join(t.TempDir(), "candidate")
	gitCommand(t, "-C", primaryRoot, "worktree", "add", "--detach", candidateRoot, "HEAD")
	return primaryRoot, candidateRoot, gitCommand(t, "-C", candidateRoot, "rev-parse", "HEAD")
}

func symlinkFile(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
}

func foreignRepository(t *testing.T, primaryRoot string) string {
	t.Helper()
	root := filepath.Join(primaryRoot, "foreign")
	writeFile(t, filepath.Join(root, "internal", "foreign.go"), "package fixture\n")
	gitCommand(t, "-C", root, "init")
	gitCommand(t, "-C", root, "add", ".")
	gitCommand(t, "-C", root, "-c", "user.name=AR1 Test", "-c", "user.email=ar1@example.invalid", "commit", "-m", "foreign")
	return root
}

func gitCommand(t *testing.T, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
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
