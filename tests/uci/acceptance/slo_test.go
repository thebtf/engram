package acceptance

import (
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/uci"
)

const (
	uciSLOReportSchemaVersion    = "engram.uci-slo-report/v1"
	uciSLOMinimumHealthySamples  = 100
	uciSLOMaximumLinesOfCode     = 1_000_000
	uciSLOMinimumActiveWorktrees = 5
	uciSLOMaximumLANRTT          = 50 * time.Millisecond
	uciSLOMaximumChangedFiles    = 20
	uciSLOMaximumChangedBytes    = 1 << 20
)

type uciSLOGateExpectation struct {
	name          string
	operation     string
	p95Limit      time.Duration
	maxGraphDepth int
}

var uciSLOExpectedWarmGates = []uciSLOGateExpectation{
	{name: "update.structural_fts", operation: "update", p95Limit: 2 * time.Second},
	{name: "update.embedding_readiness", operation: "update", p95Limit: 10 * time.Second},
	{name: "search.server_retrieval", operation: "search", p95Limit: 800 * time.Millisecond},
	{name: "search.query_embedding", operation: "search", p95Limit: 2500 * time.Millisecond},
	{name: "graph.small_explain_neighbors_impact", operation: "graph", p95Limit: time.Second, maxGraphDepth: 4},
}

func TestUCISLOProfile(t *testing.T) {
	if os.Getenv(uciSLOInputPathEnv) == "" {
		t.Skip("UCI SLO profile requires externally recorded evidence at ENGRAM_UCI_SLO_INPUT")
	}

	report, err := RunUCISLOProfile(t.Context())
	if err != nil {
		t.Fatalf("run UCI healthy-profile SLO fixture: %v", err)
	}

	if violations := uci.ValidateUCISLOReport(report); len(violations) != 0 {
		t.Fatalf("UCI SLO report violations = %#v", violations)
	}

	requireUCISLOIdentity(t, report)
	requireUCIHealthyProfile(t, report)
	requireUCISLOColdObservations(t, report)
	requireUCISLOWarmGates(t, report)
}

func TestUCISLOProfileRejectsIncompleteUnsafeReport(t *testing.T) {
	// This is deliberately incomplete invalid input, not measurement evidence.
	report := uci.UCISLOReport{
		SchemaVersion: uciSLOReportSchemaVersion,
		Gates: []uci.UCISLOGateReport{
			{
				Name:               "update.structural_fts",
				Operation:          "update",
				Warmth:             "warm",
				ProfileID:          "profile-a",
				ContextID:          "context-a",
				HealthySampleCount: 1,
				Samples: []uci.UCISLOSample{
					{
						Outcome:   "degraded",
						Warmth:    "warm",
						ProfileID: "profile-b",
						ContextID: "context-b",
						Reason:    "degraded",
					},
				},
				Percentile: uci.UCISLOPercentileDisclosure{
					Population:         "filtered_healthy_samples",
					OmittedSampleCount: 1,
				},
			},
		},
	}

	requireUCISLOViolationCodes(t, uci.ValidateUCISLOReport(report),
		"candidate_identity_missing",
		"minimum_healthy_samples",
		"degraded_sample_counted_healthy",
		"mixed_profile",
		"mixed_context",
		"unbounded_wait",
		"undisclosed_partial_percentile",
	)
}

func requireUCISLOIdentity(t *testing.T, report uci.UCISLOReport) {
	t.Helper()

	if report.SchemaVersion != uciSLOReportSchemaVersion {
		t.Fatalf("report schema version = %q, want %q", report.SchemaVersion, uciSLOReportSchemaVersion)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "candidate branch", value: report.Candidate.Branch},
		{name: "candidate commit", value: report.Candidate.Commit},
		{name: "candidate tree", value: report.Candidate.Tree},
		{name: "host identity", value: report.Environment.Host.ID},
		{name: "database identity", value: report.Environment.Database.ID},
		{name: "database version", value: report.Environment.Database.Version},
		{name: "corpus identity", value: report.Environment.Corpus.ID},
		{name: "corpus manifest", value: report.Environment.Corpus.ManifestDigest},
		{name: "provider identity", value: report.Environment.Provider.ID},
		{name: "provider model", value: report.Environment.Provider.Model},
		{name: "provider status", value: report.Environment.Provider.Status},
		{name: "profile identity", value: report.Profile.ID},
	} {
		if strings.TrimSpace(field.value) == "" {
			t.Fatalf("report omitted %s", field.name)
		}
	}
	if len(report.Candidate.ArtifactDigests) == 0 {
		t.Fatal("report omitted installed artifact digests")
	}
	for _, artifact := range report.Candidate.ArtifactDigests {
		if strings.TrimSpace(artifact.Name) == "" || strings.TrimSpace(artifact.Digest) == "" {
			t.Fatalf("report has incomplete installed artifact identity: %#v", artifact)
		}
	}
	if report.Environment.Database.DataSizeBytes == 0 {
		t.Fatal("report omitted database data size")
	}
}

func requireUCIHealthyProfile(t *testing.T, report uci.UCISLOReport) {
	t.Helper()

	profile := report.Profile
	if profile.TextFileCount == 0 {
		t.Fatal("report omitted corpus text-file count")
	}
	if profile.LinesOfCode == 0 {
		t.Fatal("report omitted corpus line count")
	}
	if profile.LinesOfCode > uciSLOMaximumLinesOfCode {
		t.Fatalf("profile lines of code = %d, exceeds accepted profile limit %d", profile.LinesOfCode, uciSLOMaximumLinesOfCode)
	}
	if profile.ActiveWorktreeCount < uciSLOMinimumActiveWorktrees {
		t.Fatalf("profile active worktrees = %d, want at least %d", profile.ActiveWorktreeCount, uciSLOMinimumActiveWorktrees)
	}
	if profile.InactiveRegistrationCount == 0 {
		t.Fatal("report omitted inactive registry coverage")
	}
	if profile.LANRTT > uciSLOMaximumLANRTT {
		t.Fatalf("profile LAN RTT = %s, exceeds accepted profile limit %s", profile.LANRTT, uciSLOMaximumLANRTT)
	}
	if profile.ChangedFileCount == 0 {
		t.Fatal("report omitted saved-update workload")
	}
	if profile.ChangedFileCount > uciSLOMaximumChangedFiles {
		t.Fatalf("profile changed files = %d, exceeds accepted profile limit %d", profile.ChangedFileCount, uciSLOMaximumChangedFiles)
	}
	if profile.ChangedBytes == 0 {
		t.Fatal("report omitted saved-update byte count")
	}
	if profile.ChangedBytes > uciSLOMaximumChangedBytes {
		t.Fatalf("profile changed bytes = %d, exceeds accepted profile limit %d", profile.ChangedBytes, uciSLOMaximumChangedBytes)
	}
	if report.Environment.Provider.Status != "healthy" {
		t.Fatalf("provider status = %q, want healthy profile", report.Environment.Provider.Status)
	}
}

func requireUCISLOColdObservations(t *testing.T, report uci.UCISLOReport) {
	t.Helper()

	if len(report.ColdObservations) == 0 {
		t.Fatal("report omitted separately labelled cold observations")
	}
	for _, observation := range report.ColdObservations {
		if observation.Warmth != "cold" {
			t.Fatalf("cold observation warmth = %q, want cold", observation.Warmth)
		}
		if strings.TrimSpace(observation.Operation) == "" {
			t.Fatal("cold observation omitted operation label")
		}
	}
}

func requireUCISLOWarmGates(t *testing.T, report uci.UCISLOReport) {
	t.Helper()

	byName := make(map[string]uci.UCISLOGateReport, len(report.Gates))
	for _, gate := range report.Gates {
		if _, duplicate := byName[gate.Name]; duplicate {
			t.Fatalf("report contains duplicate SLO gate %q", gate.Name)
		}
		byName[gate.Name] = gate
	}

	for _, want := range uciSLOExpectedWarmGates {
		gate, found := byName[want.name]
		if !found {
			t.Fatalf("report omitted SLO gate %q", want.name)
		}
		requireUCISLOWarmGate(t, report, gate, want)
	}
}

func requireUCISLOWarmGate(t *testing.T, report uci.UCISLOReport, gate uci.UCISLOGateReport, want uciSLOGateExpectation) {
	t.Helper()

	if gate.Operation != want.operation {
		t.Fatalf("gate %q operation = %q, want %q", want.name, gate.Operation, want.operation)
	}
	if gate.Warmth != "warm" {
		t.Fatalf("gate %q warmth = %q, want warm", want.name, gate.Warmth)
	}
	if gate.Threshold != want.p95Limit {
		t.Fatalf("gate %q threshold = %s, want documented p95 limit %s", want.name, gate.Threshold, want.p95Limit)
	}
	if gate.WaitBound <= 0 {
		t.Fatalf("gate %q has an unbounded wait", want.name)
	}
	if strings.TrimSpace(gate.ProfileID) == "" || strings.TrimSpace(gate.ContextID) == "" {
		t.Fatalf("gate %q omitted profile or context identity", want.name)
	}
	if gate.ProfileID != report.Profile.ID {
		t.Fatalf("gate %q profile = %q, report profile = %q", want.name, gate.ProfileID, report.Profile.ID)
	}
	if want.maxGraphDepth != 0 && (gate.MaxGraphDepth <= 0 || gate.MaxGraphDepth > want.maxGraphDepth || len(gate.QueryCaps) == 0) {
		t.Fatalf("gate %q graph bound = depth %d caps %#v, want depth <= %d with declared caps", want.name, gate.MaxGraphDepth, gate.QueryCaps, want.maxGraphDepth)
	}
	if gate.SampleCount != len(gate.Samples) {
		t.Fatalf("gate %q sample count = %d, raw samples = %d", want.name, gate.SampleCount, len(gate.Samples))
	}

	healthyLatencies := make([]time.Duration, 0, len(gate.Samples))
	degradedReasons := make([]string, 0, gate.DegradedCount)
	unavailableReasons := make([]string, 0, gate.UnavailableCount)
	failureCount := 0
	for index, sample := range gate.Samples {
		if sample.Warmth != gate.Warmth {
			t.Fatalf("gate %q sample %d warmth = %q, want %q", want.name, index, sample.Warmth, gate.Warmth)
		}
		if sample.ProfileID != gate.ProfileID {
			t.Fatalf("gate %q sample %d profile = %q, want %q", want.name, index, sample.ProfileID, gate.ProfileID)
		}
		if sample.ContextID != gate.ContextID {
			t.Fatalf("gate %q sample %d context = %q, want %q", want.name, index, sample.ContextID, gate.ContextID)
		}
		if sample.Waited < 0 {
			t.Fatalf("gate %q sample %d recorded a negative wait %s", want.name, index, sample.Waited)
		}
		if sample.Waited > gate.WaitBound {
			t.Fatalf("gate %q sample %d waited %s beyond bound %s", want.name, index, sample.Waited, gate.WaitBound)
		}

		switch sample.Outcome {
		case "healthy":
			if sample.Latency <= 0 {
				t.Fatalf("gate %q healthy sample %d omitted a positive latency", want.name, index)
			}
			healthyLatencies = append(healthyLatencies, sample.Latency)
		case "degraded":
			if strings.TrimSpace(sample.Reason) == "" {
				t.Fatalf("gate %q degraded sample %d omitted reason", want.name, index)
			}
			degradedReasons = append(degradedReasons, sample.Reason)
		case "unavailable":
			if strings.TrimSpace(sample.Reason) == "" {
				t.Fatalf("gate %q unavailable sample %d omitted reason", want.name, index)
			}
			unavailableReasons = append(unavailableReasons, sample.Reason)
		case "failed":
			failureCount++
		default:
			t.Fatalf("gate %q sample %d has unsupported outcome %q", want.name, index, sample.Outcome)
		}
	}

	if gate.HealthySampleCount < uciSLOMinimumHealthySamples {
		t.Fatalf("gate %q healthy samples = %d, want at least %d", want.name, gate.HealthySampleCount, uciSLOMinimumHealthySamples)
	}
	if gate.HealthySampleCount != len(healthyLatencies) {
		t.Fatalf("gate %q healthy samples = %d, healthy raw outcomes = %d", want.name, gate.HealthySampleCount, len(healthyLatencies))
	}
	if gate.DegradedCount != len(degradedReasons) {
		t.Fatalf("gate %q degraded count = %d, degraded raw outcomes = %d", want.name, gate.DegradedCount, len(degradedReasons))
	}
	if gate.UnavailableCount != len(unavailableReasons) {
		t.Fatalf("gate %q unavailable count = %d, unavailable raw outcomes = %d", want.name, gate.UnavailableCount, len(unavailableReasons))
	}
	if gate.FailureCount != failureCount {
		t.Fatalf("gate %q failure count = %d, failed raw outcomes = %d", want.name, gate.FailureCount, failureCount)
	}
	requireUCISLOReasons(t, want.name, "degraded", gate.DegradedCount, gate.DegradedReasons, degradedReasons)
	requireUCISLOReasons(t, want.name, "unavailable", gate.UnavailableCount, gate.UnavailableReasons, unavailableReasons)

	if len(gate.HealthyLatencyInputs) != len(healthyLatencies) {
		t.Fatalf("gate %q healthy latency inputs = %d, healthy outcomes = %d", want.name, len(gate.HealthyLatencyInputs), len(healthyLatencies))
	}
	if !slices.IsSorted(gate.HealthyLatencyInputs) {
		t.Fatalf("gate %q healthy latency inputs are not sorted", want.name)
	}
	sort.Slice(healthyLatencies, func(i, j int) bool { return healthyLatencies[i] < healthyLatencies[j] })
	if !slices.Equal(gate.HealthyLatencyInputs, healthyLatencies) {
		t.Fatalf("gate %q latency inputs do not match every healthy sample", want.name)
	}

	if gate.Percentile.Population != "all_healthy_samples" ||
		gate.Percentile.InputSampleCount != gate.HealthySampleCount ||
		gate.Percentile.OmittedSampleCount != 0 ||
		len(gate.Percentile.OmissionReasons) != 0 {
		t.Fatalf("gate %q percentile population is not a complete disclosed healthy set: %#v", want.name, gate.Percentile)
	}
	if got, wantP95 := gate.P95, uciNearestRankP95(gate.HealthyLatencyInputs); got != wantP95 {
		t.Fatalf("gate %q p95 = %s, want nearest-rank p95 %s from every healthy input", want.name, got, wantP95)
	}
	if gate.Max != gate.HealthyLatencyInputs[len(gate.HealthyLatencyInputs)-1] {
		t.Fatalf("gate %q max = %s, want largest healthy latency %s", want.name, gate.Max, gate.HealthyLatencyInputs[len(gate.HealthyLatencyInputs)-1])
	}
	if gate.P95 > want.p95Limit {
		t.Fatalf("gate %q p95 = %s, exceeds documented limit %s", want.name, gate.P95, want.p95Limit)
	}
}

func requireUCISLOReasons(t *testing.T, gateName, outcome string, count int, reported, observed []string) {
	t.Helper()

	if count == 0 {
		if len(reported) != 0 {
			t.Fatalf("gate %q reports %s reasons without %s samples: %#v", gateName, outcome, outcome, reported)
		}
		return
	}
	if len(reported) == 0 {
		t.Fatalf("gate %q omitted %s reasons", gateName, outcome)
	}
	for _, reason := range observed {
		if !slices.Contains(reported, reason) {
			t.Fatalf("gate %q omitted %s reason %q", gateName, outcome, reason)
		}
	}
}

func requireUCISLOViolationCodes(t *testing.T, violations []uci.UCISLOViolation, wantCodes ...string) {
	t.Helper()

	codes := make(map[string]struct{}, len(violations))
	for _, violation := range violations {
		codes[violation.Code] = struct{}{}
	}
	for _, want := range wantCodes {
		if _, found := codes[want]; !found {
			t.Fatalf("SLO validation violations = %#v, missing %q", violations, want)
		}
	}
}

func uciNearestRankP95(sorted []time.Duration) time.Duration {
	return sorted[(95*len(sorted)+99)/100-1]
}
