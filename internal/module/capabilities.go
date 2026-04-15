package module

import (
	"context"
	"encoding/json"

	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// Snapshotter is implemented by modules that need to persist state across
// daemon restarts. Stateless modules MUST NOT implement this interface;
// doing so wastes snapshot pipeline cycles and creates misleading manifest
// entries.
//
// The snapshot pipeline calls Snapshot in REVERSE registration order and
// Restore in FORWARD order — see design.md Section 4.3 for ordering rationale.
type Snapshotter interface {
	// Snapshot serializes restorable state. Called during the pre-shutdown
	// snapshot phase, BEFORE Shutdown. MUST be fast (<1s recommended).
	// For heavy state, write to deps.StorageDir asynchronously during runtime
	// and return a lightweight pointer (e.g. filepath + last-committed marker)
	// in the bytes.
	//
	// Use [MarshalSnapshot](version, payload) to produce versioned output
	// compatible with the forward-compat fallback in [UnmarshalSnapshot].
	Snapshot() ([]byte, error)

	// Restore rehydrates state. Called AFTER Init and BEFORE the first
	// HandleTool call. data is exactly what a previous Snapshot returned for
	// this module's name.
	//
	// nil or empty data means first boot — module MUST start with defaults.
	//
	// Returning an error logs a warning but does NOT abort startup; the module
	// continues with default (empty) state.
	//
	// Use [UnmarshalSnapshot](b, maxSupportedVersion) to extract the payload
	// and handle forward-compat mismatches via [ErrUnsupportedVersion].
	Restore(data []byte) error
}

// ProjectLifecycle is implemented by modules that care about CC session
// connect/disconnect events. The callbacks are sourced from muxcore session
// events and carry the same semantics as muxcore.ProjectLifecycle, but scoped
// to the engram module framework's lifecycle sequencing.
//
// Callbacks are invoked sequentially by the lifecycle goroutine — no
// concurrency within lifecycle callbacks, but they may overlap with concurrent
// HandleTool calls.
type ProjectLifecycle interface {
	// OnSessionConnect fires when a CC session opens a new connection AND it
	// is the first connection for this ProjectContext.ID. Repeat connections
	// for the same project do NOT fire this callback.
	//
	// p carries the project identity and per-session environment variables.
	OnSessionConnect(p muxcore.ProjectContext)

	// OnSessionDisconnect fires when the last session for this
	// ProjectContext.ID closes. NOT called on project removal — see
	// [ProjectRemovalAware] for that event.
	//
	// IMPORTANT: modules MUST NOT cancel long-running tasks here. Tasks
	// OUTLIVE sessions by design. This contract is shared with the loom
	// integration (Phase B): loom.Submit returns immediately and the
	// dispatched task runs on engine-lifetime goroutines independent of the
	// calling session.
	OnSessionDisconnect(projectID string)
}

// ProjectRemovalAware is implemented by modules that must clean up
// per-project state when a project is explicitly removed via the dashboard,
// CLI, or API. This is distinct from session disconnect, which is transient
// (the same project may reconnect later).
//
// The callback is delivered by the serverevents bridge when it receives a
// ProjectRemoved event from engram-server. In v4.3.0, the bridge is a stub
// (ProjectEvents stream not yet available on engram-server — see P003 outcome
// in tasks-trace.md). The interface is still implemented by engramcore for
// unit-testability via the moduletest harness.
type ProjectRemovalAware interface {
	// OnProjectRemoved fires when an authoritative project removal event is
	// received. Modules SHOULD cancel in-flight work for the project, release
	// caches, and free on-disk storage allocated for the project.
	//
	// For loom-module (Phase B): invoke engine.CancelAllForProject(id).
	// For engramcore: drop slugCache entry, close per-project gRPC conns.
	// For vectorindex (Phase D1): delete $storage/modules/vectorindex/<id>/.
	OnProjectRemoved(projectID string)
}

// ToolProvider is implemented by modules that expose MCP tools — the content
// layer of the engram protocol.
//
// The registry calls Tools once at Register time for conflict detection and
// tools/list aggregation. HandleTool is called concurrently by the dispatcher
// from multiple session goroutines; the module MUST be thread-safe.
type ToolProvider interface {
	// Tools returns this module's static tool definitions. Called ONCE by the
	// registry at Register time. The list MUST be stable — the same call MUST
	// return the same set across the daemon lifetime.
	//
	// Dynamic per-project tool visibility is NOT supported in v0.1.0 — see
	// roadmap for Phase D1 "dynamic tool filtering" discussion.
	Tools() []ToolDef

	// HandleTool executes a tool call routed by name. Called concurrently from
	// multiple session goroutines — module MUST be thread-safe.
	//
	// ctx is session-scoped (cancelled on session disconnect or daemon
	// shutdown). The dispatcher ALSO imposes a 30s hard cap as a defensive
	// safety net — module should return ctx.Err() on cancellation.
	//
	// p carries the originating session's project context.
	//
	// name is one of the tool names returned by Tools().
	//
	// args is the raw JSON "arguments" field from the MCP tools/call request.
	//
	// The return value is the MCP "result" field; error becomes a JSON-RPC
	// error response.
	//
	// CONTRACT RULE: HandleTool MUST be synchronous and bounded <1s wall-clock.
	// For operations expected to exceed 1s, the tool MUST submit work as a
	// background task (typically via loom, Phase B), return a task reference
	// immediately, and let the client poll via a status tool or subscribe via
	// EventBus. HandleTool is request/response, NOT streaming. In-request
	// progress notifications are FORBIDDEN in v0.1.0.
	HandleTool(ctx context.Context, p muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error)
}

// ProxyToolProvider is implemented by modules whose tool list cannot be
// declared at compile time because it is owned by a backend system and
// fetched at runtime (e.g. the engramcore module which proxies 68+ tools
// from engram-server via a gRPC Initialize handshake).
//
// This is an ADDITIVE capability — it supplements ToolProvider rather than
// replacing it. A single daemon may have both static ToolProvider modules and
// a single ProxyToolProvider module simultaneously. The dispatcher merges
// their tool lists at tools/list time and routes tools/call requests to
// whichever owns the requested name.
//
// CONTRACT RULE (FR-11a): at most ONE module in the registry may implement
// ProxyToolProvider. Registering a second one MUST fail fast at Register time
// with [registry.ErrMultipleProxyToolProviders]. This single-instance rule
// prevents ambiguous routing when a tool name is not found in any static
// ToolProvider.
//
// Routing precedence (dispatcher):
//  1. Look up tool name in static ToolProvider registry.
//  2. If not found AND a ProxyToolProvider is registered, forward the call
//     via ProxyHandleTool.
//  3. If still not found, return JSON-RPC -32601 method not found.
type ProxyToolProvider interface {
	// ProxyTools returns the dynamic tool list fetched from the backend for a
	// specific project context. Called by the dispatcher on every tools/list
	// request because the backend tool set MAY vary by project (different
	// projects can have different enabled tool sets on the server).
	//
	// The call is synchronous but MAY block on network I/O (typically a gRPC
	// Initialize handshake to engram-server). A reasonable timeout (configured
	// by the module implementer, not the framework) MUST be applied internally.
	//
	// Returning an error means the tool list is temporarily unavailable; the
	// dispatcher logs a warning and returns ONLY the static tool list. This is
	// graceful degradation — a network blip MUST NOT break tools/list.
	ProxyTools(ctx context.Context, p muxcore.ProjectContext) ([]ToolDef, error)

	// ProxyHandleTool forwards a tools/call request to the backend when the
	// tool name is not found in any static ToolProvider. Called concurrently
	// from multiple session goroutines — implementations MUST be thread-safe.
	//
	// The same <1s soft contract from ToolProvider.HandleTool applies to the
	// synchronous portion of ProxyHandleTool. Long-running operations MUST be
	// submitted as background tasks and return a task reference immediately.
	//
	// ctx carries the dispatcher's 30s hard cap. p is the originating session's
	// project context. name is the tool name as received from the client
	// (guaranteed not to match any static ToolProvider tool). args is the raw
	// JSON "arguments" field from the request.
	//
	// The return value is the MCP "result" field; a *module.ModuleError is
	// mapped to result-level isError:true per FR-12; any other error maps to
	// JSON-RPC -32603.
	ProxyHandleTool(ctx context.Context, p muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error)
}
