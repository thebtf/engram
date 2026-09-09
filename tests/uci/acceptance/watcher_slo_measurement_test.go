package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auth"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
)

const uciWatcherSLORecordPathEnv = "ENGRAM_UCI_WATCHER_SLO_RECORD_PATH"

// TestUCIRecordWatcherSLOMeasurement records actual update-stage timings,
// provider counters, and A/B View transitions from the exact candidate. The
// accounting test remains a pure reader of the resulting external evidence.
func TestUCIRecordWatcherSLOMeasurement(t *testing.T) {
	recordPath := strings.TrimSpace(os.Getenv(uciWatcherSLORecordPathEnv))
	if recordPath == "" {
		t.Skipf("%s not set; skipping UCI watcher SLO measurement recording", uciWatcherSLORecordPathEnv)
	}
	if !filepath.IsAbs(recordPath) {
		t.Fatal("UCI watcher SLO record path must be absolute")
	}
	if strings.TrimSpace(os.Getenv("DATABASE_DSN")) == "" {
		t.Skip("DATABASE_DSN not set; UCI watcher SLO measurement recording requires a caller-supplied disposable PostgreSQL database")
	}

	candidate, err := uciSLORecordCandidate(t)
	if err != nil {
		t.Fatalf("build exact watcher SLO candidate: %v", err)
	}
	fixture := newUCIRetrievalSliceFixture(t)
	activeCheckouts, inactiveRegistrations, err := uciSLORegisterCheckoutCoverage(fixture)
	if err != nil {
		t.Fatalf("register watcher SLO checkout coverage: %v", err)
	}
	corpusA, err := newUCISLORecordCorpus(t, fixture)
	if err != nil {
		t.Fatalf("publish watcher SLO A corpus: %v", err)
	}
	fixtureB, err := uciWatcherSLOSecondFixture(fixture)
	if err != nil {
		t.Fatalf("bind watcher SLO B checkout: %v", err)
	}
	corpusB, err := newUCISLORecordCorpus(t, fixtureB)
	if err != nil {
		t.Fatalf("publish watcher SLO B corpus: %v", err)
	}

	localRTT, localRTTErr := uciSLOMeasureLocalProviderRTT(fixture)
	databaseVersion, databaseSize, err := uciSLODatabaseMeasurement(fixture)
	if err != nil {
		t.Fatalf("measure watcher SLO database: %v", err)
	}
	textFiles, linesOfCode, err := uciSLOCorpusCounts(fixture)
	if err != nil {
		t.Fatalf("measure watcher SLO corpus: %v", err)
	}
	providerCalls := uciSLOProviderCalls(fixture.provider)
	providerStatus := "healthy"
	if localRTTErr != nil || providerCalls.failed != 0 {
		providerStatus = "unavailable"
	}
	environment := uci.UCISLOEnvironment{
		Host:     uci.UCISLOHost{ID: "local-uci-watcher-slo-recorder"},
		Database: uci.UCISLODatabase{ID: "postgresql-isolated-schema", Version: databaseVersion, DataSizeBytes: databaseSize},
		Corpus:   uci.UCISLOCorpus{ID: "uci-watcher-slo-bounded-corpus-v1", ManifestDigest: string(corpusA.manifestDigest)},
		Provider: uci.UCISLOProvider{ID: uciSLORecordProviderID, Model: uciSLORecordProviderModel, Status: providerStatus},
	}
	profile := uci.UCISLOProfile{
		ID:                        fixture.profile.ProfileID,
		TextFileCount:             textFiles,
		LinesOfCode:               linesOfCode,
		ActiveWorktreeCount:       activeCheckouts,
		InactiveRegistrationCount: inactiveRegistrations,
		LANRTT:                    localRTT,
		ChangedFileCount:          1,
		ChangedBytes:              1,
	}
	identity := uci.UCISLOSampleIdentity{Candidate: candidate, Environment: environment}

	bView := corpusB.current
	batches := make([]UCIWatcherSLOBatch, 0, uciWatcherSLOMinimumHealthyWarm+1)
	cold, err := uciWatcherSLOMeasureUpdate(corpusA, bView, identity, profile.ID, 1, "cold")
	if err != nil {
		t.Fatalf("record cold watcher update: %v", err)
	}
	batches = append(batches, cold)
	for sequence := 2; len(batches)-1 < uciWatcherSLOMinimumHealthyWarm; sequence++ {
		batch, measureErr := uciWatcherSLOMeasureUpdate(corpusA, bView, identity, profile.ID, sequence, "warm")
		if measureErr != nil {
			t.Fatalf("record warm watcher update %d: %v", sequence, measureErr)
		}
		batches = append(batches, batch)
	}
	profile.ChangedBytes = corpusA.maxChanged
	identity.Environment = environment
	for index := range batches {
		batches[index].Identity = identity
	}

	unchangedBefore := uciSLOProviderCalls(fixture.provider)
	authorized, err := corpusA.authorize(fixture.callerContext)
	if err != nil {
		t.Fatalf("authorize unchanged watcher input: %v", err)
	}
	if err := uciSLOEnsurePathEmbedding(fixture.callerContext, fixture, authorized, "docs/slo_update.md"); err != nil {
		t.Fatalf("verify unchanged watcher embedding: %v", err)
	}
	unchangedAfter := uciSLOProviderCalls(fixture.provider)
	input := UCIWatcherSLOInput{
		SchemaVersion: UCIWatcherSLOSchemaVersion,
		Candidate:     candidate,
		Environment:   environment,
		Profile:       profile,
		Batches:       batches,
		UnchangedInputCounters: []UCIWatcherSLOUnchangedInputCounter{{
			Identity:              identity,
			Context:               corpusA.current.Context,
			InputDigest:           string(corpusA.current.ManifestDigest),
			Unchanged:             true,
			ProviderCallsMeasured: true,
			ProviderCallsBefore:   int64(unchangedBefore.requests),
			ProviderCallsAfter:    int64(unchangedAfter.requests),
			ReembeddedCandidates:  0,
		}},
	}
	report, err := CalculateUCIWatcherSLOReport(input)
	if err != nil {
		t.Fatalf("calculate recorded watcher SLO report: %v", err)
	}
	if !report.Passed {
		t.Fatal("recorded watcher SLO report is not accepted")
	}
	if err := uciWatcherSLOWriteInputAtomically(recordPath, input); err != nil {
		t.Fatalf("write watcher SLO input: %v", err)
	}
}

func uciWatcherSLOSecondFixture(fixture *uciRetrievalSliceFixture) (*uciRetrievalSliceFixture, error) {
	var checkout gormstore.UCICheckout
	result := fixture.store.GetDB().WithContext(fixture.context).
		Where("source_id = ? AND checkout_id <> ? AND state IN ?", fixture.source.SourceID, fixture.checkout.CheckoutID, []gormstore.UCICheckoutState{gormstore.UCICheckoutRegistered, gormstore.UCICheckoutWatching, gormstore.UCICheckoutCatchingUp}).
		Order("checkout_id ASC").First(&checkout)
	if result.Error != nil {
		return nil, result.Error
	}
	clone := *fixture
	clone.checkout = &checkout
	clone.clientSessionID = "uci-watcher-slo-b-" + uuid.NewString()
	clone.indexCaller.Principal = checkout.OwnerPrincipal
	callerIdentity := auth.ClientWithPrincipal("read-write", checkout.WorkstationID, checkout.OwnerPrincipal, auth.PrincipalKindAgent)
	clone.callerContext = auth.WithIdentity(mcp.ContextWithSession(clone.context, clone.clientSessionID), callerIdentity)
	clone.indexCaller.OwnerInstance = "uci-watcher-slo-b-" + uuid.NewString()
	clone.privateLocator = "fixture://uci-watcher-slo/b"
	return &clone, nil
}

func uciWatcherSLOMeasureUpdate(corpus *uciSLORecordCorpus, bView uci.IndexPublishedView, identity uci.UCISLOSampleIdentity, profileID string, sequence int, warmth string) (UCIWatcherSLOBatch, error) {
	started := time.Now().UTC()
	aBefore := corpus.current
	changedBytes := uciSLOChangedBytes(corpus.markdownBytes, uciSLOMarkdownSource(sequence))
	providerBefore := uciSLOProviderCalls(corpus.fixture.provider)

	structuralCtx, cancelStructural := context.WithTimeout(corpus.fixture.callerContext, uciSLORecordStructuralWaitBound)
	after, err := corpus.publishUpdate(structuralCtx, sequence)
	scanCompleted := time.Now().UTC()
	if err != nil {
		cancelStructural()
		return UCIWatcherSLOBatch{}, err
	}
	corpus.current = after
	authorized, err := corpus.authorize(structuralCtx)
	if err != nil {
		cancelStructural()
		return UCIWatcherSLOBatch{}, err
	}
	response, err := corpus.fixture.queryService.Query(structuralCtx, authorized, uci.QuerySpec{
		ClientSessionID: corpus.fixture.clientSessionID,
		Mode:            uci.QueryModeFTS,
		Text:            uciSLOUpdateMarker(sequence),
		Order:           uci.QueryOrderRelevance,
		Limit:           10,
	})
	structuralCompleted := time.Now().UTC()
	cancelStructural()
	if err != nil || response.Response.Status != uci.QueryStatusOK {
		return UCIWatcherSLOBatch{}, fmt.Errorf("watcher structural query status=%s error=%v", response.Response.Status, err)
	}

	embeddingCtx, cancelEmbedding := context.WithTimeout(corpus.fixture.callerContext, uciSLORecordEmbeddingWaitBound)
	if err := uciSLOEnsurePathEmbedding(embeddingCtx, corpus.fixture, authorized, "docs/slo_update.md"); err != nil {
		cancelEmbedding()
		return UCIWatcherSLOBatch{}, err
	}
	embeddingCompleted := time.Now().UTC()
	cancelEmbedding()
	providerAfter := uciSLOProviderCalls(corpus.fixture.provider)
	ackCompleted := time.Now().UTC()

	return UCIWatcherSLOBatch{
		ID:                   fmt.Sprintf("watcher-%s-%03d", warmth, sequence),
		Identity:             identity,
		ProfileID:            profileID,
		ObservedFSSeq:        after.AcceptedFSSeq,
		ChangedFileCount:     1,
		ChangedBytes:         changedBytes,
		ScanOutcome:          uci.IndexScanComplete,
		ResultStatus:         string(response.Response.Status),
		Coverage:             string(response.Response.Coverage.Structural),
		Outcome:              "healthy",
		Warmth:               warmth,
		Scan:                 uciWatcherSLOTiming(started, scanCompleted),
		StructuralFTS:        uciWatcherSLOTiming(started, structuralCompleted),
		EmbeddingReadiness:   uciWatcherSLOTiming(started, embeddingCompleted),
		LocalACK:             uciWatcherSLOTiming(started, ackCompleted),
		ProviderCalls:        UCIWatcherSLOProviderCallCounters{Measured: true, Before: int64(providerBefore.requests), After: int64(providerAfter.requests)},
		ReembeddedCandidates: providerAfter.corpus - providerBefore.corpus,
		ABefore:              aBefore,
		AAfter:               after,
		BBefore:              bView,
		BAfter:               bView,
	}, nil
}

func uciWatcherSLOTiming(started, completed time.Time) UCIWatcherSLOTiming {
	latency := completed.Sub(started)
	return UCIWatcherSLOTiming{Measured: true, StartedAt: started, CompletedAt: completed, Latency: latency}
}

func uciWatcherSLOInputWireFor(input UCIWatcherSLOInput) uciWatcherSLOInputWire {
	wire := uciWatcherSLOInputWire{
		SchemaVersion: input.SchemaVersion,
		Candidate:     uciWatcherSLOCandidateWireFor(input.Candidate),
		Environment:   uciWatcherSLOEnvironmentWireFor(input.Environment),
		Profile: uciWatcherSLOProfileWire{
			ID: input.Profile.ID, TextFileCount: input.Profile.TextFileCount, LinesOfCode: input.Profile.LinesOfCode,
			ActiveWorktreeCount: input.Profile.ActiveWorktreeCount, InactiveRegistrationCount: input.Profile.InactiveRegistrationCount,
			LANRTT: input.Profile.LANRTT, ChangedFileCount: input.Profile.ChangedFileCount, ChangedBytes: input.Profile.ChangedBytes,
		},
		Batches:                make([]uciWatcherSLOBatchWire, 0, len(input.Batches)),
		UnchangedInputCounters: make([]uciWatcherSLOUnchangedCounterWire, 0, len(input.UnchangedInputCounters)),
	}
	for _, batch := range input.Batches {
		wire.Batches = append(wire.Batches, uciWatcherSLOBatchWire{
			ID: batch.ID, Identity: uciWatcherSLOIdentityWireFor(batch.Identity), ProfileID: batch.ProfileID,
			ObservedFSSeq: batch.ObservedFSSeq, ChangedFileCount: batch.ChangedFileCount, ChangedBytes: batch.ChangedBytes,
			ScanOutcome: batch.ScanOutcome, ResultStatus: batch.ResultStatus, Coverage: batch.Coverage, Outcome: batch.Outcome,
			Reason: batch.Reason, Warmth: batch.Warmth, Scan: batch.Scan, StructuralFTS: batch.StructuralFTS,
			EmbeddingReadiness: batch.EmbeddingReadiness, LocalACK: batch.LocalACK, ProviderCalls: batch.ProviderCalls,
			ReembeddedCandidates: batch.ReembeddedCandidates, ABefore: uciWatcherSLOViewWireFor(batch.ABefore),
			AAfter: uciWatcherSLOViewWireFor(batch.AAfter), BBefore: uciWatcherSLOViewWireFor(batch.BBefore), BAfter: uciWatcherSLOViewWireFor(batch.BAfter),
		})
	}
	for _, counter := range input.UnchangedInputCounters {
		wire.UnchangedInputCounters = append(wire.UnchangedInputCounters, uciWatcherSLOUnchangedCounterWire{
			Identity: uciWatcherSLOIdentityWireFor(counter.Identity), Context: uciWatcherSLOContextWireFor(counter.Context),
			InputDigest: counter.InputDigest, Unchanged: counter.Unchanged, ProviderCallsMeasured: counter.ProviderCallsMeasured,
			ProviderCallsBefore: counter.ProviderCallsBefore, ProviderCallsAfter: counter.ProviderCallsAfter, ReembeddedCandidates: counter.ReembeddedCandidates,
		})
	}
	return wire
}

func uciWatcherSLOCandidateWireFor(candidate uci.UCISLOCandidate) uciWatcherSLOCandidateWire {
	wire := uciWatcherSLOCandidateWire{Branch: candidate.Branch, Commit: candidate.Commit, Tree: candidate.Tree, ArtifactDigests: make([]uciWatcherSLOArtifactDigestWire, 0, len(candidate.ArtifactDigests))}
	for _, artifact := range candidate.ArtifactDigests {
		wire.ArtifactDigests = append(wire.ArtifactDigests, uciWatcherSLOArtifactDigestWire{Name: artifact.Name, Digest: artifact.Digest})
	}
	return wire
}

func uciWatcherSLOEnvironmentWireFor(environment uci.UCISLOEnvironment) uciWatcherSLOEnvironmentWire {
	return uciWatcherSLOEnvironmentWire{
		Host:     uciWatcherSLOHostWire{ID: environment.Host.ID},
		Database: uciWatcherSLODatabaseWire{ID: environment.Database.ID, Version: environment.Database.Version, DataSizeBytes: environment.Database.DataSizeBytes},
		Corpus:   uciWatcherSLOCorpusWire{ID: environment.Corpus.ID, ManifestDigest: environment.Corpus.ManifestDigest},
		Provider: uciWatcherSLOProviderWire{ID: environment.Provider.ID, Model: environment.Provider.Model, Status: environment.Provider.Status},
	}
}

func uciWatcherSLOIdentityWireFor(identity uci.UCISLOSampleIdentity) uciWatcherSLOIdentityWire {
	return uciWatcherSLOIdentityWire{Candidate: uciWatcherSLOCandidateWireFor(identity.Candidate), Environment: uciWatcherSLOEnvironmentWireFor(identity.Environment)}
}

func uciWatcherSLOContextWireFor(context uci.ContextRef) uciWatcherSLOContextWire {
	return uciWatcherSLOContextWire{SpaceID: context.SpaceID, SourceID: context.SourceID, CheckoutID: context.CheckoutID, ViewID: context.ViewID, AnalysisProfileID: context.AnalysisProfileID, Generation: context.Generation}
}

func uciWatcherSLOViewWireFor(view UCIWatcherSLOView) uciWatcherSLOViewWire {
	return uciWatcherSLOViewWire{BuildID: view.BuildID, Context: uciWatcherSLOContextWireFor(view.Context), ManifestDigest: string(view.ManifestDigest), AcceptedFSSeq: view.AcceptedFSSeq, PublishedAt: view.PublishedAt}
}

func uciWatcherSLOWriteInputAtomically(path string, input UCIWatcherSLOInput) error {
	encoded, err := json.Marshal(uciWatcherSLOInputWireFor(input))
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".uci-watcher-slo-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
