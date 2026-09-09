# Collection Selection, Mutation Truth, and Domain Operations Contract

## Shared Mutation Result

The browser shared seam returns exactly one `OperatorMutationResult` outcome:

| Outcome | Meaning | Required presentation/retry behavior |
|---|---|---|
| `committed_verified` | Server commitment is known and an authorized operation-specific postcondition was read successfully. | Present verified result and authoritative version/absence; no duplicate retry. |
| `committed_verification_pending` | Server confirmed commitment, but authorized postcondition readback failed or returned a load `error` state. | Preserve request reference and explain verification is pending; offer authorized readback/status, not rollback. |
| `partial` | Targeted items have mixed durable results. | Show every permitted item outcome and retry only items known unfinished. |
| `failed` | Commitment is definitively rejected/failed before a known effect. | Show typed reason; retry only when the action/validation makes it safe. |
| `outcome_unknown` | Transport/result loss leaves commitment unknown. | Preserve request reference; require a domain status query or authorized postcondition readback before replaying a non-idempotent action. |

A callback invocation is not verification. A local optimistic snapshot is presentation-only and may be refreshed/replaced, but no message or type calls that replacement a server rollback. All current direct `runOperatorMutation` callers migrate together in S1b: Access, Books, Documents, Domain Registry, Health Settings, Issues, Keycards, Memory Lab, Projects, Queue, Rules, and Secrets.

## Selection and Pagination

The collection UI holds one typed selection per domain:

```text
none
explicit { domain, ids, expected_versions? }
page { domain, cursor, visible_ids, expected_versions? }
frozen_filter { domain, selection_token, excluded_ids }
```

`page` is visibly named current-page selection and never silently becomes all results. `frozen_filter` is created by the server after authorizing the canonical filter/sort/context and freezing the permitted target set, count, expected versions, expiry, and query fingerprint. The opaque token grants no permission: action execution rechecks subject, domain permissions, state, and expected versions. Changing filter, context, grant, or relevant sort clears selection or requires confirmation before a destructive action.

List endpoints return a bounded page, an opaque continuation cursor when more data is available, and a total only when the server actually computed an authoritative scoped total. Client page-size preference is local UI state, not a server total or authorization input. Bounds are validated and rejected rather than silently expanded.

## Domain Action Matrices

| Domain | Allowed operations | Required postcondition / restriction |
|---|---|---|
| Rules — first consumer | Enable, disable, delete, supported content/priority changes, scoped atomic reorder | Mutated field and Version match authoritative row; delete reads as authorized absence. Reorder validates complete scope/versions and commits all rows or none. Scope change remains unsupported. |
| Issues | Acknowledge, status, priority, labels | Every selected issue rechecks version/action permission. Item result carries known commit/readback outcome; current `limit=100` and parallel PATCH are not accepted as final bulk semantics. |
| Memory | Suppress, unsuppress, permitted archival only | Existing privacy/visibility rules remain. No privacy widening or cognitive Memory R1 work. Access loss discloses no row, only safe operation state. |
| Queue candidates | Promote, reject, valid-target supersede | Valid target is checked at execution; Queue remains candidate workflow, not review-packet preview/apply. |
| Documents | Export or attach selected permitted versions | Version/permission are checked for each selected document version; document bodies are not returned merely to report operation status. |

Bulk secret reveal, arbitrary code/legacy graph edits, unsupported actions, Queue review-packet operations, lifecycle/ingestion controls, and generic cross-domain operations are forbidden.

## Rules Operation Wire Shape

Rules owns a domain endpoint family rather than relying on concurrent `PATCH /api/rules/{id}` requests. A selection operation includes an opaque request reference, action, typed selection, and expected versions. The server returns an `OperatorMutationResult` envelope with permitted per-item results and status/readback reference where applicable.

A scoped reorder includes one scope key and a complete ordered list of `{rule_id, expected_version}`. The Rules owner loads and validates every active member in that scope in one transaction, verifies the submitted set/order/version, applies all priorities/version bumps, and commits once. Any conflict, denial, missing row, or invalid membership aborts the transaction and leaves the scope unchanged. There is no partial fallback and no client-side compensating reorder.

## Status and Idempotency

New multi-item collection operations that need safe reconciliation persist a domain-owned `CollectionOperationStatus` keyed by the authenticated BrowserSubject, action, and request idempotency key. Exact same request binding returns the original result/status; changed binding under the same key is refused without exposing stale results. The status contains only safe operation metadata and permitted item outcomes. It is not a general workflow engine and does not replace domain audit records.

Legacy mutation endpoints that have not gained a status resource remain usable only through the shared honest result union. If a network loss makes commitment unknowable and no authorized postcondition can settle it, the caller remains `outcome_unknown`; the UI does not repeat it automatically.

## Required Acceptance

1. Seed more than 200 Rules and more than 100 Issues in disposable PostgreSQL.
2. Demonstrate explicit IDs, current page, frozen filter, exclusions, expiry, permission revocation, and version change after preview.
3. Demonstrate one Rules action mixed with denial/conflict and one Rules reorder whose injected conflict leaves the complete scope unchanged.
4. Demonstrate known commit followed by failed readback, response loss after commit, response loss before commitment, access loss after commit, and retry that excludes all known-success targets.
5. Exercise each allowed action matrix through its real handler/domain/readback path; mock UI interactions are recorded separately and do not establish persistence truth.
6. Verify keyboard selection, indeterminate header state, 1440/980/390 layouts, 200% zoom, RU/EN, and zh regression while result states remain distinguishable.
