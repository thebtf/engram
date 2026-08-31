package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/vectordim"
)

func TestParseCommandInputBoundaries(t *testing.T) {
	validID := strings.Repeat("a", 40)
	input, err := parseCommandInput([]string{
		"--run-id", "tm05_fixture",
		"--source-commit", validID,
		"--source-tree", validID,
	}, func(string) string {
		return "postgres://fixture:password@127.0.0.1/hap02a_tm05_fixture?sslmode=disable"
	})
	if err != nil {
		t.Fatalf("parse valid input: %v", err)
	}
	if input.RunID != "tm05_fixture" || input.SourceCommit != validID || input.SourceTree != validID {
		t.Fatalf("parsed input = %#v", input)
	}

	for _, arguments := range [][]string{
		{"--run-id", "INVALID", "--source-commit", validID, "--source-tree", validID},
		{"--run-id", "tm05_fixture", "--source-commit", strings.Repeat("A", 40), "--source-tree", validID},
		{"--run-id", "tm05_fixture", "--source-commit", validID},
		{"--unexpected"},
	} {
		if _, err := parseCommandInput(arguments, func(string) string { return "postgres://fixture@localhost/hap02a_tm05_fixture" }); err == nil {
			t.Fatalf("arguments %q unexpectedly accepted", arguments)
		}
	}
}

func TestDedicatedDSNBoundaries(t *testing.T) {
	valid := "postgres://fixture:password@127.0.0.1:5432/hap02a_tm05_fixture?sslmode=disable"
	connection, err := parseDedicatedDSN(valid)
	if err != nil {
		t.Fatalf("parse dedicated DSN: %v", err)
	}
	if connection.database != "hap02a_tm05_fixture" || connection.dsn == "" {
		t.Fatalf("connection = %#v", connection)
	}

	for _, dsn := range []string{
		"mysql://fixture:password@127.0.0.1/hap02a_tm05_fixture",
		"postgres://fixture:password@example.invalid/hap02a_tm05_fixture",
		"postgres://fixture:password@127.0.0.1/shared_database",
		"postgres://@127.0.0.1/hap02a_tm05_fixture",
		"postgres://fixture:password@127.0.0.1/hap02a_tm05_fixture?host=example.invalid",
	} {
		if _, err := parseDedicatedDSN(dsn); err == nil {
			t.Fatalf("unsafe DSN accepted: %q", dsn)
		}
	}
}

func TestReceiptRedactionAndClosedShape(t *testing.T) {
	receipt := qualificationReceipt{
		Schema: qualificationSchema,
		Source: sourceReceipt{
			Commit:                  strings.Repeat("a", 40),
			Tree:                    strings.Repeat("b", 40),
			PreparationRevision:     "task-memory-prepare/1",
			QueryProfileFingerprint: strings.Repeat("c", 64),
		},
		CleanupRequired:       true,
		TransactionRolledBack: true,
	}
	encoded, err := marshalReceipt(receipt)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	for _, forbidden := range [][]byte{[]byte("postgres://fixture:password@localhost/hap02a_tm05_fixture"), []byte("password"), []byte(`C:\fixture`), []byte(exactQuery), []byte("chunk body")} {
		if bytes.Contains(encoded, forbidden) {
			t.Fatalf("receipt retained forbidden value %q: %s", forbidden, encoded)
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	want := map[string]struct{}{
		"schema": {}, "source": {}, "database": {}, "scenarios": {}, "mutation": {}, "cleanup_required": {}, "transaction_rolled_back": {},
	}
	if len(fields) != len(want) {
		t.Fatalf("receipt fields = %v, want %v", fields, want)
	}
	for key := range want {
		if _, ok := fields[key]; !ok {
			t.Fatalf("receipt missing field %q", key)
		}
	}
}

func TestQualificationExpectationsAreDeterministicAndNonzero(t *testing.T) {
	first := qualificationProfileFingerprint()
	second := qualificationProfileFingerprint()
	if first == "" || first != second {
		t.Fatalf("profile fingerprint first=%q second=%q", first, second)
	}
	for _, scenario := range qualificationScenarios {
		if len(scenario.ExpectedReferences) == 0 {
			t.Fatalf("scenario %q has a zero expected denominator", scenario.Name)
		}
		if !equalReferenceTriples(scenario.ExpectedReferences, cloneReferenceTriples(scenario.ExpectedReferences)) {
			t.Fatalf("scenario %q references are not deterministic", scenario.Name)
		}
	}
}

func TestFixtureUnitVector(t *testing.T) {
	vector := fixtureUnitVector(0.8)
	if len(vector) != vectordim.Dimension {
		t.Fatalf("vector length = %d, want %d", len(vector), vectordim.Dimension)
	}
	var normSquared float64
	for _, value := range vector {
		normSquared += float64(value) * float64(value)
	}
	if math.Abs(normSquared-1) > 1e-5 {
		t.Fatalf("vector norm squared = %v", normSquared)
	}
}

func TestHashSourceFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.go")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	digest, err := hashSourceFile(path)
	if err != nil {
		t.Fatalf("hash source: %v", err)
	}
	if digest != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("digest = %q", digest)
	}
}

func TestSourceRootBindsCurrentCheckout(t *testing.T) {
	root, err := sourceRoot()
	if err != nil {
		t.Fatalf("resolve source root: %v", err)
	}
	expected, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve expected source root: %v", err)
	}
	if filepath.Clean(root) != filepath.Clean(expected) {
		t.Fatalf("source root = %q, want current checkout %q", root, expected)
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "taskmemory", "task_memory.go")); err != nil {
		t.Fatalf("current checkout taskmemory source: %v", err)
	}
}

func TestValidateAuditDelta(t *testing.T) {
	comparison := comparisonAuditReceipt{
		IdempotencyKey:         "sha256:" + strings.Repeat("a", 64),
		EvidenceFingerprint:    "sha256:" + strings.Repeat("b", 64),
		ClientInstanceIDSHA256: strings.Repeat("c", 64),
		V3Outcome:              "PROJECT_RESOLVED",
		LegacyOutcome:          "unavailable",
		Classification:         "unavailable",
		Transport:              "daemon",
		Scope:                  "repository",
		Freshness:              "unknown",
	}
	attempt := resolutionAuditReceipt{
		Intent:            "read_filter",
		Outcome:           "PROJECT_RESOLVED",
		DescriptorVersion: 3,
		Provenance:        "anchor_v3",
	}
	unique := auditDeltaReceipt{
		Applicable:               true,
		CorrelationSHA256:        strings.Repeat("d", 64),
		ResolutionAttemptsBefore: 0,
		ResolutionAttemptsAfter:  1,
		ResolutionAttemptsDelta:  1,
		ResolutionAttempts:       []resolutionAuditReceipt{attempt},
		ComparisonsBefore:        0,
		ComparisonsAfter:         1,
		ComparisonsDelta:         1,
		Comparisons:              []comparisonAuditReceipt{comparison},
	}
	if err := validateAuditDelta(unique, 1); err != nil {
		t.Fatalf("validate unique delta: %v", err)
	}
	replay := unique
	replay.ResolutionAttemptsBefore = 1
	replay.ResolutionAttemptsAfter = 2
	replay.ResolutionAttempts = []resolutionAuditReceipt{attempt, attempt}
	replay.ComparisonsBefore = 1
	replay.ComparisonsAfter = 1
	replay.ComparisonsDelta = 0
	if err := validateAuditDelta(replay, 0); err != nil {
		t.Fatalf("validate replay delta: %v", err)
	}
	replay.ResolutionAttemptsDelta = 0
	if err := validateAuditDelta(replay, 0); err == nil {
		t.Fatal("invalid resolution delta accepted")
	}
}
