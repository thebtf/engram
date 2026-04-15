# Module Error Taxonomy

The engram module framework uses a **three-layer** error model (FR-12). This
document enumerates every layer, when to use each, and provides one real-world
example per `ModuleError` code.

## Three-Layer Taxonomy

```
Layer 1 — Protocol   JSON-RPC -32xxx codes
Layer 2 — Module     result-level ModuleError with stable code enum
Layer 3 — Internal   wrapped Go errors; logged, never surfaced to clients
```

### Layer 1 — Protocol Layer (`-32xxx` JSON-RPC errors)

Used for violations of the JSON-RPC / MCP protocol itself. The dispatcher
generates these; modules do not return them directly.

| Code | Meaning |
|------|---------|
| `-32700` | Parse error — malformed JSON in request |
| `-32601` | Method not found — unknown tool name with no proxy fallback |
| `-32602` | Invalid params — required argument missing or wrong type |
| `-32603` | Internal error — unrecovered panic, dispatcher timeout, or internal bug |

A `-32603` response always means a stack trace was logged server-side. If you
see `-32603` from a `tools/call` request, check the daemon's structured logs.

### Layer 2 — Module Layer (`ModuleError` with stable code enum)

Returned from `HandleTool` (and `ProxyHandleTool`) as the MCP **result** field
with `isError: true`. This is intentionally different from a JSON-RPC error
response: it reaches the AI agent client as structured data so the agent can
reason about the error code and decide on retry strategy.

**Result-level errors are NOT retried at transport.** The MCP transport layer
sees a successful JSON-RPC response containing a structured error payload. The
AI agent parses the content and decides whether to retry, escalate, or abort.
This design preserves byte-identity with upstream server errors and lets
agents apply domain-specific retry logic.

Use `*module.ModuleError` constructors from `internal/module/errors.go`.
Do not construct `ModuleError` directly with ad-hoc codes.

### Layer 3 — Internal Layer (wrapped Go errors)

Any Go error that does not map cleanly to a `ModuleError` code should be
returned as a plain `error`. The dispatcher boundary catches it, maps it to
JSON-RPC `-32603`, and logs the full wrapped chain with structured fields
(module name, tool name, session ID, project ID). The error text is **never**
surfaced to the client verbatim.

## ModuleError Codes

Source: `internal/module/errors.go`. The `proxy_is_error` sentinel is defined
in `internal/module/proxy_iserror.go`.

### `not_ready`

The module has not yet completed initialisation or its backend dependency is
temporarily unavailable. The `RetryAfter` field provides a suggested wait time.

```go
// LLM client is still establishing its connection pool on first use.
return module.ErrNotReady("backend warming up", 500*time.Millisecond)
```

The agent should wait `RetryAfter` and reissue the call. If `RetryAfter` is
nil in the JSON response, use an exponential backoff.

### `project_not_found`

The project ID supplied in the request context cannot be resolved to a known
project. This is a permanent error for this request — the project does not
exist on this server.

```go
// Slug cache miss AND server lookup returned 404.
return module.ErrProjectNotFound(p.ID)
```

The agent should not retry without a different project context.

### `tool_disabled`

The named tool exists in the tool registry but has been disabled, either by
configuration or because a required dependency (e.g. another module) is absent.

```go
// The "task.submit" tool requires the loom module, which is not registered
// in this binary build.
return module.ErrToolDisabled("task.submit", "requires loom module (Phase B)")
```

The agent should not retry; the tool will remain unavailable until the daemon
is rebuilt with the dependency.

### `resource_exhausted`

A resource limit (connection pool, rate limit, quota, semaphore) has been
reached. Typically transient — the agent should back off and retry.

```go
// gRPC connection pool to engram-server is fully occupied.
return module.ErrResourceExhausted("grpc-connection-pool")
```

### `upstream_unavailable`

A required upstream service (engram-server, external API) is unreachable or
returned a non-retryable error. The `cause` field in `Details` contains the
underlying Go error string for log correlation.

```go
// gRPC dial to engram-server failed after all retries.
return module.ErrUpstreamUnavailable("engram-server", err)
```

The agent may retry after a delay; if the upstream remains unreachable the
daemon will log repeated failures.

### `timeout`

The tool's internal timeout (shorter than the dispatcher's 30-second hard cap)
was exceeded. `Details.wall_clock` records the deadline that was exceeded.

```go
// Internal 2-second budget for a cache-miss path exceeded.
return module.ErrTimeout(2 * time.Second)
```

This is the **module-voluntary** timeout. It is distinct from the dispatcher's
30-second hard cap, which produces a JSON-RPC `-32603` response. The agent
should retry with a lighter request or accept eventual consistency.

### `precondition_failed`

A required precondition was not met before the operation could proceed. The
`reason` string describes which precondition failed; `details` carries
optional structured context (e.g. expected vs actual state).

```go
// Session must be initialised (Initialize RPC) before any tool call.
return module.ErrPreconditionFailed(
    "session not initialised",
    map[string]any{"hint": "send Initialize request first"},
)
```

### `proxy_is_error` sentinel (`ProxyIsError`)

This is not a `ModuleError` code but a separate sentinel type for
`ProxyToolProvider` implementations that need to forward a backend-generated
`isError: true` response with byte-identical content (NFR-5).

```go
// engram-server returned isError:true; engramcore must forward it verbatim
// to preserve v4.2.0 byte-identity per NFR-5.
return &module.ProxyIsError{
    RawContent: json.RawMessage(`{"type":"text","text":"memory write failed: quota exceeded"}`),
}
```

The dispatcher emits `{"content":[<RawContent>],"isError":true}` for this type.
Non-proxy modules must not use `ProxyIsError`.

## Quick Reference

| Code | Transient? | Agent retry? |
|------|-----------|-------------|
| `not_ready` | Yes | Yes — wait `RetryAfter` |
| `project_not_found` | No | No — change project context |
| `tool_disabled` | No | No — rebuild binary or change config |
| `resource_exhausted` | Yes | Yes — back off |
| `upstream_unavailable` | Yes | Yes — back off |
| `timeout` | Yes | Yes — lighter request or retry |
| `precondition_failed` | No | Only after fixing precondition |

## Source References

- `internal/module/errors.go` — `ModuleError` struct + 7 constructors
- `internal/module/proxy_iserror.go` — `ProxyIsError` sentinel
- `internal/module/dispatcher/content.go` — dispatcher error-mapping boundary
