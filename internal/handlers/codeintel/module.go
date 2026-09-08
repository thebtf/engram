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
// The short admission critical section is keyed by the complete
// server-authorized target identity.
//
// CLEAN-ROOM: no AGPL source referenced during implementation.
package codeintel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"sync"
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
	indexRunRecordLimit                            = 256
	indexRunTargetPathCount                  int64 = 1
)

var errInvalidServerStatusPayload = errors.New("failed to parse server response")

// indexStateKey isolates daemon liveness and execution by the complete
// server-authorized target identity. A publication from no View to a real View
// preserves the same scope entry across client sessions.
type indexStateKey struct {
	ScopeSourceID      string
	ScopeCheckoutID    string
	ScopeIncarnationID string
	ProfileID          string
	LocalRootID        string
	WorkstationID      string
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

// indexRunRecord keeps the immutable identity and completion signal for one
// opaque run token. startMu protects the terminal fields and records remain
// available independently of the current per-target liveness row.
type indexRunRecord struct {
	key       indexStateKey
	runID     string
	startedAt time.Time
	pathCount int64
	done      chan struct{}

	terminal       bool
	terminalStatus string
	terminalErr    string
}

type indexRunSnapshot struct {
	runID     string
	pathCount int64
	status    string
	err       string
	terminal  bool
}

type indexBarrierOutcome uint8

const (
	indexBarrierSatisfied indexBarrierOutcome = iota
	indexBarrierTimedOut
	indexBarrierStale
	indexBarrierError
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

// IndexTargetRebinder is an optional production capability. It refreshes an
// already-authorized target without accepting any raw project or path selector.
// Tests and compatibility fakes can omit it without gaining a second authority
// path.
type IndexTargetRebinder interface {
	RebindIndexTarget(context.Context, ResolvedIndexTarget) (ResolvedIndexTarget, error)
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

func (a *engramCoreAdapter) RebindIndexTarget(ctx context.Context, target ResolvedIndexTarget) (ResolvedIndexTarget, error) {
	return a.adapter.RebindIndexTarget(ctx, target)
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
	// indexStates maps each authorized scope to its current liveness state.
	// startMu serializes admission, terminal completion, and the bounded token
	// ledger; index work itself remains concurrent for disjoint scope keys.
	indexStates     sync.Map // indexStateKey -> *indexState
	startMu         sync.Mutex
	pendingAuto     map[indexStateKey]indexRunRequest
	runRecords      map[string]*indexRunRecord
	completedRunIDs []string

	watcherConsumerMu     sync.Mutex
	watcherConsumers      map[string]uciRuntimeWatcherChangeSource
	watcherConsumerCtx    context.Context
	watcherConsumerCancel context.CancelFunc
	watcherConsumerWG     sync.WaitGroup
}

type indexRunOrigin uint8

const (
	indexRunManual indexRunOrigin = iota
	indexRunAutomatic
)

const automaticIndexFailureDiagnostic = "automatic reindex failed; durable watcher state remains pending"

type indexRunRequest struct {
	target         ResolvedIndexTarget
	root           string
	origin         indexRunOrigin
	correlation    auditcontext.UCIRequestCorrelation
	hasCorrelation bool
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
		m.watcherConsumerMu.Lock()
		m.watcherConsumerCtx, m.watcherConsumerCancel = context.WithCancel(deps.DaemonCtx)
		m.watcherConsumers = make(map[string]uciRuntimeWatcherChangeSource)
		m.watcherConsumerMu.Unlock()
	}
	if deps.Logger != nil {
		deps.Logger.Info("codeintel module initialised")
	}
	return nil
}

// Shutdown stops watcher resources before closing the module-owned SQLite
// registry. Index goroutines use DaemonCtx and are cancelled by the framework.
func (m *Module) Shutdown(ctx context.Context) error {
	m.watcherConsumerMu.Lock()
	cancelConsumers := m.watcherConsumerCancel
	m.watcherConsumerCancel = nil
	m.watcherConsumerCtx = nil
	m.watcherConsumers = nil
	m.watcherConsumerMu.Unlock()
	if cancelConsumers != nil {
		cancelConsumers()
		m.watcherConsumerWG.Wait()
	}

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
		"properties": map[string]any{
			"context_handle": map[string]any{
				"type":        "string",
				"description": "Optional opaque handle returned for this client by codebase_context. Without one, only this client's existing server-side binding may authorize a target.",
			},
			"root": map[string]any{
				"type":        "string",
				"description": "Optional local working root. It is evidence only and cannot select authority.",
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
						"description": "Opaque target-bound run_id returned by codebase_index.",
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
			Description: "Trigger an async code index run for one resolved context. Returns immediately with an opaque run_id that can be used as codebase_status.after_barrier.token. Requires ENGRAM_CODE_INTEL_ENABLED=true.",
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
	ContextHandle string `json:"context_handle"`
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
	contextHandle := ""
	if parsed.ContextHandle != nil {
		contextHandle = *parsed.ContextHandle
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
	if target.ClientSessionID != clientSessionID || !validCodeintelIdentity(target.ContextHandle, 128) {
		return false
	}
	return contextHandle == "" || target.ContextHandle == contextHandle
}

func indexKeyFor(target ResolvedIndexTarget) indexStateKey {
	binding := target.BindingClone()
	return indexStateKey{
		ScopeSourceID:      binding.Scope.SourceID,
		ScopeCheckoutID:    binding.Scope.CheckoutID,
		ScopeIncarnationID: binding.Scope.IncarnationID,
		ProfileID:          binding.ProfileID,
		LocalRootID:        binding.LocalRootID,
		WorkstationID:      binding.WorkstationID,
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
	payload, err := decodeServerStatusObject(raw)
	if err != nil {
		return nil, errInvalidServerStatusPayload
	}
	if serverStatusTextBlockPresent(payload) {
		raw, err = unwrapServerStatusTextBlock(payload)
		if err != nil {
			return nil, errInvalidServerStatusPayload
		}
		payload, err = decodeServerStatusObject(raw)
		if err != nil {
			return nil, errInvalidServerStatusPayload
		}
	}
	if serverStatusContentEnvelopePresent(payload) {
		raw, err = unwrapServerStatusContentEnvelope(payload)
		if err != nil {
			return nil, errInvalidServerStatusPayload
		}
		payload, err = decodeServerStatusObject(raw)
		if err != nil {
			return nil, errInvalidServerStatusPayload
		}
	}
	if serverStatusTextBlockPresent(payload) || serverStatusContentEnvelopePresent(payload) {
		return nil, errInvalidServerStatusPayload
	}
	return payload, nil
}

func decodeServerStatusObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, errInvalidServerStatusPayload
	}
	return payload, nil
}

func serverStatusTextBlockPresent(payload map[string]json.RawMessage) bool {
	_, hasType := payload["type"]
	_, hasText := payload["text"]
	return hasType || hasText
}

func serverStatusContentEnvelopePresent(payload map[string]json.RawMessage) bool {
	_, hasContent := payload["content"]
	_, hasIsError := payload["isError"]
	return hasContent || hasIsError
}

func unwrapServerStatusTextBlock(payload map[string]json.RawMessage) (json.RawMessage, error) {
	if len(payload) != 2 {
		return nil, errInvalidServerStatusPayload
	}
	rawType, hasType := payload["type"]
	rawText, hasText := payload["text"]
	if !hasType || !hasText {
		return nil, errInvalidServerStatusPayload
	}
	var blockType, text string
	if err := json.Unmarshal(rawType, &blockType); err != nil || blockType != "text" {
		return nil, errInvalidServerStatusPayload
	}
	if err := json.Unmarshal(rawText, &text); err != nil || text == "" {
		return nil, errInvalidServerStatusPayload
	}
	next := json.RawMessage(text)
	if !json.Valid(next) {
		return nil, errInvalidServerStatusPayload
	}
	return next, nil
}

func unwrapServerStatusContentEnvelope(payload map[string]json.RawMessage) (json.RawMessage, error) {
	rawContent, hasContent := payload["content"]
	rawIsError, hasIsError := payload["isError"]
	if !hasContent || len(payload) < 1 || len(payload) > 2 {
		return nil, errInvalidServerStatusPayload
	}
	if hasIsError {
		var isError *bool
		if err := json.Unmarshal(rawIsError, &isError); err != nil || isError == nil || *isError {
			return nil, errInvalidServerStatusPayload
		}
	}
	var content []json.RawMessage
	if err := json.Unmarshal(rawContent, &content); err != nil || len(content) != 1 {
		return nil, errInvalidServerStatusPayload
	}
	var block map[string]json.RawMessage
	if err := json.Unmarshal(content[0], &block); err != nil || block == nil {
		return nil, errInvalidServerStatusPayload
	}
	return unwrapServerStatusTextBlock(block)
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
		if err := m.armRuntimeWatcher(target); err != nil {
			return nil, fmt.Errorf("codebase_index: arm authorized watcher: %w", err)
		}
	}

	correlation, hasCorrelation := auditcontext.UCIRequestCorrelationFromContext(ctx)
	state, started, err := m.startIndexRun(indexRunRequest{
		target:         target.Clone(),
		root:           root,
		origin:         indexRunManual,
		correlation:    correlation,
		hasCorrelation: hasCorrelation,
	})
	if err != nil {
		return nil, fmt.Errorf("codebase_index: allocate run identity: %w", err)
	}
	if !started {
		out, _ := json.Marshal(map[string]any{
			"status": "already_running",
			"run_id": state.RunID,
		})
		return out, nil
	}
	out, _ := json.Marshal(map[string]any{
		"status": "started",
		"run_id": state.RunID,
	})
	return out, nil
}

func (m *Module) armRuntimeWatcher(target ResolvedIndexTarget) error {
	if m.runtime == nil {
		return nil
	}
	source, err := m.runtime.watcherChangeSource(target)
	if err != nil {
		return err
	}
	m.watcherConsumerMu.Lock()
	ctx := m.watcherConsumerCtx
	if ctx == nil || ctx.Err() != nil {
		m.watcherConsumerMu.Unlock()
		return fmt.Errorf("watcher consumer is unavailable")
	}
	if existing, found := m.watcherConsumers[source.checkoutID]; found && existing.incarnationID == source.incarnationID && existing.rootPath == source.rootPath && existing.changes == source.changes {
		m.watcherConsumerMu.Unlock()
		return nil
	}
	m.watcherConsumers[source.checkoutID] = source
	m.watcherConsumerWG.Add(1)
	m.watcherConsumerMu.Unlock()
	go m.consumeRuntimeWatcher(ctx, source)
	return nil
}

func (m *Module) consumeRuntimeWatcher(ctx context.Context, source uciRuntimeWatcherChangeSource) {
	defer m.watcherConsumerWG.Done()
	defer func() {
		m.watcherConsumerMu.Lock()
		if retained, found := m.watcherConsumers[source.checkoutID]; found && retained.incarnationID == source.incarnationID && retained.rootPath == source.rootPath && retained.changes == source.changes {
			delete(m.watcherConsumers, source.checkoutID)
		}
		m.watcherConsumerMu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-source.changes:
			if !open {
				return
			}
			snapshot, found := m.runtime.watcherIndexSnapshot(source)
			if !found {
				continue
			}
			if _, _, err := m.startIndexRun(indexRunRequest{target: snapshot.target, root: snapshot.rootPath, origin: indexRunAutomatic}); err != nil {
				if logger := m.deps.Logger; logger != nil {
					logger.Error("codeintel: automatic reindex scheduling failed", "checkout_id", source.checkoutID, "error", err.Error())
				}
			}
		}
	}
}

func (m *Module) startIndexRun(request indexRunRequest) (*indexState, bool, error) {
	key := indexKeyFor(request.target)
	m.startMu.Lock()
	if raw, found := m.indexStates.Load(key); found {
		if existing := raw.(*indexState); existing.Status == statusRunning {
			if request.origin == indexRunAutomatic && m.daemonIndexContext().Err() == nil {
				if m.pendingAuto == nil {
					m.pendingAuto = make(map[indexStateKey]indexRunRequest)
				}
				m.pendingAuto[key] = request
			}
			m.startMu.Unlock()
			return existing, false, nil
		}
	}
	if request.origin == indexRunAutomatic && m.daemonIndexContext().Err() != nil {
		m.startMu.Unlock()
		return nil, false, nil
	}
	state, record, err := m.newRunningIndexStateLocked(key)
	if err != nil {
		m.startMu.Unlock()
		return nil, false, err
	}
	m.indexStates.Store(key, state)
	m.startMu.Unlock()
	m.launchIndexRun(request, state, record)
	return state, true, nil
}

func (m *Module) newRunningIndexStateLocked(key indexStateKey) (*indexState, *indexRunRecord, error) {
	if m.runRecords == nil {
		m.runRecords = make(map[string]*indexRunRecord)
	}
	for range 4 {
		runID, err := newIndexRunID()
		if err != nil {
			return nil, nil, err
		}
		if _, found := m.runRecords[runID]; found {
			continue
		}
		startedAt := time.Now()
		record := &indexRunRecord{
			key:       key,
			runID:     runID,
			startedAt: startedAt,
			pathCount: indexRunTargetPathCount,
			done:      make(chan struct{}),
		}
		m.runRecords[runID] = record
		return &indexState{Status: statusRunning, RunID: runID, StartedAt: startedAt}, record, nil
	}
	return nil, nil, fmt.Errorf("generate a unique index run ID")
}

func newIndexRunID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("read cryptographic run entropy: %w", err)
	}
	return "uci-run-v1-" + hex.EncodeToString(nonce[:]), nil
}

func (m *Module) launchIndexRun(request indexRunRequest, state *indexState, record *indexRunRecord) {
	go func() {
		var terminal *indexState
		defer func() {
			if recovered := recover(); recovered != nil {
				if logger := m.deps.Logger; logger != nil {
					logger.Error("codeintel: index goroutine panicked",
						"client_session_id", request.target.ClientSessionID,
						"context_handle", request.target.ContextHandle,
						"run_id", state.RunID,
						"panic", fmt.Sprintf("%v", recovered),
						"stack", string(debug.Stack()),
					)
				}
				terminal = m.failedIndexState(request, state, fmt.Errorf("panic: %v", recovered))
			}
			if terminal == nil {
				terminal = m.failedIndexState(request, state, fmt.Errorf("index execution did not complete"))
			}
			m.completeIndexRun(request, terminal, record)
		}()
		terminal = m.executeIndexRun(request, state)
	}()
}

func (m *Module) executeIndexRun(request indexRunRequest, state *indexState) *indexState {
	target := request.target.Clone()
	if logger := m.deps.Logger; logger != nil {
		logger.Info("codeintel: starting index run",
			"client_session_id", target.ClientSessionID,
			"context_handle", target.ContextHandle,
			"run_id", state.RunID,
			"root", request.root,
		)
	}
	indexContext := m.indexContext(target, request)
	if rebinder, supported := m.core.(IndexTargetRebinder); supported {
		rebound, err := rebinder.RebindIndexTarget(indexContext, target)
		if err != nil {
			return m.logIndexFailure(request, state, target, err)
		}
		if !sameUCIRuntimeTargetIdentity(target, rebound) {
			return m.logIndexFailure(request, state, target, fmt.Errorf("codebase_index: rebound target changed authorization"))
		}
		target = rebound
		if m.runtime != nil {
			if err := m.runtime.updateReboundTarget(target); err != nil {
				return m.logIndexFailure(request, state, target, err)
			}
		}
	}
	result, err := m.core.IndexCodebase(indexContext, target, request.root)
	if err != nil {
		return m.logIndexFailure(request, state, target, err)
	}
	if !indexResultMatchesBinding(result, target.BindingClone()) {
		return m.logIndexFailure(request, state, target, fmt.Errorf("codebase_index: index result context does not match resolved target"))
	}
	if logger := m.deps.Logger; logger != nil {
		logger.Info("codeintel: index run complete",
			"client_session_id", target.ClientSessionID,
			"context_handle", target.ContextHandle,
			"run_id", state.RunID,
			"uploaded", result.Uploaded,
			"embedded", result.Embedded,
			"deleted", result.Deleted,
		)
	}
	return &indexState{Status: statusIdle, RunID: state.RunID, StartedAt: state.StartedAt}
}

func (m *Module) logIndexFailure(request indexRunRequest, state *indexState, target ResolvedIndexTarget, err error) *indexState {
	if logger := m.deps.Logger; logger != nil {
		logger.Error("codeintel: index run failed",
			"client_session_id", target.ClientSessionID,
			"context_handle", target.ContextHandle,
			"run_id", state.RunID,
			"error", err.Error(),
		)
	}
	return m.failedIndexState(request, state, err)
}

func (m *Module) failedIndexState(request indexRunRequest, state *indexState, err error) *indexState {
	diagnostic := err.Error()
	if uci.IsIndexCapacityError(err) {
		diagnostic = string(uci.IndexCapacityExceeded)
	} else if request.origin == indexRunAutomatic {
		diagnostic = automaticIndexFailureDiagnostic
	}
	return &indexState{Status: statusError, RunID: state.RunID, StartedAt: state.StartedAt, Err: diagnostic}
}

func (m *Module) completeIndexRun(request indexRunRequest, terminal *indexState, record *indexRunRecord) {
	key := indexKeyFor(request.target)
	m.startMu.Lock()
	terminal = m.completeIndexRunRecordLocked(record, terminal)
	if pending, found := m.pendingAuto[key]; found && m.daemonIndexContext().Err() == nil {
		delete(m.pendingAuto, key)
		next, nextRecord, err := m.newRunningIndexStateLocked(key)
		if err == nil {
			m.indexStates.Store(key, next)
			m.startMu.Unlock()
			m.launchIndexRun(pending, next, nextRecord)
			return
		}
		m.indexStates.Store(key, terminal)
		m.startMu.Unlock()
		if logger := m.deps.Logger; logger != nil {
			logger.Error("codeintel: automatic reindex scheduling failed", "checkout_id", key.ScopeCheckoutID, "error", err.Error())
		}
		return
	}
	delete(m.pendingAuto, key)
	m.indexStates.Store(key, terminal)
	m.startMu.Unlock()
}

func (m *Module) completeIndexRunRecordLocked(record *indexRunRecord, terminal *indexState) *indexState {
	if record == nil {
		return &indexState{Status: statusError, Err: "index completion record is unavailable"}
	}
	if record.terminal {
		return &indexState{Status: record.terminalStatus, RunID: record.runID, StartedAt: record.startedAt, Err: record.terminalErr}
	}
	if terminal == nil || terminal.RunID != record.runID || (terminal.Status != statusIdle && terminal.Status != statusError) {
		terminal = &indexState{Status: statusError, RunID: record.runID, StartedAt: record.startedAt, Err: "index completion receipt is invalid"}
	}
	record.terminal = true
	record.terminalStatus = terminal.Status
	record.terminalErr = terminal.Err
	close(record.done)
	m.completedRunIDs = append(m.completedRunIDs, record.runID)
	m.pruneIndexRunRecordsLocked()
	return terminal
}

func (m *Module) pruneIndexRunRecordsLocked() {
	for len(m.completedRunIDs) > indexRunRecordLimit {
		runID := m.completedRunIDs[0]
		m.completedRunIDs = m.completedRunIDs[1:]
		if record, found := m.runRecords[runID]; found && record.terminal {
			delete(m.runRecords, runID)
		}
	}
}

func (m *Module) waitForIndexBarrier(ctx context.Context, key indexStateKey, barrier codebaseStatusAfterBarrier) (indexRunSnapshot, indexBarrierOutcome, error) {
	if ctx == nil {
		return indexRunSnapshot{}, indexBarrierError, fmt.Errorf("codebase_status: request context is required")
	}
	m.startMu.Lock()
	record, found := m.runRecords[barrier.Token]
	if !found || record.key != key {
		m.startMu.Unlock()
		return indexRunSnapshot{}, indexBarrierStale, nil
	}
	snapshot := indexRunRecordSnapshot(record)
	done := record.done
	m.startMu.Unlock()
	if snapshot.terminal {
		return snapshot, indexBarrierOutcomeForSnapshot(snapshot), nil
	}

	timer := time.NewTimer(time.Duration(barrier.WaitMS) * time.Millisecond)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	select {
	case <-ctx.Done():
		return indexRunSnapshot{}, indexBarrierError, ctx.Err()
	case <-done:
		m.startMu.Lock()
		snapshot = indexRunRecordSnapshot(record)
		m.startMu.Unlock()
		return snapshot, indexBarrierOutcomeForSnapshot(snapshot), nil
	case <-timer.C:
		m.startMu.Lock()
		snapshot = indexRunRecordSnapshot(record)
		m.startMu.Unlock()
		if snapshot.terminal {
			return snapshot, indexBarrierOutcomeForSnapshot(snapshot), nil
		}
		return snapshot, indexBarrierTimedOut, nil
	}
}

func indexRunRecordSnapshot(record *indexRunRecord) indexRunSnapshot {
	snapshot := indexRunSnapshot{runID: record.runID, pathCount: record.pathCount, status: statusRunning, terminal: record.terminal}
	if record.terminal {
		snapshot.status = record.terminalStatus
		snapshot.err = record.terminalErr
	}
	return snapshot
}

func indexBarrierOutcomeForSnapshot(snapshot indexRunSnapshot) indexBarrierOutcome {
	if !snapshot.terminal {
		return indexBarrierTimedOut
	}
	switch snapshot.status {
	case statusIdle:
		return indexBarrierSatisfied
	case statusError:
		return indexBarrierError
	default:
		return indexBarrierStale
	}
}

func (m *Module) daemonIndexContext() context.Context {
	if m.deps.DaemonCtx != nil {
		return m.deps.DaemonCtx
	}
	return context.Background()
}

func (m *Module) indexContext(target ResolvedIndexTarget, request indexRunRequest) context.Context {
	ctx := auditcontext.WithUCITransportSession(m.daemonIndexContext(), target.ClientSessionID)
	if request.hasCorrelation {
		ctx = auditcontext.WithUCIRequestCorrelation(ctx, request.correlation)
	}
	return ctx
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
	capacityFailure := false
	var barrierSnapshot indexRunSnapshot
	var barrierOutcome indexBarrierOutcome
	if afterBarrier != nil {
		barrierSnapshot, barrierOutcome, err = m.waitForIndexBarrier(ctx, indexKeyFor(target), *afterBarrier)
		if err != nil {
			return nil, err
		}
		switch barrierOutcome {
		case indexBarrierStale:
			return nil, fmt.Errorf("codebase_status: after_barrier token is stale for this target")
		case indexBarrierError:
			if barrierSnapshot.err != "" {
				return nil, fmt.Errorf("codebase_status: after_barrier run failed: %s", barrierSnapshot.err)
			}
			return nil, fmt.Errorf("codebase_status: after_barrier run failed")
		case indexBarrierSatisfied, indexBarrierTimedOut:
			result["status"] = barrierSnapshot.status
			result["run_id"] = barrierSnapshot.runID
		default:
			return nil, fmt.Errorf("codebase_status: after_barrier has an invalid outcome")
		}
	} else if raw, ok := m.indexStates.Load(indexKeyFor(target)); ok {
		state := raw.(*indexState)
		result["status"] = state.Status
		result["run_id"] = state.RunID
		if state.Err != "" {
			result["error"] = state.Err
		}
		capacityFailure = state.Status == statusError && state.Err == string(uci.IndexCapacityExceeded)
	}
	if capacityFailure {
		result["server_counts_available"] = false
		out, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return nil, fmt.Errorf("codebase_status: marshal: %w", marshalErr)
		}
		return out, nil
	}
	if afterBarrier != nil {
		refreshed, refreshErr := m.core.ResolveIndexTarget(ctx, p, contextHandle)
		if refreshErr != nil {
			return nil, refreshErr
		}
		if !requestedTargetMatches(refreshed, clientSessionID, contextHandle) || indexKeyFor(refreshed) != indexKeyFor(target) {
			return nil, fmt.Errorf("codebase_status: target changed while waiting for local barrier")
		}
		target = refreshed
	}
	if target.ContextClone() == nil {
		if afterBarrier != nil && barrierOutcome != indexBarrierTimedOut {
			return nil, fmt.Errorf("codebase_status: satisfied after_barrier requires a published View")
		}
		result["server_counts_available"] = false
		result["current_context"] = nil
		out, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return nil, fmt.Errorf("codebase_status: marshal: %w", marshalErr)
		}
		return out, nil
	}

	statusArgs, err := json.Marshal(codebaseStatusProxyArgs{ContextHandle: target.ContextHandle})
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
		if afterBarrier != nil {
			return nil, fmt.Errorf("codebase_status: load local barrier status: %w", proxyErr)
		}
		result["server_counts_available"] = false
		result["server_counts_error"] = proxyErr.Error()
	} else {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		serverPayload, payloadErr := decodeServerStatusPayload(serverRaw)
		if payloadErr != nil {
			if afterBarrier != nil {
				return nil, fmt.Errorf("codebase_status: decode local barrier status: %w", payloadErr)
			}
			result["server_counts_available"] = false
			result["server_counts_error"] = payloadErr.Error()
		} else {
			if afterBarrier != nil {
				if err := mergeIndexBarrierFreshness(serverPayload, barrierSnapshot, *afterBarrier, barrierOutcome); err != nil {
					return nil, err
				}
			}
			for _, key := range []string{"total_chunks", "embedded_chunks", "last_indexed_at", "context", "rows", "edges", "embedding", "evidence_recorder", "freshness"} {
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

func mergeIndexBarrierFreshness(payload map[string]json.RawMessage, snapshot indexRunSnapshot, request codebaseStatusAfterBarrier, outcome indexBarrierOutcome) error {
	rawFreshness, found := payload["freshness"]
	if !found {
		return fmt.Errorf("codebase_status: local barrier status omitted freshness")
	}
	var freshness uci.QueryFreshness
	if err := json.Unmarshal(rawFreshness, &freshness); err != nil {
		return fmt.Errorf("codebase_status: decode local barrier freshness: %w", err)
	}
	if err := freshness.Validate(); err != nil {
		return fmt.Errorf("codebase_status: local barrier freshness is invalid: %w", err)
	}
	state := uci.QueryBarrierSatisfied
	if outcome == indexBarrierTimedOut {
		state = uci.QueryBarrierTimedOut
	} else if outcome != indexBarrierSatisfied {
		return fmt.Errorf("codebase_status: local barrier cannot merge outcome")
	}
	freshness.Method = uci.QueryFreshnessPathHashBarrier
	freshness.Barrier = &uci.QueryBarrier{
		Scope:      uci.QueryBarrierScope{Kind: uci.QueryBarrierPaths, PathCount: snapshot.pathCount},
		DeadlineMS: request.WaitMS,
		State:      state,
	}
	if err := freshness.Validate(); err != nil {
		return fmt.Errorf("codebase_status: merged local barrier freshness is invalid: %w", err)
	}
	encoded, err := json.Marshal(freshness)
	if err != nil {
		return fmt.Errorf("codebase_status: encode local barrier freshness: %w", err)
	}
	payload["freshness"] = encoded
	return nil
}
