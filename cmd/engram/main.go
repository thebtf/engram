// Package main is the engram daemon entry point for v4.3.0. It wires the
// modular framework (internal/module + internal/module/registry +
// internal/module/lifecycle + internal/module/dispatcher) to the muxcore
// engine and runs until the process receives SIGINT / SIGTERM.
//
// v4.2.0 was a monolithic engramHandler implementing muxcore.SessionHandler
// and muxcore.ProjectLifecycle inline. In v4.3.0 all that logic lives in
// internal/handlers/engramcore wrapped as an EngramModule +
// ProxyToolProvider + ProjectLifecycle tenant, registered here via
// wiring.go.
//
// Design reference: design.md §4.1 (startup/shutdown sequence), plan.md
// Phase 5 (US2 engramcore first tenant), tasks T040/T041.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/control"
	"github.com/thebtf/engram/internal/handlers/serverevents"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/dispatcher"
	"github.com/thebtf/engram/internal/module/lifecycle"
	"github.com/thebtf/engram/internal/module/registry"
	"github.com/thebtf/engram/internal/version"
	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	"github.com/thebtf/mcp-mux/muxcore/engine"
	muxregistry "github.com/thebtf/mcp-mux/muxcore/registry"
	"github.com/thebtf/mcp-mux/muxcore/serverid"
	"github.com/thebtf/mcp-mux/muxcore/upgrade"
	"golang.org/x/mod/semver"
)

// daemonVersion is the string reported to gRPC Initialize and used in
// structured logs. Tracks Constitution §15 unified engram + plugin version.
var daemonVersion = version.Daemon

const (
	muxcoreDaemonFlag        = "--muxcore-daemon"
	muxcoreEmbeddedVersion   = "v0.29.1"
	muxcoreNamespace         = "engram"
	muxcoreDaemonCompatEpoch = 1
	legacyDaemonVersion       = "v6.46.4"
)

type muxcoreDaemonVersionMarker struct {
	SchemaVersion     int    `json:"schema_version"`
	ProductVersion    string `json:"product_version"`
	DaemonCompatEpoch int    `json:"daemon_compat_epoch"`
	PID               int    `json:"pid"`
	DaemonGeneration  string `json:"daemon_generation"`
	Exe               string `json:"exe"`
}

type legacyMuxcoreDaemonVersionMarker struct {
	Version string `json:"version"`
	PID     int    `json:"pid"`
	Exe     string `json:"exe"`
}

type muxcoreDaemonStatusIdentity struct {
	PID              int    `json:"pid"`
	DaemonGeneration string `json:"daemon_generation"`
}

type daemonConvergenceIdentity struct {
	ProductVersion    string
	DaemonCompatEpoch int
}

type daemonConvergenceAction uint8

const (
	daemonConvergenceJoin daemonConvergenceAction = iota
	daemonConvergenceReplace
	daemonConvergenceFail
)

// startupGate enforces FR-4 / Plan ADR-005. When the daemon process starts
// with a configured server URL but no ENGRAM_TOKEN, exit non-zero with a
// single user-actionable diagnostic. When no server URL is configured the
// gate is silent — local-only flows (loom_*) continue to work without a
// token. Returns true on pass; false (after writing stderr + os.Exit) is
// unreachable from the caller's perspective.
func startupGate() {
	serverURL := os.Getenv(config.EnvServerURL)
	if serverURL == "" {
		serverURL = os.Getenv(config.EnvServerURLAlt)
	}
	if serverURL == "" {
		// No back-end configured — local-only flows are allowed without token.
		return
	}
	if os.Getenv(config.EnvWorkstationToken) != "" {
		return
	}
	fmt.Fprintf(os.Stderr,
		"[engram] FATAL: %s is empty. Generate a keycard at %s/tokens and run /engram:setup.\n",
		config.EnvWorkstationToken, serverURL,
	)
	os.Exit(1)
}

func isMuxcoreDaemonMode() bool {
	for _, arg := range os.Args[1:] {
		if arg == muxcoreDaemonFlag {
			return true
		}
	}
	return false
}

func isMuxcoreProxyMode() bool {
	return os.Getenv("MCP_MUX_SESSION_ID") != ""
}

func muxcoreDaemonMarkerPath() string {
	return serverid.DaemonControlPath("", muxcoreNamespace) + ".marker.json"
}

func muxcoreLegacyDaemonVersionPath() string {
	return serverid.DaemonControlPath("", muxcoreNamespace) + ".version"
}

func normalizedExecutablePath(path string) (string, bool) {
	path = filepath.Clean(strings.TrimSpace(path))
	return path, filepath.IsAbs(path)
}

func validDaemonIdentity(identity daemonConvergenceIdentity) bool {
	return identity.DaemonCompatEpoch >= 0 && semver.IsValid(identity.ProductVersion) && semver.Canonical(identity.ProductVersion) == identity.ProductVersion
}

// classifyDaemonConvergence is pure: a compatible daemon is never replaced by
// an equal or newer same-epoch caller, irrespective of its object-store path.
func classifyDaemonConvergence(client, live daemonConvergenceIdentity) (daemonConvergenceAction, error) {
	if !validDaemonIdentity(client) || !validDaemonIdentity(live) {
		return daemonConvergenceFail, errors.New("malformed daemon compatibility identity")
	}
	switch {
	case live.DaemonCompatEpoch < client.DaemonCompatEpoch:
		return daemonConvergenceReplace, nil
	case live.DaemonCompatEpoch > client.DaemonCompatEpoch:
		return daemonConvergenceFail, fmt.Errorf("incompatible newer daemon epoch %d (client epoch %d)", live.DaemonCompatEpoch, client.DaemonCompatEpoch)
	case semver.Compare(live.ProductVersion, client.ProductVersion) < 0:
		return daemonConvergenceReplace, nil
	default:
		return daemonConvergenceJoin, nil
	}
}

func decodeStrictJSON(raw []byte, target any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func readMuxcoreDaemonVersionMarker(path string) (muxcoreDaemonVersionMarker, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return muxcoreDaemonVersionMarker{}, err
	}
	var marker muxcoreDaemonVersionMarker
	if err := decodeStrictJSON(raw, &marker); err != nil {
		return muxcoreDaemonVersionMarker{}, err
	}
	if marker.SchemaVersion != 2 || marker.PID <= 0 || strings.TrimSpace(marker.DaemonGeneration) == "" {
		return muxcoreDaemonVersionMarker{}, errors.New("invalid daemon marker schema or identity")
	}
	if _, ok := normalizedExecutablePath(marker.Exe); !ok || !validDaemonIdentity(daemonConvergenceIdentity{ProductVersion: marker.ProductVersion, DaemonCompatEpoch: marker.DaemonCompatEpoch}) {
		return muxcoreDaemonVersionMarker{}, errors.New("invalid daemon marker compatibility identity")
	}
	return marker, nil
}

func readLegacyMuxcoreDaemonVersionMarker(path string) (legacyMuxcoreDaemonVersionMarker, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return legacyMuxcoreDaemonVersionMarker{}, err
	}
	var marker legacyMuxcoreDaemonVersionMarker
	if err := decodeStrictJSON(raw, &marker); err != nil {
		return legacyMuxcoreDaemonVersionMarker{}, err
	}
	if marker.Version != legacyDaemonVersion || marker.PID <= 0 {
		return legacyMuxcoreDaemonVersionMarker{}, errors.New("unsupported legacy daemon marker")
	}
	if _, ok := normalizedExecutablePath(marker.Exe); !ok {
		return legacyMuxcoreDaemonVersionMarker{}, errors.New("invalid legacy daemon executable")
	}
	return marker, nil
}

func muxcoreDaemonStatus(ctlPath string) (muxcoreDaemonStatusIdentity, bool) {
	resp, err := muxcontrol.SendWithTimeout(ctlPath, muxcontrol.Request{Cmd: "status"}, 2*time.Second)
	if err != nil || resp == nil || !resp.OK || len(resp.Data) == 0 {
		return muxcoreDaemonStatusIdentity{}, false
	}
	var status muxcoreDaemonStatusIdentity
	if err := json.Unmarshal(resp.Data, &status); err != nil || status.PID <= 0 || strings.TrimSpace(status.DaemonGeneration) == "" {
		return muxcoreDaemonStatusIdentity{}, false
	}
	return status, true
}

func clientDaemonIdentity() (daemonConvergenceIdentity, error) {
	identity := daemonConvergenceIdentity{ProductVersion: daemonVersion, DaemonCompatEpoch: muxcoreDaemonCompatEpoch}
	if identity.DaemonCompatEpoch <= 0 || !validDaemonIdentity(identity) {
		return daemonConvergenceIdentity{}, fmt.Errorf("invalid client daemon compatibility identity %q", daemonVersion)
	}
	return identity, nil
}

// muxcoreDaemonConvergenceAction accepts a schema-2 marker only after a fresh
// muxcore status response correlates both PID and generation.
func muxcoreDaemonConvergenceAction(path string, status muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity) (daemonConvergenceAction, error) {
	if status.PID <= 0 || strings.TrimSpace(status.DaemonGeneration) == "" {
		return daemonConvergenceFail, errors.New("missing fresh muxcore daemon status identity")
	}
	marker, err := readMuxcoreDaemonVersionMarker(path)
	if err != nil {
		return daemonConvergenceFail, err
	}
	if marker.PID != status.PID || marker.DaemonGeneration != status.DaemonGeneration {
		return daemonConvergenceFail, errors.New("daemon marker does not correlate to fresh control status")
	}
	return classifyDaemonConvergence(client, daemonConvergenceIdentity{ProductVersion: marker.ProductVersion, DaemonCompatEpoch: marker.DaemonCompatEpoch})
}

func muxcoreLegacyDaemonConvergenceAction(path string, status muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity) (daemonConvergenceAction, error) {
	if status.PID <= 0 || strings.TrimSpace(status.DaemonGeneration) == "" {
		return daemonConvergenceFail, errors.New("missing fresh muxcore daemon status identity")
	}
	legacy, err := readLegacyMuxcoreDaemonVersionMarker(path)
	if err == nil && legacy.PID == status.PID && client.DaemonCompatEpoch == 1 && validDaemonIdentity(client) {
		return daemonConvergenceReplace, nil
	}
	return daemonConvergenceFail, errors.New("missing, malformed, or uncorrelated legacy daemon marker")
}

func readLiveMuxcoreDaemonActionFromDisk(status muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity) (daemonConvergenceAction, error) {
	action, err := muxcoreDaemonConvergenceAction(muxcoreDaemonMarkerPath(), status, client)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return action, err
	}
	return muxcoreLegacyDaemonConvergenceAction(muxcoreLegacyDaemonVersionPath(), status, client)
}

var (
	readMuxcoreDaemonStatusIdentity  = muxcoreDaemonStatus
	readLiveMuxcoreDaemonAction      = readLiveMuxcoreDaemonActionFromDisk
	currentExecutable                = os.Executable
	restartMuxcoreDaemon             = restartMuxcoreDaemonWithSuccessor
	waitForCurrentMuxcoreDaemonReady = waitForMuxcoreDaemonVersion
)

func waitForMuxcoreDaemonVersion(ctx context.Context) error {
	const pollInterval = 100 * time.Millisecond
	client, err := clientDaemonIdentity()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		status, ok := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
		if ok {
			action, actionErr := readLiveMuxcoreDaemonAction(status, client)
			if actionErr == nil && action == daemonConvergenceJoin {
				return nil
			}
			if action == daemonConvergenceFail && actionErr != nil {
				return actionErr
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func isConcurrentMuxcoreRestart(err error) bool {
	var updateErr *engine.UpdateAndRestartError
	return errors.As(err, &updateErr) && updateErr.Phase == engine.UpdatePhaseLock
}

func isExpectedContextShutdown(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, context.Canceled)
}

func reconcileMuxcoreDaemonVersion(parent context.Context) error {
	if isMuxcoreDaemonMode() || isMuxcoreProxyMode() {
		return nil
	}

	status, ok := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
	if !ok {
		return nil // no live daemon: muxcore starts the normal shim/daemon path
	}
	client, err := clientDaemonIdentity()
	if err != nil {
		return err
	}
	action, err := readLiveMuxcoreDaemonAction(status, client)
	if err != nil || action == daemonConvergenceFail {
		if err == nil {
			err = errors.New("incompatible live daemon")
		}
		return fmt.Errorf("authenticate live muxcore daemon PID %d: %w", status.PID, err)
	}
	if action == daemonConvergenceJoin {
		return nil
	}

	successorExe, err := currentExecutable()
	if err != nil {
		return fmt.Errorf("resolve muxcore successor executable: %w", err)
	}
	successorExe, ok = normalizedExecutablePath(successorExe)
	if !ok {
		return fmt.Errorf("resolve muxcore successor executable: path %q is not absolute", successorExe)
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()

	result, err := restartMuxcoreDaemon(ctx, successorExe)
	if err != nil {
		if isConcurrentMuxcoreRestart(err) {
			if waitErr := waitForCurrentMuxcoreDaemonReady(ctx); waitErr == nil {
				fmt.Fprintf(os.Stderr, "[engram] joined concurrent muxcore daemon replacement for %s\n", daemonVersion)
				return nil
			} else {
				return fmt.Errorf("restart lower muxcore daemon PID %d with %s: %w; concurrent replacement did not converge: %v", status.PID, daemonVersion, err, waitErr)
			}
		}
		return fmt.Errorf("restart lower muxcore daemon PID %d with %s: %w", status.PID, daemonVersion, err)
	}
	if result.DaemonWasRunning {
		if !result.ReplacementReady {
			return fmt.Errorf("restart lower muxcore daemon PID %d did not produce a ready replacement", status.PID)
		}
		if waitErr := waitForCurrentMuxcoreDaemonReady(ctx); waitErr != nil {
			return fmt.Errorf("restart lower muxcore daemon PID %d reported ready but replacement did not converge to %s: %w", status.PID, daemonVersion, waitErr)
		}
		fmt.Fprintf(os.Stderr,
			"[engram] replaced lower muxcore daemon PID %d with %s (graceful=%t fallback_shutdown=%t)\n",
			status.PID, daemonVersion, result.GracefulRestarted, result.FallbackShutdown,
		)
	}
	return nil
}

func writeMuxcoreDaemonVersionMarker(logger *slog.Logger) {
	if !isMuxcoreDaemonMode() {
		return
	}
	writeMuxcoreDaemonVersionMarkerAt(muxcoreDaemonMarkerPath(), logger)
}

func writeMuxcoreDaemonVersionMarkerAt(markerPath string, logger *slog.Logger) {
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
		logger.Warn("could not create run directory for muxcore version marker", "dir", filepath.Dir(markerPath), "error", err)
		return
	}
	status, ok := readMuxcoreDaemonStatusIdentity(serverid.DaemonControlPath("", muxcoreNamespace))
	if !ok || status.PID != os.Getpid() {
		logger.Warn("could not correlate muxcore daemon status before publishing version marker")
		return
	}
	exePath, err := currentExecutable()
	if err != nil {
		logger.Warn("could not resolve executable path for muxcore daemon version marker", "error", err)
		return
	}
	exePath, ok = normalizedExecutablePath(exePath)
	if !ok {
		logger.Warn("could not publish muxcore daemon version marker with relative executable", "exe", exePath)
		return
	}
	client, err := clientDaemonIdentity()
	if err != nil {
		logger.Warn("could not publish muxcore daemon version marker", "error", err)
		return
	}
	payload, err := json.Marshal(muxcoreDaemonVersionMarker{SchemaVersion: 2, ProductVersion: client.ProductVersion, DaemonCompatEpoch: client.DaemonCompatEpoch, PID: status.PID, DaemonGeneration: status.DaemonGeneration, Exe: exePath})
	if err != nil {
		logger.Warn("could not marshal muxcore daemon version marker", "error", err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(markerPath), ".engram-daemon-marker-*.tmp")
	if err != nil {
		logger.Warn("could not create muxcore daemon version marker", "error", err)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(append(payload, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		logger.Warn("could not write muxcore daemon version marker", "error", err)
		return
	}
	if err := os.Rename(tmpName, markerPath); err != nil {
		logger.Warn("could not atomically publish muxcore daemon version marker", "error", err)
	}
}

func muxcoreBaseConfig() engine.Config {
	return engine.Config{
		Name:         "engram",
		Namespace:    muxcoreNamespace,
		DaemonFlag:   muxcoreDaemonFlag,
		SkipSnapshot: true,
		// Opt in to muxcore's daemon registry (v0.26+) so the shared mcp-mux
		// operator point can discover the engram engine via mux_engines /
		// mux_list(engine_name:"engram"). Read-only: ListOwners only; no
		// cross-engine stop/restart/update is advertised. Nil would be the
		// opt-out zero value that preserves pre-registry behavior.
		Registry: &muxregistry.Config{
			ProductName:    "engram",
			MuxcoreVersion: muxcoreEmbeddedVersion,
			Capabilities:   muxregistry.Capabilities{ListOwners: true},
		},
	}
}

func muxcoreDaemonConfig(disp *dispatcher.Dispatcher) engine.Config {
	cfg := muxcoreBaseConfig()
	cfg.Persistent = true // daemon owns durable module/background state
	cfg.SessionHandler = disp
	return cfg
}

func muxcoreShimConfig() engine.Config {
	cfg := muxcoreBaseConfig()
	cfg.Persistent = false // host shim owns no durable state
	// IdleSuspendDelay and IdleDormantGrace stay zero: muxcore's supervisor
	// exists, but Engram's plugin launcher does not wire it.
	cfg.Handler = func(context.Context, io.Reader, io.Writer) error {
		return fmt.Errorf("engram muxcore shim cannot serve MCP traffic directly")
	}
	return cfg
}

func restartMuxcoreDaemonWithSuccessor(ctx context.Context, successorExe string) (engine.UpdateAndRestartResult, error) {
	cfg := muxcoreShimConfig()
	eng, err := engine.New(cfg)
	if err != nil {
		return engine.UpdateAndRestartResult{}, err
	}
	return eng.RestartWithSuccessor(ctx, engine.RestartWithSuccessorOptions{
		SuccessorExe:    successorExe,
		DrainTimeout:    30 * time.Second,
		RestartTimeout:  60 * time.Second,
		ShutdownTimeout: 10 * time.Second,
		ReadyTimeout:    30 * time.Second,
	})
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h" || os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("engram %s — stdio MCP daemon for Claude Code\n", daemonVersion)
		fmt.Println()
		fmt.Println("This binary is invoked automatically by the engram plugin.")
		fmt.Println("It is not intended to be run directly.")
		fmt.Println()
		fmt.Println("Environment:")
		fmt.Printf("  %-28s  Server URL (e.g. http://host:37777)\n", config.EnvServerURL)
		fmt.Printf("  %-28s  Workstation keycard (issued via dashboard /tokens)\n", config.EnvWorkstationToken)
		os.Exit(0)
	}

	// FR-4 / ADR-005: fail-fast on missing workstation credential BEFORE
	// any heavy initialisation. Loud failure beats silent loom_*-only
	// graceful degradation that masked PR #203's regression for days.
	startupGate()

	daemonCtx, daemonCancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer daemonCancel()

	daemonMode := isMuxcoreDaemonMode()
	if !daemonMode {
		if err := reconcileMuxcoreDaemonVersion(daemonCtx); err != nil {
			if daemonCtx.Err() != nil {
				return
			}
			fmt.Fprintf(os.Stderr, "[engram] FATAL: muxcore daemon version reconciliation failed: %v\n", err)
			os.Exit(1)
		}
	}

	// Clean stale binaries from previous upgrades (.old.* files).
	if exePath, err := os.Executable(); err == nil {
		if cleaned := upgrade.CleanStale(exePath); cleaned > 0 {
			fmt.Fprintf(os.Stderr, "[engram] cleaned %d stale binary file(s)\n", cleaned)
		}
	}

	if !daemonMode {
		eng, err := engine.New(muxcoreShimConfig())
		if err != nil {
			fmt.Fprintf(os.Stderr, "[engram] FATAL: muxcore shim setup failed: %v\n", err)
			os.Exit(1)
		}
		if err := eng.Run(daemonCtx); err != nil && !isExpectedContextShutdown(daemonCtx, err) {
			fmt.Fprintf(os.Stderr, "[engram] FATAL: muxcore shim terminated: %v\n", err)
			os.Exit(1)
		}
		return
	}

	dd := dataDir()
	logger := newRootLogger()

	// --- Framework wiring ------------------------------------------------
	reg := registry.New()
	if err := registerModules(reg); err != nil {
		logger.Error("module registration failed", "error", err)
		os.Exit(1)
	}
	reg.Freeze()

	logger.Info("module registry frozen",
		"modules", reg.ListNames(),
		"version", daemonVersion,
	)

	disp := dispatcher.NewWithVersion(reg, logger, daemonVersion)
	pipeline := lifecycle.New(reg, logger)

	// Init context is distinct from daemon context — see design.md §3.2
	// and clarification C3 (Init ctx vs deps.DaemonCtx).
	initCtx, initCancel := context.WithCancel(context.Background())

	if err := pipeline.Start(initCtx, depsProviderFor(logger, daemonCtx)); err != nil {
		initCancel()
		logger.Error("lifecycle Start failed", "error", err)
		os.Exit(1)
	}
	initCancel()

	// --- muxcore engine boot ---------------------------------------------
	// The dispatcher satisfies BOTH muxcore.SessionHandler (HandleRequest)
	// and muxcore.ProjectLifecycle (OnProjectConnect/OnProjectDisconnect).
	// muxcore type-asserts on the SessionHandler to detect the optional
	// lifecycle methods — see muxcore.ProjectLifecycle docs.
	eng, err := engine.New(muxcoreDaemonConfig(disp))
	if err != nil {
		logger.Error("engine.New failed", "error", err)
		_ = pipeline.ShutdownAll(daemonCtx)
		os.Exit(1)
	}

	// Multiple clients may race to spawn the daemon. Only the process that
	// actually binds muxcore's control socket reaches Ready; losers must never
	// overwrite the live daemon's version marker or product control surface.
	runErr := make(chan error, 1)
	go func() {
		runErr <- eng.Run(daemonCtx)
	}()
	select {
	case <-eng.Ready():
		if eng.Mode() != engine.ModeDaemon || eng.Daemon() == nil {
			logger.Error("muxcore engine reported ready without a live daemon", "mode", eng.Mode())
			_ = pipeline.ShutdownAll(daemonCtx)
			os.Exit(1)
		}
	case err := <-runErr:
		_ = pipeline.ShutdownAll(daemonCtx)
		if isExpectedContextShutdown(daemonCtx, err) {
			return
		}
		logger.Error("engine.Run terminated before daemon readiness", "error", err)
		os.Exit(1)
	}
	writeMuxcoreDaemonVersionMarker(logger)

	// --- Product control socket -----------------------------------------
	// Start only after muxcore readiness so losing daemon-spawn candidates do
	// not publish a stale PID or contend for Engram's restart control surface.
	sockPath := control.SocketPath(dd)
	pidPath := control.PIDPath(dd)
	if err := os.MkdirAll(control.SocketDir(dd), 0o700); err != nil {
		logger.Warn("could not create run directory for control socket",
			"dir", control.SocketDir(dd),
			"error", err,
		)
	}
	ctrlListener := control.NewListener(sockPath, pidPath,
		func(cmd string) string {
			switch cmd {
			case "graceful-restart":
				go handleGracefulRestart(logger, pipeline, disp, filepath.Join(dd, "modules"))
				return "ACK"
			default:
				return "ERR unknown command"
			}
		},
		logger,
	)
	if err := ctrlListener.Start(); err != nil {
		// Non-fatal: daemon continues without the legacy product control socket.
		logger.Warn("control socket start failed — graceful-restart unavailable",
			"error", err,
		)
	}
	defer ctrlListener.Close()

	// --- Serverevents bridge ---------------------------------------------
	// Start the bridge that consumes engram-server's ProjectEvents gRPC stream
	// and fans out OnProjectRemoved to all ProjectRemovalAware modules. The
	// bridge runs in the background for the daemon's lifetime.
	//
	// The bridge is started after the muxcore engine is ready and stopped before
	// pipeline.ShutdownAll so in-flight fan-outs complete before module teardown.
	//
	// If ENGRAM_SERVER_URL is not set the bridge logs a warning and is a no-op.
	// The dispatcher satisfies serverevents.ProjectTracker via its
	// ConnectedProjectIDs() method, populated by OnProjectConnect /
	// OnProjectDisconnect callbacks. This gives the heartbeat path real
	// visibility into active sessions (Phase 5 CRIT fix from PR #171 review).
	sevBridge := serverevents.NewBridge(logger, reg, disp, nil /* production: dial own conn */)
	sevBridge.Start(daemonCtx)

	logger.Info("engram daemon ready", "version", daemonVersion)

	if err := <-runErr; err != nil && !isExpectedContextShutdown(daemonCtx, err) {
		logger.Error("engine.Run terminated", "error", err)
		sevBridge.Stop()
		_ = pipeline.ShutdownAll(daemonCtx)
		os.Exit(1)
	}

	logger.Info("engram daemon shutting down")
	sevBridge.Stop()
	if err := pipeline.ShutdownAll(daemonCtx); err != nil {
		logger.Error("lifecycle Shutdown error", "error", err)
	}
}

// handleGracefulRestart executes the retained explicit graceful-restart sequence:
//  1. Log INFO
//  2. Drain — stop accepting new tool calls (5 s sleep)
//  3. SnapshotAll — persist module state
//  4. ShutdownAll — clean module shutdown
//  5. Check for an operator-provided <current executable>.new binary
//  6. upgrade.Swap(currentExe, newExe) — atomic rename
//  7. execReplace — exec-in-place (Unix) or spawn+exit (Windows)
//
// Each phase is best-effort: failures are logged but do not abort later
// phases. The whole sequence runs under a 60 s hard deadline so a stuck
// module cannot hold up the restart indefinitely.
//
// Step 5 allows the command to be used even when no .new file exists (e.g.
// admin-triggered restart or test). In that case steps 1–4 execute cleanly and
// the daemon exits, leaving supervisor to restart it on the next CC session.
//
// Design reference: tasks.md T058.
func handleGracefulRestart(
	logger *slog.Logger,
	pipeline *lifecycle.Pipeline,
	disp *dispatcher.Dispatcher,
	storageDir string,
) {
	const hardDeadline = 60 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), hardDeadline)
	defer cancel()

	logger.Info("graceful restart initiated", "budget_s", hardDeadline.Seconds())

	// Phase 1 — Drain: refuse new tool calls, wait for in-flight to finish.
	if err := pipeline.Drain(ctx, disp, 5*time.Second); err != nil {
		logger.Warn("Drain error (continuing)", "error", err)
	}

	// Phase 2 — Snapshot: persist module state.
	if _, err := pipeline.SnapshotAll(ctx, storageDir, daemonVersion); err != nil {
		logger.Warn("SnapshotAll error (continuing)", "error", err)
	}

	// Phase 3 — Shutdown: clean module teardown.
	if err := pipeline.ShutdownAll(ctx); err != nil {
		logger.Warn("ShutdownAll error (continuing)", "error", err)
	}

	// Phase 4 — Find new binary.
	currentExe, err := os.Executable()
	if err != nil {
		logger.Error("os.Executable failed — cannot swap binary", "error", err)
		os.Exit(1) // Signal failure to supervisor.
		return
	}
	newExe := currentExe + ".new"

	if _, statErr := os.Stat(newExe); os.IsNotExist(statErr) {
		logger.Warn("no .new binary found — exiting for supervisor restart",
			"looked_for", newExe,
		)
		os.Exit(0)
		return
	}

	// Phase 5 — Atomic swap.
	oldPath, swapErr := upgrade.Swap(currentExe, newExe)
	if swapErr != nil {
		logger.Error("upgrade.Swap failed — exiting for supervisor restart",
			"current", currentExe,
			"new", newExe,
			"error", swapErr,
		)
		os.Exit(1) // Signal failure to supervisor.
		return
	}
	logger.Info("binary swapped",
		"old_backed_up_as", oldPath,
		"new_active", currentExe,
	)

	// Phase 6 — Exec-replace.
	if err := execReplace(currentExe, logger); err != nil {
		logger.Error("exec-replace failed — supervisor will restart on next session",
			"binary", currentExe,
			"error", err,
		)
		os.Exit(1) // Signal failure to supervisor.
	}
}

// newRootLogger returns a JSON-format slog logger by default, or a text
// logger when ENGRAM_LOG_FORMAT=text is set. Structured by design decision
// D12 and NFR-4 (structured logging).
func newRootLogger() *slog.Logger {
	var handler slog.Handler
	if os.Getenv("ENGRAM_LOG_FORMAT") == "text" {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	} else {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	}
	return slog.New(handler).With("component", "engram-daemon", "version", daemonVersion)
}

// depsProviderFor returns a closure that builds a ModuleDeps value per
// module. Each module gets its own slog.Logger (with the "module" field
// attached), a private storage directory under $ENGRAM_DATA_DIR/modules/,
// and a shared DaemonCtx that is cancelled on SIGINT/SIGTERM.
//
// Storage dir convention (clarification C5): ${DATA_DIR}/modules/${moduleName}/
// with 0700 permissions. Created lazily on first module that needs it.
func depsProviderFor(root *slog.Logger, daemonCtx context.Context) func(name string) module.ModuleDeps {
	return func(name string) module.ModuleDeps {
		storageDir := filepath.Join(dataDir(), "modules", name)
		if err := os.MkdirAll(storageDir, 0o700); err != nil {
			root.Warn("failed to create module storage dir",
				"module", name,
				"path", storageDir,
				"error", err,
			)
		}
		return module.ModuleDeps{
			Logger:     root.With("module", name),
			DaemonCtx:  daemonCtx,
			StorageDir: storageDir,
			Config:     nil, // module-specific config comes from env in v0.1.0
			Notifier:   nil, // muxcore notifier wiring deferred to Phase 6
			Lookup:     nil, // cross-module lookup not used by engramcore
		}
	}
}

// dataDir returns the engram data directory. Honors ENGRAM_DATA_DIR env var
// with a sensible fallback under the user's home directory.
func dataDir() string {
	if dir := os.Getenv("ENGRAM_DATA_DIR"); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".engram")
	}
	return filepath.Join(os.TempDir(), "engram")
}
