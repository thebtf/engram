package acceptance

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/uci"
)

const (
	// UCIWatcherSLOSchemaVersion identifies the external watcher-update evidence schema.
	UCIWatcherSLOSchemaVersion = "engram.uci-watcher-slo/v2"

	uciWatcherSLOInputEnv             = "ENGRAM_UCI_WATCHER_SLO_INPUT"
	uciWatcherSLOMinimumHealthyWarm   = 100
	watcherSLOMaximumChangedFiles     = 20
	watcherSLOMaximumChangedBytes     = int64(1 << 20)
	uciWatcherSLOStructuralFTSTarget  = 2 * time.Second
	uciWatcherSLOEmbeddingReadyTarget = 10 * time.Second
	uciWatcherSLOMaximumTextBytes     = 256
)

// UCIWatcherSLOView is the durable UCI View shape retained by every watcher
// batch. It remains the application-owned publication type rather than a
// parallel acceptance-only View model.
type UCIWatcherSLOView = uci.IndexPublishedView

// UCIWatcherSLOOrigin names the concrete path which produced a retained
// watcher-evidence fact. An unavailable observation is explicit rather than
// borrowing a timestamp or counter from a different operation.
type UCIWatcherSLOOrigin string

const (
	UCIWatcherSLOOriginUnknownNotMeasured       UCIWatcherSLOOrigin = "unknown_not_measured"
	UCIWatcherSLOOriginInstalledClientSearch    UCIWatcherSLOOrigin = "installed_client_search"
	UCIWatcherSLOOriginInstalledEmbeddingStatus UCIWatcherSLOOrigin = "installed_embedding_status"
	UCIWatcherSLOOriginInstalledStatusAndView   UCIWatcherSLOOrigin = "installed_status_and_authoritative_view"
)

// UCIWatcherSLOTiming proves a stage was observed through timestamps. A
// measured zero/default duration cannot represent a healthy readiness sample.
type UCIWatcherSLOTiming struct {
	Origin      UCIWatcherSLOOrigin `json:"origin"`
	Measured    bool                `json:"measured"`
	StartedAt   time.Time           `json:"started_at"`
	CompletedAt time.Time           `json:"completed_at"`
	Latency     time.Duration       `json:"latency"`
}

// UCIWatcherSLOEmbeddingCounters are the before/after ready-candidate counts
// the installed client observes from codebase_status. They do not claim to be
// opaque provider request counters.
type UCIWatcherSLOEmbeddingCounters struct {
	Origin   UCIWatcherSLOOrigin `json:"origin"`
	Measured bool                `json:"measured"`
	Before   int64               `json:"before"`
	After    int64               `json:"after"`
}

// UCIWatcherSLOBatch is one retained A-only watcher update attempt. It carries
// raw observations whether the attempt was healthy, degraded, unavailable, or
// failed; the reporter never drops non-healthy attempts.
type UCIWatcherSLOBatch struct {
	ID                   string                         `json:"id"`
	Identity             uci.UCISLOSampleIdentity       `json:"identity"`
	ProfileID            string                         `json:"profile_id"`
	ObservedFSSeq        int64                          `json:"observed_fs_seq"`
	ChangedFileCount     int                            `json:"changed_file_count"`
	ChangedBytes         int64                          `json:"changed_bytes"`
	ScanOutcome          uci.IndexScanOutcome           `json:"scan_outcome"`
	ResultStatus         string                         `json:"result_status"`
	Coverage             string                         `json:"coverage"`
	Outcome              string                         `json:"outcome"`
	Reason               string                         `json:"reason,omitempty"`
	Warmth               string                         `json:"warmth"`
	Scan                 UCIWatcherSLOTiming            `json:"scan"`
	StructuralFTS        UCIWatcherSLOTiming            `json:"structural_fts"`
	EmbeddingReadiness   UCIWatcherSLOTiming            `json:"embedding_readiness"`
	LocalACK             UCIWatcherSLOTiming            `json:"local_ack"`
	EmbeddingCounters    UCIWatcherSLOEmbeddingCounters `json:"embedding_counters"`
	ReembeddedCandidates int                            `json:"reembedded_candidates"`
	ABefore              UCIWatcherSLOView              `json:"a_before"`
	AAfter               UCIWatcherSLOView              `json:"a_after"`
	BBefore              UCIWatcherSLOView              `json:"b_before"`
	BAfter               UCIWatcherSLOView              `json:"b_after"`
	ABeforeOrigin        UCIWatcherSLOOrigin            `json:"a_before_origin"`
	AAfterOrigin         UCIWatcherSLOOrigin            `json:"a_after_origin"`
	BBeforeOrigin        UCIWatcherSLOOrigin            `json:"b_before_origin"`
	BAfterOrigin         UCIWatcherSLOOrigin            `json:"b_after_origin"`
}

// UCIWatcherSLOUnchangedInputCounter proves that an exact unchanged A input
// did not cause another embedding-ready candidate or re-embedding.
type UCIWatcherSLOUnchangedInputCounter struct {
	Identity                  uci.UCISLOSampleIdentity `json:"identity"`
	Context                   uci.ContextRef           `json:"context"`
	InputDigest               string                   `json:"input_digest"`
	Unchanged                 bool                     `json:"unchanged"`
	EmbeddingCountersOrigin   UCIWatcherSLOOrigin      `json:"embedding_counters_origin"`
	EmbeddingCountersMeasured bool                     `json:"embedding_counters_measured"`
	EmbeddingCountersBefore   int64                    `json:"embedding_counters_before"`
	EmbeddingCountersAfter    int64                    `json:"embedding_counters_after"`
	ReembeddedCandidates      int                      `json:"reembedded_candidates"`
}

// UCIWatcherSLOInput is one externally recorded exact-candidate measurement
// fixture. It has no runner, benchmark, clock, host-discovery, or provider
// execution fields.
type UCIWatcherSLOInput struct {
	SchemaVersion          string                               `json:"schema_version"`
	Candidate              uci.UCISLOCandidate                  `json:"candidate"`
	Environment            uci.UCISLOEnvironment                `json:"environment"`
	Profile                uci.UCISLOProfile                    `json:"profile"`
	Batches                []UCIWatcherSLOBatch                 `json:"batches"`
	UnchangedInputCounters []UCIWatcherSLOUnchangedInputCounter `json:"unchanged_input_counters"`
}

// UCIWatcherSLOReport is deterministic accounting over every retained raw
// batch. Percentiles contain only healthy warm observations; counts retain all
// other outcomes separately.
type UCIWatcherSLOReport struct {
	SchemaVersion                  string                               `json:"schema_version"`
	Candidate                      uci.UCISLOCandidate                  `json:"candidate"`
	Environment                    uci.UCISLOEnvironment                `json:"environment"`
	Profile                        uci.UCISLOProfile                    `json:"profile"`
	Batches                        []UCIWatcherSLOBatch                 `json:"batches"`
	UnchangedInputCounters         []UCIWatcherSLOUnchangedInputCounter `json:"unchanged_input_counters"`
	RetainedBatchCount             int                                  `json:"retained_batch_count"`
	HealthyWarmBatchCount          int                                  `json:"healthy_warm_batch_count"`
	EmbeddingHealthyWarmBatchCount int                                  `json:"embedding_healthy_warm_batch_count"`
	ColdBatchCount                 int                                  `json:"cold_batch_count"`
	DegradedBatchCount             int                                  `json:"degraded_batch_count"`
	UnavailableBatchCount          int                                  `json:"unavailable_batch_count"`
	FailedBatchCount               int                                  `json:"failed_batch_count"`
	StructuralFTSP95               time.Duration                        `json:"structural_fts_p95"`
	EmbeddingReadinessP95          time.Duration                        `json:"embedding_readiness_p95"`
	Passed                         bool                                 `json:"passed"`
}

// RunUCIWatcherSLO reads exactly one strict externally recorded watcher SLO
// input and accounts for it. It never runs a benchmark, infers host identity,
// or creates missing observations.
func RunUCIWatcherSLO(ctx context.Context) (UCIWatcherSLOReport, error) {
	if err := ctx.Err(); err != nil {
		return UCIWatcherSLOReport{}, fmt.Errorf("UCI watcher SLO context: %w", err)
	}

	path := strings.TrimSpace(os.Getenv(uciWatcherSLOInputEnv))
	if path == "" {
		return UCIWatcherSLOReport{}, fmt.Errorf("UCI watcher SLO RED prerequisite: externally recorded exact-candidate evidence is required via %s; refusing to fabricate update batches", uciWatcherSLOInputEnv)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return UCIWatcherSLOReport{}, fmt.Errorf("UCI watcher SLO RED prerequisite: %s points to unreadable external evidence", uciWatcherSLOInputEnv)
	}
	if err := ctx.Err(); err != nil {
		return UCIWatcherSLOReport{}, fmt.Errorf("UCI watcher SLO context: %w", err)
	}

	input, err := decodeUCIWatcherSLOInput(contents)
	if err != nil {
		return UCIWatcherSLOReport{}, fmt.Errorf("strictly decode externally recorded UCI watcher SLO input: %w", err)
	}
	report, err := CalculateUCIWatcherSLOReport(input)
	if err != nil {
		return report, fmt.Errorf("account externally recorded UCI watcher SLO input: %w", err)
	}
	if !report.Passed {
		return report, fmt.Errorf("UCI watcher SLO evidence is not an accepted healthy update profile: healthy warm batches=%d, structural p95=%s, embedding healthy warm batches=%d, embedding p95=%s", report.HealthyWarmBatchCount, report.StructuralFTSP95, report.EmbeddingHealthyWarmBatchCount, report.EmbeddingReadinessP95)
	}
	return report, nil
}

// CalculateUCIWatcherSLOReport deterministically accounts for supplied raw
// watcher evidence without executing any runtime operation.
func CalculateUCIWatcherSLOReport(input UCIWatcherSLOInput) (UCIWatcherSLOReport, error) {
	canonical, scopes, err := canonicalUCIWatcherSLOInput(input)
	if err != nil {
		return UCIWatcherSLOReport{}, err
	}

	report := UCIWatcherSLOReport{
		SchemaVersion:          UCIWatcherSLOSchemaVersion,
		Candidate:              canonical.Candidate,
		Environment:            canonical.Environment,
		Profile:                canonical.Profile,
		Batches:                canonical.Batches,
		UnchangedInputCounters: canonical.UnchangedInputCounters,
		RetainedBatchCount:     len(canonical.Batches),
	}
	structuralLatencies := make([]time.Duration, 0, len(report.Batches))
	embeddingLatencies := make([]time.Duration, 0, len(report.Batches))
	for _, batch := range report.Batches {
		switch batch.Outcome {
		case "healthy":
			if batch.Warmth == "warm" {
				report.HealthyWarmBatchCount++
				structuralLatencies = append(structuralLatencies, batch.StructuralFTS.Latency)
				if report.Environment.Provider.Status == "healthy" {
					report.EmbeddingHealthyWarmBatchCount++
					embeddingLatencies = append(embeddingLatencies, batch.EmbeddingReadiness.Latency)
				}
			}
		case "degraded":
			report.DegradedBatchCount++
		case "unavailable":
			report.UnavailableBatchCount++
		case "failed":
			report.FailedBatchCount++
		}
		if batch.Warmth == "cold" {
			report.ColdBatchCount++
		}
	}
	if report.HealthyWarmBatchCount == 0 {
		return UCIWatcherSLOReport{}, fmt.Errorf("watcher SLO input has a zero healthy warm-batch denominator")
	}
	if report.ColdBatchCount == 0 {
		return UCIWatcherSLOReport{}, fmt.Errorf("watcher SLO input omits separately retained cold batches")
	}
	if report.Environment.Provider.Status == "healthy" && report.EmbeddingHealthyWarmBatchCount == 0 {
		return UCIWatcherSLOReport{}, fmt.Errorf("watcher SLO input has a zero healthy embedding-readiness denominator while provider is healthy")
	}

	sort.Slice(structuralLatencies, func(i, j int) bool { return structuralLatencies[i] < structuralLatencies[j] })
	report.StructuralFTSP95 = watcherSLONearestRankP95(structuralLatencies)
	if report.Environment.Provider.Status == "healthy" {
		sort.Slice(embeddingLatencies, func(i, j int) bool { return embeddingLatencies[i] < embeddingLatencies[j] })
		report.EmbeddingReadinessP95 = watcherSLONearestRankP95(embeddingLatencies)
	}
	report.Passed = watcherSLOReportPasses(report, scopes)
	return report, nil
}

type watcherSLOScope struct {
	sourceID   string
	checkoutID string
	profileID  string
	spaceID    string
	hasSpace   bool
}

type watcherSLOInputScopes struct {
	a watcherSLOScope
	b watcherSLOScope
}

func canonicalUCIWatcherSLOInput(input UCIWatcherSLOInput) (UCIWatcherSLOInput, watcherSLOInputScopes, error) {
	if input.SchemaVersion != UCIWatcherSLOSchemaVersion {
		return UCIWatcherSLOInput{}, watcherSLOInputScopes{}, fmt.Errorf("watcher SLO input has unsupported schema %q", input.SchemaVersion)
	}
	if !validWatcherSLOCandidate(input.Candidate) {
		return UCIWatcherSLOInput{}, watcherSLOInputScopes{}, fmt.Errorf("watcher SLO input omits exact candidate identity")
	}
	if !validWatcherSLOEnvironment(input.Environment) {
		return UCIWatcherSLOInput{}, watcherSLOInputScopes{}, fmt.Errorf("watcher SLO input omits environment identity")
	}
	if !validWatcherSLOProfile(input.Profile) {
		return UCIWatcherSLOInput{}, watcherSLOInputScopes{}, fmt.Errorf("watcher SLO input profile is outside accepted bounds")
	}
	if len(input.Batches) == 0 {
		return UCIWatcherSLOInput{}, watcherSLOInputScopes{}, fmt.Errorf("watcher SLO input has a zero retained-batch denominator")
	}
	if len(input.UnchangedInputCounters) == 0 {
		return UCIWatcherSLOInput{}, watcherSLOInputScopes{}, fmt.Errorf("watcher SLO input omits unchanged-input provider counters")
	}

	canonical := UCIWatcherSLOInput{
		SchemaVersion:          UCIWatcherSLOSchemaVersion,
		Candidate:              canonicalWatcherSLOCandidate(input.Candidate),
		Environment:            input.Environment,
		Profile:                input.Profile,
		Batches:                make([]UCIWatcherSLOBatch, 0, len(input.Batches)),
		UnchangedInputCounters: make([]UCIWatcherSLOUnchangedInputCounter, 0, len(input.UnchangedInputCounters)),
	}
	seenBatchIDs := make(map[string]struct{}, len(input.Batches))
	var scopes watcherSLOInputScopes
	for index, batch := range input.Batches {
		batch = cloneWatcherSLOBatch(batch)
		batch.Identity.Candidate = canonicalWatcherSLOCandidate(batch.Identity.Candidate)
		if !validWatcherSLOText(batch.ID) {
			return UCIWatcherSLOInput{}, watcherSLOInputScopes{}, fmt.Errorf("watcher SLO batch %d omits a bounded ID", index)
		}
		if _, duplicate := seenBatchIDs[batch.ID]; duplicate {
			return UCIWatcherSLOInput{}, watcherSLOInputScopes{}, fmt.Errorf("watcher SLO input repeats batch ID %q", batch.ID)
		}
		seenBatchIDs[batch.ID] = struct{}{}
		batchScopes, err := validateWatcherSLOBatch(batch, canonical.Candidate, canonical.Environment, canonical.Profile)
		if err != nil {
			return UCIWatcherSLOInput{}, watcherSLOInputScopes{}, fmt.Errorf("watcher SLO batch %q: %w", batch.ID, err)
		}
		if index == 0 {
			scopes = batchScopes
		} else if scopes != batchScopes {
			return UCIWatcherSLOInput{}, watcherSLOInputScopes{}, fmt.Errorf("watcher SLO batch %q mixes A or B Source/Checkout/Profile scope", batch.ID)
		}
		canonical.Batches = append(canonical.Batches, batch)
	}
	sort.Slice(canonical.Batches, func(i, j int) bool { return canonical.Batches[i].ID < canonical.Batches[j].ID })

	seenCounters := make(map[string]struct{}, len(input.UnchangedInputCounters))
	for index, counter := range input.UnchangedInputCounters {
		counter = cloneWatcherSLOUnchangedInputCounter(counter)
		counter.Identity.Candidate = canonicalWatcherSLOCandidate(counter.Identity.Candidate)
		if err := validateWatcherSLOUnchangedInputCounter(counter, canonical.Candidate, canonical.Environment, scopes.a); err != nil {
			return UCIWatcherSLOInput{}, watcherSLOInputScopes{}, fmt.Errorf("watcher SLO unchanged-input counter %d: %w", index, err)
		}
		key := watcherSLOContextKey(counter.Context) + "\x1f" + counter.InputDigest
		if _, duplicate := seenCounters[key]; duplicate {
			return UCIWatcherSLOInput{}, watcherSLOInputScopes{}, fmt.Errorf("watcher SLO input repeats unchanged-input counter for %q", counter.InputDigest)
		}
		seenCounters[key] = struct{}{}
		canonical.UnchangedInputCounters = append(canonical.UnchangedInputCounters, counter)
	}
	sort.Slice(canonical.UnchangedInputCounters, func(i, j int) bool {
		left, right := canonical.UnchangedInputCounters[i], canonical.UnchangedInputCounters[j]
		leftKey := watcherSLOContextKey(left.Context) + "\x1f" + left.InputDigest
		rightKey := watcherSLOContextKey(right.Context) + "\x1f" + right.InputDigest
		return leftKey < rightKey
	})
	return canonical, scopes, nil
}

func validateWatcherSLOBatch(batch UCIWatcherSLOBatch, candidate uci.UCISLOCandidate, environment uci.UCISLOEnvironment, profile uci.UCISLOProfile) (watcherSLOInputScopes, error) {
	if !watcherSLOCandidateEqual(batch.Identity.Candidate, candidate) || batch.Identity.Environment != environment {
		return watcherSLOInputScopes{}, fmt.Errorf("mixes candidate or environment identity")
	}
	if batch.ProfileID != profile.ID || batch.ObservedFSSeq < 0 || batch.ChangedFileCount <= 0 || batch.ChangedFileCount > watcherSLOMaximumChangedFiles || batch.ChangedBytes <= 0 || batch.ChangedBytes > watcherSLOMaximumChangedBytes {
		return watcherSLOInputScopes{}, fmt.Errorf("does not bind accepted profile, observed sequence, and changed-file bounds")
	}
	if !validWatcherSLOScanOutcome(batch.ScanOutcome) || !validWatcherSLOResultStatus(batch.ResultStatus) || !validWatcherSLOCoverage(batch.Coverage) || !validWatcherSLOOutcome(batch.Outcome) || (batch.Warmth != "warm" && batch.Warmth != "cold") {
		return watcherSLOInputScopes{}, fmt.Errorf("has unsupported scan, result, coverage, outcome, or warmth state")
	}
	if batch.ReembeddedCandidates < 0 {
		return watcherSLOInputScopes{}, fmt.Errorf("has a negative re-embedding count")
	}
	if err := validateWatcherSLOTiming(batch.Scan, UCIWatcherSLOOriginUnknownNotMeasured, false); err != nil {
		return watcherSLOInputScopes{}, fmt.Errorf("scan timing: %w", err)
	}
	if err := validateWatcherSLOTiming(batch.StructuralFTS, UCIWatcherSLOOriginInstalledClientSearch, false); err != nil {
		return watcherSLOInputScopes{}, fmt.Errorf("structural/FTS timing: %w", err)
	}
	if err := validateWatcherSLOTiming(batch.EmbeddingReadiness, UCIWatcherSLOOriginInstalledEmbeddingStatus, false); err != nil {
		return watcherSLOInputScopes{}, fmt.Errorf("embedding-readiness timing: %w", err)
	}
	if err := validateWatcherSLOTiming(batch.LocalACK, UCIWatcherSLOOriginUnknownNotMeasured, false); err != nil {
		return watcherSLOInputScopes{}, fmt.Errorf("local-ACK timing: %w", err)
	}
	if batch.EmbeddingReadiness.Measured && (!batch.StructuralFTS.Measured || !watcherSLOTimingFollows(batch.EmbeddingReadiness, batch.StructuralFTS)) {
		return watcherSLOInputScopes{}, fmt.Errorf("embedding readiness precedes structural/FTS readiness")
	}
	if err := validateWatcherSLOEmbeddingCounters(batch.EmbeddingCounters, false); err != nil {
		return watcherSLOInputScopes{}, fmt.Errorf("embedding counters: %w", err)
	}

	aBeforeScope, err := validateWatcherSLOView(batch.ABefore, profile.ID)
	if err != nil {
		return watcherSLOInputScopes{}, fmt.Errorf("A before View: %w", err)
	}
	aAfterScope, err := validateWatcherSLOView(batch.AAfter, profile.ID)
	if err != nil {
		return watcherSLOInputScopes{}, fmt.Errorf("A after View: %w", err)
	}
	bBeforeScope, err := validateWatcherSLOView(batch.BBefore, profile.ID)
	if err != nil {
		return watcherSLOInputScopes{}, fmt.Errorf("B before View: %w", err)
	}
	bAfterScope, err := validateWatcherSLOView(batch.BAfter, profile.ID)
	if err != nil {
		return watcherSLOInputScopes{}, fmt.Errorf("B after View: %w", err)
	}
	for name, origin := range map[string]UCIWatcherSLOOrigin{
		"A before": batch.ABeforeOrigin,
		"A after":  batch.AAfterOrigin,
		"B before": batch.BBeforeOrigin,
		"B after":  batch.BAfterOrigin,
	} {
		if origin != UCIWatcherSLOOriginInstalledStatusAndView {
			return watcherSLOInputScopes{}, fmt.Errorf("%s View origin = %q, want %q", name, origin, UCIWatcherSLOOriginInstalledStatusAndView)
		}
	}
	if aBeforeScope != aAfterScope || bBeforeScope != bAfterScope {
		return watcherSLOInputScopes{}, fmt.Errorf("before/after View scope changed within one checkout")
	}
	if aAfterScope.checkoutID == bAfterScope.checkoutID || aAfterScope.sourceID != bAfterScope.sourceID || aAfterScope.profileID != bAfterScope.profileID || aAfterScope.hasSpace != bAfterScope.hasSpace || aAfterScope.spaceID != bAfterScope.spaceID {
		return watcherSLOInputScopes{}, fmt.Errorf("does not retain distinct A and B checkouts in one source/profile scope")
	}
	if !watcherSLOViewsEqual(batch.BBefore, batch.BAfter) {
		return watcherSLOInputScopes{}, fmt.Errorf("mutates unaffected checkout B")
	}

	if batch.Outcome == "healthy" {
		if environment.Provider.Status != "healthy" || batch.ScanOutcome != uci.IndexScanComplete || watcherSLOResultIndicatesDegradation(batch.ResultStatus, batch.Coverage) || batch.Scan.Measured || !batch.StructuralFTS.Measured || batch.StructuralFTS.Latency <= 0 || !batch.EmbeddingReadiness.Measured || batch.EmbeddingReadiness.Latency <= 0 || batch.LocalACK.Measured || !batch.EmbeddingCounters.Measured || strings.TrimSpace(batch.Reason) != "" {
			return watcherSLOInputScopes{}, fmt.Errorf("labels unresolved, degraded, or unmeasured update evidence as healthy")
		}
		if batch.ABefore.Context.ViewID == batch.AAfter.Context.ViewID || batch.ABefore.Context.Generation >= batch.AAfter.Context.Generation || batch.ABefore.ManifestDigest == batch.AAfter.ManifestDigest || batch.ABefore.AcceptedFSSeq >= batch.AAfter.AcceptedFSSeq || batch.AAfter.AcceptedFSSeq != batch.ObservedFSSeq {
			return watcherSLOInputScopes{}, fmt.Errorf("does not publish a new coherent A View at the observed sequence")
		}
	} else {
		if !validWatcherSLOReason(batch.Reason) {
			return watcherSLOInputScopes{}, fmt.Errorf("non-healthy outcome omits a bounded reason")
		}
		if batch.ScanOutcome != uci.IndexScanComplete && !watcherSLOViewsEqual(batch.ABefore, batch.AAfter) {
			return watcherSLOInputScopes{}, fmt.Errorf("incomplete or failed scan changes A instead of retaining its prior coherent View")
		}
	}
	return watcherSLOInputScopes{a: aAfterScope, b: bAfterScope}, nil
}

func validateWatcherSLOUnchangedInputCounter(counter UCIWatcherSLOUnchangedInputCounter, candidate uci.UCISLOCandidate, environment uci.UCISLOEnvironment, scope watcherSLOScope) error {
	if !counter.Unchanged || !validWatcherSLODigest(counter.InputDigest) {
		return fmt.Errorf("is not bound to an unchanged input digest")
	}
	if !watcherSLOCandidateEqual(counter.Identity.Candidate, candidate) || counter.Identity.Environment != environment {
		return fmt.Errorf("mixes candidate or environment identity")
	}
	counterScope, err := watcherSLOScopeForContext(counter.Context)
	if err != nil {
		return fmt.Errorf("context is invalid: %w", err)
	}
	if counterScope != scope {
		return fmt.Errorf("context is outside the A checkout scope")
	}
	if counter.EmbeddingCountersOrigin != UCIWatcherSLOOriginInstalledEmbeddingStatus || !counter.EmbeddingCountersMeasured || counter.EmbeddingCountersBefore < 0 || counter.EmbeddingCountersAfter < counter.EmbeddingCountersBefore {
		return fmt.Errorf("embedding counter bounds or origin are unresolved")
	}
	if counter.EmbeddingCountersAfter != counter.EmbeddingCountersBefore || counter.ReembeddedCandidates != 0 {
		return fmt.Errorf("unchanged input caused embedding work or re-embedding")
	}
	return nil
}

func watcherSLOReportPasses(report UCIWatcherSLOReport, scopes watcherSLOInputScopes) bool {
	return report.SchemaVersion == UCIWatcherSLOSchemaVersion &&
		validWatcherSLOCandidate(report.Candidate) &&
		validWatcherSLOEnvironment(report.Environment) &&
		validWatcherSLOProfile(report.Profile) &&
		report.Environment.Provider.Status == "healthy" &&
		report.RetainedBatchCount == len(report.Batches) &&
		report.RetainedBatchCount > 0 &&
		report.HealthyWarmBatchCount >= uciWatcherSLOMinimumHealthyWarm &&
		report.EmbeddingHealthyWarmBatchCount >= uciWatcherSLOMinimumHealthyWarm &&
		report.ColdBatchCount > 0 &&
		len(report.UnchangedInputCounters) > 0 &&
		report.StructuralFTSP95 > 0 && report.StructuralFTSP95 <= uciWatcherSLOStructuralFTSTarget &&
		report.EmbeddingReadinessP95 > 0 && report.EmbeddingReadinessP95 <= uciWatcherSLOEmbeddingReadyTarget &&
		scopes.a.checkoutID != scopes.b.checkoutID
}

func validWatcherSLOCandidate(candidate uci.UCISLOCandidate) bool {
	if !validWatcherSLOText(candidate.Branch) || !validWatcherSLOGitRevision(candidate.Commit) || !validWatcherSLOGitRevision(candidate.Tree) || len(candidate.ArtifactDigests) == 0 {
		return false
	}
	seenNames := make(map[string]struct{}, len(candidate.ArtifactDigests))
	for _, artifact := range candidate.ArtifactDigests {
		if !validWatcherSLOText(artifact.Name) || !validWatcherSLODigest(artifact.Digest) {
			return false
		}
		if _, duplicate := seenNames[artifact.Name]; duplicate {
			return false
		}
		seenNames[artifact.Name] = struct{}{}
	}
	return true
}

func validWatcherSLOEnvironment(environment uci.UCISLOEnvironment) bool {
	return validWatcherSLOText(environment.Host.ID) &&
		validWatcherSLOText(environment.Database.ID) &&
		validWatcherSLOText(environment.Database.Version) &&
		environment.Database.DataSizeBytes > 0 &&
		validWatcherSLOText(environment.Corpus.ID) &&
		validWatcherSLODigest(environment.Corpus.ManifestDigest) &&
		validWatcherSLOText(environment.Provider.ID) &&
		validWatcherSLOText(environment.Provider.Model) &&
		validWatcherSLOText(environment.Provider.Status)
}

func validWatcherSLOProfile(profile uci.UCISLOProfile) bool {
	return validWatcherSLOText(profile.ID) &&
		profile.TextFileCount > 0 &&
		profile.LinesOfCode > 0 && profile.LinesOfCode <= 1_000_000 &&
		profile.ActiveWorktreeCount >= 5 &&
		profile.InactiveRegistrationCount > 0 &&
		profile.LANRTT >= 0 && profile.LANRTT <= 50*time.Millisecond &&
		profile.ChangedFileCount > 0 && profile.ChangedFileCount <= watcherSLOMaximumChangedFiles &&
		profile.ChangedBytes > 0 && profile.ChangedBytes <= watcherSLOMaximumChangedBytes
}

func validWatcherSLOText(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > uciWatcherSLOMaximumTextBytes {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validWatcherSLOScanOutcome(outcome uci.IndexScanOutcome) bool {
	switch outcome {
	case uci.IndexScanComplete, uci.IndexScanIncomplete, uci.IndexScanFailed:
		return true
	default:
		return false
	}
}

func validWatcherSLOResultStatus(status string) bool {
	switch status {
	case "ok", "empty", "partial", "stale", "unavailable", "degraded", "offline", "unsupported", "failed", "error", "unknown":
		return true
	default:
		return false
	}
}

func validWatcherSLOCoverage(coverage string) bool {
	switch coverage {
	case "complete", "partial", "unavailable", "degraded", "offline", "unsupported", "failed", "unknown":
		return true
	default:
		return false
	}
}

func validWatcherSLOOutcome(outcome string) bool {
	switch outcome {
	case "healthy", "degraded", "unavailable", "failed":
		return true
	default:
		return false
	}
}

func validWatcherSLOReason(reason string) bool {
	return validWatcherSLOText(reason)
}

func validateWatcherSLOTiming(timing UCIWatcherSLOTiming, expectedOrigin UCIWatcherSLOOrigin, required bool) error {
	if !timing.Measured {
		if required || timing.Origin != UCIWatcherSLOOriginUnknownNotMeasured || !timing.StartedAt.IsZero() || !timing.CompletedAt.IsZero() || timing.Latency != 0 {
			return fmt.Errorf("unmeasured timing carries values or an unmeasurable origin")
		}
		return nil
	}
	if timing.Origin != expectedOrigin {
		return fmt.Errorf("origin = %q, want %q", timing.Origin, expectedOrigin)
	}
	if expectedOrigin == UCIWatcherSLOOriginUnknownNotMeasured || timing.StartedAt.IsZero() || timing.CompletedAt.IsZero() || timing.CompletedAt.Before(timing.StartedAt) || timing.Latency < 0 || timing.Latency != timing.CompletedAt.Sub(timing.StartedAt) {
		return fmt.Errorf("timing is not an actual non-negative timestamp interval")
	}
	return nil
}

func watcherSLOTimingFollows(next, previous UCIWatcherSLOTiming) bool {
	return next.StartedAt.Equal(previous.StartedAt) && !next.CompletedAt.Before(previous.CompletedAt)
}

func validateWatcherSLOEmbeddingCounters(counters UCIWatcherSLOEmbeddingCounters, required bool) error {
	if !counters.Measured {
		if required || counters.Origin != UCIWatcherSLOOriginUnknownNotMeasured || counters.Before != 0 || counters.After != 0 {
			return fmt.Errorf("unmeasured counter carries values or an unmeasurable origin")
		}
		return nil
	}
	if counters.Origin != UCIWatcherSLOOriginInstalledEmbeddingStatus {
		return fmt.Errorf("origin = %q, want %q", counters.Origin, UCIWatcherSLOOriginInstalledEmbeddingStatus)
	}
	if counters.Before < 0 || counters.After < counters.Before {
		return fmt.Errorf("counter bounds are invalid")
	}
	return nil
}

func validateWatcherSLOView(view UCIWatcherSLOView, profileID string) (watcherSLOScope, error) {
	if !canonicalWatcherSLOUUID(view.BuildID) || !validWatcherSLODigest(string(view.ManifestDigest)) || view.AcceptedFSSeq < 0 || view.PublishedAt.IsZero() {
		return watcherSLOScope{}, fmt.Errorf("durable View facts are incomplete")
	}
	scope, err := watcherSLOScopeForContext(view.Context)
	if err != nil {
		return watcherSLOScope{}, err
	}
	if scope.profileID != profileID {
		return watcherSLOScope{}, fmt.Errorf("View profile differs from batch profile")
	}
	return scope, nil
}

func watcherSLOScopeForContext(context uci.ContextRef) (watcherSLOScope, error) {
	if !canonicalWatcherSLOUUID(context.SourceID) || !canonicalWatcherSLOUUID(context.CheckoutID) || !canonicalWatcherSLOUUID(context.ViewID) || !canonicalWatcherSLOUUID(context.AnalysisProfileID) || context.Generation <= 0 {
		return watcherSLOScope{}, fmt.Errorf("ContextRef is incomplete")
	}
	scope := watcherSLOScope{
		sourceID:   context.SourceID,
		checkoutID: context.CheckoutID,
		profileID:  context.AnalysisProfileID,
		hasSpace:   context.SpaceID != nil,
	}
	if context.SpaceID != nil {
		if !canonicalWatcherSLOUUID(*context.SpaceID) {
			return watcherSLOScope{}, fmt.Errorf("ContextRef space is invalid")
		}
		scope.spaceID = *context.SpaceID
	}
	return scope, nil
}

func canonicalWatcherSLOUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func validWatcherSLODigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	digest := value[len("sha256:"):]
	_, err := hex.DecodeString(digest)
	return err == nil && strings.ToLower(digest) == digest
}

func validWatcherSLOGitRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func watcherSLOResultIndicatesDegradation(resultStatus, coverage string) bool {
	switch resultStatus {
	case "partial", "stale", "unavailable", "degraded", "offline", "unsupported", "failed", "error", "unknown":
		return true
	}
	switch coverage {
	case "partial", "unavailable", "degraded", "offline", "unsupported", "failed", "unknown":
		return true
	}
	return false
}

func watcherSLOViewsEqual(left, right UCIWatcherSLOView) bool {
	return left.BuildID == right.BuildID &&
		watcherSLOContextsEqual(left.Context, right.Context) &&
		left.ManifestDigest == right.ManifestDigest &&
		left.AcceptedFSSeq == right.AcceptedFSSeq &&
		left.PublishedAt.Equal(right.PublishedAt)
}

func watcherSLOContextsEqual(left, right uci.ContextRef) bool {
	if left.SourceID != right.SourceID || left.CheckoutID != right.CheckoutID || left.ViewID != right.ViewID || left.AnalysisProfileID != right.AnalysisProfileID || left.Generation != right.Generation || (left.SpaceID == nil) != (right.SpaceID == nil) {
		return false
	}
	return left.SpaceID == nil || *left.SpaceID == *right.SpaceID
}

func watcherSLOContextKey(context uci.ContextRef) string {
	spaceID := ""
	if context.SpaceID != nil {
		spaceID = *context.SpaceID
	}
	return strings.Join([]string{context.SourceID, context.CheckoutID, context.ViewID, fmt.Sprintf("%d", context.Generation), context.AnalysisProfileID, spaceID}, "\x1e")
}

func watcherSLONearestRankP95(sorted []time.Duration) time.Duration {
	return sorted[(95*len(sorted)+99)/100-1]
}

func canonicalWatcherSLOCandidate(candidate uci.UCISLOCandidate) uci.UCISLOCandidate {
	copy := candidate
	copy.ArtifactDigests = append([]uci.UCISLOArtifactDigest(nil), candidate.ArtifactDigests...)
	sort.Slice(copy.ArtifactDigests, func(i, j int) bool {
		left, right := copy.ArtifactDigests[i], copy.ArtifactDigests[j]
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.Digest < right.Digest
	})
	return copy
}

func watcherSLOCandidateEqual(left, right uci.UCISLOCandidate) bool {
	left, right = canonicalWatcherSLOCandidate(left), canonicalWatcherSLOCandidate(right)
	if left.Branch != right.Branch || left.Commit != right.Commit || left.Tree != right.Tree || len(left.ArtifactDigests) != len(right.ArtifactDigests) {
		return false
	}
	for index := range left.ArtifactDigests {
		if left.ArtifactDigests[index] != right.ArtifactDigests[index] {
			return false
		}
	}
	return true
}

func cloneWatcherSLOBatch(batch UCIWatcherSLOBatch) UCIWatcherSLOBatch {
	clone := batch
	clone.Identity.Candidate = canonicalWatcherSLOCandidate(batch.Identity.Candidate)
	clone.ABefore = cloneWatcherSLOView(batch.ABefore)
	clone.AAfter = cloneWatcherSLOView(batch.AAfter)
	clone.BBefore = cloneWatcherSLOView(batch.BBefore)
	clone.BAfter = cloneWatcherSLOView(batch.BAfter)
	clone.Scan = canonicalWatcherSLOTiming(batch.Scan)
	clone.StructuralFTS = canonicalWatcherSLOTiming(batch.StructuralFTS)
	clone.EmbeddingReadiness = canonicalWatcherSLOTiming(batch.EmbeddingReadiness)
	clone.LocalACK = canonicalWatcherSLOTiming(batch.LocalACK)
	return clone
}

func cloneWatcherSLOUnchangedInputCounter(counter UCIWatcherSLOUnchangedInputCounter) UCIWatcherSLOUnchangedInputCounter {
	clone := counter
	clone.Identity.Candidate = canonicalWatcherSLOCandidate(counter.Identity.Candidate)
	clone.Context = cloneWatcherSLOContext(counter.Context)
	return clone
}

func cloneWatcherSLOView(view UCIWatcherSLOView) UCIWatcherSLOView {
	clone := view
	clone.Context = cloneWatcherSLOContext(view.Context)
	clone.PublishedAt = clone.PublishedAt.UTC()
	return clone
}

func cloneWatcherSLOContext(context uci.ContextRef) uci.ContextRef {
	clone := context
	if context.SpaceID != nil {
		spaceID := *context.SpaceID
		clone.SpaceID = &spaceID
	}
	return clone
}

func canonicalWatcherSLOTiming(timing UCIWatcherSLOTiming) UCIWatcherSLOTiming {
	if !timing.StartedAt.IsZero() {
		timing.StartedAt = timing.StartedAt.UTC()
	}
	if !timing.CompletedAt.IsZero() {
		timing.CompletedAt = timing.CompletedAt.UTC()
	}
	return timing
}

type uciWatcherSLOInputWire struct {
	SchemaVersion          string                              `json:"schema_version"`
	Candidate              uciWatcherSLOCandidateWire          `json:"candidate"`
	Environment            uciWatcherSLOEnvironmentWire        `json:"environment"`
	Profile                uciWatcherSLOProfileWire            `json:"profile"`
	Batches                []uciWatcherSLOBatchWire            `json:"batches"`
	UnchangedInputCounters []uciWatcherSLOUnchangedCounterWire `json:"unchanged_input_counters"`
}

type uciWatcherSLOArtifactDigestWire struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type uciWatcherSLOCandidateWire struct {
	Branch          string                            `json:"branch"`
	Commit          string                            `json:"commit"`
	Tree            string                            `json:"tree"`
	ArtifactDigests []uciWatcherSLOArtifactDigestWire `json:"artifact_digests"`
}

type uciWatcherSLOHostWire struct {
	ID string `json:"id"`
}

type uciWatcherSLODatabaseWire struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	DataSizeBytes int64  `json:"data_size_bytes"`
}

type uciWatcherSLOCorpusWire struct {
	ID             string `json:"id"`
	ManifestDigest string `json:"manifest_digest"`
}

type uciWatcherSLOProviderWire struct {
	ID     string `json:"id"`
	Model  string `json:"model"`
	Status string `json:"status"`
}

type uciWatcherSLOEnvironmentWire struct {
	Host     uciWatcherSLOHostWire     `json:"host"`
	Database uciWatcherSLODatabaseWire `json:"database"`
	Corpus   uciWatcherSLOCorpusWire   `json:"corpus"`
	Provider uciWatcherSLOProviderWire `json:"provider"`
}

type uciWatcherSLOProfileWire struct {
	ID                        string        `json:"id"`
	TextFileCount             int           `json:"text_file_count"`
	LinesOfCode               int           `json:"lines_of_code"`
	ActiveWorktreeCount       int           `json:"active_worktree_count"`
	InactiveRegistrationCount int           `json:"inactive_registration_count"`
	LANRTT                    time.Duration `json:"lan_rtt"`
	ChangedFileCount          int           `json:"changed_file_count"`
	ChangedBytes              int64         `json:"changed_bytes"`
}

type uciWatcherSLOIdentityWire struct {
	Candidate   uciWatcherSLOCandidateWire   `json:"candidate"`
	Environment uciWatcherSLOEnvironmentWire `json:"environment"`
}

type uciWatcherSLOContextWire struct {
	SpaceID           *string `json:"space_id"`
	SourceID          string  `json:"source_id"`
	CheckoutID        string  `json:"checkout_id"`
	ViewID            string  `json:"view_id"`
	AnalysisProfileID string  `json:"analysis_profile_id"`
	Generation        int64   `json:"generation"`
}

type uciWatcherSLOViewWire struct {
	BuildID        string                   `json:"build_id"`
	Context        uciWatcherSLOContextWire `json:"context"`
	ManifestDigest string                   `json:"manifest_digest"`
	AcceptedFSSeq  int64                    `json:"accepted_fs_seq"`
	PublishedAt    time.Time                `json:"published_at"`
}

type uciWatcherSLOBatchWire struct {
	ID                   string                         `json:"id"`
	Identity             uciWatcherSLOIdentityWire      `json:"identity"`
	ProfileID            string                         `json:"profile_id"`
	ObservedFSSeq        int64                          `json:"observed_fs_seq"`
	ChangedFileCount     int                            `json:"changed_file_count"`
	ChangedBytes         int64                          `json:"changed_bytes"`
	ScanOutcome          uci.IndexScanOutcome           `json:"scan_outcome"`
	ResultStatus         string                         `json:"result_status"`
	Coverage             string                         `json:"coverage"`
	Outcome              string                         `json:"outcome"`
	Reason               string                         `json:"reason"`
	Warmth               string                         `json:"warmth"`
	Scan                 uciWatcherSLOTimingWire        `json:"scan"`
	StructuralFTS        uciWatcherSLOTimingWire        `json:"structural_fts"`
	EmbeddingReadiness   uciWatcherSLOTimingWire        `json:"embedding_readiness"`
	LocalACK             uciWatcherSLOTimingWire        `json:"local_ack"`
	EmbeddingCounters    UCIWatcherSLOEmbeddingCounters `json:"embedding_counters"`
	ReembeddedCandidates int                            `json:"reembedded_candidates"`
	ABefore              uciWatcherSLOViewWire          `json:"a_before"`
	AAfter               uciWatcherSLOViewWire          `json:"a_after"`
	BBefore              uciWatcherSLOViewWire          `json:"b_before"`
	BAfter               uciWatcherSLOViewWire          `json:"b_after"`
	ABeforeOrigin        UCIWatcherSLOOrigin            `json:"a_before_origin"`
	AAfterOrigin         UCIWatcherSLOOrigin            `json:"a_after_origin"`
	BBeforeOrigin        UCIWatcherSLOOrigin            `json:"b_before_origin"`
	BAfterOrigin         UCIWatcherSLOOrigin            `json:"b_after_origin"`
}

type uciWatcherSLOUnchangedCounterWire struct {
	Identity                  uciWatcherSLOIdentityWire `json:"identity"`
	Context                   uciWatcherSLOContextWire  `json:"context"`
	InputDigest               string                    `json:"input_digest"`
	Unchanged                 bool                      `json:"unchanged"`
	EmbeddingCountersOrigin   UCIWatcherSLOOrigin       `json:"embedding_counters_origin"`
	EmbeddingCountersMeasured bool                      `json:"embedding_counters_measured"`
	EmbeddingCountersBefore   int64                     `json:"embedding_counters_before"`
	EmbeddingCountersAfter    int64                     `json:"embedding_counters_after"`
	ReembeddedCandidates      int                       `json:"reembedded_candidates"`
}

type uciWatcherSLOTimingWire struct {
	Origin      UCIWatcherSLOOrigin `json:"origin"`
	Measured    bool                `json:"measured"`
	StartedAt   *time.Time          `json:"started_at,omitempty"`
	CompletedAt *time.Time          `json:"completed_at,omitempty"`
	Latency     time.Duration       `json:"latency"`
}

// EncodeUCIWatcherSLOInput validates and encodes strict externally recorded
// watcher evidence. Recorder commands use this exact wire contract; component
// publication measurements are intentionally a different schema.
func EncodeUCIWatcherSLOInput(input UCIWatcherSLOInput) ([]byte, error) {
	canonical, _, err := canonicalUCIWatcherSLOInput(input)
	if err != nil {
		return nil, err
	}
	return json.Marshal(watcherSLOInputWireFor(canonical))
}

func watcherSLOInputWireFor(input UCIWatcherSLOInput) uciWatcherSLOInputWire {
	wire := uciWatcherSLOInputWire{
		SchemaVersion: input.SchemaVersion,
		Candidate: uciWatcherSLOCandidateWire{
			Branch: input.Candidate.Branch, Commit: input.Candidate.Commit, Tree: input.Candidate.Tree,
			ArtifactDigests: make([]uciWatcherSLOArtifactDigestWire, 0, len(input.Candidate.ArtifactDigests)),
		},
		Environment: uciWatcherSLOEnvironmentWire{
			Host:     uciWatcherSLOHostWire{ID: input.Environment.Host.ID},
			Database: uciWatcherSLODatabaseWire{ID: input.Environment.Database.ID, Version: input.Environment.Database.Version, DataSizeBytes: input.Environment.Database.DataSizeBytes},
			Corpus:   uciWatcherSLOCorpusWire{ID: input.Environment.Corpus.ID, ManifestDigest: input.Environment.Corpus.ManifestDigest},
			Provider: uciWatcherSLOProviderWire{ID: input.Environment.Provider.ID, Model: input.Environment.Provider.Model, Status: input.Environment.Provider.Status},
		},
		Profile: uciWatcherSLOProfileWire{
			ID: input.Profile.ID, TextFileCount: input.Profile.TextFileCount, LinesOfCode: input.Profile.LinesOfCode,
			ActiveWorktreeCount: input.Profile.ActiveWorktreeCount, InactiveRegistrationCount: input.Profile.InactiveRegistrationCount,
			LANRTT: input.Profile.LANRTT, ChangedFileCount: input.Profile.ChangedFileCount, ChangedBytes: input.Profile.ChangedBytes,
		},
		Batches:                make([]uciWatcherSLOBatchWire, 0, len(input.Batches)),
		UnchangedInputCounters: make([]uciWatcherSLOUnchangedCounterWire, 0, len(input.UnchangedInputCounters)),
	}
	for _, artifact := range input.Candidate.ArtifactDigests {
		wire.Candidate.ArtifactDigests = append(wire.Candidate.ArtifactDigests, uciWatcherSLOArtifactDigestWire{Name: artifact.Name, Digest: artifact.Digest})
	}
	for _, batch := range input.Batches {
		wire.Batches = append(wire.Batches, uciWatcherSLOBatchWire{
			ID: batch.ID, Identity: watcherSLOIdentityWireFor(batch.Identity), ProfileID: batch.ProfileID,
			ObservedFSSeq: batch.ObservedFSSeq, ChangedFileCount: batch.ChangedFileCount, ChangedBytes: batch.ChangedBytes,
			ScanOutcome: batch.ScanOutcome, ResultStatus: batch.ResultStatus, Coverage: batch.Coverage, Outcome: batch.Outcome,
			Reason: batch.Reason, Warmth: batch.Warmth, Scan: watcherSLOTimingWireFor(batch.Scan), StructuralFTS: watcherSLOTimingWireFor(batch.StructuralFTS),
			EmbeddingReadiness: watcherSLOTimingWireFor(batch.EmbeddingReadiness), LocalACK: watcherSLOTimingWireFor(batch.LocalACK), EmbeddingCounters: batch.EmbeddingCounters,
			ReembeddedCandidates: batch.ReembeddedCandidates, ABefore: watcherSLOViewWireFor(batch.ABefore), AAfter: watcherSLOViewWireFor(batch.AAfter),
			BBefore: watcherSLOViewWireFor(batch.BBefore), BAfter: watcherSLOViewWireFor(batch.BAfter),
			ABeforeOrigin: batch.ABeforeOrigin, AAfterOrigin: batch.AAfterOrigin, BBeforeOrigin: batch.BBeforeOrigin, BAfterOrigin: batch.BAfterOrigin,
		})
	}
	for _, counter := range input.UnchangedInputCounters {
		wire.UnchangedInputCounters = append(wire.UnchangedInputCounters, uciWatcherSLOUnchangedCounterWire{
			Identity: watcherSLOIdentityWireFor(counter.Identity), Context: watcherSLOContextWireFor(counter.Context),
			InputDigest: counter.InputDigest, Unchanged: counter.Unchanged, EmbeddingCountersOrigin: counter.EmbeddingCountersOrigin,
			EmbeddingCountersMeasured: counter.EmbeddingCountersMeasured, EmbeddingCountersBefore: counter.EmbeddingCountersBefore,
			EmbeddingCountersAfter: counter.EmbeddingCountersAfter, ReembeddedCandidates: counter.ReembeddedCandidates,
		})
	}
	return wire
}

func watcherSLOIdentityWireFor(identity uci.UCISLOSampleIdentity) uciWatcherSLOIdentityWire {
	return uciWatcherSLOIdentityWire{
		Candidate: uciWatcherSLOCandidateWire{Branch: identity.Candidate.Branch, Commit: identity.Candidate.Commit, Tree: identity.Candidate.Tree, ArtifactDigests: watcherSLOArtifactWires(identity.Candidate.ArtifactDigests)},
		Environment: uciWatcherSLOEnvironmentWire{
			Host:     uciWatcherSLOHostWire{ID: identity.Environment.Host.ID},
			Database: uciWatcherSLODatabaseWire{ID: identity.Environment.Database.ID, Version: identity.Environment.Database.Version, DataSizeBytes: identity.Environment.Database.DataSizeBytes},
			Corpus:   uciWatcherSLOCorpusWire{ID: identity.Environment.Corpus.ID, ManifestDigest: identity.Environment.Corpus.ManifestDigest},
			Provider: uciWatcherSLOProviderWire{ID: identity.Environment.Provider.ID, Model: identity.Environment.Provider.Model, Status: identity.Environment.Provider.Status},
		},
	}
}

func watcherSLOArtifactWires(artifacts []uci.UCISLOArtifactDigest) []uciWatcherSLOArtifactDigestWire {
	result := make([]uciWatcherSLOArtifactDigestWire, 0, len(artifacts))
	for _, artifact := range artifacts {
		result = append(result, uciWatcherSLOArtifactDigestWire{Name: artifact.Name, Digest: artifact.Digest})
	}
	return result
}

func watcherSLOContextWireFor(context uci.ContextRef) uciWatcherSLOContextWire {
	return uciWatcherSLOContextWire{SpaceID: context.SpaceID, SourceID: context.SourceID, CheckoutID: context.CheckoutID, ViewID: context.ViewID, AnalysisProfileID: context.AnalysisProfileID, Generation: context.Generation}
}

func watcherSLOTimingWireFor(timing UCIWatcherSLOTiming) uciWatcherSLOTimingWire {
	wire := uciWatcherSLOTimingWire{Origin: timing.Origin, Measured: timing.Measured, Latency: timing.Latency}
	if !timing.StartedAt.IsZero() {
		startedAt := timing.StartedAt
		wire.StartedAt = &startedAt
	}
	if !timing.CompletedAt.IsZero() {
		completedAt := timing.CompletedAt
		wire.CompletedAt = &completedAt
	}
	return wire
}

func watcherSLOViewWireFor(view UCIWatcherSLOView) uciWatcherSLOViewWire {
	return uciWatcherSLOViewWire{BuildID: view.BuildID, Context: watcherSLOContextWireFor(view.Context), ManifestDigest: string(view.ManifestDigest), AcceptedFSSeq: view.AcceptedFSSeq, PublishedAt: view.PublishedAt}
}

func decodeUCIWatcherSLOInput(contents []byte) (UCIWatcherSLOInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var wire uciWatcherSLOInputWire
	if err := decoder.Decode(&wire); err != nil {
		return UCIWatcherSLOInput{}, err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return UCIWatcherSLOInput{}, fmt.Errorf("input contains more than one JSON value")
		}
		return UCIWatcherSLOInput{}, err
	}
	return wire.input(), nil
}

func (wire uciWatcherSLOInputWire) input() UCIWatcherSLOInput {
	input := UCIWatcherSLOInput{
		SchemaVersion:          wire.SchemaVersion,
		Candidate:              wire.Candidate.candidate(),
		Environment:            wire.Environment.environment(),
		Profile:                wire.Profile.profile(),
		Batches:                make([]UCIWatcherSLOBatch, 0, len(wire.Batches)),
		UnchangedInputCounters: make([]UCIWatcherSLOUnchangedInputCounter, 0, len(wire.UnchangedInputCounters)),
	}
	for _, batch := range wire.Batches {
		input.Batches = append(input.Batches, batch.batch())
	}
	for _, counter := range wire.UnchangedInputCounters {
		input.UnchangedInputCounters = append(input.UnchangedInputCounters, counter.counter())
	}
	return input
}

func (wire uciWatcherSLOCandidateWire) candidate() uci.UCISLOCandidate {
	candidate := uci.UCISLOCandidate{Branch: wire.Branch, Commit: wire.Commit, Tree: wire.Tree}
	if wire.ArtifactDigests != nil {
		candidate.ArtifactDigests = make([]uci.UCISLOArtifactDigest, 0, len(wire.ArtifactDigests))
		for _, artifact := range wire.ArtifactDigests {
			candidate.ArtifactDigests = append(candidate.ArtifactDigests, uci.UCISLOArtifactDigest{Name: artifact.Name, Digest: artifact.Digest})
		}
	}
	return candidate
}

func (wire uciWatcherSLOEnvironmentWire) environment() uci.UCISLOEnvironment {
	return uci.UCISLOEnvironment{
		Host:     uci.UCISLOHost{ID: wire.Host.ID},
		Database: uci.UCISLODatabase{ID: wire.Database.ID, Version: wire.Database.Version, DataSizeBytes: wire.Database.DataSizeBytes},
		Corpus:   uci.UCISLOCorpus{ID: wire.Corpus.ID, ManifestDigest: wire.Corpus.ManifestDigest},
		Provider: uci.UCISLOProvider{ID: wire.Provider.ID, Model: wire.Provider.Model, Status: wire.Provider.Status},
	}
}

func (wire uciWatcherSLOProfileWire) profile() uci.UCISLOProfile {
	return uci.UCISLOProfile{
		ID:                        wire.ID,
		TextFileCount:             wire.TextFileCount,
		LinesOfCode:               wire.LinesOfCode,
		ActiveWorktreeCount:       wire.ActiveWorktreeCount,
		InactiveRegistrationCount: wire.InactiveRegistrationCount,
		LANRTT:                    wire.LANRTT,
		ChangedFileCount:          wire.ChangedFileCount,
		ChangedBytes:              wire.ChangedBytes,
	}
}

func (wire uciWatcherSLOIdentityWire) identity() uci.UCISLOSampleIdentity {
	return uci.UCISLOSampleIdentity{Candidate: wire.Candidate.candidate(), Environment: wire.Environment.environment()}
}

func (wire uciWatcherSLOContextWire) context() uci.ContextRef {
	context := uci.ContextRef{
		SourceID:          wire.SourceID,
		CheckoutID:        wire.CheckoutID,
		ViewID:            wire.ViewID,
		AnalysisProfileID: wire.AnalysisProfileID,
		Generation:        wire.Generation,
	}
	if wire.SpaceID != nil {
		spaceID := *wire.SpaceID
		context.SpaceID = &spaceID
	}
	return context
}

func (wire uciWatcherSLOViewWire) view() UCIWatcherSLOView {
	return UCIWatcherSLOView{
		BuildID:        wire.BuildID,
		Context:        wire.Context.context(),
		ManifestDigest: uci.IndexDigest(wire.ManifestDigest),
		AcceptedFSSeq:  wire.AcceptedFSSeq,
		PublishedAt:    wire.PublishedAt,
	}
}

func (wire uciWatcherSLOTimingWire) timing() UCIWatcherSLOTiming {
	timing := UCIWatcherSLOTiming{Origin: wire.Origin, Measured: wire.Measured, Latency: wire.Latency}
	if wire.StartedAt != nil {
		timing.StartedAt = wire.StartedAt.UTC()
	}
	if wire.CompletedAt != nil {
		timing.CompletedAt = wire.CompletedAt.UTC()
	}
	return timing
}

func (wire uciWatcherSLOBatchWire) batch() UCIWatcherSLOBatch {
	return UCIWatcherSLOBatch{
		ID:                   wire.ID,
		Identity:             wire.Identity.identity(),
		ProfileID:            wire.ProfileID,
		ObservedFSSeq:        wire.ObservedFSSeq,
		ChangedFileCount:     wire.ChangedFileCount,
		ChangedBytes:         wire.ChangedBytes,
		ScanOutcome:          wire.ScanOutcome,
		ResultStatus:         wire.ResultStatus,
		Coverage:             wire.Coverage,
		Outcome:              wire.Outcome,
		Reason:               wire.Reason,
		Warmth:               wire.Warmth,
		Scan:                 wire.Scan.timing(),
		StructuralFTS:        wire.StructuralFTS.timing(),
		EmbeddingReadiness:   wire.EmbeddingReadiness.timing(),
		LocalACK:             wire.LocalACK.timing(),
		EmbeddingCounters:    wire.EmbeddingCounters,
		ReembeddedCandidates: wire.ReembeddedCandidates,
		ABefore:              wire.ABefore.view(),
		AAfter:               wire.AAfter.view(),
		BBefore:              wire.BBefore.view(),
		BAfter:               wire.BAfter.view(),
		ABeforeOrigin:        wire.ABeforeOrigin,
		AAfterOrigin:         wire.AAfterOrigin,
		BBeforeOrigin:        wire.BBeforeOrigin,
		BAfterOrigin:         wire.BAfterOrigin,
	}
}

func (wire uciWatcherSLOUnchangedCounterWire) counter() UCIWatcherSLOUnchangedInputCounter {
	return UCIWatcherSLOUnchangedInputCounter{
		Identity:                  wire.Identity.identity(),
		Context:                   wire.Context.context(),
		InputDigest:               wire.InputDigest,
		Unchanged:                 wire.Unchanged,
		EmbeddingCountersOrigin:   wire.EmbeddingCountersOrigin,
		EmbeddingCountersMeasured: wire.EmbeddingCountersMeasured,
		EmbeddingCountersBefore:   wire.EmbeddingCountersBefore,
		EmbeddingCountersAfter:    wire.EmbeddingCountersAfter,
		ReembeddedCandidates:      wire.ReembeddedCandidates,
	}
}
