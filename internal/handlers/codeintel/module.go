// Package codeintel is the code intelligence tenant of the engram modular daemon
// framework. It exposes three MCP tools when ENGRAM_CODE_INTEL_ENABLED=true:
//
//   - codebase_index: triggers an async code index run on a project root.
//   - codebase_status: reports index liveness (daemon-side) merged with server-side
//     chunk counts via the engramcore gRPC proxy.
//
// codebase_search is registered on the SERVER side (internal/mcp/tools_code_intel.go)
// because it needs direct access to the gorm.CodeChunkStore in the server process.
// This module provides only codebase_index and codebase_status (liveness layer).
//
// # Architecture (daemon-side)
//
// The module owns an in-memory sync.Map of per-project indexState values that track
// running/idle/error state for each project. HandleTool is synchronous and bounded
// <1s: codebase_index spawns a background goroutine (DaemonCtx-scoped) and returns
// immediately with {status:"started",run_id:<id>}.
//
// # codebase_status design decision (V1)
//
// This module's codebase_status returns ONLY daemon-side liveness (status/run_id/error).
// Chunk counts (total_chunks/embedded_chunks/last_indexed_at) come from the server-side
// codebase_status handler via the engramcore ProxyHandleTool call. The daemon merges
// both payloads and returns the combined result.
//
// If the server-side proxy call fails (network blip, flag-off on server), the daemon
// returns the liveness-only payload with a note that server counts are unavailable.
// This is acceptable V1 behaviour — the index process state is always authoritative
// from the daemon.
//
// # Concurrency
//
// sync.Map is used for indexStates so no global lock is needed for tool calls.
// Per-project CAS (LoadOrStore) prevents double-indexing: a second codebase_index
// call for the same project while one is running returns {status:"already_running"}.
//
// CLEAN-ROOM: no AGPL source referenced during implementation.
package codeintel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/obs"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// compile-time interface assertions.
var (
	_ module.EngramModule = (*Module)(nil)
	_ module.ToolProvider = (*Module)(nil)
)

const moduleName = "codeintel"

// runCounter is an atomic counter used to generate monotonically-increasing
// run IDs within the daemon lifetime. Using a counter instead of UUID/time
// keeps run IDs small and avoids importing additional packages.
var runCounter atomic.Int64

// indexState holds the per-project index run state.
// The Status field uses string constants: statusNeverIndexed, statusRunning,
// statusIdle, statusError.
type indexState struct {
	StartedAt time.Time
	Err       string
	RunID     string
	Status    string
}

const (
	statusRunning = "running"
	statusIdle    = "idle"
	statusError   = "error"
)

// CoreProvider is the interface the codeintel module uses from the engramcore
// module. Defined as an interface here so tests can supply a fake without
// importing engramcore (which would require a live gRPC server).
//
// *engramcore.Module satisfies this interface. Tests use a local fakeCore.
type CoreProvider interface {
	// IndexCodebase triggers a full code index run via the gRPC server.
	IndexCodebase(ctx context.Context, p muxcore.ProjectContext, root string) (*IndexResult, error)
	// ProxyHandleTool forwards a tool call to the engram server via gRPC.
	// Used to fetch server-side codebase_status chunk counts.
	ProxyHandleTool(ctx context.Context, p muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error)
}

// IndexResult mirrors engramcore.CodeIndexResult, re-declared here so the
// codeintel package can expose it without importing engramcore (avoiding a
// circular dependency in tests).
//
// The adapter in module.go converts *engramcore.CodeIndexResult to *IndexResult
// when the concrete *engramcore.Module is wired. For the test fakeCore the
// conversion is not needed — fakeCore returns *IndexResult directly.
type IndexResult struct {
	Embedded int
	Deleted  int
	Uploaded int
	Errors   []string
}

// engramCoreAdapter wraps *engramcore.Module to satisfy CoreProvider.
// This adapter is used only when the real engramcore module is wired
// (production path). The conversion from CodeIndexResult → IndexResult is
// trivial and allocation-cheap (a struct copy).
type engramCoreAdapter struct {
	m *engramcore.Module
}

func (a *engramCoreAdapter) IndexCodebase(ctx context.Context, p muxcore.ProjectContext, root string) (*IndexResult, error) {
	r, err := a.m.IndexCodebase(ctx, p, root)
	if err != nil {
		return nil, err
	}
	return &IndexResult{
		Embedded: r.Embedded,
		Deleted:  r.Deleted,
		Uploaded: r.Uploaded,
		Errors:   r.Errors,
	}, nil
}

func (a *engramCoreAdapter) ProxyHandleTool(ctx context.Context, p muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error) {
	return a.m.ProxyHandleTool(ctx, p, name, args)
}

// Module is the codeintel tenant of the engram modular daemon framework.
// It implements module.EngramModule and module.ToolProvider.
type Module struct {
	core CoreProvider
	deps module.ModuleDeps
	// indexStates maps projectID → *indexState. Reads (handleStatus) use the
	// lock-free sync.Map fast path. The admit-or-reject transition in handleIndex
	// is serialised by startMu because LoadOrStore alone cannot make the
	// "idle/error → running" replacement atomic: two concurrent callers could
	// both observe a non-running state and both spawn a goroutine. startMu closes
	// that TOCTOU window so at most one index goroutine runs per project.
	indexStates sync.Map
	startMu     sync.Mutex
}

// NewModule constructs an unstarted Module backed by a real *engramcore.Module.
// core MUST be the engramcore module registered in the same daemon — it is used
// to call IndexCodebase and to proxy the server-side codebase_status for chunk
// counts.
//
// NewModule does NOT register the module; call cmd/engram/wiring.go's
// registerModules to place it in the registry.
func NewModule(core *engramcore.Module) *Module {
	return &Module{core: &engramCoreAdapter{m: core}}
}

// NewModuleWithCore constructs an unstarted Module backed by any CoreProvider.
// Used in tests to inject a fake core without importing engramcore.
func NewModuleWithCore(core CoreProvider) *Module {
	return &Module{core: core}
}

// -----------------------------------------------------------------------
// EngramModule
// -----------------------------------------------------------------------

// Name returns the stable module identifier.
func (m *Module) Name() string { return moduleName }

// Init captures ModuleDeps for later use. No blocking initialisation.
func (m *Module) Init(_ context.Context, deps module.ModuleDeps) error {
	m.deps = deps
	if deps.Logger != nil {
		deps.Logger.Info("codeintel module initialised")
	}
	return nil
}

// Shutdown is a no-op: background goroutines are bound to DaemonCtx, which is
// cancelled by the framework before Shutdown is called, so they will exit on
// their own. We do not wait for them here to keep shutdown fast (<1 s).
func (m *Module) Shutdown(_ context.Context) error {
	if m.deps.Logger != nil {
		m.deps.Logger.Info("codeintel module shut down")
	}
	return nil
}

// -----------------------------------------------------------------------
// ToolProvider
// -----------------------------------------------------------------------

// Tools returns the static tool definitions for codebase_index and
// codebase_status. Called once at registration time.
func (m *Module) Tools() []module.ToolDef {
	indexSchema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"root": map[string]any{
				"type":        "string",
				"description": "Absolute path to the project root to index. Defaults to the current working directory of the session.",
			},
		},
	})
	statusSchema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"project": map[string]any{
				"type":        "string",
				"description": "Project ID (defaults to the current session project)",
			},
		},
	})

	return []module.ToolDef{
		{
			Name:        "codebase_index",
			Description: "Trigger an async code index run for the current project. Returns immediately with a run_id. Poll codebase_status to track progress. Requires ENGRAM_CODE_INTEL_ENABLED=true.",
			InputSchema: indexSchema,
		},
		{
			Name:        "codebase_status",
			Description: "Report the code index status for the current project: run state, run_id, chunk counts, and last indexed time. Requires ENGRAM_CODE_INTEL_ENABLED=true.",
			InputSchema: statusSchema,
		},
	}
}

// HandleTool dispatches to the appropriate handler. Synchronous, bounded <1s.
func (m *Module) HandleTool(ctx context.Context, p muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error) {
	if os.Getenv("ENGRAM_CODE_INTEL_ENABLED") != "true" {
		return nil, fmt.Errorf("tool %q requires ENGRAM_CODE_INTEL_ENABLED=true", name)
	}
	switch name {
	case "codebase_index":
		return m.handleIndex(ctx, p, args)
	case "codebase_status":
		return m.handleStatus(ctx, p, args)
	default:
		return nil, fmt.Errorf("codeintel: unknown tool %q", name)
	}
}

// -----------------------------------------------------------------------
// handleIndex
// -----------------------------------------------------------------------

// handleIndex implements the codebase_index tool. Returns {status:"started",
// run_id:<id>} immediately after spawning a background goroutine. If an index
// run is already in progress for this project, returns {status:"already_running",
// run_id:<id>} without spawning a second goroutine.
func (m *Module) handleIndex(_ context.Context, p muxcore.ProjectContext, args json.RawMessage) (json.RawMessage, error) {
	var params struct {
		Root string `json:"root"`
	}
	if args != nil {
		_ = json.Unmarshal(args, &params)
	}
	root := params.Root
	if root == "" {
		root = p.Cwd
	}
	if root == "" {
		return nil, fmt.Errorf("codebase_index: root is required (set via 'root' param or ensure session has a Cwd)")
	}

	projectID := p.ID
	if projectID == "" {
		return nil, fmt.Errorf("codebase_index: project ID missing from session context")
	}

	// Admit-or-reject under startMu so the "is one already running? if not, mark
	// running" decision is atomic. sync.Map.LoadOrStore cannot do this alone: it
	// is atomic only for the absent→present insert, not for the idle/error→running
	// replacement, so two callers racing on a just-finished project could both be
	// admitted. startMu is held only for the O(1) state check + store, never
	// across the index goroutine, so it does not serialise across projects in any
	// meaningful way.
	newRunID := fmt.Sprintf("run-%d", runCounter.Add(1))
	newState := &indexState{
		Status:    statusRunning,
		RunID:     newRunID,
		StartedAt: time.Now(),
	}

	m.startMu.Lock()
	if raw, ok := m.indexStates.Load(projectID); ok {
		if existing := raw.(*indexState); existing.Status == statusRunning {
			// Another goroutine is already indexing this project — reject.
			m.startMu.Unlock()
			out, _ := json.Marshal(map[string]any{
				"status": "already_running",
				"run_id": existing.RunID,
			})
			return out, nil
		}
	}
	// No run in progress (absent, idle, or errored): claim the slot.
	m.indexStates.Store(projectID, newState)
	m.startMu.Unlock()

	// Capture what we need in the closure; do not capture m.deps.DaemonCtx
	// via the call-site ctx (which is session-scoped and will be cancelled
	// before the goroutine finishes).
	daemonCtx := m.deps.DaemonCtx
	logger := m.deps.Logger
	core := m.core
	runID := newRunID
	states := &m.indexStates

	go func() {
		defer func() {
			if r := recover(); r != nil {
				obs.RecordRuntimeEvent(daemonCtx, "index", "panic")
				// Panic recovery: log stack and mark state as error.
				if logger != nil {
					logger.Error("codeintel: index goroutine panicked",
						"project_id", projectID,
						"run_id", runID,
						"panic", fmt.Sprintf("%v", r),
						"stack", string(debug.Stack()),
					)
				}
				errState := &indexState{
					Status:    statusError,
					RunID:     runID,
					StartedAt: newState.StartedAt,
					Err:       fmt.Sprintf("panic: %v", r),
				}
				states.Store(projectID, errState)
			}
		}()

		if logger != nil {
			logger.Info("codeintel: starting index run",
				"project_id", projectID,
				"run_id", runID,
				"root", root,
			)
		}

		result, err := core.IndexCodebase(daemonCtx, p, root)

		if err != nil {
			obs.RecordRuntimeEvent(daemonCtx, "index", "run_error")
			if logger != nil {
				logger.Error("codeintel: index run failed",
					"project_id", projectID,
					"run_id", runID,
					"error", err.Error(),
				)
			}
			errState := &indexState{
				Status:    statusError,
				RunID:     runID,
				StartedAt: newState.StartedAt,
				Err:       err.Error(),
			}
			states.Store(projectID, errState)
			return
		}

		if logger != nil {
			logger.Info("codeintel: index run complete",
				"project_id", projectID,
				"run_id", runID,
				"uploaded", result.Uploaded,
				"embedded", result.Embedded,
				"deleted", result.Deleted,
			)
		}
		idleState := &indexState{
			Status:    statusIdle,
			RunID:     runID,
			StartedAt: newState.StartedAt,
		}
		states.Store(projectID, idleState)
	}()

	out, _ := json.Marshal(map[string]any{
		"status": "started",
		"run_id": newRunID,
	})
	return out, nil
}

// -----------------------------------------------------------------------
// handleStatus
// -----------------------------------------------------------------------

// handleStatus implements the codebase_status tool. It merges:
//  1. Daemon-side liveness from m.indexStates (status / run_id / error).
//  2. Server-side chunk counts fetched via the engramcore proxy.
//
// If the server-side call fails, the daemon returns liveness-only with a
// note that server counts are unavailable. This is the V1 acceptable fallback.
func (m *Module) handleStatus(ctx context.Context, p muxcore.ProjectContext, args json.RawMessage) (json.RawMessage, error) {
	projectID := p.ID
	if projectID == "" {
		// Try args.project as a fallback.
		var params struct {
			Project string `json:"project"`
		}
		if args != nil {
			_ = json.Unmarshal(args, &params)
		}
		projectID = params.Project
	}
	if projectID == "" {
		return nil, fmt.Errorf("codebase_status: project ID missing from session context")
	}

	// --- Daemon-side liveness ---
	result := map[string]any{
		"project": projectID,
		"status":  "never_indexed",
	}
	if raw, ok := m.indexStates.Load(projectID); ok {
		state := raw.(*indexState)
		result["status"] = state.Status
		result["run_id"] = state.RunID
		if state.Err != "" {
			result["error"] = state.Err
		}
	}

	// --- Server-side counts via engramcore proxy ---
	// We call the server's codebase_status tool via ProxyHandleTool. This
	// round-trip is acceptable because HandleTool has a 30 s budget from the
	// dispatcher, and the proxy call should complete in <1 s on a healthy
	// network. If it fails we degrade gracefully.
	statusArgs, _ := json.Marshal(map[string]any{"project": projectID})
	serverRaw, proxyErr := m.core.ProxyHandleTool(ctx, p, "codebase_status", statusArgs)
	if proxyErr != nil {
		// Degraded: return liveness only.
		result["server_counts_available"] = false
		result["server_counts_error"] = proxyErr.Error()
	} else if serverRaw != nil {
		// Merge server payload into our result. The server returns a JSON object;
		// we extract the fields we care about.
		var serverPayload map[string]any
		// serverRaw is the MCP inner block: {"type":"text","text":"<json>"}
		// ProxyHandleTool returns the raw block; we need to extract the text.
		var block struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(serverRaw, &block); err == nil && block.Text != "" {
			if err2 := json.Unmarshal([]byte(block.Text), &serverPayload); err2 == nil {
				for _, key := range []string{"total_chunks", "embedded_chunks", "last_indexed_at"} {
					if v, exists := serverPayload[key]; exists {
						result[key] = v
					}
				}
				result["server_counts_available"] = true
			} else {
				result["server_counts_available"] = false
				result["server_counts_error"] = "failed to parse server response"
			}
		} else {
			// serverRaw might already be the text directly (not wrapped in a block)
			// if called from a test context. Try to unmarshal directly.
			if err3 := json.Unmarshal(serverRaw, &serverPayload); err3 == nil {
				for _, key := range []string{"total_chunks", "embedded_chunks", "last_indexed_at"} {
					if v, exists := serverPayload[key]; exists {
						result[key] = v
					}
				}
				result["server_counts_available"] = true
			} else {
				result["server_counts_available"] = false
				result["server_counts_error"] = "failed to parse server response"
			}
		}
	}

	out, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("codebase_status: marshal: %w", err)
	}
	return out, nil
}
