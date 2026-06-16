package codeintel_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/handlers/codeintel"
	"github.com/thebtf/engram/internal/moduletest"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// fakeCore is a minimal stand-in for *engramcore.Module that allows the
// codeintel module to be tested without a live gRPC server. It exposes the
// same methods the codeintel module calls: IndexCodebase and ProxyHandleTool.
type fakeCore struct {
	mu          sync.Mutex
	indexCalled int
	indexDelay  time.Duration
	indexErr    error

	// statusResponse is returned by ProxyHandleTool for codebase_status calls.
	statusResponse []byte
	statusErr      error
}

// IndexCodebase records the call and returns the configured result.
func (f *fakeCore) IndexCodebase(_ context.Context, _ muxcore.ProjectContext, _ string) (*codeintel.IndexResult, error) {
	f.mu.Lock()
	delay := f.indexDelay
	err := f.indexErr
	f.indexCalled++
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if err != nil {
		return nil, err
	}
	return &codeintel.IndexResult{Uploaded: 5, Embedded: 3, Deleted: 1}, nil
}

// ProxyHandleTool returns the configured status response for codebase_status calls.
func (f *fakeCore) ProxyHandleTool(_ context.Context, _ muxcore.ProjectContext, _ string, _ json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	if f.statusResponse != nil {
		return f.statusResponse, nil
	}
	// Default: return a minimal server payload.
	return json.Marshal(map[string]any{
		"total_chunks":    int64(10),
		"embedded_chunks": int64(8),
		"last_indexed_at": "2026-06-16T00:00:00Z",
	})
}

// newTestModule constructs a codeintel.Module backed by a fakeCore so tests can
// run without a live gRPC server.
func newTestModule(core codeintel.CoreProvider) *codeintel.Module {
	return codeintel.NewModuleWithCore(core)
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

	p := muxcore.ProjectContext{ID: "proj-1", Cwd: t.TempDir()}
	args, _ := json.Marshal(map[string]any{"root": p.Cwd})

	start := time.Now()
	raw, err := h.CallToolWithProject(context.Background(), p, "codebase_index", args)
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
		raw, err := h.CallToolWithProject(context.Background(), p, "codebase_status", nil)
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

	p := muxcore.ProjectContext{ID: "proj-concurrent", Cwd: t.TempDir()}
	args, _ := json.Marshal(map[string]any{"root": p.Cwd})

	// First call — should start.
	raw1, err := h.CallToolWithProject(context.Background(), p, "codebase_index", args)
	require.NoError(t, err)
	var r1 map[string]any
	require.NoError(t, json.Unmarshal(raw1, &r1))
	require.Equal(t, "started", r1["status"], "first call must return 'started'")

	// Second call — should see the running state.
	raw2, err := h.CallToolWithProject(context.Background(), p, "codebase_index", args)
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

	p := muxcore.ProjectContext{ID: "proj-race", Cwd: t.TempDir()}
	args, _ := json.Marshal(map[string]any{"root": p.Cwd})

	const n = 16
	var wg sync.WaitGroup
	var startedCount, alreadyCount atomic.Int32
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			raw, err := h.CallToolWithProject(context.Background(), p, "codebase_index", args)
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

	core := &fakeCore{}
	mod := newTestModule(core)

	h := moduletest.New(t)
	require.NoError(t, h.Register(mod))
	h.Freeze()

	p := muxcore.ProjectContext{ID: "proj-new", Cwd: t.TempDir()}
	raw, err := h.CallToolWithProject(context.Background(), p, "codebase_status", nil)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result))
	assert.Equal(t, "never_indexed", result["status"])
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

	p := muxcore.ProjectContext{ID: "proj-transition", Cwd: t.TempDir()}
	args, _ := json.Marshal(map[string]any{"root": p.Cwd})

	// Start the index.
	raw, err := h.CallToolWithProject(context.Background(), p, "codebase_index", args)
	require.NoError(t, err)
	var startResult map[string]any
	require.NoError(t, json.Unmarshal(raw, &startResult))
	assert.Equal(t, "started", startResult["status"])

	// Poll until idle or timeout.
	deadline := time.Now().Add(2 * time.Second)
	var finalStatus string
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		raw2, err2 := h.CallToolWithProject(context.Background(), p, "codebase_status", nil)
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

	p := muxcore.ProjectContext{ID: "proj-flagoff", Cwd: t.TempDir()}
	args, _ := json.Marshal(map[string]any{"root": p.Cwd})

	_, err := h.CallToolWithProject(context.Background(), p, "codebase_index", args)
	require.Error(t, err, "codebase_index must return an error when flag is off")
	assert.Contains(t, err.Error(), "ENGRAM_CODE_INTEL_ENABLED")
}
