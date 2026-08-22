# Evidence Requirements Checklist: Engram Architectural Recovery

**Purpose**: Review whether acceptance and receipt requirements prove behavior rather than
artifact ceremony.
**Created**: 2026-08-22
**Feature**: `../spec.md`, `../quickstart.md`, `../research.md`
**Review Ownership**: Requirements-quality reviewer. `[x]` means requirements quality only.

## Evidence Pyramid Coverage

- [x] CHK001 Are invariant, cross-language, PostgreSQL integration, migration/rollback, transport,
      fault-injection, corpus, installed dogfood, clean-install/upgrade, and source-scan evidence
      layers all required where relevant? [Completeness, Acceptance Gates]
- [x] CHK002 Does each acceptance gate name observable product behavior, negative cases, fixture
      class, metric source, and installed receipt rather than package/flag existence? [Clarity,
      Constitution XIII]
- [x] CHK003 Are source-only, fixture, installed-dogfood, and production claims explicitly
      distinguished so evidence scope cannot inflate a conclusion? [Consistency, Research §Freshness]

## Measurability and Traceability

- [x] CHK004 Does the pre-task traceability contract require every FR and buildable SC to map to
      generated task IDs, every generated task to map back to a requirement/obligation, and final
      analysis to publish the end-to-end matrix? [Traceability,
      `analysis/traceability-contract.md`]
- [x] CHK005 Are zero denominators, partial coverage, stale evidence, and unknown outcome states
      prohibited from producing green acceptance claims? [Measurability, FR-019]
- [x] CHK006 Are behavioral thresholds tied to a corpus, fixture, or observation window rather
      than invented from current source? [Clarity, SC-005/SC-008/SC-011]

## Receipt Quality

- [x] CHK007 Are release/migration receipts required to identify source/build/installed versions,
      schema, health, counts, quarantines, rollback, observation, and verifier verdict? [Completeness,
      Quickstart]
- [x] CHK008 Are receipt hashes treated as integrity aids rather than behavior proof? [Consistency,
      Constitution XIII]
- [x] CHK009 Are receipts and analysis artifacts prohibited from containing secrets, raw
      credentials, or unredacted private evidence? [Security, Constitution Constraints]

## Notes

- [x] CHK010 Does the evidence model require an independent verifier for material migration,
      security, and final release claims? [Independence, Acceptance Gates]

## Review Notes

- Review iteration: AR-0 requirements-quality review, 2026-08-22.
- Result: **FAIL** — 7/10 checked; 3 findings remain unchecked.
- CHK002 finding: `spec.md` independent tests and `quickstart.md:36-131` define observable scenarios and fixture classes, while `quickstart.md:145-147` defines one aggregate receipt shape; no per-acceptance-gate requirement binds a metric source and installed receipt. Package/flag existence is rejected generally, but gate-level evidence binding is incomplete.
- CHK004 finding: `specs/001-engram-architectural-recovery/tasks.md` is absent and `analysis/` contains only the architecture challenge, not an FR/SC traceability matrix. Traceability is present in selected contracts, but it cannot be claimed consistently through tasks, checklists, and analysis before task generation.
- CHK010 finding: `migration-receipt.schema.json` requires a `verifier` field and `quickstart.md:145-147` requires a verifier verdict, but neither defines independence (separate role/identity, conflict-of-interest rule, or second-party check) for material migration, security, or final-release claims. A receipt can name the same actor as implementer.


## R2 Review Notes

- Review iteration: AR-0 R2 requirements-quality re-review, 2026-08-22.
- Result: **FAIL** — 9/10 checked; exact unmet ID: CHK004.
- CHK002: The plan's per-gate Evidence Gate Matrix binds each gate to observable behavioral evidence, metric/receipt evidence, and a named verifier; the quickstart supplies repository-local fixture/server/scenario/receipt commands, gate-specific negative and corpus cases, and a receipt shape containing input fixture class, built/installed version, metrics, rollback, observation, dogfood, and verifier evidence.
- CHK004 remains unmet: `tasks.md` is still absent and `analysis/` still contains no FR/SC traceability matrix spanning specification, plan, contracts, tasks, checklists, and analysis. No marker was toggled.
- CHK010: The Evidence Gate Matrix now names an independent verifier role for identity/migration, durable work, reconciliation, retrieval/privacy, and contraction/release gates; the migration receipt schema requires verifier provenance, so material migration, security/privacy, and final-release claims cannot rely on an unnamed or purely self-attested check.

### Phase Correction

The original CHK004 wording demanded an already-generated `tasks.md` before the Spec Kit task
stage, creating an unsatisfiable pre-task checklist condition. It is replaced with the equivalent
pre-task requirements-quality criterion above: the binding traceability contract must require the
post-task matrix and final analyzer gate. This does not relax end-to-end traceability; final
analysis must still reject any missing FR/SC-to-task or task-to-requirement mapping.


## R3 Review Notes

- Review iteration: AR-0 R3 requirements-quality re-review, 2026-08-22.
- Result: **PASS** — `analysis/traceability-contract.md` rules 1–3 require FR/buildable-SC-to-generated-task coverage, task-to-requirement/obligation citations, and final exact matrix, unmapped-task list, coverage denominator, and zero CRITICAL/HIGH findings. The Spec Kit tasks contract generates tasks from spec/plan/contracts, while the analyze contract requires `tasks.md` first and reports requirement coverage and unmapped tasks; `spec.md` and `plan.md` provide the FR/SC and release-obligation sets. The pre-task wording therefore preserves a mandatory eventual end-to-end traceability gate without requiring `tasks.md` before task generation.

## Final Review Notes

- Review iteration: AR-0 final requirements-quality recheck after R1 remediation and task generation, 2026-08-22.
- Result: **PASS** — 10/10 checked; no regressions identified.
- Rechecked the final evidence gate matrix, traceability contract/matrix, generated tasks, operational acceptance policy, processing/retrieval/outcome contracts, migration receipt schema, quickstart, and research evidence boundaries. Gate-level behavior/negative/fixture/metric/receipt bindings, exact task traceability, zero-denominator handling, scope-labeled evidence, receipt integrity, redaction, and independent verifier roles are present.
- Exact regression IDs: none.
- Per-file checked/total: `evidence.md` 10/10.

## R4 Review Notes

- Review iteration: AR-0 R4 requirements-quality regression recheck after R2 changes, 2026-08-22.
- Result: **PASS** — 10/10 checked; no regressions identified.
- Rechecked T074/T075/T076 gate evidence and the synchronized FR/SC traceability matrix: both emergency branches, active render-budget policy, cutover dependency, installed/rollback receipts, and exact task mappings remain explicit and non-inflating.
- Exact regression IDs: none.
- Per-file checked/total: `evidence.md` 10/10.
