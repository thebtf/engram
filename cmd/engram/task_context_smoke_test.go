package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/taskcontext"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	"github.com/thebtf/mcp-mux/muxcore/serverid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type taskContextBaselineBackend struct {
	pb.UnimplementedEngramServiceServer
	calls atomic.Int32
}

func (*taskContextBaselineBackend) Initialize(context.Context, *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{Tools: []*pb.ToolDefinition{{Name: "codebase_task_context", InputSchemaJson: []byte(`{"type":"object"}`)}}}, nil
}

func (backend *taskContextBaselineBackend) CallTool(ctx context.Context, request *pb.CallToolRequest) (*pb.CallToolResponse, error) {
	if request.ToolName != "codebase_task_context" || request.Project != "10000000-0000-4000-8000-000000000001" || request.SessionId == "host-baseline" {
		panic("invalid native boundary")
	}
	incoming, _ := metadata.FromIncomingContext(ctx)
	if len(incoming.Get(taskcontext.LocatorMetadataKey)) != 1 {
		panic("missing native locator")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > taskcontext.MaxDuration {
		panic("unbounded native RPC")
	}
	backend.calls.Add(1)
	content, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": `{"schema":1,"status":"empty","reason_code":"NO_MATCH"}`}}, "isError": false})
	return &pb.CallToolResponse{CanonicalProject: request.Project, ContentJson: content}, nil
}

// This baseline measures admission + the actual native proxy/RPC path only.
// It is deliberately not evidence of a useful code/source/call result.
func TestTaskContextReadyDaemonTransportBaseline(t *testing.T) {
	binary := os.Getenv("ENGRAM_TASK_CONTEXT_TEST_BINARY")
	if binary == "" {
		t.Skip("ENGRAM_TASK_CONTEXT_TEST_BINARY must name this candidate's built CLI")
	}
	require.True(t, filepath.IsAbs(binary))
	root := t.TempDir()
	cwd := filepath.Join(root, "worktree")
	require.NoError(t, os.Mkdir(cwd, 0o700))
	git := exec.Command("git", "init", "--quiet", cwd)
	require.NoError(t, git.Run())
	marker := []byte("{\"version\":3,\"project_id\":\"10000000-0000-4000-8000-000000000001\",\"name\":\"task fixture\",\"scope\":\"repository\"}\n")
	require.NoError(t, os.WriteFile(filepath.Join(cwd, ".engram-project"), marker, 0o600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	backend := &taskContextBaselineBackend{}
	pb.RegisterEngramServiceServer(server, backend)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	instance := "task-baseline-" + strings.ReplaceAll(filepath.Base(root), "_", "-")
	environment := make([]string, 0)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "ENGRAM_") || strings.HasPrefix(upper, "MCP_MUX_") || strings.HasPrefix(upper, "CLAUDE_") || upper == "HOME" || upper == "USERPROFILE" || upper == "TEMP" || upper == "TMP" || upper == "TMPDIR" || upper == "APPDATA" || upper == "LOCALAPPDATA" || upper == "XDG_CACHE_HOME" {
			continue
		}
		environment = append(environment, entry)
	}
	for _, part := range []string{"home", "temp", "installation", "cache"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, part), 0o700))
	}
	environment = append(environment, "ENGRAM_URL=http://"+listener.Addr().String(), "ENGRAM_TOKEN=synthetic-fixture-keycard", "ENGRAM_CLIENT_INSTANCE_ID="+instance, "ENGRAM_DATA_DIR="+filepath.Join(root, "installation"), "HOME="+filepath.Join(root, "home"), "USERPROFILE="+filepath.Join(root, "home"), "TEMP="+filepath.Join(root, "temp"), "TMP="+filepath.Join(root, "temp"), "TMPDIR="+filepath.Join(root, "temp"), "APPDATA="+filepath.Join(root, "cache"), "LOCALAPPDATA="+filepath.Join(root, "cache"), "XDG_CACHE_HOME="+filepath.Join(root, "cache"))
	controlPath := serverid.DaemonControlPath(filepath.Join(root, "temp"), muxcoreInstallationNamespace(instance))
	invoke := func() (taskcontext.Response, time.Duration, int) {
		request := taskcontext.Request{Schema: 1, HostSessionID: "host-baseline", Query: "Alpha", DeadlineUnixMS: time.Now().Add(500 * time.Millisecond).UnixMilli(), MaxBytes: 8000}
		raw, err := json.Marshal(request)
		require.NoError(t, err)
		command := exec.Command(binary, "task-context", "--json")
		command.Dir = cwd
		command.Env = environment
		command.Stdin = bytes.NewReader(raw)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		start := time.Now()
		output, err := command.Output()
		elapsed := time.Since(start)
		require.NoError(t, err, "stderr=%s", stderr.String())
		var response taskcontext.Response
		require.NoError(t, json.Unmarshal(output, &response), "output=%s", output)
		require.LessOrEqual(t, len(output), 8000)
		return response, elapsed, len(output)
	}
	cold, elapsed, size := invoke()
	require.Equal(t, "not_ready", cold.Status, "cold=%+v", cold)
	t.Logf("NATIVE_COLD elapsed_ms=%.3f bytes=%d status=%s", float64(elapsed.Microseconds())/1000, size, cold.Status)
	_, err = os.Stat(controlPath + ".marker.json")
	require.True(t, os.IsNotExist(err), "cold path started a daemon")
	daemon := exec.Command(binary, muxcoreDaemonFlag)
	daemon.Dir = cwd
	daemon.Env = environment
	var daemonLog bytes.Buffer
	daemon.Stderr = &daemonLog
	require.NoError(t, daemon.Start())
	done := make(chan error, 1)
	go func() { done <- daemon.Wait() }()
	t.Cleanup(func() {
		response, err := muxcontrol.SendWithTimeout(controlPath, muxcontrol.Request{Cmd: "shutdown", DrainTimeoutMs: 1000}, 2*time.Second)
		if err != nil || response == nil || !response.OK {
			t.Errorf("owned daemon shutdown failed: %v", err)
			return
		}
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("owned daemon did not terminate")
		}
	})
	ready := time.NewTimer(5 * time.Second)
	defer ready.Stop()
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(controlPath + ".marker.json"); err == nil {
			break
		}
		select {
		case <-tick.C:
		case <-ready.C:
			t.Fatalf("owned daemon not ready: %s", daemonLog.String())
		}
	}
	for index := range 3 {
		response, elapsed, size := invoke()
		t.Logf("NATIVE_ADMISSION_BASELINE sample=%d elapsed_ms=%.3f bytes=%d status=%s backend_calls=%d useful=false", index, float64(elapsed.Microseconds())/1000, size, response.Status, backend.calls.Load())
		require.Equal(t, "empty", response.Status, "response=%+v", response)
		require.Equal(t, "NO_MATCH", response.ReasonCode)
		require.Less(t, elapsed, 500*time.Millisecond, "transport alone exceeds total callback budget")
	}
	require.EqualValues(t, 3, backend.calls.Load())
	stored, err := os.ReadFile(filepath.Join(cwd, ".engram-project"))
	require.NoError(t, err)
	require.Equal(t, marker, stored)
	_, err = os.Stat(filepath.Join(cwd, ".git", "engram-project.json"))
	require.True(t, os.IsNotExist(err), "native check published a cache")
}
