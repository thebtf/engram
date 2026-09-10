# Feature 011 Data Model

**Status**: Phase 1 planning model. It identifies ownership and invariants; implementation rereads the current migration registry and selects additive migration numbers then. Existing Feature010 Source, Checkout, View, UCI exposure, and daemon job entities remain normative for their fields and lifecycle.

## Authority Model

- PostgreSQL is authoritative for explicit browser grants, server-issued tab bindings, frozen all-filter selection tokens, collection operation status where a domain needs durable inquiry, and index intents.
- UCI remains authoritative for Source, Checkout, immutable View, query/graph/source facts, exposure evidence, and View publication.
- Browser `sessionStorage` retains only the tab resume pair; the current document proof remains in document memory and the one-time reload token is server-issued in an HttpOnly cookie. None is an ACL, a grant, a View authority, or a cross-tab/MCP default. The server-issued `tab_binding_id` is the only browser selection key.
- Domain entities retain their existing owners: Rules, Issues, Memory, Queue candidates, and Documents do not become rows in a universal collection table.

## Entities

| Entity | Identity / fields | Owner and relationships | Invariants |
|---|---|---|---|
| BrowserSubject | Canonical nonempty real-user reference; `auth_realm`; kind `human` | Auth/grant owner; attached to one authenticated browser session | Derived from an enabled persisted user, not role, Source label, Space, path, branch, legacy project, master key, keycard, disabled-auth identity, or tab data. Its source-owner principal must equal `ci_checkouts.owner_principal` to issue/revoke grants. |
| BrowserReadGrant | Opaque grant ID; realm; BrowserSubject; Source; Checkout; `active|revoked|expired`; issuer principal; expiry; timestamps | `CodeGrantApplication`; references existing UCI Source and Checkout | Exact tuple only. Grant issue/revoke and `code_grant_issued|code_grant_revoked` audit append commit together; audit failure aborts transition. It grants code read, never index ownership/source registration/View publication. |
| BrowserTabBinding | Server-issued opaque `tab_binding_id`; browser session reference; resume-nonce, current document-proof, and reload-token digests; document lease state/expiry; exact optional pinned `ContextRef`; binding expiry | Browser Binding Application owner; references BrowserSubject and UCI ContextRef | The normative browser lifecycle is the single transition table in `browser-context-and-grants.md`. Console Code classifies bootstrap from `window.opener` plus `PerformanceNavigationTiming.type`; only supported no-opener `reload` may resume, while `navigate` with a copied pair collides and every ambiguous classifier result is fresh/unselected. Every binding-bound Code request and lease operation presents the current binding ID plus document proof. Document lease expiry retains the binding/pin; only binding or session expiry destroys them. |
| ContextRef | Existing UCI `source_id`, `checkout_id`, `view_id`, `analysis_profile_id`, `generation`; safe display metadata only after authorization | UCI owner | Search, graph, source, cursor continuation, and released intent result use the same exact View. |
| CodeContinuation | Opaque cursor; subject/session/`tab_binding_id` and document-proof digests; ContextRef; normalized query/graph shape digest; ACL epoch; expiry | HTTP/UCI release owners | Mismatch, expiry, changed session/binding/document proof, revoked grant, or changed context returns non-disclosing refusal and reveals no stale IDs/counts/body. |
| CodeReleaseCaller | Non-persisted typed mapper: `mcp_keycard` plus existing MCP identity/session, or `browser_subject` plus BrowserSubject/session/tab binding/current document proof | UCI release owner | Browser caller is never a synthetic keycard. The document proof identifies one live document only; it does not replace separate grant/ContextRef authorization. Opaque exposure hashes and idempotency bind kind, caller, session/binding, request, operation, and exact View. |
| OperatorMutationResult | Client discriminated outcome `committed_verified|committed_verification_pending|partial|failed|outcome_unknown`; request reference; per-item results; safe next action | Shared mutation owner; maps domain response/readback | A local snapshot is presentation state, never rollback evidence. Verified state requires the named authorized postcondition. Unknown commitment is never retried blindly. |
| CollectionSelection | `none`, `explicit`, `page`, or `frozen_filter`; domain; typed IDs or cursor page; optional exclusions | Selection owner; consumed by one domain action | Page and all-filter are visibly distinct. Selection itself does not authorize the action. |
| SelectionToken | Opaque token; BrowserSubject; domain; canonical filter/sort/context fingerprint; frozen authorized target IDs/revisions; count; exclusions; issued/expiry | Selection owner; PostgreSQL authoritative | Token has bounded lifetime, cannot add targets, and is invalidated/reconfirmed after relevant context/filter/permission change. Action rechecks current grant and expected version. |
| CollectionOperationStatus | Opaque operation reference; BrowserSubject; domain/action; request idempotency key; item outcome list; state; readback status; timestamps | Domain owner for new status-capable collection actions | Does not contain secret bodies or unauthorized rows. Exact idempotency replay returns same operation; changed request binding is refused. Legacy actions without an inquiry endpoint may stay outcome-unknown. |
| RuleOperationItem | Rule ID, expected version, observed version, desired operation/field, outcome | Rules owner | Every row belongs to one selected scope. An edit/enable/delete verifies rule version; reorder validates every target before mutating scope. |
| CursorPage | Opaque cursor; domain/filter/sort fingerprint; permitted page boundary; page-size preference handled client-side | Selection/domain owner | Server validates bounds rather than silently expanding/clamping. Page count is not a total unless server explicitly returns authoritative total. |
| IndexIntent | Opaque intent ID; caller BrowserSubject; exact ContextRef; kind `reindex|reconcile`; idempotency key; state; daemon ACK/claim metadata; safe error; resulting View reference | Daemon-intent owner | Browser submission is not execution. Only daemon owner transitions ACK/claim and only published/readable resulting View completes it. Before shared release, status contains no resulting contextual metadata. |
| MechanismAdaptationRecord | Mechanism; task; pinned reference commit/URL/license/files/tests/issues; observed/inferred label; limitation; native adaptation; acceptance evidence; scope claim | R-C research/QA handoff owner | Current-slice matrix is fixed planning evidence. New parity/language/relation claims require a fresh record; no record imports runtime authority or benchmarks. |

## State Transitions

### Browser grant and tab binding

```text
real BrowserSubject + exact Source owner
  -> BrowserReadGrant(active, audited)
  -> Browser Binding Application transition
  -> optional pinned ContextRef
  -> binding ID + current document proof on each request
  -> release reauthorization

supported no-opener `navigate` + copied pair -> TAB_BINDING_COLLISION -> fresh binding without ContextRef
ambiguous opener/navigation bootstrap -> TAB_BOOTSTRAP_AMBIGUOUS -> fresh binding without ContextRef
document lease expiry -> binding and pinned ContextRef retained -> valid resume may replace only document proof
BrowserTabBinding session|binding expiry -> binding/pinned ContextRef destroyed -> explicit authorized selection required
BrowserReadGrant(active) -> revoked|expired -> denied (no context metadata/body/count)
BrowserTabBinding(selected) -> newer View observed -> selected + explicit transition available
```

### Mutation truth

```text
request submitted
  -> server commitment known + authorized postcondition passes -> committed_verified
  -> server commitment known + readback fails -> committed_verification_pending
  -> mixed target outcomes -> partial
  -> commitment definitively rejected/failed before effect -> failed
  -> transport loss or no trustworthy commitment result -> outcome_unknown
```

`committed_verification_pending` and `outcome_unknown` retain the request reference. Only the first may resolve by authorized readback proving the postcondition; the latter needs status/readback before replay. `partial` preserves each item outcome rather than synthesizing rollback.

### Frozen selection, operation, and index intent

```text
none -> explicit|page -> frozen_filter (server freezes permitted membership)
frozen_filter -> action preview -> per-item result|atomic Rules reorder result
frozen_filter -> expiry|filter/context/permission change -> cleared or reconfirmed

submitted -> queued -> acknowledged -> running -> completed(resulting readable View)
submitted|queued -> unavailable -> queued (authorized retry with same intent/reference)
acknowledged|running -> failed
```

A completed intent requires owner acknowledgement and authorized resulting-View readback. HTTP acceptance or queued row alone cannot enter `completed`.

## Keys, Constraints, Privacy, and Rollback

- Browser grant lookup indexes realm, subject, Source, Checkout, and active state; revoked/expired grants are not read authority. The grant store validates issuer equality against the current `ci_checkouts.owner_principal` in the same transaction as audit.
- Tab binding lookup indexes browser session plus binding ID and retains only nonce/proof/token digests, lease state, binding expiry, and optional pinned context. The normative transition table prevents a collision or stale proof from overwriting an existing binding/context.
- Selection/continuation/operation/intent values are opaque. Request body, source text, absolute checkout locator, credentials, raw query text, and unauthorized item IDs are excluded from durable status fields.
- Existing UCI exposure records remain UCI-owned non-content evidence. Browser grants, bindings, selection tokens, and operation statuses neither create nor replace exposure evidence; browser release uses the typed caller mapper.
- Rules expected-version checks use conditional update/transaction. Any mismatch makes the item conflict; reorder conflict leaves its entire declared scope unchanged.
- Grant revocation is immediate at authorization/release. Retained resume pairs, replayed tokens, stale document proofs, cursors, selection tokens, and stale bindings cannot bypass it. Feature rollback disables browser handlers or reverses additive handlers, but never deletes UCI Views, legacy graph data, grants needed by another accepted surface, or in-flight daemon work.
