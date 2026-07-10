# RELEASE-GATES Foundation Revision 5 — Maker Handoff

Status: `REVIEW_REQUIRED`
Foundation verdict: `READY_FOR_INDEPENDENT_CHECK`
Project-wide production verdict: `BLOCKED`
Exact revision-4 candidate/base: `4812589b9920c187a92a03d210d2e9d5eb53862f`
Revision-4 checker evidence commit: `bd6af3dcd1fa0c2675102a1b91e7c59c8c7c85df`
Revision-4 checker report blob: `27519369cc3e5b79d5188eaf07026cb7076cec13`
Revision-5 implementation commit: `eb44a8e694c60856176c93c64080002231648b4b`
Canonical UTF-8/LF plan SHA256: `d7bcfd122e456d9b764595524292d53b0c99447b7f716a1be0707341e4681bf9`

## Outcome

Revision 5 closes both release-gate false-green classes found by the independent revision-4 checker.

1. Workflow conformance now parses the actual PowerShell AST. It binds exact executable/argument/assignment/function/branch/loop shapes, rejects parse errors, requires one reachable live Docker build and one reachable live Docker Scout command, and rejects comments, dead strings, unreachable branches, duplicate canonical calls, and renamed duplicate calls. The live source/build/pull/post-build/image/launch/scan order and exact image map remain locked.
2. Fresh database identities are now `engram_prc_rg_test_<sha256-prefix>_rN`. The caller run ID is hashed so operator-controlled `prod`, `production`, or `staging` text cannot poison the test-only guard. Every unfiltered canonical `./...` repeat emits a fail-closed proof for the exact 12 required gRPC session-start tests. Pass and fail are both executed terminal outcomes; skip, missing, duplicate, or incomplete is fatal.

The conformance suite rejects 42 permanent mutations. New revision-5 mutations cover comment-only, dead-string, and unreachable build calls; duplicate and renamed-duplicate live build calls; unreachable, duplicate, and renamed-duplicate Scout calls; comment-only/unsafe database assignment; comment-only/bypassed session-start proof; and removal of the literal-test identity.

No product code, product tests, plan/state authority, or v5-demolished graph/rerank/composite-scoring/SDK-extraction/server-HTTP-MCP path was changed.

## Verification

| Gate | Exit | Result |
|---|---:|---|
| Nine production-gate self-tests | 0 | PASS, 9/9 |
| Extracted workflow conformance block | 0 | PASS, 42/42 mutations rejected |
| PowerShell parser | 0 | PASS |
| `actionlint .github/workflows/test.yml` | 0 | PASS |
| `git diff --check` | 0 | PASS |
| `go vet ./...` | 0 | PASS |
| Critical suite | 0 | PASS, 7/7, skip=0 |
| RELEASE-GATES Ledger | 0 | PASS, 48 slices / 325 declarations |
| RELEASE-GATES implementation Diff | 0 | PASS, 2 changed paths / 0 violations |
| Windows path budget at implementation commit | 0 | PASS, 1,405 paths, longest 166, ceiling 240 |
| Actual 66-character fresh detached checkout | 0 | PASS, exact implementation HEAD, `core.longpaths=UNSET`, clean; worktree removed |
| R5 implementation patch gitleaks scan | 0 | PASS, no leak found |
| OpenClaw node matrix | 1 | Expected fail-closed: tracked `package-lock.json` absent; 0 npm steps, pre/post clean, cleanup PASS |
| Canonical full fresh-DB/race/repeat-3 gate | 1 | Expected project RED; foundation invariants below all passed |
| Exact-head dev stand lifecycle | 1 | Up/Ready/Down PASS; Scan correctly failed on exact-image findings; residue zero |

## Exact 12-test execution proof

The canonical command used the shared `engram-prc-postgres` PostgreSQL 17 + pgvector service without stopping or removing it:

```powershell
$env:ENGRAM_TEST_ADMIN_DSN = 'postgres://<redacted>@127.0.0.1:55432/postgres?sslmode=disable'
pwsh -NoProfile -File scripts/production-gates/run-db-suite.ps1 `
  -FreshDatabase -Package ./... -Race -FailOnUnexpectedSkip `
  -Repeat 3 -CoveragePolicy Full `
  -PostgresContainer engram-prc-postgres `
  -PostgresImage pgvector/pgvector:pg17 `
  -ArtifactRoot .agent/e/rg4/r5-maker/runtime `
  -RunId canonical-full-race-repeat3
```

All three repeats produced the same required-test proof:

- expected=12, observed=12, executed=12;
- passed=10, failed=2, skipped=0;
- missing=0, duplicate=0, incomplete=0;
- proof verdict `PASS`;
- generated identities ended in `_r1`, `_r2`, `_r3` and all contained the literal `test` marker;
- sessions_before=0, sessions_after=0, cleanup exit=0 and cleanup verdict `PASS` in every repeat.

The two required tests that reached a real failing terminal outcome were `TestGetSessionStartContext_HappyPath` and `TestGetSessionStartContext_RuleRouterEnabledPacketShape`. They remain visible product blockers; they were not converted to skips or treated as successful product behavior.

## Truthful project-wide RED

The full gate returned FAIL in all three repetitions, as required by the current project state:

- failing tests: 29 / 30 / 29;
- unexpected skips: 13 / 13 / 13;
- overall coverage: 53.35% / 53.35% / 53.35%, below 60%;
- seven package floors remain below contract;
- cleanup exit: 0 / 0 / 0;
- direct SQL after the run found zero `engram_prc_rg_test_%` databases and zero matching sessions;
- shared container remained `/engram-prc-postgres|pgvector/pgvector:pg17|true`.

The stable test/skip inventory and coverage floors are in `fail.json`. Graph T015/T016 failures are recorded as demolition-classification work, not repaired or resurrected by this slice.

## Exact-head dev stand

The lifecycle challenged clean commit `eb44a8e694c60856176c93c64080002231648b4b`:

- Up PASS: source clean, compose build PASS, PostgreSQL pull PASS, launch used `--no-build --pull never`, all three prelaunch image IDs equalled running IDs, three cryptographic credentials were distinct/runtime-injected/not persisted.
- Ready PASS: direct and operator-proxied liveness/readiness endpoints returned HTTP 200 with the required semantic payloads.
- Scan FAIL as expected: immutable `local://sha256:...` references found operator-console=5, PostgreSQL=20, server=13 HIGH/CRITICAL findings.
- Down PASS: zero compose containers, volumes, or networks remained.

## Evidence integrity

`manifest.json` uses the explicit `git-blob-lf` representation. Source entries bind the exact implementation commit, Git blob OID, SHA256 of the raw Git blob bytes, zero CR bytes, and no UTF-8 BOM. This avoids ambiguity from the Windows checkout's `core.autocrlf=true` working-tree representation.

The manifest deliberately excludes itself and `SHA256SUMS`. `SHA256SUMS` hashes `report.md`, `proof.json`, `fail.json`, and `manifest.json`, and deliberately excludes itself. There is no self-hash or manifest/checksum cycle. The final evidence commit cannot name its own commit ID; its exact final candidate SHA is supplied out of band after commit and must have implementation commit `eb44a8e6...` as its direct parent.

## Changed paths

- `.github/workflows/test.yml`
- `scripts/production-gates/run-db-suite.ps1`
- `.agent/e/rg4/r5-maker/report.md`
- `.agent/e/rg4/r5-maker/proof.json`
- `.agent/e/rg4/r5-maker/fail.json`
- `.agent/e/rg4/r5-maker/manifest.json`
- `.agent/e/rg4/r5-maker/SHA256SUMS`

Maker position: `READY_FOR_INDEPENDENT_CHECK` for the release-gate foundation. Independent checker and post-review remain mandatory. Project-wide readiness remains `BLOCKED` by the exact failures, skips, coverage deficits, image findings, existing tracked-tree gitleaks classifications, and missing OpenClaw lock recorded in compact evidence.
