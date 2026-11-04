package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/version"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	muxserverid "github.com/thebtf/mcp-mux/muxcore/serverid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type directDiscoveryFixture struct {
	pb.UnimplementedEngramServiceServer
	requests  chan *pb.InitializeRequest
	mutations atomic.Int32
	statuses  atomic.Int32
}

func (f *directDiscoveryFixture) Initialize(ctx context.Context, request *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	if request.GetProject() != "" {
		return nil, errors.New("direct discovery must be unscoped")
	}
	incoming, _ := metadata.FromIncomingContext(ctx)
	if values := incoming.Get("authorization"); len(values) != 1 || values[0] != "Bearer fixture-keycard" {
		return nil, status.Error(codes.Unauthenticated, "fixture requires its synthetic bearer")
	}
	select {
	case f.requests <- request:
	default:
	}
	return &pb.InitializeResponse{
		ServerVersion: version.Daemon,
		Tools: []*pb.ToolDefinition{
			{Name: "vault", Description: "fixture backend status and store", InputSchemaJson: []byte(`{"type":"object","properties":{"action":{"type":"string","enum":["status","store"]},"name":{"type":"string"},"value":{"type":"string"},"scope":{"type":"string","enum":["global"]}},"required":["action"],"additionalProperties":false}`)},
			{Name: "codebase_context", Description: "select authorized checkout", InputSchemaJson: []byte(`{"type":"object"}`)},
			{Name: "codebase_index", Description: "index selected checkout", InputSchemaJson: []byte(`{"type":"object"}`)},
			{Name: "codebase_status", Description: "read selected index status", InputSchemaJson: []byte(`{"type":"object"}`)},
			{Name: "codebase_search", Description: "search authorized view", InputSchemaJson: []byte(`{"type":"object"}`)},
			{Name: "codebase_read", Description: "read authorized source span", InputSchemaJson: []byte(`{"type":"object"}`)},
			{Name: "codebase_graph", Description: "explore authorized graph", InputSchemaJson: []byte(`{"type":"object"}`)},
		},
	}, nil
}

func (f *directDiscoveryFixture) CallTool(_ context.Context, request *pb.CallToolRequest) (*pb.CallToolResponse, error) {
	var args struct {
		Action string `json:"action"`
		Name   string `json:"name"`
		Value  string `json:"value"`
		Scope  string `json:"scope"`
	}
	if request.GetToolName() != "vault" || json.Unmarshal(request.GetArgumentsJson(), &args) != nil {
		return nil, errors.New("unexpected fixture tool call")
	}
	if args.Action == "store" && args.Name == "outage-fixture" && args.Value == "synthetic-only" && args.Scope == "global" {
		f.mutations.Add(1)
		return nil, status.Error(codes.Unavailable, "fixture recorded mutation; outcome unknown")
	}
	if args.Action != "status" {
		return nil, errors.New("unexpected fixture tool call")
	}
	f.statuses.Add(1)
	return &pb.CallToolResponse{ContentJson: []byte(`{"fixture":"backend-recovered"}`)}, nil
}

func directFixtureTempRoot(t *testing.T, cwd, physicalRoot string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return physicalRoot
	}
	relative, err := filepath.Rel(cwd, physicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(physicalRoot) < len(relative) {
		return physicalRoot
	}
	return relative
}

func TestDirectFixtureTempRoot(t *testing.T) {
	physical := "/primary/.agent/tmp/ed-1/temp"
	cases := []struct{ cwd, want string }{
		{"/candidate", physical},
		{"/candidate/cmd/engram", physical},
		{"/primary", ".agent/tmp/ed-1/temp"},
		{"/primary/candidate", "../.agent/tmp/ed-1/temp"},
	}
	if runtime.GOOS == "windows" {
		physical = `D:\primary\.agent\tmp\ed-1\temp`
		cases = []struct{ cwd, want string }{
			{`C:\candidate`, physical},
			{`C:\candidate\cmd\engram`, physical},
		}
	}
	for _, tc := range cases {
		t.Run(tc.cwd, func(t *testing.T) {
			if got := directFixtureTempRoot(t, tc.cwd, physical); got != tc.want {
				t.Fatalf("fixture temp root = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDirectBinaryStdioBackendOutageRecovery(t *testing.T) {
	testDirectBinaryStdio(t, true)
}

func TestDirectBinaryStdioListsCodeToolsWithoutManualIdentity(t *testing.T) {
	testDirectBinaryStdio(t, false)
}

func testDirectBinaryStdio(t *testing.T, outage bool) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := grpc.NewServer()
	discovery := &directDiscoveryFixture{requests: make(chan *pb.InitializeRequest, 16)}
	pb.RegisterEngramServiceServer(fixture, discovery)
	if !outage {
		go func() { _ = fixture.Serve(listener) }()
	}
	defer listener.Close()
	defer fixture.Stop()
	root := uciInstalledAcceptanceCandidateSourceRoot(t)
	git := exec.Command("git", "rev-parse", "--show-toplevel", "--git-common-dir")
	git.Dir = root
	output, err := git.Output()
	if err != nil {
		t.Fatalf("resolve direct fixture Git checkout: %v", err)
	}
	paths := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(paths) != 2 || filepath.Clean(paths[0]) != root {
		t.Fatalf("direct fixture source is not the current Git checkout: %q", output)
	}
	commonDir := strings.TrimSpace(paths[1])
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	if filepath.Base(commonDir) != ".git" {
		t.Fatalf("direct fixture scratch needs primary checkout Git directory: %s", commonDir)
	}
	if info, err := os.Stat(commonDir); err != nil || !info.IsDir() {
		t.Fatalf("direct fixture primary Git directory unavailable: %s: %v", commonDir, err)
	}
	scratch := filepath.Join(filepath.Dir(commonDir), ".agent", "tmp")
	if primary := os.Getenv("ENGRAM_TEST_PRIMARY_ROOT"); primary != "" {
		if !filepath.IsAbs(primary) {
			t.Fatalf("primary scratch root must be absolute: %q", primary)
		}
		primary, err = filepath.EvalSymlinks(primary)
		if err != nil {
			t.Fatal(err)
		}
		agent := filepath.Join(primary, ".agent")
		if info, err := os.Lstat(agent); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("unsafe primary coordination directory %q: %v", agent, err)
		}
		scratch = filepath.Join(agent, "tmp")
	}
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(scratch); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("unsafe primary scratch directory %q: %v", scratch, err)
	}
	state, err := os.MkdirTemp(scratch, "ed-")
	if err != nil {
		t.Fatal(err)
	}
	// Use the shortest real pathname from each caller cwd. Relative paths are
	// not always shorter than the absolute private primary scratch path.
	physicalTempRoot := filepath.Join(state, "temp")
	if err := os.Mkdir(physicalTempRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	tempRoot := directFixtureTempRoot(t, root, physicalTempRoot)
	t.Cleanup(func() {
		if err := os.RemoveAll(state); err != nil {
			t.Error(err)
		} else {
			t.Logf("removed owned fixture state %s", state)
		}
	})
	probeRoot := directFixtureTempRoot(t, filepath.Join(root, "cmd", "engram"), physicalTempRoot)
	namespace := muxcoreInstallationNamespace(strings.Repeat("0", 32))
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		for _, socketRoot := range []string{tempRoot, probeRoot} {
			for _, socket := range []string{
				muxserverid.DaemonControlPath(socketRoot, namespace),
				muxserverid.ControlPath(socketRoot, namespace, strings.Repeat("0", 16)),
			} {
				if len([]byte(socket)) > 103 {
					t.Fatalf("direct fixture Unix socket exceeds Darwin AF_UNIX pathname bound: %q (%d bytes)", socket, len([]byte(socket)))
				}
			}
		}
		for _, socket := range []string{
			muxserverid.DaemonControlPath(probeRoot, namespace),
			muxserverid.ControlPath(probeRoot, namespace, strings.Repeat("0", 16)),
		} {
			probe, err := net.Listen("unix", socket)
			if errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) {
				t.Skipf("AF_UNIX unsupported on test scratch filesystem %s: %v", scratch, err)
			}
			if err != nil {
				t.Fatalf("probe muxcore socket at %s: %v", socket, err)
			}
			if err := probe.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, part := range []string{"home", "appdata", "localappdata"} {
		if err := os.Mkdir(filepath.Join(state, part), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(state, "engram")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build direct binary: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	env := make([]string, 0)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "ENGRAM_") || strings.HasPrefix(upper, "MCP_MUX_") || strings.HasPrefix(upper, "CLAUDE_") || upper == "USERPROFILE" || upper == "HOME" || upper == "APPDATA" || upper == "LOCALAPPDATA" || upper == "TEMP" || upper == "TMP" || upper == "TMPDIR" || upper == "XDG_CACHE_HOME" {
			continue
		}
		env = append(env, entry)
	}
	localCache := filepath.Join(state, "localappdata")
	env = append(env, "ENGRAM_URL=http://"+listener.Addr().String(), "ENGRAM_TOKEN=fixture-keycard", "ENGRAM_CODE_INTEL_ENABLED=true", "ENGRAM_DATA_DIR="+filepath.Join(state, "installation"), "USERPROFILE="+filepath.Join(state, "home"), "HOME="+filepath.Join(state, "home"), "APPDATA="+filepath.Join(state, "appdata"), "LOCALAPPDATA="+localCache, "XDG_CACHE_HOME="+localCache, "TEMP="+tempRoot, "TMP="+tempRoot, "TMPDIR="+tempRoot)
	commandDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	controlRoot := directFixtureTempRoot(t, commandDir, physicalTempRoot)
	t.Cleanup(func() {
		identity, err := os.ReadFile(filepath.Join(state, "installation", "client-instance-id"))
		if err != nil {
			t.Error(err)
			return
		}
		controlPath := muxserverid.DaemonControlPath(controlRoot, muxcoreInstallationNamespace(strings.TrimSpace(string(identity))))
		markerPath := controlPath + ".marker.json"
		pid, found, err := uciInstalledAcceptanceDaemonPID(controlPath, markerPath, binary)
		if err != nil || !found {
			t.Errorf("direct fixture daemon election: pid=%d found=%v error=%v", pid, found, err)
			return
		}
		status, ok := readMuxcoreDaemonStatusIdentity(controlPath)
		marker, markerErr := readMuxcoreDaemonVersionMarker(markerPath)
		if !ok || markerErr != nil || status.PID != pid || marker.PID != pid || status.DaemonGeneration != marker.DaemonGeneration || status.ShuttingDown {
			t.Errorf("direct fixture daemon ownership changed before shutdown: pid=%d status=%+v marker=%+v error=%v", pid, status, marker, markerErr)
			return
		}
		response, err := muxcontrol.SendWithTimeout(controlPath, muxcontrol.Request{Cmd: "shutdown", DrainTimeoutMs: 2_000}, 5*time.Second)
		if err != nil || response == nil || !response.OK {
			t.Errorf("stop owned direct fixture daemon: response=%+v error=%v", response, err)
			return
		}
		if err := uciWaitInstalledAcceptanceProcessExit(pid, 5*time.Second); err != nil {
			t.Error(err)
		} else {
			t.Logf("owned fixture daemon %d exited after identity-checked shutdown", pid)
		}
	})
	run := func() {
		command := exec.CommandContext(ctx, binary)
		command.Dir = root
		command.Env = env
		stdin, err := command.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := command.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr strings.Builder
		command.Stderr = &stderr
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		var waited sync.Once
		finish := func() {
			waited.Do(func() { _ = stdin.Close(); _ = command.Wait() })
		}
		defer finish()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 16<<20)
		encoder := json.NewEncoder(stdin)
		if outage {
			directOutageScenario(ctx, t, encoder, scanner, fixture, listener, discovery)
			return
		}
		for _, method := range []string{"initialize", "tools/list"} {
			params := map[string]any{}
			if method == "initialize" {
				params = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "direct-fixture", "version": "1"}}
			}
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": method, "method": method, "params": params}); err != nil {
				t.Fatal(err)
			}
			response := make(chan []byte, 1)
			go func() {
				if scanner.Scan() {
					response <- append([]byte(nil), scanner.Bytes()...)
				} else {
					response <- nil
				}
			}()
			select {
			case raw := <-response:
				var frame struct {
					Result json.RawMessage `json:"result"`
					Error  json.RawMessage `json:"error"`
				}
				if len(raw) == 0 || json.Unmarshal(raw, &frame) != nil || len(frame.Error) != 0 || len(frame.Result) == 0 {
					finish()
					t.Fatalf("%s failed: response %s stderr %s", method, raw, stderr.String())
				}
				if method == "tools/list" {
					var listed struct {
						Tools []struct {
							Name string `json:"name"`
						} `json:"tools"`
					}
					if err := json.Unmarshal(frame.Result, &listed); err != nil {
						t.Fatal(err)
					}
					names := make(map[string]bool)
					for _, tool := range listed.Tools {
						names[tool.Name] = true
					}
					for _, name := range []string{"codebase_context", "codebase_index", "codebase_status", "codebase_search", "codebase_read", "codebase_graph"} {
						if !names[name] {
							t.Fatalf("direct tools/list omitted %s (tools: %v)", name, names)
						}
					}
				}
			case <-ctx.Done():
				finish()
				t.Fatalf("%s timed out: %v stderr %s", method, ctx.Err(), stderr.String())
			}
		}
	}
	if outage {
		run()
		return
	}
	var first string
	for client := 0; client < 2; client++ {
		t.Logf("direct client %d", client+1)
		run()
		id, err := os.ReadFile(filepath.Join(state, "installation", "client-instance-id"))
		if err != nil || !strings.HasPrefix(string(id), "engram-") {
			t.Fatalf("direct installation identity absent: %v", err)
		}
		// The daemon can serve the second client from its authenticated catalog;
		// a second Initialize RPC is not required for a shared connection.
		if client == 0 {
			select {
			case request := <-discovery.requests:
				if request.GetProject() != "" || request.GetClientName() != "engram-daemon" || request.GetClientVersion() == "" {
					t.Fatalf("direct Initialize was not unscoped capability discovery: %s", request)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("first direct client did not send Initialize to fixture within 15s")
			case <-ctx.Done():
				t.Fatalf("first direct client discovery stopped: %v", ctx.Err())
			}
		}
		if first != "" && string(id) != first {
			t.Fatalf("direct client restart changed identity")
		}
		first = string(id)
	}
}

func directOutageScenario(ctx context.Context, t *testing.T, encoder *json.Encoder, scanner *bufio.Scanner, fixture *grpc.Server, listener net.Listener, discovery *directDiscoveryFixture) {
	t.Helper()
	type frame struct {
		ID     *string         `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	frames := make(chan []byte, 16)
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(frames)
		for scanner.Scan() {
			raw := append([]byte(nil), scanner.Bytes()...)
			select {
			case frames <- raw:
			case <-done:
				return
			}
		}
	}()
	pending := make(map[string]bool)
	responses := make(map[string]frame)
	var notifications []frame
	defer func() { t.Logf("received %d stdio notifications", len(notifications)) }()
	send := func(id, method string, params any) string {
		t.Helper()
		if pending[id] {
			t.Fatalf("duplicate request ID %q", id)
		}
		pending[id] = true
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	read := func(id, label string, timeout time.Duration) (frame, bool) {
		t.Helper()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			if result, ok := responses[id]; ok {
				delete(responses, id)
				delete(pending, id)
				return result, true
			}
			select {
			case raw, ok := <-frames:
				if !ok {
					t.Fatalf("%s: stdio closed: %v", label, scanner.Err())
				}
				var result frame
				if err := json.Unmarshal(raw, &result); err != nil {
					t.Fatalf("%s returned invalid frame: %q: %v", label, raw, err)
				}
				if result.ID == nil {
					if result.Method == "" {
						t.Fatalf("%s returned a frame without ID or notification method: %s", label, raw)
					}
					if len(notifications) == 64 {
						t.Fatalf("%s received too many stdio notifications", label)
					}
					notifications = append(notifications, result)
					continue
				}
				if !pending[*result.ID] {
					t.Fatalf("%s returned unexpected response ID %q: %s", label, *result.ID, raw)
				}
				if _, exists := responses[*result.ID]; exists {
					t.Fatalf("%s returned duplicate response ID %q", label, *result.ID)
				}
				t.Logf("stdio response %s: %s", *result.ID, raw)
				responses[*result.ID] = result
			case <-timer.C:
				return frame{}, false
			}
		}
	}
	initialized, ok := read(send("initialize", "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "outage-fixture", "version": "1"}}), "initialize", 10*time.Second)
	if !ok || len(initialized.Error) != 0 || len(initialized.Result) == 0 {
		t.Fatalf("local initialize failed: response=%+v received=%t", initialized, ok)
	}
	listed, ok := read(send("outage-list", "tools/list", map[string]any{}), "outage tools/list", 5*time.Second)
	if !ok || len(listed.Error) != 0 || len(listed.Result) == 0 {
		t.Fatalf("configured backend unavailable: tools/list did not return: response=%+v received=%t", listed, ok)
	}
	if !strings.Contains(string(listed.Result), `"name":"vault"`) || !strings.Contains(string(listed.Result), `"name":"codebase_search"`) {
		t.Fatalf("configured backend unavailable: tools/list did not expose the supported backend catalog: %s", listed.Result)
	}
	// Stdio has no caller-supplied timeout field on this route. The existing
	// dispatcher bounds each tool call to 30 seconds; allow five seconds only
	// for the response to cross the local transport after that real deadline.
	unavailable, ok := read(send("unavailable-call", "tools/call", map[string]any{"name": "vault", "arguments": map[string]string{"action": "status"}}), "unavailable call", 35*time.Second)
	if !ok || len(unavailable.Error) != 0 || !strings.Contains(string(unavailable.Result), `"isError":true`) || !strings.Contains(string(unavailable.Result), "CAPABILITY_NOT_SENT") {
		t.Fatalf("unresponsive backend did not report not-sent within the existing call budget: response=%+v received=%t", unavailable, ok)
	}
	if count := discovery.statuses.Load(); count != 0 {
		t.Fatalf("unresponsive backend received %d status calls", count)
	}
	// This original request is already pending while the listener is held.
	// It must survive more than three seconds and complete once discovery works.
	slowCall := send("slow-first-call", "tools/call", map[string]any{"name": "vault", "arguments": map[string]string{"action": "status"}})
	if response, returned := read(slowCall, "slow first call before recovery", 4*time.Second); returned {
		t.Fatalf("slow first call returned before backend recovery: %+v", response)
	}
	ping, ok := read(send("pending-ping", "ping", map[string]any{}), "ping during discovery", 5*time.Second)
	if !ok || len(ping.Error) != 0 || len(ping.Result) == 0 {
		t.Fatalf("local ping blocked during discovery: response=%+v received=%t", ping, ok)
	}
	go func() { _ = fixture.Serve(listener) }()
	first, ok := read(slowCall, "original slow first call", 15*time.Second)
	if !ok || len(first.Error) != 0 || strings.Contains(string(first.Result), `"isError":true`) || !strings.Contains(string(first.Result), "backend-recovered") {
		t.Fatalf("original slow first call did not recover: response=%+v received=%t", first, ok)
	}
	if count := discovery.statuses.Load(); count != 1 {
		t.Fatalf("backend received %d status calls, want original request once", count)
	}
	recoveredList, ok := read(send("recovered-list", "tools/list", map[string]any{}), "recovered tools/list", 15*time.Second)
	if !ok || len(recoveredList.Error) != 0 || !strings.Contains(string(recoveredList.Result), `"name":"vault"`) {
		t.Fatalf("same-client discovery did not recover: response=%+v received=%t", recoveredList, ok)
	}
	call, ok := read(send("recovered-call", "tools/call", map[string]any{"name": "vault", "arguments": map[string]string{"action": "status"}}), "recovered call", 15*time.Second)
	if !ok || len(call.Error) != 0 || strings.Contains(string(call.Result), `"isError":true`) || !strings.Contains(string(call.Result), "backend-recovered") {
		t.Fatalf("same-client backend call did not recover: response=%+v received=%t", call, ok)
	}

	mutation, ok := read(send("unknown-mutation", "tools/call", map[string]any{"name": "vault", "arguments": map[string]string{"action": "store", "name": "outage-fixture", "value": "synthetic-only", "scope": "global"}}), "unknown-outcome mutation", 5*time.Second)
	if !ok || (len(mutation.Error) == 0 && !strings.Contains(string(mutation.Result), `"isError":true`)) {
		t.Fatalf("mutation with unknown outcome did not return an error: response=%+v received=%t", mutation, ok)
	}
	if count := discovery.mutations.Load(); count != 1 {
		t.Fatalf("fixture received mutation %d times before backend loss; want exactly once", count)
	}

	drainInitializes := func() {
		for count := len(discovery.requests); count > 0; count-- {
			<-discovery.requests
		}
	}
	drainInitializes()
	address := listener.Addr().String()
	fixture.Stop()
	drainInitializes()
	statusBeforeLoss := discovery.statuses.Load()
	lostCall := send("lost-call", "tools/call", map[string]any{"name": "vault", "arguments": map[string]string{"action": "status"}})
	lost, returned := read(lostCall, "call after backend loss", time.Second)
	if returned && !strings.Contains(string(lost.Result), "OUTCOME_UNKNOWN") && !strings.Contains(string(lost.Result), "CAPABILITY_NOT_SENT") {
		t.Fatalf("call after backend loss returned neither unknown nor not-sent: %+v", lost)
	}
	statusAtRestart := discovery.statuses.Load()

	restartedListener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("restart synthetic backend listener: %v", err)
	}
	restarted := grpc.NewServer()
	pb.RegisterEngramServiceServer(restarted, discovery)
	defer restarted.Stop()
	defer restartedListener.Close()
	go func() { _ = restarted.Serve(restartedListener) }()
	if !returned {
		lost, ok = read(lostCall, "original pending call after second recovery", 20*time.Second)
		if !ok || len(lost.Error) != 0 || strings.Contains(string(lost.Result), `"isError":true`) || !strings.Contains(string(lost.Result), "backend-recovered") {
			t.Fatalf("original pending call did not recover: response=%+v received=%t", lost, ok)
		}
		if count := discovery.statuses.Load(); count != statusBeforeLoss+1 {
			t.Fatalf("pending original call produced %d status calls, want one", count-statusBeforeLoss)
		}
	} else if count := discovery.statuses.Load(); count != statusAtRestart {
		t.Fatalf("failed original call was replayed after restart: before=%d after=%d", statusAtRestart, count)
	}
	readyCtx, cancelReady := context.WithTimeout(ctx, 15*time.Second)
	defer cancelReady()
	select {
	case request := <-discovery.requests:
		if request.GetProject() != "" || request.GetClientName() != "engram-daemon" || request.GetClientVersion() == "" {
			t.Fatalf("post-restart discovery was not unscoped daemon initialization: %s", request)
		}
	case <-readyCtx.Done():
		t.Fatalf("authenticated backend discovery did not resume after restart: %v", readyCtx.Err())
	}
	call, ok = read(send("second-recovered-call", "tools/call", map[string]any{"name": "vault", "arguments": map[string]string{"action": "status"}}), "call after backend restart", 15*time.Second)
	if !ok || len(call.Error) != 0 || strings.Contains(string(call.Result), `"isError":true`) || !strings.Contains(string(call.Result), "backend-recovered") {
		t.Fatalf("same-client backend call did not recover after restart: response=%+v received=%t", call, ok)
	}
	if count := discovery.mutations.Load(); count != 1 {
		t.Errorf("unknown-outcome mutation was automatically replayed: fixture received %d calls, want 1", count)
	}
}
