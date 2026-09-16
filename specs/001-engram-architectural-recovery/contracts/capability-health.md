# Capability Health Contract

**Scope**: FR-019 and FR-020. This contract defines observable operational capability state. Capability health reports configuration and operating conditions; it MUST NOT select an alternative domain architecture, become a second authority for evidence or knowledge, or hide required product behavior behind an architecture-era flag.

## Capability Record

Each named external provider, operational dependency, projection, adapter, or emergency control that can affect a defined workflow MUST have a durable `CapabilityHealth` record scoped to the capability and its declared measurement scope. A record MUST contain:

- stable capability identity, owner, consumer workflow, and measurement scope;
- current health state from the closed set in this contract;
- configuration identity/version and non-secret configuration validation result;
- last probe time and result, last successful use, last state transition, and state reason;
- redacted error class, summary, and first/most-recent failure timestamps when applicable;
- declared safe behavior, including queue impact, fallback eligibility, and whether work may remain pending;
- paused-by authority, pause reason, and resume condition when paused; and
- observation provenance, retention policy, and an auditable history of state changes.

A capability record MUST not contain secrets, raw credentials, private payloads, or unredacted provider responses. A health probe or successful use is evidence about that named capability and scope, not evidence that session evidence was accepted, knowledge was correct, or a user outcome succeeded.

## State Semantics

The current state MUST be exactly one of the following:

| State | Required meaning | Required operational posture |
|---|---|---|
| `configured` | Required configuration is present, syntactically valid, authorized for its declared scope, and not yet proven currently usable by a successful probe or use. | The capability may be probed or used according to policy. It MUST NOT be reported as available merely because configuration exists. |
| `available` | Configuration is valid and a current, scope-matching successful probe or use has established the capability is usable within its declared service conditions. | The normal workflow may use the capability. Availability expiry or a qualifying failure must cause a visible reassessment. |
| `degraded` | The capability is configured and only partly usable, outside a declared service condition, rate-limited, or unavailable for a nonessential enhancement while a named same-domain safe behavior remains available. | The product MUST expose the limitation and use only its declared safe behavior. Durable evidence acceptance and pending-work discovery continue. |
| `paused` | An authorized, intentional, time-bounded operational stop is active. | No work requiring the capability may be claimed or dispatched through it. Existing durable work remains visible with queue impact. A pause needs authority, reason, safe default, expiry/removal condition, and resume evidence. |
| `failed` | Configuration is missing or invalid for a required capability, or current evidence establishes no safe usable capability behavior within its declared scope. | The failure and impact MUST be visible. The product must retain durable work and follow its job retry, terminal-failure, or quarantine contract rather than silently selecting another domain workflow. |

A configuration error, absent required configuration, unauthorized credential reference, or failed configuration validation MUST be `failed`, with a non-secret error class. An optional capability intentionally not enabled for a declared deployment scope MUST be represented by a capability record in `paused` with explicit authority and scope; it MUST NOT disappear from health reporting.

State is determined by declared configuration validation, current probe/use evidence, approved pause information, and the capability's service conditions. A stale successful probe cannot indefinitely preserve `available`. A recovery observation transitions `failed` or `degraded` to `configured` or `available` only after the required validation/probe evidence is recorded. Transitions MUST be append-only auditable facts; a new state must not erase the prior failure.

## Domain and Queue Boundaries

Capability health separates operational variability from domain truth:

- Evidence intake MUST NOT require an extraction, embedding, reranking, outcome, or projection provider to be `available` before it durably acknowledges valid evidence.
- A degraded or failed extraction provider leaves the causal processing job visible and subject to its durable retry policy. It does not erase evidence or pretend completion.
- A degraded retrieval enhancement may use a declared deterministic same-domain fallback only when that fallback preserves canonical-project, principal, privacy, status, validity, bounded-packet, rationale, and exposure requirements. It MUST NOT route to stale static context or a competing architecture.
- A paused or failed outcome adapter leaves the durable outcome gap visible; it cannot manufacture success, partial, failure, or utility.
- A projection capability is rebuildable and cannot become a write authority because its health is available. Its absence must be visible and cannot block evidence acceptance or project identity resolution.
- Health state does not grant authority to bypass privacy, authorization, identity, reconciliation, or retention rules.

Architecture-era maturity, milestone, VNext, candidate, lifecycle, legacy-injection, or competing-workflow controls MUST NOT be expressed as capability health. A temporary operational control is valid only when it has a named owner, protected risk, safe default, metrics, expiry or removal release/date, both-branch evidence, and tracked deletion boundary. It may not survive two completed recovery releases without constitutional amendment.

## Probes, Error Handling, and Idempotency

A probe MUST be scoped, bounded, non-destructive, and privacy-safe. The record must state what was measured; a probe in one project, principal, region, provider model, or deployment scope MUST NOT be generalized beyond its declared scope. Successful real use may update health only when its scope and result are recorded at least as precisely as a probe.

Repeated identical probe or transition reports MUST be idempotent for the same capability, measurement scope, observation identity, and semantic result. Replays MUST not inflate outage, availability, or recovery counts. A semantically distinct observation is retained with its own provenance and may cause a state transition according to the declared evaluation rule.

Probe failure MUST record the redacted failure class and queue impact. It MUST NOT expose provider credentials or caller content. A provider transport failure, rate limit, timeout, configuration failure, policy denial, and integrity failure MUST be distinguishable where safely possible, because their recovery and job handling differ.

No health state may be inferred from an absent probe alone. If current availability is unknown after the available observation expires, the capability transitions to `configured` when configuration remains valid, or `failed` when configuration is invalid or a required readiness condition is known to be absent. The state reason must make that distinction observable.

## Metrics and Failure Visibility

Health, job, outcome, and downstream product metrics MUST declare their measurement scope, window, source, numerator, denominator, and freshness. A zero denominator yields `not_computable`; it MUST NOT be rendered as green, complete, or zero-risk.

| Metric | Numerator | Denominator | Zero denominator |
|---|---|---|---|
| configured capability coverage | In-scope required capabilities with valid declared configuration | In-scope required capabilities | `not_computable`. |
| available capability coverage | In-scope required capabilities currently `available` | In-scope required capabilities | `not_computable`. |
| degraded-or-failed capability rate | In-scope required capabilities currently `degraded` or `failed` | In-scope required capabilities | `not_computable`. |
| paused capability rate | In-scope capabilities currently `paused` | In-scope capabilities | `not_computable`. |
| stale-health rate | In-scope capabilities whose last qualifying observation exceeds their declared freshness limit | In-scope capabilities requiring a freshness limit | `not_computable`. |
| health-attributable pending-work rate | Pending jobs whose recorded blocking capability is degraded, paused, or failed | Pending jobs with a recorded capability dependency | `not_computable`. |

The health view MUST expose backlog and age impact, terminal failures attributable to capability conditions, outcome-adapter gaps, and the measurement denominators by linking to their authoritative job and outcome records. It MUST NOT duplicate those records or infer their content from health alone. Decision, revision, retrieval-query, and exposure metrics remain owned by their respective authoritative contracts but must use the same denominator and `not_computable` rule.

## Compatibility and Rollback

A compatibility release may retain a versioned operational adapter only while its consumer, scope, owner, replacement evidence, sunset gate, and rollback boundary are declared. Health records may report such an adapter but MUST NOT make a dead route or retired architecture appear supported.

Rollback restores only the declared last-compatible operational configuration and adapter behavior. It preserves health history, affected job references, outcome gaps, and privacy-safe diagnostics. It MUST NOT delete evidence, reset job retry history, conceal a prior failed/degraded state, or reactivate an architecture-era flag as a substitute for rollback. A destructive removal of a capability or projection requires export or backup where applicable, migration/observation receipts, compatibility sunset, and independently verified rollback evidence.

Verification MUST demonstrate valid-but-unproven configuration, available probe/use, partial degradation with declared safe behavior, intentional pause, configuration failure, provider failure, recovery, stale-health handling, duplicate probe idempotency, queue impact, and that durable evidence acceptance continues during provider degradation.
