# Architecture Requirements Checklist: Engram Architectural Recovery

**Purpose**: Review whether architecture requirements define one coherent, dependency-safe
recovery shape.
**Created**: 2026-08-22
**Feature**: `../spec.md`, `../plan.md`, `../research.md`
**Review Ownership**: Requirements-quality reviewer. `[x]` means the written requirement is
satisfied; it never means implementation is complete.

## Requirement Completeness

- [x] CHK001 Are authoritative ownership requirements defined for Identity, Intake, Knowledge,
      Retrieval, Learning, State, Governance, Projections, and Adjacent Primitives? [Completeness,
      Plan §Target Module Boundaries]
- [x] CHK002 Are adapter-to-workflow boundaries and the prohibition on handler-side architecture
      selection specified for every supported transport? [Completeness, FR-021]
- [x] CHK003 Are all current parallel authorities assigned a target-core, move, projection,
      adjacent, compatibility, quarantine, delete, or historical disposition? [Completeness,
      Evidence inventories]

## Requirement Clarity and Consistency

- [x] CHK004 Are dependency directions explicit enough to prevent Project Identity depending on
      knowledge/retrieval and projections becoming write authorities? [Clarity, Plan §Target Module
      Boundaries]
- [x] CHK005 Are State Plane and historical knowledge consistently distinguished across the
      specification, data model, plan, and contracts? [Consistency, FR-013]
- [x] CHK006 Does the one-product-loop requirement remain consistent with every stated fallback,
      compatibility adapter, and temporary operational control? [Consistency, Constitution I-II]

## Scenario and Change Coverage

- [x] CHK007 Are normal, degraded-provider, identity-convergence, and rollback critical-path
      requirements described without a hidden second workflow? [Coverage, Plan §Integration
      Simulations]
- [x] CHK008 Are D3 release boundaries and deferred decisions explicit enough to prevent AR-1
      implementation from absorbing AR-2 through AR-7 scope? [Scope, Plan §Release Map]
- [x] CHK009 Are the no-new-network-service and no-working-surface-design constraints expressed as
      enforceable boundaries rather than intentions? [Constraint, Constitution XII]

## Notes

- [x] CHK010 Does the plan state a measurable closure condition for every architecture decision
      classified as temporary or quarantine-pending-proof? [Measurability, Evidence inventories]

## Review Notes

- Review iteration: AR-0 requirements-quality review, 2026-08-22.
- Result: **FAIL** — 8/10 checked; 2 findings remain unchecked.
- CHK004 finding: `specs/001-engram-architectural-recovery/plan.md:106-107` draws `P[Rebuildable Projections] --> K` and `P --> L`, while `plan.md:120` says Projections may depend on authoritative records/events and **must not** have authoritative writes. The diagram and boundary table give opposite dependency direction, so the authority boundary is not unambiguous.
- CHK010 finding: the source, route, data, and identity inventories provide `owner`, `target_release`, and notes or required proof, but do not provide a measurable per-entry `closure_rule`/verifier for every `compatibility-temporary` or `quarantine-pending-proof` decision. A target release alone does not close the temporary architecture decision.


## R2 Review Notes

- Review iteration: AR-0 R2 requirements-quality re-review, 2026-08-22.
- Result: **PASS** — 10/10 checked; no remaining unmet architecture IDs.
- CHK004: The revised dependency diagram now points authoritative Knowledge Core and Learning into Rebuildable Projections (`K --> P`, `L --> P`), while the boundary table explicitly forbids projection authority writes and forbids Project Identity dependencies on knowledge/retrieval.
- CHK010: The Removal Ledger and Closure Rule require every inventory entry to name `owner`, `evidence`, `target_release`, `closure_rule`, and independent `closure_verifier`; compatibility entries additionally require replacement, observed consumer/retirement evidence, sunset, rollback, and source/configuration scan, while quarantine entries require a recorded safe disposition.


## Final Review Notes

- Review iteration: AR-0 final requirements-quality recheck after R1 remediation and task generation, 2026-08-22.
- Result: **PASS** — 10/10 checked; no regressions identified.
- Rechecked the final `spec.md`, `plan.md`, `tasks.md`, traceability contract/matrix, target contracts, quickstart, and final evidence inventories. Ownership/dependency boundaries, one-loop sequencing, D3 release boundaries, projection authority, continuity migration ordering, and measurable closure rules remain coherent.
- Exact regression IDs: none.
- Per-file checked/total: `architecture.md` 10/10.


## R4 Review Notes

- Review iteration: AR-0 R4 requirements-quality regression recheck after R2 task/inventory remediation, 2026-08-22.
- Result: **PASS** — 10/10 checked; no regressions identified.
- Rechecked T074/T075/T076 emergency-control and render-budget/cutover ordering, T083 adjacent compatibility, and inventory closure/dependency boundaries; one-loop authority and D3 scope remain explicit.
- Exact regression IDs: none.
- Per-file checked/total: `architecture.md` 10/10.
