# PLAN-GOVERNANCE-R8 maker report

Verdict: `PASS_PENDING_INDEPENDENT_CHECKER`

## Authority

- Reconstruction base: `d59d1605969b1f567506e96ded524dfd1e4be08a`.
- Rejected R7 plan: `a99ce0dfe4d415f90c0f192cbf96bd88710a48f5`.
- Diagnostic R7 release-gates source: `144eeefa003c3e1c0009c4264f41236ee3453b65`.
- R7 checker: `d8ba52d29f1c7f2169e3f76576248ab32d3b6646` (`REVISE`).
- Register freeze provenance: `AB5F882FA110CA823A317061ECBCA0C62516702735325893A56206F9E7A29415`, `updated_at=2026-07-10T22:46:01.2938194+03:00`, 67 rows / 67 unique slices.
- Canonical UTF-8/LF plan SHA256: `fd2b223a9a62848efc39e1c33bf739bada191508bccb7ba9a73140185638e43d`.
- Scope-map SHA256: `81093184036672008d6b85dfa88a431998ef70b587ab11475aa2b315f03ddf79`.

The register SHA and timestamp freeze provenance only. Live conformance uses the exact unique slice set, classifications, literal owner/fold targets, required plan rows/epochs, and the four explicitly marked rejected-head policies. Same-lane status/head progress plus timestamp, command, artifact, and notes changes do not self-invalidate the plan.

## Reconciliation

- Restored all eight omitted predecessor obligations.
- Added all six newly explicit implementation/evidence lanes.
- Mapped all 67 register slices: 56 maker, 4 checker-evidence, 4 meta-fold, 1 historical, and 2 root-integration.
- Kept `MCP-STRUCTURED-INPUT-CLASSIFICATION` and `SECURITY-REVIEW` as plan-only authority rows.
- Reconciled the accepted DB behavioral-edge head, pending pool-hygiene stack, immutable DB embedding product head, rejected interim embedding evidence, and both confirmed SECURITY-PROJECT-IDENTITY R2 blockers.
- Expanded the ownership state from 34 to 36 epochs for `internal/mcp/tools_memory.go` and `docs/operating-engram.md`; updated `candidate_store_test.go` and `internal/worker/service.go` serialization.

## Verification

`assert-plan-path-ownership.ps1 -SelfTest` passed. Ledger passed with 57 parsed maker rows, 333 declarations, 34 repeated exact paths, 36 declared/state epochs, and zero errors. Scope parity passed at 67/67 with no missing owner or fold target. Full machine-readable evidence is under `.agent/specs/release-gates-r8/evidence/plan-governance/`.

TDD is not applicable to this first commit because it changes only governance data and reports. Executable RED/GREEN/Prove-It work belongs to the directly stacked RELEASE-GATES-R8 commit.

This is maker evidence, not checker acceptance, post-review, integration, or release authorization.
