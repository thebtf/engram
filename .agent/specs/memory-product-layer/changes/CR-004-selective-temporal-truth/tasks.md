# Tasks — ENG-MPL-1 / CR-004-selective-temporal-truth

**Feature:** `memory-product-layer`  
**CR:** `CR-004-selective-temporal-truth`  
**Source artifacts:** `spec.md`, `plan.md`, `architecture.md`, `milestone-map.md`, `prd.md`, `changes/CR-004-selective-temporal-truth/change.md`  
**Task mode:** CR-scoped  
**MVP for this CR:** land narrow temporal-truth contract + validity/invalidation history + provenance-first retrieval + explicit narrow-scope proof. No broad graphification.

---

## Story Group 1 — Temporal truth becomes explicit for a narrow fact set

**Outcome:** when this group is done, selected evolving facts can answer `true now vs then` with provenance and validity history.

**Independent Test:** I will prove this group by querying a bounded temporal truth path over selected fixtures and checking results against `spec.md` and `plan.md` Phase 6.

**Checkpoint:** temporal truth is real, queryable, and still narrow.

### T001 — Define temporal truth contract [X]
- Path: `pkg/cognitive/types.go`, `pkg/cognitive/interfaces.go`, `.agent/specs/memory-product-layer/contracts/temporal-truth.md`
- TDD: RED — failing contract tests for current truth, prior truth, validity windows, invalidation rationale, and provenance; GREEN — align types/interfaces; REFACTOR — tighten names/comments.
- Test owner: code worker
- Source evidence: PASS `plan.md` Phase 6, `design.md` MPL-6 guard, `architecture.md` proposed temporal-truth family.
- Data evidence: PASS no graph-wide assumption yet.
- Confidence gate: the contract must name a selected-fact scope, not universal truth graph semantics.
- AC: temporal truth contract exists with bounded fields for `true now`, `true then`, provenance, and invalidation history.

### T002 — Implement bounded temporal-truth query path [X]
- Path: minimal live seam for temporal truth service + focused tests
- TDD: RED — failing temporal query tests; GREEN — query path returns bounded results; REFACTOR — remove duplicate mapping.
- Test owner: code worker
- Source evidence: PASS `architecture.md` Happy path + data-architecture sections.
- Data evidence: PASS selected-fact scope only.
- Confidence gate: implementation may not broaden into general graph traversal or broad cross-domain truth inference.
- AC: caller can query selected facts and receive current truth plus prior validity context.

### G001 — GATE: temporal contract proof [X]
- Path: `.agent/specs/memory-product-layer/evidence/phase-6-temporal-gate.json`
- TDD CHECK: contract tests + temporal query tests green.
- RUN: `go test ./...`
- CHECK: temporal truth is explicit, provenance-first, and bounded.
- ENFORCE: no graph-expansion work starts before the bounded contract is proven.
- SAVE: gate evidence JSON + raw command output.

Dependencies: none.

---

## Story Group 2 — Validity/invalidation history stays provenance-first

**Outcome:** when this group is done, truth changes are visible as bounded history with explicit invalidation rationale and provenance instead of silent overwrite.

**Independent Test:** I will prove this group by replaying truth-change fixtures and checking that invalidation history and provenance survive the read path.

**Checkpoint:** truth evolution is inspectable without opening a broad graph.

### T003 — Implement validity/invalidation history [X]
- Path: temporal-truth seam + tests + `.agent/specs/memory-product-layer/contracts/temporal-truth.md`
- TDD: RED — failing validity/invalidation fixtures; GREEN — history survives the read path; REFACTOR — centralize history shaping.
- Test owner: code worker
- Source evidence: PASS `plan.md` Phase 6 scope, `architecture.md` proposed temporal-truth split.
- Data evidence: PASS provenance-first retrieval.
- Confidence gate: invalidation must keep rationale and provenance, not just timestamps.
- AC: truth changes expose prior value, current value, invalidation rationale, and provenance chain.

### G002 — GATE: validity history proof [X]
- Path: `.agent/specs/memory-product-layer/evidence/phase-6-history-gate.json`
- TDD CHECK: validity/invalidation fixtures green.
- RUN: `go test ./...`
- CHECK: truth evolution is visible and provenance-first.
- ENFORCE: no broad graph claims before history proof is real.
- SAVE: gate evidence JSON + raw command output.

Dependencies:
- G001 -> T003

---

## Story Group 3 — Narrow-scope proof and CR boundary

**Outcome:** when this group is done, CR-004 closes with explicit proof that temporal truth stayed narrow and did not drift into broad graphification.

**Independent Test:** I will prove this group by writing the narrow-scope proof artifact and checking closeout evidence against the temporal gates.

**Checkpoint:** temporal truth remains a selected-fact slice, not a graph-first detour.

### T004 — Write narrow-scope proof artifact [X]
- Path: `.agent/specs/memory-product-layer/contracts/temporal-truth-scope-proof.md`
- TDD: N/A — proof artifact.
- Test owner: N/A proof artifact
- Source evidence: PASS `design.md` MPL-6 guard and `plan.md` Phase 6 scope.
- Data evidence: PASS outputs from T001-T003.
- Confidence gate: proof must name what was included and what stayed out.
- AC: proof artifact states selected fact classes, excluded graph work, and why the CR stayed narrow.

### G003 — GATE: CR-004 complete [X]
- Path: `.agent/specs/memory-product-layer/evidence/phase-6-closeout.json`
- TDD CHECK: G001 + G002 passed; narrow-scope proof present.
- RUN: `go test ./...`
- CHECK: temporal truth is queryable, provenance-first, and intentionally narrow.
- ENFORCE: roadmap moves only after closeout names the exact remaining exclusions.
- SAVE: closeout JSON + proof artifact path + raw command output.

Dependencies:
- G002 -> T004
- T004 -> G003

---

## Follow-up CR boundary

The following slices stay **out of CR-004 scope** and remain deferred unless the roadmap is amended:
- learned applicability model
- broad graph projection
- broad operator-console redesign
- full company-brain ingestion platform

Parent roadmap stays unchanged; CR-004 is the bounded Phase 6 slice only.

---

## Dependency Graph

- G001 -> Story Group 2
- G002 -> Story Group 3
- G003 -> CR-004 complete
