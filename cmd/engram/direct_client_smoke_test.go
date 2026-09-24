package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
)

type directDiscoveryFixture struct {
	pb.UnimplementedEngramServiceServer
}

func (*directDiscoveryFixture) Initialize(context.Context, *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{Tools: []*pb.ToolDefinition{{Name: "codebase_context", Description: "select authorized checkout"}}}, nil
}

func TestDirectBinaryStdioListsCodeToolsWithoutManualIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := grpc.NewServer()
	pb.RegisterEngramServiceServer(fixture, &directDiscoveryFixture{})
	go func() { _ = fixture.Serve(listener) }()
	defer fixture.Stop()
	state := t.TempDir()
	for _, part := range []string{"home", "appdata", "localappdata", "temp"} {
		if err := os.Mkdir(filepath.Join(state, part), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(state, "engram")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build direct binary: %v: %s", err, out)
	}
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
		}
	})
	run := func() {
		command := exec.CommandContext(ctx, binary)
		command.Dir = filepath.Join(state, "home")
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
		defer func() { _ = stdin.Close(); _ = command.Wait() }()
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
		if first != "" && string(id) != first {
			t.Fatalf("direct client restart changed identity")
		}
		first = string(id)
	}
}
