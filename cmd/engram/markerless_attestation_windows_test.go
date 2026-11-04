//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	"golang.org/x/sys/windows"
)

func TestMarkerlessAttestationPolicy(t *testing.T) {
	policy, err := parseMarkerlessPolicy(markerlessAttestationData)
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.WindowsAMD64) != 1 {
		t.Fatal("expected the bounded predecessor")
	}
	target := policy.WindowsAMD64[0]
	client := daemonConvergenceIdentity{ProductVersion: "v7.0.20", DaemonCompatEpoch: 1}
	if action, err := policy.classify(target.SHA256, target.Size, false, client); err != nil || action != daemonConvergenceReplace {
		t.Fatalf("attested predecessor action=%v error=%v", action, err)
	}
	if action, err := policy.classify(target.SHA256, target.Size+1, false, client); err == nil || action != daemonConvergenceFail {
		t.Fatalf("wrong size action=%v error=%v", action, err)
	}
	if action, err := policy.classify(strings.Repeat("a", 64), target.Size, false, client); err == nil || action != daemonConvergenceFail {
		t.Fatalf("wrong image action=%v error=%v", action, err)
	}
	if action, err := policy.classify(strings.Repeat("a", 64), target.Size, true, client); err != nil || action != daemonConvergenceJoin {
		t.Fatalf("current image action=%v error=%v", action, err)
	}
	policy.RevokedSHA256 = append(policy.RevokedSHA256, target.SHA256)
	revokedSource, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policy, err = parseMarkerlessPolicy(revokedSource)
	if err != nil {
		t.Fatal(err)
	}
	if action, err := policy.classify(target.SHA256, target.Size, false, client); err == nil || action != daemonConvergenceFail {
		t.Fatalf("revoked predecessor action=%v error=%v", action, err)
	}
	if action, err := policy.classify(target.SHA256, target.Size, true, client); err == nil || action != daemonConvergenceFail {
		t.Fatalf("revoked current image action=%v error=%v", action, err)
	}
	policy.RevokedSHA256 = nil
	if action, err := policy.classify(target.SHA256, target.Size, false, daemonConvergenceIdentity{ProductVersion: "v7.0.19", DaemonCompatEpoch: 1}); err == nil || action != daemonConvergenceFail {
		t.Fatalf("unrevoked non-older predecessor action=%v error=%v", action, err)
	}
}

func TestMarkerlessPolicyRejectsMalformedInput(t *testing.T) {
	var source map[string]any
	if err := json.Unmarshal(markerlessAttestationData, &source); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"schema_version":1,"schema_version":1,"revoked_sha256":[],"windows_amd64":[]}`,
		`{"schema_version":1,"revoked_sha256":["bad"],"windows_amd64":[]}`,
		`{"schema_version":1,"revoked_sha256":[],"windows_amd64":[],"unknown":true}`,
		`{"schema_version":1,"revoked_sha256":[],"windows_amd64":[]} {}`,
	} {
		if _, err := parseMarkerlessPolicy([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed policy: %s", raw)
		}
	}
}

func TestMarkerlessMappingFailsClosed(t *testing.T) {
	old := queryMarkerlessImageMapping
	t.Cleanup(func() { queryMarkerlessImageMapping = old })
	queryMarkerlessImageMapping = func(windows.Handle, windows.Handle) (uint32, error) { return 0, errors.New("API unavailable") }
	if err := imageMappingMatches(1, 1); err == nil {
		t.Fatal("missing API admitted")
	}
	queryMarkerlessImageMapping = func(windows.Handle, windows.Handle) (uint32, error) { return 0xc0000001, nil }
	if err := imageMappingMatches(1, 1); err == nil {
		t.Fatal("non-success NTSTATUS admitted")
	}
}

func TestMarkerlessStatusRequiresPeerPIDGenerationAndLiveState(t *testing.T) {
	oldDial, oldPeer, oldSend := dialMuxcoreControl, readMuxcoreControlPeerPID, sendMuxcoreControlRequest
	t.Cleanup(func() {
		dialMuxcoreControl, readMuxcoreControlPeerPID, sendMuxcoreControlRequest = oldDial, oldPeer, oldSend
	})
	dialMuxcoreControl = func(string, time.Duration) (net.Conn, error) { a, b := net.Pipe(); _ = b.Close(); return a, nil }
	expected := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "generation"}
	for _, tc := range []struct {
		name   string
		peer   int
		status muxcoreDaemonStatusIdentity
		ok     bool
	}{
		{"matching", 42, expected, true},
		{"wrong peer", 43, expected, false},
		{"PID reuse", 42, muxcoreDaemonStatusIdentity{PID: 43, DaemonGeneration: "generation"}, false},
		{"generation reuse", 42, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "different"}, false},
		{"empty generation", 42, muxcoreDaemonStatusIdentity{PID: 42}, false},
		{"shutdown", 42, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "generation", ShuttingDown: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readMuxcoreControlPeerPID = func(net.Conn) (int, error) { return tc.peer, nil }
			sendMuxcoreControlRequest = func(context.Context, net.Conn, muxcontrol.Request) (*muxcontrol.Response, error) {
				data, err := json.Marshal(tc.status)
				return &muxcontrol.Response{OK: true, Data: data}, err
			}
			err := markerlessBoundStatus(context.Background(), expected)
			if (err == nil) != tc.ok {
				t.Fatalf("status error=%v want success=%t", err, tc.ok)
			}
		})
	}
}

func TestMarkerlessRequiresBothPathsAbsent(t *testing.T) {
	dir := t.TempDir()
	marker, legacy := filepath.Join(dir, "daemon.marker.json"), filepath.Join(dir, "daemon.version")
	if err := markerlessMarkersAbsentAt(marker, legacy); err != nil {
		t.Fatalf("both absent: %v", err)
	}
	for _, path := range []string{marker, legacy} {
		if err := os.WriteFile(path, []byte("malformed or replaced"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := markerlessMarkersAbsentAt(marker, legacy); err == nil {
			t.Fatalf("present marker %s was treated as absent", path)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostRestartAdmissionFencesLegacyOnlyPublication(t *testing.T) {
	for _, tc := range []struct {
		name               string
		publish, malformed bool
	}{
		{"schema published", true, false}, {"legacy only", false, false}, {"malformed legacy", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldStatus, oldAction, oldRun, oldAdmit := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, admitMarkerlessCurrent
			oldLock, oldDial, oldPeer, oldSend := acquireRestartLock, dialMuxcoreControl, readMuxcoreControlPeerPID, sendMuxcoreControlRequest
			t.Cleanup(func() {
				readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, admitMarkerlessCurrent = oldStatus, oldAction, oldRun, oldAdmit
				acquireRestartLock, dialMuxcoreControl, readMuxcoreControlPeerPID, sendMuxcoreControlRequest = oldLock, oldDial, oldPeer, oldSend
			})
			before, after := muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "old"}, muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "new"}
			status := before
			dir := t.TempDir()
			markerPath, legacyPath, exe := filepath.Join(dir, "daemon.marker.json"), filepath.Join(dir, "daemon.version"), filepath.Join(dir, "engram.exe")
			legacy, err := json.Marshal(legacyMuxcoreDaemonVersionMarker{Version: daemonVersion, PID: after.PID, Exe: exe})
			if err != nil {
				t.Fatal(err)
			}
			if tc.malformed {
				legacy = []byte("malformed")
			}
			if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
				t.Fatal(err)
			}
			readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return status, true }
			readLiveMuxcoreDaemonAction = func(got muxcoreDaemonStatusIdentity, client daemonConvergenceIdentity) (daemonConvergenceAction, error) {
				if got == before {
					return daemonConvergenceFail, os.ErrNotExist
				}
				return readLiveMuxcoreDaemonActionAt(markerPath, legacyPath, got, client)
			}
			runMarkerlessReconciliation = func(context.Context, muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (bool, bool, error) {
				status = after
				return true, false, nil
			}
			admitMarkerlessCurrent = func(context.Context, muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) error {
				t.Fatal("legacy-only state was admitted as absent")
				return errors.New("unexpected")
			}
			fences, probes := 0, 0
			acquireRestartLock = func() (io.Closer, error) {
				fences++
				if tc.publish {
					marker := muxcoreDaemonVersionMarker{SchemaVersion: 2, ProductVersion: daemonVersion, DaemonCompatEpoch: muxcoreDaemonCompatEpoch, PID: after.PID, DaemonGeneration: after.DaemonGeneration, Exe: exe}
					raw, err := json.Marshal(marker)
					if err != nil {
						return nil, err
					}
					if err := os.WriteFile(markerPath, raw, 0o600); err != nil {
						return nil, err
					}
				}
				return io.NopCloser(strings.NewReader("")), nil
			}
			dialMuxcoreControl = func(string, time.Duration) (net.Conn, error) {
				probes++
				a, b := net.Pipe()
				_ = b.Close()
				return a, nil
			}
			readMuxcoreControlPeerPID = func(net.Conn) (int, error) { return after.PID, nil }
			sendMuxcoreControlRequest = func(context.Context, net.Conn, muxcontrol.Request) (*muxcontrol.Response, error) {
				raw, err := json.Marshal(after)
				return &muxcontrol.Response{OK: true, Data: raw}, err
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = reconcileMuxcoreDaemonVersion(ctx)
			if tc.publish {
				if err != nil || fences != 1 || probes != 1 {
					t.Fatalf("schema not freshly admitted: err=%v fences=%d probes=%d", err, fences, probes)
				}
			} else if !errors.Is(err, errMuxcoreDaemonMarkerUncorrelated) || fences != 1 || probes != 0 {
				t.Fatalf("invalid legacy state admitted: err=%v fences=%d probes=%d", err, fences, probes)
			}
		})
	}
}
