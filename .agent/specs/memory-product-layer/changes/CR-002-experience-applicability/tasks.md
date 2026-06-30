# Tasks — ENG-MPL-1 / CR-002-experience-applicability

**Feature:** `memory-product-layer`  
**CR:** `CR-002-experience-applicability`  
**Source artifacts:** `spec.md`, `plan.md`, `architecture.md`, `milestone-map.md`, `prd.md`, `changes/CR-002-experience-applicability/change.md`  
**Task mode:** CR-scoped  
**MVP for this CR:** land first-class experience retrieval contract + applicability gate + bounded archive-trigger path, with proof for storage shape. Forgetting/consolidation and temporal truth stay later.

---

## Story Group 1 — Experience retrieval becomes a first-class contract

**Outcome:** when this group is done, agent/operator can retrieve bounded historical experience distinct from hot memory, with explicit applicability and anti-applicability semantics.

**Independent Test:** I will prove this group by querying an experience-oriented path that returns lesson + applicability rationale against worked fixtures, then checking the result against `spec.md` §US3 and `architecture.md` Happy path C.

**Checkpoint:** experience retrieval is real, separate, and bounded; hot memory retrieval is not silently reused as a fake substitute.

### T001 — Define experience contract and V1 envelope
- Path: `pkg/cognitive/types.go`, `pkg/cognitive/interfaces.go`, `.agent/specs/memory-product-layer/contracts/experience-contract.md`
- TDD: RED — failing contract tests for lesson, applicability, anti-applicability, trigger class, and source attribution; GREEN — align types/interfaces; REFACTOR — tighten field names/comments.
- Test owner: code worker
- Source evidence: PASS `spec.md` FR-5 FR-7 FR-8, `architecture.md` Happy path C, `design.md` V1 scoping guard.
- Data evidence: PASS no dedicated storage assumption yet.
- Confidence gate: minimum applicability envelope must be explicit, not placeholder prose.
- AC: first-class experience response contract exists and names `applies | uncertain | blocked` semantics plus anti-applicability rationale.
- Status: [X] Completed 2026-06-29.
- Artifacts: `pkg/cognitive/types.go`, `pkg/cognitive/interfaces.go`, `pkg/cognitive/types_test.go`, `pkg/cognitive/interfaces_test.go`, `.agent/specs/memory-product-layer/contracts/experience-contract.md`, `.agent/tasks/T001/implementation-log.md`.
- Evidence: RED `go test ./pkg/cognitive` failed on missing experience contract symbols; GREEN `go test ./pkg/cognitive`; prove-it enum mutation failed `TestExperienceApplicabilityStateEnum`; coverage `go test -cover ./pkg/cognitive` 94.2%.

### T002 — Implement bounded experience query path
- Path: `internal/<experience-service>*.go`, `internal/mcp/*`, `internal/worker/*`, or the smallest live seam the implementation proves correct
- TDD: RED — failing retrieval tests; GREEN — bounded experience query path; REFACTOR — remove duplicate mapping.
- Test owner: code worker
- Source evidence: PASS `architecture.md` data-architecture split, `plan.md` Phase 4 scope.
- Data evidence: PASS projection/materialization over existing evidence unless dedicated storage is proven necessary.
- Confidence gate: if implementation needs a new persistence family, prove why projection/materialization fails first.
- AC: caller can request experience separately from hot memory and receive bounded historical/causal payloads.
- Status: [X] Completed 2026-06-29.
- Artifacts: `internal/experience/service.go`, `internal/experience/service_test.go`, `.agent/tasks/T002/implementation-log.md`.
- Evidence: RED `go test ./internal/experience` failed on missing service symbols; GREEN `go test ./internal/experience`; coverage `go test -cover ./internal/experience` 87.4%; focused contract `go test ./internal/experience ./pkg/cognitive`.

### T003 — Add applicability and anti-applicability gate behavior
- Path: query service + tests + `.agent/specs/memory-product-layer/contracts/experience-contract.md`
- TDD: RED — failing `applies/uncertain/blocked` tests; GREEN — gate classifies candidates with rationale; REFACTOR — centralize gate logic.
- Test owner: code worker
- Source evidence: PASS `spec.md` US3, `plan.md` Phase 4 verification, `design.md` V1 scoping guard.
- Data evidence: PASS worked examples showing bad reuse blocked.
- Confidence gate: anti-applicability must block silent auto-reuse under strong mismatch.
- AC: applicability gate returns explicit state + rationale and prevents naive semantic reuse.
- Status: [X] Completed 2026-06-29.
- Artifacts: `internal/experience/service.go`, `internal/experience/service_test.go`, `.agent/tasks/T003/implementation-log.md`.
- Evidence: RED `go test ./internal/experience` failed on missing service symbols; GREEN `go test ./internal/experience`; prove-it blocked-to-uncertain mutation failed targeted test; coverage `go test -cover ./internal/experience` 87.4%.

### G001 — GATE: experience contract proof
- Path: `.agent/specs/memory-product-layer/evidence/phase-4-gate.json`
- TDD CHECK: contract tests + retrieval tests + applicability gate tests all green.
- RUN: `go test ./...`
- CHECK: experience retrieval is separate from hot memory and bounded by explicit applicability semantics.
- ENFORCE: no forgetting or temporal slice starts if experience still collapses into generic memory rows.
- SAVE: gate evidence JSON + raw command output.
- Status: [X] Completed 2026-06-29.
- Evidence: `go test ./...` PASS; saved `.agent/specs/memory-product-layer/evidence/phase-4-gate.json` and raw output `.agent/specs/memory-product-layer/evidence/phase-4-gate-go-test-all.txt`.

Dependencies: none (first story group of CR-002).

---

## Story Group 2 — Archive resurfacing stays trigger-gated and honest

**Outcome:** when this group is done, archive/historical resurfacing runs only for named trigger classes and returns bounded context instead of polluting the hot path.

**Independent Test:** I will prove this group with trigger fixture tests showing when archive context is added, skipped, or bounded, checking results against `spec.md` §US5 and `architecture.md` error-path/archive rules.

**Checkpoint:** archive retrieval is explicit, bounded, and off the ordinary hot path.

### T004 — Define archive trigger taxonomy and bounded packet behavior
- Path: `.agent/specs/memory-product-layer/contracts/experience-contract.md`, implementation files that hold trigger enums/logic
- TDD: RED — failing trigger classification tests; GREEN — named trigger taxonomy + packet rules; REFACTOR — remove ambiguous branches.
- Test owner: code worker
- Source evidence: PASS `plan.md` unknown/risk row for archive trigger taxonomy, `architecture.md` trigger-gated archive path.
- Data evidence: PASS bounded resurfacing only.
- Confidence gate: trigger classes must be finite and auditable.
- AC: archive trigger classes are explicit and packet behavior is bounded.
- Status: [X] Completed 2026-06-29.
- Artifacts: `internal/experience/service.go`, `internal/experience/archive_test.go`, `.agent/specs/memory-product-layer/contracts/experience-contract.md`, `.agent/tasks/T004/implementation-log.md`.
- Evidence: RED `go test ./internal/experience` failed on missing archive taxonomy/seam symbols; GREEN `go test ./internal/experience`; prove-it removed trigger gate and failed ordinary-path test; coverage `go test -cover ./internal/experience` 89.0%.

### T005 — Implement archive-triggered resurfacing on the experience path
- Path: same bounded retrieval seam as T002 plus focused tests/evidence
- TDD: RED — failing trigger-path tests; GREEN — bounded archive context appears only on allowed triggers; REFACTOR — keep hot path cheap.
- Test owner: code worker
- Source evidence: PASS `spec.md` FR-6, NFR-5.
- Data evidence: PASS ordinary requests skip archive by default.
- Confidence gate: ordinary experience queries must not drag archive by default.
- AC: archive resurfacing is trigger-gated, bounded, and logged in evidence.
- Status: [X] Completed 2026-06-29.
- Artifacts: `internal/experience/service.go`, `internal/experience/archive_test.go`, `.agent/tasks/T005/implementation-log.md`.
- Evidence: RED `go test ./internal/experience`; GREEN `go test ./internal/experience`; ordinary no-trigger request kept archive call count at zero; valid trigger returned bounded archive responses and `ArchiveEvidence()` entry.

### G002 — GATE: archive-trigger proof
- Path: `.agent/specs/memory-product-layer/evidence/phase-4-archive-gate.json`
- TDD CHECK: trigger tests + bounded resurfacing tests green.
- RUN: `go test ./...`
- CHECK: archive path is explicit, bounded, and absent from ordinary hot path.
- ENFORCE: no forgetting/consolidation slice starts if archive behavior is still fuzzy.
- SAVE: gate evidence JSON + raw command output.
- Status: [X] Completed 2026-06-29.
- Evidence: `go test ./...` PASS; saved `.agent/specs/memory-product-layer/evidence/phase-4-archive-gate.json` and raw output `.agent/specs/memory-product-layer/evidence/phase-4-archive-gate-go-test-all.txt`.

Dependencies:
- G001 -> T004, T005
- T004 -> T005

---

## Story Group 3 — Storage-shape proof and CR boundary

**Outcome:** when this group is done, CR-002 closes with evidence for whether V1 stays on projection/materialization or needs dedicated `ExperienceRecord` storage, without leaking into forgetting or temporal-truth work.

**Independent Test:** I will prove this group by documenting the live implementation choice, its evidence, and the rejected alternative in a bounded proof artifact tied to the tests above.

**Checkpoint:** storage direction is proven, not guessed; next CR boundary is explicit.

### T006 — Write storage-shape proof for V1 experience path
- Path: `.agent/specs/memory-product-layer/contracts/experience-storage-proof.md`, implementation notes/evidence sidecar if needed
- TDD: N/A — proof artifact.
- Test owner: N/A proof artifact
- Source evidence: PASS `architecture.md` ADR-003 and data architecture section, `plan.md` Phase 4 contingency.
- Data evidence: PASS outputs from T002-T005.
- Confidence gate: dedicated storage may be chosen only with concrete failing/prohibitive evidence for projection/materialization.
- AC: proof artifact states chosen shape, rejected shape, evidence, and next-slice consequence.
- Status: [X] Completed 2026-06-29.
- Artifacts: `.agent/specs/memory-product-layer/contracts/experience-storage-proof.md`, `.agent/specs/memory-product-layer/evidence/experience-storage-proof.json`, `.agent/tasks/T006/implementation-log.md`.
- Evidence: storage proof chooses projection/materialization for CR-002 V1, rejects dedicated `ExperienceRecord` tables for this slice, cites T002-T005/G001/G002, and names next-slice conditions for dedicated storage.

### G003 — GATE: CR-002 complete
- Path: `.agent/specs/memory-product-layer/evidence/phase-4-closeout.json`
- TDD CHECK: G001 + G002 passed; storage proof present.
- RUN: `go test ./...`
- CHECK: experience retrieval, applicability gate, and archive trigger path are real; forgetting/consolidation and temporal truth remain later.
- ENFORCE: next CR starts only after closeout names the exact boundary.
- SAVE: closeout JSON + proof artifact path + raw command output.
- Status: [X] Completed 2026-06-29.
- Evidence: `go test ./...` PASS; saved `.agent/specs/memory-product-layer/evidence/phase-4-closeout.json` and raw output `.agent/specs/memory-product-layer/evidence/phase-4-closeout-go-test-all.txt`.

Dependencies:
- G002 -> T006
- T006 -> G003

---

## Follow-up CR boundary

The following slices stay **out of CR-002 scope** and move to later CRs after experience/applicability proof is closed:
- Forgetting / Consolidation
- Selective Temporal Truth
- learned applicability model
- broad graph projection
- any broad operator-console redesign beyond currently touched surfaces

Parent roadmap stays unchanged; CR-002 is the bounded Phase 4 slice only.

---

## Dependency Graph

- G001 -> Story Group 2
- G002 -> Story Group 3
- G003 -> CR-002 complete
