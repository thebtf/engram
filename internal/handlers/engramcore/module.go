// Package engramcore is the first tenant of the modular daemon framework.
// It wraps the legacy engramHandler (a transparent MCP→gRPC proxy to the
// engram server) as an EngramModule + ProjectLifecycle + ProjectRemovalAware
// + ProxyToolProvider.
//
// Design reference: design.md §4.2 (US2 first tenant migration) and spec.md
// §FR-11a (ProxyToolProvider amendment — necessary because the engram tool
// set is owned by the server and not knowable at compile time).
//
// Constraints:
//   - NFR-5 zero breaking change: tools/list and tools/call output MUST be
//     byte-identical to v4.2.0 for any given gRPC backend response.
//   - FR-13 HandleTool <1s soft contract is satisfied by the server-side
//     timeout; the proxy itself adds no blocking.
//   - FR-11a single ProxyToolProvider: the registry rejects any attempt to
//     register a second proxy module.
package engramcore

import (
	"context"
	"fmt"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/module"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// moduleName is the stable identifier used by ModuleDeps, storage
// paths, metric labels, and the registry. MUST NOT change across releases
// without a migration path for snapshot files keyed on it.
const moduleName = "engramcore"

// Module is the engramcore tenant of the modular daemon framework. It
// implements:
//
//   - module.EngramModule       — core lifecycle (Name/Init/Shutdown)
//   - module.ProjectLifecycle   — session connect/disconnect logging
//   - module.ProjectRemovalAware — clear slug cache on project removal
//   - module.ProxyToolProvider  — dynamic tool set fetched from engram-server
//
// It deliberately does NOT implement module.ToolProvider because the engram
// tool list is not knowable at compile time — the server returns a list via
// the gRPC Initialize RPC, and the client has no static inventory of tool
// metadata. FR-11a exists solely for this shape of module.
type Module struct {
	pool  *grpcPool
	cache *slugCache
	deps  module.ModuleDeps
}

// NewModule constructs an unstarted engramcore module. Call Init before
// HandleTool / ProxyTools. This is the single entry point for the daemon
// wiring in cmd/engram/main.go.
func NewModule() *Module {
	return &Module{
		pool:  &grpcPool{},
		cache: &slugCache{},
	}
}

// -----------------------------------------------------------------------
// EngramModule
// -----------------------------------------------------------------------

// Name returns the stable module identifier. Implements module.EngramModule.
func (m *Module) Name() string { return moduleName }

// Init captures ModuleDeps for later use. The module has no blocking
// initialisation — gRPC connections are dialled lazily on first use.
// Implements module.EngramModule.
func (m *Module) Init(_ context.Context, deps module.ModuleDeps) error {
	m.deps = deps
	if deps.Logger != nil {
		deps.Logger.Info("engramcore module initialised",
			"storage_dir", deps.StorageDir,
			"config_bytes", len(deps.Config),
		)
	}
	return nil
}

// Shutdown closes all pooled gRPC connections. Implements module.EngramModule.
//
// Per design.md §4.1 shutdown proceeds in reverse registration order; this
// module is typically registered first so it drains last. Closing gRPC
// connections is idempotent so concurrent Shutdown calls are safe.
func (m *Module) Shutdown(_ context.Context) error {
	m.pool.closeAll()
	if m.deps.Logger != nil {
		m.deps.Logger.Info("engramcore module shut down")
	}
	return nil
}

// -----------------------------------------------------------------------
// ProjectLifecycle
// -----------------------------------------------------------------------

// OnSessionConnect logs the first session for a project. Implements
// module.ProjectLifecycle. Behaviour ported from engramHandler.OnProjectConnect.
func (m *Module) OnSessionConnect(p muxcore.ProjectContext) {
	// Trigger slug resolution eagerly so the first tools/call does not pay
	// the git I/O cost. Ignore the return value — the cache owns it.
	_ = m.cache.Resolve(p)
	if m.deps.Logger != nil {
		m.deps.Logger.Info("session connected",
			"project_id", p.ID,
			"cwd", p.Cwd,
		)
	}
}

// OnSessionDisconnect is a no-op log. Per design.md §3.3, modules MUST NOT
// cancel long-running tasks on session disconnect — tasks outlive sessions.
// Implements module.ProjectLifecycle.
func (m *Module) OnSessionDisconnect(projectID string) {
	if m.deps.Logger != nil {
		m.deps.Logger.Info("session disconnected", "project_id", projectID)
	}
}

// -----------------------------------------------------------------------
// ProjectRemovalAware
// -----------------------------------------------------------------------

// OnProjectRemoved clears the slug cache for the removed project. In Phase B
// this callback is driven by the serverevents bridge subscribing to the
// engram-server ProjectEvents stream; in v4.3.0 the bridge is a stub per P003
// outcome (b) but the handler is implemented so unit tests can exercise it
// via moduletest.Harness.SimulateProjectRemoved. Implements
// module.ProjectRemovalAware.
func (m *Module) OnProjectRemoved(projectID string) {
	m.cache.Forget(projectID)
	if m.deps.Logger != nil {
		m.deps.Logger.Info("project removed — cleared slug cache",
			"project_id", projectID,
		)
	}
	// Note: gRPC connections are NOT closed here. They are pooled by
	// (addr, tls mode) not by project — every project shares the same
	// connection to the engram server, so closing on per-project removal
	// would be wrong.
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// envFor returns the value of an env variable for a given project context,
// preferring session-scoped env over host env. Mirrors envOrDefault but is
// kept as a method for clarity at the call site.
func (m *Module) envFor(p muxcore.ProjectContext, key string) string {
	return envOrDefault(p.Env, key)
}

// requireServerURL returns the configured server URL for the session, preferring
// ENGRAM_URL and falling back to the supported ENGRAM_SERVER_URL alias. Extracted
// so ProxyTools, ProxyHandleTool, and code indexing share identical validation.
func (m *Module) requireServerURL(p muxcore.ProjectContext) (string, error) {
	serverURL := m.envFor(p, config.EnvServerURL)
	if serverURL == "" {
		serverURL = m.envFor(p, config.EnvServerURLAlt)
	}
	if serverURL == "" {
		return "", fmt.Errorf("%s or %s not set for project %s", config.EnvServerURL, config.EnvServerURLAlt, p.ID)
	}
	return serverURL, nil
}
