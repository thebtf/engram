//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func stubObservedProofUnavailable(t *testing.T) {
	t.Helper()
	old := bindObservedMuxcoreOwner
	t.Cleanup(func() { bindObservedMuxcoreOwner = old })
	bindObservedMuxcoreOwner = func(context.Context, muxcoreDaemonStatusIdentity) (muxcoreProcessExitProof, observedMarkerSnapshot, error) {
		return nil, observedMarkerSnapshot{}, errors.New("fixture has no bound exit proof")
	}
}

type observedClosingProof struct{ closed bool }

func (*observedClosingProof) Exited() (bool, error) { return false, nil }
func (proof *observedClosingProof) Close() error    { proof.closed = true; return nil }

func TestMarkerlessFinalAdmissionIsFreshAfterRestart(t *testing.T) {
	for _, tc := range []struct {
		name         string
		admissionErr error
	}{
		{"fresh admission", nil},
		{"rejected fresh image", errors.New("image no longer trusted")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubObservedProofUnavailable(t)
			oldStatus, oldAction := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction
			oldRun, oldAdmit := runMarkerlessReconciliation, admitMarkerlessCurrent
			oldDial, oldPeer, oldSend := dialMuxcoreControl, readMuxcoreControlPeerPID, sendMuxcoreControlRequest
			t.Cleanup(func() {
				readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction = oldStatus, oldAction
				runMarkerlessReconciliation, admitMarkerlessCurrent = oldRun, oldAdmit
				dialMuxcoreControl, readMuxcoreControlPeerPID, sendMuxcoreControlRequest = oldDial, oldPeer, oldSend
			})
			before := muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "old"}
			after := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "new"}
			proof := &observedClosingProof{}
			bindObservedMuxcoreOwner = func(_ context.Context, got muxcoreDaemonStatusIdentity) (muxcoreProcessExitProof, observedMarkerSnapshot, error) {
				if got != before {
					t.Fatalf("observed proof used wrong owner: %+v", got)
				}
				return proof, observedMarkerSnapshot{}, nil
			}
			status := before
			readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return status, true }
			readLiveMuxcoreDaemonAction = func(muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (daemonConvergenceAction, error) {
				return daemonConvergenceFail, os.ErrNotExist
			}
			runMarkerlessReconciliation = func(_ context.Context, got muxcoreDaemonStatusIdentity, _ daemonConvergenceIdentity) (bool, bool, error) {
				if got != before {
					t.Fatalf("restart used wrong owner: %+v", got)
				}
				status = after
				return true, false, nil
			}
			admissions := 0
			admitMarkerlessCurrent = func(_ context.Context, got muxcoreDaemonStatusIdentity, _ daemonConvergenceIdentity) error {
				admissions++
				if !proof.closed {
					t.Fatal("pre-ACK proof was retained into final admission")
				}
				if got != after {
					t.Fatalf("final admission used stale owner: %+v", got)
				}
				return tc.admissionErr
			}
			dialMuxcoreControl = func(string, time.Duration) (net.Conn, error) { a, b := net.Pipe(); _ = b.Close(); return a, nil }
			readMuxcoreControlPeerPID = func(net.Conn) (int, error) { return after.PID, nil }
			sendMuxcoreControlRequest = func(context.Context, net.Conn, muxcontrol.Request) (*muxcontrol.Response, error) {
				raw, err := json.Marshal(after)
				return &muxcontrol.Response{OK: true, Data: raw}, err
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := reconcileMuxcoreDaemonVersion(ctx)
			if admissions != 1 || (err == nil) != (tc.admissionErr == nil) {
				t.Fatalf("admissions=%d error=%v", admissions, err)
			}
			if tc.admissionErr != nil && !errors.Is(err, tc.admissionErr) {
				t.Fatalf("lost final admission error: %v", err)
			}
		})
	}
}

func TestMarkerlessWaiterRebindsAndNeverColdStartsFromGap(t *testing.T) {
	for _, permanentGap := range []bool{false, true} {
		t.Run(fmt.Sprintf("permanent-gap-%t", permanentGap), func(t *testing.T) {
			stubObservedProofUnavailable(t)
			oldStatus, oldAction, oldRun, oldWait := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, waitForMuxcoreConvergence
			t.Cleanup(func() {
				readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, waitForMuxcoreConvergence = oldStatus, oldAction, oldRun, oldWait
			})
			before := muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "old"}
			after := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "new"}
			reads, calls := 0, 0
			readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
				reads++
				if reads == 1 {
					return before, true
				}
				if reads == 2 || permanentGap {
					return muxcoreDaemonStatusIdentity{}, false
				}
				return after, true
			}
			readLiveMuxcoreDaemonAction = func(muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (daemonConvergenceAction, error) {
				return daemonConvergenceFail, os.ErrNotExist
			}
			runMarkerlessReconciliation = func(_ context.Context, got muxcoreDaemonStatusIdentity, _ daemonConvergenceIdentity) (bool, bool, error) {
				calls++
				if calls == 1 && got == before {
					return false, false, errMuxcoreDaemonOwnerChanged
				}
				if calls == 2 && got == after && !permanentGap {
					return false, false, nil
				}
				t.Fatalf("stale or unexpected owner: %+v", got)
				return false, false, errors.New("unreachable")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waits := 0
			waitForMuxcoreConvergence = func(ctx context.Context) error {
				waits++
				if permanentGap && waits == 2 {
					cancel()
				}
				return ctx.Err()
			}
			err := reconcileMuxcoreDaemonVersion(ctx)
			if permanentGap {
				if !errors.Is(err, context.Canceled) || calls != 1 {
					t.Fatalf("gap admitted cold start: calls=%d err=%v", calls, err)
				}
			} else if err != nil || calls != 2 {
				t.Fatalf("waiter did not rebind: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestMarkerlessFenceDetectsOwnerChangeBeforeImageProbe(t *testing.T) {
	oldStatus, oldLock := readMuxcoreDaemonStatusIdentity, acquireRestartLock
	t.Cleanup(func() { readMuxcoreDaemonStatusIdentity, acquireRestartLock = oldStatus, oldLock })
	acquireRestartLock = func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }
	for _, tc := range []struct {
		name   string
		status muxcoreDaemonStatusIdentity
		found  bool
	}{
		{"new PID", muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "new"}, true},
		{"new generation", muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "new"}, true},
		{"shutdown", muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "old", ShuttingDown: true}, true},
		{"gap", muxcoreDaemonStatusIdentity{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return tc.status, tc.found }
			_, _, err := reconcileMarkerlessDaemon(context.Background(), muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "old"}, daemonConvergenceIdentity{ProductVersion: daemonVersion, DaemonCompatEpoch: 1})
			if !errors.Is(err, errMuxcoreDaemonOwnerChanged) {
				t.Fatalf("stale owner did not re-enter convergence: %v", err)
			}
		})
	}
}

func TestRestartWaitReentersForMarkerlessSuccessor(t *testing.T) {
	oldStatus, oldAction := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction
	t.Cleanup(func() { readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction = oldStatus, oldAction })
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		return muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "new"}, true
	}
	readLiveMuxcoreDaemonAction = func(muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (daemonConvergenceAction, error) {
		return daemonConvergenceFail, os.ErrNotExist
	}
	err := waitForMuxcoreDaemonVersionOrExit(context.Background(), fixtureProcessExitProof{}, muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "old"})
	if !errors.Is(err, errMuxcoreDaemonStillLower) {
		t.Fatalf("markerless successor did not trigger fresh reconciliation: %v", err)
	}
}

func TestMarkerlessPeerMismatchIsNotTransitionWhenOwnerUnchanged(t *testing.T) {
	oldStatus, oldDial, oldPeer := readMuxcoreDaemonStatusIdentity, dialMuxcoreControl, readMuxcoreControlPeerPID
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, dialMuxcoreControl, readMuxcoreControlPeerPID = oldStatus, oldDial, oldPeer
	})
	expected := muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "old"}
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) { return expected, true }
	dialMuxcoreControl = func(string, time.Duration) (net.Conn, error) { a, b := net.Pipe(); _ = b.Close(); return a, nil }
	readMuxcoreControlPeerPID = func(net.Conn) (int, error) { return 42, nil }
	err := markerlessBoundStatusOrTransition(context.Background(), expected)
	if err == nil || errors.Is(err, errMuxcoreDaemonOwnerChanged) {
		t.Fatalf("wrong peer was treated as an ordinary owner transition: %v", err)
	}
}

func TestObservedShuttingDownSuccessorInvalidatesExitHandback(t *testing.T) {
	stubObservedProofUnavailable(t)
	oldStatus, oldAction, oldRun, oldWait := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, waitForMuxcoreConvergence
	oldDial, oldLock := dialMuxcoreControl, acquireRestartLock
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, waitForMuxcoreConvergence = oldStatus, oldAction, oldRun, oldWait
		dialMuxcoreControl, acquireRestartLock = oldDial, oldLock
	})
	reads := 0
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		reads++
		switch reads {
		case 1:
			return muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "old"}, true
		case 2:
			return muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "new", ShuttingDown: true}, true
		default:
			return muxcoreDaemonStatusIdentity{}, false
		}
	}
	readLiveMuxcoreDaemonAction = func(muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (daemonConvergenceAction, error) {
		return daemonConvergenceFail, os.ErrNotExist
	}
	runMarkerlessReconciliation = func(context.Context, muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (bool, bool, error) {
		return true, true, errMuxcoreDaemonLockAvailable
	}
	acquireRestartLock = func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }
	dialMuxcoreControl = func(string, time.Duration) (net.Conn, error) { return nil, os.ErrNotExist }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waits := 0
	waitForMuxcoreConvergence = func(ctx context.Context) error {
		waits++
		if waits == 2 {
			cancel()
		}
		return ctx.Err()
	}
	if err := reconcileMuxcoreDaemonVersion(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("stale cold-start proof admitted: %v", err)
	}
}

func TestObservedDeadOwnerRequiresFencedUnchangedMarkers(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		markerBacked, changed bool
	}{
		{"markerless", false, false},
		{"correlated marker-backed", true, false},
		{"changed markers fail closed", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldStatus, oldAction, oldRun, oldWait := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, waitForMuxcoreConvergence
			oldBind, oldCapture, oldLock, oldDial := bindObservedMuxcoreOwner, captureObservedMarkers, acquireRestartLock, dialMuxcoreControl
			t.Cleanup(func() {
				readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, waitForMuxcoreConvergence = oldStatus, oldAction, oldRun, oldWait
				bindObservedMuxcoreOwner, captureObservedMarkers, acquireRestartLock, dialMuxcoreControl = oldBind, oldCapture, oldLock, oldDial
			})
			owner := muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "old"}
			snapshot := observedMarkerSnapshot{}
			if tc.markerBacked {
				snapshot = observedMarkerSnapshot{schema: "bound-schema", legacy: "bound-legacy", schemaPresent: true, legacyPresent: true}
			}
			reads, captures, locks, probes := 0, 0, 0, 0
			readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
				reads++
				if reads == 1 {
					return owner, true
				}
				return muxcoreDaemonStatusIdentity{}, false
			}
			readLiveMuxcoreDaemonAction = func(muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (daemonConvergenceAction, error) {
				if tc.markerBacked {
					return daemonConvergenceReplace, nil
				}
				return daemonConvergenceFail, os.ErrNotExist
			}
			bindObservedMuxcoreOwner = func(_ context.Context, got muxcoreDaemonStatusIdentity) (muxcoreProcessExitProof, observedMarkerSnapshot, error) {
				if got != owner {
					t.Fatal("proof bound to wrong owner")
				}
				return fixtureProcessExitProof{exited: true}, snapshot, nil
			}
			runMarkerlessReconciliation = func(context.Context, muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (bool, bool, error) {
				return false, false, errMuxcoreDaemonOwnerChanged
			}
			captureObservedMarkers = func(got muxcoreDaemonStatusIdentity) (observedMarkerSnapshot, error) {
				captures++
				if got != owner {
					t.Fatal("snapshot rebound to wrong owner")
				}
				if tc.changed {
					return observedMarkerSnapshot{schema: "replaced", schemaPresent: true}, nil
				}
				return snapshot, nil
			}
			acquireRestartLock = func() (io.Closer, error) { locks++; return io.NopCloser(strings.NewReader("")), nil }
			dialMuxcoreControl = func(string, time.Duration) (net.Conn, error) { probes++; return nil, os.ErrNotExist }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waitForMuxcoreConvergence = func(ctx context.Context) error {
				if tc.changed && captures > 0 {
					cancel()
				}
				return ctx.Err()
			}
			err := reconcileMuxcoreDaemonVersion(ctx)
			if tc.changed {
				if !errors.Is(err, context.Canceled) || captures != 1 || probes != 0 {
					t.Fatalf("changed marker authorized cold start: err=%v captures=%d probes=%d", err, captures, probes)
				}
			} else if err != nil || captures != 2 || probes != 1 || locks == 0 {
				t.Fatalf("proven exit did not obtain fenced handback: err=%v captures=%d probes=%d locks=%d", err, captures, probes, locks)
			}
		})
	}
}

func TestObservedMarkerSnapshotRequiresSameIncarnation(t *testing.T) {
	dir := t.TempDir()
	markerPath, legacyPath := filepath.Join(dir, "daemon.marker.json"), filepath.Join(dir, "daemon.version")
	status := muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "generation"}
	exe := filepath.Join(dir, "engram.exe")
	marker := muxcoreDaemonVersionMarker{SchemaVersion: 2, ProductVersion: daemonVersion, DaemonCompatEpoch: 1, PID: status.PID, DaemonGeneration: status.DaemonGeneration, Exe: exe}
	legacy := legacyMuxcoreDaemonVersionMarker{Version: daemonVersion, PID: status.PID, Exe: exe}
	writeJSON := func(path string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(markerPath, marker)
	writeJSON(legacyPath, legacy)
	snapshot, err := snapshotObservedMarkerStateAt(markerPath, legacyPath, status)
	if err != nil || !snapshot.schemaPresent || !snapshot.legacyPresent {
		t.Fatalf("correlated markers rejected: %+v %v", snapshot, err)
	}
	marker.DaemonGeneration = "other"
	writeJSON(markerPath, marker)
	if _, err := snapshotObservedMarkerStateAt(markerPath, legacyPath, status); !errors.Is(err, errMuxcoreDaemonOwnerChanged) {
		t.Fatalf("wrong generation did not trigger rebind: %v", err)
	}
	marker.DaemonGeneration = status.DaemonGeneration
	writeJSON(markerPath, marker)
	legacy.PID++
	writeJSON(legacyPath, legacy)
	if _, err := snapshotObservedMarkerStateAt(markerPath, legacyPath, status); !errors.Is(err, errMuxcoreDaemonOwnerChanged) {
		t.Fatalf("wrong legacy PID did not trigger rebind: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotObservedMarkerStateAt(markerPath, legacyPath, status); err == nil || errors.Is(err, errMuxcoreDaemonOwnerChanged) {
		t.Fatalf("malformed marker was treated as an ordinary transition: %v", err)
	}
}

func TestObservedExitProofRequiresLivePeerBinding(t *testing.T) {
	oldOpen, oldDial, oldPeer, oldSend, oldCapture := openMuxcorePredecessorProcess, dialMuxcoreControl, readMuxcoreControlPeerPID, sendMuxcoreControlRequest, captureObservedMarkers
	t.Cleanup(func() {
		openMuxcorePredecessorProcess, dialMuxcoreControl, readMuxcoreControlPeerPID, sendMuxcoreControlRequest, captureObservedMarkers = oldOpen, oldDial, oldPeer, oldSend, oldCapture
	})
	status := muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "generation"}
	peer, alreadyExited, probes := status.PID, false, 0
	openMuxcorePredecessorProcess = func(int) (muxcoreProcessExitProof, error) { return fixtureProcessExitProof{exited: alreadyExited}, nil }
	dialMuxcoreControl = func(string, time.Duration) (net.Conn, error) { a, b := net.Pipe(); _ = b.Close(); return a, nil }
	readMuxcoreControlPeerPID = func(net.Conn) (int, error) { return peer, nil }
	sendMuxcoreControlRequest = func(context.Context, net.Conn, muxcontrol.Request) (*muxcontrol.Response, error) {
		probes++
		raw, err := json.Marshal(status)
		return &muxcontrol.Response{OK: true, Data: raw}, err
	}
	captureObservedMarkers = func(got muxcoreDaemonStatusIdentity) (observedMarkerSnapshot, error) {
		if got != status {
			t.Fatal("snapshot owner mismatch")
		}
		return observedMarkerSnapshot{}, nil
	}
	proof, _, err := openBoundObservedMuxcoreOwner(context.Background(), status)
	if err != nil || proof == nil || probes != 2 {
		t.Fatalf("live peer was not bound twice: proof=%v probes=%d err=%v", proof, probes, err)
	}
	if err := proof.Close(); err != nil {
		t.Fatal(err)
	}
	peer = 42
	if proof, _, err := openBoundObservedMuxcoreOwner(context.Background(), status); err == nil || proof != nil {
		t.Fatalf("wrong peer supplied exit authority: %v", err)
	}
	peer, alreadyExited = status.PID, true
	if proof, _, err := openBoundObservedMuxcoreOwner(context.Background(), status); err == nil || proof != nil {
		t.Fatalf("already-exited proof was bound to a peer: %v", err)
	}
}

func TestObservedLiveProofNeverAuthorizesColdStart(t *testing.T) {
	oldLock := acquireRestartLock
	t.Cleanup(func() { acquireRestartLock = oldLock })
	acquireRestartLock = func() (io.Closer, error) {
		t.Fatal("live process must not reach namespace handback")
		return nil, errors.New("unexpected fence")
	}
	allowed, err := fencedObservedDaemonExit(context.Background(), fixtureProcessExitProof{}, muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "old"}, observedMarkerSnapshot{})
	if err != nil || allowed {
		t.Fatalf("live process authorized cold start: allowed=%t err=%v", allowed, err)
	}
}

type observedExitedProof struct{ closed bool }

func (*observedExitedProof) Exited() (bool, error) { return true, nil }
func (proof *observedExitedProof) Close() error    { proof.closed = true; return nil }

func TestObservedMarkerTransitionDiscardsProofBeforeRebinding(t *testing.T) {
	oldStatus, oldAction, oldRun, oldWait := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, waitForMuxcoreConvergence
	oldBind, oldCapture, oldLock := bindObservedMuxcoreOwner, captureObservedMarkers, acquireRestartLock
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, waitForMuxcoreConvergence = oldStatus, oldAction, oldRun, oldWait
		bindObservedMuxcoreOwner, captureObservedMarkers, acquireRestartLock = oldBind, oldCapture, oldLock
	})
	before := muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "old"}
	after := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "new"}
	proof := &observedExitedProof{}
	reads, calls := 0, 0
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		reads++
		if reads == 1 {
			return before, true
		}
		if reads == 2 {
			return muxcoreDaemonStatusIdentity{}, false
		}
		return after, true
	}
	readLiveMuxcoreDaemonAction = func(muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (daemonConvergenceAction, error) {
		return daemonConvergenceFail, os.ErrNotExist
	}
	bindObservedMuxcoreOwner = func(_ context.Context, got muxcoreDaemonStatusIdentity) (muxcoreProcessExitProof, observedMarkerSnapshot, error) {
		if got == before {
			return proof, observedMarkerSnapshot{}, nil
		}
		if !proof.closed {
			t.Fatal("stale proof remained open during successor binding")
		}
		return nil, observedMarkerSnapshot{}, errors.New("no successor proof")
	}
	runMarkerlessReconciliation = func(_ context.Context, got muxcoreDaemonStatusIdentity, _ daemonConvergenceIdentity) (bool, bool, error) {
		calls++
		if got == before {
			return false, false, errMuxcoreDaemonOwnerChanged
		}
		if got == after && proof.closed {
			return false, false, nil
		}
		t.Fatalf("unexpected owner %+v", got)
		return false, false, errors.New("unreachable")
	}
	captureObservedMarkers = func(muxcoreDaemonStatusIdentity) (observedMarkerSnapshot, error) {
		return observedMarkerSnapshot{}, errMuxcoreDaemonOwnerChanged
	}
	acquireRestartLock = func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }
	waitForMuxcoreConvergence = func(context.Context) error { return nil }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := reconcileMuxcoreDaemonVersion(ctx); err != nil || calls != 2 || !proof.closed {
		t.Fatalf("successor was not rebound: calls=%d closed=%t err=%v", calls, proof.closed, err)
	}
}

func TestObservedSuccessorRetiresDeadPredecessorProof(t *testing.T) {
	oldStatus, oldAction, oldRun, oldWait := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, waitForMuxcoreConvergence
	oldBind, oldCapture, oldLock := bindObservedMuxcoreOwner, captureObservedMarkers, acquireRestartLock
	oldDial, oldPeer, oldSend := dialMuxcoreControl, readMuxcoreControlPeerPID, sendMuxcoreControlRequest
	t.Cleanup(func() {
		readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, waitForMuxcoreConvergence = oldStatus, oldAction, oldRun, oldWait
		bindObservedMuxcoreOwner, captureObservedMarkers, acquireRestartLock = oldBind, oldCapture, oldLock
		dialMuxcoreControl, readMuxcoreControlPeerPID, sendMuxcoreControlRequest = oldDial, oldPeer, oldSend
	})
	ownerA := muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "old"}
	ownerB := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "new"}
	proof := &observedExitedProof{}
	reads, probes, waits := 0, 0, 0
	readMuxcoreDaemonStatusIdentity = func(string) (muxcoreDaemonStatusIdentity, bool) {
		reads++
		if reads == 1 {
			return ownerA, true
		}
		return muxcoreDaemonStatusIdentity{}, false
	}
	readLiveMuxcoreDaemonAction = func(muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (daemonConvergenceAction, error) {
		return daemonConvergenceFail, os.ErrNotExist
	}
	bindObservedMuxcoreOwner = func(context.Context, muxcoreDaemonStatusIdentity) (muxcoreProcessExitProof, observedMarkerSnapshot, error) {
		return proof, observedMarkerSnapshot{}, nil
	}
	runMarkerlessReconciliation = func(context.Context, muxcoreDaemonStatusIdentity, daemonConvergenceIdentity) (bool, bool, error) {
		return false, false, errMuxcoreDaemonOwnerChanged
	}
	captureObservedMarkers = func(muxcoreDaemonStatusIdentity) (observedMarkerSnapshot, error) {
		return observedMarkerSnapshot{}, nil
	}
	acquireRestartLock = func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }
	dialMuxcoreControl = func(string, time.Duration) (net.Conn, error) {
		probes++
		if probes > 1 {
			t.Fatal("dead A proof reused after live B disappeared")
		}
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
	readMuxcoreControlPeerPID = func(net.Conn) (int, error) { return ownerB.PID, nil }
	sendMuxcoreControlRequest = func(context.Context, net.Conn, muxcontrol.Request) (*muxcontrol.Response, error) {
		raw, err := json.Marshal(ownerB)
		return &muxcontrol.Response{OK: true, Data: raw}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitForMuxcoreConvergence = func(ctx context.Context) error {
		waits++
		if waits == 3 {
			cancel()
		}
		return ctx.Err()
	}
	err := reconcileMuxcoreDaemonVersion(ctx)
	if !errors.Is(err, context.Canceled) || !proof.closed || probes != 1 {
		t.Fatalf("B disappearance reused A exit proof: err=%v closed=%t probes=%d", err, proof.closed, probes)
	}
}

func TestPostRestartAdmissionFencesLegacyOnlyPublication(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		publishSchema, malformed bool
	}{
		{"schema published", true, false},
		{"legacy only", false, false},
		{"malformed legacy", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubObservedProofUnavailable(t)
			oldStatus, oldAction, oldRun, oldAdmit := readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, admitMarkerlessCurrent
			oldLock, oldDial, oldPeer, oldSend := acquireRestartLock, dialMuxcoreControl, readMuxcoreControlPeerPID, sendMuxcoreControlRequest
			t.Cleanup(func() {
				readMuxcoreDaemonStatusIdentity, readLiveMuxcoreDaemonAction, runMarkerlessReconciliation, admitMarkerlessCurrent = oldStatus, oldAction, oldRun, oldAdmit
				acquireRestartLock, dialMuxcoreControl, readMuxcoreControlPeerPID, sendMuxcoreControlRequest = oldLock, oldDial, oldPeer, oldSend
			})
			before := muxcoreDaemonStatusIdentity{PID: 41, DaemonGeneration: "old"}
			after := muxcoreDaemonStatusIdentity{PID: 42, DaemonGeneration: "new"}
			status := before
			dir := t.TempDir()
			markerPath, legacyPath := filepath.Join(dir, "daemon.marker.json"), filepath.Join(dir, "daemon.version")
			exe := filepath.Join(dir, "engram.exe")
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
				t.Fatal("legacy-only publication must not be admitted as markerless")
				return errors.New("unexpected admission")
			}
			fences, probes := 0, 0
			acquireRestartLock = func() (io.Closer, error) {
				fences++
				if tc.publishSchema {
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
			if tc.publishSchema {
				if err != nil || fences != 1 || probes != 1 {
					t.Fatalf("published schema did not receive fresh peer admission: err=%v fences=%d probes=%d", err, fences, probes)
				}
			} else if !errors.Is(err, errMuxcoreDaemonMarkerUncorrelated) || fences != 1 || probes != 0 {
				t.Fatalf("uncorrelated legacy state was admitted: err=%v fences=%d probes=%d", err, fences, probes)
			}
		})
	}
}
