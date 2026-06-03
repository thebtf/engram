// Package dispatcher implements the stateless MCP protocol handler that sits
// between muxcore's SessionHandler contract and the module registry.
//
// Design reference: design.md Section 2.2 (Dispatcher responsibilities),
// Section 5.1 (happy-path tool call data flow), FR-4 (Dispatcher), FR-14
// (30 s timeout), FR-15 (panic isolation).
package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/obs"
	"github.com/thebtf/engram/internal/module/registry"
	"github.com/thebtf/engram/internal/version"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// Dispatcher is an MCP protocol handler that sits between muxcore's
// SessionHandler contract and the module registry. It is mostly stateless —
// the registry is immutable after Freeze and the toolIndex map is read-only —
// but it tracks two pieces of lock-free mutable state:
//
//  1. draining (atomic.Bool, see drain.go) — when set, handleToolsCall
//     rejects new requests for graceful restart.
//  2. tracked (sync.Map) — maps projectID → struct{} for every project the
//     dispatcher has seen via OnProjectConnect but not yet OnProjectDisconnect.
//     Exposed read-only via ConnectedProjectIDs() for the serverevents
//     bridge's SyncProjectState heartbeat (v4.4.0 US4).
//
// Design decision D18 (revised 2026-04-15 for Phase 5): "no mutex" now means
// "no mutex in the hot HandleRequest path". The session-lifecycle callbacks
// use sync.Map which is lock-free for reads and amortised O(1) for writes —
// they are not in the tool-dispatch hot path.
//
// HandleRequest is safe for concurrent calls from multiple goroutines because:
//   - The registry is immutable after Freeze.
//   - The toolIndex map is read-only (no writes after Register).
//   - draining is an atomic.Bool.
//   - tracked is a sync.Map — the HandleRequest path never touches it.
type Dispatcher struct {
	reg           *registry.Registry
	logger        *slog.Logger
	serverVersion string
	draining      atomic.Bool
	tracked       sync.Map // projectID (string) → struct{} — active sessions per US4
}

// New creates a Dispatcher bound to the given frozen registry and logger.
// The registry MUST be frozen before the dispatcher processes any requests.
func New(r *registry.Registry, logger *slog.Logger) *Dispatcher {
	return NewWithVersion(r, logger, version.Daemon)
}

// NewWithVersion creates a Dispatcher with an explicit MCP serverInfo.version.
func NewWithVersion(r *registry.Registry, logger *slog.Logger, serverVersion string) *Dispatcher {
	if serverVersion == "" {
		serverVersion = version.Daemon
	}
	return &Dispatcher{reg: r, logger: logger, serverVersion: serverVersion}
}

// ConnectedProjectIDs returns a snapshot of all project IDs with an active
// session as observed by OnProjectConnect / OnProjectDisconnect callbacks.
// Order is not stable (sync.Map iteration order). The snapshot is consistent
// at the moment of the call — subsequent connect/disconnect events are not
// reflected.
//
// This accessor is used by internal/handlers/serverevents/Bridge to populate
// SyncProjectState heartbeat requests so the server can reconcile its
// authoritative project list against the daemon's live set (v4.4.0 FR-9 +
// US4). Returning an empty slice is valid (no active sessions).
func (d *Dispatcher) ConnectedProjectIDs() []string {
	var ids []string
	d.tracked.Range(func(k, _ any) bool {
		if id, ok := k.(string); ok {
			ids = append(ids, id)
		}
		return true
	})
	return ids
}

// OnProjectConnect implements muxcore.ProjectLifecycle. It records the
// project in the tracked set, increments the active-sessions metric, and
// fans out the session-connect event to every module that implements
// module.ProjectLifecycle, preserving registration order.
//
// Panic isolation (FR-15): each module callback runs under
// recoverLifecycleCallback so a crashy module cannot stall session setup.
func (d *Dispatcher) OnProjectConnect(p muxcore.ProjectContext) {
	d.tracked.Store(p.ID, struct{}{})
	obs.IncrementActiveSessions(context.Background())
	d.reg.ForEachLifecycleHandler(func(h module.ProjectLifecycle) {
		recoverLifecycleCallback(
			"", // name resolution deferred — logger field comes from d.logger
			"OnSessionConnect",
			d.logger,
			func() { h.OnSessionConnect(p) },
		)
	})
}

// OnProjectDisconnect implements muxcore.ProjectLifecycle. It removes the
// project from the tracked set, decrements the active-sessions metric, and
// fans out the session-disconnect event to every module that implements
// module.ProjectLifecycle, preserving registration order.
//
// Per design.md §3.3 modules MUST NOT cancel long-running tasks on
// disconnect — tasks outlive sessions. The dispatcher enforces nothing here
// other than panic isolation.
func (d *Dispatcher) OnProjectDisconnect(projectID string) {
	d.tracked.Delete(projectID)
	obs.DecrementActiveSessions(context.Background())
	d.reg.ForEachLifecycleHandler(func(h module.ProjectLifecycle) {
		recoverLifecycleCallback(
			"",
			"OnSessionDisconnect",
			d.logger,
			func() { h.OnSessionDisconnect(projectID) },
		)
	})
}

// HandleRequest implements muxcore.SessionHandler. It parses the incoming
// JSON-RPC 2.0 request bytes, routes to the appropriate handler, and returns
// the response bytes.
//
// Protocol methods (initialize, ping, notifications/cancelled) are handled
// directly. Content methods (tools/list, tools/call) are routed through the
// registry.
//
// HandleRequest never returns a Go error — all errors are encoded as JSON-RPC
// error responses so muxcore can write them to the session.
func (d *Dispatcher) HandleRequest(ctx context.Context, p muxcore.ProjectContext, request []byte) ([]byte, error) {
	var req jsonrpcRequest
	if err := json.Unmarshal(request, &req); err != nil {
		// Parse error — we can't read the ID so use null.
		return marshalError(json.RawMessage("null"), -32700, "parse error: "+err.Error()), nil
	}

	d.logger.Debug("dispatcher request",
		"method", req.Method,
		"project_id", p.ID,
	)

	switch req.Method {
	case "initialize":
		return d.handleInitialize(ctx, p, &req)
	case "ping":
		return d.handlePing(ctx, p, &req)
	case "notifications/cancelled", "notifications/initialized":
		// JSON-RPC 2.0 notifications have no response.
		return nil, nil
	case "tools/list":
		return d.handleToolsList(ctx, p, &req)
	case "tools/call":
		return d.handleToolsCall(ctx, p, &req)
	default:
		return marshalError(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method)), nil
	}
}

// ---------------------------------------------------------------------------
// Internal JSON-RPC types
// ---------------------------------------------------------------------------

// jsonrpcRequest is the minimal JSON-RPC 2.0 request envelope parsed by the
// dispatcher. The ID is preserved as raw JSON to support both numeric and
// string IDs per the JSON-RPC 2.0 specification.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonrpcResponse is the JSON-RPC 2.0 response envelope.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcErr     `json:"error,omitempty"`
}

// jsonrpcErr is the JSON-RPC 2.0 error object.
type jsonrpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// marshalError returns a JSON-encoded JSON-RPC error response.
// If marshalling fails (should never happen), returns a hard-coded fallback.
func marshalError(id json.RawMessage, code int, message string) []byte {
	resp := jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonrpcErr{Code: code, Message: message},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"internal marshalling error"}}`)
	}
	return b
}

// marshalResult returns a JSON-encoded JSON-RPC success response.
func marshalResult(id json.RawMessage, result any) []byte {
	raw, err := json.Marshal(result)
	if err != nil {
		return marshalError(id, -32603, "internal: marshal result: "+err.Error())
	}
	resp := jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  raw,
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return marshalError(id, -32603, "internal: marshal response: "+err.Error())
	}
	return b
}
