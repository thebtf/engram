# Tasks — ENG-MPL-1 / CR-003-forgetting-consolidation

**Feature:** `memory-product-layer`  
**CR:** `CR-003-forgetting-consolidation`  
**Source artifacts:** `spec.md`, `plan.md`, `architecture.md`, `milestone-map.md`, `prd.md`, `changes/CR-003-forgetting-consolidation/change.md`  
**Task mode:** CR-scoped  
**MVP for this CR:** land explicit forgetting taxonomy + structural-loss guard + risky packet routing + audit/export proof. Temporal truth stays later.

---

## Story Group 1 — Forgetting taxonomy becomes executable and explicit

**Outcome:** when this group is done, forgetting is no longer a fuzzy delete path; `suppress`, `expire`, `archive`, `consolidate`, and `destroy` are explicit, distinct operations.

**Independent Test:** I will prove this group by classifying worked fixtures through the taxonomy contract and showing each operation returns the expected bounded state against `prd.md` and `plan.md` Phase 5.

**Checkpoint:** taxonomy is first-class and auditable before any risky merge/destructive path runs.

### T001 — Define forgetting taxonomy contract [X]
- Path: `pkg/cognitive/types.go`, `pkg/cognitive/interfaces.go`, `.agent/specs/memory-product-layer/contracts/forgetting-taxonomy.md`
- TDD: RED — failing contract tests for the 5 operation classes; GREEN — align types/interfaces; REFACTOR — tighten names/comments.
- Test owner: code worker
- Source evidence: PASS `prd.md` scope rows, `plan.md` Phase 5 scope, `architecture.md` Happy path D.
- Data evidence: PASS no destructive path runs yet.
- Confidence gate: no operation may collapse back into a boolean delete semantics.
- AC: taxonomy contract exists and names semantics, boundaries, and required audit surface for each operation.

### T002 — Implement forgetting classification path on existing seams [X]
- Path: bounded service seam plus focused tests; reuse existing candidate/snapshot/audit seams first
- TDD: RED — failing classification tests; GREEN — operation classification path real; REFACTOR — remove duplicate branching.
- Test owner: code worker
- Source evidence: PASS `architecture.md` data-architecture split and existing moderation seams.
- Data evidence: PASS candidate/snapshot/audit reuse, not governance rebuild.
- Confidence gate: if implementation needs a brand-new governance substrate, prove current seams insufficient first.
- AC: low-value / duplicate / risky cases classify into the 5 forgetting operations without destroying data by default.

### G001 — GATE: taxonomy proof [X]
- Path: `.agent/specs/memory-product-layer/evidence/phase-5-taxonomy-gate.json`
- TDD CHECK: taxonomy contract + classification tests green.
- RUN: `go test ./...`
- CHECK: forgetting is explicit, auditable, and not a disguised delete path.
- ENFORCE: no consolidation/destroy path starts before taxonomy is proven.
- SAVE: gate evidence JSON + raw command output.

Dependencies: none.

---

## Story Group 2 — Structural-loss guard and risky packet routing

**Outcome:** when this group is done, risky consolidation or destructive forgetting paths block or escalate through bounded review packets instead of silently collapsing meaning.

**Independent Test:** I will prove this group by running structural-loss fixtures and risky review-packet routing checks against `spec.md` FR-9/FR-10 and `architecture.md` Happy path D.

**Checkpoint:** high-risk forgetting/consolidation can no longer pass as a silent background action.

### T003 — Implement structural-loss guard [X]
- Path: bounded forgetting/consolidation seam + tests + `.agent/specs/memory-product-layer/contracts/forgetting-taxonomy.md`
- TDD: RED — failing structural-loss fixtures; GREEN — guard blocks risky merges/destructive paths; REFACTOR — centralize loss checks.
- Test owner: code worker
- Source evidence: PASS `spec.md` FR-9 FR-10, `plan.md` Phase 5 verification.
- Data evidence: PASS fixtures showing distinct constraints/provenance would be lost.
- Confidence gate: silent destructive merge is forbidden under any fixture that loses unique meaning or provenance.
- AC: structural-loss guard returns explicit block/escalate semantics with rationale.

### T004 — Route risky forgetting/consolidation into bounded review packets [X]
- Path: existing candidate/snapshot/audit extension seam + focused tests/evidence
- TDD: RED — failing risky packet tests; GREEN — risky cases emit bounded packets; REFACTOR — keep packet shape honest and bounded.
- Test owner: code worker
- Source evidence: PASS `architecture.md` exception-surface governance rule.
- Data evidence: PASS candidate/snapshot/audit reuse.
- Confidence gate: operator must review packets, not raw row sludge.
- AC: risky or destructive cases emit bounded packets and safe low-risk actions remain auto-resolvable where allowed.

### G002 — GATE: risky-path proof [X]
- Path: `.agent/specs/memory-product-layer/evidence/phase-5-risk-gate.json`
- TDD CHECK: structural-loss tests + risky packet routing tests green.
- RUN: `go test ./...`
- CHECK: risky forgetting paths escalate through packets with rationale.
- ENFORCE: no temporal-truth slice starts if forgetting still lacks safe escalation behavior.
- SAVE: gate evidence JSON + raw command output.

Dependencies:
- G001 -> T003, T004
- T003 -> T004

---

## Story Group 3 — Audit/export proof and CR boundary

**Outcome:** when this group is done, CR-003 closes with proof that destructive/high-risk forgetting paths leave audit/export evidence and that temporal truth remains later.

**Independent Test:** I will prove this group by writing the audit/export proof artifact and checking closeout evidence against the phase-5 gates.

**Checkpoint:** dangerous forgetting paths are reviewable and exportable; next CR boundary stays explicit.

### T005 — Write audit/export proof for high-risk forgetting paths [X]
- Path: `.agent/specs/memory-product-layer/contracts/forgetting-audit-proof.md`, evidence sidecar if needed
- TDD: N/A — proof artifact.
- Test owner: N/A proof artifact
- Source evidence: PASS `plan.md` Phase 5 verification and PRD forgetting scope.
- Data evidence: PASS outputs from T001-T004.
- Confidence gate: destructive/high-risk paths need named audit/export evidence, not implied logging.
- AC: proof artifact states what audit/export evidence exists, what remains blocked, and why the path is safe enough for this CR.

### G003 — GATE: CR-003 complete [X]
- Path: `.agent/specs/memory-product-layer/evidence/phase-5-closeout.json`
- TDD CHECK: G001 + G002 passed; audit/export proof present.
- RUN: `go test ./...`
- CHECK: forgetting taxonomy, structural-loss guard, and risky packet routing are real; temporal truth remains later.
- ENFORCE: next CR starts only after closeout names the exact boundary.
- SAVE: closeout JSON + proof artifact path + raw command output.

Dependencies:
- G002 -> T005
- T005 -> G003

---

## Follow-up CR boundary

The following slices stay **out of CR-003 scope** and move to later CRs after forgetting/consolidation proof is closed:
- Selective Temporal Truth
- learned applicability model
- broad graph projection
- broad operator-console redesign

Parent roadmap stays unchanged; CR-003 is the bounded Phase 5 slice only.

---

## Dependency Graph

- G001 -> Story Group 2
- G002 -> Story Group 3
- G003 -> CR-003 complete
