package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestReadLiveMuxcoreDaemonActionRejectsLegacyWhenSchemaTwoIsUncorrelated(t *testing.T) {
	dir := t.TempDir()
	v2Path := filepath.Join(dir, "daemon.marker.json")
	legacyPath := filepath.Join(dir, "daemon.version")
	exe := filepath.ToSlash(filepath.Join(dir, "engram.exe"))
	if err := os.WriteFile(v2Path, []byte(`{"schema_version":2,"product_version":"v6.47.0","daemon_compat_epoch":1,"pid":11,"daemon_generation":"new","exe":"`+exe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"version":"v6.46.4","pid":12,"exe":"`+exe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	action, err := readLiveMuxcoreDaemonActionAt(v2Path, legacyPath, muxcoreDaemonStatusIdentity{PID: 12, DaemonGeneration: "legacy"}, daemonConvergenceIdentity{ProductVersion: "v6.47.0", DaemonCompatEpoch: 1})
	if err == nil || action != daemonConvergenceFail {
		t.Fatalf("schema-2 marker must prevent legacy fallback: action, error = %v, %v", action, err)
	}
}

func TestReadLiveMuxcoreDaemonActionRejectsUncorrelatedHigherDaemon(t *testing.T) {
	dir := t.TempDir()
	v2Path := filepath.Join(dir, "daemon.marker.json")
	legacyPath := filepath.Join(dir, "daemon.version")
	exe := filepath.ToSlash(filepath.Join(dir, "engram.exe"))
	if err := os.WriteFile(v2Path, []byte(`{"schema_version":2,"product_version":"v6.47.1","daemon_compat_epoch":1,"pid":41,"daemon_generation":"old","exe":"`+exe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"version":"v6.47.1","pid":42,"exe":"`+exe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	action, err := readLiveMuxcoreDaemonActionAt(v2Path, legacyPath, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "new"}, daemonConvergenceIdentity{ProductVersion: "v6.47.0", DaemonCompatEpoch: 1})
	if err == nil || action != daemonConvergenceFail {
		t.Fatalf("action, error = %v, %v; want fail-closed higher daemon", action, err)
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
	oldLock, oldContended := acquireRestartLock, isRestartLockContended
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return status, true }
	readLiveMuxcoreDaemonAction = func(muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (daemonConvergenceAction, error) {
		return action, actionErr
	}
	acquireRestartLock = func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, currentExecutable, restartMuxcoreDaemon, waitForCurrentMuxcoreDaemonReady = oldStatus, oldAction, oldExe, oldRestart, oldWait
		acquireRestartLock, isRestartLockContended = oldLock, oldContended
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

func TestWriteMuxcoreDaemonVersionMarkerPublishesBidirectionalMarkers(t *testing.T) {
	oldStatus, oldExe := readMuxcoreDaemonStatusIdentity, currentExecutable
	t.Cleanup(func() { readMuxcoreDaemonStatusIdentity, currentExecutable = oldStatus, oldExe })
	path := filepath.Join(t.TempDir(), "marker.marker.json")
	legacyPath := filepath.Join(filepath.Dir(path), "marker.version")
	currentExecutable = func() (string, error) { return filepath.Join(t.TempDir(), "engram.exe"), nil }
	generation := "daemon-new"
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		return muxcoreDaemonStatusIdentity{PID: os.Getpid(), DaemonGeneration: generation}, true
	}
	if err := writeMuxcoreDaemonVersionMarkerAt(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	generation = "daemon-newer"
	if err := writeMuxcoreDaemonVersionMarkerAt(path); err != nil {
		t.Fatalf("overwrite existing Windows markers: %v", err)
	}
	marker, err := readMuxcoreDaemonVersionMarker(path)
	if err != nil || marker.SchemaVersion != 2 || marker.PID != os.Getpid() || marker.DaemonGeneration != generation {
		t.Fatalf("published schema-2 marker = %#v, error = %v", marker, err)
	}
	legacy, err := os.ReadFile(legacyPath)
	if err != nil || !strings.Contains(string(legacy), `"version":"`+daemonVersion+`"`) {
		t.Fatalf("published legacy marker = %q, error = %v", legacy, err)
	}
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		return muxcoreDaemonStatusIdentity{PID: os.Getpid() + 1, DaemonGeneration: "daemon-wrong"}, true
	}
	if err := writeMuxcoreDaemonVersionMarkerAt(filepath.Join(t.TempDir(), "missing.marker.json")); err == nil {
		t.Fatal("uncorrelated marker publication succeeded")
	}
}

func TestWriteMuxcoreDaemonVersionMarkerPartialPublishFailsClosed(t *testing.T) {
	oldStatus, oldExe, oldWrite := readMuxcoreDaemonStatusIdentity, currentExecutable, writeMuxcoreMarker
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, currentExecutable, writeMuxcoreMarker = oldStatus, oldExe, oldWrite
	})
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "daemon.marker.json")
	legacyPath := filepath.Join(dir, "daemon.version")
	exe := filepath.ToSlash(filepath.Join(dir, "engram.exe"))
	if err := os.WriteFile(markerPath, []byte(`{"schema_version":2,"product_version":"v6.46.4","daemon_compat_epoch":1,"pid":11,"daemon_generation":"old","exe":"`+exe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"version":"v6.46.4","pid":11,"exe":"`+exe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	currentExecutable = func() (string, error) { return exe, nil }
	status := muxcoreDaemonStatusIdentity{PID: os.Getpid(), DaemonGeneration: "new"}
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return status, true }
	writes := 0
	writeMuxcoreMarker = func(path string, payload []byte) error {
		writes++
		if writes == 1 {
			return oldWrite(path, payload)
		}
		return errors.New("schema-2 marker publish failed")
	}
	if err := writeMuxcoreDaemonVersionMarkerAt(markerPath); err == nil {
		t.Fatal("partial marker publication succeeded")
	}
	legacy, err := os.ReadFile(legacyPath)
	if err != nil || !strings.Contains(string(legacy), `"version":"`+daemonVersion+`"`) {
		t.Fatalf("legacy marker did not publish new daemon identity: %q, %v", legacy, err)
	}
	action, err := readLiveMuxcoreDaemonActionAt(markerPath, legacyPath, status, daemonConvergenceIdentity{ProductVersion: daemonVersion, DaemonCompatEpoch: muxcoreDaemonCompatEpoch})
	if err == nil || action != daemonConvergenceFail {
		t.Fatalf("partial markers action, error = %v, %v; want fail, error", action, err)
	}
}

func TestWaitForMuxcoreDaemonVersionAcceptsFreshMarkerAfterStaleValidWindow(t *testing.T) {
	oldStatus, oldAction := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction
	t.Cleanup(func() { readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction = oldStatus, oldAction })
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "daemon.marker.json")
	legacyPath := filepath.Join(dir, "daemon.version")
	exe := filepath.ToSlash(filepath.Join(dir, "engram.exe"))
	writeMarker := func(pid int, generation string) {
		t.Helper()
		raw := `{"schema_version":2,"product_version":"` + daemonVersion + `","daemon_compat_epoch":1,"pid":` + strconv.Itoa(pid) + `,"daemon_generation":"` + generation + `","exe":"` + exe + `"}`
		if err := os.WriteFile(markerPath, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeMarker(41, "old")
	status := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "new"}
	statusReads := 0
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		statusReads++
		if statusReads == 2 {
			writeMarker(status.PID, status.DaemonGeneration)
		}
		return status, true
	}
	readLiveMuxcoreDaemonAction = func(live muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity) (daemonConvergenceAction, error) {
		return readLiveMuxcoreDaemonActionAt(markerPath, legacyPath, live, client)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForMuxcoreDaemonVersion(ctx); err != nil {
		t.Fatal(err)
	}
	if statusReads < 2 {
		t.Fatal("stale marker did not consume a bounded publication retry")
	}
}

func TestReconcileMuxcoreDaemonVersionWaitsForUncorrelatedMarker(t *testing.T) {
	stubReconciliation(t, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-starting"}, daemonConvergenceFail, errMuxcoreDaemonMarkerUncorrelated)
	called := false
	restartMuxcoreDaemon = func(context.Context, string) (engine.UpdateAndRestartResult, error) {
		called = true
		return engine.UpdateAndRestartResult{}, nil
	}
	waitForCurrentMuxcoreDaemonReady = func(context.Context) error { return nil }
	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("uncorrelated schema-2 marker triggered replacement")
	}
}

func TestReconcileMuxcoreDaemonVersionWaitsForLongLockHolderWithoutRetryExhaustion(t *testing.T) {
	status := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-old"}
	oldStatus, oldAction, oldLock, oldContended := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, acquireRestartLock, isRestartLockContended
	defer func() {
		readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, acquireRestartLock, isRestartLockContended = oldStatus, oldAction, oldLock, oldContended
	}()
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return status, true }
	actionReads := 0
	readLiveMuxcoreDaemonAction = func(muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (daemonConvergenceAction, error) {
		actionReads++
		if actionReads > 10 {
			return daemonConvergenceJoin, nil
		}
		return daemonConvergenceReplace, nil
	}
	lockAttempts := 0
	acquireRestartLock = func() (io.Closer, error) {
		lockAttempts++
		return nil, errors.New("held")
	}
	isRestartLockContended = func(error) bool { return true }
	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	if actionReads <= 10 || lockAttempts <= 1 {
		t.Fatalf("action reads=%d lock probes=%d; long holder was not waited under the outer deadline", actionReads, lockAttempts)
	}
}

func TestReconcileMuxcoreDaemonVersionRechecksStatusUnderLock(t *testing.T) {
	lower := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-old"}
	higher := muxcoreDaemonStatusIdentity{PID: 43, DaemonGeneration: "daemon-new"}
	oldStatus, oldAction, oldLock, oldRestart := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, acquireRestartLock, restartMuxcoreDaemon
	defer func() {
		readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, acquireRestartLock, restartMuxcoreDaemon = oldStatus, oldAction, oldLock, oldRestart
	}()
	current := lower
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return current, true }
	readLiveMuxcoreDaemonAction = func(status muxcoreDaemonStatusIdentity, _ daemonConvergenceIdentity) (daemonConvergenceAction, error) {
		if sameMuxcoreDaemon(status, higher) {
			return daemonConvergenceJoin, nil
		}
		return daemonConvergenceReplace, nil
	}
	acquireRestartLock = func() (io.Closer, error) {
		current = higher
		return io.NopCloser(strings.NewReader("")), nil
	}
	restarted := false
	restartMuxcoreDaemon = func(context.Context, string) (engine.UpdateAndRestartResult, error) {
		restarted = true
		return engine.UpdateAndRestartResult{}, nil
	}
	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarted {
		t.Fatal("stale lower decision restarted a newer daemon")
	}
}

func TestWaitForMuxcoreDaemonVersionAcceptsFreshMarkerAfterMissingWindow(t *testing.T) {
	oldStatus, oldAction := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction
	t.Cleanup(func() { readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction = oldStatus, oldAction })
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "daemon.marker.json")
	legacyPath := filepath.Join(dir, "daemon.version")
	exe := filepath.ToSlash(filepath.Join(dir, "engram.exe"))
	status := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "new"}
	statusReads := 0
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		statusReads++
		if statusReads == 2 {
			raw := `{"schema_version":2,"product_version":"` + daemonVersion + `","daemon_compat_epoch":1,"pid":` + strconv.Itoa(status.PID) + `,"daemon_generation":"` + status.DaemonGeneration + `","exe":"` + exe + `"}`
			if err := os.WriteFile(markerPath, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return status, true
	}
	readLiveMuxcoreDaemonAction = func(live muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity) (daemonConvergenceAction, error) {
		return readLiveMuxcoreDaemonActionAt(markerPath, legacyPath, live, client)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForMuxcoreDaemonVersion(ctx); err != nil {
		t.Fatal(err)
	}
	if statusReads < 2 {
		t.Fatal("missing marker did not consume a bounded publication retry")
	}
}

func TestVerifyCurrentMuxcoreDaemonIdentityRejectsLowerElectionWinner(t *testing.T) {
	oldStatus, oldAction := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction
	t.Cleanup(func() { readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction = oldStatus, oldAction })
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		return muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "lower-winner"}, true
	}
	readLiveMuxcoreDaemonAction = func(muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (daemonConvergenceAction, error) {
		return daemonConvergenceReplace, nil
	}
	if err := verifyCurrentMuxcoreDaemonIdentity(muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "lower-winner"}); err == nil {
		t.Fatal("lower cold-start election winner was accepted")
	}
}

func TestReadLiveMuxcoreDaemonActionRejectsMalformedSchemaBeforeLegacyFallback(t *testing.T) {
	dir := t.TempDir()
	v2Path := filepath.Join(dir, "daemon.marker.json")
	legacyPath := filepath.Join(dir, "daemon.version")
	exe := filepath.ToSlash(filepath.Join(dir, "engram.exe"))
	if err := os.WriteFile(v2Path, []byte(`{"schema_version":2,"pid":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"version":"v6.46.4","pid":42,"exe":"`+exe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	action, err := readLiveMuxcoreDaemonActionAt(v2Path, legacyPath, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "legacy"}, daemonConvergenceIdentity{ProductVersion: "v6.47.0", DaemonCompatEpoch: 1})
	if err == nil || action != daemonConvergenceFail {
		t.Fatalf("malformed schema action, error = %v, %v; want fail, error", action, err)
	}
}

func TestVerifyCurrentMuxcoreDaemonIdentityRequiresExactLiveIdentity(t *testing.T) {
	oldStatus, oldAction := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction
	defer func() { readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction = oldStatus, oldAction }()
	expected := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "generation-a"}
	readLiveMuxcoreDaemonAction = func(muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (daemonConvergenceAction, error) {
		return daemonConvergenceJoin, nil
	}
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return expected, true }
	if err := verifyCurrentMuxcoreDaemonIdentity(expected); err != nil {
		t.Fatalf("exact live daemon was rejected: %v", err)
	}
	for _, status := range []muxcoreDaemonStatusIdentity{
		{PID: 43, DaemonGeneration: "generation-a"},
		{PID: 42, DaemonGeneration: "generation-b"},
		{PID: 42, DaemonGeneration: "generation-a", ShuttingDown: true},
	} {
		readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return status, true }
		if err := verifyCurrentMuxcoreDaemonIdentity(expected); err == nil {
			t.Fatalf("status %#v was accepted for %#v", status, expected)
		}
	}
}
func TestDaemonTerminationStatusTreatsCancellationAndCleanCompletionAsNormal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if normal, observed := daemonTerminationStatus(ctx, make(chan error, 1)); !normal || !observed {
		t.Fatal("cancellation during marker publication was treated as fatal")
	}
	done := make(chan error, 1)
	done <- nil
	if normal, observed := daemonTerminationStatus(context.Background(), done); !normal || !observed {
		t.Fatal("clean daemon completion during marker verification was treated as fatal")
	}
}

func TestReadLiveMuxcoreDaemonActionTreatsShuttingDownAsTransient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.json")
	exe := filepath.ToSlash(filepath.Join(t.TempDir(), "engram.exe"))
	if err := os.WriteFile(path, []byte(`{"schema_version":2,"product_version":"v6.47.0","daemon_compat_epoch":1,"pid":42,"daemon_generation":"generation-a","exe":"`+exe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	action, err := readLiveMuxcoreDaemonActionAt(path, path+".legacy", muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "generation-a", ShuttingDown: true}, daemonConvergenceIdentity{ProductVersion: "v6.47.0", DaemonCompatEpoch: 1})
	if action != daemonConvergenceFail || !errors.Is(err, errMuxcoreDaemonMarkerUncorrelated) {
		t.Fatalf("shutting-down daemon action, error = %v, %v", action, err)
	}
}

func TestReconcileMuxcoreDaemonVersionReprobesFreedLock(t *testing.T) {
	status := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-old"}
	oldStatus, oldAction, oldLock, oldContended, oldExe, oldRestart := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, acquireRestartLock, isRestartLockContended, currentExecutable, restartMuxcoreDaemon
	defer func() {
		readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, acquireRestartLock, isRestartLockContended, currentExecutable, restartMuxcoreDaemon = oldStatus, oldAction, oldLock, oldContended, oldExe, oldRestart
	}()
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return status, true }
	readLiveMuxcoreDaemonAction = func(muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (daemonConvergenceAction, error) {
		return daemonConvergenceReplace, nil
	}
	currentExecutable = func() (string, error) { return filepath.Join(t.TempDir(), "engram.exe"), nil }
	acquisitions := 0
	acquireRestartLock = func() (io.Closer, error) {
		acquisitions++
		if acquisitions == 1 {
			return nil, errors.New("held")
		}
		return io.NopCloser(strings.NewReader("")), nil
	}
	isRestartLockContended = func(error) bool { return true }
	restarted := false
	restartMuxcoreDaemon = func(context.Context, string) (engine.UpdateAndRestartResult, error) {
		restarted = true
		return engine.UpdateAndRestartResult{}, nil
	}
	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	if acquisitions != 3 || !restarted {
		t.Fatalf("acquisitions=%d restarted=%t; freed lock was not freshly fenced", acquisitions, restarted)
	}
}

type failingCloser struct{}

func (failingCloser) Close() error { return errors.New("unlock failed") }

func TestReconcileMuxcoreDaemonVersionDoesNotFailSuccessfulRestartOnUnlockError(t *testing.T) {
	stubReconciliation(t, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-old"}, daemonConvergenceReplace, nil)
	currentExecutable = func() (string, error) { return filepath.Join(t.TempDir(), "engram.exe"), nil }
	acquireRestartLock = func() (io.Closer, error) { return failingCloser{}, nil }
	restartMuxcoreDaemon = func(context.Context, string) (engine.UpdateAndRestartResult, error) {
		return engine.UpdateAndRestartResult{}, nil
	}
	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatalf("successful restart was reported failed after unlock error: %v", err)
	}
}
