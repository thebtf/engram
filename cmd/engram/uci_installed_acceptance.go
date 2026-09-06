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
	"runtime/debug"
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
	errUCIInstalledAcceptanceNonTestPostgres                 = errors.New("UCI installed acceptance requires an explicit loopback test PostgreSQL database")
	errUCIInstalledAcceptanceParserBoundary                  = errors.New("UCI installed acceptance parser boundary is not observable through the installed runtime")
	errUCIInstalledAcceptanceBarrierBoundary                 = errors.New("UCI installed acceptance read-your-save barrier is not observable through the installed runtime")
	errUCIInstalledAcceptanceNegativeRecorderRestartBoundary = errors.New("UCI installed acceptance authorization-negative, recorder, and restart matrix boundary follows successful public isolation")
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
	UnchangedInputReembedded bool
	ClientTranscripts        map[string]uciInstalledAcceptanceClientTranscript
	ClientContexts           map[string]uciInstalledAcceptanceContext
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
	daemonControlRoot := filepath.Join(request.LocalStateRoot, "temp")
	defer func() {
		daemonPID := result.Processes.DaemonPID
		if installation != nil && installation.tree != nil {
			_ = installation.tree.Close()
		}
		if stopErr := uciStopInstalledAcceptanceDaemon(daemonControlRoot, daemonPID); stopErr != nil {
			err = errors.Join(err, stopErr)
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

	return result, errUCIInstalledAcceptanceNegativeRecorderRestartBoundary
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
			ClientTranscripts: make(map[string]uciInstalledAcceptanceClientTranscript),
			ClientContexts:    make(map[string]uciInstalledAcceptanceContext),
			Observations:      make(map[string]uciInstalledAcceptanceObservations),
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
	parts := []string{
		"uci-tree-sitter-bundle/v1",
		"github.com/tree-sitter/go-tree-sitter@v0.25.0",
		"github.com/tree-sitter/tree-sitter-javascript@v0.25.0",
		"github.com/tree-sitter/tree-sitter-typescript@v0.23.2",
		"go=" + runtime.Version(),
		"target=" + runtime.GOOS + "/" + runtime.GOARCH,
	}
	if build, ok := debug.ReadBuildInfo(); ok && build.GoVersion != "" {
		parts = append(parts, "build-go="+build.GoVersion)
	}
	sort.Strings(parts)
	digest := sha256.New()
	for _, part := range parts {
		length := uint32(len(part))
		_, _ = digest.Write([]byte{byte(length >> 24), byte(length >> 16), byte(length >> 8), byte(length)})
		_, _ = digest.Write([]byte(part))
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
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
	uciInstalledAcceptanceQuiescenceObservations       = 3
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
		state   string
		barrier *struct {
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
			State   string `json:"state"`
			Barrier *struct {
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
			state   string
			barrier *struct {
				scope struct {
					kind      string
					pathCount int64
				}
				deadlineMS int64
				state      string
			}
		}{state: wire.Freshness.State}
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
	if status.status != "idle" || status.error != "" {
		return uciInstalledAcceptancePublication{}, errors.New("installed standard MCP status is not a clean idle observation")
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
		payload, err := client.Tool(ctx, "codebase_status", map[string]any{"context_handle": selection.contextHandle})
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
	payload, err := client.Tool(ctx, "codebase_status", map[string]any{
		"context_handle": selection.contextHandle,
		"after_barrier": map[string]any{
			"token":   selection.runID,
			"wait_ms": uciInstalledAcceptanceBarrierWait(ctx),
		},
	})
	if err != nil {
		statusPayload, statusErr := client.Tool(ctx, "codebase_status", map[string]any{"context_handle": selection.contextHandle})
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
		return uciInstalledAcceptanceClosedOutcome{}, errors.New("installed standard MCP closed response disclosed contextual data")
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
