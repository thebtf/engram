# Module Lifecycle

This document covers the lifecycle pipeline phases, the Init-context vs
DaemonCtx split, panic responsibility, control socket authorization, and the
graceful-restart sequence introduced in Phase 8 (closes #71).

## Init Context vs DaemonCtx (clarification C3)

Every module's `Init(ctx context.Context, deps module.ModuleDeps)` receives
**two** distinct contexts:

| Context | Lifetime | Use for |
|---|---|---|
| `ctx` (Init parameter) | Init-phase only; cancelled when `Init` returns | Blocking I/O during startup (DB ping, gRPC handshake, file load) |
| `deps.DaemonCtx` | Daemon lifetime; cancelled only on full shutdown | Background goroutines, long-lived connections, event loop |

**The single most important rule:** do not store `ctx`. If you do, your
background goroutines will receive a cancelled context the moment `Init`
returns, producing silent no-ops or confusing errors at runtime.

```go
// WRONG: stores the init-phase context
func (m *Module) Init(ctx context.Context, deps module.ModuleDeps) error {
    m.ctx = ctx          // cancelled after Init returns — DO NOT DO THIS
    go m.eventLoop()
    return nil
}

// CORRECT: stores the daemon-lifetime context
func (m *Module) Init(ctx context.Context, deps module.ModuleDeps) error {
    m.ctx = deps.DaemonCtx   // lives until daemon shuts down
    go m.eventLoop()
    return nil
}
```

The `ctx` parameter is intentionally scoped to the init phase so that Init
can apply a bounded deadline (30 seconds per NFR-2) without affecting the
module's runtime behavior. The lifecycle pipeline cancels it after `Init`
returns, regardless of whether `Init` succeeded or failed.

## Module Panic Responsibility (FR-15)

The lifecycle pipeline recovers panics in the following framework-managed call
sites:

- `Init`
- `Shutdown`
- `HandleTool` (via the dispatcher recover wrapper)
- `Snapshot` and `Restore`
- Session lifecycle callbacks (`OnSessionConnect`, `OnSessionDisconnect`,
  `OnProjectRemoved`)

A panic in any of these call sites is caught, logged with module name and stack
trace, and converted to an error (or a `-32603 internal error` response in the
`HandleTool` case). The daemon continues running; other sessions are
unaffected.

**The framework does NOT recover panics in module-owned goroutines.** A panic
in a goroutine that the module started will terminate the daemon process.
Module authors are responsible for adding recover blocks to any goroutines they
launch:

```go
go func() {
    defer func() {
        if r := recover(); r != nil {
            deps.Logger.Error("background goroutine panicked",
                "module", "mymod",
                "panic", r,
            )
        }
    }()
    // ... background work
}()
```

This design is intentional: framework recovery for unmanaged goroutines would
hide goroutine leaks and make debugging harder. Module authors bear the
responsibility because they control the goroutine's launch point.

## Control Socket Authorization (clarification C4)

The `graceful-restart` command is received over the daemon's control socket.
In v4.3.0, engram owns the control socket directly (not muxcore, which does
not yet expose this API). Authorization is determined entirely by the
**filesystem permissions on the socket file**:

- **Linux / macOS:** Unix domain socket at a path under `$XDG_RUNTIME_DIR` or
  `$DATA_DIR`. Default permissions `0600` (owner read/write only).
- **Windows:** Named pipe with ACL restricting access to the daemon's running
  user.

The daemon adds no additional authentication layer. Any process that can open
the socket is authorized. On multi-user systems, ensure the socket path and
permissions are configured so that only the operator's account can reach it.

This is clarification C4 from `spec.md`. A richer per-command authorization
model is deferred to a future release.

## Pipeline Phases

### Startup

```
1.  Freeze registry
         (no further Register calls accepted; ErrRegistryFrozen returned)
2.  Init all modules — forward registration order
         each Init deadline: 30 s per module (NFR-2)
         first Init error → Shutdown already-initialized modules in reverse,
         then abort daemon startup with diagnostic
3.  Restore Snapshotter modules — forward registration order
         nil/empty data = first boot, module uses defaults
         ErrUnsupportedVersion → WARN log + Restore(nil) + continue (US7)
         other error → WARN log + continue (non-fatal)
4.  Run — hand control to the dispatcher (muxcore engine)
```

### Shutdown

```
5.  Drain — stop accepting new sessions
         allow in-flight HandleTool calls to complete (5 s deadline)
         calls exceeding deadline: context cancelled, -32603 returned
6.  Snapshot all Snapshotter modules — REVERSE registration order
         errors logged but do NOT abort snapshot of other modules
         MANIFEST.json written atomically after all per-module files are stable
7.  Shutdown all modules — REVERSE registration order
         shared 30 s deadline across all modules (NFR-3)
         errors logged but do NOT abort shutdown of other modules
8.  Socket cleanup
```

Reverse order on shutdown and snapshot ensures that modules with dependencies
on others are cleaned up first (dependency graph flows in registration order;
teardown is the inverse).

## Graceful Restart Sequence (Phase 8, closes #71)

When the `graceful-restart` command arrives on the control socket, the daemon
executes the full drain → snapshot → shutdown → swap → exec chain:

```
1.  Drain (5-second deadline)
         atomic flag stops new session acceptance
         in-flight HandleTool calls complete or are cancelled
2.  SnapshotAll (reverse order)
         same as normal shutdown snapshot phase
         per-module snapshot.bin files + MANIFEST.json
3.  ShutdownAll (reverse order)
         modules release resources, close connections
4.  upgrade.Swap
         new binary (downloaded by ensure-binary.js) atomically replaces
         the running binary
         current binary moved to <path>.old.<pid> for rollback
5.  exec new binary
         new process starts with Init → Restore → Run
         modules transparently read their snapshot.bin and resume state
         sessions reconnect via muxcore session topology snapshot
```

On startup, the new binary calls `upgrade.CleanStale()` to remove any
`.old.<pid>` files from previous upgrades.

If `upgrade.Swap` fails (step 4), the old binary remains in place, the daemon
continues serving requests, and an error log with a non-zero exit code is
emitted. Manual operator intervention is required (rename `.old.<pid>` back or
re-download). Automatic rollback is a Phase B candidate.

### Snapshot Storage Layout

```
$DATA_DIR/
  modules/
    <module-name>/
      snapshot.bin      ← per-module opaque bytes (SnapshotEnvelope JSON)
  MANIFEST.json         ← top-level manifest, written atomically last
```

`MANIFEST.json` schema (version 1):

```json
{
  "schema_version": 1,
  "modules": [
    {
      "name": "mymod",
      "size": 128,
      "declared_version": 1
    }
  ]
}
```

On restore, the pipeline prefers the manifest for discovery. If the manifest is
missing or corrupt (e.g. crash between snapshot phase and manifest write), the
pipeline falls back to `os.Stat` discovery of per-module `snapshot.bin` files.

## Summary Diagram

```
STARTUP
┌─────────────────────────────────────────────────────┐
│  Freeze → Init[0..n] → Restore[Snapshotter, 0..n]  │
│  → Run (dispatcher handles requests)                │
└─────────────────────────────────────────────────────┘

SHUTDOWN / GRACEFUL RESTART
┌─────────────────────────────────────────────────────┐
│  Drain → Snapshot[Snapshotter, n..0]                │
│  → Shutdown[n..0] → Socket cleanup                  │
│  (+ Swap + exec for graceful restart)               │
└─────────────────────────────────────────────────────┘
```
