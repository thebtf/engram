package codeintel_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/handlers/codeintel"
	"github.com/thebtf/engram/internal/moduletest"
	"github.com/thebtf/engram/internal/uci"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

const (
	uciCutoverSharedProjectID = "uci-cutover-shared-project"
	uciCutoverSessionA        = "uci-cutover-transport-a"
	uciCutoverSessionB        = "uci-cutover-transport-b"
	uciCutoverSessionC        = "uci-cutover-transport-c"
	uciCutoverHostSession     = "same-host-session-must-not-be-used"
	uciCutoverHandleA         = "uci-cutover-handle-a"
	uciCutoverHandleB         = "uci-cutover-handle-b"
	uciCutoverRelativePath    = "internal/shared.go"
	uciCutoverLabel           = "SharedTarget"
)

type uciCutoverRow struct {
	RelativePath string `json:"relative_path"`
	Label        string `json:"label"`
	Body         string `json:"body"`
}

type uciCutoverEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type uciCutoverEvidenceRecorder struct {
	State           string `json:"state"`
	LastFailureCode string `json:"last_failure_code"`
}

type uciCutoverServerStatus struct {
	Context          uci.ContextRef             `json:"context"`
	Rows             []uciCutoverRow            `json:"rows"`
	Edges            []uciCutoverEdge           `json:"edges"`
	EvidenceRecorder uciCutoverEvidenceRecorder `json:"evidence_recorder"`
	TotalChunks      int                        `json:"total_chunks"`
	EmbeddedChunks   int                        `json:"embedded_chunks"`
	LastIndexedAt    string                     `json:"last_indexed_at"`
	Freshness        uci.QueryFreshness         `json:"freshness"`
}

type uciCutoverStatus struct {
	Status           string                     `json:"status"`
	RunID            string                     `json:"run_id"`
	Error            string                     `json:"error"`
	Context          uci.ContextRef             `json:"context"`
	Rows             []uciCutoverRow            `json:"rows"`
	Edges            []uciCutoverEdge           `json:"edges"`
	EvidenceRecorder uciCutoverEvidenceRecorder `json:"evidence_recorder"`
	TotalChunks      int                        `json:"total_chunks"`
	EmbeddedChunks   int                        `json:"embedded_chunks"`
	LastIndexedAt    string                     `json:"last_indexed_at"`
	Freshness        uci.QueryFreshness         `json:"freshness"`
}

type uciCutoverAfterBarrier struct {
	Token  string `json:"token"`
	WaitMS int64  `json:"wait_ms"`
}

type uciCutoverStart struct {
	Status string `json:"status"`
	RunID  string `json:"run_id"`
}

type uciCutoverTargetKey struct {
	ClientSessionID string
	ContextHandle   string
}

type uciCutoverResolveCall struct {
	ClientSessionID  string
	TransportSession string
	ContextHandle    string
	Project          muxcore.ProjectContext
	Target           codeintel.ResolvedIndexTarget
	Resolved         bool
}

type uciCutoverIndexCall struct {
	TransportSession string
	Target           codeintel.ResolvedIndexTarget
	Root             string
}

type uciCutoverProxyCall struct {
	TransportSession string
	Target           codeintel.ResolvedIndexTarget
	Name             string
	Args             json.RawMessage
}

type uciCutoverGate struct {
	started   chan struct{}
	release   chan struct{}
	completed chan struct{}

	startedOnce   sync.Once
	releaseOnce   sync.Once
	completedOnce sync.Once
}

func newUCICutoverGate() *uciCutoverGate {
	return &uciCutoverGate{
		started:   make(chan struct{}),
		release:   make(chan struct{}),
		completed: make(chan struct{}),
	}
}

func (gate *uciCutoverGate) markStarted() {
	gate.startedOnce.Do(func() { close(gate.started) })
}

func (gate *uciCutoverGate) releaseRun() {
	gate.releaseOnce.Do(func() { close(gate.release) })
}

func (gate *uciCutoverGate) markCompleted() {
	gate.completedOnce.Do(func() { close(gate.completed) })
}

type uciCutoverCore struct {
	mu sync.Mutex

	targets  map[uciCutoverTargetKey]codeintel.ResolvedIndexTarget
	results  map[uciCutoverTargetKey]codeintel.IndexResult
	statuses map[uciCutoverTargetKey]json.RawMessage
	gates    map[uciCutoverTargetKey]*uciCutoverGate

	resolveCalls []uciCutoverResolveCall
	indexCalls   []uciCutoverIndexCall
	proxyCalls   []uciCutoverProxyCall
}

func newUCICutoverCore(targets []codeintel.ResolvedIndexTarget, statuses map[uciCutoverTargetKey]json.RawMessage) *uciCutoverCore {
	core := &uciCutoverCore{
		targets:  make(map[uciCutoverTargetKey]codeintel.ResolvedIndexTarget, len(targets)),
		results:  make(map[uciCutoverTargetKey]codeintel.IndexResult, len(targets)),
		statuses: statuses,
		gates:    make(map[uciCutoverTargetKey]*uciCutoverGate, len(targets)),
	}
	for _, target := range targets {
		key := uciCutoverKeyForTarget(target)
		core.targets[key] = target
		binding := target.BindingClone()
		if binding.Context != nil {
			core.results[key] = codeintel.IndexResult{Context: *binding.Context}
		}
		core.gates[key] = newUCICutoverGate()
	}
	return core
}

func (core *uciCutoverCore) ResolveIndexTarget(ctx context.Context, project muxcore.ProjectContext, contextHandle string) (codeintel.ResolvedIndexTarget, error) {
	clientSessionID := auditcontext.UCITransportSession(ctx)
	key := uciCutoverTargetKey{ClientSessionID: clientSessionID, ContextHandle: contextHandle}

	core.mu.Lock()
	target, found := core.targets[key]
	core.resolveCalls = append(core.resolveCalls, uciCutoverResolveCall{
		ClientSessionID:  clientSessionID,
		TransportSession: clientSessionID,
		ContextHandle:    contextHandle,
		Project:          project,
		Target:           target,
		Resolved:         found,
	})
	core.mu.Unlock()

	if !auditcontext.ValidUCITransportSession(clientSessionID) {
		return codeintel.ResolvedIndexTarget{}, fmt.Errorf("missing transport session")
	}
	if !found {
		return codeintel.ResolvedIndexTarget{}, fmt.Errorf("context handle %q is not owned by client transport %q", contextHandle, clientSessionID)
	}
	return target, nil
}

func (core *uciCutoverCore) IndexCodebase(ctx context.Context, target codeintel.ResolvedIndexTarget, root string) (*codeintel.IndexResult, error) {
	transportSession := auditcontext.UCITransportSession(ctx)
	key := uciCutoverKeyForTarget(target)

	core.mu.Lock()
	expected, found := core.targets[key]
	result, hasResult := core.results[key]
	gate := core.gates[key]
	core.indexCalls = append(core.indexCalls, uciCutoverIndexCall{TransportSession: transportSession, Target: target, Root: root})
	core.mu.Unlock()

	if transportSession != target.ClientSessionID {
		return nil, fmt.Errorf("index transport session does not match target")
	}
	if !found || !reflect.DeepEqual(expected, target) {
		return nil, fmt.Errorf("index received an unrecognized typed target")
	}
	if !hasResult || gate == nil {
		return nil, fmt.Errorf("index has no configured result for typed target")
	}
	if root == "" {
		return nil, fmt.Errorf("index root is empty")
	}

	gate.markStarted()
	select {
	case <-gate.release:
	case <-ctx.Done():
		gate.markCompleted()
		return nil, ctx.Err()
	}
	gate.markCompleted()
	return &result, nil
}

func (core *uciCutoverCore) ProxyHandleTool(ctx context.Context, target codeintel.ResolvedIndexTarget, name string, args json.RawMessage) (json.RawMessage, error) {
	transportSession := auditcontext.UCITransportSession(ctx)
	key := uciCutoverKeyForTarget(target)

	core.mu.Lock()
	expected, found := core.targets[key]
	payload, hasPayload := core.statuses[key]
	core.proxyCalls = append(core.proxyCalls, uciCutoverProxyCall{
		TransportSession: transportSession,
		Target:           target,
		Name:             name,
		Args:             append(json.RawMessage(nil), args...),
	})
	core.mu.Unlock()

	if transportSession != target.ClientSessionID {
		return nil, fmt.Errorf("proxy transport session does not match target")
	}
	if !found || !reflect.DeepEqual(expected, target) {
		return nil, fmt.Errorf("proxy received an unrecognized typed target")
	}
	if name != "codebase_status" || !hasPayload {
		return nil, fmt.Errorf("unexpected typed proxy call %q", name)
	}
	return append(json.RawMessage(nil), payload...), nil
}

func (core *uciCutoverCore) setResult(target codeintel.ResolvedIndexTarget, result codeintel.IndexResult) {
	core.mu.Lock()
	core.results[uciCutoverKeyForTarget(target)] = result
	core.mu.Unlock()
}

func (core *uciCutoverCore) awaitStarted(t *testing.T, target codeintel.ResolvedIndexTarget) {
	t.Helper()
	uciCutoverAwait(t, core.gateFor(t, target).started, "index fake start")
}

func (core *uciCutoverCore) release(t *testing.T, target codeintel.ResolvedIndexTarget) {
	t.Helper()
	core.gateFor(t, target).releaseRun()
}

func (core *uciCutoverCore) awaitCompleted(t *testing.T, target codeintel.ResolvedIndexTarget) {
	t.Helper()
	uciCutoverAwait(t, core.gateFor(t, target).completed, "index fake completion")
}

func (core *uciCutoverCore) releaseAll() {
	core.mu.Lock()
	gates := make([]*uciCutoverGate, 0, len(core.gates))
	for _, gate := range core.gates {
		gates = append(gates, gate)
	}
	core.mu.Unlock()
	for _, gate := range gates {
		gate.releaseRun()
	}
}

func (core *uciCutoverCore) gateFor(t *testing.T, target codeintel.ResolvedIndexTarget) *uciCutoverGate {
	t.Helper()
	key := uciCutoverKeyForTarget(target)
	core.mu.Lock()
	gate := core.gates[key]
	core.mu.Unlock()
	require.NotNil(t, gate, "missing fake gate for typed target")
	return gate
}

func (core *uciCutoverCore) resolveCallsSnapshot() []uciCutoverResolveCall {
	core.mu.Lock()
	defer core.mu.Unlock()
	return append([]uciCutoverResolveCall(nil), core.resolveCalls...)
}

func (core *uciCutoverCore) indexCallsSnapshot() []uciCutoverIndexCall {
	core.mu.Lock()
	defer core.mu.Unlock()
	return append([]uciCutoverIndexCall(nil), core.indexCalls...)
}

func (core *uciCutoverCore) proxyCallsSnapshot() []uciCutoverProxyCall {
	core.mu.Lock()
	defer core.mu.Unlock()
	return append([]uciCutoverProxyCall(nil), core.proxyCalls...)
}

func (core *uciCutoverCore) awaitResolveCallCount(t *testing.T, count int) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		if len(core.resolveCallsSnapshot()) >= count {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("typed resolver did not reach %d calls", count)
		default:
			runtime.Gosched()
		}
	}
}

type uciCutoverFixture struct {
	t       *testing.T
	harness *moduletest.Harness
	core    *uciCutoverCore

	projectA muxcore.ProjectContext
	projectB muxcore.ProjectContext
	projectC muxcore.ProjectContext
	ctxA     context.Context
	ctxB     context.Context
	ctxC     context.Context
	rootA    string
	rootB    string
	targetA  codeintel.ResolvedIndexTarget
	targetB  codeintel.ResolvedIndexTarget
	statusA  uciCutoverServerStatus
	statusB  uciCutoverServerStatus
}

func newUCICutoverFixture(t *testing.T) *uciCutoverFixture {
	t.Helper()
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	t.Setenv(config.EnvClaudeSessionID, "host-global-session-must-not-be-used")

	rootBase := filepath.Join(t.TempDir(), "compatible", "worktree")
	rootA := filepath.Join(rootBase, "checkout-a")
	rootB := filepath.Join(rootBase, "checkout-b")
	require.NoError(t, os.MkdirAll(rootA, 0o755))
	require.NoError(t, os.MkdirAll(rootB, 0o755))

	contextA := uciCutoverContext("30000000-0000-4000-8000-000000000001", "40000000-0000-4000-8000-000000000001", 1)
	contextB := uciCutoverContext("30000000-0000-4000-8000-000000000002", "40000000-0000-4000-8000-000000000002", 2)
	targetA := uciCutoverResolvedTarget(
		uciCutoverSessionA,
		uciCutoverHandleA,
		contextA,
		"50000000-0000-4000-8000-000000000001",
	)
	targetB := uciCutoverResolvedTarget(
		uciCutoverSessionB,
		uciCutoverHandleB,
		contextB,
		"50000000-0000-4000-8000-000000000002",
	)

	pendingChanges := int64(0)
	freshnessA := uci.QueryFreshness{
		State:          uci.QueryFreshnessObservedCurrent,
		Method:         uci.QueryFreshnessWatchWatermark,
		PendingChanges: &pendingChanges,
		EnrichmentWatermark: uci.QueryEnrichmentWatermark{
			Sequence: 1,
			State:    uci.QueryEnrichmentCurrent,
		},
	}
	freshnessB := uci.QueryFreshness{
		State:          uci.QueryFreshnessCatchingUp,
		Method:         uci.QueryFreshnessWatchWatermark,
		PendingChanges: &pendingChanges,
		EnrichmentWatermark: uci.QueryEnrichmentWatermark{
			Sequence: 2,
			State:    uci.QueryEnrichmentPending,
		},
	}

	statusA := uciCutoverServerStatus{
		Context: contextA,
		Rows: []uciCutoverRow{{
			RelativePath: uciCutoverRelativePath,
			Label:        uciCutoverLabel,
			Body:         "saved-body-A",
		}},
		Edges: []uciCutoverEdge{{From: uciCutoverLabel, To: "A-Callee"}},
		EvidenceRecorder: uciCutoverEvidenceRecorder{
			State:           "healthy",
			LastFailureCode: "NONE",
		},
		TotalChunks:    23,
		EmbeddedChunks: 17,
		LastIndexedAt:  "2026-09-05T12:00:00Z",
		Freshness:      freshnessA,
	}
	statusB := uciCutoverServerStatus{
		Context: contextB,
		Rows: []uciCutoverRow{{
			RelativePath: uciCutoverRelativePath,
			Label:        uciCutoverLabel,
			Body:         "saved-body-B",
		}},
		Edges: []uciCutoverEdge{{From: uciCutoverLabel, To: "B-Callee"}},
		EvidenceRecorder: uciCutoverEvidenceRecorder{
			State:           "degraded",
			LastFailureCode: "COMPLETION_EVIDENCE_UNAVAILABLE",
		},
		TotalChunks:    23,
		EmbeddedChunks: 19,
		LastIndexedAt:  "2026-09-05T12:01:00Z",
		Freshness:      freshnessB,
	}

	core := newUCICutoverCore(
		[]codeintel.ResolvedIndexTarget{targetA, targetB},
		map[uciCutoverTargetKey]json.RawMessage{
			uciCutoverKeyForTarget(targetA): uciCutoverStatusJSON(t, statusA),
			uciCutoverKeyForTarget(targetB): uciCutoverStatusJSON(t, statusB),
		},
	)

	mod := codeintel.NewModuleWithCore(core)
	harness := moduletest.New(t)
	require.NoError(t, harness.Register(mod))
	harness.Freeze()
	t.Cleanup(core.releaseAll)

	hostEnv := func() map[string]string {
		return map[string]string{config.EnvClaudeSessionID: uciCutoverHostSession}
	}
	projectA := muxcore.ProjectContext{
		ID:  uciCutoverSharedProjectID,
		Cwd: rootA,
		Env: hostEnv(),
	}
	projectB := muxcore.ProjectContext{
		ID:  uciCutoverSharedProjectID,
		Cwd: rootB,
		Env: hostEnv(),
	}
	projectC := muxcore.ProjectContext{
		ID:  uciCutoverSharedProjectID,
		Cwd: rootA,
		Env: hostEnv(),
	}

	return &uciCutoverFixture{
		t:        t,
		harness:  harness,
		core:     core,
		projectA: projectA,
		projectB: projectB,
		projectC: projectC,
		ctxA:     auditcontext.WithUCITransportSession(context.Background(), uciCutoverSessionA),
		ctxB:     auditcontext.WithUCITransportSession(context.Background(), uciCutoverSessionB),
		ctxC:     auditcontext.WithUCITransportSession(context.Background(), uciCutoverSessionC),
		rootA:    rootA,
		rootB:    rootB,
		targetA:  targetA,
		targetB:  targetB,
		statusA:  statusA,
		statusB:  statusB,
	}
}

func TestUCICutoverKeepsSameProjectCallersIsolated(t *testing.T) {
	fixture := newUCICutoverFixture(t)
	require.Equal(t, fixture.projectA.Cwd, fixture.projectC.Cwd, "A and C model identical host roots")
	require.Equal(t, fixture.projectA.Env[config.EnvClaudeSessionID], fixture.projectC.Env[config.EnvClaudeSessionID], "A and C model identical host sessions")
	require.NotEqual(t, auditcontext.UCITransportSession(fixture.ctxA), auditcontext.UCITransportSession(fixture.ctxC), "each downstream transport needs a fresh tag")

	startedA, err := fixture.start(fixture.ctxA, fixture.projectA, fixture.rootA, uciCutoverHandleA)
	require.NoError(t, err)
	require.Equal(t, "started", startedA.Status)
	require.NotEmpty(t, startedA.RunID)
	fixture.core.awaitStarted(t, fixture.targetA)

	repeatedA, err := fixture.start(fixture.ctxA, fixture.projectA, fixture.rootA, uciCutoverHandleA)
	require.NoError(t, err)
	require.Equal(t, "already_running", repeatedA.Status)
	require.Equal(t, startedA.RunID, repeatedA.RunID)

	startedB, err := fixture.start(fixture.ctxB, fixture.projectB, fixture.rootB, uciCutoverHandleB)
	require.NoError(t, err)
	require.Equal(t, "started", startedB.Status)
	require.NotEmpty(t, startedB.RunID)
	require.NotEqual(t, startedA.RunID, startedB.RunID)
	fixture.core.awaitStarted(t, fixture.targetB)

	indexCalls := fixture.core.indexCallsSnapshot()
	require.Len(t, indexCalls, 2, "only the two distinct typed targets may start index work")
	requireUCICutoverIndexCall(t, indexCalls[0], fixture.targetA, fixture.rootA)
	requireUCICutoverIndexCall(t, indexCalls[1], fixture.targetB, fixture.rootB)

	fixture.core.release(t, fixture.targetA)
	fixture.core.awaitCompleted(t, fixture.targetA)
	statusA := fixture.waitForStatus(fixture.ctxA, fixture.projectA, uciCutoverHandleA, "idle")

	fixture.core.release(t, fixture.targetB)
	fixture.core.awaitCompleted(t, fixture.targetB)
	statusB := fixture.waitForStatus(fixture.ctxB, fixture.projectB, uciCutoverHandleB, "idle")

	requireUCICutoverStatus(t, statusA, fixture.statusA, fixture.statusB)
	requireUCICutoverStatus(t, statusB, fixture.statusB, fixture.statusA)
	require.Equal(t, statusA.TotalChunks, statusB.TotalChunks, "recorder health must not be inferred from chunk counts")
	require.NotEqual(t, statusA.EvidenceRecorder, statusB.EvidenceRecorder, "daemon must preserve each authorized context's exact secret-free recorder health")

	resolveCalls := fixture.core.resolveCallsSnapshot()
	requireUCICutoverResolveCalls(t, resolveCalls, fixture.targetA)
	requireUCICutoverResolveCalls(t, resolveCalls, fixture.targetB)
	requireUCICutoverProxyCalls(t, fixture.core.proxyCallsSnapshot(), fixture.targetA, fixture.targetB)

	_, err = fixture.call(fixture.ctxC, fixture.projectC, "codebase_index", map[string]any{
		"root":           fixture.rootA,
		"context_handle": uciCutoverHandleA,
	})
	require.Error(t, err, "client C must not start an A-owned context through the shared project ID")
	_, err = fixture.call(fixture.ctxC, fixture.projectC, "codebase_status", map[string]any{
		"context_handle": uciCutoverHandleA,
	})
	require.Error(t, err, "client C must not query an A-owned context through the shared project ID")

	statusAAfterC := fixture.waitForStatus(fixture.ctxA, fixture.projectA, uciCutoverHandleA, "idle")
	statusBAfterC := fixture.waitForStatus(fixture.ctxB, fixture.projectB, uciCutoverHandleB, "idle")
	require.Equal(t, statusA, statusAAfterC, "client C must not mutate A's state or default")
	require.Equal(t, statusB, statusBAfterC, "client C must not mutate B's state or default")
	require.Len(t, fixture.core.indexCallsSnapshot(), 2, "client C must not reach index execution")
}

func TestUCICutoverStatusWaitsForLocalBarrierAndReplaysExactToken(t *testing.T) {
	fixture := newUCICutoverFixture(t)
	started, err := fixture.start(fixture.ctxA, fixture.projectA, fixture.rootA, uciCutoverHandleA)
	require.NoError(t, err)
	require.Equal(t, "started", started.Status)
	fixture.core.awaitStarted(t, fixture.targetA)

	type barrierResult struct {
		status uciCutoverStatus
		err    error
	}
	results := make(chan barrierResult, 1)
	go func() {
		status, callErr := fixture.barrierStatus(fixture.ctxA, fixture.projectA, uciCutoverHandleA, uciCutoverAfterBarrier{Token: started.RunID, WaitMS: 25})
		results <- barrierResult{status: status, err: callErr}
	}()
	fixture.core.awaitResolveCallCount(t, 2)
	for range 8 {
		runtime.Gosched()
	}
	require.Empty(t, fixture.core.proxyCallsSnapshot(), "local barrier must not proxy before durable completion")

	fixture.core.release(t, fixture.targetA)
	var status uciCutoverStatus
	select {
	case received := <-results:
		require.NoError(t, received.err)
		status = received.status
	case <-time.After(2 * time.Second):
		t.Fatal("local barrier did not finish after index completion")
	}
	require.Equal(t, "idle", status.Status)
	require.Equal(t, started.RunID, status.RunID)
	require.Equal(t, fixture.statusA.Context, status.Context)
	require.Equal(t, fixture.statusA.Rows, status.Rows)
	require.Equal(t, fixture.statusA.Edges, status.Edges)
	require.Equal(t, fixture.statusA.EvidenceRecorder, status.EvidenceRecorder)
	require.Equal(t, fixture.statusA.TotalChunks, status.TotalChunks)
	require.Equal(t, fixture.statusA.EmbeddedChunks, status.EmbeddedChunks)
	require.Equal(t, fixture.statusA.LastIndexedAt, status.LastIndexedAt)
	requireUCICutoverBarrierFreshness(t, status.Freshness, fixture.statusA.Freshness, 25, uci.QueryBarrierSatisfied)

	calls := fixture.core.proxyCallsSnapshot()
	require.Len(t, calls, 1)
	requireUCICutoverProxyArgs(t, calls[0])

	replayed, err := fixture.barrierStatus(fixture.ctxA, fixture.projectA, uciCutoverHandleA, uciCutoverAfterBarrier{Token: started.RunID, WaitMS: 25})
	require.NoError(t, err)
	require.Equal(t, status, replayed, "the retained token must replay its immutable terminal receipt")
	calls = fixture.core.proxyCallsSnapshot()
	require.Len(t, calls, 2)
	requireUCICutoverProxyArgs(t, calls[1])
}

func TestUCICutoverStatusBarrierTimesOutWithoutClaimingCompletion(t *testing.T) {
	fixture := newUCICutoverFixture(t)
	started, err := fixture.start(fixture.ctxA, fixture.projectA, fixture.rootA, uciCutoverHandleA)
	require.NoError(t, err)
	fixture.core.awaitStarted(t, fixture.targetA)

	status, err := fixture.barrierStatus(fixture.ctxA, fixture.projectA, uciCutoverHandleA, uciCutoverAfterBarrier{Token: started.RunID, WaitMS: 1})
	require.NoError(t, err)
	require.Equal(t, "running", status.Status)
	require.Equal(t, started.RunID, status.RunID)
	require.Equal(t, fixture.statusA.Context, status.Context)
	requireUCICutoverBarrierFreshness(t, status.Freshness, fixture.statusA.Freshness, 1, uci.QueryBarrierTimedOut)
	calls := fixture.core.proxyCallsSnapshot()
	require.Len(t, calls, 1)
	requireUCICutoverProxyArgs(t, calls[0])

	fixture.core.release(t, fixture.targetA)
	fixture.core.awaitCompleted(t, fixture.targetA)
}

func TestUCICutoverStatusRejectsForeignAndStaleBarrierTokens(t *testing.T) {
	fixture := newUCICutoverFixture(t)
	started, err := fixture.start(fixture.ctxA, fixture.projectA, fixture.rootA, uciCutoverHandleA)
	require.NoError(t, err)
	fixture.core.awaitStarted(t, fixture.targetA)

	_, err = fixture.barrierStatus(fixture.ctxB, fixture.projectB, uciCutoverHandleB, uciCutoverAfterBarrier{Token: started.RunID, WaitMS: 25})
	require.ErrorContains(t, err, "stale")
	_, err = fixture.barrierStatus(fixture.ctxA, fixture.projectA, uciCutoverHandleA, uciCutoverAfterBarrier{Token: "unknown-local-run", WaitMS: 25})
	require.ErrorContains(t, err, "stale")
	require.Empty(t, fixture.core.proxyCallsSnapshot(), "foreign or unknown tokens must not reach server status")

	fixture.core.release(t, fixture.targetA)
	fixture.core.awaitCompleted(t, fixture.targetA)
}

func TestUCICutoverStatusBarrierPreservesCallerCancellation(t *testing.T) {
	fixture := newUCICutoverFixture(t)
	started, err := fixture.start(fixture.ctxA, fixture.projectA, fixture.rootA, uciCutoverHandleA)
	require.NoError(t, err)
	fixture.core.awaitStarted(t, fixture.targetA)

	cancelled, cancel := context.WithCancel(fixture.ctxA)
	cancel()
	_, err = fixture.barrierStatus(cancelled, fixture.projectA, uciCutoverHandleA, uciCutoverAfterBarrier{Token: started.RunID, WaitMS: 25})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, fixture.core.proxyCallsSnapshot(), "cancelled local barrier must not proxy")

	fixture.core.release(t, fixture.targetA)
	fixture.core.awaitCompleted(t, fixture.targetA)
}

func TestUCICutoverStatusUsesPriorTokenAfterLaterRunReplacesLiveness(t *testing.T) {
	fixture := newUCICutoverFixture(t)
	first, err := fixture.start(fixture.ctxA, fixture.projectA, fixture.rootA, uciCutoverHandleA)
	require.NoError(t, err)
	fixture.core.awaitStarted(t, fixture.targetA)
	fixture.core.release(t, fixture.targetA)
	firstCurrent := fixture.waitForStatus(fixture.ctxA, fixture.projectA, uciCutoverHandleA, "idle")
	require.Equal(t, first.RunID, firstCurrent.RunID)

	second, err := fixture.start(fixture.ctxA, fixture.projectA, fixture.rootA, uciCutoverHandleA)
	require.NoError(t, err)
	require.Equal(t, "started", second.Status)
	require.NotEqual(t, first.RunID, second.RunID)
	secondCurrent := fixture.waitForStatus(fixture.ctxA, fixture.projectA, uciCutoverHandleA, "idle")
	require.Equal(t, second.RunID, secondCurrent.RunID)

	prior, err := fixture.barrierStatus(fixture.ctxA, fixture.projectA, uciCutoverHandleA, uciCutoverAfterBarrier{Token: first.RunID, WaitMS: 25})
	require.NoError(t, err)
	require.Equal(t, "idle", prior.Status)
	require.Equal(t, first.RunID, prior.RunID, "barrier lookup must not consult the mutable current row")
	requireUCICutoverBarrierFreshness(t, prior.Freshness, fixture.statusA.Freshness, 25, uci.QueryBarrierSatisfied)
}

func TestUCICutoverRejectsMismatchedIndexResult(t *testing.T) {
	fixture := newUCICutoverFixture(t)
	contextB := fixture.targetB.ContextClone()
	require.NotNil(t, contextB)
	fixture.core.setResult(fixture.targetA, codeintel.IndexResult{Context: *contextB})

	started, err := fixture.start(fixture.ctxA, fixture.projectA, fixture.rootA, uciCutoverHandleA)
	require.NoError(t, err)
	require.Equal(t, "started", started.Status)
	fixture.core.awaitStarted(t, fixture.targetA)
	fixture.core.release(t, fixture.targetA)
	fixture.core.awaitCompleted(t, fixture.targetA)

	status := fixture.waitForStatus(fixture.ctxA, fixture.projectA, uciCutoverHandleA, "error")
	require.NotEmpty(t, status.Error)
	require.NotEqual(t, "idle", status.Status, "a mismatched returned ContextRef must not become idle success")

	indexCalls := fixture.core.indexCallsSnapshot()
	require.Len(t, indexCalls, 1)
	requireUCICutoverIndexCall(t, indexCalls[0], fixture.targetA, fixture.rootA)
}

func (fixture *uciCutoverFixture) start(ctx context.Context, project muxcore.ProjectContext, root, contextHandle string) (uciCutoverStart, error) {
	raw, err := fixture.call(ctx, project, "codebase_index", map[string]any{
		"root":           root,
		"context_handle": contextHandle,
	})
	if err != nil {
		return uciCutoverStart{}, err
	}
	var started uciCutoverStart
	if err := json.Unmarshal(raw, &started); err != nil {
		return uciCutoverStart{}, fmt.Errorf("decode codebase_index result: %w", err)
	}
	return started, nil
}

func (fixture *uciCutoverFixture) waitForStatus(ctx context.Context, project muxcore.ProjectContext, contextHandle, wantStatus string) uciCutoverStatus {
	fixture.t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()

	for {
		status, err := fixture.status(ctx, project, contextHandle)
		if err == nil && status.Status == wantStatus {
			return status
		}
		select {
		case <-deadline.C:
			fixture.t.Fatalf("codebase_status for %q did not reach %q", contextHandle, wantStatus)
			return uciCutoverStatus{}
		default:
			runtime.Gosched()
		}
	}
}

func (fixture *uciCutoverFixture) status(ctx context.Context, project muxcore.ProjectContext, contextHandle string) (uciCutoverStatus, error) {
	raw, err := fixture.call(ctx, project, "codebase_status", map[string]any{"context_handle": contextHandle})
	if err != nil {
		return uciCutoverStatus{}, err
	}
	var status uciCutoverStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return uciCutoverStatus{}, fmt.Errorf("decode codebase_status result: %w", err)
	}
	return status, nil
}

func (fixture *uciCutoverFixture) barrierStatus(ctx context.Context, project muxcore.ProjectContext, contextHandle string, barrier uciCutoverAfterBarrier) (uciCutoverStatus, error) {
	raw, err := fixture.call(ctx, project, "codebase_status", map[string]any{
		"context_handle": contextHandle,
		"after_barrier":  barrier,
	})
	if err != nil {
		return uciCutoverStatus{}, err
	}
	var status uciCutoverStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return uciCutoverStatus{}, fmt.Errorf("decode barrier codebase_status result: %w", err)
	}
	return status, nil
}

func (fixture *uciCutoverFixture) call(ctx context.Context, project muxcore.ProjectContext, name string, args any) (json.RawMessage, error) {
	fixture.t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal %s args: %w", name, err)
	}
	return fixture.harness.CallToolWithProject(ctx, project, name, raw)
}

func uciCutoverResolvedTarget(clientSessionID, contextHandle string, context uci.ContextRef, incarnationID string) codeintel.ResolvedIndexTarget {
	return codeintel.ResolvedIndexTarget{
		ClientSessionID: clientSessionID,
		ContextHandle:   contextHandle,
		Binding: uci.IndexBinding{
			Context: &context,
			Scope: uci.IndexScope{
				SourceID:      context.SourceID,
				CheckoutID:    context.CheckoutID,
				IncarnationID: incarnationID,
			},
			ProfileID:     context.AnalysisProfileID,
			LocalRootID:   "uci-cutover-root",
			WorkstationID: "uci-cutover-workstation",
		},
	}
}

func uciCutoverContext(checkoutID, viewID string, generation int64) uci.ContextRef {
	spaceID := "10000000-0000-4000-8000-000000000001"
	return uci.ContextRef{
		SpaceID:           &spaceID,
		SourceID:          "20000000-0000-4000-8000-000000000001",
		CheckoutID:        checkoutID,
		ViewID:            viewID,
		AnalysisProfileID: "60000000-0000-4000-8000-000000000001",
		Generation:        generation,
	}
}

func uciCutoverKeyForTarget(target codeintel.ResolvedIndexTarget) uciCutoverTargetKey {
	return uciCutoverTargetKey{
		ClientSessionID: target.ClientSessionID,
		ContextHandle:   target.ContextHandle,
	}
}

func uciCutoverStatusJSON(t *testing.T, status uciCutoverServerStatus) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(status)
	require.NoError(t, err)
	return raw
}

func uciCutoverAwait(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func requireUCICutoverIndexCall(t *testing.T, got uciCutoverIndexCall, want codeintel.ResolvedIndexTarget, root string) {
	t.Helper()
	require.Equal(t, want.ClientSessionID, got.TransportSession, "index work must retain the calling transport tag")
	require.Equal(t, want, got.Target)
	require.Equal(t, root, got.Root)
}

func requireUCICutoverResolveCalls(t *testing.T, calls []uciCutoverResolveCall, want codeintel.ResolvedIndexTarget) {
	t.Helper()
	found := false
	for _, call := range calls {
		if !call.Resolved {
			continue
		}
		require.Equal(t, call.ClientSessionID, call.TransportSession, "module must derive the client session from the transport context")
		require.Equal(t, uciCutoverHostSession, call.Project.Env[config.EnvClaudeSessionID], "host session is compatibility-only and shared")
		if call.ClientSessionID != want.ClientSessionID || call.ContextHandle != want.ContextHandle {
			continue
		}
		found = true
		require.Equal(t, uciCutoverSharedProjectID, call.Project.ID)
		require.Equal(t, want, call.Target)
	}
	require.Truef(t, found, "missing typed resolve call for session=%q handle=%q", want.ClientSessionID, want.ContextHandle)
}

func requireUCICutoverProxyCalls(t *testing.T, calls []uciCutoverProxyCall, targets ...codeintel.ResolvedIndexTarget) {
	t.Helper()
	require.NotEmpty(t, calls)
	seen := make(map[uciCutoverTargetKey]bool, len(targets))
	for _, call := range calls {
		require.Equal(t, call.Target.ClientSessionID, call.TransportSession, "status proxy must retain the calling transport tag")
		requireUCICutoverProxyArgs(t, call)
		seen[uciCutoverKeyForTarget(call.Target)] = true
	}
	for _, target := range targets {
		require.Truef(t, seen[uciCutoverKeyForTarget(target)], "missing typed proxy call for session=%q handle=%q", target.ClientSessionID, target.ContextHandle)
	}
}

func requireUCICutoverProxyArgs(t *testing.T, call uciCutoverProxyCall) {
	t.Helper()
	require.Equal(t, "codebase_status", call.Name)
	var args map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(call.Args, &args))
	for _, forbidden := range []string{"after_barrier", "context", "project", "cwd", "root", "path"} {
		require.NotContains(t, args, forbidden, "status proxy must not receive %q", forbidden)
	}

	rawContextHandle, found := args["context_handle"]
	require.True(t, found)
	var contextHandle string
	require.NoError(t, json.Unmarshal(rawContextHandle, &contextHandle))
	require.Equal(t, call.Target.ContextHandle, contextHandle)
	require.Len(t, args, 1)
}

func requireUCICutoverStatus(t *testing.T, got uciCutoverStatus, want, forbidden uciCutoverServerStatus) {
	t.Helper()
	require.Equal(t, "idle", got.Status)
	require.NotEmpty(t, got.RunID)
	require.Equal(t, want.Context, got.Context)
	require.NotEqual(t, forbidden.Context, got.Context)
	require.Equal(t, want.Rows, got.Rows)
	require.Equal(t, want.Edges, got.Edges)
	require.Equal(t, want.EvidenceRecorder, got.EvidenceRecorder)
	require.Equal(t, want.TotalChunks, got.TotalChunks)
	require.Equal(t, want.EmbeddedChunks, got.EmbeddedChunks)
	require.Equal(t, want.LastIndexedAt, got.LastIndexedAt)
	require.Equal(t, want.Freshness, got.Freshness)
	require.Len(t, got.Rows, 1)
	require.Equal(t, uciCutoverRelativePath, got.Rows[0].RelativePath)
	require.Equal(t, uciCutoverLabel, got.Rows[0].Label)
	require.NotContains(t, got.Rows[0].Body, forbidden.Rows[0].Body)
	require.NotContains(t, got.Edges, forbidden.Edges[0])
}

func requireUCICutoverBarrierFreshness(t *testing.T, got, want uci.QueryFreshness, waitMS int64, state uci.QueryBarrierState) {
	t.Helper()
	require.Equal(t, want.State, got.State)
	require.Equal(t, uci.QueryFreshnessPathHashBarrier, got.Method)
	require.Equal(t, want.PendingChanges, got.PendingChanges)
	require.Equal(t, want.EnrichmentWatermark, got.EnrichmentWatermark)
	require.NotNil(t, got.Barrier)
	require.Equal(t, uci.QueryBarrier{
		Scope:      uci.QueryBarrierScope{Kind: uci.QueryBarrierPaths, PathCount: 1},
		DeadlineMS: waitMS,
		State:      state,
	}, *got.Barrier)
	require.NoError(t, got.Validate())
}
