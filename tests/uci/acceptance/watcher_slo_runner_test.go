package acceptance

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/uci"
)

func TestWatcherSLOAccountingRejectsUnobservedOrMisoriginatedEvidence(t *testing.T) {
	t.Run("independently observed B mutation is rejected", func(t *testing.T) {
		batch, candidate, environment, profile := validWatcherSLOBatchForTest()
		batch.BAfter = watcherSLOViewForTest("44444444-4444-4444-8444-444444444445", "22222222-2222-4222-8222-222222222222", 2, 2)

		_, err := validateWatcherSLOBatch(batch, candidate, environment, profile)
		if err == nil {
			t.Fatal("independently changed B was accepted")
		}
	})

	t.Run("component publication timing cannot claim installed client search origin", func(t *testing.T) {
		batch, candidate, environment, profile := validWatcherSLOBatchForTest()
		batch.StructuralFTS.Origin = "component_direct_publication"

		_, err := validateWatcherSLOBatch(batch, candidate, environment, profile)
		if err == nil || !strings.Contains(err.Error(), "structural/FTS timing") {
			t.Fatalf("validate component-origin structural timing = %v, want origin rejection", err)
		}
	})

	t.Run("stalled watcher cannot be recorded as healthy", func(t *testing.T) {
		batch, candidate, environment, profile := validWatcherSLOBatchForTest()
		batch.StructuralFTS = UCIWatcherSLOTiming{Origin: UCIWatcherSLOOriginUnknownNotMeasured}

		_, err := validateWatcherSLOBatch(batch, candidate, environment, profile)
		if err == nil {
			t.Fatal("stalled watcher was accepted as healthy")
		}
	})
}

func TestUCIWatcherSLOEncoderRetainsMeasuredOrigins(t *testing.T) {
	batch, candidate, environment, profile := validWatcherSLOBatchForTest()
	input := UCIWatcherSLOInput{
		SchemaVersion: UCIWatcherSLOSchemaVersion,
		Candidate:     candidate,
		Environment:   environment,
		Profile:       profile,
		Batches:       []UCIWatcherSLOBatch{batch},
		UnchangedInputCounters: []UCIWatcherSLOUnchangedInputCounter{{
			Identity:                  batch.Identity,
			Context:                   batch.AAfter.Context,
			InputDigest:               string(batch.AAfter.ManifestDigest),
			Unchanged:                 true,
			EmbeddingCountersOrigin:   UCIWatcherSLOOriginInstalledEmbeddingStatus,
			EmbeddingCountersMeasured: true,
			EmbeddingCountersBefore:   2,
			EmbeddingCountersAfter:    2,
		}},
	}
	encoded, err := EncodeUCIWatcherSLOInput(input)
	if err != nil {
		t.Fatalf("encode watcher evidence: %v", err)
	}
	decoded, err := decodeUCIWatcherSLOInput(encoded)
	if err != nil {
		t.Fatalf("decode watcher evidence: %v", err)
	}
	got := decoded.Batches[0]
	if got.StructuralFTS.Origin != UCIWatcherSLOOriginInstalledClientSearch || got.EmbeddingReadiness.Origin != UCIWatcherSLOOriginInstalledEmbeddingStatus || got.ABeforeOrigin != UCIWatcherSLOOriginInstalledStatusAndView || got.BAfterOrigin != UCIWatcherSLOOriginInstalledStatusAndView || got.EmbeddingCounters.Origin != UCIWatcherSLOOriginInstalledEmbeddingStatus {
		t.Fatalf("decoded watcher evidence lost field origins: %#v", got)
	}
	if !reflect.DeepEqual(got.Scan, batch.Scan) || !reflect.DeepEqual(got.LocalACK, batch.LocalACK) {
		t.Fatalf("decoded watcher evidence invented an unknown timing: scan=%#v local_ack=%#v", got.Scan, got.LocalACK)
	}
}

func TestUCIWatcherSLODecoderRejectsComponentPublicationSchema(t *testing.T) {
	input, err := decodeUCIWatcherSLOInput([]byte(`{"schema_version":"engram.uci-component-publication/v1"}`))
	if err != nil {
		t.Fatalf("decode component publication record: %v", err)
	}
	if _, err := CalculateUCIWatcherSLOReport(input); err == nil || !strings.Contains(err.Error(), "unsupported schema") {
		t.Fatalf("account component publication record = %v, want SLO schema rejection", err)
	}
}

func validWatcherSLOBatchForTest() (UCIWatcherSLOBatch, uci.UCISLOCandidate, uci.UCISLOEnvironment, uci.UCISLOProfile) {
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	started := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	structuralDone := started.Add(5 * time.Millisecond)
	embeddingDone := started.Add(10 * time.Millisecond)
	candidate := uci.UCISLOCandidate{
		Branch: "uci/watcher-slo",
		Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Tree:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ArtifactDigests: []uci.UCISLOArtifactDigest{{
			Name:   "engram-daemon",
			Digest: digest,
		}},
	}
	environment := uci.UCISLOEnvironment{
		Host:     uci.UCISLOHost{ID: "installed-watcher-recorder"},
		Database: uci.UCISLODatabase{ID: "isolated-postgres", Version: "PostgreSQL 17", DataSizeBytes: 1},
		Corpus:   uci.UCISLOCorpus{ID: "bounded-watcher-corpus", ManifestDigest: digest},
		Provider: uci.UCISLOProvider{ID: "sha256:provider", Model: "configured-model", Status: "healthy"},
	}
	profile := uci.UCISLOProfile{
		ID:                        "33333333-3333-4333-8333-333333333333",
		TextFileCount:             1,
		LinesOfCode:               1,
		ActiveWorktreeCount:       5,
		InactiveRegistrationCount: 1,
		LANRTT:                    time.Millisecond,
		ChangedFileCount:          1,
		ChangedBytes:              1,
	}
	batch := UCIWatcherSLOBatch{
		ID:                 "watcher-warm-001",
		Identity:           uci.UCISLOSampleIdentity{Candidate: candidate, Environment: environment},
		ProfileID:          profile.ID,
		ObservedFSSeq:      2,
		ChangedFileCount:   1,
		ChangedBytes:       1,
		ScanOutcome:        uci.IndexScanComplete,
		ResultStatus:       "ok",
		Coverage:           "complete",
		Outcome:            "healthy",
		Warmth:             "warm",
		Scan:               UCIWatcherSLOTiming{Origin: UCIWatcherSLOOriginUnknownNotMeasured},
		StructuralFTS:      UCIWatcherSLOTiming{Origin: UCIWatcherSLOOriginInstalledClientSearch, Measured: true, StartedAt: started, CompletedAt: structuralDone, Latency: structuralDone.Sub(started)},
		EmbeddingReadiness: UCIWatcherSLOTiming{Origin: UCIWatcherSLOOriginInstalledEmbeddingStatus, Measured: true, StartedAt: started, CompletedAt: embeddingDone, Latency: embeddingDone.Sub(started)},
		LocalACK:           UCIWatcherSLOTiming{Origin: UCIWatcherSLOOriginUnknownNotMeasured},
		EmbeddingCounters:  UCIWatcherSLOEmbeddingCounters{Origin: UCIWatcherSLOOriginInstalledEmbeddingStatus, Measured: true, Before: 1, After: 2},
		ABefore:            watcherSLOViewForTest("11111111-1111-4111-8111-111111111111", "11111111-1111-4111-8111-111111111112", 1, 1),
		AAfter:             watcherSLOViewForTest("11111111-1111-4111-8111-111111111113", "11111111-1111-4111-8111-111111111112", 2, 2),
		BBefore:            watcherSLOViewForTest("44444444-4444-4444-8444-444444444444", "22222222-2222-4222-8222-222222222222", 1, 1),
		BAfter:             watcherSLOViewForTest("44444444-4444-4444-8444-444444444444", "22222222-2222-4222-8222-222222222222", 1, 1),
		ABeforeOrigin:      UCIWatcherSLOOriginInstalledStatusAndView,
		AAfterOrigin:       UCIWatcherSLOOriginInstalledStatusAndView,
		BBeforeOrigin:      UCIWatcherSLOOriginInstalledStatusAndView,
		BAfterOrigin:       UCIWatcherSLOOriginInstalledStatusAndView,
	}
	return batch, candidate, environment, profile
}

func watcherSLOViewForTest(buildID, checkoutID string, generation, sequence int64) UCIWatcherSLOView {
	manifestDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if sequence > 1 {
		manifestDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	}
	return UCIWatcherSLOView{
		BuildID: buildID,
		Context: uci.ContextRef{
			SourceID:          "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			CheckoutID:        checkoutID,
			ViewID:            buildID,
			AnalysisProfileID: "33333333-3333-4333-8333-333333333333",
			Generation:        generation,
		},
		ManifestDigest: uci.IndexDigest(manifestDigest),
		AcceptedFSSeq:  sequence,
		PublishedAt:    time.Date(2026, time.September, 9, 12, 0, int(sequence), 0, time.UTC),
	}
}
