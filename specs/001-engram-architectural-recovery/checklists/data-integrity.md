# Data Integrity Requirements Checklist: Engram Architectural Recovery

**Purpose**: Review whether durable evidence, knowledge, identity, exposure, outcome, and
migration requirements preserve meaning and enforce invariant boundaries.
**Created**: 2026-08-22
**Feature**: `../data-model.md`, `../contracts/`, `../spec.md`
**Review Ownership**: Requirements-quality reviewer. `[x]` means requirement quality only.

## Entity and Invariant Completeness

- [x] CHK001 Are identity, evidence, job, proposal, belief, revision, evidence-link, exposure,
      outcome, health, receipt, and quarantine entities defined with owner and lifecycle?
      [Completeness, Data Model]
- [x] CHK002 Are uniqueness and idempotency keys defined for accepted evidence, processing jobs,
      reconciliation decisions, exposure delivery, outcomes, and merge operations? [Integrity,
      FR-008 through FR-018]
- [x] CHK003 Are one-active-belief, append-only revision, immutable evidence, and no-projection-
      authority invariants stated without conflicting exceptions? [Consistency, Constitution IV-VI]

## State and Failure Coverage

- [x] CHK004 Are allowed processing-job transitions, terminality, lease ownership, stale-owner,
      retry exhaustion, and quarantine semantics explicit? [Clarity, Processing Job Contract]
- [x] CHK005 Are reconciliation decisions closed, mutually distinguishable, and tied to durable
      effects or explicit no-change outcomes? [Clarity, Knowledge Contract]
- [x] CHK006 Are outcome success, partial, failure, abandoned, unknown, certainty, timeout, and
      conflicting-repeat semantics separated? [Coverage, Outcome Contract]

## Migration Integrity

- [x] CHK007 Are row-count, provenance, privacy, revision, relation, and payload preservation
      requirements specified for every accepted merge/backfill? [Completeness, FR-007/FR-025]
- [x] CHK008 Are schema-uncertain JSON/cache families explicitly quarantined rather than assumed
      safe to rewrite? [Safety, Data Inventory]
- [x] CHK009 Are secret and credential equality/fingerprint restrictions explicit for every
      integrity comparison? [Security, Constitution Architecture Constraints]

## Notes

- [x] CHK010 Can every listed data-integrity success criterion be demonstrated by a fixture,
      receipt, or durable record rather than an in-process counter? [Measurability, SC-003..SC-013]


## AR-0 Review Note

- Result: FAIL
- Checked: 9/10.
- Unchecked: CHK007. `data-model.md` §Project Merge Audit, `plan.md` §Schema and Migration Sequence, `quickstart.md` AR-3, and `contracts/migration-receipt.schema.json` require counts, fingerprints, provenance, privacy, revisions, and referential-integrity/readback evidence, but none states a per-family payload/content preservation or equivalence requirement for accepted merge/backfill rows. Structural fingerprints and row counts do not establish payload preservation, leaving serialized/content-bearing families without an explicit meaning-preservation assertion.

## R2 Review Note

- Result: PASS
- Checked: 10/10.
- Remaining unmet IDs: none.
- CHK007 is now supported. `data-model.md` §Migration Receipt requires a semantic payload-preservation or quarantine result for every migration object family; `plan.md` §Schema and Migration Sequence requires that per-family verification; `quickstart.md` AR-3 requires every table/payload/cache/job family to be counted or quarantined; and `contracts/migration-receipt.schema.json` requires a per-family `payload_preservation` entry whose status is `preserved`, `quarantined`, or `not_applicable` with an evidence reference.

## R3 Final Review Note

- Result: PASS
- Checked: 10/10.
- Regressions: none.
- Final recheck: The final data model and contracts retain immutable evidence, durable job state, closed reconciliation decisions, one-active-belief and append-only revision invariants, explicit outcome conflicts, scoped identity/privacy, credential-safe comparison rules, and quarantine for uncertain JSON/cache families. Migration receipts now require per-family semantic payload preservation or quarantine in addition to counts, provenance, privacy, revision, rollback, and zero-denominator evidence. No data-integrity checklist criterion regressed.


## R4 Review Notes

- Review iteration: AR-0 R4 requirements-quality regression recheck after R2 task/inventory remediation, 2026-08-22.
- Result: **PASS** — 10/10 checked; no regressions identified.
- Rechecked per-family semantic payload preservation, quarantine, idempotency, provenance/privacy/revision invariants, durable receipt metrics, and the T074/T075/T076 evidence dependencies; no integrity boundary was weakened.
- Exact regression IDs: none.
- Per-file checked/total: `data-integrity.md` 10/10.
