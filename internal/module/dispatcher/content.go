package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/thebtf/engram/internal/module"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// handleToolsList processes the MCP "tools/list" request.
//
// Aggregates tool definitions from all static ToolProvider modules via
// registry.AggregateTools — the list is built in module registration order —
// then appends the dynamic tool list from the single registered
// ProxyToolProvider (if any) per FR-11a.
//
// This is a hot path: the aggregated slice is rebuilt on each call in v0.1.0.
// Static aggregation is O(n) across ~1-5 modules. The proxy call may block on
// network I/O (typically a gRPC Initialize handshake) — this is accepted
// because tools/list is low-frequency (once per session open).
//
// Graceful degradation: if ProxyTools returns an error, the dispatcher logs a
// warning and returns ONLY the static tools. A network blip MUST NOT break
// tools/list — this is explicit in the FR-11a contract.
//
// Design reference: FR-4 (Dispatcher) + FR-11a (ProxyToolProvider) and
// design.md Section 5.1.
func (d *Dispatcher) handleToolsList(ctx context.Context, p muxcore.ProjectContext, req *jsonrpcRequest) ([]byte, error) {
	tools := d.reg.AggregateTools()

	if proxy, proxyName, ok := d.reg.GetProxyToolProvider(); ok {
		proxyTools, err := proxy.ProxyTools(ctx, p)
		if err != nil {
			// FR-11a graceful degradation: log + fall through to static-only.
			d.logger.Warn("proxy tool provider ProxyTools failed, returning static tools only",
				"module", proxyName,
				"project_id", p.ID,
				"error", err,
			)
		} else {
			tools = append(tools, proxyTools...)
		}
	}

	type toolEntry struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	}
	entries := make([]toolEntry, len(tools))
	for i, t := range tools {
		entries[i] = toolEntry{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
	}

	result := map[string]any{"tools": entries}
	return marshalResult(req.ID, result), nil
}

// handleToolsCall processes the MCP "tools/call" request.
//
// Routing: look up the tool name in the registry via ToolByName. If not
// found, return JSON-RPC -32601. If found, call the owning module's
// HandleTool under a 30 s hard cap with panic recovery.
//
// Error taxonomy (design.md Section 3.4):
//   - Protocol errors (-32xxx): returned as JSON-RPC "error" field.
//   - Module errors (*module.ModuleError): returned as JSON-RPC "result" with
//     isError=true in the content block. NOT as JSON-RPC error — per FR-12.
//   - Internal errors (panic, timeout): mapped to -32603 internal error.
func (d *Dispatcher) handleToolsCall(ctx context.Context, p muxcore.ProjectContext, req *jsonrpcRequest) ([]byte, error) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return marshalError(req.ID, -32602, "invalid params: "+err.Error()), nil
	}
	if params.Name == "" {
		return marshalError(req.ID, -32602, "invalid params: tool name is required"), nil
	}

	// Priority A: static ToolProvider lookup. O(1) hash hit.
	entry, _, ok := d.reg.ToolByName(params.Name)

	var (
		raw     json.RawMessage
		callErr error
	)
	if ok {
		// Static path: call module.ToolProvider.HandleTool under 30 s cap +
		// panic recovery.
		raw, callErr = callToolWithTimeout(ctx, entry.ToolProv, p, params.Name, params.Arguments,
			params.Name, p.ID, d.logger)
	} else {
		// Priority B: ProxyToolProvider fallthrough per FR-11a. At most one
		// proxy is registered so ambiguity is impossible.
		proxy, _, hasProxy := d.reg.GetProxyToolProvider()
		if !hasProxy {
			return marshalError(req.ID, -32601, fmt.Sprintf("tool not found: %s", params.Name)), nil
		}
		// Proxy path: call ProxyHandleTool under the same 30 s cap + panic
		// recovery contract. An unknown-tool result from the proxy is NOT
		// re-mapped to -32601 here — the proxy itself is responsible for
		// surfacing tool-not-found via *module.ModuleError (result-level
		// isError:true) or JSON-RPC error (-32603) as appropriate.
		raw, callErr = callProxyToolWithTimeout(ctx, proxy, p, params.Name, params.Arguments,
			params.Name, p.ID, d.logger)
	}

	if callErr != nil {
		// Priority 1: dispatcher-injected 30 s timeout → -32603 per spec edge case.
		// The sentinel [dispatcherTimeoutError] is specifically emitted by
		// callToolWithTimeout when our own cap fires, distinct from a module
		// voluntarily returning [module.ErrTimeout]. See timeout.go docs for the
		// full distinction and spec rationale.
		if _, isTimeout := callErr.(*dispatcherTimeoutError); isTimeout {
			return marshalError(req.ID, -32603, "internal error: "+callErr.Error()), nil
		}

		// Priority 2: module-returned structured error → result-level per FR-12.
		// AI agents parse this structured error and decide retry strategy; the
		// transport layer does NOT auto-retry (which is why this is NOT a
		// JSON-RPC -32xxx error).
		var modErr *module.ModuleError
		if isModuleError(callErr, &modErr) {
			content := []map[string]any{
				{"type": "text", "text": modErr.Error()},
			}
			result := map[string]any{
				"content": content,
				"isError": true,
			}
			return marshalResult(req.ID, result), nil
		}

		// Priority 3: internal / panic / unknown error → -32603.
		return marshalError(req.ID, -32603, callErr.Error()), nil
	}

	// Success: wrap the raw module result in the MCP content envelope.
	content := []json.RawMessage{raw}
	result := map[string]any{
		"content": content,
		"isError": false,
	}
	return marshalResult(req.ID, result), nil
}

// isModuleError checks whether err is a *module.ModuleError and assigns it to
// out if so. Returns true on match.
func isModuleError(err error, out **module.ModuleError) bool {
	if me, ok := err.(*module.ModuleError); ok {
		*out = me
		return true
	}
	return false
}
