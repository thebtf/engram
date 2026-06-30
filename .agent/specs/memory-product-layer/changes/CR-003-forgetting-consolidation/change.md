---
feature_id: ENG-MPL-1
created_in_session: unknown
cr_id: CR-003-forgetting-consolidation
status: OPEN
created: 2026-06-29
owner: Developer/PM
---

# CR-003: Forgetting / Consolidation

## Delta

Create the next executable implementation slice for Agent Knowledge and Experience Layer:
- explicit forgetting taxonomy implementation (`suppress`, `expire`, `archive`, `consolidate`, `destroy`),
- structural-loss guard,
- risky review packets over existing candidate/snapshot/audit seams,
- audit/export proof for destructive and high-risk operations,
- no temporal-truth expansion yet.

## Rationale

CR-002 closed the experience/applicability contract and trigger-gated historical resurfacing. The next highest-value slice is safe memory shrink/merge behavior, because the roadmap now has enough visibility and historical context to gate destructive or risky consolidation decisions honestly instead of leaving forgetting as a fuzzy future concept.

## Acceptance

- [ ] `changes/CR-003-forgetting-consolidation/tasks.md` exists and is the active task list for this CR.
- [ ] Forgetting taxonomy is explicit in code/contracts: `suppress`, `expire`, `archive`, `consolidate`, `destroy` are distinct operations.
- [ ] Structural-loss guard blocks or escalates risky merges/destructive actions.
- [ ] Risky review packets reuse existing candidate/snapshot/audit seams instead of rebuilding governance.
- [ ] Audit/export evidence exists for destructive or high-risk forgetting paths.
- [ ] Temporal truth remains out of CR-003 scope.
- [ ] Any implied operator-facing UI remains blocked unless a designer-owned contract is created for this slice.
