# Operational Acceptance Policy Contract

**Scope**: SC-005, SC-010, FR-019, FR-020, and release receipt evidence.  
**Purpose**: Define the one typed, versioned configuration surface that makes evidence-processing
and retrieval acceptance thresholds executable without turning architecture maturity into flags.

## Policy Record

Each installed AR-4+ or AR-6+ release that claims its corresponding criterion MUST load one
`RecoveryAcceptancePolicy` record at startup and bind its immutable policy ID/version to every
scenario, metric, and release receipt. The record is operational configuration, not a second domain
workflow and not a feature-era switch.

| Field | Requirement |
|---|---|
| `policy_id`, `policy_version` | Stable non-secret identifiers. A changed threshold produces a new version. |
| `effective_release` | The exact release that activates this policy. |
| `owner` | `AR-4 session-evidence owner` for evidence objective or `AR-6 retrieval owner` for render budget. |
| `measurement_scope` | Supported hosts, adapters, projects/principals, and corpus/fixture scope to which the declaration applies. |
| `baseline_reference` | Redacted AR-1/AR-6 baseline evidence used to select the value. |
| `rollback_reference` | Prior policy version and rule for rollback/re-comparison. |
| `approval_reference` | Release approval/receipt reference; absence blocks acceptance claim. |

## Evidence Processing Objective

`evidence_processing_terminal_objective` contains a finite positive duration, the durable
acceptance-to-terminal start/end definition, eligible evidence predicate, excluded-event policy, and
service objective measurement window. The active value is required before SC-005 is computed.

If the policy is absent, invalid, scope-mismatched, or its eligible denominator is zero, the
terminal-attribution result is `not_computable`. An AR-4 exit receipt MUST not claim SC-005 in that
state. Changing the duration requires a new policy version, comparison with the previous result,
and rollback semantics; it cannot reset job/evidence history.

## Retrieval Render Budget

`retrieval_render_budget` contains a stable `render_budget_id`, finite positive `max_items`, finite
positive `max_tokens`, qualifying trigger types, permitted fallback behavior, and corpus scope. The
active values are required before SC-010 is computed or static context is cut over.

If the policy is absent, invalid, scope-mismatched, or the corpus has no eligible render attempts,
the packet-budget result is `not_computable`. An AR-6 exit receipt MUST not claim SC-010 or retire
normal static context in that state. A budget change requires a new policy version, comparison at
both values, and a rollback policy; it cannot alter prior exposure semantics.

## Validation and Receipt Rules

1. Startup rejects non-positive duration/item/token values and emits a redacted capability-policy
   failure; it never substitutes an implicit default for an acceptance claim.
2. Every scenario/metric receipt identifies `policy_id`, `policy_version`, active values or their
   redacted fingerprint, measurement scope, numerator/denominator, zero-denominator state, and
   baseline/approval/rollback references.
3. The policy is readable as a non-secret capability/acceptance declaration but its mutation path
   is a release-controlled configuration change with owner, approval, and rollback audit.
4. Provider health may cause degraded behavior but cannot rewrite an active objective/budget,
   suppress its `not_computable` result, or select a second domain workflow.
5. Independent release verification validates the policy before interpreting SC-005 or SC-010.
