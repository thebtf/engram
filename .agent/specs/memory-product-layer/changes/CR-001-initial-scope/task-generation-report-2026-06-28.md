# Task Generation Report — Agent Knowledge and Experience Layer

**Date:** 2026-06-28  
**Feature:** `memory-product-layer`  
**Mode:** CR-scoped  
**CR:** `CR-001-initial-scope`  
**Output path:** `.agent/specs/memory-product-layer/changes/CR-001-initial-scope/tasks.md`

## Source artifacts read

- `.agent/specs/memory-product-layer/spec.md`
- `.agent/specs/memory-product-layer/plan.md`
- `.agent/specs/memory-product-layer/completeness-report.md`
- `.agent/specs/memory-product-layer/checklists/general.md`
- `.agent/specs/memory-product-layer/changes/CR-001-initial-scope/change.md`

## Generation summary

- Task groups: 3
- Executable tasks: 12
- GATE tasks: 3
- Total tasks: 15
- Priority basis: P1 user stories first, then later-risk slices
- MVP scope: Story Groups 1-2

## Parallelism provenance

- Parallel design work allowed only where file ownership is disjoint.
- UI wiring tasks stay blocked behind designer-contract tasks.
- Soft parallelism uses `PARALLEL-AFTER` only where signature handoff exists.

## Key decomposition choices

1. State plane first — highest operator/agent value, strongest pain proof.
2. Principal explorer/briefs second — build missing principal/domain explorer substrate on top of privacy visibility, extend `get_memory_brief`, and allow only minimal principal-memory UI delivery on approved design contract.
3. Review loop backend third — reuses existing candidate/snapshot/audit seams; queue UI itself stays out of CR-001.
4. Experience, forgetting, and temporal truth move to later CRs.
5. UI/UX tasks include hard blocker on approved designer contract + scenario-proof review.

## Validation checklist result

- Every story group has outcome, independent test, checkpoint.
- Every executable story group ends with a GATE task.
- Every code-producing task has TDD / Test owner / Source evidence.
- Every implied UI slice carries designer-contract blocker semantics.
- Dependency order follows state -> principal explorer/briefs -> review-loop backend substrate.

## Anti-stall policy

- Two varied fix attempts per defect before debug/investigate routing.
- Backend work may proceed without UI only when designer contract is absent and the story group explicitly allows backend-only increment.
- No silent governance rebuild; existing candidate/snapshot/audit seams are the default base.
