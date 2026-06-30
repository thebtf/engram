# Forgetting Taxonomy Contract

ENG-MPL-1 CR-003 defines forgetting as five explicit operations, not a boolean
delete path. Classification is separate from execution: a classifier may return
`destroy`, but it does not mutate storage and it must return audit/review
requirements with the decision.

## Operation Classes

| Operation | Semantics | Policy Boundary | Required Audit Surface |
| --- | --- | --- | --- |
| `suppress` | Hide low-value or noisy memory from hot retrieval/injection. | Reversible soft-hide; original memory remains reachable for audit and rollback. | Decision rationale, memory id, evidence handles, `audit_log`, and snapshot eligibility. |
| `expire` | Apply retention policy to low-value episodic traces. | Retention-governed; not a generic delete shortcut and not used for high-value semantic memory. | Retention rule, decision rationale, memory id, evidence handles, `audit_log`, and snapshot eligibility. |
| `archive` | Move cold but still useful memory outside hot retrieval. | Retains reachability through explicit archive/history/export paths. | Archive rationale, memory id, evidence handles, `audit_log`, archive/export path when materialized. |
| `consolidate` | Merge redundant items into a stronger semantic record. | Review-required when meaning, provenance, or scope might be lost; source evidence stays attributable. | Source ids, target/summary rationale, evidence handles, `bulk_op_snapshots`, and `audit_log`. |
| `destroy` | Hard-delete class for operator-approved removal. | Blocked by default; requires explicit review and audit/export proof before execution. | Operator authorization, rationale, source ids, pre-action snapshot/export proof, and `audit_log`. |

## Safety Rules

- Classification never destroys data by default.
- `consolidate` and `destroy` are risky classes unless a later guard proves no
  unique meaning, provenance, or scope would be lost.
- When unique meaning, provenance, or scope would be lost, consolidation must
  escalate to review and destructive removal must block with rationale.
- Risky or destructive decisions emit a bounded `forgetting_review` packet with
  operation, state, rationale, capped evidence, memory ids, snapshot policy, and
  audit policy. Safe low-risk suppress/expire/archive decisions remain
  auto-resolvable and do not emit review packets.
- Every decision carries an audit surface with snapshot/audit store names before
  any execution path can act on it.
- The taxonomy does not introduce temporal truth behavior; temporal validity and
  fact evolution remain out of CR-003 scope.
