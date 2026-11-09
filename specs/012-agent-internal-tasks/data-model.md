# Data model: existing issue task slice

**State**: Additive model follows accepted #513 comments 1730/1739. Main message 39669b21-48b8-43c1-9544-c0cd0a7c94be/comment 1742 accepted P1 and authorized the lane to choose T001/T002; Engram session 01a18a3b made the bounded model/representation choices from cited owners. This provenance correction changes no schema/data/code/tests/build/commit. Lane-owned verified candidate commits and Main-owned integration/default/product acceptance/release remain distinct.

## Data architecture boundary

| Field | Design |
| --- | --- |
| Data owners | Existing IssueStore owns issues, issue_comments and new dependent issue-create receipts/issue-change history. Existing project/auth owners resolve identity and permission. No receipt/history record is an independent task or grant. |
| Invariants | Same issue/comment IDs and lifecycle; authenticated-actor + canonical-source + key uniqueness/isolation; one monotonic revision; atomic winner/history/comment; append-only comments/history; existing creator/operator/collaborator distinction; ordinary visibility plus the narrow retry privacy policy. |
| Migration shape | Expand, quiesced baseline/backfill, writer cutover, conservation proof. No contraction/drop or broad historical reconstruction. |
| Engine constraints | The repository uses PostgreSQL, GORM and gormigrate. Existing `issue_store.go` uses transactions/row locks for selection, JSONB labels and DB-generated issue IDs. Exact lock/index mechanics must be established by future isolated PostgreSQL proof, not assumed from this design. |

## Issue (existing `issues`)

Keep current fields unchanged: id, title, body, status, priority, type, source_project, target_project, source_agent, created_by_session, creator_keycard_id, labels and lifecycle/created/updated timestamps.

Add:

- `metadata`: non-null object default {}; compact JSON ≤1 MiB, ≤256 nested containers, admitted finite binary64-preservable decimal/safe-integer domain from contracts/issues.md. Unknown admitted keys/values survive unchanged; no control/auth interpretation. Unsupported raw numbers/duplicates and recognized disallowed secret classes are rejected before mutation, never silently redacted. This new issue guard is not already present or comprehensive.
- `revision`: positive monotonic 64-bit integer, starting at 1. Encode as decimal string on the wire so JavaScript cannot round it. Never use updated_at as the new revision.
- `owner_label`, `recipient_label`: optional non-authoritative strings, absent stored/read as null. They are labels, not users, ownership grants or subscriptions. Exact filters are allowed; metadata-wide search is not.

A material change is a change to title/body/type/priority/labels/metadata/owner_label/recipient_label or an authorized lifecycle transition and its related timestamp. Source/target/creator fields are not newly editable. A successful multi-field edit advances revision once. A normalized no-op returns the unchanged revision and adds no material history. Comment count is a derived value; a comment-only append does not advance revision.

## IssueComment (existing `issue_comments`)

Retain id, issue_id, author_project, author_agent, body and created_at. Add object metadata defaulting to `{}`, and a server-authenticated actor reference distinct from submitted author attribution. New comments are append-only; no comment editing/deletion feature is added. A standalone comment append does not promise retry deduplication; an unknown result requires read/reconciliation, not automatic blind retry. A comment inside a failed CAS transaction is not appended.

Use these standalone comments for ordinary progress and outcomes rather than replacing the statement. Concurrent permitted appends need no task revision when the caller does not supply one; both successful records survive and a later authorized reader can page them alongside the unchanged statement. This does not weaken explicit field-edit conflicts or make an unknown comment result safe to replay.

Existing full-comment readers cut over explicitly: comments/history default 50/max 200 each; 4 MiB serialized-page target, continuation before overflow and intact oversized singleton where actual transport permits. Legacy unpaged complete comments require ≤200/4 MiB, otherwise DETAIL_PAGING_REQUIRED without partial success. Actual native 16 MiB failure is safe/explicit and retains authorized REST access. These targets are not HTTP response caps, and no historical bytes are trimmed/lost.

## Dependent issue-create receipt (proposed `issue_create_receipts`)

- Unique `(actor_identity_key, canonical_source_project_id, request_key)`. Actor key is source-qualified client KeycardID, validated persisted browser user ID, or distinct singleton master/HMAC-session/auth-disabled synthetic operator; never a claimed source_agent/principal label or fabricated human. Native source is locally resolved; existing SessionStore.CanonicalProjectExists checks active canonical existence/admission in actor context, not ownership/authenticity. No ACL/account/per-agent key. Request key is opaque valid UTF-8, nonempty, ≤512 bytes, no NUL.
- `issue_id`, canonical logical payload version 1, server-normalized original payload SHA-256 binding and created_at. Store binding only in the issue receipt, not raw payload snapshots or exported digests. Existing auth/read visibility precedes lookup/replay/comparison; private equality remains solely same-result retry. No secret/vault admission, no logs/review/telemetry/embeddings/similarity/merge export. Original binding never changes when the issue is edited; different actors stay isolated. Representation is decided, with transaction/constraint proof still future.
- Receipt/issue/initial revision/history publish in one DB transaction. Concurrent equal requests converge on the winner; an insert conflict triggers authorized comparison, not another issue insert.
- Compare original payload, not current edited card, only within the exact actor+canonical-source key namespace after current authorization/visibility. Same key/different actor never returns or discloses the other result; an authorized actor may independently create in its own namespace under existing create authority. Source attribution does not grant read/write access.
- Do not expire/reuse keys automatically. Preserve a minimal policy-authorized tombstone for an explicitly deleted issue so a retry cannot resurrect it or allocate another ID. It is not readable as a deleted body. Any eventual retention change needs its owning conservation/privacy policy and cannot silently reuse a live key.

## Dependent issue change (proposed `issue_changes`)

One record per committed material revision, unique (issue_id,revision), with previous revision, changed field names, timestamp, authenticated actor and separate source/agent/session attribution. Field-change/revision/actor evidence fulfills the material-history contract without inventing raw private snapshots. Any before/after values must have their applicable issue-domain privacy authority; the narrow retry policy alone does not authorize additional protected history retention. Never copy private values/digests into operational audit or lower-trust evidence; returned actor references are non-secret.

New create records revision 1 with action `created`. An old-row baseline records revision 1 with action `baseline`, unknown actor, baseline time and available existing provenance; previous values/history are unknown, not fabricated. Existing comments retain their IDs and author labels; new actor information for old comments remains unknown. Rejection/resolution with an outcome comment uses one issue change plus the one comment in the same transaction. No-op and CAS loser create neither change nor compound comment.

## Lifecycle and transaction rules

Retain open, acknowledged, resolved, reopened, closed and rejected. Resolved awaits verification; an authorized read-write participant may close after checks or reopen from resolved or closed. Target projects, recipients and reported sessions grant no authority. Operator-only actions remain operator-only; protected field edits retain creator/operator authority.

For material mutation: validate request; resolve verified context and authenticate; establish read/action authority; lock current issue; recheck authority and any supplied expected revision; compute actual changes; apply fields/state/timestamps, one revision/history and optional comment; commit; return authorized readback. Field edits and other non-exempt material changes require a caller revision. Standalone resolve/reopen/close may omit it and use the locked current-state transition; this cannot prevent a delayed request from applying after an intervening state cycle. Persistence failure or stale supplied revision rolls back all effects of that request, without undoing an already committed competing winner.

Bulk acknowledgment/selection retains its existing all-or-nothing operation semantics. Carry and compare each selected row's revision under its existing selection binding. A server-internal conditional open-to-acknowledged operation may obtain its expected state/revision as part of its own locked transaction and must record its known system actor. This does not authorize refreshing a stale UI base and overwriting edits. Existing delete checks retain their authority and preconditions and preserve receipt non-resurrection; deleting dependent history must not be added as an incidental cleanup.

## Additive migration and rollback

1. Re-read the migration registry and current writers; allocate a new migration ID without renumbering applied entries. Record pre-migration issue IDs, comment IDs/issue references, content/state/known-provenance values and counts on an isolated authorized fixture.
2. Pause incompatible writers. Add object metadata defaults, revision baseline, optional labels and actor references, receipt/history storage and required uniqueness/validation. Backfill old rows idempotently; no new issue/comment ID, no invented request key, no claim of historical authorship. Migration must not log/export private bodies.
3. Verify unchanged ID sets, comments/order/relationships, bodies, lifecycle timestamps and known attribution; check one baseline per issue and metadata defaults. Resume only revision/history-compatible writers. Never publish a trusted revision baseline while an old server can still mutate it.
4. Before any feature write, reverting application changes is possible only with writers held and explicit conservation of the expanded state. After any new metadata/history/receipt write, retain schema and values; roll forward or use a compatible earlier application, not a destructive down migration or pre-feature unconditional writer. Backup restoration that discards later comments is not a safe rollback of this slice.
