package main

import (
	"bufio"
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
	uciInstalledAcceptanceParserCanaryRelativePath = "parser-canary.ts"
	uciInstalledAcceptanceParserCanarySource       = "export function InstalledParserCanary(): string {\n\treturn \"UCI_INSTALLED_PARSER_CANARY\"\n}\n"
	uciInstalledAcceptanceSchemaPrefix             = "uci_installed_"
)

var (
	errUCIInstalledAcceptanceNonTestPostgres      = errors.New("UCI installed acceptance requires an explicit loopback test PostgreSQL database")
	errUCIInstalledAcceptanceParserBoundary       = errors.New("UCI installed acceptance parser boundary is not observable through the installed runtime")
	errUCIInstalledAcceptanceBarrierBoundary      = errors.New("UCI installed acceptance read-your-save barrier is not observable through the installed runtime")
	errUCIInstalledAcceptanceWatcherCanaryMissing = errors.New("installed standard MCP watcher search omitted the canary")
	errUCIInstalledAcceptanceWatcherCanaryPresent = errors.New("installed standard MCP watcher delete still exposes the canary")
)

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
	Fixture                   uciInstalledAcceptanceFixture
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
	InstallHarnessVersion string
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

type uciInstalledAcceptanceWatcher struct {
	AfterWriteA  uciInstalledAcceptancePublicationEvidence
	AfterDeleteA uciInstalledAcceptancePublicationEvidence
	AfterWriteB  uciInstalledAcceptancePublicationEvidence
	AfterDeleteB uciInstalledAcceptancePublicationEvidence
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
	primaryRoot string
	linkedRoot  string
	head        string
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
func runUCIInstalledAcceptance(ctx context.Context, request uciInstalledAcceptanceRequest) (result uciInstalledAcceptanceResult, err error) {
	result = uciNewInstalledAcceptanceResult(request)
	if err := uciValidateInstalledAcceptanceRequest(ctx, request); err != nil {
		return result, err
	}

	operationCtx, cancelOperation := context.WithTimeout(ctx, request.OperationTimeout)
	defer cancelOperation()

	var installation *uciInstallHarnessInstallation
	var authority *uciInstalledAcceptanceAuthority
	var reservations []*uciInstalledAcceptanceReservation
	activeDaemonPID := 0
	daemonControlRoot := filepath.Join(request.LocalStateRoot, "temp")
	defer func() {
		daemonPID := activeDaemonPID
		if stopErr := uciStopInstalledAcceptanceDaemon(daemonControlRoot, daemonPID); stopErr != nil {
			err = errors.Join(err, stopErr)
		}
		if installation != nil && installation.tree != nil {
			if closeErr := installation.tree.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close installed acceptance process tree: %w", closeErr))
			}
		}
		daemonExited := true
		if daemonPID > 0 {
			if waitErr := uciWaitInstalledAcceptanceProcessExit(daemonPID, 5*time.Second); waitErr != nil {
				err = errors.Join(err, waitErr)
				daemonExited = false
			}
		}
		if installation != nil && daemonExited {
			if closeErr := installation.Close(); closeErr != nil {
				err = errors.Join(err, closeErr)
			} else {
				result.Cleanup.ProcessTreeClosed = true
			}
		}
		for _, reservation := range reservations {
			if reservation != nil && reservation.listener != nil {
				if closeErr := reservation.listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
					err = errors.Join(err, fmt.Errorf("close reserved UCI loopback port: %w", closeErr))
				}
			}
		}
		if authority != nil && daemonExited {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			cleanupErr := authority.Close(cleanupCtx)
			cancel()
			if cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
		} else if authority != nil {
			err = errors.Join(err, errors.New("installed acceptance schema retained because daemon exit is unconfirmed"))
		}
		if installation == nil || daemonExited {
			result.Cleanup.InstallRootRemoved = uciInstalledAcceptanceRemoveRoot(&err, request.InstallRoot)
			result.Cleanup.FixtureRootRemoved = uciInstalledAcceptanceRemoveRoot(&err, request.FixtureRoot)
			result.Cleanup.LocalStateRootRemoved = uciInstalledAcceptanceRemoveRoot(&err, request.LocalStateRoot)
		}
	}()

	if err := os.Mkdir(request.FixtureRoot, 0o700); err != nil {
		return result, fmt.Errorf("create installed acceptance fixture root: %w", err)
	}
	if err := os.Mkdir(request.LocalStateRoot, 0o700); err != nil {
		return result, fmt.Errorf("create installed acceptance local state root: %w", err)
	}

	candidates, err := uciBuildInstalledAcceptanceCandidates(operationCtx, request.CandidateSourceRoot, filepath.Join(request.FixtureRoot, "candidate-build"))
	if err != nil {
		return result, err
	}
	parserBundleDigest := uciInstalledAcceptanceParserBundleDigest()
	anchorProjectID := uuid.NewString()
	worktrees, worktreeResult, err := uciCreateInstalledAcceptanceWorktrees(operationCtx, request.FixtureRoot, request.Fixture, anchorProjectID)
	if err != nil {
		return result, err
	}
	result.Worktrees = worktreeResult

	authority, err = uciPrepareInstalledAcceptanceAuthority(operationCtx, request.TestPostgresDSN, worktrees, parserBundleDigest, anchorProjectID)
	if err != nil {
		return result, err
	}
	if err := uciAssertInstalledAcceptanceNoProjection(operationCtx, authority); err != nil {
		return result, err
	}
	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB} {
		result.Worktrees.Registered[client] = true
	}

	installResult, err := runUCIInstallHarness(operationCtx, uciInstallHarnessRequest{
		Version:          request.InstallHarnessVersion,
		Scenario:         uciInstallHarnessScenarioMaterialize,
		InstallRoot:      request.InstallRoot,
		Server:           candidates["server"],
		Daemon:           candidates["daemon"],
		Parser:           candidates["parser"],
		ReadinessTimeout: request.ReadinessTimeout,
	})
	if err != nil {
		return result, fmt.Errorf("materialize installed UCI candidates: %w", err)
	}
	installation = installResult.Installation
	if installation == nil {
		return result, errors.New("materialize installed UCI candidates returned no installation")
	}
	for _, role := range []string{"server", "daemon", "parser"} {
		candidateHash, hashErr := uciInstalledAcceptanceFileSHA256(candidates[role].Executable)
		if hashErr != nil {
			return result, hashErr
		}
		installedPath, pathErr := installation.Executable(role)
		if pathErr != nil {
			return result, pathErr
		}
		installedHash, hashErr := uciInstalledAcceptanceFileSHA256(installedPath)
		if hashErr != nil {
			return result, hashErr
		}
		if candidateHash != installedHash {
			return result, fmt.Errorf("installed %s artifact bytes do not match its candidate", role)
		}
		result.Artifacts[role] = uciInstalledAcceptanceArtifact{CandidateSHA256: candidateHash, InstalledSHA256: installedHash}
	}
	parserPath, err := installation.Executable("parser")
	if err != nil {
		return result, err
	}
	daemonPath, err := installation.Executable("daemon")
	if err != nil {
		return result, err
	}

	reservations, err = uciReserveInstalledAcceptanceLoopback(request.LoopbackHost, request.ReservedLoopbackPortCount)
	if err != nil {
		return result, err
	}
	result.ReservedLoopbackPorts = uciInstalledAcceptanceReservationPorts(reservations)
	serverPort := reservations[0].port
	if err := reservations[0].listener.Close(); err != nil {
		return result, fmt.Errorf("release reserved server loopback port: %w", err)
	}
	reservations[0].listener = nil

	serverEnvironment, clientEnvironment, err := uciInstalledAcceptanceEnvironment(request, authority, serverPort, parserBundleDigest, parserPath)
	if err != nil {
		return result, err
	}
	server, err := installation.Start(operationCtx, uciInstalledHarnessLaunchRequest{
		Role:        "server",
		Environment: serverEnvironment,
	})
	if err != nil {
		return result, err
	}
	result.Processes.ServerPID = server.command.Process.Pid
	readinessCtx, cancelReadiness := context.WithTimeout(operationCtx, request.ReadinessTimeout)
	if err := uciWaitForInstalledAcceptanceLoopback(readinessCtx, request.LoopbackHost, serverPort); err != nil {
		cancelReadiness()
		return result, err
	}
	cancelReadiness()

	clientAProcess, err := installation.Start(operationCtx, uciInstalledHarnessLaunchRequest{
		Role:             "daemon",
		WorkingDirectory: worktrees.primaryRoot,
		Environment:      clientEnvironment,
		WithStdio:        true,
	})
	if err != nil {
		return result, err
	}
	clientA, err := newUCIInstalledAcceptanceMCPClient(uciInstalledAcceptanceClientA, clientAProcess)
	if err != nil {
		return result, err
	}
	if err := clientA.InitializeAndList(operationCtx); err != nil {
		return result, err
	}
	result.Processes.DaemonPID, err = uciWaitForInstalledAcceptanceDaemonPID(operationCtx, filepath.Join(request.LocalStateRoot, "temp"), daemonPath)
	if err != nil {
		return result, err
	}
	activeDaemonPID = result.Processes.DaemonPID

	clientBProcess, err := installation.Start(operationCtx, uciInstalledHarnessLaunchRequest{
		Role:             "daemon",
		WorkingDirectory: worktrees.linkedRoot,
		Environment:      clientEnvironment,
		WithStdio:        true,
	})
	if err != nil {
		return result, err
	}
	clientB, err := newUCIInstalledAcceptanceMCPClient(uciInstalledAcceptanceClientB, clientBProcess)
	if err != nil {
		return result, err
	}
	if err := clientB.InitializeAndList(operationCtx); err != nil {
		return result, err
	}
	defer func() {
		result.ClientTranscripts[uciInstalledAcceptanceClientA] = clientA.Transcript()
		result.ClientTranscripts[uciInstalledAcceptanceClientB] = clientB.Transcript()
	}()
	result.SimultaneousAB = true
	if err := uciRequireInstalledAcceptanceTools(clientA.Transcript()); err != nil {
		return result, err
	}
	if err := uciRequireInstalledAcceptanceTools(clientB.Transcript()); err != nil {
		return result, err
	}

	selectedA, selectedB, err := uciSelectInstalledAcceptanceCheckouts(operationCtx, clientA, clientB, authority)
	if err != nil {
		return result, err
	}
	result.Bootstrap.InitialViewAbsent[uciInstalledAcceptanceClientA] = selectedA.viewID == ""
	result.Bootstrap.InitialViewAbsent[uciInstalledAcceptanceClientB] = selectedB.viewID == ""

	if !result.Bootstrap.InitialViewAbsent[uciInstalledAcceptanceClientA] || !result.Bootstrap.InitialViewAbsent[uciInstalledAcceptanceClientB] {
		return result, errors.New("installed UCI checkout selection was not View-free before the first index")
	}

	selectedA, selectedB, err = uciStartInstalledAcceptanceIndexes(operationCtx, clientA, clientB, selectedA, selectedB, worktrees)
	if err != nil {
		return result, err
	}
	result.Bootstrap.IndexStarted[uciInstalledAcceptanceClientA] = true
	result.Bootstrap.IndexStarted[uciInstalledAcceptanceClientB] = true
	publications, err := uciObserveInstalledAcceptancePublications(operationCtx, clientA, clientB, selectedA, selectedB, &result)
	if err != nil {
		return result, fmt.Errorf("%w: %v", errUCIInstalledAcceptanceBarrierBoundary, err)
	}

	parserPIDs := uciWaitForInstalledAcceptanceParserPIDs(operationCtx, installation, parserPath)
	if len(parserPIDs) == 0 {
		return result, fmt.Errorf("%w: codebase_index reached published Views without an installed parser process", errUCIInstalledAcceptanceParserBoundary)
	}
	parserCanarySource, err := uciRequireInstalledAcceptanceParserCanaryPublished(operationCtx, authority, worktrees, publications, parserBundleDigest)
	if err != nil {
		return result, fmt.Errorf("%w: %v", errUCIInstalledAcceptanceParserBoundary, err)
	}
	requestDigest, err := uci.TreeSitterWireRequestDigest(uciInstalledAcceptanceParserCanaryRequest(authority.profile.ProfileID, parserBundleDigest, parserCanarySource))
	if err != nil {
		return result, fmt.Errorf("%w: hash installed parser canary request: %v", errUCIInstalledAcceptanceParserBoundary, err)
	}
	parserRequestDigest, err := uciInstalledAcceptanceBareSHA256(string(requestDigest))
	if err != nil {
		return result, fmt.Errorf("%w: validate installed parser request digest: %v", errUCIInstalledAcceptanceParserBoundary, err)
	}
	result.Processes.ParserPID = parserPIDs[0]
	result.Parser = uciInstalledAcceptanceParserEvidence{
		UsedInstalledArtifact: true,
		InvocationCount:       len(parserPIDs),
		RequestDigest:         parserRequestDigest,
	}

	for _, item := range []struct {
		name           string
		client         *uciInstalledAcceptanceMCPClient
		selection      uciInstalledAcceptanceSelection
		expectedCallee string
	}{
		{uciInstalledAcceptanceClientA, clientA, selectedA, request.Fixture.PrimaryCallee},
		{uciInstalledAcceptanceClientB, clientB, selectedB, request.Fixture.LinkedCallee},
	} {
		observation, observationErr := uciObserveInstalledAcceptanceSearchGraphRead(operationCtx, item.client, item.selection, publications[item.name], request.Fixture, item.expectedCallee)
		if observationErr != nil {
			return result, fmt.Errorf("installed standard MCP public isolation for %s: %w", item.name, observationErr)
		}
		result.Observations[item.name] = observation
	}

	clientCProcess, err := installation.Start(operationCtx, uciInstalledHarnessLaunchRequest{
		Role:             "daemon",
		WorkingDirectory: worktrees.primaryRoot,
		Environment:      clientEnvironment,
		WithStdio:        true,
	})
	if err != nil {
		return result, err
	}
	clientC, err := newUCIInstalledAcceptanceMCPClient(uciInstalledAcceptanceClientC, clientCProcess)
	if err != nil {
		return result, err
	}
	defer func() {
		result.ClientTranscripts[uciInstalledAcceptanceClientC] = clientC.Transcript()
	}()
	if err := clientC.InitializeAndList(operationCtx); err != nil {
		return result, err
	}
	if err := uciRequireInstalledAcceptanceTools(clientC.Transcript()); err != nil {
		return result, err
	}
	if err := uciExerciseInstalledAcceptanceThirdClient(operationCtx, clientA, clientB, clientC, authority, &result); err != nil {
		return result, fmt.Errorf("installed standard MCP third-client default isolation: %w", err)
	}

	if err := uciExerciseInstalledAcceptanceRefusals(operationCtx, clientA, clientB, clientC, selectedA, selectedB, authority, &result); err != nil {
		return result, fmt.Errorf("installed standard MCP authorization-negative matrix: %w", err)
	}
	recorderProcess, err := installation.Start(operationCtx, uciInstalledHarnessLaunchRequest{
		Role:             "daemon",
		WorkingDirectory: worktrees.primaryRoot,
		Environment:      clientEnvironment,
		WithStdio:        true,
	})
	if err != nil {
		return result, err
	}
	recorderClient, err := newUCIInstalledAcceptanceMCPClient("client-recorder", recorderProcess)
	if err != nil {
		return result, err
	}
	if err := recorderClient.InitializeAndList(operationCtx); err != nil {
		return result, err
	}
	if err := uciRequireInstalledAcceptanceTools(recorderClient.Transcript()); err != nil {
		return result, err
	}
	recorderSelection, err := uciSelectInstalledAcceptanceCheckout(operationCtx, recorderClient, uciInstalledAcceptanceClientA, authority)
	if err != nil {
		return result, err
	}
	if err := uciExerciseInstalledAcceptanceRecorder(operationCtx, recorderClient, recorderSelection, authority, request.Fixture.SharedSymbol, &result); err != nil {
		return result, fmt.Errorf("installed standard MCP recorder matrix: %w", err)
	}
	if err := recorderProcess.closePipes(); err != nil {
		return result, fmt.Errorf("close installed recorder client: %w", err)
	}
	watcherPublications, watcherErr := uciExerciseInstalledAcceptanceWatcher(operationCtx, request.Fixture, worktrees, clientA, clientB, selectedA, selectedB, publications, &result)
	if watcherErr != nil {
		return result, fmt.Errorf("installed standard MCP watcher save-delete isolation: %w", watcherErr)
	}
	primaryWatcherPublication, primaryWatcherFound := watcherPublications[uciInstalledAcceptanceClientA]
	linkedWatcherPublication, linkedWatcherFound := watcherPublications[uciInstalledAcceptanceClientB]
	if !primaryWatcherFound || !linkedWatcherFound {
		return result, errors.New("installed standard MCP watcher did not retain both publication baselines")
	}
	publications = watcherPublications
	selectedA.viewID = primaryWatcherPublication.viewID
	selectedA.runID = primaryWatcherPublication.runID
	selectedB.viewID = linkedWatcherPublication.viewID
	selectedB.runID = linkedWatcherPublication.runID

	restartedInstallation, restartedDaemonPID, restartErr := uciRestartInstalledAcceptance(
		operationCtx,
		request,
		candidates,
		authority,
		worktrees,
		serverPort,
		parserBundleDigest,
		daemonControlRoot,
		installation,
		activeDaemonPID,
		map[string]*uciInstalledAcceptanceMCPClient{
			uciInstalledAcceptanceClientA: clientA,
			uciInstalledAcceptanceClientB: clientB,
			uciInstalledAcceptanceClientC: clientC,
		},
		map[string]uciInstalledAcceptanceSelection{
			uciInstalledAcceptanceClientA: selectedA,
			uciInstalledAcceptanceClientB: selectedB,
		},
		publications,
		&result,
	)
	if restartErr != nil {
		return result, fmt.Errorf("installed standard MCP restart matrix: %w", restartErr)
	}
	installation = restartedInstallation
	activeDaemonPID = restartedDaemonPID
	return result, nil
}

func uciNewInstalledAcceptanceResult(request uciInstalledAcceptanceRequest) uciInstalledAcceptanceResult {
	return uciInstalledAcceptanceResult{
		InstallHarnessVersion: request.InstallHarnessVersion,
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
	if err := uciValidateInstalledAcceptanceSourceRoot(request.CandidateSourceRoot); err != nil {
		return err
	}
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
	if err := uciValidateInstalledAcceptanceFixture(request.Fixture); err != nil {
		return err
	}
	return nil
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
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "uci-installed@example.test"},
		{"config", "user.name", "UCI Installed Acceptance"},
		{"add", "--", ".engram-project", fixture.RelativePath, uciInstalledAcceptanceParserCanaryRelativePath},
		{"commit", "-m", "create installed UCI fixture"},
		{"worktree", "add", "--detach", linkedRoot, "HEAD"},
	} {
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
	if err := os.WriteFile(filepath.Join(primaryRoot, fixture.RelativePath), []byte(fixture.PrimarySource), 0o600); err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, fmt.Errorf("write installed acceptance primary dirty source: %w", err)
	}
	if err := os.WriteFile(filepath.Join(linkedRoot, fixture.RelativePath), []byte(fixture.LinkedSource), 0o600); err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, fmt.Errorf("write installed acceptance linked dirty source: %w", err)
	}
	primaryHead, err := uciReadInstalledAcceptanceGit(ctx, primaryRoot, "rev-parse", "HEAD")
	if err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, err
	}
	linkedHead, err := uciReadInstalledAcceptanceGit(ctx, linkedRoot, "rev-parse", "HEAD")
	if err != nil {
		return uciInstalledAcceptanceWorktreesFixture{}, uciInstalledAcceptanceWorktrees{}, err
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
	return uciInstalledAcceptanceWorktreesFixture{primaryRoot: primaryRoot, linkedRoot: linkedRoot, head: primaryHead}, result, nil
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
	expiresAt := time.Now().UTC().Add(30 * time.Minute)
	tokens := gormdb.NewTokenStore(store)
	authority.token, err = tokens.CreateWithPrincipal(ctx,
		"uci-installed-"+strings.ReplaceAll(uuid.NewString(), "-", ""),
		string(tokenHash),
		authority.rawToken[len("engram_"):len("engram_")+8],
		"read-write",
		authority.principal,
		"agent",
		&expiresAt,
	)
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
	for client, root := range map[string]string{
		uciInstalledAcceptanceClientA: worktrees.primaryRoot,
		uciInstalledAcceptanceClientB: worktrees.linkedRoot,
	} {
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
		IgnorePolicyDigest:   "sha256:" + uciInstalledAcceptanceStringDigest("uci-installed-ignore-policy-v1"),
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

func uciAssertInstalledAcceptanceNoProjection(ctx context.Context, authority *uciInstalledAcceptanceAuthority) error {
	if authority == nil || authority.store == nil || authority.source == nil || authority.profile == nil {
		return errors.New("installed acceptance authority is incomplete")
	}
	checkoutIDs := make([]string, 0, len(authority.checkouts))
	for _, checkout := range authority.checkouts {
		if checkout == nil || checkout.CurrentViewID != nil {
			return errors.New("installed acceptance checkout unexpectedly has a current View")
		}
		checkoutIDs = append(checkoutIDs, checkout.CheckoutID)
	}
	if len(checkoutIDs) != 2 {
		return errors.New("installed acceptance did not register two checkouts")
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
	serverEnvironment := []string{
		"DATABASE_DSN=" + authority.scopedDSN,
		"ENGRAM_WORKER_HOST=" + request.LoopbackHost,
		"ENGRAM_WORKER_PORT=" + strconv.Itoa(serverPort),
		"ENGRAM_AUTH_ADMIN_TOKEN=" + adminToken,
		"ENGRAM_CODE_INTEL_ENABLED=true",
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

func uciStopInstalledAcceptanceDaemon(controlRoot string, verifiedPID int) error {
	if verifiedPID <= 0 {
		return nil
	}
	physicalRoot, err := uciInstalledAcceptancePhysicalPath(controlRoot)
	if err != nil {
		return fmt.Errorf("resolve installed acceptance daemon control root: %w", err)
	}
	controlPath := muxserverid.DaemonControlPath(physicalRoot, muxcoreNamespace)
	status, found := readMuxcoreDaemonStatusIdentity(controlPath)
	if !found || status.PID <= 0 || status.ShuttingDown {
		return nil
	}
	if status.PID != verifiedPID {
		return errors.New("installed acceptance daemon control does not match the verified daemon PID")
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
	controlPath := muxserverid.DaemonControlPath(controlRoot, muxcoreNamespace)
	markerPath := controlPath + ".marker.json"
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, found := readMuxcoreDaemonStatusIdentity(controlPath)
		if found && status.PID > 0 && !status.ShuttingDown {
			marker, markerErr := readMuxcoreDaemonVersionMarker(markerPath)
			switch {
			case markerErr == nil:
				if marker.PID != status.PID || marker.DaemonGeneration != status.DaemonGeneration {
					return 0, errors.New("installed acceptance daemon marker does not match its live control status")
				}
				matches, matchErr := uciInstalledAcceptanceSamePhysicalPath(marker.Exe, installedExecutable)
				if matchErr != nil {
					return 0, fmt.Errorf("verify installed acceptance daemon executable: %w", matchErr)
				}
				if !matches {
					return 0, errors.New("installed acceptance daemon marker does not name the materialized executable")
				}
				return status.PID, nil
			case !errors.Is(markerErr, os.ErrNotExist):
				return 0, fmt.Errorf("read installed acceptance daemon marker: %w", markerErr)
			}
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("wait for installed acceptance daemon election: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

type uciInstalledAcceptanceMCPClient struct {
	name       string
	process    *uciStartedInstallHarnessProcess
	writer     *bufio.Writer
	scanner    *bufio.Scanner
	mu         sync.Mutex
	nextID     uint64
	transcript uciInstalledAcceptanceClientTranscript
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

func uciInstalledAcceptanceStatusTool(ctx context.Context, client *uciInstalledAcceptanceMCPClient, arguments map[string]any) (json.RawMessage, error) {
	var lastErr error
	for range 3 {
		payload, err := client.Tool(ctx, "codebase_status", arguments)
		if err == nil {
			return payload, nil
		}
		lastErr = err
		var mcpErr *uciInstalledAcceptanceMCPError
		if !errors.As(err, &mcpErr) || mcpErr.detail != "CONTEXT_MISMATCH" {
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

func (client *uciInstalledAcceptanceMCPClient) Tool(ctx context.Context, name string, arguments any) (json.RawMessage, error) {
	result, err := client.ToolWithCall(ctx, name, arguments)
	if err != nil {
		return nil, err
	}
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
	raw, request, err := client.call(ctx, "tools/call", json.RawMessage(params))
	call := uciInstalledAcceptanceMCPToolCall{client: client.name, name: name, request: request}
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{call: call}, err
	}
	return uciInstalledAcceptanceDecodeToolResult(raw, call)
}

// RetryTool sends the byte-identical prior tools/call frame with the same
// JSON-RPC ID. It deliberately does not advance the client's ordinary ID
// sequence, which remains monotonic for every non-retry call.
func (client *uciInstalledAcceptanceMCPClient) RetryTool(ctx context.Context, call uciInstalledAcceptanceMCPToolCall) (uciInstalledAcceptanceMCPToolResult, error) {
	if call.client != client.name || call.name == "" || call.request.method != "tools/call" {
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
	if prior.client != client.name || prior.request.method != "tools/call" || prior.request.id == "" {
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

func uciInstalledAcceptanceDecodeToolResult(raw json.RawMessage, call uciInstalledAcceptanceMCPToolCall) (uciInstalledAcceptanceMCPToolResult, error) {
	decodeEnvelope := func(encoded json.RawMessage) (json.RawMessage, bool, bool, error) {
		var envelope struct {
			Content []json.RawMessage `json:"content"`
			IsError bool              `json:"isError"`
		}
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

	payload, isError, wrapped, err := decodeEnvelope(raw)
	if err != nil {
		return uciInstalledAcceptanceMCPToolResult{}, err
	}
	if !wrapped {
		return uciInstalledAcceptanceMCPToolResult{}, errors.New("installed standard MCP tools/call response is invalid")
	}
	for depth := 0; depth < 2; depth++ {
		nested, nestedError, nestedWrapped, nestedErr := decodeEnvelope(payload)
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

func (client *uciInstalledAcceptanceMCPClient) readResponse(ctx context.Context, wantID, method string) (json.RawMessage, error) {
	type responseResult struct {
		payload json.RawMessage
		err     error
	}
	responses := make(chan responseResult, 1)
	go func() {
		for client.scanner.Scan() {
			var frame struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Result  json.RawMessage `json:"result"`
				Error   *struct {
					Code    json.RawMessage `json:"code"`
					Message string          `json:"message"`
					Data    json.RawMessage `json:"data"`
				} `json:"error"`
			}
			if err := json.Unmarshal(client.scanner.Bytes(), &frame); err != nil {
				continue
			}
			var id string
			if err := json.Unmarshal(frame.ID, &id); err != nil || id != wantID {
				continue
			}
			if frame.JSONRPC != "2.0" {
				responses <- responseResult{err: errors.New("installed standard MCP response has an invalid protocol")}
				return
			}
			if frame.Error != nil {
				code := uciInstalledAcceptanceErrorCode(frame.Error.Code)
				if code == "" {
					code = uciInstalledAcceptancePublicErrorCode(frame.Error.Data)
				}
				detail := uciInstalledAcceptancePublicErrorDetail(frame.Error.Data)
				if detail == "" || strings.HasPrefix(detail, "JSON ") || detail == "unknown JSON payload" {
					detail = uciInstalledAcceptanceSafeErrorDetail(frame.Error.Message)
				}
				responses <- responseResult{err: &uciInstalledAcceptanceMCPError{method: method, code: code, detail: detail}}
				return
			}
			responses <- responseResult{payload: append(json.RawMessage(nil), frame.Result...)}
			return
		}
		if err := client.scanner.Err(); err != nil {
			responses <- responseResult{err: fmt.Errorf("read installed standard MCP %s: %w", method, err)}
			return
		}
		responses <- responseResult{err: errors.New("installed standard MCP closed before its response")}
	}()
	select {
	case response := <-responses:
		return response.payload, response.err
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for installed standard MCP %s: %w", method, ctx.Err())
	}
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

func uciSelectInstalledAcceptanceCheckouts(ctx context.Context, first, second *uciInstalledAcceptanceMCPClient, authority *uciInstalledAcceptanceAuthority) (uciInstalledAcceptanceSelection, uciInstalledAcceptanceSelection, error) {
	if authority == nil || authority.source == nil || authority.profile == nil {
		return uciInstalledAcceptanceSelection{}, uciInstalledAcceptanceSelection{}, errors.New("installed acceptance UCI authority is incomplete")
	}
	type selected struct {
		client string
		value  uciInstalledAcceptanceSelection
		err    error
	}
	results := make(chan selected, 2)
	for _, item := range []struct {
		clientName string
		client     *uciInstalledAcceptanceMCPClient
	}{
		{uciInstalledAcceptanceClientA, first},
		{uciInstalledAcceptanceClientB, second},
	} {
		go func(item struct {
			clientName string
			client     *uciInstalledAcceptanceMCPClient
		},
		) {
			checkout := authority.checkouts[item.clientName]
			if checkout == nil {
				results <- selected{client: item.clientName, err: errors.New("installed acceptance checkout is unavailable")}
				return
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
				results <- selected{client: item.clientName, err: err}
				return
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
				results <- selected{client: item.clientName, err: errors.New("installed standard MCP checkout selection response is invalid")}
				return
			}
			if response.Context != nil && response.Context.ViewID != "" {
				response.ViewID = response.Context.ViewID
			}
			results <- selected{client: item.clientName, value: uciInstalledAcceptanceSelection{contextHandle: response.ContextHandle, viewID: response.ViewID}}
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
		if checkout == nil || !found || publication.sourceID != authority.source.SourceID || publication.checkoutID != checkout.CheckoutID || publication.profileID != authority.profile.ProfileID || publication.viewID == "" {
			return nil, errors.New("installed parser canary publication identity is incomplete")
		}
		source, err := os.ReadFile(filepath.Join(roots[client], uciInstalledAcceptanceParserCanaryRelativePath))
		if err != nil || strings.ReplaceAll(string(source), "\r\n", "\n") != uciInstalledAcceptanceParserCanarySource {
			return nil, errors.New("installed parser canary worktree bytes are invalid")
		}
		if client == uciInstalledAcceptanceClientA {
			primarySource = append([]byte(nil), source...)
		}
		contentDigest := "sha256:" + uciInstalledAcceptanceStringDigest(string(source))
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
	}
	if len(primarySource) == 0 {
		return nil, errors.New("installed parser canary primary request is unavailable")
	}
	return primarySource, nil
}

func uciInstalledAcceptanceBareSHA256(value string) (string, error) {
	const prefix = "sha256:"
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
	uciInstalledAcceptanceBarrierWaitMS          int64 = 30_000
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
		if status.status == "running" {
			observations = 0
		} else {
			publication, err := uciInstalledAcceptanceQuiescentPublication(status, barrier)
			if err != nil {
				return uciInstalledAcceptancePublication{}, err
			}
			if observations == 0 || !uciInstalledAcceptanceSamePublicationTuple(candidate, publication) {
				candidate = publication
				observations = 1
			} else {
				observations++
			}
			if observations >= uciInstalledAcceptanceQuiescenceObservations {
				return candidate, nil
			}
		}

		select {
		case <-ctx.Done():
			return uciInstalledAcceptancePublication{}, ctx.Err()
		case <-ticker.C:
		}
	}
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
	publication, err := uciInstalledAcceptanceStatusPublication(status, selection)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	if status.status != "idle" || status.freshness == nil || status.freshness.state != "observed_current" || status.freshness.barrier == nil ||
		status.freshness.barrier.state != "satisfied" || status.freshness.barrier.deadlineMS < 1 ||
		(status.freshness.barrier.scope.kind != "paths" && status.freshness.barrier.scope.kind != "paths_with_hashes") || status.freshness.barrier.scope.pathCount < 1 {
		return uciInstalledAcceptancePublication{}, errors.New("installed standard MCP after_barrier did not return satisfied target freshness")
	}
	publication.freshnessState = status.freshness.state
	publication.barrierState = status.freshness.barrier.state
	publication.evidenceRecorder = status.evidenceRecorder.state
	return publication, nil
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
	searchPayload, err := client.Tool(ctx, "codebase_search", map[string]any{
		"context_handle": selection.contextHandle,
		"query":          fixture.SharedSymbol,
		"path_prefix":    "pkg",
		"limit":          10,
	})
	if err != nil {
		return uciInstalledAcceptanceObservations{}, err
	}
	search, err := uciDecodeInstalledAcceptanceQuery(searchPayload)
	if err != nil {
		return uciInstalledAcceptanceObservations{}, err
	}
	if (search.Status != uci.QueryStatusOK && search.Status != uci.QueryStatusPartial) || !uciInstalledAcceptanceQueryMatchesPublication(search, publication) || search.Items == nil {
		return uciInstalledAcceptanceObservations{}, fmt.Errorf("installed standard MCP search result is not selected-View evidence: status=%s", search.Status)
	}
	var searchItem *uci.QueryItem
	for index := range *search.Items {
		item := &(*search.Items)[index]
		if item.Ref.SourceID != publication.sourceID || item.Ref.ViewID != publication.viewID {
			return uciInstalledAcceptanceObservations{}, errors.New("installed standard MCP search disclosed an item outside the selected View")
		}
		caller, callerOK := uciInstalledAcceptanceGoFunctionName(item.Ref.EntityKey)
		if item.Path != fixture.RelativePath || !callerOK || caller != fixture.SharedSymbol {
			continue
		}
		if !uciInstalledAcceptanceIsBareSHA256(string(item.ContentDigest)) {
			return uciInstalledAcceptanceObservations{}, errors.New("installed standard MCP search citation has an invalid content digest")
		}
		if searchItem != nil && searchItem.ContentDigest != item.ContentDigest {
			return uciInstalledAcceptanceObservations{}, errors.New("installed standard MCP search returned conflicting SharedTarget artifacts")
		}
		if searchItem == nil || item.Span.ByteEnd-item.Span.ByteStart < searchItem.Span.ByteEnd-searchItem.Span.ByteStart {
			searchItem = item
		}
	}
	if searchItem == nil {
		return uciInstalledAcceptanceObservations{}, errors.New("installed standard MCP search omitted the qualified SharedTarget citation")
	}

	caller, callerOK := uciInstalledAcceptanceGoFunctionName(searchItem.Ref.EntityKey)
	if !callerOK || caller != fixture.SharedSymbol {
		return uciInstalledAcceptanceObservations{}, errors.New("installed standard MCP search did not cite the qualified SharedTarget entity")
	}
	graphPayload, err := client.Tool(ctx, "codebase_graph", map[string]any{
		"context_handle": selection.contextHandle,
		"action":         "neighbors",
		"target": map[string]any{
			"source_id":  publication.sourceID,
			"view_id":    publication.viewID,
			"entity_key": searchItem.Ref.EntityKey,
		},
		"direction":   "outgoing",
		"max_depth":   4,
		"max_visited": 64,
		"max_nodes":   32,
		"max_edges":   64,
		"deadline_ms": 30_000,
	})
	if err != nil {
		return uciInstalledAcceptanceObservations{}, err
	}
	graphResponse, err := uciDecodeInstalledAcceptanceQuery(graphPayload)
	if err != nil {
		return uciInstalledAcceptanceObservations{}, err
	}
	if graphResponse.Status != uci.QueryStatusOK || !uciInstalledAcceptanceQueryMatchesPublication(graphResponse, publication) || graphResponse.Graph == nil {
		return uciInstalledAcceptanceObservations{}, errors.New("installed standard MCP graph did not return the selected View")
	}
	callee := ""
	for _, edge := range graphResponse.Graph.Edges {
		if string(edge.Relation) != "calls" || edge.From.SourceID != publication.sourceID || edge.From.ViewID != publication.viewID || edge.From.EntityKey != searchItem.Ref.EntityKey {
			continue
		}
		if edge.To.SourceID != publication.sourceID || edge.To.ViewID != publication.viewID {
			return uciInstalledAcceptanceObservations{}, errors.New("installed standard MCP graph calls edge left the selected View")
		}
		calleeName, calleeOK := uciInstalledAcceptanceGoFunctionName(edge.To.EntityKey)
		if !calleeOK || calleeName != expectedCallee {
			return uciInstalledAcceptanceObservations{}, errors.New("installed standard MCP graph calls edge did not resolve the expected fixture callee")
		}
		if callee != "" {
			return uciInstalledAcceptanceObservations{}, errors.New("installed standard MCP graph returned more than one SharedTarget calls edge")
		}
		callee = calleeName
	}
	if callee == "" {
		return uciInstalledAcceptanceObservations{}, errors.New("installed standard MCP graph omitted the SharedTarget calls edge")
	}

	readPayload, err := client.Tool(ctx, "codebase_read", map[string]any{
		"context_handle": selection.contextHandle,
		"ref": map[string]any{
			"source_id":  searchItem.Ref.SourceID,
			"view_id":    searchItem.Ref.ViewID,
			"entity_key": searchItem.Ref.EntityKey,
		},
		"span": map[string]any{
			"byte_start": searchItem.Span.ByteStart,
			"byte_end":   searchItem.Span.ByteEnd,
			"line_start": searchItem.Span.LineStart,
			"line_end":   searchItem.Span.LineEnd,
		},
		"content_digest":      string(searchItem.ContentDigest),
		"verify_working_copy": false,
		"max_bytes":           8_192,
	})
	if err != nil {
		return uciInstalledAcceptanceObservations{}, err
	}
	read, err := uciDecodeInstalledAcceptanceQuery(readPayload)
	if err != nil {
		return uciInstalledAcceptanceObservations{}, err
	}
	if read.Status != uci.QueryStatusOK || !uciInstalledAcceptanceQueryMatchesPublication(read, publication) || read.Items == nil || len(*read.Items) != 1 {
		return uciInstalledAcceptanceObservations{}, errors.New("installed standard MCP read did not return one exact selected-View artifact")
	}
	readItem := (*read.Items)[0]
	if readItem.Ref != searchItem.Ref || readItem.Span != searchItem.Span || readItem.ContentDigest != searchItem.ContentDigest {
		return uciInstalledAcceptanceObservations{}, errors.New("installed standard MCP read did not preserve the search citation")
	}
	return uciInstalledAcceptanceObservations{
		SearchArtifactDigests: []string{string(searchItem.ContentDigest)},
		GraphCalleeDigests:    []string{uciInstalledAcceptanceStringDigest(callee)},
		ReadArtifactDigests:   []string{string(readItem.ContentDigest)},
	}, nil
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

func uciExerciseInstalledAcceptanceRefusals(
	ctx context.Context,
	first, second, third *uciInstalledAcceptanceMCPClient,
	firstSelection, secondSelection uciInstalledAcceptanceSelection,
	authority *uciInstalledAcceptanceAuthority,
	result *uciInstalledAcceptanceResult,
) error {
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

func uciExerciseInstalledAcceptanceRecorder(
	ctx context.Context,
	client *uciInstalledAcceptanceMCPClient,
	selection uciInstalledAcceptanceSelection,
	authority *uciInstalledAcceptanceAuthority,
	mismatchQuery string,
	result *uciInstalledAcceptanceResult,
) (retErr error) {
	if client == nil || selection.contextHandle == "" || mismatchQuery == "" || result == nil {
		return errors.New("installed acceptance recorder matrix is incomplete")
	}
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

	unavailable, err := client.ToolWithCall(ctx, "codebase_search", uciInstalledAcceptanceSearchArguments(selection.contextHandle, "recorder-unavailable"))
	if err != nil {
		return err
	}
	if unavailable.isError {
		return errors.New("installed standard MCP recorder failure returned a protocol-level tool error")
	}
	result.Recorder.InitialUnavailable, err = uciDecodeInstalledAcceptanceClosedOutcome(unavailable.payload)
	if err != nil {
		return fmt.Errorf("decode initial exposure-unavailable response: %w", err)
	}
	if result.Recorder.InitialUnavailable.Status != "unavailable" || result.Recorder.InitialUnavailable.ErrorCode != "EXPOSURE_UNAVAILABLE" {
		return errors.New("installed standard MCP recorder failure did not return EXPOSURE_UNAVAILABLE")
	}
	afterUnavailable, err := uciInstalledAcceptanceExposureCount(ctx, authority)
	if err != nil {
		return err
	}
	if afterUnavailable != beforeUnavailable {
		return errors.New("installed standard MCP recorder failure appended UCI exposure evidence")
	}
	statusAfterUnavailable, err := uciInstalledAcceptanceStatusForSelection(ctx, client, selection)
	if err != nil {
		return err
	}
	result.Recorder.HealthAfterInitialUnavailable = statusAfterUnavailable.evidenceRecorder.state
	if statusAfterUnavailable.evidenceRecorder.state != "unavailable" || statusAfterUnavailable.evidenceRecorder.lastFailureCode != "EXPOSURE_UNAVAILABLE" {
		return errors.New("installed standard MCP recorder failure did not make health unavailable")
	}
	if err := fault.Close(); err != nil {
		return err
	}

	first, err := client.ToolWithCall(ctx, "codebase_search", uciInstalledAcceptanceSearchArguments(selection.contextHandle, "recorder-exact-retry"))
	if err != nil {
		return err
	}
	if first.isError {
		return errors.New("installed standard MCP recorder success returned a protocol-level tool error")
	}
	firstExposure, err := uciInstalledAcceptanceExposureReference(first.payload)
	if err != nil {
		return err
	}
	result.Recorder.FirstExposureDigest = uciInstalledAcceptanceStringDigest(firstExposure)
	afterFirst, err := uciInstalledAcceptanceExposureCount(ctx, authority)
	if err != nil {
		return err
	}
	if afterFirst != afterUnavailable+1 {
		return errors.New("installed standard MCP recorder success did not append one UCI exposure")
	}

	retry, err := client.RetryTool(ctx, first.call)
	if err != nil {
		return err
	}
	if retry.isError {
		return errors.New("installed standard MCP recorder exact retry returned a protocol-level tool error")
	}
	retryExposure, err := uciInstalledAcceptanceExposureReference(retry.payload)
	if err != nil {
		return err
	}
	if retryExposure != firstExposure {
		return errors.New("installed standard MCP recorder exact retry changed its exposure reference")
	}
	result.Recorder.ExactRetryExposureDigest = uciInstalledAcceptanceStringDigest(retryExposure)
	afterRetry, err := uciInstalledAcceptanceExposureCount(ctx, authority)
	if err != nil {
		return err
	}
	if afterRetry != afterFirst {
		return errors.New("installed standard MCP recorder exact retry appended a second UCI exposure")
	}

	statusBeforeMismatch, err := uciInstalledAcceptanceStatusForSelection(ctx, client, selection)
	if err != nil {
		return err
	}
	result.Recorder.HealthBeforeMismatch = statusBeforeMismatch.evidenceRecorder.state
	if statusBeforeMismatch.evidenceRecorder.state != "healthy" || statusBeforeMismatch.evidenceRecorder.lastFailureCode != "NONE" {
		return errors.New("installed standard MCP recorder did not recover healthy state")
	}

	mismatch, err := client.ReplayToolWithSameJSONRPCID(ctx, first.call, "codebase_search", uciInstalledAcceptanceSearchArguments(selection.contextHandle, mismatchQuery))
	if err != nil {
		return err
	}
	if mismatch.isError {
		return errors.New("installed standard MCP recorder mismatch returned a protocol-level tool error")
	}
	afterMismatch, err := uciInstalledAcceptanceExposureCount(ctx, authority)
	if err != nil {
		return err
	}
	if afterMismatch != afterRetry {
		return errors.New("installed standard MCP recorder mismatch appended UCI exposure evidence under a different idempotency key")
	}
	result.Recorder.Mismatch, err = uciDecodeInstalledAcceptanceClosedOutcome(mismatch.payload)
	if err != nil {
		return fmt.Errorf("decode exposure idempotency-mismatch response: %w", err)
	}
	if result.Recorder.Mismatch.Status != "unavailable" || result.Recorder.Mismatch.ErrorCode != "IDEMPOTENCY_MISMATCH" {
		return errors.New("installed standard MCP recorder mismatch did not return IDEMPOTENCY_MISMATCH")
	}
	statusAfterMismatch, err := uciInstalledAcceptanceStatusForSelection(ctx, client, selection)
	if err != nil {
		return err
	}
	result.Recorder.HealthAfterMismatch = statusAfterMismatch.evidenceRecorder.state
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
	if (response.Status != uci.QueryStatusOK && response.Status != uci.QueryStatusPartial) || response.Exposure == nil || response.Exposure.ExposureRef == "" {
		return "", errors.New("installed standard MCP recorder success has no exposure reference")
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
		return "view_delta=unavailable"
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
		return "view_delta=unavailable"
	}
	byID := make(map[string]viewRow, 2)
	for _, row := range rows {
		byID[row.ViewID] = row
	}
	before, beforeOK := byID[beforeViewID]
	after, afterOK := byID[afterViewID]
	if !beforeOK || !afterOK {
		return "view_delta=unavailable"
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
		if status.status == "idle" && status.context != nil && status.freshness != nil && status.freshness.state == "observed_current" && status.freshness.pendingChanges != nil && *status.freshness.pendingChanges == 0 {
			publication := uciInstalledAcceptancePublication{
				sourceID:         status.context.sourceID,
				checkoutID:       status.context.checkoutID,
				viewID:           status.context.viewID,
				profileID:        status.context.profileID,
				generation:       status.context.generation,
				runID:            status.runID,
				freshnessState:   status.freshness.state,
				evidenceRecorder: status.evidenceRecorder.state,
			}
			if !uciInstalledAcceptanceSameViewPublication(publication, expected) {
				return uciInstalledAcceptancePublication{}, errors.New("installed standard MCP restart selected a changed View")
			}
			if observations == 0 || !uciInstalledAcceptanceSameViewPublication(candidate, publication) || candidate.runID != publication.runID {
				candidate = publication
				observations = 1
			} else {
				observations++
			}
			if observations >= uciInstalledAcceptanceQuiescenceObservations {
				return candidate, nil
			}
		} else {
			observations = 0
		}
		select {
		case <-ctx.Done():
			return uciInstalledAcceptancePublication{}, fmt.Errorf("wait for installed acceptance restart quiescence: %w", ctx.Err())
		case <-ticker.C:
		}
	}
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

func uciRestartInstalledAcceptance(
	ctx context.Context,
	request uciInstalledAcceptanceRequest,
	candidates map[string]uciInstallHarnessCommand,
	authority *uciInstalledAcceptanceAuthority,
	worktrees uciInstalledAcceptanceWorktreesFixture,
	serverPort int,
	parserBundleDigest, daemonControlRoot string,
	oldInstallation *uciInstallHarnessInstallation,
	oldDaemonPID int,
	oldClients map[string]*uciInstalledAcceptanceMCPClient,
	selections map[string]uciInstalledAcceptanceSelection,
	publications map[string]uciInstalledAcceptancePublication,
	result *uciInstalledAcceptanceResult,
) (next *uciInstallHarnessInstallation, newDaemonPID int, retErr error) {
	if oldInstallation == nil || authority == nil || result == nil || oldDaemonPID <= 0 || result.Processes.DaemonPID != oldDaemonPID || result.Processes.ServerPID <= 0 || result.Processes.ParserPID <= 0 {
		return nil, 0, errors.New("installed acceptance restart baseline is incomplete")
	}
	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB, uciInstalledAcceptanceClientC} {
		if oldClients[client] == nil || oldClients[client].process == nil {
			return nil, 0, errors.New("installed acceptance restart client is incomplete")
		}
	}
	if result.Restart.BeforeClientContexts == nil {
		result.Restart.BeforeClientContexts = make(map[string]uciInstalledAcceptanceContext)
	}
	if result.Restart.BeforeObservations == nil {
		result.Restart.BeforeObservations = make(map[string]uciInstalledAcceptanceObservations)
	}
	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB} {
		publication, found := publications[client]
		if !found || selections[client].contextHandle == "" || publication.sourceID == "" || publication.checkoutID == "" || publication.viewID == "" || publication.profileID == "" || publication.generation < 1 || publication.runID == "" {
			return nil, 0, errors.New("installed acceptance restart selected publication is incomplete")
		}
		observation, found := result.Observations[client]
		if !found {
			return nil, 0, errors.New("installed acceptance restart has no observation baseline")
		}
		result.Restart.BeforeClientContexts[client] = uciInstalledAcceptanceContextForPublication(publication)
		result.Restart.BeforeObservations[client] = uciInstalledAcceptanceCloneObservations(observation)
	}

	result.Restart.BeforeProcesses = result.Processes
	beforeCounts, err := uciInstalledAcceptanceProjectionCountsForAuthority(ctx, authority)
	if err != nil {
		return nil, 0, err
	}
	result.Restart.BeforeProjectionCounts = beforeCounts

	var shutdownErrors []error
	for _, client := range []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB, uciInstalledAcceptanceClientC} {
		if closeErr := oldClients[client].process.closePipes(); closeErr != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("close installed acceptance %s stdio: %w", client, closeErr))
		}
	}
	if stopErr := uciStopInstalledAcceptanceDaemon(daemonControlRoot, oldDaemonPID); stopErr != nil {
		shutdownErrors = append(shutdownErrors, stopErr)
	}
	if waitErr := uciWaitInstalledAcceptanceProcessExit(oldDaemonPID, 5*time.Second); waitErr != nil {
		shutdownErrors = append(shutdownErrors, waitErr)
	}
	if closeErr := oldInstallation.Close(); closeErr != nil {
		shutdownErrors = append(shutdownErrors, closeErr)
	}
	if shutdownErr := errors.Join(shutdownErrors...); shutdownErr != nil {
		return nil, 0, shutdownErr
	}
	if err := uciWaitForInstalledAcceptanceLoopbackRelease(ctx, request.LoopbackHost, serverPort); err != nil {
		return nil, 0, err
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
		return nil, 0, fmt.Errorf("rematerialize installed UCI candidates: %w", err)
	}
	next = installResult.Installation
	if next == nil {
		return nil, 0, errors.New("rematerialize installed UCI candidates returned no installation")
	}
	defer func() {
		if retErr == nil || next == nil {
			return
		}
		var cleanupErrors []error
		if newDaemonPID > 0 {
			if stopErr := uciStopInstalledAcceptanceDaemon(daemonControlRoot, newDaemonPID); stopErr != nil {
				cleanupErrors = append(cleanupErrors, stopErr)
			}
			if waitErr := uciWaitInstalledAcceptanceProcessExit(newDaemonPID, 5*time.Second); waitErr != nil {
				cleanupErrors = append(cleanupErrors, waitErr)
			}
		}
		if closeErr := next.Close(); closeErr != nil {
			cleanupErrors = append(cleanupErrors, closeErr)
		}
		retErr = errors.Join(retErr, errors.Join(cleanupErrors...))
		next = nil
		newDaemonPID = 0
	}()

	installedPaths, err := uciVerifyInstalledAcceptanceRestartArtifacts(candidates, next, result.Artifacts)
	if err != nil {
		return nil, 0, err
	}
	serverEnvironment, clientEnvironment, err := uciInstalledAcceptanceEnvironment(request, authority, serverPort, parserBundleDigest, installedPaths["parser"])
	if err != nil {
		return nil, 0, err
	}
	server, err := next.Start(ctx, uciInstalledHarnessLaunchRequest{Role: "server", Environment: serverEnvironment})
	if err != nil {
		return nil, 0, err
	}
	if server == nil || server.command == nil || server.command.Process == nil || server.command.Process.Pid <= 0 {
		return nil, 0, errors.New("restarted installed server has no process")
	}
	restartedServerPID := server.command.Process.Pid
	readinessCtx, cancelReadiness := context.WithTimeout(ctx, request.ReadinessTimeout)
	if err := uciWaitForInstalledAcceptanceLoopback(readinessCtx, request.LoopbackHost, serverPort); err != nil {
		cancelReadiness()
		return nil, 0, err
	}
	cancelReadiness()

	restartedClients := make(map[string]*uciInstalledAcceptanceMCPClient, 3)
	defer func() {
		for name, client := range restartedClients {
			result.Restart.ClientTranscripts[name] = client.Transcript()
		}
	}()
	startClient := func(name, workingDirectory string) (*uciInstalledAcceptanceMCPClient, error) {
		process, startErr := next.Start(ctx, uciInstalledHarnessLaunchRequest{
			Role:             "daemon",
			WorkingDirectory: workingDirectory,
			Environment:      clientEnvironment,
			WithStdio:        true,
		})
		if startErr != nil {
			return nil, startErr
		}
		client, clientErr := newUCIInstalledAcceptanceMCPClient(name, process)
		if clientErr != nil {
			return nil, clientErr
		}
		if initErr := client.InitializeAndList(ctx); initErr != nil {
			return nil, initErr
		}
		if toolErr := uciRequireInstalledAcceptanceTools(client.Transcript()); toolErr != nil {
			return nil, toolErr
		}
		restartedClients[name] = client
		return client, nil
	}

	first, err := startClient(uciInstalledAcceptanceClientA, worktrees.primaryRoot)
	if err != nil {
		return nil, 0, err
	}
	newDaemonPID, err = uciWaitForInstalledAcceptanceDaemonPID(ctx, daemonControlRoot, installedPaths["daemon"])
	if err != nil {
		return nil, 0, err
	}
	if newDaemonPID == oldDaemonPID {
		return nil, 0, errors.New("restarted installed daemon retained its previous PID")
	}
	second, err := startClient(uciInstalledAcceptanceClientB, worktrees.linkedRoot)
	if err != nil {
		return nil, 0, err
	}
	third, err := startClient(uciInstalledAcceptanceClientC, worktrees.primaryRoot)
	if err != nil {
		return nil, 0, err
	}

	firstSelection, secondSelection, err := uciSelectInstalledAcceptanceCheckouts(ctx, first, second, authority)
	if err != nil {
		return nil, 0, err
	}
	thirdSelection, err := uciSelectInstalledAcceptanceCheckout(ctx, third, uciInstalledAcceptanceClientA, authority)
	if err != nil {
		return nil, 0, err
	}
	if firstSelection.viewID == "" || secondSelection.viewID == "" || thirdSelection.viewID == "" {
		return nil, 0, errors.New("restarted installed clients did not select published Views")
	}

	primaryBefore, primaryFound := publications[uciInstalledAcceptanceClientA]
	linkedBefore, linkedFound := publications[uciInstalledAcceptanceClientB]
	if !primaryFound || !linkedFound {
		return nil, 0, errors.New("installed acceptance restart publication baseline is incomplete")
	}
	firstSelection, secondSelection, err = uciStartInstalledAcceptanceIndexes(ctx, first, second, firstSelection, secondSelection, worktrees)
	if err != nil {
		return nil, 0, err
	}
	restartedPublications := make(map[string]uciInstalledAcceptancePublication, 3)
	for _, item := range []struct {
		name      string
		client    *uciInstalledAcceptanceMCPClient
		selection uciInstalledAcceptanceSelection
		expected  uciInstalledAcceptancePublication
	}{
		{name: uciInstalledAcceptanceClientA, client: first, selection: firstSelection, expected: primaryBefore},
		{name: uciInstalledAcceptanceClientB, client: second, selection: secondSelection, expected: linkedBefore},
	} {
		barrier, waitErr := uciWaitForInstalledAcceptanceBarrier(ctx, item.client, item.selection)
		if waitErr != nil {
			return nil, 0, waitErr
		}
		publication, waitErr := uciWaitForInstalledAcceptanceQuiescence(ctx, item.client, item.selection, barrier)
		if waitErr != nil {
			return nil, 0, waitErr
		}
		contextRef := uciInstalledAcceptanceContextForPublication(publication)
		if !uciInstalledAcceptanceSameViewPublication(publication, item.expected) {
			delta := uciInstalledAcceptanceViewDelta(ctx, authority, item.expected.viewID, publication.viewID)
			return nil, 0, fmt.Errorf("restarted installed client %s advanced from generation %d/view %s to generation %d/view %s (%s)", item.name, item.expected.generation, uciInstalledAcceptanceStringDigest(item.expected.viewID), publication.generation, uciInstalledAcceptanceStringDigest(publication.viewID), delta)
		}
		if before := result.Restart.BeforeClientContexts[item.name]; before != contextRef {
			return nil, 0, fmt.Errorf("restarted installed client %s context digest changed", item.name)
		}
		restartedPublications[item.name] = publication
		result.Restart.ClientContexts[item.name] = contextRef
	}
	thirdPublication, err := uciWaitForInstalledAcceptanceRestartQuiescence(ctx, third, thirdSelection, primaryBefore)
	if err != nil {
		return nil, 0, err
	}
	restartedPublications[uciInstalledAcceptanceClientC] = thirdPublication
	result.Restart.ClientContexts[uciInstalledAcceptanceClientC] = uciInstalledAcceptanceContextForPublication(thirdPublication)

	restartedParserPID, err := uciWaitForInstalledAcceptanceRestartParserPID(ctx, next, installedPaths["parser"], result.Processes.ParserPID)
	if err != nil {
		return nil, 0, err
	}
	afterProcesses := uciInstalledAcceptanceProcesses{
		ServerPID: restartedServerPID,
		DaemonPID: newDaemonPID,
		ParserPID: restartedParserPID,
	}
	if afterProcesses.ServerPID == result.Processes.ServerPID || afterProcesses.DaemonPID == result.Processes.DaemonPID || afterProcesses.ParserPID == result.Processes.ParserPID {
		return nil, 0, errors.New("restarted installed process retained a previous PID")
	}
	result.Restart.AfterProcesses = afterProcesses

	for _, item := range []struct {
		name           string
		client         *uciInstalledAcceptanceMCPClient
		selection      uciInstalledAcceptanceSelection
		expectedCallee string
	}{
		{name: uciInstalledAcceptanceClientA, client: first, selection: firstSelection, expectedCallee: request.Fixture.PrimaryCallee},
		{name: uciInstalledAcceptanceClientB, client: second, selection: secondSelection, expectedCallee: request.Fixture.LinkedCallee},
	} {
		observation, observationErr := uciObserveInstalledAcceptanceSearchGraphRead(ctx, item.client, item.selection, restartedPublications[item.name], request.Fixture, item.expectedCallee)
		if observationErr != nil {
			return nil, 0, fmt.Errorf("restarted installed standard MCP observation for %s: %w", item.name, observationErr)
		}
		beforeObservation, found := result.Restart.BeforeObservations[item.name]
		if !found || !uciInstalledAcceptanceSameObservations(beforeObservation, observation) {
			return nil, 0, errors.New("restarted installed standard MCP observation changed")
		}
		result.Restart.Observations[item.name] = observation
	}

	afterCounts, err := uciInstalledAcceptanceProjectionCountsForAuthority(ctx, authority)
	if err != nil {
		return nil, 0, err
	}
	result.Restart.AfterProjectionCounts = afterCounts
	result.Restart.UnchangedInputReembedded = afterCounts.Embeddings > beforeCounts.Embeddings || afterCounts.ChunkEmbeddings > beforeCounts.ChunkEmbeddings
	if result.Restart.UnchangedInputReembedded {
		return nil, 0, errors.New("restarted installed runtime re-embedded unchanged input")
	}
	if afterCounts.Embeddings != beforeCounts.Embeddings || afterCounts.ChunkEmbeddings != beforeCounts.ChunkEmbeddings {
		return nil, 0, errors.New("restarted installed runtime changed unchanged-input embedding counts")
	}
	if afterCounts.ResolvedEdges != beforeCounts.ResolvedEdges {
		return nil, 0, errors.New("restarted installed runtime changed unchanged-input link counts")
	}
	return next, newDaemonPID, nil
}

func uciExerciseInstalledAcceptanceWatcher(
	ctx context.Context,
	fixture uciInstalledAcceptanceFixture,
	worktrees uciInstalledAcceptanceWorktreesFixture,
	first, second *uciInstalledAcceptanceMCPClient,
	firstSelection, secondSelection uciInstalledAcceptanceSelection,
	before map[string]uciInstalledAcceptancePublication,
	result *uciInstalledAcceptanceResult,
) (after map[string]uciInstalledAcceptancePublication, retErr error) {
	if first == nil || second == nil || result == nil || firstSelection.contextHandle == "" || secondSelection.contextHandle == "" || worktrees.primaryRoot == "" {
		return nil, errors.New("installed acceptance watcher proof is incomplete")
	}
	primaryBefore, primaryFound := before[uciInstalledAcceptanceClientA]
	linkedBefore, linkedFound := before[uciInstalledAcceptanceClientB]
	if !primaryFound || !linkedFound || primaryBefore.runID == "" || linkedBefore.viewID == "" {
		return nil, errors.New("installed acceptance watcher publication baseline is incomplete")
	}

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "UCIInstalledWatcher" + suffix
	relativePath := "pkg/uci_installed_watcher_" + suffix + ".go"
	canaryPath := filepath.Join(worktrees.primaryRoot, filepath.FromSlash(relativePath))
	canarySource := "package fixture\n\nfunc " + functionName + "() string { return \"" + functionName + "\" }\n"
	if err := os.WriteFile(canaryPath, []byte(canarySource), 0o600); err != nil {
		return nil, fmt.Errorf("write installed acceptance watcher canary: %w", err)
	}
	defer func() {
		if removeErr := os.Remove(canaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("remove installed acceptance watcher canary: %w", removeErr))
		}
	}()

	afterWriteA, err := uciWaitForInstalledAcceptanceWatcherState(ctx, first, firstSelection, primaryBefore, functionName, relativePath, true)
	if err != nil {
		return nil, err
	}
	afterWriteB, err := uciWaitForInstalledAcceptanceRestartQuiescence(ctx, second, secondSelection, linkedBefore)
	if err != nil {
		return nil, err
	}
	if uciInstalledAcceptanceSameViewPublication(afterWriteA, primaryBefore) {
		return nil, errors.New("installed acceptance watcher write did not publish a new primary View")
	}
	if !uciInstalledAcceptanceSameViewPublication(afterWriteB, linkedBefore) {
		return nil, errors.New("installed acceptance watcher write changed linked View")
	}

	if err := os.Remove(canaryPath); err != nil {
		return nil, fmt.Errorf("delete installed acceptance watcher canary: %w", err)
	}
	afterDeleteA, err := uciWaitForInstalledAcceptanceWatcherState(ctx, first, firstSelection, afterWriteA, functionName, relativePath, false)
	if err != nil {
		return nil, err
	}
	afterDeleteB, err := uciWaitForInstalledAcceptanceRestartQuiescence(ctx, second, secondSelection, afterWriteB)
	if err != nil {
		return nil, err
	}
	if uciInstalledAcceptanceSameViewPublication(afterDeleteA, afterWriteA) {
		return nil, errors.New("installed acceptance watcher delete did not publish a new primary View")
	}
	if !uciInstalledAcceptanceSameViewPublication(afterDeleteB, linkedBefore) {
		return nil, errors.New("installed acceptance watcher delete changed linked View")
	}

	result.Watcher = uciInstalledAcceptanceWatcher{
		AfterWriteA:  uciInstalledAcceptancePublicationEvidenceFor(afterWriteA),
		AfterDeleteA: uciInstalledAcceptancePublicationEvidenceFor(afterDeleteA),
		AfterWriteB:  uciInstalledAcceptancePublicationEvidenceFor(afterWriteB),
		AfterDeleteB: uciInstalledAcceptancePublicationEvidenceFor(afterDeleteB),
	}
	return map[string]uciInstalledAcceptancePublication{
		uciInstalledAcceptanceClientA: afterDeleteA,
		uciInstalledAcceptanceClientB: afterDeleteB,
	}, nil
}

func uciWaitForInstalledAcceptanceWatcherPublication(
	ctx context.Context,
	client *uciInstalledAcceptanceMCPClient,
	selection uciInstalledAcceptanceSelection,
	previous uciInstalledAcceptancePublication,
) (uciInstalledAcceptancePublication, error) {
	if client == nil || selection.contextHandle == "" || previous.runID == "" {
		return uciInstalledAcceptancePublication{}, errors.New("installed acceptance watcher status target is incomplete")
	}
	ticker := time.NewTicker(uciInstalledAcceptanceQuiescencePollInterval)
	defer ticker.Stop()
	for {
		status, err := uciInstalledAcceptanceStatusForSelection(ctx, client, selection)
		if err != nil {
			return uciInstalledAcceptancePublication{}, err
		}
		if status.error != "" {
			return uciInstalledAcceptancePublication{}, fmt.Errorf("installed standard MCP watcher status error: %s", uciInstalledAcceptanceSafeErrorDetail(status.error))
		}
		if status.runID != "" && status.runID != previous.runID {
			watchedSelection := selection
			watchedSelection.runID = status.runID
			barrier, barrierErr := uciWaitForInstalledAcceptanceBarrier(ctx, client, watchedSelection)
			if barrierErr != nil {
				return uciInstalledAcceptancePublication{}, barrierErr
			}
			return uciWaitForInstalledAcceptanceQuiescence(ctx, client, watchedSelection, barrier)
		}
		select {
		case <-ctx.Done():
			return uciInstalledAcceptancePublication{}, fmt.Errorf("wait for installed acceptance watcher run: %w", ctx.Err())
		case <-ticker.C:
		}
	}
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
	payload, err := client.Tool(ctx, "codebase_search", map[string]any{
		"context_handle": selection.contextHandle,
		"query":          functionName,
		"path_prefix":    relativePath,
		"limit":          10,
	})
	if err != nil {
		return err
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil {
		return err
	}
	if !uciInstalledAcceptanceQueryMatchesPublication(response, publication) {
		return errors.New("installed standard MCP watcher search did not retain the selected View")
	}
	if !wantPresent {
		if (response.Status != uci.QueryStatusEmpty && response.Status != uci.QueryStatusPartial) || (response.Items != nil && len(*response.Items) != 0) {
			return errUCIInstalledAcceptanceWatcherCanaryPresent
		}
		return nil
	}
	if (response.Status != uci.QueryStatusOK && response.Status != uci.QueryStatusPartial) || response.Items == nil {
		return errors.New("installed standard MCP watcher write did not return the canary")
	}
	found := false
	for _, item := range *response.Items {
		if item.Ref.SourceID != publication.sourceID || item.Ref.ViewID != publication.viewID {
			return errors.New("installed standard MCP watcher search disclosed an item outside the selected View")
		}
		name, nameOK := uciInstalledAcceptanceGoFunctionName(item.Ref.EntityKey)
		if item.Path == relativePath && nameOK && name == functionName {
			if !uciInstalledAcceptanceIsBareSHA256(string(item.ContentDigest)) {
				return errors.New("installed standard MCP watcher canary has an invalid content digest")
			}
			found = true
		}
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
