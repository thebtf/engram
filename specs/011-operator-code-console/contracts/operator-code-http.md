# Operator Code HTTP Contract

**Status**: Feature011 implementation contract. The listed routes are planned normal authenticated HTTP presentation endpoints; they do not exist in the current candidate and are not HTTP-MCP aliases.

## Boundary and Caller Mapping

`internal/worker` validates DTOs and invokes one composed UCI application. It never queries `ci_*` tables directly, reconstructs a graph, accepts a filesystem path, creates AST facts, or calls an MCP tool. UCI owns `ContextRef` validation/authorization, query/graph/source semantics, response validation, reauthorization, and initial exposure recording.

The release port receives a typed caller rather than a fabricated browser keycard:

| Caller kind | Stable caller reference | Session/reference binding | Compatibility rule |
|---|---|---|
| `mcp_keycard` | Existing validated MCP keycard identity | Existing admitted MCP transport session and context handle | Preserves the current MCP release/idempotency vectors. |
| `browser_subject` | Canonical realm-qualified BrowserSubject | Authenticated browser session, server-issued `tab_binding_id`, and request reference | Never populates or pretends to own `ClientKeycard`; it is a distinct caller kind. |

The release owner evolves `uci.ExposureInput` behind this typed mapper so persisted opaque hashes and canonical idempotency bind caller kind, caller reference, session/binding, request reference, operation, and exact View. The MCP mapper retains the current hash/vector compatibility contract; the browser mapper uses the browser subject and tab binding. Neither raw caller identity nor request body is persisted. A browser request cannot borrow an MCP keycard hash, and an MCP request cannot select a browser tab binding.

Every contextual Code route requires a real authenticated browser session, a server-issued `X-Engram-Tab-Binding-ID`, and an active exact browser read grant for the selected Source and Checkout. A malformed, absent, foreign, expired, revoked, collision-rotated, or mismatched binding/context has the same non-disclosing failure posture: no source body, source/check-out identifier, count, edge, cursor, or exposure receipt.

## Common Release Categories

The common release operation reauthorizes the exact `ContextRef` against the current grant and UCI epoch, validates a pre-exposure response, appends non-content exposure when required, validates the released response, and only then returns a serializable envelope. A release/recorder failure returns UCI’s closed unavailable envelope with `exposure: null` and no contextual data. Permission/context refusal records no exposure.

| Category | Routes | Authorization and exposure rule |
|---|---|---|
| Binding bootstrap | `POST /api/code/tab-bindings/handshake` | Authenticated browser session plus nonce/session proof; no Code context/grant lookup and no exposure. Collision returns only a replacement binding, never the prior context. |
| Ordinary authorized metadata | `GET /api/code/contexts`; `PUT /api/code/tabs/{tab_binding_id}/context`; `POST /api/code/grants`; `POST /api/code/grants/{grant_ref}/revoke` | Exact grant or Source-owner authorization is checked before safe metadata/operation acknowledgement. These responses contain no code fact, coverage/status, result View, or source relation and do not append exposure. |
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
| `GET /api/code/contexts` | Binding ID; optional bounded cursor | Returns only grant-authorized Source/Checkout/View safe labels and opaque `ContextRef`s; no owner, workstation, absolute locator, or ungranted count. |
| `PUT /api/code/tabs/{tab_binding_id}/context` | Exact `ContextRef` selector | Validates context tuple, active grant, and binding; pins one context to that binding only and never changes MCP/another tab. |
| `GET /api/code/status` | Binding ID; optional exact context selector | Returns released status/coverage/freshness for the bound authorized View. |
| `POST /api/code/search` | Binding ID, bounded query, requested retrieval mode, bounded limit, optional continuation | Resolves bound/exact context, calls UCI search, and returns a released envelope. A semantic request reports UCI’s actual semantic/hybrid/degraded outcome; lexical FTS is never named semantic. |
| `POST /api/code/graph` | Binding ID, bounded graph operation/target, direction/relation filters, UCI budgets, optional continuation | Returns released bounded relations or visible cap/partial/unsupported/no-path qualification. Edges are UCI code facts, not legacy memory graph records. |
| `POST /api/code/source` | Binding ID, UCI entity reference or relative path plus bounded source span | Calls UCI versioned read only. Source bytes are the exact persisted artifact/span from the selected View; there is no working-copy/disk fallback. |

Index intent endpoints are specified in [index-intents.md](index-intents.md); grant and binding routes are specified in [browser-context-and-grants.md](browser-context-and-grants.md).

## Validation, Compatibility, and Failure Vectors

- Limits and graph budgets use UCI maxima; invalid, negative, or oversized values are rejected rather than silently clamped.
- Multiple possible contexts without explicit authorized selection return `CONTEXT_REQUIRED`; a label/path/Space/legacy project is not a selection.
- `loading`, `empty`, `denied`, `error`, `stale`, `partial`, `unsupported`, `timeout`, and `offline` remain distinct. `empty` means a complete authorized query has no match; it never replaces denied, incomplete, capped, or unavailable.
- Grant revocation between application result and release serializes no contextual body. A new View leaves the pinned View stable and may only announce a newer candidate.
- MCP remains a first-class release consumer. Parity/failure vectors cover MCP and browser caller attribution, authorized search/graph/source/status/index-result, ordinary discovery/binding/admission, recorder failure, context mismatch, grant revocation after application work, invalid response, idempotency mismatch, bounded partial graph, continuation mismatch, and browser tab collision. Equivalent contextual inputs yield the same UCI content/redaction/exposure semantics; transport framing and caller kind are the only permitted differences.

## Explicit Non-Contracts

This surface does not expose an MCP JSON-RPC endpoint, raw UCI stores, arbitrary server paths, View publication, source registration, daemon credentials, legacy graph mutation, manual AST editing, full graph download, a browser request for a local workstation filesystem action, broad browser IAM, or an administrator-role implied grant.
