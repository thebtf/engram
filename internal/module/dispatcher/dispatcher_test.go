package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/registry"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// ---------------------------------------------------------------------------
// Fake modules
// ---------------------------------------------------------------------------

type fakeMod struct {
	name        string
	tools       []module.ToolDef
	handleFn    func(ctx context.Context, p muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error)
	callsToTool *int32 // atomic
}

func (f *fakeMod) Name() string                                      { return f.name }
func (f *fakeMod) Init(_ context.Context, _ module.ModuleDeps) error { return nil }
func (f *fakeMod) Shutdown(_ context.Context) error                  { return nil }
func (f *fakeMod) Tools() []module.ToolDef                           { return f.tools }
func (f *fakeMod) HandleTool(ctx context.Context, p muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error) {
	if f.callsToTool != nil {
		atomic.AddInt32(f.callsToTool, 1)
	}
	if f.handleFn != nil {
		return f.handleFn(ctx, p, name, args)
	}
	return json.RawMessage(`"ok"`), nil
}

// panicMod panics inside HandleTool.
type panicMod struct {
	name    string
	tools   []module.ToolDef
	message string
}

func (f *panicMod) Name() string                                      { return f.name }
func (f *panicMod) Init(_ context.Context, _ module.ModuleDeps) error { return nil }
func (f *panicMod) Shutdown(_ context.Context) error                  { return nil }
func (f *panicMod) Tools() []module.ToolDef                           { return f.tools }
func (f *panicMod) HandleTool(_ context.Context, _ muxcore.ProjectContext, _ string, _ json.RawMessage) (json.RawMessage, error) {
	panic(f.message)
}

// slowMod blocks in HandleTool until ctx is cancelled or 35 s elapses.
type slowMod struct {
	name  string
	tools []module.ToolDef
}

func (f *slowMod) Name() string                                      { return f.name }
func (f *slowMod) Init(_ context.Context, _ module.ModuleDeps) error { return nil }
func (f *slowMod) Shutdown(_ context.Context) error                  { return nil }
func (f *slowMod) Tools() []module.ToolDef                           { return f.tools }
func (f *slowMod) HandleTool(ctx context.Context, _ muxcore.ProjectContext, _ string, _ json.RawMessage) (json.RawMessage, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(35 * time.Second):
		return json.RawMessage(`"late"`), nil
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func buildDispatcher(t *testing.T, mods ...module.EngramModule) *Dispatcher {
	t.Helper()
	r := registry.New()
	for _, m := range mods {
		if err := r.Register(m); err != nil {
			t.Fatalf("Register %q: %v", m.Name(), err)
		}
	}
	r.Freeze()
	return New(r, slog.Default())
}

func projectCtx(id string) muxcore.ProjectContext {
	return muxcore.ProjectContext{ID: id, Cwd: "/fake/" + id}
}

func jsonrpcReq(id int, method string, params any) []byte {
	type req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	r := req{JSONRPC: "2.0", ID: id, Method: method}
	if params != nil {
		b, _ := json.Marshal(params)
		r.Params = b
	}
	b, _ := json.Marshal(r)
	return b
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func parseResp(t *testing.T, b []byte) rpcResp {
	t.Helper()
	var r rpcResp
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("parseResp: %v (input: %s)", err, b)
	}
	return r
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestHandleRequest_Initialize_ReturnsCorrectServerInfo(t *testing.T) {
	d := buildDispatcher(t)
	req := jsonrpcReq(1, "initialize", nil)
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	r := parseResp(t, resp)
	if r.Error != nil {
		t.Fatalf("unexpected error: %+v", r.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	si, _ := result["serverInfo"].(map[string]any)
	if si["name"] != "engram" {
		t.Errorf("serverInfo.name: got %v, want engram", si["name"])
	}
	if si["version"] != "v4.3.0" {
		t.Errorf("serverInfo.version: got %v, want v4.3.0", si["version"])
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion: got %v, want 2024-11-05", result["protocolVersion"])
	}
}

func TestHandleRequest_Ping_ReturnsEmptyResult(t *testing.T) {
	d := buildDispatcher(t)
	req := jsonrpcReq(2, "ping", nil)
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	r := parseResp(t, resp)
	if r.Error != nil {
		t.Fatalf("unexpected error: %+v", r.Error)
	}
}

func TestHandleRequest_NotificationsCancelled_ReturnsNil(t *testing.T) {
	d := buildDispatcher(t)
	req := jsonrpcReq(3, "notifications/cancelled", nil)
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response for notification, got: %s", resp)
	}
}

func TestHandleRequest_ToolsList_AggregatesFrom3Modules(t *testing.T) {
	m1 := &fakeMod{name: "m1", tools: []module.ToolDef{{Name: "m1.a"}, {Name: "m1.b"}}}
	m2 := &fakeMod{name: "m2", tools: []module.ToolDef{{Name: "m2.a"}, {Name: "m2.b"}}}
	m3 := &fakeMod{name: "m3", tools: []module.ToolDef{{Name: "m3.a"}, {Name: "m3.b"}}}

	d := buildDispatcher(t, m1, m2, m3)
	req := jsonrpcReq(4, "tools/list", nil)
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	r := parseResp(t, resp)
	if r.Error != nil {
		t.Fatalf("unexpected error: %+v", r.Error)
	}
	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatalf("unmarshal tools: %v", err)
	}
	if len(result.Tools) != 6 {
		t.Fatalf("tools count: got %d, want 6", len(result.Tools))
	}
	// Verify registration order is preserved.
	wantNames := []string{"m1.a", "m1.b", "m2.a", "m2.b", "m3.a", "m3.b"}
	for i, w := range wantNames {
		if result.Tools[i].Name != w {
			t.Errorf("tool[%d].name: got %q, want %q", i, result.Tools[i].Name, w)
		}
	}
}

func TestHandleRequest_ToolsCall_RoutesToCorrectModule(t *testing.T) {
	var callsA, callsB int32
	m1 := &fakeMod{
		name:        "m1",
		tools:       []module.ToolDef{{Name: "m1.ping"}},
		callsToTool: &callsA,
	}
	m2 := &fakeMod{
		name:        "m2",
		tools:       []module.ToolDef{{Name: "m2.ping"}},
		callsToTool: &callsB,
	}

	d := buildDispatcher(t, m1, m2)
	req := jsonrpcReq(5, "tools/call", map[string]any{"name": "m2.ping", "arguments": map[string]any{}})
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	r := parseResp(t, resp)
	if r.Error != nil {
		t.Fatalf("unexpected error: %+v", r.Error)
	}
	if atomic.LoadInt32(&callsA) != 0 {
		t.Errorf("m1 should not have been called")
	}
	if atomic.LoadInt32(&callsB) != 1 {
		t.Errorf("m2 should have been called exactly once, got %d", callsB)
	}
}

func TestHandleRequest_UnknownTool_Returns32601(t *testing.T) {
	d := buildDispatcher(t)
	req := jsonrpcReq(6, "tools/call", map[string]any{"name": "nonexistent", "arguments": map[string]any{}})
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	r := parseResp(t, resp)
	if r.Error == nil {
		t.Fatal("expected JSON-RPC error, got nil")
	}
	if r.Error.Code != -32601 {
		t.Errorf("error code: got %d, want -32601", r.Error.Code)
	}
}

func TestHandleRequest_UnknownMethod_Returns32601(t *testing.T) {
	d := buildDispatcher(t)
	req := jsonrpcReq(7, "unknown/method", nil)
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	r := parseResp(t, resp)
	if r.Error == nil || r.Error.Code != -32601 {
		t.Errorf("expected -32601, got: %+v", r.Error)
	}
}

func TestHandleRequest_PanicIsolation_OtherSessionsUnaffected(t *testing.T) {
	panicker := &panicMod{
		name:    "panicker",
		tools:   []module.ToolDef{{Name: "bad.tool"}},
		message: "deliberate panic",
	}
	healthy := &fakeMod{
		name:  "healthy",
		tools: []module.ToolDef{{Name: "good.tool"}},
	}
	d := buildDispatcher(t, panicker, healthy)

	var wg sync.WaitGroup
	var panicErrors int32
	var healthyOK int32

	// 1 goroutine calls the panicking tool.
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := jsonrpcReq(100, "tools/call", map[string]any{"name": "bad.tool", "arguments": map[string]any{}})
		resp, _ := d.HandleRequest(context.Background(), projectCtx("panic-sess"), req)
		r := parseResp(t, resp)
		if r.Error != nil && r.Error.Code == -32603 {
			atomic.AddInt32(&panicErrors, 1)
		}
	}()

	// 9 goroutines call the healthy tool.
	for i := 0; i < 9; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := jsonrpcReq(200+i, "tools/call", map[string]any{"name": "good.tool", "arguments": map[string]any{}})
			resp, _ := d.HandleRequest(context.Background(), projectCtx(fmt.Sprintf("h%d", i)), req)
			r := parseResp(t, resp)
			if r.Error == nil {
				atomic.AddInt32(&healthyOK, 1)
			}
		}(i)
	}
	wg.Wait()

	if atomic.LoadInt32(&panicErrors) != 1 {
		t.Errorf("panic goroutine: expected 1 -32603, got %d", panicErrors)
	}
	if atomic.LoadInt32(&healthyOK) != 9 {
		t.Errorf("healthy goroutines: expected 9 successes, got %d", healthyOK)
	}
}

func TestHandleRequest_ToolTimeout_Returns32603(t *testing.T) {
	slow := &slowMod{
		name:  "slow",
		tools: []module.ToolDef{{Name: "slow.tool"}},
	}
	// Override the timeout to 200 ms for a fast test.
	d := buildDispatcher(t, slow)

	// We override defaultToolTimeout at package level for the duration of
	// this test. Since the test is in the same package, we can use a context
	// with the short deadline directly instead.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	req := jsonrpcReq(300, "tools/call", map[string]any{"name": "slow.tool", "arguments": map[string]any{}})
	start := time.Now()
	resp, err := d.HandleRequest(ctx, projectCtx("p1"), req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	// Must return before the 35 s module sleep.
	if elapsed > 5*time.Second {
		t.Errorf("HandleRequest took %v; want < 5s", elapsed)
	}
	r := parseResp(t, resp)
	if r.Error == nil {
		t.Fatal("expected JSON-RPC error for timeout, got nil")
	}
	if r.Error.Code != -32603 {
		t.Errorf("error code: got %d, want -32603", r.Error.Code)
	}
}

func TestHandleRequest_100ConcurrentCalls_NoDataRaces(t *testing.T) {
	var calls int32
	m := &fakeMod{
		name:        "concurrent",
		tools:       []module.ToolDef{{Name: "c.noop"}},
		callsToTool: &calls,
	}
	d := buildDispatcher(t, m)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := jsonrpcReq(i, "tools/call", map[string]any{"name": "c.noop", "arguments": map[string]any{}})
			resp, err := d.HandleRequest(context.Background(), projectCtx(fmt.Sprintf("p%d", i)), req)
			if err != nil {
				t.Errorf("goroutine %d: HandleRequest error: %v", i, err)
				return
			}
			r := parseResp(t, resp)
			if r.Error != nil {
				t.Errorf("goroutine %d: unexpected error: %+v", i, r.Error)
			}
		}(i)
	}
	wg.Wait()

	if n := atomic.LoadInt32(&calls); n != 100 {
		t.Errorf("expected 100 calls, got %d", n)
	}
}

func TestHandleRequest_ModuleError_ReturnedAsResultNotProtocolError(t *testing.T) {
	modErr := module.ErrProjectNotFound("proj-abc")
	m := &fakeMod{
		name:  "errmod",
		tools: []module.ToolDef{{Name: "err.tool"}},
		handleFn: func(_ context.Context, _ muxcore.ProjectContext, _ string, _ json.RawMessage) (json.RawMessage, error) {
			return nil, modErr
		},
	}
	d := buildDispatcher(t, m)
	req := jsonrpcReq(400, "tools/call", map[string]any{"name": "err.tool", "arguments": map[string]any{}})
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	r := parseResp(t, resp)
	// Must be a successful JSON-RPC response (no protocol error).
	if r.Error != nil {
		t.Fatalf("expected result-level error, got protocol error: %+v", r.Error)
	}
	// Result must contain isError=true.
	var result map[string]any
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["isError"] != true {
		t.Errorf("result.isError: got %v, want true", result["isError"])
	}
}

func TestHandleRequest_ParseError_Returns32700(t *testing.T) {
	d := buildDispatcher(t)
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), []byte(`not json`))
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	r := parseResp(t, resp)
	if r.Error == nil || r.Error.Code != -32700 {
		t.Errorf("expected -32700, got: %+v", r.Error)
	}
}

// ---------------------------------------------------------------------------
// ConnectedProjectIDs — Option A accessor for serverevents bridge heartbeat
// ---------------------------------------------------------------------------

// TestConnectedProjectIDs_EmptyTracker verifies the initial state: no
// sessions connected yet → empty slice.
func TestConnectedProjectIDs_EmptyTracker(t *testing.T) {
	d := buildDispatcher(t)
	ids := d.ConnectedProjectIDs()
	if len(ids) != 0 {
		t.Errorf("expected empty, got %v", ids)
	}
}

// TestConnectedProjectIDs_AfterConnect verifies that OnProjectConnect adds
// the project ID to the snapshot returned by ConnectedProjectIDs.
func TestConnectedProjectIDs_AfterConnect(t *testing.T) {
	d := buildDispatcher(t)
	d.OnProjectConnect(projectCtx("proj-alpha"))
	d.OnProjectConnect(projectCtx("proj-beta"))

	ids := d.ConnectedProjectIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 tracked IDs, got %d (%v)", len(ids), ids)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got["proj-alpha"] || !got["proj-beta"] {
		t.Errorf("missing expected IDs in %v", ids)
	}
}

// TestConnectedProjectIDs_AfterDisconnect verifies that OnProjectDisconnect
// removes the project ID from the snapshot.
func TestConnectedProjectIDs_AfterDisconnect(t *testing.T) {
	d := buildDispatcher(t)
	d.OnProjectConnect(projectCtx("proj-a"))
	d.OnProjectConnect(projectCtx("proj-b"))
	d.OnProjectDisconnect("proj-a")

	ids := d.ConnectedProjectIDs()
	if len(ids) != 1 {
		t.Fatalf("expected 1 tracked ID, got %d (%v)", len(ids), ids)
	}
	if ids[0] != "proj-b" {
		t.Errorf("expected proj-b, got %s", ids[0])
	}
}

// TestConnectedProjectIDs_Snapshot verifies the returned slice is a
// snapshot — mutations to the underlying tracked map after the call must
// not affect the already-returned slice.
func TestConnectedProjectIDs_Snapshot(t *testing.T) {
	d := buildDispatcher(t)
	d.OnProjectConnect(projectCtx("proj-x"))

	snapshot := d.ConnectedProjectIDs()
	d.OnProjectDisconnect("proj-x") // mutate after snapshot

	if len(snapshot) != 1 || snapshot[0] != "proj-x" {
		t.Errorf("snapshot was affected by post-call mutation: %v", snapshot)
	}
}
