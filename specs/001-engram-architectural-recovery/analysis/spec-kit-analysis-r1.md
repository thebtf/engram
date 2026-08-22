# Specification Analysis Report — R1

**Feature**: `001-engram-architectural-recovery`  
**Method**: Read-only Spec Kit cross-artifact analysis plus independent execution/inventory lens.  
**Verdict**: **FAIL** — remediation required before a clean final analysis.

## Findings

| ID | Category | Severity | Location(s) | Summary | Remediation owner |
|---|---|---|---|---|---|
| A1 | Task ordering | CRITICAL | `tasks.md` T035–T036, T045 | AR-3 typed-key backfill requires `ProcessingJob` before AR-4 creates its schema. | Plan/tasks/data-model owner |
| A2 | Traceability | HIGH | `analysis/fr-sc-task-traceability.md` FR-019 | T029 cites FR-019 but is absent from the FR-019 row. | Traceability owner |
| A3 | Traceability | HIGH | `analysis/fr-sc-task-traceability.md` SC-009/SC-010 | T076 cites SC-008 through SC-011 but is absent from SC-009 and SC-010 rows. | Traceability owner |
| A4 | Traceability | HIGH | `analysis/fr-sc-task-traceability.md` governance rows | T086 is not mapped to FR/SC or explicit governance despite the matrix claiming zero unmapped tasks. | Traceability owner |
| A5 | Analysis procedure | HIGH | `analysis/traceability-contract.md`, T091 | The contract requires analyzer file writing while `/speckit.analyze` is read-only. | Analysis/handoff owner |
| A6 | Acceptance metric | HIGH | `spec.md` SC-005; Processing Job contract | Evidence processing service objective lacks declaration owner, configuration surface, activation receipt, and release gate. | AR-4 operability owner |
| A7 | Acceptance metric | HIGH | `spec.md` SC-010; Retrieval contract | Render item/token budget lacks declaration contract, owner, activation, and rollback rule. | AR-6 retrieval owner |
| A8 | Inventory coverage | HIGH | current-source/package-route/flag/claim inventories; AR-7 T079/T082 | Vue `ui/` and deployable Nuxt `apps/operator-console/` are distinct roots but not reconciled in one surface disposition. | AR-1 inventory / AR-7 contraction owner |
| A9 | Release ordering | HIGH | plan continuity map; source/package inventories; T039/T081 | Continuity-slot migration/observation ownership conflicts between AR-3 and AR-6 and lacks an explicit receipt task. | AR-3 state/identity owner |
| A10 | Inventory closure | MEDIUM | `plan.md` Removal Ledger; evidence JSON | Plan says every inventory entry has closure fields while evidence applies them only to temporary/quarantine entries. | Plan/inventory owner |
| A11 | Identity coverage | MEDIUM | FR-006, T004/T032, data inventory | Import/export field inventory is not explicitly owned by a task or evidence family. | AR-1/AR-3 identity owner |
| A12 | Contraction scope | HIGH | AR-7 T079, T084, plan Removal Ledger | Flag-removal task does not own all coupled defaults/configuration, routes/tools, `ui/`, package seams, and release assertions. | AR-7 contraction owner |

## Coverage Summary

| Requirement Key | Has Task? | Notes |
|---|---:|---|
| FR-001..FR-028 | Yes, nominally 28/28 | Exact task-matrix rows A2–A4 require correction. |
| SC-001..SC-018 | Yes, nominally 18/18 | SC-005 and SC-010 are not executable until A6/A7 are resolved. |
| Constitution principles | Yes | No direct semantic constitution conflict found. |
| Working-surface implementation | No | No task authorizes new operator working-surface design. |

## Constitution Alignment

No direct contradiction with Constitution principles was found. The task ordering, metric,
inventory, and analysis-procedure failures above nevertheless block implementation because they
would violate durable-work, evidence, removal, and migration gates if executed unchanged.

## Unmapped Tasks

- T086 is currently unmapped and must be classified as the constitution's exact-head SonarQube
  release-gate obligation.

## Metrics

- Functional requirements: 28
- Buildable success criteria: 18
- Tasks: 91
- Nominal FR coverage: 100%
- Nominal SC coverage: 100%
- Exact task matrix defects: 3
- CRITICAL findings: 1
- HIGH findings: 9
- MEDIUM findings: 2
- LOW findings: 0

## Remediation Gate

Remediate A1–A12 in their owning artifacts, rerun affected checklist reviews, regenerate the exact
FR/SC/task matrix, and then run a fresh read-only Spec Kit analysis. No implementation may start
from this R1 candidate.
