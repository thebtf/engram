# Operator Code HTTP Contract

**Status**: Feature011 implementation contract. The listed routes are planned normal authenticated HTTP presentation endpoints; they do not exist in the current candidate and are not HTTP-MCP aliases.

## 2026-09-11 delivery amendment — current candidate gaps

The intended API below remains the acceptance contract. The current candidate implementation is narrower, so the Console Code runtime must use only the real calls it can make and render the following limitations rather than fabricate selection, continuation, or source data:

| Required operator outcome | Current implementation | Console posture until serialized backend delivery |
|---|---|---|
| Authorized Source → Checkout → View catalog and exact selection | `POST /api/code/contexts` returns exactly one server-resolved `{context:{source,checkout,view}}`; `PUT /api/code/tabs/{tab_binding_id}/context` accepts only `document_proof` and pins that server-resolved context. It returns no catalog, opaque `ContextRef`, or selector. | Display only the one safe server label set; pin it explicitly. Do not create a local worktree/View picker or claim A/B worktree selection. |
| Search continuation | `operatorCodeSearchRequest` accepts only `query` and `limit`; no continuation route is registered. | Preserve the shown query and View, announce that the returned cursor cannot be continued, and expose no control that implies otherwise. |
| Graph continuation and filters | `POST /api/code/graph` accepts `direction`, relation filters, bounded budgets, and an inline opaque continuation. | Re-submit only the original graph target and filters with the opaque server cursor; each call remains binding/grant/UCI authorized. The browser treats the cursor as opaque. |
| Graph-node source inspection | `POST /api/code/source` requires a released search item's entity key, span, and content digest. Graph edges contain no source descriptor. | Let an operator inspect exact source only when the selected graph node matches a released search item; otherwise say that no published source descriptor was released. |
| New View after index request | Intent endpoints persist/read safe state, but the current slice has no daemon execution consumer proving a newly published readable View. | Render acknowledgement and state only. Never switch the pin, expose a path, or call completion a new selectable View. |

The actual implementation also uses JSON binding/proof DTOs and `POST` for contextual reads while the planned table names header/`GET` shapes. The browser must follow the actual handler contract until the backend cutover ships; this does not relax the required same-View, grant, and release semantics.

### Current handler seam (delivery candidate)

The Console consumes the following registered shapes until the planned contract above is delivered. This is the one typed response-to-screen adaptation seam: `useOperatorCode.ts` validates these JSON DTOs before rendering; it neither guesses planned fields nor constructs a local substitute.

| Route | Current request shape | Current response and UI constraint |
|---|---|---|
| `POST /api/code/tabs/handshake` | `document_nonce`, optional copied binding/resume nonce, optional `ambiguous` | Returns only binding transition material. No context is selected by bootstrap. |
| `POST /api/code/tabs/resume` | binding ID, resume nonce, reload token, document nonce | Resumes only the retained binding or returns `RELOAD_PENDING`; contextual data stays concealed until a current proof is established. |
| `POST /api/code/contexts` | `tab_binding_id`, `document_proof` | Returns one safe `{context:{source,checkout,view}}` or no safe labels. |
| `PUT /api/code/tabs/{tab_binding_id}/context` | `{document_proof}` | Pins only that server-resolved context; returns `204` on confirmation. |
| `POST /api/code/status` | `tab_binding_id`, `document_proof` | Returns released coverage/freshness metadata for the pin. |
| `POST /api/code/search` | binding proof, `query`, optional bounded `limit` | Returns a released query envelope. A returned search cursor has no current continuation route and is announced as unavailable. |
| `POST /api/code/graph` | binding proof, target, direction, supported relations, declared bounds, optional opaque `continuation` | Returns only server-supplied nodes and edges; the same target and filters may be resubmitted with its opaque cursor. |
| `POST /api/code/source` | binding proof, released item `entity_key`, exact span, `content_digest` | Returns one exact persisted source item or a closed non-content state. No graph-only node receives invented source text. |
| `POST`/`GET` `/api/code/index-intents…` | binding proof, opaque request/intent references and kind as applicable | Presents durable admission/status only. It never switches the pin or exposes a resulting View reference/path. |

## Boundary and Caller Mapping

`internal/worker` validates DTOs and invokes one composed UCI application. It never queries `ci_*` tables directly, reconstructs a graph, accepts a filesystem path, creates AST facts, or calls an MCP tool. UCI owns `ContextRef` validation/authorization, query/graph/source semantics, response validation, reauthorization, and initial exposure recording.

The release port receives a typed caller rather than a fabricated browser keycard:

| Caller kind | Stable caller reference | Session/reference binding | Compatibility rule |
|---|---|---|
| `mcp_keycard` | Existing validated MCP keycard identity | Existing admitted MCP transport session and context handle | Preserves the current MCP release/idempotency vectors. |
| `browser_subject` | Canonical realm-qualified BrowserSubject | Authenticated browser session, server-issued `tab_binding_id`, current document proof, and request reference | Never populates or pretends to own `ClientKeycard`; the proof identifies a document only and is a distinct caller kind from MCP. |

The release owner evolves `uci.ExposureInput` behind this typed mapper so persisted opaque hashes and canonical idempotency bind caller kind, caller reference, session/binding/document-proof digest, request reference, operation, and exact View. The MCP mapper retains the current hash/vector compatibility contract; the browser mapper uses the browser subject, binding, and current document proof. Neither raw caller identity nor request body is persisted. A browser request cannot borrow an MCP keycard hash, and an MCP request cannot select a browser tab binding.

Every contextual Code route requires a real authenticated browser session, server-issued `X-Engram-Tab-Binding-ID`, matching current `X-Engram-Document-Proof`, and an active exact browser read grant for the selected Source and Checkout. A malformed, absent, foreign, expired, revoked, collision-rotated, replayed, or mismatched binding/proof/context has the same non-disclosing failure posture: no source body, source/check-out identifier, count, edge, cursor, or exposure receipt.
Except for the first `handshake` or `resume` transition, every `/api/code` route—including safe context metadata, pin selection, and grant administration—and every lease operation carries both binding ID and current document proof. `handshake` carries a fresh document nonce; `resume` carries fresh nonce, binding ID, resume nonce, and the server-owned one-time reload cookie. The Console Code bootstrap selects `resume` only after one-time opener normalization/readback reports no opener and `PerformanceNavigationTiming.type === "reload"`; a normalized no-opener `navigate` copied pair uses handshake, while opener failure/persistence or ambiguous metadata clears to fresh handshake. The cookie, opener, and bootstrap signal do not substitute for exact grant or UCI ContextRef authorization.

## Common Release Categories

The common release operation reauthorizes the exact `ContextRef` against the current grant and UCI epoch, validates a pre-exposure response, appends non-content exposure when required, validates the released response, and only then returns a serializable envelope. A release/recorder failure returns UCI’s closed unavailable envelope with `exposure: null` and no contextual data. Permission/context refusal records no exposure.

| Category | Routes | Authorization and exposure rule |
|---|---|---|
| Binding transition | `POST /api/code/tab-bindings/handshake`; `POST /api/code/tab-bindings/resume`; `POST /api/code/tabs/{tab_binding_id}/lease/renew`; `POST /api/code/tabs/{tab_binding_id}/lease/close` | Handshake uses authenticated session plus fresh document nonce and either no pair or a copied pair; resume uses the exact server-owned one-time token material only after the Console Code normalized no-opener `reload` classifier branch; lease calls require current binding ID/proof. A normalized `navigate` duplicate uses handshake and returns only `TAB_BINDING_COLLISION`; delayed reload returns only `RELOAD_PENDING`; opener-normalization failure/persistence or ambiguous bootstrap returns only `TAB_BOOTSTRAP_AMBIGUOUS`; none returns prior context. |
| Ordinary authorized metadata | `GET /api/code/contexts`; `PUT /api/code/tabs/{tab_binding_id}/context`; `POST /api/code/grants`; `POST /api/code/grants/{grant_ref}/revoke` | Current binding/document proof plus exact grant or Source-owner authorization is checked before safe metadata/operation acknowledgement. These responses contain no code fact, coverage/status, result View, or source relation and do not append exposure. |
| Contextual status metadata | `GET /api/code/status` | Exact binding/grant/context authorization plus common release and `code_status` exposure before View/freshness/coverage metadata serializes. It is not a generic health endpoint and cannot call a chunk count semantic readiness. |
| Contextual query/fact response | `POST /api/code/search`, `POST /api/code/graph`, `POST /api/code/source` | Common release with respectively `code_search`, `code_graph`, or `versioned_read`; it returns only the released UCI envelope. |
| Continuation | `GET /api/code/continuations/{cursor}` | Cursor validation is ordinary non-disclosing authorization. It then re-executes the underlying search/graph category and records that operation’s exposure; a cursor never serializes cached contextual payload itself. |
| Intent admission/retry acknowledgement | `POST /api/code/index-intents`, `POST /api/code/index-intents/{intent_ref}/retry` | Exact current grant/context check; returns only opaque intent reference, safe state, and retryability. No exposure because it returns no contextual code fact or View. |
| Intent result metadata | `GET /api/code/index-intents/{intent_ref}` | Reauthorizes before all status. A state without a readable resulting View returns only safe operation metadata with no exposure. If the response includes resulting `ContextRef`/View, freshness, or coverage, it uses common release and `code_index_result` exposure; release failure omits those fields and receipt. |

`code_status` and `code_index_result` are explicit planned exposure operations in addition to the current `code_search`, `code_graph`, and `versioned_read` vocabulary. They cannot be mislabeled as search merely to reuse a recorder branch.

## Endpoints and Envelope

The contextual envelope mirrors the released UCI response: `status`, `context`, `freshness`, `retrieval`, `coverage`, `warnings`, `items`, `graph`, `source`, `error`, and opaque `exposure`. HTTP may add request correlation metadata but cannot add an unredacted internal error, locator, source text outside the UCI source excerpt, query log, or credential.

| Endpoint | Input | Output contract |
|---|---|---|
| `GET /api/code/contexts` | Binding ID + current document proof; optional bounded cursor | Returns only grant-authorized Source/Checkout/View safe labels and opaque `ContextRef`s; no owner, workstation, absolute locator, or ungranted count. |
| `PUT /api/code/tabs/{tab_binding_id}/context` | Binding ID + current document proof; exact `ContextRef` selector | Validates context tuple, active grant, and binding; pins one context to that binding only and never changes MCP/another tab. |
| `GET /api/code/status` | Binding ID + current document proof; optional exact context selector | Returns released status/coverage/freshness for the bound authorized View. |
| `POST /api/code/search` | Binding ID + current document proof; bounded query, requested retrieval mode, bounded limit, optional continuation | Resolves bound/exact context, calls UCI search, and returns a released envelope. A semantic request reports UCI’s actual semantic/hybrid/degraded outcome; lexical FTS is never named semantic. |
| `POST /api/code/graph` | Binding ID + current document proof; bounded graph operation/target, direction/relation filters, UCI budgets, optional continuation | Returns released bounded relations or visible cap/partial/unsupported/no-path qualification. Edges are UCI code facts, not legacy memory graph records. |
| `POST /api/code/source` | Binding ID + current document proof; UCI entity reference or relative path plus bounded source span | Calls UCI versioned read only. Source bytes are the exact persisted artifact/span from the selected View; there is no working-copy/disk fallback. |

Index intent endpoints are specified in [index-intents.md](index-intents.md); grant and binding routes are specified in [browser-context-and-grants.md](browser-context-and-grants.md).

## Validation, Compatibility, and Failure Vectors

- Limits and graph budgets use UCI maxima; invalid, negative, or oversized values are rejected rather than silently clamped.
- Multiple possible contexts without explicit authorized selection return `CONTEXT_REQUIRED`; a label/path/Space/legacy project is not a selection.
- `loading`, `empty`, `denied`, `error`, `stale`, `partial`, `unsupported`, `timeout`, and `offline` remain distinct. `empty` means a complete authorized query has no match; it never replaces denied, incomplete, capped, or unavailable.
- Grant revocation between application result and release serializes no contextual body. A new View leaves the pinned View stable and may only announce a newer candidate.
- MCP remains a first-class release consumer. Parity/failure vectors cover MCP and browser caller attribution, binding/document-proof validation, fresh opener, copied-tab collision, acknowledged and delayed/crash reload, token/proof replay, document versus binding/session expiry, authorized search/graph/source/status/index-result, ordinary discovery/binding/admission, recorder failure, context mismatch, grant revocation after application work, invalid response, idempotency mismatch, bounded partial graph, and continuation mismatch. Equivalent contextual inputs yield the same UCI content/redaction/exposure semantics; transport framing and caller kind are the only permitted differences.

## Explicit Non-Contracts

This surface does not expose an MCP JSON-RPC endpoint, raw UCI stores, arbitrary server paths, View publication, source registration, daemon credentials, legacy graph mutation, manual AST editing, full graph download, a browser request for a local workstation filesystem action, broad browser IAM, or an administrator-role implied grant.
