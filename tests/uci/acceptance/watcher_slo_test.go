package acceptance

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/uci"
)

const (
	uciWatcherSLOSchemaVersion                   = "engram.uci-watcher-slo/v2"
	uciWatcherSLOMinimumHealthyWarmBatches       = 100
	uciWatcherSLOMaximumChangedFiles             = 20
	uciWatcherSLOMaximumChangedBytes       int64 = 1 << 20
	uciWatcherSLOStructuralFTSLimit              = 2 * time.Second
	uciWatcherSLOEmbeddingReadinessLimit         = 10 * time.Second
)

type uciWatcherSLOScope struct {
	sourceID   string
	checkoutID string
	profileID  string
	spaceID    string
	hasSpace   bool
}

func TestUCIWatcherSLO(t *testing.T) {
	if os.Getenv(uciWatcherSLOInputEnv) == "" {
		t.Skip("UCI watcher SLO requires externally recorded evidence at ENGRAM_UCI_WATCHER_SLO_INPUT")
	}

	// T064 reads one strict externally recorded exact-candidate input. It does
	// not run a benchmark, infer identity from this process, or manufacture timings.
	report, err := RunUCIWatcherSLO(t.Context())
	if err != nil {
		t.Fatalf("run externally recorded UCI watcher SLO input: %v", err)
	}

	if report.SchemaVersion != uciWatcherSLOSchemaVersion {
		t.Fatalf("watcher SLO schema version = %q, want %q", report.SchemaVersion, uciWatcherSLOSchemaVersion)
	}
	requireUCIWatcherSLOIdentity(t, report.Candidate, report.Environment, report.Profile)

	providerHealthy := report.Environment.Provider.Status == "healthy"
	if !providerHealthy {
		t.Fatalf("watcher SLO provider status = %q, cannot satisfy the healthy acceptance profile", report.Environment.Provider.Status)
	}
	if report.Profile.ChangedFileCount <= 0 || report.Profile.ChangedFileCount > uciWatcherSLOMaximumChangedFiles {
		t.Fatalf("watcher SLO profile changed files = %d, want 1 through %d", report.Profile.ChangedFileCount, uciWatcherSLOMaximumChangedFiles)
	}
	if report.Profile.ChangedBytes <= 0 || report.Profile.ChangedBytes > uciWatcherSLOMaximumChangedBytes {
		t.Fatalf("watcher SLO profile changed bytes = %d, want 1 through %d", report.Profile.ChangedBytes, uciWatcherSLOMaximumChangedBytes)
	}
	if report.RetainedBatchCount != len(report.Batches) {
		t.Fatalf("watcher SLO retained batch count = %d, raw retained batches = %d", report.RetainedBatchCount, len(report.Batches))
	}
	if len(report.Batches) == 0 {
		t.Fatal("watcher SLO has a zero retained-batch denominator")
	}

	seenBatchIDs := make(map[string]struct{}, len(report.Batches))
	structuralLatencies := make([]time.Duration, 0, len(report.Batches))
	embeddingLatencies := make([]time.Duration, 0, len(report.Batches))
	healthyWarmBatches := 0
	coldBatches := 0
	degradedBatches := 0
	unavailableBatches := 0
	failedBatches := 0

	var expectedAScope, expectedBScope uciWatcherSLOScope
	haveScopes := false
	for index, batch := range report.Batches {
		if strings.TrimSpace(batch.ID) == "" {
			t.Fatalf("watcher SLO batch %d omitted its retained ID", index)
		}
		if _, duplicate := seenBatchIDs[batch.ID]; duplicate {
			t.Fatalf("watcher SLO retained duplicate batch ID %q", batch.ID)
		}
		seenBatchIDs[batch.ID] = struct{}{}

		if !reflect.DeepEqual(batch.Identity.Candidate, report.Candidate) {
			t.Fatalf("watcher SLO batch %q mixed candidate identity: %#v", batch.ID, batch.Identity.Candidate)
		}
		if !reflect.DeepEqual(batch.Identity.Environment, report.Environment) {
			t.Fatalf("watcher SLO batch %q mixed host, database, corpus, or provider identity: %#v", batch.ID, batch.Identity.Environment)
		}
		if batch.ProfileID != report.Profile.ID {
			t.Fatalf("watcher SLO batch %q profile = %q, report profile = %q", batch.ID, batch.ProfileID, report.Profile.ID)
		}
		if strings.TrimSpace(batch.ResultStatus) == "" || strings.TrimSpace(batch.Coverage) == "" {
			t.Fatalf("watcher SLO batch %q omitted result status or coverage", batch.ID)
		}
		if batch.ChangedFileCount <= 0 || batch.ChangedFileCount > uciWatcherSLOMaximumChangedFiles {
			t.Fatalf("watcher SLO batch %q changed files = %d, want 1 through %d", batch.ID, batch.ChangedFileCount, uciWatcherSLOMaximumChangedFiles)
		}
		if batch.ChangedBytes <= 0 || batch.ChangedBytes > uciWatcherSLOMaximumChangedBytes {
			t.Fatalf("watcher SLO batch %q changed bytes = %d, want 1 through %d", batch.ID, batch.ChangedBytes, uciWatcherSLOMaximumChangedBytes)
		}

		aBeforeScope := requireUCIWatcherSLOView(t, batch.ID+" A before", batch.ABefore)
		aAfterScope := requireUCIWatcherSLOView(t, batch.ID+" A after", batch.AAfter)
		bBeforeScope := requireUCIWatcherSLOView(t, batch.ID+" B before", batch.BBefore)
		bAfterScope := requireUCIWatcherSLOView(t, batch.ID+" B after", batch.BAfter)
		requireUCIWatcherSLOSameScope(t, batch.ID+" A", aBeforeScope, aAfterScope)
		requireUCIWatcherSLOSameScope(t, batch.ID+" B", bBeforeScope, bAfterScope)
		if !haveScopes {
			expectedAScope, expectedBScope = aAfterScope, bAfterScope
			haveScopes = true
			if expectedAScope.checkoutID == expectedBScope.checkoutID {
				t.Fatalf("watcher SLO batch %q does not distinguish A and B checkouts", batch.ID)
			}
		} else {
			requireUCIWatcherSLOSameScope(t, batch.ID+" A", expectedAScope, aAfterScope)
			requireUCIWatcherSLOSameScope(t, batch.ID+" B", expectedBScope, bAfterScope)
		}
		if !reflect.DeepEqual(batch.BBefore, batch.BAfter) {
			t.Fatalf("watcher SLO batch %q mutated unaffected worktree B", batch.ID)
		}
		if batch.ABeforeOrigin != UCIWatcherSLOOriginInstalledStatusAndView || batch.AAfterOrigin != UCIWatcherSLOOriginInstalledStatusAndView || batch.BBeforeOrigin != UCIWatcherSLOOriginInstalledStatusAndView || batch.BAfterOrigin != UCIWatcherSLOOriginInstalledStatusAndView {
			t.Fatalf("watcher SLO batch %q used an unverified A/B View origin", batch.ID)
		}

		warm := false
		switch batch.Warmth {
		case "warm":
			warm = true
		case "cold":
			coldBatches++
		default:
			t.Fatalf("watcher SLO batch %q warmth = %q, want separately retained warm or cold observation", batch.ID, batch.Warmth)
		}

		healthy := false
		switch batch.Outcome {
		case "healthy":
			healthy = true
			if batch.ScanOutcome != uci.IndexScanComplete {
				t.Fatalf("watcher SLO batch %q counted %q scan as healthy", batch.ID, batch.ScanOutcome)
			}
			if uciWatcherSLOResultIsDegraded(batch.ResultStatus, batch.Coverage) {
				t.Fatalf("watcher SLO batch %q counted degraded result state %q/%q as healthy", batch.ID, batch.ResultStatus, batch.Coverage)
			}
			if batch.Scan.Origin != UCIWatcherSLOOriginUnknownNotMeasured || batch.Scan.Measured || batch.LocalACK.Origin != UCIWatcherSLOOriginUnknownNotMeasured || batch.LocalACK.Measured {
				t.Fatalf("watcher SLO batch %q substituted an unobserved scan or local-ACK boundary", batch.ID)
			}
			if batch.StructuralFTS.Origin != UCIWatcherSLOOriginInstalledClientSearch || !batch.StructuralFTS.Measured || batch.StructuralFTS.Latency <= 0 {
				t.Fatalf("watcher SLO batch %q used a synthetic/default structural/FTS timing: %#v", batch.ID, batch.StructuralFTS)
			}
			if providerHealthy && (batch.EmbeddingReadiness.Origin != UCIWatcherSLOOriginInstalledEmbeddingStatus || !batch.EmbeddingReadiness.Measured || batch.EmbeddingReadiness.Latency <= 0 || batch.EmbeddingCounters.Origin != UCIWatcherSLOOriginInstalledEmbeddingStatus || !batch.EmbeddingCounters.Measured) {
				t.Fatalf("watcher SLO batch %q used an unverified embedding observation: readiness=%#v counters=%#v", batch.ID, batch.EmbeddingReadiness, batch.EmbeddingCounters)
			}
		case "degraded":
			degradedBatches++
		case "unavailable":
			unavailableBatches++
		case "failed":
			failedBatches++
		default:
			t.Fatalf("watcher SLO batch %q has unsupported outcome %q", batch.ID, batch.Outcome)
		}
		if !healthy && strings.TrimSpace(batch.Reason) == "" {
			t.Fatalf("watcher SLO nonhealthy batch %q omitted its reason", batch.ID)
		}

		if healthy {
			if batch.ABefore.Context.ViewID == batch.AAfter.Context.ViewID ||
				batch.ABefore.Context.Generation >= batch.AAfter.Context.Generation ||
				batch.ABefore.ManifestDigest == batch.AAfter.ManifestDigest {
				t.Fatalf("watcher SLO batch %q did not publish a new coherent A view", batch.ID)
			}
		} else if batch.ScanOutcome != uci.IndexScanComplete && !reflect.DeepEqual(batch.ABefore, batch.AAfter) {
			t.Fatalf("watcher SLO failed or incomplete batch %q changed A instead of retaining the prior coherent view", batch.ID)
		}

		if warm && healthy {
			healthyWarmBatches++
			structuralLatencies = append(structuralLatencies, batch.StructuralFTS.Latency)
			if providerHealthy {
				embeddingLatencies = append(embeddingLatencies, batch.EmbeddingReadiness.Latency)
			}
		}
	}

	if coldBatches == 0 {
		t.Fatal("watcher SLO omitted separately labelled cold observations")
	}
	if healthyWarmBatches == 0 {
		t.Fatal("watcher SLO has a zero healthy warm-batch denominator")
	}
	if healthyWarmBatches < uciWatcherSLOMinimumHealthyWarmBatches {
		t.Fatalf("watcher SLO healthy warm batches = %d, want at least %d", healthyWarmBatches, uciWatcherSLOMinimumHealthyWarmBatches)
	}
	if report.HealthyWarmBatchCount != healthyWarmBatches {
		t.Fatalf("watcher SLO reported healthy warm batches = %d, raw healthy warm batches = %d", report.HealthyWarmBatchCount, healthyWarmBatches)
	}
	if report.ColdBatchCount != coldBatches {
		t.Fatalf("watcher SLO reported cold batches = %d, raw cold batches = %d", report.ColdBatchCount, coldBatches)
	}
	if report.DegradedBatchCount != degradedBatches || report.UnavailableBatchCount != unavailableBatches || report.FailedBatchCount != failedBatches {
		t.Fatalf("watcher SLO outcome counts = degraded %d unavailable %d failed %d, raw = degraded %d unavailable %d failed %d", report.DegradedBatchCount, report.UnavailableBatchCount, report.FailedBatchCount, degradedBatches, unavailableBatches, failedBatches)
	}

	sort.Slice(structuralLatencies, func(i, j int) bool { return structuralLatencies[i] < structuralLatencies[j] })
	structuralP95 := uciWatcherSLOP95(structuralLatencies)
	if report.StructuralFTSP95 != structuralP95 {
		t.Fatalf("watcher SLO structural/FTS p95 = %s, want nearest-rank p95 %s from every healthy warm batch", report.StructuralFTSP95, structuralP95)
	}
	if structuralP95 > uciWatcherSLOStructuralFTSLimit {
		t.Fatalf("watcher SLO structural/FTS p95 = %s, exceeds %s", structuralP95, uciWatcherSLOStructuralFTSLimit)
	}

	if providerHealthy {
		if len(embeddingLatencies) == 0 {
			t.Fatal("watcher SLO has a zero healthy embedding-readiness denominator while provider is healthy")
		}
		if len(embeddingLatencies) < uciWatcherSLOMinimumHealthyWarmBatches {
			t.Fatalf("watcher SLO healthy embedding-readiness batches = %d, want at least %d while provider is healthy", len(embeddingLatencies), uciWatcherSLOMinimumHealthyWarmBatches)
		}
		if report.EmbeddingHealthyWarmBatchCount != len(embeddingLatencies) {
			t.Fatalf("watcher SLO reported healthy embedding-readiness batches = %d, raw = %d", report.EmbeddingHealthyWarmBatchCount, len(embeddingLatencies))
		}
		sort.Slice(embeddingLatencies, func(i, j int) bool { return embeddingLatencies[i] < embeddingLatencies[j] })
		embeddingP95 := uciWatcherSLOP95(embeddingLatencies)
		if report.EmbeddingReadinessP95 != embeddingP95 {
			t.Fatalf("watcher SLO embedding-readiness p95 = %s, want nearest-rank p95 %s from every healthy provider batch", report.EmbeddingReadinessP95, embeddingP95)
		}
		if embeddingP95 > uciWatcherSLOEmbeddingReadinessLimit {
			t.Fatalf("watcher SLO embedding-readiness p95 = %s, exceeds %s", embeddingP95, uciWatcherSLOEmbeddingReadinessLimit)
		}
	}

	if len(report.UnchangedInputCounters) == 0 {
		t.Fatal("watcher SLO omitted unchanged-input no-reembedding counters")
	}
	for index, counter := range report.UnchangedInputCounters {
		if !counter.Unchanged || strings.TrimSpace(counter.InputDigest) == "" {
			t.Fatalf("watcher SLO unchanged-input counter %d is not bound to an unchanged input digest: %#v", index, counter)
		}
		if !reflect.DeepEqual(counter.Identity.Candidate, report.Candidate) || !reflect.DeepEqual(counter.Identity.Environment, report.Environment) {
			t.Fatalf("watcher SLO unchanged-input counter %d mixed candidate or environment identity: %#v", index, counter.Identity)
		}
		counterScope := requireUCIWatcherSLOContext(t, "unchanged-input counter", counter.Context)
		requireUCIWatcherSLOSameScope(t, "unchanged-input counter", expectedAScope, counterScope)
		if counter.EmbeddingCountersOrigin != UCIWatcherSLOOriginInstalledEmbeddingStatus || !counter.EmbeddingCountersMeasured || counter.EmbeddingCountersBefore < 0 || counter.EmbeddingCountersAfter < counter.EmbeddingCountersBefore {
			t.Fatalf("watcher SLO unchanged-input counter %d has invalid embedding counter bounds or origin: %#v", index, counter)
		}
		if counter.EmbeddingCountersAfter != counter.EmbeddingCountersBefore || counter.ReembeddedCandidates != 0 {
			t.Fatalf("watcher SLO unchanged-input counter %d re-embedded unchanged input: before %d after %d candidates %d", index, counter.EmbeddingCountersBefore, counter.EmbeddingCountersAfter, counter.ReembeddedCandidates)
		}
	}
}

func requireUCIWatcherSLOIdentity(t *testing.T, candidate uci.UCISLOCandidate, environment uci.UCISLOEnvironment, profile uci.UCISLOProfile) {
	t.Helper()

	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "candidate branch", value: candidate.Branch},
		{name: "candidate commit", value: candidate.Commit},
		{name: "candidate tree", value: candidate.Tree},
		{name: "host identity", value: environment.Host.ID},
		{name: "database identity", value: environment.Database.ID},
		{name: "database version", value: environment.Database.Version},
		{name: "corpus identity", value: environment.Corpus.ID},
		{name: "corpus manifest", value: environment.Corpus.ManifestDigest},
		{name: "provider identity", value: environment.Provider.ID},
		{name: "provider model", value: environment.Provider.Model},
		{name: "provider status", value: environment.Provider.Status},
		{name: "profile identity", value: profile.ID},
	} {
		if strings.TrimSpace(field.value) == "" {
			t.Fatalf("watcher SLO report omitted %s", field.name)
		}
	}
	if len(candidate.ArtifactDigests) == 0 {
		t.Fatal("watcher SLO report omitted installed candidate artifact digests")
	}
	for _, artifact := range candidate.ArtifactDigests {
		if strings.TrimSpace(artifact.Name) == "" || strings.TrimSpace(artifact.Digest) == "" {
			t.Fatalf("watcher SLO report has incomplete installed artifact identity: %#v", artifact)
		}
	}
	if environment.Database.DataSizeBytes <= 0 {
		t.Fatal("watcher SLO report omitted measured database data size")
	}
}

func requireUCIWatcherSLOView(t *testing.T, name string, view uci.IndexPublishedView) uciWatcherSLOScope {
	t.Helper()

	if strings.TrimSpace(view.BuildID) == "" || strings.TrimSpace(string(view.ManifestDigest)) == "" || view.PublishedAt.IsZero() || view.AcceptedFSSeq < 0 {
		t.Fatalf("watcher SLO %s view is not a complete durable publication: %#v", name, view)
	}
	return requireUCIWatcherSLOContext(t, name, view.Context)
}

func requireUCIWatcherSLOContext(t *testing.T, name string, context uci.ContextRef) uciWatcherSLOScope {
	t.Helper()

	scope := uciWatcherSLOScope{
		sourceID:   strings.TrimSpace(context.SourceID),
		checkoutID: strings.TrimSpace(context.CheckoutID),
		profileID:  strings.TrimSpace(context.AnalysisProfileID),
		hasSpace:   context.SpaceID != nil,
	}
	if strings.TrimSpace(context.ViewID) == "" || context.Generation <= 0 || scope.sourceID == "" || scope.checkoutID == "" || scope.profileID == "" {
		t.Fatalf("watcher SLO %s context is incomplete: %#v", name, context)
	}
	if context.SpaceID != nil {
		scope.spaceID = strings.TrimSpace(*context.SpaceID)
		if scope.spaceID == "" {
			t.Fatalf("watcher SLO %s context has an empty space identity", name)
		}
	}
	return scope
}

func requireUCIWatcherSLOSameScope(t *testing.T, name string, want, got uciWatcherSLOScope) {
	t.Helper()

	if got != want {
		t.Fatalf("watcher SLO %s context scope = %#v, want %#v", name, got, want)
	}
}

func uciWatcherSLOResultIsDegraded(resultStatus, coverage string) bool {
	for _, value := range []string{resultStatus, coverage} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "degraded", "partial", "stale", "offline", "unsupported", "unavailable", "failed", "error", "unknown":
			return true
		}
	}
	return false
}

func uciWatcherSLOP95(sorted []time.Duration) time.Duration {
	return sorted[(95*len(sorted)+99)/100-1]
}
