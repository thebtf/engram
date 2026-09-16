# Task Retrieval and Exposure Contract

## Purpose and Authority

This contract defines the single automatic knowledge retrieval, packet rendering, and exposure
workflow required by FR-015 through FR-017. It operates only on authoritative knowledge beliefs
and revisions. A cache, vector index, graph, summary, reranker, session-start assembler, or
legacy recall surface MUST NOT become an independent selection or write authority.

Normal automatic context requires a concrete task, an explicit query, or a defined topic shift.
Session start MAY deliver only current state and independently valid universal policy; it MUST NOT
inject a recent-memory dump or execute normal knowledge retrieval before a qualifying retrieval
trigger exists.

## Retrieval Request Validation

A request MUST contain all of the following before candidate selection begins:

- a resolved canonical `project_key`;
- a supported session or task identity;
- a request source and one trigger type: `task`, `explicit_query`, or `defined_topic_shift`;
- a non-empty task or query representation, or a topic-shift record that names the prior and new
  topic boundaries;
- resolved principal and privacy context; and
- an idempotency key that identifies one logical delivery attempt.

The service MUST derive a redacted query fingerprint for correlation and idempotency. It MUST NOT
store raw secret material or unauthorized private task content in the fingerprint, packet,
exposure, logs, or projections.

A request missing a qualifying trigger MUST return `task_context_required`; empty text alone is
not a task, query, or topic shift. Other validation failures are `project_unresolved`,
`project_not_active`, `session_or_task_required`, `principal_scope_unresolved`,
`privacy_scope_denied`, `topic_shift_invalid`, `query_invalid`, and `idempotency_conflict`.
Rejected requests MUST create no delivery and no exposure. A duplicate valid request with the same
idempotency key and semantics MUST return the original delivery receipt and MUST NOT create a
second exposure. Reusing a key with different project, task/query fingerprint, privacy/principal
scope, render budget, or delivery target MUST return `idempotency_conflict`.

## Mandatory Candidate Filter Order

Before ranking, the workflow MUST apply these filters in the stated order:

1. **Canonical project**: retain only beliefs whose canonical `project_key` equals the resolved
   request project. Project paths, names, remote values, historical selectors, aliases, and
   projections do not authorize a cross-project result.
2. **Principal and privacy**: retain only beliefs, revisions, and evidence references permitted to
   the resolved principal and request privacy scope. The resulting packet MUST preserve or narrow
   the source privacy scope; it MUST never widen it.
3. **Lifecycle status**: retain only beliefs eligible for the requested retrieval mode. Normal
   automatic retrieval excludes `superseded`, `suppressed`, `archived`, and expired beliefs.
4. **Validity and applicability**: retain only beliefs valid at retrieval time and applicable to
   the declared task/query context. Historical retrieval, if explicitly supported, MUST identify
   itself as historical and MUST retain the same project, principal, and privacy restrictions.
5. **Policy authority**: retain policy or rule beliefs only when their recorded authority is valid
   for the request and their validity interval is current.

Filtering MUST complete before lexical, semantic, graph, recency, confidence, or reranking score
is considered. The system MUST retain a filter disposition summary in the exposure receipt without
recording unauthorized content.

## Ranking and Degraded Capability Rules

Only candidates that pass every mandatory filter may be ranked. Ranking MUST use the one declared
retrieval policy for the product path and MUST record the policy/version and per-item selection
rationale. Recency alone, global memory counts, provider availability, or a projection's contents
MUST NOT select content outside the filtered candidate set.

Embedding, vector, graph, and reranking providers are capabilities, not prerequisites for durable
knowledge or a substitute domain workflow. If a requested provider is unavailable, degraded,
paused, or failed, the workflow MUST:

- expose the capability health state and relevant error class without revealing secrets;
- use the declared deterministic lexical fallback over the already filtered authoritative
  candidates when that fallback is available;
- mark the delivery receipt and exposure as degraded, including the unavailable capability and
  fallback policy/version; and
- return an explicit empty result if the fallback finds no eligible result.

Provider loss MUST NOT select recent unrelated content, mutate authoritative beliefs, fabricate a
success claim, or prevent a valid empty-result receipt. If no deterministic fallback is available,
the request MUST return the explicit degraded-empty result with health information rather than an
unbounded or cross-scope packet.

## Render Budget Declaration

The bounded packet uses one versioned active retrieval policy declaration. Before AR-6 corpus or
cutover evidence can claim SC-010, that declaration MUST bind a stable `render_budget_id`, finite
positive `max_items`, finite positive `max_tokens`, owner (`AR-6 retrieval owner`), effective
release, eligible request scope, fallback behavior, source corpus/baseline reference, and
rollback/revision reference. The policy receipt records the exact active values used by a test or
dogfood observation.

No active declaration, non-positive limit, or scope-mismatched declaration yields
`not_computable` for SC-010 and blocks static-context cutover. Changing a budget requires a new
policy version, regression comparison at both values, and explicit rollback behavior; it MUST NOT
silently alter the interpretation of prior exposures or quality metrics.

## Bounded Render Packet

A successful delivery renders one identity- and rationale-bearing packet. The packet MUST state
its canonical project identity, retrieval trigger/source, retrieval-policy version, privacy scope,
render-budget identifier, degraded state if any, and whether the result is historical. For every
selected belief it MUST include the belief and current revision identities, a scope-safe rendered
representation, applicability/validity information, and selection rationale.

The packet MUST remain within the active declared maximum item count and token budget. Selection
and rendering MUST stop at that budget; an oversized candidate MUST be safely omitted or reduced
only through an authorized scope-safe representation. The packet MUST NOT reveal the existence,
content, score, or provenance of candidates removed by privacy, principal, or project filtering.

When no candidate survives filtering or ranking, the workflow MUST render an explicit empty result
that identifies the qualifying trigger, project identity, policy version, and any degraded state.
It MUST NOT replace an empty result with recent memories, a stale/superseded belief, unrelated
policy, or content from another project.

## Exposure and Delivery Receipt

Each logical rendered delivery, including an explicit empty result, MUST create exactly one
idempotent Retrieval Exposure receipt. The exposure is append-only and MUST record:

- immutable exposure identity, canonical project key, session/task identity, trigger source, and
  redacted query fingerprint;
- principal and privacy context used for filtering;
- retrieval and fallback policy versions, capability health/degraded state, and render-budget
  identifier;
- filter disposition summary, ranking and selection rationale, and the ordered selected
  belief/revision identities;
- the rendering timestamp, delivery target and confirmation receipt, and idempotency key; and
- `empty_result` when no belief was selected, with an empty selected-belief list.

The exposure records delivery, not usefulness, acceptance, or session success. Outcome evidence is
recorded separately and may reference exposure identities. A delivery transport failure before its
confirmation boundary MUST not be represented as a completed exposure; it MUST retain an
inspectable retryable or terminal delivery disposition without falsely claiming delivery. Once
delivery is confirmed, a retry of the same logical request returns the existing receipt.

## Compatibility, Privacy, and Recovery

Supported temporary legacy retrieval interfaces MAY delegate to this workflow during their declared
compatibility window, but they MUST supply a qualifying task/query/topic-shift trigger and receive
the same validation, filtering, packet, and exposure behavior. They MUST NOT preserve static
recent-memory injection, alternate ranking semantics, or an exposure bypass. Retirement behavior
must be actionable and versioned at the declared sunset boundary.

No packet, exposure, cache key, ranking trace, or projection may contain plaintext credentials,
raw secrets, or private content outside the request's authorization. Retention and destruction of
exposures follow explicit audited policy; deletion or expiration of a belief must not erase the
fact that it was delivered.

Migration or rollback MUST preserve each exposure's canonical project, selected belief/revision
identities, privacy context, trigger/query fingerprint, selection rationale, delivery receipt, and
idempotency semantics. If those facts cannot be preserved, the affected record MUST be quarantined
with its migration evidence and excluded from normal analytics rather than reassigned, widened, or
silently discarded.