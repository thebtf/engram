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
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/uci"
)

const (
	uciRealCorpusEnabledEnv               = "ENGRAM_UCI_REAL_CORPUS_ENABLED"
	uciRealCorpusRootEnv                  = "ENGRAM_UCI_REAL_CORPUS_ROOT"
	uciRealCorpusDatabaseDSNEnv           = "ENGRAM_UCI_REAL_CORPUS_DATABASE_DSN"
	uciRealCorpusProviderRefEnv           = "ENGRAM_UCI_REAL_CORPUS_PROVIDER_REF"
	uciRealCorpusPreprocessingRevisionEnv = "ENGRAM_UCI_REAL_CORPUS_PREPROCESSING_REVISION"
	uciRealCorpusRecordPathEnv            = "ENGRAM_UCI_REAL_CORPUS_RECORD_PATH"

	uciRealCorpusGoCallerPath  = "internal/uci/zz_uci_real_corpus_caller.go"
	uciRealCorpusGoCalleePath  = "internal/uci/zz_uci_real_corpus_callee.go"
	uciRealCorpusTSRoot        = "apps/operator-console/composables/zz-uci-real-corpus"
	uciRealCorpusExpectedPath  = "internal/uci/index_admission.go"
	uciRealCorpusQuery         = "Как ссылки на выражения, похожие на приватные адреса, превращаются в безопасные стабильные идентификаторы без потери позиции в исходнике?"
	uciRealCorpusBarrierWaitMS = int64(5_000)
)

var uciRealCorpusCanaries = map[string]string{
	uciRealCorpusGoCallerPath:           "package uci\n\nfunc UCIRealCorpusCaller() string { return UCIRealCorpusCallee() }\n",
	uciRealCorpusGoCalleePath:           "package uci\n\nfunc UCIRealCorpusCallee() string { return \"before\" }\n",
	uciRealCorpusTSRoot + "/remote.ts":  "export function build(input: string): string { return input.trim() }\n",
	uciRealCorpusTSRoot + "/caller.ts":  "import { build as localBuild } from \"./remote.ts\"\nexport function start(input: string): string { return localBuild(input) }\n",
	uciRealCorpusTSRoot + "/bridge.ts":  "export { build as publicBuild } from \"./remote.ts\"\n",
	uciRealCorpusTSRoot + "/widget.tsx": "export function Widget({ title }: { title: string }) { return <span>{title}</span> }\n",
	uciRealCorpusTSRoot + "/screen.tsx": "import { Widget as RemoteWidget } from \"./widget.tsx\"\nexport function Screen({ title }: { title: string }) { return <RemoteWidget title={title} /> }\n",
}

const uciRealCorpusChangedCallee = "package uci\n\nfunc UCIRealCorpusCallee() string { return \"after\" }\n"

type uciRealCorpusCandidate struct {
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
}

type uciRealCorpusProvider struct {
	EndpointDigest         string `json:"endpoint_digest"`
	ProviderRef            string `json:"provider_ref"`
	Model                  string `json:"model"`
	Dimension              int    `json:"dimension"`
	ProviderBatchSize      int    `json:"provider_batch_size"`
	PreprocessingRevision  string `json:"preprocessing_revision"`
	CredentialRetained     bool   `json:"credential_retained"`
	SourceTransferApproved bool   `json:"source_transfer_approved"`
}

type uciRealCorpusCounts struct {
	Memberships           int64            `json:"memberships"`
	PresentMemberships    int64            `json:"present_memberships"`
	ExcludedMemberships   int64            `json:"excluded_memberships"`
	UnreadableMemberships int64            `json:"unreadable_memberships"`
	Artifacts             int64            `json:"artifacts"`
	Definitions           int64            `json:"definitions"`
	References            int64            `json:"references"`
	Chunks                int64            `json:"chunks"`
	StagedParts           int64            `json:"staged_parts"`
	StagedPartBytes       int64            `json:"staged_part_bytes"`
	Edges                 map[string]int64 `json:"edges"`
	EmbeddingCandidates   uint64           `json:"embedding_candidates"`
	ReadyEmbeddings       uint64           `json:"ready_embeddings"`
	PendingEmbeddingJobs  uint64           `json:"pending_embedding_jobs"`
	EmbeddingJobState     string           `json:"embedding_job_state"`
	CoverageJSON          json.RawMessage  `json:"coverage"`
}

type uciRealCorpusSemantic struct {
	QueryDigest       string   `json:"query_digest"`
	ExpectedPath      string   `json:"expected_path"`
	ExpectedRank      int      `json:"expected_rank"`
	RetrievalMode     string   `json:"retrieval_mode"`
	VectorCoverage    float64  `json:"vector_coverage"`
	MatchSources      []string `json:"match_sources"`
	LexicalOverlap    []string `json:"lexical_overlap"`
	CitationReadBack  bool     `json:"citation_read_back"`
	ExposureAvailable bool     `json:"exposure_available"`
}

type uciRealCorpusGraph struct {
	InterfileCalls         int64  `json:"interfile_calls"`
	Imports                int64  `json:"imports"`
	Exports                int64  `json:"exports"`
	References             int64  `json:"references"`
	OutgoingMCPCall        bool   `json:"outgoing_mcp_call"`
	IncomingMCPReverse     bool   `json:"incoming_mcp_reverse"`
	CallerArtifactStable   bool   `json:"caller_artifact_stable"`
	CalleeArtifactChanged  bool   `json:"callee_artifact_changed"`
	ChangedTargetPublished bool   `json:"changed_target_published"`
	InitialTargetDigest    string `json:"initial_target_digest"`
	ChangedTargetDigest    string `json:"changed_target_digest"`
	InitialViewDigest      string `json:"initial_view_digest"`
	ChangedViewDigest      string `json:"changed_view_digest"`
}

type uciRealCorpusRecord struct {
	SchemaVersion string                 `json:"schema_version"`
	RecordedAt    string                 `json:"recorded_at"`
	Candidate     uciRealCorpusCandidate `json:"candidate"`
	Pilot         struct {
		Source          string `json:"source"`
		FullWorktree    bool   `json:"full_worktree"`
		InitialGitClean bool   `json:"initial_git_clean"`
		FinalGitClean   bool   `json:"final_git_clean"`
	} `json:"pilot"`
	Provider    uciRealCorpusProvider `json:"provider"`
	Initial     uciRealCorpusCounts   `json:"initial"`
	AfterChange uciRealCorpusCounts   `json:"after_change"`
	Semantic    uciRealCorpusSemantic `json:"semantic"`
	Graph       uciRealCorpusGraph    `json:"graph"`
	Installed   struct {
		ServerSHA256 string `json:"server_sha256"`
		DaemonSHA256 string `json:"daemon_sha256"`
		ParserSHA256 string `json:"parser_sha256"`
		StandardMCP  bool   `json:"standard_mcp"`
	} `json:"installed"`
	Scope struct {
		SecretsRetained         bool `json:"secrets_retained"`
		SourceBodiesRetained    bool `json:"source_bodies_retained"`
		PrivateLocatorsRetained bool `json:"private_locators_retained"`
		ProductionMutation      bool `json:"production_mutation"`
		ReleaseClaim            bool `json:"release_claim"`
	} `json:"scope"`
}

type uciRealCorpusEmbeddingStatus struct {
	TotalChunks    int64 `json:"total_chunks"`
	EmbeddedChunks int64 `json:"embedded_chunks"`
	Context        struct {
		ViewID string `json:"view_id"`
	} `json:"context"`
	Embedding struct {
		EmbeddingProfileID *string `json:"embedding_profile_id"`
		Coverage           string  `json:"coverage"`
		TotalCandidates    uint64  `json:"total_candidates"`
		ReadyCandidates    uint64  `json:"ready_candidates"`
		PendingJobs        uint64  `json:"pending_jobs"`
		JobState           *string `json:"job_state"`
		ErrorCode          *string `json:"error_code"`
	} `json:"embedding"`
}

type uciRealCorpusEdgeRow struct {
	SourceArtifact string `gorm:"column:source_artifact"`
	TargetArtifact string `gorm:"column:target_artifact"`
	TargetSymbol   string `gorm:"column:target_symbol"`
}

func TestUCIRealCorpusInstalledProviderLifecycle(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows real-corpus installed acceptance")
	}
	if strings.TrimSpace(os.Getenv(uciRealCorpusEnabledEnv)) != "1" {
		t.Skip("real-corpus installed acceptance requires ENGRAM_UCI_REAL_CORPUS_ENABLED=1")
	}

	root := uciRealCorpusRequiredPath(t, uciRealCorpusRootEnv)
	dsn := strings.TrimSpace(os.Getenv(uciRealCorpusDatabaseDSNEnv))
	if dsn == "" {
		t.Fatal("real-corpus installed acceptance requires a disposable PostgreSQL DSN")
	}
	uciInstalledAcceptanceRequireTestPostgres(t, dsn)
	providerURL := strings.TrimSpace(os.Getenv("ENGRAM_EMBEDDING_URL"))
	providerModel := strings.TrimSpace(os.Getenv("ENGRAM_EMBEDDING_MODEL"))
	providerRef := strings.TrimSpace(os.Getenv(uciRealCorpusProviderRefEnv))
	preprocessing := strings.TrimSpace(os.Getenv(uciRealCorpusPreprocessingRevisionEnv))
	if providerURL == "" || providerModel == "" || providerRef == "" || preprocessing == "" {
		t.Fatal("real-corpus installed acceptance requires explicit provider URL, model, provider ref, and preprocessing revision")
	}
	if clean, err := uciRealCorpusGitClean(root); err != nil || !clean {
		t.Fatalf("real-corpus pilot must start from a clean full worktree: clean=%t err=%v", clean, err)
	}

	recordPath := strings.TrimSpace(os.Getenv(uciRealCorpusRecordPathEnv))
	if recordPath != "" {
		if !filepath.IsAbs(recordPath) || uciInstalledAcceptancePathOverlaps(root, recordPath) {
			t.Fatal("real-corpus record path must be absolute and outside the pilot worktree")
		}
	}

	sandbox := t.TempDir()
	request := uciInstalledAcceptanceRequest{
		Version:                   uciInstalledAcceptanceVersionV1,
		InstallHarnessVersion:     uciInstallHarnessVersionV1,
		CandidateSourceRoot:       root,
		InstallRoot:               filepath.Join(sandbox, "installed UCI real corpus Кириллица"),
		FixtureRoot:               filepath.Join(sandbox, "real corpus build Кириллица"),
		LocalStateRoot:            filepath.Join(sandbox, "real corpus state Кириллица"),
		TestPostgresDSN:           dsn,
		LoopbackHost:              "127.0.0.1",
		ReservedLoopbackPortCount: 2,
		ReadinessTimeout:          30 * time.Second,
		OperationTimeout:          90 * time.Minute,
	}
	ctx, cancel := context.WithTimeout(context.Background(), request.OperationTimeout)
	defer cancel()
	record, err := runUCIRealCorpusInstalledAcceptance(ctx, request, providerURL, providerModel, providerRef, preprocessing)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Pilot.FinalGitClean || !record.Pilot.FullWorktree || !record.Installed.StandardMCP {
		t.Fatal("real-corpus acceptance did not restore a clean full installed pilot")
	}
	if recordPath != "" {
		encoded, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, '\n')
		if err := os.WriteFile(recordPath, encoded, 0o600); err != nil {
			t.Fatalf("write real-corpus acceptance record: %v", err)
		}
	}
}

func runUCIRealCorpusInstalledAcceptance(ctx context.Context, request uciInstalledAcceptanceRequest, providerURL, providerModel, providerRef, preprocessing string) (record uciRealCorpusRecord, retErr error) {
	root, err := uciInstalledAcceptancePhysicalPath(request.CandidateSourceRoot)
	if err != nil {
		return record, err
	}
	if err := os.Mkdir(request.FixtureRoot, 0o700); err != nil {
		return record, err
	}
	if err := os.Mkdir(request.LocalStateRoot, 0o700); err != nil {
		return record, err
	}
	candidates, err := uciBuildInstalledAcceptanceCandidates(ctx, root, filepath.Join(request.FixtureRoot, "candidate-build"))
	if err != nil {
		return record, err
	}
	candidateCommit, err := uciRealCorpusGitValue(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return record, err
	}
	candidateTree, err := uciRealCorpusGitValue(ctx, root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return record, err
	}
	anchorProjectID, err := uciRealCorpusAnchorProjectID(root)
	if err != nil {
		return record, err
	}
	defer func() {
		clean, cleanErr := uciRealCorpusGitClean(root)
		if cleanErr != nil {
			retErr = errors.Join(retErr, cleanErr)
			return
		}
		record.Pilot.FinalGitClean = clean
	}()
	if err := uciRealCorpusWriteCanaries(root); err != nil {
		return record, err
	}
	defer func() {
		if cleanupErr := uciRealCorpusRemoveCanaries(root); cleanupErr != nil {
			retErr = errors.Join(retErr, cleanupErr)
		}
	}()

	parserBundleDigest := uciInstalledAcceptanceParserBundleDigest()
	worktrees := uciInstalledAcceptanceWorktreesFixture{primaryRoot: root, linkedRoot: root, head: candidateCommit}
	var authority *uciInstalledAcceptanceAuthority
	var installation *uciInstallHarnessInstallation
	var reservations []*uciInstalledAcceptanceReservation
	activeDaemonPID := 0
	defer func() {
		stopErr := uciStopInstalledAcceptanceDaemon(filepath.Join(request.LocalStateRoot, "temp"), activeDaemonPID)
		if stopErr != nil {
			retErr = errors.Join(retErr, stopErr)
		} else if activeDaemonPID > 0 {
			if waitErr := uciWaitInstalledAcceptanceProcessExit(activeDaemonPID, 15*time.Second); waitErr != nil {
				retErr = errors.Join(retErr, waitErr)
			}
		}
		if installation != nil {
			if closeErr := installation.Close(); closeErr != nil {
				retErr = errors.Join(retErr, closeErr)
			}
		}
		for _, reservation := range reservations {
			if reservation != nil && reservation.listener != nil {
				_ = reservation.listener.Close()
			}
		}
		if authority != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			retErr = errors.Join(retErr, authority.Close(cleanupCtx))
			cancel()
		}
	}()

	authority, err = uciPrepareInstalledAcceptanceAuthority(ctx, request.TestPostgresDSN, worktrees, parserBundleDigest, anchorProjectID)
	if err != nil {
		return record, err
	}
	if err := uciAssertInstalledAcceptanceNoProjection(ctx, authority); err != nil {
		return record, err
	}
	installResult, err := runUCIInstallHarness(ctx, uciInstallHarnessRequest{
		Version:          request.InstallHarnessVersion,
		Scenario:         uciInstallHarnessScenarioMaterialize,
		InstallRoot:      request.InstallRoot,
		Server:           candidates["server"],
		Daemon:           candidates["daemon"],
		Parser:           candidates["parser"],
		ReadinessTimeout: request.ReadinessTimeout,
	})
	if err != nil {
		return record, err
	}
	installation = installResult.Installation
	if installation == nil {
		return record, errors.New("real-corpus installation is unavailable")
	}
	parserPath, err := installation.Executable("parser")
	if err != nil {
		return record, err
	}
	daemonPath, err := installation.Executable("daemon")
	if err != nil {
		return record, err
	}
	reservations, err = uciReserveInstalledAcceptanceLoopback(request.LoopbackHost, request.ReservedLoopbackPortCount)
	if err != nil {
		return record, err
	}
	serverPort := reservations[0].port
	if err := reservations[0].listener.Close(); err != nil {
		return record, err
	}
	reservations[0].listener = nil

	serverEnvironment, clientEnvironment, err := uciInstalledAcceptanceEnvironment(request, authority, serverPort, parserBundleDigest, parserPath)
	if err != nil {
		return record, err
	}
	serverEnvironment = append(serverEnvironment,
		"ENGRAM_EMBEDDING_URL="+providerURL,
		"ENGRAM_EMBEDDING_MODEL="+providerModel,
		"ENGRAM_EMBEDDING_API_KEY="+os.Getenv("ENGRAM_EMBEDDING_API_KEY"),
	)
	if _, err := installation.Start(ctx, uciInstalledHarnessLaunchRequest{Role: "server", Environment: serverEnvironment}); err != nil {
		return record, err
	}
	readinessCtx, cancelReadiness := context.WithTimeout(ctx, request.ReadinessTimeout)
	err = uciWaitForInstalledAcceptanceLoopback(readinessCtx, request.LoopbackHost, serverPort)
	cancelReadiness()
	if err != nil {
		return record, err
	}
	clientProcess, err := installation.Start(ctx, uciInstalledHarnessLaunchRequest{
		Role:             "daemon",
		WorkingDirectory: root,
		Environment:      clientEnvironment,
		WithStdio:        true,
	})
	if err != nil {
		return record, err
	}
	client, err := newUCIInstalledAcceptanceMCPClient("real-corpus-client", clientProcess)
	if err != nil {
		return record, err
	}
	if err := client.InitializeAndList(ctx); err != nil {
		return record, err
	}
	if err := uciRequireInstalledAcceptanceTools(client.Transcript()); err != nil {
		return record, err
	}
	activeDaemonPID, err = uciWaitForInstalledAcceptanceDaemonPID(ctx, filepath.Join(request.LocalStateRoot, "temp"), daemonPath)
	if err != nil {
		return record, err
	}
	selection, err := uciSelectInstalledAcceptanceCheckout(ctx, client, uciInstalledAcceptanceClientA, authority)
	if err != nil {
		return record, err
	}
	if selection.viewID != "" {
		return record, errors.New("real-corpus checkout was not fresh before initial index")
	}
	started, err := client.Tool(ctx, "codebase_index", map[string]any{"context_handle": selection.contextHandle, "root": root})
	if err != nil {
		return record, err
	}
	var start struct {
		Status string `json:"status"`
		RunID  string `json:"run_id"`
	}
	if err := json.Unmarshal(started, &start); err != nil || start.Status != "started" || start.RunID == "" {
		return record, errors.New("real-corpus codebase_index did not start")
	}
	selection.runID = start.RunID
	initialPublication, err := uciWaitForRealCorpusPublication(ctx, client, selection)
	if err != nil {
		return record, err
	}
	initialEmbedding, err := uciWaitForRealCorpusEmbeddings(ctx, client, selection, initialPublication.viewID)
	if err != nil {
		return record, err
	}
	if err := uciRealCorpusVerifyProviderProfile(ctx, authority, initialEmbedding, providerRef, providerModel, preprocessing); err != nil {
		return record, err
	}
	initialCounts, err := uciRealCorpusCountsFor(ctx, authority, initialPublication, initialEmbedding)
	if err != nil {
		return record, err
	}
	semantic, err := uciRealCorpusSemanticProof(ctx, client, selection, initialPublication, root)
	if err != nil {
		return record, err
	}
	initialEdge, err := uciRealCorpusCanaryEdge(ctx, authority, initialPublication, uciRealCorpusGoCallerPath, uciRealCorpusGoCalleePath)
	if err != nil {
		return record, err
	}
	graph, err := uciRealCorpusGraphProof(ctx, client, selection, initialPublication, authority, initialEdge)
	if err != nil {
		return record, err
	}

	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(uciRealCorpusGoCalleePath)), []byte(uciRealCorpusChangedCallee), 0o600); err != nil {
		return record, err
	}
	changedPublication, err := uciWaitForInstalledAcceptanceWatcherState(ctx, client, selection, initialPublication, "UCIRealCorpusCallee", uciRealCorpusGoCalleePath, true)
	if err != nil {
		return record, err
	}
	changedEmbedding, err := uciWaitForRealCorpusEmbeddings(ctx, client, selection, changedPublication.viewID)
	if err != nil {
		return record, err
	}
	changedCounts, err := uciRealCorpusCountsFor(ctx, authority, changedPublication, changedEmbedding)
	if err != nil {
		return record, err
	}
	changedEdge, err := uciRealCorpusCanaryEdge(ctx, authority, changedPublication, uciRealCorpusGoCallerPath, uciRealCorpusGoCalleePath)
	if err != nil {
		return record, err
	}
	graph.CallerArtifactStable = initialEdge.SourceArtifact == changedEdge.SourceArtifact
	graph.CalleeArtifactChanged = initialEdge.TargetArtifact != changedEdge.TargetArtifact
	graph.ChangedTargetPublished = changedEdge.TargetSymbol == "func:UCIRealCorpusCallee"
	graph.InitialTargetDigest = uciInstalledAcceptanceStringDigest(initialEdge.TargetArtifact)
	graph.ChangedTargetDigest = uciInstalledAcceptanceStringDigest(changedEdge.TargetArtifact)
	graph.InitialViewDigest = uciInstalledAcceptanceStringDigest(initialPublication.viewID)
	graph.ChangedViewDigest = uciInstalledAcceptanceStringDigest(changedPublication.viewID)
	if !graph.CallerArtifactStable || !graph.CalleeArtifactChanged || !graph.ChangedTargetPublished || initialPublication.viewID == changedPublication.viewID {
		return record, errors.New("real-corpus changed-callee invalidation did not publish the expected new target")
	}

	record.SchemaVersion = "engram.uci-real-corpus/v1"
	record.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	record.Candidate = uciRealCorpusCandidate{Commit: candidateCommit, Tree: candidateTree}
	record.Pilot.Source = "engram-full-candidate-worktree"
	record.Pilot.FullWorktree = initialCounts.Memberships >= 1000 && initialCounts.Artifacts >= 1000
	record.Pilot.InitialGitClean = true
	record.Provider = uciRealCorpusProvider{
		EndpointDigest:         uciInstalledAcceptanceStringDigest(providerURL),
		ProviderRef:            providerRef,
		Model:                  providerModel,
		Dimension:              1536,
		ProviderBatchSize:      uci.DefaultEmbeddingWorkerLimits().ProviderBatchSize,
		PreprocessingRevision:  preprocessing,
		CredentialRetained:     false,
		SourceTransferApproved: true,
	}
	record.Initial = initialCounts
	record.AfterChange = changedCounts
	record.Semantic = semantic
	record.Graph = graph
	for role, destination := range map[string]*string{
		"server": &record.Installed.ServerSHA256,
		"daemon": &record.Installed.DaemonSHA256,
		"parser": &record.Installed.ParserSHA256,
	} {
		candidateHash, hashErr := uciInstalledAcceptanceFileSHA256(candidates[role].Executable)
		if hashErr != nil {
			return record, hashErr
		}
		installedPath, pathErr := installation.Executable(role)
		if pathErr != nil {
			return record, pathErr
		}
		installedHash, hashErr := uciInstalledAcceptanceFileSHA256(installedPath)
		if hashErr != nil {
			return record, hashErr
		}
		if candidateHash != installedHash {
			return record, fmt.Errorf("real-corpus installed %s artifact differs from candidate", role)
		}
		*destination = installedHash
	}
	record.Installed.StandardMCP = client.Transcript().UsedStdio
	record.Scope.SecretsRetained = false
	record.Scope.SourceBodiesRetained = false
	record.Scope.PrivateLocatorsRetained = false
	record.Scope.ProductionMutation = false
	record.Scope.ReleaseClaim = false
	if !record.Pilot.FullWorktree || !record.Installed.StandardMCP {
		return record, errors.New("real-corpus result did not prove a full installed standard-MCP worktree")
	}
	return record, nil
}

func uciWaitForRealCorpusPublication(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection) (uciInstalledAcceptancePublication, error) {
	for {
		payload, err := uciInstalledAcceptanceStatusTool(ctx, client, map[string]any{
			"context_handle": selection.contextHandle,
			"after_barrier": map[string]any{
				"token":   selection.runID,
				"wait_ms": min(uciRealCorpusBarrierWaitMS, uciInstalledAcceptanceBarrierWait(ctx)),
			},
		})
		if err != nil {
			return uciInstalledAcceptancePublication{}, err
		}
		status, err := uciDecodeInstalledAcceptanceStatus(payload)
		if err != nil {
			return uciInstalledAcceptancePublication{}, err
		}
		if status.error != "" {
			return uciInstalledAcceptancePublication{}, fmt.Errorf("real-corpus target index status: %s", uciInstalledAcceptanceSafeErrorDetail(status.error))
		}
		if status.status == "idle" && status.runID == selection.runID && status.context != nil && status.context.viewID != "" {
			publication, err := uciInstalledAcceptanceStatusPublication(status, selection)
			if err != nil {
				return uciInstalledAcceptancePublication{}, err
			}
			if status.freshness == nil || status.freshness.barrier == nil || status.freshness.barrier.state != "satisfied" {
				return uciInstalledAcceptancePublication{}, errors.New("real-corpus target barrier was not satisfied")
			}
			publication.freshnessState = status.freshness.state
			publication.barrierState = status.freshness.barrier.state
			publication.evidenceRecorder = status.evidenceRecorder.state
			return publication, nil
		}
		select {
		case <-ctx.Done():
			return uciInstalledAcceptancePublication{}, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func uciWaitForRealCorpusEmbeddings(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, expectedViewID string) (uciRealCorpusEmbeddingStatus, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		payload, err := client.Tool(ctx, "codebase_status", map[string]any{"context_handle": selection.contextHandle})
		if err != nil {
			return uciRealCorpusEmbeddingStatus{}, err
		}
		var status uciRealCorpusEmbeddingStatus
		if err := json.Unmarshal(payload, &status); err != nil {
			return uciRealCorpusEmbeddingStatus{}, err
		}
		if status.Embedding.ErrorCode != nil && status.Embedding.JobState != nil && *status.Embedding.JobState == "failed_terminal" {
			return status, fmt.Errorf("real-corpus embedding job failed: %s", *status.Embedding.ErrorCode)
		}
		if status.Context.ViewID == expectedViewID && status.Embedding.EmbeddingProfileID != nil && status.Embedding.Coverage == string(uci.IndexCoverageComplete) && status.Embedding.TotalCandidates > 0 && status.Embedding.ReadyCandidates == status.Embedding.TotalCandidates && status.Embedding.PendingJobs == 0 {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-ticker.C:
		}
	}
}

func uciRealCorpusVerifyProviderProfile(ctx context.Context, authority *uciInstalledAcceptanceAuthority, status uciRealCorpusEmbeddingStatus, providerRef, model, preprocessing string) error {
	if authority == nil || authority.store == nil || status.Embedding.EmbeddingProfileID == nil {
		return errors.New("real-corpus persisted embedding profile is unavailable")
	}
	var persisted struct {
		ProviderRef           string `gorm:"column:provider_ref"`
		Model                 string `gorm:"column:model"`
		Dimension             int    `gorm:"column:dimension"`
		PreprocessingRevision string `gorm:"column:preprocessing_revision"`
		IncludeRelativePath   bool   `gorm:"column:include_relative_path"`
	}
	if err := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT provider_ref, model, dimension, preprocessing_revision, include_relative_path
		FROM ci_embedding_profiles
		WHERE embedding_profile_id = ?
	`, *status.Embedding.EmbeddingProfileID).Scan(&persisted).Error; err != nil {
		return err
	}
	if persisted.ProviderRef != providerRef || persisted.Model != model || persisted.Dimension != 1536 || persisted.PreprocessingRevision != preprocessing || !persisted.IncludeRelativePath {
		return errors.New("real-corpus persisted embedding profile does not match the configured provider contract")
	}
	return nil
}

func uciRealCorpusCountsFor(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication, embedding uciRealCorpusEmbeddingStatus) (uciRealCorpusCounts, error) {
	if authority == nil || authority.store == nil || authority.source == nil {
		return uciRealCorpusCounts{}, errors.New("real-corpus count authority is unavailable")
	}
	db := authority.store.GetDB().WithContext(ctx)
	counts := uciRealCorpusCounts{Edges: make(map[string]int64)}
	checkoutID := authority.checkouts[uciInstalledAcceptanceClientA].CheckoutID
	queries := []struct {
		query string
		args  []any
		into  *int64
	}{
		{`SELECT COUNT(*) FROM ci_memberships WHERE checkout_id = ? AND valid_from_generation <= ? AND (valid_to_generation IS NULL OR valid_to_generation > ?)`, []any{checkoutID, publication.generation, publication.generation}, &counts.Memberships},
		{`SELECT COUNT(*) FROM ci_memberships WHERE checkout_id = ? AND file_state = 'present' AND valid_from_generation <= ? AND (valid_to_generation IS NULL OR valid_to_generation > ?)`, []any{checkoutID, publication.generation, publication.generation}, &counts.PresentMemberships},
		{`SELECT COUNT(*) FROM ci_memberships WHERE checkout_id = ? AND file_state = 'excluded' AND valid_from_generation <= ? AND (valid_to_generation IS NULL OR valid_to_generation > ?)`, []any{checkoutID, publication.generation, publication.generation}, &counts.ExcludedMemberships},
		{`SELECT COUNT(*) FROM ci_memberships WHERE checkout_id = ? AND file_state = 'unreadable' AND valid_from_generation <= ? AND (valid_to_generation IS NULL OR valid_to_generation > ?)`, []any{checkoutID, publication.generation, publication.generation}, &counts.UnreadableMemberships},
		{`SELECT COUNT(*) FROM ci_parse_artifacts WHERE source_id = ?`, []any{authority.source.SourceID}, &counts.Artifacts},
		{`SELECT COUNT(*) FROM ci_definitions WHERE artifact_id IN (SELECT artifact_id FROM ci_parse_artifacts WHERE source_id = ?)`, []any{authority.source.SourceID}, &counts.Definitions},
		{`SELECT COUNT(*) FROM ci_reference_sites WHERE artifact_id IN (SELECT artifact_id FROM ci_parse_artifacts WHERE source_id = ?)`, []any{authority.source.SourceID}, &counts.References},
		{`SELECT COUNT(*) FROM ci_chunks WHERE source_id = ?`, []any{authority.source.SourceID}, &counts.Chunks},
		{`SELECT COUNT(*) FROM ci_index_build_parts WHERE build_id IN (SELECT job_id FROM ci_jobs WHERE result_view_id = ?)`, []any{publication.viewID}, &counts.StagedParts},
		{`SELECT COALESCE(SUM(payload_bytes), 0) FROM ci_index_build_parts WHERE build_id IN (SELECT job_id FROM ci_jobs WHERE result_view_id = ?)`, []any{publication.viewID}, &counts.StagedPartBytes},
	}
	for _, query := range queries {
		if err := db.Raw(query.query, query.args...).Scan(query.into).Error; err != nil {
			return uciRealCorpusCounts{}, err
		}
	}
	for _, relation := range []string{"calls", "imports", "exports", "references"} {
		var count int64
		if err := db.Raw(`SELECT COUNT(*) FROM ci_resolved_edges WHERE checkout_id = ? AND relation = ? AND valid_from_generation <= ? AND (valid_to_generation IS NULL OR valid_to_generation > ?)`, checkoutID, relation, publication.generation, publication.generation).Scan(&count).Error; err != nil {
			return uciRealCorpusCounts{}, err
		}
		counts.Edges[relation] = count
	}
	if err := db.Raw(`SELECT coverage_json FROM ci_views WHERE view_id = ?`, publication.viewID).Scan(&counts.CoverageJSON).Error; err != nil {
		return uciRealCorpusCounts{}, err
	}
	counts.EmbeddingCandidates = embedding.Embedding.TotalCandidates
	counts.ReadyEmbeddings = embedding.Embedding.ReadyCandidates
	counts.PendingEmbeddingJobs = embedding.Embedding.PendingJobs
	if embedding.Embedding.JobState != nil {
		counts.EmbeddingJobState = *embedding.Embedding.JobState
	}
	if counts.Memberships < 1000 || counts.Artifacts < 1000 || counts.Chunks < 1000 || counts.ReadyEmbeddings != counts.EmbeddingCandidates {
		return counts, errors.New("real-corpus database counts do not prove a full ready pilot")
	}
	return counts, nil
}

func uciRealCorpusSemanticProof(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication, root string) (uciRealCorpusSemantic, error) {
	expectedSource, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(uciRealCorpusExpectedPath)))
	if err != nil {
		return uciRealCorpusSemantic{}, err
	}
	overlap := uciRealCorpusLexicalOverlap(uciRealCorpusQuery, string(expectedSource))
	if len(overlap) != 0 {
		return uciRealCorpusSemantic{}, fmt.Errorf("real-corpus semantic query has lexical overlap: %v", overlap)
	}
	payload, err := client.Tool(ctx, "codebase_search", map[string]any{"context_handle": selection.contextHandle, "query": uciRealCorpusQuery, "limit": 5})
	if err != nil {
		return uciRealCorpusSemantic{}, err
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil {
		return uciRealCorpusSemantic{}, err
	}
	if !uciInstalledAcceptanceQueryMatchesPublication(response, publication) || response.Retrieval == nil || response.Retrieval.Mode != uci.QueryRetrievalHybrid || response.Retrieval.VectorCoverage == nil || *response.Retrieval.VectorCoverage < 1 || response.Items == nil {
		return uciRealCorpusSemantic{}, errors.New("real-corpus semantic response is not a complete selected-View hybrid result")
	}
	proof := uciRealCorpusSemantic{QueryDigest: uciInstalledAcceptanceStringDigest(uciRealCorpusQuery), ExpectedPath: uciRealCorpusExpectedPath, RetrievalMode: string(response.Retrieval.Mode), VectorCoverage: *response.Retrieval.VectorCoverage, LexicalOverlap: overlap, ExposureAvailable: response.Exposure != nil}
	var item *uci.QueryItem
	for index := range *response.Items {
		candidate := &(*response.Items)[index]
		if candidate.Path == uciRealCorpusExpectedPath {
			proof.ExpectedRank = index + 1
			proof.MatchSources = make([]string, len(candidate.MatchSources))
			for matchIndex, source := range candidate.MatchSources {
				proof.MatchSources[matchIndex] = string(source)
			}
			item = candidate
			break
		}
	}
	if item == nil || proof.ExpectedRank > 5 || !uciRealCorpusContains(proof.MatchSources, string(uci.QueryMatchVector)) {
		return proof, errors.New("real-corpus semantic query did not return the expected vector-backed source in top five")
	}
	readPayload, err := client.Tool(ctx, "codebase_read", map[string]any{
		"context_handle": selection.contextHandle,
		"ref":            map[string]any{"source_id": item.Ref.SourceID, "view_id": item.Ref.ViewID, "entity_key": item.Ref.EntityKey},
		"span":           map[string]any{"byte_start": item.Span.ByteStart, "byte_end": item.Span.ByteEnd, "line_start": item.Span.LineStart, "line_end": item.Span.LineEnd},
		"content_digest": string(item.ContentDigest), "verify_working_copy": false, "max_bytes": 8192,
	})
	if err != nil {
		return proof, err
	}
	read, err := uciDecodeInstalledAcceptanceQuery(readPayload)
	if err != nil || read.Items == nil || len(*read.Items) != 1 || (*read.Items)[0].Ref != item.Ref || (*read.Items)[0].ContentDigest != item.ContentDigest {
		return proof, errors.New("real-corpus semantic citation did not read back exactly")
	}
	proof.CitationReadBack = true
	return proof, nil
}

func uciRealCorpusGraphProof(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication, authority *uciInstalledAcceptanceAuthority, initial uciRealCorpusEdgeRow) (uciRealCorpusGraph, error) {
	checkoutID := authority.checkouts[uciInstalledAcceptanceClientA].CheckoutID
	db := authority.store.GetDB().WithContext(ctx)
	proof := uciRealCorpusGraph{InitialTargetDigest: uciInstalledAcceptanceStringDigest(initial.TargetArtifact), InitialViewDigest: uciInstalledAcceptanceStringDigest(publication.viewID)}
	if err := db.Raw(`SELECT COUNT(*) FROM ci_resolved_edges WHERE checkout_id = ? AND relation = 'calls' AND source_path <> target_path AND valid_to_generation IS NULL`, checkoutID).Scan(&proof.InterfileCalls).Error; err != nil {
		return proof, err
	}
	for relation, destination := range map[string]*int64{"imports": &proof.Imports, "exports": &proof.Exports, "references": &proof.References} {
		if err := db.Raw(`SELECT COUNT(*) FROM ci_resolved_edges WHERE checkout_id = ? AND relation = ? AND valid_to_generation IS NULL`, checkoutID, relation).Scan(destination).Error; err != nil {
			return proof, err
		}
	}
	if proof.InterfileCalls == 0 || proof.Imports == 0 || proof.Exports == 0 || proof.References == 0 {
		return proof, errors.New("real-corpus graph lacks required source-derived relation families")
	}
	caller, err := uciRealCorpusFindItem(ctx, client, selection.contextHandle, publication, "UCIRealCorpusCaller", uciRealCorpusGoCallerPath)
	if err != nil {
		return proof, err
	}
	callee, err := uciRealCorpusFindItem(ctx, client, selection.contextHandle, publication, "UCIRealCorpusCallee", uciRealCorpusGoCalleePath)
	if err != nil {
		return proof, err
	}
	outgoing, err := uciRealCorpusGraphCall(ctx, client, selection.contextHandle, publication, caller.Ref, "outgoing")
	if err != nil {
		return proof, err
	}
	incoming, err := uciRealCorpusGraphCall(ctx, client, selection.contextHandle, publication, callee.Ref, "incoming")
	if err != nil {
		return proof, err
	}
	proof.OutgoingMCPCall = uciRealCorpusGraphHasEdge(outgoing, caller.Ref, callee.Ref)
	proof.IncomingMCPReverse = uciRealCorpusGraphHasEdge(incoming, caller.Ref, callee.Ref)
	if !proof.OutgoingMCPCall || !proof.IncomingMCPReverse {
		return proof, errors.New("real-corpus MCP graph did not expose both outgoing and reverse call navigation")
	}
	return proof, nil
}

func uciRealCorpusCanaryEdge(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication, sourcePath, targetPath string) (uciRealCorpusEdgeRow, error) {
	var rows []uciRealCorpusEdgeRow
	checkoutID := authority.checkouts[uciInstalledAcceptanceClientA].CheckoutID
	err := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT source_artifact, target_artifact, COALESCE(target_symbol, '') AS target_symbol
		FROM ci_resolved_edges
		WHERE checkout_id = ? AND source_path = ? AND target_path = ? AND relation = 'calls'
			AND valid_from_generation <= ? AND (valid_to_generation IS NULL OR valid_to_generation > ?)
		ORDER BY edge_key
	`, checkoutID, sourcePath, targetPath, publication.generation, publication.generation).Scan(&rows).Error
	if err != nil {
		return uciRealCorpusEdgeRow{}, err
	}
	if len(rows) != 1 || rows[0].TargetArtifact == "" {
		return uciRealCorpusEdgeRow{}, errors.New("real-corpus Go canary call edge is missing or ambiguous")
	}
	return rows[0], nil
}

func uciRealCorpusFindItem(ctx context.Context, client *uciInstalledAcceptanceMCPClient, handle string, publication uciInstalledAcceptancePublication, query, expectedPath string) (uci.QueryItem, error) {
	payload, err := client.Tool(ctx, "codebase_search", map[string]any{"context_handle": handle, "query": query, "path_prefix": expectedPath, "limit": 10})
	if err != nil {
		return uci.QueryItem{}, err
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil || !uciInstalledAcceptanceQueryMatchesPublication(response, publication) || response.Items == nil {
		return uci.QueryItem{}, errors.New("real-corpus graph search did not return selected-View items")
	}
	for _, item := range *response.Items {
		if item.Path == expectedPath {
			return item, nil
		}
	}
	return uci.QueryItem{}, fmt.Errorf("real-corpus graph search omitted %s", expectedPath)
}

func uciRealCorpusGraphCall(ctx context.Context, client *uciInstalledAcceptanceMCPClient, handle string, publication uciInstalledAcceptancePublication, target uci.QueryEntityRef, direction string) (uci.QueryResponse, error) {
	payload, err := client.Tool(ctx, "codebase_graph", map[string]any{
		"context_handle": handle,
		"action":         "neighbors",
		"target":         map[string]any{"source_id": target.SourceID, "view_id": target.ViewID, "entity_key": target.EntityKey},
		"direction":      direction,
		"max_depth":      4,
		"max_visited":    256,
		"max_nodes":      128,
		"max_edges":      256,
		"deadline_ms":    30_000,
	})
	if err != nil {
		return uci.QueryResponse{}, err
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil || !uciInstalledAcceptanceQueryMatchesPublication(response, publication) || response.Graph == nil {
		return uci.QueryResponse{}, errors.New("real-corpus graph response is invalid")
	}
	return response, nil
}

func uciRealCorpusGraphHasEdge(response uci.QueryResponse, from, to uci.QueryEntityRef) bool {
	if response.Graph == nil {
		return false
	}
	for _, edge := range response.Graph.Edges {
		if edge.Relation == uci.IndexRelation("calls") && edge.From == from && edge.To == to {
			return true
		}
	}
	return false
}

func uciRealCorpusLexicalOverlap(query, source string) []string {
	tokens := func(value string) map[string]struct{} {
		out := make(map[string]struct{})
		var current []rune
		flush := func() {
			if len(current) >= 3 {
				out[strings.ToLower(string(current))] = struct{}{}
			}
			current = current[:0]
		}
		for _, character := range value {
			if unicode.IsLetter(character) || unicode.IsDigit(character) {
				current = append(current, character)
			} else {
				flush()
			}
		}
		flush()
		return out
	}
	left, right := tokens(query), tokens(source)
	overlap := make([]string, 0)
	for token := range left {
		if _, found := right[token]; found {
			overlap = append(overlap, token)
		}
	}
	sort.Strings(overlap)
	return overlap
}

func uciRealCorpusContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func uciRealCorpusRequiredPath(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	value = filepath.Clean(value)
	physical, err := uciInstalledAcceptancePhysicalPath(value)
	if err != nil {
		t.Fatal(err)
	}
	return physical
}

func uciRealCorpusGitValue(ctx context.Context, root string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		return "", errors.New("real-corpus Git returned an empty value")
	}
	return value, nil
}

func uciRealCorpusGitClean(root string) (bool, error) {
	command := exec.Command("git", "status", "--porcelain=v1")
	command.Dir = root
	output, err := command.Output()
	return len(strings.TrimSpace(string(output))) == 0, err
}

func uciRealCorpusAnchorProjectID(root string) (string, error) {
	encoded, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	if err != nil {
		return "", err
	}
	var marker struct {
		Version   int    `json:"version"`
		ProjectID string `json:"project_id"`
		Scope     string `json:"scope"`
	}
	if err := json.Unmarshal(encoded, &marker); err != nil || marker.Version != 3 || marker.Scope != "repository" {
		return "", errors.New("real-corpus project marker is invalid")
	}
	if _, err := uuid.Parse(marker.ProjectID); err != nil {
		return "", errors.New("real-corpus project marker ID is invalid")
	}
	return marker.ProjectID, nil
}

func uciRealCorpusWriteCanaries(root string) error {
	for relativePath, content := range uciRealCorpusCanaries {
		path := filepath.Join(root, filepath.FromSlash(relativePath))
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("real-corpus canary already exists: %s", relativePath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func uciRealCorpusRemoveCanaries(root string) error {
	var cleanup []error
	for relativePath := range uciRealCorpusCanaries {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(relativePath))); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanup = append(cleanup, err)
		}
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(uciRealCorpusTSRoot))); err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanup = append(cleanup, err)
	}
	return errors.Join(cleanup...)
}
