---
title: Agent Knowledge and Experience Layer
status: Draft
owner: operator
created: 2026-06-27
updated: 2026-06-28
milestones: [MPL-1, MPL-2, MPL-3, MPL-4, MPL-5, MPL-6]
prd_mode: platform-milestone
handoff_target: nvmd-architect
evidence_tier: evidence-backed
depends_on: [ENG-OC-1, ENG-PIM-1, ENG-PMQ-1]
---

# PRD: Agent Knowledge and Experience Layer

## 1. Summary

Engram must evolve from a memory substrate into a full agent knowledge and experience system. The target is not merely persistent memory; it is a layered architecture that separates state, episodic traces, distilled memories, contextualized experience, applicability conditions, temporal truth, and operator moderation. The operator needs control, but must not become a human garbage collector for memory rows. The system must decide what to remember, what to rank, what to forget, what to consolidate, when archived historical experience should resurface, and when a past lesson must **not** be applied in the current context.

## 2. Problem

| Field | Content |
| --- | --- |
| Target users | The Engram operator; agent-team projects using principal/domain memory; future shared control-plane/SaaS consumers. |
| Current struggle | Engram already stores and ranks memories, but the operator still lacks a coherent product workflow for usefulness/noise review, safe forgetting, consolidation, principal-scoped memory reasoning, and historical experience reuse. Handoff/session-state/goal/task transfer still depends primarily on filesystem oracles instead of native server-backed state. |
| Current workaround | Use filesystem state (`current.json`, `CONTINUITY.md`), manual memory inspection, issue/retro artifacts, and ad hoc interpretation of retrieval/injection behavior. This is expensive in tokens and fragile across long-running loops. |
| Why now | Operator-console shell is already live enough. The next value layer is memory governance and native handoff. The roadmap already names `Memory Product Layer` as the next epic, and current retrieval/ranking seeds are strong enough that the missing work is now product architecture, not proof-of-concept substrate. |
| Evidence | VERIFIED: `internal/injection/injection.go`, `internal/retrieval/scoring.go`, `internal/feedback/updater.go`, `internal/lifecycle/decay.go`, `internal/worker/dream_cycle.go`, `internal/mcp/tools_brief.go`, `pkg/cognitive/types.go`, `pkg/cognitive/interfaces.go`, `.agent/reports/retro-sessions-2026-06-24.html`, `.agent/session-state/current.json`, `.agent/specs/principal-memory-query-domain-registry/prd.md`, `.agent/goals/2026-06-23-engram-roadmap-stabilization-marathon.md`, external reference set in `outputs/agent-memory-design.provenance.md`. |

### Current UX debt (verified)

The current Memory Lab/operator-memory surface still speaks the old row-centric language. Existing copy and controls imply that the operator manually decides per record (`Operator decides`, `keep this record in prompts`, `hide as noise`) instead of consuming bounded packets and exception queues. The new epic must replace this interaction model rather than merely layering new backend capability underneath it.

## 3. Goals and Success Metrics

| Goal | Metric | Baseline | Target | Evidence source |
| --- | --- | --- | --- | --- |
| Native handoff/state plane | Session/goal/task/project state is fetched from Engram-native records before filesystem fallbacks | `current.json` is the practical oracle; `StateWriter` is design/noop/test only | Native state read/write exists and becomes the primary handoff path; filesystem is cache/export/fallback | server tests, load/save smoke, reduced recovery-token cost |
| Deterministic resume protocol | Resume chooses one cheap first-read source and one exact next action | load/recovery still depends on broad file archaeology plus fallback heuristics | resume protocol is machine-readable, freshness-marked, and conflict-aware | session load tests, oracle drift tests, recovery traces |
| Principal memory visibility | Operator can inspect memory by principal/domain with safe attribution | principal substrate exists but product workflow is partial | operator can answer “what does principal X know/carry?” without tag archaeology | API/MCP tests + browser smoke |
| Useful ranking | Retrieval/injection uses utility-aware ranking, not only similarity | Thompson + feedback seeds already exist | ranking includes applicability and contradiction-aware context where designed, with evidence of improved usefulness | retrieval metrics + reviewed samples |
| Experience layer | Engram distinguishes “memory/fact” from “experience/trajectory/lesson” and encodes where a lesson does or does not apply | no first-class experience layer | experience records synthesize time-spanning decisions, reversals, and lessons with applicability + anti-applicability conditions | experience synthesis tests, retrieval examples |
| Forgetting as a product capability | Engram can suppress, expire, archive, consolidate, or destroy by policy | suppression exists; semantic forgetting policy missing | each forgetting mode has explicit semantics, audit, and UI/operator path | API tests, moderation workflow evidence |
| Operator-light governance | operator reviews bounded queues and policy packets, not raw row sludge | manual inspection burden still high | risky merges / policy shifts go to queue; low-value memory handled automatically | moderation queue counts, review packet examples |
| Historical resurfacing | archived knowledge resurfaces only when the question actually needs history | archive today is mostly files and ad hoc retros | archive retrieval is trigger-based, bounded, and explainable | retrieval traces, archive packet samples |

## 4. Non-Goals

- Rebuilding `ENG-PIM-1` or `ENG-PMQ-1` from scratch.
- Turning every memory row into a graph entity or treating graphification as the default storage model.
- Making the operator manually triage every low-value or stale memory.
- Shipping a giant hidden backend rewrite with no operator-visible increment.
- Treating raw transcripts as the final memory product.
- Using the filesystem as the permanent primary state authority once native handoff exists.
- Automatically applying historical experience to current work without an applicability check.
- Searching archive/experience layers on every ordinary hot-path request.

## 5. Milestone Map

| Milestone | Product outcome | Scope | Success gate | Out of scope |
| --- | --- | --- | --- | --- |
| MPL-1 | Native state plane | session/goal/task/project state in Engram | load/save/recovery prove native state is primary and filesystem is fallback | memory quality UI |
| MPL-2 | Principal Memory Explorer + Briefs | inspect + summarize principal/domain memory + minimal touched-surface UX migration | operator can inspect principal memory, fetch principal-scoped brief safely, and stop depending on row-centric copy on touched surfaces | forgetting/consolidation mutations |
| MPL-3 | Usefulness / Noise Review Loop | operator reviews bounded memory-quality queue built on existing candidate/audit/rollback seams | live review actions with honest metrics and audit | high-risk forgetting |
| MPL-4 | Experience + Applicability Layer | experience contract, applicability envelopes, historical triggers | agent/operator can retrieve what happened, why it changed, and when a past lesson should or should not apply | broad forgetting policy |
| MPL-5 | Forgetting / Consolidation | policy-driven suppression/expiry/archive/consolidate/destroy | structural-loss guard + audit + release evidence | broad graphification |
| MPL-6 | Selective Temporal Truth Graph | temporal truth for high-value evolving facts | operator/agent can query what is true now vs then with provenance | full company-brain ingestion platform |

## 6. Scope

| In scope | Out of scope | Deferred |
| --- | --- | --- |
| Native Engram records for handoff/state | Filesystem-only long-term authority | Filesystem remains export/cache/fallback |
| Principal/domain-aware memory inspection | Manual raw-row archaeology as the primary path | richer visualizations after core workflow |
| Explicit forgetting taxonomy: suppress, expire, archive, consolidate, destroy | Single-mode delete-only memory lifecycle | exact threshold tuning |
| Experience synthesis from time-spanning evidence | Treating all experience as plain memories | advanced causal graph learning |
| Applicability and anti-applicability envelopes | Pure relevance-only retrieval | learned applicability model after baseline heuristics |
| Bounded operator moderation queues | Operator sorting every memory row | advanced multi-tenant policy UI |
| Archive resurfacing triggers and packets | Archive always-on in hot path | deep historical analytics UI |
| Deterministic resume protocol | Best-effort “find the likely current state” heuristics | richer cross-session visualization of state provenance |
| Migration from row-centric operator language to packet/queue-centric governance | Preserving the existing “operator decides per memory row” interaction as the future model | polish/visual redesign beyond workflow correction |

## 7. Users, Roles, and Jobs

| User / role | Job-to-be-done | Pain today | Desired outcome |
| --- | --- | --- | --- |
| Operator | Understand what a principal knows and why | Must inspect mixed memory rows plus filesystem/state artifacts | One query/brief view answers what is known, carried, and currently active |
| Operator | Improve memory quality without becoming a janitor | Noise and usefulness require too much manual interpretation | Bounded review queues and policy packets, not full dumps |
| Operator | Preserve valuable historical lessons while keeping hot path clean | Old decisions either pollute current context or disappear into files | Archive-first model with explicit resurfacing triggers |
| Agent principal | Resume work cheaply and correctly | Broad file/spec/git archaeology after compaction or session restart | Native state plane supplies exact current work, contract, and next action |
| Agent principal | Reuse past lessons only when applicable | Similar-looking old sessions can poison new work | Experience retrieval is filtered by applicability and anti-applicability, not just semantic similarity |
| PM / governance owner | Set policy defaults | Hard to know which thresholds to trust | Decision packets show effect, risk, and before/after impact |

## 8. Requirements

| ID | Requirement | Acceptance signal | Evidence / assumption |
| --- | --- | --- | --- |
| PRD-R1 | Engram must distinguish state, memory, and experience as separate product layers. | Specs, APIs, and operator UI name separate concepts and flows. | Derived from operator brief + research synthesis. |
| PRD-R2 | Session/goal/task/project handoff must have a native Engram path. | Session load reads native state first; filesystem fallback remains explicit. | Current `pkg/cognitive` types and retro findings justify the gap. |
| PRD-R3 | Principal/domain context must be first-class in retrieval and governance. | Every memory-governance flow can scope by principal/domain/project/privacy. | PMQ substrate already exists. |
| PRD-R4 | Retrieval must account for utility and applicability, not just similarity. | Ranking explanation includes usefulness/recency/scope and applicability qualifiers. | Current Thompson + feedback seeds; applicability is new work. |
| PRD-R5 | Forgetting must be a taxonomy, not a boolean delete. | Suppress / expire / archive / consolidate / destroy semantics are distinct in API and UI. | Research conclusion; current suppression-only gap. |
| PRD-R6 | Experience must be a first-class record type separate from atomic memory. | Experience records store trajectory/lesson/applicability fields and can be retrieved independently. | Operator requirement from current discussion. |
| PRD-R7 | Operator review must consume bounded decision packets. | Policy decisions, risky consolidations, archive resurfacing, and rare escalations show evidence cards with before/after impact. | Current operator burden critique. |
| PRD-R8 | Historical value must survive without polluting hot context. | Archive/cold tiers exist and are consulted only under explicit triggers. | Archive-first design choice. |
| PRD-R9 | High-value evolving truths must preserve temporal history and provenance. | Facts can be invalidated/superseded without losing history. | Graphiti-style temporal lessons, scoped selectively. |
| PRD-R10 | Each milestone must ship as a functional increment. | Every milestone has its own release/smoke proof. | Active PM contract for developer loop. |
| PRD-R11 | Experience-derived lessons must encode both applicability and anti-applicability conditions before they influence automation. | Experience records and retrieval logic can state where a lesson applies and where it must not auto-apply. | Current discussion about cross-session false similarity. |
| PRD-R12 | Archive resurfacing must be trigger-based, not always-on. | Historical retrieval runs only on “why / rollback / regression / what changed / revisit old decision” classes of prompts or workflows. | Archive-first but hot-path-clean requirement. |
| PRD-R13 | Memory and experience must expose different retrieval contracts. | Memory retrieval optimizes for current-use facts; experience retrieval optimizes for causal/historical/applicability answers. | Current design discussion; prevents ExperienceRecord collapsing into long memory rows. |
| PRD-R14 | Native state plane must define a deterministic resume protocol. | A first-read state packet, freshness marker, drift/conflict flags, and exact next-action semantics exist and are tested. | Current oracle and retro demonstrate the need. |
| PRD-R15 | Moderation queue is an exception surface, not the default operator workflow. | Queue SLO/backlog stays bounded; low-value safe decisions auto-resolve without operator review. | Current operator burden critique and product goal. |
| PRD-R16 | Archive retrieval may auto-run only for named trigger classes. | Archive digs are logged with trigger class and bounded result size; ordinary hot-path requests do not search archive. | Historical retrieval must stay useful without polluting normal prompts. |
| PRD-R17 | The current row-centric Memory Lab decision language must be replaced on touched surfaces. | Touched memory UX no longer frames operator work as per-row adjudication (`keep in prompts`, `hide as noise`, etc.); packet/queue-centric governance becomes the primary interaction. | Verified current UX debt in operator-console copy. |
| PRD-R18 | Any implied operator-facing UI/UX surface in this epic must have a designer-owned contract before implementation. | PM hands developer a reviewed design contract describing controls, usage flow, visual intent, and backend wiring needs; absence of contract is a blocker. | User directive in session; prevents backend-first UI drift. |

## 9. UX / Workflow Expectations

The operator workflow is not “read rows and sort them manually”. It is:

1. Inspect current state for a principal/domain/project.
2. Read a bounded principal brief and, when needed, archived experience signals.
3. Receive bounded review packets for low-value, conflicting, or risky memory items.
4. Approve/reject policy packets or risky merges with before/after previews.
5. Let the system auto-handle safe low-value forgetting and reinforcement.

The UI must distinguish at least:
- hot memory,
- archived memory,
- experience summaries,
- risky moderation queue,
- native state/handoff records.

### Required packet types
- **Policy Decision Packet** — threshold/default change with effect/risk preview.
- **Risky Consolidation Packet** — merge candidate, loss analysis, provenance, preview.
- **Rare Escalation Packet** — privacy/ownership/destructive conflict the system cannot settle safely.
- **Archive Resurfacing Packet** — historical experience that appears relevant, plus why the system believes archive digging is warranted and where applicability is uncertain.

### Required design-contract sidecars for implied UI/UX work
For every milestone or CR that implies operator-facing UI/UX, PM must produce or require a designer-owned contract before implementation. The contract may be Markdown plus machine-readable sidecars, but it must answer five things:
- **behavioral flow** — how the operator enters, navigates, decides, confirms, exits;
- **control map** — which visible controls or UX blocks exist;
- **backend wiring map** — which API/handler/state source each control binds to;
- **interaction rules** — flags, honesty states, loading/empty/error/gated behavior, and parity/i18n constraints;
- **usage scenarios with branches** — how the surface is actually used in happy path, empty state, validation failure, gated path, risky confirmation, rollback, and recovery.

PM does not design the UI directly. PM writes the designer task/contract, then pressure-tests it by a thought experiment from the operator seat: step-by-step usage scenarios with all meaningful branch points. A contract that names controls but does not prove how the operator can actually use the surface is incomplete.

Recommended machine sidecar: a JSON map per touched surface describing `surface`, `blocks`, `controls`, `data_sources`, `server_components`, `api_routes`, `states`, `scenarios`, `branches`, and `design_constraints`. Missing reviewed contract is a blocker.

### Current UX migration requirement
The current `Memory Lab` language and actions imply row-by-row operator adjudication (`Operator decides`, `keep this record in prompts`, `hide as noise`). This epic must migrate touched surfaces from row-centric controls to packet/queue-centric governance. The old language may survive only as an explicit transitional artifact, not as the target interaction model.

## 9A. Business Rules / State Map

| State / trigger | Outcome |
| --- | --- |
| Memory repeatedly useful across sessions | strengthen ranking / keep out of delete path |
| Memory low-use, low-confidence, and outside retention | suppress or archive automatically according to policy |
| Memory duplicate with no new information | consolidate or suppress automatically |
| Memory potentially loses unique meaning if merged | route to risky consolidation queue |
| Principal brief request | scope to principal-allowed memory only |
| Resume/session load | native state plane first, filesystem fallback second |
| Historical question / regression / “why did we change this?” | archive/experience search is allowed and preferred |
| Similar past solution but mismatched context envelope | do not auto-apply; downgrade or block on applicability mismatch |
| Experience says “worked under X” but current context includes anti-applicability Y | surface as warning or suppress from auto-apply path |

## 10. Data, Security, Reliability, and Operations

| Area | Product constraint | Impact |
| --- | --- | --- |
| Data | State plane records and memory/experience records need separate schemas and lifecycles. | new tables/models/API surfaces |
| Data | Experience synthesis is a pipeline, not just a table: episodes -> candidate experience -> validated experience -> distilled lesson -> memory/brief impact. | workflow + queue design |
| Security | Private principal memory remains fail-closed; state plane may contain sensitive workflow data. | privacy gates and audit required |
| Reliability | Handoff retrieval must be cheap and deterministic. | native state plane must outrank broad semantic search on resume |
| Reliability | Resume protocol must name one exact next action and one exact verification step. | recovery-state contract and tests |
| Operations | Archive tiers need retention and storage policy, but not hot-path injection. | cold storage/search triggers |
| Auditability | Every risky forget/consolidate/state mutation must be reconstructible. | audit models/UI |

## 11. Risks and Dependencies

| Risk / dependency | Severity | Owner | Mitigation or next evidence |
| --- | --- | --- | --- |
| Similar sessions with different hidden constraints produce bad memory reuse | Critical | architecture/retrieval owner | add applicability envelope and anti-applicability rules before automation |
| Native handoff competes with current filesystem oracle | High | state-plane owner | filesystem stays fallback until native path proves stable |
| Over-automation turns memory into silent loss | High | policy owner | archive-first defaults + risky queue + structural-loss guard |
| Over-graphification slows delivery and creates infra-first detour | Medium | architecture owner | scope graph to high-value temporal truths only |
| Operator queue still grows into garbage duty | High | product owner | packets must be bounded and policy-driven |
| Experience synthesis becomes vague summaries with no causal value | Medium | experience owner | require context, reversal, lesson, and applicability fields |
| Archive retrieval becomes over-eager | Medium | retrieval owner | trigger-gate archive digs; cap results and require resurfacing rationale |
| Old row-centric UI semantics survive under the new architecture | Medium | product + UI owner | explicitly replace row-adjudication copy and flows on touched surfaces |

## 12. Open Questions

| Question | Severity | Recommended answer | Impact if different |
| --- | --- | --- | --- |
| What exactly counts as an `ExperienceRecord` worthy of persistence? | High | Require a time span, at least one decision/action, and an outcome or reversal. | If broader, experience layer may become noisy narrative sludge. |
| Should native state plane live in the main memory store or a parallel state store? | Critical | Parallel but integrated state plane, so resume is deterministic and cheap. | If mixed into generic memory, retrieval ambiguity and audit complexity rise sharply. |
| Which forgetting modes may be fully automatic by default? | High | suppress / expire / archive yes; consolidate maybe; destroy no. | More aggressive defaults increase risk of silent loss. |
| When should archive/experience search be triggered automatically? | High | Only on historical/regression/rollback/why-did-we-change-this signals. | If always, hot path gets polluted; if never, history becomes useless. |
| How strict should anti-applicability be for automatic reuse? | High | Block automatic reuse when anti-applicability matches strongly; allow only explicit operator/agent override with evidence. | If weaker, past “similar” sessions will continue poisoning new work. |

## 13. PRD Mode and Iteration Log

| Field | Value |
| --- | --- |
| PRD mode | platform-milestone |
| Why this depth is enough | The product problem is clear and grounded in current Engram code and operator pain. The main remaining work is architectural decomposition, not more ideation. |
| Iterations completed | design + spec + external research synthesis + current Engram gap audit + challenge-pass revision 1 + challenge-pass revision 2 + challenge-pass revision 3 |
| Remaining product debt | threshold tuning, exact packet layouts, final state-plane schema decisions, archive trigger taxonomy |

## 14. Handoff

| Field | Value |
| --- | --- |
| Verdict | READY_FOR_ARCHITECTURE |
| Next owner | `nvmd-architect` |
| Reason | Product direction is clear, but the state/memory/experience layer split, packet contracts, experience pipeline, archive trigger model, native handoff/storage boundaries, and current UI migration need explicit architecture before deeper SpecKit planning. |
| Files produced | `prd.md`, `glossary.md`, `milestone-map.md` |
