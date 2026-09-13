package uci

import (
	"fmt"
	"testing"
	"time"
)

func TestCalculateUCISLOReportAccountsCompleteHealthyPopulation(t *testing.T) {
	input := uciSLOTestMeasurementInput()
	degraded := input.Gates[0].Samples[0]
	degraded.ID = "update.structural_fts-degraded"
	degraded.Outcome = "degraded"
	degraded.ResultStatus = "partial"
	degraded.Coverage = "partial"
	degraded.Reason = "vector_catching_up"
	input.Gates[0].Samples = append(input.Gates[0].Samples, degraded)

	report, err := CalculateUCISLOReport(input)
	if err != nil {
		t.Fatalf("CalculateUCISLOReport() error = %v", err)
	}
	if violations := ValidateUCISLOReport(report); len(violations) != 0 {
		t.Fatalf("ValidateUCISLOReport() violations = %#v", violations)
	}
	if !report.Passed {
		t.Fatal("report passed = false, want true for five complete healthy gates")
	}

	var structural UCISLOGateReport
	for _, gate := range report.Gates {
		if gate.Name == "update.structural_fts" {
			structural = gate
			break
		}
	}
	if structural.Name == "" {
		t.Fatal("report omitted update.structural_fts")
	}
	if structural.SampleCount != 101 || structural.HealthySampleCount != 100 || structural.DegradedCount != 1 {
		t.Fatalf("structural accounting = samples:%d healthy:%d degraded:%d", structural.SampleCount, structural.HealthySampleCount, structural.DegradedCount)
	}
	if structural.P95 != 95*time.Millisecond || structural.Max != 100*time.Millisecond {
		t.Fatalf("structural latency = p95:%s max:%s, want 95ms/100ms", structural.P95, structural.Max)
	}
	if len(structural.HealthyLatencyInputs) != 100 {
		t.Fatalf("healthy latency inputs = %d, want 100", len(structural.HealthyLatencyInputs))
	}
	for _, latency := range structural.HealthyLatencyInputs {
		if latency <= 0 {
			t.Fatalf("healthy latency input = %s, want positive", latency)
		}
	}
}

func TestCalculateUCISLOReportRejectsDuplicateOrZeroHealthySamples(t *testing.T) {
	t.Run("duplicate sample ID", func(t *testing.T) {
		input := uciSLOTestMeasurementInput()
		input.Gates[0].Samples[1].ID = input.Gates[0].Samples[0].ID
		if _, err := CalculateUCISLOReport(input); err == nil {
			t.Fatal("CalculateUCISLOReport() accepted duplicate sample IDs")
		}
	})

	t.Run("zero healthy latency", func(t *testing.T) {
		input := uciSLOTestMeasurementInput()
		input.Gates[0].Samples[0].Latency = 0
		if _, err := CalculateUCISLOReport(input); err == nil {
			t.Fatal("CalculateUCISLOReport() accepted a zero-latency healthy sample")
		}
	})
}

func TestValidateUCISLOReportRejectsSlowHealthyTailOmittedFromPercentile(t *testing.T) {
	report, err := CalculateUCISLOReport(uciSLOTestMeasurementInput())
	if err != nil {
		t.Fatalf("CalculateUCISLOReport() error = %v", err)
	}

	var gate *UCISLOGateReport
	for index := range report.Gates {
		if report.Gates[index].Name == "update.structural_fts" {
			gate = &report.Gates[index]
			break
		}
	}
	if gate == nil {
		t.Fatal("report omitted update.structural_fts")
	}
	for index := len(gate.Samples) - 6; index < len(gate.Samples); index++ {
		gate.Samples[index].Latency = 3 * time.Second
	}

	codes := make(map[string]bool)
	for _, violation := range ValidateUCISLOReport(report) {
		codes[violation.Code] = true
	}
	for _, code := range []string{"healthy_latency_input_mismatch", "p95_mismatch", "max_mismatch"} {
		if !codes[code] {
			t.Fatalf("ValidateUCISLOReport() violations = %#v, want %q after slow healthy tail was omitted from percentile accounting", codes, code)
		}
	}
}

func uciSLOTestMeasurementInput() UCISLOMeasurementInput {
	candidate := UCISLOCandidate{
		Branch: "uci/test",
		Commit: "candidate-commit",
		Tree:   "candidate-tree",
		ArtifactDigests: []UCISLOArtifactDigest{
			{Name: "engram", Digest: "sha256:artifact"},
		},
	}
	environment := UCISLOEnvironment{
		Host:     UCISLOHost{ID: "host-a"},
		Database: UCISLODatabase{ID: "db-a", Version: "17", DataSizeBytes: 1},
		Corpus:   UCISLOCorpus{ID: "corpus-a", ManifestDigest: "sha256:manifest"},
		Provider: UCISLOProvider{ID: "provider-a", Model: "model-a", Status: "healthy"},
	}
	profile := UCISLOProfile{
		ID:                        "profile-a",
		TextFileCount:             10_000,
		LinesOfCode:               1_000_000,
		ActiveWorktreeCount:       5,
		InactiveRegistrationCount: 1,
		LANRTT:                    10 * time.Millisecond,
		ChangedFileCount:          20,
		ChangedBytes:              1 << 20,
	}
	identity := UCISLOSampleIdentity{Candidate: candidate, Environment: environment}
	definitions := []struct {
		name      string
		operation string
		waitBound time.Duration
		depth     int
		caps      map[string]int
	}{
		{name: "update.structural_fts", operation: "update", waitBound: 3 * time.Second},
		{name: "update.embedding_readiness", operation: "update", waitBound: 12 * time.Second},
		{name: "search.server_retrieval", operation: "search", waitBound: time.Second},
		{name: "search.query_embedding", operation: "search", waitBound: 3 * time.Second},
		{name: "graph.small_explain_neighbors_impact", operation: "graph", waitBound: 2 * time.Second, depth: 4, caps: map[string]int{"nodes": 32}},
	}
	gates := make([]UCISLOGateInput, 0, len(definitions))
	for _, definition := range definitions {
		samples := make([]UCISLOSample, UCISLOMinimumHealthySamples)
		for index := range samples {
			samples[index] = UCISLOSample{
				ID:           fmt.Sprintf("%s-%03d", definition.name, index),
				Identity:     identity,
				Outcome:      "healthy",
				Warmth:       "warm",
				ProfileID:    profile.ID,
				ContextID:    "context-a",
				ResultStatus: "ok",
				Coverage:     "complete",
				Waited:       time.Millisecond,
				Latency:      time.Duration(index+1) * time.Millisecond,
			}
		}
		gates = append(gates, UCISLOGateInput{
			Name:          definition.name,
			Operation:     definition.operation,
			Warmth:        "warm",
			WaitBound:     definition.waitBound,
			ProfileID:     profile.ID,
			ContextID:     "context-a",
			MaxGraphDepth: definition.depth,
			QueryCaps:     definition.caps,
			Samples:       samples,
		})
	}
	coldSample := UCISLOSample{
		ID:           "cold-index-001",
		Identity:     identity,
		Outcome:      "healthy",
		Warmth:       "cold",
		ProfileID:    profile.ID,
		ContextID:    "context-a",
		ResultStatus: "ok",
		Coverage:     "complete",
		Waited:       time.Millisecond,
		Latency:      time.Second,
	}
	return UCISLOMeasurementInput{
		SchemaVersion: UCISLOReportSchemaVersion,
		Candidate:     candidate,
		Environment:   environment,
		Profile:       profile,
		Gates:         gates,
		ColdObservations: []UCISLOColdObservation{
			{Operation: "index", Warmth: "cold", ProfileID: profile.ID, ContextID: "context-a", WaitBound: 2 * time.Second, Samples: []UCISLOSample{coldSample}},
		},
	}
}
