# Milestone Map — Agent Knowledge and Experience Layer

## Milestone MPL-1 — Native State Plane

**Outcome:** session/goal/task/project handoff becomes Engram-native, with filesystem state demoted to fallback/export.

### Candidate feature slices
- native session state persistence
- native goal / assignment / execution pointer records
- load/save/read path that prefers Engram over `current.json`
- audit trail for state mutations
- deterministic resume protocol (freshness marker, drift/conflict flags, exact next action + next verification)

### Exit gate
- native handoff path is live
- resume can fetch current state cheaply without broad file archaeology
- filesystem oracle remains only as fallback/cache
- resume order is deterministic and tested

## Milestone MPL-2 — Principal Memory Explorer + Briefs

**Outcome:** operator can inspect principal/domain memory, retrieve principal-scoped briefs, and stop depending on row-centric Memory Lab semantics on touched surfaces.

### Candidate feature slices
- principal explorer surface
- principal/domain filters
- principal-scoped `get_memory_brief` built atop existing PMQ + adaptive brief seams
- scope/freshness evidence
- privacy-safe widening controls
- minimal packet/queue UX migration on touched memory surfaces

### Exit gate
- operator can answer what a principal knows/carries without tag archaeology
- principal briefs are real and bounded
- touched principal-memory UI no longer depends on `Operator decides` row language

## Milestone MPL-3 — Usefulness / Noise Review Loop

**Outcome:** operator can improve memory quality through bounded queues built on existing candidate/snapshot/audit seams, not raw row sorting.

### Candidate feature slices
- usefulness/noise queue
- suppression/preservation actions
- metrics showing effect honestly
- moderation queue semantics and packet types
- policy decision packets
- queue SLO / bounded backlog rules
- migration of current row-centric Memory Lab semantics to packet/queue-centric governance
- explicit reuse of candidate/snapshot/audit primitives where they already exist

### Exit gate
- low-value/noisy memory is reviewable through bounded queues
- metrics and audit evidence stay honest
- operator sees decision packets, not raw row sludge
- queue acts as exception surface, not default operating mode
- touched UI no longer frames the operator as per-row adjudicator

## Milestone MPL-4 — Experience + Applicability Layer

**Outcome:** Engram can retrieve contextualized experience, not just facts, and can explain when a past lesson should or should not be auto-applied.

### Candidate feature slices
- first-class experience contract and retrieval path
- applicability envelope fields
- anti-applicability conditions
- historical retrieval triggers
- lesson distillation from repeated/revised trajectories
- archive resurfacing packets
- separate retrieval contract for experience vs memory
- decide whether V1 needs dedicated `ExperienceRecord` storage or projection/materialization over existing evidence

### Exit gate
- agent/operator can retrieve what happened, why it changed, and when a past lesson should or should not influence current work
- archive retrieval is trigger-based and bounded
- experience retrieval differs explicitly from hot memory retrieval
- V1 storage shape is proven, not guessed

## Milestone MPL-5 — Forgetting / Consolidation

**Outcome:** Engram can intentionally shrink or merge memory without silent data loss.

### Candidate feature slices
- forgetting taxonomy implementation
- consolidation candidates
- structural-loss guard
- archive/cold-tier movement
- destroy path policy gate
- risky consolidation packets

### Exit gate
- suppress / expire / archive / consolidate / destroy are distinct, auditable operations
- high-risk merges escalate with packets rather than raw rows
- automatic forgetting obeys archive-first defaults

## Milestone MPL-6 — Selective Temporal Truth Graph

**Outcome:** Engram preserves high-value evolving truths over time without graphifying everything.

### Candidate feature slices
- temporal truth records for high-value evolving facts
- validity windows and invalidation history
- provenance-first time-bounded retrieval
- selective graph projection for principals/domains/decisions with temporal change

### Exit gate
- agent can retrieve not only what is true now, but what was true then and why it changed, for selected high-value truths
- temporal graph scope remains intentionally narrow

## Dependency order

1. MPL-1
2. MPL-2
3. MPL-3
4. MPL-4
5. MPL-5
6. MPL-6

## Why this order

- state plane first lowers token cost and recovery complexity for every later step;
- visibility before mutation;
- experience/applicability before dangerous forgetting and consolidation;
- review loop before high-risk memory mutation;
- temporal truth graph last, once state, visibility, experience, and forgetting semantics are already stable.
