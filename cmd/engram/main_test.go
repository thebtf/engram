package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/mcp-mux/muxcore/engine"
)

func TestMuxcoreDaemonVersionMatches(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "muxcore-daemon.version")
	currentExe := filepath.Join(t.TempDir(), "engram.exe")

	if muxcoreDaemonVersionMarkerMatches(path, "v6.4.6", 1234, currentExe) {
		t.Fatal("missing marker must not match")
	}

	if err := os.WriteFile(path, []byte("v6.4.6\n"), 0o600); err != nil {
		t.Fatalf("write legacy marker: %v", err)
	}
	if muxcoreDaemonVersionMarkerMatches(path, "v6.4.6", 1234, currentExe) {
		t.Fatal("legacy version-only marker must not match")
	}

	if err := os.WriteFile(path, []byte(`{"version":"v6.4.6","pid":1234}`+"\n"), 0o600); err != nil {
		t.Fatalf("write marker without exe: %v", err)
	}
	if muxcoreDaemonVersionMarkerMatches(path, "v6.4.6", 1234, currentExe) {
		t.Fatal("marker without executable path must not match")
	}

	if err := os.WriteFile(path, []byte(`{"version":"v6.4.6","pid":1234,"exe":"`+filepath.ToSlash(currentExe)+`"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write marker with exe: %v", err)
	}
	if !muxcoreDaemonVersionMarkerMatches(path, "v6.4.6", 1234, filepath.ToSlash(currentExe)) {
		t.Fatal("marker with matching version, pid, and executable should match")
	}
	if muxcoreDaemonVersionMarkerMatches(path, "v6.4.7", 1234, currentExe) {
		t.Fatal("different daemon version must not match")
	}
	if muxcoreDaemonVersionMarkerMatches(path, "v6.4.6", 5678, currentExe) {
		t.Fatal("different daemon pid must not match")
	}
	if muxcoreDaemonVersionMarkerMatches(path, "v6.4.6", 1234, filepath.Join(t.TempDir(), "other-engram.exe")) {
		t.Fatal("different daemon executable must not match")
	}

	for _, relativeExe := range []string{"engram.exe", "../engram.exe"} {
		if err := os.WriteFile(path, []byte(`{"version":"v6.4.6","pid":1234,"exe":"`+relativeExe+`"}`+"\n"), 0o600); err != nil {
			t.Fatalf("write marker with relative executable %q: %v", relativeExe, err)
		}
		if muxcoreDaemonVersionMarkerMatches(path, "v6.4.6", 1234, relativeExe) {
			t.Fatalf("strict match accepted relative executable %q", relativeExe)
		}
		if muxcoreDaemonVersionMarkerMatchesVersionAndPID(path, "v6.4.6", 1234) {
			t.Fatalf("relaxed match accepted relative executable %q", relativeExe)
		}
	}
}

func TestMuxcoreDaemonVersionMarkerMatchesVersionAndPID(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "muxcore-daemon.version")
	otherExe := filepath.Join(t.TempDir(), "other-engram.exe")
	if err := os.WriteFile(path, []byte(`{"version":"v6.4.6","pid":1234,"exe":"`+filepath.ToSlash(otherExe)+`"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if !muxcoreDaemonVersionMarkerMatchesVersionAndPID(path, "v6.4.6", 1234) {
		t.Fatal("matching version and status PID should ignore a concurrent winner's executable path")
	}
	if err := os.WriteFile(path, []byte(`{"version":"v6.4.6","pid":1234}`+"\n"), 0o600); err != nil {
		t.Fatalf("write marker without exe: %v", err)
	}
	if muxcoreDaemonVersionMarkerMatchesVersionAndPID(path, "v6.4.6", 1234) {
		t.Fatal("marker without executable path must not match")
	}
	if err := os.WriteFile(path, []byte(`{"version":"v6.4.6","pid":1234,"exe":"   "}`+"\n"), 0o600); err != nil {
		t.Fatalf("write marker with blank exe: %v", err)
	}
	if muxcoreDaemonVersionMarkerMatchesVersionAndPID(path, "v6.4.6", 1234) {
		t.Fatal("marker with blank executable path must not match")
	}
	if err := os.WriteFile(path, []byte(`{"version":"v6.4.6","pid":1234,"exe":"C:/other/engram.exe"}`+"\n"), 0o600); err != nil {
		t.Fatalf("restore marker: %v", err)
	}
	if muxcoreDaemonVersionMarkerMatchesVersionAndPID(path, "v6.4.7", 1234) {
		t.Fatal("different daemon version must not match")
	}
	if muxcoreDaemonVersionMarkerMatchesVersionAndPID(path, "v6.4.6", 5678) {
		t.Fatal("different daemon PID must not match")
	}
	if err := os.WriteFile(path, []byte("not json\n"), 0o600); err != nil {
		t.Fatalf("write malformed marker: %v", err)
	}
	if muxcoreDaemonVersionMarkerMatchesVersionAndPID(path, "v6.4.6", 1234) {
		t.Fatal("malformed marker must not match")
	}
}

func TestExecutablePathsAreCaseInsensitive(t *testing.T) {
	t.Parallel()

	if !executablePathsAreCaseInsensitive("windows") {
		t.Fatal("Windows executable paths should be compared case-insensitively")
	}
	if !executablePathsAreCaseInsensitive("darwin") {
		t.Fatal("macOS executable paths should be compared case-insensitively")
	}
	if executablePathsAreCaseInsensitive("linux") {
		t.Fatal("Linux executable paths should stay case-sensitive")
	}
}

func TestMuxcoreDaemonConfigPreservesPersistentAlwaysConnectedPolicy(t *testing.T) {
	t.Parallel()

	cfg := muxcoreDaemonConfig(nil)
	if !cfg.Persistent {
		t.Fatal("Engram owner must remain persistent")
	}
	if cfg.Namespace != muxcoreNamespace {
		t.Fatalf("muxcore namespace = %q, want legacy-compatible %q", cfg.Namespace, muxcoreNamespace)
	}
	if cfg.IdleSuspendDelay != 0 {
		t.Fatalf("IdleSuspendDelay = %s, want zero for always-connected transport", cfg.IdleSuspendDelay)
	}
	if cfg.IdleDormantGrace != 0 {
		t.Fatalf("IdleDormantGrace = %s, want zero without a capable native supervisor", cfg.IdleDormantGrace)
	}
	if cfg.AllowPersistentIdleSuspend {
		t.Fatal("AllowPersistentIdleSuspend must remain false while Engram emits unbuffered background notifications")
	}
	if cfg.Registry == nil {
		t.Fatal("muxcore registry metadata must be configured")
	}
	if got := cfg.Registry.MuxcoreVersion; got != muxcoreEmbeddedVersion {
		t.Fatalf("registry muxcore version = %q, want %q", got, muxcoreEmbeddedVersion)
	}
	if !cfg.Registry.Capabilities.ListOwners {
		t.Fatal("registry ListOwners capability must remain enabled")
	}

	shimCfg := muxcoreShimConfig()
	if shimCfg.Persistent {
		t.Fatal("per-host muxcore shim owner must be non-persistent")
	}
	if shimCfg.IdleSuspendDelay != 0 || shimCfg.IdleDormantGrace != 0 {
		t.Fatalf("shim idle lifecycle = (%s, %s), want disabled until native supervisor release", shimCfg.IdleSuspendDelay, shimCfg.IdleDormantGrace)
	}
	if shimCfg.AllowPersistentIdleSuspend {
		t.Fatal("shim must not assert persistent idle-suspend safety while Engram emits background notifications")
	}
	if shimCfg.SessionHandler != nil {
		t.Fatal("shim config must not initialize the daemon SessionHandler")
	}
	if shimCfg.Handler == nil {
		t.Fatal("shim config must provide a non-serving Handler shape for engine.New")
	}
	if _, err := engine.New(shimCfg); err != nil {
		t.Fatalf("engine.New(shim config) error = %v", err)
	}
}

func TestReconcileMuxcoreDaemonVersionUsesProviderRestart(t *testing.T) {
	t.Setenv("MCP_MUX_SESSION_ID", "")
	originalStatus := readMuxcoreDaemonStatusPID
	originalCurrent := isCurrentMuxcoreDaemon
	originalExecutable := currentExecutable
	originalRestart := restartMuxcoreDaemon
	originalWait := waitForCurrentMuxcoreDaemonReady
	t.Cleanup(func() {
		readMuxcoreDaemonStatusPID = originalStatus
		isCurrentMuxcoreDaemon = originalCurrent
		currentExecutable = originalExecutable
		restartMuxcoreDaemon = originalRestart
		waitForCurrentMuxcoreDaemonReady = originalWait
	})

	readMuxcoreDaemonStatusPID = func(string) (int, bool) { return 4242, true }
	isCurrentMuxcoreDaemon = func(string, string, int) bool { return false }
	currentExecutable = func() (string, error) { return `C:\Engram\engram.exe`, nil }
	restarted := false
	restartMuxcoreDaemon = func(_ context.Context, successorExe string) (engine.UpdateAndRestartResult, error) {
		restarted = true
		if successorExe != `C:\Engram\engram.exe` {
			t.Fatalf("successor executable = %q", successorExe)
		}
		return engine.UpdateAndRestartResult{
			DaemonWasRunning:  true,
			GracefulRestarted: true,
			ReplacementReady:  true,
		}, nil
	}
	waited := false
	waitForCurrentMuxcoreDaemonReady = func(context.Context) error {
		waited = true
		return nil
	}

	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatalf("reconcileMuxcoreDaemonVersion() error = %v", err)
	}
	if !restarted {
		t.Fatal("stale daemon was not routed through muxcore RestartWithSuccessor")
	}
	if !waited {
		t.Fatal("provider-ready replacement was accepted before marker convergence")
	}
}

func TestReconcileMuxcoreDaemonVersionFailsWhenReadyReplacementDoesNotConverge(t *testing.T) {
	t.Setenv("MCP_MUX_SESSION_ID", "")
	originalStatus := readMuxcoreDaemonStatusPID
	originalCurrent := isCurrentMuxcoreDaemon
	originalExecutable := currentExecutable
	originalRestart := restartMuxcoreDaemon
	originalWait := waitForCurrentMuxcoreDaemonReady
	t.Cleanup(func() {
		readMuxcoreDaemonStatusPID = originalStatus
		isCurrentMuxcoreDaemon = originalCurrent
		currentExecutable = originalExecutable
		restartMuxcoreDaemon = originalRestart
		waitForCurrentMuxcoreDaemonReady = originalWait
	})

	readMuxcoreDaemonStatusPID = func(string) (int, bool) { return 4242, true }
	isCurrentMuxcoreDaemon = func(string, string, int) bool { return false }
	currentExecutable = func() (string, error) { return `C:\Engram\engram.exe`, nil }
	restartMuxcoreDaemon = func(context.Context, string) (engine.UpdateAndRestartResult, error) {
		return engine.UpdateAndRestartResult{
			DaemonWasRunning: true,
			ReplacementReady: true,
		}, nil
	}
	waitForCurrentMuxcoreDaemonReady = func(context.Context) error {
		return errors.New("replacement marker stayed stale")
	}

	err := reconcileMuxcoreDaemonVersion(context.Background())
	if err == nil {
		t.Fatal("provider-ready replacement without marker convergence must fail closed")
	}
	if !strings.Contains(err.Error(), "replacement marker stayed stale") {
		t.Fatalf("error = %v, want marker convergence failure", err)
	}
}

func TestReconcileMuxcoreDaemonVersionReturnsProviderRestartError(t *testing.T) {
	t.Setenv("MCP_MUX_SESSION_ID", "")
	originalStatus := readMuxcoreDaemonStatusPID
	originalCurrent := isCurrentMuxcoreDaemon
	originalExecutable := currentExecutable
	originalRestart := restartMuxcoreDaemon
	t.Cleanup(func() {
		readMuxcoreDaemonStatusPID = originalStatus
		isCurrentMuxcoreDaemon = originalCurrent
		currentExecutable = originalExecutable
		restartMuxcoreDaemon = originalRestart
	})

	readMuxcoreDaemonStatusPID = func(string) (int, bool) { return 4242, true }
	isCurrentMuxcoreDaemon = func(string, string, int) bool { return false }
	currentExecutable = func() (string, error) { return `C:\Engram\engram.exe`, nil }
	restartMuxcoreDaemon = func(context.Context, string) (engine.UpdateAndRestartResult, error) {
		return engine.UpdateAndRestartResult{}, errors.New("restart blocked")
	}

	err := reconcileMuxcoreDaemonVersion(context.Background())
	if err == nil {
		t.Fatal("provider restart failure must be returned for caller recovery policy")
	}
	if !strings.Contains(err.Error(), "restart blocked") {
		t.Fatalf("error = %v, want provider restart failure", err)
	}
}

func TestReconcileMuxcoreDaemonVersionJoinsConcurrentRestart(t *testing.T) {
	t.Setenv("MCP_MUX_SESSION_ID", "")
	originalStatus := readMuxcoreDaemonStatusPID
	originalCurrent := isCurrentMuxcoreDaemon
	originalExecutable := currentExecutable
	originalRestart := restartMuxcoreDaemon
	originalWait := waitForCurrentMuxcoreDaemonReady
	t.Cleanup(func() {
		readMuxcoreDaemonStatusPID = originalStatus
		isCurrentMuxcoreDaemon = originalCurrent
		currentExecutable = originalExecutable
		restartMuxcoreDaemon = originalRestart
		waitForCurrentMuxcoreDaemonReady = originalWait
	})

	readMuxcoreDaemonStatusPID = func(string) (int, bool) { return 4242, true }
	isCurrentMuxcoreDaemon = func(string, string, int) bool { return false }
	currentExecutable = func() (string, error) { return `C:\Engram\engram.exe`, nil }
	restartMuxcoreDaemon = func(context.Context, string) (engine.UpdateAndRestartResult, error) {
		return engine.UpdateAndRestartResult{}, &engine.UpdateAndRestartError{
			Phase: engine.UpdatePhaseLock,
			Err:   errors.New("restart lock held"),
		}
	}
	waited := false
	waitForCurrentMuxcoreDaemonReady = func(context.Context) error {
		waited = true
		return nil
	}

	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatalf("reconcileMuxcoreDaemonVersion() error = %v", err)
	}
	if !waited {
		t.Fatal("concurrent restart did not wait for the winning replacement")
	}
}

func TestReconcileMuxcoreDaemonVersionFailsWhenConcurrentRestartDoesNotConverge(t *testing.T) {
	t.Setenv("MCP_MUX_SESSION_ID", "")
	originalStatus := readMuxcoreDaemonStatusPID
	originalCurrent := isCurrentMuxcoreDaemon
	originalExecutable := currentExecutable
	originalRestart := restartMuxcoreDaemon
	originalWait := waitForCurrentMuxcoreDaemonReady
	t.Cleanup(func() {
		readMuxcoreDaemonStatusPID = originalStatus
		isCurrentMuxcoreDaemon = originalCurrent
		currentExecutable = originalExecutable
		restartMuxcoreDaemon = originalRestart
		waitForCurrentMuxcoreDaemonReady = originalWait
	})

	readMuxcoreDaemonStatusPID = func(string) (int, bool) { return 4242, true }
	isCurrentMuxcoreDaemon = func(string, string, int) bool { return false }
	currentExecutable = func() (string, error) { return `C:\Engram\engram.exe`, nil }
	restartMuxcoreDaemon = func(context.Context, string) (engine.UpdateAndRestartResult, error) {
		return engine.UpdateAndRestartResult{}, &engine.UpdateAndRestartError{
			Phase: engine.UpdatePhaseLock,
			Err:   errors.New("restart lock held"),
		}
	}
	waitForCurrentMuxcoreDaemonReady = func(context.Context) error {
		return errors.New("replacement wait expired")
	}

	err := reconcileMuxcoreDaemonVersion(context.Background())
	if err == nil {
		t.Fatal("non-converging concurrent restart must fail closed")
	}
	if !strings.Contains(err.Error(), "restart lock held") || !strings.Contains(err.Error(), "replacement wait expired") {
		t.Fatalf("error = %v, want restart and convergence failures", err)
	}
}

func TestReconcileMuxcoreDaemonVersionPropagatesParentCancellation(t *testing.T) {
	t.Setenv("MCP_MUX_SESSION_ID", "")
	originalStatus := readMuxcoreDaemonStatusPID
	originalCurrent := isCurrentMuxcoreDaemon
	originalExecutable := currentExecutable
	originalRestart := restartMuxcoreDaemon
	t.Cleanup(func() {
		readMuxcoreDaemonStatusPID = originalStatus
		isCurrentMuxcoreDaemon = originalCurrent
		currentExecutable = originalExecutable
		restartMuxcoreDaemon = originalRestart
	})

	readMuxcoreDaemonStatusPID = func(string) (int, bool) { return 4242, true }
	isCurrentMuxcoreDaemon = func(string, string, int) bool { return false }
	currentExecutable = func() (string, error) { return `C:\Engram\engram.exe`, nil }
	restartStarted := make(chan struct{})
	restartMuxcoreDaemon = func(ctx context.Context, _ string) (engine.UpdateAndRestartResult, error) {
		close(restartStarted)
		<-ctx.Done()
		return engine.UpdateAndRestartResult{}, ctx.Err()
	}

	parent, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- reconcileMuxcoreDaemonVersion(parent)
	}()
	<-restartStarted
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want parent cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconciliation ignored parent cancellation")
	}
}
