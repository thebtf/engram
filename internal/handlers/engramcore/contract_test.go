package engramcore

// contract_test.go verifies tools/list and tools/call envelopes against the
// backend response, together with transport cancellation and context isolation.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/config"
	loomhandler "github.com/thebtf/engram/internal/handlers/loom"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/dispatcher"
	"github.com/thebtf/engram/internal/module/lifecycle"
	"github.com/thebtf/engram/internal/module/registry"
	"github.com/thebtf/engram/internal/uci"
	"github.com/thebtf/engram/internal/version"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
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
	callErr      error
	initReq      *pb.InitializeRequest
	callReq      *pb.CallToolRequest
	callMetadata metadata.MD
	initCalls    int
}

func (s *mockEngramServer) Initialize(ctx context.Context, req *pb.InitializeRequest) (*pb.InitializeResponse, error) {
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

func (s *mockEngramServer) CallTool(ctx context.Context, req *pb.CallToolRequest) (*pb.CallToolResponse, error) {
	s.mu.Lock()
	s.callReq = req
	s.callMetadata, _ = metadata.FromIncomingContext(ctx)
	s.mu.Unlock()
	if s.callErr != nil {
		return nil, s.callErr
	}
	if s.callResp == nil {
		return &pb.CallToolResponse{}, nil
	}
	return s.callResp, nil
}

// startMockGRPC starts a mock gRPC server on an ephemeral port, waits for its
// transport handshake, and returns the listener address ("host:port"). The
// server and readiness probe are registered for cleanup via t.Cleanup.
func startMockGRPC(t *testing.T, srv pb.EngramServiceServer) string {
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

	readyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	readyConn, err := grpc.DialContext(readyCtx, lis.Addr().String(), grpc.WithBlock(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("wait for mock gRPC readiness: %v", err)
	}
	t.Cleanup(func() { _ = readyConn.Close() })
	return lis.Addr().String()
}

// startDeferredMockGRPC starts a mock server immediately, but holds the first
// accepted TCP connection until release observes the client's connection
// attempt. This exercises the production dial path without scheduler delays.
func startDeferredMockGRPC(t *testing.T, srv *mockEngramServer) (string, func() error, <-chan struct{}) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	acceptStarted := make(chan struct{})
	releaseAccept := make(chan struct{})
	var releaseOnce sync.Once
	delayedLis := &delayedAcceptListener{
		Listener:      lis,
		acceptStarted: acceptStarted,
		release:       releaseAccept,
	}

	gs := grpc.NewServer()
	pb.RegisterEngramServiceServer(gs, srv)
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = gs.Serve(delayedLis)
	}()

	release := func() error {
		select {
		case <-acceptStarted:
			releaseOnce.Do(func() { close(releaseAccept) })
			return nil
		case <-time.After(5 * time.Second):
			return fmt.Errorf("wait for client TCP connection")
		}
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseAccept) })
		gs.Stop()
		_ = lis.Close()
		select {
		case <-serveDone:
		case <-time.After(5 * time.Second):
			t.Error("mock gRPC Serve did not stop")
		}
	})
	return lis.Addr().String(), release, acceptStarted
}

type delayedAcceptListener struct {
	net.Listener
	acceptStarted chan struct{}
	release       <-chan struct{}
	acceptOnce    sync.Once
}

func (l *delayedAcceptListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.acceptOnce.Do(func() { close(l.acceptStarted) })
	<-l.release
	return conn, nil
}

type discoveryNotifier struct{ notifications chan string }

func (n *discoveryNotifier) Notify(projectID string, notification []byte) error {
	if string(notification) == `{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}` {
		n.notifications <- projectID
	}
	return nil
}

func (n *discoveryNotifier) Broadcast([]byte) {}

func assertLocalCatalog(t *testing.T, response []byte) {
	t.Helper()
	if !strings.Contains(string(response), `"name":"recall"`) || !strings.Contains(string(response), `"name":"vault"`) || !strings.Contains(string(response), "remote support and authorization are unverified") {
		t.Fatalf("local catalog missing or not candid: %s", response)
	}
}

// ---------------------------------------------------------------------------
// Dispatcher bootstrap helpers for contract tests
// ---------------------------------------------------------------------------

// buildContractDispatcher injects a local backend without requiring project setup.
func buildContractDispatcher(t *testing.T, grpcAddr string, staticModules ...module.EngramModule) (*dispatcher.Dispatcher, *Module, muxcore.ProjectContext) {
	return buildContractDispatcherWithClientInstanceID(t, grpcAddr, "", staticModules...)
}

// buildV3ContractDispatcher supplies the stable installed-client ID used by UCI.
func buildV3ContractDispatcher(t *testing.T, grpcAddr string, staticModules ...module.EngramModule) (*dispatcher.Dispatcher, *Module, muxcore.ProjectContext) {
	return buildContractDispatcherWithClientInstanceID(t, grpcAddr, "fixture-daemon-install", staticModules...)
}

func buildContractDispatcherWithClientInstanceID(t *testing.T, grpcAddr, clientInstanceID string, staticModules ...module.EngramModule) (*dispatcher.Dispatcher, *Module, muxcore.ProjectContext) {
	t.Helper()

	mod := NewModuleWithClientInstanceID(clientInstanceID)

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

	if grpcAddr == "" {
		return disp, mod, p
	}

	// Pass http:// prefix so getOrDialGRPC uses plaintext.
	p.Env["ENGRAM_URL"] = "http://" + grpcAddr

	// Pre-populate the pool with a connected plaintext transport matching our
	// mock; grpc.NewClient alone defers its handshake until the assertion RPC.
	readyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(
		readyCtx,
		grpcAddr,
		grpc.WithBlock(),
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
		t.Fatalf("connect to mock gRPC server: %v", err)
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

func TestContract_ToolsList_ConfiguredInitializeFailureReturnsLocalCatalog(t *testing.T) {
	srv := &mockEngramServer{initErr: status.Error(codes.Unavailable, "backend starting")}
	grpcAddr := startMockGRPC(t, srv)
	disp, _, p := buildContractDispatcher(t, grpcAddr)

	resp, err := disp.HandleRequest(context.Background(), p, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	assertLocalCatalog(t, resp)
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
	assertLocalCatalog(t, resp)
}

func TestContract_ToolsList_NonblockingRecoveryAndCancellation(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"content":{"type":"string"}}}`)
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{{Name: "memory_store", Description: "store", InputSchemaJson: schema}}}}
	addr, release, _ := startDeferredMockGRPC(t, srv)
	disp, _, project := buildContractDispatcher(t, "")
	notifier := &discoveryNotifier{notifications: make(chan string, 2)}
	disp.SetNotifier(notifier)
	project.Env[config.EnvServerURL] = "http://" + addr
	response, err := disp.HandleRequest(t.Context(), project, jsonrpcListReq(1))
	if err != nil {
		t.Fatal(err)
	}
	assertLocalCatalog(t, response)
	if err := release(); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-notifier.notifications:
		if id != project.ID {
			t.Fatalf("notification project=%q", id)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recovered discovery did not notify")
	}
	response, err = disp.HandleRequest(t.Context(), project, jsonrpcListReq(2))
	if err != nil || !strings.Contains(string(response), `"name":"memory_store"`) || !strings.Contains(string(response), `"content":{"type":"string"}`) || !strings.Contains(string(response), "Previously discovered remote schema") {
		t.Fatalf("recovered list lost remote schema: %s (%v)", response, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	response, err = disp.HandleRequest(ctx, project, jsonrpcListReq(3))
	if err != nil {
		t.Fatal(err)
	}
	assertToolsListServiceUnavailable(t, response)
}

func TestContract_ToolsList_ShutdownStopsInflightDiscovery(t *testing.T) {
	srv := &mockEngramServer{}
	addr, release, accepted := startDeferredMockGRPC(t, srv)
	disp, mod, project := buildContractDispatcher(t, "")
	project.Env[config.EnvServerURL] = "http://" + addr
	response, err := disp.HandleRequest(t.Context(), project, jsonrpcListReq(1))
	if err != nil {
		t.Fatal(err)
	}
	assertLocalCatalog(t, response)
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("background discovery did not connect")
	}
	var pooled *grpc.ClientConn
	connections := 0
	mod.pool.conns.Range(func(_, value any) bool {
		connections++
		pooled = value.(*grpc.ClientConn)
		return true
	})
	if connections != 1 || pooled == nil {
		t.Fatalf("discovery pooled %d connections, want one", connections)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- mod.Shutdown(context.Background()) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not stop in-flight discovery")
	}
	if pooled.GetState() != connectivity.Shutdown {
		t.Fatalf("pooled connection remains active: %s", pooled.GetState())
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	observerCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	observer, err := grpc.DialContext(observerCtx, addr, grpc.WithBlock(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("verify recovered listener: %v", err)
	}
	_ = observer.Close()
	if _, err := mod.ProxyTools(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	mod.refreshMu.Lock()
	active := len(mod.refreshing)
	mod.refreshMu.Unlock()
	srv.mu.Lock()
	initializes := srv.initCalls
	srv.mu.Unlock()
	if active != 0 || initializes != 0 || pooled.GetState() != connectivity.Shutdown {
		t.Fatalf("discovery restarted after shutdown: active=%d initializes=%d state=%s", active, initializes, pooled.GetState())
	}
}

// The live catalog preserves the backend schema while marking availability unverified.
func TestContract_ToolsList_PreservesDiscoveredSchemas(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"content":{"type":"string"}},"required":["content"]}`)
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{{Name: "memory_store", Description: "Store a memory observation", InputSchemaJson: schema}}}}
	disp, _, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	notifier := &discoveryNotifier{notifications: make(chan string, 1)}
	disp.SetNotifier(notifier)
	if _, err := disp.HandleRequest(t.Context(), project, jsonrpcListReq(1)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notifier.notifications:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not complete")
	}
	response, err := disp.HandleRequest(t.Context(), project, jsonrpcListReq(2))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Result struct {
			Tools []struct {
				Name, Description string
				InputSchema       json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		t.Fatal(err)
	}
	for _, tool := range result.Result.Tools {
		if tool.Name == "memory_store" {
			if !strings.HasSuffix(tool.Description, "Store a memory observation") {
				t.Fatalf("description=%q", tool.Description)
			}
			assertJSONEqual(t, "remote schema", tool.InputSchema, schema)
			return
		}
	}
	t.Fatalf("remote tool missing: %s", response)
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

func TestProxyHandleToolUsesTransportTagForUCITools(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	registrationRoot := daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	project.Env[config.EnvClaudeSessionID] = "host-session-must-not-be-used"
	project.Cwd = "untrusted-cwd-must-not-be-inspected"
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	ctx := auditcontext.WithUCITransportSession(context.Background(), "transport-tag-a")

	_, err := mod.ProxyHandleTool(ctx, project, "codebase_context", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("UCI proxy call: %v", err)
	}
	srv.mu.Lock()
	request := srv.callReq
	metadata := srv.callMetadata.Copy()
	srv.mu.Unlock()
	if request == nil || request.GetSessionId() != "transport-tag-a" {
		t.Fatalf("UCI CallTool session ID = %#v, want transport tag", request)
	}
	if request.GetProject() != "" {
		t.Fatalf("UCI CallTool carried project-derived authority: %#v", request)
	}
	if got := metadata.Get(auditcontext.SourceSessionMetadataKey); len(got) != 1 || got[0] != "transport-tag-a" {
		t.Fatalf("UCI CallTool source metadata = %v, want transport tag", got)
	}
	if got := metadata.Get(uci.NoAuthCodeClientInstanceMetadataKey); len(got) != 1 || got[0] != "fixture-daemon-install" {
		t.Fatalf("UCI CallTool client instance metadata = %v", got)
	}
	if got := metadata.Get("x-engram-verified-parser-bundle"); len(got) != 0 {
		t.Fatalf("unverified client sent parser proof: %v", got)
	}

	_, err = mod.ProxyHandleTool(context.Background(), project, "codebase_context", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("UCI proxy accepted a request without a transport tag")
	}
	srv.mu.Lock()
	requestAfterMissingTag := srv.callReq
	srv.mu.Unlock()
	if requestAfterMissingTag != request {
		t.Fatal("UCI proxy dispatched a request without a transport tag")
	}
	mod.ConfigureRegistrationParser()
	t.Setenv("PATH", originalPath)
	project.Cwd = registrationRoot
	registerArgs := json.RawMessage(`{"action":"register","source_label":"engram","locator":"file:///private/worktree"}`)
	if _, err := mod.ProxyHandleTool(ctx, project, "codebase_context", registerArgs); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	registered := srv.callReq
	parserMetadata := srv.callMetadata.Copy()
	srv.mu.Unlock()
	if string(registered.GetArgumentsJson()) != string(registerArgs) {
		t.Fatalf("registration arguments mutated: %s", registered.GetArgumentsJson())
	}
	if registered.GetProject() != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("registration canonical project = %q", registered.GetProject())
	}
	if got := parserMetadata.Get(auditcontext.SourceSessionMetadataKey); len(got) != 1 || got[0] != "transport-tag-a" {
		t.Fatalf("registration source session metadata = %v", got)
	}
	if got := parserMetadata.Get(uci.NoAuthCodeClientInstanceMetadataKey); len(got) != 1 || got[0] != "fixture-daemon-install" {
		t.Fatalf("registration client instance metadata = %v", got)
	}
	if got := parserMetadata.Get("x-engram-verified-parser-bundle"); len(got) != 1 || got[0] != string(uci.TreeSitterSemanticContractDigest()) {
		t.Fatalf("verified parser metadata = %v", got)
	}
}

func TestProxyHandleToolSkipsProjectV3ForUCITools(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "project-authority-must-not-be-forwarded"
	project.Cwd = "untrusted-cwd-must-not-be-inspected"
	ctx := auditcontext.WithUCITransportSession(context.Background(), "transport-tag-v3")

	_, err := mod.ProxyHandleTool(ctx, project, "codebase_context", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("UCI V3 proxy call: %v", err)
	}
	srv.mu.Lock()
	request := srv.callReq
	srv.mu.Unlock()
	if request == nil || request.GetSessionId() != "transport-tag-v3" {
		t.Fatalf("UCI V3 CallTool session ID = %#v, want transport tag", request)
	}
	if request.GetProject() != "" {
		t.Fatalf("UCI call carried project authority: request=%#v", request)
	}
}

func TestProxyHandleToolAnchorsEveryUCIToolAndRejectsSpoofedMetadata(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	ctx := auditcontext.WithUCITransportSession(context.Background(), "transport-tag-v3")
	ctx = metadata.AppendToOutgoingContext(ctx, uci.NoAuthCodeClientInstanceMetadataKey, "spoofed", uci.NoAuthCodeClientInstanceMetadataKey, "duplicate")
	for _, name := range []string{"codebase_context", "codebase_index", "codebase_status", "codebase_search", "codebase_read", "codebase_graph"} {
		_, err := mod.ProxyHandleTool(ctx, project, name, json.RawMessage(`{"client_instance_id":"argument-must-not-win"}`))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		srv.mu.Lock()
		request, callMetadata := srv.callReq, srv.callMetadata.Copy()
		srv.mu.Unlock()
		if got := callMetadata.Get(uci.NoAuthCodeClientInstanceMetadataKey); len(got) != 1 || got[0] != mod.v3ClientInstanceID {
			t.Fatalf("%s metadata = %v, want one anchored ID", name, got)
		}
		if request.GetProject() != "" {
			t.Fatalf("%s carried legacy authority: %#v", name, request)
		}
	}
	mod.v3ClientInstanceID = "invalid\r\ninstance"
	srv.mu.Lock()
	previousRequest := srv.callReq
	srv.mu.Unlock()
	if _, err := mod.ProxyHandleTool(ctx, project, "codebase_read", json.RawMessage(`{}`)); err == nil {
		t.Fatal("invalid anchor reached UCI server")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.callReq != previousRequest {
		t.Fatal("invalid anchor dispatched UCI CallTool")
	}
}

func TestProxyHandleToolKeepsHostSessionSeparateFromLogicalProject(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.Env[config.EnvClaudeSessionID] = "host-session"
	writeContractProjectMarker(t, project.Cwd, contractProjectID)
	ctx := auditcontext.WithUCITransportSession(context.Background(), "transport-tag-a")

	_, err := mod.ProxyHandleTool(ctx, project, "store", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("non-UCI proxy call: %v", err)
	}
	srv.mu.Lock()
	request := srv.callReq
	metadata := srv.callMetadata.Copy()
	srv.mu.Unlock()
	if request == nil || request.GetSessionId() != "host-session" {
		t.Fatalf("non-UCI CallTool session ID = %#v, want host session", request)
	}
	if request.GetProject() != contractProjectID {
		t.Fatalf("non-UCI CallTool did not use the logical UUID alone: %#v", request)
	}
	if got := metadata.Get(auditcontext.SourceSessionMetadataKey); len(got) != 0 {
		t.Fatalf("non-UCI CallTool unexpectedly forwarded transport metadata %v", got)
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

func TestHAP01SourceDiagnostic_LocalMCPInitializeDoesNotReachBackend(t *testing.T) {
	t.Parallel()

	srv := &mockEngramServer{initResp: &pb.InitializeResponse{ServerName: "fixture-server", ServerVersion: "fixture-version"}}
	grpcAddr := startMockGRPC(t, srv)
	disp, _, project := buildContractDispatcher(t, grpcAddr)
	response, err := disp.HandleRequest(context.Background(), project, jsonrpcInitReq(1))
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	var projection struct {
		Result struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &projection); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if projection.Result.ServerInfo.Name != "engram" {
		t.Fatalf("local initialize server name = %q, want engram", projection.Result.ServerInfo.Name)
	}
	srv.mu.Lock()
	initCalls := srv.initCalls
	srv.mu.Unlock()
	if initCalls != 0 {
		t.Fatalf("local MCP initialize reached backend %d times", initCalls)
	}
}
