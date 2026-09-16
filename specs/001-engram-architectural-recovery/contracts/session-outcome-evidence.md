# Session Outcome Evidence Contract

**Scope**: FR-018 and the outcome-coverage portion of FR-019. This contract defines durable completion evidence for supported hosts after retrieval delivery or session/task completion. It does not infer knowledge utility from process exit, citation presence, or provider availability.

## Outcome Record and Scope

A `SessionOutcomeEvidence` record is immutable evidence about one supported session or task. It MUST contain:

| Fact | Contract |
|---|---|
| `outcome_id` | Stable opaque identity of one accepted outcome observation. |
| `project_key` and session/task identity | Canonical project plus the supported host session or task to which the observation applies. |
| `exposure_refs` | Zero or more relevant retrieval exposure identities when known. Absence of an exposure does not prohibit a session outcome record. |
| `outcome` | Exactly one of `success`, `partial`, `failure`, `abandoned`, or `unknown`. |
| `certainty` | Exactly one of `observed`, `explicit`, `inferred`, or `timeout`. |
| `source_adapter` and source event identity | Versioned supported-host producer and its idempotency scope. |
| `observed_at` and accepted timestamp | Source observation time and durable acceptance time, retained separately. |
| `idempotency_fingerprint` | Privacy-safe fingerprint of source, canonical project, session/task, and outcome semantics. |
| `explanatory_evidence_refs` | Authorized redacted references that explain the classification or uncertainty. |
| `conflict_disposition` | The observation's current conflict status without altering its immutable original fact. |

Outcome evidence MUST preserve principal and privacy scope compatible with the session and every referenced exposure. It MUST retain redacted provenance and MUST NOT store credentials, raw private transcripts, or unauthorized content merely to classify completion.

## Automatic Capture

For every supported host, the product MUST define a versioned automatic outcome adapter and the host events that it recognizes. The adapter MUST durably record each admissible terminal host observation without requiring an agent citation, manual feedback, or an extraction provider.

Automatic capture is complete only after the outcome record and its idempotency claim are durably committed. The receipt MUST identify the original `outcome_id`, whether the event was newly recorded or replayed, its outcome and certainty, and conflict disposition visible to the authorized caller.

Explicit user or agent feedback remains an additional signal, not a replacement for automatic capture. It MUST use certainty `explicit`; it cannot silently overwrite automatic evidence. Direct host-confirmed completion or failure uses `observed`. `inferred` may be used only when the supported-host contract identifies the specific admissible evidence and inference rule; it MUST NOT be used for process exit alone, absent citation, or provider loss.

## Outcome Semantics

| Outcome | Meaning |
|---|---|
| `success` | Supported evidence explicitly establishes intended completion or success under the host contract. |
| `partial` | Supported evidence establishes that some intended work occurred but terminal success is not established. |
| `failure` | Supported evidence explicitly establishes a terminal failed outcome. |
| `abandoned` | The host contract establishes that work was intentionally or operationally abandoned before a supported terminal completion result. |
| `unknown` | The product lacks sufficient admissible evidence to assert success, partial, failure, or abandonment. |

Certainty qualifies evidence strength; it does not silently change the outcome vocabulary. A process exit by itself MUST result only in `unknown` or, where the declared host timeout policy proves abandonment, `abandoned`. It MUST NEVER produce `success`, `partial`, or `failure` solely because a process exited. Citation absence, provider failure, failure to retrieve, or missing callback is likewise not evidence of success or utility.

## Idempotency and Conflicting Observations

The idempotency fingerprint identifies one logical outcome observation. Exact re-delivery MUST return the original receipt and MUST NOT create a second observation, mutate its outcome, or duplicate any linked exposure accounting.

A semantically different observation for the same project and session/task is not a duplicate. It MUST be retained as a separate immutable outcome observation and evaluated under this conflict policy:

1. Compatible observations receive `uncontested` disposition and remain independently traceable.
2. A contradictory automatic or inferred observation receives `conflict_pending`; neither observation is overwritten, deleted, or elevated into a fabricated resolved outcome.
3. An authorized explicit adjudication may set the linked observations to `resolved_by_authorized_evidence` and record the adjudicating evidence, actor, time, and rationale. The original facts remain visible.
4. A duplicate replay receives `duplicate_of_existing` as a receipt fact only; it does not create another stored observation.

Conflict resolution MUST use the stated evidence and privacy rules. Timestamp order, source arrival order, or raw certainty rank alone MUST NOT silently select a winner. Metrics and downstream learning MUST report unresolved conflicts rather than treating them as success.

## Timeout, Absence, and Adapter Failure

Each supported-host outcome policy MUST declare the activity boundary, terminal-event set, timeout duration, whether its timeout proves `abandoned` or only `unknown`, and the policy version. The timeout clock begins from the last durable host activity defined by that policy, not from a worker's in-memory observation.

When the timeout expires without admissible terminal evidence, the product MUST automatically create one idempotent timeout observation:

- `abandoned` with certainty `timeout` only when the host policy establishes an abandonment condition; otherwise
- `unknown` with certainty `timeout`.

The timeout observation MUST cite the policy version and durable activity boundary. A later supported terminal observation is retained; it does not erase the timeout record and is subject to the conflict policy when incompatible.

If the automatic adapter cannot accept an event or run the timeout evaluation because its operational capability is unavailable, evidence intake and retrieval exposure remain durable and unaffected. The failure MUST be observable through capability health and the product MUST retain one actionable outcome gap for each affected eligible session/task. Once capability resumes, the adapter MUST reconcile the gap from durable host facts where available; it MUST NOT invent an outcome for lost or unavailable facts. If the configured timeout boundary passes without usable facts, it produces the required `unknown` or policy-qualified `abandoned` timeout observation.

## Coverage, Privacy, and Rollback

Outcome coverage MUST be measured with named denominator and uncertainty:

| Metric | Numerator | Denominator | Zero denominator |
|---|---|---|---|
| automatic terminal outcome coverage | Supported-host sessions/tasks that normally completed and have at least one automatically captured terminal outcome observation | Supported-host sessions/tasks classified as normally completed by the declared host policy in the observation window | `not_computable`. |
| timeout attribution coverage | Eligible supported-host sessions/tasks past their policy timeout with a durable timeout observation | Eligible supported-host sessions/tasks past their policy timeout | `not_computable`. |
| unresolved outcome conflict rate | Session/task outcome groups with one or more `conflict_pending` observations | Outcome groups with two or more non-duplicate observations | `not_computable`. |
| actionable outcome-gap rate | Eligible supported-host sessions/tasks with an open, attributable adapter gap | Eligible supported-host sessions/tasks | `not_computable`. |

A zero denominator MUST never be converted into 100% coverage or zero failure. Outcome metrics MUST distinguish `unknown`, `abandoned`, unresolved conflict, and adapter gap from success.

Compatibility adapters for legacy callbacks may emit this contract only when they can establish canonical identity, supported session/task identity, source version, privacy scope, and idempotency. Unsupported dead routes must return actionable retirement behavior and MUST NOT fail silently. A legacy event lacking these facts is an explicit gap or rejection, not a fabricated automatic outcome.

Rollback preserves accepted outcome observations, source/certainty, conflict disposition, privacy scope, exposure references, and timeout-policy provenance. It MAY restore a declared compatible adapter but MUST NOT rewrite `unknown` to success, erase a conflict, or discard outcome history as a side effect. Any retention or destruction action requires its own authorized privacy, export, audit, and rollback boundary.

Verification MUST exercise normal automatic completion, automatic failure, explicit feedback, duplicate replay, abrupt termination, timeout, late terminal event after timeout, conflicting repeats, unavailable outcome capability, and privacy-restricted observations.
