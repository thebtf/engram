package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	pb "github.com/thebtf/engram/proto/engram/v1"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type sessionConflictBackend struct {
	pb.UnimplementedEngramServiceServer
	mu       sync.Mutex
	projects []string
}

func (b *sessionConflictBackend) Initialize(context.Context, *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{}, nil
}

func (b *sessionConflictBackend) CallTool(ctx context.Context, req *pb.CallToolRequest) (*pb.CallToolResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer "+memoryTestTokenA {
		return nil, status.Error(codes.Unauthenticated, "fixture token required")
	}
	if req.GetToolName() != "recall_memory" {
		return nil, status.Error(codes.Unimplemented, "unexpected fixture tool")
	}
	b.mu.Lock()
	b.projects = append(b.projects, req.GetProject())
	b.mu.Unlock()
	return &pb.CallToolResponse{ContentJson: []byte(`{"content":[{"type":"text","text":"session-conflict-existing-memory"}]}`)}, nil
}

func TestSessionMemoryConflictChoiceAndSessionIsolation(t *testing.T) {
	scratch := clientIdentityTempDir(t)
	binary := filepath.Join(scratch, "engram")
	goName := "go"
	if runtime.GOOS == "windows" {
		binary += ".exe"
		goName += ".exe"
	}
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", goName), "build", "-p=1", "-o", binary, ".")
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI fixture: %v: %s", err, raw)
	}
	root := memoryTestRepository(t, scratch, "conflict", memoryTestProjectA)
	markerPath := filepath.Join(root, ".engram-project")
	cachePath := filepath.Join(root, ".git", "engram-project.json")
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	cache := []byte(`{"project_id":"` + memoryTestProjectB + `"}`)
	if err := os.WriteFile(cachePath, cache, 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &sessionConflictBackend{}
	grpcServer := grpc.NewServer()
	pb.RegisterEngramServiceServer(grpcServer, backend)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+memoryTestTokenA || r.URL.Path != "/api/projects" {
			http.Error(w, "unexpected request", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"project_id":"` + memoryTestProjectA + `","name":"Fixture A"},{"project_id":"` + memoryTestProjectB + `","name":"Fixture B"}],"unresolved":[]}`))
	}), &http2.Server{}))
	t.Cleanup(func() { grpcServer.Stop(); server.Close() })
	installation := filepath.Join(scratch, "install")
	a := startMemoryTestClient(t, binary, root, installation, server.URL, memoryTestTokenA)
	b := startMemoryTestClient(t, binary, root, installation, server.URL, memoryTestTokenA)
	memoryTestInitialize(t, a)
	memoryTestInitialize(t, b)
	call := func(client *memoryTestClient, id, tool string, args map[string]any) map[string]any {
		return client.call(t, id, "tools/call", map[string]any{"name": tool, "arguments": args})
	}
	read := map[string]any{"query": "session-conflict-existing-memory"}
	if response := call(a, "read-a", "recall_memory", read); !memoryTestRefused(response) {
		t.Fatalf("conflicting read was not held: %v", response)
	}
	response := call(a, "choices-a", "project_context", map[string]any{"action": "repair"})
	if memoryTestRefused(response) {
		t.Fatalf("choices unavailable: %v", response)
	}
	data := memoryTestUnwrap(response)
	if data["status"] != "user_input_required" || data["session_binding"] != "transport" {
		t.Fatalf("missing isolated transport choices: %v", response)
	}
	choices, ok := data["choices"].([]any)
	if !ok || len(choices) != 2 {
		t.Fatalf("missing choices: %v", response)
	}
	ref := ""
	for _, raw := range choices {
		choice := raw.(map[string]any)
		if choice["provenance"] == "common_cache" {
			ref, _ = choice["selection_ref"].(string)
		}
	}
	if ref == "" {
		t.Fatal("missing cache choice")
	}
	selection := map[string]any{"action": "repair", "selection_ref": ref}
	if response := call(b, "foreign-choice", "project_context", selection); !memoryTestRefused(response) {
		t.Fatalf("another process accepted choice: %v", response)
	}
	if response := call(a, "select-a", "project_context", selection); memoryTestRefused(response) || memoryTestUnwrap(response)["status"] != "session_read_bound" {
		t.Fatalf("selection failed: %v", response)
	}
	if response := call(b, "read-b", "recall_memory", read); !memoryTestRefused(response) {
		t.Fatalf("another process used binding: %v", response)
	}
	response = call(a, "resume-a", "recall_memory", read)
	raw, _ := json.Marshal(response)
	if memoryTestRefused(response) || !strings.Contains(string(raw), "session-conflict-existing-memory") {
		t.Fatalf("safe read failed: %v", response)
	}
	if response := call(a, "replay-a", "recall_memory", read); !memoryTestRefused(response) {
		t.Fatalf("one-use binding replayed: %v", response)
	}
	backend.mu.Lock()
	if len(backend.projects) != 1 || backend.projects[0] != memoryTestProjectB {
		t.Errorf("wrong backend scoped call: %v", backend.projects)
	}
	backend.mu.Unlock()
	for path, want := range map[string][]byte{markerPath: marker, cachePath: cache} {
		current, err := os.ReadFile(path)
		if err != nil || string(current) != string(want) {
			t.Fatal("file conflict was modified")
		}
	}
}
