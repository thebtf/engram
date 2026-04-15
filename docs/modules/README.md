# Engram Module Author Guide

This guide explains how to write a module for the engram daemon. A module is a
self-contained component that plugs into the daemon's session and tool
namespaces. The first release ships one built-in module (`engramcore`) and the
framework that routes to it; every future capability (loom task processing,
vector search, semantic refactor) will be an additional module.

## What Is a Module?

A module is any Go type that satisfies `module.EngramModule` (defined in
`internal/module/module.go`). It provides a stable name, an init hook, and a
shutdown hook. Beyond the core contract the module may opt into optional
capabilities — the framework discovers them at registration time via type
assertion and never performs runtime type assertions in hot-path code.

A module is **not** a goroutine, a background thread, or a persistent process.
It is a stateful value whose lifetime is managed by the lifecycle pipeline. It
may start its own background goroutines inside `Init` (capturing
`deps.DaemonCtx`, not the init-phase `ctx` — see "Lifecycle" below and
clarification C3 in `spec.md`), but the goroutines are the module's
responsibility; the framework does not manage them.

The maximum footprint for a minimal, tools-only module is approximately 80 LOC:
`Name()`, `Init()`, `Shutdown()`, `Tools()`, and `HandleTool()`.

### Core Interface

```go
// From internal/module/module.go
type EngramModule interface {
    Name()     string
    Init(ctx context.Context, deps ModuleDeps) error
    Shutdown(ctx context.Context) error
}
```

The `Init` context has a **30-second deadline** (NFR-2). Modules that need more
time must refactor to an async init pattern (start background goroutine, return
immediately). The `Shutdown` context shares a **30-second deadline** across all
modules (NFR-3); respect `ctx.Done()`.

### Dependency Injection

Every `Init` call receives a fully-populated `ModuleDeps`:

```go
// From internal/module/module.go
type ModuleDeps struct {
    Logger     *slog.Logger          // pre-scoped to module name
    DaemonCtx  context.Context       // daemon-lifetime; safe to capture
    StorageDir string                // $DATA_DIR/modules/<name>/, 0700
    Config     json.RawMessage       // raw JSON config slice, may be nil
    Notifier   muxcore.Notifier      // push unsolicited notifications
    Lookup     ModuleLookup          // read-only view of registry
}
```

`StorageDir` follows the convention from clarification C5: it is created with
`0700` permissions before `Init` is called. The module owns its contents
entirely; no other module reads or writes there (NFR-10 storage isolation).

## Minimum Contract

Implement `EngramModule` and register it in `cmd/engram/main.go`:

```go
package mymod

import (
    "context"

    "github.com/thebtf/engram/internal/module"
)

type Module struct{}

func New() *Module { return &Module{} }

func (m *Module) Name() string { return "mymod" }

func (m *Module) Init(_ context.Context, deps module.ModuleDeps) error {
    // Do NOT store ctx — it is init-phase-scoped and is cancelled when
    // Init returns. Store deps.DaemonCtx for background goroutines.
    return nil
}

func (m *Module) Shutdown(_ context.Context) error { return nil }
```

Registration in `cmd/engram/main.go` (one line, no other changes):

```go
registry.Register(mymod.New())
```

## Optional Capabilities

The framework discovers optional interfaces at `Register` time using type
assertions. There are five optional capabilities:

### 1. `ToolProvider` — static MCP tools

```go
// From internal/module/capabilities.go
type ToolProvider interface {
    Tools() []ToolDef
    HandleTool(ctx context.Context, p muxcore.ProjectContext,
               name string, args json.RawMessage) (json.RawMessage, error)
}
```

`Tools()` is called **once** at registration; the list must be stable for the
daemon's lifetime. Tool names are globally unique — a conflict fails fast at
`Register` time (FR-3). `HandleTool` is called concurrently; the module must be
thread-safe. The **soft contract is <1 second** wall-clock (NFR-1, FR-13); the
dispatcher enforces a hard 30-second cap (FR-14, clarification C3). For
operations that exceed 1 second, submit a background task and return a task
reference immediately.

### 2. `ProxyToolProvider` — dynamic MCP tools (FR-11a)

```go
// From internal/module/capabilities.go
type ProxyToolProvider interface {
    ProxyTools(ctx context.Context, p muxcore.ProjectContext) ([]ToolDef, error)
    ProxyHandleTool(ctx context.Context, p muxcore.ProjectContext,
                    name string, args json.RawMessage) (json.RawMessage, error)
}
```

Use this when the tool list is fetched from a backend at runtime (e.g.
`engramcore` proxies 68+ tools discovered via a gRPC `Initialize` handshake).
At most **one** module per daemon may implement `ProxyToolProvider`; a second
registration fails with `registry.ErrMultipleProxyToolProviders` (FR-11a).

Dispatcher routing: static `ToolProvider` registry is checked first; if the
tool is not found AND a `ProxyToolProvider` is registered, the call is
forwarded via `ProxyHandleTool`; if still not found, JSON-RPC -32601 is
returned.

### 3. `Snapshotter` — state persistence across restart

```go
// From internal/module/capabilities.go
type Snapshotter interface {
    Snapshot() ([]byte, error)
    Restore(data []byte) error
}
```

Called by the lifecycle pipeline during graceful restart: `Snapshot` (before
`Shutdown`) and `Restore` (after `Init`). Use `module.MarshalSnapshot` and
`module.UnmarshalSnapshot` to handle versioned envelopes; see "Snapshots"
below.

### 4. `ProjectLifecycle` — session connect/disconnect events

```go
// From internal/module/capabilities.go
type ProjectLifecycle interface {
    OnSessionConnect(p muxcore.ProjectContext)
    OnSessionDisconnect(projectID string)
}
```

`OnSessionConnect` fires for the **first** session bound to a project.
`OnSessionDisconnect` fires when the **last** session for that project closes.
Do **not** cancel long-running tasks on disconnect — tasks outlive sessions by
design (FR-9).

### 5. `ProjectRemovalAware` — explicit project deletion

```go
// From internal/module/capabilities.go
type ProjectRemovalAware interface {
    OnProjectRemoved(projectID string)
}
```

Fires when a project is deleted via the dashboard or API. Drop per-project
caches and close per-project connections here. Errors are logged but do not
block other modules.

## Lifecycle

### Startup sequence

```
Freeze registry
  → Init all modules (forward registration order, fail-fast on error)
  → Restore Snapshotter modules (forward order)
  → Run (hand control to muxcore engine)
```

An `Init` error aborts daemon startup. Any modules that already completed
`Init` are shut down in reverse order before the process exits (FR-5).

### Shutdown sequence

```
Drain (stop accepting new sessions, await in-flight HandleTool calls)
  → Snapshot all Snapshotter modules (reverse registration order)
  → Shutdown all modules (reverse registration order)
  → Socket cleanup
```

### Graceful restart (Phase 8, closes #71)

The Node shim (`ensure-binary.js`) sends `graceful-restart` over the daemon's
control socket. The daemon then:

1. Drains (5-second deadline for in-flight calls)
2. Snapshots all `Snapshotter` modules
3. Shuts down all modules
4. Swaps the binary (`upgrade.Swap`)
5. Re-execs the new binary

The new process runs `Init → Restore → Run`, transparently picking up
persisted module state. See `docs/modules/lifecycle.md` for the full sequence.

### Init context vs DaemonCtx (clarification C3)

The `ctx` passed to `Init` is **init-phase-scoped** — it is cancelled as soon
as `Init` returns. You **must not** store it for use in background goroutines.
For long-lived background work, capture `deps.DaemonCtx` instead:

```go
func (m *Module) Init(ctx context.Context, deps module.ModuleDeps) error {
    // WRONG: m.ctx = ctx  (will be cancelled immediately after Init returns)
    m.ctx = deps.DaemonCtx  // correct: daemon-lifetime context
    go m.runBackground()
    return nil
}
```

This is clarification C3 from `spec.md` and is the most common Init mistake.

## Errors

The framework uses a three-layer error model (FR-12):

| Layer | When | How |
|---|---|---|
| Protocol | JSON-RPC violations (`-32xxx`) | Dispatcher returns these directly |
| Module | Structured tool errors for AI clients | Return `*module.ModuleError` from `HandleTool` |
| Internal | Go errors logged with stack trace | Never surfaced verbatim to clients |

`ModuleError` constructors (from `internal/module/errors.go`):

```go
module.ErrNotReady("backend warming up", 500*time.Millisecond)
module.ErrProjectNotFound(projectID)
module.ErrToolDisabled(toolName, "requires loom module")
module.ErrResourceExhausted("grpc-pool")
module.ErrUpstreamUnavailable("engram-server", err)
module.ErrTimeout(wallClock)
module.ErrPreconditionFailed("session not initialised", nil)
```

Full taxonomy and real-world examples: `docs/modules/errors.md`.

## Snapshots

Use the `SnapshotEnvelope` helpers from `internal/module/snapshot.go`:

```go
// Snapshot() implementation
func (m *Module) Snapshot() ([]byte, error) {
    return module.MarshalSnapshot(1, m.state)
}

// Restore() implementation
func (m *Module) Restore(data []byte) error {
    payload, version, err := module.UnmarshalSnapshot(data, 1 /* maxSupported */)
    if err != nil {
        // ErrUnsupportedVersion — pipeline already logged a WARN;
        // start with defaults.
        m.state = defaultState()
        return nil
    }
    if len(payload) == 0 {
        m.state = defaultState() // first boot
        return nil
    }
    _ = version // for diagnostics if needed
    return json.Unmarshal(payload, &m.state)
}
```

`SnapshotEnvelope` carries an integer `Version` field. If the loaded version
exceeds `maxSupported`, `UnmarshalSnapshot` returns `ErrUnsupportedVersion`.
The pipeline logs a WARN, discards the payload, and calls `Restore(nil)` so
the module starts with default state (US7 forward-compat).

On disk, each module's payload is stored as `snapshot.bin` under its
`StorageDir`. A top-level `MANIFEST.json` is written atomically last (FR-6).

## Testing

Use `internal/moduletest.Harness` to test your module without booting a full
daemon. See `docs/modules/testing.md` for a complete copy-pasteable example.

## Common Pitfalls

1. **Storing the Init `ctx`** — it is cancelled immediately after `Init`
   returns. Always store `deps.DaemonCtx` for background goroutines (C3).

2. **Exceeding the HandleTool 1-second soft contract** (NFR-1, FR-13) — the
   dispatcher enforces a hard 30-second cap (C3), but you should stay under 1
   second for synchronous operations. Submit long-running work as a background
   task and return a task reference.

3. **Panics in module-owned goroutines** (FR-15) — the framework recovers
   panics in `Init`, `Shutdown`, `HandleTool`, `Snapshot`, `Restore`, and
   lifecycle callbacks. It does **not** recover panics in goroutines that the
   module starts itself. Wrap your goroutines:

   ```go
   go func() {
       defer func() {
           if r := recover(); r != nil {
               deps.Logger.Error("goroutine panic", "panic", r)
           }
       }()
       // ... background work
   }()
   ```

4. **Reading from another module's StorageDir** — forbidden (NFR-10). Use
   `deps.Lookup` for cross-module communication, not the filesystem.

5. **Registering a second `ProxyToolProvider`** — fails fast at `Register`
   time (FR-11a). Only `engramcore` uses this interface; static `ToolProvider`
   is the correct path for all other modules.

---

See also:
- `docs/modules/lifecycle.md` — pipeline phases, graceful restart, control socket auth
- `docs/modules/errors.md` — error taxonomy, all 7 `ModuleError` codes, real-world examples
- `docs/modules/testing.md` — `moduletest.Harness` usage with full working example
