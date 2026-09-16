# Daemon-Owned Index Intent Contract

## Boundary

A browser can request reindex/reconcile work for an authorized code context. It cannot read a workstation path, send a daemon credential, poll a workstation directly, stage source data, invoke parser execution, or publish a View. The browser creates a durable server-side `IndexIntent`; the daemon/workstation owner is the sole execution owner.

`IndexIntent` builds on existing UCI Source/Checkout/View and durable job/publication semantics. It is not a second index queue, graph service, or browser-to-daemon transport. The daemon uses its private authenticated UCI path and current lease/fencing rules to ACK/claim it and publish a new View.

## HTTP Presentation and Release Category

| Endpoint | Input | Semantics and release rule |
|---|---|---|
| `POST /api/code/index-intents` | Server-issued tab binding, exact authorized `ContextRef`, kind `reindex` or `reconcile`, opaque client request reference | Reauthorizes current browser grant/context and creates or returns an idempotent intent. Returns only opaque intent reference, `submitted|queued`, and retryability: ordinary authorization, no exposure. It never returns `completed` from request acceptance. |
| `GET /api/code/index-intents/{intent_ref}` | Server-issued tab binding | Reauthorizes before all status. Before a readable result exists, returns only safe state/attempt metadata without exposure. If it returns a resulting `ContextRef`/View, freshness, or coverage, it invokes the shared release port as `code_index_result`; recorder/release failure omits contextual fields and receipt. |
| `POST /api/code/index-intents/{intent_ref}/retry` | Server-issued tab binding, same intent/reference | Admits an authorized retry only from a retryable safe state. It returns an acknowledgement only, creates no duplicate execution, and does not change Source/Checkout context. |

The response never contains a host, path, common Git directory, daemon token, environment, source body, job lease, provider detail, or an un-released resulting View. Browser identity is `browser_subject` plus tab binding at the common release mapper; it is never converted to an MCP keycard.

## States

```text
submitted -> queued -> acknowledged -> running -> completed
submitted|queued -> unavailable -> queued
acknowledged|running -> failed
```

- `submitted`: browser request persisted and idempotency binding accepted.
- `queued`: eligible work exists but owner has not acknowledged it.
- `acknowledged`: the authorized daemon owner durably accepted/claimed the exact intent.
- `running`: daemon began UCI-owned work under its normal lease/fencing mechanism.
- `completed`: a new resulting View was published and authorized readback confirms it is readable. Only its released result state includes a resulting View reference.
- `unavailable`: daemon/owner cannot currently accept the request. It is visible, nonterminal, and may offer authorized retry.
- `failed`: daemon reported terminal failure through the durable contract; no completion is manufactured.

`HTTP 202`, `submitted`, `queued`, and `acknowledged` are distinct from `completed`. A vanished response after intent persistence reconciles by request reference/status endpoint, not a duplicate intent.

## Daemon Contract

The daemon receives an intent only through server-authorized private UCI composition. Before ACK/claim and before work/publish, it validates exact Source/Checkout context and workstation/owner eligibility. It may coalesce compatible intents only when every requester can later read the resulting View and each intent records its own terminal/readback state. A stale lease, changed checkout incarnation, revoked source authority, lost server connection, or untrusted local root cannot publish a View.

Existing UCI durable job, retry, fenced publication, watcher/reconcile, and old-View preservation rules remain authoritative. Failure, overflow, timeout, owner-offline, or partial scan preserves the old published View and reports its true state; it never publishes a silently truncated corpus.

## Acceptance and Rollback

- In an online two-worktree fixture, the browser submits one intent, observes daemon ACK, completion, and a shared-release-authorized new readable View only for the selected checkout.
- In an offline fixture, the browser observes `queued` or `unavailable`, no false completion/private locator, and an authorized retry path.
- Revoking grant/context before result release conceals the resulting View and receipt while retaining a non-sensitive operation state.
- Hard reload, tab-binding collision, and response loss reconcile by intent reference without duplicate daemon execution.
- Rollback stops new browser intent admission or reverts the adapter while preserving durable intents and UCI recovery/lease behavior. It never deletes worktrees, source data, or existing Views.
