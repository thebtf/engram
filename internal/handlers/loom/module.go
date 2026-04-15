// Package loom is the loom tenant of the engram modular daemon framework.
// v4.4.0 is a plumbing-only landing: the module coexists with engramcore as a
// second registered tenant, opens tasks.db, wires the EventBus, and performs
// crash recovery. No MCP tools are exposed and no workers are registered in
// this release; those land in a follow-up PR.
package loom

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"

	loom "github.com/thebtf/aimux/loom"
	"github.com/thebtf/engram/internal/module"
	muxcore "github.com/thebtf/mcp-mux/muxcore"

	// Register the pure-Go SQLite driver so sql.Open("sqlite", ...) works.
	_ "modernc.org/sqlite"
)

// compile-time interface assertions — fail at build time if Module drifts from
// the framework contracts.
var (
	_ module.EngramModule        = (*Module)(nil)
	_ module.ProjectLifecycle    = (*Module)(nil)
	_ module.ProjectRemovalAware = (*Module)(nil)
	_ module.Snapshotter         = (*Module)(nil)
)

const moduleName = "loom"

// Module is the loom tenant of the engram modular daemon framework.
// It owns a SQLite DB at ${StorageDir}/tasks.db, delegates all task work to
// the embedded loom engine, and forwards task lifecycle events to connected
// sessions as JSON-RPC notifications.
//
// v4.4.0 constraints: no MCP tools exposed, no workers registered.
// Full tools + workers land in the follow-up PR.
type Module struct {
	engine   loomEngine
	db       *sql.DB
	unsub    func()
	deps     module.ModuleDeps
	notifier muxcore.Notifier

	// engineOverride is non-nil only in tests. When set, Init skips the
	// sql.Open + NewEngine path and wires this engine directly.
	engineOverride loomEngine

	// tracked maps projectID → struct{} for every connected project.
	// Used during Shutdown to issue best-effort CancelAllForProject sweeps
	// before closing the DB.
	tracked sync.Map
}

// NewModule constructs an unstarted Module. Call Init before any other method.
func NewModule() *Module {
	return &Module{}
}

// NewModuleWithEngine constructs a Module whose engine is pre-configured.
// Intended for testing only — production code uses NewModule(). Init still
// runs the full lifecycle (deps capture, event subscription, RecoverCrashed)
// but skips the sql.Open and PRAGMA setup.
func NewModuleWithEngine(eng loomEngine) *Module {
	return &Module{engineOverride: eng}
}

// -----------------------------------------------------------------------
// EngramModule
// -----------------------------------------------------------------------

// Name returns the stable module identifier. Implements module.EngramModule.
func (m *Module) Name() string { return moduleName }

// Init opens tasks.db, applies WAL pragmas, creates the loom engine, subscribes
// to the event bus, and runs crash recovery. Implements module.EngramModule.
//
// If PRAGMA application or engine creation fails, the DB is closed and an error
// is returned — daemon startup aborts per framework contract.
// RecoverCrashed failure is non-fatal: crash recovery is best-effort (a
// previous in-flight task ends up in failed_crash rather than being replayed).
func (m *Module) Init(ctx context.Context, deps module.ModuleDeps) error {
	m.deps = deps
	m.notifier = deps.Notifier

	var eng loomEngine
	if m.engineOverride != nil {
		// Test path: skip DB creation and use the injected engine directly.
		eng = m.engineOverride
	} else {
		dbPath := filepath.Join(deps.StorageDir, "tasks.db")
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return fmt.Errorf("loom: open tasks.db: %w", err)
		}

		// Apply recommended SQLite durability settings before any schema work.
		pragmas := []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA synchronous=NORMAL",
			"PRAGMA busy_timeout=5000",
		}
		for _, p := range pragmas {
			if err := ctx.Err(); err != nil {
				_ = db.Close()
				return fmt.Errorf("loom: init canceled: %w", err)
			}
			if _, err := db.ExecContext(ctx, p); err != nil {
				_ = db.Close()
				return fmt.Errorf("loom: %s: %w", p, err)
			}
		}

		loomEng, err := loom.NewEngine(db, loom.WithLogger(deps.Logger))
		if err != nil {
			_ = db.Close()
			return fmt.Errorf("loom: create engine: %w", err)
		}
		m.db = db
		eng = loomEng
	}

	m.engine = eng

	// Subscribe to task events before RecoverCrashed so crash-recovery events
	// are forwarded to any connected sessions that registered during Init.
	m.unsub = eng.Events().Subscribe(m.handleTaskEvent)

	// RecoverCrashed must run once at startup to mark stale dispatched/running
	// tasks as failed_crash. Failure is non-fatal — log and continue.
	if n, err := eng.RecoverCrashed(); err != nil {
		deps.Logger.WarnContext(ctx, "loom: crash recovery failed",
			"error", err,
		)
	} else if n > 0 {
		deps.Logger.InfoContext(ctx, "loom: crash recovery complete",
			"recovered", n,
		)
	}

	return nil
}

// Shutdown unsubscribes from the event bus, cancels tracked project tasks,
// and closes the DB. Implements module.EngramModule.
//
// Shutdown sequence per design.md §2.1:
//  1. unsub — stops further event delivery to avoid writes after DB close.
//  2. CancelAllForProject for each tracked project — reduces straggling tasks.
//  3. db.Close — any remaining dispatch goroutines finish their next store
//     call with a "database closed" error and the task transitions to failed.
func (m *Module) Shutdown(ctx context.Context) error {
	if m.unsub != nil {
		m.unsub()
	}

	if m.engine != nil {
		m.tracked.Range(func(key, _ any) bool {
			select {
			case <-ctx.Done():
				return false
			default:
			}
			projectID, _ := key.(string)
			_, _ = m.engine.CancelAllForProject(projectID)
			return true
		})
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	if m.db != nil {
		return m.db.Close()
	}
	return nil
}

// -----------------------------------------------------------------------
// ProjectLifecycle
// -----------------------------------------------------------------------

// OnSessionConnect records the project as active. Tasks submitted via this
// project outlive the session — no task work is started here.
// Implements module.ProjectLifecycle.
func (m *Module) OnSessionConnect(p muxcore.ProjectContext) {
	m.tracked.Store(p.ID, struct{}{})
	m.deps.Logger.InfoContext(m.deps.DaemonCtx, "loom: session connected",
		"project_id", p.ID,
	)
}

// OnSessionDisconnect logs the disconnect. Per design.md §3.3, modules MUST
// NOT cancel long-running tasks on session disconnect — tasks outlive sessions.
// Implements module.ProjectLifecycle.
func (m *Module) OnSessionDisconnect(projectID string) {
	m.deps.Logger.InfoContext(m.deps.DaemonCtx, "loom: session disconnected",
		"project_id", projectID,
	)
}

// -----------------------------------------------------------------------
// ProjectRemovalAware
// -----------------------------------------------------------------------

// OnProjectRemoved cancels all running tasks for the project and removes it
// from the tracked set. Implements module.ProjectRemovalAware.
func (m *Module) OnProjectRemoved(projectID string) {
	if m.engine != nil {
		if n, err := m.engine.CancelAllForProject(projectID); err != nil {
			m.deps.Logger.WarnContext(m.deps.DaemonCtx, "loom: cancel tasks on project removal failed",
				"project_id", projectID,
				"error", err,
			)
		} else {
			m.deps.Logger.InfoContext(m.deps.DaemonCtx, "loom: cancelled tasks for removed project",
				"project_id", projectID,
				"cancelled", n,
			)
		}
	}
	m.tracked.Delete(projectID)
}

// -----------------------------------------------------------------------
// Snapshotter
// -----------------------------------------------------------------------

// Snapshot returns nil — all loom state lives in tasks.db which persists on
// disk without snapshot involvement. Implements module.Snapshotter.
func (m *Module) Snapshot() ([]byte, error) {
	return nil, nil
}

// Restore is a no-op — state is recovered from tasks.db via RecoverCrashed in
// Init, not from the snapshot pipeline. Implements module.Snapshotter.
func (m *Module) Restore(_ []byte) error {
	return nil
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// buildNotificationPayload serialises the event into a JSON-RPC notification
// body ready to pass to muxcore.Notifier.Notify.
func buildNotificationPayload(method string, params any) ([]byte, error) {
	type notification struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(notification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  raw,
	})
}
