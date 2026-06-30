# Forgetting Audit / Export Proof

ENG-MPL-1 CR-003 closes only if high-risk forgetting/consolidation paths are
auditable, reviewable, and bounded. This proof records what evidence exists,
what remains blocked, and why the CR is safe without expanding into temporal
truth.

## Evidence That Exists

| Evidence | Path | Proof |
| --- | --- | --- |
| Taxonomy contract | `.agent/specs/memory-product-layer/contracts/forgetting-taxonomy.md` | Names `suppress`, `expire`, `archive`, `consolidate`, and `destroy` with semantics, policy boundaries, audit surface, structural-loss rule, and review-packet rule. |
| T001/T002 RED/GREEN/Prove-It | `.agent/specs/memory-product-layer/evidence/T001-T002.tdd.json` | Contract tests failed before public taxonomy/classifier symbols existed, passed after implementation, and failed again when `destroy` collapsed to `delete` or audit requirement was removed. |
| G001 taxonomy gate | `.agent/specs/memory-product-layer/evidence/phase-5-taxonomy-gate.json` | `go test ./...` passed; gate records taxonomy explicit/auditable/not disguised delete. |
| T003/T004 RED/GREEN/Prove-It | `.agent/specs/memory-product-layer/evidence/T003-T004.tdd.json` | Structural-loss and packet tests failed before fields existed, passed after implementation, and failed when the guard/cap were broken. |
| G002 risky-path gate | `.agent/specs/memory-product-layer/evidence/phase-5-risk-gate.json` | `go test ./...` passed; gate records risky paths escalate with rationale and temporal-truth work did not start. |

## Runtime Audit Surface

The CR-003 classifier returns `ForgettingDecision` without mutating storage.
Every decision carries:

- `Audit.Required=true`
- `Audit.SnapshotStore="bulk_op_snapshots"`
- `Audit.AuditStore="audit_log"`
- `Audit.Evidence=[...]`
- `DataDestructionByDefault=false`

Risky or destructive decisions also emit a bounded `forgetting_review` packet
with:

- operation and state (`review_required` or `blocked`)
- rationale, including structural-loss rationale when applicable
- bounded memory ids and capped evidence handles
- snapshot policy using `bulk_op_snapshots`
- audit policy using `audit_log`
- allowed actions the operator may review

## Export Surface

CR-003 does not introduce an execution/export backend for hard delete. Export
evidence for this CR is the durable proof bundle:

- taxonomy contract
- audit/export proof contract
- RED/GREEN/Prove-It evidence JSON
- G001/G002/G003 gate JSON
- raw `go test ./...` sidecars

`ForgettingAuditSurface.ExportPath` remains part of the public contract for a
future materialized export path once an execution backend exists. In this CR it
is intentionally not populated by classification because classification is not
execution.

## What Remains Blocked

- Hard-delete execution remains blocked by default. The classifier may classify
  `destroy`, but it returns `blocked` and a review packet; it does not delete.
- Silent consolidation with structural loss remains blocked/escalated. Loss of
  unique meaning, provenance, or scope forces rationale-bearing review.
- Temporal truth remains out of scope. CR-003 does not add validity windows,
  invalidation graphs, learned applicability, or fact evolution.
- Broad operator-console redesign remains out of scope until a designer-owned
  contract exists.

## Why This Is Safe Enough For CR-003

The dangerous part of forgetting is not allowed to execute silently. Safe
low-risk actions (`suppress`, `expire`, `archive`) can classify as
auto-resolvable with audit. Risky consolidation emits a bounded review packet.
Destructive removal is blocked by default. All of this is covered by tests,
Prove-It mutations, and full-suite gates, while execution and temporal truth
stay explicitly deferred.
