package main

import (
	"bufio"
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lib/pq"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	muxserverid "github.com/thebtf/mcp-mux/muxcore/serverid"
	"golang.org/x/crypto/bcrypt"
)

const (
	uciInstalledAcceptanceVersionV1                = "uci-installed-acceptance/v1"
	uciInstalledAcceptanceParserExecutableEnv      = "ENGRAM_UCI_PARSER_EXECUTABLE"
	uciInstalledAcceptanceClientA                  = "client-a"
	uciInstalledAcceptanceClientB                  = "client-b"
	uciInstalledAcceptanceClientC                  = "client-c"
	uciInstalledAcceptanceClientRecorder           = "client-recorder"
	uciInstalledAcceptanceParserCanaryRelativePath = "parser-canary.ts"
	uciInstalledAcceptanceParserCanarySource       = "export function InstalledParserCanary(): string {\n\treturn \"UCI_INSTALLED_PARSER_CANARY\"\n}\n"
	uciInstalledAcceptanceSchemaPrefix             = "uci_installed_"
	uciInstalledAcceptanceTokenDefaultTTL          = 30 * time.Minute
	uciInstalledAcceptanceTokenGrace               = 5 * time.Minute
	uciInstalledAcceptanceScenarioCodeObserved     = "OBSERVED_INSTALLED_LIFECYCLE"
)

const (
	uciInstalledAcceptanceGitRevParse          = "rev-parse"
	uciInstalledAcceptanceSHA256Prefix         = "sha256:"
	uciInstalledAcceptanceToolsCallMethod      = "tools/call"
	uciInstalledAcceptanceViewDeltaUnavailable = "view_delta=unavailable"
)

var (
	errUCIInstalledAcceptanceNonTestPostgres      = errors.New("UCI installed acceptance requires an explicit loopback test PostgreSQL database")
	errUCIInstalledAcceptanceParserBoundary       = errors.New("UCI installed acceptance parser boundary is not observable through the installed runtime")
	errUCIInstalledAcceptanceBarrierBoundary      = errors.New("UCI installed acceptance read-your-save barrier is not observable through the installed runtime")
	errUCIInstalledAcceptanceWatcherCanaryMissing = errors.New("installed standard MCP watcher search omitted the canary")
	errUCIInstalledAcceptanceWatcherCanaryPresent = errors.New("installed standard MCP watcher delete still exposes the canary")
)

// uciInstalledAcceptanceScenarioProbePhase chooses whether an optional probe
// follows the complete base lifecycle or consumes the initial installed A/B
// publications before the base watcher and restart matrices.
type uciInstalledAcceptanceScenarioProbePhase string

const uciInstalledAcceptanceScenarioProbeBeforeBaseWatcher uciInstalledAcceptanceScenarioProbePhase = "before_base_watcher"

// uciInstalledAcceptanceRequest contains only disposable candidate source,
// filesystem roots, and a caller-supplied loopback PostgreSQL test target.
type uciInstalledAcceptanceRequest struct {
	Version                   string
	InstallHarnessVersion     string
	CandidateSourceRoot       string
	InstallRoot               string
	FixtureRoot               string
	LocalStateRoot            string
	TestPostgresDSN           string
	LoopbackHost              string
	ReservedLoopbackPortCount int
	ReadinessTimeout          time.Duration
	OperationTimeout          time.Duration
	EmbeddingProvider         *uciInstalledAcceptanceEmbeddingProvider
	Fixture                   uciInstalledAcceptanceFixture
	ScenarioProbe             uciInstalledAcceptanceScenarioProbe
	ScenarioProbePhase        uciInstalledAcceptanceScenarioProbePhase
}

// uciInstalledAcceptanceEmbeddingProvider is an explicit caller-provided
// server-only provider configuration for an opt-in installed recorder. The
// resulting receipt retains only the endpoint digest and model, never Key.
type uciInstalledAcceptanceEmbeddingProvider struct {
	URL   string
	Model string
	Key   string
}

// uciInstalledAcceptanceScenarioProbe runs at the request-selected lifecycle
// phase. It must restore every live-state mutation before returning and must
// not retain its runtime after it returns.
type uciInstalledAcceptanceScenarioProbe func(context.Context, uciInstalledAcceptanceScenarioRuntime) (map[string]uciInstalledAcceptanceScenarioEvidence, error)

// uciInstalledAcceptanceScenarioRuntime exposes only the already-live
// installed lifecycle resources needed by same-package scenario probes.
type uciInstalledAcceptanceScenarioRuntime struct {
	Request            uciInstalledAcceptanceRequest
	Installation       *uciInstallHarnessInstallation
	Authority          *uciInstalledAcceptanceAuthority
	Worktrees          uciInstalledAcceptanceWorktreesFixture
	ClientA            *uciInstalledAcceptanceMCPClient
	ClientB            *uciInstalledAcceptanceMCPClient
	ClientC            *uciInstalledAcceptanceMCPClient
	Recorder           *uciInstalledAcceptanceMCPClient
	Selections         map[string]uciInstalledAcceptanceSelection
	Publications       map[string]uciInstalledAcceptancePublication
	ParserBundleDigest string
	Candidates         map[string]uciInstallHarnessCommand
	ServerEnvironment  []string
	ClientEnvironment  []string
}

type uciInstalledAcceptanceRefusalInput struct {
	first, second, third            *uciInstalledAcceptanceMCPClient
	firstSelection, secondSelection uciInstalledAcceptanceSelection
	authority                       *uciInstalledAcceptanceAuthority
	result                          *uciInstalledAcceptanceResult
}

type uciInstalledAcceptanceRestartInput struct {
	request            uciInstalledAcceptanceRequest
	candidates         map[string]uciInstallHarnessCommand
	authority          *uciInstalledAcceptanceAuthority
	worktrees          uciInstalledAcceptanceWorktreesFixture
	serverPort         int
	parserBundleDigest string
	daemonControlRoot  string
	oldInstallation    *uciInstallHarnessInstallation
	daemonOwner        *int
	oldClients         map[string]*uciInstalledAcceptanceMCPClient
	selections         map[string]uciInstalledAcceptanceSelection
	publications       map[string]uciInstalledAcceptancePublication
	result             *uciInstalledAcceptanceResult
}
type uciInstalledAcceptanceRestartState struct {
	input             uciInstalledAcceptanceRestartInput
	oldDaemonPID      int
	next              *uciInstallHarnessInstallation
	installedPaths    map[string]string
	serverEnvironment []string
	clientEnvironment []string
	clients           map[string]*uciInstalledAcceptanceMCPClient
	selections        map[string]uciInstalledAcceptanceSelection
	publications      map[string]uciInstalledAcceptancePublication
	serverPID         int
	daemonPID         int
}

type uciInstalledAcceptanceWatcherCanaryInput struct {
	client       *uciInstalledAcceptanceMCPClient
	selection    uciInstalledAcceptanceSelection
	publication  uciInstalledAcceptancePublication
	functionName string
	relativePath string
	wantPresent  bool
}

type uciInstalledAcceptanceWatcherInput struct {
	fixture                         uciInstalledAcceptanceFixture
	authority                       *uciInstalledAcceptanceAuthority
	worktrees                       uciInstalledAcceptanceWorktreesFixture
	first, second                   *uciInstalledAcceptanceMCPClient
	firstSelection, secondSelection uciInstalledAcceptanceSelection
	before                          map[string]uciInstalledAcceptancePublication
	result                          *uciInstalledAcceptanceResult
}

type uciInstalledAcceptanceWatcherSnapshotInput struct {
	authority      *uciInstalledAcceptanceAuthority
	client         *uciInstalledAcceptanceMCPClient
	selection      uciInstalledAcceptanceSelection
	publication    uciInstalledAcceptancePublication
	root           string
	fixture        uciInstalledAcceptanceFixture
	expectedCallee string
}

// uciInstalledAcceptanceScenarioEvidence retains only the safe receipt fields
// a live installed scenario may contribute.
type uciInstalledAcceptanceScenarioEvidence struct {
	Code   string
	Digest string
}

type uciInstalledAcceptanceFixture struct {
	RelativePath  string
	SharedSymbol  string
	PrimarySource string
	LinkedSource  string
	PrimaryCallee string
	LinkedCallee  string
}

type uciInstalledAcceptanceResult struct {
	AcceptanceVersion     string
	InstallHarnessVersion string
	Fixture               uciInstalledAcceptanceFixtureEvidence
	Artifacts             map[string]uciInstalledAcceptanceArtifact
	Processes             uciInstalledAcceptanceProcesses
	RawServiceCallCount   int
	Parser                uciInstalledAcceptanceParserEvidence
	LoopbackHost          string
	ReservedLoopbackPorts []int
	ClientTranscripts     map[string]uciInstalledAcceptanceClientTranscript
	SimultaneousAB        bool
	Bootstrap             uciInstalledAcceptanceBootstrap
	Worktrees             uciInstalledAcceptanceWorktrees
	ClientContexts        map[string]uciInstalledAcceptanceContext
	Observations          map[string]uciInstalledAcceptanceObservations
	Defaults              uciInstalledAcceptanceDefaults
	Refusals              map[string]uciInstalledAcceptanceClosedOutcome
	Recorder              uciInstalledAcceptanceRecorder
	ScenarioEvidence      map[string]uciInstalledAcceptanceScenarioEvidence
	Watcher               uciInstalledAcceptanceWatcher
	Restart               uciInstalledAcceptanceRestart
	Cleanup               uciInstalledAcceptanceCleanup
}

type uciInstalledAcceptanceArtifact struct {
	CandidateSHA256 string
	InstalledSHA256 string
}

type uciInstalledAcceptanceProcesses struct {
	ServerPID int
	DaemonPID int
	ParserPID int
}

type uciInstalledAcceptanceParserEvidence struct {
	UsedInstalledArtifact bool
	InvocationCount       int
	RequestDigest         string
}

type uciInstalledAcceptanceClientTranscript struct {
	UsedStdio bool
	Methods   []string
	Tools     []string
}

type uciInstalledAcceptanceBootstrap struct {
	InitialViewAbsent map[string]bool
	IndexStarted      map[string]bool
	StatusStates      map[string]string
	BarrierStates     map[string]string
	UnboundClient     uciInstalledAcceptanceClosedOutcome
}

type uciInstalledAcceptanceWorktrees struct {
	Registered             map[string]bool
	UsedGitArgumentVectors bool
	LinkedGitFile          bool
	SameHead               bool
	PrimaryDirty           bool
	LinkedDirty            bool
	SharedHeadDigest       string
	RelativePathDigest     string
}

type uciInstalledAcceptanceContext struct {
	SourceDigest   string
	CheckoutDigest string
	ViewDigest     string
}

type uciInstalledAcceptanceObservations struct {
	SearchArtifactDigests []string
	GraphCalleeDigests    []string
	ReadArtifactDigests   []string
}
type uciInstalledAcceptanceProjectionCounts struct {
	Embeddings      int64
	ChunkEmbeddings int64
	ResolvedEdges   int64
}
type uciInstalledAcceptancePublicationEvidence struct {
	SourceDigest   string
	CheckoutDigest string
	ViewDigest     string
	RunDigest      string
	Generation     int64
	FreshnessState string
	BarrierState   string
}

type uciInstalledAcceptanceWatcherSnapshot struct {
	Publication      uciInstalledAcceptancePublicationEvidence
	BytesDigest      string
	HeadDigest       string
	TreeDigest       string
	MembershipDigest string
	EdgesDigest      string
	SearchDigest     string
	GraphDigest      string
	ReadDigest       string
}

type uciInstalledAcceptanceWatcher struct {
	AfterWriteA  uciInstalledAcceptancePublicationEvidence
	AfterDeleteA uciInstalledAcceptancePublicationEvidence
	AfterWriteB  uciInstalledAcceptancePublicationEvidence
	AfterDeleteB uciInstalledAcceptancePublicationEvidence
	InitialA     uciInstalledAcceptanceWatcherSnapshot
	UpdatedA     uciInstalledAcceptanceWatcherSnapshot
	RestoredA    uciInstalledAcceptanceWatcherSnapshot
	InitialB     uciInstalledAcceptanceWatcherSnapshot
	UpdatedB     uciInstalledAcceptanceWatcherSnapshot
	RestoredB    uciInstalledAcceptanceWatcherSnapshot
}

type uciInstalledAcceptanceDefaults struct {
	BeforeThirdClientViewDigests map[string]string
	AfterThirdClientViewDigests  map[string]string
}

type uciInstalledAcceptanceClosedOutcome struct {
	Status         string
	ErrorCode      string
	ContextDigest  string
	ContentDigests []string
	GraphDigests   []string
	ExposureDigest string
}

type uciInstalledAcceptanceRecorder struct {
	InitialUnavailable            uciInstalledAcceptanceClosedOutcome
	HealthAfterInitialUnavailable string
	FirstExposureDigest           string
	ExactRetryExposureDigest      string
	HealthBeforeMismatch          string
	HealthAfterMismatch           string
	Mismatch                      uciInstalledAcceptanceClosedOutcome
}

type uciInstalledAcceptanceRestart struct {
	BeforeProcesses          uciInstalledAcceptanceProcesses
	AfterProcesses           uciInstalledAcceptanceProcesses
	BeforeProjectionCounts   uciInstalledAcceptanceProjectionCounts
	AfterProjectionCounts    uciInstalledAcceptanceProjectionCounts
	UnchangedInputReembedded bool
	ClientTranscripts        map[string]uciInstalledAcceptanceClientTranscript
	BeforeClientContexts     map[string]uciInstalledAcceptanceContext
	ClientContexts           map[string]uciInstalledAcceptanceContext
	BeforeObservations       map[string]uciInstalledAcceptanceObservations
	Observations             map[string]uciInstalledAcceptanceObservations
}

type uciInstalledAcceptanceCleanup struct {
	ProcessTreeClosed     bool
	InstallRootRemoved    bool
	FixtureRootRemoved    bool
	LocalStateRootRemoved bool
}

type uciInstalledAcceptanceWorktreesFixture struct {
	primaryRoot    string
	linkedRoot     string
	auxiliaryRoots []string
	head           string
}

type uciInstalledAcceptanceAuthority struct {
	store            *gormdb.Store
	adminDB          *sql.DB
	schema           string
	scopedDSN        string
	token            *gormdb.APIToken
	rawToken         string
	principal        string
	clientInstanceID string
	source           *gormdb.UCISource
	profile          *gormdb.UCIAnalysisProfile
	checkouts        map[string]*gormdb.UCICheckout
	anchorProjectID  string
	projectKey       string
}

type uciInstalledAcceptanceReservation struct {
	listener net.Listener
	port     int
}

// runUCIInstalledAcceptance deliberately stops at the first public installed
// behavior that cannot satisfy the contract. It never invents an index,
// parser, query, authorization, recorder, or restart result.
type uciInstalledAcceptanceRuntime struct {
	request            uciInstalledAcceptanceRequest
	result             *uciInstalledAcceptanceResult
	installation       *uciInstallHarnessInstallation
	authority          *uciInstalledAcceptanceAuthority
	reservations       []*uciInstalledAcceptanceReservation
	activeDaemonPID    int
	daemonControlRoot  string
	candidates         map[string]uciInstallHarnessCommand
	parserBundleDigest string
	worktrees          uciInstalledAcceptanceWorktreesFixture
	parserPath         string
	daemonPath         string
	serverPort         int
	serverEnvironment  []string
	clientEnvironment  []string
	clientA            *uciInstalledAcceptanceMCPClient
	clientB            *uciInstalledAcceptanceMCPClient
	clientC            *uciInstalledAcceptanceMCPClient
	selectedA          uciInstalledAcceptanceSelection
	selectedB          uciInstalledAcceptanceSelection
	publications       map[string]uciInstalledAcceptancePublication
}

func runUCIInstalledAcceptance(ctx context.Context, request uciInstalledAcceptanceRequest) (result uciInstalledAcceptanceResult, err error) {
	result = uciNewInstalledAcceptanceResult(request)
	if err := uciValidateInstalledAcceptanceRequest(ctx, request); err != nil {
		return result, err
	}
	operationCtx, cancelOperation := context.WithTimeout(ctx, request.OperationTimeout)
	defer cancelOperation()
	runtime := &uciInstalledAcceptanceRuntime{request: request, result: &result, daemonControlRoot: filepath.Join(request.LocalStateRoot, "temp")}
	defer runtime.cleanup(&err)
	if err := runtime.setupRootsAndAuthority(operationCtx); err != nil {
		return result, err
	}
	if err := runtime.materialize(operationCtx); err != nil {
		return result, err
	}
	if err := runtime.startServer(operationCtx); err != nil {
		return result, err
	}
	if err := runtime.startClientsAndBootstrap(operationCtx); err != nil {
		return result, err
	}
	defer runtime.captureClientTranscripts()
	if uciInstalledAcceptanceScenarioRunsBeforeBaseWatcher(request) {
		if err := runtime.runScenarioProbe(operationCtx); err != nil {
			return result, fmt.Errorf("installed UCI pre-base scenario probe: %w", err)
		}
		return result, nil
	}
	if err := runtime.exerciseBase(operationCtx); err != nil {
		return result, err
	}
	return result, nil
}

func (runtime *uciInstalledAcceptanceRuntime) cleanup(retErr *error) {
	runtime.stopDaemon(retErr)
	runtime.closeProcessTree(retErr)
	daemonExited := runtime.waitForDaemonExit(retErr)
	runtime.closeInstallation(daemonExited, retErr)
	runtime.closeReservations(retErr)
	runtime.closeAuthority(daemonExited, retErr)
	if runtime.installation == nil || daemonExited {
		runtime.result.Cleanup.InstallRootRemoved = uciInstalledAcceptanceRemoveRoot(retErr, runtime.request.InstallRoot)
		runtime.result.Cleanup.FixtureRootRemoved = uciInstalledAcceptanceRemoveRoot(retErr, runtime.request.FixtureRoot)
		runtime.result.Cleanup.LocalStateRootRemoved = uciInstalledAcceptanceRemoveRoot(retErr, runtime.request.LocalStateRoot)
	}
}

func (runtime *uciInstalledAcceptanceRuntime) stopDaemon(retErr *error) {
	if stopErr := uciStopInstalledAcceptanceDaemon(runtime.daemonControlRoot, runtime.activeDaemonPID); stopErr != nil {
		*retErr = errors.Join(*retErr, stopErr)
	}
}

func (runtime *uciInstalledAcceptanceRuntime) waitForDaemonExit(retErr *error) bool {
	if runtime.activeDaemonPID <= 0 {
		return true
	}
	if waitErr := uciWaitInstalledAcceptanceProcessExit(runtime.activeDaemonPID, 5*time.Second); waitErr != nil {
		*retErr = errors.Join(*retErr, waitErr)
		return false
	}
	return true
}

func (runtime *uciInstalledAcceptanceRuntime) closeProcessTree(retErr *error) {
	if runtime.installation == nil || runtime.installation.tree == nil {
		return
	}
	if closeErr := runtime.installation.tree.Close(); closeErr != nil {
		*retErr = errors.Join(*retErr, fmt.Errorf("close installed acceptance process tree: %w", closeErr))
	}
}

func (runtime *uciInstalledAcceptanceRuntime) closeInstallation(daemonExited bool, retErr *error) {
	if runtime.installation == nil || !daemonExited {
		return
	}
	if closeErr := runtime.installation.Close(); closeErr != nil {
		*retErr = errors.Join(*retErr, closeErr)
		return
	}
	runtime.result.Cleanup.ProcessTreeClosed = true
}

func (runtime *uciInstalledAcceptanceRuntime) closeReservations(retErr *error) {
	for _, reservation := range runtime.reservations {
		if reservation != nil && reservation.listener != nil {
			if closeErr := reservation.listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
				*retErr = errors.Join(*retErr, fmt.Errorf("close reserved UCI loopback port: %w", closeErr))
			}
		}
	}
}

func (runtime *uciInstalledAcceptanceRuntime) closeAuthority(daemonExited bool, retErr *error) {
	if runtime.authority == nil {
		return
	}
	if !daemonExited {
		*retErr = errors.Join(*retErr, errors.New("installed acceptance schema retained because daemon exit is unconfirmed"))
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	cleanupErr := runtime.authority.Close(cleanupCtx)
	cancel()
	if cleanupErr != nil {
		*retErr = errors.Join(*retErr, cleanupErr)
	}
}

func (runtime *uciInstalledAcceptanceRuntime) setupRootsAndAuthority(ctx context.Context) error {
	if err := os.Mkdir(runtime.request.FixtureRoot, 0o700); err != nil {
		return fmt.Errorf("create installed acceptance fixture root: %w", err)
	}
	if err := os.Mkdir(runtime.request.LocalStateRoot, 0o700); err != nil {
		return fmt.Errorf("create installed acceptance local state root: %w", err)
	}
	candidates, err := uciBuildInstalledAcceptanceCandidates(ctx, runtime.request.CandidateSourceRoot, filepath.Join(runtime.request.FixtureRoot, "candidate-build"))
	if err != nil {
		return err
	}
	runtime.candidates = candidates
	runtime.parserBundleDigest = uciInstalledAcceptanceParserBundleDigest()
	anchorProjectID := uuid.NewString()
	worktrees, worktreeResult, err := uciCreateInstalledAcceptanceWorktrees(ctx, runtime.request.FixtureRoot, runtime.request.Fixture, anchorProjectID)
	if err != nil {
		return err
	}
	runtime.worktrees = worktrees
	runtime.result.Worktrees = worktreeResult
	authority, err := uciPrepareInstalledAcceptanceAuthority(ctx, runtime.request.TestPostgresDSN, worktrees, runtime.parserBundleDigest, anchorProjectID)
	if err != nil {
		return err
	}
	runtime.authority = authority
	if err := uciAssertInstalledAcceptanceNoProjection(ctx, authority); err != nil {
		return err
	}
	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB} {
		runtime.result.Worktrees.Registered[client] = true
	}
	return nil
}

func (runtime *uciInstalledAcceptanceRuntime) materialize(ctx context.Context) error {
	installResult, err := runUCIInstallHarness(ctx, uciInstallHarnessRequest{
		Version:          runtime.request.InstallHarnessVersion,
		Scenario:         uciInstallHarnessScenarioMaterialize,
		InstallRoot:      runtime.request.InstallRoot,
		Server:           runtime.candidates["server"],
		Daemon:           runtime.candidates["daemon"],
		Parser:           runtime.candidates["parser"],
		ReadinessTimeout: runtime.request.ReadinessTimeout,
	})
	if err != nil {
		return fmt.Errorf("materialize installed UCI candidates: %w", err)
	}
	runtime.installation = installResult.Installation
	if runtime.installation == nil {
		return errors.New("materialize installed UCI candidates returned no installation")
	}
	if err := runtime.verifyMaterializedArtifacts(); err != nil {
		return err
	}
	parserPath, err := runtime.installation.Executable("parser")
	if err != nil {
		return err
	}
	daemonPath, err := runtime.installation.Executable("daemon")
	if err != nil {
		return err
	}
	runtime.parserPath, runtime.daemonPath = parserPath, daemonPath
	return nil
}

func (runtime *uciInstalledAcceptanceRuntime) verifyMaterializedArtifacts() error {
	for _, role := range []string{"server", "daemon", "parser"} {
		candidateHash, err := uciInstalledAcceptanceFileSHA256(runtime.candidates[role].Executable)
		if err != nil {
			return err
		}
		installedPath, err := runtime.installation.Executable(role)
		if err != nil {
			return err
		}
		installedHash, err := uciInstalledAcceptanceFileSHA256(installedPath)
		if err != nil {
			return err
		}
		if candidateHash != installedHash {
			return fmt.Errorf("installed %s artifact bytes do not match its candidate", role)
		}
		runtime.result.Artifacts[role] = uciInstalledAcceptanceArtifact{CandidateSHA256: candidateHash, InstalledSHA256: installedHash}
	}
	return nil
}

func (runtime *uciInstalledAcceptanceRuntime) startServer(ctx context.Context) error {
	reservations, err := uciReserveInstalledAcceptanceLoopback(runtime.request.LoopbackHost, runtime.request.ReservedLoopbackPortCount)
	if err != nil {
		return err
	}
	runtime.reservations = reservations
	runtime.result.ReservedLoopbackPorts = uciInstalledAcceptanceReservationPorts(reservations)
	runtime.serverPort = reservations[0].port
	if err := reservations[0].listener.Close(); err != nil {
		return fmt.Errorf("release reserved server loopback port: %w", err)
	}
	reservations[0].listener = nil
	serverEnvironment, clientEnvironment, err := uciInstalledAcceptanceEnvironment(runtime.request, runtime.authority, runtime.serverPort, runtime.parserBundleDigest, runtime.parserPath)
	if err != nil {
		return err
	}
	runtime.serverEnvironment, runtime.clientEnvironment = serverEnvironment, clientEnvironment
	server, err := runtime.installation.Start(ctx, uciInstalledHarnessLaunchRequest{Role: "server", Environment: serverEnvironment})
	if err != nil {
		return err
	}
	runtime.result.Processes.ServerPID = server.command.Process.Pid
	readinessCtx, cancel := context.WithTimeout(ctx, runtime.request.ReadinessTimeout)
	err = uciWaitForInstalledAcceptanceLoopback(readinessCtx, runtime.request.LoopbackHost, runtime.serverPort)
	cancel()
	return err
}

func (runtime *uciInstalledAcceptanceRuntime) startClientsAndBootstrap(ctx context.Context) error {
	clientA, err := runtime.startPrimaryClient(ctx)
	if err != nil {
		return err
	}
	runtime.clientA = clientA
	runtime.result.Processes.DaemonPID, err = uciWaitForInstalledAcceptanceDaemonPID(ctx, runtime.daemonControlRoot, runtime.daemonPath)
	if err != nil {
		return err
	}
	runtime.activeDaemonPID = runtime.result.Processes.DaemonPID
	clientB, err := runtime.startClient(ctx, uciInstalledAcceptanceClientB, runtime.worktrees.linkedRoot)
	if err != nil {
		return err
	}
	runtime.clientB = clientB
	runtime.result.SimultaneousAB = true
	if err := uciRequireInstalledAcceptanceTools(clientA.Transcript()); err != nil {
		return err
	}
	if err := uciRequireInstalledAcceptanceTools(clientB.Transcript()); err != nil {
		return err
	}
	selectedA, selectedB, err := uciSelectInstalledAcceptanceCheckouts(ctx, clientA, clientB, runtime.authority)
	if err != nil {
		return err
	}
	runtime.selectedA, runtime.selectedB = selectedA, selectedB
	runtime.result.Bootstrap.InitialViewAbsent[uciInstalledAcceptanceClientA] = selectedA.viewID == ""
	runtime.result.Bootstrap.InitialViewAbsent[uciInstalledAcceptanceClientB] = selectedB.viewID == ""
	if !runtime.result.Bootstrap.InitialViewAbsent[uciInstalledAcceptanceClientA] || !runtime.result.Bootstrap.InitialViewAbsent[uciInstalledAcceptanceClientB] {
		return errors.New("installed UCI checkout selection was not View-free before the first index")
	}
	selectedA, selectedB, err = uciStartInstalledAcceptanceIndexes(ctx, clientA, clientB, selectedA, selectedB, runtime.worktrees)
	if err != nil {
		return err
	}
	runtime.selectedA, runtime.selectedB = selectedA, selectedB
	runtime.result.Bootstrap.IndexStarted[uciInstalledAcceptanceClientA] = true
	runtime.result.Bootstrap.IndexStarted[uciInstalledAcceptanceClientB] = true
	publications, err := uciObserveInstalledAcceptancePublications(ctx, clientA, clientB, selectedA, selectedB, runtime.result)
	if err != nil {
		return fmt.Errorf("%w: %v", errUCIInstalledAcceptanceBarrierBoundary, err)
	}
	runtime.publications = publications
	return nil
}

func (runtime *uciInstalledAcceptanceRuntime) startPrimaryClient(ctx context.Context) (*uciInstalledAcceptanceMCPClient, error) {
	process, err := runtime.installation.Start(ctx, uciInstalledHarnessLaunchRequest{Role: "daemon", WorkingDirectory: runtime.worktrees.primaryRoot, Environment: runtime.clientEnvironment, WithStdio: true})
	if err != nil {
		return nil, err
	}
	client, err := newUCIInstalledAcceptanceMCPClient(uciInstalledAcceptanceClientA, process)
	if err != nil {
		return nil, errors.Join(err, process.closePipes(), runtime.installation.tree.Close(), process.wait())
	}
	if err := client.InitializeAndList(ctx); err != nil {
		return nil, errors.Join(err, process.closePipes(), runtime.installation.tree.Close(), process.wait())
	}
	return client, nil
}

func (runtime *uciInstalledAcceptanceRuntime) startClient(ctx context.Context, name, workingDirectory string) (*uciInstalledAcceptanceMCPClient, error) {
	process, err := runtime.installation.Start(ctx, uciInstalledHarnessLaunchRequest{Role: "daemon", WorkingDirectory: workingDirectory, Environment: runtime.clientEnvironment, WithStdio: true})
	if err != nil {
		return nil, err
	}
	client, err := newUCIInstalledAcceptanceMCPClient(name, process)
	if err != nil {
		return nil, err
	}
	if err := client.InitializeAndList(ctx); err != nil {
		return client, err
	}
	return client, nil
}

func (runtime *uciInstalledAcceptanceRuntime) captureClientTranscripts() {
	if runtime.clientA != nil {
		runtime.result.ClientTranscripts[uciInstalledAcceptanceClientA] = runtime.clientA.Transcript()
	}
	if runtime.clientB != nil {
		runtime.result.ClientTranscripts[uciInstalledAcceptanceClientB] = runtime.clientB.Transcript()
	}
	if runtime.clientC != nil {
		runtime.result.ClientTranscripts[uciInstalledAcceptanceClientC] = runtime.clientC.Transcript()
	}
}

func (runtime *uciInstalledAcceptanceRuntime) runScenarioProbe(ctx context.Context) error {
	return uciRunInstalledAcceptanceScenarioProbe(ctx, uciInstalledAcceptanceScenarioRuntime{
		Request:            runtime.request,
		Installation:       runtime.installation,
		Authority:          runtime.authority,
		Worktrees:          runtime.worktrees,
		ClientA:            runtime.clientA,
		ClientB:            runtime.clientB,
		Recorder:           runtime.clientA,
		Selections:         map[string]uciInstalledAcceptanceSelection{uciInstalledAcceptanceClientA: runtime.selectedA, uciInstalledAcceptanceClientB: runtime.selectedB, uciInstalledAcceptanceClientRecorder: runtime.selectedA},
		Publications:       map[string]uciInstalledAcceptancePublication{uciInstalledAcceptanceClientA: runtime.publications[uciInstalledAcceptanceClientA], uciInstalledAcceptanceClientB: runtime.publications[uciInstalledAcceptanceClientB], uciInstalledAcceptanceClientRecorder: runtime.publications[uciInstalledAcceptanceClientA]},
		ParserBundleDigest: runtime.parserBundleDigest,
		Candidates:         runtime.candidates,
		ServerEnvironment:  runtime.serverEnvironment,
		ClientEnvironment:  runtime.clientEnvironment,
	}, runtime.result)
}

func (runtime *uciInstalledAcceptanceRuntime) exerciseBase(ctx context.Context) error {
	if err := runtime.recordParserAndObservations(ctx); err != nil {
		return err
	}
	if err := runtime.exerciseThirdClientAndRecorder(ctx); err != nil {
		return err
	}
	return runtime.exerciseWatcherAndRestart(ctx)
}

func (runtime *uciInstalledAcceptanceRuntime) recordParserAndObservations(ctx context.Context) error {
	parserPIDs := uciWaitForInstalledAcceptanceParserPIDs(ctx, runtime.installation, runtime.parserPath)
	if len(parserPIDs) == 0 {
		return fmt.Errorf("%w: codebase_index reached published Views without an installed parser process", errUCIInstalledAcceptanceParserBoundary)
	}
	parserCanarySource, err := uciRequireInstalledAcceptanceParserCanaryPublished(ctx, runtime.authority, runtime.worktrees, runtime.publications, runtime.parserBundleDigest)
	if err != nil {
		return fmt.Errorf("%w: %v", errUCIInstalledAcceptanceParserBoundary, err)
	}
	requestDigest, err := uci.TreeSitterWireRequestDigest(uciInstalledAcceptanceParserCanaryRequest(runtime.authority.profile.ProfileID, runtime.parserBundleDigest, parserCanarySource))
	if err != nil {
		return fmt.Errorf("%w: hash installed parser canary request: %v", errUCIInstalledAcceptanceParserBoundary, err)
	}
	parserRequestDigest, err := uciInstalledAcceptanceBareSHA256(string(requestDigest))
	if err != nil {
		return fmt.Errorf("%w: validate installed parser request digest: %v", errUCIInstalledAcceptanceParserBoundary, err)
	}
	runtime.result.Processes.ParserPID = parserPIDs[0]
	runtime.result.Parser = uciInstalledAcceptanceParserEvidence{UsedInstalledArtifact: true, InvocationCount: len(parserPIDs), RequestDigest: parserRequestDigest}
	for _, item := range []struct {
		name           string
		client         *uciInstalledAcceptanceMCPClient
		selection      uciInstalledAcceptanceSelection
		expectedCallee string
	}{
		{uciInstalledAcceptanceClientA, runtime.clientA, runtime.selectedA, runtime.request.Fixture.PrimaryCallee},
		{uciInstalledAcceptanceClientB, runtime.clientB, runtime.selectedB, runtime.request.Fixture.LinkedCallee},
	} {
		observation, err := uciObserveInstalledAcceptanceSearchGraphRead(ctx, item.client, item.selection, runtime.publications[item.name], runtime.request.Fixture, item.expectedCallee)
		if err != nil {
			return fmt.Errorf("installed standard MCP public isolation for %s: %w", item.name, err)
		}
		runtime.result.Observations[item.name] = observation
	}
	return nil
}

func (runtime *uciInstalledAcceptanceRuntime) exerciseThirdClientAndRecorder(ctx context.Context) error {
	clientC, err := runtime.startClient(ctx, uciInstalledAcceptanceClientC, runtime.worktrees.primaryRoot)
	runtime.clientC = clientC
	if err != nil {
		return err
	}
	if err := uciRequireInstalledAcceptanceTools(clientC.Transcript()); err != nil {
		return err
	}
	if err := uciExerciseInstalledAcceptanceThirdClient(ctx, runtime.clientA, runtime.clientB, clientC, runtime.authority, runtime.result); err != nil {
		return fmt.Errorf("installed standard MCP third-client default isolation: %w", err)
	}
	if err := uciExerciseInstalledAcceptanceRefusals(ctx, uciInstalledAcceptanceRefusalInput{first: runtime.clientA, second: runtime.clientB, third: clientC, firstSelection: runtime.selectedA, secondSelection: runtime.selectedB, authority: runtime.authority, result: runtime.result}); err != nil {
		return fmt.Errorf("installed standard MCP authorization-negative matrix: %w", err)
	}
	recorder, err := runtime.startClient(ctx, "client-recorder", runtime.worktrees.primaryRoot)
	if err != nil {
		return err
	}
	if err := uciRequireInstalledAcceptanceTools(recorder.Transcript()); err != nil {
		return err
	}
	selection, err := uciSelectInstalledAcceptanceCheckout(ctx, recorder, uciInstalledAcceptanceClientA, runtime.authority)
	if err != nil {
		return err
	}
	if err := uciExerciseInstalledAcceptanceRecorder(ctx, recorder, selection, runtime.authority, runtime.request.Fixture.SharedSymbol, runtime.result); err != nil {
		return fmt.Errorf("installed standard MCP recorder matrix: %w", err)
	}
	if err := recorder.process.closePipes(); err != nil {
		return fmt.Errorf("close installed recorder client: %w", err)
	}
	return nil
}

func (runtime *uciInstalledAcceptanceRuntime) exerciseWatcherAndRestart(ctx context.Context) error {
	publications, err := uciExerciseInstalledAcceptanceWatcher(ctx, uciInstalledAcceptanceWatcherInput{fixture: runtime.request.Fixture, authority: runtime.authority, worktrees: runtime.worktrees, first: runtime.clientA, second: runtime.clientB, firstSelection: runtime.selectedA, secondSelection: runtime.selectedB, before: runtime.publications, result: runtime.result})
	if err != nil {
		return fmt.Errorf("installed standard MCP watcher A/B dirty-view isolation: %w", err)
	}
	primary, primaryFound := publications[uciInstalledAcceptanceClientA]
	linked, linkedFound := publications[uciInstalledAcceptanceClientB]
	if !primaryFound || !linkedFound {
		return errors.New("installed standard MCP watcher did not retain both publication baselines")
	}
	runtime.publications = publications
	runtime.selectedA.viewID, runtime.selectedA.runID = primary.viewID, primary.runID
	runtime.selectedB.viewID, runtime.selectedB.runID = linked.viewID, linked.runID
	installation, daemonPID, err := uciRestartInstalledAcceptance(ctx, uciInstalledAcceptanceRestartInput{
		request: runtime.request, candidates: runtime.candidates, authority: runtime.authority, worktrees: runtime.worktrees,
		serverPort: runtime.serverPort, parserBundleDigest: runtime.parserBundleDigest, daemonControlRoot: runtime.daemonControlRoot,
		oldInstallation: runtime.installation, daemonOwner: &runtime.activeDaemonPID,
		oldClients:   map[string]*uciInstalledAcceptanceMCPClient{uciInstalledAcceptanceClientA: runtime.clientA, uciInstalledAcceptanceClientB: runtime.clientB, uciInstalledAcceptanceClientC: runtime.clientC},
		selections:   map[string]uciInstalledAcceptanceSelection{uciInstalledAcceptanceClientA: runtime.selectedA, uciInstalledAcceptanceClientB: runtime.selectedB},
		publications: runtime.publications, result: runtime.result,
	})
	if err != nil {
		return fmt.Errorf("installed standard MCP restart matrix: %w", err)
	}
	runtime.installation, runtime.activeDaemonPID = installation, daemonPID
	return nil
}

func uciNewInstalledAcceptanceResult(request uciInstalledAcceptanceRequest) uciInstalledAcceptanceResult {
	return uciInstalledAcceptanceResult{
		AcceptanceVersion:     request.Version,
		InstallHarnessVersion: request.InstallHarnessVersion,
		Fixture:               uciInstalledAcceptanceFixtureEvidenceFor(request.Fixture),
		Artifacts:             make(map[string]uciInstalledAcceptanceArtifact),
		LoopbackHost:          request.LoopbackHost,
		ReservedLoopbackPorts: make([]int, 0),
		ClientTranscripts:     make(map[string]uciInstalledAcceptanceClientTranscript),
		Bootstrap: uciInstalledAcceptanceBootstrap{
			InitialViewAbsent: make(map[string]bool),
			IndexStarted:      make(map[string]bool),
			StatusStates:      make(map[string]string),
			BarrierStates:     make(map[string]string),
		},
		Worktrees:      uciInstalledAcceptanceWorktrees{Registered: make(map[string]bool)},
		ClientContexts: make(map[string]uciInstalledAcceptanceContext),
		Observations:   make(map[string]uciInstalledAcceptanceObservations),
		Defaults: uciInstalledAcceptanceDefaults{
			BeforeThirdClientViewDigests: make(map[string]string),
			AfterThirdClientViewDigests:  make(map[string]string),
		},
		Refusals: make(map[string]uciInstalledAcceptanceClosedOutcome),
		Restart: uciInstalledAcceptanceRestart{
			ClientTranscripts:    make(map[string]uciInstalledAcceptanceClientTranscript),
			BeforeClientContexts: make(map[string]uciInstalledAcceptanceContext),
			ClientContexts:       make(map[string]uciInstalledAcceptanceContext),
			BeforeObservations:   make(map[string]uciInstalledAcceptanceObservations),
			Observations:         make(map[string]uciInstalledAcceptanceObservations),
		},
	}
}

func uciValidateInstalledAcceptanceRequest(ctx context.Context, request uciInstalledAcceptanceRequest) error {
	if ctx == nil {
		return errors.New("UCI installed acceptance context is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("UCI installed acceptance context: %w", err)
	}
	if request.Version != uciInstalledAcceptanceVersionV1 {
		return fmt.Errorf("UCI installed acceptance version = %q", request.Version)
	}
	if request.InstallHarnessVersion != uciInstallHarnessVersionV1 {
		return fmt.Errorf("UCI installed acceptance install harness version = %q", request.InstallHarnessVersion)
	}
	if err := uciValidateInstalledAcceptanceTestPostgres(request.TestPostgresDSN); err != nil {
		return err
	}
	if request.LoopbackHost != "127.0.0.1" && request.LoopbackHost != "localhost" && request.LoopbackHost != "::1" {
		return errors.New("UCI installed acceptance loopback host is not local")
	}
	if request.ReservedLoopbackPortCount < 2 {
		return errors.New("UCI installed acceptance needs at least two reserved loopback ports")
	}
	if request.ReadinessTimeout <= 0 || request.OperationTimeout <= 0 {
		return errors.New("UCI installed acceptance timeouts must be positive")
	}
	if request.ReadinessTimeout > request.OperationTimeout {
		return errors.New("UCI installed acceptance readiness timeout exceeds operation timeout")
	}
	if err := uciValidateInstalledAcceptanceEmbeddingProvider(request.EmbeddingProvider); err != nil {
		return err
	}
	if err := uciValidateInstalledAcceptanceScenarioProbe(request); err != nil {
		return err
	}
	if err := uciValidateInstalledAcceptanceSourceRoot(request.CandidateSourceRoot); err != nil {
		return err
	}
	if err := uciValidateInstalledAcceptanceDisposableRoots(request); err != nil {
		return err
	}
	return uciValidateInstalledAcceptanceFixture(request.Fixture)
}

func uciValidateInstalledAcceptanceScenarioProbe(request uciInstalledAcceptanceRequest) error {
	switch request.ScenarioProbePhase {
	case "":
		return nil
	case uciInstalledAcceptanceScenarioProbeBeforeBaseWatcher:
		if request.ScenarioProbe == nil {
			return errors.New("installed acceptance pre-base scenario phase requires a probe")
		}
		return nil
	default:
		return fmt.Errorf("installed acceptance scenario probe phase = %q", request.ScenarioProbePhase)
	}
}

func uciValidateInstalledAcceptanceDisposableRoots(request uciInstalledAcceptanceRequest) error {
	roots := []string{request.InstallRoot, request.FixtureRoot, request.LocalStateRoot}
	for index, root := range roots {
		if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return errors.New("UCI installed acceptance disposable root is invalid")
		}
		if _, err := os.Lstat(root); err == nil {
			return errors.New("UCI installed acceptance disposable root already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect UCI installed acceptance disposable root: %w", err)
		}
		for _, prior := range roots[:index] {
			if uciInstalledAcceptancePathOverlaps(root, prior) {
				return errors.New("UCI installed acceptance disposable roots overlap")
			}
		}
	}
	return nil
}

func uciValidateInstalledAcceptanceEmbeddingProvider(provider *uciInstalledAcceptanceEmbeddingProvider) error {
	if provider == nil {
		return nil
	}
	if provider.URL == "" || provider.Model == "" || provider.Key == "" || strings.TrimSpace(provider.URL) != provider.URL || strings.TrimSpace(provider.Model) != provider.Model || strings.TrimSpace(provider.Key) != provider.Key {
		return errors.New("installed acceptance embedding provider is incomplete")
	}
	parsed, err := url.Parse(provider.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("installed acceptance embedding provider URL is invalid")
	}
	return nil
}

func uciValidateInstalledAcceptanceScenarioEvidence(evidence map[string]uciInstalledAcceptanceScenarioEvidence) error {
	for scenarioID, scenarioEvidence := range evidence {
		if !uciInstalledAcceptanceSafeScenarioID(scenarioID) {
			return errors.New("installed scenario evidence ID is unsafe")
		}
		if scenarioEvidence.Code != uciInstalledAcceptanceScenarioCodeObserved {
			return errors.New("installed scenario evidence code is unsafe")
		}
		if !uciInstalledAcceptanceIsBareSHA256(scenarioEvidence.Digest) {
			return errors.New("installed scenario evidence digest is unsafe")
		}
	}
	return nil
}

func uciInstalledAcceptanceScenarioRunsBeforeBaseWatcher(request uciInstalledAcceptanceRequest) bool {
	return request.ScenarioProbe != nil && request.ScenarioProbePhase == uciInstalledAcceptanceScenarioProbeBeforeBaseWatcher
}

func uciRunInstalledAcceptanceScenarioProbe(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, result *uciInstalledAcceptanceResult) error {
	if runtime.Request.ScenarioProbe == nil {
		return nil
	}
	if result == nil {
		return errors.New("installed UCI scenario result is unavailable")
	}
	evidence, err := runtime.Request.ScenarioProbe(ctx, runtime)
	if err != nil {
		return err
	}
	if err := uciValidateInstalledAcceptanceScenarioEvidence(evidence); err != nil {
		return err
	}
	if len(evidence) > 0 {
		result.ScenarioEvidence = make(map[string]uciInstalledAcceptanceScenarioEvidence, len(evidence))
		for scenarioID, scenarioEvidence := range evidence {
			result.ScenarioEvidence[scenarioID] = scenarioEvidence
		}
	}
	return nil
}

func uciInstalledAcceptanceSafeScenarioID(id string) bool {
	if len(id) != 3 || id[0] != 'U' {
		return false
	}
	return id[1] >= '0' && id[1] <= '9' && id[2] >= '0' && id[2] <= '9'
}

func uciValidateInstalledAcceptanceTestPostgres(raw string) error {
	originalDSN := strings.TrimSpace(raw)
	parsed, err := url.Parse(originalDSN)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return errUCIInstalledAcceptanceNonTestPostgres
	}
	if _, err := url.ParseQuery(parsed.RawQuery); err != nil {
		return errUCIInstalledAcceptanceNonTestPostgres
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return errUCIInstalledAcceptanceNonTestPostgres
	}
	value := strings.ToLower(originalDSN)
	if !strings.Contains(value, "test") || strings.Contains(value, "prod") || strings.Contains(value, "staging") {
		return errUCIInstalledAcceptanceNonTestPostgres
	}
	return nil
}

func uciValidateInstalledAcceptanceSourceRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("UCI installed acceptance candidate source root is invalid")
	}
	info, err := os.Stat(filepath.Join(root, "go.mod"))
	if err != nil || info.IsDir() {
		return errors.New("UCI installed acceptance candidate source root has no go.mod")
	}
	return nil
}

func uciInstalledAcceptancePhysicalPath(raw string) (string, error) {
	if raw == "" || !utf8.ValidString(raw) || strings.IndexByte(raw, 0) >= 0 || !filepath.IsAbs(raw) || filepath.Clean(raw) != raw {
		return "", errors.New("path is invalid")
	}
	for _, value := range raw {
		if value < 0x20 || value == 0x7f {
			return "", errors.New("path contains control characters")
		}
	}
	physical, err := filepath.EvalSymlinks(raw)
	if err != nil {
		return "", fmt.Errorf("resolve physical path: %w", err)
	}
	return filepath.Clean(physical), nil
}

func uciInstalledAcceptanceFileURI(raw string) (string, error) {
	physical, err := uciInstalledAcceptancePhysicalPath(raw)
	if err != nil {
		return "", err
	}
	filePath := filepath.ToSlash(physical)
	if runtime.GOOS == "windows" {
		filePath = "/" + filePath
	}
	return (&url.URL{Scheme: "file", Path: filePath}).String(), nil
}

func uciInstalledAcceptanceSamePhysicalPath(left, right string) (bool, error) {
	left, err := uciInstalledAcceptancePhysicalPath(left)
	if err != nil {
		return false, err
	}
	right, err = uciInstalledAcceptancePhysicalPath(right)
	if err != nil {
		return false, err
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right), nil
	}
	return left == right, nil
}

func uciValidateInstalledAcceptanceFixture(fixture uciInstalledAcceptanceFixture) error {
	if fixture.RelativePath == "" || path.IsAbs(fixture.RelativePath) || path.Clean(fixture.RelativePath) != fixture.RelativePath || fixture.RelativePath == ".." || strings.HasPrefix(fixture.RelativePath, "../") || strings.Contains(fixture.RelativePath, `\`) {
		return errors.New("UCI installed acceptance fixture path is invalid")
	}
	for _, value := range []string{fixture.SharedSymbol, fixture.PrimaryCallee, fixture.LinkedCallee} {
		if value == "" || strings.TrimSpace(value) != value {
			return errors.New("UCI installed acceptance fixture identifier is invalid")
		}
	}
	if fixture.PrimarySource == "" || fixture.LinkedSource == "" || fixture.PrimarySource == fixture.LinkedSource {
		return errors.New("UCI installed acceptance fixture sources are invalid")
	}
	return nil
}

func uciInstalledAcceptancePathOverlaps(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		left, right = strings.ToLower(left), strings.ToLower(right)
	}
	return left == right || strings.HasPrefix(left, right+string(filepath.Separator)) || strings.HasPrefix(right, left+string(filepath.Separator))
}

func uciInstalledAcceptanceRemoveRoot(targetErr *error, root string) bool {
	if removeErr := os.RemoveAll(root); removeErr != nil {
		*targetErr = errors.Join(*targetErr, fmt.Errorf("remove UCI installed acceptance disposable root: %w", removeErr))
		return false
	}
	_, err := os.Lstat(root)
	return errors.Is(err, os.ErrNotExist)
}

func uciBuildInstalledAcceptanceCandidates(ctx context.Context, sourceRoot, buildRoot string) (map[string]uciInstallHarnessCommand, error) {
	if err := os.Mkdir(buildRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create installed acceptance candidate build root: %w", err)
	}
	extension := ""
	if runtime.GOOS == "windows" {
		extension = ".exe"
	}
	candidates := map[string]struct {
		output string
		args   []string
		env    []string
	}{
		"server": {
			output: filepath.Join(buildRoot, "engram-server"+extension),
			args:   []string{"build", "-tags", "fts5", "-o", filepath.Join(buildRoot, "engram-server"+extension), "./cmd/engram-server"},
			env:    []string{"CGO_ENABLED=1"},
		},
		"daemon": {
			output: filepath.Join(buildRoot, "engram"+extension),
			args:   []string{"build", "-o", filepath.Join(buildRoot, "engram"+extension), "./cmd/engram"},
			env:    []string{"CGO_ENABLED=0"},
		},
		"parser": {
			output: filepath.Join(buildRoot, "uci-parser"+extension),
			args:   []string{"build", "-o", filepath.Join(buildRoot, "uci-parser"+extension), "./tools/uci-parser"},
			env:    []string{"CGO_ENABLED=1"},
		},
	}
	result := make(map[string]uciInstallHarnessCommand, len(candidates))
	for _, role := range []string{"server", "daemon", "parser"} {
		candidate := candidates[role]
		command := exec.CommandContext(ctx, "go", candidate.args...)
		command.Dir = sourceRoot
		command.Env = append(os.Environ(), candidate.env...)
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		if err := command.Run(); err != nil {
			return nil, fmt.Errorf("build installed acceptance %s candidate: %w", role, err)
		}
		info, err := os.Stat(candidate.output)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("installed acceptance %s candidate was not built", role)
		}
		result[role] = uciInstallHarnessCommand{Executable: candidate.output}
	}
	return result, nil
}

func uciInstalledAcceptanceFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open installed acceptance artifact: %w", err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("hash installed acceptance artifact: %w", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func uciInstalledAcceptanceParserBundleDigest() string {
	return string(uci.TreeSitterBundleDigest())
}

func uciCreateInstalledAcceptanceWorktrees(ctx context.Context, fixtureRoot string, fixture uciInstalledAcceptanceFixture, anchorProjectID string) (uciInstalledAcceptanceWorktreesFixture, uciInstalledAcceptanceWorktrees, error) {
	primaryRoot := filepath.Join(fixtureRoot, "primary")
	linkedRoot := filepath.Join(fixtureRoot, "linked")
	auxiliaryRoots := []string{
		filepath.Join(fixtureRoot, "auxiliary-1"),
		filepath.Join(fixtureRoot, "auxiliary-2"),
		filepath.Join(fixtureRoot, "auxiliary-3"),
		filepath.Join(fixtureRoot, "auxiliary-4"),
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(primaryRoot, fixture.RelativePath)), 0o700); err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, fmt.Errorf("create installed acceptance primary fixture: %w", err)
	}
	anchor, err := json.Marshal(struct {
		Version   int    `json:"version"`
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
		Scope     string `json:"scope"`
	}{Version: 3, ProjectID: anchorProjectID, Name: "uci-installed-acceptance", Scope: "repository"})
	if err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, fmt.Errorf("marshal installed acceptance V3 anchor: %w", err)
	}
	if err := os.WriteFile(filepath.Join(primaryRoot, ".engram-project"), append(anchor, '\n'), 0o600); err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, fmt.Errorf("write installed acceptance V3 anchor: %w", err)
	}
	baseline := "package fixture\n\nfunc " + fixture.SharedSymbol + "() string { return BaseCallee() }\nfunc BaseCallee() string { return \"UCI_BASE\" }\n"
	if err := os.WriteFile(filepath.Join(primaryRoot, fixture.RelativePath), []byte(baseline), 0o600); err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, fmt.Errorf("write installed acceptance fixture baseline: %w", err)
	}
	if err := os.WriteFile(filepath.Join(primaryRoot, uciInstalledAcceptanceParserCanaryRelativePath), []byte(uciInstalledAcceptanceParserCanarySource), 0o600); err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, fmt.Errorf("write installed acceptance parser canary: %w", err)
	}
	commands := [][]string{
		{"init"},
		{"config", "user.email", "uci-installed@example.test"},
		{"config", "user.name", "UCI Installed Acceptance"},
		{"add", "--", ".engram-project", fixture.RelativePath, uciInstalledAcceptanceParserCanaryRelativePath},
		{"commit", "-m", "create installed UCI fixture"},
	}
	for _, root := range append([]string{linkedRoot}, auxiliaryRoots...) {
		commands = append(commands, []string{"worktree", "add", "--detach", root, "HEAD"})
	}
	for _, args := range commands {
		if err := uciRunInstalledAcceptanceGit(ctx, primaryRoot, args...); err != nil {
			return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, err
		}
	}
	primaryRoot, err = uciInstalledAcceptancePhysicalPath(primaryRoot)
	if err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, fmt.Errorf("resolve installed acceptance primary checkout: %w", err)
	}
	linkedRoot, err = uciInstalledAcceptancePhysicalPath(linkedRoot)
	if err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, fmt.Errorf("resolve installed acceptance linked checkout: %w", err)
	}
	for index, root := range auxiliaryRoots {
		physical, physicalErr := uciInstalledAcceptancePhysicalPath(root)
		if physicalErr != nil {
			return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, fmt.Errorf("resolve installed acceptance auxiliary checkout: %w", physicalErr)
		}
		auxiliaryRoots[index] = physical
	}
	if err := os.WriteFile(filepath.Join(primaryRoot, fixture.RelativePath), []byte(fixture.PrimarySource), 0o600); err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, fmt.Errorf("write installed acceptance primary dirty source: %w", err)
	}
	if err := os.WriteFile(filepath.Join(linkedRoot, fixture.RelativePath), []byte(fixture.LinkedSource), 0o600); err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, fmt.Errorf("write installed acceptance linked dirty source: %w", err)
	}
	primaryHead, err := uciReadInstalledAcceptanceGit(ctx, primaryRoot, uciInstalledAcceptanceGitRevParse, "HEAD")
	if err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, err
	}
	linkedHead, err := uciReadInstalledAcceptanceGit(ctx, linkedRoot, uciInstalledAcceptanceGitRevParse, "HEAD")
	if err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, err
	}
	for _, root := range auxiliaryRoots {
		auxiliaryHead, auxiliaryErr := uciReadInstalledAcceptanceGit(ctx, root, uciInstalledAcceptanceGitRevParse, "HEAD")
		if auxiliaryErr != nil || auxiliaryHead != primaryHead {
			return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, errors.New("installed acceptance auxiliary worktree is not at the shared HEAD")
		}
	}
	primaryDirty, err := uciInstalledAcceptanceGitPathDirty(ctx, primaryRoot, fixture.RelativePath)
	if err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, err
	}
	linkedDirty, err := uciInstalledAcceptanceGitPathDirty(ctx, linkedRoot, fixture.RelativePath)
	if err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, err
	}
	linkedGit, err := os.Stat(filepath.Join(linkedRoot, ".git"))
	if err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, fmt.Errorf("inspect installed acceptance linked Git file: %w", err)
	}
	result := uciInstalledAcceptanceWorktrees{
		Registered:             make(map[string]bool),
		UsedGitArgumentVectors: true,
		LinkedGitFile:          !linkedGit.IsDir(),
		SameHead:               primaryHead == linkedHead,
		PrimaryDirty:           primaryDirty,
		LinkedDirty:            linkedDirty,
		SharedHeadDigest:       uciInstalledAcceptanceStringDigest(primaryHead),
		RelativePathDigest:     uciInstalledAcceptanceStringDigest(fixture.RelativePath),
	}
	if !result.LinkedGitFile || !result.SameHead || !result.PrimaryDirty || !result.LinkedDirty {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, errors.New("installed acceptance Git worktree topology is invalid")
	}
	return uciInstalledAcceptanceWorktreesFixture{primaryRoot: primaryRoot, linkedRoot: linkedRoot, auxiliaryRoots: auxiliaryRoots, head: primaryHead}, result, nil
}

func uciRunInstalledAcceptanceGit(ctx context.Context, directory string, args ...string) error {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return fmt.Errorf("run installed acceptance Git argument vector: %w", err)
	}
	return nil
}

func uciReadInstalledAcceptanceGit(ctx context.Context, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	command.Stderr = io.Discard
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("read installed acceptance Git argument vector: %w", err)
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		return "", errors.New("installed acceptance Git returned an empty value")
	}
	return value, nil
}

func uciInstalledAcceptanceGitPathDirty(ctx context.Context, directory, relativePath string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "status", "--porcelain=v1", "--", relativePath)
	command.Dir = directory
	command.Stderr = io.Discard
	output, err := command.Output()
	if err != nil {
		return false, fmt.Errorf("inspect installed acceptance Git worktree dirt: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

func uciInstalledAcceptanceTokenExpiry(ctx context.Context, now time.Time) time.Time {
	expiresAt := now.UTC().Add(uciInstalledAcceptanceTokenDefaultTTL)
	if ctx == nil {
		return expiresAt
	}
	if deadline, found := ctx.Deadline(); found {
		deadlineExpiry := deadline.UTC().Add(uciInstalledAcceptanceTokenGrace)
		if deadlineExpiry.After(expiresAt) {
			expiresAt = deadlineExpiry
		}
	}
	return expiresAt
}

func uciPrepareInstalledAcceptanceAuthority(ctx context.Context, dsn string, worktrees uciInstalledAcceptanceWorktreesFixture, parserBundleDigest, anchorProjectID string) (_ *uciInstalledAcceptanceAuthority, retErr error) {
	if err := uciValidateInstalledAcceptanceTestPostgres(dsn); err != nil {
		return nil, err
	}
	originalDSN := strings.TrimSpace(dsn)
	adminDB, err := sql.Open("postgres", originalDSN)
	if err != nil {
		return nil, fmt.Errorf("open installed acceptance test database administration: %w", err)
	}
	authority := &uciInstalledAcceptanceAuthority{
		adminDB:         adminDB,
		checkouts:       make(map[string]*gormdb.UCICheckout),
		anchorProjectID: anchorProjectID,
	}
	failed := true
	defer func() {
		if failed {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			cleanupErr := authority.Close(cleanupCtx)
			cancel()
			if cleanupErr != nil {
				retErr = errors.Join(retErr, cleanupErr)
			}
		}
	}()
	if err := adminDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping installed acceptance test database administration: %w", err)
	}
	schema, err := uciNewInstalledAcceptanceSchema()
	if err != nil {
		return nil, err
	}
	quotedSchema := pq.QuoteIdentifier(schema)
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		return nil, fmt.Errorf("create installed acceptance schema: %w", err)
	}
	authority.schema = schema
	authority.scopedDSN, err = uciInstalledAcceptanceScopedPostgresDSN(originalDSN, schema)
	if err != nil {
		return nil, err
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: authority.scopedDSN, MaxConns: 4})
	if err != nil {
		return nil, fmt.Errorf("open installed acceptance isolated database: %w", err)
	}
	authority.store = store

	authority.clientInstanceID = "uci-installed-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	authority.principal = "agent/uci-installed-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	authority.rawToken, err = uciInstalledAcceptanceKeycard()
	if err != nil {
		return nil, err
	}
	tokenHash, err := bcrypt.GenerateFromPassword([]byte(authority.rawToken), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash installed acceptance keycard: %w", err)
	}
	expiresAt := uciInstalledAcceptanceTokenExpiry(ctx, time.Now())
	tokens := gormdb.NewTokenStore(store)
	authority.token, err = tokens.CreateWithPrincipal(ctx, gormdb.TokenCreatePrincipalInput{
		Name:          "uci-installed-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		TokenHash:     string(tokenHash),
		TokenPrefix:   authority.rawToken[len("engram_") : len("engram_")+8],
		Scope:         "read-write",
		Principal:     authority.principal,
		PrincipalKind: "agent",
		ExpiresAt:     &expiresAt,
	})
	if err != nil {
		return nil, fmt.Errorf("create installed acceptance SourceClient keycard: %w", err)
	}

	contexts := gormdb.NewUCIContextStore(store.GetDB())
	authority.source, err = contexts.CreateSource(ctx, gormdb.CreateSourceInput{
		AuthRealm:   "client",
		Kind:        gormdb.UCISourceGit,
		DisplayName: "uci-installed-source-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
	})
	if err != nil {
		return nil, fmt.Errorf("create installed acceptance source: %w", err)
	}
	checkoutRoots := map[string]string{
		uciInstalledAcceptanceClientA: worktrees.primaryRoot,
		uciInstalledAcceptanceClientB: worktrees.linkedRoot,
	}
	for index, root := range worktrees.auxiliaryRoots {
		checkoutRoots[fmt.Sprintf("auxiliary-%d", index+1)] = root
	}
	for client, root := range checkoutRoots {
		locatorRef, locatorErr := uciInstalledAcceptanceFileURI(root)
		if locatorErr != nil {
			return nil, fmt.Errorf("resolve installed acceptance checkout locator: %w", locatorErr)
		}
		checkout, registerErr := contexts.RegisterCheckout(ctx, gormdb.RegisterCheckoutInput{
			SourceID:       authority.source.SourceID,
			WorkstationID:  authority.token.ID,
			Kind:           gormdb.UCICheckoutWorkingTree,
			OwnerPrincipal: authority.principal,
			LocatorRef:     locatorRef,
			OwnerInstance:  authority.clientInstanceID,
		})
		if registerErr != nil {
			return nil, fmt.Errorf("register installed acceptance checkout: %w", registerErr)
		}
		authority.checkouts[client] = checkout
	}
	authority.profile, err = contexts.CreateProfile(ctx, gormdb.CreateProfileInput{
		ParserBundleDigest:   parserBundleDigest,
		ResolverRevision:     "uci-installed-resolver-v1",
		ChunkerRevision:      "uci-installed-chunker-v1",
		IgnorePolicyDigest:   uciInstalledAcceptanceSHA256Prefix + uciInstalledAcceptanceStringDigest("uci-installed-ignore-policy-v1"),
		BuildContextJSON:     `{"mode":"uci-installed-acceptance"}`,
		SecretPolicyRevision: "uci-installed-secret-policy-v1",
	})
	if err != nil {
		return nil, fmt.Errorf("create installed acceptance analysis profile: %w", err)
	}
	authority.projectKey = uuid.NewString()
	project := gormdb.Project{
		ID:              authority.projectKey,
		ProjectKey:      sql.NullString{String: authority.projectKey, Valid: true},
		AnchorProjectID: sql.NullString{String: authority.anchorProjectID, Valid: true},
		IdentityScope:   sql.NullString{String: "repository", Valid: true},
		IdentityStatus:  sql.NullString{String: "active", Valid: true},
		LegacyIDs:       pq.StringArray{},
	}
	if err := store.GetDB().WithContext(ctx).Create(&project).Error; err != nil {
		return nil, fmt.Errorf("seed installed acceptance V3 anchor registration: %w", err)
	}
	failed = false
	return authority, nil
}

func (authority *uciInstalledAcceptanceAuthority) Close(ctx context.Context) error {
	if authority == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var cleanupErrors []error
	if authority.store != nil {
		if closeErr := authority.store.Close(); closeErr != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("close installed acceptance isolated database: %w", closeErr))
		}
		authority.store = nil
	}
	if authority.adminDB != nil {
		if authority.schema != "" {
			quotedSchema := pq.QuoteIdentifier(authority.schema)
			if _, dropErr := authority.adminDB.ExecContext(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE"); dropErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("drop installed acceptance schema: %w", dropErr))
			} else {
				authority.schema = ""
			}
		}
		if closeErr := authority.adminDB.Close(); closeErr != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("close installed acceptance database administration: %w", closeErr))
		}
		authority.adminDB = nil
	}
	return errors.Join(cleanupErrors...)
}

func uciInstalledAcceptanceCheckoutIDs(checkouts map[string]*gormdb.UCICheckout) ([]string, error) {
	required := [...]string{
		uciInstalledAcceptanceClientA,
		uciInstalledAcceptanceClientB,
		"auxiliary-1",
		"auxiliary-2",
		"auxiliary-3",
		"auxiliary-4",
	}
	if len(checkouts) != len(required) {
		return nil, errors.New("installed acceptance checkout registration shape is incomplete")
	}
	checkoutIDs := make([]string, 0, len(required))
	for _, name := range required {
		checkout := checkouts[name]
		if checkout == nil || checkout.CheckoutID == "" {
			return nil, fmt.Errorf("installed acceptance checkout %q is unavailable", name)
		}
		if checkout.CurrentViewID != nil {
			return nil, fmt.Errorf("installed acceptance checkout %q unexpectedly has a current View", name)
		}
		checkoutIDs = append(checkoutIDs, checkout.CheckoutID)
	}
	if checkoutIDs[0] == checkoutIDs[1] {
		return nil, errors.New("installed acceptance did not register two distinct A/B checkouts")
	}
	return checkoutIDs, nil
}

func uciAssertInstalledAcceptanceNoProjection(ctx context.Context, authority *uciInstalledAcceptanceAuthority) error {
	if authority == nil || authority.store == nil || authority.source == nil || authority.profile == nil {
		return errors.New("installed acceptance authority is incomplete")
	}
	checkoutIDs, err := uciInstalledAcceptanceCheckoutIDs(authority.checkouts)
	if err != nil {
		return err
	}
	type projectionCount struct {
		name  string
		query string
		args  []any
	}
	sourceID := authority.source.SourceID
	counts := []projectionCount{
		{name: "ci_views", query: `SELECT COUNT(*) FROM ci_views WHERE checkout_id IN (?)`, args: []any{checkoutIDs}},
		{name: "ci_blobs", query: `SELECT COUNT(*) FROM ci_blobs WHERE source_id = ?`, args: []any{sourceID}},
		{name: "ci_parse_artifacts", query: `SELECT COUNT(*) FROM ci_parse_artifacts WHERE source_id = ?`, args: []any{sourceID}},
		{name: "ci_definitions", query: `SELECT COUNT(*) FROM ci_definitions WHERE artifact_id IN (SELECT artifact_id FROM ci_parse_artifacts WHERE source_id = ?)`, args: []any{sourceID}},
		{name: "ci_reference_sites", query: `SELECT COUNT(*) FROM ci_reference_sites WHERE artifact_id IN (SELECT artifact_id FROM ci_parse_artifacts WHERE source_id = ?)`, args: []any{sourceID}},
		{name: "ci_chunks", query: `SELECT COUNT(*) FROM ci_chunks WHERE artifact_id IN (SELECT artifact_id FROM ci_parse_artifacts WHERE source_id = ?)`, args: []any{sourceID}},
		{name: "ci_memberships", query: `SELECT COUNT(*) FROM ci_memberships WHERE checkout_id IN (?)`, args: []any{checkoutIDs}},
		{name: "ci_resolved_edges", query: `SELECT COUNT(*) FROM ci_resolved_edges WHERE checkout_id IN (?)`, args: []any{checkoutIDs}},
		{name: "ci_embeddings", query: `SELECT COUNT(*) FROM ci_embeddings WHERE source_id = ?`, args: []any{sourceID}},
		{name: "ci_chunk_embeddings", query: `SELECT COUNT(*) FROM ci_chunk_embeddings WHERE chunk_id IN (SELECT chunk_id FROM ci_chunks WHERE artifact_id IN (SELECT artifact_id FROM ci_parse_artifacts WHERE source_id = ?))`, args: []any{sourceID}},
		{name: "ci_embedding_profiles", query: `SELECT COUNT(*) FROM ci_embedding_profiles WHERE analysis_profile_id = ?`, args: []any{authority.profile.ProfileID}},
		{name: "ci_jobs", query: `SELECT COUNT(*) FROM ci_jobs WHERE checkout_id IN (?)`, args: []any{checkoutIDs}},
		{name: "ci_index_build_parts", query: `SELECT COUNT(*) FROM ci_index_build_parts WHERE build_id IN (SELECT job_id FROM ci_jobs WHERE source_id = ?)`, args: []any{sourceID}},
		{name: "ci_analyses", query: `SELECT COUNT(*) FROM ci_analyses WHERE view_id IN (SELECT view_id FROM ci_views WHERE checkout_id IN (?))`, args: []any{checkoutIDs}},
		{name: "uci_exposures", query: `SELECT COUNT(*) FROM uci_exposures WHERE source_id = ?`, args: []any{sourceID}},
		{name: "uci_completion_evidence", query: `SELECT COUNT(*) FROM uci_completion_evidence WHERE exposure_id IN (SELECT exposure_id FROM uci_exposures WHERE source_id = ?)`, args: []any{sourceID}},
	}
	for _, count := range counts {
		var actual int64
		if err := authority.store.GetDB().WithContext(ctx).Raw(count.query, count.args...).Scan(&actual).Error; err != nil {
			return fmt.Errorf("inspect installed acceptance %s precondition: %w", count.name, err)
		}
		if actual != 0 {
			return fmt.Errorf("installed acceptance %s precondition is not empty", count.name)
		}
	}
	return nil
}

func uciInstalledAcceptanceKeycard() (string, error) {
	bytes := make([]byte, 16)
	if _, err := cryptorand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate installed acceptance keycard: %w", err)
	}
	return "engram_" + hex.EncodeToString(bytes), nil
}

func uciNewInstalledAcceptanceSchema() (string, error) {
	random := make([]byte, 16)
	if _, err := cryptorand.Read(random); err != nil {
		return "", fmt.Errorf("generate installed acceptance schema: %w", err)
	}
	schema := uciInstalledAcceptanceSchemaPrefix + hex.EncodeToString(random)
	if !uciInstalledAcceptanceRunSchema(schema) {
		return "", errors.New("generated installed acceptance schema is invalid")
	}
	return schema, nil
}

func uciInstalledAcceptanceRunSchema(schema string) bool {
	if !strings.HasPrefix(schema, uciInstalledAcceptanceSchemaPrefix) || len(schema) > 63 {
		return false
	}
	suffix := strings.TrimPrefix(schema, uciInstalledAcceptanceSchemaPrefix)
	if len(suffix) != 32 {
		return false
	}
	for _, character := range suffix {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func uciInstalledAcceptanceScopedPostgresDSN(raw, schema string) (string, error) {
	if err := uciValidateInstalledAcceptanceTestPostgres(raw); err != nil {
		return "", err
	}
	if !uciInstalledAcceptanceRunSchema(schema) {
		return "", errors.New("installed acceptance schema is invalid")
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("parse installed acceptance test database DSN")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", errors.New("parse installed acceptance test database DSN query")
	}
	query.Set("options", "-c search_path="+schema+",public")
	parsed.RawQuery = query.Encode()
	scopedDSN := parsed.String()
	if scopedDSN == "" {
		return "", errors.New("construct installed acceptance scoped database DSN")
	}
	return scopedDSN, nil
}

func uciReserveInstalledAcceptanceLoopback(host string, count int) ([]*uciInstalledAcceptanceReservation, error) {
	reservations := make([]*uciInstalledAcceptanceReservation, 0, count)
	for range count {
		listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			for _, reservation := range reservations {
				_ = reservation.listener.Close()
			}
			return nil, fmt.Errorf("reserve installed acceptance loopback port: %w", err)
		}
		_, rawPort, err := net.SplitHostPort(listener.Addr().String())
		if err != nil {
			_ = listener.Close()
			for _, reservation := range reservations {
				_ = reservation.listener.Close()
			}
			return nil, fmt.Errorf("read installed acceptance loopback port: %w", err)
		}
		port, err := strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 {
			_ = listener.Close()
			for _, reservation := range reservations {
				_ = reservation.listener.Close()
			}
			return nil, errors.New("reserved installed acceptance loopback port is invalid")
		}
		reservations = append(reservations, &uciInstalledAcceptanceReservation{listener: listener, port: port})
	}
	return reservations, nil
}

func uciInstalledAcceptanceReservationPorts(reservations []*uciInstalledAcceptanceReservation) []int {
	ports := make([]int, 0, len(reservations))
	for _, reservation := range reservations {
		if reservation != nil {
			ports = append(ports, reservation.port)
		}
	}
	return ports
}

func uciInstalledAcceptanceEnvironment(request uciInstalledAcceptanceRequest, authority *uciInstalledAcceptanceAuthority, serverPort int, parserBundleDigest, parserExecutable string) ([]string, []string, error) {
	if authority == nil || authority.token == nil || authority.profile == nil || authority.rawToken == "" || authority.scopedDSN == "" {
		return nil, nil, errors.New("installed acceptance authority environment is incomplete")
	}
	if authority.profile.ParserBundleDigest != parserBundleDigest {
		return nil, nil, errors.New("installed acceptance parser profile digest does not match runtime contract")
	}
	parserExecutable, err := uciInstalledAcceptancePhysicalPath(parserExecutable)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve installed acceptance parser executable: %w", err)
	}
	serverData := filepath.Join(request.LocalStateRoot, "server-data")
	daemonData := filepath.Join(request.LocalStateRoot, "daemon-data")
	tempRoot := filepath.Join(request.LocalStateRoot, "temp")
	homeRoot := filepath.Join(request.LocalStateRoot, "home")
	for _, directory := range []string{serverData, daemonData, tempRoot, homeRoot, filepath.Join(homeRoot, "appdata"), filepath.Join(homeRoot, "localappdata")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, nil, fmt.Errorf("create installed acceptance process state: %w", err)
		}
	}
	serverURL := "http://" + net.JoinHostPort(request.LoopbackHost, strconv.Itoa(serverPort))
	adminToken, err := uciInstalledAcceptanceKeycard()
	if err != nil {
		return nil, nil, err
	}
	embeddingProvider := uciInstalledAcceptanceEmbeddingProvider{}
	if request.EmbeddingProvider != nil {
		embeddingProvider = *request.EmbeddingProvider
	}

	serverEnvironment := []string{
		"DATABASE_DSN=" + authority.scopedDSN,
		"ENGRAM_WORKER_HOST=" + request.LoopbackHost,
		"ENGRAM_WORKER_PORT=" + strconv.Itoa(serverPort),
		"ENGRAM_AUTH_ADMIN_TOKEN=" + adminToken,
		"ENGRAM_CODE_INTEL_ENABLED=true",
		"ENGRAM_EMBEDDING_URL=" + embeddingProvider.URL,
		"ENGRAM_EMBEDDING_MODEL=" + embeddingProvider.Model,
		"ENGRAM_EMBEDDING_API_KEY=" + embeddingProvider.Key,
		"ENGRAM_RERANK_URL=",
		"ENGRAM_RERANK_MODEL=",
		"ENGRAM_RERANK_API_KEY=",
		"ENGRAM_DATA_DIR=" + serverData,
		"TEMP=" + tempRoot,
		"TMP=" + tempRoot,
		"TMPDIR=" + tempRoot,
		"USERPROFILE=" + homeRoot,
		"HOME=" + homeRoot,
		"APPDATA=" + filepath.Join(homeRoot, "appdata"),
		"LOCALAPPDATA=" + filepath.Join(homeRoot, "localappdata"),
	}
	clientEnvironment := []string{
		"ENGRAM_EMBEDDING_URL=",
		"ENGRAM_EMBEDDING_MODEL=",
		"ENGRAM_EMBEDDING_API_KEY=",
		"ENGRAM_RERANK_URL=",
		"ENGRAM_RERANK_MODEL=",
		"ENGRAM_RERANK_API_KEY=",
		"ENGRAM_CODE_INTEL_ENABLED=true",
		"ENGRAM_URL=" + serverURL,
		"ENGRAM_TOKEN=" + authority.rawToken,
		"ENGRAM_CLIENT_INSTANCE_ID=" + authority.clientInstanceID,
		"ENGRAM_UCI_PARSER_BUNDLE_DIGEST=" + parserBundleDigest,
		uciInstalledAcceptanceParserExecutableEnv + "=" + parserExecutable,
		"ENGRAM_DATA_DIR=" + daemonData,
		"TEMP=" + tempRoot,
		"TMP=" + tempRoot,
		"TMPDIR=" + tempRoot,
		"USERPROFILE=" + homeRoot,
		"HOME=" + homeRoot,
		"APPDATA=" + filepath.Join(homeRoot, "appdata"),

		"LOCALAPPDATA=" + filepath.Join(homeRoot, "localappdata"),
	}
	return serverEnvironment, clientEnvironment, nil
}

func uciInstalledAcceptanceDaemonControlPaths(controlRoot string) ([]string, error) {
	return filepath.Glob(muxserverid.DaemonControlPath(controlRoot, "engram-*") + ".marker.json")
}

func uciStopInstalledAcceptanceDaemon(controlRoot string, verifiedPID int) error {
	if verifiedPID <= 0 {
		return nil
	}
	physicalRoot, err := uciInstalledAcceptancePhysicalPath(controlRoot)
	if err != nil {
		return fmt.Errorf("resolve installed acceptance daemon control root: %w", err)
	}
	markerPaths, err := uciInstalledAcceptanceDaemonControlPaths(physicalRoot)
	if err != nil {
		return err
	}
	for _, markerPath := range markerPaths {
		controlPath := strings.TrimSuffix(markerPath, ".marker.json")
		status, found := readMuxcoreDaemonStatusIdentity(controlPath)
		if !found || status.PID != verifiedPID || status.ShuttingDown {
			continue
		}
		marker, err := readMuxcoreDaemonVersionMarker(markerPath)
		if err != nil || marker.PID != status.PID || marker.DaemonGeneration != status.DaemonGeneration {
			return errors.New("installed acceptance daemon marker does not match the verified daemon")
		}
		response, err := muxcontrol.SendWithTimeout(controlPath, muxcontrol.Request{Cmd: "shutdown", DrainTimeoutMs: 2_000}, 5*time.Second)
		if err != nil {
			return fmt.Errorf("stop installed acceptance daemon: %w", err)
		}
		if response == nil || !response.OK {
			return errors.New("stop installed acceptance daemon was rejected")
		}
		return nil
	}
	return nil
}

func uciWaitInstalledAcceptanceProcessExit(pid int, timeout time.Duration) error {
	if runtime.GOOS != "windows" || pid <= 0 {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	waited := make(chan error, 1)
	go func() {
		_, waitErr := process.Wait()
		waited <- waitErr
	}()
	select {
	case waitErr := <-waited:
		if waitErr == nil || errors.Is(waitErr, os.ErrProcessDone) || errors.Is(waitErr, syscall.EINVAL) {
			return nil
		}
		return fmt.Errorf("wait for installed acceptance daemon exit: %w", waitErr)
	case <-time.After(timeout):
		return errors.New("installed acceptance daemon process did not exit")
	}
}

func uciWaitForInstalledAcceptanceLoopback(ctx context.Context, host string, port int) error {
	dialer := net.Dialer{Timeout: 100 * time.Millisecond}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for installed acceptance server loopback: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func uciWaitForInstalledAcceptanceDaemonPID(ctx context.Context, controlRoot, installedExecutable string) (int, error) {
	controlRoot, err := uciInstalledAcceptancePhysicalPath(controlRoot)
	if err != nil {
		return 0, fmt.Errorf("resolve installed acceptance daemon control root: %w", err)
	}
	installedExecutable, err = uciInstalledAcceptancePhysicalPath(installedExecutable)
	if err != nil {
		return 0, fmt.Errorf("resolve installed acceptance daemon executable: %w", err)
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		markerPaths, err := uciInstalledAcceptanceDaemonControlPaths(controlRoot)
		if err != nil {
			return 0, err
		}
		for _, markerPath := range markerPaths {
			controlPath := strings.TrimSuffix(markerPath, ".marker.json")
			pid, found, err := uciInstalledAcceptanceDaemonPID(controlPath, markerPath, installedExecutable)
			if err != nil {
				return 0, err
			}
			if found {
				return pid, nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("wait for installed acceptance daemon election: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func uciInstalledAcceptanceDaemonPID(controlPath, markerPath, installedExecutable string) (int, bool, error) {
	status, found := readMuxcoreDaemonStatusIdentity(controlPath)
	if !found || status.PID <= 0 || status.ShuttingDown {
		return 0, false, nil
	}
	marker, markerErr := readMuxcoreDaemonVersionMarker(markerPath)
	if errors.Is(markerErr, os.ErrNotExist) {
		return 0, false, nil
	}
	if markerErr != nil {
		return 0, false, fmt.Errorf("read installed acceptance daemon marker: %w", markerErr)
	}
	if marker.PID != status.PID || marker.DaemonGeneration != status.DaemonGeneration {
		return 0, false, errors.New("installed acceptance daemon marker does not match its live control status")
	}
	matches, matchErr := uciInstalledAcceptanceSamePhysicalPath(marker.Exe, installedExecutable)
	if matchErr != nil {
		return 0, false, fmt.Errorf("verify installed acceptance daemon executable: %w", matchErr)
	}
	if !matches {
		return 0, false, errors.New("installed acceptance daemon marker does not name the materialized executable")
	}
	return status.PID, true, nil
}

type uciInstalledAcceptanceMCPToolObserver func(name string, started, returned time.Time, payload json.RawMessage, err error)

type uciInstalledAcceptanceMCPClient struct {
	name        string
	process     *uciStartedInstallHarnessProcess
	writer      *bufio.Writer
	scanner     *bufio.Scanner
	mu          sync.Mutex
	observerMu  sync.RWMutex
	nextID      uint64
	transcript  uciInstalledAcceptanceClientTranscript
	observeTool uciInstalledAcceptanceMCPToolObserver
}
type uciInstalledAcceptanceMCPRequest struct {
	id     string
	method string
	params json.RawMessage
}

// uciInstalledAcceptanceMCPToolCall preserves the exact emitted tools/call
// frame so an idempotency retry can reuse its JSON-RPC ID and JSON parameters.
// Ordinary calls never reuse this value.
type uciInstalledAcceptanceMCPToolCall struct {
	client  string
	name    string
	request uciInstalledAcceptanceMCPRequest
}

type uciInstalledAcceptanceMCPToolResult struct {
	payload json.RawMessage
	isError bool
	call    uciInstalledAcceptanceMCPToolCall
}

type uciInstalledAcceptanceMCPError struct {
	method string
	code   string
	detail string
}

func (err *uciInstalledAcceptanceMCPError) Error() string {
	message := "installed standard MCP " + err.method + " failed with " + err.code
	if err.detail != "" {
		message += ": " + err.detail
	}
	return message
}

func uciInstalledAcceptanceIsTransientContextMismatch(err error) bool {
	var mcpErr *uciInstalledAcceptanceMCPError
	return errors.As(err, &mcpErr) && (mcpErr.detail == "CONTEXT_MISMATCH" || mcpErr.detail == "UCI Bind: rpc error: code = FailedPrecondition desc = CONTEXT_MISMATCH")
}

func uciInstalledAcceptanceStatusTool(ctx context.Context, client *uciInstalledAcceptanceMCPClient, arguments map[string]any) (json.RawMessage, error) {
	var lastErr error
	for range 3 {
		payload, err := client.Tool(ctx, "codebase_status", arguments)
		if err == nil {
			return payload, nil
		}
		lastErr = err
		if !uciInstalledAcceptanceIsTransientContextMismatch(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return nil, lastErr
}

func newUCIInstalledAcceptanceMCPClient(name string, process *uciStartedInstallHarnessProcess) (*uciInstalledAcceptanceMCPClient, error) {
	if process == nil || process.command == nil || process.command.Process == nil || process.command.Process.Pid <= 0 || process.stdin == nil || process.stdout == nil {
		return nil, errors.New("installed standard MCP client has no external stdio process")
	}
	scanner := bufio.NewScanner(process.stdout)
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	return &uciInstalledAcceptanceMCPClient{
		name:    name,
		process: process,
		writer:  bufio.NewWriter(process.stdin),
		scanner: scanner,
		transcript: uciInstalledAcceptanceClientTranscript{
			UsedStdio: true,
		},
	}, nil
}

func (client *uciInstalledAcceptanceMCPClient) InitializeAndList(ctx context.Context) error {
	initialize, _, err := client.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "uci-installed-acceptance", "version": "1"},
	})
	if err != nil {
		return err
	}
	var handshake struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(initialize, &handshake); err != nil || handshake.ProtocolVersion == "" {
		return errors.New("installed standard MCP initialize did not return a protocol version")
	}
	if err := client.notification("notifications/initialized", map[string]any{}); err != nil {
		return err
	}
	listed, _, err := client.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return err
	}
	var toolList struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listed, &toolList); err != nil {
		return errors.New("installed standard MCP tools/list response is invalid")
	}
	client.mu.Lock()
	client.transcript.Tools = client.transcript.Tools[:0]
	for _, tool := range toolList.Tools {
		if tool.Name != "" {
			client.transcript.Tools = append(client.transcript.Tools, tool.Name)
		}
	}
	client.mu.Unlock()
	return nil
}

func (client *uciInstalledAcceptanceMCPClient) setToolObserver(observer uciInstalledAcceptanceMCPToolObserver) func() {
	client.observerMu.Lock()
	previous := client.observeTool
	client.observeTool = observer
	client.observerMu.Unlock()
	return func() {
		client.observerMu.Lock()
		client.observeTool = previous
		client.observerMu.Unlock()
	}
}

func (client *uciInstalledAcceptanceMCPClient) observeToolCall(name string, started, returned time.Time, payload json.RawMessage, err error) {
	client.observerMu.RLock()
	observer := client.observeTool
	client.observerMu.RUnlock()
	if observer != nil {
		observer(name, started, returned, payload, err)
	}
}

func (client *uciInstalledAcceptanceMCPClient) Tool(ctx context.Context, name string, arguments any) (payload json.RawMessage, retErr error) {
	started := time.Now()
	var observed json.RawMessage
	defer func() {
		client.observeToolCall(name, started, time.Now(), observed, retErr)
	}()
	result, err := client.ToolWithCall(ctx, name, arguments)
	if err != nil {
		return nil, err
	}
	observed = result.payload
	if result.isError {
		return nil, &uciInstalledAcceptanceMCPError{method: name, code: uciInstalledAcceptancePublicErrorCode(result.payload), detail: uciInstalledAcceptancePublicErrorDetail(result.payload)}
	}
	return result.payload, nil
}

func (client *uciInstalledAcceptanceMCPClient) ToolWithCall(ctx context.Context, name string, arguments any) (uciInstalledAcceptanceMCPToolResult, error) {
	params, err := json.Marshal(map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{}, fmt.Errorf("marshal installed standard MCP tools/call: %w", err)
	}
	raw, request, err := client.call(ctx, uciInstalledAcceptanceToolsCallMethod, json.RawMessage(params))
	call := uciInstalledAcceptanceMCPToolCall{client: client.name, name: name, request: request}
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{call: call}, fmt.Errorf("installed standard MCP %s tools/call failed: %w", name, err)
	}
	return uciInstalledAcceptanceDecodeToolResult(raw, call)
}

// RetryTool sends the byte-identical prior tools/call frame with the same
// JSON-RPC ID. It deliberately does not advance the client's ordinary ID
// sequence, which remains monotonic for every non-retry call.
func (client *uciInstalledAcceptanceMCPClient) RetryTool(ctx context.Context, call uciInstalledAcceptanceMCPToolCall) (uciInstalledAcceptanceMCPToolResult, error) {
	if call.client != client.name || call.name == "" || call.request.method != uciInstalledAcceptanceToolsCallMethod {
		return uciInstalledAcceptanceMCPToolResult{}, errors.New("installed standard MCP retry does not belong to this client")
	}
	raw, err := client.callWithRequest(ctx, call.request)
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{call: call}, err
	}
	return uciInstalledAcceptanceDecodeToolResult(raw, call)
}

// ReplayToolWithSameJSONRPCID is the deliberate negative-test companion to
// RetryTool: it retains one prior JSON-RPC ID while changing a tools/call
// payload so the installed recorder must return IDEMPOTENCY_MISMATCH.
func (client *uciInstalledAcceptanceMCPClient) ReplayToolWithSameJSONRPCID(ctx context.Context, prior uciInstalledAcceptanceMCPToolCall, name string, arguments any) (uciInstalledAcceptanceMCPToolResult, error) {
	if prior.client != client.name || prior.request.method != uciInstalledAcceptanceToolsCallMethod || prior.request.id == "" {
		return uciInstalledAcceptanceMCPToolResult{}, errors.New("installed standard MCP replay does not belong to this client")
	}
	params, err := json.Marshal(map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{}, fmt.Errorf("marshal installed standard MCP replay: %w", err)
	}
	call := uciInstalledAcceptanceMCPToolCall{
		client: client.name,
		name:   name,
		request: uciInstalledAcceptanceMCPRequest{
			id:     prior.request.id,
			method: prior.request.method,
			params: params,
		},
	}
	raw, err := client.callWithRequest(ctx, call.request)
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{call: call}, err
	}
	return uciInstalledAcceptanceDecodeToolResult(raw, call)
}

type uciInstalledAcceptanceToolEnvelope struct {
	Content []json.RawMessage `json:"content"`
	IsError bool              `json:"isError"`
}

func uciInstalledAcceptanceDecodeToolResult(raw json.RawMessage, call uciInstalledAcceptanceMCPToolCall) (uciInstalledAcceptanceMCPToolResult, error) {
	payload, isError, wrapped, err := uciInstalledAcceptanceDecodeToolEnvelope(raw)
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{}, err
	}
	if !wrapped {
		return uciInstalledAcceptanceMCPToolResult{}, errors.New("installed standard MCP tools/call response is invalid")
	}
	for range 2 {
		nested, nestedError, nestedWrapped, nestedErr := uciInstalledAcceptanceDecodeToolEnvelope(payload)
		if nestedErr != nil {
			return uciInstalledAcceptanceMCPToolResult{}, nestedErr
		}
		if !nestedWrapped {
			break
		}
		payload = nested
		isError = isError || nestedError
	}
	return uciInstalledAcceptanceMCPToolResult{payload: payload, isError: isError, call: call}, nil
}

func uciInstalledAcceptanceDecodeToolEnvelope(encoded json.RawMessage) (json.RawMessage, bool, bool, error) {
	var envelope uciInstalledAcceptanceToolEnvelope
	if err := json.Unmarshal(encoded, &envelope); err != nil || len(envelope.Content) == 0 {
		return nil, false, false, nil
	}
	for _, content := range envelope.Content {
		var textBlock struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(content, &textBlock) == nil && textBlock.Type == "text" {
			payload := json.RawMessage(textBlock.Text)
			if !json.Valid(payload) {
				return nil, false, true, errors.New("installed standard MCP tools/call text is not JSON")
			}
			return append(json.RawMessage(nil), payload...), envelope.IsError, true, nil
		}
		if json.Valid(content) {
			return append(json.RawMessage(nil), content...), envelope.IsError, true, nil
		}
	}
	return nil, false, true, errors.New("installed standard MCP tools/call did not return valid JSON content")
}

func (client *uciInstalledAcceptanceMCPClient) call(ctx context.Context, method string, params any) (json.RawMessage, uciInstalledAcceptanceMCPRequest, error) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, uciInstalledAcceptanceMCPRequest{}, fmt.Errorf("marshal installed standard MCP %s: %w", method, err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	client.nextID++
	request := uciInstalledAcceptanceMCPRequest{
		id:     client.name + "-" + strconv.FormatUint(client.nextID, 10),
		method: method,
		params: encoded,
	}
	payload, err := client.callLocked(ctx, request)
	return payload, request, err
}

func (client *uciInstalledAcceptanceMCPClient) callWithRequest(ctx context.Context, request uciInstalledAcceptanceMCPRequest) (json.RawMessage, error) {
	if request.id == "" || request.method == "" || !json.Valid(request.params) {
		return nil, errors.New("installed standard MCP retry request is invalid")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.callLocked(ctx, request)
}

func (client *uciInstalledAcceptanceMCPClient) callLocked(ctx context.Context, request uciInstalledAcceptanceMCPRequest) (json.RawMessage, error) {
	client.transcript.Methods = append(client.transcript.Methods, request.method)
	frame := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{
		JSONRPC: "2.0",
		ID:      request.id,
		Method:  request.method,
		Params:  request.params,
	}
	if err := json.NewEncoder(client.writer).Encode(frame); err != nil {
		return nil, fmt.Errorf("write installed standard MCP %s: %w", request.method, err)
	}
	if err := client.writer.Flush(); err != nil {
		return nil, fmt.Errorf("flush installed standard MCP %s: %w", request.method, err)
	}
	return client.readResponse(ctx, request.id, request.method)
}

func (client *uciInstalledAcceptanceMCPClient) Transcript() uciInstalledAcceptanceClientTranscript {
	client.mu.Lock()
	defer client.mu.Unlock()
	return uciInstalledAcceptanceClientTranscript{
		UsedStdio: client.transcript.UsedStdio,
		Methods:   append([]string(nil), client.transcript.Methods...),
		Tools:     append([]string(nil), client.transcript.Tools...),
	}
}

func (client *uciInstalledAcceptanceMCPClient) notification(method string, params any) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.transcript.Methods = append(client.transcript.Methods, method)
	if err := json.NewEncoder(client.writer).Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params}); err != nil {
		return fmt.Errorf("write installed standard MCP notification: %w", err)
	}
	return client.writer.Flush()
}

type uciInstalledAcceptanceMCPResponseResult struct {
	payload json.RawMessage
	err     error
}

type uciInstalledAcceptanceMCPResponseFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

func (client *uciInstalledAcceptanceMCPClient) readResponse(ctx context.Context, wantID, method string) (json.RawMessage, error) {
	responses := make(chan uciInstalledAcceptanceMCPResponseResult, 1)
	go client.scanResponse(wantID, method, responses)
	select {
	case response := <-responses:
		return response.payload, response.err
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for installed standard MCP %s: %w", method, ctx.Err())
	}
}

func (client *uciInstalledAcceptanceMCPClient) scanResponse(wantID, method string, responses chan<- uciInstalledAcceptanceMCPResponseResult) {
	for client.scanner.Scan() {
		response, matched := uciInstalledAcceptanceDecodeResponseFrame(client.scanner.Bytes(), wantID, method)
		if !matched {
			continue
		}
		responses <- response
		return
	}
	if err := client.scanner.Err(); err != nil {
		responses <- uciInstalledAcceptanceMCPResponseResult{err: fmt.Errorf("read installed standard MCP %s: %w", method, err)}
		return
	}
	responses <- uciInstalledAcceptanceMCPResponseResult{err: errors.New("installed standard MCP closed before its response")}
}

func uciInstalledAcceptanceDecodeResponseFrame(raw []byte, wantID, method string) (uciInstalledAcceptanceMCPResponseResult, bool) {
	var frame uciInstalledAcceptanceMCPResponseFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return uciInstalledAcceptanceMCPResponseResult{}, false
	}
	var id string
	if err := json.Unmarshal(frame.ID, &id); err != nil || id != wantID {
		return uciInstalledAcceptanceMCPResponseResult{}, false
	}
	if frame.JSONRPC != "2.0" {
		return uciInstalledAcceptanceMCPResponseResult{err: errors.New("installed standard MCP response has an invalid protocol")}, true
	}
	if frame.Error == nil {
		return uciInstalledAcceptanceMCPResponseResult{payload: append(json.RawMessage(nil), frame.Result...)}, true
	}
	code := uciInstalledAcceptanceErrorCode(frame.Error.Code)
	if code == "" {
		code = uciInstalledAcceptancePublicErrorCode(frame.Error.Data)
	}
	detail := uciInstalledAcceptancePublicErrorDetail(frame.Error.Data)
	if detail == "" || strings.HasPrefix(detail, "JSON ") || detail == "unknown JSON payload" {
		detail = uciInstalledAcceptanceSafeErrorDetail(frame.Error.Message)
	}
	return uciInstalledAcceptanceMCPResponseResult{err: &uciInstalledAcceptanceMCPError{method: method, code: code, detail: detail}}, true
}

func uciInstalledAcceptancePublicErrorCode(payload json.RawMessage) string {
	var envelope struct {
		Error *struct {
			Code json.RawMessage `json:"code"`
		} `json:"error"`
		Code json.RawMessage `json:"code"`
	}
	if json.Unmarshal(payload, &envelope) == nil {
		if envelope.Error != nil {
			if code := uciInstalledAcceptanceErrorCode(envelope.Error.Code); code != "" {
				return code
			}
		}
		if code := uciInstalledAcceptanceErrorCode(envelope.Code); code != "" {
			return code
		}
	}
	return "MCP_ERROR"
}

func uciInstalledAcceptanceErrorCode(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil && text != "" {
		return text
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" && json.Valid(raw) && (trimmed[0] == '-' || trimmed[0] >= '0' && trimmed[0] <= '9') {
		return trimmed
	}
	return ""
}

func uciInstalledAcceptancePublicErrorDetail(payload json.RawMessage) string {
	var direct string
	if json.Unmarshal(payload, &direct) == nil {
		return uciInstalledAcceptanceSafeErrorDetail(direct)
	}
	var envelope struct {
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return uciInstalledAcceptancePayloadShape(payload)
	}
	detail := ""
	if len(envelope.Data) != 0 {
		_ = json.Unmarshal(envelope.Data, &detail)
	}
	if strings.TrimSpace(detail) == "" {
		detail = envelope.Message
	}
	if sanitized := uciInstalledAcceptanceSafeErrorDetail(detail); sanitized != "" {
		return sanitized
	}
	return uciInstalledAcceptancePayloadShape(payload)
}

func uciInstalledAcceptanceSafeErrorDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" || len(detail) > 1_024 || !utf8.ValidString(detail) || strings.Contains(detail, "engram_") || strings.Contains(detail, "://") {
		return ""
	}
	for _, character := range detail {
		if character < 0x20 || character == 0x7f {
			return ""
		}
	}
	return detail
}

func uciInstalledAcceptancePayloadShape(payload json.RawMessage) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(payload, &object) == nil && object != nil {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return "JSON object keys=" + strings.Join(keys, ",")
	}
	var array []json.RawMessage
	if json.Unmarshal(payload, &array) == nil && array != nil {
		return "JSON array"
	}
	var text string
	if json.Unmarshal(payload, &text) == nil {
		return "JSON string"
	}
	return "unknown JSON payload"
}

func uciRequireInstalledAcceptanceTools(transcript uciInstalledAcceptanceClientTranscript) error {
	required := map[string]bool{
		"codebase_context": false,
		"codebase_index":   false,
		"codebase_status":  false,
		"codebase_search":  false,
		"codebase_graph":   false,
		"codebase_read":    false,
	}
	for _, tool := range transcript.Tools {
		if _, wanted := required[tool]; wanted {
			required[tool] = true
		}
	}
	for tool, found := range required {
		if !found {
			return fmt.Errorf("installed standard MCP tools/list omitted %s", tool)
		}
	}
	return nil
}

type uciInstalledAcceptanceSelection struct {
	contextHandle string
	viewID        string
	runID         string
}

type uciInstalledAcceptanceSelectedCheckout struct {
	client string
	value  uciInstalledAcceptanceSelection
	err    error
}

type uciInstalledAcceptanceCheckoutClient struct {
	clientName string
	client     *uciInstalledAcceptanceMCPClient
}

func uciSelectInstalledAcceptanceCheckouts(ctx context.Context, first, second *uciInstalledAcceptanceMCPClient, authority *uciInstalledAcceptanceAuthority) (uciInstalledAcceptanceSelection, uciInstalledAcceptanceSelection, error) {
	if authority == nil || authority.source == nil || authority.profile == nil {
		return uciInstalledAcceptanceSelection{}, uciInstalledAcceptanceSelection{}, errors.New("installed acceptance UCI authority is incomplete")
	}
	results := make(chan uciInstalledAcceptanceSelectedCheckout, 2)
	for _, item := range []uciInstalledAcceptanceCheckoutClient{
		{uciInstalledAcceptanceClientA, first},
		{uciInstalledAcceptanceClientB, second},
	} {
		go func(item uciInstalledAcceptanceCheckoutClient) {
			results <- uciSelectInstalledAcceptanceCheckoutForClient(ctx, item, authority)
		}(item)
	}
	values := make(map[string]uciInstalledAcceptanceSelection, 2)
	for range 2 {
		result := <-results
		if result.err != nil {
			return uciInstalledAcceptanceSelection{}, uciInstalledAcceptanceSelection{}, result.err
		}
		values[result.client] = result.value
	}
	return values[uciInstalledAcceptanceClientA], values[uciInstalledAcceptanceClientB], nil
}

func uciSelectInstalledAcceptanceCheckoutForClient(ctx context.Context, item uciInstalledAcceptanceCheckoutClient, authority *uciInstalledAcceptanceAuthority) uciInstalledAcceptanceSelectedCheckout {
	checkout := authority.checkouts[item.clientName]
	if checkout == nil {
		return uciInstalledAcceptanceSelectedCheckout{client: item.clientName, err: errors.New("installed acceptance checkout is unavailable")}
	}
	payload, err := item.client.Tool(ctx, "codebase_context", map[string]any{
		"action": "select",
		"checkout": map[string]any{
			"source_id":           authority.source.SourceID,
			"checkout_id":         checkout.CheckoutID,
			"incarnation_id":      checkout.IncarnationID,
			"analysis_profile_id": authority.profile.ProfileID,
		},
	})
	if err != nil {
		return uciInstalledAcceptanceSelectedCheckout{client: item.clientName, err: err}
	}
	var response struct {
		ContextHandle string `json:"context_handle"`
		BindingKind   string `json:"binding_kind"`
		ViewID        string `json:"view_id"`
		Context       *struct {
			ViewID string `json:"view_id"`
		} `json:"context"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || response.ContextHandle == "" || response.BindingKind != "checkout" {
		return uciInstalledAcceptanceSelectedCheckout{client: item.clientName, err: errors.New("installed standard MCP checkout selection response is invalid")}
	}
	if response.Context != nil && response.Context.ViewID != "" {
		response.ViewID = response.Context.ViewID
	}
	return uciInstalledAcceptanceSelectedCheckout{client: item.clientName, value: uciInstalledAcceptanceSelection{contextHandle: response.ContextHandle, viewID: response.ViewID}}
}

func uciStartInstalledAcceptanceIndexes(ctx context.Context, first, second *uciInstalledAcceptanceMCPClient, firstSelection, secondSelection uciInstalledAcceptanceSelection, worktrees uciInstalledAcceptanceWorktreesFixture) (uciInstalledAcceptanceSelection, uciInstalledAcceptanceSelection, error) {
	type started struct {
		client string
		runID  string
		err    error
	}
	results := make(chan started, 2)
	for _, item := range []struct {
		clientName string
		client     *uciInstalledAcceptanceMCPClient
		selection  uciInstalledAcceptanceSelection
		root       string
	}{
		{uciInstalledAcceptanceClientA, first, firstSelection, worktrees.primaryRoot},
		{uciInstalledAcceptanceClientB, second, secondSelection, worktrees.linkedRoot},
	} {
		go func(item struct {
			clientName string
			client     *uciInstalledAcceptanceMCPClient
			selection  uciInstalledAcceptanceSelection
			root       string
		},
		) {
			payload, err := item.client.Tool(ctx, "codebase_index", map[string]any{"context_handle": item.selection.contextHandle, "root": item.root})
			if err != nil {
				results <- started{client: item.clientName, err: err}
				return
			}
			var response struct {
				Status string `json:"status"`
				RunID  string `json:"run_id"`
			}
			if err := json.Unmarshal(payload, &response); err != nil || response.Status != "started" || response.RunID == "" {
				results <- started{client: item.clientName, err: errors.New("installed standard MCP codebase_index did not return a target run_id")}
				return
			}
			results <- started{client: item.clientName, runID: response.RunID}
		}(item)
	}
	selections := map[string]uciInstalledAcceptanceSelection{
		uciInstalledAcceptanceClientA: firstSelection,
		uciInstalledAcceptanceClientB: secondSelection,
	}
	for range 2 {
		result := <-results
		if result.err != nil {
			return uciInstalledAcceptanceSelection{}, uciInstalledAcceptanceSelection{}, result.err
		}
		selection := selections[result.client]
		selection.runID = result.runID
		selections[result.client] = selection
	}
	return selections[uciInstalledAcceptanceClientA], selections[uciInstalledAcceptanceClientB], nil
}

func uciObserveInstalledAcceptancePublications(ctx context.Context, first, second *uciInstalledAcceptanceMCPClient, firstSelection, secondSelection uciInstalledAcceptanceSelection, result *uciInstalledAcceptanceResult) (map[string]uciInstalledAcceptancePublication, error) {
	publications := make(map[string]uciInstalledAcceptancePublication, 2)
	for _, item := range []struct {
		clientName string
		client     *uciInstalledAcceptanceMCPClient
		selection  uciInstalledAcceptanceSelection
	}{
		{uciInstalledAcceptanceClientA, first, firstSelection},
		{uciInstalledAcceptanceClientB, second, secondSelection},
	} {
		publication, err := uciWaitForInstalledAcceptanceBarrier(ctx, item.client, item.selection)
		if err != nil {
			return nil, err
		}
		publication, err = uciWaitForInstalledAcceptanceQuiescence(ctx, item.client, item.selection, publication)
		if err != nil {
			return nil, err
		}
		publications[item.clientName] = publication
		result.Bootstrap.StatusStates[item.clientName] = publication.freshnessState
		result.Bootstrap.BarrierStates[item.clientName] = publication.barrierState
		result.ClientContexts[item.clientName] = uciInstalledAcceptanceContext{
			SourceDigest:   uciInstalledAcceptanceStringDigest(publication.sourceID),
			CheckoutDigest: uciInstalledAcceptanceStringDigest(publication.checkoutID),
			ViewDigest:     uciInstalledAcceptanceStringDigest(publication.viewID),
		}
	}
	return publications, nil
}

type uciInstalledAcceptancePublication struct {
	sourceID         string
	checkoutID       string
	viewID           string
	profileID        string
	generation       int64
	runID            string
	freshnessState   string
	barrierState     string
	evidenceRecorder string
}

func uciWaitForInstalledAcceptanceParserPIDs(ctx context.Context, installation *uciInstallHarnessInstallation, parserPath string) []int {
	deadline := time.NewTimer(250 * time.Millisecond)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if pids := installation.ObservedPIDsForExecutable(parserPath); len(pids) > 0 {
			return pids
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			return nil
		case <-ticker.C:
		}
	}
}

func uciInstalledAcceptanceParserCanaryRequest(profileID, parserBundleDigest string, source []byte) uci.TreeSitterParseRequest {
	return uci.TreeSitterParseRequest{
		Language:   uci.TreeSitterLanguageTypeScript,
		ProfileKey: "uci-prepared-tree-sitter/v1:" + profileID + ":" + string(uci.TreeSitterLanguageTypeScript) + ":" + parserBundleDigest,
		Source:     append([]byte(nil), source...),
	}
}

func uciRequireInstalledAcceptanceParserCanaryPublished(ctx context.Context, authority *uciInstalledAcceptanceAuthority, worktrees uciInstalledAcceptanceWorktreesFixture, publications map[string]uciInstalledAcceptancePublication, parserBundleDigest string) ([]byte, error) {
	if authority == nil || authority.store == nil || authority.source == nil || authority.profile == nil {
		return nil, errors.New("installed parser canary authority is incomplete")
	}
	roots := map[string]string{
		uciInstalledAcceptanceClientA: worktrees.primaryRoot,
		uciInstalledAcceptanceClientB: worktrees.linkedRoot,
	}
	var primarySource []byte
	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB} {
		checkout := authority.checkouts[client]
		publication, found := publications[client]
		source, err := uciRequireInstalledAcceptanceParserCanaryForClient(ctx, authority, roots[client], checkout, publication, found, parserBundleDigest)
		if err != nil {
			return nil, err
		}
		if client == uciInstalledAcceptanceClientA {
			primarySource = source
		}
	}
	if len(primarySource) == 0 {
		return nil, errors.New("installed parser canary primary request is unavailable")
	}
	return primarySource, nil
}

func uciRequireInstalledAcceptanceParserCanaryForClient(ctx context.Context, authority *uciInstalledAcceptanceAuthority, root string, checkout *gormdb.UCICheckout, publication uciInstalledAcceptancePublication, found bool, parserBundleDigest string) ([]byte, error) {
	if checkout == nil || !found || publication.sourceID != authority.source.SourceID || publication.checkoutID != checkout.CheckoutID || publication.profileID != authority.profile.ProfileID || publication.viewID == "" {
		return nil, errors.New("installed parser canary publication identity is incomplete")
	}
	source, err := os.ReadFile(filepath.Join(root, uciInstalledAcceptanceParserCanaryRelativePath))
	if err != nil || strings.ReplaceAll(string(source), "\r\n", "\n") != uciInstalledAcceptanceParserCanarySource {
		return nil, errors.New("installed parser canary worktree bytes are invalid")
	}
	contentDigest := uciInstalledAcceptanceSHA256Prefix + uciInstalledAcceptanceStringDigest(string(source))
	var count int64
	if err := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT COUNT(*)
		FROM ci_views AS view
		JOIN ci_memberships AS membership
		  ON membership.checkout_id = view.checkout_id
		 AND membership.valid_from_generation <= view.generation
		 AND (membership.valid_to_generation IS NULL OR membership.valid_to_generation > view.generation)
		JOIN ci_parse_artifacts AS artifact ON artifact.artifact_id = membership.artifact_id
		JOIN ci_blobs AS blob ON blob.source_id = artifact.source_id AND blob.blob_id = artifact.blob_id
		WHERE view.view_id = ?
		  AND view.source_id = ?
		  AND view.checkout_id = ?
		  AND membership.path_key = ?
		  AND membership.file_state = 'present'
		  AND artifact.language = ?
		  AND artifact.status = 'complete'
		  AND artifact.grammar_digest = ?
		  AND artifact.extraction_profile_digest = ?
		  AND blob.content_digest = ?`,
		publication.viewID,
		authority.source.SourceID,
		checkout.CheckoutID,
		uciInstalledAcceptanceParserCanaryRelativePath,
		string(uci.TreeSitterLanguageTypeScript),
		parserBundleDigest,
		parserBundleDigest,
		contentDigest,
	).Scan(&count).Error; err != nil {
		return nil, fmt.Errorf("inspect installed parser canary publication: %w", err)
	}
	if count != 1 {
		return nil, errors.New("installed parser canary is not published exactly once in the selected View")
	}
	return append([]byte(nil), source...), nil
}

func uciInstalledAcceptanceBareSHA256(value string) (string, error) {
	const prefix = uciInstalledAcceptanceSHA256Prefix
	if !strings.HasPrefix(value, prefix) {
		return "", errors.New("digest has no SHA-256 prefix")
	}
	bare := strings.TrimPrefix(value, prefix)
	if len(bare) != sha256.Size*2 {
		return "", errors.New("digest has an invalid SHA-256 length")
	}
	if _, err := hex.DecodeString(bare); err != nil {
		return "", fmt.Errorf("decode SHA-256 digest: %w", err)
	}
	return bare, nil
}

func uciInstalledAcceptanceStringDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

const (
	uciInstalledAcceptanceBarrierWaitMS          int64 = 5_000
	uciInstalledAcceptanceQuiescenceObservations       = 15
	uciInstalledAcceptanceQuiescencePollInterval       = 100 * time.Millisecond
)

type uciInstalledAcceptanceStatus struct {
	status string
	runID  string
	error  string

	context *struct {
		sourceID   string
		checkoutID string
		viewID     string
		profileID  string
		generation int64
	}
	freshness *struct {
		state          string
		pendingChanges *int64
		barrier        *struct {
			scope struct {
				kind      string
				pathCount int64
			}
			deadlineMS int64
			state      string
		}
	}
	evidenceRecorder struct {
		state           string
		lastFailureCode string
	}
}

func uciDecodeInstalledAcceptanceStatus(payload json.RawMessage) (uciInstalledAcceptanceStatus, error) {
	var wire struct {
		Status  string `json:"status"`
		RunID   string `json:"run_id"`
		Error   string `json:"error"`
		Context *struct {
			SourceID   string `json:"source_id"`
			CheckoutID string `json:"checkout_id"`
			ViewID     string `json:"view_id"`
			ProfileID  string `json:"profile_id"`
			Generation int64  `json:"generation"`
		} `json:"context"`
		Freshness *struct {
			State          string `json:"state"`
			PendingChanges *int64 `json:"pending_changes"`
			Barrier        *struct {
				Scope struct {
					Kind      string `json:"kind"`
					PathCount int64  `json:"path_count"`
				} `json:"scope"`
				DeadlineMS int64  `json:"deadline_ms"`
				State      string `json:"state"`
			} `json:"barrier"`
		} `json:"freshness"`
		EvidenceRecorder struct {
			State           string `json:"state"`
			LastFailureCode string `json:"last_failure_code"`
		} `json:"evidence_recorder"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return uciInstalledAcceptanceStatus{}, errors.New("installed standard MCP codebase_status response is invalid")
	}
	status := uciInstalledAcceptanceStatus{
		status: wire.Status,
		runID:  wire.RunID,
		error:  wire.Error,
		evidenceRecorder: struct {
			state           string
			lastFailureCode string
		}{state: wire.EvidenceRecorder.State, lastFailureCode: wire.EvidenceRecorder.LastFailureCode},
	}
	if wire.Context != nil {
		status.context = &struct {
			sourceID   string
			checkoutID string
			viewID     string
			profileID  string
			generation int64
		}{
			sourceID:   wire.Context.SourceID,
			checkoutID: wire.Context.CheckoutID,
			viewID:     wire.Context.ViewID,
			profileID:  wire.Context.ProfileID,
			generation: wire.Context.Generation,
		}
	}
	if wire.Freshness != nil {
		status.freshness = &struct {
			state          string
			pendingChanges *int64
			barrier        *struct {
				scope struct {
					kind      string
					pathCount int64
				}
				deadlineMS int64
				state      string
			}
		}{state: wire.Freshness.State, pendingChanges: wire.Freshness.PendingChanges}
		if wire.Freshness.Barrier != nil {
			status.freshness.barrier = &struct {
				scope struct {
					kind      string
					pathCount int64
				}
				deadlineMS int64
				state      string
			}{deadlineMS: wire.Freshness.Barrier.DeadlineMS, state: wire.Freshness.Barrier.State}
			status.freshness.barrier.scope.kind = wire.Freshness.Barrier.Scope.Kind
			status.freshness.barrier.scope.pathCount = wire.Freshness.Barrier.Scope.PathCount
		}
	}
	return status, nil
}

func uciInstalledAcceptanceStatusPublication(status uciInstalledAcceptanceStatus, selection uciInstalledAcceptanceSelection) (uciInstalledAcceptancePublication, error) {
	if selection.runID == "" || status.runID != selection.runID {
		return uciInstalledAcceptancePublication{}, errors.New("installed standard MCP status did not retain the target index run_id")
	}
	if status.context == nil || status.context.sourceID == "" || status.context.checkoutID == "" || status.context.viewID == "" || status.context.profileID == "" || status.context.generation < 1 {
		return uciInstalledAcceptancePublication{}, errors.New("installed standard MCP status has no complete server context")
	}
	if status.freshness == nil || status.freshness.state != "observed_current" {
		return uciInstalledAcceptancePublication{}, errors.New("installed standard MCP status lacks observed-current freshness")
	}
	return uciInstalledAcceptancePublication{
		sourceID:         status.context.sourceID,
		checkoutID:       status.context.checkoutID,
		viewID:           status.context.viewID,
		profileID:        status.context.profileID,
		generation:       status.context.generation,
		runID:            status.runID,
		freshnessState:   status.freshness.state,
		barrierState:     "",
		evidenceRecorder: status.evidenceRecorder.state,
	}, nil
}

func uciInstalledAcceptanceQuiescentPublication(status uciInstalledAcceptanceStatus, barrier uciInstalledAcceptancePublication) (uciInstalledAcceptancePublication, error) {
	if status.status != "idle" || status.error != "" || status.freshness == nil || status.freshness.pendingChanges == nil || *status.freshness.pendingChanges != 0 {
		return uciInstalledAcceptancePublication{}, errors.New("installed standard MCP status is not a clean zero-pending idle observation")
	}
	if status.runID == "" || status.context == nil || status.context.sourceID != barrier.sourceID || status.context.checkoutID != barrier.checkoutID || status.context.profileID != barrier.profileID {
		return uciInstalledAcceptancePublication{}, errors.New("installed standard MCP status advanced outside the selected source, checkout, or profile")
	}
	publication, err := uciInstalledAcceptanceStatusPublication(status, uciInstalledAcceptanceSelection{runID: status.runID})
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	publication.barrierState = barrier.barrierState
	return publication, nil
}

func uciInstalledAcceptanceSamePublicationTuple(left, right uciInstalledAcceptancePublication) bool {
	return left.runID == right.runID && left.viewID == right.viewID && left.generation == right.generation
}

// uciWaitForInstalledAcceptanceQuiescence freezes evidence only after a settled
// ordinary status stream. The initial after_barrier receipt proves its exact run;
// a queued watcher run may publish a newer View before this quiet checkpoint.
func uciWaitForInstalledAcceptanceQuiescence(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, barrier uciInstalledAcceptancePublication) (uciInstalledAcceptancePublication, error) {
	if client == nil || selection.contextHandle == "" || barrier.barrierState != "satisfied" {
		return uciInstalledAcceptancePublication{}, errors.New("installed standard MCP quiescence target is incomplete")
	}
	var candidate uciInstalledAcceptancePublication
	observations := 0
	ticker := time.NewTicker(uciInstalledAcceptanceQuiescencePollInterval)
	defer ticker.Stop()
	for {
		payload, err := uciInstalledAcceptanceStatusTool(ctx, client, map[string]any{"context_handle": selection.contextHandle})
		if err != nil {
			return uciInstalledAcceptancePublication{}, err
		}
		status, err := uciDecodeInstalledAcceptanceStatus(payload)
		if err != nil {
			return uciInstalledAcceptancePublication{}, err
		}
		if status.error != "" {
			return uciInstalledAcceptancePublication{}, fmt.Errorf("installed standard MCP quiescence status error: %s", uciInstalledAcceptanceSafeErrorDetail(status.error))
		}
		var ready bool
		candidate, observations, ready, err = uciInstalledAcceptanceObserveQuiescence(status, barrier, candidate, observations)
		if err != nil {
			return uciInstalledAcceptancePublication{}, err
		}
		if ready {
			return candidate, nil
		}
		select {
		case <-ctx.Done():
			return uciInstalledAcceptancePublication{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func uciInstalledAcceptanceObserveQuiescence(status uciInstalledAcceptanceStatus, barrier, candidate uciInstalledAcceptancePublication, observations int) (uciInstalledAcceptancePublication, int, bool, error) {
	if status.status == "running" {
		return uciInstalledAcceptancePublication{}, 0, false, nil
	}
	publication, err := uciInstalledAcceptanceQuiescentPublication(status, barrier)
	if err != nil {
		return uciInstalledAcceptancePublication{}, 0, false, err
	}
	if observations == 0 || !uciInstalledAcceptanceSamePublicationTuple(candidate, publication) {
		candidate, observations = publication, 1
	} else {
		observations++
	}
	return candidate, observations, observations >= uciInstalledAcceptanceQuiescenceObservations, nil
}

func uciInstalledAcceptanceBarrierWait(ctx context.Context) int64 {
	waitMS := uciInstalledAcceptanceBarrierWaitMS
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline).Milliseconds()
		if remaining < 1 {
			return 1
		}
		if remaining < waitMS {
			waitMS = remaining
		}
	}
	return waitMS
}

func uciWaitForInstalledAcceptanceBarrier(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection) (uciInstalledAcceptancePublication, error) {
	if selection.contextHandle == "" || selection.runID == "" {
		return uciInstalledAcceptancePublication{}, errors.New("installed standard MCP barrier target has no context handle or run_id")
	}
	for {
		payload, err := uciInstalledAcceptanceStatusTool(ctx, client, map[string]any{
			"context_handle": selection.contextHandle,
			"after_barrier": map[string]any{
				"token":   selection.runID,
				"wait_ms": uciInstalledAcceptanceBarrierWait(ctx),
			},
		})
		if err != nil {
			statusPayload, statusErr := uciInstalledAcceptanceStatusTool(ctx, client, map[string]any{"context_handle": selection.contextHandle})
			if statusErr == nil {
				if status, decodeErr := uciDecodeInstalledAcceptanceStatus(statusPayload); decodeErr == nil {
					if detail := uciInstalledAcceptanceSafeErrorDetail(status.error); detail != "" {
						return uciInstalledAcceptancePublication{}, fmt.Errorf("%w: %s", err, detail)
					}
				}
			}
			return uciInstalledAcceptancePublication{}, err
		}
		status, err := uciDecodeInstalledAcceptanceStatus(payload)
		if err != nil {
			return uciInstalledAcceptancePublication{}, err
		}
		publication, retry, err := uciInstalledAcceptanceBarrierResult(status, selection)
		if err != nil {
			return uciInstalledAcceptancePublication{}, err
		}
		if retry {
			if err := ctx.Err(); err != nil {
				return uciInstalledAcceptancePublication{}, err
			}
			continue
		}
		return publication, nil
	}
}

func uciInstalledAcceptanceBarrierResult(status uciInstalledAcceptanceStatus, selection uciInstalledAcceptanceSelection) (uciInstalledAcceptancePublication, bool, error) {
	if detail := uciInstalledAcceptanceSafeErrorDetail(status.error); detail != "" {
		return uciInstalledAcceptancePublication{}, false, fmt.Errorf("installed standard MCP after_barrier status error: %s", detail)
	}
	if status.status == "running" && status.runID == selection.runID {
		return uciInstalledAcceptancePublication{}, true, nil
	}
	publication, err := uciInstalledAcceptanceStatusPublication(status, selection)
	if err != nil {
		return uciInstalledAcceptancePublication{}, false, err
	}
	if status.status != "idle" || status.freshness == nil || status.freshness.state != "observed_current" || status.freshness.barrier == nil ||
		status.freshness.barrier.state != "satisfied" || status.freshness.barrier.deadlineMS < 1 ||
		(status.freshness.barrier.scope.kind != "paths" && status.freshness.barrier.scope.kind != "paths_with_hashes") || status.freshness.barrier.scope.pathCount < 1 {
		return uciInstalledAcceptancePublication{}, false, errors.New("installed standard MCP after_barrier did not return satisfied target freshness")
	}
	publication.freshnessState = status.freshness.state
	publication.barrierState = status.freshness.barrier.state
	publication.evidenceRecorder = status.evidenceRecorder.state
	return publication, false, nil
}

func uciDecodeInstalledAcceptanceQuery(payload json.RawMessage) (uci.QueryResponse, error) {
	var response uci.QueryResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return uci.QueryResponse{}, errors.New("installed standard MCP query response is invalid")
	}
	if err := response.Validate(); err != nil {
		return uci.QueryResponse{}, errors.New("installed standard MCP query response violates its public contract")
	}
	return response, nil
}

func uciInstalledAcceptanceQueryMatchesPublication(response uci.QueryResponse, publication uciInstalledAcceptancePublication) bool {
	if response.Contexts == nil || len(*response.Contexts) != 1 {
		return false
	}
	context := (*response.Contexts)[0]
	return context.SourceID == publication.sourceID && context.CheckoutID == publication.checkoutID &&
		context.ViewID == publication.viewID && context.ProfileID == publication.profileID && context.Generation == publication.generation
}

func uciDecodeInstalledAcceptanceClosedOutcome(payload json.RawMessage) (uciInstalledAcceptanceClosedOutcome, error) {
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil {
		return uciInstalledAcceptanceClosedOutcome{}, err
	}
	if response.Error == nil || response.Contexts != nil || response.Items != nil || response.Graph != nil || response.Exposure != nil {
		return uciInstalledAcceptanceClosedOutcome{}, fmt.Errorf("installed standard MCP closed response disclosed contextual data: status=%s error=%t contexts=%t items=%t graph=%t exposure=%t %s", response.Status, response.Error != nil, response.Contexts != nil, response.Items != nil, response.Graph != nil, response.Exposure != nil, uciInstalledAcceptancePayloadShape(payload))
	}
	return uciInstalledAcceptanceClosedOutcome{
		Status:    string(response.Status),
		ErrorCode: string(response.Error.Code),
	}, nil
}

func uciObserveInstalledAcceptanceSearchGraphRead(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication, fixture uciInstalledAcceptanceFixture, expectedCallee string) (uciInstalledAcceptanceObservations, error) {
	searchItem, err := uciInstalledAcceptanceSearchCitation(ctx, client, selection, publication, fixture)
	if err != nil {
		return uciInstalledAcceptanceObservations{}, err
	}
	callee, err := uciInstalledAcceptanceGraphCallee(ctx, client, selection, publication, searchItem, expectedCallee)
	if err != nil {
		return uciInstalledAcceptanceObservations{}, err
	}
	readItem, err := uciInstalledAcceptanceReadCitation(ctx, client, selection, publication, searchItem)
	if err != nil {
		return uciInstalledAcceptanceObservations{}, err
	}
	return uciInstalledAcceptanceObservations{
		SearchArtifactDigests: []string{string(searchItem.ContentDigest)},
		GraphCalleeDigests:    []string{uciInstalledAcceptanceStringDigest(callee)},
		ReadArtifactDigests:   []string{string(readItem.ContentDigest)},
	}, nil
}

func uciInstalledAcceptanceSearchCitation(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication, fixture uciInstalledAcceptanceFixture) (uci.QueryItem, error) {
	searchPayload, err := client.Tool(ctx, "codebase_search", map[string]any{
		"context_handle": selection.contextHandle,
		"query":          fixture.SharedSymbol,
		"path_prefix":    "pkg",
		"limit":          10,
	})
	if err != nil {
		return uci.QueryItem{}, err
	}
	search, err := uciDecodeInstalledAcceptanceQuery(searchPayload)
	if err != nil {
		return uci.QueryItem{}, err
	}
	if (search.Status != uci.QueryStatusOK && search.Status != uci.QueryStatusPartial) || !uciInstalledAcceptanceQueryMatchesPublication(search, publication) || search.Items == nil {
		return uci.QueryItem{}, fmt.Errorf("installed standard MCP search result is not selected-View evidence: status=%s", search.Status)
	}
	return uciInstalledAcceptanceFindSearchCitation(*search.Items, publication, fixture)
}

func uciInstalledAcceptanceFindSearchCitation(items uci.QueryItems, publication uciInstalledAcceptancePublication, fixture uciInstalledAcceptanceFixture) (uci.QueryItem, error) {
	var searchItem *uci.QueryItem
	for index := range items {
		item := &items[index]
		if err := uciInstalledAcceptanceValidateSearchCitationPublication(*item, publication); err != nil {
			return uci.QueryItem{}, err
		}
		if !uciInstalledAcceptanceMatchesSearchCitation(*item, fixture) {
			continue
		}
		if err := uciInstalledAcceptanceSelectSearchCitation(&searchItem, item); err != nil {
			return uci.QueryItem{}, err
		}
	}
	if searchItem == nil {
		return uci.QueryItem{}, errors.New("installed standard MCP search omitted the qualified SharedTarget citation")
	}
	if err := uciInstalledAcceptanceValidateSearchCitationEntity(*searchItem, fixture); err != nil {
		return uci.QueryItem{}, err
	}
	return *searchItem, nil
}

func uciInstalledAcceptanceValidateSearchCitationPublication(item uci.QueryItem, publication uciInstalledAcceptancePublication) error {
	if item.Ref.SourceID != publication.sourceID || item.Ref.ViewID != publication.viewID {
		return errors.New("installed standard MCP search disclosed an item outside the selected View")
	}
	return nil
}

func uciInstalledAcceptanceMatchesSearchCitation(item uci.QueryItem, fixture uciInstalledAcceptanceFixture) bool {
	caller, callerOK := uciInstalledAcceptanceGoFunctionName(item.Ref.EntityKey)
	return item.Path == fixture.RelativePath && callerOK && caller == fixture.SharedSymbol
}

func uciInstalledAcceptanceSelectSearchCitation(current **uci.QueryItem, candidate *uci.QueryItem) error {
	if !uciInstalledAcceptanceIsBareSHA256(string(candidate.ContentDigest)) {
		return errors.New("installed standard MCP search citation has an invalid content digest")
	}
	if *current != nil && (*current).ContentDigest != candidate.ContentDigest {
		return errors.New("installed standard MCP search returned conflicting SharedTarget artifacts")
	}
	if *current == nil || candidate.Span.ByteEnd-candidate.Span.ByteStart < (*current).Span.ByteEnd-(*current).Span.ByteStart {
		*current = candidate
	}
	return nil
}

func uciInstalledAcceptanceValidateSearchCitationEntity(item uci.QueryItem, fixture uciInstalledAcceptanceFixture) error {
	caller, callerOK := uciInstalledAcceptanceGoFunctionName(item.Ref.EntityKey)
	if !callerOK || caller != fixture.SharedSymbol {
		return errors.New("installed standard MCP search did not cite the qualified SharedTarget entity")
	}
	return nil
}

func uciInstalledAcceptanceGraphCallee(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication, searchItem uci.QueryItem, expectedCallee string) (string, error) {
	graphPayload, err := client.Tool(ctx, "codebase_graph", map[string]any{
		"context_handle": selection.contextHandle,
		"action":         "neighbors",
		"target": map[string]any{
			"source_id":  publication.sourceID,
			"view_id":    publication.viewID,
			"entity_key": searchItem.Ref.EntityKey,
		},
		"direction": "outgoing", "max_depth": 4, "max_visited": 64, "max_nodes": 32, "max_edges": 64, "deadline_ms": 30_000,
	})
	if err != nil {
		return "", err
	}
	graphResponse, err := uciDecodeInstalledAcceptanceQuery(graphPayload)
	if err != nil {
		return "", err
	}
	if graphResponse.Status != uci.QueryStatusOK || !uciInstalledAcceptanceQueryMatchesPublication(graphResponse, publication) || graphResponse.Graph == nil {
		return "", errors.New("installed standard MCP graph did not return the selected View")
	}
	callee := ""
	for _, edge := range graphResponse.Graph.Edges {
		if string(edge.Relation) != "calls" || edge.From.SourceID != publication.sourceID || edge.From.ViewID != publication.viewID || edge.From.EntityKey != searchItem.Ref.EntityKey {
			continue
		}
		if edge.To.SourceID != publication.sourceID || edge.To.ViewID != publication.viewID {
			return "", errors.New("installed standard MCP graph calls edge left the selected View")
		}
		calleeName, calleeOK := uciInstalledAcceptanceGoFunctionName(edge.To.EntityKey)
		if !calleeOK || calleeName != expectedCallee {
			return "", errors.New("installed standard MCP graph calls edge did not resolve the expected fixture callee")
		}
		if callee != "" {
			return "", errors.New("installed standard MCP graph returned more than one SharedTarget calls edge")
		}
		callee = calleeName
	}
	if callee == "" {
		return "", errors.New("installed standard MCP graph omitted the SharedTarget calls edge")
	}
	return callee, nil
}

func uciInstalledAcceptanceReadCitation(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication, searchItem uci.QueryItem) (uci.QueryItem, error) {
	readPayload, err := client.Tool(ctx, "codebase_read", map[string]any{
		"context_handle": selection.contextHandle,
		"ref": map[string]any{
			"source_id": searchItem.Ref.SourceID, "view_id": searchItem.Ref.ViewID, "entity_key": searchItem.Ref.EntityKey,
		},
		"span": map[string]any{
			"byte_start": searchItem.Span.ByteStart, "byte_end": searchItem.Span.ByteEnd, "line_start": searchItem.Span.LineStart, "line_end": searchItem.Span.LineEnd,
		},
		"content_digest": string(searchItem.ContentDigest), "verify_working_copy": false, "max_bytes": 8_192,
	})
	if err != nil {
		return uci.QueryItem{}, err
	}
	read, err := uciDecodeInstalledAcceptanceQuery(readPayload)
	if err != nil {
		return uci.QueryItem{}, err
	}
	if read.Status != uci.QueryStatusOK || !uciInstalledAcceptanceQueryMatchesPublication(read, publication) || read.Items == nil || len(*read.Items) != 1 {
		return uci.QueryItem{}, errors.New("installed standard MCP read did not return one exact selected-View artifact")
	}
	readItem := (*read.Items)[0]
	if readItem.Ref != searchItem.Ref || readItem.Span != searchItem.Span || readItem.ContentDigest != searchItem.ContentDigest {
		return uci.QueryItem{}, errors.New("installed standard MCP read did not preserve the search citation")
	}
	return readItem, nil
}

func uciInstalledAcceptanceGoFunctionName(entityKey string) (string, bool) {
	const marker = "/func:"
	index := strings.LastIndex(entityKey, marker)
	if !strings.HasPrefix(entityKey, "go:") || index <= len("go:") {
		return "", false
	}
	name := entityKey[index+len(marker):]
	if name == "" || strings.TrimSpace(name) != name || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

func uciInstalledAcceptanceDefaultView(ctx context.Context, client *uciInstalledAcceptanceMCPClient) (string, error) {
	payload, err := client.Tool(ctx, "codebase_context", map[string]any{"action": "resolve"})
	if err != nil {
		return "", err
	}
	var response struct {
		BindingKind string `json:"binding_kind"`
		ViewID      string `json:"view_id"`
		Context     *struct {
			ViewID string `json:"view_id"`
		} `json:"context"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || response.BindingKind != "checkout" {
		return "", errors.New("installed standard MCP context resolve response is invalid")
	}
	if response.Context != nil && response.Context.ViewID != "" {
		response.ViewID = response.Context.ViewID
	}
	if response.ViewID == "" {
		return "", errors.New("installed standard MCP context resolve has no selected View")
	}
	return response.ViewID, nil
}

func uciSelectInstalledAcceptanceCheckout(ctx context.Context, client *uciInstalledAcceptanceMCPClient, clientName string, authority *uciInstalledAcceptanceAuthority) (uciInstalledAcceptanceSelection, error) {
	if authority == nil || authority.source == nil || authority.profile == nil {
		return uciInstalledAcceptanceSelection{}, errors.New("installed acceptance UCI authority is incomplete")
	}
	checkout := authority.checkouts[clientName]
	if checkout == nil {
		return uciInstalledAcceptanceSelection{}, errors.New("installed acceptance checkout is unavailable")
	}
	payload, err := client.Tool(ctx, "codebase_context", map[string]any{
		"action": "select",
		"checkout": map[string]any{
			"source_id":           authority.source.SourceID,
			"checkout_id":         checkout.CheckoutID,
			"incarnation_id":      checkout.IncarnationID,
			"analysis_profile_id": authority.profile.ProfileID,
		},
	})
	if err != nil {
		return uciInstalledAcceptanceSelection{}, err
	}
	var response struct {
		ContextHandle string `json:"context_handle"`
		BindingKind   string `json:"binding_kind"`
		ViewID        string `json:"view_id"`
		Context       *struct {
			ViewID string `json:"view_id"`
		} `json:"context"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || response.ContextHandle == "" || response.BindingKind != "checkout" {
		return uciInstalledAcceptanceSelection{}, errors.New("installed standard MCP checkout selection response is invalid")
	}
	if response.Context != nil && response.Context.ViewID != "" {
		response.ViewID = response.Context.ViewID
	}
	return uciInstalledAcceptanceSelection{contextHandle: response.ContextHandle, viewID: response.ViewID}, nil
}

func uciExerciseInstalledAcceptanceThirdClient(ctx context.Context, first, second, third *uciInstalledAcceptanceMCPClient, authority *uciInstalledAcceptanceAuthority, result *uciInstalledAcceptanceResult) error {
	for _, item := range []struct {
		name   string
		client *uciInstalledAcceptanceMCPClient
	}{
		{uciInstalledAcceptanceClientA, first},
		{uciInstalledAcceptanceClientB, second},
	} {
		viewID, err := uciInstalledAcceptanceDefaultView(ctx, item.client)
		if err != nil {
			return err
		}
		result.Defaults.BeforeThirdClientViewDigests[item.name] = uciInstalledAcceptanceStringDigest(viewID)
		if contextRef, found := result.ClientContexts[item.name]; !found || result.Defaults.BeforeThirdClientViewDigests[item.name] != contextRef.ViewDigest {
			return errors.New("installed standard MCP default advanced before the third client connected")
		}
	}

	unbound, err := third.ToolWithCall(ctx, "codebase_search", map[string]any{"query": "unbound-context-probe", "limit": 1})
	if err != nil {
		return err
	}
	if unbound.isError {
		return errors.New("unbound installed standard MCP search returned a protocol-level tool error")
	}
	result.Bootstrap.UnboundClient, err = uciDecodeInstalledAcceptanceClosedOutcome(unbound.payload)
	if err != nil {
		return err
	}
	if result.Bootstrap.UnboundClient.Status != "context_required" || result.Bootstrap.UnboundClient.ErrorCode != "CONTEXT_REQUIRED" {
		return errors.New("unbound installed standard MCP client did not return CONTEXT_REQUIRED")
	}

	selected, err := uciSelectInstalledAcceptanceCheckout(ctx, third, uciInstalledAcceptanceClientA, authority)
	if err != nil {
		return err
	}
	if selected.viewID == "" {
		return errors.New("third installed standard MCP client selection has no published View")
	}
	if primary, found := result.ClientContexts[uciInstalledAcceptanceClientA]; !found || uciInstalledAcceptanceStringDigest(selected.viewID) != primary.ViewDigest {
		return errors.New("third installed standard MCP client did not select its requested checkout View")
	}

	for _, item := range []struct {
		name   string
		client *uciInstalledAcceptanceMCPClient
	}{
		{uciInstalledAcceptanceClientA, first},
		{uciInstalledAcceptanceClientB, second},
	} {
		viewID, err := uciInstalledAcceptanceDefaultView(ctx, item.client)
		if err != nil {
			return err
		}
		result.Defaults.AfterThirdClientViewDigests[item.name] = uciInstalledAcceptanceStringDigest(viewID)
	}
	return nil
}

func uciInstalledAcceptanceIsBareSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func uciExerciseInstalledAcceptanceRefusals(ctx context.Context, input uciInstalledAcceptanceRefusalInput) error {
	first, second, third := input.first, input.second, input.third
	firstSelection, secondSelection := input.firstSelection, input.secondSelection
	authority, result := input.authority, input.result
	if first == nil || second == nil || third == nil || result == nil {
		return errors.New("installed acceptance authorization-negative matrix is incomplete")
	}

	run := func(name string, client *uciInstalledAcceptanceMCPClient, handle, query, wantStatus, wantCode string) error {
		if client == nil || handle == "" {
			return errors.New("installed acceptance authorization-negative client context is incomplete")
		}
		before, err := uciInstalledAcceptanceExposureCount(ctx, authority)
		if err != nil {
			return err
		}
		toolResult, err := client.ToolWithCall(ctx, "codebase_search", uciInstalledAcceptanceSearchArguments(handle, query))
		if err != nil {
			return err
		}
		if toolResult.isError {
			return errors.New("installed standard MCP authorization refusal returned a protocol-level tool error")
		}
		outcome, err := uciDecodeInstalledAcceptanceClosedOutcome(toolResult.payload)
		if err != nil {
			return err
		}
		if outcome.Status != wantStatus || outcome.ErrorCode != wantCode {
			return fmt.Errorf("installed standard MCP %s refusal = %s/%s", name, outcome.Status, outcome.ErrorCode)
		}
		after, err := uciInstalledAcceptanceExposureCount(ctx, authority)
		if err != nil {
			return err
		}
		if after != before {
			return errors.New("installed standard MCP refusal appended UCI exposure evidence")
		}
		result.Refusals[name] = outcome
		return nil
	}

	deniedOwner := "agent/uci-installed-denied-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := uciWithInstalledAcceptanceCheckoutOwner(ctx, authority, uciInstalledAcceptanceClientA, deniedOwner, func() error {
		return run("denied", first, firstSelection.contextHandle, "denied-owner", "forbidden", "PERMISSION_DENIED")
	}); err != nil {
		return err
	}

	revokedPrincipal := "agent/uci-installed-revoked-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := uciWithInstalledAcceptanceTokenPrincipal(ctx, authority, revokedPrincipal, func() error {
		return run("revoked", first, firstSelection.contextHandle, "revoked-source", "forbidden", "PERMISSION_DENIED")
	}); err != nil {
		return err
	}

	if err := run("mismatched", third, firstSelection.contextHandle, "another-session-handle", "context_required", "CONTEXT_MISMATCH"); err != nil {
		return err
	}
	if err := run("private", first, secondSelection.contextHandle, "private-client-local-handle", "context_required", "CONTEXT_MISMATCH"); err != nil {
		return err
	}
	return nil
}

func uciWithInstalledAcceptanceCheckoutOwner(
	ctx context.Context,
	authority *uciInstalledAcceptanceAuthority,
	clientName, replacement string,
	action func() error,
) (retErr error) {
	if authority == nil || authority.store == nil || action == nil || replacement == "" {
		return errors.New("installed acceptance checkout-owner mutation is incomplete")
	}
	checkout := authority.checkouts[clientName]
	if checkout == nil || checkout.CheckoutID == "" {
		return errors.New("installed acceptance checkout-owner mutation has no checkout")
	}
	db := authority.store.GetDB()
	var row struct {
		OwnerPrincipal string `gorm:"column:owner_principal"`
	}
	loaded := db.WithContext(ctx).Raw(`SELECT owner_principal FROM ci_checkouts WHERE checkout_id = ?`, checkout.CheckoutID).Scan(&row)
	if loaded.Error != nil || loaded.RowsAffected != 1 || row.OwnerPrincipal == "" || row.OwnerPrincipal == replacement {
		return errors.New("load installed acceptance checkout owner")
	}
	updated := db.WithContext(ctx).Exec(`UPDATE ci_checkouts SET owner_principal = ? WHERE checkout_id = ? AND owner_principal = ?`, replacement, checkout.CheckoutID, row.OwnerPrincipal)
	if updated.Error != nil || updated.RowsAffected != 1 {
		return errors.New("mutate installed acceptance checkout owner")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		restored := db.WithContext(cleanupCtx).Exec(`UPDATE ci_checkouts SET owner_principal = ? WHERE checkout_id = ? AND owner_principal = ?`, row.OwnerPrincipal, checkout.CheckoutID, replacement)
		if restored.Error != nil || restored.RowsAffected != 1 {
			retErr = errors.Join(retErr, errors.New("restore installed acceptance checkout owner"))
		}
	}()
	return action()
}

func uciWithInstalledAcceptanceTokenPrincipal(
	ctx context.Context,
	authority *uciInstalledAcceptanceAuthority,
	replacement string,
	action func() error,
) (retErr error) {
	if authority == nil || authority.store == nil || authority.token == nil || authority.token.ID == "" || action == nil || replacement == "" {
		return errors.New("installed acceptance source-revocation mutation is incomplete")
	}
	db := authority.store.GetDB()
	var row struct {
		Principal string `gorm:"column:principal"`
	}
	loaded := db.WithContext(ctx).Raw(`SELECT principal FROM api_tokens WHERE id = ?`, authority.token.ID).Scan(&row)
	if loaded.Error != nil || loaded.RowsAffected != 1 || row.Principal == "" || row.Principal == replacement {
		return errors.New("load installed acceptance source principal")
	}
	updated := db.WithContext(ctx).Exec(`UPDATE api_tokens SET principal = ? WHERE id = ? AND principal = ?`, replacement, authority.token.ID, row.Principal)
	if updated.Error != nil || updated.RowsAffected != 1 {
		return errors.New("revoke installed acceptance source principal")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		restored := db.WithContext(cleanupCtx).Exec(`UPDATE api_tokens SET principal = ? WHERE id = ? AND principal = ?`, row.Principal, authority.token.ID, replacement)
		if restored.Error != nil || restored.RowsAffected != 1 {
			retErr = errors.Join(retErr, errors.New("restore installed acceptance source principal"))
		}
	}()
	return action()
}

func uciInstalledAcceptanceSearchArguments(handle, query string) map[string]any {
	return map[string]any{
		"context_handle": handle,
		"query":          query,
		"path_prefix":    "pkg",
		"limit":          1,
	}
}

func uciInstalledAcceptanceExposureCount(ctx context.Context, authority *uciInstalledAcceptanceAuthority) (int64, error) {
	if authority == nil || authority.store == nil || authority.source == nil || authority.source.SourceID == "" {
		return 0, errors.New("installed acceptance exposure count authority is incomplete")
	}
	var count int64
	if err := authority.store.GetDB().WithContext(ctx).Raw(`SELECT COUNT(*) FROM uci_exposures WHERE source_id = ?`, authority.source.SourceID).Scan(&count).Error; err != nil {
		return 0, fmt.Errorf("count installed acceptance UCI exposures: %w", err)
	}
	return count, nil
}

func uciInstalledAcceptanceStatusForSelection(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection) (uciInstalledAcceptanceStatus, error) {
	if client == nil || selection.contextHandle == "" {
		return uciInstalledAcceptanceStatus{}, errors.New("installed acceptance status target is incomplete")
	}
	payload, err := uciInstalledAcceptanceStatusTool(ctx, client, map[string]any{"context_handle": selection.contextHandle})
	if err != nil {
		return uciInstalledAcceptanceStatus{}, err
	}
	return uciDecodeInstalledAcceptanceStatus(payload)
}

type uciInstalledAcceptanceExposureFault struct {
	authority    *uciInstalledAcceptanceAuthority
	functionName string
	triggerName  string
	closed       bool
}

func uciInstallInstalledAcceptanceExposureFault(ctx context.Context, authority *uciInstalledAcceptanceAuthority) (*uciInstalledAcceptanceExposureFault, error) {
	if authority == nil || authority.store == nil || !uciInstalledAcceptanceRunSchema(authority.schema) {
		return nil, errors.New("installed acceptance exposure fault authority is incomplete")
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	fault := &uciInstalledAcceptanceExposureFault{
		authority:    authority,
		functionName: "uci_exp_fail_" + suffix,
		triggerName:  "uci_exp_fail_tr_" + suffix,
	}
	schema := pq.QuoteIdentifier(authority.schema)
	qualifiedFunction := schema + "." + pq.QuoteIdentifier(fault.functionName)
	qualifiedTable := schema + "." + pq.QuoteIdentifier("uci_exposures")
	db := authority.store.GetDB().WithContext(ctx)
	if err := db.Exec(`CREATE FUNCTION ` + qualifiedFunction + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'installed UCI exposure append fault'; END; $$`).Error; err != nil {
		return nil, fmt.Errorf("install installed acceptance exposure fault function: %w", err)
	}
	if err := db.Exec(`CREATE TRIGGER ` + pq.QuoteIdentifier(fault.triggerName) + ` BEFORE INSERT ON ` + qualifiedTable + ` FOR EACH ROW EXECUTE FUNCTION ` + qualifiedFunction + `()`).Error; err != nil {
		return nil, errors.Join(fmt.Errorf("install installed acceptance exposure fault trigger: %w", err), fault.Close())
	}
	return fault, nil
}

func (fault *uciInstalledAcceptanceExposureFault) Close() error {
	if fault == nil || fault.closed {
		return nil
	}
	if fault.authority == nil || fault.authority.store == nil || !uciInstalledAcceptanceRunSchema(fault.authority.schema) {
		return errors.New("installed acceptance exposure fault cleanup is incomplete")
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	schema := pq.QuoteIdentifier(fault.authority.schema)
	qualifiedFunction := schema + "." + pq.QuoteIdentifier(fault.functionName)
	qualifiedTable := schema + "." + pq.QuoteIdentifier("uci_exposures")
	db := fault.authority.store.GetDB().WithContext(cleanupCtx)
	triggerErr := db.Exec(`DROP TRIGGER IF EXISTS ` + pq.QuoteIdentifier(fault.triggerName) + ` ON ` + qualifiedTable).Error
	functionErr := db.Exec(`DROP FUNCTION IF EXISTS ` + qualifiedFunction + `()`).Error
	if triggerErr == nil && functionErr == nil {
		fault.closed = true
	}
	return errors.Join(triggerErr, functionErr)
}

type uciInstalledAcceptanceRecorderInput struct {
	client    *uciInstalledAcceptanceMCPClient
	selection uciInstalledAcceptanceSelection
	authority *uciInstalledAcceptanceAuthority
	result    *uciInstalledAcceptanceResult
}

func uciExerciseInstalledAcceptanceRecorder(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, authority *uciInstalledAcceptanceAuthority, mismatchQuery string, result *uciInstalledAcceptanceResult) (retErr error) {
	if client == nil || selection.contextHandle == "" || mismatchQuery == "" || result == nil {
		return errors.New("installed acceptance recorder matrix is incomplete")
	}
	input := uciInstalledAcceptanceRecorderInput{client: client, selection: selection, authority: authority, result: result}
	beforeUnavailable, err := uciInstalledAcceptanceExposureCount(ctx, authority)
	if err != nil {
		return err
	}
	fault, err := uciInstallInstalledAcceptanceExposureFault(ctx, authority)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, fault.Close())
	}()
	afterUnavailable, err := uciExerciseInstalledAcceptanceRecorderFailure(ctx, input, beforeUnavailable)
	if err != nil {
		return err
	}
	if err := fault.Close(); err != nil {
		return err
	}
	first, afterRetry, err := uciExerciseInstalledAcceptanceRecorderRetry(ctx, input, afterUnavailable)
	if err != nil {
		return err
	}
	return uciExerciseInstalledAcceptanceRecorderMismatch(ctx, input, first, afterRetry, mismatchQuery)
}

func uciExerciseInstalledAcceptanceRecorderFailure(ctx context.Context, input uciInstalledAcceptanceRecorderInput, beforeUnavailable int64) (int64, error) {
	unavailable, err := input.client.ToolWithCall(ctx, "codebase_search", uciInstalledAcceptanceSearchArguments(input.selection.contextHandle, "recorder-unavailable"))
	if err != nil {
		return 0, err
	}
	if unavailable.isError {
		return 0, errors.New("installed standard MCP recorder failure returned a protocol-level tool error")
	}
	input.result.Recorder.InitialUnavailable, err = uciDecodeInstalledAcceptanceClosedOutcome(unavailable.payload)
	if err != nil {
		return 0, fmt.Errorf("decode initial exposure-unavailable response: %w", err)
	}
	if input.result.Recorder.InitialUnavailable.Status != "unavailable" || input.result.Recorder.InitialUnavailable.ErrorCode != "EXPOSURE_UNAVAILABLE" {
		return 0, errors.New("installed standard MCP recorder failure did not return EXPOSURE_UNAVAILABLE")
	}
	afterUnavailable, err := uciInstalledAcceptanceExposureCount(ctx, input.authority)
	if err != nil {
		return 0, err
	}
	if afterUnavailable != beforeUnavailable {
		return 0, errors.New("installed standard MCP recorder failure appended UCI exposure evidence")
	}
	status, err := uciInstalledAcceptanceStatusForSelection(ctx, input.client, input.selection)
	if err != nil {
		return 0, err
	}
	input.result.Recorder.HealthAfterInitialUnavailable = status.evidenceRecorder.state
	if status.evidenceRecorder.state != "unavailable" || status.evidenceRecorder.lastFailureCode != "EXPOSURE_UNAVAILABLE" {
		return 0, errors.New("installed standard MCP recorder failure did not make health unavailable")
	}
	return afterUnavailable, nil
}

func uciExerciseInstalledAcceptanceRecorderRetry(ctx context.Context, input uciInstalledAcceptanceRecorderInput, afterUnavailable int64) (uciInstalledAcceptanceMCPToolResult, int64, error) {
	first, err := input.client.ToolWithCall(ctx, "codebase_search", uciInstalledAcceptanceSearchArguments(input.selection.contextHandle, "recorder-exact-retry"))
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{}, 0, err
	}
	if first.isError {
		return uciInstalledAcceptanceMCPToolResult{}, 0, errors.New("installed standard MCP recorder success returned a protocol-level tool error")
	}
	firstExposure, err := uciInstalledAcceptanceExposureReference(first.payload)
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{}, 0, err
	}
	input.result.Recorder.FirstExposureDigest = uciInstalledAcceptanceStringDigest(firstExposure)
	afterFirst, err := uciInstalledAcceptanceExposureCount(ctx, input.authority)
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{}, 0, err
	}
	if afterFirst != afterUnavailable+1 {
		return uciInstalledAcceptanceMCPToolResult{}, 0, errors.New("installed standard MCP recorder success did not append one UCI exposure")
	}
	retry, err := input.client.RetryTool(ctx, first.call)
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{}, 0, err
	}
	if retry.isError {
		return uciInstalledAcceptanceMCPToolResult{}, 0, errors.New("installed standard MCP recorder exact retry returned a protocol-level tool error")
	}
	retryExposure, err := uciInstalledAcceptanceExposureReference(retry.payload)
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{}, 0, err
	}
	if retryExposure != firstExposure {
		return uciInstalledAcceptanceMCPToolResult{}, 0, errors.New("installed standard MCP recorder exact retry changed its exposure reference")
	}
	input.result.Recorder.ExactRetryExposureDigest = uciInstalledAcceptanceStringDigest(retryExposure)
	afterRetry, err := uciInstalledAcceptanceExposureCount(ctx, input.authority)
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{}, 0, err
	}
	if afterRetry != afterFirst {
		return uciInstalledAcceptanceMCPToolResult{}, 0, errors.New("installed standard MCP recorder exact retry appended a second UCI exposure")
	}
	return first, afterRetry, nil
}

func uciExerciseInstalledAcceptanceRecorderMismatch(ctx context.Context, input uciInstalledAcceptanceRecorderInput, first uciInstalledAcceptanceMCPToolResult, afterRetry int64, mismatchQuery string) error {
	statusBeforeMismatch, err := uciInstalledAcceptanceStatusForSelection(ctx, input.client, input.selection)
	if err != nil {
		return err
	}
	input.result.Recorder.HealthBeforeMismatch = statusBeforeMismatch.evidenceRecorder.state
	if statusBeforeMismatch.evidenceRecorder.state != "healthy" || statusBeforeMismatch.evidenceRecorder.lastFailureCode != "NONE" {
		return errors.New("installed standard MCP recorder did not recover healthy state")
	}
	mismatch, err := input.client.ReplayToolWithSameJSONRPCID(ctx, first.call, "codebase_search", uciInstalledAcceptanceSearchArguments(input.selection.contextHandle, mismatchQuery))
	if err != nil {
		return err
	}
	if mismatch.isError {
		return errors.New("installed standard MCP recorder mismatch returned a protocol-level tool error")
	}
	afterMismatch, err := uciInstalledAcceptanceExposureCount(ctx, input.authority)
	if err != nil {
		return err
	}
	if afterMismatch != afterRetry {
		return errors.New("installed standard MCP recorder mismatch appended UCI exposure evidence under a different idempotency key")
	}
	input.result.Recorder.Mismatch, err = uciDecodeInstalledAcceptanceClosedOutcome(mismatch.payload)
	if err != nil {
		return fmt.Errorf("decode exposure idempotency-mismatch response: %w", err)
	}
	if input.result.Recorder.Mismatch.Status != "unavailable" || input.result.Recorder.Mismatch.ErrorCode != "IDEMPOTENCY_MISMATCH" {
		return errors.New("installed standard MCP recorder mismatch did not return IDEMPOTENCY_MISMATCH")
	}
	statusAfterMismatch, err := uciInstalledAcceptanceStatusForSelection(ctx, input.client, input.selection)
	if err != nil {
		return err
	}
	input.result.Recorder.HealthAfterMismatch = statusAfterMismatch.evidenceRecorder.state
	if statusAfterMismatch.evidenceRecorder.state != "healthy" || statusAfterMismatch.evidenceRecorder.lastFailureCode != "NONE" {
		return errors.New("installed standard MCP recorder mismatch changed healthy state")
	}
	return nil
}

func uciInstalledAcceptanceExposureReference(payload json.RawMessage) (string, error) {
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil {
		return "", err
	}
	if (response.Status != uci.QueryStatusOK && response.Status != uci.QueryStatusPartial && response.Status != uci.QueryStatusEmpty) || response.Exposure == nil || response.Exposure.ExposureRef == "" {
		return "", fmt.Errorf("installed standard MCP recorder success has no exposure reference: status=%s exposure=%t %s", response.Status, response.Exposure != nil, uciInstalledAcceptancePayloadShape(payload))
	}
	return response.Exposure.ExposureRef, nil
}

func uciInstalledAcceptanceProjectionCountsForAuthority(ctx context.Context, authority *uciInstalledAcceptanceAuthority) (uciInstalledAcceptanceProjectionCounts, error) {
	if authority == nil || authority.store == nil || authority.source == nil || authority.source.SourceID == "" {
		return uciInstalledAcceptanceProjectionCounts{}, errors.New("installed acceptance projection count authority is incomplete")
	}
	checkoutIDs := make([]string, 0, 2)
	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB} {
		checkout := authority.checkouts[client]
		if checkout == nil || checkout.CheckoutID == "" {
			return uciInstalledAcceptanceProjectionCounts{}, errors.New("installed acceptance projection count checkout is unavailable")
		}
		checkoutIDs = append(checkoutIDs, checkout.CheckoutID)
	}
	db := authority.store.GetDB().WithContext(ctx)
	counts := uciInstalledAcceptanceProjectionCounts{}
	for _, count := range []struct {
		name  string
		query string
		into  *int64
		args  []any
	}{
		{name: "embeddings", query: `SELECT COUNT(*) FROM ci_embeddings WHERE source_id = ?`, into: &counts.Embeddings, args: []any{authority.source.SourceID}},
		{name: "chunk embeddings", query: `SELECT COUNT(*) FROM ci_chunk_embeddings WHERE source_id = ?`, into: &counts.ChunkEmbeddings, args: []any{authority.source.SourceID}},
		{name: "resolved edges", query: `SELECT COUNT(*) FROM ci_resolved_edges WHERE checkout_id IN (?)`, into: &counts.ResolvedEdges, args: []any{checkoutIDs}},
	} {
		if err := db.Raw(count.query, count.args...).Scan(count.into).Error; err != nil {
			return uciInstalledAcceptanceProjectionCounts{}, fmt.Errorf("count installed acceptance %s: %w", count.name, err)
		}
	}
	return counts, nil
}

func uciInstalledAcceptanceViewDelta(ctx context.Context, authority *uciInstalledAcceptanceAuthority, beforeViewID, afterViewID string) string {
	if authority == nil || authority.store == nil || beforeViewID == "" || afterViewID == "" {
		return uciInstalledAcceptanceViewDeltaUnavailable
	}
	type viewRow struct {
		ViewID         string         `gorm:"column:view_id"`
		ObservedFSSeq  int64          `gorm:"column:observed_fs_seq"`
		ManifestDigest string         `gorm:"column:manifest_digest"`
		CoverageJSON   string         `gorm:"column:coverage_json"`
		HeadOID        sql.NullString `gorm:"column:head_oid"`
		ObjectFormat   sql.NullString `gorm:"column:object_format"`
		RefLabel       sql.NullString `gorm:"column:ref_label"`
		Dirty          bool           `gorm:"column:dirty"`
	}
	var rows []viewRow
	if err := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT view_id, observed_fs_seq, manifest_digest, coverage_json::text AS coverage_json,
		       head_oid, object_format, ref_label, dirty
		FROM ci_views WHERE view_id IN (?, ?)`, beforeViewID, afterViewID).Scan(&rows).Error; err != nil || len(rows) != 2 {
		return uciInstalledAcceptanceViewDeltaUnavailable
	}
	byID := make(map[string]viewRow, 2)
	for _, row := range rows {
		byID[row.ViewID] = row
	}
	before, beforeOK := byID[beforeViewID]
	after, afterOK := byID[afterViewID]
	if !beforeOK || !afterOK {
		return uciInstalledAcceptanceViewDeltaUnavailable
	}
	gitSame := before.HeadOID == after.HeadOID && before.ObjectFormat == after.ObjectFormat && before.RefLabel == after.RefLabel && before.Dirty == after.Dirty
	var producerCount int64
	_ = authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM ci_jobs AS job
		JOIN ci_views AS view_row ON view_row.view_id = job.result_view_id
		WHERE job.result_view_id = ? AND job.state = 'succeeded'
		  AND job.updated_at = view_row.published_at AND job.sealed_manifest IS NOT NULL`, beforeViewID).Scan(&producerCount).Error
	return fmt.Sprintf("fs_seq=%d->%d manifest_same=%t coverage_same=%t git_same=%t producer_count=%d", before.ObservedFSSeq, after.ObservedFSSeq, before.ManifestDigest == after.ManifestDigest, before.CoverageJSON == after.CoverageJSON, gitSame, producerCount)
}

func uciVerifyInstalledAcceptanceRestartArtifacts(
	candidates map[string]uciInstallHarnessCommand,
	installation *uciInstallHarnessInstallation,
	expected map[string]uciInstalledAcceptanceArtifact,
) (map[string]string, error) {
	if installation == nil {
		return nil, errors.New("restarted installed acceptance has no installation")
	}
	paths := make(map[string]string, 3)
	for _, role := range []string{"server", "daemon", "parser"} {
		candidate, found := candidates[role]
		if !found || candidate.Executable == "" {
			return nil, fmt.Errorf("restarted installed acceptance has no %s candidate", role)
		}
		artifact, found := expected[role]
		if !found || artifact.CandidateSHA256 == "" || artifact.InstalledSHA256 != artifact.CandidateSHA256 {
			return nil, fmt.Errorf("restarted installed acceptance has no verified %s artifact", role)
		}
		candidateHash, err := uciInstalledAcceptanceFileSHA256(candidate.Executable)
		if err != nil {
			return nil, err
		}
		installedPath, err := installation.Executable(role)
		if err != nil {
			return nil, err
		}
		installedHash, err := uciInstalledAcceptanceFileSHA256(installedPath)
		if err != nil {
			return nil, err
		}
		if candidateHash != artifact.CandidateSHA256 || installedHash != candidateHash {
			return nil, fmt.Errorf("restarted installed %s artifact bytes do not match the original candidate", role)
		}
		paths[role] = installedPath
	}
	return paths, nil
}

func uciWaitForInstalledAcceptanceLoopbackRelease(ctx context.Context, host string, port int) error {
	if port < 1 || port > 65535 {
		return errors.New("installed acceptance loopback release port is invalid")
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		listener, err := net.Listen("tcp", address)
		if err == nil {
			closeErr := listener.Close()
			if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
				return fmt.Errorf("release installed acceptance loopback probe: %w", closeErr)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for installed acceptance loopback release: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func uciWaitForInstalledAcceptanceRestartQuiescence(
	ctx context.Context,
	client *uciInstalledAcceptanceMCPClient,
	selection uciInstalledAcceptanceSelection,
	expected uciInstalledAcceptancePublication,
) (uciInstalledAcceptancePublication, error) {
	if client == nil || selection.contextHandle == "" || expected.sourceID == "" || expected.checkoutID == "" || expected.viewID == "" || expected.profileID == "" || expected.generation < 1 {
		return uciInstalledAcceptancePublication{}, errors.New("installed acceptance restart quiescence target is incomplete")
	}
	var candidate uciInstalledAcceptancePublication
	observations := 0
	ticker := time.NewTicker(uciInstalledAcceptanceQuiescencePollInterval)
	defer ticker.Stop()
	for {
		status, err := uciInstalledAcceptanceStatusForSelection(ctx, client, selection)
		if err != nil {
			return uciInstalledAcceptancePublication{}, err
		}
		if status.error != "" {
			return uciInstalledAcceptancePublication{}, fmt.Errorf("installed standard MCP restart status error: %s", uciInstalledAcceptanceSafeErrorDetail(status.error))
		}
		var ready bool
		candidate, observations, ready, err = uciInstalledAcceptanceRestartQuiescenceObservation(status, expected, candidate, observations)
		if err != nil {
			return uciInstalledAcceptancePublication{}, err
		}
		if ready {
			return candidate, nil
		}
		select {
		case <-ctx.Done():
			return uciInstalledAcceptancePublication{}, fmt.Errorf("wait for installed acceptance restart quiescence: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func uciInstalledAcceptanceRestartQuiescenceObservation(status uciInstalledAcceptanceStatus, expected, candidate uciInstalledAcceptancePublication, observations int) (uciInstalledAcceptancePublication, int, bool, error) {
	if status.status != "idle" || status.context == nil || status.freshness == nil || status.freshness.state != "observed_current" || status.freshness.pendingChanges == nil || *status.freshness.pendingChanges != 0 {
		return uciInstalledAcceptancePublication{}, 0, false, nil
	}
	publication := uciInstalledAcceptancePublication{
		sourceID: status.context.sourceID, checkoutID: status.context.checkoutID, viewID: status.context.viewID, profileID: status.context.profileID,
		generation: status.context.generation, runID: status.runID, freshnessState: status.freshness.state, evidenceRecorder: status.evidenceRecorder.state,
	}
	if !uciInstalledAcceptanceSameViewPublication(publication, expected) {
		return uciInstalledAcceptancePublication{}, 0, false, errors.New("installed standard MCP restart selected a changed View")
	}
	if observations == 0 || !uciInstalledAcceptanceSameViewPublication(candidate, publication) || candidate.runID != publication.runID {
		candidate, observations = publication, 1
	} else {
		observations++
	}
	return candidate, observations, observations >= uciInstalledAcceptanceQuiescenceObservations, nil
}

func uciInstalledAcceptanceSameViewPublication(left, right uciInstalledAcceptancePublication) bool {
	return left.sourceID == right.sourceID &&
		left.checkoutID == right.checkoutID &&
		left.viewID == right.viewID &&
		left.profileID == right.profileID &&
		left.generation == right.generation
}

func uciInstalledAcceptanceContextForPublication(publication uciInstalledAcceptancePublication) uciInstalledAcceptanceContext {
	return uciInstalledAcceptanceContext{
		SourceDigest:   uciInstalledAcceptanceStringDigest(publication.sourceID),
		CheckoutDigest: uciInstalledAcceptanceStringDigest(publication.checkoutID),
		ViewDigest:     uciInstalledAcceptanceStringDigest(publication.viewID),
	}
}

func uciInstalledAcceptanceSameObservations(left, right uciInstalledAcceptanceObservations) bool {
	return uciInstalledAcceptanceSameStrings(left.SearchArtifactDigests, right.SearchArtifactDigests) &&
		uciInstalledAcceptanceSameStrings(left.GraphCalleeDigests, right.GraphCalleeDigests) &&
		uciInstalledAcceptanceSameStrings(left.ReadArtifactDigests, right.ReadArtifactDigests)
}

func uciInstalledAcceptanceCloneObservations(observation uciInstalledAcceptanceObservations) uciInstalledAcceptanceObservations {
	return uciInstalledAcceptanceObservations{
		SearchArtifactDigests: append([]string(nil), observation.SearchArtifactDigests...),
		GraphCalleeDigests:    append([]string(nil), observation.GraphCalleeDigests...),
		ReadArtifactDigests:   append([]string(nil), observation.ReadArtifactDigests...),
	}
}

func uciInstalledAcceptanceSameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func uciWaitForInstalledAcceptanceRestartParserPID(ctx context.Context, installation *uciInstallHarnessInstallation, parserPath string, previousPID int) (int, error) {
	if installation == nil || parserPath == "" || previousPID <= 0 {
		return 0, errors.New("installed acceptance restart parser target is incomplete")
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, pid := range installation.ObservedPIDsForExecutable(parserPath) {
			if pid > 0 && pid != previousPID {
				return pid, nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("wait for restarted installed parser process: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func uciRestartInstalledAcceptance(ctx context.Context, input uciInstalledAcceptanceRestartInput) (next *uciInstallHarnessInstallation, newDaemonPID int, retErr error) {
	state, err := uciPrepareInstalledAcceptanceRestart(ctx, input)
	if err != nil {
		return nil, 0, err
	}
	if err := state.materialize(ctx); err != nil {
		return nil, 0, err
	}
	defer func() {
		if retErr == nil || state.next == nil {
			return
		}
		retErr = errors.Join(retErr, state.cleanup())
		next = nil
		newDaemonPID = 0
	}()
	if err := state.startServer(ctx); err != nil {
		return nil, 0, err
	}
	state.clients = make(map[string]*uciInstalledAcceptanceMCPClient, 3)
	defer state.captureClientTranscripts()
	if err := state.startClients(ctx); err != nil {
		return nil, 0, err
	}
	if err := state.restartPublications(ctx); err != nil {
		return nil, 0, err
	}
	if err := state.recordRestartedProcesses(ctx); err != nil {
		return nil, 0, err
	}
	if err := state.verifyRestartedObservations(ctx); err != nil {
		return nil, 0, err
	}
	if err := state.verifyRestartedProjections(ctx); err != nil {
		return nil, 0, err
	}
	if err := state.runScenarioProbe(ctx); err != nil {
		return nil, 0, err
	}
	return state.next, state.daemonPID, nil
}

func uciPrepareInstalledAcceptanceRestart(ctx context.Context, input uciInstalledAcceptanceRestartInput) (uciInstalledAcceptanceRestartState, error) {
	state, err := uciNewInstalledAcceptanceRestartState(input)
	if err != nil {
		return uciInstalledAcceptanceRestartState{}, err
	}
	if err := state.captureBaseline(ctx); err != nil {
		return uciInstalledAcceptanceRestartState{}, err
	}
	if err := state.retire(ctx); err != nil {
		return uciInstalledAcceptanceRestartState{}, err
	}
	// The restarted lifecycle now owns successor cleanup. A later failure must
	// not stop the retired PID against a successor daemon control record.
	*state.input.daemonOwner = 0
	if err := uciWaitForInstalledAcceptanceLoopbackRelease(ctx, state.input.request.LoopbackHost, state.input.serverPort); err != nil {
		return uciInstalledAcceptanceRestartState{}, err
	}
	return state, nil
}

func uciNewInstalledAcceptanceRestartState(input uciInstalledAcceptanceRestartInput) (uciInstalledAcceptanceRestartState, error) {
	if input.daemonOwner == nil {
		return uciInstalledAcceptanceRestartState{}, errors.New("installed acceptance restart daemon owner is unavailable")
	}
	state := uciInstalledAcceptanceRestartState{input: input, oldDaemonPID: *input.daemonOwner}
	result := input.result
	if input.oldInstallation == nil || input.authority == nil || result == nil || state.oldDaemonPID <= 0 || result.Processes.DaemonPID != state.oldDaemonPID || result.Processes.ServerPID <= 0 || result.Processes.ParserPID <= 0 {
		return uciInstalledAcceptanceRestartState{}, errors.New("installed acceptance restart baseline is incomplete")
	}
	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB, uciInstalledAcceptanceClientC} {
		if input.oldClients[client] == nil || input.oldClients[client].process == nil {
			return uciInstalledAcceptanceRestartState{}, errors.New("installed acceptance restart client is incomplete")
		}
	}
	return state, nil
}

func (state *uciInstalledAcceptanceRestartState) captureBaseline(ctx context.Context) error {
	result := state.input.result
	if result.Restart.BeforeClientContexts == nil {
		result.Restart.BeforeClientContexts = make(map[string]uciInstalledAcceptanceContext)
	}
	if result.Restart.BeforeObservations == nil {
		result.Restart.BeforeObservations = make(map[string]uciInstalledAcceptanceObservations)
	}
	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB} {
		publication, found := state.input.publications[client]
		if !found || state.input.selections[client].contextHandle == "" || publication.sourceID == "" || publication.checkoutID == "" || publication.viewID == "" || publication.profileID == "" || publication.generation < 1 || publication.runID == "" {
			return errors.New("installed acceptance restart selected publication is incomplete")
		}
		observation, found := result.Observations[client]
		if !found {
			return errors.New("installed acceptance restart has no observation baseline")
		}
		result.Restart.BeforeClientContexts[client] = uciInstalledAcceptanceContextForPublication(publication)
		result.Restart.BeforeObservations[client] = uciInstalledAcceptanceCloneObservations(observation)
	}
	result.Restart.BeforeProcesses = result.Processes
	beforeCounts, err := uciInstalledAcceptanceProjectionCountsForAuthority(ctx, state.input.authority)
	if err != nil {
		return err
	}
	result.Restart.BeforeProjectionCounts = beforeCounts
	return nil
}

func (state *uciInstalledAcceptanceRestartState) retire(ctx context.Context) error {
	var shutdownErrors []error
	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB, uciInstalledAcceptanceClientC} {
		if closeErr := state.input.oldClients[client].process.closePipes(); closeErr != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("close installed acceptance %s stdio: %w", client, closeErr))
		}
	}
	if stopErr := uciStopInstalledAcceptanceDaemon(state.input.daemonControlRoot, state.oldDaemonPID); stopErr != nil {
		shutdownErrors = append(shutdownErrors, stopErr)
	}
	if waitErr := uciWaitInstalledAcceptanceProcessExit(state.oldDaemonPID, 5*time.Second); waitErr != nil {
		shutdownErrors = append(shutdownErrors, waitErr)
	}
	if closeErr := state.input.oldInstallation.Close(); closeErr != nil {
		shutdownErrors = append(shutdownErrors, closeErr)
	}
	return errors.Join(shutdownErrors...)
}

func (state *uciInstalledAcceptanceRestartState) materialize(ctx context.Context) error {
	request := state.input.request
	installResult, err := runUCIInstallHarness(ctx, uciInstallHarnessRequest{
		Version:          request.InstallHarnessVersion,
		Scenario:         uciInstallHarnessScenarioMaterialize,
		InstallRoot:      request.InstallRoot,
		Server:           state.input.candidates["server"],
		Daemon:           state.input.candidates["daemon"],
		Parser:           state.input.candidates["parser"],
		ReadinessTimeout: request.ReadinessTimeout,
	})
	if err != nil {
		return fmt.Errorf("rematerialize installed UCI candidates: %w", err)
	}
	state.next = installResult.Installation
	if state.next == nil {
		return errors.New("rematerialize installed UCI candidates returned no installation")
	}
	return nil
}

func (state *uciInstalledAcceptanceRestartState) cleanup() error {
	var cleanupErrors []error
	if state.daemonPID > 0 {
		if stopErr := uciStopInstalledAcceptanceDaemon(state.input.daemonControlRoot, state.daemonPID); stopErr != nil {
			cleanupErrors = append(cleanupErrors, stopErr)
		}
		if waitErr := uciWaitInstalledAcceptanceProcessExit(state.daemonPID, 5*time.Second); waitErr != nil {
			cleanupErrors = append(cleanupErrors, waitErr)
		}
	}
	if closeErr := state.next.Close(); closeErr != nil {
		cleanupErrors = append(cleanupErrors, closeErr)
	}
	return errors.Join(cleanupErrors...)
}

func (state *uciInstalledAcceptanceRestartState) startServer(ctx context.Context) error {
	var err error
	state.installedPaths, err = uciVerifyInstalledAcceptanceRestartArtifacts(state.input.candidates, state.next, state.input.result.Artifacts)
	if err != nil {
		return err
	}
	state.serverEnvironment, state.clientEnvironment, err = uciInstalledAcceptanceEnvironment(state.input.request, state.input.authority, state.input.serverPort, state.input.parserBundleDigest, state.installedPaths["parser"])
	if err != nil {
		return err
	}
	server, err := state.next.Start(ctx, uciInstalledHarnessLaunchRequest{Role: "server", Environment: state.serverEnvironment})
	if err != nil {
		return err
	}
	if server == nil || server.command == nil || server.command.Process == nil || server.command.Process.Pid <= 0 {
		return errors.New("restarted installed server has no process")
	}
	state.serverPID = server.command.Process.Pid
	readinessCtx, cancelReadiness := context.WithTimeout(ctx, state.input.request.ReadinessTimeout)
	if err := uciWaitForInstalledAcceptanceLoopback(readinessCtx, state.input.request.LoopbackHost, state.input.serverPort); err != nil {
		cancelReadiness()
		return err
	}
	cancelReadiness()
	return nil
}

func (state *uciInstalledAcceptanceRestartState) captureClientTranscripts() {
	for name, client := range state.clients {
		state.input.result.Restart.ClientTranscripts[name] = client.Transcript()
	}
}

func (state *uciInstalledAcceptanceRestartState) startClients(ctx context.Context) error {
	if err := state.startClient(ctx, uciInstalledAcceptanceClientA, state.input.worktrees.primaryRoot); err != nil {
		return err
	}
	var err error
	state.daemonPID, err = uciWaitForInstalledAcceptanceDaemonPID(ctx, state.input.daemonControlRoot, state.installedPaths["daemon"])
	if err != nil {
		return err
	}
	if state.daemonPID == state.oldDaemonPID {
		return errors.New("restarted installed daemon retained its previous PID")
	}
	if err := state.startClient(ctx, uciInstalledAcceptanceClientB, state.input.worktrees.linkedRoot); err != nil {
		return err
	}
	return state.startClient(ctx, uciInstalledAcceptanceClientC, state.input.worktrees.primaryRoot)
}

func (state *uciInstalledAcceptanceRestartState) startClient(ctx context.Context, name, workingDirectory string) error {
	process, err := state.next.Start(ctx, uciInstalledHarnessLaunchRequest{
		Role:             "daemon",
		WorkingDirectory: workingDirectory,
		Environment:      state.clientEnvironment,
		WithStdio:        true,
	})
	if err != nil {
		return err
	}
	client, err := newUCIInstalledAcceptanceMCPClient(name, process)
	if err != nil {
		return err
	}
	if err := client.InitializeAndList(ctx); err != nil {
		return err
	}
	if err := uciRequireInstalledAcceptanceTools(client.Transcript()); err != nil {
		return err
	}
	state.clients[name] = client
	return nil
}

func (state *uciInstalledAcceptanceRestartState) restartPublications(ctx context.Context) error {
	first, second, third := state.clients[uciInstalledAcceptanceClientA], state.clients[uciInstalledAcceptanceClientB], state.clients[uciInstalledAcceptanceClientC]
	firstSelection, secondSelection, err := uciSelectInstalledAcceptanceCheckouts(ctx, first, second, state.input.authority)
	if err != nil {
		return err
	}
	thirdSelection, err := uciSelectInstalledAcceptanceCheckout(ctx, third, uciInstalledAcceptanceClientA, state.input.authority)
	if err != nil {
		return err
	}
	if firstSelection.viewID == "" || secondSelection.viewID == "" || thirdSelection.viewID == "" {
		return errors.New("restarted installed clients did not select published Views")
	}
	primaryBefore, primaryFound := state.input.publications[uciInstalledAcceptanceClientA]
	linkedBefore, linkedFound := state.input.publications[uciInstalledAcceptanceClientB]
	if !primaryFound || !linkedFound {
		return errors.New("installed acceptance restart publication baseline is incomplete")
	}
	firstSelection, secondSelection, err = uciStartInstalledAcceptanceIndexes(ctx, first, second, firstSelection, secondSelection, state.input.worktrees)
	if err != nil {
		return err
	}
	state.selections = map[string]uciInstalledAcceptanceSelection{
		uciInstalledAcceptanceClientA: firstSelection,
		uciInstalledAcceptanceClientB: secondSelection,
		uciInstalledAcceptanceClientC: thirdSelection,
	}
	state.publications = make(map[string]uciInstalledAcceptancePublication, 3)
	if err := state.observeRestartedABPublications(ctx, primaryBefore, linkedBefore); err != nil {
		return err
	}
	thirdPublication, err := uciWaitForInstalledAcceptanceRestartQuiescence(ctx, third, thirdSelection, primaryBefore)
	if err != nil {
		return err
	}
	state.publications[uciInstalledAcceptanceClientC] = thirdPublication
	state.input.result.Restart.ClientContexts[uciInstalledAcceptanceClientC] = uciInstalledAcceptanceContextForPublication(thirdPublication)
	return nil
}

func (state *uciInstalledAcceptanceRestartState) observeRestartedABPublications(ctx context.Context, primaryBefore, linkedBefore uciInstalledAcceptancePublication) error {
	for _, item := range []struct {
		name      string
		client    *uciInstalledAcceptanceMCPClient
		selection uciInstalledAcceptanceSelection
		expected  uciInstalledAcceptancePublication
	}{
		{name: uciInstalledAcceptanceClientA, client: state.clients[uciInstalledAcceptanceClientA], selection: state.selections[uciInstalledAcceptanceClientA], expected: primaryBefore},
		{name: uciInstalledAcceptanceClientB, client: state.clients[uciInstalledAcceptanceClientB], selection: state.selections[uciInstalledAcceptanceClientB], expected: linkedBefore},
	} {
		barrier, err := uciWaitForInstalledAcceptanceBarrier(ctx, item.client, item.selection)
		if err != nil {
			return err
		}
		publication, err := uciWaitForInstalledAcceptanceQuiescence(ctx, item.client, item.selection, barrier)
		if err != nil {
			return err
		}
		contextRef := uciInstalledAcceptanceContextForPublication(publication)
		if !uciInstalledAcceptanceSameViewPublication(publication, item.expected) {
			delta := uciInstalledAcceptanceViewDelta(ctx, state.input.authority, item.expected.viewID, publication.viewID)
			return fmt.Errorf("restarted installed client %s advanced from generation %d/view %s to generation %d/view %s (%s)", item.name, item.expected.generation, uciInstalledAcceptanceStringDigest(item.expected.viewID), publication.generation, uciInstalledAcceptanceStringDigest(publication.viewID), delta)
		}
		if before := state.input.result.Restart.BeforeClientContexts[item.name]; before != contextRef {
			return fmt.Errorf("restarted installed client %s context digest changed", item.name)
		}
		state.publications[item.name] = publication
		state.input.result.Restart.ClientContexts[item.name] = contextRef
	}
	return nil
}

func (state *uciInstalledAcceptanceRestartState) recordRestartedProcesses(ctx context.Context) error {
	restartedParserPID, err := uciWaitForInstalledAcceptanceRestartParserPID(ctx, state.next, state.installedPaths["parser"], state.input.result.Processes.ParserPID)
	if err != nil {
		return err
	}
	afterProcesses := uciInstalledAcceptanceProcesses{ServerPID: state.serverPID, DaemonPID: state.daemonPID, ParserPID: restartedParserPID}
	if afterProcesses.ServerPID == state.input.result.Processes.ServerPID || afterProcesses.DaemonPID == state.input.result.Processes.DaemonPID || afterProcesses.ParserPID == state.input.result.Processes.ParserPID {
		return errors.New("restarted installed process retained a previous PID")
	}
	state.input.result.Restart.AfterProcesses = afterProcesses
	return nil
}

func (state *uciInstalledAcceptanceRestartState) verifyRestartedObservations(ctx context.Context) error {
	for _, item := range []struct {
		name           string
		client         *uciInstalledAcceptanceMCPClient
		selection      uciInstalledAcceptanceSelection
		expectedCallee string
	}{
		{name: uciInstalledAcceptanceClientA, client: state.clients[uciInstalledAcceptanceClientA], selection: state.selections[uciInstalledAcceptanceClientA], expectedCallee: state.input.request.Fixture.PrimaryCallee},
		{name: uciInstalledAcceptanceClientB, client: state.clients[uciInstalledAcceptanceClientB], selection: state.selections[uciInstalledAcceptanceClientB], expectedCallee: state.input.request.Fixture.LinkedCallee},
	} {
		observation, err := uciObserveInstalledAcceptanceSearchGraphRead(ctx, item.client, item.selection, state.publications[item.name], state.input.request.Fixture, item.expectedCallee)
		if err != nil {
			return fmt.Errorf("restarted installed standard MCP observation for %s: %w", item.name, err)
		}
		beforeObservation, found := state.input.result.Restart.BeforeObservations[item.name]
		if !found || !uciInstalledAcceptanceSameObservations(beforeObservation, observation) {
			return errors.New("restarted installed standard MCP observation changed")
		}
		state.input.result.Restart.Observations[item.name] = observation
	}
	return nil
}

func (state *uciInstalledAcceptanceRestartState) verifyRestartedProjections(ctx context.Context) error {
	afterCounts, err := uciInstalledAcceptanceProjectionCountsForAuthority(ctx, state.input.authority)
	if err != nil {
		return err
	}
	beforeCounts := state.input.result.Restart.BeforeProjectionCounts
	state.input.result.Restart.AfterProjectionCounts = afterCounts
	state.input.result.Restart.UnchangedInputReembedded = afterCounts.Embeddings > beforeCounts.Embeddings || afterCounts.ChunkEmbeddings > beforeCounts.ChunkEmbeddings
	if state.input.result.Restart.UnchangedInputReembedded {
		return errors.New("restarted installed runtime re-embedded unchanged input")
	}
	if afterCounts.Embeddings != beforeCounts.Embeddings || afterCounts.ChunkEmbeddings != beforeCounts.ChunkEmbeddings {
		return errors.New("restarted installed runtime changed unchanged-input embedding counts")
	}
	if afterCounts.ResolvedEdges != beforeCounts.ResolvedEdges {
		return errors.New("restarted installed runtime changed unchanged-input link counts")
	}
	return nil
}

func (state *uciInstalledAcceptanceRestartState) runScenarioProbe(ctx context.Context) error {
	if state.input.request.ScenarioProbe == nil {
		return nil
	}
	first := state.clients[uciInstalledAcceptanceClientA]
	scenarioRuntime := uciInstalledAcceptanceScenarioRuntime{
		Request:      state.input.request,
		Installation: state.next,
		Authority:    state.input.authority,
		Worktrees:    state.input.worktrees,
		ClientA:      first,
		ClientB:      state.clients[uciInstalledAcceptanceClientB],
		ClientC:      state.clients[uciInstalledAcceptanceClientC],
		Recorder:     first,
		Selections: map[string]uciInstalledAcceptanceSelection{
			uciInstalledAcceptanceClientA:        state.selections[uciInstalledAcceptanceClientA],
			uciInstalledAcceptanceClientB:        state.selections[uciInstalledAcceptanceClientB],
			uciInstalledAcceptanceClientC:        state.selections[uciInstalledAcceptanceClientC],
			uciInstalledAcceptanceClientRecorder: state.selections[uciInstalledAcceptanceClientA],
		},
		Publications: map[string]uciInstalledAcceptancePublication{
			uciInstalledAcceptanceClientA:        state.publications[uciInstalledAcceptanceClientA],
			uciInstalledAcceptanceClientB:        state.publications[uciInstalledAcceptanceClientB],
			uciInstalledAcceptanceClientC:        state.publications[uciInstalledAcceptanceClientC],
			uciInstalledAcceptanceClientRecorder: state.publications[uciInstalledAcceptanceClientA],
		},
		ParserBundleDigest: state.input.parserBundleDigest,
		Candidates:         state.input.candidates,
		ServerEnvironment:  state.serverEnvironment,
		ClientEnvironment:  state.clientEnvironment,
	}
	if err := uciRunInstalledAcceptanceScenarioProbe(ctx, scenarioRuntime, state.input.result); err != nil {
		return fmt.Errorf("installed UCI scenario probe: %w", err)
	}
	return nil
}

func uciExerciseInstalledAcceptanceWatcher(ctx context.Context, input uciInstalledAcceptanceWatcherInput) (after map[string]uciInstalledAcceptancePublication, retErr error) {
	fixture, authority, worktrees := input.fixture, input.authority, input.worktrees
	first, second := input.first, input.second
	firstSelection, secondSelection := input.firstSelection, input.secondSelection
	before, result := input.before, input.result
	if first == nil || second == nil || authority == nil || result == nil || firstSelection.contextHandle == "" || secondSelection.contextHandle == "" || worktrees.primaryRoot == "" || worktrees.linkedRoot == "" {
		return nil, errors.New("installed acceptance watcher proof is incomplete")
	}
	primaryBefore, primaryFound := before[uciInstalledAcceptanceClientA]
	linkedBefore, linkedFound := before[uciInstalledAcceptanceClientB]
	if !primaryFound || !linkedFound || primaryBefore.runID == "" || linkedBefore.viewID == "" {
		return nil, errors.New("installed acceptance watcher publication baseline is incomplete")
	}

	primaryPath := filepath.Join(worktrees.primaryRoot, filepath.FromSlash(fixture.RelativePath))
	savedSource, err := os.ReadFile(primaryPath)
	if err != nil {
		return nil, fmt.Errorf("read installed acceptance watcher A v1 source: %w", err)
	}
	if !bytes.Equal(savedSource, []byte(fixture.PrimarySource)) {
		return nil, errors.New("installed acceptance watcher A v1 bytes differ from the fixture")
	}
	updatedCallee := fixture.PrimaryCallee + "V2"
	updatedSource := strings.ReplaceAll(string(savedSource), fixture.PrimaryCallee, updatedCallee)
	if fixture.PrimaryCallee == "" || updatedSource == string(savedSource) || !strings.Contains(updatedSource, updatedCallee) {
		return nil, errors.New("installed acceptance watcher cannot construct A v2 source")
	}

	initialA, err := uciSnapshotInstalledAcceptanceWatcher(ctx, uciInstalledAcceptanceWatcherSnapshotInput{
		authority:      authority,
		client:         first,
		selection:      firstSelection,
		publication:    primaryBefore,
		root:           worktrees.primaryRoot,
		fixture:        fixture,
		expectedCallee: fixture.PrimaryCallee,
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot installed acceptance watcher A v1: %w", err)
	}
	initialB, err := uciSnapshotInstalledAcceptanceWatcher(ctx, uciInstalledAcceptanceWatcherSnapshotInput{
		authority:      authority,
		client:         second,
		selection:      secondSelection,
		publication:    linkedBefore,
		root:           worktrees.linkedRoot,
		fixture:        fixture,
		expectedCallee: fixture.LinkedCallee,
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot installed acceptance watcher B baseline: %w", err)
	}

	restorePending := false
	defer func() {
		if !restorePending {
			return
		}
		if restoreErr := os.WriteFile(primaryPath, savedSource, 0o600); restoreErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("restore installed acceptance watcher A bytes: %w", restoreErr))
		}
	}()
	if err := os.WriteFile(primaryPath, []byte(updatedSource), 0o600); err != nil {
		return nil, fmt.Errorf("write installed acceptance watcher A v2 source: %w", err)
	}
	restorePending = true
	afterUpdateA, err := uciWaitForInstalledAcceptanceWatcherState(ctx, first, firstSelection, primaryBefore, updatedCallee, fixture.RelativePath, true)
	if err != nil {
		return nil, err
	}
	afterUpdateB, err := uciWaitForInstalledAcceptanceRestartQuiescence(ctx, second, secondSelection, linkedBefore)
	if err != nil {
		return nil, err
	}
	if uciInstalledAcceptanceSameViewPublication(afterUpdateA, primaryBefore) {
		return nil, errors.New("installed acceptance watcher A v1 to v2 update did not publish a new View")
	}
	if !uciInstalledAcceptanceSameViewPublication(afterUpdateB, linkedBefore) {
		return nil, errors.New("installed acceptance watcher A v1 to v2 update changed B View")
	}
	updatedA, err := uciSnapshotInstalledAcceptanceWatcher(ctx, uciInstalledAcceptanceWatcherSnapshotInput{
		authority:      authority,
		client:         first,
		selection:      firstSelection,
		publication:    afterUpdateA,
		root:           worktrees.primaryRoot,
		fixture:        fixture,
		expectedCallee: updatedCallee,
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot installed acceptance watcher A v2: %w", err)
	}
	updatedB, err := uciSnapshotInstalledAcceptanceWatcher(ctx, uciInstalledAcceptanceWatcherSnapshotInput{
		authority:      authority,
		client:         second,
		selection:      secondSelection,
		publication:    afterUpdateB,
		root:           worktrees.linkedRoot,
		fixture:        fixture,
		expectedCallee: fixture.LinkedCallee,
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot installed acceptance watcher B after A v2: %w", err)
	}

	if err := os.WriteFile(primaryPath, savedSource, 0o600); err != nil {
		return nil, fmt.Errorf("restore installed acceptance watcher A v1 source: %w", err)
	}
	restorePending = false
	afterRestoreA, err := uciWaitForInstalledAcceptanceWatcherState(ctx, first, firstSelection, afterUpdateA, fixture.PrimaryCallee, fixture.RelativePath, true)
	if err != nil {
		return nil, err
	}
	afterRestoreB, err := uciWaitForInstalledAcceptanceRestartQuiescence(ctx, second, secondSelection, afterUpdateB)
	if err != nil {
		return nil, err
	}
	if uciInstalledAcceptanceSameViewPublication(afterRestoreA, afterUpdateA) {
		return nil, errors.New("installed acceptance watcher A restore did not publish a new View")
	}
	if !uciInstalledAcceptanceSameViewPublication(afterRestoreB, linkedBefore) {
		return nil, errors.New("installed acceptance watcher A restore changed B View")
	}
	restoredA, err := uciSnapshotInstalledAcceptanceWatcher(ctx, uciInstalledAcceptanceWatcherSnapshotInput{
		authority:      authority,
		client:         first,
		selection:      firstSelection,
		publication:    afterRestoreA,
		root:           worktrees.primaryRoot,
		fixture:        fixture,
		expectedCallee: fixture.PrimaryCallee,
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot restored installed acceptance watcher A v1: %w", err)
	}
	restoredB, err := uciSnapshotInstalledAcceptanceWatcher(ctx, uciInstalledAcceptanceWatcherSnapshotInput{
		authority:      authority,
		client:         second,
		selection:      secondSelection,
		publication:    afterRestoreB,
		root:           worktrees.linkedRoot,
		fixture:        fixture,
		expectedCallee: fixture.LinkedCallee,
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot installed acceptance watcher B after A restore: %w", err)
	}

	result.Watcher = uciInstalledAcceptanceWatcher{
		AfterWriteA:  updatedA.Publication,
		AfterDeleteA: restoredA.Publication,
		AfterWriteB:  updatedB.Publication,
		AfterDeleteB: restoredB.Publication,
		InitialA:     initialA,
		UpdatedA:     updatedA,
		RestoredA:    restoredA,
		InitialB:     initialB,
		UpdatedB:     updatedB,
		RestoredB:    restoredB,
	}
	return map[string]uciInstalledAcceptancePublication{
		uciInstalledAcceptanceClientA: afterRestoreA,
		uciInstalledAcceptanceClientB: afterRestoreB,
	}, nil
}

func uciSnapshotInstalledAcceptanceWatcher(ctx context.Context, input uciInstalledAcceptanceWatcherSnapshotInput) (uciInstalledAcceptanceWatcherSnapshot, error) {
	authority, client, selection, publication := input.authority, input.client, input.selection, input.publication
	root, fixture, expectedCallee := input.root, input.fixture, input.expectedCallee
	if authority == nil || authority.store == nil || client == nil || root == "" || fixture.RelativePath == "" || expectedCallee == "" {
		return uciInstalledAcceptanceWatcherSnapshot{}, errors.New("installed acceptance watcher snapshot target is incomplete")
	}
	bytes, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(fixture.RelativePath)))
	if err != nil {
		return uciInstalledAcceptanceWatcherSnapshot{}, fmt.Errorf("read installed acceptance watcher snapshot bytes: %w", err)
	}
	head, err := uciReadInstalledAcceptanceGit(ctx, root, uciInstalledAcceptanceGitRevParse, "HEAD")
	if err != nil {
		return uciInstalledAcceptanceWatcherSnapshot{}, err
	}
	tree, err := uciReadInstalledAcceptanceGit(ctx, root, uciInstalledAcceptanceGitRevParse, "HEAD^{tree}")
	if err != nil {
		return uciInstalledAcceptanceWatcherSnapshot{}, err
	}
	observations, err := uciObserveInstalledAcceptanceSearchGraphRead(ctx, client, selection, publication, fixture, expectedCallee)
	if err != nil {
		return uciInstalledAcceptanceWatcherSnapshot{}, err
	}
	if len(observations.SearchArtifactDigests) != 1 || len(observations.GraphCalleeDigests) != 1 || len(observations.ReadArtifactDigests) != 1 {
		return uciInstalledAcceptanceWatcherSnapshot{}, errors.New("installed standard MCP watcher snapshot is incomplete")
	}
	bytesDigest := uciInstalledAcceptanceStringDigest(string(bytes))
	if observations.SearchArtifactDigests[0] != bytesDigest || observations.ReadArtifactDigests[0] != bytesDigest || observations.GraphCalleeDigests[0] != uciInstalledAcceptanceStringDigest(expectedCallee) {
		return uciInstalledAcceptanceWatcherSnapshot{}, errors.New("installed standard MCP watcher snapshot does not bind its exact source bytes")
	}
	membershipDigest, edgesDigest, err := uciInstalledAcceptanceWatcherProjectionDigests(ctx, authority, publication)
	if err != nil {
		return uciInstalledAcceptanceWatcherSnapshot{}, err
	}
	return uciInstalledAcceptanceWatcherSnapshot{
		Publication:      uciInstalledAcceptancePublicationEvidenceFor(publication),
		BytesDigest:      bytesDigest,
		HeadDigest:       uciInstalledAcceptanceStringDigest(head),
		TreeDigest:       uciInstalledAcceptanceStringDigest(tree),
		MembershipDigest: membershipDigest,
		EdgesDigest:      edgesDigest,
		SearchDigest:     observations.SearchArtifactDigests[0],
		GraphDigest:      observations.GraphCalleeDigests[0],
		ReadDigest:       observations.ReadArtifactDigests[0],
	}, nil
}

func uciInstalledAcceptanceWatcherProjectionDigests(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication) (string, string, error) {
	if authority == nil || authority.store == nil || publication.sourceID == "" || publication.checkoutID == "" || publication.generation < 1 {
		return "", "", errors.New("installed acceptance watcher projection target is incomplete")
	}
	type membershipRow struct {
		PathKey     string `gorm:"column:path_key"`
		DisplayPath string `gorm:"column:display_path"`
		Mode        string `gorm:"column:mode"`
		FileState   string `gorm:"column:file_state"`
		ArtifactID  string `gorm:"column:artifact_id"`
	}
	type edgeRow struct {
		EdgeKey          string `gorm:"column:edge_key"`
		SourcePath       string `gorm:"column:source_path"`
		SourceArtifact   string `gorm:"column:source_artifact"`
		SourceSymbol     string `gorm:"column:source_symbol"`
		TargetPath       string `gorm:"column:target_path"`
		TargetArtifact   string `gorm:"column:target_artifact"`
		TargetSymbol     string `gorm:"column:target_symbol"`
		Relation         string `gorm:"column:relation"`
		EvidenceKind     string `gorm:"column:evidence_kind"`
		ResolverRevision string `gorm:"column:resolver_revision"`
		EvidenceJSON     string `gorm:"column:evidence_json"`
		ResolutionState  string `gorm:"column:resolution_state"`
	}
	db := authority.store.GetDB().WithContext(ctx)
	var memberships []membershipRow
	if err := db.Raw(`
		SELECT path_key, display_path, mode, file_state, COALESCE(artifact_id::text, '') AS artifact_id
		FROM ci_memberships
		WHERE source_id = ? AND checkout_id = ?
		  AND valid_from_generation <= ? AND (valid_to_generation IS NULL OR valid_to_generation > ?)
		ORDER BY path_key, display_path, mode, file_state, COALESCE(artifact_id::text, '')`,
		publication.sourceID, publication.checkoutID, publication.generation, publication.generation).Scan(&memberships).Error; err != nil {
		return "", "", fmt.Errorf("read installed acceptance watcher memberships: %w", err)
	}
	if len(memberships) == 0 {
		return "", "", errors.New("installed acceptance watcher View has no memberships")
	}
	membershipValues := []string{"uci-installed-watcher-memberships/v1"}
	for _, membership := range memberships {
		membershipValues = append(membershipValues, membership.PathKey, membership.DisplayPath, membership.Mode, membership.FileState, membership.ArtifactID)
	}
	var edges []edgeRow
	if err := db.Raw(`
		SELECT edge_key, source_path, source_artifact::text AS source_artifact, COALESCE(source_symbol, '') AS source_symbol,
		       COALESCE(target_path, '') AS target_path, COALESCE(target_artifact::text, '') AS target_artifact,
		       COALESCE(target_symbol, '') AS target_symbol, relation, evidence_kind, resolver_revision,
		       evidence_json::text AS evidence_json, resolution_state
		FROM ci_resolved_edges
		WHERE source_id = ? AND checkout_id = ?
		  AND valid_from_generation <= ? AND (valid_to_generation IS NULL OR valid_to_generation > ?)
		ORDER BY edge_key, source_path, source_artifact, COALESCE(source_symbol, ''), COALESCE(target_path, ''),
		         COALESCE(target_artifact::text, ''), COALESCE(target_symbol, ''), relation, evidence_kind,
		         resolver_revision, evidence_json::text, resolution_state`,
		publication.sourceID, publication.checkoutID, publication.generation, publication.generation).Scan(&edges).Error; err != nil {
		return "", "", fmt.Errorf("read installed acceptance watcher edges: %w", err)
	}
	if len(edges) == 0 {
		return "", "", errors.New("installed acceptance watcher View has no resolved edges")
	}
	edgeValues := []string{"uci-installed-watcher-edges/v1"}
	for _, edge := range edges {
		edgeValues = append(edgeValues, edge.EdgeKey, edge.SourcePath, edge.SourceArtifact, edge.SourceSymbol, edge.TargetPath, edge.TargetArtifact, edge.TargetSymbol, edge.Relation, edge.EvidenceKind, edge.ResolverRevision, edge.EvidenceJSON, edge.ResolutionState)
	}
	return uciInstalledReceiptDigestStrings(membershipValues...), uciInstalledReceiptDigestStrings(edgeValues...), nil
}

// uciWaitForInstalledAcceptanceWatcherRun polls the normal installed status
// path until the watcher owns a new run. It returns context expiry rather than
// treating an unchanged status as evidence that the watcher is healthy.
func uciWaitForInstalledAcceptanceWatcherRun(
	ctx context.Context,
	previousRunID string,
	observe func(context.Context) (uciInstalledAcceptanceStatus, error),
) (uciInstalledAcceptanceStatus, error) {
	if previousRunID == "" || observe == nil {
		return uciInstalledAcceptanceStatus{}, errors.New("installed acceptance watcher status target is incomplete")
	}
	ticker := time.NewTicker(uciInstalledAcceptanceQuiescencePollInterval)
	defer ticker.Stop()
	for {
		status, err := observe(ctx)
		if err != nil {
			return uciInstalledAcceptanceStatus{}, err
		}
		if status.error != "" {
			return uciInstalledAcceptanceStatus{}, fmt.Errorf("installed standard MCP watcher status error: %s", uciInstalledAcceptanceSafeErrorDetail(status.error))
		}
		if status.runID != "" && status.runID != previousRunID {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return uciInstalledAcceptanceStatus{}, fmt.Errorf("wait for installed acceptance watcher run: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func uciWaitForInstalledAcceptanceWatcherFirstCurrentPublication(
	ctx context.Context,
	client *uciInstalledAcceptanceMCPClient,
	selection uciInstalledAcceptanceSelection,
	previous uciInstalledAcceptancePublication,
) (uciInstalledAcceptancePublication, error) {
	if client == nil {
		return uciInstalledAcceptancePublication{}, errors.New("installed acceptance watcher status target is incomplete")
	}
	return uciWaitForInstalledAcceptanceWatcherFirstCurrentPublicationObserved(ctx, selection, previous, func(ctx context.Context, observed uciInstalledAcceptanceSelection) (uciInstalledAcceptanceStatus, error) {
		return uciInstalledAcceptanceStatusForSelection(ctx, client, observed)
	})
}

func uciWaitForInstalledAcceptanceWatcherFirstCurrentStatus(
	ctx context.Context,
	selection uciInstalledAcceptanceSelection,
	observe func(context.Context, uciInstalledAcceptanceSelection) (uciInstalledAcceptanceStatus, error),
) (uciInstalledAcceptanceStatus, error) {
	ticker := time.NewTicker(uciInstalledAcceptanceQuiescencePollInterval)
	defer ticker.Stop()
	for {
		status, err := observe(ctx, selection)
		if err == nil {
			return status, nil
		}
		if !uciInstalledAcceptanceIsTransientContextMismatch(err) {
			return uciInstalledAcceptanceStatus{}, err
		}
		select {
		case <-ctx.Done():
			return uciInstalledAcceptanceStatus{}, fmt.Errorf("wait for installed acceptance watcher first current status: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func uciWaitForInstalledAcceptanceWatcherFirstCurrentPublicationObserved(
	ctx context.Context,
	selection uciInstalledAcceptanceSelection,
	previous uciInstalledAcceptancePublication,
	observe func(context.Context, uciInstalledAcceptanceSelection) (uciInstalledAcceptanceStatus, error),
) (uciInstalledAcceptancePublication, error) {
	if selection.contextHandle == "" || previous.runID == "" || observe == nil {
		return uciInstalledAcceptancePublication{}, errors.New("installed acceptance watcher status target is incomplete")
	}
	status, err := uciWaitForInstalledAcceptanceWatcherRun(ctx, previous.runID, func(ctx context.Context) (uciInstalledAcceptanceStatus, error) {
		return uciWaitForInstalledAcceptanceWatcherFirstCurrentStatus(ctx, selection, observe)
	})
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	if err := uciValidateInstalledAcceptanceWatcherStatusContext(status, previous); err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	observedSelection := selection
	observedSelection.runID = status.runID
	ticker := time.NewTicker(uciInstalledAcceptanceQuiescencePollInterval)
	defer ticker.Stop()
	for {
		if status.error != "" {
			return uciInstalledAcceptancePublication{}, fmt.Errorf("installed standard MCP watcher status error: %s", uciInstalledAcceptanceSafeErrorDetail(status.error))
		}
		if status.runID != observedSelection.runID {
			return uciInstalledAcceptancePublication{}, errors.New("installed standard MCP watcher advanced before publishing its first current View")
		}
		publication, publicationErr := uciInstalledAcceptanceStatusPublication(status, observedSelection)
		if publicationErr == nil && !uciInstalledAcceptanceSameViewPublication(publication, previous) {
			if publication.sourceID != previous.sourceID || publication.checkoutID != previous.checkoutID || publication.profileID != previous.profileID {
				return uciInstalledAcceptancePublication{}, errors.New("installed standard MCP watcher published outside the selected View context")
			}
			return publication, nil
		}
		select {
		case <-ctx.Done():
			return uciInstalledAcceptancePublication{}, fmt.Errorf("wait for installed acceptance watcher current View: %w", ctx.Err())
		case <-ticker.C:
		}
		status, err = uciWaitForInstalledAcceptanceWatcherFirstCurrentStatus(ctx, observedSelection, observe)
		if err != nil {
			return uciInstalledAcceptancePublication{}, err
		}
	}
}

func uciValidateInstalledAcceptanceWatcherStatusContext(status uciInstalledAcceptanceStatus, previous uciInstalledAcceptancePublication) error {
	if status.context == nil || status.context.sourceID != previous.sourceID || status.context.checkoutID != previous.checkoutID || status.context.profileID != previous.profileID {
		return errors.New("installed standard MCP watcher status advanced outside the selected source, checkout, or profile")
	}
	return nil
}

type uciInstalledAcceptanceWatcherStageObserver func(stage string, started, returned time.Time, err error)

func uciWaitForInstalledAcceptanceWatcherPublication(
	ctx context.Context,
	client *uciInstalledAcceptanceMCPClient,
	selection uciInstalledAcceptanceSelection,
	previous uciInstalledAcceptancePublication,
) (uciInstalledAcceptancePublication, error) {
	return uciWaitForInstalledAcceptanceWatcherPublicationObserved(ctx, client, selection, previous, nil)
}

func uciWaitForInstalledAcceptanceWatcherPublicationObserved(
	ctx context.Context,
	client *uciInstalledAcceptanceMCPClient,
	selection uciInstalledAcceptanceSelection,
	previous uciInstalledAcceptancePublication,
	observe uciInstalledAcceptanceWatcherStageObserver,
) (uciInstalledAcceptancePublication, error) {
	if client == nil || selection.contextHandle == "" || previous.runID == "" {
		return uciInstalledAcceptancePublication{}, errors.New("installed acceptance watcher status target is incomplete")
	}
	status, err := uciWaitForInstalledAcceptanceWatcherRun(ctx, previous.runID, func(ctx context.Context) (uciInstalledAcceptanceStatus, error) {
		return uciInstalledAcceptanceStatusForSelection(ctx, client, selection)
	})
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	observedSelection := selection
	observedSelection.runID = status.runID
	barrierStarted := time.Now()
	barrier, err := uciWaitForInstalledAcceptanceBarrier(ctx, client, observedSelection)
	if observe != nil {
		observe("barrier", barrierStarted, time.Now(), err)
	}
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	quiescenceStarted := time.Now()
	publication, err := uciWaitForInstalledAcceptanceQuiescence(ctx, client, observedSelection, barrier)
	if observe != nil {
		observe("quiescence", quiescenceStarted, time.Now(), err)
	}
	return publication, err
}

func uciWaitForInstalledAcceptanceWatcherState(
	ctx context.Context,
	client *uciInstalledAcceptanceMCPClient,
	selection uciInstalledAcceptanceSelection,
	previous uciInstalledAcceptancePublication,
	functionName, relativePath string,
	wantPresent bool,
) (uciInstalledAcceptancePublication, error) {
	for {
		publication, err := uciWaitForInstalledAcceptanceWatcherPublication(ctx, client, selection, previous)
		if err != nil {
			return uciInstalledAcceptancePublication{}, err
		}
		err = uciRequireInstalledAcceptanceWatcherCanary(ctx, client, selection, publication, functionName, relativePath, wantPresent)
		if err == nil {
			return publication, nil
		}
		if !errors.Is(err, errUCIInstalledAcceptanceWatcherCanaryMissing) && !errors.Is(err, errUCIInstalledAcceptanceWatcherCanaryPresent) {
			return uciInstalledAcceptancePublication{}, err
		}
		previous = publication
	}
}

func uciRequireInstalledAcceptanceWatcherCanary(
	ctx context.Context,
	client *uciInstalledAcceptanceMCPClient,
	selection uciInstalledAcceptanceSelection,
	publication uciInstalledAcceptancePublication,
	functionName, relativePath string,
	wantPresent bool,
) error {
	_, err := uciRequireInstalledAcceptanceWatcherCanaryResponse(ctx, uciInstalledAcceptanceWatcherCanaryInput{client: client, selection: selection, publication: publication, functionName: functionName, relativePath: relativePath, wantPresent: wantPresent})
	return err
}

func uciRequireInstalledAcceptanceWatcherCanaryResponse(ctx context.Context, input uciInstalledAcceptanceWatcherCanaryInput) (uci.QueryResponse, error) {
	response, err := uciInstalledAcceptanceWatcherCanarySearch(ctx, input)
	if err != nil {
		return uci.QueryResponse{}, err
	}
	if !input.wantPresent {
		return uciInstalledAcceptanceValidateWatcherCanaryAbsent(response)
	}
	if err := uciInstalledAcceptanceValidateWatcherCanaryPresent(response, input); err != nil {
		return uci.QueryResponse{}, err
	}
	return response, nil
}

func uciInstalledAcceptanceWatcherCanarySearch(ctx context.Context, input uciInstalledAcceptanceWatcherCanaryInput) (uci.QueryResponse, error) {
	payload, err := input.client.Tool(ctx, "codebase_search", map[string]any{
		"context_handle": input.selection.contextHandle,
		"query":          input.functionName,
		"path_prefix":    input.relativePath,
		"limit":          10,
	})
	if err != nil {
		return uci.QueryResponse{}, err
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil {
		return uci.QueryResponse{}, err
	}
	if !uciInstalledAcceptanceQueryMatchesPublication(response, input.publication) {
		return uci.QueryResponse{}, errors.New("installed standard MCP watcher search did not retain the selected View")
	}
	return response, nil
}

func uciInstalledAcceptanceValidateWatcherCanaryAbsent(response uci.QueryResponse) (uci.QueryResponse, error) {
	if (response.Status != uci.QueryStatusEmpty && response.Status != uci.QueryStatusPartial) || (response.Items != nil && len(*response.Items) != 0) {
		return uci.QueryResponse{}, errUCIInstalledAcceptanceWatcherCanaryPresent
	}
	return response, nil
}

func uciInstalledAcceptanceValidateWatcherCanaryPresent(response uci.QueryResponse, input uciInstalledAcceptanceWatcherCanaryInput) error {
	if (response.Status != uci.QueryStatusOK && response.Status != uci.QueryStatusPartial) || response.Items == nil {
		return errors.New("installed standard MCP watcher write did not return the canary")
	}
	found := false
	for _, item := range *response.Items {
		if item.Ref.SourceID != input.publication.sourceID || item.Ref.ViewID != input.publication.viewID {
			return errors.New("installed standard MCP watcher search disclosed an item outside the selected View")
		}
		name, nameOK := uciInstalledAcceptanceGoFunctionName(item.Ref.EntityKey)
		if item.Path != input.relativePath || !nameOK || name != input.functionName {
			continue
		}
		if !uciInstalledAcceptanceIsBareSHA256(string(item.ContentDigest)) {
			return errors.New("installed standard MCP watcher canary has an invalid content digest")
		}
		found = true
	}
	if !found {
		return errUCIInstalledAcceptanceWatcherCanaryMissing
	}
	return nil
}

func uciInstalledAcceptancePublicationEvidenceFor(publication uciInstalledAcceptancePublication) uciInstalledAcceptancePublicationEvidence {
	return uciInstalledAcceptancePublicationEvidence{
		SourceDigest:   uciInstalledAcceptanceStringDigest(publication.sourceID),
		CheckoutDigest: uciInstalledAcceptanceStringDigest(publication.checkoutID),
		ViewDigest:     uciInstalledAcceptanceStringDigest(publication.viewID),
		RunDigest:      uciInstalledAcceptanceStringDigest(publication.runID),
		Generation:     publication.generation,
		FreshnessState: publication.freshnessState,
		BarrierState:   publication.barrierState,
	}
}
