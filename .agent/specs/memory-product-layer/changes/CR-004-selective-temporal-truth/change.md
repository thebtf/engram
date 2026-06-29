---
feature_id: ENG-MPL-1
created_in_session: unknown
cr_id: CR-004-selective-temporal-truth
status: OPEN
created: 2026-06-29
owner: Developer/PM
---

# CR-004: Selective Temporal Truth

## Delta

Create the next executable implementation slice for Agent Knowledge and Experience Layer:
- narrow temporal-truth contract for selected high-value evolving facts,
- validity/invalidation history,
- provenance-first retrieval for `true now vs then`,
- explicit proof that temporal scope stays intentionally narrow,
- no broad graphification or operator-console redesign.

## Rationale

CR-003 closed explicit forgetting/consolidation semantics and structural-loss guardrails. The remaining roadmap slice is selective temporal truth: answer what is true now, what was true then, and why it changed, but only for selected high-value facts. This must stay narrow to avoid graph-first overbuild.

## Acceptance

- [ ] `changes/CR-004-selective-temporal-truth/tasks.md` exists and is the active task list for this CR.
- [ ] Temporal truth contract is explicit and bounded to selected high-value evolving facts.
- [ ] Validity/invalidation history is queryable with provenance.
- [ ] Retrieval can answer `true now vs then` for the selected scope.
- [ ] The implementation proves temporal scope stayed narrow and did not broaden into graphification.
- [ ] Broad graph projection and broad operator-console redesign remain out of CR-004 scope.
