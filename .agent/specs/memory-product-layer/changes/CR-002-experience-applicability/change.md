---
feature_id: ENG-MPL-1
created_in_session: unknown
cr_id: CR-002-experience-applicability
status: OPEN
created: 2026-06-29
owner: Developer/PM
---

# CR-002: Experience + Applicability Contract

## Delta

Create the next executable implementation slice for Agent Knowledge and Experience Layer:
- first-class experience retrieval contract,
- minimum applicability / anti-applicability envelope,
- bounded archive-trigger semantics,
- proof-driven decision between projection/materialization and dedicated `ExperienceRecord` storage,
- no forgetting/consolidation or temporal-truth expansion yet.

## Rationale

CR-001 closed the state plane, principal explorer/brief path, and packet-centric review-loop substrate. The next highest-value slice is historical experience retrieval with explicit applicability boundaries, because the feature contract already marks bad cross-session reuse as a critical risk and Phase 4 is the first place where that risk becomes executable without jumping into forgetting or graph work.

## Acceptance

- [ ] `changes/CR-002-experience-applicability/tasks.md` exists and is the active task list for this CR.
- [ ] Experience retrieval lands as a distinct contract from hot memory retrieval.
- [ ] Returned experience includes bounded lesson + applicability + anti-applicability semantics.
- [ ] V1 archive resurfacing stays trigger-gated and bounded.
- [ ] The implementation proves projection/materialization vs dedicated `ExperienceRecord` storage with evidence instead of guessing.
- [ ] Forgetting/consolidation and temporal truth remain out of CR-002 scope.
- [ ] Any implied operator-facing UI remains blocked unless a designer-owned contract is created for this slice.
