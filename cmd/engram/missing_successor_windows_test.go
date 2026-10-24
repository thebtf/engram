//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	muxserverid "github.com/thebtf/mcp-mux/muxcore/serverid"
	"google.golang.org/grpc"
)

// The receiver deliberately ACKs and exits without starting a successor.
// Production admission, peer binding, locking and readiness are not stubbed.
func TestReconcileRecoversAfterAcknowledgedSuccessorIsAbsent(t *testing.T) {
	root, current := os.Getenv("ENGRAM_TEST_MISSING_ROOT"), os.Getenv("ENGRAM_TEST_MISSING_CURRENT")
	if root == "" || current == "" {
		t.Skip("requires private missing-successor allocation and current executable")
	}
	root, current = filepath.Clean(root), filepath.Clean(current)
	if !filepath.IsAbs(root) || !filepath.IsAbs(current) {
		t.Fatal("fixture inputs must be absolute")
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "ENGRAM_") || strings.HasPrefix(upper, "MCPMUX_") || strings.HasPrefix(upper, "MCP_MUX_") || strings.HasPrefix(upper, "CLAUDE_") || strings.Contains(upper, "PROXY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "API_KEY") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	for key, part := range map[string]string{"TEMP": "temp", "TMP": "temp", "TMPDIR": "temp", "HOME": "home", "USERPROFILE": "home", "APPDATA": "appdata", "LOCALAPPDATA": "localappdata", "XDG_CACHE_HOME": "localappdata", "ENGRAM_DATA_DIR": "data"} {
		directory := filepath.Join(root, part)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, directory)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := grpc.NewServer()
	pb.RegisterEngramServiceServer(fixture, &directDiscoveryFixture{requests: make(chan *pb.InitializeRequest, 32)})
	go func() { _ = fixture.Serve(listener) }()
	t.Cleanup(fixture.Stop)
	t.Setenv("ENGRAM_URL", "http://"+listener.Addr().String())
	t.Setenv("ENGRAM_TOKEN", "fixture-keycard")
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	t.Setenv("ENGRAM_TEST_MISSING_CURRENT", current)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	receiver := filepath.Join(filepath.Dir(root), "inputs", "receiver", "receiver.exe")
	from, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	to, err := os.OpenFile(receiver, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		_ = from.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(to, from)
	if err := errors.Join(copyErr, to.Close(), from.Close()); err != nil {
		t.Fatal(err)
	}
	originalExecutable := currentExecutable
	currentExecutable = func() (string, error) { return current, nil }
	t.Cleanup(func() { currentExecutable = originalExecutable })

	for _, scenario := range []string{"compatible-current-not-stolen", "pingable-lower-not-ready", "ack-exit-no-successor"} {
		t.Run(scenario, func(t *testing.T) {
			version := "v7.0.3"
			if scenario == "compatible-current-not-stolen" {
				version = daemonVersion
			}
			oldNamespace := muxcoreNamespace
			t.Cleanup(func() { muxcoreNamespace = oldNamespace })
			t.Setenv("ENGRAM_CLIENT_INSTANCE_ID", "engram-missing-successor-01a15ebb-"+scenario)
			if err := configureMuxcoreInstallation(); err != nil {
				t.Fatal(err)
			}
			control := muxserverid.DaemonControlPath("", muxcoreNamespace)
			ackPath := filepath.Join(root, scenario+"-ack.json")
			t.Setenv("ENGRAM_TEST_MISSING_ACK", ackPath)
			t.Setenv("ENGRAM_TEST_MISSING_VERSION", version)
			if _, err := os.Lstat(control + ".marker.json"); !os.IsNotExist(err) {
				t.Fatalf("namespace is not private/empty: %v", err)
			}
			tree, err := newUCIInstallHarnessProcessTree()
			if err != nil {
				t.Fatal(err)
			}
			var processes []*uciStartedInstallHarnessProcess
			t.Cleanup(func() {
				for _, process := range processes {
					_ = process.closePipes()
				}
				if status, found := readMuxcoreDaemonStatusIdentity(control); found {
					marker, markerErr := readMuxcoreDaemonVersionMarker(control + ".marker.json")
					if markerErr == nil && marker.PID == status.PID && marker.DaemonGeneration == status.DaemonGeneration && strings.EqualFold(marker.Exe, current) {
						if err := uciStopInstalledAcceptanceDaemon(os.TempDir(), status.PID); err != nil {
							t.Error(err)
						}
					}
				}
				observed := append(tree.ObservedPIDsForExecutable(receiver), tree.ObservedPIDsForExecutable(current)...)
				if err := uciCleanupInstallHarness(tree, processes); err != nil {
					t.Error(err)
				}
				for _, process := range processes {
					t.Logf("CLOSED pid=%d exe=%s exit=%d", process.command.Process.Pid, process.command.Path, process.command.ProcessState.ExitCode())
				}
				for _, pid := range observed {
					if err := uciWaitInstalledAcceptanceProcessExit(pid, 5*time.Second); err != nil {
						t.Error(err)
					}
				}
				if _, found := readMuxcoreDaemonStatusIdentity(control); found {
					t.Error("owned control remains live")
				} else {
					t.Logf("CLOSURE scenario=%s owned_pids=%v control_unavailable=true", scenario, observed)
				}
			})
			component := uciInstallHarnessComponent{role: "missing-successor-receiver", installedExecutable: receiver, command: uciInstallHarnessCommand{Args: []string{"-test.run=^TestMissingSuccessorReceiverProcess$", "-test.v"}}}
			process, err := uciStartInstalledHarnessComponent(tree, component, append(os.Environ(), "ENGRAM_TEST_MISSING_RECEIVER=1"), root, false)
			if process != nil {
				processes = append(processes, process)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(tree.fallback) != 0 || tree.completionPort == 0 {
				t.Fatal("required private Windows job containment/observation unavailable")
			}
			exited := make(chan error, 1)
			go func() { exited <- process.wait() }()
			startup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, err = uciWaitForInstalledAcceptanceDaemonPID(startup, os.TempDir(), receiver)
			cancel()
			if err != nil {
				t.Fatalf("receiver fixture startup failed: %v", err)
			}
			before, found := readMuxcoreDaemonStatusIdentity(control)
			if !found {
				t.Fatal("receiver status absent")
			}
			peer, err := missingSuccessorPeer(control)
			if err != nil || peer != before.PID {
				t.Fatalf("receiver peer=%d status=%+v error=%v", peer, before, err)
			}
			t.Logf("BEFORE scenario=%s pid=%d full_generation=%s peer=%d version=%s epoch=1 requested_current=%s", scenario, before.PID, before.DaemonGeneration, peer, version, current)
			ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			if scenario == "pingable-lower-not-ready" {
				err := waitForMuxcoreDaemonVersion(ctx)
				after, ok := readMuxcoreDaemonStatusIdentity(control)
				if !errors.Is(err, errMuxcoreDaemonStillLower) || !ok || after != before {
					t.Fatalf("pingable lower generation accepted/changed: err=%v before=%+v after=%+v", err, before, after)
				}
				t.Log("CONTROL_PASS: pingable lower generation is not current-version readiness")
				return
			}
			reconcileErr := reconcileMuxcoreDaemonVersion(ctx)
			if scenario == "compatible-current-not-stolen" {
				after, ok := readMuxcoreDaemonStatusIdentity(control)
				if reconcileErr != nil || !ok || after != before {
					t.Fatalf("compatible generation stolen: err=%v before=%+v after=%+v", reconcileErr, before, after)
				}
				if _, err := os.Stat(ackPath); !os.IsNotExist(err) {
					t.Fatalf("compatible receiver received restart: %v", err)
				}
				t.Log("CONTROL_PASS: compatible current PID/full generation remains bound")
				return
			}
			select {
			case err := <-exited:
				if err != nil || process.command.ProcessState.ExitCode() != 0 {
					t.Fatalf("receiver did not close normally after ACK: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("fixture predecessor did not exit after ACK")
			}
			raw, err := os.ReadFile(ackPath)
			if err != nil {
				t.Fatalf("fixture never delivered ACK: %v", err)
			}
			var ack muxcontrol.GracefulRestartOptions
			if err := json.Unmarshal(raw, &ack); err != nil || ack.SuccessorExe != current || ack.DrainTimeoutMs <= 0 {
				t.Fatalf("invalid real ACK/request: %s err=%v", raw, err)
			}
			status, live := readMuxcoreDaemonStatusIdentity(control)
			marker, markerErr := readMuxcoreDaemonVersionMarker(control + ".marker.json")
			t.Logf("ACK_EXIT: predecessor_pid=%d exit=0 successor_exe=%s; AFTER live=%v status=%+v marker=%+v marker_error=%v reconcile_error=%v", before.PID, ack.SuccessorExe, live, status, marker, markerErr, reconcileErr)
			if !live || markerErr != nil || status.PID == before.PID || status.DaemonGeneration == before.DaemonGeneration || marker.PID != status.PID || marker.DaemonGeneration != status.DaemonGeneration || marker.ProductVersion != daemonVersion || marker.DaemonCompatEpoch != muxcoreDaemonCompatEpoch || !strings.EqualFold(marker.Exe, current) {
				t.Fatalf("MISSING_SUCCESSOR_AVAILABILITY_RED: real admitted restart ACKed and predecessor exited, but no requested-current executable/version/epoch-bound replacement became ready; err=%v", reconcileErr)
			}
			peer, err = missingSuccessorPeer(control)
			if err != nil || peer != status.PID || reconcileErr != nil {
				t.Fatalf("recovered current generation is not a successful peer-bound result: peer=%d status=%+v err=%v reconcile=%v", peer, status, err, reconcileErr)
			}
			// A future repair must serve actual current-version MCP, not merely create a marker.
			clientProcess, err := uciStartInstalledHarnessComponent(tree, uciInstallHarnessComponent{role: "recovered-current-client", installedExecutable: current}, os.Environ(), root, true)
			if clientProcess != nil {
				processes = append(processes, clientProcess)
			}
			if err != nil {
				t.Fatal(err)
			}
			client, err := newUCIInstalledAcceptanceMCPClient("recovered-current", clientProcess)
			if err != nil {
				t.Fatal(err)
			}
			mcpCtx, mcpCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer mcpCancel()
			initialized, _, err := client.call(mcpCtx, "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "missing-successor-proof", "version": "1"}})
			var info struct {
				ServerInfo struct {
					Version string `json:"version"`
				} `json:"serverInfo"`
			}
			if err != nil || json.Unmarshal(initialized, &info) != nil || info.ServerInfo.Version != daemonVersion {
				t.Fatalf("recovered initialize is unavailable/stale: %s err=%v", initialized, err)
			}
			if err := client.notification("notifications/initialized", map[string]any{}); err != nil {
				t.Fatal(err)
			}
			listed, _, err := client.call(mcpCtx, "tools/list", map[string]any{})
			if err != nil || !strings.Contains(string(listed), `"codebase_search"`) {
				t.Fatalf("recovered tools/list unavailable: %s err=%v", listed, err)
			}
			t.Log("RECOVERY_GREEN: requested current executable/version/epoch/peer and real MCP are ready")
		})
	}
}

func missingSuccessorPeer(control string) (int, error) {
	connection, err := dialMuxcoreControl(control, time.Second)
	if err != nil {
		return 0, err
	}
	defer connection.Close()
	return readMuxcoreControlPeerPID(connection)
}

type missingSuccessorReceiver struct {
	done       chan struct{}
	once       sync.Once
	generation string
}

func (receiver *missingSuccessorReceiver) HandleStatus() map[string]interface{} {
	return map[string]interface{}{"pid": os.Getpid(), "daemon_generation": receiver.generation, "shutting_down": false}
}

func (receiver *missingSuccessorReceiver) HandleShutdown(int) string {
	receiver.once.Do(func() { close(receiver.done) })
	return "fixture shutting down"
}

func (*missingSuccessorReceiver) HandleSpawn(muxcontrol.Request) (string, string, string, error) {
	return "", "", "", errors.New("receiver does not serve owners")
}

func (*missingSuccessorReceiver) HandleRemove(string) error {
	return errors.New("receiver does not serve owners")
}

func (*missingSuccessorReceiver) HandleRefreshSessionToken(string) (string, error) {
	return "", errors.New("receiver does not serve sessions")
}

func (*missingSuccessorReceiver) HandleReconnectGiveUp(string) error {
	return errors.New("receiver does not serve sessions")
}

func (*missingSuccessorReceiver) HandleListOwners(muxcontrol.Request) (muxcontrol.ListOwnersResponse, error) {
	return muxcontrol.ListOwnersResponse{}, errors.New("receiver does not serve owners")
}

func (*missingSuccessorReceiver) HandleGracefulRestart(int) (string, func(), error) {
	return "", nil, errors.New("explicit successor is required")
}

func (receiver *missingSuccessorReceiver) HandleGracefulRestartWithOptions(options muxcontrol.GracefulRestartOptions) (string, func(), error) {
	if options.SuccessorExe != os.Getenv("ENGRAM_TEST_MISSING_CURRENT") {
		return "", nil, errors.New("unexpected requested executable")
	}
	return "", func() {
		// muxcore calls this only after the real OK response has been written/closed.
		raw, err := json.Marshal(options)
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(os.Getenv("ENGRAM_TEST_MISSING_ACK"), raw, 0o600); err != nil {
			panic(err)
		}
		receiver.once.Do(func() { close(receiver.done) })
	}, nil
}

func TestMissingSuccessorReceiverProcess(t *testing.T) {
	if os.Getenv("ENGRAM_TEST_MISSING_RECEIVER") != "1" {
		return
	}
	if err := configureMuxcoreInstallation(); err != nil {
		t.Fatal(err)
	}
	control := muxserverid.DaemonControlPath("", muxcoreNamespace)
	receiver := &missingSuccessorReceiver{done: make(chan struct{}), generation: fmt.Sprintf("fixture-missing-successor-%d", os.Getpid())}
	server, err := muxcontrol.NewServer(control, receiver, log.New(os.Stderr, "missing-successor-receiver: ", log.LstdFlags))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.Start()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := muxcoreDaemonVersionMarker{SchemaVersion: 2, ProductVersion: os.Getenv("ENGRAM_TEST_MISSING_VERSION"), DaemonCompatEpoch: muxcoreDaemonCompatEpoch, PID: os.Getpid(), DaemonGeneration: receiver.generation, Exe: executable}
	raw, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(control+".marker.json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(control + ".marker.json")
	select {
	case <-receiver.done:
	case <-time.After(30 * time.Second):
		t.Fatal("fixture lifetime exceeded")
	}
}
