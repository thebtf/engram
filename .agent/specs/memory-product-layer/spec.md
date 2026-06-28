---
feature_id: ENG-MPL-1
slug: memory-product-layer
title: Agent Knowledge and Experience Layer
status: ACTIVE
created: 2026-06-27
modified: 2026-06-28
parent: ENG-PMQ-1
children: []
supersedes: ~
superseded_by: ~
split_from: ~
merges: []
aliases: [memory-product-layer, memory-governance-layer, principal-memory-product]
open_crs: [CR-001-initial-scope]
source_prd: prd.md
---

# Feature: Agent Knowledge and Experience Layer

**Slug:** memory-product-layer  
**Created:** 2026-06-27  
**Status:** Active  
**Author:** AI Agent

> **Provenance:** Specified from `.agent/specs/memory-product-layer/prd.md`, `.agent/specs/memory-product-layer/milestone-map.md`, `outputs/agent-memory-design.md`, `.agent/specs/principal-memory-query-domain-registry/architecture.md`, `pkg/cognitive/interfaces.go`, `internal/mcp/tools_brief.go`, and current operator-console copy surfaces.  
> **Confidence:** VERIFIED for current-code/current-artifact claims read this session; INFERRED only for some later milestone shape choices.

## Overview

Agent Knowledge and Experience Layer turns Engram from a principal-aware memory substrate into a full system for native resume, bounded memory retrieval, contextualized experience reuse, archive resurfacing, safe forgetting, and operator-light governance. The feature is broader than memory moderation alone: it separates state, memory, and experience, then applies applicability and archive rules so past knowledge helps when it should and stays out when it should not.

## Context

Current Engram already has useful substrate: principal-aware memory ownership, domain registry, retrieval/injection scoring seeds, adaptive brief helper, auditable memory mutations, and a tiny filesystem-based state oracle. But the actual product loop is still split. Resume still leans on filesystem truth, principal briefs are not yet first-class, experience is not modeled distinctly from memory, archive retrieval is not trigger-gated, and the operator UI still speaks in row-centric “sort this record” language rather than bounded packets and exception surfaces.

> **Evidence anchor (FM-10 guard):** “оператор не должен этим заниматься” — operator must not manually sort memory sludge. (user directive in session)  
> **Evidence anchor (FM-10 guard):** “ввести понятие ‘опыт’ ... есть ‘воспоминание’ ... а есть ‘опыт’” — memory and experience must be separate first-class concepts. (user directive in session)

## User Outcome Coverage

| User evidence | Covered by FR/US | Acceptance proof | Surface / workflow |
| --- | --- | --- | --- |
| Operator needs control surface, not manual memory sorting | FR-3, FR-6, FR-8, US3, US5 | bounded moderation queue + packet flows replace row-centric adjudication on touched surfaces | operator console governance flows |
| Handoff/session-state/contract/task transfer should move from filesystem into Engram | FR-1, FR-2, US1 | native state packet is primary resume path; filesystem becomes fallback/export | state plane / resume workflow |
| Need first-class “experience”, not only fact memory | FR-4, FR-5, US2 | experience retrieval can answer what happened, why it changed, and where lesson applies | experience retrieval workflow |
| Similar sessions can poison each other without context guard | FR-5, FR-7, US2 | applicability / anti-applicability gate blocks bad reuse | experience + retrieval flow |
| Archive matters more than naive deletion | FR-6, FR-7, US4 | trigger-based archive resurfacing and archive-first forgetting path are real | archive / forgetting workflow |

## Functional Requirements

### FR-1: Native State Plane
The system must provide an Engram-native state plane for session, goal, task, and project handoff so resume reads native state first and filesystem state only as fallback/export.

### FR-2: Deterministic Resume Protocol
The native state plane must expose a deterministic resume packet containing at least freshness marker, drift/conflict flags, exact next action, and exact next verification step.

### FR-3: Principal Knowledge Visibility
The system must let an authorized operator inspect principal/domain/project-scoped knowledge surfaces without tag archaeology, including honest attribution and live/gated/error states.

### FR-4: Review Loop as Exception Surface
The system must let the operator improve memory quality through bounded queues and decision packets built atop existing candidate/audit/rollback seams where those seams already exist, rather than through per-row manual sorting as the default operating mode.

### FR-5: Applicability and Anti-Applicability
Experience-derived lessons must carry applicability and anti-applicability fields before they influence retrieval or automation, and the system must block or downgrade reuse when anti-applicability matches strongly.

### FR-6: Archive-First Historical Retrieval
The system must preserve archived memory/experience outside the hot path and allow bounded historical resurfacing only for named trigger classes.

### FR-7: Distinct Retrieval Contracts
The system must expose different retrieval behavior for hot memory versus experience/history so current-use facts and causal/historical lessons do not collapse into one ranking path.

### FR-8: Experience Contract
The system must provide a first-class experience retrieval contract separate from atomic memory, even if V1 storage begins as a projection/materialization over existing evidence rather than a dedicated `ExperienceRecord` table family.

### FR-9: Safe Forgetting Taxonomy
The system must distinguish suppress, expire, archive, consolidate, and destroy as separate operations with explicit semantics, policy boundaries, and audit output.

### FR-10: Structural-Loss Guard
Consolidation and destructive memory operations must block or escalate when unique meaning, provenance, or scope would be lost.

### FR-11: Designer Contract Gate
Any implied operator-facing UI/UX surface in this feature must be implemented from a designer-owned contract reviewed by PM. Missing contract is a blocker for UI delivery.

A **touched operator-facing surface** means any operator-visible page, panel, modal, card, queue view, filter bar, detail view, action control, or workflow block whose behavior, copy, state model, or server wiring changes in the current milestone/CR.

### FR-12: UI Wiring Map
For each touched operator-facing surface, the contract must define control/UX blocks, intended usage flow, API/backend bindings, and required honesty/loading/empty/error/gated states so developer implementation does not invent UI behavior ad hoc.

### FR-13: Scenario-Proven Usability Contract
Each designer-owned UI contract must include operator usage scenarios with meaningful branch coverage: happy path, empty state, validation failure, gated path, risky confirmation, rollback, and recovery. PM must pressure-test these scenarios from the operator seat before developer implementation starts.

PM approval is PASS only when the contract includes all required scenarios, every branch ends in an operable or honest blocked state, and the backend wiring map covers every visible control on the touched surface.

### FR-14: Honest Product States
Every touched surface in this feature must classify unsupported behavior as `mustbuild`, gated behavior as gated, and live behavior as live; no operable-looking control may ship without a real backing seam.

### FR-15: Releaseable Increment Boundaries
The feature must be decomposed into independently shippable increments aligned to the milestone map so each milestone ends in a functional operator-visible improvement.

## Non-Functional Requirements

### NFR-1: Privacy Safety
Principal-private memory and state must remain fail-closed. Cross-principal visibility or widening requires explicit authorized paths and audit.

### NFR-2: Resume Cheapness
Native resume must avoid broad file archaeology on the happy path and must fetch the minimum state needed to continue work correctly.

Target gate for V1 planning: native resume returns one bounded packet without fallback archaeology in the normal path, and archive-triggered retrieval remains capped to at most 10 resurfaced items per request.

### NFR-3: Auditability
Every state mutation, risky moderation action, archive resurfacing, forgetting action, and consolidation decision in this feature must leave structured audit evidence.

### NFR-4: Honest Telemetry
Quality/retrieval/noise metrics must reflect actual available telemetry. Sparse signals stay explicitly sparse.

### NFR-5: Hot-Path Discipline
Archive/experience search must not run on every ordinary request. Trigger-gated historical retrieval is required to keep normal paths cheap and relevant.

### NFR-6: Design Contract Preservation
Touched operator-console surfaces must preserve honesty, i18n, and parity discipline while migrating away from row-centric operator language.

## User Stories

### US1: Resume Work Cheaply and Correctly (P1)
**As an** agent principal, **I want** resume to read one native state packet first, **so that** work can continue without expensive filesystem archaeology.

**Acceptance Criteria:**
- [ ] Resume reads native state before filesystem fallback.
- [ ] Resume packet includes freshness, drift/conflict, next action, and next verification.
- [ ] Filesystem state remains explicit fallback/export, not silent primary truth.

### US2: Review Memory Quality Through Packets (P1)
**As an** operator, **I want** low-value or risky memory decisions to arrive through bounded queues and packets, **so that** I improve memory quality without row-by-row janitor work.

**Acceptance Criteria:**
- [ ] Touched governance flows reuse or extend existing candidate/audit/rollback seams where possible.
- [ ] Review surface centers on bounded packets/queues, not per-row “keep in prompts” language.
- [ ] Safe low-risk actions auto-resolve; risky cases escalate.

### US3: Retrieve Historical Experience Safely (P1)
**As an** agent or operator, **I want** historical experience retrieval distinct from hot memory, **so that** I can understand what happened before, why it changed, and whether that lesson applies here.

**Acceptance Criteria:**
- [ ] Experience retrieval is separate from ordinary hot-memory retrieval.
- [ ] Returned experience includes lesson + applicability + anti-applicability information.
- [ ] Strong anti-applicability blocks silent auto-reuse.
- [ ] V1 may use projection/materialization if dedicated storage is not yet justified.

### US4: Inspect Principal Knowledge Without Row Archaeology (P1)
**As an** operator, **I want** to inspect what a principal knows/carries, **so that** I can understand current agent context without manual tag hacks.

**Acceptance Criteria:**
- [ ] Principal/domain/project-scoped views are bounded and attributed.
- [ ] Principal-scoped brief or equivalent bounded summary is available.
- [ ] Private visibility remains fail-closed.

### US5: Preserve History Without Polluting Hot Context (P1)
**As an** operator, **I want** archive-first behavior with bounded resurfacing, **so that** valuable history survives but does not flood normal retrieval.

**Acceptance Criteria:**
- [ ] Archive/cold tier exists as a distinct path.
- [ ] Historical resurfacing is trigger-based and logged.
- [ ] Ordinary hot-path requests do not search archive by default.

### US6: Govern Memory Through Packets, Not Sludge (P1)
**As an** operator, **I want** bounded decision packets and exception queues, **so that** I improve memory quality without acting as a row-by-row janitor.

**Acceptance Criteria:**
- [ ] Touched governance UI centers on packets/queues, not per-row copy like “keep this record in prompts”.
- [ ] Risky or destructive cases escalate; safe low-value handling auto-resolves.
- [ ] Audit evidence exists for each operator-reviewed decision.

### US7: Use a Designed Operator Surface, Not a Guessed One (P1)
**As an** operator, **I want** developer-built UI to follow a designer contract reviewed by PM, **so that** server capability turns into a usable, coherent surface instead of ad hoc controls.

**Acceptance Criteria:**
- [ ] Every touched operator-facing surface has a designer-owned contract before implementation.
- [ ] Contract names controls, usage flow, backend bindings, and expected state behavior.
- [ ] Contract includes scenario branches for happy path, empty state, gated path, risky confirmation, rollback, and recovery.
- [ ] Missing contract blocks UI implementation.
## Edge Cases

- Native state exists but is stale relative to filesystem fallback.
- Resume packet points to a task whose underlying contract was superseded.
- Principal has almost no hot memory but rich archived experience.
- Experience looks semantically similar but anti-applicability should block reuse.
- Archive trigger fires repeatedly on the wrong class of request.
- Forgetting candidate overlaps with ongoing moderation/audit history.
- Consolidation candidate merges similar rows that actually encode distinct constraints.
- Touched UI migrates to packet semantics while server seam for one action still does not exist.
- Designer contract is missing, rejected, or incomplete after backend seam exists; UI delivery remains blocked while backend-only increment may still ship.

## Out of Scope

- Full SaaS multi-tenant admin plane.
- Rebuilding `ENG-PIM-1` or `ENG-PMQ-1` substrate from scratch.
- Always-on archive search for every prompt.
- Learned applicability model in V1.
- Broad graphification of all memories.
- Whole-console IA redesign beyond touched knowledge/governance surfaces.

## Dependencies

- `ENG-PIM-1` principal identity and ownership substrate.
- `ENG-PMQ-1` principal query/domain registry substrate.
- Existing retrieval/injection/feedback/decay mechanisms as seeds, not finished product.
- Designer-owned contract artifacts under `.agent/specs/memory-product-layer/design-contracts/` for every implied operator-facing UI/UX slice.
- Future architecture decision for dedicated state plane storage boundary.
- Future architecture decision for `ExperienceRecord` minimum schema.

## Success Criteria

- [ ] Native state plane is specified as primary handoff path.
- [ ] Deterministic resume packet is part of the feature contract.
- [ ] Principal knowledge visibility lands before high-risk forgetting/consolidation.
- [ ] Existing candidate/audit/rollback seams are reused or explicitly superseded with justification.
- [ ] Review loop is explicitly exception-only and packet-centric.
- [ ] Experience contract is separate from atomic memory even if V1 storage is not yet a dedicated table family.
- [ ] Applicability and anti-applicability are first-class requirements.
- [ ] Archive retrieval is trigger-gated and bounded.
- [ ] Forgetting taxonomy is explicit and auditable.
- [ ] Touched surfaces migrate away from row-centric operator semantics.
- [ ] Spec is aligned with PRD and ready for architecture.

## Open Questions

- [NEEDS CLARIFICATION] What minimum fields make an `ExperienceRecord` worth persistence in V1?
- [NEEDS CLARIFICATION] Should the native state plane live in a dedicated store/table family or inside the current memory store with hard type boundaries?
- [NEEDS CLARIFICATION] Which forgetting modes may be fully automatic by default, and which must escalate?
- [NEEDS CLARIFICATION] Which exact trigger classes may invoke archive retrieval automatically?
- [NEEDS CLARIFICATION] What is the minimum applicability envelope that blocks bad reuse in V1 without overfitting?

## Strangler Fig

No separate legacy product is being replaced wholesale. This feature evolves the existing Engram control-plane/memory substrate in place. The migration seam is internal and evolutionary: filesystem-first resume, row-centric memory governance, and helper-level brief behavior are gradually demoted behind native state, packet-centric governance, and first-class experience flows.
