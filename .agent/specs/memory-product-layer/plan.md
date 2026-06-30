# Plan: Agent Knowledge and Experience Layer

**Feature:** ENG-MPL-1 (`memory-product-layer`)  
**Plan date:** 2026-06-28  
**Mode:** CREATE  
**Updated for:** ENG-MPL-1 — open CRs: [CR-001-initial-scope, CR-002-experience-applicability, CR-003-forgetting-consolidation, CR-004-selective-temporal-truth]

> **Sources:** `.agent/specs/memory-product-layer/spec.md`, `prd.md`, `architecture.md`, `milestone-map.md`, `.agent/specs/constitution.md`, `.agent/specs/principal-memory-query-domain-registry/architecture.md`, `pkg/cognitive/interfaces.go`, `pkg/cognitive/types.go`, `internal/mcp/tools_brief.go`, `internal/mcp/server.go`, `internal/mcp/tools_candidates.go`, `internal/worker/service.go`, `apps/operator-console/composables/useOperatorOverview.ts`, `apps/operator-console/useOperatorMemoryLab.ts`, `apps/operator-console/i18n/locales/en.json`, `docs/DEPLOYMENT.md`

---

## Reversibility Decision Table

| Decision | Why needed | Reversibility | Evidence | If wrong |
| --- | --- | --- | --- | --- |
| Dedicated native state plane tables | Resume must be deterministic and cheap; current path still leans on `current.json` + `CONTINUITY.md` | PARTIALLY REVERSIBLE | `pkg/cognitive/interfaces.go`, `pkg/cognitive/types.go`, `internal/cognitive/core/noop.go`, `outputs/agent-memory-design.md` | Keep filesystem fallback live; pivot by reading old fallback path until state tables corrected |
| Extend existing privacy visibility + add principal/domain query substrate for principal explorer/briefs | Existing code proves privacy visibility and bounded brief helper, but not full principal explorer substrate; build the missing query layer instead of pretending it already exists | REVERSIBLE | `.agent/specs/principal-memory-query-domain-registry/architecture.md`, `internal/mcp/tools_brief.go`, `internal/mcp/server.go`, challenge-pass findings | Fall back to current helper + scoped memory lists while explicit explorer substrate is completed |
| Build review loop on candidate/snapshot/audit seams first | Existing queue/audit substrate already exists; cheaper than governance rebuild | REVERSIBLE | `internal/mcp/tools_candidates.go`, `internal/worker/service.go`, `apps/operator-console/composables/useOperatorOverview.ts` | Keep row-centric UI dormant; route through current candidate APIs only |
| Treat experience as first-class contract before dedicated storage | Product boundary clear; storage shape still not proven | REVERSIBLE | `prd.md`, `spec.md`, `architecture.md`, challenge-pass findings | Start with projection/materialization; if weak, collapse to annotated memory rows temporarily |
| Trigger-gate archive retrieval | Always-on archive search pollutes hot path | REVERSIBLE | `prd.md`, `architecture.md`, `outputs/agent-memory-design.md` | Disable auto-trigger path; allow explicit archive-only query route |
| Designer-owned UI contract as blocker for implied UI work | Prevent hallucinated unusable UI and backend-first drift | REVERSIBLE | user directive in session, constitution principle 3 | Block UI CR until designer contract arrives |

**REVERSIBILITY_AUDIT: PASS**

Phase-ordering note:
- P1 user outcomes first: cheap resume, principal visibility, usable operator surface.
- Experience and temporal truth stay later because they are higher ambiguity and more reversible.

---

## Tech Stack

| Area | Choice | Status | Why |
| --- | --- | --- | --- |
| Server runtime | Go modular monolith | PASS | Existing runtime and deployment already proven |
| Browser surface | existing operator-console app + designer contract gate | PASS | Must evolve current console, not invent a second UI |
| Persistence | PostgreSQL 17 | PASS | Constitution + deployment proof |
| State fallback | `.agent/session-state/current.json` + `.agent/CONTINUITY.md` | PASS | Explicit fallback/export until native state proves stable |
| Principal memory query | build missing principal/domain explorer substrate on top of existing privacy visibility | PASS | Current code proves privacy visibility substrate exists, not a finished principal explorer |
| Bounded brief generation | existing `get_memory_brief` seam, extended | PASS | Current helper exists; principal scoping is missing |
| Review loop | existing candidate/snapshot/audit seams, extended | PASS | Current queue seeds exist |
| Experience storage | custom V1 projection/materialization over existing evidence, dedicated tables only if plan-phase proof says yes | PASS | No external library candidate relevant; this is product-domain storage, not package choice |
| Temporal truth | custom late milestone | PASS | High-value narrow domain model, not off-the-shelf concern |

No library candidate required for core product-domain seams. This is internal platform architecture, not a commodity framework gap.

---

## Source Requirements

| Topic | Requirement | Status | Evidence |
| --- | --- | --- | --- |
| Current server runtime topology | Verify current deployment and entrypoint shape | PASS | `cmd/engram-server/main.go`, `docs/DEPLOYMENT.md` |
| Native state types/seams | Verify state-plane substrate is types/interfaces only | PASS | `pkg/cognitive/interfaces.go`, `pkg/cognitive/types.go`, `internal/cognitive/core/noop.go` |
| Current brief surface | Verify `get_memory_brief` exists and is project-scoped | PASS | `internal/mcp/tools_brief.go`, `internal/mcp/server.go` |
| Current governance seeds | Verify candidate/snapshot/audit seams exist | PASS | `internal/mcp/tools_candidates.go`, `internal/worker/service.go`, `apps/operator-console/composables/useOperatorOverview.ts` |
| Current UX debt | Verify row-centric operator copy exists | PASS | `apps/operator-console/i18n/locales/en.json` |
| External framework/version-sensitive claims | None load-bearing for this plan | N/A | No new framework/cloud/SDK contract introduced |

---

## Design System / UI Architecture

### Design contract gate

Any milestone or CR that implies operator-facing UI/UX is blocked until a **designer-owned contract** exists and PM has reviewed it.

Required contract outputs:
1. Markdown design brief for the touched surface.
2. Machine sidecar JSON map.
3. Scenario proof with branch coverage.

### Required design JSON sidecar

Per touched surface, create a sidecar like:

```json
{
  "surface": "memory-principal-brief",
  "blocks": ["filter-bar", "brief-card", "scope-evidence", "queue-panel"],
  "controls": ["principal-select", "domain-select", "refresh", "review-action"],
  "data_sources": ["native-state", "principal-memory", "candidate-queue"],
  "server_components": ["StatePlaneService", "KnowledgeQueryService", "GovernanceExtension"],
  "api_routes": ["GET /api/..."],
  "states": ["loading", "empty", "gated", "error", "risky-confirm"],
  "scenarios": ["happy-path", "empty-path", "gated-path", "rollback-path"],
  "branches": {
    "happy-path": ["..."],
    "gated-path": ["..."],
    "rollback-path": ["..."]
  },
  "design_constraints": ["honesty-states", "i18n", "parity-ledger"]
}
```

### Scenario-proof rule

PM must run thought experiment from operator seat for every touched UI surface:
- entry
- first read
- empty state
- validation failure
- gated route
- risky confirmation
- rollback
- recovery

If any branch reads like guessed nonsense — stop. Return to designer contract.

### UI architecture rule

Developer builds UI **from** design contract. No contract — blocker.

---

## Architecture

### File responsibility map

| Path | Action | Purpose |
| --- | --- | --- |
| `pkg/cognitive/types.go` | modify | finalize state packet/value-object shapes if needed |
| `pkg/cognitive/interfaces.go` | modify | confirm state-plane interfaces and any new contract seams |
| `internal/db/gorm/*state*` (new) | create | native state persistence store |
| `internal/mcp/tools_state*.go` (new) | create | MCP surface for native state read/write/resume |
| `internal/worker/handlers_state*.go` (new) | create | REST/browser surface for state plane |
| `internal/mcp/tools_brief.go` | modify | principal/domain scoping and evidence shaping |
| `internal/mcp/server.go` | modify | advertise any new/changed state and brief tools |
| `internal/db/gorm/*experience*` (new or deferred) | conditional | only if plan phase proves dedicated storage needed |
| `internal/mcp/tools_candidates.go` | modify | packet-centric review extensions if needed |
| `internal/worker/service.go` | modify | wire state store + governance extensions |
| `apps/operator-console/composables/useOperatorOverview.ts` | modify | queue/overview reads using honest state labels |
| `apps/operator-console/composables/useOperatorMemoryLab.ts` | modify | migrate touched flows away from row-centric semantics |
| `apps/operator-console/pages/*.vue` | modify | touched UI only after designer contract |
| `apps/operator-console/i18n/locales/{ru,en,zh}.json` | modify | touched visible copy |
| `.agent/specs/memory-product-layer/design-contracts/*.md` (new) | create | designer-owned UI contracts |
| `.agent/specs/memory-product-layer/design-contracts/*.json` (new) | create | wiring maps + scenario branches |
| `.agent/specs/memory-product-layer/evidence/*` | create | smoke/test/proof artifacts |

### Data Architecture

| Field | Content |
| --- | --- |
| Data owners | state plane owns native handoff rows; existing memory store owns atomic memory; governance extension owns review packets projected from candidate/snapshot/audit seams; experience starts as contract/projection until dedicated storage is proven |
| Invariants | native state separate from generic memory; principal-private fail-closed; archive not searched on ordinary path; anti-applicability may block reuse; UI contract required before operator-facing implementation |
| Migration shape | state tables first; principal/domain explorer substrate + brief extension second; governance seam extension third; experience storage decision later; temporal truth last |
| Engine constraints | PostgreSQL 17 single source of truth; no new service boundary |
| Plan handoff | `nvmd-tasks` must generate separate CR/task groups for state, principal explorer, review loop, designer contracts, and later experience work |

### Reusability Awareness

| Candidate | Boundary | Interface stability | Cross-project similarity | Next step |
| --- | --- | --- | --- | --- |
| `StatePlaneService` resume packet | session/project input → bounded resume packet | Draft | No query run beyond local evidence | keep watching |
| `DesignContractMap` | UI contract JSON sidecar | Draft | No query run beyond local evidence | keep watching |
| `ApplicabilityGate` | current context + experience contract → applies/blocks/warns | Sketch | No query run beyond local evidence | keep watching |

### Domain Modeling

DDD evaluated — needed, but light.

Entities / aggregates worth planning around:
- `SessionState`
- `ProjectState`
- `ResumePacket`
- `PrincipalBrief`
- `ReviewPacket`
- `ExperienceContract`
- later optional `TemporalTruth`

Suggested next step:
- keep modular monolith,
- cut repositories by bounded context,
- do **not** commit full table families for every context until proof exists.

---

## API Contracts

### State plane

| Contract | Shape | Notes |
| --- | --- | --- |
| native state read | REST + MCP | returns bounded resume packet |
| native state write | agent-only paths | human/browser write not default |
| project state read | REST + MCP | operator/agent inspectable |

### Principal explorer + briefs

| Contract | Shape | Notes |
| --- | --- | --- |
| principal memory query | build principal/domain query substrate over existing privacy visibility | indexed filters, privacy-safe |
| principal-scoped brief | extend `get_memory_brief` or sibling route/tool | bounded, evidence-tagged |

### Review loop

| Contract | Shape | Notes |
| --- | --- | --- |
| review queue list | extend candidate/snapshot surface | packet-centric, not row dump |
| review action apply | promote/reject/suppress/archive/consolidate hooks | auditable, honest error/gated states |

### Experience contract

| Contract | Shape | Notes |
| --- | --- | --- |
| experience query | new read contract | first-class retrieval behavior |
| archive resurfacing | trigger-gated | bounded results + rationale |

---

## Phases

### Phase 1 — Native State Plane

**Outcome:** cheap deterministic resume from Engram-native records.

**Scope:**
- finalize state-plane schema
- implement state store
- implement native read path
- preserve filesystem fallback/export
- add audit evidence

**Verification:**
> Evidence route: `go test ./...` for state store and packet tests, targeted fallback-path assertions against `current.json` semantics, and saved artifacts under `.agent/specs/memory-product-layer/evidence/phase-1-*`.
- unit tests for state read/write
- resume packet tests
- drift/fallback tests
- proof that native path is read before filesystem fallback

#### Concurrent Work Directives (computed)
- state store implementation and MCP/REST adapter scaffolding may run in parallel **after** packet schema is fixed.
- filesystem fallback tests can run in parallel with adapter tests once read path exists.

#### Contingency Branches
- If native state parity fails against filesystem fallback — keep fallback primary and ship read-only introspection first.

### Phase 2 — Principal Memory Explorer + Briefs + minimal UX migration

**Outcome:** operator can inspect principal/domain memory and fetch real principal-scoped briefs; touched UI may start migrating away from row-centric semantics only where approved contracts and real seams exist.

**Scope:**
- build missing principal/domain explorer substrate on top of existing privacy visibility
- extend or wrap `get_memory_brief`
- ship touched-surface designer contracts
- wire only the approved touched UI with honest states

**Verification:**
> Evidence route: `go test ./...` for explorer/brief scoping, browser smoke only for approved touched surfaces, locale diff checks in `apps/operator-console/i18n/locales/{ru,en,zh}.json`, and saved design-contract review notes under `.agent/specs/memory-product-layer/design-contracts/`.
- principal/privacy query tests
- brief scoping tests
- browser smoke on approved touched surfaces
- parity/i18n checks
- scenario-proof review of design contracts

#### Concurrent Work Directives (computed)
- designer contract authoring can run in parallel with backend scoping work.
- UI implementation waits on approved design contract.

### Phase 3 — Usefulness / Noise Review Loop

**Outcome:** packet-centric review-loop substrate exists over existing candidate/snapshot/audit seams; the approved Review Queue mode inside Memory Lab is accepted in CR-001, while broader operator-facing queue expansion follows later only if another CR widens scope.

**Scope:**
- queue list/read substrate
- packet projection over candidate/snapshot/audit data
- suppression/preservation actions
- design contract for queue surface
- honest metric display

**Verification:**
> Evidence route: candidate/snapshot APIs verified by `go test ./...`, packet-contract tests, design-contract review notes, and the accepted in-surface Review Queue mode. Queue browser smoke for broader expansion runs only when a later queue-expansion CR is active.
- candidate queue API tests
- snapshot/audit integration tests
- packet contract tests
- scenario-proof review of queue design contract

### Phase 4 — Experience + Applicability Contract

**Outcome:** agent/operator can retrieve contextualized experience with applicability/anti-applicability semantics.

**Scope:**
- define experience contract
- define minimum envelope fields
- implement query path
- decide projection/materialization vs dedicated storage based on proof

**Verification:**
> Evidence route: contract tests for `applies/uncertain/blocked`, archive-trigger fixture tests, and worked scenario examples captured under `.agent/specs/memory-product-layer/evidence/phase-4-*`.
- contract tests for applies/uncertain/blocked
- archive trigger tests
- scenario examples proving usefulness

#### Contingency Branches
- If dedicated storage still not justified — keep projection/materialization and defer tables.

### Phase 5 — Forgetting / Consolidation

**Outcome:** safe, auditable suppress/expire/archive/consolidate/destroy behavior.

**Scope:**
- forgetting taxonomy implementation
- structural-loss guard
- risky review packets
- audit/export proof

**Verification:**
> Evidence route: structural-loss test fixtures, destructive-path authorization tests, queue-escalation tests, and audit artifacts written under `.agent/specs/memory-product-layer/evidence/phase-5-*`.
- structural-loss tests
- destructive-path authorization tests
- queue escalation tests

### Phase 6 — Selective Temporal Truth

**Outcome:** high-value evolving facts can answer “true now vs then”.

**Scope:**
- narrow temporal truth schema
- validity/invalidation history
- provenance-first retrieval

**Verification:**
> Evidence route: targeted temporal query tests plus a written scope proof showing which facts entered the temporal path and which did not, saved under `.agent/specs/memory-product-layer/evidence/phase-6-*`.
- targeted temporal query tests
- explicit proof this is still narrow scope

---

## Library Decisions

| Need | Decision | Why |
| --- | --- | --- |
| Native state persistence | custom in existing GORM/PostgreSQL stack | domain-specific, no external library win |
| Principal-scoped brief | extend existing helper | helper already exists |
| Review queue | extend candidate/snapshot/audit seams | seams already exist |
| Experience contract | custom domain implementation | product semantics unique |
| Temporal truth | custom late-slice implementation | narrow domain, no early package need |

---

## Unknowns and Risks

| Unknown / risk | Impact | Next evidence |
| --- | --- | --- |
| Exact state packet schema | High | plan-phase contract draft + tests |
| Minimum applicability envelope | High | designer/PM/operator scenarios + retrieval tests |
| Whether experience needs dedicated tables | High | Phase 4 prototype proof |
| Exact archive trigger taxonomy | Medium | define enum + audit fields in Phase 4 |
| UI contract throughput with new designer role | Medium | run first real contract through Phase 2 |

---

## Constitution Compliance

| Principle | Status | Note |
| --- | --- | --- |
| Reliable memory primitives over clever context magic | PASS | state-first thin strangler, no magic rebuild |
| No resurrection of demolished v5 systems | PASS | every seam classified live/partial/must-build |
| Design contract owns operator console behavior | PASS | designer contract is explicit blocker |
| Honesty beats completeness | PASS | touched UI states must stay honest |
| Secrets are write-only and transient on reveal | N/A | not touched by this plan slice directly |
| Evidence gates before completion claims | PASS | every phase has proof requirements |

---

## Unresolved blockers

- No blocker for Phase 1 backend work.
- **UI work blocker:** no approved designer contract yet.
- **Storage-shape blocker for later slices:** dedicated experience tables not yet proven.
