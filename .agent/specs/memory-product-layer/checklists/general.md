# General Requirements Quality Checklist — Agent Knowledge and Experience Layer

**Purpose:** Unit tests for requirements writing — validates spec quality, not implementation.  
**Created:** 2026-06-28  
**Spec:** `.agent/specs/memory-product-layer/spec.md`  
**Plan:** `.agent/specs/memory-product-layer/plan.md`  
**Audience:** Reviewer  
**Depth:** Comprehensive

## Requirement Completeness

- [x] CHK001 - Are native resume requirements defined for session, goal, task, and project handoff rather than only naming “state” abstractly? [Completeness, Spec §FR-1 §FR-2] — PASS: `FR-1` and `FR-2` define native state plane + deterministic resume packet.
- [x] CHK002 - Are principal knowledge visibility requirements defined for both scoped query and bounded summary behavior? [Completeness, Spec §FR-3 §US4] — PASS: spec covers principal/domain/project views plus principal-scoped brief behavior.
- [x] CHK003 - Are review-loop requirements defined as bounded packet/queue workflows rather than row-by-row manual triage? [Completeness, Spec §FR-4 §US2 §US6] — PASS: packet/queue semantics are explicit.
- [x] CHK004 - Are experience-specific requirements defined separately from hot-memory requirements? [Completeness, Spec §FR-5 §FR-7 §FR-8] — PASS: applicability, distinct retrieval contract, and first-class experience contract are all named.
- [x] CHK005 - Are UI/UX contract requirements defined when operator-facing surfaces are implied? [Completeness, Spec §FR-11 §FR-12 §FR-13 §US6] — PASS: designer gate, wiring map, and scenario-proof are explicit.
- [x] CHK006 - Does the spec define what counts as “touched operator-facing surface” for triggering the designer-contract blocker? [Completeness, Spec §FR-11] — PASS: `FR-11` now enumerates page/panel/modal/card/queue/filter/detail/action/workflow blocks whose behavior, copy, state model, or wiring changes.

## Requirement Clarity

- [x] CHK007 - Is “deterministic resume packet” clarified with concrete required fields rather than vague resumability language? [Clarity, Spec §FR-2] — PASS: freshness marker, drift/conflict flags, next action, next verification are named.
- [x] CHK008 - Is “exception surface” clarified well enough to distinguish it from raw moderation dumps? [Clarity, Spec §FR-4 §US2 §US6] — PASS: bounded queues/packets, risky escalation, and no row-centric default are explicit.
- [ ] CHK009 - Is “first-class experience retrieval contract” clarified enough to separate mandatory behavior from optional storage shape? [Clarity, Ambiguity, Spec §FR-8] — OPEN: contract is clear, but the line between required retrieval behavior and acceptable V1 storage shortcuts still invites multiple readings.
- [x] CHK010 - Is “designer-owned contract” clarified with specific required artifacts rather than just a role name? [Clarity, Spec §FR-11 §FR-12 §FR-13] — PASS: contract, wiring map, and branch scenarios are all spelled out.

## Requirement Consistency

- [x] CHK011 - Are user-story IDs unique and internally consistent across the spec? [Consistency, Spec §US1-§US7] — PASS: story IDs are now unique through `US7`.
- [x] CHK012 - Are milestone ordering expectations in spec and plan consistent after challenge-pass revisions? [Consistency, Spec §Success Criteria, Plan §Phases] — PASS: plan now puts state first, briefs second, review loop before experience storage commitment.
- [x] CHK013 - Are archive-trigger and hot-path discipline requirements consistent between NFRs and user stories? [Consistency, Spec §NFR-5 §US5] — PASS: trigger-gated archive path aligns across both sections.

## Acceptance Criteria Quality

- [x] CHK014 - Can the state-plane acceptance criteria be objectively checked from behavior or artifacts? [Measurability, Spec §US1] — PASS: native-first read, packet fields, filesystem fallback are observable.
- [x] CHK015 - Can the review-loop acceptance criteria be objectively checked from the spec as written? [Measurability, Spec §US2 §US6] — PASS: packet-centric UI, escalation, and audit evidence are testable.
- [x] CHK016 - Can the designer-contract blocker be objectively checked without PM interpretation drift? [Measurability, Spec §FR-13 §US7] — PASS: PM approval is now PASS only when required scenarios, honest/operable branch endings, and full backend wiring coverage exist.
- [x] CHK017 - Can archive behavior be objectively checked without vague “history matters” wording? [Measurability, Spec §US5] — PASS: distinct path, trigger logging, and no default hot-path archive search are measurable.

## Scenario Coverage

- [x] CHK018 - Are ordinary operator scenarios covered for resume, principal inspection, review queue, and archive resurfacing? [Coverage, Spec §US1-§US6] — PASS: core scenarios are all represented in user stories.
- [x] CHK019 - Are negative/branch scenarios required for implied UI via designer-contract rules? [Coverage, Spec §FR-13 §US6] — PASS: happy path, empty, gated, risky confirmation, rollback, and recovery are named.
- [x] CHK020 - Does the spec define an operator scenario for “server seam absent but UI surface implied”? [Coverage, Spec §FR-14 §Edge Cases] — PASS: honest `mustbuild/gated` behavior is required and the edge-case list now covers touched UI where server seam is still absent.

## Edge Case Coverage

- [x] CHK021 - Does the spec address stale native state versus filesystem fallback? [Edge Cases, Spec §Edge Cases] — PASS: stale native state versus fallback is explicitly listed.
- [x] CHK022 - Does the spec address applicability mismatch and archive-trigger misfire? [Edge Cases, Spec §Edge Cases] — PASS: both are explicit edge cases.
- [x] CHK023 - Does the spec address consolidation of similar-but-distinct memories? [Edge Cases, Spec §Edge Cases] — PASS: structural-loss-like merge ambiguity is listed.
- [x] CHK024 - Does the spec define edge-case behavior for missing or rejected designer contracts after backend work already exists? [Edge Cases, Spec §Edge Cases] — PASS: edge cases now state that UI delivery remains blocked while backend-only increment may still ship.

## Non-Functional Requirements

- [x] CHK025 - Are privacy, auditability, and hot-path discipline specified as NFRs? [NFRs, Spec §NFR-1-§NFR-5] — PASS: privacy, auditability, telemetry honesty, and hot-path discipline all exist.
- [x] CHK026 - Are design-contract preservation and UI honesty captured as NFRs rather than left as prose only? [NFRs, Spec §NFR-6] — PASS: NFR explicitly protects honesty/i18n/parity discipline.
- [x] CHK027 - Are performance targets for “cheap resume” or bounded archive retrieval quantified enough for implementation readiness? [NFRs, Spec §NFR-2] — PASS: V1 target now requires one bounded native packet on the normal path and caps archive resurfacing to at most 10 items per request.

## Dependencies & Assumptions

- [x] CHK028 - Are dependencies on `ENG-PIM-1`, `ENG-PMQ-1`, and existing ranking seeds documented? [Dependencies, Spec §Dependencies] — PASS: substrate dependencies are listed explicitly.
- [x] CHK029 - Does the spec state that dedicated experience storage is still conditional rather than assumed complete? [Dependencies, Spec §FR-8 §Open Questions] — PASS: V1 storage may begin as projection/materialization and dedicated storage remains open.
- [x] CHK030 - Does the spec explicitly depend on designer output as a pipeline artifact, not just a human role? [Dependencies, Spec §Dependencies] — PASS: `Dependencies` now names `.agent/specs/memory-product-layer/design-contracts/` artifacts explicitly.

## Ambiguities & Conflicts

- [x] CHK031 - Is the distinction between state, memory, and experience explicit enough to avoid collapse into one generic store? [Ambiguities, Spec §Overview §FR-1 §FR-8] — PASS: conceptual separation is explicit.
- [ ] CHK032 - Is the boundary between review loop and forgetting/consolidation clear enough to prevent duplicate tasking later? [Ambiguities, Spec §FR-4 §FR-9 §FR-10] — OPEN: review-loop and forgetting taxonomy are distinct, but handoff boundary between “queue decision” and “mutation implementation” could still split two ways.
- [x] CHK033 - Is the naming/numbering stable enough for downstream task generation? [Ambiguities, Spec §US1-§US7 §Edge Cases] — PASS: duplicate `US6` is removed and `## Edge Cases` heading is restored.
