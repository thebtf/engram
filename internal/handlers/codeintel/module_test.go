package codeintel_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/handlers/codeintel"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/moduletest"
	"github.com/thebtf/engram/internal/uci"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// fakeCore is a typed UCI stand-in for the daemon module tests.
type fakeCore struct {
	mu          sync.Mutex
	indexCalled int
	indexDelay  time.Duration
	indexErr    error
	afterIndex  func()
	indexGate   <-chan struct{}

	statusResponse []byte
	statusErr      error

	bindings       map[string]uci.IndexBinding
	resolveBinding func(int, string) uci.IndexBinding
	afterResolve   func(int)
	resolveCalled  int
	proxyCalled    int
}

func (f *fakeCore) ResolveIndexTarget(ctx context.Context, _ muxcore.ProjectContext, contextHandle string) (codeintel.ResolvedIndexTarget, error) {
	f.mu.Lock()
	f.resolveCalled++
	resolveCall := f.resolveCalled
	binding, found := f.bindings[contextHandle]
	if f.resolveBinding != nil {
		binding = f.resolveBinding(resolveCall, contextHandle)
		found = true
	}
	if !found {
		binding = fakeDefaultIndexBinding()
	}
	binding = binding.Clone()
	afterResolve := f.afterResolve
	f.mu.Unlock()
	if afterResolve != nil {
		afterResolve(resolveCall)
	}

	return codeintel.ResolvedIndexTarget{
		ClientSessionID: auditcontext.UCITransportSession(ctx),
		ContextHandle:   contextHandle,
		Binding:         binding,
	}, nil
}

func fakeDefaultIndexBinding() uci.IndexBinding {
	spaceID := "11111111-1111-4111-8111-111111111111"
	context := uci.ContextRef{
		SpaceID:           &spaceID,
		SourceID:          "22222222-2222-4222-8222-222222222222",
		CheckoutID:        "33333333-3333-4333-8333-333333333333",
		ViewID:            "44444444-4444-4444-8444-444444444444",
		AnalysisProfileID: "55555555-5555-4555-8555-555555555555",
		Generation:        1,
	}
	return uci.IndexBinding{
		Context: &context,
		Scope: uci.IndexScope{
			SourceID:      context.SourceID,
			CheckoutID:    context.CheckoutID,
			IncarnationID: "66666666-6666-4666-8666-666666666666",
		},
		ProfileID:     context.AnalysisProfileID,
		LocalRootID:   "fake-local-root",
		WorkstationID: "fake-workstation",
	}
}

func fakeNoViewIndexBinding(checkoutID, incarnationID string) uci.IndexBinding {
	return uci.IndexBinding{
		Scope: uci.IndexScope{
			SourceID:      "22222222-2222-4222-8222-222222222222",
			CheckoutID:    checkoutID,
			IncarnationID: incarnationID,
		},
		ProfileID:     "55555555-5555-4555-8555-555555555555",
		LocalRootID:   "fake-local-root",
		WorkstationID: "fake-workstation",
	}
}

func (f *fakeCore) IndexCodebase(ctx context.Context, target codeintel.ResolvedIndexTarget, _ string) (*codeintel.IndexResult, error) {
	f.mu.Lock()
	delay := f.indexDelay
	err := f.indexErr
	afterIndex := f.afterIndex
	indexGate := f.indexGate
	f.indexCalled++
	f.mu.Unlock()

	if indexGate != nil {
		select {
		case <-indexGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	if err != nil {
		return nil, err
	}
	if afterIndex != nil {
		afterIndex()
	}
	context := target.ContextClone()
	if context == nil {
		binding := target.BindingClone()
		context = &uci.ContextRef{
			SourceID:          binding.Scope.SourceID,
			CheckoutID:        binding.Scope.CheckoutID,
			ViewID:            "77777777-7777-4777-8777-777777777777",
			AnalysisProfileID: binding.ProfileID,
			Generation:        1,
		}
	}
	return &codeintel.IndexResult{Context: *context, Uploaded: 5, Embedded: 3, Deleted: 1}, nil
}

func (f *fakeCore) ProxyHandleTool(_ context.Context, _ codeintel.ResolvedIndexTarget, _ string, _ json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proxyCalled++
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	if f.statusResponse != nil {
		return f.statusResponse, nil
	}
	return json.Marshal(map[string]any{
		"total_chunks":    int64(10),
		"embedded_chunks": int64(8),
		"last_indexed_at": "2026-06-16T00:00:00Z",
	})
}

func (f *fakeCore) callCounts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolveCalled, f.proxyCalled
}

// newTestModule constructs a codeintel.Module backed by a fakeCore so tests can
// run without a live gRPC server.
func newTestModule(core codeintel.CoreProvider) *codeintel.Module {
	return codeintel.NewModuleWithCore(core)
}

func testProjectContext(id, cwd string) muxcore.ProjectContext {
	return muxcore.ProjectContext{
		ID:  id,
		Cwd: cwd,
		Env: map[string]string{config.EnvClaudeSessionID: id + "-host-session-must-not-be-used"},
	}
}

func testTransportContext(p muxcore.ProjectContext) context.Context {
	return auditcontext.WithUCITransportSession(context.Background(), "transport-"+p.ID)
}

func testIndexArgs(p muxcore.ProjectContext) json.RawMessage {
	return testIndexArgsForHandle(p, "handle-"+p.ID)
}

func testIndexArgsForHandle(p muxcore.ProjectContext, contextHandle string) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{"context_handle": contextHandle, "root": p.Cwd})
	return payload
}

func testStatusArgs(p muxcore.ProjectContext) json.RawMessage {
	return testStatusArgsForHandle("handle-" + p.ID)
}

func testStatusArgsForHandle(contextHandle string) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{"context_handle": contextHandle})
	return payload
}

func testStatusArgsWithBarrier(contextHandle, token string, waitMS int64) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{
		"context_handle": contextHandle,
		"after_barrier":  map[string]any{"token": token, "wait_ms": waitMS},
	})
	return payload
}

func testStatusTextBlock(t *testing.T, text json.RawMessage) json.RawMessage {
	t.Helper()
	block, err := json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: string(text)})
	require.NoError(t, err)
	return block
}

func testStatusContentEnvelope(t *testing.T, content []json.RawMessage, isError bool) json.RawMessage {
	t.Helper()
	envelope, err := json.Marshal(struct {
		Content []json.RawMessage `json:"content"`
		IsError bool              `json:"isError"`
	}{Content: content, IsError: isError})
	require.NoError(t, err)
	return envelope
}

func testNestedStatusProxyPayload(t *testing.T, status json.RawMessage) json.RawMessage {
	t.Helper()
	inner := testStatusTextBlock(t, status)
	envelope, err := json.Marshal(map[string]any{"content": []json.RawMessage{inner}})
	require.NoError(t, err)
	return testStatusTextBlock(t, envelope)
}

// -----------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------

// TestCodebaseIndex_ReturnsStartedImmediately verifies that codebase_index
// returns {status:"started",run_id:...} before the background goroutine finishes.
func TestCodebaseIndex_ReturnsStartedImmediately(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")

	core := &fakeCore{indexDelay: 50 * time.Millisecond}
	mod := newTestModule(core)

	h := moduletest.New(t)
	require.NoError(t, h.Register(mod))
	h.Freeze()

	p := testProjectContext("proj-1", t.TempDir())
	args := testIndexArgs(p)

	start := time.Now()
	raw, err := h.CallToolWithProject(testTransportContext(p), p, "codebase_index", args)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.NotNil(t, raw)

	// Must return before the 50 ms delay completes — confirms background dispatch.
	assert.Less(t, elapsed, 40*time.Millisecond, "handleIndex must return immediately (< delay)")

	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result))
	assert.Equal(t, "started", result["status"])
	assert.NotEmpty(t, result["run_id"])

	// Drain the background goroutine before returning: it logs into the harness
	// logger (t.Log-backed), which panics if invoked after the test exits.
	drainIndex(t, h, p)
}

// drainIndex polls codebase_status until the project's index run reaches idle (or
// error), ensuring the spawned background goroutine has finished — and therefore
// stopped logging — before the test function returns. Without this, the harness
// logger (backed by t.Log) panics with "Log in goroutine after test has completed"
// under the race detector.
func drainIndex(t *testing.T, h *moduletest.Harness, p muxcore.ProjectContext) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := h.CallToolWithProject(testTransportContext(p), p, "codebase_status", testStatusArgs(p))
		if err == nil {
			var st map[string]any
			if json.Unmarshal(raw, &st) == nil {
				if s, _ := st["status"].(string); s == "idle" || s == "error" {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("index goroutine did not reach a terminal state within the drain deadline")
}

// TestCodebaseIndex_ConcurrentCallReturnsAlreadyRunning verifies that a second
// codebase_index call while one is running returns {status:"already_running"}.
func TestCodebaseIndex_ConcurrentCallReturnsAlreadyRunning(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")

	// Long delay so the background goroutine is still running when the second call arrives.
	core := &fakeCore{indexDelay: 500 * time.Millisecond}
	mod := newTestModule(core)

	h := moduletest.New(t)
	require.NoError(t, h.Register(mod))
	h.Freeze()

	p := testProjectContext("proj-concurrent", t.TempDir())
	args := testIndexArgs(p)

	// First call — should start.
	raw1, err := h.CallToolWithProject(testTransportContext(p), p, "codebase_index", args)
	require.NoError(t, err)
	var r1 map[string]any
	require.NoError(t, json.Unmarshal(raw1, &r1))
	require.Equal(t, "started", r1["status"], "first call must return 'started'")

	// Second call — should see the running state.
	raw2, err := h.CallToolWithProject(testTransportContext(p), p, "codebase_index", args)
	require.NoError(t, err)
	var r2 map[string]any
	require.NoError(t, json.Unmarshal(raw2, &r2))
	assert.Equal(t, "already_running", r2["status"], "second concurrent call must return 'already_running'")
	assert.Equal(t, r1["run_id"], r2["run_id"], "run_id must match the running session")

	drainIndex(t, h, p)
}

// TestCodebaseIndex_RaceAdmitsExactlyOne fires many concurrent codebase_index
// calls at the SAME fresh project and asserts exactly one is admitted ("started")
// — i.e. exactly one index goroutine is spawned. This is the regression guard for
// the TOCTOU window that a bare sync.Map.LoadOrStore (atomic only for the
// absent→present insert, not the idle/error→running replacement) leaves open.
func TestCodebaseIndex_RaceAdmitsExactlyOne(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")

	// Long delay so every racing call observes the same running window.
	core := &fakeCore{indexDelay: 300 * time.Millisecond}
	mod := newTestModule(core)

	h := moduletest.New(t)
	require.NoError(t, h.Register(mod))
	h.Freeze()

	p := testProjectContext("proj-race", t.TempDir())
	args := testIndexArgs(p)

	const n = 16
	var wg sync.WaitGroup
	var startedCount, alreadyCount atomic.Int32
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			raw, err := h.CallToolWithProject(testTransportContext(p), p, "codebase_index", args)
			if err != nil {
				return
			}
			var r map[string]any
			if json.Unmarshal(raw, &r) != nil {
				return
			}
			switch r["status"] {
			case "started":
				startedCount.Add(1)
			case "already_running":
				alreadyCount.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), startedCount.Load(), "exactly one concurrent call may be admitted (started)")
	assert.Equal(t, int32(n-1), alreadyCount.Load(), "all other concurrent calls must be rejected (already_running)")

	// Drain the spawned index goroutine before the test returns (it logs into the
	// harness logger, which panics if invoked after the test exits).
	drainIndex(t, h, p)

	// The fake's IndexCodebase must have been entered exactly once.
	core.mu.Lock()
	called := core.indexCalled
	core.mu.Unlock()
	assert.Equal(t, 1, called, "exactly one index goroutine may invoke IndexCodebase")
}

// TestCodebaseStatus_ReturnsNeverIndexedBeforeFirstRun verifies that codebase_status
// returns {status:"never_indexed"} for a project that has not been indexed yet.
func TestCodebaseStatus_ReturnsNeverIndexedBeforeFirstRun(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")

	const contextHandle = "handle-proj-new"
	core := &fakeCore{bindings: map[string]uci.IndexBinding{
		contextHandle: fakeNoViewIndexBinding("33333333-3333-4333-8333-333333333333", "66666666-6666-4666-8666-666666666666"),
	}}
	mod := newTestModule(core)

	h := moduletest.New(t)
	require.NoError(t, h.Register(mod))
	h.Freeze()

	p := testProjectContext("proj-new", t.TempDir())
	raw, err := h.CallToolWithProject(testTransportContext(p), p, "codebase_status", testStatusArgsForHandle(contextHandle))
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result))
	assert.Equal(t, "never_indexed", result["status"])
	assert.Equal(t, false, result["server_counts_available"])
	assert.Nil(t, result["current_context"])
	_, proxyCalls := core.callCounts()
	assert.Zero(t, proxyCalls, "no-View status must not proxy before publication")
}

// TestCodebaseStatus_TransitionsRunningToIdle verifies that codebase_status
// reflects running → idle after the index goroutine completes.
func TestCodebaseStatus_TransitionsRunningToIdle(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")

	core := &fakeCore{indexDelay: 20 * time.Millisecond}
	mod := newTestModule(core)

	h := moduletest.New(t)
	require.NoError(t, h.Register(mod))
	h.Freeze()

	p := testProjectContext("proj-transition", t.TempDir())
	args := testIndexArgs(p)

	// Start the index.
	raw, err := h.CallToolWithProject(testTransportContext(p), p, "codebase_index", args)
	require.NoError(t, err)
	var startResult map[string]any
	require.NoError(t, json.Unmarshal(raw, &startResult))
	assert.Equal(t, "started", startResult["status"])

	// Poll until idle or timeout.
	deadline := time.Now().Add(2 * time.Second)
	var finalStatus string
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		raw2, err2 := h.CallToolWithProject(testTransportContext(p), p, "codebase_status", testStatusArgs(p))
		if err2 != nil {
			continue
		}
		var st map[string]any
		if json.Unmarshal(raw2, &st) != nil {
			continue
		}
		finalStatus, _ = st["status"].(string)
		if finalStatus == "idle" {
			break
		}
	}
	assert.Equal(t, "idle", finalStatus, "codebase_status must transition to idle after index completes")
}

func TestCodebaseStatusDecodesBoundedProxyPayloads(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	statusPayload := json.RawMessage(`{"total_chunks":17,"embedded_chunks":13,"last_indexed_at":"2026-09-06T00:00:00Z"}`)

	for _, test := range []struct {
		name          string
		response      json.RawMessage
		wantAvailable bool
	}{
		{name: "direct payload", response: statusPayload, wantAvailable: true},
		{name: "nested gRPC MCP payload", response: testNestedStatusProxyPayload(t, statusPayload), wantAvailable: true},
		{name: "over nested payload", response: testStatusTextBlock(t, testNestedStatusProxyPayload(t, statusPayload))},
	} {
		t.Run(test.name, func(t *testing.T) {
			mod := newTestModule(&fakeCore{statusResponse: test.response})
			p := testProjectContext("proj-status-"+strings.ReplaceAll(test.name, " ", "-"), t.TempDir())
			raw, err := mod.HandleTool(testTransportContext(p), p, "codebase_status", testStatusArgs(p))
			require.NoError(t, err)
			var status struct {
				TotalChunks           int64  `json:"total_chunks"`
				EmbeddedChunks        int64  `json:"embedded_chunks"`
				ServerCountsAvailable bool   `json:"server_counts_available"`
				ServerCountsError     string `json:"server_counts_error"`
			}
			require.NoError(t, json.Unmarshal(raw, &status))
			if !test.wantAvailable {
				require.False(t, status.ServerCountsAvailable)
				require.Equal(t, "failed to parse server response", status.ServerCountsError)
				return
			}
			require.True(t, status.ServerCountsAvailable)
			require.Equal(t, int64(17), status.TotalChunks)
			require.Equal(t, int64(13), status.EmbeddedChunks)
		})
	}
}

func TestCodebaseIndex_NoViewBindingsUseDistinctScopeKeys(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")

	const (
		handleA = "handle-no-view-a"
		handleB = "handle-no-view-b"
	)
	core := &fakeCore{
		indexDelay: 500 * time.Millisecond,
		bindings: map[string]uci.IndexBinding{
			handleA: fakeNoViewIndexBinding("33333333-3333-4333-8333-333333333333", "66666666-6666-4666-8666-666666666666"),
			handleB: fakeNoViewIndexBinding("88888888-8888-4888-8888-888888888888", "99999999-9999-4999-8999-999999999999"),
		},
	}
	mod := newTestModule(core)
	h := moduletest.New(t)
	require.NoError(t, h.Register(mod))
	h.Freeze()

	p := testProjectContext("proj-no-view", t.TempDir())
	for _, args := range []json.RawMessage{
		testIndexArgsForHandle(p, handleA),
		testIndexArgsForHandle(p, handleB),
	} {
		raw, err := h.CallToolWithProject(testTransportContext(p), p, "codebase_index", args)
		require.NoError(t, err)
		var started map[string]any
		require.NoError(t, json.Unmarshal(raw, &started))
		require.Equal(t, "started", started["status"])
	}

	deadline := time.Now().Add(2 * time.Second)
	statusByHandle := map[string]string{}
	sawRunningByHandle := map[string]bool{}
	for time.Now().Before(deadline) {
		for _, contextHandle := range []string{handleA, handleB} {
			raw, err := h.CallToolWithProject(testTransportContext(p), p, "codebase_status", testStatusArgsForHandle(contextHandle))
			if err != nil {
				continue
			}
			var status map[string]any
			if json.Unmarshal(raw, &status) == nil {
				statusByHandle[contextHandle], _ = status["status"].(string)
				if statusByHandle[contextHandle] == "running" {
					sawRunningByHandle[contextHandle] = true
				}
			}
		}
		if statusByHandle[handleA] == "idle" && statusByHandle[handleB] == "idle" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, "idle", statusByHandle[handleA])
	require.Equal(t, "idle", statusByHandle[handleB])
	require.True(t, sawRunningByHandle[handleA], "no-View liveness must expose running before completion")
	require.True(t, sawRunningByHandle[handleB], "no-View liveness must expose running before completion")

	core.mu.Lock()
	called := core.indexCalled
	core.mu.Unlock()
	require.Equal(t, 2, called, "no-View bindings with distinct scopes must not share liveness state")
	_, proxyCalls := core.callCounts()
	require.Zero(t, proxyCalls, "no-View status must remain locally observable without a View-dependent server proxy")
}

func TestCodebaseStatusKeepsRunAcrossNoViewPublication(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	noView := fakeNoViewIndexBinding("33333333-3333-4333-8333-333333333333", "66666666-6666-4666-8666-666666666666")
	published := noView.Clone()
	published.Context = &uci.ContextRef{
		SourceID:          noView.Scope.SourceID,
		CheckoutID:        noView.Scope.CheckoutID,
		ViewID:            "44444444-4444-4444-8444-444444444444",
		AnalysisProfileID: noView.ProfileID,
		Generation:        1,
	}
	var isPublished atomic.Bool
	core := &fakeCore{resolveBinding: func(_ int, _ string) uci.IndexBinding {
		if isPublished.Load() {
			return published
		}
		return noView
	}}
	mod := newTestModule(core)
	h := moduletest.New(t)
	require.NoError(t, h.Register(mod))
	h.Freeze()
	p := testProjectContext("proj-no-view-publication", t.TempDir())
	ctx := testTransportContext(p)
	contextHandle := "handle-" + p.ID

	raw, err := h.CallToolWithProject(ctx, p, "codebase_index", testIndexArgsForHandle(p, contextHandle))
	require.NoError(t, err)
	var started map[string]any
	require.NoError(t, json.Unmarshal(raw, &started))
	runID, _ := started["run_id"].(string)
	require.NotEmpty(t, runID)
	drainIndex(t, h, p)

	raw, err = h.CallToolWithProject(ctx, p, "codebase_status", testStatusArgsForHandle(contextHandle))
	require.NoError(t, err)
	var local map[string]any
	require.NoError(t, json.Unmarshal(raw, &local))
	require.Equal(t, "idle", local["status"])
	require.Equal(t, runID, local["run_id"])
	require.Equal(t, false, local["server_counts_available"])
	require.Nil(t, local["current_context"])
	_, proxyCalls := core.callCounts()
	require.Zero(t, proxyCalls, "no-View status must return liveness before server proxying")

	isPublished.Store(true)
	raw, err = h.CallToolWithProject(ctx, p, "codebase_status", testStatusArgsForHandle(contextHandle))
	require.NoError(t, err)
	var afterPublication map[string]any
	require.NoError(t, json.Unmarshal(raw, &afterPublication))
	require.Equal(t, "idle", afterPublication["status"])
	require.Equal(t, runID, afterPublication["run_id"], "published View must retain the no-View run state")
	require.Equal(t, true, afterPublication["server_counts_available"])
	_, proxyCalls = core.callCounts()
	require.Equal(t, 1, proxyCalls, "published View must reach the server status proxy")
}

func TestCodebaseStatusBarrierRefreshesNoViewTargetAfterPublication(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	noView := fakeNoViewIndexBinding("33333333-3333-4333-8333-333333333333", "66666666-6666-4666-8666-666666666666")
	published := noView.Clone()
	published.Context = &uci.ContextRef{
		SourceID:          noView.Scope.SourceID,
		CheckoutID:        noView.Scope.CheckoutID,
		ViewID:            "44444444-4444-4444-8444-444444444444",
		AnalysisProfileID: noView.ProfileID,
		Generation:        1,
	}
	pendingChanges := int64(0)
	statusResponse, err := json.Marshal(map[string]any{
		"context": *published.Context,
		"freshness": uci.QueryFreshness{
			State:          uci.QueryFreshnessObservedCurrent,
			Method:         uci.QueryFreshnessWatchWatermark,
			PendingChanges: &pendingChanges,
			EnrichmentWatermark: uci.QueryEnrichmentWatermark{
				Sequence: 1,
				State:    uci.QueryEnrichmentCurrent,
			},
		},
	})
	require.NoError(t, err)
	statusResponse = testNestedStatusProxyPayload(t, statusResponse)
	var isPublished atomic.Bool
	initialBarrierResolved := make(chan struct{})
	releaseIndex := make(chan struct{})
	core := &fakeCore{
		indexGate:      releaseIndex,
		statusResponse: statusResponse,
		resolveBinding: func(_ int, _ string) uci.IndexBinding {
			if isPublished.Load() {
				return published
			}
			return noView
		},
		afterResolve: func(call int) {
			if call == 2 {
				close(initialBarrierResolved)
			}
		},
		afterIndex: func() { isPublished.Store(true) },
	}
	mod := newTestModule(core)
	h := moduletest.New(t)
	require.NoError(t, h.Register(mod))
	h.Freeze()
	p := testProjectContext("proj-no-view-barrier", t.TempDir())
	ctx := testTransportContext(p)
	contextHandle := "handle-" + p.ID

	raw, err := h.CallToolWithProject(ctx, p, "codebase_index", testIndexArgsForHandle(p, contextHandle))
	require.NoError(t, err)
	var started struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(raw, &started))
	require.NotEmpty(t, started.RunID)

	type barrierResponse struct {
		raw json.RawMessage
		err error
	}
	responses := make(chan barrierResponse, 1)
	go func() {
		response, callErr := h.CallToolWithProject(ctx, p, "codebase_status", testStatusArgsWithBarrier(contextHandle, started.RunID, 500))
		responses <- barrierResponse{raw: response, err: callErr}
	}()
	select {
	case <-initialBarrierResolved:
	case <-time.After(2 * time.Second):
		t.Fatal("barrier status did not resolve the initial no-View target")
	}
	require.False(t, isPublished.Load(), "initial barrier resolution must observe the no-View binding")
	close(releaseIndex)
	select {
	case response := <-responses:
		raw = response.raw
		err = response.err
	case <-time.After(2 * time.Second):
		t.Fatal("barrier status did not return after publication")
	}
	require.NoError(t, err)
	var status struct {
		Status                string             `json:"status"`
		RunID                 string             `json:"run_id"`
		Context               uci.ContextRef     `json:"context"`
		Freshness             uci.QueryFreshness `json:"freshness"`
		ServerCountsAvailable bool               `json:"server_counts_available"`
	}
	require.NoError(t, json.Unmarshal(raw, &status))
	require.Equal(t, "idle", status.Status)
	require.Equal(t, started.RunID, status.RunID)
	require.Equal(t, *published.Context, status.Context)
	require.True(t, status.ServerCountsAvailable)
	require.Equal(t, uci.QueryFreshnessPathHashBarrier, status.Freshness.Method)
	require.NotNil(t, status.Freshness.Barrier)
	require.Equal(t, uci.QueryBarrierSatisfied, status.Freshness.Barrier.State)
	require.NoError(t, status.Freshness.Validate())
	resolveCalls, proxyCalls := core.callCounts()
	require.GreaterOrEqual(t, resolveCalls, 3, "barrier status must resolve once before and once after waiting")
	require.Equal(t, 1, proxyCalls)
}

func TestCodebaseStatusBarrierFailsClosedForFailedRun(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	core := &fakeCore{indexErr: errors.New("synthetic prepared-index failure")}
	mod := newTestModule(core)
	h := moduletest.New(t)
	require.NoError(t, h.Register(mod))
	h.Freeze()
	p := testProjectContext("proj-failed-barrier", t.TempDir())
	ctx := testTransportContext(p)
	contextHandle := "handle-" + p.ID

	raw, err := h.CallToolWithProject(ctx, p, "codebase_index", testIndexArgsForHandle(p, contextHandle))
	require.NoError(t, err)
	var started struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(raw, &started))
	require.NotEmpty(t, started.RunID)

	raw, err = h.CallToolWithProject(ctx, p, "codebase_status", testStatusArgsWithBarrier(contextHandle, started.RunID, 500))
	require.Nil(t, raw)
	require.EqualError(t, err, "codebase_status: after_barrier run failed")
	_, proxyCalls := core.callCounts()
	require.Zero(t, proxyCalls, "failed local barrier must not proxy stale server evidence")
}

func TestCodebaseToolsRejectMissingTransportSessionWithoutEnvironmentFallback(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	core := &fakeCore{}
	mod := newTestModule(core)
	p := testProjectContext("proj-missing-transport", t.TempDir())

	raw, err := mod.HandleTool(context.Background(), p, "codebase_status", testStatusArgs(p))
	require.Nil(t, raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "UCI transport session")
	resolveCalls, proxyCalls := core.callCounts()
	require.Zero(t, resolveCalls)
	require.Zero(t, proxyCalls)
}

func TestCodebaseStatusRejectsInvalidAfterBarrierBeforeResolution(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	p := testProjectContext("proj-status-validation", t.TempDir())

	for _, test := range []struct {
		name string
		args json.RawMessage
	}{
		{name: "null", args: json.RawMessage(`{"context_handle":"handle-proj-status-validation","after_barrier":null}`)},
		{name: "unknown field", args: json.RawMessage(`{"context_handle":"handle-proj-status-validation","after_barrier":{"token":"barrier","wait_ms":1,"sequence":1}}`)},
		{name: "empty token", args: testStatusArgsWithBarrier("handle-proj-status-validation", "", 1)},
		{name: "oversized token", args: testStatusArgsWithBarrier("handle-proj-status-validation", strings.Repeat("x", 2_049), 1)},
		{name: "zero wait", args: testStatusArgsWithBarrier("handle-proj-status-validation", "barrier", 0)},
		{name: "oversized wait", args: testStatusArgsWithBarrier("handle-proj-status-validation", "barrier", 60_001)},
		{name: "project", args: json.RawMessage(`{"context_handle":"handle-proj-status-validation","project":"forbidden"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			core := &fakeCore{}
			mod := newTestModule(core)
			raw, err := mod.HandleTool(context.Background(), p, "codebase_status", test.args)
			require.Nil(t, raw)
			require.Error(t, err)
			resolveCalls, proxyCalls := core.callCounts()
			require.Zero(t, resolveCalls, "invalid after_barrier must fail before target resolution")
			require.Zero(t, proxyCalls, "invalid after_barrier must fail before status proxying")
		})
	}
}

func TestCodebaseStatusSchemaMirrorsStrictBarrierContract(t *testing.T) {
	mod := newTestModule(&fakeCore{})
	var schema map[string]any
	for _, tool := range mod.Tools() {
		if tool.Name == "codebase_status" {
			require.NoError(t, json.Unmarshal(tool.InputSchema, &schema))
			break
		}
	}
	require.NotNil(t, schema)
	require.Equal(t, false, schema["additionalProperties"])
	require.Equal(t, []any{"context_handle"}, schema["required"])

	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	require.Len(t, properties, 2)
	barrier, ok := properties["after_barrier"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, false, barrier["additionalProperties"])
	require.Equal(t, []any{"token", "wait_ms"}, barrier["required"])
	barrierProperties, ok := barrier["properties"].(map[string]any)
	require.True(t, ok)
	token, ok := barrierProperties["token"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(2_048), token["maxLength"])
	waitMS, ok := barrierProperties["wait_ms"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(1), waitMS["minimum"])
	require.Equal(t, float64(60_000), waitMS["maximum"])
}

func TestCodebaseStatusPropagatesProxyIsError(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	expected := &module.ProxyIsError{RawContent: json.RawMessage(`{"type":"text","text":"server rejected status"}`)}
	mod := newTestModule(&fakeCore{statusErr: expected})
	p := testProjectContext("proj-proxy-is-error", t.TempDir())

	raw, err := mod.HandleTool(testTransportContext(p), p, "codebase_status", testStatusArgs(p))
	require.Nil(t, raw)
	require.Same(t, expected, err, "the dispatcher must receive the original ProxyIsError sentinel")
}

func TestCodebaseStatusDegradesGenericProxyFailure(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	mod := newTestModule(&fakeCore{statusErr: context.DeadlineExceeded})
	p := testProjectContext("proj-generic-proxy-error", t.TempDir())

	raw, err := mod.HandleTool(testTransportContext(p), p, "codebase_status", testStatusArgs(p))
	require.NoError(t, err)
	var status map[string]any
	require.NoError(t, json.Unmarshal(raw, &status))
	require.Equal(t, "never_indexed", status["status"])
	require.Equal(t, false, status["server_counts_available"])
	require.Contains(t, status["server_counts_error"], context.DeadlineExceeded.Error())
}

func TestCodebaseStatusPreservesCallerCancellation(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	mod := newTestModule(&fakeCore{statusErr: context.Canceled})
	p := testProjectContext("proj-cancelled-status", t.TempDir())
	ctx, cancel := context.WithCancel(testTransportContext(p))
	cancel()

	raw, err := mod.HandleTool(ctx, p, "codebase_status", testStatusArgs(p))
	require.Nil(t, raw)
	require.ErrorIs(t, err, context.Canceled)
}

// TestCodebaseIndex_FlagOffReturnsError verifies that tools return an error
// when ENGRAM_CODE_INTEL_ENABLED is not set to "true".
func TestCodebaseIndex_FlagOffReturnsError(t *testing.T) {
	// Do NOT set ENGRAM_CODE_INTEL_ENABLED — it must be absent/false.
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "false")

	core := &fakeCore{}
	mod := newTestModule(core)

	h := moduletest.New(t)
	require.NoError(t, h.Register(mod))
	h.Freeze()

	p := testProjectContext("proj-flagoff", t.TempDir())
	args := testIndexArgs(p)

	_, err := h.CallToolWithProject(context.Background(), p, "codebase_index", args)
	require.Error(t, err, "codebase_index must return an error when flag is off")
	assert.Contains(t, err.Error(), "ENGRAM_CODE_INTEL_ENABLED")
}
