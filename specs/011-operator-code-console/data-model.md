# Feature 011 Data Model

**Status**: Phase 1 planning model. It identifies ownership and invariants; implementation re-reads the current migration registry and selects additive migration numbers then. Existing Feature010 Source, Checkout, View, UCI exposure, and daemon job entities remain normative for their fields and lifecycle.

## Authority Model

- PostgreSQL is authoritative for explicit browser grants, frozen all-filter selection tokens, collection operation status where the domain needs durable inquiry, and index intents.
- UCI remains authoritative for Source, Checkout, immutable View, query/graph/source facts, exposure evidence, and view publication.
- A browser tab binding and browser `sessionStorage` key are convenience state. Neither is an ACL, a grant, a View authority, nor a cross-tab/MCP shared default.
- Domain entities retain their existing owners: Rules, Issues, Memory, Queue candidates, and Documents do not become rows in a universal collection table.

## Entities

| Entity | Identity / fields | Owner and relationships | Invariants |
|---|---|---|---|
| BrowserSubject | Canonical nonempty subject reference derived from real persisted user identity; `auth_realm`; subject kind `human` | Auth/grant owner; attached to one authenticated browser session | Never derived from role, Source label, Space, checkout path, legacy project, tab key, master key, or disabled-auth identity. |
| BrowserReadGrant | Opaque grant id; `auth_realm`; `browser_subject`; `source_id`; `checkout_id`; state `active|revoked|expired`; issued/revoked metadata | Auth/grant owner; references existing UCI Source and Checkout | One active exact tuple is sufficient but not broader than its tuple. It permits code read only; no daemon/index ownership, source registration, view publish, or machine metadata. |
| BrowserTabContext | Opaque server binding id; browser session id; opaque tab key; exact `ContextRef`; binding expiry; selected time | Browser context owner; references active BrowserSubject and UCI ContextRef | A tab key is unique only inside one authenticated session. Context must be reauthorized on every use. New View does not overwrite a pinned binding. |
| ContextRef | Existing UCI `source_id`, `checkout_id`, `view_id`, `analysis_profile_id`, `generation`; safe display metadata only after authorization | UCI owner | Search, graph, source, cursor continuation, and resulting intent always use the same exact View. |
| CodeContinuation | Opaque cursor; subject/session/tab binding digest; ContextRef; normalized query/graph shape digest; expiry | HTTP/UCI release owners | Mismatch, expiry, changed session, revoked grant, or changed context returns non-disclosing refusal and reveals no stale IDs/counts/body. |
| OperatorMutationResult | Client discriminated outcome `committed_verified|committed_verification_pending|partial|failed|outcome_unknown`; request reference; per-item results; safe next action | Shared mutation owner; maps domain response/readback | A local snapshot is presentation state, never rollback evidence. Verified state requires the named authorized postcondition. Unknown commitment is never retried blindly. |
| CollectionSelection | `none`, `explicit`, `page`, or `frozen_filter`; domain; typed IDs or cursor page; optional exclusions | Selection owner; consumed by one domain action | Page and all-filter are visibly distinct. Selection itself does not authorize the action. |
| SelectionToken | Opaque token; BrowserSubject; domain; canonical filter/sort/context fingerprint; frozen authorized target IDs/revisions; count; exclusions; issued/expiry | Selection owner; PostgreSQL authoritative | Token has bounded lifetime, cannot add targets, and is invalidated/reconfirmed after relevant context/filter/permission change. Action rechecks current grant and expected version. |
| CollectionOperationStatus | Opaque operation reference; BrowserSubject; domain/action; request idempotency key; item outcome list; state; readback status; timestamps | Domain owner for new status-capable collection actions | Does not contain secret bodies or unauthorized rows. Exact idempotency replay returns same operation; changed request binding is refused. Legacy actions without an inquiry endpoint may stay outcome-unknown. |
| RuleOperationItem | Rule ID, expected version, observed version, desired operation/field, outcome | Rules owner | Every row belongs to one selected scope. An edit/enable/delete verifies rule version; reorder validates every target before mutating scope. |
| CursorPage | Opaque cursor; domain/filter/sort fingerprint; permitted page boundary; page-size preference handled client-side | Selection/domain owner | Server validates bounds rather than silently expanding/clamping. Page count is not a total unless server explicitly returns authoritative total. |
| IndexIntent | Opaque intent id; caller BrowserSubject; exact ContextRef; kind `reindex|reconcile`; idempotency key; state; daemon ACK/claim metadata; safe error; resulting View reference | Daemon-intent owner | Browser submission is not execution. Only daemon owner transitions through ACK/claim and only published/readable resulting View completes it. No path, secret, or source body is persisted in intent status. |
| MechanismAdaptationRecord | Mechanism name; user task; pinned reference commit/URL/files/tests/issues; observed behavior; limitation; native adaptation; acceptance evidence; scope claim | R-C research/QA handoff owner | Required before a parity or new language/relation claim. It is evidence about reference behavior, not imported runtime authority. |

## State Transitions

### Browser grant and tab context

```text
BrowserSubject(real session)
  -> BrowserReadGrant(active)
  -> BrowserTabContext(selected pinned ContextRef)
  -> authorized Code request
  -> release reauthorization

BrowserReadGrant(active) -> revoked|expired -> denied (no context metadata/body/count)
BrowserTabContext(selected) -> newer View observed -> selected + explicit transition available
BrowserTabContext(selected) -> session expiry -> discarded -> explicit authorized selection required
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

`committed_verification_pending` and `outcome_unknown` retain the request reference. Only the first may be resolved by an authorized readback proving the postcondition; the latter needs a status/readback before any replay. `partial` preserves each item outcome rather than synthesizing rollback.

### Frozen selection and operation

```text
none -> explicit|page -> frozen_filter (server freezes permitted membership)
frozen_filter -> action preview -> per-item result|atomic Rules reorder result
frozen_filter -> expiry|filter/context/permission change -> cleared or reconfirmed
```

### Index intent

```text
submitted -> queued -> acknowledged -> running -> completed(resulting readable View)
submitted|queued -> unavailable -> queued (authorized retry with same intent/reference)
acknowledged|running -> failed
```

A completed state requires owner acknowledgement and authorized resulting-View readback. An HTTP response or queued row alone cannot enter `completed`.

## Keys, Constraints, and Privacy

- Browser grant lookup has an index on realm, subject, Source, Checkout, and active state; revoked/expired grants are not read authority.
- Selection/continuation/operation/intents use opaque random references. Request body, source text, absolute checkout locator, credentials, raw query text, and unauthorized item identifiers are excluded from their durable status fields.
- All code-related durable records reference UCI IDs, never a raw project string or path.
- Rules expected-version checks use a conditional update/transaction. Any version mismatch makes the item conflict; a reorder conflict leaves its entire declared scope unchanged.
- Existing UCI exposure records remain UCI-owned non-content evidence. Browser grants, selection tokens, and operation statuses neither create nor replace exposure evidence.

## Retention and Rollback

- Grant revocation is immediate at the authorization boundary; retained tab keys/cursors/tokens cannot bypass it.
- Selection tokens, cursors, and incomplete operation statuses have bounded retention/expiry selected by implementation from current retention conventions; expiry is visible and safe, not silently extended.
- Index intents retain enough safe status to support retry/audit and point only to authorized resulting Views.
- Feature rollback disables the browser path or reverts additive handlers. It does not delete UCI views, legacy graph data, grants required by another accepted surface, or in-flight daemon work.
