// Package codeintel is the code intelligence tenant of the engram modular daemon
// framework. It exposes three MCP tools when ENGRAM_CODE_INTEL_ENABLED=true:
//
//   - codebase_index: triggers an async code index run on a project root.
//   - codebase_status: reports index liveness (daemon-side) merged with server-side
//     chunk counts via the engramcore gRPC proxy.
//
// codebase_search is registered on the SERVER side. This module owns only
// codebase_index and codebase_status: daemon liveness plus scoped status proxying.
//
// # Architecture (daemon-side)
//
// The module owns an in-memory sync.Map of per-resolved-target indexState
// values. A target is bound to one client transport and checkout incarnation.
// HandleTool resolves that target synchronously, then codebase_index starts
// daemon-scoped work and returns immediately.
//
// # codebase_status design decision
//
// This module reports daemon-side liveness and merges server-side scoped status
// through the typed engramcore proxy. Server failure degrades only the server
// payload; the daemon state remains authoritative for the resolved target.
//
// # Concurrency
//
// The short admission critical section is keyed by the client transport plus
// server-authorized source, checkout incarnation, and profile.
//
// CLEAN-ROOM: no AGPL source referenced during implementation.
package codeintel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/uci"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// compile-time interface assertions.
var (
	_ module.EngramModule = (*Module)(nil)
	_ module.ToolProvider = (*Module)(nil)
)

const (
	moduleName = "codeintel"

	codebaseStatusAfterBarrierMaxTokenLength       = 2_048
	codebaseStatusAfterBarrierMaxWaitMS      int64 = 60_000
)

// runCounter is an atomic counter used to generate monotonically-increasing
// run IDs within the daemon lifetime. Using a counter instead of UUID/time
// keeps run IDs small and avoids importing additional packages.
var runCounter atomic.Int64

// indexStateKey isolates state by exact client transport and server-authorized
// source, checkout incarnation, and profile. A publication from no View to a
// real View preserves the same daemon liveness entry.
type indexStateKey struct {
	ClientSessionID    string
	ScopeSourceID      string
	ScopeCheckoutID    string
	ScopeIncarnationID string
	ProfileID          string
}

// indexState holds one resolved target's index run state.
// The Status field uses string constants: statusRunning, statusIdle, statusError.
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

// ResolvedIndexTarget and IndexResult are aliases for the canonical typed
// engramcore contract. They remain visible here so codeintel tests can inject
// fakes without importing a concrete daemon module.
type (
	ResolvedIndexTarget = engramcore.ResolvedIndexTarget
	IndexResult         = engramcore.IndexResult
)

// CoreProvider is the typed engramcore contract consumed by codeintel.
// *engramcore.UCIIndexAdapter satisfies this interface; tests may inject a fake.
type CoreProvider interface {
	ResolveIndexTarget(ctx context.Context, p muxcore.ProjectContext, contextHandle string) (ResolvedIndexTarget, error)
	IndexCodebase(ctx context.Context, target ResolvedIndexTarget, root string) (*IndexResult, error)
	ProxyHandleTool(ctx context.Context, target ResolvedIndexTarget, name string, args json.RawMessage) (json.RawMessage, error)
}

// engramCoreAdapter keeps codeintel dependent on the narrow typed contract.
type engramCoreAdapter struct {
	adapter *engramcore.UCIIndexAdapter
}

func (a *engramCoreAdapter) ResolveIndexTarget(ctx context.Context, p muxcore.ProjectContext, contextHandle string) (ResolvedIndexTarget, error) {
	return a.adapter.ResolveIndexTarget(ctx, p, contextHandle)
}

func (a *engramCoreAdapter) IndexCodebase(ctx context.Context, target ResolvedIndexTarget, root string) (*IndexResult, error) {
	return a.adapter.IndexCodebase(ctx, target, root)
}

func (a *engramCoreAdapter) ProxyHandleTool(ctx context.Context, target ResolvedIndexTarget, name string, args json.RawMessage) (json.RawMessage, error) {
	return a.adapter.ProxyHandleTool(ctx, target, name, args)
}

// Module is the codeintel tenant of the engram modular daemon framework.
// It implements module.EngramModule and module.ToolProvider.
type Module struct {
	core    CoreProvider
	runtime *uciRuntime
	deps    module.ModuleDeps
	// indexStates maps an exact resolved target to its liveness state. startMu
	// protects only admission for a single state transition; index work itself
	// remains concurrent for disjoint target keys.
	indexStates sync.Map // indexStateKey -> *indexState
	startMu     sync.Mutex
}

// NewModule constructs an unstarted Module backed by a real *engramcore.Module.
// It preserves the existing injection-friendly constructor. Ordinary daemon
// wiring must use NewModuleWithRuntimeConfig so local SQLite/scanner resources
// are owned by this module before an index can reach engramcore.
func NewModule(core *engramcore.Module) *Module {
	return &Module{core: &engramCoreAdapter{adapter: engramcore.NewUCIIndexAdapter(core)}}
}

// NewModuleWithRuntimeConfig constructs the ordinary daemon composition. The
// runtime opens its owned local registry during Init and configures the already
// registered engramcore module before requests are dispatched.
func NewModuleWithRuntimeConfig(core *engramcore.Module, configuration UCIRuntimeConfig) (*Module, error) {
	runtime, err := newUCIRuntime(core, configuration)
	if err != nil {
		return nil, err
	}
	return &Module{
		core:    &engramCoreAdapter{adapter: engramcore.NewUCIIndexAdapter(core)},
		runtime: runtime,
	}, nil
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

// Init captures ModuleDeps and starts the production runtime when one was
// selected by daemon wiring. No request can observe codeintel before Init
// returns, so engramcore receives its prepared collaborator before indexing.
func (m *Module) Init(_ context.Context, deps module.ModuleDeps) error {
	m.deps = deps
	if m.runtime != nil {
		if err := m.runtime.Start(deps); err != nil {
			return fmt.Errorf("initialise codeintel runtime: %w", err)
		}
	}
	if deps.Logger != nil {
		deps.Logger.Info("codeintel module initialised")
	}
	return nil
}

// Shutdown stops watcher resources before closing the module-owned SQLite
// registry. Index goroutines use DaemonCtx and are cancelled by the framework.
func (m *Module) Shutdown(ctx context.Context) error {
	var shutdownErr error
	if m.runtime != nil {
		shutdownErr = m.runtime.Close(ctx)
	}
	if m.deps.Logger != nil {
		m.deps.Logger.Info("codeintel module shut down")
	}
	return shutdownErr
}

// -----------------------------------------------------------------------
// ToolProvider
// -----------------------------------------------------------------------

// Tools returns the static tool definitions for codebase_index and
// codebase_status. Called once at registration time.
func (m *Module) Tools() []module.ToolDef {
	indexSchema, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"context_handle"},
		"properties": map[string]any{
			"context_handle": map[string]any{
				"type":        "string",
				"description": "Opaque handle returned for this client by codebase_context.",
			},
			"root": map[string]any{
				"type":        "string",
				"description": "Optional local working root. Target authority comes only from context_handle.",
			},
		},
	})
	statusSchema, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"context_handle"},
		"properties": map[string]any{
			"context_handle": map[string]any{
				"type":        "string",
				"description": "Opaque handle returned for this client by codebase_context.",
			},
			"after_barrier": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"token", "wait_ms"},
				"properties": map[string]any{
					"token": map[string]any{
						"type":        "string",
						"description": "Opaque server-issued read-your-save barrier token.",
						"minLength":   1,
						"maxLength":   codebaseStatusAfterBarrierMaxTokenLength,
					},
					"wait_ms": map[string]any{
						"type":        "integer",
						"description": "Maximum barrier wait in milliseconds.",
						"minimum":     1,
						"maximum":     codebaseStatusAfterBarrierMaxWaitMS,
					},
				},
			},
		},
	})

	return []module.ToolDef{
		{
			Name:        "codebase_index",
			Description: "Trigger an async code index run for one resolved context. Returns immediately with a run_id. Poll codebase_status to track progress. Requires ENGRAM_CODE_INTEL_ENABLED=true.",
			InputSchema: indexSchema,
		},
		{
			Name:        "codebase_status",
			Description: "Report code index liveness and scoped server evidence for one resolved context. Requires ENGRAM_CODE_INTEL_ENABLED=true.",
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

type codebaseIndexArgs struct {
	ContextHandle *string `json:"context_handle"`
	Root          *string `json:"root"`
}

type codebaseStatusArgs struct {
	ContextHandle *string                         `json:"context_handle"`
	AfterBarrier  *codebaseStatusAfterBarrierArgs `json:"after_barrier"`
}

type codebaseStatusAfterBarrierArgs struct {
	Token  *string `json:"token"`
	WaitMS *int64  `json:"wait_ms"`
}

type codebaseStatusProxyArgs struct {
	ContextHandle string                      `json:"context_handle"`
	AfterBarrier  *codebaseStatusAfterBarrier `json:"after_barrier,omitempty"`
}

type codebaseStatusAfterBarrier struct {
	Token  string `json:"token"`
	WaitMS int64  `json:"wait_ms"`
}

func parseIndexArgs(args json.RawMessage) (string, string, error) {
	var parsed codebaseIndexArgs
	if err := decodeStrictToolArgs("codebase_index", args, &parsed); err != nil {
		return "", "", err
	}
	contextHandle, err := requiredContextHandle("codebase_index", parsed.ContextHandle)
	if err != nil {
		return "", "", err
	}
	if parsed.Root == nil {
		return contextHandle, "", nil
	}
	if *parsed.Root == "" {
		return "", "", fmt.Errorf("codebase_index: root must not be empty")
	}
	return contextHandle, *parsed.Root, nil
}

func parseStatusArgs(args json.RawMessage) (string, *codebaseStatusAfterBarrier, error) {
	var parsed codebaseStatusArgs
	if err := decodeStrictToolArgs("codebase_status", args, &parsed); err != nil {
		return "", nil, err
	}
	contextHandle, err := requiredContextHandle("codebase_status", parsed.ContextHandle)
	if err != nil {
		return "", nil, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(args), &fields); err != nil {
		return "", nil, fmt.Errorf("codebase_status: invalid args: %w", err)
	}
	if _, found := fields["after_barrier"]; !found {
		return contextHandle, nil, nil
	}
	if parsed.AfterBarrier == nil {
		return "", nil, fmt.Errorf("codebase_status: after_barrier must not be null")
	}
	if parsed.AfterBarrier.Token == nil || !validCodeintelIdentity(*parsed.AfterBarrier.Token, codebaseStatusAfterBarrierMaxTokenLength) {
		return "", nil, fmt.Errorf("codebase_status: invalid after_barrier token")
	}
	if parsed.AfterBarrier.WaitMS == nil || *parsed.AfterBarrier.WaitMS < 1 || *parsed.AfterBarrier.WaitMS > codebaseStatusAfterBarrierMaxWaitMS {
		return "", nil, fmt.Errorf("codebase_status: after_barrier wait_ms must be between 1 and %d", codebaseStatusAfterBarrierMaxWaitMS)
	}
	return contextHandle, &codebaseStatusAfterBarrier{
		Token:  *parsed.AfterBarrier.Token,
		WaitMS: *parsed.AfterBarrier.WaitMS,
	}, nil
}

func decodeStrictToolArgs(tool string, args json.RawMessage, target any) error {
	if len(bytes.TrimSpace(args)) == 0 {
		return fmt.Errorf("%s: context_handle is required", tool)
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%s: invalid args: %w", tool, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("%s: multiple JSON values", tool)
	}
	return nil
}

func requiredContextHandle(tool string, contextHandle *string) (string, error) {
	if contextHandle == nil || !validCodeintelIdentity(*contextHandle, 128) {
		return "", fmt.Errorf("%s: context_handle is required", tool)
	}
	return *contextHandle, nil
}

func clientSessionID(ctx context.Context) (string, error) {
	sessionID := auditcontext.UCITransportSession(ctx)
	if !auditcontext.ValidUCITransportSession(sessionID) {
		return "", fmt.Errorf("codeintel: UCI transport session is required")
	}
	return sessionID, nil
}

func validCodeintelIdentity(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func requestedTargetMatches(target ResolvedIndexTarget, clientSessionID, contextHandle string) bool {
	return target.ClientSessionID == clientSessionID && target.ContextHandle == contextHandle
}

func indexKeyFor(target ResolvedIndexTarget) indexStateKey {
	binding := target.BindingClone()
	return indexStateKey{
		ClientSessionID:    target.ClientSessionID,
		ScopeSourceID:      binding.Scope.SourceID,
		ScopeCheckoutID:    binding.Scope.CheckoutID,
		ScopeIncarnationID: binding.Scope.IncarnationID,
		ProfileID:          binding.ProfileID,
	}
}

func indexResultMatchesBinding(result *IndexResult, binding uci.IndexBinding) bool {
	if result == nil {
		return false
	}
	context := result.Context
	binding.Context = &context
	return binding.Validate() == nil
}

func decodeServerStatusPayload(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var block struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &block); err == nil && block.Text != "" {
		raw = json.RawMessage(block.Text)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, fmt.Errorf("failed to parse server response")
	}
	return payload, nil
}

// -----------------------------------------------------------------------
// handleIndex
// -----------------------------------------------------------------------

// handleIndex implements codebase_index. It resolves the client-owned target,
// then returns {status:"started",run_id:<id>} after starting daemon-scoped work.
// A second request is already_running only when it resolves to the same target.
func (m *Module) handleIndex(ctx context.Context, p muxcore.ProjectContext, args json.RawMessage) (json.RawMessage, error) {
	contextHandle, root, err := parseIndexArgs(args)
	if err != nil {
		return nil, err
	}
	clientSessionID, err := clientSessionID(ctx)
	if err != nil {
		return nil, err
	}
	if m.core == nil {
		return nil, fmt.Errorf("SOURCE_UNAVAILABLE: typed code index core is unavailable")
	}
	target, err := m.core.ResolveIndexTarget(ctx, p, contextHandle)
	if err != nil {
		return nil, err
	}
	if !requestedTargetMatches(target, clientSessionID, contextHandle) {
		return nil, fmt.Errorf("codebase_index: resolved target does not match the requesting client handle")
	}

	if root == "" {
		root = p.Cwd
	}
	if root == "" {
		return nil, fmt.Errorf("codebase_index: current session working directory is required")
	}
	if m.runtime != nil {
		if p.Cwd == "" {
			return nil, fmt.Errorf("codebase_index: selected session working directory is required")
		}
		root, err = m.runtime.Prepare(ctx, target, p.Cwd, root)
		if err != nil {
			return nil, fmt.Errorf("codebase_index: prepare authorized worktree: %w", err)
		}
	}

	key := indexKeyFor(target)
	newRunID := fmt.Sprintf("run-%d", runCounter.Add(1))
	newState := &indexState{
		Status:    statusRunning,
		RunID:     newRunID,
		StartedAt: time.Now(),
	}

	m.startMu.Lock()
	if raw, ok := m.indexStates.Load(key); ok {
		if existing := raw.(*indexState); existing.Status == statusRunning {
			m.startMu.Unlock()
			out, _ := json.Marshal(map[string]any{
				"status": "already_running",
				"run_id": existing.RunID,
			})
			return out, nil
		}
	}
	m.indexStates.Store(key, newState)
	m.startMu.Unlock()

	daemonCtx := m.deps.DaemonCtx
	if daemonCtx == nil {
		daemonCtx = context.Background()
	}
	daemonCtx = auditcontext.WithUCITransportSession(daemonCtx, clientSessionID)
	logger := m.deps.Logger
	core := m.core
	runID := newRunID
	states := &m.indexStates

	go func() {
		defer func() {
			if r := recover(); r != nil {
				if logger != nil {
					logger.Error("codeintel: index goroutine panicked",
						"client_session_id", target.ClientSessionID,
						"context_handle", target.ContextHandle,
						"run_id", runID,
						"panic", fmt.Sprintf("%v", r),
						"stack", string(debug.Stack()),
					)
				}
				states.Store(key, &indexState{
					Status:    statusError,
					RunID:     runID,
					StartedAt: newState.StartedAt,
					Err:       fmt.Sprintf("panic: %v", r),
				})
			}
		}()

		if logger != nil {
			logger.Info("codeintel: starting index run",
				"client_session_id", target.ClientSessionID,
				"context_handle", target.ContextHandle,
				"run_id", runID,
				"root", root,
			)
		}

		result, indexErr := core.IndexCodebase(daemonCtx, target, root)
		if indexErr != nil {
			if logger != nil {
				logger.Error("codeintel: index run failed",
					"client_session_id", target.ClientSessionID,
					"context_handle", target.ContextHandle,
					"run_id", runID,
					"error", indexErr.Error(),
				)
			}
			states.Store(key, &indexState{
				Status:    statusError,
				RunID:     runID,
				StartedAt: newState.StartedAt,
				Err:       indexErr.Error(),
			})
			return
		}
		if !indexResultMatchesBinding(result, target.BindingClone()) {
			err := fmt.Errorf("codebase_index: index result context does not match resolved target")
			if logger != nil {
				logger.Error("codeintel: index run rejected",
					"client_session_id", target.ClientSessionID,
					"context_handle", target.ContextHandle,
					"run_id", runID,
					"error", err.Error(),
				)
			}
			states.Store(key, &indexState{
				Status:    statusError,
				RunID:     runID,
				StartedAt: newState.StartedAt,
				Err:       err.Error(),
			})
			return
		}

		if logger != nil {
			logger.Info("codeintel: index run complete",
				"client_session_id", target.ClientSessionID,
				"context_handle", target.ContextHandle,
				"run_id", runID,
				"uploaded", result.Uploaded,
				"embedded", result.Embedded,
				"deleted", result.Deleted,
			)
		}
		states.Store(key, &indexState{
			Status:    statusIdle,
			RunID:     runID,
			StartedAt: newState.StartedAt,
		})
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

func (m *Module) handleStatus(ctx context.Context, p muxcore.ProjectContext, args json.RawMessage) (json.RawMessage, error) {
	contextHandle, afterBarrier, err := parseStatusArgs(args)
	if err != nil {
		return nil, err
	}
	clientSessionID, err := clientSessionID(ctx)
	if err != nil {
		return nil, err
	}
	if m.core == nil {
		return nil, fmt.Errorf("SOURCE_UNAVAILABLE: typed code index core is unavailable")
	}
	target, err := m.core.ResolveIndexTarget(ctx, p, contextHandle)
	if err != nil {
		return nil, err
	}
	if !requestedTargetMatches(target, clientSessionID, contextHandle) {
		return nil, fmt.Errorf("codebase_status: resolved target does not match the requesting client handle")
	}

	result := map[string]any{"status": "never_indexed"}
	if raw, ok := m.indexStates.Load(indexKeyFor(target)); ok {
		state := raw.(*indexState)
		result["status"] = state.Status
		result["run_id"] = state.RunID
		if state.Err != "" {
			result["error"] = state.Err
		}
	}
	if target.ContextClone() == nil {
		if afterBarrier != nil {
			return nil, fmt.Errorf("codebase_status: after_barrier requires a published View")
		}
		result["server_counts_available"] = false
		result["current_context"] = nil
		out, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return nil, fmt.Errorf("codebase_status: marshal: %w", marshalErr)
		}
		return out, nil
	}

	statusArgs, err := json.Marshal(codebaseStatusProxyArgs{
		ContextHandle: target.ContextHandle,
		AfterBarrier:  afterBarrier,
	})
	if err != nil {
		return nil, fmt.Errorf("codebase_status: marshal proxy args: %w", err)
	}
	serverRaw, proxyErr := m.core.ProxyHandleTool(ctx, target, "codebase_status", statusArgs)
	if proxyErr != nil {
		if proxyIsError, ok := proxyErr.(*module.ProxyIsError); ok {
			return nil, proxyIsError
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		result["server_counts_available"] = false
		result["server_counts_error"] = proxyErr.Error()
	} else {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		serverPayload, payloadErr := decodeServerStatusPayload(serverRaw)
		if payloadErr != nil {
			result["server_counts_available"] = false
			result["server_counts_error"] = payloadErr.Error()
		} else {
			for _, key := range []string{"total_chunks", "embedded_chunks", "last_indexed_at", "context", "rows", "edges", "evidence_recorder", "freshness"} {
				if value, found := serverPayload[key]; found {
					result[key] = value
				}
			}
			result["server_counts_available"] = true
		}
	}

	out, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("codebase_status: marshal: %w", err)
	}
	return out, nil
}
