package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	acceptance "github.com/thebtf/engram/tests/uci/acceptance"
)

const (
	uciWatcherSLORecordEnabledEnv          = "ENGRAM_UCI_WATCHER_SLO_RECORD_ENABLED"
	uciWatcherSLORecordDatabaseDSNEnv      = "ENGRAM_UCI_WATCHER_SLO_DATABASE_DSN"
	uciWatcherSLORecordPathEnv             = "ENGRAM_UCI_WATCHER_SLO_RECORD_PATH"
	uciWatcherSLORecordTimeout             = 20 * time.Minute
	uciWatcherSLORecordCommand             = "go test ./cmd/engram -run '^TestUCIRecordInstalledWatcherSLO$' -count=1 -v -timeout=25m"
	uciWatcherSLORecordRequiredWarmBatches = 100
	uciWatcherSLORecordMaximumAttempts     = 150
)

var errUCIWatcherSLOEmbeddingTerminal = errors.New("installed embedding worker reported a terminal failure")

// TestUCIRecordInstalledWatcherSLO is the caller-owned, opt-in installed
// recorder command. It builds the exact candidate, saves only a disposable A
// worktree, and records normal-client observations. It never calls the direct
// Admit/Begin/Stage/Finalize publication APIs.
func TestUCIRecordInstalledWatcherSLO(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows installed watcher recorder")
	}
	if strings.TrimSpace(os.Getenv(uciWatcherSLORecordEnabledEnv)) != "1" {
		t.Skipf("%s=1 is required to run the installed watcher recorder: %s", uciWatcherSLORecordEnabledEnv, uciWatcherSLORecordCommand)
	}

	recordPath := strings.TrimSpace(os.Getenv(uciWatcherSLORecordPathEnv))
	dsn := strings.TrimSpace(os.Getenv(uciWatcherSLORecordDatabaseDSNEnv))
	provider := &uciInstalledAcceptanceEmbeddingProvider{
		URL:   strings.TrimSpace(os.Getenv(uciRealCorpusEmbeddingURLEnv)),
		Model: strings.TrimSpace(os.Getenv(uciRealCorpusEmbeddingModelEnv)),
		Key:   strings.TrimSpace(os.Getenv(uciRealCorpusEmbeddingAPIKeyEnv)),
	}
	if recordPath == "" || dsn == "" {
		t.Fatal("installed watcher recorder requires an absolute record path and a disposable PostgreSQL DSN")
	}
	if !filepath.IsAbs(recordPath) {
		t.Fatal("installed watcher record path must be absolute")
	}
	if err := uciValidateInstalledAcceptanceEmbeddingProvider(provider); err != nil {
		t.Fatal(err)
	}
	uciInstalledAcceptanceRequireTestPostgres(t, dsn)

	root := uciInstalledAcceptanceCandidateSourceRoot(t)
	if uciInstalledAcceptancePathOverlaps(root, recordPath) {
		t.Fatal("installed watcher record path must be outside the candidate source tree")
	}
	if clean, err := uciWatcherSLOGitClean(t.Context(), root); err != nil || !clean {
		t.Fatalf("installed watcher recorder requires a clean exact candidate: clean=%t err=%v", clean, err)
	}
	deadline, hasDeadline := t.Deadline()
	if hasDeadline && time.Until(deadline) < uciWatcherSLORecordTimeout+5*time.Minute {
		t.Fatalf("installed watcher recorder needs a test deadline of at least %s; rerun with %s", uciWatcherSLORecordTimeout+5*time.Minute, uciWatcherSLORecordCommand)
	}

	sandbox := t.TempDir()
	request := uciInstalledAcceptanceRequest{
		Version:                   uciInstalledAcceptanceVersionV1,
		InstallHarnessVersion:     uciInstallHarnessVersionV1,
		CandidateSourceRoot:       root,
		InstallRoot:               filepath.Join(sandbox, "installed watcher recorder"),
		FixtureRoot:               filepath.Join(sandbox, "watcher recorder worktrees"),
		LocalStateRoot:            filepath.Join(sandbox, "watcher recorder state"),
		TestPostgresDSN:           dsn,
		LoopbackHost:              "127.0.0.1",
		ReservedLoopbackPortCount: 2,
		ReadinessTimeout:          30 * time.Second,
		OperationTimeout:          uciWatcherSLORecordTimeout,
		EmbeddingProvider:         provider,
		ScenarioProbePhase:        uciInstalledAcceptanceScenarioProbeBeforeBaseWatcher,
		Fixture: uciInstalledAcceptanceFixture{
			RelativePath:  uciInstalledAcceptanceRelativePath,
			SharedSymbol:  uciInstalledAcceptanceSharedSymbol,
			PrimarySource: uciInstalledAcceptanceAlphaSource,
			LinkedSource:  uciInstalledAcceptanceBetaSource,
			PrimaryCallee: uciInstalledAcceptanceAlphaCallee,
			LinkedCallee:  uciInstalledAcceptanceBetaCallee,
		},
	}
	var input acceptance.UCIWatcherSLOInput
	request.ScenarioProbe = func(ctx context.Context, live uciInstalledAcceptanceScenarioRuntime) (map[string]uciInstalledAcceptanceScenarioEvidence, error) {
		measured, err := uciRecordInstalledWatcherSLO(ctx, live, provider)
		if err != nil {
			return nil, err
		}
		input = measured
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), request.OperationTimeout)
	defer cancel()
	if _, err := runUCIInstalledAcceptance(ctx, request); err != nil {
		t.Fatal(err)
	}
	encoded, err := acceptance.EncodeUCIWatcherSLOInput(input)
	if err != nil {
		t.Fatalf("encode installed watcher SLO evidence: %v", err)
	}
	if err := uciWriteInstalledWatcherSLORecord(recordPath, encoded); err != nil {
		t.Fatalf("write installed watcher SLO evidence: %v", err)
	}
	report, err := acceptance.CalculateUCIWatcherSLOReport(input)
	if err != nil {
		t.Fatalf("account installed watcher SLO evidence: %v", err)
	}
	if !report.Passed {
		t.Fatal("installed watcher SLO evidence did not satisfy the accepted profile")
	}
}

func uciRecordInstalledWatcherSLO(ctx context.Context, live uciInstalledAcceptanceScenarioRuntime, provider *uciInstalledAcceptanceEmbeddingProvider) (acceptance.UCIWatcherSLOInput, error) {
	if live.Authority == nil || live.Authority.profile == nil || live.Authority.source == nil || live.ClientA == nil || live.ClientB == nil || provider == nil {
		return acceptance.UCIWatcherSLOInput{}, errors.New("installed watcher recorder runtime is incomplete")
	}
	if err := uciWatcherSLOMarkAuxiliaryInactive(ctx, live.Authority); err != nil {
		return acceptance.UCIWatcherSLOInput{}, err
	}
	candidate, err := uciWatcherSLOCandidate(ctx, live.Request.CandidateSourceRoot, live.Candidates)
	if err != nil {
		return acceptance.UCIWatcherSLOInput{}, err
	}
	selectionA := live.Selections[uciInstalledAcceptanceClientA]
	selectionB := live.Selections[uciInstalledAcceptanceClientB]
	beforeA := live.Publications[uciInstalledAcceptanceClientA]
	beforeB := live.Publications[uciInstalledAcceptanceClientB]
	if selectionA.contextHandle == "" || selectionB.contextHandle == "" || beforeA.viewID == "" || beforeB.viewID == "" {
		return acceptance.UCIWatcherSLOInput{}, errors.New("installed watcher recorder has no A/B baseline")
	}

	batches := make([]acceptance.UCIWatcherSLOBatch, 0, uciWatcherSLORecordMaximumAttempts)
	previousSource, err := os.ReadFile(filepath.Join(live.Worktrees.primaryRoot, filepath.FromSlash(live.Request.Fixture.RelativePath)))
	if err != nil {
		return acceptance.UCIWatcherSLOInput{}, fmt.Errorf("read installed watcher A baseline: %w", err)
	}
	healthyWarmBatches := 0
	for sequence := 1; sequence <= uciWatcherSLORecordMaximumAttempts && healthyWarmBatches < uciWatcherSLORecordRequiredWarmBatches; sequence++ {
		warmth := "warm"
		if sequence == 1 {
			warmth = "cold"
		}
		batch, nextSource, measureErr := uciRecordInstalledWatcherSLOBatch(ctx, live, selectionA, selectionB, beforeA, beforeB, previousSource, sequence, warmth)
		if measureErr != nil {
			return acceptance.UCIWatcherSLOInput{}, measureErr
		}
		afterA, statusErr := uciWatcherSLOCurrentPublication(ctx, live.ClientA, selectionA)
		if statusErr != nil || afterA.viewID != batch.AAfter.Context.ViewID || afterA.generation != batch.AAfter.Context.Generation {
			return acceptance.UCIWatcherSLOInput{}, errors.New("normal client status changed after watcher evidence was observed")
		}
		batches = append(batches, batch)
		if batch.Warmth == "warm" && batch.Outcome == "healthy" {
			healthyWarmBatches++
		}
		previousSource = nextSource
		beforeA = afterA
		selectionA.runID = afterA.runID
	}
	if healthyWarmBatches < uciWatcherSLORecordRequiredWarmBatches {
		return acceptance.UCIWatcherSLOInput{}, fmt.Errorf("installed watcher recorder reached %d attempts with %d healthy warm samples, want %d", len(batches), healthyWarmBatches, uciWatcherSLORecordRequiredWarmBatches)
	}

	finalA := batches[len(batches)-1].AAfter
	environment, profile, err := uciWatcherSLOEnvironmentAndProfile(ctx, live, provider, finalA)
	if err != nil {
		return acceptance.UCIWatcherSLOInput{}, err
	}
	identity := uci.UCISLOSampleIdentity{Candidate: candidate, Environment: environment}
	for index := range batches {
		batches[index].Identity = identity
	}
	for _, batch := range batches {
		if batch.ChangedBytes > profile.ChangedBytes {
			profile.ChangedBytes = batch.ChangedBytes
		}
	}
	unchanged, err := uciRecordInstalledWatcherSLOUnchanged(ctx, live, selectionA, beforeA, identity)
	if err != nil {
		return acceptance.UCIWatcherSLOInput{}, err
	}
	return acceptance.UCIWatcherSLOInput{
		SchemaVersion:          acceptance.UCIWatcherSLOSchemaVersion,
		Candidate:              candidate,
		Environment:            environment,
		Profile:                profile,
		Batches:                batches,
		UnchangedInputCounters: []acceptance.UCIWatcherSLOUnchangedInputCounter{unchanged},
	}, nil
}

func uciRecordInstalledWatcherSLOBatch(ctx context.Context, live uciInstalledAcceptanceScenarioRuntime, selectionA, selectionB uciInstalledAcceptanceSelection, beforeA, beforeB uciInstalledAcceptancePublication, previousSource []byte, sequence int, warmth string) (acceptance.UCIWatcherSLOBatch, []byte, error) {
	aBefore, beforeEmbedding, err := uciWatcherSLOCurrentView(ctx, live.Authority, live.ClientA, selectionA, beforeA)
	if err != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, fmt.Errorf("observe A before save: %w", err)
	}
	bBefore, _, err := uciWatcherSLOCurrentView(ctx, live.Authority, live.ClientB, selectionB, beforeB)
	if err != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, fmt.Errorf("observe B before A save: %w", err)
	}

	functionName := fmt.Sprintf("UCIWatcherSLO%03d", sequence)
	nextSource := []byte("package fixture\n\nfunc " + live.Request.Fixture.SharedSymbol + "() string { return " + functionName + "() }\nfunc " + functionName + "() string { return \"" + functionName + "\" }\n")
	path := filepath.Join(live.Worktrees.primaryRoot, filepath.FromSlash(live.Request.Fixture.RelativePath))
	started := time.Now().UTC()
	if err := os.WriteFile(path, nextSource, 0o600); err != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, fmt.Errorf("save bounded A watcher source: %w", err)
	}
	publication, response, structuralCompleted, err := uciWatcherSLOAwaitSearchable(ctx, live.ClientA, selectionA, beforeA, functionName, live.Request.Fixture.RelativePath)
	if err != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, err
	}
	selectionA.runID = publication.runID
	embedding, embeddedPublication, embeddingCompleted, embeddingErr := uciWatcherSLOAwaitEmbeddingReady(ctx, live.Authority, live.ClientA, selectionA, publication)
	terminalEmbeddingFailure := false
	if embeddingErr != nil {
		if !errors.Is(embeddingErr, errUCIWatcherSLOEmbeddingTerminal) {
			return acceptance.UCIWatcherSLOBatch{}, nil, embeddingErr
		}
		terminalEmbeddingFailure = true
		embeddedPublication = publication
	}
	embeddingTiming := acceptance.UCIWatcherSLOTiming{Origin: acceptance.UCIWatcherSLOOriginUnknownNotMeasured}
	if !terminalEmbeddingFailure {
		embeddingTiming = uciWatcherSLOInstalledTiming(acceptance.UCIWatcherSLOOriginInstalledEmbeddingStatus, started, embeddingCompleted)
	}
	aAfter, afterEmbedding, err := uciWatcherSLOCurrentView(ctx, live.Authority, live.ClientA, selectionA, embeddedPublication)
	if err != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, fmt.Errorf("observe A after watcher publication: %w", err)
	}
	bAfter, _, err := uciWatcherSLOCurrentView(ctx, live.Authority, live.ClientB, selectionB, beforeB)
	if err != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, fmt.Errorf("independently observe B after A save: %w", err)
	}
	if !uciWatcherSLOViewsEqual(bBefore, bAfter) {
		return acceptance.UCIWatcherSLOBatch{}, nil, errors.New("installed watcher changed B while only A was saved")
	}
	coverage := "unknown"
	if response.Coverage != nil {
		coverage = string(response.Coverage.Structural)
	}
	outcome, reason := "", ""
	if terminalEmbeddingFailure {
		if afterEmbedding.Embedding.ErrorCode == nil && afterEmbedding.Embedding.Coverage != "failed" {
			return acceptance.UCIWatcherSLOBatch{}, nil, errors.New("installed embedding terminal status did not remain observable")
		}
		outcome, reason = "failed", "embedding_terminal_failure"
	} else {
		if afterEmbedding.Embedding.ReadyCandidates != embedding.Embedding.ReadyCandidates || beforeEmbedding.Embedding.ReadyCandidates > afterEmbedding.Embedding.ReadyCandidates {
			return acceptance.UCIWatcherSLOBatch{}, nil, errors.New("installed watcher embedding counter observation is inconsistent")
		}
		outcome, reason = uciWatcherSLOClassifyOutcome(response.Status, coverage, embedding.Embedding.Coverage)
	}

	return acceptance.UCIWatcherSLOBatch{
		ID:                   fmt.Sprintf("installed-watcher-%s-%03d", warmth, sequence),
		ProfileID:            aAfter.Context.AnalysisProfileID,
		ObservedFSSeq:        aAfter.AcceptedFSSeq,
		ChangedFileCount:     1,
		ChangedBytes:         uciWatcherSLOChangedBytes(previousSource, nextSource),
		ScanOutcome:          uci.IndexScanComplete,
		ResultStatus:         string(response.Status),
		Coverage:             coverage,
		Outcome:              outcome,
		Reason:               reason,
		Warmth:               warmth,
		Scan:                 acceptance.UCIWatcherSLOTiming{Origin: acceptance.UCIWatcherSLOOriginUnknownNotMeasured},
		StructuralFTS:        uciWatcherSLOInstalledTiming(acceptance.UCIWatcherSLOOriginInstalledClientSearch, started, structuralCompleted),
		EmbeddingReadiness:   embeddingTiming,
		LocalACK:             acceptance.UCIWatcherSLOTiming{Origin: acceptance.UCIWatcherSLOOriginUnknownNotMeasured},
		EmbeddingCounters:    acceptance.UCIWatcherSLOEmbeddingCounters{Origin: acceptance.UCIWatcherSLOOriginInstalledEmbeddingStatus, Measured: true, Before: int64(beforeEmbedding.Embedding.ReadyCandidates), After: int64(afterEmbedding.Embedding.ReadyCandidates)},
		ReembeddedCandidates: int(afterEmbedding.Embedding.ReadyCandidates - beforeEmbedding.Embedding.ReadyCandidates),
		ABefore:              aBefore,
		AAfter:               aAfter,
		BBefore:              bBefore,
		BAfter:               bAfter,
		ABeforeOrigin:        acceptance.UCIWatcherSLOOriginInstalledStatusAndView,
		AAfterOrigin:         acceptance.UCIWatcherSLOOriginInstalledStatusAndView,
		BBeforeOrigin:        acceptance.UCIWatcherSLOOriginInstalledStatusAndView,
		BAfterOrigin:         acceptance.UCIWatcherSLOOriginInstalledStatusAndView,
	}, nextSource, nil
}

func uciWatcherSLOAwaitSearchable(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, previous uciInstalledAcceptancePublication, functionName, relativePath string) (uciInstalledAcceptancePublication, uci.QueryResponse, time.Time, error) {
	for {
		publication, err := uciWaitForInstalledAcceptanceWatcherPublication(ctx, client, selection, previous)
		if err != nil {
			return uciInstalledAcceptancePublication{}, uci.QueryResponse{}, time.Time{}, fmt.Errorf("await installed watcher discovery: %w", err)
		}
		watched := selection
		watched.runID = publication.runID
		if err := uciRequireInstalledAcceptanceWatcherCanary(ctx, client, watched, publication, functionName, relativePath, true); err != nil {
			if errors.Is(err, errUCIInstalledAcceptanceWatcherCanaryMissing) {
				previous = publication
				continue
			}
			return uciInstalledAcceptancePublication{}, uci.QueryResponse{}, time.Time{}, fmt.Errorf("verify installed watcher search: %w", err)
		}
		payload, err := client.Tool(ctx, "codebase_search", map[string]any{"context_handle": selection.contextHandle, "query": functionName, "path_prefix": relativePath, "limit": 10})
		if err != nil {
			return uciInstalledAcceptancePublication{}, uci.QueryResponse{}, time.Time{}, err
		}
		response, err := uciDecodeInstalledAcceptanceQuery(payload)
		if err != nil {
			return uciInstalledAcceptancePublication{}, uci.QueryResponse{}, time.Time{}, err
		}
		if !uciInstalledAcceptanceQueryMatchesPublication(response, publication) {
			return uciInstalledAcceptancePublication{}, uci.QueryResponse{}, time.Time{}, errors.New("normal client search did not retain the watcher publication")
		}
		return publication, response, time.Now().UTC(), nil
	}
}

func uciWatcherSLOAwaitEmbeddingReady(ctx context.Context, authority *uciInstalledAcceptanceAuthority, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, expected uciInstalledAcceptancePublication) (uciRealCorpusEmbeddingStatus, uciInstalledAcceptancePublication, time.Time, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		view, status, err := uciWatcherSLOCurrentView(ctx, authority, client, selection, expected)
		if err != nil {
			return uciRealCorpusEmbeddingStatus{}, uciInstalledAcceptancePublication{}, time.Time{}, err
		}
		if status.Embedding.ErrorCode != nil || status.Embedding.Coverage == "failed" {
			return status, uciInstalledAcceptancePublication{}, time.Time{}, errUCIWatcherSLOEmbeddingTerminal
		}
		if uciWatcherSLOEmbeddingReady(status) {
			return status, uciInstalledAcceptancePublication{sourceID: view.Context.SourceID, checkoutID: view.Context.CheckoutID, viewID: view.Context.ViewID, profileID: view.Context.AnalysisProfileID, generation: view.Context.Generation, runID: expected.runID, freshnessState: "observed_current"}, time.Now().UTC(), nil
		}
		select {
		case <-ctx.Done():
			return status, uciInstalledAcceptancePublication{}, time.Time{}, fmt.Errorf("await installed embedding readiness: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func uciWatcherSLOEmbeddingReady(status uciRealCorpusEmbeddingStatus) bool {
	return status.Embedding.ErrorCode == nil &&
		status.Embedding.Coverage == "complete" &&
		status.Embedding.TotalCandidates > 0 &&
		status.Embedding.ReadyCandidates == status.Embedding.TotalCandidates &&
		status.Embedding.PendingJobs == 0 &&
		status.Embedding.JobState != nil && *status.Embedding.JobState == "succeeded"
}

func uciWatcherSLOCurrentView(ctx context.Context, authority *uciInstalledAcceptanceAuthority, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, expected uciInstalledAcceptancePublication) (acceptance.UCIWatcherSLOView, uciRealCorpusEmbeddingStatus, error) {
	if authority == nil || authority.store == nil || client == nil || selection.contextHandle == "" || expected.runID == "" {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, errors.New("installed watcher status target is incomplete")
	}
	payload, err := client.Tool(ctx, "codebase_status", map[string]any{"context_handle": selection.contextHandle})
	if err != nil {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, err
	}
	status, err := uciDecodeInstalledAcceptanceStatus(payload)
	if err != nil {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, err
	}
	publication, err := uciInstalledAcceptanceStatusPublication(status, uciInstalledAcceptanceSelection{contextHandle: selection.contextHandle, runID: expected.runID})
	if err != nil || !uciInstalledAcceptanceSameViewPublication(publication, expected) {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, errors.New("normal client status did not retain the expected current View")
	}
	var embedding uciRealCorpusEmbeddingStatus
	if err := json.Unmarshal(payload, &embedding); err != nil {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, err
	}
	var row struct {
		BuildID        string     `gorm:"column:build_id"`
		ManifestDigest string     `gorm:"column:manifest_digest"`
		ObservedFSSeq  int64      `gorm:"column:observed_fs_seq"`
		PublishedAt    *time.Time `gorm:"column:published_at"`
	}
	if err := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT job.job_id AS build_id, view_row.manifest_digest, view_row.observed_fs_seq, view_row.published_at
		FROM ci_views AS view_row
		JOIN ci_jobs AS job ON job.result_view_id = view_row.view_id
		WHERE view_row.view_id = ? AND view_row.source_id = ? AND view_row.checkout_id = ? AND view_row.profile_id = ? AND view_row.generation = ?`,
		publication.viewID, publication.sourceID, publication.checkoutID, publication.profileID, publication.generation).Scan(&row).Error; err != nil {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, err
	}
	if row.BuildID == "" || row.ManifestDigest == "" || row.PublishedAt == nil || row.PublishedAt.IsZero() {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, errors.New("authoritative durable View fields are incomplete")
	}
	return acceptance.UCIWatcherSLOView{
		BuildID: row.BuildID,
		Context: uci.ContextRef{
			SourceID: publication.sourceID, CheckoutID: publication.checkoutID, ViewID: publication.viewID,
			AnalysisProfileID: publication.profileID, Generation: publication.generation,
		},
		ManifestDigest: uci.IndexDigest(row.ManifestDigest),
		AcceptedFSSeq:  row.ObservedFSSeq,
		PublishedAt:    row.PublishedAt.UTC(),
	}, embedding, nil
}

func uciWatcherSLOCurrentPublication(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection) (uciInstalledAcceptancePublication, error) {
	status, err := uciInstalledAcceptanceStatusForSelection(ctx, client, selection)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	return uciInstalledAcceptanceStatusPublication(status, uciInstalledAcceptanceSelection{contextHandle: selection.contextHandle, runID: status.runID})
}

func uciWatcherSLOEnvironmentAndProfile(ctx context.Context, live uciInstalledAcceptanceScenarioRuntime, provider *uciInstalledAcceptanceEmbeddingProvider, finalA acceptance.UCIWatcherSLOView) (uci.UCISLOEnvironment, uci.UCISLOProfile, error) {
	if live.Authority == nil || live.Authority.store == nil || live.Authority.source == nil || live.Authority.profile == nil || provider == nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, errors.New("installed watcher environment is incomplete")
	}
	var databaseVersion string
	var databaseSize int64
	if err := live.Authority.store.GetDB().WithContext(ctx).Raw("SHOW server_version").Scan(&databaseVersion).Error; err != nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, err
	}
	if err := live.Authority.store.GetDB().WithContext(ctx).Raw(`SELECT COALESCE(SUM(pg_total_relation_size((quote_ident(schemaname) || '.' || quote_ident(tablename))::regclass)), 0)::bigint FROM pg_tables WHERE schemaname = current_schema()`).Scan(&databaseSize).Error; err != nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, err
	}
	textFiles, linesOfCode, err := uciWatcherSLOCorpusCounts(live.Worktrees.primaryRoot)
	if err != nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, err
	}
	var active, inactive int64
	if err := live.Authority.store.GetDB().WithContext(ctx).Model(&gormdb.UCICheckout{}).Where("source_id = ? AND state IN ?", live.Authority.source.SourceID, []gormdb.UCICheckoutState{gormdb.UCICheckoutRegistered, gormdb.UCICheckoutWatching, gormdb.UCICheckoutCatchingUp}).Count(&active).Error; err != nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, err
	}
	if err := live.Authority.store.GetDB().WithContext(ctx).Model(&gormdb.UCICheckout{}).Where("source_id = ? AND state = ?", live.Authority.source.SourceID, gormdb.UCICheckoutUnregistered).Count(&inactive).Error; err != nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, err
	}
	if active < 5 || inactive < 1 {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, errors.New("installed watcher worktree coverage does not satisfy the accepted profile")
	}
	var persisted struct {
		ProviderRef string `gorm:"column:provider_ref"`
		Model       string `gorm:"column:model"`
	}
	if err := live.Authority.store.GetDB().WithContext(ctx).Raw("SELECT provider_ref, model FROM ci_embedding_profiles WHERE analysis_profile_id = ? ORDER BY embedding_profile_id ASC LIMIT 1", live.Authority.profile.ProfileID).Scan(&persisted).Error; err != nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, err
	}
	providerRef := "sha256:" + uciInstalledAcceptanceStringDigest(provider.URL)
	if persisted.ProviderRef != providerRef || persisted.Model != provider.Model {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, errors.New("installed watcher embedding readiness did not use the caller-configured provider")
	}
	return uci.UCISLOEnvironment{
			Host:     uci.UCISLOHost{ID: "installed-watcher-recorder"},
			Database: uci.UCISLODatabase{ID: "postgresql-isolated-schema", Version: databaseVersion, DataSizeBytes: databaseSize},
			Corpus:   uci.UCISLOCorpus{ID: "installed-watcher-bounded-corpus", ManifestDigest: string(finalA.ManifestDigest)},
			Provider: uci.UCISLOProvider{ID: providerRef, Model: provider.Model, Status: "healthy"},
		}, uci.UCISLOProfile{
			ID: live.Authority.profile.ProfileID, TextFileCount: textFiles, LinesOfCode: linesOfCode,
			ActiveWorktreeCount: int(active), InactiveRegistrationCount: int(inactive), LANRTT: 0,
			ChangedFileCount: 1, ChangedBytes: 1,
		}, nil
}

type uciWatcherSLOUnchangedSnapshot struct {
	view            acceptance.UCIWatcherSLOView
	readyCandidates uint64
}

// uciWatcherSLOObserveUnchangedWindow first establishes that the preceding
// changed View is embedding-ready, then captures two independent quiescent
// installed-client snapshots without scheduling another index run.
func uciWatcherSLOObserveUnchangedWindow(
	awaitReady func() (uciInstalledAcceptancePublication, error),
	waitQuiescent func(uciInstalledAcceptancePublication) (uciInstalledAcceptancePublication, error),
	snapshot func(uciInstalledAcceptancePublication) (uciWatcherSLOUnchangedSnapshot, error),
) (uciWatcherSLOUnchangedSnapshot, uciWatcherSLOUnchangedSnapshot, error) {
	ready, err := awaitReady()
	if err != nil {
		return uciWatcherSLOUnchangedSnapshot{}, uciWatcherSLOUnchangedSnapshot{}, err
	}
	baseline, err := waitQuiescent(ready)
	if err != nil {
		return uciWatcherSLOUnchangedSnapshot{}, uciWatcherSLOUnchangedSnapshot{}, err
	}
	before, err := snapshot(baseline)
	if err != nil {
		return uciWatcherSLOUnchangedSnapshot{}, uciWatcherSLOUnchangedSnapshot{}, err
	}
	afterPublication, err := waitQuiescent(baseline)
	if err != nil {
		return uciWatcherSLOUnchangedSnapshot{}, uciWatcherSLOUnchangedSnapshot{}, err
	}
	if !uciInstalledAcceptanceSameViewPublication(baseline, afterPublication) {
		return uciWatcherSLOUnchangedSnapshot{}, uciWatcherSLOUnchangedSnapshot{}, errors.New("unchanged watcher observation published a new View")
	}
	after, err := snapshot(afterPublication)
	if err != nil {
		return uciWatcherSLOUnchangedSnapshot{}, uciWatcherSLOUnchangedSnapshot{}, err
	}
	return before, after, nil
}

func uciRecordInstalledWatcherSLOUnchanged(ctx context.Context, live uciInstalledAcceptanceScenarioRuntime, selection uciInstalledAcceptanceSelection, before uciInstalledAcceptancePublication, identity uci.UCISLOSampleIdentity) (acceptance.UCIWatcherSLOUnchangedInputCounter, error) {
	baselineBefore, baselineAfter, err := uciWatcherSLOObserveUnchangedWindow(
		func() (uciInstalledAcceptancePublication, error) {
			_, ready, _, readyErr := uciWatcherSLOAwaitEmbeddingReady(ctx, live.Authority, live.ClientA, selection, before)
			return ready, readyErr
		},
		func(expected uciInstalledAcceptancePublication) (uciInstalledAcceptancePublication, error) {
			return uciWaitForInstalledAcceptanceRestartQuiescence(ctx, live.ClientA, selection, expected)
		},
		func(expected uciInstalledAcceptancePublication) (uciWatcherSLOUnchangedSnapshot, error) {
			view, status, snapshotErr := uciWatcherSLOCurrentView(ctx, live.Authority, live.ClientA, selection, expected)
			if snapshotErr != nil {
				return uciWatcherSLOUnchangedSnapshot{}, snapshotErr
			}
			if !uciWatcherSLOEmbeddingReady(status) {
				return uciWatcherSLOUnchangedSnapshot{}, errors.New("unchanged watcher baseline is not embedding-ready")
			}
			return uciWatcherSLOUnchangedSnapshot{view: view, readyCandidates: status.Embedding.ReadyCandidates}, nil
		},
	)
	if err != nil {
		return acceptance.UCIWatcherSLOUnchangedInputCounter{}, err
	}
	if !uciWatcherSLOViewsEqual(baselineBefore.view, baselineAfter.view) || baselineBefore.readyCandidates != baselineAfter.readyCandidates {
		return acceptance.UCIWatcherSLOUnchangedInputCounter{}, errors.New("unchanged installed input changed its View or embedding-ready counter")
	}
	return acceptance.UCIWatcherSLOUnchangedInputCounter{
		Identity: identity, Context: baselineAfter.view.Context, InputDigest: string(baselineAfter.view.ManifestDigest), Unchanged: true,
		EmbeddingCountersOrigin: acceptance.UCIWatcherSLOOriginInstalledEmbeddingStatus, EmbeddingCountersMeasured: true,
		EmbeddingCountersBefore: int64(baselineBefore.readyCandidates), EmbeddingCountersAfter: int64(baselineAfter.readyCandidates),
		ReembeddedCandidates: 0,
	}, nil
}

func uciWatcherSLOMarkAuxiliaryInactive(ctx context.Context, authority *uciInstalledAcceptanceAuthority) error {
	checkout := authority.checkouts["auxiliary-4"]
	if checkout == nil {
		return errors.New("installed watcher recorder auxiliary checkout is unavailable")
	}
	result := authority.store.GetDB().WithContext(ctx).Model(&gormdb.UCICheckout{}).Where("checkout_id = ? AND state = ?", checkout.CheckoutID, gormdb.UCICheckoutRegistered).Update("state", gormdb.UCICheckoutUnregistered)
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.New("mark installed watcher auxiliary checkout inactive")
	}
	return nil
}

func uciWatcherSLOCandidate(ctx context.Context, root string, candidates map[string]uciInstallHarnessCommand) (uci.UCISLOCandidate, error) {
	branch, err := uciReadInstalledAcceptanceGit(ctx, root, "branch", "--show-current")
	if err != nil || branch == "" {
		return uci.UCISLOCandidate{}, errors.New("resolve installed watcher candidate branch")
	}
	commit, err := uciReadInstalledAcceptanceGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return uci.UCISLOCandidate{}, err
	}
	tree, err := uciReadInstalledAcceptanceGit(ctx, root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return uci.UCISLOCandidate{}, err
	}
	artifacts := []struct {
		role string
		name string
	}{{"daemon", "engram-daemon"}, {"server", "engram-server"}, {"parser", "uci-parser"}}
	digests := make([]uci.UCISLOArtifactDigest, 0, len(artifacts))
	for _, artifact := range artifacts {
		candidate, found := candidates[artifact.role]
		if !found {
			return uci.UCISLOCandidate{}, errors.New("installed watcher candidate artifact is unavailable")
		}
		digest, err := uciInstalledAcceptanceFileSHA256(candidate.Executable)
		if err != nil {
			return uci.UCISLOCandidate{}, err
		}
		digests = append(digests, uci.UCISLOArtifactDigest{Name: artifact.name, Digest: "sha256:" + digest})
	}
	return uci.UCISLOCandidate{Branch: branch, Commit: commit, Tree: tree, ArtifactDigests: digests}, nil
}

func uciWatcherSLOCorpusCounts(root string) (int, int, error) {
	files, lines := 0, 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		lines += strings.Count(string(contents), "\n")
		return nil
	})
	if err != nil || files == 0 || lines == 0 {
		return 0, 0, errors.New("measure installed watcher bounded corpus")
	}
	return files, lines, nil
}

func uciWatcherSLOChangedBytes(previous, next []byte) int64 {
	limit := len(previous)
	if len(next) < limit {
		limit = len(next)
	}
	changed := 0
	for index := range limit {
		if previous[index] != next[index] {
			changed++
		}
	}
	return int64(changed + len(previous) - limit + len(next) - limit)
}

func uciWatcherSLOGitClean(ctx context.Context, root string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "status", "--porcelain=v1")
	command.Dir = root
	command.Stderr = io.Discard
	output, err := command.Output()
	if err != nil {
		return false, fmt.Errorf("inspect installed watcher candidate Git status: %w", err)
	}
	return len(strings.TrimSpace(string(output))) == 0, nil
}

func uciWatcherSLOInstalledTiming(origin acceptance.UCIWatcherSLOOrigin, started, completed time.Time) acceptance.UCIWatcherSLOTiming {
	return acceptance.UCIWatcherSLOTiming{Origin: origin, Measured: true, StartedAt: started, CompletedAt: completed, Latency: completed.Sub(started)}
}

func uciWatcherSLOViewsEqual(left, right acceptance.UCIWatcherSLOView) bool {
	return left.BuildID == right.BuildID && left.Context == right.Context && left.ManifestDigest == right.ManifestDigest && left.AcceptedFSSeq == right.AcceptedFSSeq && left.PublishedAt.Equal(right.PublishedAt)
}

func uciWatcherSLOClassifyOutcome(status uci.QueryResponseStatus, structuralCoverage, embeddingCoverage string) (string, string) {
	if status == uci.QueryStatusOK && structuralCoverage == "complete" && embeddingCoverage == "complete" {
		return "healthy", ""
	}
	return "degraded", fmt.Sprintf("installed watcher structural readiness is %s/%s while embedding readiness is %s", status, structuralCoverage, embeddingCoverage)
}

func uciWriteInstalledWatcherSLORecord(path string, encoded []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".uci-installed-watcher-slo-")
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

func TestUCIInstalledWatcherWaitRejectsStall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	observations := 0
	_, err := uciWaitForInstalledAcceptanceWatcherRun(ctx, "watcher-run-before", func(context.Context) (uciInstalledAcceptanceStatus, error) {
		observations++
		return uciInstalledAcceptanceStatus{runID: "watcher-run-before"}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled watcher wait error = %v, want deadline exceeded", err)
	}
	if observations == 0 {
		t.Fatal("stalled watcher wait made no status observation")
	}
}

func TestUCIWatcherSLOGitCleanAcceptsEmptyPorcelain(t *testing.T) {
	root := t.TempDir()
	if err := exec.CommandContext(t.Context(), "git", "init", "--quiet", root).Run(); err != nil {
		t.Fatalf("initialize clean watcher candidate: %v", err)
	}
	clean, err := uciWatcherSLOGitClean(t.Context(), root)
	if err != nil || !clean {
		t.Fatalf("clean watcher candidate = %t, %v; want true, nil", clean, err)
	}
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatalf("write dirty watcher candidate: %v", err)
	}
	clean, err = uciWatcherSLOGitClean(t.Context(), root)
	if err != nil || clean {
		t.Fatalf("dirty watcher candidate = %t, %v; want false, nil", clean, err)
	}
}

func TestUCIInstalledAcceptanceCheckoutShapeRequiresDistinctAB(t *testing.T) {
	checkouts := map[string]*gormdb.UCICheckout{
		uciInstalledAcceptanceClientA: {CheckoutID: "checkout-a"},
		uciInstalledAcceptanceClientB: {CheckoutID: "checkout-b"},
		"auxiliary-1":                 {CheckoutID: "checkout-auxiliary-1"},
		"auxiliary-2":                 {CheckoutID: "checkout-auxiliary-2"},
		"auxiliary-3":                 {CheckoutID: "checkout-auxiliary-3"},
		"auxiliary-4":                 {CheckoutID: "checkout-auxiliary-4"},
	}
	checkoutIDs, err := uciInstalledAcceptanceCheckoutIDs(checkouts)
	if err != nil || len(checkoutIDs) != 6 {
		t.Fatalf("valid recorder checkout shape = %#v, %v", checkoutIDs, err)
	}
	checkouts[uciInstalledAcceptanceClientB] = &gormdb.UCICheckout{CheckoutID: "checkout-a"}
	if _, err := uciInstalledAcceptanceCheckoutIDs(checkouts); err == nil {
		t.Fatal("shared A/B checkout identity was accepted")
	}
	checkouts[uciInstalledAcceptanceClientB] = &gormdb.UCICheckout{CheckoutID: "checkout-b"}
	delete(checkouts, "auxiliary-4")
	if _, err := uciInstalledAcceptanceCheckoutIDs(checkouts); err == nil {
		t.Fatal("incomplete recorder checkout shape was accepted")
	}
}

func TestUCIInstalledAcceptanceScenarioProbePhaseKeepsBaseDefault(t *testing.T) {
	probe := func(context.Context, uciInstalledAcceptanceScenarioRuntime) (map[string]uciInstalledAcceptanceScenarioEvidence, error) {
		return nil, nil
	}
	request := uciInstalledAcceptanceRequest{ScenarioProbe: probe}
	if uciInstalledAcceptanceScenarioRunsBeforeBaseWatcher(request) {
		t.Fatal("ordinary scenario probe skipped the base lifecycle")
	}
	request.ScenarioProbePhase = uciInstalledAcceptanceScenarioProbeBeforeBaseWatcher
	if !uciInstalledAcceptanceScenarioRunsBeforeBaseWatcher(request) {
		t.Fatal("explicit pre-base scenario probe did not select the recorder lifecycle")
	}
	request.ScenarioProbe = nil
	if uciInstalledAcceptanceScenarioRunsBeforeBaseWatcher(request) {
		t.Fatal("absent scenario probe selected the pre-base lifecycle")
	}
}

func TestUCIWatcherSLOClassifiesPartialSearchAsDegraded(t *testing.T) {
	outcome, reason := uciWatcherSLOClassifyOutcome(uci.QueryStatusPartial, "partial", "complete")
	if outcome != "degraded" || reason == "" {
		t.Fatalf("partial structural result classification = %q, %q", outcome, reason)
	}
	outcome, reason = uciWatcherSLOClassifyOutcome(uci.QueryStatusOK, "complete", "complete")
	if outcome != "healthy" || reason != "" {
		t.Fatalf("complete structural result classification = %q, %q", outcome, reason)
	}
}

func TestUCIWatcherSLOUnchangedWindowWaitsForReadyQuiescentBaseline(t *testing.T) {
	stable := uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", viewID: "view", profileID: "profile", generation: 2}
	step := 0
	before, after, err := uciWatcherSLOObserveUnchangedWindow(
		func() (uciInstalledAcceptancePublication, error) {
			if step != 0 {
				t.Fatalf("ready step = %d, want 0", step)
			}
			step++
			return stable, nil
		},
		func(expected uciInstalledAcceptancePublication) (uciInstalledAcceptancePublication, error) {
			if expected != stable || (step != 1 && step != 3) {
				t.Fatalf("quiescence step=%d expected=%#v", step, expected)
			}
			step++
			return stable, nil
		},
		func(expected uciInstalledAcceptancePublication) (uciWatcherSLOUnchangedSnapshot, error) {
			if expected != stable || (step != 2 && step != 4) {
				t.Fatalf("snapshot step=%d expected=%#v", step, expected)
			}
			step++
			return uciWatcherSLOUnchangedSnapshot{readyCandidates: 7}, nil
		},
	)
	if err != nil || step != 5 || before.readyCandidates != 7 || after.readyCandidates != 7 {
		t.Fatalf("unchanged sequence = before=%#v after=%#v step=%d err=%v", before, after, step, err)
	}

	waits := 0
	_, _, err = uciWatcherSLOObserveUnchangedWindow(
		func() (uciInstalledAcceptancePublication, error) { return stable, nil },
		func(expected uciInstalledAcceptancePublication) (uciInstalledAcceptancePublication, error) {
			waits++
			if expected != stable {
				t.Fatalf("unexpected changed baseline: %#v", expected)
			}
			if waits == 1 {
				return stable, nil
			}
			return uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", viewID: "new-view", profileID: "profile", generation: 3}, nil
		},
		func(uciInstalledAcceptancePublication) (uciWatcherSLOUnchangedSnapshot, error) {
			return uciWatcherSLOUnchangedSnapshot{}, nil
		},
	)
	if err == nil {
		t.Fatal("unchanged window accepted a watcher-published View")
	}
}

func TestUCIWatcherSLOEmbeddingReadyRequiresSettledWorker(t *testing.T) {
	jobState := "succeeded"
	status := uciRealCorpusEmbeddingStatus{}
	status.Embedding.Coverage = "complete"
	status.Embedding.TotalCandidates = 3
	status.Embedding.ReadyCandidates = 3
	status.Embedding.JobState = &jobState
	if !uciWatcherSLOEmbeddingReady(status) {
		t.Fatal("settled embedding worker was not ready")
	}
	status.Embedding.PendingJobs = 1
	if uciWatcherSLOEmbeddingReady(status) {
		t.Fatal("pending embedding worker was accepted as ready")
	}
}
