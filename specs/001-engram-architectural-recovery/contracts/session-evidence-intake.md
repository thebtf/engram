# Session Evidence Intake Contract

**Scope**: FR-008, FR-010, and FR-011. This contract defines the authoritative boundary for accepting supported session or task evidence. It does not define knowledge extraction, retrieval, or outcome interpretation.

## Purpose and Authority

A supported adapter submits one versioned evidence event for a resolved canonical project and a supported host session or task. The product MUST durably accept valid evidence before acknowledging it. Acceptance creates immutable `SessionEvidence` and the initial durable `ProcessingJob` in one atomic acceptance boundary. A provider, worker, timer, process-local watermark, new-memory count, or later knowledge decision MUST NOT determine whether accepted evidence exists.

The accepted event is evidence, not a knowledge claim and not a statement of successful session outcome. It remains separately retained when later processing is delayed, fails, or is quarantined.

## Required Intake Record

An accepted record MUST contain the following immutable acceptance facts:

| Fact | Contract |
|---|---|
| `evidence_id` | Stable opaque identifier for the accepted logical event. |
| `project_key` | Server-resolved canonical project identity. No raw selector is a substitute. |
| `session_key` | Supported host session identity; a task-scoped event MUST also identify its task when the host supplies one. |
| `principal_ref` and `privacy_scope` | Authorization and privacy context resolved or explicitly declared under the applicable anonymous or global policy. |
| `source_adapter` and `payload_version` | Versioned producer identity and accepted payload contract version. |
| `observed_at` | Time asserted by the source, retained separately from acceptance time. |
| `accepted_at` | Time at which the durable acceptance boundary completed. |
| `provenance` | Redacted acquisition facts sufficient to identify host, client instance, anchor descriptor, and acquisition mode. |
| `transcript_ref` or redacted content reference | A retention-policy-governed reference to admissible evidence. Raw secrets and credentials MUST NOT be retained through this contract. |
| `payload_fingerprint` | A privacy-safe semantic idempotency fingerprint. It MUST be scoped to source adapter, canonical project, session, and payload semantics and MUST NOT contain raw secret material. |
| `processing_job_id` | The single initial evidence-processing job created by the same acceptance boundary. |

Evidence content, provenance, and fingerprints MUST use the stricter applicable privacy scope. The contract MUST reject or quarantine unsafe content according to policy; it MUST NOT reduce privacy or derive a fingerprint from plaintext credentials merely to admit an event.

## Validation and Durable Acknowledgment

Before acceptance, the adapter boundary MUST validate payload version, supported source identity, required host session identity, canonical project resolution, principal/privacy authorization, payload size and structure limits, timestamp shape, and admissible redaction/content reference. Validation MUST occur before any durable evidence or job mutation.

A valid event is acknowledged only after all of the following are durably committed together:

1. one immutable `SessionEvidence` record;
2. its uniqueness claim for `(source_adapter, project_key, session_key, payload_fingerprint)`;
3. one `ProcessingJob` with `kind = evidence_processing`, causal `evidence_id`, durable idempotency key, state `received`, and receipt timestamps; and
4. an intake receipt binding the adapter request to `evidence_id` and `processing_job_id`.

An acknowledgement MUST mean this durable boundary succeeded. A transport acknowledgement, queued in-memory callback, or provider submission is not an acceptance receipt. If the acceptance boundary cannot commit, the adapter MUST receive no success acknowledgement and the caller MAY safely retry with the same event.

The acceptance receipt MUST identify the original evidence and job, report whether the request created or replayed that logical event, and expose the current durable job state. It MUST NOT expose redacted content, secrets, or authorization data beyond the caller's allowed scope.

## Idempotency and Replay

The uniqueness tuple in this contract identifies one logical accepted event. A repeat submission with the same tuple MUST return the original evidence and job receipt, MUST NOT create a second job, proposal, or knowledge decision, and MUST NOT restart a completed, terminal-failure, or quarantined job.

A retry that arrives after a successful commit but before the caller receives the receipt is therefore a replay, not a new event. A replay may observe a more advanced current job state, but its `evidence_id`, `processing_job_id`, acceptance facts, and creation receipt identity remain unchanged.

An event with a different semantic fingerprint is not a duplicate merely because it shares a session identifier. It MUST either be accepted as distinct evidence or be rejected before mutation when policy defines it as an invalid conflicting producer event. The contract MUST retain the reason for a rejection without retaining forbidden payload content.

## Rejection and Failure Results

No rejected event may create evidence, a processing job, a proposal, or a knowledge mutation. The adapter result MUST be actionable and classify the failure without disclosing protected data.

| Result class | Required behavior |
|---|---|
| `unsupported_source` or `unsupported_payload_version` | Reject before mutation and identify the accepted contract version or migration path when one exists. |
| `project_unresolved`, `project_ambiguous`, or `project_not_active` | Reject before mutation; no path-, name-, remote-, or hash-derived fallback identity may be minted. |
| `session_identity_invalid` | Reject before mutation; the adapter must provide a supported host session identity. |
| `authorization_denied` or `privacy_scope_invalid` | Reject before mutation and disclose only the authorization-safe reason. |
| `payload_invalid`, `payload_too_large`, or `redaction_required` | Reject before mutation; return the bounded repair condition, not protected content. |
| `idempotency_conflict` | Reject before mutation when the same protected logical source identity cannot safely be interpreted as either replay or distinct evidence. Preserve a redacted diagnostic/audit fact. |
| `durability_unavailable` | Return no success acknowledgement. The caller may retry unchanged input; no provider call or in-memory-only acceptance is permitted. |

If an accepted item later proves unsafe to process because of newly discovered privacy, provenance, or semantic ambiguity, it remains accepted evidence and is moved by its processing contract to `quarantined`; it is not retroactively erased or silently treated as rejected intake.

## Processing Availability, Compatibility, and Rollback

Every accepted event MUST remain discoverable by its receipt and job state without waiting for global idleness, a dream cycle, memory-count threshold, provider availability, or the lifetime of the accepting process. A timer MAY awaken eligible work but MUST NOT define the work set.

A provider outage after acceptance MUST leave evidence and its job durable and inspectable; it may delay or retry extraction but cannot invalidate the receipt. Deterministic bounded extraction MAY satisfy later processing where it meets the same proposal contract, but provider availability is never an intake prerequisite.

During a declared compatibility window, a legacy transcript or callback producer MUST pass through a versioned adapter that produces this same record and receipt shape. It MUST NOT create a parallel queue authority. A legacy producer lacking enough information for canonical identity, privacy, or safe fingerprinting MUST fail closed or create a visible quarantine through an approved migration/replay path; it MUST NOT fabricate missing facts.

Rollback of a later processing or release slice MUST preserve already accepted evidence, its receipt, provenance, privacy scope, and job history. The last-compatible boundary may route a supported legacy producer through its versioned adapter, but MUST NOT reinterpret an accepted event as unaccepted or delete it as a side effect of rollback.

## Evidence and Metrics

Acceptance verification MUST demonstrate durable receipt before acknowledgement; duplicate replay returning the original receipt; server restart discoverability; provider-degraded retention; and an inspectable terminal job disposition for each accepted fixture event.

The durable-evidence metrics are defined as follows:

| Metric | Numerator | Denominator | Zero denominator |
|---|---|---|---|
| acceptance durability rate | Valid events with a committed evidence-and-job receipt before success acknowledgement | Valid events for which the adapter attempted acknowledgement | `not_computable`; it MUST NOT be reported as 100%. |
| duplicate suppression rate | Duplicate submissions returning the original logical receipt with no second job | Duplicate submissions received | `not_computable`. |
| accepted-evidence terminal attribution | Accepted evidence with an individually inspectable terminal processing disposition | Accepted evidence older than the configured processing service objective | `not_computable`. |

Retention, export, migration, and deletion of evidence require explicit privacy policy, auditable receipts, and a named rollback boundary. Neither normal retry nor job completion authorizes destructive evidence removal.
