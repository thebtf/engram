# Daemon-Owned Index Intent Contract

## Boundary

A browser can request that an authorized code context be reindexed or reconciled. It cannot read a workstation path, send a daemon credential, poll a workstation directly, stage source data, invoke parser execution, or publish a View. The browser creates a durable server-side `IndexIntent`; the daemon/workstation owner is the only execution owner.

`IndexIntent` builds on existing UCI Source/Checkout/View and durable job/publication semantics. It is not a second index queue, graph service, or browser-to-daemon transport. The daemon uses its private authenticated UCI path and current lease/fencing rules to ACK/claim it and to publish a new View.

## HTTP Presentation

| Endpoint | Input | Semantics |
|---|---|---|
| `POST /api/code/index-intents` | Tab key, exact authorized ContextRef, kind `reindex` or `reconcile`, opaque client request reference | Reauthorizes current browser read grant and validates context. Creates or returns an idempotent intent. Returns `submitted`/`queued`, never `completed` solely from request acceptance. |
| `GET /api/code/index-intents/{intent_ref}` | Tab key | Returns safe status only to the submitting/authorized BrowserSubject. Reauthorizes context/grant before exposing any status/resulting View. |
| `POST /api/code/index-intents/{intent_ref}/retry` | Tab key, same intent/reference | Admits an authorized retry only from a retryable safe state. It does not create a duplicate execution or change the source/checkout context. |

The response contains opaque intent/request references, high-level state, retryability, bounded timestamp/attempt metadata, safe closed error code, and resulting ContextRef/View metadata only after it is readable to the current browser subject. It does not contain a host, path, common git directory, daemon token, environment, source body, job lease, or provider detail.

## States

```text
submitted -> queued -> acknowledged -> running -> completed
submitted|queued -> unavailable -> queued
acknowledged|running -> failed
```

- `submitted`: browser request persisted and idempotency binding accepted.
- `queued`: eligible work exists but owner has not acknowledged it.
- `acknowledged`: the authorized daemon owner has durably accepted/claimed the exact intent.
- `running`: daemon has begun UCI-owned work under its normal lease/fencing mechanism.
- `completed`: a new resulting View was published and an authorized readback confirms it is readable. This state includes the resulting View reference.
- `unavailable`: daemon/owner cannot currently accept the request. It is visible, nonterminal, and may offer authorized retry.
- `failed`: daemon reported terminal failure through the durable contract; no completion is manufactured.

`HTTP 202`, `submitted`, `queued`, and `acknowledged` are all distinct from `completed`. A vanished response after intent persistence is reconciled by the same request reference/status endpoint, not by a duplicate intent.

## Daemon Contract

The daemon receives an intent only through server-authorized private UCI composition. Before ACK/claim and before work/publish, it validates the exact Source/Checkout context and its workstation/owner eligibility. It may coalesce compatible intents only when each requester can later read the resulting View and each intent records its own terminal/readback state. A stale lease, changed checkout incarnation, revoked source authority, lost server connection, or untrusted local root cannot publish a View.

Existing UCI durable job, retry, fenced publication, watcher/reconcile, and old-View preservation rules remain authoritative. A failure, overflow, timeout, owner offline condition, or partial scan preserves the old published View and reports the correct state; it does not publish a silently truncated corpus.

## Acceptance and Rollback

- In an online two-worktree fixture, the browser submits one intent, observes daemon ACK, completion, and an authorized new readable View for only the selected checkout.
- In an offline fixture, the browser observes `queued` or `unavailable`, no false completion, no private locator, and an authorized retry path.
- Revoking grant/context before readback conceals the resulting View and returns a non-sensitive operation state only.
- A hard reload and response loss reconcile by intent reference without duplicate daemon execution.
- Rollback stops new browser intent admission or reverts the adapter while preserving already durable intents and UCI's existing recovery/lease behavior. It never deletes worktrees, source data, or existing Views.
