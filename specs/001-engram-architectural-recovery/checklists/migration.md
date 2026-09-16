# Migration Requirements Checklist: Engram Architectural Recovery

**Purpose**: Review whether migration, compatibility, rollback, and contraction requirements are
complete and safely ordered.
**Created**: 2026-08-22
**Feature**: `../spec.md`, `../plan.md`, `../quickstart.md`, `../contracts/`
**Review Ownership**: Requirements-quality reviewer. `[x]` means requirement quality only.

## Requirement Completeness

- [x] CHK001 Are inventory, decision, execution plan, and receipt required for each migration
      slice? [Completeness, Plan §Schema and Migration Sequence]
- [x] CHK002 Are expand, dual compare, backfill, verification, cutover, observation, and contract
      ordered explicitly for identity and knowledge changes? [Ordering, FR-025]
- [x] CHK003 Are backup/export, fixture, count, provenance, privacy, referential-integrity,
      idempotency, and rollback evidence required before destructive contraction? [Coverage,
      SC-013]

## Requirement Clarity and Consistency

- [x] CHK004 Is the last compatible read boundary distinguishable from a forbidden second domain
      authority? [Clarity, Compatibility Matrix]
- [x] CHK005 Are retryable, terminal, quarantined, and operator-decision migration outcomes
      distinguished without silent fallback or last-write-wins behavior? [Consistency, Data Model]
- [x] CHK006 Do all release slices state the data/user carry-forward pattern and rollback boundary?
      [Coverage, Plan §Release Map]

## Failure and Recovery Coverage

- [x] CHK007 Are interrupted/restarted backfill, duplicate apply, merge conflict, client lag,
      rollback before contraction, and restore after contraction specified? [Edge Coverage,
      Quickstart]
- [x] CHK008 Is a zero denominator explicitly non-computable for every migration metric that could
      otherwise appear green? [Measurability, Constitution XIII]
- [x] CHK009 Are live database, credential, and production-approval boundaries stated so AR-0 does
      not misrepresent source-only evidence as runtime proof? [Scope, Research §Freshness]

## Notes

- [x] CHK010 Does each contraction requirement name source/config/docs/tests/mocks/routes/tools/UI
      cleanup as a coupled change rather than a database-only operation? [Completeness, FR-022]


## AR-0 Review Note

- Result: FAIL
- Checked: 9/10.
- Unchecked: CHK008. `plan.md` §Observability requires named denominators but does not define the zero-denominator result for migration/identity-convergence/merge-quarantine metrics; `contracts/migration-receipt.schema.json` records counts and observation status but has no `not_computable` or equivalent zero-denominator state. The explicit zero-denominator rule exists in the evidence, processing-job, outcome, and capability-health contracts, but is not carried into the migration metric/receipt contract, so an empty migration population could still be rendered as a green result.

## R2 Review Note

- Result: PASS
- Checked: 10/10.
- Remaining unmet IDs: none.
- CHK008 is now supported. `plan.md` §Observability states that a zero denominator is `not_computable`, never green, complete, or zero-risk; `data-model.md` §Migration Receipt applies the same rule to every migration metric; and `contracts/migration-receipt.schema.json` requires `denominator_value` and `result_status`, with schema conditions forcing `0` to `not_computable` and positive denominators to `computed`.

## R3 Final Review Note

- Result: PASS
- Checked: 10/10.
- Regressions: none.
- Final recheck: The final plan, task order, continuity ownership, migration receipt schema, compatibility matrix, and quickstart preserve expand/compare/backfill/verify/cutover/observe/contract ordering; ProcessingJob is established before identity backfill; continuity migration, AR-6 replacement observation, and AR-7 deletion are explicitly sequenced; and migration metrics reject zero denominators as `not_computable`. No migration checklist criterion regressed.


## R4 Review Notes

- Review iteration: AR-0 R4 requirements-quality regression recheck after R2 task/inventory remediation, 2026-08-22.
- Result: **PASS** — 10/10 checked; no regressions identified.
- Rechecked the AR-3 ProcessingJob-before-backfill order, migration receipts, continuity migration/observation/contraction sequence, T074/T075/T076 ordering, rollback boundaries, and zero-denominator rules; migration policy remains explicit and fail-closed.
- Exact regression IDs: none.
- Per-file checked/total: `migration.md` 10/10.

## AR-2 Delta Reconciliation

- [x] AR2-CHK011 Does the delta bind migration `162+` to additive nullable typed-key schema, idempotent repeat apply, real PostgreSQL fixture evidence, and V2-read rollback without backfill or contraction? [AR-2 reconciliation]
- [x] AR2-CHK012 Does the compatibility receipt require nonzero transport/comparison denominators and preserve all V3 evidence on rollback? [AR-2 acceptance contract]

- Result: **PASS** — 12/12 requirements-quality checks. AR-2 expands only; AR-3 retains merge, backfill, quarantine, and authoritative cutover.
