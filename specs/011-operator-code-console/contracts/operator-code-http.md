# Operator Code HTTP Contract

**Status**: Integrated HTTP presentation contract. `internal/worker/handlers_operator_code.go` and registered routes are authoritative. These are authenticated browser endpoints, not HTTP-MCP aliases.

## Current Routes and DTOs

The Console sends `X-Engram-Request-ID` on every request. Contextual JSON routes carry `tab_binding_id` and `document_proof` in their body; the index-intent status `GET` carries the same two values in `X-Engram-Tab-Binding-ID` and `X-Engram-Document-Proof` headers. No route accepts a browser filesystem path, principal, grant reference, project label, or raw service cursor.

| Route | Request | Response and required Console posture |
|---|---|---|
| `POST /api/code/tabs/handshake` | `document_nonce`; optional copied binding/resume pair and `ambiguous` | Returns binding transition material only. A new binding starts without a pin or selected context. |
| `POST /api/code/tabs/resume` | `tab_binding_id`, `resume_nonce`, `reload_token`, `document_nonce` | Restores only the retained binding or returns `RELOAD_PENDING`; no context is exposed until a valid current proof is used. |
| `POST /api/code/contexts` | binding proof | Returns `contexts[]`, ordered server-side. Each entry contains safe `source{id,label}`, `checkout{id,label}`, optional `view{context_ref,label}`, and `index_intent_available`. The Console must present every published/superseded View as an explicit choice. A `view:null` entry supplies no `ContextRef`, source content, or local substitute. It is actionable only when the server reports both `index_intent_available:true` and `analysis_profile_id`: exactly one fresh live daemon target advertises that Source/Checkout. The profile is availability evidence only, never browser input. |
| `PUT /api/code/tabs/{tab_binding_id}/context` | `{document_proof, context_ref:{source_id,checkout_id,view_id,analysis_profile_id,generation}}` | Pins only that exact catalog `ContextRef` for this document. The Console must not infer a ContextRef or switch a different tab. |
| `POST /api/code/status` | binding proof | Returns released non-content status: `total_chunks`, `embedded_chunks`, `embedding`, and optional `freshness`. The embedded `uci.EmbeddingStatus` currently serializes its fields as `EmbeddingProfileID`, `Coverage`, `TotalCandidates`, `ReadyCandidates`, `PendingJobs`, `JobState`, `ErrorCode`, and `RetryAfter`; it does not identify the pin. |
| `POST /api/code/search` | binding proof, `query`, optional `path_prefix`, `languages`, `limit`, and opaque `continuation` | Returns one released query envelope. Search `continuation` is a server-owned, one-use, expiring opaque cursor bound to subject, grant epoch, tab binding, exact View, normalized query/filter, and page size. The Console offers the next page only when this response contains a cursor and reuses the unchanged query/filter/View request. A cursor refusal is non-disclosing: the UI must not mix old and new result pages and can truthfully name possible expiry, consumption, or mismatch rather than guessing which occurred. |
| `POST /api/code/graph` | binding proof, graph target/action, optional filters/budgets, optional opaque continuation | Returns a released graph envelope plus `navigation`. Each navigation node contains its exact entity/context projection, `source_state`, and optional `source_read{entity_key,span,content_digest}`. Graph continuation remains the opaque UCI continuation supplied with the unchanged graph request. |
| `POST /api/code/source` | binding proof plus a released descriptor's `entity_key`, exact `span`, and `content_digest` | Reads only the persisted selected-View span. The Console calls it only when a result or graph-navigation node supplied its descriptor; it never reads disk or manufactures a graph-node source body. |
| `POST /api/code/index-intents`, `GET /api/code/index-intents/{intent_ref}`, `POST /api/code/index-intents/{intent_ref}/retry` | binding proof, opaque request/intent reference, kind where applicable | A pinned-View submit omits `target`. A first-index submit carries `target:{source_id,checkout_id}` and no profile or other daemon selector; the server derives and reauthorizes its advertised target. Status and retry use the opaque intent plus the same binding proof. Responses present `intent_ref`, `state`, `attempt`, `retryable`, `created_at`, and `updated_at`; only completed status may add `result{view_ref,generation}`. Admission/status never selects or pins that newer View. |

## Binding, Selection, and Failure Rules

Every route derives the authenticated browser subject and session server-side. Except for binding transition endpoints, no Code route can proceed without a live binding plus current document proof. Contextual handlers reauthorize the pin and exact active Source/Checkout grant before invoking UCI; refusals do not disclose contextual body, source/check-out identifiers, counts, edges, cursors, or exposure data.

- Catalog labels and opaque `ContextRef`s are browser presentation data, not authority. Only a catalog View can be submitted to the pin route.
- Catalog discovery never pins a default. A retained explicit choice may be restored only after the resumed binding and current catalog both prove the same `ContextRef`; otherwise the document remains unselected.
- A completed index intent may identify a newer View result, but does not replace the current pin or client selection.
- Completed intent status refreshes the catalog but never selects or pins its returned View; a newly available View remains an explicit operator choice.
- Search-page requests must preserve query, normalized filters, page size, and pinned View. The server consumes a successful cursor advance atomically; a rejected advance leaves no usable next page response.
- Graph navigation is bounded to the released graph context. `source_state:"unavailable"` means no source-read control; the Console does not fall back to a matching search item, a checkout path, or local disk.
- `loading`, `empty`, `denied`, `unavailable`, `offline`, `timeout`, `partial`, and `stale` remain distinct. Empty is a complete authorized result with no match, never a substitute for denial, cursor refusal, or a capped/unavailable graph.

## Release Boundary

`internal/worker` validates DTOs and delegates through the binding, grant, catalog/continuation, composed UCI, and release ports. It does not query `ci_*` tables directly, reconstruct graph facts, accept an absolute locator, create AST facts, or invoke an MCP tool. UCI owns exact `ContextRef` validation/authorization, query/graph/source semantics, response validation, reauthorization, and release/exposure rules.

The contextual envelope is the serialized `engram.code-query/1` UCI response: `schema`, `status`, `contexts`, `freshness`, `retrieval`, `coverage`, `exposure`, `error`, `items`, `graph`, `truncated`, `warnings`, and `continuation` under their UCI omission/null rules. Graph HTTP adds only the route-owned `navigation` projection described above. HTTP does not add source bytes outside a released exact-read item, a raw UCI cursor, a path locator, a query log, or credentials.

## Explicit UI Non-Contracts

The Console does not expose local worktree/disk actions, raw UCI stores, View publication, source registration, daemon credentials, a full graph download, a browser-supplied grant/principal/profile/daemon selector, a source body for an unavailable graph node, or automatic selection after catalog refresh/index completion. A `view:null` catalog item is truthfully unavailable for source inspection; it presents an Index action only with both server-declared availability and profile evidence.
