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
	"os"
	"sync"

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
//   - module.ToolProvider       — explicit V3 project registration
//   - module.ProxyToolProvider  — dynamic tool set fetched from engram-server
//
// The static registration tool is intentionally separate from the server-owned
// dynamic tool inventory: it is the only way an unbound V3 descriptor can
// establish a binding. All ordinary proxy operations remain resolve-only.
type Module struct {
	pool               *grpcPool
	cache              *slugCache
	advisorProofs      *advisorProofCache
	v3ClientInstanceID string
	deps               module.ModuleDeps

	preparedIndexMu            sync.RWMutex
	preparedIndex              PreparedIndexCollaborator
	preparedIndexConfiguration *PreparedIndexConfiguration
	shuttingDown               bool
}

var (
	_ module.EngramModule        = (*Module)(nil)
	_ module.ProjectLifecycle    = (*Module)(nil)
	_ module.ProjectRemovalAware = (*Module)(nil)
	_ module.ToolProvider        = (*Module)(nil)
	_ module.ProxyToolProvider   = (*Module)(nil)
)

// NewModule constructs an unstarted V2-compatible engramcore module. Call Init
// before HandleTool / ProxyTools.
func NewModule() *Module {
	return NewModuleWithClientInstanceID("")
}

// NewModuleWithClientInstanceID constructs a module that submits V3
// descriptors for every scoped proxy operation. The client instance reference
// is configured by daemon wiring; identity resolution never generates it.
func NewModuleWithClientInstanceID(clientInstanceID string) *Module {
	return newModule(clientInstanceID, nil)
}

// NewModuleWithPreparedIndexCollaborator constructs a module with the narrow
// UCI server-binding/prepared-index seam. A nil collaborator still permits
// server-authorized resolution and status proxying; only prepared local index
// work returns SOURCE_UNAVAILABLE until a scanner owner is supplied.
func NewModuleWithPreparedIndexCollaborator(clientInstanceID string, collaborator PreparedIndexCollaborator) *Module {
	return newModule(clientInstanceID, collaborator)
}

func newModule(clientInstanceID string, collaborator PreparedIndexCollaborator) *Module {
	return &Module{
		pool:               &grpcPool{},
		cache:              &slugCache{},
		advisorProofs:      newAdvisorProofCache(),
		v3ClientInstanceID: clientInstanceID,
		preparedIndex:      collaborator,
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
	m.preparedIndexMu.Lock()
	m.shuttingDown = true
	m.preparedIndexMu.Unlock()
	m.pool.closeAll()
	m.advisorProofs.clear()
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
	if m.v3ClientInstanceID == "" {
		// V2 compatibility eagerly resolves the slug. V3 must not resolve or
		// retain a selector before central resolution.
		_ = m.cache.Resolve(p)
	} else {
		m.cache.Forget(p.ID)
	}
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

// requireServerURL returns the configured server URL using session-first
// precedence across both supported names: session ENGRAM_URL, session
// ENGRAM_SERVER_URL, host ENGRAM_URL, then host ENGRAM_SERVER_URL. Extracted so
// ProxyTools, ProxyHandleTool, and code indexing share identical validation.
func (m *Module) requireServerURL(p muxcore.ProjectContext) (string, error) {
	if serverURL := p.Env[config.EnvServerURL]; serverURL != "" {
		return serverURL, nil
	}
	if serverURL := p.Env[config.EnvServerURLAlt]; serverURL != "" {
		return serverURL, nil
	}
	if serverURL := os.Getenv(config.EnvServerURL); serverURL != "" {
		return serverURL, nil
	}
	if serverURL := os.Getenv(config.EnvServerURLAlt); serverURL != "" {
		return serverURL, nil
	}
	return "", fmt.Errorf("%s or %s not set for project %s", config.EnvServerURL, config.EnvServerURLAlt, p.ID)
}
