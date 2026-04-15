# Engram Module Author Guide

This directory contains the contract documentation for authors of `EngramModule` implementations. Every module that plugs into the engram daemon satisfies the interfaces defined in `internal/module/` and follows the rules in the documents below.

The engram daemon is a host for modular components that share a session lifecycle and a tool namespace. Modules plug into three orthogonal extension points:

1. **Tools** — a module may expose MCP tools by implementing `ToolProvider`. Tool names are globally unique across all registered modules; conflict is detected at registration and aborts daemon startup.
2. **Lifecycle callbacks** — a module may observe session connect/disconnect (`ProjectLifecycle`) and project removal (`ProjectRemovalAware`) events via optional interfaces.
3. **Snapshot participation** — a module that needs to persist state across graceful restart implements `Snapshotter` and is called during the pre-shutdown snapshot phase and the post-init restore phase.

Capability discovery is automatic. The registry performs type assertions at `Register` time and caches typed references, so the lifecycle pipeline and dispatcher iterate pre-filtered lists without runtime type assertions in the hot path.

See each topic document for full rules and examples.

## Lifecycle

Rules governing the module lifecycle — `Init`, `Shutdown`, `Snapshot`, `Restore`, and the split between init-phase context (`Init(ctx)`) and daemon-lifetime context (`deps.DaemonCtx`). Also documents the graceful-restart flow (muxcore control socket) and authorization inheritance for the control command (filesystem permissions only).

Full content lives in `lifecycle.md`.

## Errors

The three-layer error model: Protocol (JSON-RPC `-32xxx`) / Module (`ModuleError` with stable code enum — `not_ready`, `project_not_found`, `tool_disabled`, `resource_exhausted`, `upstream_unavailable`, `timeout`, `precondition_failed`) / Internal (wrapped Go errors, logs only). Explains when to use each layer and why result-level `ModuleError` is NOT auto-retried by transport.

Full content lives in `errors.md`.

## Testing

How to use `internal/moduletest.Harness` to write unit tests for your module without booting a full daemon. Covers `Register`, `CallTool`, `SimulateSessionConnect`, `SimulateProjectRemoved`, `SimulateShutdown`, and `TakeSnapshot`. Includes the canonical engramcore test as the copy-pasteable starting point.

Full content lives in `testing.md`.

## Snapshots

Rules for `Snapshot()` and `Restore()` methods, the `SnapshotEnvelope{version int, data json.RawMessage}` helper, `MarshalSnapshot` / `UnmarshalSnapshot`, and the forward-compat fallback behaviour (`ErrUnsupportedVersion` → `Restore(nil)` with default state and a WARN log). Also documents the on-disk layout — per-module `snapshot.bin` files plus a top-level `MANIFEST.json` written atomically last.

Full content lives in `lifecycle.md` (snapshot is part of the lifecycle pipeline).
