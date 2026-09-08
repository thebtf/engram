package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/uci"
)

const (
	uciRealCorpusEmbeddingAPIKeyEnv        = "ENGRAM_EMBEDDING_API_KEY"
	uciRealCorpusEmbeddingModelEnv         = "ENGRAM_EMBEDDING_MODEL"
	uciRealCorpusEmbeddingURLEnv           = "ENGRAM_EMBEDDING_URL"
	uciRealCorpusEnabledEnv                = "ENGRAM_UCI_REAL_CORPUS_ENABLED"
	uciRealCorpusRootEnv                   = "ENGRAM_UCI_REAL_CORPUS_ROOT"
	uciRealCorpusDatabaseDSNEnv            = "ENGRAM_UCI_REAL_CORPUS_DATABASE_DSN"
	uciRealCorpusProviderRefEnv            = "ENGRAM_UCI_REAL_CORPUS_PROVIDER_REF"
	uciRealCorpusPreprocessingRevisionEnv  = "ENGRAM_UCI_REAL_CORPUS_PREPROCESSING_REVISION"
	uciRealCorpusRecordPathEnv             = "ENGRAM_UCI_REAL_CORPUS_RECORD_PATH"
	uciRealCorpusSourceTransferApprovedEnv = "ENGRAM_UCI_REAL_CORPUS_SOURCE_TRANSFER_APPROVED"
	uciRealCorpusTerminalArtifactEnv       = "ENGRAM_UCI_REAL_CORPUS_TERMINAL_ARTIFACT"
	uciRealCorpusRecordSchemaVersion       = "engram.uci-real-corpus/v3"

	uciRealCorpusGoCallerPath          = "internal/uci/zz_uci_real_corpus_caller.go"
	uciRealCorpusGoCalleePath          = "internal/uci/zz_uci_real_corpus_callee.go"
	uciRealCorpusTSRoot                = "apps/operator-console/composables/zz-uci-real-corpus"
	uciRealCorpusExpectedPath          = "internal/uci/semantic.go"
	uciRealCorpusQueryBase64           = "6KqN5Y+v5riI44G/44Gu5LiN5aSJ44OT44Ol44O85YaF44Gn44CB6Kqe5b2Z5qSc57Si44Go44OZ44Kv44OI44Or5YCZ6KOc44KS57WQ5ZCI44GX44CB5a6M5YWo44Gq5Z+L44KB6L6844G/6KKr6KaG44GM44GC44KL5aC05ZCI44Gg44GR44OP44Kk44OW44Oq44OD44OJ57WQ5p6c44KS6L+U44GZ5LuV57WE44G/44Gv77yf"
	uciRealCorpusBarrierWaitMS         = int64(5_000)
	uciRealCorpusEmbeddingStallWindow  = 5 * time.Minute
	uciRealCorpusEmbeddingRetryGrace   = 30 * time.Second
	uciRealCorpusEmbeddingPollInterval = 5 * time.Second
	uciRealCorpusOperationTimeout      = 3 * time.Hour
	uciRealCorpusCleanupHeadroom       = 5 * time.Minute
	uciRealCorpusRequiredOuterTimeout  = uciRealCorpusOperationTimeout + uciRealCorpusCleanupHeadroom
	uciRealCorpusSafeOuterTimeoutFlag  = "3h10m"
	uciRealCorpusRerunCommand          = "go test ./cmd/engram -run '^TestUCIRealCorpusInstalledProviderLifecycle$' -count=1 -v -timeout=3h10m"
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

var uciRealCorpusRequiredRerunEnvironmentVariables = [...]string{
	uciRealCorpusEmbeddingAPIKeyEnv,
	uciRealCorpusEmbeddingModelEnv,
	uciRealCorpusEmbeddingURLEnv,
	uciRealCorpusDatabaseDSNEnv,
	uciRealCorpusEnabledEnv,
	uciRealCorpusPreprocessingRevisionEnv,
	uciRealCorpusProviderRefEnv,
	uciRealCorpusRootEnv,
	uciRealCorpusSourceTransferApprovedEnv,
}

type uciRealCorpusCandidate struct {
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
}

type uciRealCorpusIdentity struct {
	SourceDigest           string `json:"source_digest"`
	CheckoutDigest         string `json:"checkout_digest"`
	AnalysisProfileDigest  string `json:"analysis_profile_digest"`
	EmbeddingProfileDigest string `json:"embedding_profile_digest"`
	FinalViewDigest        string `json:"final_view_digest"`
}

type uciRealCorpusRerunRecipe struct {
	Command                      string   `json:"command"`
	RequiredEnvironmentVariables []string `json:"required_environment_variables"`
	TerminalArtifactDigest       string   `json:"terminal_artifact_digest,omitempty"`
}

type uciRealCorpusProvider struct {
	EndpointDigest         string `json:"endpoint_digest"`
	ProviderRef            string `json:"provider_ref"`
	Model                  string `json:"model"`
	Dimension              int    `json:"dimension"`
	CandidatePageSize      int    `json:"candidate_page_size"`
	ProviderBatchSize      int    `json:"provider_batch_size"`
	ProviderConcurrency    int    `json:"provider_concurrency"`
	PreprocessingRevision  string `json:"preprocessing_revision"`
	CredentialRetained     bool   `json:"credential_retained"`
	SourceTransferApproved bool   `json:"source_transfer_approved"`
}

type uciRealCorpusCounts struct {
	Memberships                 int64            `json:"memberships"`
	PresentMemberships          int64            `json:"present_memberships"`
	ExcludedMemberships         int64            `json:"excluded_memberships"`
	UnreadableMemberships       int64            `json:"unreadable_memberships"`
	Artifacts                   int64            `json:"artifacts"`
	Definitions                 int64            `json:"definitions"`
	References                  int64            `json:"references"`
	Chunks                      int64            `json:"chunks"`
	StagedParts                 int64            `json:"staged_parts"`
	StagedPartBytes             int64            `json:"staged_part_bytes"`
	Edges                       map[string]int64 `json:"edges"`
	EmbeddingCandidates         uint64           `json:"embedding_candidates"`
	ReadyEmbeddings             uint64           `json:"ready_embeddings"`
	PendingEmbeddingJobs        uint64           `json:"pending_embedding_jobs"`
	EmbeddingJobState           string           `json:"embedding_job_state"`
	ImmutableStructuralCoverage json.RawMessage  `json:"immutable_structural_coverage"`
	LiveEmbeddingCoverage       string           `json:"live_embedding_coverage"`
}

type uciRealCorpusSemantic struct {
	QueryDigest        string   `json:"query_digest"`
	ExpectedPath       string   `json:"expected_path"`
	ExpectedRank       int      `json:"expected_rank"`
	RetrievalMode      string   `json:"retrieval_mode"`
	LiveVectorCoverage float64  `json:"live_vector_coverage"`
	MatchSources       []string `json:"match_sources"`
	LexicalOverlap     []string `json:"lexical_overlap"`
	CitationReadBack   bool     `json:"citation_read_back"`
	ExposureAvailable  bool     `json:"exposure_available"`
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
	SchemaVersion string                   `json:"schema_version"`
	RecordedAt    string                   `json:"recorded_at"`
	Candidate     uciRealCorpusCandidate   `json:"candidate"`
	Identity      uciRealCorpusIdentity    `json:"identity"`
	Rerun         uciRealCorpusRerunRecipe `json:"rerun"`
	Pilot         struct {
		Source               string `json:"source"`
		CorpusDefinition     string `json:"corpus_definition"`
		PresentSourceBytes   uint64 `json:"present_source_bytes"`
		FullScannerCorpus    bool   `json:"full_scanner_corpus"`
		IgnoredFilesIncluded bool   `json:"ignored_files_included"`
		SizeSanity           bool   `json:"size_sanity"`
		ExactComposition     bool   `json:"exact_composition"`
		InitialGitClean      bool   `json:"initial_git_clean"`
		FinalGitClean        bool   `json:"final_git_clean"`
	} `json:"pilot"`
	Provider         uciRealCorpusProvider         `json:"provider"`
	Initial          uciRealCorpusCounts           `json:"initial"`
	AfterChange      uciRealCorpusCounts           `json:"after_change"`
	Semantic         uciRealCorpusSemantic         `json:"semantic"`
	Graph            uciRealCorpusGraph            `json:"graph"`
	Composition      uciRealCorpusComposition      `json:"composition"`
	CapacityRecovery uciRealCorpusCapacityRecovery `json:"capacity_recovery"`
	NativeGraph      uciRealCorpusNativeGraph      `json:"native_graph"`
	Installed        struct {
		ServerSHA256 string `json:"server_sha256"`
		DaemonSHA256 string `json:"daemon_sha256"`
		ParserSHA256 string `json:"parser_sha256"`
		StandardMCP  bool   `json:"standard_mcp"`
	} `json:"installed"`
	Scope struct {
		EvidenceRecordSecretsRetained         bool `json:"evidence_record_secrets_retained"`
		EvidenceRecordSourceBodiesRetained    bool `json:"evidence_record_source_bodies_retained"`
		EvidenceRecordPrivateLocatorsRetained bool `json:"evidence_record_private_locators_retained"`
		ProcessRestartProof                   bool `json:"process_restart_proof"`
		ProductionMutation                    bool `json:"production_mutation"`
		ReleaseClaim                          bool `json:"release_claim"`
	} `json:"scope"`
}

type uciRealCorpusEmbeddingStatus struct {
	TotalChunks    int64 `json:"total_chunks"`
	EmbeddedChunks int64 `json:"embedded_chunks"`
	Context        struct {
		SourceID   string `json:"source_id"`
		CheckoutID string `json:"checkout_id"`
		ViewID     string `json:"view_id"`
		ProfileID  string `json:"profile_id"`
		Generation int64  `json:"generation"`
	} `json:"context"`
	Embedding struct {
		EmbeddingProfileID *string    `json:"embedding_profile_id"`
		Coverage           string     `json:"coverage"`
		TotalCandidates    uint64     `json:"total_candidates"`
		ReadyCandidates    uint64     `json:"ready_candidates"`
		PendingJobs        uint64     `json:"pending_jobs"`
		JobState           *string    `json:"job_state"`
		ErrorCode          *string    `json:"error_code"`
		RetryAfter         *time.Time `json:"retry_after"`
	} `json:"embedding"`
}

type uciRealCorpusEmbeddingStage string

const (
	uciRealCorpusEmbeddingStageReady                         uciRealCorpusEmbeddingStage = "ready"
	uciRealCorpusEmbeddingStageStillProgressing              uciRealCorpusEmbeddingStage = "still_progressing"
	uciRealCorpusEmbeddingStageTerminalFailed                uciRealCorpusEmbeddingStage = "terminal_failed"
	uciRealCorpusEmbeddingStageTerminalSucceededInconsistent uciRealCorpusEmbeddingStage = "terminal_succeeded_inconsistent"
	uciRealCorpusEmbeddingStageNoProgress                    uciRealCorpusEmbeddingStage = "no_progress"
)

type uciRealCorpusEmbeddingProgress struct {
	viewID                  string
	generation              int64
	embeddingProfilePresent bool
	jobState                string
	totalCandidates         uint64
	readyCandidates         uint64
	pendingJobs             uint64
	coverage                string
	errorCode               string
	retryAfter              time.Time
}

type uciRealCorpusEmbeddingSummary struct {
	viewPresent             bool
	generation              int64
	embeddingProfilePresent bool
	jobState                string
	coverage                string
	totalCandidates         uint64
	readyCandidates         uint64
	pendingJobs             uint64
	errorCode               string
	retryAfterPresent       bool
}

type uciRealCorpusEmbeddingClassification struct {
	stage    uciRealCorpusEmbeddingStage
	progress uciRealCorpusEmbeddingProgress
	summary  uciRealCorpusEmbeddingSummary
}

type uciRealCorpusEmbeddingProgressWindow struct {
	progress    uciRealCorpusEmbeddingProgress
	observedAt  time.Time
	initialized bool
}

type uciRealCorpusEmbeddingWaitError struct {
	stage   uciRealCorpusEmbeddingStage
	summary uciRealCorpusEmbeddingSummary
}

type uciRealCorpusEdgeRow struct {
	SourceArtifact string `gorm:"column:source_artifact"`
	TargetArtifact string `gorm:"column:target_artifact"`
	TargetSymbol   string `gorm:"column:target_symbol"`
}

func TestUCIRealCorpusOuterDeadline(t *testing.T) {
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		available time.Duration
		wantErr   bool
	}{
		{name: "required duration", available: uciRealCorpusRequiredOuterTimeout},
		{name: "insufficient duration", available: uciRealCorpusRequiredOuterTimeout - time.Nanosecond, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := uciRealCorpusValidateOuterDeadline(now, now.Add(test.available), true, uciRealCorpusOperationTimeout, uciRealCorpusCleanupHeadroom)
			if (err != nil) != test.wantErr {
				t.Fatalf("outer deadline error = %v, want error=%t", err, test.wantErr)
			}
		})
	}
}

func uciRealCorpusValidateOuterDeadline(now, deadline time.Time, deadlineSet bool, operationTimeout, cleanupHeadroom time.Duration) error {
	if !deadlineSet || deadline.Sub(now) >= operationTimeout+cleanupHeadroom {
		return nil
	}
	return fmt.Errorf("real-corpus installed acceptance needs at least %s remaining on the Go test deadline (%s operation plus %s cleanup headroom); rerun with -timeout=%s to leave startup margin", uciRealCorpusRequiredOuterTimeout, operationTimeout, cleanupHeadroom, uciRealCorpusSafeOuterTimeoutFlag)
}

func TestUCIRealCorpusInstalledProviderLifecycle(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows real-corpus installed acceptance")
	}
	if strings.TrimSpace(os.Getenv(uciRealCorpusEnabledEnv)) != "1" {
		t.Skip("real-corpus installed acceptance requires ENGRAM_UCI_REAL_CORPUS_ENABLED=1")
	}
	deadline, deadlineSet := t.Deadline()
	if err := uciRealCorpusValidateOuterDeadline(time.Now(), deadline, deadlineSet, uciRealCorpusOperationTimeout, uciRealCorpusCleanupHeadroom); err != nil {
		t.Fatal(err)
	}

	root := uciRealCorpusRequiredPath(t, uciRealCorpusRootEnv)
	dsn := strings.TrimSpace(os.Getenv(uciRealCorpusDatabaseDSNEnv))
	if dsn == "" {
		t.Fatal("real-corpus installed acceptance requires a disposable PostgreSQL DSN")
	}
	providerURL := strings.TrimSpace(os.Getenv(uciRealCorpusEmbeddingURLEnv))
	providerModel := strings.TrimSpace(os.Getenv(uciRealCorpusEmbeddingModelEnv))
	providerKey := strings.TrimSpace(os.Getenv(uciRealCorpusEmbeddingAPIKeyEnv))
	providerRef := strings.TrimSpace(os.Getenv(uciRealCorpusProviderRefEnv))
	preprocessing := strings.TrimSpace(os.Getenv(uciRealCorpusPreprocessingRevisionEnv))
	if providerURL == "" || providerModel == "" || providerKey == "" || providerRef == "" || preprocessing == "" {
		t.Fatal("real-corpus installed acceptance requires explicit provider URL, model, API key, provider ref, and preprocessing revision")
	}
	if err := uciRealCorpusValidateConfiguredProviderRef(providerURL, providerRef); err != nil {
		t.Fatal(err)
	}
	uciInstalledAcceptanceRequireTestPostgres(t, dsn)
	sourceTransferApproved := strings.TrimSpace(os.Getenv(uciRealCorpusSourceTransferApprovedEnv)) == "1"
	if !sourceTransferApproved {
		t.Fatal("real-corpus installed acceptance requires explicit source-transfer approval")
	}
	if clean, err := uciRealCorpusGitClean(root); err != nil || !clean {
		t.Fatalf("real-corpus pilot must start from a clean production-scanner corpus: clean=%t err=%v", clean, err)
	}

	recordPath := strings.TrimSpace(os.Getenv(uciRealCorpusRecordPathEnv))
	if recordPath != "" {
		if !filepath.IsAbs(recordPath) || uciInstalledAcceptancePathOverlaps(root, recordPath) {
			t.Fatal("real-corpus record path must be absolute and outside the pilot worktree")
		}
	}
	rerun, err := uciRealCorpusBuildRerunRecipe(os.Getenv(uciRealCorpusTerminalArtifactEnv))
	if err != nil {
		t.Fatal(err)
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
		OperationTimeout:          uciRealCorpusOperationTimeout,
	}
	ctx, cancel := context.WithTimeout(context.Background(), request.OperationTimeout)
	defer cancel()
	record, err := runUCIRealCorpusInstalledAcceptance(ctx, request, providerURL, providerModel, providerRef, preprocessing, sourceTransferApproved, rerun)
	if err != nil {
		t.Fatal(err)
	}
	if err := uciRealCorpusValidateRecordForWrite(record); err != nil {
		t.Fatal(err)
	}
	if !record.Pilot.FinalGitClean || !record.Pilot.FullScannerCorpus || !record.Installed.StandardMCP {
		t.Fatal("real-corpus acceptance did not restore a clean full production-scanner corpus")
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

func TestUCIRealCorpusFindQualifiedGoItemIgnoresReorderedSamePathHits(t *testing.T) {
	const (
		path       = "internal/uci/zz_uci_real_corpus_caller.go"
		function   = "UCIRealCorpusCaller"
		wrongKey   = "go:example/internal/uci/zz_uci_real_corpus_caller.go:0"
		correctKey = "go:example/internal/uci/zz_uci_real_corpus_caller.go/func:UCIRealCorpusCaller"
	)
	items := uci.QueryItems{
		{
			Ref:  uci.QueryEntityRef{SourceID: "source", ViewID: "view", EntityKey: wrongKey},
			Path: path,
			Span: uci.QuerySpan{ByteStart: 0, ByteEnd: 10, LineStart: 1, LineEnd: 1},
		},
		{
			Ref:  uci.QueryEntityRef{SourceID: "source", ViewID: "view", EntityKey: correctKey},
			Path: path,
			Span: uci.QuerySpan{ByteStart: 12, ByteEnd: 64, LineStart: 3, LineEnd: 3},
		},
	}
	item, found := uciRealCorpusFindQualifiedGoItem(items, path, function)
	if !found {
		t.Fatal("qualified Go function was not selected")
	}
	if item.Ref.EntityKey != correctKey {
		t.Fatalf("selected entity = %q, want %q", item.Ref.EntityKey, correctKey)
	}
}

func TestUCIRealCorpusEmbeddingPublicationFollowsMonotonicCurrentView(t *testing.T) {
	expected := uciInstalledAcceptancePublication{
		sourceID: "source", checkoutID: "checkout", viewID: "view-1", profileID: "profile", generation: 1,
	}
	status := uciRealCorpusEmbeddingStatus{}
	status.Context.SourceID = expected.sourceID
	status.Context.CheckoutID = expected.checkoutID
	status.Context.ViewID = "view-2"
	status.Context.ProfileID = expected.profileID
	status.Context.Generation = 2
	current, err := uciRealCorpusEmbeddingPublication(status, expected)
	if err != nil {
		t.Fatal(err)
	}
	if current.viewID != status.Context.ViewID || current.generation != status.Context.Generation {
		t.Fatalf("current publication = %#v, want monotonic status View", current)
	}
	classification := uciClassifyRealCorpusEmbedding(status, false)
	if classification.progress.viewID != current.viewID || classification.progress.generation != current.generation {
		t.Fatalf("embedding progress = %#v, want current monotonic publication", classification.progress)
	}

	status.Context.SourceID = "foreign"
	if _, err := uciRealCorpusEmbeddingPublication(status, expected); err == nil {
		t.Fatal("foreign embedding status was accepted")
	}
	status.Context.SourceID = expected.sourceID
	status.Context.Generation = expected.generation
	if _, err := uciRealCorpusEmbeddingPublication(status, expected); err == nil {
		t.Fatal("same-generation replacement View was accepted")
	}
}

func TestUCIRealCorpusEmbeddingClassifiesStatus(t *testing.T) {
	profileID := "profile://private"
	succeeded := "succeeded"
	failed := "failed_terminal"

	tests := []struct {
		name    string
		status  uciRealCorpusEmbeddingStatus
		stalled bool
		stage   uciRealCorpusEmbeddingStage
	}{
		{
			name:   "ready",
			status: uciRealCorpusEmbeddingTestStatus("view://private", 2, &profileID, string(uci.IndexCoverageComplete), 2, 2, 0, &succeeded),
			stage:  uciRealCorpusEmbeddingStageReady,
		},
		{
			name:   "terminal failed",
			status: uciRealCorpusEmbeddingTestStatus("view://private", 2, &profileID, string(uci.IndexCoveragePartial), 2, 1, 1, &failed),
			stage:  uciRealCorpusEmbeddingStageTerminalFailed,
		},
		{
			name:   "succeeded but inconsistent",
			status: uciRealCorpusEmbeddingTestStatus("view://private", 2, &profileID, string(uci.IndexCoveragePartial), 2, 1, 0, &succeeded),
			stage:  uciRealCorpusEmbeddingStageTerminalSucceededInconsistent,
		},
		{
			name:    "stagnant progress",
			status:  uciRealCorpusEmbeddingTestStatus("view://private", 2, &profileID, string(uci.IndexCoveragePartial), 2, 1, 1, nil),
			stalled: true,
			stage:   uciRealCorpusEmbeddingStageNoProgress,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classification := uciClassifyRealCorpusEmbedding(test.status, test.stalled)
			if classification.stage != test.stage {
				t.Fatalf("stage = %q, want %q", classification.stage, test.stage)
			}
		})
	}

	classification := uciClassifyRealCorpusEmbedding(tests[2].status, false)
	message := classification.waitError().Error()
	if !strings.Contains(message, "stage=terminal_succeeded_inconsistent") {
		t.Fatalf("terminal succeeded error = %q", message)
	}
	if strings.Contains(message, profileID) || strings.Contains(message, tests[2].status.Context.ViewID) {
		t.Fatalf("embedding wait error exposed a private identifier: %q", message)
	}
}

func TestUCIRealCorpusEmbeddingProgressWindowStallsOnlyWithoutProgress(t *testing.T) {
	profileID := "profile://private"
	replacementProfileID := "profile://replacement"
	running := "running"
	status := uciRealCorpusEmbeddingTestStatus("view://private", 2, &profileID, string(uci.IndexCoveragePartial), 2, 0, 1, &running)
	started := time.Date(2026, time.September, 7, 0, 0, 0, 0, time.UTC)
	classification := uciClassifyRealCorpusEmbedding(status, false)
	window, stalled := (uciRealCorpusEmbeddingProgressWindow{}).observe(classification.progress, started)
	if stalled {
		t.Fatal("first embedding status was marked stalled")
	}

	status.Embedding.EmbeddingProfileID = &replacementProfileID
	classification = uciClassifyRealCorpusEmbedding(status, false)
	window, stalled = window.observe(classification.progress, started.Add(uciRealCorpusEmbeddingStallWindow))
	if !stalled {
		t.Fatal("unchanged progress tuple did not stall at the bounded window")
	}
	if got := uciClassifyRealCorpusEmbedding(status, stalled).stage; got != uciRealCorpusEmbeddingStageNoProgress {
		t.Fatalf("stagnant status stage = %q, want %q", got, uciRealCorpusEmbeddingStageNoProgress)
	}

	status.Embedding.ReadyCandidates++
	classification = uciClassifyRealCorpusEmbedding(status, false)
	resetAt := started.Add(uciRealCorpusEmbeddingStallWindow + time.Second)
	window, stalled = window.observe(classification.progress, resetAt)
	if stalled {
		t.Fatal("changed ready candidate count did not reset the stall window")
	}
	if !window.observedAt.Equal(resetAt) {
		t.Fatalf("progress reset at %s, want %s", window.observedAt, resetAt)
	}
}

func TestUCIRealCorpusEmbeddingProgressWindowWaitsForScheduledRetry(t *testing.T) {
	profileID := "profile://private"
	retryScheduled := "retry_scheduled"
	errorCode := string(uci.EmbeddingFailureProviderUnavailable)
	started := time.Date(2026, time.September, 7, 0, 0, 0, 0, time.UTC)
	retryAfter := started.Add(10 * time.Minute)
	status := uciRealCorpusEmbeddingTestStatus("view://private", 2, &profileID, string(uci.IndexCoveragePartial), 20, 4, 1, &retryScheduled)
	status.Embedding.ErrorCode = &errorCode
	status.Embedding.RetryAfter = &retryAfter
	classification := uciClassifyRealCorpusEmbedding(status, false)
	window, stalled := (uciRealCorpusEmbeddingProgressWindow{}).observe(classification.progress, started)
	if stalled {
		t.Fatal("new retry schedule was marked stalled")
	}
	if _, stalled = window.observe(classification.progress, started.Add(uciRealCorpusEmbeddingStallWindow)); stalled {
		t.Fatal("scheduled retry was cut off by the ordinary no-progress window")
	}
	if _, stalled = window.observe(classification.progress, retryAfter.Add(uciRealCorpusEmbeddingRetryGrace)); !stalled {
		t.Fatal("unchanged retry schedule did not stop after its bounded grace")
	}
	message := uciClassifyRealCorpusEmbedding(status, true).waitError().Error()
	if !strings.Contains(message, "error_code=provider_unavailable") || !strings.Contains(message, "retry_after_present=true") {
		t.Fatalf("retry wait error omitted safe failure state: %q", message)
	}
	if strings.Contains(message, profileID) || strings.Contains(message, status.Context.ViewID) || strings.Contains(message, retryAfter.Format(time.RFC3339)) {
		t.Fatalf("retry wait error exposed a private identifier or timestamp: %q", message)
	}
}

func TestUCIRealCorpusProviderRefBindsExactEndpoint(t *testing.T) {
	endpoint := "https://provider.invalid/v1"
	providerRef := "sha256:" + uciInstalledAcceptanceStringDigest(endpoint)
	if err := uciRealCorpusValidateConfiguredProviderRef(endpoint, providerRef); err != nil {
		t.Fatalf("matching endpoint/provider_ref rejected: %v", err)
	}

	baseProviderRef := "sha256:" + uciInstalledAcceptanceStringDigest(strings.TrimSuffix(endpoint, "/v1"))
	err := uciRealCorpusValidateConfiguredProviderRef(endpoint, baseProviderRef)
	if err == nil {
		t.Fatal("provider_ref for endpoint without /v1 was accepted")
	}
	if strings.Contains(err.Error(), endpoint) {
		t.Fatalf("provider_ref mismatch exposed endpoint: %q", err)
	}
}

func uciRealCorpusEmbeddingTestStatus(viewID string, generation int64, embeddingProfileID *string, coverage string, totalCandidates, readyCandidates, pendingJobs uint64, jobState *string) uciRealCorpusEmbeddingStatus {
	status := uciRealCorpusEmbeddingStatus{}
	status.Context.SourceID = "source"
	status.Context.CheckoutID = "checkout"
	status.Context.ViewID = viewID

	status.Context.ProfileID = "profile"
	status.Context.Generation = generation
	status.Embedding.EmbeddingProfileID = embeddingProfileID
	status.Embedding.Coverage = coverage
	status.Embedding.TotalCandidates = totalCandidates
	status.Embedding.ReadyCandidates = readyCandidates
	status.Embedding.PendingJobs = pendingJobs
	status.Embedding.JobState = jobState
	return status
}

func TestUCIRealCorpusEvidenceRecordRejectsPrivateValues(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-repo")
	dsn := "postgres://test-user:test-password@127.0.0.1:5432/test-db"
	providerURL := "https://provider.invalid"
	providerKey := "provider-secret-value"
	record := uciRealCorpusRecord{
		SchemaVersion: uciRealCorpusRecordSchemaVersion,
		Identity: uciRealCorpusIdentity{
			SourceDigest:           uciInstalledAcceptanceStringDigest("source"),
			CheckoutDigest:         uciInstalledAcceptanceStringDigest("checkout"),
			AnalysisProfileDigest:  uciInstalledAcceptanceStringDigest("analysis-profile"),
			EmbeddingProfileDigest: uciInstalledAcceptanceStringDigest("embedding-profile"),
			FinalViewDigest:        uciInstalledAcceptanceStringDigest("view"),
		},
		Rerun: uciRealCorpusRerunRecipe{
			Command:                      uciRealCorpusRerunCommand,
			RequiredEnvironmentVariables: append([]string(nil), uciRealCorpusRequiredRerunEnvironmentVariables[:]...),
		},
	}
	if err := uciRealCorpusValidateEvidenceRecord(record, root, dsn, providerURL, providerKey); err != nil {
		t.Fatalf("safe evidence record rejected: %v", err)
	}
	record.Identity.SourceDigest = root
	if err := uciRealCorpusValidateEvidenceRecord(record, root, dsn, providerURL, providerKey); err == nil {
		t.Fatal("raw source identity was retained")
	}
	record.Identity.SourceDigest = uciInstalledAcceptanceStringDigest("source")

	record.Semantic.ExpectedPath = root
	if err := uciRealCorpusValidateEvidenceRecord(record, root, dsn, providerURL, providerKey); err == nil {
		t.Fatal("absolute private locator was retained")
	}
	record.Semantic.ExpectedPath = uciRealCorpusCanaries[uciRealCorpusGoCallerPath]
	if err := uciRealCorpusValidateEvidenceRecord(record, root, dsn, providerURL, providerKey); err == nil {
		t.Fatal("source body was retained")
	}
	record.Semantic.ExpectedPath = providerKey
	if err := uciRealCorpusValidateEvidenceRecord(record, root, dsn, providerURL, providerKey); err == nil {
		t.Fatal("provider credential was retained")
	}
	record.Semantic.ExpectedPath = ""
	record.Rerun.TerminalArtifactDigest = providerKey
	if err := uciRealCorpusValidateEvidenceRecord(record, root, dsn, providerURL, providerKey); err == nil {
		t.Fatal("raw terminal artifact was retained")
	}
}

func uciRealCorpusValidateConfiguredProviderRef(providerURL, providerRef string) error {
	if providerRef != "sha256:"+uciInstalledAcceptanceStringDigest(providerURL) {
		return errors.New("real-corpus configured provider_ref does not match endpoint_digest")
	}
	return nil
}

func uciRealCorpusBuildRerunRecipe(terminalArtifact string) (uciRealCorpusRerunRecipe, error) {
	recipe := uciRealCorpusRerunRecipe{
		Command:                      uciRealCorpusRerunCommand,
		RequiredEnvironmentVariables: append([]string(nil), uciRealCorpusRequiredRerunEnvironmentVariables[:]...),
	}
	terminalArtifact = strings.TrimSpace(terminalArtifact)
	if terminalArtifact == "" {
		return recipe, nil
	}
	if privacy.ContainsSecrets(terminalArtifact) {
		return uciRealCorpusRerunRecipe{}, errors.New("real-corpus terminal artifact must be explicitly non-secret")
	}
	recipe.TerminalArtifactDigest = uciInstalledAcceptanceStringDigest(terminalArtifact)
	return recipe, nil
}

func uciRealCorpusBuildReceiptIdentity(initial, final uciInstalledAcceptancePublication, finalEmbedding uciRealCorpusEmbeddingStatus) (uciRealCorpusIdentity, error) {
	initialSource := strings.TrimSpace(initial.sourceID)
	initialCheckout := strings.TrimSpace(initial.checkoutID)
	initialProfile := strings.TrimSpace(initial.profileID)
	finalSource := strings.TrimSpace(final.sourceID)
	finalCheckout := strings.TrimSpace(final.checkoutID)
	finalProfile := strings.TrimSpace(final.profileID)
	finalView := strings.TrimSpace(final.viewID)
	if initialSource == "" || initialCheckout == "" || initialProfile == "" || finalSource == "" || finalCheckout == "" || finalProfile == "" || finalView == "" {
		return uciRealCorpusIdentity{}, errors.New("real-corpus receipt identity is incomplete")
	}
	if initialSource != finalSource || initialCheckout != finalCheckout || initialProfile != finalProfile || finalView == strings.TrimSpace(initial.viewID) {
		return uciRealCorpusIdentity{}, errors.New("real-corpus final receipt identity is not bound to the initial publication")
	}
	if strings.TrimSpace(finalEmbedding.Context.SourceID) != finalSource || strings.TrimSpace(finalEmbedding.Context.CheckoutID) != finalCheckout || strings.TrimSpace(finalEmbedding.Context.ProfileID) != finalProfile || strings.TrimSpace(finalEmbedding.Context.ViewID) != finalView || finalEmbedding.Context.Generation != final.generation {
		return uciRealCorpusIdentity{}, errors.New("real-corpus final embedding context is not bound to the final publication")
	}
	if finalEmbedding.Embedding.EmbeddingProfileID == nil {
		return uciRealCorpusIdentity{}, errors.New("real-corpus final embedding profile is absent")
	}
	embeddingProfile := strings.TrimSpace(*finalEmbedding.Embedding.EmbeddingProfileID)
	if embeddingProfile == "" {
		return uciRealCorpusIdentity{}, errors.New("real-corpus final embedding profile is empty")
	}
	return uciRealCorpusIdentity{
		SourceDigest:           uciInstalledAcceptanceStringDigest(finalSource),
		CheckoutDigest:         uciInstalledAcceptanceStringDigest(finalCheckout),
		AnalysisProfileDigest:  uciInstalledAcceptanceStringDigest(finalProfile),
		EmbeddingProfileDigest: uciInstalledAcceptanceStringDigest(embeddingProfile),
		FinalViewDigest:        uciInstalledAcceptanceStringDigest(finalView),
	}, nil
}

func uciRealCorpusFrozenPresentSourceBytes(root string, frozen uciRealCorpusFrozenManifest) (uint64, error) {
	if err := uciRealCorpusValidateFrozenManifest(frozen); err != nil {
		return 0, errors.New("real-corpus frozen census is invalid while measuring present source bytes")
	}
	var total uint64
	var present uint64
	for _, entry := range frozen.entries {
		if entry.scannerState != uci.IndexFilePresent {
			continue
		}
		if !uciRealCorpusCompositionPath(entry.path) {
			return 0, errors.New("real-corpus frozen census contains an invalid present source path")
		}
		source, err := os.Open(filepath.Join(root, filepath.FromSlash(entry.path)))
		if err != nil {
			return 0, errors.New("real-corpus frozen present source could not be opened")
		}
		hasher := sha256.New()
		byteCount, copyErr := io.Copy(hasher, source)
		closeErr := source.Close()
		if copyErr != nil || closeErr != nil || byteCount < 0 {
			return 0, errors.New("real-corpus frozen present source could not be measured")
		}
		if "sha256:"+hex.EncodeToString(hasher.Sum(nil)) != entry.contentDigest {
			return 0, errors.New("real-corpus present source does not match the frozen census")
		}
		bytes := uint64(byteCount)
		if total > ^uint64(0)-bytes || present == ^uint64(0) {
			return 0, errors.New("real-corpus present source byte accounting overflowed")
		}
		total += bytes
		present++
	}
	if present == 0 {
		return 0, errors.New("real-corpus frozen census has no present source")
	}
	return total, nil
}

func runUCIRealCorpusInstalledAcceptance(
	ctx context.Context,
	request uciInstalledAcceptanceRequest,
	providerURL, providerModel, providerRef, preprocessing string,
	sourceTransferApproved bool,
	rerun uciRealCorpusRerunRecipe,
) (record uciRealCorpusRecord, retErr error) {
	root, err := uciInstalledAcceptancePhysicalPath(request.CandidateSourceRoot)
	if err != nil {
		return record, err
	}
	semanticQuery, err := uciRealCorpusSemanticQuery()
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
	frozenManifest, err := uciFreezeRealCorpusManifest(ctx, root, semanticQuery)
	if err != nil {
		return record, err
	}
	presentSourceBytes, err := uciRealCorpusFrozenPresentSourceBytes(root, frozenManifest)
	if err != nil {
		return record, err
	}

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
		uciRealCorpusEmbeddingURLEnv+"="+providerURL,
		uciRealCorpusEmbeddingModelEnv+"="+providerModel,
		uciRealCorpusEmbeddingAPIKeyEnv+"="+os.Getenv(uciRealCorpusEmbeddingAPIKeyEnv),
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
	initialEmbedding, initialPublication, err := uciWaitForRealCorpusEmbeddings(ctx, client, selection, initialPublication)
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
	composition, err := uciVerifyRealCorpusComposition(ctx, authority, initialPublication, frozenManifest)
	if err != nil {
		return record, err
	}
	if composition.MembershipCount != uint64(initialCounts.Memberships) {
		return record, errors.New("real-corpus exact composition and count receipt disagree")
	}
	if initialCounts.StagedParts < 0 || initialCounts.StagedPartBytes < 0 || uint64(initialCounts.StagedParts) != composition.Packing.ObservedPartCount || uint64(initialCounts.StagedPartBytes) != composition.Packing.ObservedTotalEncodedBytes {
		return record, errors.New("real-corpus packed part receipt and count observation disagree")
	}
	capacityRecovery, err := uciVerifyRealCorpusCapacityRecovery(ctx, authority, initialPublication, composition)
	if err != nil {
		return record, err
	}

	semantic, err := uciRealCorpusSemanticProof(ctx, client, selection, initialPublication, root, semanticQuery)
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
	nativeGraph, err := uciVerifyRealCorpusNativeGraph(ctx, authority, initialPublication)
	if err != nil {
		return record, err
	}

	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(uciRealCorpusGoCalleePath)), []byte(uciRealCorpusChangedCallee), 0o600); err != nil {
		return record, err
	}
	changedEmbedding, changedPublication, err := uciWaitForRealCorpusWatcherState(ctx, client, selection, initialPublication, "UCIRealCorpusCallee", uciRealCorpusGoCalleePath, true)
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
	identity, err := uciRealCorpusBuildReceiptIdentity(initialPublication, changedPublication, changedEmbedding)
	if err != nil {
		return record, err
	}

	record.SchemaVersion = uciRealCorpusRecordSchemaVersion
	record.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	record.Candidate = uciRealCorpusCandidate{Commit: candidateCommit, Tree: candidateTree}
	record.Identity = identity
	record.Rerun = rerun
	record.Pilot.Source = "engram-production-scanner-corpus"
	record.Pilot.CorpusDefinition = "tracked-and-nonignored-untracked"
	record.Pilot.PresentSourceBytes = presentSourceBytes
	record.Pilot.SizeSanity = initialCounts.Memberships >= 1000 && initialCounts.Artifacts >= 1000
	record.Pilot.ExactComposition = composition.MembershipCount == composition.BaselineMembershipCount+composition.CanaryMembershipCount && composition.MembershipCount > 0
	record.Pilot.FullScannerCorpus = record.Pilot.ExactComposition &&
		composition.Packing.ObservedPartCount > 0 &&
		composition.Packing.ObservedPartCount <= composition.Packing.ConfiguredMaxPartCount &&
		composition.Packing.ObservedTotalEncodedBytes <= composition.Packing.ConfiguredMaxTotalBytes &&
		composition.Packing.ObservedMaxEncodedPartBytes <= composition.Packing.ConfiguredMaxPartBytes
	record.Pilot.IgnoredFilesIncluded = false
	record.Pilot.InitialGitClean = true
	record.Provider = uciRealCorpusProvider{
		EndpointDigest:         uciInstalledAcceptanceStringDigest(providerURL),
		ProviderRef:            providerRef,
		Model:                  providerModel,
		Dimension:              1536,
		CandidatePageSize:      uci.DefaultEmbeddingWorkerLimits().CandidatePageSize,
		ProviderBatchSize:      uci.DefaultEmbeddingWorkerLimits().ProviderBatchSize,
		ProviderConcurrency:    uci.DefaultEmbeddingWorkerLimits().ProviderConcurrency,
		PreprocessingRevision:  preprocessing,
		CredentialRetained:     false,
		SourceTransferApproved: sourceTransferApproved,
	}
	record.Initial = initialCounts
	record.AfterChange = changedCounts
	record.Semantic = semantic
	record.Graph = graph
	record.Composition = composition
	record.CapacityRecovery = capacityRecovery
	record.NativeGraph = nativeGraph
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
	record.Scope.EvidenceRecordSecretsRetained = false
	record.Scope.EvidenceRecordSourceBodiesRetained = false
	record.Scope.EvidenceRecordPrivateLocatorsRetained = false
	record.Scope.ProcessRestartProof = false
	record.Scope.ProductionMutation = false
	record.Scope.ReleaseClaim = false
	if err := uciRealCorpusValidateEvidenceRecord(record, root, request.TestPostgresDSN, providerURL, os.Getenv(uciRealCorpusEmbeddingAPIKeyEnv)); err != nil {
		return record, err
	}
	if err := uciRealCorpusValidateRecordForWrite(record); err != nil {
		return record, err
	}
	if !record.Pilot.FullScannerCorpus || !record.Pilot.SizeSanity || !record.Pilot.ExactComposition || !record.Installed.StandardMCP || !record.CapacityRecovery.AllGuaranteesObserved || record.Scope.ProcessRestartProof {
		return record, errors.New("real-corpus result did not prove exact production-scanner corpus, capacity recovery, and installed standard-MCP coverage within configured limits")
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

func uciRealCorpusEmbeddingSafeJobState(value *string) string {
	if value == nil {
		return "absent"
	}
	switch *value {
	case "queued", "running", "retry_scheduled", "succeeded", "failed_terminal", "cancelled", "obsolete":
		return *value
	default:
		return "unknown"
	}
}

func uciRealCorpusEmbeddingSafeCoverage(value string) string {
	switch value {
	case string(uci.IndexCoverageComplete), string(uci.IndexCoveragePartial), string(uci.IndexCoverageUnavailable):
		return value
	default:
		return "unknown"
	}
}

func uciRealCorpusEmbeddingSafeErrorCode(value *string) string {
	if value == nil {
		return "absent"
	}
	code := uci.EmbeddingFailureCode(*value)
	if !code.ValidForEmbeddingJob() {
		return "unknown"
	}
	return string(code)
}

func uciRealCorpusEmbeddingSafeRetryAfter(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func uciClassifyRealCorpusEmbedding(status uciRealCorpusEmbeddingStatus, stalled bool) uciRealCorpusEmbeddingClassification {
	retryAfter := uciRealCorpusEmbeddingSafeRetryAfter(status.Embedding.RetryAfter)
	summary := uciRealCorpusEmbeddingSummary{
		viewPresent:             status.Context.ViewID != "",
		generation:              status.Context.Generation,
		embeddingProfilePresent: status.Embedding.EmbeddingProfileID != nil,
		jobState:                uciRealCorpusEmbeddingSafeJobState(status.Embedding.JobState),
		coverage:                uciRealCorpusEmbeddingSafeCoverage(status.Embedding.Coverage),
		totalCandidates:         status.Embedding.TotalCandidates,
		readyCandidates:         status.Embedding.ReadyCandidates,
		pendingJobs:             status.Embedding.PendingJobs,
		errorCode:               uciRealCorpusEmbeddingSafeErrorCode(status.Embedding.ErrorCode),
		retryAfterPresent:       !retryAfter.IsZero(),
	}
	classification := uciRealCorpusEmbeddingClassification{
		stage: uciRealCorpusEmbeddingStageStillProgressing,
		progress: uciRealCorpusEmbeddingProgress{
			viewID:                  status.Context.ViewID,
			generation:              status.Context.Generation,
			embeddingProfilePresent: summary.embeddingProfilePresent,
			jobState:                summary.jobState,
			totalCandidates:         summary.totalCandidates,
			readyCandidates:         summary.readyCandidates,
			pendingJobs:             summary.pendingJobs,
			coverage:                summary.coverage,
			errorCode:               summary.errorCode,
			retryAfter:              retryAfter,
		},
		summary: summary,
	}
	switch {
	case summary.jobState == "failed_terminal" || summary.jobState == "cancelled" || summary.jobState == "obsolete":
		classification.stage = uciRealCorpusEmbeddingStageTerminalFailed
	case summary.embeddingProfilePresent && summary.coverage == string(uci.IndexCoverageComplete) && summary.totalCandidates > 0 && summary.readyCandidates == summary.totalCandidates && summary.pendingJobs == 0:
		classification.stage = uciRealCorpusEmbeddingStageReady
	case summary.jobState == "succeeded":
		classification.stage = uciRealCorpusEmbeddingStageTerminalSucceededInconsistent
	case stalled:
		classification.stage = uciRealCorpusEmbeddingStageNoProgress
	}
	return classification
}

func (window uciRealCorpusEmbeddingProgressWindow) observe(progress uciRealCorpusEmbeddingProgress, now time.Time) (uciRealCorpusEmbeddingProgressWindow, bool) {
	if !window.initialized || window.progress != progress {
		return uciRealCorpusEmbeddingProgressWindow{progress: progress, observedAt: now, initialized: true}, false
	}
	deadline := window.observedAt.Add(uciRealCorpusEmbeddingStallWindow)
	if progress.jobState == "retry_scheduled" && !progress.retryAfter.IsZero() {
		retryDeadline := progress.retryAfter.Add(uciRealCorpusEmbeddingRetryGrace)
		if retryDeadline.After(deadline) {
			deadline = retryDeadline
		}
	}
	return window, !now.Before(deadline)
}

func (classification uciRealCorpusEmbeddingClassification) waitError() error {
	return uciRealCorpusEmbeddingWaitError{stage: classification.stage, summary: classification.summary}
}

func (err uciRealCorpusEmbeddingWaitError) Error() string {
	return fmt.Sprintf("real-corpus embedding wait stage=%s view_present=%t generation=%d embedding_profile_present=%t job_state=%s coverage=%s total_candidates=%d ready_candidates=%d pending_jobs=%d error_code=%s retry_after_present=%t", err.stage, err.summary.viewPresent, err.summary.generation, err.summary.embeddingProfilePresent, err.summary.jobState, err.summary.coverage, err.summary.totalCandidates, err.summary.readyCandidates, err.summary.pendingJobs, err.summary.errorCode, err.summary.retryAfterPresent)
}

func uciWaitForRealCorpusEmbeddings(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, expected uciInstalledAcceptancePublication) (uciRealCorpusEmbeddingStatus, uciInstalledAcceptancePublication, error) {
	ticker := time.NewTicker(uciRealCorpusEmbeddingPollInterval)
	defer ticker.Stop()
	var progressWindow uciRealCorpusEmbeddingProgressWindow
	var stalled bool
	for {
		payload, err := client.Tool(ctx, "codebase_status", map[string]any{"context_handle": selection.contextHandle})
		if err != nil {
			return uciRealCorpusEmbeddingStatus{}, expected, err
		}
		var status uciRealCorpusEmbeddingStatus
		if err := json.Unmarshal(payload, &status); err != nil {
			return uciRealCorpusEmbeddingStatus{}, expected, err
		}
		current, err := uciRealCorpusEmbeddingPublication(status, expected)
		if err != nil {
			return status, expected, err
		}
		classification := uciClassifyRealCorpusEmbedding(status, false)
		progressWindow, stalled = progressWindow.observe(classification.progress, time.Now())
		classification = uciClassifyRealCorpusEmbedding(status, stalled)
		switch classification.stage {
		case uciRealCorpusEmbeddingStageReady:
			return status, current, nil
		case uciRealCorpusEmbeddingStageTerminalFailed, uciRealCorpusEmbeddingStageTerminalSucceededInconsistent, uciRealCorpusEmbeddingStageNoProgress:
			return status, current, classification.waitError()
		}
		expected = current
		select {
		case <-ctx.Done():
			return status, expected, ctx.Err()
		case <-ticker.C:
		}
	}
}

func uciWaitForRealCorpusWatcherState(
	ctx context.Context,
	client *uciInstalledAcceptanceMCPClient,
	selection uciInstalledAcceptanceSelection,
	previous uciInstalledAcceptancePublication,
	functionName, relativePath string,
	wantPresent bool,
) (uciRealCorpusEmbeddingStatus, uciInstalledAcceptancePublication, error) {
	for {
		publication, err := uciWaitForInstalledAcceptanceWatcherPublication(ctx, client, selection, previous)
		if err != nil {
			return uciRealCorpusEmbeddingStatus{}, uciInstalledAcceptancePublication{}, err
		}
		embedding, current, err := uciWaitForRealCorpusEmbeddings(ctx, client, selection, publication)
		if err != nil {
			return embedding, current, err
		}
		err = uciRequireInstalledAcceptanceWatcherCanary(ctx, client, selection, current, functionName, relativePath, wantPresent)
		if err == nil {
			return embedding, current, nil
		}
		if !errors.Is(err, errUCIInstalledAcceptanceWatcherCanaryMissing) && !errors.Is(err, errUCIInstalledAcceptanceWatcherCanaryPresent) {
			return embedding, current, err
		}
		previous = current
	}
}

func uciRealCorpusEmbeddingPublication(status uciRealCorpusEmbeddingStatus, expected uciInstalledAcceptancePublication) (uciInstalledAcceptancePublication, error) {
	if expected.sourceID == "" || expected.checkoutID == "" || expected.profileID == "" || expected.viewID == "" || expected.generation < 1 {
		return uciInstalledAcceptancePublication{}, errors.New("real-corpus expected publication is incomplete")
	}
	if status.Context.SourceID != expected.sourceID || status.Context.CheckoutID != expected.checkoutID || status.Context.ProfileID != expected.profileID || status.Context.ViewID == "" || status.Context.Generation < expected.generation {
		return uciInstalledAcceptancePublication{}, errors.New("real-corpus embedding status left the expected source, checkout, profile, or generation")
	}
	if status.Context.Generation == expected.generation && status.Context.ViewID != expected.viewID {
		return uciInstalledAcceptancePublication{}, errors.New("real-corpus embedding status changed View without advancing generation")
	}
	current := expected
	current.viewID = status.Context.ViewID
	current.generation = status.Context.Generation
	return current, nil
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
	mismatches := make([]string, 0, 5)
	if persisted.ProviderRef != providerRef {
		mismatches = append(mismatches, "provider_ref")
	}
	if persisted.Model != model {
		mismatches = append(mismatches, "model")
	}
	if persisted.Dimension != 1536 {
		mismatches = append(mismatches, "dimension")
	}
	if persisted.PreprocessingRevision != preprocessing {
		mismatches = append(mismatches, "preprocessing_revision")
	}
	if !persisted.IncludeRelativePath {
		mismatches = append(mismatches, "include_relative_path")
	}
	if len(mismatches) != 0 {
		return fmt.Errorf("real-corpus persisted embedding profile mismatch fields=%s", strings.Join(mismatches, ","))
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
	var coverageJSON string
	if err := db.Raw(`SELECT coverage_json::text FROM ci_views WHERE view_id = ?`, publication.viewID).Scan(&coverageJSON).Error; err != nil {
		return uciRealCorpusCounts{}, err
	}
	if !json.Valid([]byte(coverageJSON)) {
		return uciRealCorpusCounts{}, errors.New("real-corpus coverage JSON is invalid")
	}
	counts.ImmutableStructuralCoverage = append(json.RawMessage(nil), coverageJSON...)
	counts.LiveEmbeddingCoverage = embedding.Embedding.Coverage
	counts.EmbeddingCandidates = embedding.Embedding.TotalCandidates
	counts.ReadyEmbeddings = embedding.Embedding.ReadyCandidates
	counts.PendingEmbeddingJobs = embedding.Embedding.PendingJobs
	if embedding.Embedding.JobState != nil {
		counts.EmbeddingJobState = *embedding.Embedding.JobState
	}
	if counts.Memberships < 1000 || counts.Artifacts < 1000 || counts.Chunks < 1000 || counts.ReadyEmbeddings != counts.EmbeddingCandidates || counts.LiveEmbeddingCoverage != string(uci.IndexCoverageComplete) {
		return counts, errors.New("real-corpus database counts and live embedding readiness do not prove a full ready pilot")
	}
	return counts, nil
}

func uciRealCorpusSemanticProof(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication, root, query string) (uciRealCorpusSemantic, error) {
	expectedSource, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(uciRealCorpusExpectedPath)))
	if err != nil {
		return uciRealCorpusSemantic{}, err
	}
	overlap := uciRealCorpusLexicalOverlap(query, string(expectedSource))
	if len(overlap) != 0 {
		return uciRealCorpusSemantic{}, fmt.Errorf("real-corpus semantic query has lexical overlap: %v", overlap)
	}
	payload, err := client.Tool(ctx, "codebase_search", map[string]any{"context_handle": selection.contextHandle, "query": query, "limit": 5})
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
	proof := uciRealCorpusSemantic{QueryDigest: uciInstalledAcceptanceStringDigest(query), ExpectedPath: uciRealCorpusExpectedPath, RetrievalMode: string(response.Retrieval.Mode), LiveVectorCoverage: *response.Retrieval.VectorCoverage, LexicalOverlap: overlap, ExposureAvailable: response.Exposure != nil}
	var item *uci.QueryItem
	observedPaths := make([]string, 0, len(*response.Items))
	for index := range *response.Items {
		candidate := &(*response.Items)[index]
		observedPaths = append(observedPaths, candidate.Path)
		if candidate.Path == uciRealCorpusExpectedPath && item == nil {
			proof.ExpectedRank = index + 1
			proof.MatchSources = make([]string, len(candidate.MatchSources))
			for matchIndex, source := range candidate.MatchSources {
				proof.MatchSources[matchIndex] = string(source)
			}
			item = candidate
		}
	}
	if item == nil || proof.ExpectedRank > 5 || len(proof.MatchSources) != 1 || proof.MatchSources[0] != string(uci.QueryMatchVector) {
		return proof, fmt.Errorf("real-corpus semantic query did not return the expected vector-backed source in top five: observed_paths=%q", observedPaths)
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
		return proof, fmt.Errorf("real-corpus MCP graph did not expose both outgoing and reverse call navigation: outgoing_found=%t outgoing_target=%s outgoing={%s}; incoming_found=%t incoming_target=%s incoming={%s}", proof.OutgoingMCPCall, uciRealCorpusGraphTargetKind(caller.Ref), uciRealCorpusGraphResponseSummary(outgoing), proof.IncomingMCPReverse, uciRealCorpusGraphTargetKind(callee.Ref), uciRealCorpusGraphResponseSummary(incoming))
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

func uciRealCorpusFindItem(ctx context.Context, client *uciInstalledAcceptanceMCPClient, handle string, publication uciInstalledAcceptancePublication, functionName, expectedPath string) (uci.QueryItem, error) {
	payload, err := client.Tool(ctx, "codebase_search", map[string]any{"context_handle": handle, "query": functionName, "path_prefix": expectedPath, "limit": 10})
	if err != nil {
		return uci.QueryItem{}, err
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil || !uciInstalledAcceptanceQueryMatchesPublication(response, publication) || response.Items == nil {
		return uci.QueryItem{}, errors.New("real-corpus graph search did not return selected-View items")
	}
	item, found := uciRealCorpusFindQualifiedGoItem(*response.Items, expectedPath, functionName)
	if !found {
		return uci.QueryItem{}, fmt.Errorf("real-corpus graph search omitted qualified %s function at %s", functionName, expectedPath)
	}
	return item, nil
}

func uciRealCorpusFindQualifiedGoItem(items uci.QueryItems, expectedPath, functionName string) (uci.QueryItem, bool) {
	for _, item := range items {
		name, isGoFunction := uciInstalledAcceptanceGoFunctionName(item.Ref.EntityKey)
		if item.Path == expectedPath && isGoFunction && name == functionName {
			return item, true
		}
	}
	return uci.QueryItem{}, false
}

func uciRealCorpusGraphCall(ctx context.Context, client *uciInstalledAcceptanceMCPClient, handle string, publication uciInstalledAcceptancePublication, target uci.QueryEntityRef, direction string) (uci.QueryResponse, error) {
	payload, err := client.Tool(ctx, "codebase_graph", map[string]any{
		"context_handle": handle,
		"action":         "neighbors",
		"target":         map[string]any{"source_id": target.SourceID, "view_id": target.ViewID, "entity_key": target.EntityKey},
		"direction":      direction,
		"relations":      []string{"calls"},
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
	if err != nil {
		return uci.QueryResponse{}, errors.New("real-corpus graph response is invalid")
	}
	if !uciInstalledAcceptanceQueryMatchesPublication(response, publication) {
		return uci.QueryResponse{}, errors.New("real-corpus graph response did not retain the selected View")
	}
	if response.Graph == nil {
		return uci.QueryResponse{}, fmt.Errorf("real-corpus %s graph response has no graph: %s", direction, uciRealCorpusGraphResponseSummary(response))
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

func uciRealCorpusGraphTargetKind(ref uci.QueryEntityRef) string {
	if _, ok := uciInstalledAcceptanceGoFunctionName(ref.EntityKey); ok {
		return "go_function"
	}
	return "other"
}

func uciRealCorpusGraphResponseSummary(response uci.QueryResponse) string {
	errorCode := "none"
	if response.Error != nil {
		errorCode = string(response.Error.Code)
	}
	truncated := "absent"
	if response.Truncated != nil {
		truncated = fmt.Sprintf("%t", *response.Truncated)
	}
	stopReason := "absent"
	relationCounts := make(map[string]int)
	if response.Graph != nil {
		stopReason = string(response.Graph.StopReason)
		for _, edge := range response.Graph.Edges {
			relationCounts[string(edge.Relation)]++
		}
	}
	relations := make([]string, 0, len(relationCounts))
	for relation, count := range relationCounts {
		relations = append(relations, fmt.Sprintf("%s=%d", relation, count))
	}
	sort.Strings(relations)
	if len(relations) == 0 {
		relations = append(relations, "none")
	}
	return fmt.Sprintf("status=%s,error=%s,truncated=%s,stop=%s,relations=%s", response.Status, errorCode, truncated, stopReason, strings.Join(relations, ","))
}

func uciRealCorpusSemanticQuery() (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(uciRealCorpusQueryBase64)
	if err != nil || !utf8.Valid(decoded) || strings.TrimSpace(string(decoded)) == "" {
		return "", errors.New("real-corpus semantic query encoding is invalid")
	}
	return string(decoded), nil
}

func uciRealCorpusValidateRecordForWrite(record uciRealCorpusRecord) error {
	if record.SchemaVersion != uciRealCorpusRecordSchemaVersion {
		return errors.New("real-corpus evidence record schema is not v3")
	}
	if record.Pilot.PresentSourceBytes == 0 {
		return errors.New("real-corpus evidence record has no frozen present source byte count")
	}
	if err := uciRealCorpusValidateIdentity(record.Identity); err != nil {
		return err
	}
	if err := uciRealCorpusValidateRerunRecipe(record.Rerun); err != nil {
		return err
	}
	if err := uciRealCorpusValidateCapacityRecovery(record.CapacityRecovery); err != nil {
		return err
	}
	return nil
}

func uciRealCorpusValidateIdentity(identity uciRealCorpusIdentity) error {
	for _, digest := range []string{
		identity.SourceDigest,
		identity.CheckoutDigest,
		identity.AnalysisProfileDigest,
		identity.EmbeddingProfileDigest,
		identity.FinalViewDigest,
	} {
		if !uciInstalledAcceptanceIsSHA256(digest) {
			return errors.New("real-corpus evidence record has an unredacted identity")
		}
	}
	return nil
}

func uciRealCorpusValidateRerunRecipe(recipe uciRealCorpusRerunRecipe) error {
	if recipe.Command != uciRealCorpusRerunCommand || len(recipe.RequiredEnvironmentVariables) != len(uciRealCorpusRequiredRerunEnvironmentVariables) {
		return errors.New("real-corpus evidence record rerun recipe is incomplete")
	}
	for index, name := range uciRealCorpusRequiredRerunEnvironmentVariables {
		if recipe.RequiredEnvironmentVariables[index] != name {
			return errors.New("real-corpus evidence record rerun environment names are not the exact sorted set")
		}
	}
	if recipe.TerminalArtifactDigest != "" && !uciInstalledAcceptanceIsSHA256(recipe.TerminalArtifactDigest) {
		return errors.New("real-corpus evidence record has an unredacted terminal artifact")
	}
	return nil
}

func uciRealCorpusValidateCapacityRecovery(recovery uciRealCorpusCapacityRecovery) error {
	if !recovery.FullCorpusFragmented ||
		!recovery.LegacyPartLimitExceeded ||
		!recovery.LegacyBuildLimitExceeded ||
		!recovery.InterruptedStagingObserved ||
		!recovery.ResumedStagingObserved ||
		!recovery.ExactPartDigestsObserved ||
		!recovery.IncompleteFinalizeRejected ||
		!recovery.ConflictingFinalizeRejected ||
		!recovery.AtomicFinalizeObserved ||
		!recovery.PriorViewPreservedAfterIncomplete ||
		!recovery.PriorViewPreservedAfterConflict ||
		!recovery.SuccessfulFinalizeObserved ||
		!recovery.AllGuaranteesObserved {
		return errors.New("real-corpus capacity recovery proof is incomplete")
	}
	for _, count := range []uint64{
		recovery.SourcePartCount,
		recovery.SourcePayloadBytes,
		recovery.SourceMaxPartBytes,
		recovery.LegacyMaxPartBytes,
		recovery.LegacyMaxBuildBytes,
		recovery.QuotaMaxParts,
		recovery.QuotaMaxBuildBytes,
		recovery.QuotaMaxPartBytes,
		recovery.InterruptedPartCount,
		recovery.ResumedPartCount,
		recovery.PriorViewCount,
		recovery.AfterIncompleteViewCount,
		recovery.AfterConflictViewCount,
		recovery.PublishedViewCount,
		recovery.ReplayViewCount,
	} {
		if count == 0 {
			return errors.New("real-corpus capacity recovery proof has an empty measurement")
		}
	}
	if recovery.SourcePartCount < 2 || recovery.InterruptedPartCount >= recovery.SourcePartCount || recovery.ResumedPartCount != recovery.SourcePartCount || recovery.SourcePayloadBytes <= recovery.LegacyMaxBuildBytes || recovery.SourceMaxPartBytes <= recovery.LegacyMaxPartBytes || recovery.SourcePartCount > recovery.QuotaMaxParts || recovery.SourcePayloadBytes > recovery.QuotaMaxBuildBytes || recovery.SourceMaxPartBytes > recovery.QuotaMaxPartBytes {
		return errors.New("real-corpus capacity recovery measurements violate the bounded fragmentation contract")
	}
	if recovery.SourcePartsDigest != recovery.ResumedPartsDigest || recovery.PriorViewDigest == recovery.PublishedViewDigest {
		return errors.New("real-corpus capacity recovery digests do not prove exact resume and a new atomic view")
	}
	for _, digest := range []string{
		recovery.SourcePartsDigest,
		recovery.ResumedPartsDigest,
		recovery.ManifestDigest,
		recovery.PriorViewDigest,
		recovery.PublishedViewDigest,
	} {
		if !uciInstalledAcceptanceIsSHA256(digest) {
			return errors.New("real-corpus capacity recovery proof has an unredacted digest")
		}
	}
	if recovery.PriorViewCount == ^uint64(0) || recovery.AfterIncompleteViewCount != recovery.PriorViewCount || recovery.AfterConflictViewCount != recovery.PriorViewCount || recovery.PublishedViewCount != recovery.PriorViewCount+1 || recovery.ReplayViewCount != recovery.PublishedViewCount {
		return errors.New("real-corpus capacity recovery proof did not preserve the prior view through failed finalize attempts")
	}
	return nil
}

func uciRealCorpusValidateEvidenceRecord(record uciRealCorpusRecord, root, dsn, providerURL, providerKey string) error {
	if err := uciRealCorpusValidateIdentity(record.Identity); err != nil {
		return err
	}
	if err := uciRealCorpusValidateRerunRecipe(record.Rerun); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return errors.New("real-corpus evidence record could not be encoded for privacy validation")
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil || uciRealCorpusEvidenceValueHasPrivateLocator(value) {
		return errors.New("real-corpus evidence record retained a private locator")
	}
	sensitive := []string{root, filepath.ToSlash(root), dsn, providerURL, providerKey, uciRealCorpusChangedCallee}
	for _, body := range uciRealCorpusCanaries {
		sensitive = append(sensitive, body)
	}
	if uciRealCorpusEvidenceContainsSensitiveValue(value, sensitive) {
		return errors.New("real-corpus evidence record retained a private input or source body")
	}
	text := string(encoded)
	if privacy.ContainsSecrets(text) {
		return errors.New("real-corpus evidence record retained secret-shaped content")
	}
	return nil
}

func uciRealCorpusEvidenceValueHasPrivateLocator(value any) bool {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		lower := strings.ToLower(trimmed)
		return filepath.IsAbs(trimmed) || strings.Contains(trimmed, "://") || strings.HasPrefix(lower, "file:") || strings.HasPrefix(trimmed, "~")
	case []any:
		for _, child := range typed {
			if uciRealCorpusEvidenceValueHasPrivateLocator(child) {
				return true
			}
		}
	case map[string]any:
		for _, child := range typed {
			if uciRealCorpusEvidenceValueHasPrivateLocator(child) {
				return true
			}
		}
	}
	return false
}

func uciRealCorpusEvidenceContainsSensitiveValue(value any, candidates []string) bool {
	switch typed := value.(type) {
	case string:
		for _, candidate := range candidates {
			if candidate != "" && strings.Contains(typed, candidate) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if uciRealCorpusEvidenceContainsSensitiveValue(child, candidates) {
				return true
			}
		}
	case map[string]any:
		for _, child := range typed {
			if uciRealCorpusEvidenceContainsSensitiveValue(child, candidates) {
				return true
			}
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
