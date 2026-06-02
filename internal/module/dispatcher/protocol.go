package dispatcher

import (
	"context"
	"encoding/json"

	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// handleInitialize processes the MCP "initialize" request.
//
// Returns a standard MCP InitializeResult with server info and capabilities.
// The protocolVersion, serverInfo, and capabilities fields are frozen per
// MCP specification 2024-11-05.
//
// Design reference: design.md Section 2.2, MCP protocol spec 2024-11-05.
func (d *Dispatcher) handleInitialize(_ context.Context, _ muxcore.ProjectContext, req *jsonrpcRequest) ([]byte, error) {
	result := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "engram",
			"version": d.serverVersion,
		},
	}
	return marshalResult(req.ID, result), nil
}

// handlePing processes the MCP "ping" request.
//
// Returns an empty result object per MCP specification.
func (d *Dispatcher) handlePing(_ context.Context, _ muxcore.ProjectContext, req *jsonrpcRequest) ([]byte, error) {
	return marshalResult(req.ID, json.RawMessage(`{}`)), nil
}
