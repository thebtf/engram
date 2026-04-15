package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/module"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// ---------------------------------------------------------------------------
// Fakes for ProxyToolProvider tests
// ---------------------------------------------------------------------------

// proxyMod implements EngramModule + module.ProxyToolProvider. It backs the
// FR-11a dispatcher tests. The module has NO static Tools() method so the
// registry caches only ProxyTool in its entry.
type proxyMod struct {
	name       string
	proxyTools []module.ToolDef
	proxyErr   error // if non-nil, ProxyTools returns this

	// handleFn lets individual tests inject per-call behaviour.
	handleFn func(ctx context.Context, p muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error)

	// Call counters (atomic).
	proxyToolsCalls  int32
	handleToolsCalls int32
}

func (f *proxyMod) Name() string                                       { return f.name }
func (f *proxyMod) Init(_ context.Context, _ module.ModuleDeps) error  { return nil }
func (f *proxyMod) Shutdown(_ context.Context) error                   { return nil }

func (f *proxyMod) ProxyTools(_ context.Context, _ muxcore.ProjectContext) ([]module.ToolDef, error) {
	atomic.AddInt32(&f.proxyToolsCalls, 1)
	if f.proxyErr != nil {
		return nil, f.proxyErr
	}
	return f.proxyTools, nil
}

func (f *proxyMod) ProxyHandleTool(ctx context.Context, p muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error) {
	atomic.AddInt32(&f.handleToolsCalls, 1)
	if f.handleFn != nil {
		return f.handleFn(ctx, p, name, args)
	}
	return json.RawMessage(`"proxied-ok"`), nil
}

// ---------------------------------------------------------------------------
// tools/list tests
// ---------------------------------------------------------------------------

// TestHandleToolsList_ProxyOnly verifies that a registry with only a
// ProxyToolProvider returns the proxy's dynamic tool list via tools/list.
func TestHandleToolsList_ProxyOnly(t *testing.T) {
	t.Parallel()

	proxy := &proxyMod{
		name: "engramcore",
		proxyTools: []module.ToolDef{
			{Name: "memory_store", Description: "store memory"},
			{Name: "memory_search", Description: "search memory"},
		},
	}
	d := buildDispatcher(t, proxy)

	req := jsonrpcReq(1, "tools/list", nil)
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
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result.Tools))
	}
	if result.Tools[0].Name != "memory_store" || result.Tools[1].Name != "memory_search" {
		t.Errorf("unexpected tool names: %+v", result.Tools)
	}
	if got := atomic.LoadInt32(&proxy.proxyToolsCalls); got != 1 {
		t.Errorf("ProxyTools call count: got %d, want 1", got)
	}
}

// TestHandleToolsList_StaticAndProxy_MergedInOrder verifies that a registry
// with both static ToolProvider AND a ProxyToolProvider returns the merged
// tool list: static tools first (in registration order), then proxy tools.
func TestHandleToolsList_StaticAndProxy_MergedInOrder(t *testing.T) {
	t.Parallel()

	staticMod := &fakeMod{
		name: "staticmod",
		tools: []module.ToolDef{
			{Name: "static.ping", Description: "ping"},
			{Name: "static.echo", Description: "echo"},
		},
	}
	proxy := &proxyMod{
		name: "proxymod",
		proxyTools: []module.ToolDef{
			{Name: "proxy.fetch", Description: "fetch"},
		},
	}
	d := buildDispatcher(t, staticMod, proxy)

	req := jsonrpcReq(1, "tools/list", nil)
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
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d: %+v", len(result.Tools), result.Tools)
	}
	want := []string{"static.ping", "static.echo", "proxy.fetch"}
	for i, w := range want {
		if result.Tools[i].Name != w {
			t.Errorf("tool[%d]: got %q, want %q", i, result.Tools[i].Name, w)
		}
	}
}

// TestHandleToolsList_ProxyError_GracefulDegradation verifies FR-11a graceful
// degradation: when ProxyTools returns an error, tools/list returns ONLY the
// static tools and does NOT surface an error to the client. A network blip
// MUST NOT break tools/list.
func TestHandleToolsList_ProxyError_GracefulDegradation(t *testing.T) {
	t.Parallel()

	staticMod := &fakeMod{
		name: "staticmod",
		tools: []module.ToolDef{
			{Name: "static.ping", Description: "ping"},
		},
	}
	proxy := &proxyMod{
		name:     "proxymod",
		proxyErr: errors.New("backend unreachable"),
	}
	d := buildDispatcher(t, staticMod, proxy)

	req := jsonrpcReq(1, "tools/list", nil)
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	r := parseResp(t, resp)
	// Must NOT be a JSON-RPC error — proxy error is logged and swallowed.
	if r.Error != nil {
		t.Fatalf("tools/list must not propagate proxy error as JSON-RPC error, got %+v", r.Error)
	}

	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Tools) != 1 {
		t.Fatalf("expected 1 static tool after graceful degradation, got %d", len(result.Tools))
	}
	if result.Tools[0].Name != "static.ping" {
		t.Errorf("expected static.ping, got %q", result.Tools[0].Name)
	}
	if got := atomic.LoadInt32(&proxy.proxyToolsCalls); got != 1 {
		t.Errorf("ProxyTools should have been called once, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// tools/call tests
// ---------------------------------------------------------------------------

// TestHandleToolsCall_FallsThroughToProxy verifies that a tools/call for a
// tool NOT present in any static ToolProvider is forwarded to
// ProxyHandleTool.
func TestHandleToolsCall_FallsThroughToProxy(t *testing.T) {
	t.Parallel()

	proxy := &proxyMod{
		name: "engramcore",
		proxyTools: []module.ToolDef{
			{Name: "memory_store", Description: "store"},
		},
		handleFn: func(_ context.Context, _ muxcore.ProjectContext, name string, _ json.RawMessage) (json.RawMessage, error) {
			if name != "memory_store" {
				return nil, errors.New("unexpected tool: " + name)
			}
			return json.RawMessage(`{"stored": true}`), nil
		},
	}
	d := buildDispatcher(t, proxy)

	req := jsonrpcReq(1, "tools/call", map[string]any{
		"name":      "memory_store",
		"arguments": map[string]any{"content": "hello"},
	})
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	r := parseResp(t, resp)
	if r.Error != nil {
		t.Fatalf("unexpected error: %+v", r.Error)
	}

	var result struct {
		Content []json.RawMessage `json:"content"`
		IsError bool              `json:"isError"`
	}
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.IsError {
		t.Errorf("isError must be false for successful proxy call")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content entry, got %d", len(result.Content))
	}
	var content map[string]any
	if err := json.Unmarshal(result.Content[0], &content); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if stored, _ := content["stored"].(bool); !stored {
		t.Errorf("content.stored: want true, got %v", content["stored"])
	}
	if got := atomic.LoadInt32(&proxy.handleToolsCalls); got != 1 {
		t.Errorf("ProxyHandleTool call count: got %d, want 1", got)
	}
}

// TestHandleToolsCall_StaticWinsOverProxy verifies that when a tool is
// declared by BOTH a static ToolProvider and appears to be proxyable, the
// static path wins. This guarantees zero routing ambiguity: static lookup
// is O(1) hash hit first, proxy is only consulted on fallthrough.
func TestHandleToolsCall_StaticWinsOverProxy(t *testing.T) {
	t.Parallel()

	var proxyCalls int32
	var staticCalls int32

	staticMod := &fakeMod{
		name:        "staticmod",
		tools:       []module.ToolDef{{Name: "shared.tool"}},
		callsToTool: &staticCalls,
	}
	proxy := &proxyMod{
		name: "proxymod",
		handleFn: func(_ context.Context, _ muxcore.ProjectContext, _ string, _ json.RawMessage) (json.RawMessage, error) {
			atomic.AddInt32(&proxyCalls, 1)
			return nil, errors.New("proxy must not be called when static matches")
		},
	}
	d := buildDispatcher(t, staticMod, proxy)

	req := jsonrpcReq(1, "tools/call", map[string]any{
		"name":      "shared.tool",
		"arguments": map[string]any{},
	})
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	r := parseResp(t, resp)
	if r.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %+v", r.Error)
	}

	if got := atomic.LoadInt32(&staticCalls); got != 1 {
		t.Errorf("static HandleTool calls: got %d, want 1", got)
	}
	if got := atomic.LoadInt32(&proxyCalls); got != 0 {
		t.Errorf("proxy HandleTool must NOT be called when static matches: got %d", got)
	}
	// Proxy counters
	if got := atomic.LoadInt32(&proxy.handleToolsCalls); got != 0 {
		t.Errorf("proxyMod.handleToolsCalls must be 0, got %d", got)
	}
}

// TestHandleToolsCall_NoProxy_ReturnsMethodNotFound verifies that when no
// ProxyToolProvider is registered, a tools/call for an unknown tool still
// returns JSON-RPC -32601 method not found.
func TestHandleToolsCall_NoProxy_ReturnsMethodNotFound(t *testing.T) {
	t.Parallel()

	staticMod := &fakeMod{
		name:  "staticmod",
		tools: []module.ToolDef{{Name: "static.ping"}},
	}
	d := buildDispatcher(t, staticMod)

	req := jsonrpcReq(1, "tools/call", map[string]any{
		"name":      "does.not.exist",
		"arguments": map[string]any{},
	})
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	r := parseResp(t, resp)
	if r.Error == nil {
		t.Fatalf("expected -32601 error, got result %s", r.Result)
	}
	if r.Error.Code != -32601 {
		t.Errorf("error code: got %d, want -32601", r.Error.Code)
	}
}

// TestHandleToolsCall_ProxyModuleError_ReturnsResultLevelError verifies that
// when ProxyHandleTool returns a *module.ModuleError, it travels through the
// same FR-12 result-level path as a static ToolProvider would — isError:true
// in the content envelope, NOT a JSON-RPC -32xxx error.
func TestHandleToolsCall_ProxyModuleError_ReturnsResultLevelError(t *testing.T) {
	t.Parallel()

	proxy := &proxyMod{
		name: "proxymod",
		handleFn: func(_ context.Context, _ muxcore.ProjectContext, _ string, _ json.RawMessage) (json.RawMessage, error) {
			return nil, module.ErrNotReady("warming up", 500*time.Millisecond)
		},
	}
	d := buildDispatcher(t, proxy)

	req := jsonrpcReq(1, "tools/call", map[string]any{
		"name":      "any.tool",
		"arguments": map[string]any{},
	})
	resp, err := d.HandleRequest(context.Background(), projectCtx("p1"), req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	r := parseResp(t, resp)
	if r.Error != nil {
		t.Fatalf("ModuleError from proxy must travel as result-level, not JSON-RPC error: %+v", r.Error)
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !result.IsError {
		t.Errorf("isError must be true for ModuleError from proxy")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content entry, got %d", len(result.Content))
	}
	if result.Content[0].Type != "text" {
		t.Errorf("content type: got %q, want text", result.Content[0].Type)
	}
}
