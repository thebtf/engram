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
	"syscall"
	"testing"
	"time"

	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	muxserverid "github.com/thebtf/mcp-mux/muxcore/serverid"
	"google.golang.org/grpc"
)

type directDiscoveryFixture struct {
	pb.UnimplementedEngramServiceServer
	requests chan *pb.InitializeRequest
	calls    chan *pb.CallToolRequest
}

func (f *directDiscoveryFixture) Initialize(_ context.Context, request *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	if request.GetProject() != "" {
		return nil, errors.New("direct discovery must be unscoped")
	}
	f.requests <- request
	return &pb.InitializeResponse{
		Tools: []*pb.ToolDefinition{
			{Name: "issues", Description: "issue fixture", InputSchemaJson: []byte(`{"type":"object"}`)},
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
	f.calls <- request
	return &pb.CallToolResponse{ContentJson: []byte(`{"ok":true}`)}, nil
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

func TestDirectBinaryStdioListsCodeToolsWithoutManualIdentity(t *testing.T) {
	directBinaryStdioFixture(t, false)
}

func TestDirectBinaryIssueSessionsDoNotInheritDaemonEnvironment(t *testing.T) {
	directBinaryStdioFixture(t, true)
}

func directBinaryStdioFixture(t *testing.T, issueSessions bool) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := grpc.NewServer()
	discovery := &directDiscoveryFixture{requests: make(chan *pb.InitializeRequest, 16), calls: make(chan *pb.CallToolRequest, 8)}
	pb.RegisterEngramServiceServer(fixture, discovery)
	go func() { _ = fixture.Serve(listener) }()
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
	daemonMayHaveStarted := false
	daemonStopped := false
	t.Cleanup(func() {
		if daemonMayHaveStarted && !daemonStopped {
			t.Errorf("retain direct fixture state %s: daemon closure is not verified", state)
			return
		}
		if err := os.RemoveAll(state); err != nil {
			t.Error(err)
		}
	})
	clientRoot := root
	if issueSessions {
		clientRoot = memoryTestRepository(t, state, "session-project", memoryTestProjectA)
	}
	// Use the shortest real pathname from each caller cwd. Relative paths are
	// not always shorter than the absolute private primary scratch path.
	physicalTempRoot := filepath.Join(state, "temp")
	if err := os.Mkdir(physicalTempRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	tempRoot := directFixtureTempRoot(t, clientRoot, physicalTempRoot)
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
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	t.Cleanup(cancel)
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
		if !daemonMayHaveStarted {
			return
		}
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
			return
		}
		daemonStopped = true
	})
	run := func(offline bool, session string, args map[string]any, keepAlive bool) *pb.CallToolRequest {
		command := exec.CommandContext(ctx, binary)
		command.Dir = clientRoot
		command.Env = append([]string(nil), env...)
		if session != "" {
			command.Env = append(command.Env, "CLAUDE_SESSION_ID="+session)
		}
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
		daemonMayHaveStarted = true
		var waited sync.Once
		finish := func() {
			waited.Do(func() { _ = stdin.Close(); _ = command.Wait() })
		}
		if keepAlive {
			t.Cleanup(finish)
		} else {
			defer finish()
		}
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 16<<20)
		encoder := json.NewEncoder(stdin)
		methods := []string{"initialize", "tools/list"}
		if offline {
			methods = methods[:1]
		}
		if args != nil {
			methods = append(methods, "tools/call")
		}
		for _, method := range methods {
			params := map[string]any{}
			if method == "initialize" {
				params = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "direct-fixture", "version": "1"}}
			}
			if method == "tools/call" {
				params = map[string]any{"name": "issues", "arguments": args}
			}
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": method, "method": method, "params": params}); err != nil {
				t.Fatal(err)
			}
			response := make(chan []byte, 1)
			go func() {
				for scanner.Scan() {
					raw := append([]byte(nil), scanner.Bytes()...)
					var frame struct {
						ID json.RawMessage `json:"id"`
					}
					if json.Unmarshal(raw, &frame) == nil && string(frame.ID) == `"`+method+`"` {
						response <- raw
						return
					}
				}
				response <- nil
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
				if method == "initialize" {
					var initialized struct {
						Instructions string `json:"instructions"`
					}
					if err := json.Unmarshal(frame.Result, &initialized); err != nil {
						t.Fatal(err)
					}
					for _, want := range []string{"Use available injected evidence before repeating discovery", "not before every task or file edit", "Routine task completion requires neither a memory write nor feedback", "native caller's verified canonical project", "PROJECT_MIGRATION_REQUIRED"} {
						if !strings.Contains(initialized.Instructions, want) {
							t.Fatalf("initialize omitted %q (backend unavailable: %t)", want, offline)
						}
					}
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
		if args != nil {
			select {
			case request := <-discovery.calls:
				return request
			case <-ctx.Done():
				t.Fatalf("issue call did not reach gRPC fixture: %v", ctx.Err())
			}
		}
		return nil
	}
	if issueSessions {
		const hostA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		const hostB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		const reportedB = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		cases := []struct {
			name, session, expected string
			args                    map[string]any
		}{
			{"host_a_create", hostA, hostA, map[string]any{"action": "create", "title": "Synthetic A", "request_key": "session-a", "format": "json"}},
			{"absent_b_create", "", "", map[string]any{"action": "create", "title": "Synthetic B", "request_key": "session-b", "format": "json"}},
			{"absent_b_reported_comment", "", "", map[string]any{"action": "comment", "id": 1, "body": "Synthetic B comment", "created_by_session": reportedB, "format": "json"}},
			{"host_b_material_update", hostB, hostB, map[string]any{"action": "update", "id": 1, "expected_revision": "1", "title": "Synthetic B update", "created_by_session": reportedB, "format": "json"}},
		}
		var owner muxcoreDaemonStatusIdentity
		for index, tc := range cases {
			request := run(false, tc.session, tc.args, index == 0)
			identity, err := os.ReadFile(filepath.Join(state, "installation", "client-instance-id"))
			if err != nil {
				t.Fatal(err)
			}
			control := muxserverid.DaemonControlPath(controlRoot, muxcoreInstallationNamespace(strings.TrimSpace(string(identity))))
			status, ok := readMuxcoreDaemonStatusIdentity(control)
			marker, markerErr := readMuxcoreDaemonVersionMarker(control + ".marker.json")
			if !ok || markerErr != nil || status.PID <= 0 || status.DaemonGeneration == "" || status.ShuttingDown || marker.PID != status.PID || marker.DaemonGeneration != status.DaemonGeneration {
				t.Fatalf("%s: private daemon status/marker mismatch: status=%+v marker=%+v err=%v", tc.name, status, marker, markerErr)
			}
			if index == 0 {
				owner = status
			} else if status.PID != owner.PID || status.DaemonGeneration != owner.DaemonGeneration {
				t.Fatalf("%s: client did not join the same private daemon", tc.name)
			}
			if request.GetToolName() != "issues" || request.GetProject() != memoryTestProjectA {
				t.Errorf("%s: wrong tool or canonical project: %s %s", tc.name, request.GetToolName(), request.GetProject())
			}
			if request.GetSessionId() != tc.expected {
				t.Errorf("%s: forwarded host session %q, want %q; absent must not inherit another client or use a correlation tag", tc.name, request.GetSessionId(), tc.expected)
			}
			if expected, ok := tc.args["created_by_session"].(string); ok {
				var arguments map[string]any
				if err := json.Unmarshal(request.GetArgumentsJson(), &arguments); err != nil || arguments["created_by_session"] != expected {
					t.Errorf("%s: reported session did not survive stdio forwarding", tc.name)
				}
			}
		}
		return
	}
	var first string
	for client := 0; client < 2; client++ {
		run(false, "", nil, false)
		id, err := os.ReadFile(filepath.Join(state, "installation", "client-instance-id"))
		if err != nil || !strings.HasPrefix(string(id), "engram-") {
			t.Fatalf("direct installation identity absent: %v", err)
		}
		if client == 0 {
			select {
			case request := <-discovery.requests:
				if request.GetProject() != "" || request.GetClientName() != "engram-daemon" || request.GetClientVersion() == "" {
					t.Fatalf("direct Initialize was not unscoped capability discovery: %s", request)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("direct client did not send Initialize to fixture")
			}
		}
		if first != "" && string(id) != first {
			t.Fatalf("direct client restart changed identity")
		}
		first = string(id)
	}
	fixture.Stop()
	run(true, "", nil, false)
}
