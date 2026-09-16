# FR/SC Traceability Contract

**Purpose**: Pre-task requirement mapping for `001-engram-architectural-recovery`. It makes the
post-task Spec Kit analysis mechanical without pretending that ungenerated task IDs already exist.

## Binding Rules

1. Every FR-001 through FR-028 and every buildable SC-001 through SC-018 MUST map to one or more
   generated task IDs in `tasks.md`.
2. Every generated task MUST cite at least one FR, SC, user story, or explicit migration/governance
   obligation. A task with no citation is invalid.
3. The read-only `/speckit.analyze` stage MUST emit the exact FR/SC-to-task matrix, unmapped-task
   list, coverage denominator, and zero CRITICAL/HIGH finding count to its orchestrator. After the
   read-only stage completes, the orchestrator MAY persist that immutable output as
   `analysis/spec-kit-analysis-rN.md` without attributing the write to the analyzer.
4. A requirement may map to a release-observation task as well as construction work; post-launch
   dogfood metrics alone do not replace buildable task coverage.
5. A no-implementation scope constraint is traceable through negative task checks: `tasks.md` MUST
   contain no operator working-surface-design task and AR-0 MUST contain no application-source task.

## Pre-Task Release and Contract Map

| Requirement set | Planned release | Primary contracts/evidence | Required task shape |
|---|---|---|---|
| FR-001..FR-007; SC-001..SC-003; SC-013 | AR-2/AR-3 | Project Identity V3, anchor/merge schemas, compatibility matrix, identity/data inventories | vectors, resolver boundary, inventory, expand/backfill, merge/rollback, sunset scan |
| FR-008..FR-011; SC-004..SC-005 | AR-4 | Session Evidence Intake, Processing Job, Capability Health, Operational Acceptance Policy | test-first durable acceptance, leases/retry, replay, provider degradation, service-objective declaration, dogfood receipt |
| FR-012..FR-014; SC-006..SC-007; SC-012 | AR-5 | Knowledge Proposal and Revision, data model | reconciliation matrix, belief/revision migration, conflict/policy review, fixture/dogfood proof |
| FR-015..FR-017; SC-008..SC-010 | AR-6 | Task Retrieval and Exposure, Capability Health, Operational Acceptance Policy | task capture, filters/ranking/render budget declaration, exposure idempotency, corpus, fallback/cutover proof |
| FR-018..FR-020; SC-011 | AR-4/AR-6 | Session Outcome Evidence, Capability Health | host outcome adapters, timeout/conflict, exposure linkage, coverage metrics/observation |
| FR-021..FR-024; SC-014..SC-017 | AR-1/AR-3/AR-7 | source/package/route/flag/data/claim inventories | inventory, adjacent compatibility, projection decision, coupled deletion and scan |
| FR-025..FR-026; SC-013, SC-018 | AR-3/AR-7 | Migration Receipt, compatibility matrix, quickstart | backup/export, fixture, receipt, rollback/restore, contraction/release verification |
| FR-027 | AR-0..AR-7 | Constitution XII, plan exclusions | negative scope scan; no new operator working-surface design task |
| FR-028; SC-018 | AR-1..AR-7 | plan release map, quickstart, pipeline receipt | per-release task group, clean-main build, installed receipt, observation boundary |

## Final Gate

The task generator must add exact IDs to this map or to the persisted analysis report. The
read-only analyzer treats an absent mapping, a task with no requirement, a task ordered before its
required migration/cutover/observation predecessor, or a working-surface implementation task as a
CRITICAL finding. The orchestrator persists its output only after the analysis is complete. This
contract is requirements authority; it does not mark implementation complete.
