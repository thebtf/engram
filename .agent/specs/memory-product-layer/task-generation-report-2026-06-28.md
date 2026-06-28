# Task Generation Report — Agent Knowledge and Experience Layer

**Date:** 2026-06-28  
**Feature:** `memory-product-layer`  
**Mode:** STALE — superseded by CR-scoped report under `changes/CR-001-initial-scope/`  
**Output path:** `.agent/specs/memory-product-layer/tasks.md`  
**Status:** superseded after CR folder repair; do not use as execution source.

## Source artifacts read

- `.agent/specs/memory-product-layer/spec.md`
- `.agent/specs/memory-product-layer/plan.md`
- `.agent/specs/memory-product-layer/completeness-report.md`
- `.agent/specs/memory-product-layer/checklists/general.md`

## Generation summary

- Task groups: 6
- Executable tasks: 19
- GATE tasks: 6
- Total tasks: 25
- Priority basis: P1 user stories first, then later-risk slices
- MVP scope: Story Groups 1-3

## Parallelism provenance

- Inherited from plan/task reasoning, then constrained to explicit provenance lines in `tasks.md`.
- Parallel design work allowed only where file ownership is disjoint.
- UI wiring tasks remain blocked behind designer-contract tasks.

## Key decomposition choices

1. State plane first — highest operator/agent value, strongest existing pain proof.
2. Principal explorer/briefs second — extends PMQ + `get_memory_brief`, not a rebuild.
3. Review loop before experience storage commitment — reuses existing candidate/snapshot/audit seams.
4. Experience stays contract-first; dedicated tables deferred until proof.
5. UI/UX tasks include hard blocker on approved designer contract + scenario-proof review.

## Validation checklist result

- Every story group has outcome, independent test, checkpoint.
- Every executable story group ends with a GATE task.
- Every code-producing task has TDD / Test owner / Source evidence.
- Every implied UI slice carries designer-contract blocker semantics.
- Dependency order follows state -> visibility -> review loop -> experience -> forgetting -> temporal truth.

## Anti-stall policy

- Two varied fix attempts per defect before debug/investigate routing.
- Backend work may proceed without UI only when designer contract is absent and the story group explicitly allows backend-only increment.
- No silent governance rebuild; existing candidate/snapshot/audit seams are the default base.
