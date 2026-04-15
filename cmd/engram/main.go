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
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/thebtf/engram/internal/control"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/dispatcher"
	"github.com/thebtf/engram/internal/module/lifecycle"
	"github.com/thebtf/engram/internal/module/registry"
	"github.com/thebtf/mcp-mux/muxcore/engine"
	"github.com/thebtf/mcp-mux/muxcore/upgrade"
)

// daemonVersion is the string reported to gRPC Initialize and used in
// structured logs. Tracks Constitution §15 unified engram + plugin version.
const daemonVersion = "v4.3.0"

func main() {
	// Clean stale binaries from previous upgrades (.old.* files).
	if exePath, err := os.Executable(); err == nil {
		if cleaned := upgrade.CleanStale(exePath); cleaned > 0 {
			fmt.Fprintf(os.Stderr, "[engram] cleaned %d stale binary file(s)\n", cleaned)
		}
	}

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

	disp := dispatcher.New(reg, logger)
	pipeline := lifecycle.New(reg, logger)

	// Init context is distinct from daemon context — see design.md §3.2
	// and clarification C3 (Init ctx vs deps.DaemonCtx).
	initCtx, initCancel := context.WithCancel(context.Background())
	daemonCtx, daemonCancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer daemonCancel()

	if err := pipeline.Start(initCtx, depsProviderFor(logger, daemonCtx)); err != nil {
		initCancel()
		logger.Error("lifecycle Start failed", "error", err)
		os.Exit(1)
	}
	initCancel()

	// --- Control socket --------------------------------------------------
	// Must be started BEFORE engine.Run so that ensure-binary.js can
	// connect immediately after the PID file appears. The socket path is
	// ${ENGRAM_DATA_DIR}/run/engram.sock. On Windows the Start() call is a
	// no-op (named-pipe support deferred to v4.4.0).
	dd := dataDir()
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
		// Non-fatal: daemon continues without graceful-restart support.
		logger.Warn("control socket start failed — graceful-restart unavailable",
			"error", err,
		)
	}
	defer ctrlListener.Close()

	// --- muxcore engine boot ---------------------------------------------
	// The dispatcher satisfies BOTH muxcore.SessionHandler (HandleRequest)
	// and muxcore.ProjectLifecycle (OnProjectConnect/OnProjectDisconnect).
	// muxcore type-asserts on the SessionHandler to detect the optional
	// lifecycle methods — see muxcore.ProjectLifecycle docs.
	eng, err := engine.New(engine.Config{
		Name:           "engram",
		Persistent:     true,
		SessionHandler: disp,
	})
	if err != nil {
		logger.Error("engine.New failed", "error", err)
		_ = pipeline.ShutdownAll(daemonCtx)
		os.Exit(1)
	}

	logger.Info("engram daemon ready", "version", daemonVersion)

	if err := eng.Run(daemonCtx); err != nil && err != context.Canceled {
		logger.Error("engine.Run terminated", "error", err)
		_ = pipeline.ShutdownAll(daemonCtx)
		os.Exit(1)
	}

	logger.Info("engram daemon shutting down")
	if err := pipeline.ShutdownAll(daemonCtx); err != nil {
		logger.Error("lifecycle Shutdown error", "error", err)
	}
}

// handleGracefulRestart executes the full graceful-restart sequence:
//  1. Log INFO
//  2. Drain — stop accepting new tool calls (5 s sleep)
//  3. SnapshotAll — persist module state
//  4. ShutdownAll — clean module shutdown
//  5. Check for a .new binary written by ensure-binary.js
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
