# RELEASE-GATES Foundation Revision 4 — Maker Handoff

Status: `REVIEW_REQUIRED`
Foundation/conformance verdict: `READY_FOR_INDEPENDENT_CHECK`
Project-wide release verdict: `BLOCKED`
Rejected predecessor/base: `586b39df3465fb51779cf9225deaedbc212e4f9f`
Plan authority commit: `f987ce16ee1a0777793bc95113edd2885b19e202`
Implementation commit: `46ccf27968055a670f2b89907cc6da4478ac04fd`
Canonical UTF-8/LF plan SHA256: `d7bcfd122e456d9b764595524292d53b0c99447b7f716a1be0707341e4681bf9`

## Outcome

Revision 4 closes the five independent-check rejection classes in the release-gate foundation:

1. Plan authority is now hashed after UTF-8/no-BOM and LF canonicalization. LF and CRLF checkout forms produce the same authority hash; a semantic mutation does not.
2. Workflow conformance now rejects removal or reordering of the explicit source build and rejects removal of `--no-build` from launch. The runtime chain is clean source commit -> server/operator build plus PostgreSQL pull -> post-build source recheck -> prelaunch image IDs -> `up --no-build --pull never` -> running IDs -> immutable-ID Scout inputs.
3. The master plan contains an exact, fail-closed `CRYSTALLIZATION-DREAM-CYCLE-CORRECTNESS` lane on `internal/worker/dream_cycle.go` and `internal/worker/dream_cycle_test.go`; store expansion requires a prior root amendment. The lane forbids resurrection of the v5-demolished direct-memory path.
4. The OpenClaw/ingest source lock is the actual root-owned artifact hash `A095E9D7B69DC95CAC4022EB97D2EA9B403D5132F5602FDD85E7D3A93092F5D4`.
5. The overlong revision-3 raw evidence tree and stale maker report were removed. A deterministic 66-character-prefix path-budget gate is tracked and the compact revision-4 evidence is under `.agent/e/rg4/`.

This is not a project-wide PASS. The foundation fails closed and exposes the remaining product, test, image, OpenClaw, skip, and coverage blockers instead of suppressing them.

## TDD and adversarial proof

- RED: the old ownership runner rejected the single-owner dream epoch; raw LF and CRLF hashes differed.
- GREEN: ownership selftest proves LF == CRLF, semantic mutation != authority hash, and singleton plan/state epochs are valid.
- RED: before compaction, a 66-character checkout prefix produced length 262 with 73 tracked-path violations against the 240 ceiling.
- GREEN: implementation head has 1,402 tracked paths, longest combined length 166, zero violations.
- RED: conformance accepted the old stand without a source/build provenance contract.
- GREEN: conformance rejects 30 mutations, including removed build, build moved after launch, removed `--no-build`, wrapper removals, narrowed packages, changed race/count/coverage semantics, CRLF drift, and authority/state corruption.
- A bounded maker/checker review found wrong-type Boolean coercion, weak Scan linkage, order-only false greens, and untracked source contamination. The fixes use strict Boolean types, exact per-service scan/map/command binding, ordered conformance, `--untracked-files=all`, and a post-build HEAD/status recheck. Re-review found no blocker in that bounded scope.

## Foundation evidence

- Nine gate selftests: PASS.
- PowerShell parser: PASS for all changed scripts.
- `actionlint .github/workflows/test.yml`: PASS.
- Workflow conformance: PASS; 30 mutations rejected.
- Ownership Ledger: PASS; 48 slices, 325 declarations, 34 epochs.
- PLAN-GOVERNANCE Diff `586b39df..f987ce16`: PASS; 2 changed paths, zero violations.
- RELEASE-GATES Diff `f987ce16..46ccf279`: PASS; 135 changed paths, zero violations.
- Critical suite: PASS; 7/7 tests, zero failures/skips/malformed JSON.
- Dev stand on clean `46ccf279`: Up PASS, Ready PASS, Down PASS, zero residual resources. Build/pull/no-build and prelaunch/running identity all passed.
- Scout expected-negative used exact immutable image IDs: operator-console 5 findings, PostgreSQL 20, server 13. These are release blockers owned by image remediation, not foundation false greens.
- OpenClaw matrix expected-negative stopped before npm because the required tracked `package-lock.json` is absent; pre/post surface clean and cleanup passed.

Compact machine evidence: `.agent/e/rg4/proof.json`.

## Truthful project-wide RED

The canonical full gate was executed without scope reduction:

```powershell
pwsh ./scripts/production-gates/run-db-suite.ps1 `
  -FreshDatabase -Package ./... -Race -FailOnUnexpectedSkip `
  -Repeat 3 -PostgresContainer <shared-pg17> `
  -PostgresImage pgvector/pgvector:pg17
```

It returned FAIL in all three repetitions:

- test failures: 27 / 28 / 27;
- unexpected skips: 25 in every repetition;
- overall coverage: 53.00 / 52.99 / 52.99, below 60;
- seven package coverage floors remain below contract;
- cleanup exit: 0 / 0 / 0;
- direct SQL after the run: zero `engram_prc_rg_%` databases and zero matching sessions;
- the operator-owned PostgreSQL container remained running.

The exact test, skip, and coverage inventory is `.agent/e/rg4/fail.json`. No allowlist, skip suppression, threshold reduction, or out-of-scope product patch was introduced.

## Changed foundation surfaces

- `.agent/plans/2026-07-10-engram-production-ready-master-plan.md`
- `.agent/plans/2026-07-10-engram-production-ready-ownership-state.json`
- `.github/workflows/test.yml`
- `scripts/production-gates/assert-plan-path-ownership.ps1`
- `scripts/production-gates/assert-windows-path-budget.ps1`
- `scripts/production-gates/run-db-suite.ps1`
- `scripts/production-gates/run-dev-stand.ps1`
- compact revision-4 report/evidence paths declared by the plan
- deletion of the declared legacy revision-3 maker report/evidence prefix

## Independent checker commands

Use the final candidate SHA from `git rev-parse HEAD`; the report cannot embed the hash of the commit that contains itself. The maker must provide that exact SHA out of band after this report commit and prove it from a clean worktree.

```powershell
$Plan = '.agent/plans/2026-07-10-engram-production-ready-master-plan.md'
$State = '.agent/plans/2026-07-10-engram-production-ready-ownership-state.json'
$PlanSha = pwsh ./scripts/production-gates/assert-plan-path-ownership.ps1 -Plan $Plan -PrintCanonicalPlanSha256
$Head = git rev-parse HEAD

pwsh ./scripts/production-gates/assert-plan-path-ownership.ps1 -SelfTest
pwsh ./scripts/production-gates/assert-windows-path-budget.ps1 -SelfTest
pwsh ./scripts/production-gates/run-db-suite.ps1 -SelfTest
pwsh ./scripts/production-gates/run-dev-stand.ps1 -Config .agent/dev-stand.config.yaml -SelfTest
actionlint .github/workflows/test.yml

pwsh ./scripts/production-gates/assert-plan-path-ownership.ps1 `
  -Mode Ledger -Plan $Plan -ExpectedPlanSha256 $PlanSha -State $State `
  -Artifact .agent/e/rg4-check/ledger.json

pwsh ./scripts/production-gates/assert-plan-path-ownership.ps1 `
  -Mode Diff -Slice PLAN-GOVERNANCE `
  -Base 586b39df3465fb51779cf9225deaedbc212e4f9f `
  -Head f987ce16ee1a0777793bc95113edd2885b19e202 `
  -EvidenceNamespace '.agent/reports/evidence/production-ready/plan-governance/**' `
  -ReportNamespace '.agent/reports/production-ready/plan-governance/**' `
  -Plan $Plan -ExpectedPlanSha256 $PlanSha -State $State `
  -Artifact .agent/e/rg4-check/diff-plan.json

pwsh ./scripts/production-gates/assert-plan-path-ownership.ps1 `
  -Mode Diff -Slice RELEASE-GATES `
  -Base f987ce16ee1a0777793bc95113edd2885b19e202 -Head $Head `
  -EvidenceNamespace '.agent/e/rg4/**' `
  -ReportNamespace .agent/reports/2026-07-10-release-gates-foundation-revision-4-maker.md `
  -Plan $Plan -ExpectedPlanSha256 $PlanSha -State $State `
  -Artifact .agent/e/rg4-check/diff-gate.json

pwsh ./scripts/production-gates/assert-windows-path-budget.ps1 `
  -Repository . -Ref $Head -CheckoutPrefixLength 66 `
  -MaximumCombinedPathLength 240 -Artifact .agent/e/rg4-check/path.json
```

The checker must also extract and run the workflow conformance block, challenge strict Boolean types, duplicate/mismatched Scan services and IDs, late/dead build tokens, `--no-build` removal, CRLF checkout mutation, and an actual fresh 66-character-prefix checkout with `core.longpaths` unset/false.

## Handoff

Maker position: `READY_FOR_INDEPENDENT_CHECK` for the revision-4 foundation only. Independent checker and post-review remain mandatory. Project-wide production readiness remains `BLOCKED` by `.agent/e/rg4/fail.json`, exact image findings, and the missing OpenClaw lock/release lane.
