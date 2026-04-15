package loom_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	loomlib "github.com/thebtf/aimux/loom"
	loomhandler "github.com/thebtf/engram/internal/handlers/loom"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/moduletest"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// ---------------------------------------------------------------------------
// Fake loomEngine — records calls without touching SQLite.
// ---------------------------------------------------------------------------

type fakeEngine struct {
	mu sync.Mutex

	recoverCrashedCalled bool
	cancelAllCalls       []string
	eventBus             *loomlib.EventBus
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		eventBus: loomlib.NewEventBus(nil),
	}
}

func (f *fakeEngine) RegisterWorker(_ loomlib.WorkerType, _ loomlib.Worker) {}

func (f *fakeEngine) RecoverCrashed() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recoverCrashedCalled = true
	return 0, nil
}

func (f *fakeEngine) Submit(_ context.Context, _ loomlib.TaskRequest) (string, error) {
	return "fake-task-id", nil
}

func (f *fakeEngine) Get(_ string) (*loomlib.Task, error) { return nil, nil }

func (f *fakeEngine) List(_ string, _ ...loomlib.TaskStatus) ([]*loomlib.Task, error) {
	return nil, nil
}

func (f *fakeEngine) Cancel(_ string) error { return nil }

func (f *fakeEngine) CancelAllForProject(projectID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelAllCalls = append(f.cancelAllCalls, projectID)
	return 0, nil
}

func (f *fakeEngine) Events() *loomlib.EventBus { return f.eventBus }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestLoomModule_Init_OpensTasksDB verifies that Init creates tasks.db inside
// StorageDir.
func TestLoomModule_Init_OpensTasksDB(t *testing.T) {
	t.Parallel()

	storageDir := t.TempDir()
	m := loomhandler.NewModule()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := m.Init(ctx, makeDeps(t, storageDir, nil)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	dbPath := filepath.Join(storageDir, "tasks.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Errorf("tasks.db not found at %s after Init", dbPath)
	}
}

// TestLoomModule_Init_CallsRecoverCrashed verifies that RecoverCrashed is
// invoked exactly once during Init.
func TestLoomModule_Init_CallsRecoverCrashed(t *testing.T) {
	t.Parallel()

	fake := newFakeEngine()
	m := loomhandler.NewModuleWithEngine(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := m.Init(ctx, makeDeps(t, t.TempDir(), nil)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !fake.recoverCrashedCalled {
		t.Error("RecoverCrashed was not called during Init")
	}
}

// TestLoomModule_Init_SubscribesToEvents verifies that the module subscribes
// to the engine EventBus during Init: emitting an event delivers a notification
// to the harness notifier.
func TestLoomModule_Init_SubscribesToEvents(t *testing.T) {
	t.Parallel()

	fake := newFakeEngine()
	m := loomhandler.NewModuleWithEngine(fake)

	h := moduletest.New(t)
	if err := h.Register(m); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h.Freeze()

	fake.eventBus.Emit(loomlib.TaskEvent{
		Type:      loomlib.EventTaskCreated,
		TaskID:    "t-probe",
		ProjectID: "proj-probe",
		Status:    loomlib.TaskStatusPending,
		Timestamp: time.Now().UTC(),
	})

	recs := h.Notifications("loom")
	if len(recs) == 0 {
		t.Error("no notifications received after EventBus.Emit; module likely did not subscribe")
	}
}

// TestLoomModule_Shutdown_UnsubscribesAndClosesDB verifies that Shutdown
// completes without error and that events emitted after Shutdown do not cause
// further notifications (the subscription was removed).
func TestLoomModule_Shutdown_UnsubscribesAndClosesDB(t *testing.T) {
	t.Parallel()

	fake := newFakeEngine()
	m := loomhandler.NewModuleWithEngine(fake)

	h := moduletest.New(t)
	if err := h.Register(m); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h.Freeze()

	// Emit before shutdown — should arrive.
	fake.eventBus.Emit(loomlib.TaskEvent{
		Type:      loomlib.EventTaskCreated,
		TaskID:    "t-before",
		ProjectID: "proj-1",
		Status:    loomlib.TaskStatusPending,
		Timestamp: time.Now().UTC(),
	})

	beforeCount := len(h.Notifications("loom"))

	// Explicitly shut down (the Harness cleanup will also call Shutdown,
	// but SimulateShutdown is idempotent in the pipeline).
	h.SimulateShutdown()

	// Emit after shutdown — the subscription should be gone.
	fake.eventBus.Emit(loomlib.TaskEvent{
		Type:      loomlib.EventTaskCompleted,
		TaskID:    "t-after",
		ProjectID: "proj-1",
		Status:    loomlib.TaskStatusCompleted,
		Timestamp: time.Now().UTC(),
	})

	afterCount := len(h.Notifications("loom"))
	if afterCount != beforeCount {
		t.Errorf("notifications grew after Shutdown (%d → %d); unsub likely not called",
			beforeCount, afterCount)
	}
}

// TestLoomModule_OnSessionDisconnect_DoesNotCancelTasks verifies that
// OnSessionDisconnect does NOT call CancelAllForProject. Per design.md §3.3
// tasks outlive sessions.
func TestLoomModule_OnSessionDisconnect_DoesNotCancelTasks(t *testing.T) {
	t.Parallel()

	fake := newFakeEngine()
	m := loomhandler.NewModuleWithEngine(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := m.Init(ctx, makeDeps(t, t.TempDir(), nil)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	m.OnSessionConnect(muxcore.ProjectContext{ID: "proj-1", Cwd: "/work"})
	m.OnSessionDisconnect("proj-1")

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.cancelAllCalls) != 0 {
		t.Errorf("CancelAllForProject was called on disconnect: %v", fake.cancelAllCalls)
	}
}

// TestLoomModule_OnProjectRemoved_CancelsAllTasks verifies that
// OnProjectRemoved invokes CancelAllForProject(projectID).
func TestLoomModule_OnProjectRemoved_CancelsAllTasks(t *testing.T) {
	t.Parallel()

	fake := newFakeEngine()
	m := loomhandler.NewModuleWithEngine(fake)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := m.Init(ctx, makeDeps(t, t.TempDir(), nil)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	m.OnProjectRemoved("proj-removed")

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.cancelAllCalls) != 1 || fake.cancelAllCalls[0] != "proj-removed" {
		t.Errorf("CancelAllForProject calls = %v, want [proj-removed]", fake.cancelAllCalls)
	}
}

// TestLoomModule_Snapshot_ReturnsEmpty verifies that Snapshot returns nil (or
// empty) bytes with no error. State lives in tasks.db, not the snapshot
// pipeline.
func TestLoomModule_Snapshot_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	h := moduletest.New(t)
	if err := h.Register(loomhandler.NewModule()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h.Freeze()

	snapshots, err := h.TakeSnapshot()
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}

	if b, ok := snapshots["loom"]; ok && len(b) != 0 {
		t.Errorf("Snapshot[loom] = %q (len=%d), want nil or empty", b, len(b))
	}
}

// TestLoomModule_HandleTaskEvent_EmitsNotification verifies that a TaskEvent
// emitted on the engine EventBus reaches the muxcore notifier as a
// notifications/loom/task_event JSON-RPC notification with the correct fields.
func TestLoomModule_HandleTaskEvent_EmitsNotification(t *testing.T) {
	t.Parallel()

	fake := newFakeEngine()
	m := loomhandler.NewModuleWithEngine(fake)

	h := moduletest.New(t)
	if err := h.Register(m); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h.Freeze()

	now := time.Now().UTC().Truncate(time.Millisecond)
	fake.eventBus.Emit(loomlib.TaskEvent{
		Type:      loomlib.EventTaskCompleted,
		TaskID:    "task-abc",
		ProjectID: "proj-xyz",
		RequestID: "req-001",
		Status:    loomlib.TaskStatusCompleted,
		Timestamp: now,
	})

	recs := h.Notifications("loom")
	if len(recs) == 0 {
		t.Fatal("no notifications recorded; expected one notifications/loom/task_event")
	}

	rec := recs[0]
	if rec.ProjectID != "proj-xyz" {
		t.Errorf("notification ProjectID = %q, want proj-xyz", rec.ProjectID)
	}

	var env struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(rec.Notification, &env); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}
	if env.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", env.JSONRPC)
	}
	if env.Method != "notifications/loom/task_event" {
		t.Errorf("method = %q, want notifications/loom/task_event", env.Method)
	}

	var payload struct {
		TaskID    string `json:"task_id"`
		ProjectID string `json:"project_id"`
		Type      string `json:"type"`
		Status    string `json:"status"`
		RequestID string `json:"request_id"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(env.Params, &payload); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if payload.TaskID != "task-abc" {
		t.Errorf("task_id = %q, want task-abc", payload.TaskID)
	}
	if payload.ProjectID != "proj-xyz" {
		t.Errorf("project_id = %q, want proj-xyz", payload.ProjectID)
	}
	if payload.Type != "task.completed" {
		t.Errorf("type = %q, want task.completed", payload.Type)
	}
	if payload.Status != "completed" {
		t.Errorf("status = %q, want completed", payload.Status)
	}
	if payload.RequestID != "req-001" {
		t.Errorf("request_id = %q, want req-001", payload.RequestID)
	}
	if payload.Timestamp == "" {
		t.Fatal("timestamp is empty, want RFC3339Nano string")
	}
	if _, err := time.Parse(time.RFC3339Nano, payload.Timestamp); err != nil {
		t.Fatalf("timestamp = %q, not RFC3339Nano: %v", payload.Timestamp, err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeDeps constructs a minimal module.ModuleDeps for tests that call Init
// directly rather than via the Harness. notifier may be nil.
func makeDeps(t *testing.T, storageDir string, notifier muxcore.Notifier) module.ModuleDeps {
	t.Helper()
	return module.ModuleDeps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DaemonCtx:  context.Background(),
		StorageDir: storageDir,
		Config:     nil,
		Notifier:   notifier,
		Lookup:     nil,
	}
}
