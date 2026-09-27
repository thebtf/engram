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
}

func (f *directDiscoveryFixture) Initialize(_ context.Context, request *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	identity := request.GetProjectIdentityV3()
	if identity == nil {
		return nil, errors.New("direct discovery requires a V3 repository descriptor")
	}
	f.requests <- request
	projectKey, scope := identity.GetAnchorProjectId(), identity.GetScope()
	return &pb.InitializeResponse{
		Tools:            []*pb.ToolDefinition{{Name: "codebase_context", Description: "select authorized checkout"}},
		CanonicalProject: projectKey,
		ProjectResolutionV3: &pb.ProjectResolutionResultV3{
			Outcome:       pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED,
			Correlation:   "direct-stdio-fixture",
			ProjectKey:    &projectKey,
			ResolvedScope: &scope,
		},
	}, nil
}

func TestDirectBinaryStdioListsCodeToolsWithoutManualIdentity(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := grpc.NewServer()
	discovery := &directDiscoveryFixture{requests: make(chan *pb.InitializeRequest, 2)}
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
	var project struct {
		Version int    `json:"version"`
		ID      string `json:"project_id"`
		Name    string `json:"name"`
		Scope   string `json:"scope"`
	}
	marker, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(marker, &project); err != nil || project.Version != 3 || project.ID == "" || project.Scope != "repository" {
		t.Fatalf("direct fixture requires V3 repository anchor in current checkout: %v", err)
	}
	scratch := filepath.Join(filepath.Dir(commonDir), ".agent", "tmp")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := os.MkdirTemp(scratch, "ed-")
	if err != nil {
		t.Fatal(err)
	}
	// The client and its daemon inherit the candidate checkout cwd. A relative
	// temp path keeps both sockets short while resolving to private primary scratch.
	physicalTempRoot := filepath.Join(state, "temp")
	if err := os.Mkdir(physicalTempRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	unixTempRoot, err := filepath.Rel(root, physicalTempRoot)
	if err != nil {
		t.Fatal(err)
	}
	tempRoot := unixTempRoot
	if runtime.GOOS == "windows" {
		// GetTempPath2 resolves Windows TEMP independently of the child cwd.
		tempRoot = physicalTempRoot
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(state); err != nil {
			t.Error(err)
		}
	})
	probeRoot, err := filepath.Rel(filepath.Join(root, "cmd", "engram"), physicalTempRoot)
	if err != nil {
		t.Fatal(err)
	}
	namespace := muxcoreInstallationNamespace(strings.Repeat("0", 32))
	for _, socketRoot := range []string{unixTempRoot, probeRoot} {
		for _, socket := range []string{
			muxserverid.DaemonControlPath(socketRoot, namespace),
			muxserverid.ControlPath(socketRoot, namespace, strings.Repeat("0", 16)),
		} {
			if len([]byte(socket)) > 103 {
				t.Fatalf("direct fixture Unix socket exceeds Darwin AF_UNIX pathname bound: %q (%d bytes)", socket, len([]byte(socket)))
			}
		}
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
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
	controlRoot, err := filepath.Rel(commandDir, physicalTempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		controlRoot = physicalTempRoot
	}
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
					for _, name := range []string{"codebase_context", "codebase_index", "codebase_status"} {
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
	var first string
	for range 2 {
		run()
		id, err := os.ReadFile(filepath.Join(state, "installation", "client-instance-id"))
		if err != nil || !strings.HasPrefix(string(id), "engram-") {
			t.Fatalf("direct installation identity absent: %v", err)
		}
		select {
		case request := <-discovery.requests:
			identity := request.GetProjectIdentityV3()
			if request.GetProject() != "" || request.GetProjectIdentity() != nil || identity.GetVersion() != 3 || identity.GetAnchorProjectId() != project.ID || identity.GetName() != project.Name || identity.GetScope() != project.Scope || identity.GetClientInstanceId() != strings.TrimSuffix(string(id), "\n") {
				t.Fatalf("direct Initialize did not derive V3 identity from repository and installation: %s", request)
			}
		default:
			t.Fatal("direct client did not send V3 Initialize to fixture")
		}
		if first != "" && string(id) != first {
			t.Fatalf("direct client restart changed identity")
		}
		first = string(id)
	}
}
