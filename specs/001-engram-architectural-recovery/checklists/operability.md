# Operability Requirements Checklist: Engram Architectural Recovery

**Purpose**: Review whether durable work, capability health, metrics, release, and dogfood
requirements are observable and operationally actionable.
**Created**: 2026-08-22
**Feature**: `../spec.md`, `../plan.md`, `../quickstart.md`, `../contracts/capability-health.md`
**Review Ownership**: Requirements-quality reviewer. `[x]` means requirement quality only.

## Observability Completeness

- [x] CHK001 Are backlog, oldest pending age, attempts, terminal failures, quarantines, decision,
      revision, query, exposure, outcome, identity, merge, and capability metrics specified with
      durable sources? [Completeness, FR-019]
- [x] CHK002 Does every metric declare numerator, denominator, scope, window, freshness, and
      non-computable zero-denominator behavior? [Measurability, Capability Health Contract]
- [x] CHK003 Are direct current-process counters and cumulative records prevented from being
      combined without a named semantic distinction? [Consistency, Research §Freshness]

## Degradation and Recovery Coverage

- [x] CHK004 Are configured, available, degraded, paused, and failed capability states defined
      without selecting a competing domain architecture? [Clarity, FR-020]
- [x] CHK005 Are provider outage, worker crash, lease loss, restart, retry, pause, resumed work,
      timeout, and outcome-adapter gap requirements complete? [Coverage, Processing/Outcome
      Contracts]
- [x] CHK006 Are normal acceptance, provider-degraded processing, lexical fallback, and explicit
      empty-result requirements kept consistent? [Consistency, Retrieval Contract]

## Release and Dogfood Quality

- [x] CHK007 Does every AR release specify one installed outcome, observation condition, receipt,
      rollback boundary, and clean-main build condition? [Completeness, Plan §Release Map]
- [x] CHK008 Are dogfood targets/observation windows defined with individually attributable
      remainder cases instead of aggregate green claims? [Measurability, SC-005/SC-011]
- [x] CHK009 Is the exact-head SonarQube gate stated as a release prerequisite independent of
      source/test success? [Coverage, Constitution Delivery Gates]

- [x] CHK010 Are operator working-surface changes explicitly excluded from operational recovery
      requirements unless a separate accepted feature authorizes them? [Scope, Constitution XII]

## Notes

**AR-0 review result: 8/10 supported (PASS items: CHK001, CHK004-CHK010; findings: CHK002, CHK003).**

- **CHK002 remains unchecked — FAIL:** `plan.md` §Observability names source, denominator, time window, and exclusion policy, while `capability-health.md:61-72` supplies complete freshness and `not_computable` rules only for capability/job-linked metrics. No AR-0 artifact gives numerator, freshness, and zero-denominator definitions for every decision, revision, retrieval-query, exposure, identity-convergence, and merge metric.
- **CHK003 remains unchecked — FAIL:** `plan.md` §Observability requires durable records rather than in-process counters, but `research.md` §Freshness and the contracts do not define a named semantic distinction or an explicit prohibition against combining current-process counters with cumulative durable records.

**R2 re-review result: 10/10 supported (PASS items: CHK001-CHK010; remaining unmet IDs: none).**

- **CHK002 now supported:** `plan.md` §Observability requires every metric to declare numerator, denominator, scope, window, freshness, source, and exclusion policy, with zero denominators reported as `not_computable`; capability-health, processing-job, session-evidence, and session-outcome contracts enumerate the owner metric semantics, while `migration-receipt.schema.json` enforces scope/window/numerator/denominator fields and the zero-denominator result status.
- **CHK003 now supported:** `plan.md` §Observability explicitly makes durable records authoritative, names current-process counters and cumulative records as separate metric families, and permits combination only after a written mapping proves the same event class, window, and denominator.


## R3 Final Review Notes

- Review iteration: final AR-0 requirements-quality recheck after R1 remediation and task generation, 2026-08-22.
- Result: **PASS** — 10/10 checked; no regressions identified.
- Rechecked CHK001-CHK010 against the final `plan.md`, `spec.md`, `tasks.md`, `quickstart.md`, operational acceptance and processing-job/capability/retrieval/outcome contracts, migration receipt schema, and final source/package/flag/data inventories. Durable metric ownership, numerator/denominator/freshness semantics, `not_computable` zero-denominator behavior, failure/recovery states, policy declarations, exact-head SonarQube gating, AR-3→AR-6 continuity migration/observation/deletion ordering, and the explicit no-new-operator-surface boundary remain internally consistent.
- Exact regression IDs: none.

## R4 Review Notes

- Review iteration: AR-0 R4 requirements-quality regression recheck after R2 changes, 2026-08-22.
- Result: **PASS** — 10/10 checked; no regressions identified.
- Rechecked the temporary-control capability metrics and safe degradation, T075's active scope-matched render-budget policy gate before T076 cutover, and the synchronized AR-6 release/rollback and observation ordering; operational metric, failure-state, and no-new-surface requirements remain complete.
- Exact regression IDs: none.
