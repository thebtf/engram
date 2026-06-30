# Contract Hardening Packet — ENG-MPL-1 / CR-005

**Status:** PM packet source for later Developer handoff  
**Created:** 2026-06-30  
**Scope:** post-v6.30 contract/schema tightening only; no reopening shipped CR-001..CR-004 behavior

## Purpose

CR-005 turns the remaining roadmap debt into exact implementation contracts. These contracts are the acceptance source for later code work; if code and this packet disagree, implementation must amend the CR rather than silently broadening scope.

## Scope Boundaries

### In
- Resume packet field contract and fallback semantics.
- Experience retrieval contract and projection-first storage rule.
- Applicability / anti-applicability envelope fields.
- Archive trigger taxonomy and logging/audit fields.
- Review packet versus mutation execution boundary.

### Out
- New operator-console redesign beyond touched contract wiring.
- Broad graphification or universal temporal truth expansion.
- Learned applicability model.
- Reworking shipped CR-001..CR-004 behavior or release v6.30.0.

## Resume Packet Contract

A V1 resume packet must expose these product fields, regardless of final Go struct names:

| Field | Meaning | Required proof |
| --- | --- | --- |
| `packet_id` | stable identifier for this emitted packet | non-empty in packet fixture |
| `project` | project/repo identity used for lookup | scoped lookup test |
| `principal` | requesting principal or agent identity | privacy/scope test |
| `session_id` | active or source session when known | unknown is explicit, never omitted silently |
| `state_version` | version/freshness marker for optimistic conflict handling | stale packet fixture |
| `source` | native, filesystem-fallback, imported, or mixed | fallback marker test |
| `fallback_used` | whether filesystem fallback contributed | false on native happy path, true on fallback fixture |
| `drift` | stale/conflict flags between native and fallback state | drift fixture |
| `conflicts` | specific conflicting fields or empty list | conflict fixture |
| `next_action` | exact next contractual action | required, non-empty |
| `next_verification` | exact proof that the next action worked | required, non-empty |
| `evidence_refs` | artifacts/refs supporting packet truth | at least one source ref |

## Experience Contract V1

Experience is not an atomic memory row. V1 must represent:

| Field | Meaning |
| --- | --- |
| `situation` | context in which the experience occurred |
| `time_span` | when the trajectory happened |
| `decision_or_action` | what changed or was tried |
| `outcome` | observed result |
| `revision_or_reversal` | later correction, if any |
| `lesson` | durable lesson distilled from trajectory |
| `applicability` | context where lesson may apply |
| `anti_applicability` | context where lesson must not auto-apply |
| `provenance` | evidence refs for every non-obvious claim |
| `storage_origin` | projection/materialized/dedicated, explicit in output |

Storage rule: start projection/materialization-first over existing evidence. Dedicated `ExperienceRecord` tables require implementation evidence that projection cannot satisfy retrieval, audit, or performance acceptance.

## Applicability Envelope

| Field | Purpose |
| --- | --- |
| `applies_when` | positive context conditions |
| `does_not_apply_when` | anti-applicability blockers |
| `required_context` | fields that must be known before automatic reuse |
| `confidence` | high/medium/low or equivalent calibrated score |
| `block_reason` | why reuse was blocked or downgraded |
| `override_evidence` | explicit evidence needed for human/agent override |

Rule: a strong anti-applicability match blocks silent auto-reuse. A weak or uncertain match may surface as warning, but must not be treated as clean applicability.

## Archive Trigger Taxonomy

Archive/cold retrieval must not run on ordinary hot-path requests. Allowed V1 trigger classes:

| Trigger class | Fires when | Required log fields |
| --- | --- | --- |
| `historical_why` | caller asks why something changed or happened | prompt/classifier evidence, bounded query, result count |
| `regression_or_rollback` | current work references regression, rollback, revert, or broken-again behavior | failing artifact, compared prior state, result count |
| `revisit_old_decision` | caller reopens a prior decision or asks for old rationale | decision id/ref if known, query scope, result count |
| `similar_prior_failure` | current symptom resembles a previous failed attempt and hot memory is insufficient | similarity basis, confidence, anti-applicability check |
| `temporal_truth_change` | a selected high-value fact changed and prior truth is required | current truth ref, prior truth ref, validity window |
| `explicit_archive_lookup` | caller explicitly requests archive/history/cold search | caller text, scope, result count |

Every trigger log must record trigger class, caller/session/project scope, bounded result limit, whether experience retrieval ran, whether anti-applicability blocked reuse, and evidence refs. Default result cap: 10 resurfaced items unless a CR explicitly changes it.

## Review Packet / Mutation Boundary

Review packets are decision surfaces; mutation paths are execution surfaces.

| Surface | Owns | Must not do |
| --- | --- | --- |
| Review packet | preview, rationale, risk, before/after, operator/agent decision | silently mutate memory/state without explicit mutation call |
| Mutation path | apply suppress/expire/archive/consolidate/destroy/state-write and audit result | ask operator to infer risk from raw rows |
| Audit record | reconstruct who/what/why/when/result | hide structural-loss or provenance warnings |

Boundary rule: a risky packet can approve a mutation, but the mutation path must still validate structural-loss, privacy scope, and audit write before applying.

## Validation Expectations

CR-005 implementation is complete only when:
- contract tests or fixtures prove every required field above is emitted or intentionally `unknown` with a legal reason;
- archive-trigger tests prove ordinary hot-path requests do not search archive;
- applicability tests cover applies / uncertain / blocked;
- review-packet tests prove preview and mutation are separate;
- closeout evidence proves shipped CR-001..CR-004 behavior was not reopened.
