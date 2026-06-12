// Package mcp provides the MCP (Model Context Protocol) server for engram.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// JSON-RPC struct marshaling / unmarshaling
// ---------------------------------------------------------------------------

func TestRequest_Marshal_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		req     Request
		wantKey string
	}{
		{
			name:    "initialize",
			req:     Request{JSONRPC: "2.0", ID: float64(1), Method: "initialize"},
			wantKey: `"method":"initialize"`,
		},
		{
			name:    "string id",
			req:     Request{JSONRPC: "2.0", ID: "req-abc", Method: "tools/list"},
			wantKey: `"id":"req-abc"`,
		},
		{
			name: "with params",
			req: Request{
				JSONRPC: "2.0",
				ID:      float64(2),
				Method:  "tools/call",
				Params:  json.RawMessage(`{"name":"recall"}`),
			},
			wantKey: `"params"`,
		},
		{
			name:    "null id",
			req:     Request{JSONRPC: "2.0", ID: nil, Method: "initialize"},
			wantKey: `"id":null`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(tc.req)
			require.NoError(t, err)
			assert.Contains(t, string(data), tc.wantKey)
		})
	}
}

func TestRequest_Unmarshal_RoundTrip(t *testing.T) {
	t.Parallel()
	raw := `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`
	var req Request
	require.NoError(t, json.Unmarshal([]byte(raw), &req))
	assert.Equal(t, "2.0", req.JSONRPC)
	assert.Equal(t, "tools/list", req.Method)
}

func TestRequest_Unmarshal_NullID(t *testing.T) {
	t.Parallel()
	raw := `{"jsonrpc":"2.0","id":null,"method":"initialize"}`
	var req Request
	require.NoError(t, json.Unmarshal([]byte(raw), &req))
	assert.Nil(t, req.ID)
}

func TestResponse_Marshal_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		resp     Response
		want     string
		notWant  string
	}{
		{
			name:    "success result",
			resp:    Response{JSONRPC: "2.0", ID: float64(1), Result: map[string]string{"status": "ok"}},
			want:    `"result"`,
			notWant: `"error"`,
		},
		{
			name: "error response",
			resp: Response{
				JSONRPC: "2.0",
				ID:      float64(2),
				Error:   &Error{Code: -32600, Message: "Invalid Request"},
			},
			want:    `"error"`,
			notWant: `"result"`,
		},
		{
			name: "error with data",
			resp: Response{
				JSONRPC: "2.0",
				ID:      float64(3),
				Error:   &Error{Code: -32602, Message: "Invalid params", Data: "missing field"},
			},
			want:    `"data"`,
			notWant: `"result"`,
		},
		{
			name:    "nil id",
			resp:    Response{JSONRPC: "2.0", ID: nil, Result: "ok"},
			want:    `"id":null`,
			notWant: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(tc.resp)
			require.NoError(t, err)
			assert.Contains(t, string(data), tc.want)
			if tc.notWant != "" {
				assert.NotContains(t, string(data), tc.notWant)
			}
		})
	}
}

func TestError_Marshal_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		e       Error
		wantKey string
	}{
		{
			name:    "parse error",
			e:       Error{Code: -32700, Message: "Parse error"},
			wantKey: `"code":-32700`,
		},
		{
			name:    "method not found",
			e:       Error{Code: -32601, Message: "Method not found"},
			wantKey: `"code":-32601`,
		},
		{
			name:    "with data",
			e:       Error{Code: -32602, Message: "Invalid params", Data: "extra"},
			wantKey: `"data":"extra"`,
		},
		{
			name:    "nil data omitted",
			e:       Error{Code: -32600, Message: "Invalid Request", Data: nil},
			wantKey: `"message":"Invalid Request"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(tc.e)
			require.NoError(t, err)
			assert.Contains(t, string(data), tc.wantKey)
		})
	}
}

func TestError_NilData_NotInOutput(t *testing.T) {
	t.Parallel()
	e := Error{Code: -32600, Message: "Invalid Request", Data: nil}
	data, err := json.Marshal(e)
	require.NoError(t, err)
	assert.NotContains(t, string(data), `"data"`)
}

func TestToolCallParams_Unmarshal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		raw      string
		wantName string
	}{
		{"recall", `{"name":"recall","arguments":{"query":"test"}}`, "recall"},
		{"store", `{"name":"store","arguments":{"content":"x"}}`, "store"},
		{"no-args", `{"name":"feedback","arguments":{}}`, "feedback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var p ToolCallParams
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &p))
			assert.Equal(t, tc.wantName, p.Name)
			assert.NotNil(t, p.Arguments)
		})
	}
}

func TestToolCallParams_ComplexArgs(t *testing.T) {
	t.Parallel()
	raw := `{"name":"recall","arguments":{"query":"auth","project":"eng","limit":5}}`
	var p ToolCallParams
	require.NoError(t, json.Unmarshal([]byte(raw), &p))
	assert.Equal(t, "recall", p.Name)
	assert.Contains(t, string(p.Arguments), "auth")
}

func TestTool_Marshal_RoundTrip(t *testing.T) {
	t.Parallel()
	tool := Tool{
		Name:        "recall",
		Description: "Search memories",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
		},
	}
	data, err := json.Marshal(tool)
	require.NoError(t, err)

	var parsed Tool
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, "recall", parsed.Name)
	assert.Equal(t, "Search memories", parsed.Description)
}

func TestTimelineParams_Unmarshal_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		raw       string
		wantOK    bool
		anchorID  int64
		query     string
		project   string
	}{
		{
			name:     "anchor_id",
			raw:      `{"anchor_id":123,"before":5,"after":5}`,
			wantOK:   true,
			anchorID: 123,
		},
		{
			name:    "query only",
			raw:     `{"query":"auth test","project":"eng"}`,
			wantOK:  true,
			query:   "auth test",
			project: "eng",
		},
		{
			name:   "invalid json",
			raw:    `{invalid`,
			wantOK: false,
		},
		{
			name:   "empty object valid",
			raw:    `{}`,
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var params TimelineParams
			err := json.Unmarshal([]byte(tc.raw), &params)
			if tc.wantOK {
				require.NoError(t, err)
				assert.Equal(t, tc.anchorID, params.AnchorID)
				assert.Equal(t, tc.query, params.Query)
				assert.Equal(t, tc.project, params.Project)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestTimelineParams_AllFields(t *testing.T) {
	t.Parallel()
	raw := `{
		"anchor_id":100,"query":"test","before":10,"after":20,
		"project":"proj","obs_type":"bugfix","concepts":"security",
		"files":"main.go","dateStart":1700000000000,"dateEnd":1700100000000,
		"format":"full"
	}`
	var p TimelineParams
	require.NoError(t, json.Unmarshal([]byte(raw), &p))
	assert.Equal(t, int64(100), p.AnchorID)
	assert.Equal(t, "test", p.Query)
	assert.Equal(t, 10, p.Before)
	assert.Equal(t, 20, p.After)
	assert.Equal(t, "proj", p.Project)
	assert.Equal(t, "bugfix", p.ObsType)
	assert.Equal(t, "security", p.Concepts)
	assert.Equal(t, "main.go", p.Files)
	assert.Equal(t, int64(1700000000000), p.DateStart)
	assert.Equal(t, int64(1700100000000), p.DateEnd)
	assert.Equal(t, "full", p.Format)
}

// ---------------------------------------------------------------------------
// NewServer / Version / ServerOptions
// ---------------------------------------------------------------------------

func TestNewServer_CreatesWithVersion(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "2.5.0"})
	require.NotNil(t, s)
	assert.Equal(t, "2.5.0", s.version)
}

func TestNewServer_HasStdinStdout(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	assert.NotNil(t, s.stdin)
	assert.NotNil(t, s.stdout)
}

func TestVersion_ReturnsVersion(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "3.0.1"})
	assert.Equal(t, "3.0.1", s.Version())
}

func TestServer_FieldsInjected(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s := &Server{stdin: strings.NewReader(""), stdout: &buf, version: "test"}
	assert.Equal(t, "test", s.version)
	assert.NotNil(t, s.stdin)
	assert.Equal(t, &buf, s.stdout)
}

// ---------------------------------------------------------------------------
// handleInitialize
// ---------------------------------------------------------------------------

func TestHandleInitialize_ProtocolAndVersion(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "4.0.0"})
	req := &Request{JSONRPC: "2.0", ID: float64(1), Method: "initialize"}
	resp := s.handleInitialize(req)

	assert.Equal(t, "2.0", resp.JSONRPC)
	assert.Nil(t, resp.Error)
	require.NotNil(t, resp.Result)

	result := resp.Result.(map[string]any)
	assert.Equal(t, "2024-11-05", result["protocolVersion"])

	info := result["serverInfo"].(map[string]any)
	assert.Equal(t, "engram", info["name"])
	assert.Equal(t, "4.0.0", info["version"])
}

func TestHandleInitialize_CapabilitiesPresent(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	resp := s.handleInitialize(&Request{JSONRPC: "2.0", ID: float64(1), Method: "initialize"})
	result := resp.Result.(map[string]any)
	caps, ok := result["capabilities"].(map[string]any)
	require.True(t, ok)
	_, hasTools := caps["tools"]
	assert.True(t, hasTools)
}

func TestHandleInitialize_IDEchoed(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	req := &Request{JSONRPC: "2.0", ID: "my-init-id", Method: "initialize"}
	resp := s.handleInitialize(req)
	assert.Equal(t, "my-init-id", resp.ID)
}

// ---------------------------------------------------------------------------
// handleToolsList
// ---------------------------------------------------------------------------

func TestHandleToolsList_PrimaryToolsPresent(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	resp := s.handleToolsList(&Request{JSONRPC: "2.0", ID: float64(1), Method: "tools/list"})
	require.Nil(t, resp.Error)

	result := resp.Result.(map[string]any)
	tools := result["tools"].([]Tool)
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"recall", "store", "feedback", "vault", "docs", "admin", "issues"} {
		assert.True(t, names[want], "primary tool %q must be present", want)
	}
}

func TestHandleToolsList_DefaultCountMatchesPrimary(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	resp := s.handleToolsList(&Request{JSONRPC: "2.0", ID: float64(1), Method: "tools/list"})
	result := resp.Result.(map[string]any)
	tools := result["tools"].([]Tool)
	// primary tools only: recall, store, feedback, vault, docs, admin, issues, check_system_health
	assert.Equal(t, 8, len(tools))
}

func TestHandleToolsList_IncludeAllReturnsMore(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	respDefault := s.handleToolsList(&Request{JSONRPC: "2.0", ID: float64(1), Method: "tools/list"})
	respAll := s.handleToolsList(&Request{
		JSONRPC: "2.0", ID: float64(2), Method: "tools/list",
		Params: json.RawMessage(`{"include_all":true}`),
	})
	defaultTools := respDefault.Result.(map[string]any)["tools"].([]Tool)
	allTools := respAll.Result.(map[string]any)["tools"].([]Tool)
	assert.Greater(t, len(allTools), len(defaultTools))
}

func TestHandleToolsList_IncludeAllContainsLegacy(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	resp := s.handleToolsList(&Request{
		JSONRPC: "2.0", ID: float64(1), Method: "tools/list",
		Params: json.RawMessage(`{"include_all":true}`),
	})
	tools := resp.Result.(map[string]any)["tools"].([]Tool)
	names := make(map[string]bool)
	for _, t2 := range tools {
		names[t2.Name] = true
	}
	assert.True(t, names["find_by_file"], "find_by_file should appear with include_all")
}

func TestHandleToolsList_RemovedToolsAbsent(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	respAll := s.handleToolsList(&Request{
		JSONRPC: "2.0", ID: float64(1), Method: "tools/list",
		Params: json.RawMessage(`{"include_all":true}`),
	})
	allTools := respAll.Result.(map[string]any)["tools"].([]Tool)
	allNames := make(map[string]bool)
	for _, t2 := range allTools {
		allNames[t2.Name] = true
	}
	for _, removed := range []string{"search", "decisions", "trigger_maintenance", "get_maintenance_stats"} {
		assert.False(t, allNames[removed], "removed tool %q must not appear", removed)
	}
}

func TestHandleToolsList_SchemaCompliance_NoForbiddenTopLevelKeys(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	resp := s.handleToolsList(&Request{
		JSONRPC: "2.0", ID: float64(1), Method: "tools/list",
		Params: json.RawMessage(`{"include_all":true}`),
	})
	tools := resp.Result.(map[string]any)["tools"].([]Tool)
	forbidden := []string{"oneOf", "allOf", "anyOf"}
	for _, tool := range tools {
		for _, key := range forbidden {
			_, found := tool.InputSchema[key]
			assert.False(t, found, "tool %q must not have forbidden top-level key %q", tool.Name, key)
		}
	}
}

func TestHandleToolsList_AllToolSchemasHaveTypeAndProperties(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	resp := s.handleToolsList(&Request{JSONRPC: "2.0", ID: float64(1), Method: "tools/list"})
	tools := resp.Result.(map[string]any)["tools"].([]Tool)
	for _, tool := range tools {
		assert.NotEmpty(t, tool.Name)
		assert.NotEmpty(t, tool.Description)
		schemaType, ok := tool.InputSchema["type"]
		assert.True(t, ok, "tool %q schema lacks type", tool.Name)
		assert.Equal(t, "object", schemaType, "tool %q schema type should be object", tool.Name)
		_, hasProps := tool.InputSchema["properties"]
		assert.True(t, hasProps, "tool %q schema lacks properties", tool.Name)
	}
}

func TestHandleToolsList_FeedbackSchemaCorrect(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	resp := s.handleToolsList(&Request{JSONRPC: "2.0", ID: float64(1), Method: "tools/list"})
	tools := resp.Result.(map[string]any)["tools"].([]Tool)
	var fb *Tool
	for i := range tools {
		if tools[i].Name == "feedback" {
			fb = &tools[i]
			break
		}
	}
	require.NotNil(t, fb)
	props := fb.InputSchema["properties"].(map[string]any)
	_, hasRating := props["rating"]
	assert.True(t, hasRating)
	_, hasSessionID := props["session_id"]
	assert.True(t, hasSessionID)
	_, hasUseful := props["useful"]
	assert.False(t, hasUseful, "feedback must not expose 'useful' boolean — use rating enum")
}

func TestHandleToolsList_StoreTypeEnumCorrect(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	resp := s.handleToolsList(&Request{JSONRPC: "2.0", ID: float64(1), Method: "tools/list"})
	tools := resp.Result.(map[string]any)["tools"].([]Tool)
	var storeTool *Tool
	for i := range tools {
		if tools[i].Name == "store" {
			storeTool = &tools[i]
			break
		}
	}
	require.NotNil(t, storeTool)
	props := storeTool.InputSchema["properties"].(map[string]any)
	typeSchema := props["type"].(map[string]any)
	enum := typeSchema["enum"].([]string)
	for _, want := range []string{"decision", "discovery", "pitfall", "timeline"} {
		assert.Contains(t, enum, want)
	}
	assert.NotContains(t, enum, "insight")
}

// ---------------------------------------------------------------------------
// handleRequest dispatch
// ---------------------------------------------------------------------------

func TestHandleRequest_InitializeRoute(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	resp := s.handleRequest(context.Background(), &Request{JSONRPC: "2.0", ID: float64(1), Method: "initialize"})
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)
	assert.NotNil(t, resp.Result)
}

func TestHandleRequest_ToolsListRoute(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	resp := s.handleRequest(context.Background(), &Request{JSONRPC: "2.0", ID: float64(2), Method: "tools/list"})
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)
}

func TestHandleRequest_UnknownMethodError(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	resp := s.handleRequest(context.Background(), &Request{JSONRPC: "2.0", ID: float64(3), Method: "no_such_method"})
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32601, resp.Error.Code)
	assert.Equal(t, "Method not found", resp.Error.Message)
}

func TestHandleRequest_NotificationReturnsNil(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	// JSON-RPC 2.0 notification: ID is nil → no response
	resp := s.handleRequest(context.Background(), &Request{JSONRPC: "2.0", ID: nil, Method: "initialized"})
	assert.Nil(t, resp)
}

func TestHandleRequest_CapabilityStubs(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	ctx := context.Background()
	stubs := []struct {
		method string
		key    string
	}{
		{"resources/list", "resources"},
		{"resources/templates/list", "resourceTemplates"},
		{"prompts/list", "prompts"},
		{"completion/complete", "completion"},
	}
	for _, tc := range stubs {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()
			resp := s.handleRequest(ctx, &Request{JSONRPC: "2.0", ID: float64(1), Method: tc.method})
			require.NotNil(t, resp)
			assert.Nil(t, resp.Error)
			result, ok := resp.Result.(map[string]any)
			require.True(t, ok)
			_, found := result[tc.key]
			assert.True(t, found, "stub response for %q must contain %q key", tc.method, tc.key)
		})
	}
}

// ---------------------------------------------------------------------------
// handleToolsCall
// ---------------------------------------------------------------------------

func TestHandleToolsCall_InvalidParamsJSON(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	req := &Request{JSONRPC: "2.0", ID: float64(1), Method: "tools/call", Params: json.RawMessage(`{invalid}`)}
	resp := s.handleToolsCall(context.Background(), req)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32602, resp.Error.Code)
}

func TestHandleToolsCall_EmptyParams(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	req := &Request{JSONRPC: "2.0", ID: float64(1), Method: "tools/call", Params: json.RawMessage(`{}`)}
	resp := s.handleToolsCall(context.Background(), req)
	require.NotNil(t, resp.Error)
}

func TestHandleToolsCall_UnknownTool(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	req := &Request{
		JSONRPC: "2.0", ID: float64(1), Method: "tools/call",
		Params: json.RawMessage(`{"name":"no_such_tool","arguments":{}}`),
	}
	resp := s.handleToolsCall(context.Background(), req)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32000, resp.Error.Code)
}

// ---------------------------------------------------------------------------
// callTool
// ---------------------------------------------------------------------------

func TestCallTool_UnknownToolReturnsError(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	_, err := s.callTool(context.Background(), "nonexistent_tool", json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool")
}

func TestCallTool_UnknownToolNames_Table(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	ctx := context.Background()
	for _, name := range []string{"invalid_tool", "nonexistent", "search_v2", "timeline_x"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, err := s.callTool(ctx, name, json.RawMessage(`{}`))
			assert.Error(t, err)
			assert.Empty(t, result)
			assert.Contains(t, err.Error(), "unknown tool")
		})
	}
}

func TestCallTool_FindByFile_Removed(t *testing.T) {
	t.Parallel()
	// find_by_file was retired in v5; even invalid JSON returns "removed in v5"
	s := NewServer(ServerOptions{Version: "1.0.0"})
	_, err := s.callTool(context.Background(), "find_by_file", json.RawMessage(`{invalid}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "removed in v5")
}

func TestCallTool_GetMemoryStats_NilStores(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	result, err := s.callTool(context.Background(), "get_memory_stats", json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.NotEmpty(t, result)
}

func TestCallTool_CheckSystemHealth_NilStores(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	result, err := s.callTool(context.Background(), "check_system_health", json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.NotEmpty(t, result)
}

func TestCallTool_ParameterValidation_Table(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	ctx := context.Background()
	// Tools that still exist in the switch and validate params before hitting the DB
	cases := []struct {
		tool        string
		args        string
		errContains string
	}{
		{"find_related_observations", `{invalid`, "invalid"},
		{"find_related_observations", `{}`, "id is required"},
		{"find_similar_observations", `{invalid`, "invalid"},
		{"find_similar_observations", `{}`, "query is required"},
		{"analyze_search_patterns", `{invalid`, "invalid"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.tool+"/"+tc.args, func(t *testing.T) {
			t.Parallel()
			_, err := s.callTool(ctx, tc.tool, json.RawMessage(tc.args))
			require.Error(t, err)
			if tc.errContains != "" {
				assert.Contains(t, err.Error(), tc.errContains)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// handleGetMemoryStats / handleCheckSystemHealth
// ---------------------------------------------------------------------------

func TestHandleGetMemoryStats_NilStores_ValidJSON(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	result, err := s.handleGetMemoryStats(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, result)
	var stats map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &stats))
}

func TestHandleCheckSystemHealth_NilStores_StructuredResponse(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	result, err := s.handleCheckSystemHealth(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, result)
	var health map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &health))
	assert.Contains(t, health, "overall_status")
	assert.Contains(t, health, "subsystems")
}

// ---------------------------------------------------------------------------
// handleFindRelatedObservations / handleFindSimilarObservations
// ---------------------------------------------------------------------------

func TestHandleFindRelatedObservations_Validation(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	ctx := context.Background()

	_, err := s.handleFindRelatedObservations(ctx, json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id is required")

	_, err = s.handleFindRelatedObservations(ctx, json.RawMessage(`{invalid`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid arguments")
}

func TestHandleFindSimilarObservations_Validation(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	ctx := context.Background()

	_, err := s.handleFindSimilarObservations(ctx, json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query is required")

	_, err = s.handleFindSimilarObservations(ctx, json.RawMessage(`{invalid`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid arguments")
}

func TestHandleFindSimilarObservations_EmptyResultInV5(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	result, err := s.handleFindSimilarObservations(context.Background(), json.RawMessage(`{"query":"test"}`))
	require.NoError(t, err)

	var payload struct {
		Count        int   `json:"count"`
		Observations []any `json:"observations"`
	}
	require.NoError(t, json.Unmarshal([]byte(result), &payload))
	assert.Equal(t, 0, payload.Count)
	require.NotNil(t, payload.Observations)
	assert.Len(t, payload.Observations, 0)
}

func TestHandleAnalyzeSearchPatterns_InvalidJSON(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerOptions{Version: "1.0.0"})
	_, err := s.handleAnalyzeSearchPatterns(context.Background(), json.RawMessage(`{invalid`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid arguments")
}

// ---------------------------------------------------------------------------
// sendResponse / sendError
// ---------------------------------------------------------------------------

func TestSendResponse_ContainsJSONRPC(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s := &Server{stdout: &buf}
	s.sendResponse(&Response{JSONRPC: "2.0", ID: float64(1), Result: map[string]string{"ok": "yes"}})
	output := buf.String()
	assert.Contains(t, output, `"jsonrpc":"2.0"`)
	assert.Contains(t, output, `"result"`)
}

func TestSendResponse_ErrorResponse(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s := &Server{stdout: &buf}
	s.sendResponse(&Response{JSONRPC: "2.0", ID: float64(1), Error: &Error{Code: -32600, Message: "Invalid Request"}})
	output := buf.String()
	assert.Contains(t, output, `"error"`)
	assert.Contains(t, output, `-32600`)
}

func TestSendResponse_NilID(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s := &Server{stdout: &buf}
	s.sendResponse(&Response{JSONRPC: "2.0", ID: nil, Result: "note"})
	assert.Contains(t, buf.String(), `"id":null`)
}

func TestSendResponse_VariousIDTypes(t *testing.T) {
	t.Parallel()
	for _, id := range []any{float64(1), "abc-123", 1.5, nil} {
		var buf bytes.Buffer
		s := &Server{stdout: &buf}
		s.sendResponse(&Response{JSONRPC: "2.0", ID: id, Result: "ok"})
		assert.Contains(t, buf.String(), `"jsonrpc":"2.0"`)
	}
}

func TestSendError_OutputShape(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s := &Server{stdout: &buf}
	s.sendError(float64(1), -32700, "Parse error", "details")
	output := buf.String()
	assert.Contains(t, output, `"error"`)
	assert.Contains(t, output, `-32700`)
	assert.Contains(t, output, `"Parse error"`)
}

// ---------------------------------------------------------------------------
// Run loop
// ---------------------------------------------------------------------------

func TestRun_ParseError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s := &Server{stdin: strings.NewReader("not json\n"), stdout: &buf}
	require.NoError(t, s.Run(context.Background()))
	out := buf.String()
	assert.Contains(t, out, `"error"`)
	assert.Contains(t, out, `-32700`)
	assert.Contains(t, out, `"Parse error"`)
}

func TestRun_EmptyLinesSkipped(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s := &Server{stdin: strings.NewReader("\n\n\n"), stdout: &buf}
	require.NoError(t, s.Run(context.Background()))
	assert.Empty(t, buf.String())
}

func TestRun_ValidInitialize(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s := &Server{
		stdin:   strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n"),
		stdout:  &buf,
		version: "1.0.0",
	}
	require.NoError(t, s.Run(context.Background()))
	out := buf.String()
	assert.Contains(t, out, `"jsonrpc":"2.0"`)
	assert.Contains(t, out, `"protocolVersion"`)
}

func TestRun_MultipleRequests(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	s := &Server{stdin: strings.NewReader(input), stdout: &buf, version: "1.0.0"}
	require.NoError(t, s.Run(context.Background()))
	out := buf.String()
	assert.Contains(t, out, `"id":1`)
	assert.Contains(t, out, `"id":2`)
}

func TestRun_MixedValidAndInvalid(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
		`bad json here` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}` + "\n"
	s := &Server{stdin: strings.NewReader(input), stdout: &buf, version: "1.0.0"}
	require.NoError(t, s.Run(context.Background()))
	out := buf.String()
	assert.Contains(t, out, `"id":1`)
	assert.Contains(t, out, `"error"`)
	assert.Contains(t, out, `"id":3`)
}

func TestRun_NotificationNoResponse(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	// notification has no id field — server must not respond
	input := `{"jsonrpc":"2.0","method":"initialized"}` + "\n"
	s := &Server{stdin: strings.NewReader(input), stdout: &buf, version: "1.0.0"}
	require.NoError(t, s.Run(context.Background()))
	assert.Empty(t, buf.String())
}

// ---------------------------------------------------------------------------
// JSON-RPC standard error codes (regression guard)
// ---------------------------------------------------------------------------

func TestJSONRPCErrorCodes_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		msg  string
		code int
	}{
		{"Parse error", -32700},
		{"Invalid Request", -32600},
		{"Method not found", -32601},
		{"Invalid params", -32602},
		{"Internal error", -32603},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			t.Parallel()
			e := Error{Code: tc.code, Message: tc.msg}
			assert.Equal(t, tc.code, e.Code)
			assert.Equal(t, tc.msg, e.Message)
		})
	}
}

// ---------------------------------------------------------------------------
// Tier constants
// ---------------------------------------------------------------------------

func TestTierConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 1, tierCore)
	assert.Equal(t, 2, tierUseful)
	assert.Equal(t, 3, tierAdmin)
}
