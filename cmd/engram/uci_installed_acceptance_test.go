package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	uciInstalledAcceptanceTestPostgresDSNEnv = "ENGRAM_UCI_INSTALLED_TEST_DATABASE_DSN"

	uciInstalledAcceptanceArtifactServer = "server"
	uciInstalledAcceptanceArtifactDaemon = "daemon"
	uciInstalledAcceptanceArtifactParser = "parser"

	uciInstalledAcceptanceRelativePath = "pkg/fixture.go"
	uciInstalledAcceptanceSharedSymbol = "SharedTarget"
	uciInstalledAcceptanceAlphaCallee  = "AlphaCallee"
	uciInstalledAcceptanceBetaCallee   = "BetaCallee"

	uciInstalledAcceptanceAlphaSource = `package fixture

func SharedTarget() string { return AlphaCallee() }
func AlphaCallee() string { return "T071_ALPHA_DIRTY_BODY" }
`
	uciInstalledAcceptanceBetaSource = `package fixture

func SharedTarget() string { return BetaCallee() }
func BetaCallee() string { return "T071_BETA_DIRTY_BODY" }
`
)

// T072 owns this driver. The request deliberately provides only candidate source,
// disposable roots, a test-only database target, and synthetic fixture data: it
// has no service, in-process transport, or host-profile injection seam.
var _ func(context.Context, uciInstalledAcceptanceRequest) (uciInstalledAcceptanceResult, error) = runUCIInstalledAcceptance

func TestUCIInstalledAcceptanceTokenExpiryCoversOperationDeadline(t *testing.T) {
	now := time.Date(2026, time.September, 7, 6, 0, 0, 0, time.UTC)
	requireExpiry := func(t *testing.T, ctx context.Context, want time.Time) {
		t.Helper()
		if got := uciInstalledAcceptanceTokenExpiry(ctx, now); !got.Equal(want) {
			t.Fatalf("token expiry = %s, want %s", got, want)
		}
	}

	t.Run("default", func(t *testing.T) {
		requireExpiry(t, context.Background(), now.Add(uciInstalledAcceptanceTokenDefaultTTL))
	})
	t.Run("short operation keeps default", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), now.Add(10*time.Minute))
		defer cancel()
		requireExpiry(t, ctx, now.Add(uciInstalledAcceptanceTokenDefaultTTL))
	})
	t.Run("long operation includes grace", func(t *testing.T) {
		deadline := now.Add(90 * time.Minute)
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		requireExpiry(t, ctx, deadline.Add(uciInstalledAcceptanceTokenGrace))
	})
}

func TestUCIInstalledStandardClientsKeepDirtyViewsIsolated(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows installed standard-client acceptance")
	}

	request := uciInstalledAcceptanceConfiguredRequest(t)
	uciInstalledAcceptanceRequireCleanup(t, request)

	ctx, cancel := context.WithTimeout(context.Background(), request.OperationTimeout+5*time.Second)
	defer cancel()
	result, err := runUCIInstalledAcceptance(ctx, request)
	if err != nil {
		t.Fatalf("run installed UCI standard-client acceptance: %v", err)
	}

	if result.InstallHarnessVersion != uciInstallHarnessVersionV1 {
		t.Fatal("installed acceptance did not use the versioned T017 installation harness")
	}

	uciInstalledAcceptanceRequireArtifacts(t, result)
	uciInstalledAcceptanceRequireExternalProcesses(t, result)
	uciInstalledAcceptanceRequireReservedLoopback(t, request, result)
	for _, client := range []string{
		uciInstalledAcceptanceClientA,
		uciInstalledAcceptanceClientB,
		uciInstalledAcceptanceClientC,
	} {
		uciInstalledAcceptanceRequireStandardMCP(t, result, client)
	}
	if !result.SimultaneousAB {
		t.Fatal("installed acceptance did not overlap the two bound standard MCP clients")
	}

	uciInstalledAcceptanceRequireBootstrap(t, result)
	uciInstalledAcceptanceRequireWorktreeTopology(t, request, result)
	uciInstalledAcceptanceRequireClientContexts(t, result)
	uciInstalledAcceptanceRequireIsolation(t, request, result)
	uciInstalledAcceptanceRequireDefaultIsolation(t, result)
	uciInstalledAcceptanceRequireRefusals(t, result)
	uciInstalledAcceptanceRequireRecorderBehavior(t, result)
	uciInstalledAcceptanceRequireWatcherIsolation(t, result)
	uciInstalledAcceptanceRequireRestartContinuity(t, request, result)

	if !result.Cleanup.ProcessTreeClosed ||
		!result.Cleanup.InstallRootRemoved ||
		!result.Cleanup.FixtureRootRemoved ||
		!result.Cleanup.LocalStateRootRemoved {
		t.Fatal("installed acceptance did not close its child tree and disposable state")
	}
	receipt, receiptErr := BuildUCIInstalledReceipt(result)
	if receiptErr != nil {
		t.Fatalf("assemble installed receipt from current result: %v", receiptErr)
	}
	if _, receiptErr := EncodeUCIInstalledReceipt(receipt); receiptErr != nil {
		t.Fatalf("encode installed receipt from current result: %v", receiptErr)
	}
}

func TestUCIInstalledAcceptanceRefusesNonTestPostgresBeforeMaterialization(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows installed standard-client acceptance")
	}

	request := uciInstalledAcceptanceNewRequest(t, "postgres://fixture:fixture@127.0.0.1:54329/engram_production?sslmode=disable")
	uciInstalledAcceptanceRequireCleanup(t, request)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := runUCIInstalledAcceptance(ctx, request)
	if !errors.Is(err, errUCIInstalledAcceptanceNonTestPostgres) {
		t.Fatal("installed acceptance accepted or failed to identify a non-test PostgreSQL target")
	}
}

func uciInstalledAcceptanceConfiguredRequest(t *testing.T) uciInstalledAcceptanceRequest {
	t.Helper()

	dsn := strings.TrimSpace(os.Getenv(uciInstalledAcceptanceTestPostgresDSNEnv))
	if dsn == "" {
		t.Skip("installed acceptance requires an explicit disposable PostgreSQL target")
	}
	uciInstalledAcceptanceRequireTestPostgres(t, dsn)
	return uciInstalledAcceptanceNewRequest(t, dsn)
}

func uciInstalledAcceptanceNewRequest(t *testing.T, testPostgresDSN string) uciInstalledAcceptanceRequest {
	t.Helper()

	sandbox := t.TempDir()
	return uciInstalledAcceptanceRequest{
		Version:                   uciInstalledAcceptanceVersionV1,
		InstallHarnessVersion:     uciInstallHarnessVersionV1,
		CandidateSourceRoot:       uciInstalledAcceptanceCandidateSourceRoot(t),
		InstallRoot:               filepath.Join(sandbox, "installed UCI Кириллица"),
		FixtureRoot:               filepath.Join(sandbox, "fixture worktrees Кириллица"),
		LocalStateRoot:            filepath.Join(sandbox, "local state Кириллица"),
		TestPostgresDSN:           testPostgresDSN,
		LoopbackHost:              "127.0.0.1",
		ReservedLoopbackPortCount: 2,
		ReadinessTimeout:          10 * time.Second,
		OperationTimeout:          3 * time.Minute,
		Fixture: uciInstalledAcceptanceFixture{
			RelativePath:  uciInstalledAcceptanceRelativePath,
			SharedSymbol:  uciInstalledAcceptanceSharedSymbol,
			PrimarySource: uciInstalledAcceptanceAlphaSource,
			LinkedSource:  uciInstalledAcceptanceBetaSource,
			PrimaryCallee: uciInstalledAcceptanceAlphaCallee,
			LinkedCallee:  uciInstalledAcceptanceBetaCallee,
		},
	}
}

func uciInstalledAcceptanceCandidateSourceRoot(t *testing.T) string {
	t.Helper()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal("locate installed acceptance candidate source")
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	info, err := os.Stat(filepath.Join(root, "go.mod"))
	if err != nil || info.IsDir() {
		t.Fatal("resolve installed acceptance candidate source")
	}
	return root
}

func uciInstalledAcceptanceRequireTestPostgres(t *testing.T, dsn string) {
	t.Helper()

	normalized := strings.ToLower(dsn)
	if !strings.Contains(normalized, "test") ||
		strings.Contains(normalized, "prod") ||
		strings.Contains(normalized, "staging") ||
		(!strings.Contains(normalized, "127.0.0.1") && !strings.Contains(normalized, "localhost")) {
		t.Fatal("installed acceptance requires an explicit loopback non-production test database")
	}
}

func uciInstalledAcceptanceRequireCleanup(t *testing.T, request uciInstalledAcceptanceRequest) {
	t.Helper()
	t.Cleanup(func() {
		for _, root := range []string{request.InstallRoot, request.FixtureRoot, request.LocalStateRoot} {
			if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("installed acceptance left disposable state behind")
			}
		}
	})
}

func uciInstalledAcceptanceRequireArtifacts(t *testing.T, result uciInstalledAcceptanceResult) {
	t.Helper()

	for _, role := range []string{
		uciInstalledAcceptanceArtifactServer,
		uciInstalledAcceptanceArtifactDaemon,
		uciInstalledAcceptanceArtifactParser,
	} {
		artifact, ok := result.Artifacts[role]
		if !ok ||
			!uciInstalledAcceptanceIsSHA256(artifact.CandidateSHA256) ||
			artifact.CandidateSHA256 != artifact.InstalledSHA256 {
			t.Fatal("installed artifact proof is missing or differs from its exact candidate")
		}
	}
}

func uciInstalledAcceptanceRequireExternalProcesses(t *testing.T, result uciInstalledAcceptanceResult) {
	t.Helper()

	processes := []int{
		result.Processes.ServerPID,
		result.Processes.DaemonPID,
		result.Processes.ParserPID,
	}
	for index, pid := range processes {
		if pid <= 0 || pid == os.Getpid() {
			t.Fatal("installed component was not an external child process")
		}
		for _, earlier := range processes[:index] {
			if pid == earlier {
				t.Fatal("installed components must be distinct child processes")
			}
		}
	}
	if result.RawServiceCallCount != 0 {
		t.Fatal("installed acceptance used a raw service call instead of standard MCP")
	}
	if !result.Parser.UsedInstalledArtifact || result.Parser.InvocationCount < 1 ||
		!uciInstalledAcceptanceIsSHA256(result.Parser.RequestDigest) {
		t.Fatal("installed parser worker was not invoked through its built artifact")
	}
}

func uciInstalledAcceptanceRequireReservedLoopback(t *testing.T, request uciInstalledAcceptanceRequest, result uciInstalledAcceptanceResult) {
	t.Helper()

	if result.LoopbackHost != request.LoopbackHost || len(result.ReservedLoopbackPorts) != request.ReservedLoopbackPortCount {
		t.Fatal("installed acceptance did not use the requested reserved loopback topology")
	}
	for index, port := range result.ReservedLoopbackPorts {
		if port < 1 || port > 65535 {
			t.Fatal("installed acceptance reported an invalid loopback port")
		}
		for _, earlier := range result.ReservedLoopbackPorts[:index] {
			if port == earlier {
				t.Fatal("installed acceptance reused a reserved loopback port")
			}
		}
	}
}

func uciInstalledAcceptanceRequireStandardMCP(t *testing.T, result uciInstalledAcceptanceResult, client string) {
	t.Helper()

	transcript, ok := result.ClientTranscripts[client]
	if !ok || !transcript.UsedStdio {
		t.Fatal("installed acceptance did not create a standard stdio client")
	}
	uciInstalledAcceptanceRequireMethodSubsequence(t, transcript.Methods,
		"initialize",
		"notifications/initialized",
		"tools/list",
		"tools/call",
	)
	for _, tool := range []string{
		"codebase_context",
		"codebase_index",
		"codebase_status",
		"codebase_search",
		"codebase_graph",
		"codebase_read",
	} {
		if !uciInstalledAcceptanceContains(transcript.Tools, tool) {
			t.Fatal("standard MCP client did not observe the installed UCI tool surface")
		}
	}
}

func uciInstalledAcceptanceRequireMethodSubsequence(t *testing.T, methods []string, want ...string) {
	t.Helper()

	next := 0
	for _, method := range methods {
		if next < len(want) && method == want[next] {
			next++
		}
	}
	if next != len(want) {
		t.Fatal("standard MCP transcript omitted or reordered required protocol flow")
	}
}

func uciInstalledAcceptanceRequireBootstrap(t *testing.T, result uciInstalledAcceptanceResult) {
	t.Helper()

	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB} {
		if !result.Bootstrap.InitialViewAbsent[client] ||
			!result.Bootstrap.IndexStarted[client] ||
			result.Bootstrap.StatusStates[client] != "observed_current" ||
			result.Bootstrap.BarrierStates[client] != "satisfied" {
			t.Fatal("registered worktree did not bootstrap from no View through status and barrier")
		}
	}
	outcome := result.Bootstrap.UnboundClient
	uciInstalledAcceptanceRequireClosedOutcome(
		t,
		outcome.Status,
		outcome.ErrorCode,
		outcome.ContextDigest,
		outcome.ContentDigests,
		outcome.GraphDigests,
		outcome.ExposureDigest,
		"context_required",
		"CONTEXT_REQUIRED",
	)
}

func uciInstalledAcceptanceRequireWorktreeTopology(t *testing.T, request uciInstalledAcceptanceRequest, result uciInstalledAcceptanceResult) {
	t.Helper()

	if !result.Worktrees.Registered[uciInstalledAcceptanceClientA] ||
		!result.Worktrees.Registered[uciInstalledAcceptanceClientB] ||
		!result.Worktrees.UsedGitArgumentVectors ||
		!result.Worktrees.LinkedGitFile ||
		!result.Worktrees.SameHead ||
		!result.Worktrees.PrimaryDirty ||
		!result.Worktrees.LinkedDirty ||
		!uciInstalledAcceptanceIsSHA256(result.Worktrees.SharedHeadDigest) ||
		result.Worktrees.RelativePathDigest != uciInstalledAcceptanceDigest(request.Fixture.RelativePath) {
		t.Fatal("installed acceptance did not create registered dirty linked worktrees of one source")
	}
}

func uciInstalledAcceptanceRequireClientContexts(t *testing.T, result uciInstalledAcceptanceResult) {
	t.Helper()

	primary, primaryOK := result.ClientContexts[uciInstalledAcceptanceClientA]
	linked, linkedOK := result.ClientContexts[uciInstalledAcceptanceClientB]
	if !primaryOK || !linkedOK ||
		!uciInstalledAcceptanceIsSHA256(primary.SourceDigest) ||
		!uciInstalledAcceptanceIsSHA256(primary.CheckoutDigest) ||
		!uciInstalledAcceptanceIsSHA256(primary.ViewDigest) ||
		!uciInstalledAcceptanceIsSHA256(linked.SourceDigest) ||
		!uciInstalledAcceptanceIsSHA256(linked.CheckoutDigest) ||
		!uciInstalledAcceptanceIsSHA256(linked.ViewDigest) ||
		primary.SourceDigest != linked.SourceDigest ||
		primary.CheckoutDigest == linked.CheckoutDigest ||
		primary.ViewDigest == linked.ViewDigest {
		t.Fatal("simultaneous clients did not retain distinct selected worktree contexts")
	}
}

func uciInstalledAcceptanceRequireIsolation(t *testing.T, request uciInstalledAcceptanceRequest, result uciInstalledAcceptanceResult) {
	t.Helper()

	uciInstalledAcceptanceRequireOnlyViewDigests(
		t,
		result.Observations[uciInstalledAcceptanceClientA].SearchArtifactDigests,
		uciInstalledAcceptanceDigest(request.Fixture.PrimarySource),
	)
	uciInstalledAcceptanceRequireOnlyViewDigests(
		t,
		result.Observations[uciInstalledAcceptanceClientA].GraphCalleeDigests,
		uciInstalledAcceptanceDigest(request.Fixture.PrimaryCallee),
	)
	uciInstalledAcceptanceRequireOnlyViewDigests(
		t,
		result.Observations[uciInstalledAcceptanceClientA].ReadArtifactDigests,
		uciInstalledAcceptanceDigest(request.Fixture.PrimarySource),
	)
	uciInstalledAcceptanceRequireOnlyViewDigests(
		t,
		result.Observations[uciInstalledAcceptanceClientB].SearchArtifactDigests,
		uciInstalledAcceptanceDigest(request.Fixture.LinkedSource),
	)
	uciInstalledAcceptanceRequireOnlyViewDigests(
		t,
		result.Observations[uciInstalledAcceptanceClientB].GraphCalleeDigests,
		uciInstalledAcceptanceDigest(request.Fixture.LinkedCallee),
	)
	uciInstalledAcceptanceRequireOnlyViewDigests(
		t,
		result.Observations[uciInstalledAcceptanceClientB].ReadArtifactDigests,
		uciInstalledAcceptanceDigest(request.Fixture.LinkedSource),
	)
}

func uciInstalledAcceptanceRequireOnlyViewDigests(t *testing.T, got []string, want string) {
	t.Helper()

	if len(got) != 1 || !uciInstalledAcceptanceIsSHA256(got[0]) || got[0] != want {
		t.Fatal("installed query disclosed a body or edge outside the selected dirty View")
	}
}

func uciInstalledAcceptanceRequireDefaultIsolation(t *testing.T, result uciInstalledAcceptanceResult) {
	t.Helper()

	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB} {
		context, ok := result.ClientContexts[client]
		if !ok ||
			result.Defaults.BeforeThirdClientViewDigests[client] != context.ViewDigest ||
			result.Defaults.AfterThirdClientViewDigests[client] != context.ViewDigest {
			t.Fatal("third-client connect changed an established client default")
		}
	}
}

func uciInstalledAcceptanceRequireRefusals(t *testing.T, result uciInstalledAcceptanceResult) {
	t.Helper()

	for _, test := range []struct {
		name   string
		status string
		code   string
	}{
		{name: "denied", status: "forbidden", code: "PERMISSION_DENIED"},
		{name: "revoked", status: "forbidden", code: "PERMISSION_DENIED"},
		{name: "mismatched", status: "context_required", code: "CONTEXT_MISMATCH"},
		{name: "private", status: "context_required", code: "CONTEXT_MISMATCH"},
	} {
		outcome, ok := result.Refusals[test.name]
		if !ok {
			t.Fatal("installed acceptance omitted an authorization refusal case")
		}
		uciInstalledAcceptanceRequireClosedOutcome(
			t,
			outcome.Status,
			outcome.ErrorCode,
			outcome.ContextDigest,
			outcome.ContentDigests,
			outcome.GraphDigests,
			outcome.ExposureDigest,
			test.status,
			test.code,
		)
	}
}

func uciInstalledAcceptanceRequireRecorderBehavior(t *testing.T, result uciInstalledAcceptanceResult) {
	t.Helper()

	unavailable := result.Recorder.InitialUnavailable
	uciInstalledAcceptanceRequireClosedOutcome(
		t,
		unavailable.Status,
		unavailable.ErrorCode,
		unavailable.ContextDigest,
		unavailable.ContentDigests,
		unavailable.GraphDigests,
		unavailable.ExposureDigest,
		"unavailable",
		"EXPOSURE_UNAVAILABLE",
	)
	if result.Recorder.HealthAfterInitialUnavailable != "unavailable" ||
		!uciInstalledAcceptanceIsSHA256(result.Recorder.FirstExposureDigest) ||
		result.Recorder.FirstExposureDigest != result.Recorder.ExactRetryExposureDigest ||
		result.Recorder.HealthBeforeMismatch != "healthy" ||
		result.Recorder.HealthAfterMismatch != "healthy" {
		t.Fatal("installed recorder did not preserve unavailable and exact-retry semantics")
	}

	mismatch := result.Recorder.Mismatch
	uciInstalledAcceptanceRequireClosedOutcome(
		t,
		mismatch.Status,
		mismatch.ErrorCode,
		mismatch.ContextDigest,
		mismatch.ContentDigests,
		mismatch.GraphDigests,
		mismatch.ExposureDigest,
		"unavailable",
		"IDEMPOTENCY_MISMATCH",
	)
}

func uciInstalledAcceptanceRequireWatcherIsolation(t *testing.T, result uciInstalledAcceptanceResult) {
	t.Helper()

	initialA, initialAOK := result.ClientContexts[uciInstalledAcceptanceClientA]
	initialB, initialBOK := result.ClientContexts[uciInstalledAcceptanceClientB]
	writeA, deleteA := result.Watcher.AfterWriteA, result.Watcher.AfterDeleteA
	writeB, deleteB := result.Watcher.AfterWriteB, result.Watcher.AfterDeleteB
	if !initialAOK || !initialBOK ||
		writeA.SourceDigest != initialA.SourceDigest || writeA.CheckoutDigest != initialA.CheckoutDigest || writeA.ViewDigest == initialA.ViewDigest ||
		deleteA.SourceDigest != initialA.SourceDigest || deleteA.CheckoutDigest != initialA.CheckoutDigest || deleteA.ViewDigest == writeA.ViewDigest ||
		writeA.Generation < 2 || deleteA.Generation <= writeA.Generation ||
		writeA.FreshnessState != "observed_current" || writeA.BarrierState != "satisfied" ||
		deleteA.FreshnessState != "observed_current" || deleteA.BarrierState != "satisfied" {
		t.Fatal("installed watcher did not publish isolated write and delete Views for client A")
	}
	if writeB.SourceDigest != initialB.SourceDigest || writeB.CheckoutDigest != initialB.CheckoutDigest || writeB.ViewDigest != initialB.ViewDigest ||
		deleteB != writeB || writeB.Generation < 1 || writeB.FreshnessState != "observed_current" {
		t.Fatal("installed watcher changed client B while client A saved and deleted a file")
	}
}

func uciInstalledAcceptanceRequireRestartContinuity(t *testing.T, request uciInstalledAcceptanceRequest, result uciInstalledAcceptanceResult) {
	t.Helper()

	before := result.Restart.BeforeProcesses
	after := result.Restart.AfterProcesses
	if before.ServerPID <= 0 || before.DaemonPID <= 0 || before.ParserPID <= 0 ||
		after.ServerPID <= 0 || after.DaemonPID <= 0 || after.ParserPID <= 0 ||
		before.ServerPID == after.ServerPID || before.DaemonPID == after.DaemonPID || before.ParserPID == after.ParserPID ||
		result.Restart.UnchangedInputReembedded {
		t.Fatal("installed restart did not replace child processes while preserving unchanged input")
	}

	for _, client := range []string{
		uciInstalledAcceptanceClientA,
		uciInstalledAcceptanceClientB,
		uciInstalledAcceptanceClientC,
	} {
		transcript, ok := result.Restart.ClientTranscripts[client]
		if !ok || !transcript.UsedStdio {
			t.Fatal("standard MCP client did not reconnect through the restarted installed daemon")
		}
		uciInstalledAcceptanceRequireMethodSubsequence(t, transcript.Methods,
			"initialize",
			"notifications/initialized",
			"tools/list",
			"tools/call",
		)
	}

	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB} {
		beforeContext, beforeOK := result.Restart.BeforeClientContexts[client]
		afterContext, afterOK := result.Restart.ClientContexts[client]
		if !beforeOK || !afterOK || beforeContext != afterContext {
			t.Fatal("standard MCP reconnect did not recover the correct selected context")
		}
		beforeObservation, beforeObservationOK := result.Restart.BeforeObservations[client]
		afterObservation, afterObservationOK := result.Restart.Observations[client]
		if !beforeObservationOK || !afterObservationOK || !uciInstalledAcceptanceSameObservations(beforeObservation, afterObservation) {
			t.Fatal("standard MCP reconnect changed source observations")
		}
	}
	if result.Restart.ClientContexts[uciInstalledAcceptanceClientC] != result.Restart.BeforeClientContexts[uciInstalledAcceptanceClientA] {
		t.Fatal("restarted third client did not recover its requested primary checkout")
	}
	if result.Restart.BeforeProjectionCounts != result.Restart.AfterProjectionCounts {
		t.Fatal("installed restart changed unchanged-input projection counts")
	}

	uciInstalledAcceptanceRequireOnlyViewDigests(
		t,
		result.Restart.Observations[uciInstalledAcceptanceClientA].SearchArtifactDigests,
		uciInstalledAcceptanceDigest(request.Fixture.PrimarySource),
	)
	uciInstalledAcceptanceRequireOnlyViewDigests(
		t,
		result.Restart.Observations[uciInstalledAcceptanceClientB].SearchArtifactDigests,
		uciInstalledAcceptanceDigest(request.Fixture.LinkedSource),
	)
}

func uciInstalledAcceptanceRequireClosedOutcome(
	t *testing.T,
	status string,
	errorCode string,
	contextDigest string,
	contentDigests []string,
	graphDigests []string,
	exposureDigest string,
	wantStatus string,
	wantErrorCode string,
) {
	t.Helper()

	if status != wantStatus || errorCode != wantErrorCode ||
		contextDigest != "" || len(contentDigests) != 0 || len(graphDigests) != 0 || exposureDigest != "" {
		t.Fatal("closed UCI outcome disclosed context, content, graph, or exposure data")
	}
}

func uciInstalledAcceptanceDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func uciInstalledAcceptanceIsSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func uciInstalledAcceptanceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestUCIInstalledReceiptContract(t *testing.T) {
	t.Run("complete synthetic result is deterministic and redacted", func(t *testing.T) {
		result := uciCompleteInstalledReceiptResult()
		first, err := BuildUCIInstalledReceipt(result)
		if err != nil {
			t.Fatalf("build first receipt: %v", err)
		}
		second, err := BuildUCIInstalledReceipt(result)
		if err != nil {
			t.Fatalf("build second receipt: %v", err)
		}
		firstJSON, err := EncodeUCIInstalledReceipt(first)
		if err != nil {
			t.Fatalf("encode first receipt: %v", err)
		}
		secondJSON, err := EncodeUCIInstalledReceipt(second)
		if err != nil {
			t.Fatalf("encode second receipt: %v", err)
		}
		if string(firstJSON) != string(secondJSON) || !json.Valid(firstJSON) {
			t.Fatal("receipt bytes are not deterministic valid JSON")
		}

		reordered := uciCompleteInstalledReceiptResult()
		tools := reordered.ClientTranscripts[uciInstalledAcceptanceClientA].Tools
		for left, right := 0, len(tools)-1; left < right; left, right = left+1, right-1 {
			tools[left], tools[right] = tools[right], tools[left]
		}
		transcript := reordered.ClientTranscripts[uciInstalledAcceptanceClientA]
		transcript.Tools = tools
		reordered.ClientTranscripts[uciInstalledAcceptanceClientA] = transcript
		reorderedReceipt, err := BuildUCIInstalledReceipt(reordered)
		if err != nil {
			t.Fatalf("build receipt with reordered tool surface: %v", err)
		}
		reorderedJSON, err := EncodeUCIInstalledReceipt(reorderedReceipt)
		if err != nil {
			t.Fatalf("encode receipt with reordered tool surface: %v", err)
		}
		if string(firstJSON) != string(reorderedJSON) {
			t.Fatal("receipt retained unstable tool-list ordering")
		}

		for _, raw := range []string{
			"postgres://fixture:secret@127.0.0.1/private_test",
			`D:\private\uci-installed-root`,
			"fixture source body must never be retained",
			"fixture-token-must-never-be-retained",
		} {
			if strings.Contains(string(firstJSON), raw) {
				t.Fatalf("receipt retained raw private input %q", raw)
			}
		}
		if first.SchemaVersion != UCIInstalledReceiptSchemaVersion || first.Completion != uciInstalledReceiptCompletionVerified || first.Scope.Production != uciInstalledReceiptNotClaimed || first.Fixture.PrimarySourceDigest == "fixture source body must never be retained" {
			t.Fatalf("receipt schema or redaction boundary = %#v", first)
		}
	})

	t.Run("rejects incomplete unsafe and unstable result evidence", func(t *testing.T) {
		tests := []struct {
			name   string
			mutate func(*uciInstalledAcceptanceResult)
		}{
			{
				name: "missing candidate digest",
				mutate: func(result *uciInstalledAcceptanceResult) {
					artifact := result.Artifacts[uciInstalledAcceptanceArtifactParser]
					artifact.CandidateSHA256 = ""
					result.Artifacts[uciInstalledAcceptanceArtifactParser] = artifact
				},
			},
			{
				name: "missing fixture manifest digest",
				mutate: func(result *uciInstalledAcceptanceResult) {
					result.Fixture.ManifestDigest = ""
				},
			},
			{
				name: "false completion before cleanup",
				mutate: func(result *uciInstalledAcceptanceResult) {
					result.Cleanup.ProcessTreeClosed = false
				},
			},
			{
				name: "raw DSN in transcript",
				mutate: func(result *uciInstalledAcceptanceResult) {
					transcript := result.ClientTranscripts[uciInstalledAcceptanceClientA]
					transcript.Tools = append(transcript.Tools, "postgres://fixture:secret@127.0.0.1/private_test")
					result.ClientTranscripts[uciInstalledAcceptanceClientA] = transcript
				},
			},
			{
				name: "absolute private locator in transcript",
				mutate: func(result *uciInstalledAcceptanceResult) {
					transcript := result.ClientTranscripts[uciInstalledAcceptanceClientA]
					transcript.Tools = append(transcript.Tools, `D:\private\uci-installed-root`)
					result.ClientTranscripts[uciInstalledAcceptanceClientA] = transcript
				},
			},
			{
				name: "source body substituted for digest",
				mutate: func(result *uciInstalledAcceptanceResult) {
					result.Fixture.PrimarySourceDigest = "fixture source body must never be retained"
				},
			},
			{
				name: "unstable map key set",
				mutate: func(result *uciInstalledAcceptanceResult) {
					result.Artifacts["unexpected"] = result.Artifacts[uciInstalledAcceptanceArtifactServer]
				},
			},
			{
				name: "missing refusal outcome",
				mutate: func(result *uciInstalledAcceptanceResult) {
					delete(result.Refusals, "private")
				},
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				result := uciCompleteInstalledReceiptResult()
				test.mutate(&result)
				if _, err := BuildUCIInstalledReceipt(result); err == nil {
					t.Fatal("receipt assembly accepted incomplete or unsafe evidence")
				}
			})
		}
	})

	t.Run("rejects receipt tampering", func(t *testing.T) {
		receipt, err := BuildUCIInstalledReceipt(uciCompleteInstalledReceiptResult())
		if err != nil {
			t.Fatalf("build receipt: %v", err)
		}
		tests := []struct {
			name   string
			mutate func(*UCIInstalledReceipt)
		}{
			{
				name: "false completion",
				mutate: func(receipt *UCIInstalledReceipt) {
					receipt.Completion = "complete"
				},
			},
			{
				name: "candidate artifact mismatch",
				mutate: func(receipt *UCIInstalledReceipt) {
					receipt.Candidate.Artifacts[0].InstalledSHA256 = uciInstalledAcceptanceDigest("tampered installed artifact")
				},
			},
			{
				name: "unsupported production claim",
				mutate: func(receipt *UCIInstalledReceipt) {
					receipt.Scope.Production = "proven"
				},
			},
			{
				name: "watcher isolation mismatch",
				mutate: func(receipt *UCIInstalledReceipt) {
					receipt.Watcher.AfterDeleteB.ViewDigest = uciInstalledAcceptanceDigest("tampered linked view")
				},
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				mutated := receipt
				mutated.Candidate.Artifacts = append([]UCIInstalledReceiptArtifactDigest(nil), receipt.Candidate.Artifacts...)
				test.mutate(&mutated)
				if err := ValidateUCIInstalledReceipt(mutated); err == nil {
					t.Fatal("receipt validator accepted tampering")
				}
			})
		}
	})
}

func uciCompleteInstalledReceiptResult() uciInstalledAcceptanceResult {
	fixture := uciInstalledAcceptanceFixture{
		RelativePath:  `private/fixture-path-must-never-be-retained.go`,
		SharedSymbol:  "fixture-token-must-never-be-retained",
		PrimarySource: "fixture source body must never be retained",
		LinkedSource:  "different fixture source body must never be retained",
		PrimaryCallee: "PrimaryFixtureCallee",
		LinkedCallee:  "LinkedFixtureCallee",
	}
	result := uciNewInstalledAcceptanceResult(uciInstalledAcceptanceRequest{
		Version:               uciInstalledAcceptanceVersionV1,
		InstallHarnessVersion: uciInstallHarnessVersionV1,
		LoopbackHost:          "127.0.0.1",
		Fixture:               fixture,
	})
	result.Artifacts = map[string]uciInstalledAcceptanceArtifact{
		uciInstalledAcceptanceArtifactServer: {CandidateSHA256: uciInstalledAcceptanceDigest("candidate-server"), InstalledSHA256: uciInstalledAcceptanceDigest("candidate-server")},
		uciInstalledAcceptanceArtifactDaemon: {CandidateSHA256: uciInstalledAcceptanceDigest("candidate-daemon"), InstalledSHA256: uciInstalledAcceptanceDigest("candidate-daemon")},
		uciInstalledAcceptanceArtifactParser: {CandidateSHA256: uciInstalledAcceptanceDigest("candidate-parser"), InstalledSHA256: uciInstalledAcceptanceDigest("candidate-parser")},
	}
	result.Processes = uciInstalledAcceptanceProcesses{ServerPID: 101, DaemonPID: 102, ParserPID: 103}
	result.Parser = uciInstalledAcceptanceParserEvidence{UsedInstalledArtifact: true, InvocationCount: 1, RequestDigest: uciInstalledAcceptanceDigest("installed-parser-request")}
	result.ReservedLoopbackPorts = []int{33001, 33002}
	result.SimultaneousAB = true

	transcript := uciInstalledReceiptTestTranscript()
	result.ClientTranscripts = map[string]uciInstalledAcceptanceClientTranscript{
		uciInstalledAcceptanceClientA: transcript,
		uciInstalledAcceptanceClientB: transcript,
		uciInstalledAcceptanceClientC: transcript,
	}
	result.Bootstrap = uciInstalledAcceptanceBootstrap{
		InitialViewAbsent: map[string]bool{uciInstalledAcceptanceClientA: true, uciInstalledAcceptanceClientB: true},
		IndexStarted:      map[string]bool{uciInstalledAcceptanceClientA: true, uciInstalledAcceptanceClientB: true},
		StatusStates:      map[string]string{uciInstalledAcceptanceClientA: "observed_current", uciInstalledAcceptanceClientB: "observed_current"},
		BarrierStates:     map[string]string{uciInstalledAcceptanceClientA: "satisfied", uciInstalledAcceptanceClientB: "satisfied"},
		UnboundClient:     uciInstalledAcceptanceClosedOutcome{Status: "context_required", ErrorCode: "CONTEXT_REQUIRED"},
	}
	result.Worktrees = uciInstalledAcceptanceWorktrees{
		Registered:             map[string]bool{uciInstalledAcceptanceClientA: true, uciInstalledAcceptanceClientB: true},
		UsedGitArgumentVectors: true,
		LinkedGitFile:          true,
		SameHead:               true,
		PrimaryDirty:           true,
		LinkedDirty:            true,
		SharedHeadDigest:       uciInstalledAcceptanceDigest("shared-head"),
		RelativePathDigest:     result.Fixture.RelativePathDigest,
	}
	primary := uciInstalledAcceptanceContext{
		SourceDigest:   uciInstalledAcceptanceDigest("source"),
		CheckoutDigest: uciInstalledAcceptanceDigest("checkout-primary"),
		ViewDigest:     uciInstalledAcceptanceDigest("view-primary"),
	}
	linked := uciInstalledAcceptanceContext{
		SourceDigest:   primary.SourceDigest,
		CheckoutDigest: uciInstalledAcceptanceDigest("checkout-linked"),
		ViewDigest:     uciInstalledAcceptanceDigest("view-linked"),
	}
	result.ClientContexts = map[string]uciInstalledAcceptanceContext{
		uciInstalledAcceptanceClientA: primary,
		uciInstalledAcceptanceClientB: linked,
	}
	primaryObservation := uciInstalledAcceptanceObservations{
		SearchArtifactDigests: []string{result.Fixture.PrimarySourceDigest},
		GraphCalleeDigests:    []string{result.Fixture.PrimaryCalleeDigest},
		ReadArtifactDigests:   []string{result.Fixture.PrimarySourceDigest},
	}
	linkedObservation := uciInstalledAcceptanceObservations{
		SearchArtifactDigests: []string{result.Fixture.LinkedSourceDigest},
		GraphCalleeDigests:    []string{result.Fixture.LinkedCalleeDigest},
		ReadArtifactDigests:   []string{result.Fixture.LinkedSourceDigest},
	}
	result.Observations = map[string]uciInstalledAcceptanceObservations{
		uciInstalledAcceptanceClientA: primaryObservation,
		uciInstalledAcceptanceClientB: linkedObservation,
	}
	result.Defaults = uciInstalledAcceptanceDefaults{
		BeforeThirdClientViewDigests: map[string]string{uciInstalledAcceptanceClientA: primary.ViewDigest, uciInstalledAcceptanceClientB: linked.ViewDigest},
		AfterThirdClientViewDigests:  map[string]string{uciInstalledAcceptanceClientA: primary.ViewDigest, uciInstalledAcceptanceClientB: linked.ViewDigest},
	}
	result.Refusals = map[string]uciInstalledAcceptanceClosedOutcome{
		"denied":     {Status: "forbidden", ErrorCode: "PERMISSION_DENIED"},
		"revoked":    {Status: "forbidden", ErrorCode: "PERMISSION_DENIED"},
		"mismatched": {Status: "context_required", ErrorCode: "CONTEXT_MISMATCH"},
		"private":    {Status: "context_required", ErrorCode: "CONTEXT_MISMATCH"},
	}
	result.Recorder = uciInstalledAcceptanceRecorder{
		InitialUnavailable:            uciInstalledAcceptanceClosedOutcome{Status: "unavailable", ErrorCode: "EXPOSURE_UNAVAILABLE"},
		HealthAfterInitialUnavailable: "unavailable",
		FirstExposureDigest:           uciInstalledAcceptanceDigest("exposure"),
		ExactRetryExposureDigest:      uciInstalledAcceptanceDigest("exposure"),
		HealthBeforeMismatch:          "healthy",
		HealthAfterMismatch:           "healthy",
		Mismatch:                      uciInstalledAcceptanceClosedOutcome{Status: "unavailable", ErrorCode: "IDEMPOTENCY_MISMATCH"},
	}
	writeA := uciInstalledReceiptTestPublication(primary, "watcher-write-a", 2)
	deleteA := uciInstalledReceiptTestPublication(primary, "watcher-delete-a", 3)
	writeB := uciInstalledReceiptTestPublication(linked, "watcher-steady-b", 1)
	writeB.ViewDigest = linked.ViewDigest
	writeB.BarrierState = ""
	result.Watcher = uciInstalledAcceptanceWatcher{
		AfterWriteA:  writeA,
		AfterDeleteA: deleteA,
		AfterWriteB:  writeB,
		AfterDeleteB: writeB,
	}
	restartPrimary := primary
	restartPrimary.ViewDigest = deleteA.ViewDigest
	restartLinked := linked
	restartLinked.ViewDigest = writeB.ViewDigest
	result.Restart = uciInstalledAcceptanceRestart{
		BeforeProcesses:        result.Processes,
		AfterProcesses:         uciInstalledAcceptanceProcesses{ServerPID: 201, DaemonPID: 202, ParserPID: 203},
		BeforeProjectionCounts: uciInstalledAcceptanceProjectionCounts{Embeddings: 3, ChunkEmbeddings: 4, ResolvedEdges: 5},
		AfterProjectionCounts:  uciInstalledAcceptanceProjectionCounts{Embeddings: 3, ChunkEmbeddings: 4, ResolvedEdges: 5},
		ClientTranscripts: map[string]uciInstalledAcceptanceClientTranscript{
			uciInstalledAcceptanceClientA: transcript,
			uciInstalledAcceptanceClientB: transcript,
			uciInstalledAcceptanceClientC: transcript,
		},
		BeforeClientContexts: map[string]uciInstalledAcceptanceContext{
			uciInstalledAcceptanceClientA: restartPrimary,
			uciInstalledAcceptanceClientB: restartLinked,
		},
		ClientContexts: map[string]uciInstalledAcceptanceContext{
			uciInstalledAcceptanceClientA: restartPrimary,
			uciInstalledAcceptanceClientB: restartLinked,
			uciInstalledAcceptanceClientC: restartPrimary,
		},
		BeforeObservations: map[string]uciInstalledAcceptanceObservations{
			uciInstalledAcceptanceClientA: primaryObservation,
			uciInstalledAcceptanceClientB: linkedObservation,
		},
		Observations: map[string]uciInstalledAcceptanceObservations{
			uciInstalledAcceptanceClientA: primaryObservation,
			uciInstalledAcceptanceClientB: linkedObservation,
		},
	}
	result.Cleanup = uciInstalledAcceptanceCleanup{
		ProcessTreeClosed:     true,
		InstallRootRemoved:    true,
		FixtureRootRemoved:    true,
		LocalStateRootRemoved: true,
	}
	return result
}

func uciInstalledReceiptTestTranscript() uciInstalledAcceptanceClientTranscript {
	return uciInstalledAcceptanceClientTranscript{
		UsedStdio: true,
		Methods:   []string{"initialize", "notifications/initialized", "tools/list", "tools/call"},
		Tools:     []string{"codebase_read", "codebase_graph", "codebase_search", "codebase_status", "codebase_index", "codebase_context"},
	}
}

func uciInstalledReceiptTestPublication(context uciInstalledAcceptanceContext, viewSeed string, generation int64) uciInstalledAcceptancePublicationEvidence {
	return uciInstalledAcceptancePublicationEvidence{
		SourceDigest:   context.SourceDigest,
		CheckoutDigest: context.CheckoutDigest,
		ViewDigest:     uciInstalledAcceptanceDigest(viewSeed),
		RunDigest:      uciInstalledAcceptanceDigest("run-" + viewSeed),
		Generation:     generation,
		FreshnessState: "observed_current",
		BarrierState:   "satisfied",
	}
}
