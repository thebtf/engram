# Identity Requirements Checklist: Engram Architectural Recovery

**Purpose**: Review whether Project Identity V3 requirements are complete, deterministic, and
safe across clients and historical data.
**Created**: 2026-08-22
**Feature**: `../spec.md`, `../data-model.md`, `../contracts/project-identity-v3.md`
**Review Ownership**: Requirements-quality reviewer. `[x]` never claims implementation completion.

## Requirement Completeness

- [x] CHK001 Are `.engram-project` V3 anchor fields, immutability, tracked-root location, and
      server-key mapping fully specified? [Completeness, Contract §2]
- [x] CHK002 Are worktree, clone, branch, move, remote rename, nested Git, directory scope,
      non-Git, missing, malformed, copied-anchor, and ambiguous-selector cases defined?
      [Coverage, FR-003]
- [x] CHK003 Are all project-bearing tables, role columns, payloads, cache keys, exports/imports,
      and job families required to be inventoried before merge/contraction? [Completeness, FR-006]

## Requirement Clarity and Consistency

- [x] CHK004 Is the distinction between client anchor `project_id`, server mapped
      `anchor_project_id`, and canonical `project_key` unambiguous across all artifacts?
      [Clarity, Data Model §Canonical Project Identity]
- [x] CHK005 Are refusal outcomes explicit enough to prohibit path/name/remote/hash fallback
      creation and cross-project disclosure? [Safety, Contract §Required Resolution Outcomes]
- [x] CHK006 Are Go, JavaScript, OpenClaw, cache, daemon, import/export, and code-index
      compatibility divergences accounted for without claiming they are already resolved?
      [Consistency, Identity Inventory]

## Merge and Sunset Coverage

- [x] CHK007 Are merge evidence rank, target selection, conflict classes, privacy non-widening,
      quarantine, dry-run, apply, repeat-run, and rollback requirements specified? [Coverage,
      FR-007]
- [x] CHK008 Are compatibility-window metrics and sunset conditions measurable with nonzero
      denominators rather than date-only retirement? [Measurability, Compatibility Matrix]
- [x] CHK009 Are operations that require explicit operator approval distinguished from automatic
      resolver behavior? [Authority, Project Merge Contract]

## Notes

- [x] CHK010 Do identity acceptance criteria cover all supported transport/client descriptors with
      one shared field meaning and cross-language test-vector obligation? [Traceability, FR-005]


## AR-0 Review Note

- Result: PASS
- Checked: 10/10.
- Evidence: `contracts/project-identity-v3.md` §§1–8 and `project-anchor-v3.schema.json` define V3 fields, immutability, root/scope rules, typed refusal outcomes, copied-anchor decisions, and the shared descriptor/vector obligation. `evidence/project-identity-merge-inventory.json`, `project-bearing-data-inventory.json`, `current-source-inventory.json`, and `package-route-hook-disposition.json` enumerate Go/JavaScript/OpenClaw, daemon, cache, import/export, and code-index boundaries without claiming runtime convergence; `contracts/legacy-compatibility-matrix.md` and `project-merge-manifest.schema.json` define bounded translation, metrics, approval, dry-run/apply/rollback, quarantine, and privacy constraints.

## R3 Final Review Note

- Result: PASS
- Checked: 10/10.
- Regressions: none.
- Final recheck: The remediation is reflected in the final candidate. V3 anchor/descriptor semantics and refusal behavior remain explicit; the source, package/route/hook, project-bearing, merge, and dual-operator-surface inventories cover the required identity boundaries without claiming runtime proof; compatibility windows require per-family nonzero observation and rollback evidence; and tasks T004/T032/T039/T040/T078-T084 explicitly own import/export, job, cache, adjacent, `ui/`, and `apps/operator-console/` coverage. No identity checklist criterion regressed.


## R4 Review Notes

- Review iteration: AR-0 R4 requirements-quality regression recheck after R2 task/inventory remediation, 2026-08-22.
- Result: **PASS** — 10/10 checked; no regressions identified.
- Rechecked T004/T032/T039/T040/T083 and the identity, merge, project-data, surface, and compatibility inventories; V3 semantics, role/import-export/cache/job coverage, authentication continuity, and privacy-safe merge gates remain explicit.
- Exact regression IDs: none.

## AR-2 Delta Reconciliation

- [x] AR2-CHK011 Does the exact current-source map assign V3 authority to `internal/projectidentity` while correcting hook, OpenClaw, daemon, protobuf, and generated-binding ownership? [AR-2 reconciliation]
- [x] AR2-CHK012 Are the tracked V3 anchor, server-issued key, explicit intents, typed refusals, and no-dual-authority comparison boundary still preserved by the delta? [AR-2 intake AD-1 through AD-8]

- Result: **PASS** — 12/12 requirements-quality checks. The delta records existing V2 divergence without treating it as V3 authority, corrects adapter/protobuf task paths, and retains AR-3 merge/backfill/cutover as out of scope. This is not an implementation-completion claim.
- Per-file checked/total: `identity.md` 12/12 after AR-2 delta reconciliation; historical R4 review remains 10/10.
