// Package engramcore is the first tenant of the modular daemon framework.
// It wraps the legacy engramHandler (a transparent MCP→gRPC proxy to the
// engram server) as an EngramModule + ProjectLifecycle + ProjectRemovalAware
// + ProxyToolProvider.
//
// Design reference: design.md §4.2 (US2 first tenant migration) and spec.md
// The backend owns the live catalog. This client also exposes the same
// source-owned schemas with explicit unverified-availability notices during
// backend outages, and requests rediscovery after recovery.
//
// Constraints:
//   - Preserve the server's live descriptors and authorization outcomes.
//   - Guarded calls wait only within the caller and discovery bounds for
//     authenticated discovery; local tool listing remains nonblocking.
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
//   - module.ProjectRemovalAware — project removal lifecycle logging
//   - module.ProxyToolProvider  — dynamic tool set fetched from engram-server
//   - module.ToolProvider       — local caller project-context recovery
type Module struct {
	pool                        *grpcPool
	advisorProofs               *advisorProofCache
	v3ClientInstanceID          string
	registrationParserAvailable bool
	deps                        module.ModuleDeps
	refreshMu                   sync.Mutex
	refreshing                  map[advisorProofKey]map[string]bool
	liveCatalog                 map[advisorProofKey]discoveredCatalog
	discoveryGeneration         map[advisorProofKey]uint64
	discoverySignals            map[advisorProofKey]chan struct{}
	discoveryUpdates            map[advisorProofKey]chan struct{}
	terminalDiscovery           map[advisorProofKey]bool
	refreshWG                   sync.WaitGroup
	notifier                    muxcore.Notifier
	refreshCtx                  context.Context
	refreshCancel               context.CancelFunc

	preparedIndexMu            sync.RWMutex
	preparedIndex              PreparedIndexCollaborator
	preparedIndexConfiguration *PreparedIndexConfiguration
	shuttingDown               bool
}

var (
	_ module.EngramModule        = (*Module)(nil)
	_ module.ProjectLifecycle    = (*Module)(nil)
	_ module.ProjectRemovalAware = (*Module)(nil)
	_ module.ProxyToolProvider   = (*Module)(nil)
	_ module.ToolProvider        = (*Module)(nil)
)

// NewModule constructs an unstarted engramcore module. Call Init before
// ProxyHandleTool / ProxyTools.
func NewModule() *Module {
	return NewModuleWithClientInstanceID("")
}

// NewModuleWithClientInstanceID constructs a module with the installation
// reference configured by daemon wiring for UCI transport metadata.
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
	m.refreshMu.Lock()
	parent := deps.DaemonCtx
	if parent == nil {
		parent = context.Background()
	}
	m.refreshCtx, m.refreshCancel = context.WithCancel(parent)
	m.refreshing = make(map[advisorProofKey]map[string]bool)
	m.liveCatalog = make(map[advisorProofKey]discoveredCatalog)
	m.discoveryGeneration = make(map[advisorProofKey]uint64)
	m.discoverySignals = make(map[advisorProofKey]chan struct{})
	m.discoveryUpdates = make(map[advisorProofKey]chan struct{})
	m.terminalDiscovery = make(map[advisorProofKey]bool)
	m.refreshMu.Unlock()
	m.deps = deps
	if deps.Logger != nil {
		deps.Logger.Info("engramcore module initialised",
			"storage_dir", deps.StorageDir,
			"config_bytes", len(deps.Config),
		)
	}
	return nil
}

// SetNotifier receives muxcore's scoped notifier through the dispatcher.
func (m *Module) SetNotifier(notifier muxcore.Notifier) {
	m.refreshMu.Lock()
	m.notifier = notifier
	m.refreshMu.Unlock()
}

// Shutdown closes all pooled gRPC connections. Implements module.EngramModule.
//
// Per design.md §4.1 shutdown proceeds in reverse registration order; this
// module is typically registered first so it drains last. Closing gRPC
// connections is idempotent so concurrent Shutdown calls are safe.
func (m *Module) Shutdown(_ context.Context) error {
	m.refreshMu.Lock()
	if m.refreshCancel != nil {
		m.refreshCancel()
	}
	m.refreshMu.Unlock()
	m.refreshWG.Wait()
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

// OnProjectRemoved records project removal without closing the shared connection
// pool. Implements module.ProjectRemovalAware.
func (m *Module) OnProjectRemoved(projectID string) {
	if m.deps.Logger != nil {
		m.deps.Logger.Info("project removed",
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
