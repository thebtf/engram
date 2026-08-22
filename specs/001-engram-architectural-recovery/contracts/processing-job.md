# Processing Job Contract

**Scope**: FR-009, FR-010, and FR-011. This contract defines the persisted state machine for asynchronous evidence processing. It applies to evidence-backed work and may be reused by other durable recovery workflows only when their causal source and idempotency scope are explicit.

## Durable Job Identity

A `ProcessingJob` is the only authority for pending asynchronous work. It MUST persist:

- immutable `job_id`, `kind`, `project_key`, and, for evidence-backed work, `evidence_id`;
- a durable `idempotency_key` unique within `kind` and `project_key`;
- current `state`, checkpoint or resume stage, attempt count, next eligible time, and lifecycle timestamps;
- current lease owner, lease expiry, and monotonically advancing lease epoch or equivalent fencing identity;
- redacted last error code and summary, error classification, and retry history sufficient to explain every attempt;
- terminal reason and terminal timestamp for every terminal state; and
- links to produced proposals and any quarantine disposition without making those projections the work authority.

For `evidence_processing`, the idempotency key MUST bind the job to the accepted evidence identity and processing semantics. A repeated intake event MUST resolve to the original job. A worker retry MUST update the same job, never mint duplicate knowledge work.

Pending work is discoverable solely from persisted state and eligibility. Global idleness, new-memory counts, in-process watermarks, provider readiness, or a timer's last execution MUST NOT hide, delete, or decide whether work exists.

## Scope and Authorization Gate

Before a worker claims, advances, retries, quarantines, or terminalizes a project-scoped job, it
MUST validate the job's canonical `project_key`, source/provenance, principal authorization, and
privacy scope against the current worker operation. Lease eligibility and fencing authorize only
ownership of an already scoped job; they do not authorize a cross-project, stale-principal, or
privacy-widening mutation. A mismatch is recorded as a redacted authorization/scope failure and
the job remains unchanged or moves only through the approved quarantine policy.

Merge, import, export, and administrative job kinds additionally require their declared
authorization reference, audit correlation, and rollback/retention boundary before claim. No job
kind may accept a raw selector, path, name, remote, hash, or caller-supplied project as a substitute
for central project resolution.

## States and Terminality

The durable state machine is:

```text
received -> normalized -> extracting -> proposed -> reconciling -> completed
       \-> retryable_failure -> normalized | extracting | reconciling
       \-> terminal_failure
       \-> quarantined
```

| State | Meaning | Terminal |
|---|---|---|
| `received` | Intake committed the job; no processing checkpoint is complete. | No |
| `normalized` | Evidence was validated and normalized into the bounded processing representation. | No |
| `extracting` | Bounded proposal extraction is actively owned or has an extraction checkpoint to resume. | No |
| `proposed` | Extraction produced zero or more persisted bounded proposals and no further extraction is required. | No |
| `reconciling` | Persisted proposals are being brought to their required terminal reconciliation decisions. | No |
| `retryable_failure` | A non-terminal failure stopped the recorded stage. The job retains its resume stage and next eligible time. | No |
| `completed` | All required processing reached its defined terminal disposition. `completed_no_proposals` is valid only with an explicit terminal reason. | Yes |
| `terminal_failure` | Processing cannot proceed under the contract or retry policy. Evidence and diagnostics remain inspectable. | Yes |
| `quarantined` | Processing is unsafe or undecidable without an explicit authorized disposition. Evidence remains retained and no unsafe proposal or belief activation occurs. | Yes |

Only the following transitions are valid:

- `received` transitions to `normalized`, `retryable_failure`, `terminal_failure`, or `quarantined`.
- `normalized` transitions to `extracting`, `retryable_failure`, `terminal_failure`, or `quarantined`.
- `extracting` transitions to `proposed`, `retryable_failure`, `terminal_failure`, or `quarantined`.
- `proposed` transitions to `reconciling`, `completed`, `retryable_failure`, `terminal_failure`, or `quarantined`.
- `reconciling` transitions to `completed`, `retryable_failure`, `terminal_failure`, or `quarantined`.
- `retryable_failure` transitions only to its recorded resume stage (`normalized`, `extracting`, or `reconciling`), `terminal_failure`, or `quarantined`.

A terminal state is immutable with respect to normal worker processing. Any authorized remediation MUST create a separately identified successor or review workflow that cites the terminal job and evidence; it MUST NOT erase the original terminal state, attempts, or error history.

## Ownership and Leases

A worker may claim only a persisted eligible non-terminal job whose next eligible time has arrived and whose lease is absent or expired. A successful claim records worker identity, lease expiry, and a new fencing identity before work begins. Job advancement, retry scheduling, terminalization, and emitted proposals MUST be committed only by the holder of the current unexpired fencing identity.

A worker that loses, cannot renew, or discovers expiration of its lease MUST stop advancing the job. Its partial external or provider result is not authoritative until a current owner safely records it. A later worker may claim the expired job from its last committed checkpoint; it MUST NOT assume uncommitted work happened.

Competing claims, late completions, and duplicate worker deliveries MUST be rejected as stale ownership, not treated as a reason to duplicate proposals or terminalize the job incorrectly. Lease expiration makes the same persisted work eligible again; it does not change evidence meaning, increment attempts by itself, or erase diagnostics.

## Attempts, Retry, and Provider Degradation

An attempt begins when a valid lease holder starts a durable stage and increments `attempt_count` exactly once. A lease claim without stage execution is not an attempt. Each failed attempt MUST record a redacted error classification, error code, summary, timestamp, and resume stage.

Retryable failures include bounded transient conditions such as provider unavailability, rate limiting, timeout, temporary dependency failure, lease loss before commit, and retry-safe storage or transport interruption. They MUST move the job to `retryable_failure`, set a persisted future `next_attempt_at` using the configured bounded retry policy, and leave the evidence inspectable.

The retry policy MUST be versioned or otherwise durably attributable for each job. It MUST define maximum attempts, delay/backoff bounds, any retry deadline, and error classes that are not retryable. A worker MUST NOT retry indefinitely, busy-loop, or silently reset attempt history after restart, deployment, or provider recovery.

Provider degradation is not an intake failure and cannot remove or conceal jobs. Where a deterministic extraction path satisfies the same bounded proposal contract, it MAY be used; otherwise the job remains retryable until the persisted policy reaches a terminal outcome. Provider health is recorded separately by the capability-health contract.

The job MUST enter `terminal_failure` when a non-quarantinable failure is non-retryable or the durable retry policy is exhausted. It MUST enter `quarantined` when continued processing would violate privacy, provenance, identity, policy, or semantic-safety requirements, or when an approved review is required to resolve an unsafe ambiguity. An implementation defect, malformed accepted legacy payload, or unavailable provider MUST NOT be misreported as successful completion.

## Evidence Processing Service Objective

The terminal-attribution service objective is a versioned operational declaration, not an implicit
timer setting. Before an AR-4 dogfood claim, the active release receipt MUST bind exactly one
`evidence_processing_terminal_objective` with: a stable objective ID, finite positive duration,
eligible evidence definition, measurement start event, owner (`AR-4 session-evidence owner`),
effective release, source of its AR-1 baseline, and rollback/revision reference. Its value applies
only to the stated project/host/adapter measurement scope.

The terminal-attribution denominator contains only eligible accepted evidence older than the active
objective duration. If no active declaration exists or the denominator is zero, the metric result
is `not_computable`; no release may claim SC-005 or terminal-attribution success from that result.
Changing the duration requires a new policy version, receipt, comparison with the previous value,
and an explicit rollout/rollback boundary; it MUST NOT silently reset backlog or outcome evidence.

## Completion and Proposal Boundary

A job may reach `completed` only when all its required stages have durably completed. For evidence processing, this means either:

1. bounded extraction yielded no admissible proposals and the terminal reason is `completed_no_proposals`; or
2. every emitted proposal has one persisted reconciliation terminal decision under the knowledge contract.

A provider result alone is not a proposal. Proposals must be bounded, retain evidence provenance and compatible privacy scope, and be persisted before the job advances to `proposed`. An extraction provider is never required for evidence durability; failed extraction follows retry, terminal-failure, or quarantine rules rather than deleting evidence.

## Errors, Observability, and Recovery

The job record MUST retain a diagnostic fit for authorized inspection while redacting payload content, credentials, and private data. At minimum, worker and operator views MUST distinguish `retryable_failure`, `terminal_failure`, `quarantined`, and `completed`, display current/last checkpoint, attempt count, next eligible time when applicable, lease status, terminal reason, and causal evidence receipt.

A process restart, worker replacement, or timer outage MUST be recoverable by scanning persisted eligible jobs. Timers may perform this scan, but their absence cannot make work undiscoverable. A stuck active lease becomes recoverable only upon expiry or an explicit safe ownership-release procedure; a second worker must not steal an unexpired lease.

Required backlog metrics have named denominators:

| Metric | Numerator | Denominator | Zero denominator |
|---|---|---|---|
| pending backlog | Jobs in non-terminal states | All jobs in the stated project, kind, and observation window | `not_computable`. |
| aged pending backlog | Non-terminal jobs older than the configured age objective | Non-terminal jobs in the same scope | `not_computable`. |
| terminal failure rate | Jobs entering `terminal_failure` in the window | Jobs reaching any terminal state in the window | `not_computable`. |
| quarantine rate | Jobs entering `quarantined` in the window | Jobs reaching any terminal state in the window | `not_computable`. |
| terminal attribution rate | Accepted evidence whose causal job is terminal | Accepted evidence older than the configured service objective | `not_computable`. |

## Compatibility and Rollback

A legacy transcript or workflow may be replayed only through a declared adapter that creates this state machine and preserves source provenance. Legacy timer rows, process watermarks, and provider callbacks MUST NOT remain parallel job authorities. A replay is idempotent against the resulting job key and preserves the original source reference.

Rollback preserves job identity, state history, evidence links, attempts, leases, diagnostics, and terminal reasons. A release rollback may pause claims or route compatible producers through a declared adapter, but MUST NOT silently delete jobs, reset retry budgets, convert terminal failure into completion, or re-run terminal work as if it were new. Destructive contraction requires separate migration, export, observation, and rollback evidence.

Verification MUST cover duplicate intake, restart recovery, lease expiry and stale-owner rejection, provider outage, bounded retry exhaustion, unsafe-data quarantine, no-proposal completion, and every permitted terminal disposition.
