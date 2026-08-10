package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/module/dispatcher"
	"github.com/thebtf/mcp-mux/muxcore/engine"
)

func TestClassifyDaemonConvergence(t *testing.T) {
	client := daemonConvergenceIdentity{ProductVersion: "v6.47.0", DaemonCompatEpoch: 1}
	for _, test := range []struct {
		name    string
		live    daemonConvergenceIdentity
		want    daemonConvergenceAction
		wantErr string
	}{
		{"lower epoch replaces", daemonConvergenceIdentity{ProductVersion: "v6.99.0", DaemonCompatEpoch: 0}, daemonConvergenceReplace, ""},
		{"equal epoch lower version replaces", daemonConvergenceIdentity{ProductVersion: "v6.46.4", DaemonCompatEpoch: 1}, daemonConvergenceReplace, ""},
		{"equal epoch equal version joins", daemonConvergenceIdentity{ProductVersion: "v6.47.0", DaemonCompatEpoch: 1}, daemonConvergenceJoin, ""},
		{"equal epoch higher version joins", daemonConvergenceIdentity{ProductVersion: "v6.47.1", DaemonCompatEpoch: 1}, daemonConvergenceJoin, ""},
		{"higher epoch fails closed", daemonConvergenceIdentity{ProductVersion: "v6.47.1", DaemonCompatEpoch: 2}, daemonConvergenceFail, "incompatible newer"},
		{"malformed version fails closed", daemonConvergenceIdentity{ProductVersion: "6.47.0", DaemonCompatEpoch: 1}, daemonConvergenceFail, "malformed"},
		{"malformed epoch fails closed", daemonConvergenceIdentity{ProductVersion: "v6.47.0", DaemonCompatEpoch: -1}, daemonConvergenceFail, "malformed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := classifyDaemonConvergence(client, test.live)
			if got != test.want {
				t.Fatalf("action = %v, want %v", got, test.want)
			}
			if test.wantErr == "" && err != nil {
				t.Fatalf("error = %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestReadMuxcoreDaemonVersionMarkerRejectsMalformedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.json")
	validExe := filepath.ToSlash(filepath.Join(t.TempDir(), "engram.exe"))
	valid := `{"schema_version":2,"product_version":"v6.47.0","daemon_compat_epoch":1,"pid":42,"daemon_generation":"daemon-1","exe":"` + validExe + `"}`
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{"valid schema two", valid, true},
		{"schema one rejected", strings.Replace(valid, `"schema_version":2`, `"schema_version":1`, 1), false},
		{"unknown field rejected", strings.TrimSuffix(valid, "}") + `,"extra":true}`, false},
		{"missing generation rejected", strings.Replace(valid, `"daemon_generation":"daemon-1",`, "", 1), false},
		{"relative executable rejected", strings.Replace(valid, `"exe":"`+validExe+`"`, `"exe":"engram.exe"`, 1), false},
		{"noncanonical semver rejected", strings.Replace(valid, "v6.47.0", "6.47.0", 1), false},
		{"multiple JSON values rejected", valid + "\n{}", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := readMuxcoreDaemonVersionMarker(path)
			if (err == nil) != test.want {
				t.Fatalf("read error = %v, want valid=%t", err, test.want)
			}
		})
	}
}

func TestMuxcoreDaemonConvergenceActionCorrelatesStatusAndLimitsLegacyTakeover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.json")
	client := daemonConvergenceIdentity{ProductVersion: "v6.47.0", DaemonCompatEpoch: 1}
	exe := filepath.ToSlash(filepath.Join(t.TempDir(), "engram.exe"))
	v2 := `{"schema_version":2,"product_version":"v6.47.0","daemon_compat_epoch":1,"pid":42,"daemon_generation":"daemon-1","exe":"` + exe + `"}`
	legacy := `{"version":"v6.46.4","pid":42,"exe":"` + exe + `"}`
	for _, test := range []struct {
		name    string
		raw     string
		status  muxcoreDaemonStatusIdentity
		client  daemonConvergenceIdentity
		legacy  bool
		want    daemonConvergenceAction
		wantErr bool
	}{
		{"v2 PID and generation correlate", v2, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-1"}, client, false, daemonConvergenceJoin, false},
		{"PID mismatch fails closed", v2, muxcoreDaemonStatusIdentity{PID: 43, DaemonGeneration: "daemon-1"}, client, false, daemonConvergenceFail, true},
		{"generation mismatch fails closed", v2, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-2"}, client, false, daemonConvergenceFail, true},
		{"known legacy takes over", legacy, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-legacy"}, client, true, daemonConvergenceReplace, false},
		{"legacy PID mismatch fails closed", legacy, muxcoreDaemonStatusIdentity{PID: 43, DaemonGeneration: "daemon-legacy"}, client, true, daemonConvergenceFail, true},
		{"other legacy version fails closed", strings.Replace(legacy, "v6.46.4", "v6.46.5", 1), muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-legacy"}, client, true, daemonConvergenceFail, true},
		{"legacy does not bypass non-epoch-one client", legacy, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-legacy"}, daemonConvergenceIdentity{ProductVersion: "v6.47.0", DaemonCompatEpoch: 2}, true, daemonConvergenceFail, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			var got daemonConvergenceAction
			var err error
			if test.legacy {
				got, err = muxcoreLegacyDaemonConvergenceAction(path, test.status, test.client)
			} else {
				got, err = muxcoreDaemonConvergenceAction(path, test.status, test.client)
			}
			if got != test.want || (err != nil) != test.wantErr {
				t.Fatalf("action, error = %v, %v; want %v, error=%t", got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestMuxcoreDaemonConfigPreservesPersistentAlwaysConnectedPolicy(t *testing.T) {
	cfg := muxcoreDaemonConfig(&dispatcher.Dispatcher{})
	if !cfg.Persistent || cfg.Namespace != muxcoreNamespace || cfg.LifecycleProtocol.Enabled() || cfg.AllowPersistentIdleSuspend {
		t.Fatal("daemon lifecycle configuration changed")
	}
	if cfg.Registry == nil || cfg.Registry.MuxcoreVersion != "v0.29.1" || !cfg.Registry.Capabilities.ListOwners {
		t.Fatal("muxcore v0.29.1 registry policy changed")
	}
	shim := muxcoreShimConfig()
	if shim.Persistent || shim.SessionHandler != nil || shim.Handler == nil {
		t.Fatal("shim configuration changed")
	}
	if err := shim.Handler(context.Background(), nil, io.Discard); err == nil {
		t.Fatal("shim handler must remain non-serving")
	}
}

func stubReconciliation(t *testing.T, status muxcoreDaemonStatusIdentity, action daemonConvergenceAction, actionErr error) {
	t.Helper()
	oldStatus, oldAction, oldExe, oldRestart, oldWait := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, currentExecutable, restartMuxcoreDaemon, waitForCurrentMuxcoreDaemonReady
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return status, true }
	readLiveMuxcoreDaemonAction = func(muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (daemonConvergenceAction, error) {
		return action, actionErr
	}
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, currentExecutable, restartMuxcoreDaemon, waitForCurrentMuxcoreDaemonReady = oldStatus, oldAction, oldExe, oldRestart, oldWait
	})
}

func TestReconcileMuxcoreDaemonVersionProxyBypassesAuthority(t *testing.T) {
	t.Setenv("MCP_MUX_SESSION_ID", "external")
	old := readMuxcoreDaemonStatusIdentity
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		t.Fatal("proxy mode must not query muxcore status")
		return muxcoreDaemonStatusIdentity{}, false
	}
	t.Cleanup(func() { readMuxcoreDaemonStatusIdentity = old })
	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileMuxcoreDaemonVersionStartsNormallyWithoutLiveDaemon(t *testing.T) {
	oldStatus, oldExe := readMuxcoreDaemonStatusIdentity, currentExecutable
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return muxcoreDaemonStatusIdentity{}, false }
	currentExecutable = func() (string, error) {
		t.Fatal("no live daemon must not enter replacement")
		return "", nil
	}
	t.Cleanup(func() { readMuxcoreDaemonStatusIdentity, currentExecutable = oldStatus, oldExe })
	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileMuxcoreDaemonVersionDoesNotReplaceHigherOrMalformedDaemon(t *testing.T) {
	for _, test := range []struct {
		name   string
		action daemonConvergenceAction
		err    error
	}{
		{"higher epoch", daemonConvergenceFail, errors.New("incompatible newer daemon epoch 2")},
		{"uncorrelated marker", daemonConvergenceFail, errors.New("daemon marker does not correlate to fresh control status")},
	} {
		t.Run(test.name, func(t *testing.T) {
			stubReconciliation(t, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-1"}, test.action, test.err)
			called := false
			restartMuxcoreDaemon = func(context.Context, string) (engine.UpdateAndRestartResult, error) {
				called = true
				return engine.UpdateAndRestartResult{}, nil
			}
			if err := reconcileMuxcoreDaemonVersion(context.Background()); err == nil || !strings.Contains(err.Error(), test.err.Error()) {
				t.Fatalf("error = %v, want %q", err, test.err)
			}
			if called {
				t.Fatal("higher or malformed daemon reached restart")
			}
		})
	}
}

func TestReconcileMuxcoreDaemonVersionReplacesLowerAndWaitsForMarkerConvergence(t *testing.T) {
	stubReconciliation(t, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-old"}, daemonConvergenceReplace, nil)
	currentExecutable = func() (string, error) { return filepath.Join(t.TempDir(), "engram.exe"), nil }
	restarted, converged := false, false
	restartMuxcoreDaemon = func(context.Context, string) (engine.UpdateAndRestartResult, error) {
		restarted = true
		return engine.UpdateAndRestartResult{DaemonWasRunning: true, ReplacementReady: true}, nil
	}
	waitForCurrentMuxcoreDaemonReady = func(context.Context) error { converged = true; return nil }
	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !restarted || !converged {
		t.Fatal("lower daemon replacement did not require replacement marker convergence")
	}
}

func TestReconcileMuxcoreDaemonVersionLowerClientJoinsHigherConcurrentWinner(t *testing.T) {
	stubReconciliation(t, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-old"}, daemonConvergenceReplace, nil)
	currentExecutable = func() (string, error) { return filepath.Join(t.TempDir(), "engram.exe"), nil }
	restarts := 0
	restartMuxcoreDaemon = func(context.Context, string) (engine.UpdateAndRestartResult, error) {
		restarts++
		return engine.UpdateAndRestartResult{}, &engine.UpdateAndRestartError{Phase: engine.UpdatePhaseLock, Err: errors.New("winner holds restart lock")}
	}
	waitForCurrentMuxcoreDaemonReady = func(context.Context) error {
		action, err := classifyDaemonConvergence(
			daemonConvergenceIdentity{ProductVersion: "v6.47.0", DaemonCompatEpoch: 1},
			daemonConvergenceIdentity{ProductVersion: "v6.47.1", DaemonCompatEpoch: 1},
		)
		if err != nil || action != daemonConvergenceJoin {
			return errors.New("higher same-epoch winner was not joinable")
		}
		return nil
	}
	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarts != 1 {
		t.Fatalf("lower client restarted %d times, want only its lock-losing attempt", restarts)
	}
}

func TestWriteMuxcoreDaemonVersionMarkerPublishesOnlyCorrelatedIdentity(t *testing.T) {
	oldStatus, oldExe := readMuxcoreDaemonStatusIdentity, currentExecutable
	t.Cleanup(func() { readMuxcoreDaemonStatusIdentity, currentExecutable = oldStatus, oldExe })
	path := filepath.Join(t.TempDir(), "marker.json")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	currentExecutable = func() (string, error) { return filepath.Join(t.TempDir(), "engram.exe"), nil }
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		return muxcoreDaemonStatusIdentity{PID: os.Getpid(), DaemonGeneration: "daemon-new"}, true
	}
	writeMuxcoreDaemonVersionMarkerAt(path, logger)
	marker, err := readMuxcoreDaemonVersionMarker(path)
	if err != nil || marker.SchemaVersion != 2 || marker.PID != os.Getpid() || marker.DaemonGeneration != "daemon-new" {
		t.Fatalf("published marker = %#v, error = %v", marker, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		return muxcoreDaemonStatusIdentity{PID: os.Getpid() + 1, DaemonGeneration: "daemon-wrong"}, true
	}
	writeMuxcoreDaemonVersionMarkerAt(path, logger)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uncorrelated marker publication error = %v, want absent marker", err)
	}
}
