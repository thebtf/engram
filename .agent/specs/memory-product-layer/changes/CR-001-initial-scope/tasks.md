# Tasks — ENG-MPL-1 / CR-001-initial-scope

**Feature:** `memory-product-layer`  
**CR:** `CR-001-initial-scope`  
**Source artifacts:** `spec.md`, `plan.md`, `completeness-report.md`, `checklists/general.md`, `changes/CR-001-initial-scope/change.md`  
**Task mode:** CR-scoped  
**MVP for this CR:** land native state plane + principal explorer/briefs substrate + backend packet-centric review-loop substrate + designer-contract gate artifacts. Experience storage proof, forgetting engine, and temporal truth stay later.

---

## Story Group 1 — Native resume becomes Engram-first

**Outcome:** when this group is done, agent can resume from a native bounded state packet before touching filesystem fallback.

**Independent Test:** I will prove this group by creating/updating native session + project state through the new seam, then reading one bounded resume packet and comparing the result to the fallback semantics described in `spec.md` §US1 and `plan.md` Phase 1.

**Checkpoint:** native state store, read path, and resume packet contract exist with proof.

### T001 — Define state packet and persistence contract
- Path: `pkg/cognitive/types.go`, `pkg/cognitive/interfaces.go`, `.agent/specs/memory-product-layer/contracts/state-plane.md`
- TDD: RED — add contract test cases for `freshness`, `drift/conflict`, `next_action`, `next_verification`; GREEN — align types/interfaces; REFACTOR — tighten comments/field names.
- Test owner: code worker
- Source evidence: PASS `pkg/cognitive/types.go`, `pkg/cognitive/interfaces.go`
- Data evidence: PASS native state separate from generic memory; fallback remains explicit.
- Confidence gate: re-check packet shape against `spec.md` FR-1/FR-2 before implementation.
- AC: resume packet fields are binary-defined, state write/read interface remains agent-owned, no field left as vague placeholder.
- Status: [X] Completed 2026-06-28.
- Artifacts: `pkg/cognitive/types.go`, `pkg/cognitive/interfaces.go`, `pkg/cognitive/state_plane_test.go`, `pkg/cognitive/types_test.go`, `pkg/cognitive/interfaces_test.go`, `pkg/cognitive/doc.go`, `.agent/specs/memory-product-layer/contracts/state-plane.md`, `.agent/tasks/T001/implementation-log.md`.
- Evidence: RED `go test ./pkg/cognitive` failed on missing T001 contract symbols before implementation; GREEN `go test ./pkg/cognitive ./internal/cognitive/core`; GREEN `go test ./...`; REVIEW degraded native route `019f0d0f-19a5-7c36-bfb7-086e179e1699` plus manual lite review with no blocking findings.

### T002 — Implement native state store and wire service seam
- Path: `internal/db/gorm/*state*`, `internal/worker/service.go`
- TDD: RED — failing state-store tests; GREEN — create store + wire into service; REFACTOR — remove duplication.
- Test owner: code worker
- Source evidence: PASS `internal/worker/service.go`, `pkg/cognitive/interfaces.go`
- Data evidence: PASS PostgreSQL 17 store; migration/rollback note required in implementation evidence.
- Confidence gate: if state-table shape drifts from T001 contract, stop and update contract first.
- AC: state rows persist and load through a dedicated seam; no direct filesystem primary read remains inside native path.
- Status: [X] Completed 2026-06-28.
- Artifacts: `internal/db/gorm/state_store.go`, `internal/db/gorm/state_store_test.go`, `internal/db/gorm/state_store_migration_test.go`, `internal/db/gorm/migrations.go`, `internal/worker/service.go`, `internal/worker/wiring_state_store_test.go`, `docs/arch/DATA_MODEL.md`, `.agent/tasks/T002/implementation-log.md`.
- Evidence: RED `go test ./internal/db/gorm ./internal/worker` failed on missing state-store seam; GREEN `go test ./internal/db/gorm ./internal/worker`; GREEN pgvector/PostgreSQL 17 state-store integration; GREEN `go test ./...`; REVIEW degraded native route `019f0d20-be01-78a3-823f-22ad2354926a` plus manual lite review with no blocking findings.

### T003 — Expose native resume/state read surface
- Path: `internal/mcp/tools_state*.go`, `internal/worker/handlers_state*.go`, `internal/mcp/server.go`
- TDD: RED — failing MCP/REST contract tests; GREEN — add read endpoints/tools; REFACTOR — unify adapter mapping.
- Test owner: code worker
- Source evidence: PASS `internal/mcp/server.go`, `cmd/engram-server/main.go`
- Data evidence: PASS native-first + filesystem fallback semantics.
- Confidence gate: verify auth/scope path stays fail-closed for project/session reads.
- AC: MCP and REST return bounded resume/state payloads; fallback marker appears only on fallback path; no broad file archaeology on happy path.
- Status: [X] Completed 2026-06-28.
- Artifacts: `internal/db/gorm/state_store.go`, `internal/mcp/tools_state.go`, `internal/mcp/tools_state_test.go`, `internal/mcp/server.go`, `internal/worker/handlers_state.go`, `internal/worker/handlers_state_test.go`, `internal/worker/service.go`, `.agent/tasks/T003/implementation-log.md`.
- Evidence: RED `go test ./internal/db/gorm ./internal/mcp ./internal/worker` failed on missing native state surface; GREEN `go test ./internal/db/gorm ./internal/mcp ./internal/worker`; GREEN pgvector/PostgreSQL 17 resume integration; GREEN `go test ./...`; REVIEW degraded native route `019f0d2d-4c3f-71cc-a547-a4880f366546` plus manual lite review with no blocking findings.

### T004 — Prove fallback and drift behavior
- Path: `internal/*state*_test.go`, `.agent/specs/memory-product-layer/evidence/phase-1-*`
- TDD: RED — failing drift/fallback assertions; GREEN — tests pass; REFACTOR — keep fixtures minimal.
- Test owner: code worker
- Source evidence: PASS `outputs/agent-memory-design.md`, current `current.json` role from plan.md
- Data evidence: PASS fallback semantics only; rollback = keep filesystem path primary.
- Confidence gate: if native parity fails, trigger plan contingency and park write path.
- AC: tests prove native-first read, explicit fallback marker, and drift/conflict path.
- Status: [X] Completed 2026-06-28.
- Artifacts: `internal/stateplane/service.go`, `internal/stateplane/service_test.go`, `internal/mcp/tools_state.go`, `internal/mcp/tools_state_test.go`, `internal/worker/handlers_state.go`, `internal/worker/handlers_state_test.go`, `internal/worker/service.go`, `.agent/specs/memory-product-layer/evidence/phase-1-fallback-drift.json`, `.agent/tasks/T004/implementation-log.md`.
- Evidence: RED `go test ./internal/stateplane ./internal/mcp ./internal/worker` failed on missing fallback/drift wrapper and fallback flag forwarding; GREEN `go test ./internal/stateplane ./internal/mcp ./internal/worker`; GREEN `go test ./pkg/cognitive ./internal/db/gorm ./internal/stateplane ./internal/mcp ./internal/worker`; GREEN `go test ./... -count=1`; REVIEW degraded native route `019f0d43-56ee-7405-a029-d8021bb884d2` plus manual lite review with no blocking findings.

### G001 — GATE: native resume proof
- Path: `.agent/specs/memory-product-layer/evidence/phase-1-gate.json`
- TDD CHECK: state packet tests, store tests, fallback/drift tests all green.
- RUN: `go test ./...`
- CHECK: native-first path proven; fallback explicit; no spec drift.
- ENFORCE: no downstream story starts if resume still depends on filesystem on happy path.
- RESOLVE: if parity fails, park write path and keep read-only introspection only.
- SAVE: write gate evidence JSON + raw command output.
- Status: [X] Passed 2026-06-28.
- Artifacts: `.agent/specs/memory-product-layer/evidence/phase-1-gate.json`, `.agent/specs/memory-product-layer/evidence/phase-1-gate.go-test.txt`.
- Evidence: GREEN `go test ./...`; phase-1 gate JSON records state packet/store/fallback checks, native-first proof, explicit fallback proof, and no spec drift.

Dependencies: none (first story group).

---

## Story Group 2 — Operator can inspect principal knowledge and use a designed brief surface

**Outcome:** when this group is done, operator can inspect principal/domain memory and fetch principal-scoped brief through a touched UI surface governed by a reviewed designer contract.

**Independent Test:** I will prove this group with principal query tests, brief scoping tests, and browser smoke tied to the approved design contract files under `.agent/specs/memory-product-layer/design-contracts/`, checking the touched UI against `spec.md` §US4 and §US7.

**Checkpoint:** principal explorer/brief backend is real; touched UI is blocked until design contract exists and passes PM scenario review.

### T005 — Write designer contract for touched principal-memory surface
- Path: `.agent/specs/memory-product-layer/design-contracts/principal-memory-surface.md`, `.agent/specs/memory-product-layer/design-contracts/principal-memory-surface.json`
- TDD: N/A — design-contract artifact, not code.
- Test owner: N/A design artifact
- Source evidence: PASS `spec.md` FR-11/FR-12/FR-13, `plan.md` Design System / UI Architecture
- Data evidence: N/A design-contract artifact
- Confidence gate: PM must run operator-seat thought experiment across happy/empty/gated/risky/rollback/recovery branches.
- AC: contract defines controls, usage flow, backend bindings, states, and branch scenarios; PM review result recorded.
- IF-WRONG: scenario proof exposes unusable flow → return to designer contract before any UI wiring.
- Status: [X] Completed 2026-06-28.
- Artifacts: `.agent/specs/memory-product-layer/design-contracts/principal-memory-surface.md`, `.agent/specs/memory-product-layer/design-contracts/principal-memory-surface.json`, `.agent/tasks/T005/implementation-log.md`.
- Evidence: JSON sidecar parsed successfully with `pm_review.result=PASS`; Markdown contract records controls, usage flow, backend bindings, states, required branch scenarios, non-goals, and PM scenario review result.

### T006 — Build principal/domain query substrate for explorer surface
- Path: `internal/worker/handlers_memories.go` or adjacent query handler, `internal/mcp/tools_memory*.go` or new focused query seam, `.agent/specs/memory-product-layer/contracts/principal-explorer.md`
- TDD: RED — failing principal/domain query tests; GREEN — scope-safe query path; REFACTOR — align with existing privacy visibility seam.
- Test owner: code worker
- Source evidence: PASS `.agent/specs/principal-memory-query-domain-registry/architecture.md`, challenge-pass code findings
- Data evidence: PASS build missing explorer substrate on top of existing privacy visibility; fail-closed private visibility.
- Confidence gate: re-check no finished principal explorer implementation is being assumed from PMQ docs.
- AC: operator/agent can query principal/domain/project memory through a bounded substrate layered over existing privacy visibility with attribution.
- Status: [X] Completed 2026-06-28.
- Artifacts: `.agent/specs/memory-product-layer/contracts/principal-explorer.md`, `.agent/tasks/T006/implementation-log.md`, `internal/principalmemory/query_service.go`, `internal/mcp/tools_principal_memory.go`, `internal/worker/handlers_principal_memory.go`.
- Evidence: current-code classification found the principal explorer substrate already live, wired through MCP and REST, and layered over existing privacy/domain/audit seams; focused verification `go test ./internal/principalmemory ./internal/mcp ./internal/worker -run "PrincipalMemory|QueryPrincipal" -count=1` passed and broader package verification `go test ./internal/principalmemory ./internal/mcp ./internal/worker -count=1` passed. Source discrepancy recorded: `.agent/specs/principal-memory-query-domain-registry/architecture.md` is absent from this worktree and absent from `HEAD`, so T006 did not assume PMQ docs as current source.

### T007 — Extend `get_memory_brief` with principal/domain scoping
- Path: `internal/mcp/tools_brief.go`, `internal/mcp/server.go`
- TDD: RED — failing brief scope tests; GREEN — principal/domain-aware brief path; REFACTOR — keep bounded compact response.
- Test owner: code worker
- Source evidence: PASS `internal/mcp/tools_brief.go`, `internal/mcp/server.go`
- Data evidence: PASS current helper exists, but principal/domain scoping is must-build.
- Confidence gate: if extension path gets messy, spin out sibling route/tool without changing bounded response contract.
- AC: principal-scoped brief or equivalent exists, remains bounded, and returns scope/freshness evidence.
- Status: [X] Completed 2026-06-28.
- Artifacts: `internal/mcp/tools_brief.go`, `internal/mcp/tools_brief_test.go`, `internal/mcp/server.go`, `.agent/tasks/T007/implementation-log.md`.
- Evidence: RED `go test ./internal/mcp -run "TestGetMemoryBrief" -count=1` failed on missing principal brief schema and scoped args falling through to the legacy project-injection path; GREEN same command passed; GREEN `go test ./internal/mcp ./internal/principalmemory -count=1` passed; GREEN `go test ./internal/mcp ./internal/principalmemory ./internal/worker -count=1` passed. Scoped briefs reuse the T006 principal query service, keep max `limit=10`, and return `source`, `freshness`, `generated_at`, and `scope` evidence.

### T008 — Wire touched principal-memory UI from approved design contract
- Path: `apps/operator-console/composables/useOperatorMemoryLab.ts`, `apps/operator-console/pages/memory.vue`, touched locale files
- TDD: RED — failing browser-state expectations from contract; GREEN — UI uses honest loading/empty/gated/error states; REFACTOR — remove row-centric copy on touched surface.
- Test owner: code worker
- Source evidence: PASS approved `design-contracts/principal-memory-surface.md`, current copy in `apps/operator-console/i18n/locales/en.json`
- Data evidence: N/A browser wiring task
- Confidence gate: do not start until T005 PASS recorded.
- AC: touched UI matches contract controls and state behavior; no per-row “keep in prompts” semantics remain on touched principal-memory flow.
- IF-WRONG: contract and backend seam diverge → stop, update contract or API map first.

### G002 — GATE: principal explorer + brief + design-contract proof
- Path: `.agent/specs/memory-product-layer/evidence/phase-2-gate.json`
- TDD CHECK: query tests, brief tests, browser smoke, locale/parity checks green.
- RUN: `go test ./...` plus browser smoke route.
- CHECK: explorer substrate and brief scoping are real; touched UI uses approved contract; PM scenario review recorded.
- ENFORCE: no review-loop UI starts before touched surface is contract-driven and honest.
- RESOLVE: if designer contract absent or rejected, backend may ship; UI stays blocked and must-build.
- SAVE: gate evidence + PM review note + browser artifacts.

Dependencies:
- G001 -> T006, T007, T008
- T005 -> T008
- T006 -> T007 (shared principal/domain scoping seam)

#### Concurrent Execution Map
- [P] T005 PARALLEL-WITH: T006, T007 — design artifact can proceed while backend scoping work proceeds; no shared code files.
- [P] T006 PARALLEL-WITH: T005 — PMQ extension work independent of design artifact.
- [P] T007 PARALLEL-AFTER: T006 signature lands — interface: principal/domain scope contract for brief path.

---

## Story Group 3 — Review-loop backend substrate is real and design-ready

**Outcome:** when this group is done, backend packet-centric review-loop substrate exists over existing candidate/snapshot/audit seams, and queue UI remains a later delivery behind a reviewed design contract.

**Independent Test:** I will prove this group by reading queue payloads from the extended candidate/snapshot seams and checking the packet contract against `spec.md` §US2 and §US6, without claiming queue UI delivery yet.

**Checkpoint:** review loop reuses existing governance substrate, not a parallel rebuild, and queue UI stays out of CR-001.

### T009 — Define packet projection contract over candidate/snapshot/audit seams
- Path: `.agent/specs/memory-product-layer/contracts/review-packets.md`, `internal/mcp/tools_candidates.go`, `internal/worker/service.go`
- TDD: RED — failing contract tests or fixture checks for queue payload shape; GREEN — packet projection contract defined and server seam shaped; REFACTOR — remove duplicate fields.
- Test owner: code worker
- Source evidence: PASS `internal/mcp/tools_candidates.go`, `internal/worker/service.go`, `apps/operator-console/composables/useOperatorOverview.ts`
- Data evidence: PASS existing candidate/snapshot/audit reuse; no governance rebuild.
- Confidence gate: if packet shape requires new persistence, prove existing seams insufficient first.
- AC: packet contract maps existing candidate/snapshot/audit data into bounded review payloads.

### T010 — Implement queue list/read actions on existing seams
- Path: `internal/mcp/tools_candidates.go`, related REST handlers, `internal/worker/service.go`
- TDD: RED — failing queue list/read tests; GREEN — packet-centric queue read path; REFACTOR — align MCP and REST shapes.
- Test owner: code worker
- Source evidence: PASS existing candidate tooling and service wiring
- Data evidence: PASS snapshot/audit integration
- Confidence gate: keep tasks on existing seams unless blocker proven.
- AC: review queue list/read works through current substrate; audit/snapshot evidence remains intact.

### T011 — Write designer contract for later review-queue surface
- Path: `.agent/specs/memory-product-layer/design-contracts/review-queue.md`, `.json`
- TDD: N/A — design artifact.
- Test owner: N/A design artifact
- Source evidence: PASS `spec.md` FR-4 FR-11 FR-12 FR-13, `plan.md` phase 3
- Data evidence: N/A design-contract artifact
- Confidence gate: PM scenario proof required before any future queue UI wiring.
- AC: queue contract defines packet layout, actions, state behavior, and branches for empty/gated/risky/rollback paths.
- IF-WRONG: scenario proof reveals garbage-duty UX → return to designer contract.

### G003 — GATE: packet-centric review-loop substrate proof
- Path: `.agent/specs/memory-product-layer/evidence/phase-3-gate.json`
- TDD CHECK: queue tests, snapshot/audit integration, packet contract checks green.
- RUN: `go test ./...`
- CHECK: backend queue substrate built on existing seams; no governance rebuild drift; future queue UI contract exists.
- ENFORCE: no experience/governance mutation work proceeds if review loop still dumps rows or fake states at backend contract level.
- RESOLVE: if existing seams insufficient, amend spec/plan before building new persistence.
- SAVE: gate evidence + contract review note + backend artifacts.

Dependencies:
- G002 -> T009, T010, T011
- T009 -> T010

#### Concurrent Execution Map
- [P] T011 PARALLEL-WITH: T009, T010 — design artifact independent of backend shaping.
- [P] T009 PARALLEL-WITH: T011 — contract projection work independent of design artifact.

---

## Follow-up CR boundary

The following slices are intentionally **out of CR-001 scope** and move to later CRs after state plane, explorer substrate, brief path, and review-loop substrate prove out:
- Experience + Applicability Contract
- Forgetting / Consolidation
- Selective Temporal Truth

Parent roadmap stays unchanged; only CR-001 is narrowed.

---

## Dependency Graph

- G001 -> Story Group 2
- G002 -> Story Group 3
- G003 -> CR-001 complete

---

## Suggested MVP scope

MVP for first execution run:
- Story Group 1
- Story Group 2

Stop there if:
- native resume still weak,
- principal brief still not scoped,
- explorer substrate still leaky or ambiguous,
- designer contract pipeline still shaky.
