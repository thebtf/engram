# Operator Code HTTP Contract

**Status**: Feature011 implementation contract. The listed routes are planned normal authenticated HTTP presentation endpoints; they do not exist in the current candidate and are not HTTP-MCP aliases.

## Boundary

`internal/worker` validates DTOs and invokes the one composed UCI application. It never queries `ci_*` tables directly, reconstructs a graph, accepts a filesystem path, creates AST facts, or calls an MCP tool. UCI owns ContextRef validation/authorization, query/graph/source semantics, response validation, reauthorization, and initial exposure recording.

Every code route requires a real authenticated browser session, an opaque `X-Engram-Tab-Key`, and an active exact browser read grant for the selected Source and Checkout. The response contains safe display metadata only after authorization. A malformed, absent, foreign, expired, revoked, or mismatched context has the same non-disclosing failure posture: no source body, source/check-out identifier, count, edge, cursor, or exposure receipt.

## Common Envelope

The transport representation mirrors the UCI closed query response after shared release. It preserves UCI `status`, `context`, `freshness`, `retrieval`, `coverage`, `warnings`, `items`, `graph`, `source`, `error`, and opaque `exposure` semantics. HTTP may add request correlation metadata but cannot add unredacted internal error, locator, source text outside UCI source excerpt, query log, or credential fields.

Before serialization, the HTTP owner calls the common release operation with:

1. the UCI application pre-exposure response;
2. the requested exact ContextRef/tab binding;
3. the UCI exposure operation (`code search`, `code graph`, or `code read`); and
4. the response-to-context matcher.

The common release operation reauthorizes the exact ContextRef against the current grant and current UCI epoch, validates the pre-exposure response, appends non-content exposure, validates the released response, and only then returns a serializable envelope. A release/recorder failure returns UCI's closed unavailable envelope with `exposure: null` and no contextual data. Permission/context refusal records no exposure.

## Endpoints

| Endpoint | Input | Output and contract |
|---|---|---|
| `GET /api/code/contexts` | Tab key; optional bounded cursor | Authorizes before list. Returns only grant-authorized Source/Checkout/View safe labels and opaque ContextRefs; no owner, workstation, absolute locator, or ungranted count. |
| `PUT /api/code/tabs/{tab_key}/context` | Exact ContextRef selector | Validates context tuple and active grant, binds one pinned ContextRef to this authenticated session/tab, then returns safe metadata. It never changes an MCP default or another tab. |
| `GET /api/code/status` | Tab key, optional exact context selector | Returns safe UCI status/coverage/freshness for the bound authorized ContextRef. It is not a generic health endpoint and does not claim semantic readiness from a chunk count. |
| `POST /api/code/search` | Tab key, bounded query, requested retrieval mode, bounded limit, optional continuation | Resolves bound/exact authorized context; calls UCI search; returns a released UCI envelope. A semantic request produces UCI's actual semantic/hybrid/degraded outcome and never labels lexical FTS as semantic. |
| `POST /api/code/graph` | Tab key, bounded graph operation/target, direction/relation filters, UCI budgets, optional continuation | Calls UCI bounded graph exploration and returns released relations or a visible cap/partial/unsupported/no-path qualification. Graph edges are code-derived UCI facts, not legacy memory graph records. |
| `POST /api/code/source` | Tab key, UCI entity reference or relative path plus bounded source span | Calls UCI versioned read only. Response source bytes are the exact persisted artifact/span from the same pinned View; there is no working-copy/disk fallback. |
| `GET /api/code/continuations/{cursor}` | Tab key | Resolves only a cursor bound to the same browser subject/session/tab, ContextRef, query shape, ACL epoch, and expiry. Any mismatch is non-disclosing. |

Index intent endpoints are specified separately in [index-intents.md](index-intents.md).

## Validation and Failure Mapping

- Limits and graph budgets use UCI maxima; invalid/negative/oversized values are rejected rather than silently clamped.
- Context selection is explicit. Multiple possible contexts without an authorized selection returns `CONTEXT_REQUIRED`; a label/path/Space/legacy project is not a selection.
- `loading`, `empty`, `denied`, `error`, `stale`, `partial`, `unsupported`, `timeout`, and `offline` map to distinct browser presentation states with an available action or limitation. `empty` means a complete authorized query had no matching result; it never replaces denied, incomplete, capped, or unavailable.
- On grant revocation between application result and release, reauthorization fails and HTTP releases no contextual body. On a new View publication, the bound pinned View remains stable and the response may announce a newer candidate without changing selection.
- HTTP cancellation/deadline does not fabricate a completion, exposure, index result, or mutation result.

## Compatibility and Test Vectors

MCP remains a first-class consumer of the shared release operation. The UCI release owner maintains equivalent pre-exposure vectors for MCP and HTTP: authorized success, authorized unavailable with recorder healthy, recorder failure, context mismatch, grant revocation after query, invalid response, idempotency mismatch, bounded partial graph, and versioned source read. Equivalent input must yield the same UCI content/redaction/exposure semantics; transport framing is the only permitted difference.

## Explicit Non-Contracts

This surface does not expose an MCP JSON-RPC endpoint, raw UCI stores, arbitrary server paths, View publication, source registration, grant issuance UI, daemon credentials, legacy graph mutation, manual AST editing, full graph download, or a browser request for a local workstation filesystem action.
