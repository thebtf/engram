# loom module

The `loom` module is the second `EngramModule` tenant in the engram daemon,
exposing 4 MCP tools backed by a CLI worker that shells out to allowlisted
binaries. Tasks are persisted in a local SQLite database and execute
asynchronously — clients submit work via `loom_submit` and poll via `loom_get`.

## Overview

The loom module mediates long-running agent tasks that must outlive a single
MCP session. It is designed for multi-tenant production use: every task is
scoped to an authenticated project, cross-project access is silently blocked,
and all task state survives daemon restarts via crash recovery.

Canonical source: `internal/handlers/loom/`
Loom library: `github.com/thebtf/aimux/loom@v0.1.0` — see
[CONTRACT.md](https://github.com/thebtf/aimux/blob/main/loom/CONTRACT.md)
for the authoritative Worker interface semantics.

## Architecture

```
loom_submit / loom_get / loom_list / loom_cancel
        ↓
internal/handlers/loom/tools.go       (ToolProvider impl, scoping, error mapping)
        ↓
internal/handlers/loom/loom_iface.go  (narrow loomEngine interface)
        ↓
github.com/thebtf/aimux/loom          (LoomEngine, task store, quality gate)
        ↓
${StorageDir}/tasks.db                (SQLite WAL, WAL/synchronous=NORMAL)
        ↓
internal/handlers/loom/workers.go     (cliWorker: exec.CommandContext + allowlist)
        ↓
allowlisted binary (codex | claude | aimux)
```

Task lifecycle events bubble up through `loom.EventBus` → `events.go` →
`muxcore.Notifier` as `notifications/loom/task_event` JSON-RPC pushes to
connected MCP sessions.

## RegisterWorker Extension Example

To add a `WorkerTypeThinker` worker in a follow-up PR, add a new worker type
in `workers.go` and register it inside `registerWorkers`:

```go
// thinkerWorker executes tasks via the aimux thinker API.
type thinkerWorker struct{ endpoint string }

func (w *thinkerWorker) Type() loom.WorkerType { return loom.WorkerTypeThinker }

func (w *thinkerWorker) Execute(ctx context.Context, task *loom.Task) (*loom.WorkerResult, error) {
    // ... call w.endpoint with task.Prompt, return WorkerResult
    return &loom.WorkerResult{Content: "..."}, nil
}

// In registerWorkers:
func registerWorkers(eng loomEngine, deps module.ModuleDeps) {
    eng.RegisterWorker(loom.WorkerTypeCLI, newCLIWorker())
    eng.RegisterWorker(loom.WorkerTypeThinker, &thinkerWorker{endpoint: "..."})
}
```

## Tool Reference

| Tool | Description | Required Fields |
|------|-------------|-----------------|
| `loom_submit` | Submit a background task. Returns `{task_id, status:"dispatched"}`. | `worker_type`, `prompt` |
| `loom_get` | Get the current state of a task by ID. | `task_id` |
| `loom_list` | List tasks for the current project. Optional `statuses` filter. | — |
| `loom_cancel` | Cancel a running task. Soft-success if already terminal. | `task_id` |

All tools are scoped to the authenticated session's project. Clients cannot
supply a `project_id` to access another project's tasks.

### loom_submit fields

| Field | Type | Notes |
|-------|------|-------|
| `worker_type` | string enum `["cli"]` | v4.4.0 only supports `cli`. |
| `prompt` | string, minLength 1 | Delivered to the worker on stdin. Never on the command line. |
| `cli` | string | Binary name (no path separators). Must be in allowlist: `codex`, `claude`, `aimux`. |
| `cwd` | string | Working directory for the subprocess. |
| `env` | object{string} | Env vars merged over daemon env. Keys must match `[A-Za-z_][A-Za-z0-9_]*`. |
| `model` | string | Passed as `--model` to the CLI. |
| `role` | string | Passed as `--role` to the CLI. |
| `effort` | string | Passed as `--effort` to the CLI. |
| `timeout_sec` | integer ≥ 0 | Task timeout in seconds. 0 = no timeout. |
| `metadata` | object | Free-form. Stored and returned by `loom_get`. |

## Operator Notes

- **SQLite WAL mode** is applied at Init time. The file is at
  `${ENGRAM_DATA_DIR}/modules/loom/tasks.db`.
- **Crash recovery**: stale `dispatched`/`running` tasks are marked
  `failed_crash` on daemon startup (before workers are registered).
- **Allowlist**: the default CLI allowlist is `[codex, claude, aimux]`.
  To extend it, add a new `WorkerType` and register a corresponding worker.
  There is no runtime config — the list is compile-time only.
- **Task retention**: tasks are never automatically purged in v4.4.0. Operators
  may run `DELETE FROM tasks WHERE completed_at < datetime('now', '-30 days')`
  directly against `tasks.db` for maintenance. A retention job is a v0.2.0 concern.
- **Observability**: loom OTel instruments (`loom.tasks.submitted`, etc.) emit
  through the engram meter once PR `loom-meter` lands. Until then a noop meter
  is used.
