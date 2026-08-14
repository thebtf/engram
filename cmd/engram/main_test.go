package main

import (
	"bufio"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/module/dispatcher"
	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	muxregistry "github.com/thebtf/mcp-mux/muxcore/registry"
	"github.com/thebtf/mcp-mux/muxcore/serverid"
)

const muxcoreLockTestRootEnv = "ENGRAM_TEST_MUXCORE_LOCK_ROOT"

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

func testLegacyMuxcoreBuildInfo() *buildinfo.BuildInfo {
	return &buildinfo.BuildInfo{
		Path: legacyEngramCommandPath,
		Main: debug.Module{Path: legacyEngramModulePath},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: legacyEngramRevision},
			{Key: "vcs.modified", Value: "false"},
		},
		Deps: []*debug.Module{{Path: legacyMuxcoreModulePath, Version: legacyMuxcoreEmbeddedVersion}},
	}
}

func testLegacyMuxcoreRegistry(controlPath string, pid int) []muxregistry.Record {
	descriptor := muxregistry.Descriptor{
		SchemaVersion:     muxregistry.SchemaVersion,
		EngineName:        muxcoreNamespace,
		ProductName:       muxcoreNamespace,
		PID:               pid,
		DaemonControlPath: controlPath,
		MuxcoreVersion:    legacyMuxcoreEmbeddedVersion,
		Capabilities:      muxregistry.Capabilities{ListOwners: true},
	}
	path, err := muxregistry.DescriptorPath(filepath.Dir(controlPath), descriptor)
	if err != nil {
		panic(err)
	}
	return []muxregistry.Record{{Path: path, Descriptor: descriptor}}
}

func stubLegacyLiveProof(t *testing.T, imagePath string, info *buildinfo.BuildInfo, buildInfoErr error, records []muxregistry.Record, recordsErr error) {
	t.Helper()
	oldImage, oldBuildInfo, oldRecords, oldImageBinding := readLiveProcessImage, readProcessBuildInfo, listMuxcoreRegistryDescriptors, verifyLiveProcessImageBindingForLegacyProof
	verifyLiveProcessImageBindingForLegacyProof = func(int, *processImageIdentity) error { return nil }
	var opened *os.File
	readLiveProcessImage = func(int) (*processImageIdentity, error) {
		file, err := os.Open(imagePath)
		if err != nil {
			return nil, err
		}
		opened = file
		return &processImageIdentity{File: file}, nil
	}
	readProcessBuildInfo = func(reader io.ReaderAt) (*buildinfo.BuildInfo, error) {
		if reader != opened {
			t.Fatal("build info reader did not receive the held live image file")
		}
		return info, buildInfoErr
	}
	listMuxcoreRegistryDescriptors = func(string) ([]muxregistry.Record, error) { return records, recordsErr }
	t.Cleanup(func() {
		readLiveProcessImage, readProcessBuildInfo, listMuxcoreRegistryDescriptors, verifyLiveProcessImageBindingForLegacyProof = oldImage, oldBuildInfo, oldRecords, oldImageBinding
	})
}

func TestMuxcoreDaemonConvergenceActionPreservesSchemaTwoCorrelation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.json")
	client := daemonConvergenceIdentity{ProductVersion: "v6.47.0", DaemonCompatEpoch: 1}
	exe := filepath.ToSlash(filepath.Join(t.TempDir(), "engram.exe"))
	v2 := `{"schema_version":2,"product_version":"v6.47.0","daemon_compat_epoch":1,"pid":42,"daemon_generation":"daemon-1","exe":"` + exe + `"}`
	for _, test := range []struct {
		name    string
		status  muxcoreDaemonStatusIdentity
		want    daemonConvergenceAction
		wantErr bool
	}{
		{"PID and generation correlate", muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-1"}, daemonConvergenceJoin, false},
		{"PID mismatch fails closed", muxcoreDaemonStatusIdentity{PID: 43, DaemonGeneration: "daemon-1"}, daemonConvergenceFail, true},
		{"generation mismatch fails closed", muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-2"}, daemonConvergenceFail, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := muxcoreDaemonConvergenceAction(path, test.status, client)
			if got != test.want || (err != nil) != test.wantErr {
				t.Fatalf("action, error = %v, %v; want %v, error=%t", got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestLegacyMuxcoreBuildInfoMatchesExactOfficial(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*buildinfo.BuildInfo)
		want   bool
	}{
		{"exact official v6.46.4 identity", func(*buildinfo.BuildInfo) {}, true},
		{"wrong command fails", func(info *buildinfo.BuildInfo) { info.Path = "example.com/fork/cmd/engram" }, false},
		{"wrong main module fails", func(info *buildinfo.BuildInfo) { info.Main.Path = "example.com/fork" }, false},
		{"wrong revision fails", func(info *buildinfo.BuildInfo) { info.Settings[0].Value = "deadbeef" }, false},
		{"modified build fails", func(info *buildinfo.BuildInfo) { info.Settings[1].Value = "true" }, false},
		{"duplicate VCS setting fails", func(info *buildinfo.BuildInfo) {
			info.Settings = append(info.Settings, debug.BuildSetting{Key: "vcs.revision", Value: legacyEngramRevision})
		}, false},
		{"fork replacement fails", func(info *buildinfo.BuildInfo) {
			info.Deps[0].Replace = &debug.Module{Path: "example.com/fork", Version: legacyMuxcoreEmbeddedVersion}
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			info := testLegacyMuxcoreBuildInfo()
			test.mutate(info)
			if got := legacyMuxcoreBuildInfoMatches(info); got != test.want {
				t.Fatalf("legacy build identity match = %t, want %t", got, test.want)
			}
		})
	}
}

func TestLegacyMuxcoreDaemonConvergenceUsesHeldLiveImage(t *testing.T) {
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "daemon.version")
	executablePath := filepath.Join(dir, "engram.exe")
	if err := os.WriteFile(executablePath, []byte("daemon"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, []byte(`{"version":"v6.46.4","pid":42,"exe":"`+filepath.ToSlash(executablePath)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	status := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "legacy"}
	stubLegacyLiveProof(t, executablePath, testLegacyMuxcoreBuildInfo(), nil,
		testLegacyMuxcoreRegistry(serverid.DaemonControlPath("", muxcoreNamespace), status.PID), nil)
	got, err := muxcoreLegacyDaemonConvergenceAction(markerPath, status, daemonConvergenceIdentity{ProductVersion: daemonVersion, DaemonCompatEpoch: 1})
	if err != nil || got != daemonConvergenceReplace {
		t.Fatalf("legacy action, error = %v, %v; want replace, nil", got, err)
	}
}

func TestSameDarwinProcessImageBinding(t *testing.T) {
	uuid := [16]byte{1}
	if !sameDarwinProcessImageBinding(uuid, 7, uuid, 7) {
		t.Fatal("equal nonzero Darwin image bindings did not match")
	}
	if sameDarwinProcessImageBinding(uuid, 7, uuid, 8) ||
		sameDarwinProcessImageBinding(uuid, 7, [16]byte{2}, 7) ||
		sameDarwinProcessImageBinding([16]byte{}, 7, uuid, 7) {
		t.Fatal("mismatched or missing Darwin image bindings matched")
	}
}

func TestReadLiveMuxcoreDaemonActionFallsBackToProvenLegacyMarkerAfterStaleSchemaTwoMarker(t *testing.T) {
	dir := t.TempDir()
	v2Path := filepath.Join(dir, "daemon.marker.json")
	legacyPath := filepath.Join(dir, "daemon.version")
	executablePath := filepath.Join(dir, "engram.exe")
	if err := os.WriteFile(executablePath, []byte("daemon"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v2Path, []byte(`{"schema_version":2,"product_version":"v6.47.3","daemon_compat_epoch":1,"pid":47652,"daemon_generation":"stale","exe":"`+filepath.ToSlash(executablePath)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"version":"v6.46.4","pid":88060,"exe":"`+filepath.ToSlash(executablePath)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	status := muxcoreDaemonStatusIdentity{PID: 88060, DaemonGeneration: "legacy"}
	stubLegacyLiveProof(t, executablePath, testLegacyMuxcoreBuildInfo(), nil,
		testLegacyMuxcoreRegistry(serverid.DaemonControlPath("", muxcoreNamespace), status.PID), nil)
	action, err := readLiveMuxcoreDaemonActionAt(v2Path, legacyPath, status, daemonConvergenceIdentity{ProductVersion: daemonVersion, DaemonCompatEpoch: 1})
	if err != nil || action != daemonConvergenceReplace {
		t.Fatalf("stale schema-2 marker blocked proven legacy recovery: action, error = %v, %v", action, err)
	}
}

func TestReadLiveMuxcoreDaemonActionRejectsSamePIDGenerationABAAfterLegacyRebind(t *testing.T) {
	dir := t.TempDir()
	v2Path := filepath.Join(dir, "daemon.marker.json")
	legacyPath := filepath.Join(dir, "daemon.version")
	executablePath := filepath.Join(dir, "engram.exe")
	if err := os.WriteFile(v2Path, []byte(`{"schema_version":2,"product_version":"`+daemonVersion+`","daemon_compat_epoch":1,"pid":88060,"daemon_generation":"generation-old","exe":"`+filepath.ToSlash(executablePath)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"version":"v6.46.4","pid":88060,"exe":"`+filepath.ToSlash(executablePath)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	status := muxcoreDaemonStatusIdentity{PID: 88060, DaemonGeneration: "generation-reused"}
	oldReadLiveProcessImage := readLiveProcessImage
	liveProcessImageCalls := 0
	readLiveProcessImage = func(int) (*processImageIdentity, error) {
		liveProcessImageCalls++
		return nil, errors.New("unexpected live image probe")
	}
	t.Cleanup(func() { readLiveProcessImage = oldReadLiveProcessImage })
	action, err := readLiveMuxcoreDaemonActionAt(v2Path, legacyPath, status, daemonConvergenceIdentity{ProductVersion: daemonVersion, DaemonCompatEpoch: 1})
	if !errors.Is(err, errMuxcoreDaemonMarkerUncorrelated) || action != daemonConvergenceFail {
		t.Fatalf("same-PID ABA action, error = %v, %v; want fail, uncorrelated", action, err)
	}
	if liveProcessImageCalls != 0 {
		t.Fatalf("same-PID ABA read live process image %d times; want 0", liveProcessImageCalls)
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

func TestReadLiveMuxcoreDaemonActionRejectsUncorrelatedHigherEpoch(t *testing.T) {
	dir := t.TempDir()
	v2Path := filepath.Join(dir, "daemon.marker.json")
	legacyPath := filepath.Join(dir, "daemon.version")
	exe := filepath.ToSlash(filepath.Join(dir, "engram.exe"))
	if err := os.WriteFile(v2Path, []byte(`{"schema_version":2,"product_version":"v6.47.3","daemon_compat_epoch":2,"pid":47652,"daemon_generation":"stale","exe":"`+exe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"version":"v6.46.4","pid":88060,"exe":"`+exe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	action, err := readLiveMuxcoreDaemonActionAt(v2Path, legacyPath, muxcoreDaemonStatusIdentity{PID: 88060, DaemonGeneration: "legacy"}, daemonConvergenceIdentity{ProductVersion: daemonVersion, DaemonCompatEpoch: 1})
	if err == nil || action != daemonConvergenceFail {
		t.Fatalf("higher schema-2 epoch action, error = %v, %v; want fail, error", action, err)
	}
}

func TestReadLiveMuxcoreDaemonActionRejectsIncompleteFreshStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.json")
	action, err := readLiveMuxcoreDaemonActionAt(path, path+".legacy", muxcoreDaemonStatusIdentity{PID: 42}, daemonConvergenceIdentity{ProductVersion: daemonVersion, DaemonCompatEpoch: 1})
	if !errors.Is(err, errMuxcoreDaemonMarkerUncorrelated) || action != daemonConvergenceFail {
		t.Fatalf("incomplete fresh status action, error = %v, %v; want fail, uncorrelated", action, err)
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
	waitForCurrentMuxcoreDaemonReady = func(context.Context) error { return nil }
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
			restartMuxcoreDaemon = func(context.Context, muxcoreDaemonStatusIdentity, string) error {
				called = true
				return nil
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
	released := false
	restartMuxcoreDaemon = func(_ context.Context, expected muxcoreDaemonStatusIdentity, successorExe string) error {
		if expected.PID != 42 || expected.DaemonGeneration != "daemon-old" || successorExe == "" {
			t.Fatalf("restart fence = %#v successor=%q", expected, successorExe)
		}
		restarted = true
		return nil
	}
	acquireRestartLock = func() (io.Closer, error) {
		return closerFunc(func() error { released = true; return nil }), nil
	}
	waitForCurrentMuxcoreDaemonReady = func(context.Context) error {
		if !released {
			t.Fatal("successor marker wait began before the outer restart lock was released")
		}
		converged = true
		return nil
	}
	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !restarted || !converged {
		t.Fatal("lower daemon replacement did not require replacement marker convergence")
	}
}

func TestWriteMuxcoreDaemonVersionMarkerPublishesBidirectionalMarkers(t *testing.T) {
	oldStatus, oldExe, oldLock := readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock = oldStatus, oldExe, oldLock
	})
	path := filepath.Join(t.TempDir(), "marker.marker.json")
	legacyPath := filepath.Join(filepath.Dir(path), "marker.version")
	currentExecutable = func() (string, error) { return filepath.Join(t.TempDir(), "engram.exe"), nil }
	acquireRestartLock = func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }
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

func TestWriteMuxcoreDaemonVersionMarkerWaitsForPredecessorLock(t *testing.T) {
	oldStatus, oldExe, oldLock, oldContended := readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock, isRestartLockContended
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock, isRestartLockContended = oldStatus, oldExe, oldLock, oldContended
	})
	dir := t.TempDir()
	ambientLockPath := serverid.DaemonLockPath("", muxcoreNamespace)
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, dir)
	}
	t.Setenv(muxcoreLockTestRootEnv, dir)
	if got := filepath.Clean(os.TempDir()); got != filepath.Clean(dir) {
		t.Fatalf("isolated muxcore lock root = %q, want %q", got, dir)
	}
	lockPath := serverid.DaemonLockPath("", muxcoreNamespace)
	if filepath.Clean(filepath.Dir(lockPath)) != filepath.Clean(dir) || filepath.Clean(lockPath) == filepath.Clean(ambientLockPath) {
		t.Fatalf("isolated muxcore lock path = %q, ambient path = %q", lockPath, ambientLockPath)
	}
	readyCtx, cancelReady := context.WithTimeout(context.Background(), time.Second)
	cmd := exec.Command(os.Args[0], "-test.run=^TestMuxcoreDaemonLockHolderProcess$", "-test.v=false")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(os.Environ(), "ENGRAM_TEST_MUXCORE_LOCK_HOLDER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	released := false
	var releaseErr error
	release := func() error {
		if released {
			return releaseErr
		}
		released = true
		releaseErr = errors.Join(stdin.Close(), cmd.Wait())
		return releaseErr
	}
	t.Cleanup(func() {
		wasReleased := released
		if err := release(); err != nil && !wasReleased {
			t.Error(err)
		}
	})
	type readiness struct {
		line string
		err  error
	}
	readyCh := make(chan readiness, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		readyCh <- readiness{line: line, err: err}
	}()
	select {
	case ready := <-readyCh:
		if ready.err != nil || ready.line != "ready\n" {
			if releaseErr := release(); releaseErr != nil {
				t.Fatalf("lock holder readiness = %q, %v; release = %v", ready.line, ready.err, releaseErr)
			}
			t.Fatalf("lock holder readiness = %q, %v", ready.line, ready.err)
		}
	case <-readyCtx.Done():
		killErr := cmd.Process.Kill()
		releaseErr := release()
		ready := <-readyCh
		t.Fatalf("lock holder readiness timed out: %v; kill = %v; release = %v; read = %q, %v", readyCtx.Err(), killErr, releaseErr, ready.line, ready.err)
	}
	cancelReady()

	markerPath := filepath.Join(dir, "daemon.marker.json")
	successorExe := filepath.Join(dir, "engram.exe")
	live := muxcoreDaemonStatusIdentity{PID: os.Getpid(), DaemonGeneration: "successor"}
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return live, true }
	currentExecutable = func() (string, error) { return successorExe, nil }
	contended := make(chan struct{})
	originalAcquire := acquireRestartLock
	acquireRestartLock = func() (io.Closer, error) {
		lock, err := originalAcquire()
		if err != nil && oldContended(err) {
			select {
			case <-contended:
			default:
				close(contended)
			}
		}
		return lock, err
	}
	published := make(chan error, 1)
	go func() { published <- writeMuxcoreDaemonVersionMarkerAt(markerPath) }()
	select {
	case <-contended:
	case err := <-published:
		t.Fatalf("successor marker publication terminated before predecessor contention was observed: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := <-published; err != nil {
		t.Fatalf("successor marker publication did not wait for predecessor lock: %v", err)
	}

	marker, err := readMuxcoreDaemonVersionMarker(markerPath)
	if err != nil || marker.PID != live.PID || marker.DaemonGeneration != live.DaemonGeneration || marker.ProductVersion != daemonVersion {
		t.Fatalf("schema-2 successor marker = %#v, error = %v", marker, err)
	}
	rawLegacy, err := os.ReadFile(muxcoreLegacyDaemonVersionPathForMarker(markerPath))
	if err != nil {
		t.Fatal(err)
	}
	var legacy legacyMuxcoreDaemonVersionMarker
	if err := json.Unmarshal(rawLegacy, &legacy); err != nil || legacy.Version != daemonVersion || legacy.PID != live.PID || filepath.Clean(legacy.Exe) != filepath.Clean(successorExe) {
		t.Fatalf("legacy successor marker = %#v, error = %v", legacy, err)
	}
}

func TestMuxcoreDaemonLockHolderProcess(t *testing.T) {
	if os.Getenv("ENGRAM_TEST_MUXCORE_LOCK_HOLDER") != "1" {
		return
	}
	root := os.Getenv(muxcoreLockTestRootEnv)
	if root == "" {
		t.Fatal("missing isolated muxcore lock root")
	}
	if got := filepath.Clean(os.TempDir()); got != filepath.Clean(root) {
		t.Fatalf("isolated muxcore lock root = %q, want %q", got, root)
	}
	if got := filepath.Clean(filepath.Dir(serverid.DaemonLockPath("", muxcoreNamespace))); got != filepath.Clean(root) {
		t.Fatalf("isolated muxcore lock directory = %q, want %q", got, root)
	}
	lock, err := acquireMuxcoreDaemonLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRestartLockWithContextDoesNotRetryNonContention(t *testing.T) {
	oldLock, oldContended := acquireRestartLock, isRestartLockContended
	t.Cleanup(func() { acquireRestartLock, isRestartLockContended = oldLock, oldContended })
	want := errors.New("restart lock unavailable")
	attempts := 0
	acquireRestartLock = func() (io.Closer, error) {
		attempts++
		return nil, want
	}
	isRestartLockContended = func(error) bool { return false }
	_, err := acquireRestartLockWithContext(context.Background())
	if !errors.Is(err, want) || attempts != 1 {
		t.Fatalf("lock error, attempts = %v, %d; want non-contention error after one attempt", err, attempts)
	}
}

func TestAcquireRestartLockWithContextHonorsCancellationAfterContention(t *testing.T) {
	oldLock, oldContended := acquireRestartLock, isRestartLockContended
	t.Cleanup(func() { acquireRestartLock, isRestartLockContended = oldLock, oldContended })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	acquireRestartLock = func() (io.Closer, error) {
		attempts++
		cancel()
		return nil, errors.New("restart lock held")
	}
	isRestartLockContended = func(error) bool { return true }
	_, err := acquireRestartLockWithContext(ctx)
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("lock error, attempts = %v, %d; want cancellation after one contended attempt", err, attempts)
	}
}

func TestWriteMuxcoreDaemonVersionMarkerSurfacesLockCloseError(t *testing.T) {
	oldStatus, oldExe, oldLock := readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock = oldStatus, oldExe, oldLock
	})
	dir := t.TempDir()
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		return muxcoreDaemonStatusIdentity{PID: os.Getpid(), DaemonGeneration: "successor"}, true
	}
	currentExecutable = func() (string, error) { return filepath.Join(dir, "engram.exe"), nil }
	acquireRestartLock = func() (io.Closer, error) { return failingCloser{}, nil }
	if err := writeMuxcoreDaemonVersionMarkerAtWithContext(context.Background(), filepath.Join(dir, "daemon.marker.json")); err == nil || !strings.Contains(err.Error(), "unlock failed") {
		t.Fatalf("marker publication error = %v, want lock close error", err)
	}
}

func TestWriteMuxcoreDaemonVersionMarkerSupersededBeforePublicationWritesNeitherMarker(t *testing.T) {
	oldStatus, oldExe, oldLock, oldWrite := readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock, writeMuxcoreMarker
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock, writeMuxcoreMarker = oldStatus, oldExe, oldLock, oldWrite
	})
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "daemon.marker.json")
	legacyPath := filepath.Join(dir, "daemon.version")
	currentExe := filepath.ToSlash(filepath.Join(dir, daemonVersion, "engram.exe"))
	legacyExe := filepath.ToSlash(filepath.Join(dir, "v6.46.4", "engram.exe"))
	const successorPID = 47475
	if err := os.WriteFile(markerPath, []byte(`{"schema_version":2,"product_version":"`+daemonVersion+`","daemon_compat_epoch":1,"pid":47475,"daemon_generation":"successor","exe":"`+currentExe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"version":"v6.46.4","pid":47475,"exe":"`+legacyExe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	acquires, statusReads, writes := 0, 0, 0
	live := muxcoreDaemonStatusIdentity{PID: os.Getpid(), DaemonGeneration: "superseded"}
	acquireRestartLock = func() (io.Closer, error) {
		acquires++
		// A replacement completed while this stale publisher waited for the
		// common restart/publication lock.
		live = muxcoreDaemonStatusIdentity{PID: successorPID, DaemonGeneration: "successor"}
		return io.NopCloser(strings.NewReader("")), nil
	}
	currentExecutable = func() (string, error) { return currentExe, nil }
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		statusReads++
		return live, true
	}
	writeMuxcoreMarker = func(string, []byte) error {
		writes++
		return errors.New("superseded publisher wrote a marker")
	}
	if err := writeMuxcoreDaemonVersionMarkerAt(markerPath); err == nil {
		t.Fatal("superseded daemon published markers")
	}
	if acquires != 1 || statusReads != 1 || writes != 0 {
		t.Fatalf("lock acquisitions=%d status reads=%d writes=%d; want 1, 1, 0", acquires, statusReads, writes)
	}
	marker, err := readMuxcoreDaemonVersionMarker(markerPath)
	if err != nil || marker.PID != successorPID || marker.DaemonGeneration != "successor" || marker.ProductVersion != daemonVersion {
		t.Fatalf("schema-2 marker after superseded publish = %#v, error = %v", marker, err)
	}
	legacy, err := readLegacyMuxcoreDaemonVersionMarker(legacyPath)
	if err != nil || legacy.PID != successorPID || legacy.Exe != legacyExe {
		t.Fatalf("legacy marker after superseded publish = %#v, error = %v", legacy, err)
	}
	status := muxcoreDaemonStatusIdentity{PID: successorPID, DaemonGeneration: "successor"}
	if action, err := readLiveMuxcoreDaemonActionAt(markerPath, legacyPath, status, daemonConvergenceIdentity{ProductVersion: daemonVersion, DaemonCompatEpoch: 1}); err != nil || action != daemonConvergenceJoin {
		t.Fatalf("new client action after superseded publish = %v, %v; want join, nil", action, err)
	}
}

func TestWriteMuxcoreDaemonVersionMarkerRetainsLegacyClientJoinIdentity(t *testing.T) {
	oldStatus, oldExe, oldLock := readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock = oldStatus, oldExe, oldLock
	})
	acquireRestartLock = func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "daemon.marker.json")
	legacyPath := filepath.Join(dir, "daemon.version")
	legacyExe := filepath.ToSlash(filepath.Join(dir, "v6.46.4", "engram.exe"))
	currentExe := filepath.ToSlash(filepath.Join(dir, "v6.47.3", "engram.exe"))
	if err := os.WriteFile(legacyPath, []byte(`{"version":"v6.46.4","pid":11,"exe":"`+legacyExe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	status := muxcoreDaemonStatusIdentity{PID: os.Getpid(), DaemonGeneration: "new"}
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return status, true }
	currentExecutable = func() (string, error) { return currentExe, nil }
	if err := writeMuxcoreDaemonVersionMarkerAt(markerPath); err != nil {
		t.Fatal(err)
	}
	legacy, err := readLegacyMuxcoreDaemonVersionMarker(legacyPath)
	if err != nil || legacy.Version != legacyDaemonVersion || legacy.PID != status.PID || legacy.Exe != legacyExe {
		t.Fatalf("v6.46.4 client would restart instead of join: marker=%#v error=%v", legacy, err)
	}
	marker, err := readMuxcoreDaemonVersionMarker(markerPath)
	if err != nil || marker.ProductVersion != daemonVersion || marker.PID != status.PID || marker.DaemonGeneration != status.DaemonGeneration || filepath.Clean(marker.Exe) != filepath.Clean(currentExe) {
		t.Fatalf("schema-2 marker did not publish current live identity: marker=%#v error=%v", marker, err)
	}
}

func TestWriteMuxcoreDaemonVersionMarkerCompensatesSchemaTwoFailure(t *testing.T) {
	oldStatus, oldExe, oldLock, oldWrite := readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock, writeMuxcoreMarker
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock, writeMuxcoreMarker = oldStatus, oldExe, oldLock, oldWrite
	})
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "daemon.marker.json")
	legacyPath := filepath.Join(dir, "daemon.version")
	exe := filepath.ToSlash(filepath.Join(dir, "engram.exe"))
	priorLegacy := []byte("{\"version\":\"v6.46.4\",\"pid\":11,\"exe\":\"" + exe + "\"}\n")
	if err := os.WriteFile(markerPath, []byte(`{"schema_version":2,"product_version":"v6.46.4","daemon_compat_epoch":1,"pid":11,"daemon_generation":"old","exe":"`+exe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, priorLegacy, 0o600); err != nil {
		t.Fatal(err)
	}
	currentExecutable = func() (string, error) { return exe, nil }
	acquireRestartLock = func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }
	status := muxcoreDaemonStatusIdentity{PID: os.Getpid(), DaemonGeneration: "new"}
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return status, true }
	writes := 0
	writeMuxcoreMarker = func(path string, payload []byte) error {
		writes++
		if writes == 2 && path == markerPath {
			return errors.New("schema-2 marker publish failed")
		}
		return oldWrite(path, payload)
	}
	if err := writeMuxcoreDaemonVersionMarkerAt(markerPath); err == nil || !strings.Contains(err.Error(), "schema-2 marker publish failed") {
		t.Fatalf("publication error = %v, want schema-2 failure", err)
	}
	gotLegacy, err := os.ReadFile(legacyPath)
	if err != nil || string(gotLegacy) != string(priorLegacy) {
		t.Fatalf("legacy rollback = %q, error = %v; want exact prior bytes %q", gotLegacy, err, priorLegacy)
	}
	marker, err := readMuxcoreDaemonVersionMarker(markerPath)
	if err != nil || marker.PID != 11 || marker.DaemonGeneration != "old" {
		t.Fatalf("schema-2 after failed compensation = %#v, error = %v", marker, err)
	}
	action, err := readLiveMuxcoreDaemonActionAt(markerPath, legacyPath, status, daemonConvergenceIdentity{ProductVersion: daemonVersion, DaemonCompatEpoch: 1})
	if err == nil || action != daemonConvergenceFail {
		t.Fatalf("new client partial tuple action, error = %v, %v; want fail, error", action, err)
	}
}

func TestWriteMuxcoreDaemonVersionMarkerRemovesNewLegacyAfterSchemaTwoFailure(t *testing.T) {
	oldStatus, oldExe, oldLock, oldWrite := readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock, writeMuxcoreMarker
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, currentExecutable, acquireRestartLock, writeMuxcoreMarker = oldStatus, oldExe, oldLock, oldWrite
	})
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "daemon.marker.json")
	legacyPath := filepath.Join(dir, "daemon.version")
	exe := filepath.ToSlash(filepath.Join(dir, "engram.exe"))
	if err := os.WriteFile(markerPath, []byte(`{"schema_version":2,"product_version":"v6.46.4","daemon_compat_epoch":1,"pid":11,"daemon_generation":"old","exe":"`+exe+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	currentExecutable = func() (string, error) { return exe, nil }
	acquireRestartLock = func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }
	status := muxcoreDaemonStatusIdentity{PID: os.Getpid(), DaemonGeneration: "new"}
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return status, true }
	writes := 0
	writeMuxcoreMarker = func(path string, payload []byte) error {
		writes++
		if writes == 2 && path == markerPath {
			return errors.New("schema-2 marker publish failed")
		}
		return oldWrite(path, payload)
	}
	if err := writeMuxcoreDaemonVersionMarkerAt(markerPath); err == nil || !strings.Contains(err.Error(), "schema-2 marker publish failed") {
		t.Fatalf("publication error = %v, want schema-2 failure", err)
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new legacy marker remained after schema-2 failure: %v", err)
	}
	marker, err := readMuxcoreDaemonVersionMarker(markerPath)
	if err != nil || marker.PID != 11 || marker.DaemonGeneration != "old" {
		t.Fatalf("schema-2 after failed compensation = %#v, error = %v", marker, err)
	}
	action, err := readLiveMuxcoreDaemonActionAt(markerPath, legacyPath, status, daemonConvergenceIdentity{ProductVersion: daemonVersion, DaemonCompatEpoch: 1})
	if err == nil || action != daemonConvergenceFail {
		t.Fatalf("new client absent-legacy tuple action, error = %v, %v; want fail, error", action, err)
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

func TestWaitForMuxcoreDaemonMarkerUsesOuterDeadlineAfterLongPredecessorShutdown(t *testing.T) {
	oldWait := waitForCurrentMuxcoreDaemonReady
	t.Cleanup(func() { waitForCurrentMuxcoreDaemonReady = oldWait })

	outerCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	predecessorShutdown := 2*time.Second + time.Millisecond
	successorPublication := time.Now().Add(predecessorShutdown)
	waitForCurrentMuxcoreDaemonReady = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.After(successorPublication) {
			return context.DeadlineExceeded
		}
		return nil
	}

	if err := waitForMuxcoreDaemonMarker(outerCtx); err != nil {
		t.Fatalf("successor publication after a two-second predecessor shutdown failed before the outer deadline: %v", err)
	}
}

func TestWaitForMuxcoreDaemonMarkerFailsWhenOuterDeadlineExpires(t *testing.T) {
	oldWait := waitForCurrentMuxcoreDaemonReady
	t.Cleanup(func() { waitForCurrentMuxcoreDaemonReady = oldWait })

	waitForCurrentMuxcoreDaemonReady = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	outerCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := waitForMuxcoreDaemonMarker(outerCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired outer deadline error = %v, want deadline exceeded", err)
	}
}

func TestReconcileMuxcoreDaemonVersionWaitsForUncorrelatedMarker(t *testing.T) {
	stubReconciliation(t, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-starting"}, daemonConvergenceFail, errMuxcoreDaemonMarkerUncorrelated)
	called := false
	restartMuxcoreDaemon = func(context.Context, muxcoreDaemonStatusIdentity, string) error {
		called = true
		return nil
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
	restartMuxcoreDaemon = func(context.Context, muxcoreDaemonStatusIdentity, string) error {
		restarted = true
		return nil
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
	oldStatus, oldAction, oldLock, oldContended, oldExe, oldRestart, oldWait := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, acquireRestartLock, isRestartLockContended, currentExecutable, restartMuxcoreDaemon, waitForCurrentMuxcoreDaemonReady
	defer func() {
		readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, acquireRestartLock, isRestartLockContended, currentExecutable, restartMuxcoreDaemon, waitForCurrentMuxcoreDaemonReady = oldStatus, oldAction, oldLock, oldContended, oldExe, oldRestart, oldWait
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
	restartMuxcoreDaemon = func(context.Context, muxcoreDaemonStatusIdentity, string) error {
		restarted = true
		return nil
	}
	waitForCurrentMuxcoreDaemonReady = func(context.Context) error { return nil }
	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	if acquisitions != 3 || !restarted {
		t.Fatalf("acquisitions=%d restarted=%t; freed lock was not freshly fenced", acquisitions, restarted)
	}
}

type failingCloser struct{}

type closerFunc func() error

func (closeFn closerFunc) Close() error { return closeFn() }
func (failingCloser) Close() error      { return errors.New("unlock failed") }

func TestReconcileMuxcoreDaemonVersionDoesNotFailSuccessfulRestartOnUnlockError(t *testing.T) {
	stubReconciliation(t, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "daemon-old"}, daemonConvergenceReplace, nil)
	currentExecutable = func() (string, error) { return filepath.Join(t.TempDir(), "engram.exe"), nil }
	acquireRestartLock = func() (io.Closer, error) { return failingCloser{}, nil }
	restartMuxcoreDaemon = func(context.Context, muxcoreDaemonStatusIdentity, string) error {
		return nil
	}
	if err := reconcileMuxcoreDaemonVersion(context.Background()); err != nil {
		t.Fatalf("successful restart was reported failed after unlock error: %v", err)
	}
}

func TestRestartMuxcoreDaemonBoundFailsClosedBeforeWritingOnPeerMismatch(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	oldDial, oldPeer, oldStatus, oldSend := dialMuxcoreControl, readMuxcoreControlPeerPID, readMuxcoreDaemonStatusIdentity, sendMuxcoreControlRequest
	t.Cleanup(func() {
		dialMuxcoreControl, readMuxcoreControlPeerPID, readMuxcoreDaemonStatusIdentity, sendMuxcoreControlRequest = oldDial, oldPeer, oldStatus, oldSend
	})
	dialMuxcoreControl = func(string, time.Duration) (net.Conn, error) { return client, nil }
	readMuxcoreControlPeerPID = func(net.Conn) (int, error) { return 43, nil }
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		t.Fatal("peer mismatch must not re-read status or send a request")
		return muxcoreDaemonStatusIdentity{}, false
	}
	sendMuxcoreControlRequest = func(context.Context, net.Conn, muxcontrol.Request) (*muxcontrol.Response, error) {
		t.Fatal("peer mismatch sent a request")
		return nil, nil
	}
	if err := restartMuxcoreDaemonBound(context.Background(), muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "old"}, "C:/engram.exe"); err == nil {
		t.Fatal("peer mismatch was accepted")
	}
}

func TestRestartMuxcoreDaemonBoundFailsClosedOnGenerationChangeBeforeWriting(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	oldDial, oldPeer, oldStatus, oldSend := dialMuxcoreControl, readMuxcoreControlPeerPID, readMuxcoreDaemonStatusIdentity, sendMuxcoreControlRequest
	t.Cleanup(func() {
		dialMuxcoreControl, readMuxcoreControlPeerPID, readMuxcoreDaemonStatusIdentity, sendMuxcoreControlRequest = oldDial, oldPeer, oldStatus, oldSend
	})
	dialMuxcoreControl = func(string, time.Duration) (net.Conn, error) { return client, nil }
	readMuxcoreControlPeerPID = func(net.Conn) (int, error) { return 42, nil }
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		return muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "new"}, true
	}
	sendMuxcoreControlRequest = func(context.Context, net.Conn, muxcontrol.Request) (*muxcontrol.Response, error) {
		t.Fatal("generation change sent a request")
		return nil, nil
	}
	if err := restartMuxcoreDaemonBound(context.Background(), muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "old"}, "C:/engram.exe"); err == nil {
		t.Fatal("generation change was accepted")
	}
}

func TestRestartMuxcoreDaemonBoundSendsOneGracefulRestartWithSuccessor(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	oldDial, oldPeer, oldStatus := dialMuxcoreControl, readMuxcoreControlPeerPID, readMuxcoreDaemonStatusIdentity
	t.Cleanup(func() {
		dialMuxcoreControl, readMuxcoreControlPeerPID, readMuxcoreDaemonStatusIdentity = oldDial, oldPeer, oldStatus
	})
	expected := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "old"}
	dialCount := 0
	dialMuxcoreControl = func(string, time.Duration) (net.Conn, error) {
		dialCount++
		return client, nil
	}
	readMuxcoreControlPeerPID = func(net.Conn) (int, error) { return expected.PID, nil }
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return expected, true }
	request := make(chan muxcontrol.Request, 1)
	go func() {
		defer server.Close()
		var got muxcontrol.Request
		if err := json.NewDecoder(server).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		request <- got
		if err := json.NewEncoder(server).Encode(muxcontrol.Response{OK: true}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}()
	const successor = "C:/engram-v6.47.0.exe"
	if err := restartMuxcoreDaemonBound(context.Background(), expected, successor); err != nil {
		t.Fatal(err)
	}
	if dialCount != 1 {
		t.Fatalf("control dials=%d, want 1", dialCount)
	}
	got := <-request
	if got.Cmd != "graceful-restart" || got.SuccessorExe != successor {
		t.Fatalf("request = %#v, want graceful restart with successor", got)
	}
}

func TestRestartMuxcoreDaemonBoundDoesNotFallbackToShutdown(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	oldDial, oldPeer, oldStatus := dialMuxcoreControl, readMuxcoreControlPeerPID, readMuxcoreDaemonStatusIdentity
	t.Cleanup(func() {
		dialMuxcoreControl, readMuxcoreControlPeerPID, readMuxcoreDaemonStatusIdentity = oldDial, oldPeer, oldStatus
	})
	expected := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "old"}
	dialCount := 0
	dialMuxcoreControl = func(string, time.Duration) (net.Conn, error) {
		dialCount++
		return client, nil
	}
	readMuxcoreControlPeerPID = func(net.Conn) (int, error) { return expected.PID, nil }
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return expected, true }
	request := make(chan muxcontrol.Request, 1)
	go func() {
		defer server.Close()
		var got muxcontrol.Request
		if err := json.NewDecoder(server).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		request <- got
		_ = json.NewEncoder(server).Encode(muxcontrol.Response{OK: false, Message: "rejected"})
	}()
	if err := restartMuxcoreDaemonBound(context.Background(), expected, "C:/engram.exe"); err == nil {
		t.Fatal("rejected graceful restart was accepted")
	}
	if got := <-request; got.Cmd != "graceful-restart" {
		t.Fatalf("request = %#v, want only graceful restart", got)
	}
	if dialCount != 1 {
		t.Fatalf("control dials=%d, want no shutdown fallback reconnect", dialCount)
	}
}
