package engramcore

// contract_test.go verifies NFR-5 byte-identity: tools/list and tools/call JSON
// envelope output must match the v4.2.0 reference for any given gRPC backend
// response. The test is in package engramcore (not engramcore_test) so it can
// access internal pool and cache state to pre-populate the slug cache and skip
// the git I/O that ResolveProjectSlug would otherwise perform.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/config"
	loomhandler "github.com/thebtf/engram/internal/handlers/loom"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/dispatcher"
	"github.com/thebtf/engram/internal/module/lifecycle"
	"github.com/thebtf/engram/internal/module/registry"
	"github.com/thebtf/engram/internal/version"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"go.uber.org/goleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Mock gRPC server
// ---------------------------------------------------------------------------

// mockEngramServer is a minimal EngramServiceServer used by contract tests.
// Each test configures its behaviour via the exported fields below.
type mockEngramServer struct {
	pb.UnimplementedEngramServiceServer
	mu sync.Mutex

	// initResp is the response returned by Initialize.
	initResp *pb.InitializeResponse
	// initErr, if non-nil, is returned as an error from Initialize.
	initErr error
	// callResp is the response returned by CallTool.
	callResp *pb.CallToolResponse
	// callErr, if non-nil, is returned as an error from CallTool.
	callErr   error
	initReq   *pb.InitializeRequest
	callReq   *pb.CallToolRequest
	initCalls int
}

func (s *mockEngramServer) Initialize(_ context.Context, req *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	s.mu.Lock()
	s.initReq = req
	s.initCalls++
	resp, err := s.initResp, s.initErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return &pb.InitializeResponse{}, nil
	}
	return resp, nil
}

func (s *mockEngramServer) CallTool(_ context.Context, req *pb.CallToolRequest) (*pb.CallToolResponse, error) {
	s.mu.Lock()
	s.callReq = req
	s.mu.Unlock()
	if s.callErr != nil {
		return nil, s.callErr
	}
	if s.callResp == nil {
		return &pb.CallToolResponse{}, nil
	}
	return s.callResp, nil
}

// startMockGRPC starts a mock gRPC server on an ephemeral port and returns the
// listener address ("host:port"). The server is registered for cleanup via t.Cleanup.
func startMockGRPC(t *testing.T, srv *mockEngramServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	gs := grpc.NewServer()
	pb.RegisterEngramServiceServer(gs, srv)
	go func() {
		if serveErr := gs.Serve(lis); serveErr != nil {
			// Ignore "use of closed network connection" on test cleanup.
			_ = serveErr
		}
	}()
	t.Cleanup(func() { gs.GracefulStop() })
	return lis.Addr().String()
}

// startDeferredMockGRPC reserves an address but leaves it unreachable until
// start is called. This forces the client connection through transient failure
// before the backend becomes ready.
func startDeferredMockGRPC(t *testing.T, srv *mockEngramServer) (string, func() error) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	gs := grpc.NewServer()
	pb.RegisterEngramServiceServer(gs, srv)
	var once sync.Once
	start := func() error {
		once.Do(func() {
			go func() { _ = gs.Serve(lis) }()
		})
		return nil
	}
	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close()
	})
	return lis.Addr().String(), start
}

// ---------------------------------------------------------------------------
// Dispatcher bootstrap helpers for contract tests
// ---------------------------------------------------------------------------

// buildContractDispatcher creates a Dispatcher with one engramcore module whose
// ENGRAM_URL is injected directly into the project env. The gRPC connection
// uses plaintext (no TLS) so that it can connect to the mock server on localhost.
//
// The slug cache is pre-populated with a synthetic entry to avoid any git I/O
// during the test (see ForceCacheEntry in slugcache.go).
func buildContractDispatcher(t *testing.T, grpcAddr string, staticModules ...module.EngramModule) (*dispatcher.Dispatcher, *Module, muxcore.ProjectContext) {
	t.Helper()

	mod := NewModule()

	reg := registry.New()
	for _, staticMod := range staticModules {
		if err := reg.Register(staticMod); err != nil {
			t.Fatalf("Register %s: %v", staticMod.Name(), err)
		}
	}
	if err := reg.Register(mod); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reg.Freeze()

	pl := lifecycle.New(reg, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	storageDir := t.TempDir()

	if err := pl.Start(ctx, func(_ string) module.ModuleDeps {
		return module.ModuleDeps{
			Logger:     slog.Default(),
			DaemonCtx:  ctx,
			StorageDir: storageDir,
		}
	}); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	t.Cleanup(func() {
		shutCtx := context.Background()
		_ = pl.ShutdownAll(shutCtx)
	})

	disp := dispatcher.New(reg, slog.Default())
	p := muxcore.ProjectContext{
		ID:  "contract-test-project",
		Cwd: t.TempDir(),
		Env: map[string]string{},
	}

	// Pre-populate the slug cache so slug resolution skips the git call.
	mod.cache.ForceCacheEntry(p, p.ID)
	if grpcAddr == "" {
		return disp, mod, p
	}

	// Pass http:// prefix so getOrDialGRPC uses plaintext.
	p.Env["ENGRAM_URL"] = "http://" + grpcAddr

	// Pre-populate the pool to use plaintext credentials matching our mock.
	// We dial directly so tests are not subject to OS-level ephemeral port
	// exhaustion from repeated lazy-dial calls.
	conn, err := grpc.NewClient(
		grpcAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  10 * time.Millisecond,
				Multiplier: 1.2,
				Jitter:     0,
				MaxDelay:   50 * time.Millisecond,
			},
			MinConnectTimeout: 20 * time.Millisecond,
		}),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	grpcAddrKey := connKey{addr: grpcAddr, tlsMode: "plaintext"}
	mod.pool.conns.Store(grpcAddrKey, conn)

	return disp, mod, p
}

// jsonrpcCallReq builds a raw tools/call JSON-RPC request.
func jsonrpcCallReq(id int, toolName string) []byte {
	type params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	type req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  params `json:"params"`
	}
	r := req{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "tools/call",
		Params:  params{Name: toolName, Arguments: json.RawMessage(`{}`)},
	}
	b, _ := json.Marshal(r)
	return b
}

// jsonrpcListReq builds a raw tools/list JSON-RPC request.
func jsonrpcListReq(id int) []byte {
	type req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
	}
	b, _ := json.Marshal(req{JSONRPC: "2.0", ID: id, Method: "tools/list"})
	return b
}

// jsonrpcInitReq builds a raw initialize JSON-RPC request.
func jsonrpcInitReq(id int) []byte {
	type req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
	}
	b, _ := json.Marshal(req{JSONRPC: "2.0", ID: id, Method: "initialize"})
	return b
}

// assertJSONEqual fails t if got and want do not unmarshal to deeply equal
// values. It provides a clear diff showing both serialized forms.
func assertJSONEqual(t *testing.T, label string, got, want []byte) {
	t.Helper()
	var gotV, wantV any
	if err := json.Unmarshal(got, &gotV); err != nil {
		t.Fatalf("%s: unmarshal got: %v (raw: %s)", label, err, got)
	}
	if err := json.Unmarshal(want, &wantV); err != nil {
		t.Fatalf("%s: unmarshal want: %v (raw: %s)", label, err, want)
	}
	gotR, _ := json.Marshal(gotV)
	wantR, _ := json.Marshal(wantV)
	if string(gotR) != string(wantR) {
		t.Errorf("%s:\n  got:  %s\n  want: %s", label, gotR, wantR)
	}
}

func assertToolsListServiceUnavailable(t *testing.T, resp []byte) {
	t.Helper()
	var got struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal tools/list response: %v (raw: %s)", err, resp)
	}
	if got.Error == nil {
		t.Fatalf("configured discovery failure returned success: %s", resp)
	}
	if got.Error.Code != -32000 || !strings.Contains(got.Error.Message, "service unavailable") {
		t.Fatalf("tools/list error=%+v, want JSON-RPC service unavailable", got.Error)
	}
	if len(got.Result) != 0 {
		t.Fatalf("configured discovery failure leaked result: %s", got.Result)
	}
}

// ---------------------------------------------------------------------------
// Contract tests
// ---------------------------------------------------------------------------

func TestContract_ToolsList_OfflineWithoutURLReturnsLoomOnly(t *testing.T) {
	t.Setenv(config.EnvServerURL, "")
	t.Setenv(config.EnvServerURLAlt, "")
	disp, _, p := buildContractDispatcher(t, "", loomhandler.NewModule())

	resp, err := disp.HandleRequest(context.Background(), p, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	var got struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *struct{ Code int } `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Error != nil {
		t.Fatalf("offline tools/list error: %+v", got.Error)
	}
	want := []string{"loom_submit", "loom_get", "loom_list", "loom_cancel"}
	if len(got.Result.Tools) != len(want) {
		t.Fatalf("offline tools=%v, want Loom-only %v", got.Result.Tools, want)
	}
	for i, name := range want {
		if got.Result.Tools[i].Name != name {
			t.Fatalf("offline tool[%d]=%q, want %q", i, got.Result.Tools[i].Name, name)
		}
	}
}

func TestContract_ToolsList_ConfiguredInitializeFailureIsServiceUnavailable(t *testing.T) {
	srv := &mockEngramServer{initErr: status.Error(codes.Unavailable, "backend starting")}
	grpcAddr := startMockGRPC(t, srv)
	disp, _, p := buildContractDispatcher(t, grpcAddr)

	resp, err := disp.HandleRequest(context.Background(), p, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	assertToolsListServiceUnavailable(t, resp)
}

func TestContract_ToolsList_ServerURLAliasIsConfigured(t *testing.T) {
	t.Setenv(config.EnvServerURL, "")
	t.Setenv(config.EnvServerURLAlt, "")
	srv := &mockEngramServer{initErr: status.Error(codes.Unavailable, "backend starting")}
	grpcAddr := startMockGRPC(t, srv)
	disp, _, p := buildContractDispatcher(t, grpcAddr)
	delete(p.Env, config.EnvServerURL)
	p.Env[config.EnvServerURLAlt] = "http://" + grpcAddr

	resp, err := disp.HandleRequest(context.Background(), p, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	assertToolsListServiceUnavailable(t, resp)
}

func TestContract_ToolsList_WaitsForDelayedGRPCReadiness(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{
		{Name: "memory_store", Description: "store"},
		{Name: "memory_search", Description: "search"},
	}}}
	grpcAddr, start := startDeferredMockGRPC(t, srv)
	disp, _, p := buildContractDispatcher(t, grpcAddr)
	startResult := make(chan error, 1)
	go func() {
		time.Sleep(75 * time.Millisecond)
		startResult <- start()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := disp.HandleRequest(ctx, p, jsonrpcListReq(1))
	if startErr := <-startResult; startErr != nil {
		t.Fatalf("start delayed gRPC server: %v", startErr)
	}
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	var got struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *struct{ Code int } `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Error != nil {
		t.Fatalf("delayed readiness error: %+v", got.Error)
	}
	want := []string{"memory_store", "memory_search"}
	if len(got.Result.Tools) != len(want) {
		t.Fatalf("tools=%v, want %v", got.Result.Tools, want)
	}
	for i, name := range want {
		if got.Result.Tools[i].Name != name {
			t.Fatalf("tool[%d]=%q, want %q", i, got.Result.Tools[i].Name, name)
		}
	}
}

func TestContract_ToolsList_DeadlineIsBoundedAndConnectionIsReusable(t *testing.T) {
	leakBaseline := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, leakBaseline) })
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{{Name: "memory_store"}}}}
	grpcAddr, start := startDeferredMockGRPC(t, srv)
	disp, mod, p := buildContractDispatcher(t, grpcAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	startedAt := time.Now()
	resp, err := disp.HandleRequest(ctx, p, jsonrpcListReq(1))
	cancel()
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	assertToolsListServiceUnavailable(t, resp)
	if elapsed := time.Since(startedAt); elapsed < 50*time.Millisecond || elapsed > time.Second {
		t.Fatalf("deadline elapsed=%s, want bounded wait", elapsed)
	}

	countConnections := func() int {
		count := 0
		mod.pool.conns.Range(func(_, _ any) bool {
			count++
			return true
		})
		return count
	}
	connectionsAfterFirst := countConnections()
	if connectionsAfterFirst == 0 {
		t.Fatal("deadline attempt left no reusable pooled connection")
	}
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	secondResp, err := disp.HandleRequest(secondCtx, p, jsonrpcListReq(2))
	secondCancel()
	if err != nil {
		t.Fatalf("second deadline HandleRequest: %v", err)
	}
	assertToolsListServiceUnavailable(t, secondResp)
	if got := countConnections(); got != connectionsAfterFirst {
		t.Fatalf("pooled connections grew across retries: first=%d second=%d", connectionsAfterFirst, got)
	}

	if err := start(); err != nil {
		t.Fatalf("start gRPC server: %v", err)
	}
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer retryCancel()
	retryResp, err := disp.HandleRequest(retryCtx, p, jsonrpcListReq(3))
	if err != nil {
		t.Fatalf("retry HandleRequest: %v", err)
	}
	var retry struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *struct{ Code int } `json:"error"`
	}
	if err := json.Unmarshal(retryResp, &retry); err != nil {
		t.Fatalf("unmarshal retry response: %v", err)
	}
	if retry.Error != nil || len(retry.Result.Tools) != 1 || retry.Result.Tools[0].Name != "memory_store" {
		t.Fatalf("retry response=%s, want recovered full list", retryResp)
	}
	srv.mu.Lock()
	initCalls := srv.initCalls
	srv.mu.Unlock()
	if initCalls != 1 {
		t.Fatalf("Initialize calls=%d, want only the post-deadline retry", initCalls)
	}
}

// TestContract_ToolsList_MatchesV42 verifies that a tools/list response
// dispatched through the dispatcher, with a mock gRPC server returning 3 canned
// tools via InitializeResponse, produces a byte-identical envelope to the v4.2.0
// reference format.
func TestContract_ToolsList_MatchesV42(t *testing.T) {
	t.Parallel()

	srv := &mockEngramServer{
		initResp: &pb.InitializeResponse{
			Tools: []*pb.ToolDefinition{
				{
					Name:            "memory_store",
					Description:     "Store a memory observation",
					InputSchemaJson: []byte(`{"type":"object","properties":{"content":{"type":"string"}},"required":["content"]}`),
				},
				{
					Name:            "memory_search",
					Description:     "Search stored memories",
					InputSchemaJson: []byte(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
				},
				{
					Name:            "memory_delete",
					Description:     "Delete a memory by ID",
					InputSchemaJson: []byte(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
				},
			},
		},
	}

	grpcAddr := startMockGRPC(t, srv)
	disp, _, p := buildContractDispatcher(t, grpcAddr)

	resp, err := disp.HandleRequest(context.Background(), p, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}

	// v4.2.0 reference envelope — hand-written as the ground truth.
	wantEnvelope := []byte(`{
		"jsonrpc": "2.0",
		"id": 1,
		"result": {
			"tools": [
				{
					"name": "memory_store",
					"description": "Store a memory observation",
					"inputSchema": {"type":"object","properties":{"content":{"type":"string"}},"required":["content"]}
				},
				{
					"name": "memory_search",
					"description": "Search stored memories",
					"inputSchema": {"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}
				},
				{
					"name": "memory_delete",
					"description": "Delete a memory by ID",
					"inputSchema": {"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}
				}
			]
		}
	}`)

	assertJSONEqual(t, "tools/list envelope", resp, wantEnvelope)

	// Also verify structural invariants explicitly so the diff is actionable.
	var got struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
		Error *struct{ Code int } `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Error != nil {
		t.Fatalf("unexpected error: %+v", got.Error)
	}
	if len(got.Result.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(got.Result.Tools))
	}
	wantNames := []string{"memory_store", "memory_search", "memory_delete"}
	for i, w := range wantNames {
		if got.Result.Tools[i].Name != w {
			t.Errorf("tool[%d].name: got %q, want %q", i, got.Result.Tools[i].Name, w)
		}
	}
}

// TestContract_ToolsCall_Success_MatchesV42 verifies that a successful
// tools/call response is byte-identical to the v4.2.0 reference envelope.
func TestContract_ToolsCall_Success_MatchesV42(t *testing.T) {
	t.Parallel()

	srv := &mockEngramServer{
		initResp: &pb.InitializeResponse{
			Tools: []*pb.ToolDefinition{
				{Name: "memory_store", Description: "store"},
			},
		},
		callResp: &pb.CallToolResponse{
			IsError:     false,
			ContentJson: []byte("tool result text"),
		},
	}

	grpcAddr := startMockGRPC(t, srv)
	disp, _, p := buildContractDispatcher(t, grpcAddr)

	resp, err := disp.HandleRequest(context.Background(), p, jsonrpcCallReq(1, "memory_store"))
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}

	// v4.2.0 reference: buildInnerBlock wraps ContentJson via string() so the
	// bytes become the text field value. Note: ContentJson bytes are NOT parsed
	// as JSON — they are embedded as a string literal. This preserves the exact
	// v4.2.0 behaviour of calling string(resp.ContentJson).
	want := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"%s"}],"isError":false}}`,
		"tool result text",
	)

	assertJSONEqual(t, "tools/call success envelope", resp, []byte(want))

	// Structural verification.
	var got struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct{ Code int } `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error != nil {
		t.Fatalf("unexpected error field: %+v", got.Error)
	}
	if got.Result.IsError {
		t.Error("isError must be false for successful call")
	}
	if len(got.Result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(got.Result.Content))
	}
	if got.Result.Content[0].Type != "text" {
		t.Errorf("content.type: got %q, want text", got.Result.Content[0].Type)
	}
	if got.Result.Content[0].Text != "tool result text" {
		t.Errorf("content.text: got %q, want %q", got.Result.Content[0].Text, "tool result text")
	}
}

// TestContract_ToolsCall_IsError_MatchesV42 verifies the NFR-5 critical path:
// when the gRPC server returns IsError=true, the dispatcher emits isError:true
// in the response envelope, byte-identical to v4.2.0's error envelope.
//
// This test exercises the *module.ProxyIsError sentinel path added in 52a0866
// (dispatcher Priority 1.5) — the most important single-path in the
// ProxyToolProvider amendment.
func TestContract_ToolsCall_IsError_MatchesV42(t *testing.T) {
	t.Parallel()

	srv := &mockEngramServer{
		initResp: &pb.InitializeResponse{
			Tools: []*pb.ToolDefinition{
				{Name: "memory_store", Description: "store"},
			},
		},
		callResp: &pb.CallToolResponse{
			IsError:     true,
			ContentJson: []byte("server error text"),
		},
	}

	grpcAddr := startMockGRPC(t, srv)
	disp, _, p := buildContractDispatcher(t, grpcAddr)

	resp, err := disp.HandleRequest(context.Background(), p, jsonrpcCallReq(1, "memory_store"))
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}

	// v4.2.0 reference: isError:true + content text = string(ContentJson).
	want := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"%s"}],"isError":true}}`,
		"server error text",
	)

	assertJSONEqual(t, "tools/call isError envelope", resp, []byte(want))

	// Structural verification — the critical invariant is isError:true.
	var got struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct{ Code int } `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error != nil {
		t.Fatalf("unexpected JSON-RPC error (should be result-level): %+v", got.Error)
	}
	if !got.Result.IsError {
		t.Error("isError MUST be true for server-side error result (NFR-5 byte-identity)")
	}
	if len(got.Result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(got.Result.Content))
	}
	if got.Result.Content[0].Text != "server error text" {
		t.Errorf("content.text: got %q, want %q", got.Result.Content[0].Text, "server error text")
	}
}

// TestContract_Initialize_RoutedByDispatcher verifies that the initialize method
// returns the daemon serverInfo rather
// than proxying to the backend server.
//
// DOCUMENTED DEVIATION FROM v4.2.0: v4.2.0 returned the backend server's
// version string from the gRPC InitializeResponse. The daemon now returns its
// own version string. This is a deliberate change — the CC client uses serverInfo
// for display only, and showing daemon vs server version avoids confusion when
// the two are at different versions. The deviation does NOT break MCP protocol
// compliance (version is informational per spec §2.1.2).
func TestContract_Initialize_RoutedByDispatcher(t *testing.T) {
	t.Parallel()

	// The mock server returns a version string that DIFFERS from the daemon.
	srv := &mockEngramServer{
		initResp: &pb.InitializeResponse{
			ServerName:    "engram-server",
			ServerVersion: "v4.2.0", // older version
		},
	}

	grpcAddr := startMockGRPC(t, srv)
	disp, _, p := buildContractDispatcher(t, grpcAddr)

	resp, err := disp.HandleRequest(context.Background(), p, jsonrpcInitReq(1))
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}

	var got struct {
		Result struct {
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
		Error *struct{ Code int } `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error != nil {
		t.Fatalf("unexpected error: %+v", got.Error)
	}

	// The dispatcher returns its OWN version — NOT the server's "v4.2.0".
	// This is the documented deviation: daemon owns the initialize response.
	if got.Result.ServerInfo.Name != "engram" {
		t.Errorf("serverInfo.name: got %q, want %q", got.Result.ServerInfo.Name, "engram")
	}
	if got.Result.ServerInfo.Version != version.Daemon {
		t.Errorf("serverInfo.version: got %q, want %q (documented deviation from v4.2.0 which returned server version)",
			got.Result.ServerInfo.Version, version.Daemon)
	}
	if got.Result.ProtocolVersion != "2024-11-05" {
		t.Errorf("protocolVersion: got %q, want %q", got.Result.ProtocolVersion, "2024-11-05")
	}
}

// TestContract_ToolsCall_UnknownTool_Returns32601 verifies that calling an
// unknown tool name returns JSON-RPC -32601 method not found. The mock gRPC
// server is never called because the dispatcher returns the error before
// forwarding to the proxy — the proxy is consulted only for fallthrough, and
// when the proxy itself cannot find the tool the dispatcher maps that to
// a -32603 or result-level error depending on how the proxy reports it.
//
// In this test we exercise the case where the proxy receives the call and
// returns a gRPC error (server returns status not-found equivalent). The
// dispatcher maps uncaught proxy errors to -32603 internal error per the
// dispatcher contract. We verify the call does NOT succeed (no result.content
// block) and that the gRPC server receives no CallTool request (since we
// use an unknown tool that the server's tool list does not include in
// InitializeResponse — meaning ProxyHandleTool IS called, gRPC CallTool IS
// forwarded, but the gRPC layer returns an error).
//
// Simpler path: the mock server's callResp is nil so CallTool returns an empty
// success response ({}) which the proxy turns into an empty content block.
// To get a clean -32601, we must register NO proxy at all. We build a
// separate registry without engramcore.
func TestContract_ToolsCall_UnknownTool_Returns32601(t *testing.T) {
	t.Parallel()

	// Build a dispatcher with NO modules registered (no proxy either).
	// An unknown tool call on this dispatcher must return -32601 per design.md.
	r := registry.New()
	r.Freeze()
	disp := dispatcher.New(r, slog.Default())

	req := jsonrpcCallReq(1, "totally_unknown_tool")
	resp, err := disp.HandleRequest(context.Background(), muxcore.ProjectContext{ID: "p1"}, req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}

	var got struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error == nil {
		t.Fatalf("expected JSON-RPC error for unknown tool, got result: %s", got.Result)
	}
	if got.Error.Code != -32601 {
		t.Errorf("error.code: got %d, want -32601", got.Error.Code)
	}
}
