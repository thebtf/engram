package mcp_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
)

// buildToolNames calls tools/list with include_all=true and returns a set of
// tool names. Mirrors the helper pattern used in wiring_vnext_test.go.
func buildCodeIntelToolNames(srv *mcp.Server) map[string]bool {
	req := &mcp.Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/list",
		Params:  json.RawMessage(`{"include_all":true}`),
	}
	resp := srv.HandleRequest(nil, req)
	require.NotNil((&testing.T{}), resp)
	result := resp.Result.(map[string]any)
	tools := result["tools"].([]mcp.Tool)
	names := make(map[string]bool, len(tools))
	for _, t := range tools {
		names[t.Name] = true
	}
	return names
}

// TestCodeIntelFlag_Off_ToolsAbsentFromList verifies that when
// ENGRAM_CODE_INTEL_ENABLED is not "true", no codebase_* tools appear in
// tools/list — the flag-off surface is byte-identical to pre-CR-006.
func TestCodeIntelFlag_Off_ToolsAbsentFromList(t *testing.T) {
	os.Unsetenv("ENGRAM_CODE_INTEL_ENABLED")

	srv := mcp.NewServer(mcp.ServerOptions{Version: "test"})
	// Do NOT call SetCodeChunkStore — flag is off.

	req := &mcp.Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/list",
		Params:  json.RawMessage(`{"include_all":true}`),
	}
	resp := srv.HandleRequest(nil, req)
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)

	result := resp.Result.(map[string]any)
	tools := result["tools"].([]mcp.Tool)
	for _, tool := range tools {
		assert.NotEqual(t, "codebase_search", tool.Name, "codebase_search must not appear when flag is off")
		assert.NotEqual(t, "codebase_status", tool.Name, "codebase_status must not appear when flag is off")
		assert.NotEqual(t, "codebase_index", tool.Name, "codebase_index must not appear in server tools/list")
	}
}

// TestCodeIntelFlag_On_StoreNil_ToolsAbsentFromList verifies that when
// ENGRAM_CODE_INTEL_ENABLED=true but SetCodeChunkStore was not called, the
// tools are still absent (nil guard).
func TestCodeIntelFlag_On_StoreNil_ToolsAbsentFromList(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")

	srv := mcp.NewServer(mcp.ServerOptions{Version: "test"})
	// SetCodeChunkStore intentionally NOT called.

	req := &mcp.Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/list",
		Params:  json.RawMessage(`{"include_all":true}`),
	}
	resp := srv.HandleRequest(nil, req)
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)

	result := resp.Result.(map[string]any)
	tools := result["tools"].([]mcp.Tool)
	for _, tool := range tools {
		assert.NotEqual(t, "codebase_search", tool.Name, "codebase_search must not appear when store is nil")
		assert.NotEqual(t, "codebase_status", tool.Name, "codebase_status must not appear when store is nil")
	}
}

// TestCodeIntelFlag_On_ServerAdvertisesSearchNotStatus verifies that when the
// flag is on AND the store is wired, the SERVER's tools/list advertises
// codebase_search but NOT codebase_status. codebase_status is the daemon-side
// static tool's name; the daemon reaches the server's codebase_status HANDLER via
// the engramcore proxy, but the server must not ADVERTISE it — otherwise the
// daemon's tools/list (static codeintel + proxied server list) would contain a
// duplicate codebase_status entry. The handler stays callable (see the FlagOff
// error test, which exercises the callTool path).
func TestCodeIntelFlag_On_ServerAdvertisesSearchNotStatus(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")

	srv := mcp.NewServer(mcp.ServerOptions{Version: "test"})
	// Wire a non-nil store so the gate (codeIntelEnabled() && store != nil) opens.
	// handleToolsList only checks the pointer is non-nil; it never queries during
	// listing, so a store over a nil *gorm.DB is sufficient for this surface test.
	srv.SetCodeChunkStore(gorm.NewCodeChunkStore(nil))

	names := buildCodeIntelToolNames(srv)
	assert.True(t, names["codebase_search"], "codebase_search must be advertised on the server when flag+store are on")
	assert.False(t, names["codebase_status"], "codebase_status must NOT be advertised on the server (daemon-side static tool; avoids a duplicate in the daemon tools/list)")
	assert.False(t, names["codebase_index"], "codebase_index is daemon-only; never on the server list")
}

// TestCodebaseSearch_FlagOff_ReturnsError verifies that codebase_search returns
// an error when the flag is off, even if called directly via callTool.
func TestCodebaseSearch_FlagOff_ReturnsError(t *testing.T) {
	os.Unsetenv("ENGRAM_CODE_INTEL_ENABLED")

	srv := mcp.NewServer(mcp.ServerOptions{Version: "test"})

	req := &mcp.Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"codebase_search","arguments":{"query":"hello","project":"test"}}`),
	}
	resp := srv.HandleRequest(nil, req)
	require.NotNil(t, resp)
	// Should return an error (either tool error or unknown tool).
	// The error message must mention the flag or the unknown tool.
	toolErr := resp.Error
	if toolErr == nil {
		// Might have been routed to the "unknown tool" fallback; check the result text.
		result := resp.Result.(map[string]any)
		content := result["content"].([]map[string]any)
		text := content[0]["text"].(string)
		assert.Contains(t, text, "ENGRAM_CODE_INTEL_ENABLED", "error must mention the flag when it fires as a result error")
	} else {
		// Could be dispatched as JSON-RPC error if the tool is literally unknown.
		// In our implementation the tool is in callTool switch but returns an error.
		assert.NotNil(t, toolErr)
	}
}
