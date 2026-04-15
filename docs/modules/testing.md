# Testing Modules with moduletest.Harness

The `internal/moduletest` package provides a test harness that lets you write
unit tests for your module without booting a full muxcore engine or opening any
network sockets. It wires together a registry, a lifecycle pipeline, and
per-module mock dependencies in a single `New(t)` call.

Sources:
- `internal/moduletest/harness.go` — `Harness` type, `New`, `Register`, `Freeze`
- `internal/moduletest/invoke.go` — `CallTool`, `CallToolWithProject`
- `internal/moduletest/simulate.go` — `SimulateSessionConnect`, `SimulateSessionDisconnect`, `SimulateProjectRemoved`, `SimulateShutdown`
- `internal/moduletest/snapshot.go` — `TakeSnapshot`
- `internal/moduletest/mocks.go` — `newMockDeps`, `NotificationRecord`, `recordingNotifier`

## Full Working Example

The following test exercises every harness method. Copy, paste, and adapt it
for your module.

```go
package mymod_test

import (
    "context"
    "encoding/json"
    "strings"
    "testing"

    "github.com/thebtf/engram/internal/module"
    "github.com/thebtf/engram/internal/moduletest"
    muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// ---------------------------------------------------------------------------
// Minimal module under test — replace with your real module.
// ---------------------------------------------------------------------------

type counterModule struct {
    deps    module.ModuleDeps
    counter int

    connectProjects  []string
    removedProjects  []string
    shutdownCount    int
}

func newCounterModule() *counterModule { return &counterModule{} }

func (m *counterModule) Name() string { return "counter" }

func (m *counterModule) Init(_ context.Context, deps module.ModuleDeps) error {
    m.deps = deps
    return nil
}

func (m *counterModule) Shutdown(_ context.Context) error {
    m.shutdownCount++
    return nil
}

// ToolProvider — exposes "counter.increment" and "counter.get"
func (m *counterModule) Tools() []module.ToolDef {
    schema := json.RawMessage(`{"type":"object","properties":{}}`)
    return []module.ToolDef{
        {Name: "counter.increment", Description: "adds 1", InputSchema: schema},
        {Name: "counter.get",       Description: "returns current value", InputSchema: schema},
    }
}

func (m *counterModule) HandleTool(
    _ context.Context,
    _ muxcore.ProjectContext,
    name string,
    _ json.RawMessage,
) (json.RawMessage, error) {
    switch name {
    case "counter.increment":
        m.counter++
        return json.RawMessage(`{"ok":true}`), nil
    case "counter.get":
        b, _ := json.Marshal(map[string]int{"value": m.counter})
        return b, nil
    default:
        return nil, module.ErrToolDisabled(name, "unknown tool")
    }
}

// Snapshotter — persists the counter value across restarts
func (m *counterModule) Snapshot() ([]byte, error) {
    return module.MarshalSnapshot(1, map[string]int{"counter": m.counter})
}

func (m *counterModule) Restore(data []byte) error {
    payload, _, err := module.UnmarshalSnapshot(data, 1)
    if err != nil || len(payload) == 0 {
        m.counter = 0
        return nil
    }
    var state map[string]int
    if err := json.Unmarshal(payload, &state); err != nil {
        return err
    }
    m.counter = state["counter"]
    return nil
}

// ProjectLifecycle — tracks connect events
func (m *counterModule) OnSessionConnect(p muxcore.ProjectContext) {
    m.connectProjects = append(m.connectProjects, p.ID)
}

func (m *counterModule) OnSessionDisconnect(_ string) {}

// ProjectRemovalAware — clears state on removal
func (m *counterModule) OnProjectRemoved(projectID string) {
    m.removedProjects = append(m.removedProjects, projectID)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCounterModule(t *testing.T) {
    mod := newCounterModule()

    // 1. Create harness and register the module.
    h := moduletest.New(t)
    if err := h.Register(mod); err != nil {
        t.Fatalf("Register: %v", err)
    }

    // 2. Freeze finalises registration and calls Init on each module.
    //    t.Cleanup is automatically registered to call SimulateShutdown.
    h.Freeze()

    // 3. Call a tool directly.
    _, err := h.CallTool(context.Background(), "counter.increment", nil)
    if err != nil {
        t.Fatalf("CallTool counter.increment: %v", err)
    }

    got, err := h.CallTool(context.Background(), "counter.get", nil)
    if err != nil {
        t.Fatalf("CallTool counter.get: %v", err)
    }
    if !strings.Contains(string(got), `"value":1`) {
        t.Errorf("counter.get = %s, want value:1", got)
    }

    // 4. Simulate a session connect event.
    h.SimulateSessionConnect(muxcore.ProjectContext{
        ID:  "proj-123",
        Cwd: "/home/user/repo",
    })
    if len(mod.connectProjects) != 1 || mod.connectProjects[0] != "proj-123" {
        t.Errorf("OnSessionConnect not called correctly; got %v", mod.connectProjects)
    }

    // 5. Simulate a project removal event.
    h.SimulateProjectRemoved("proj-123")
    if len(mod.removedProjects) != 1 || mod.removedProjects[0] != "proj-123" {
        t.Errorf("OnProjectRemoved not called correctly; got %v", mod.removedProjects)
    }

    // 6. Take a snapshot — no disk writes (TakeSnapshot returns raw bytes only).
    snapshots, err := h.TakeSnapshot()
    if err != nil {
        t.Fatalf("TakeSnapshot: %v", err)
    }
    snapshotBytes, ok := snapshots["counter"]
    if !ok {
        t.Fatal("TakeSnapshot: no entry for module counter")
    }
    if len(snapshotBytes) == 0 {
        t.Error("TakeSnapshot: snapshot bytes are empty")
    }

    // Verify the snapshot round-trip.
    payload, _, _ := module.UnmarshalSnapshot(snapshotBytes, 1)
    var state map[string]int
    if err := json.Unmarshal(payload, &state); err != nil {
        t.Fatalf("unmarshal snapshot payload: %v", err)
    }
    if state["counter"] != 1 {
        t.Errorf("snapshot counter = %d, want 1", state["counter"])
    }
}

func TestCounterModule_ToolNotFound(t *testing.T) {
    h := moduletest.New(t)
    if err := h.Register(newCounterModule()); err != nil {
        t.Fatalf("Register: %v", err)
    }
    h.Freeze()

    _, err := h.CallTool(context.Background(), "counter.nonexistent", nil)
    if err == nil {
        t.Fatal("expected error for unknown tool, got nil")
    }
    if !strings.Contains(err.Error(), "tool not found") {
        t.Errorf("error = %q, want 'tool not found'", err.Error())
    }
}
```

## Mock Dependencies Provided by the Harness

`moduletest.New(t)` injects the following mock dependencies into every
module's `Init` call via `module.ModuleDeps`:

| Field | Mock Value |
|---|---|
| `Logger` | `*slog.Logger` writing to `t.Log()` via `testingWriter`. Output appears only on test failure or with `-v`. Scoped to module name. |
| `DaemonCtx` | `context.Background()` — does not cancel unless the test explicitly cancels it. |
| `StorageDir` | Sub-directory under `t.TempDir()`: `<tmpdir>/modules/<name>/`. Created with `0700` permissions. Automatically removed after the test. |
| `Config` | `nil` (no config). Tests inject raw JSON directly if needed: `module.Config = json.RawMessage(...)` before `Freeze`. |
| `Notifier` | `*recordingNotifier` — records all `Notify` and `Broadcast` calls. Retrieve via `h.Notifications(moduleName)`. |
| `Lookup` | The harness registry, implementing `module.ModuleLookup`. `Has("name")` returns `true` for any registered module. |

### Asserting on Notifications

```go
// Module pushes a notification in its handler.
n := deps.Notifier
n.Notify("proj-123", []byte(`{"jsonrpc":"2.0","method":"mymod/event","params":{}}`))

// Retrieve and assert via the harness.
recs := h.Notifications("mymod")
if len(recs) != 1 {
    t.Fatalf("expected 1 notification, got %d", len(recs))
}
if recs[0].ProjectID != "proj-123" {
    t.Errorf("projectID = %q, want %q", recs[0].ProjectID, "proj-123")
}
```

`Broadcast` calls are recorded with an empty `ProjectID`.

## CallToolWithProject

When the tool under test branches on project identity, use
`CallToolWithProject` to supply a custom `muxcore.ProjectContext`:

```go
proj := muxcore.ProjectContext{ID: "custom-proj", Cwd: "/workspace"}
result, err := h.CallToolWithProject(context.Background(), proj, "mymod.tool", args)
```

## Shutdown Behaviour

`Freeze` registers a `t.Cleanup` function that calls `SimulateShutdown` when
the test exits, ensuring every module receives its `Shutdown` callback.
If you need to verify shutdown behaviour mid-test, call `h.SimulateShutdown()`
explicitly. Calling it multiple times is safe — the pipeline logs errors but
continues the shutdown fan-out.

## Tips

- Register multiple modules in the same harness to test inter-module
  interactions (e.g. `deps.Lookup.Has("peer-module")` returns true).
- The harness does not use the dispatcher — `CallTool` routes directly to the
  owning module's `HandleTool`. Protocol methods (`initialize`, `ping`) are
  not tested via the harness; use dispatcher unit tests for those.
- Use `h.Register` with a module that returns an error from `Init` to verify
  that your init error paths surface correctly. `Freeze` calls `t.Fatalf` on
  Init failure so the test fails fast with a clear message.
