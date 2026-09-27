# Operator Workspace HTTP Contract — D-A

**Status**: D-A presentation contract. `internal/worker/handlers_operator_code.go` and its route table remain authoritative once implemented, and the D-A single integration owner is their sole writer after binding/catalog/read-model/retirement owners hand off declarations. These are authenticated browser endpoints, not HTTP-MCP aliases or direct storage APIs.

## Context, onboarding, and selected-View routes

The Console sends `X-Engram-Request-ID` on each request. Binding-bound requests carry the existing `tab_binding_id` and current `document_proof`. No route accepts a browser filesystem path, principal, raw grant reference, project label, raw service cursor, or operator-entered Source/Checkout/View identifier.

| Route / boundary | Request | Response and required Console posture |
|---|---|---|
| `POST /api/code/tabs/handshake`, `POST /api/code/tabs/resume` | Existing document nonce, binding/resume material | Binding transition material only. A new binding starts unselected; resume never establishes code authority by itself. |
| `POST /api/code/contexts` | Current binding proof | Returns only currently grant-authorized Workspace catalog choices: human-readable `repository`, `working_copy`, and optional `indexed_snapshot` plus opaque selection material, readiness summary, and first-index availability. The Console renders dependent selections and never renders raw IDs as task input. |
| Owner-scoped grant chooser and `POST /api/code/grants`, `POST /api/code/grants/{grant_ref}/revoke` | Current authenticated owner session; server-issued opaque chooser values; enabled target subject; optional bounded expiry | Lists only choices the exact Source owner may administer, then creates/restores or revokes one exact grant with audit. An administrator role, label, branch, path, or browser tab never substitutes for owner equality. |
| `PUT /api/code/tabs/{tab_binding_id}/context` | Current proof and one catalog-supplied opaque context choice | Pins exactly one authorized ContextRef to that binding. The Console never infers a context or changes another tab. A newer View is an explicit offered transition. |
| `POST /api/code/status` | Current proof | Returns released selected-View freshness, coverage, supported scope, and truthful Ready/Updating/Needs indexing/Failed/Newer snapshot availability. It does not expose a path, credential, or global readiness inferred from a page. |
| `POST /api/code/structure` | Current proof, normalized relative prefix, bounded limit, opaque continuation | Returns a bounded selected-View path/entity page, coverage/limitation state, and next opaque cursor. It does not perform an empty fake search, disk read, or global enumeration. |
| `POST /api/code/search` | Current proof, query, optional normalized path/language filters, bounded limit, opaque continuation | Returns one released selected-View query envelope. Page size, shown count, server total/lower bound, provider/degraded mode, and continuation remain distinct. A source-acceptance semantic claim requires the recorded real-provider non-lexical >50-candidate result; lexical or degraded output is `NOT_PROVEN`. A rejected continuation is non-disclosing and is never mixed with another View/query page. |
| `POST /api/code/graph` | Current proof, target/action, optional allowed budget/filter/continuation | Returns released direct/reverse relation data and navigation in one View. Each relation preserves its `evidence_refs` and limitation/provenance fields. A bounded visual is optional; the relation list is always semantically complete. |
| `POST /api/code/source` | Current proof and one released evidence/source descriptor | Reads only the persisted selected-View span. The Console calls it only with a released descriptor and labels its evidence granularity; it never reads disk or substitutes a destination definition for relation evidence. |
| Existing index-intent routes | Current proof and opaque intent/context values | Preserve the current daemon-owned request/status/retry boundary. Admission or acknowledgement is not a completed index; any resulting View remains an explicit context choice. |

## Release and failure rules

1. The server derives authenticated subject/session; binding proof, exact active grant, and ContextRef are revalidated before contextual UCI work and again at release.
2. UCI owns ContextRef validation, query/structure/relation/source semantics, response validation, reauthorization, and exposure. `internal/worker` validates DTOs and delegates; it does not query `ci_*`, reconstruct facts, invoke MCP, accept locators, or create AST facts.
3. Missing, stale, revoked, expired, ambiguous, continuation-mismatched, or release-failed authority reveals no contextual source body, identifier, count, edge, evidence, cursor, or exposure receipt.
4. `loading`, genuine `empty`, `denied`, `error`, `stale`, `partial`, `unsupported`, `timeout`, and `offline` remain distinct. A missing source/grant or no View is not rendered as a successful empty result.
5. A catalog refresh or completed index intent never auto-pins a newer View. Existing selected source, relation, and evidence remain coherent until the operator explicitly switches.

## D-A retirement boundary

Manual graph create/delete and plaintext book-intake routes are not represented by new aliases in this contract. In the existing single-container cutover, no old book-writer process may remain live before residual jobs transition idempotently to `failed` with their retirement reason; no lease/heartbeat is added. Their compatible status behavior follows the approved consumer map, but no response may continue old mutation semantics. Retained readers include `internal/retrieval/hybrid.go` Tier2 `Traverse`, historical graph reads, current `/api/context/search`, `internal/graph`, and the separate UCI code graph; their existing typed owners and ACLs remain.
