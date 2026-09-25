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
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		gitDir, err := os.Stat(filepath.Join(root, ".git"))
		if err == nil && gitDir.IsDir() {
			if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
				break
			}
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("primary repository root not found")
		}
		root = parent
	}
	var project struct {
		ID    string `json:"project_id"`
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	marker, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(marker, &project); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(root, ".agent", "tmp")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := os.MkdirTemp(scratch, "ed-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(state); err != nil {
			t.Error(err)
		}
	})
	if runtime.GOOS == "linux" {
		probe, err := net.Listen("unix", filepath.Join(state, "probe.sock"))
		if errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) {
			t.Skipf("AF_UNIX unsupported on test scratch filesystem %s: %v", scratch, err)
		}
		if err != nil {
			t.Fatalf("probe AF_UNIX on test scratch filesystem: %v", err)
		}
		if err := probe.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, part := range []string{"home", "appdata", "localappdata", "temp"} {
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
	env = append(env, "ENGRAM_URL=http://"+listener.Addr().String(), "ENGRAM_TOKEN=fixture-keycard", "ENGRAM_CODE_INTEL_ENABLED=true", "ENGRAM_DATA_DIR="+filepath.Join(state, "installation"), "USERPROFILE="+filepath.Join(state, "home"), "HOME="+filepath.Join(state, "home"), "APPDATA="+filepath.Join(state, "appdata"), "LOCALAPPDATA="+localCache, "XDG_CACHE_HOME="+localCache, "TEMP="+filepath.Join(state, "temp"), "TMP="+filepath.Join(state, "temp"), "TMPDIR="+filepath.Join(state, "temp"))
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		controlRoot, err := uciInstalledAcceptancePhysicalPath(filepath.Join(state, "temp"))
		if err != nil {
			t.Error(err)
			return
		}
		pid, err := uciWaitForInstalledAcceptanceDaemonPID(cleanupCtx, controlRoot, binary)
		if err != nil {
			t.Error(err)
			return
		}
		if err := uciStopInstalledAcceptanceDaemon(controlRoot, pid); err != nil {
			t.Error(err)
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
			if request.GetProject() != "" || request.GetProjectIdentity() != nil || identity.GetVersion() != 3 || identity.GetAnchorProjectId() != project.ID || identity.GetName() != project.Name || identity.GetScope() != project.Scope || identity.GetClientInstanceId() != string(id) {
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
