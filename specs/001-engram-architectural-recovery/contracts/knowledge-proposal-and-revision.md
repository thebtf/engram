# Knowledge Proposal and Revision Contract

## Purpose and Authority

This contract defines the authoritative reconciliation boundary for knowledge proposed from
accepted evidence. It implements FR-012 through FR-014. It does not authorize a second memory,
candidate, graph, summary, or ranking write path.

A proposal is not active knowledge. A proposal MUST receive exactly one persisted terminal
reconciliation decision before it can create, change, retain, suppress, or otherwise affect an
active belief. Reconciliation, belief state, revision history, evidence links, and exception
review are authoritative durable records; embeddings, graph records, summaries, clusters, and
caches are rebuildable projections only.

## Preconditions and Validation

A reconciliation request is valid only when all of the following are true:

- Its `proposal_id` identifies one bounded proposal derived from accepted Session Evidence.
- The proposal has one resolved canonical `project_key`; a path, remote, name, hash, slug, or
  legacy selector is not a substitute.
- The proposal carries its source evidence references, provenance, privacy scope, applicability,
  validity information, normalized proposition identity, extraction method/version, and content
  fingerprint.
- Every linked evidence record belongs to the same canonical project and has privacy and
  principal scope compatible with the proposed action.
- The request supplies an idempotency key that identifies the logical reconciliation attempt.
- A policy or rule proposal supplies an authority reference acceptable for its policy class.

The system MUST reject invalid input without creating an active belief or a partial revision. It
MUST return one actionable error disposition: `proposal_not_found`, `project_unresolved`,
`evidence_not_accepted`, `evidence_scope_mismatch`, `privacy_scope_denied`,
`applicability_invalid`, `validity_invalid`, `policy_authority_missing`,
`policy_authority_denied`, `unsupported_decision`, or `idempotency_conflict`.

A repeated valid request with the same idempotency key and semantically identical proposal MUST
return the original decision receipt. A key reused for different semantics MUST return
`idempotency_conflict`; it MUST NOT choose a new decision or overwrite the earlier receipt.

## Decision Record

For each proposal, the authoritative reconciliation record MUST persist exactly one terminal
`decision` from this closed set:

| Decision | Required durable result | Active-knowledge effect |
|---|---|---|
| `create` | Create a new active belief and its initial revision. | Adds one active belief. |
| `strengthen` | Append a support revision and supporting evidence links to the compatible active belief. | Retains the belief and may change its justified confidence only. |
| `revise` | Append a revision that records the prior and replacement representations, rationale, applicability, validity, and evidence links. | Changes the existing compatible belief without erasing history. |
| `supersede` | Append successor and predecessor revisions; preserve the predecessor as superseded with the causal relationship. | Activates the successor and removes only the incompatible predecessor from active selection. |
| `contradiction_pending` | Preserve the proposal, contradictory evidence, rationale, and an exception-review item. | No activation or confidence change. |
| `reject_noise` | Preserve the rejection reason, evaluator/workflow identity, and evidence references. | No belief mutation. |
| `reject_policy` | Preserve the governing policy reference and privacy or authority reason. | No belief mutation or exposure. |
| `no_op_duplicate` | Preserve the duplicate comparison result and original logical decision reference. | No semantic change. |

The decision record MUST include its immutable decision identifier, `proposal_id`, canonical
`project_key`, idempotency key, decision timestamp, reconciler identity, decision rationale,
normalized proposition comparison basis, privacy and principal result, applicability result,
validity result, and references to all resulting belief, revision, exception-review, and evidence
records. It MUST retain a redacted comparison fingerprint rather than raw private content where
that content is not authorized for the record.

A decision is terminal for that proposal. Later evidence MUST be represented as a new proposal
and a new decision; it MUST NOT amend a terminal decision in place. Administrative correction of
a malformed decision requires an append-only correction record that names the original decision,
authority, reason, and resulting revision or exception state.

## Belief and Revision Invariants

A belief represents one current reusable proposition for a canonical project, proposition
identity, compatible applicability, privacy/principal scope, and validity context. At most one
belief in that context MAY have `active` status. A `superseded`, `archived`, `expired`, or
suppressed belief remains addressable as historical knowledge and is excluded from normal
automatic retrieval.

Every knowledge-changing decision MUST append a Knowledge Revision. A revision MUST include:

- immutable revision and belief identities;
- the optional originating proposal and mandatory decision reference;
- operation, actor or workflow identity, timestamp, rationale, and conflict provenance;
- before and after representation references sufficient to reconstruct the change without
  destructive overwrite;
- the privacy, principal, applicability, validity, and confidence results effective at that
  revision; and
- links to all supporting, contradicting, or contextual evidence used by the decision.

`create`, `strengthen`, `revise`, and `supersede` decisions MUST produce revisions matching their
names. A decision that makes no belief mutation (`contradiction_pending`, `reject_noise`,
`reject_policy`, or `no_op_duplicate`) MUST retain its decision record and evidence links but
MUST NOT fabricate a belief revision merely to satisfy bookkeeping.

No confidence increase, applicability expansion, validity extension, privacy relaxation, or
active-status transition is valid without the corresponding revision and permitted evidence links.
Revision history is append-only. Restore, suppression, expiration, archive, or destruction is a
new revision operation subject to its governing retention and policy rules, never an update that
erases prior meaning.

## Evidence-Link Contract

A Knowledge Evidence Link binds one decision or revision to one accepted Session Evidence record.
It MUST persist the revision or decision reference, `evidence_id`, role, certainty or weight,
provenance reference, and creation timestamp. Its role is one of `supporting`, `contradicting`, or
`contextual`.

Evidence links MUST satisfy all of these rules:

- Evidence remains immutable and independently addressable; a link never copies raw credential or
  secret material into a proposal, belief, revision, fingerprint, or comparison record.
- A supporting link may justify a strength or confidence change only when its project, principal,
  and privacy scope is compatible with the belief's resulting scope.
- A contradicting link MUST remain visible with a `contradiction_pending` decision until an
  authorized later proposal resolves it; it MUST NOT silently reduce, replace, or activate a
  belief.
- Contextual evidence may explain applicability or provenance but MUST NOT independently justify
  a confidence increase.
- Evidence from another canonical project, a wider privacy scope, or an unauthorized principal
  MUST be rejected rather than linked or transformed into a less restrictive belief.
- Removing or retaining a belief does not delete its evidence, revisions, or links. Retention and
  destruction follow their explicit audited policies.

## Policy and Rule Knowledge

Policy and rule knowledge is a stricter knowledge class, not a second proposal store or routing
workflow. It MUST identify its policy class, governing authority, authority scope, authority
validation result, effective validity interval, and any exception authority in its decision and
revision history.

Only evidence and actors authorized for that policy class may create, revise, supersede, restore,
or relax a policy belief. Ordinary session evidence may be retained as supporting or contradicting
input, but it MUST NOT activate or relax policy knowledge by itself. Missing, expired, conflicting,
or insufficient authority produces `reject_policy` or `contradiction_pending`, as applicable.

Policy knowledge with unresolved authority, invalidity, or privacy restrictions MUST be excluded
from automatic retrieval. Universal policy that is independently valid for a session may be
delivered at session start under its own policy contract; it is not a substitute for task-aware
knowledge retrieval.

## Atomicity, Privacy, and Recovery Boundaries

The terminal decision, all belief/revision mutations, and required evidence links MUST become
visible atomically. Failure before that boundary MUST leave no active belief or orphaned revision.
After the boundary, retries return the stored receipt through the idempotency rule.

Reconciliation MUST preserve or narrow privacy and principal scope. Any widening requires explicit
authorization recorded in the resulting revision; absent that authority, the proposal MUST receive
`reject_policy`. Redacted provenance and content fingerprints MAY be stored only where authorized;
plaintext secrets and unauthorized private content MUST NOT be stored in decision records,
revisions, links, logs, exports, or projections.

A rollback or migration may restore a prior compatible read boundary only by recorded,
append-only compensating revisions and retained evidence links. It MUST NOT delete accepted
evidence, rewrite historical decisions, or silently reactivate a superseded belief. Rows whose
meaning, identity, provenance, or privacy cannot be preserved during migration MUST remain in the
quarantine ledger with an explicit decision requirement rather than being activated.