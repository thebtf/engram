# RELEASE-GATES Revision 5 Maker Evidence

Maker verdict: `READY_FOR_INDEPENDENT_CHECK` for the release-gate foundation only.

Project release verdict: `BLOCKED`. The gate now reports the current failures, skips, coverage deficits, image vulnerabilities, OpenClaw lock defect, and secret-scan classifications without converting them into success.

## Authority

- Challenged revision-4 candidate/base: `4812589b9920c187a92a03d210d2e9d5eb53862f` (tree `764fd987cc606754f037de0647f8a10e7080adc2`).
- Revision-4 independent checker: commit `bd6af3dcd1fa0c2675102a1b91e7c59c8c7c85df`, report blob `27519369cc3e5b79d5188eaf07026cb7076cec13`.
- Primary revision-5 implementation: `eb44a8e694c60856176c93c64080002231648b4b`, direct parent `4812589b9920c187a92a03d210d2e9d5eb53862f`.
- Superseded evidence commit: `135e4a0a180f112906e89c211f976ce519212347`. Its evidence predates the static pre-review correction and is not final authority.
- Static pre-review correction and effective implementation head: `5dd3e3c4e2f87a52a44465d8a3a63d3b68a55f65`, direct parent `135e4a0a180f112906e89c211f976ce519212347`, tree `5f6465deaebcd0899e27af231550726f2adc37f6`.
- Canonical plan SHA256: `d7bcfd122e456d9b764595524292d53b0c99447b7f716a1be0707341e4681bf9`.

The branch is `work/prc-release-gates-revision5-maker` in `D:/Dev/engram/.agent/worktrees/prc-release-gates-r5-maker`. No merge, push, tag, release, or primary-worktree write was performed.

## Closed false-green classes

1. The workflow conformance gate now parses the PowerShell AST and proves the source-built compose invocation and each exact-image Docker Scout invocation are reachable, structurally correct, and present exactly once. Text in comments, strings, or unreachable blocks cannot satisfy the contract.
2. Fresh databases use an unambiguous test-only identity, `engram_prc_rg_test_<16-lower-hex>_rN`. Every repeat emits an exact execution proof for the required session-start package and rejects skip, missing, duplicate, incomplete, wrong-package, or substituted-test inventory.
3. Root static pre-review found that the first revision-5 self-test derived synthetic events from the same mutable inventory it challenged. Commit `5dd3e3c4...` fixed this by independently pinning the exact package and ordered 12-test list in both the runner self-test and workflow conformance. Permanent mutations now substitute one test identity and the package; both are rejected.

No product failure, skip, coverage floor, vulnerability, or OpenClaw precondition was allowlisted or suppressed. No v5-demolished graph/rerank/scoring behavior was restored.

## Foundation verification at effective implementation head

| Gate | Exit | Result |
|---|---:|---|
| Nine production-gate self-tests | 0 | PASS 9/9 |
| Extracted workflow conformance | 0 | PASS; PowerShell AST; 44 mutations rejected |
| PowerShell parse: DB runner + extracted conformance block | 0 | PASS |
| actionlint | 0 | PASS |
| `git diff --check 4812589b...5dd3e3c4` | 0 | PASS |
| `go vet ./...` | 0 | PASS |
| `go build ./...` | 0 | PASS |
| gitleaks over both implementation files, base through effective head | 0 | PASS; 0 findings |
| Critical suite | 0 | PASS 7/7; skip=0 |
| Ownership Ledger | 0 | PASS; 48 slices, 325 declarations |
| Ownership Diff at code head | 0 | PASS; RELEASE-GATES; 7 changed paths, 0 violations |
| Windows tracked path budget | 0 | PASS; 1,410 paths, longest=166, ceiling=240 |
| Actual 66-character fresh checkout | 0 | PASS; exact head, clean, `core.longpaths=UNSET`, removed |

Critical-suite raw summary SHA256: `C696FEDF8898460DADC89871905AD73F69A0360E8764526584B80BFB4554754E`.

## Canonical full fresh-DB race proof

The shared `engram-prc-postgres` PostgreSQL 17 + pgvector service was used without stopping or removing it:

```powershell
$env:ENGRAM_TEST_ADMIN_DSN = 'postgres://<redacted>@127.0.0.1:55432/postgres?sslmode=disable'
pwsh -NoProfile -File scripts/production-gates/run-db-suite.ps1 `
  -FreshDatabase -Package ./... -Race -FailOnUnexpectedSkip `
  -Repeat 3 -CoveragePolicy Full `
  -PostgresContainer engram-prc-postgres `
  -PostgresImage pgvector/pgvector:pg17 `
  -ArtifactRoot .agent/e/rg4/r5-maker/runtime `
  -RunId canonical-full-race-repeat3-r5-inventory-pin
```

The command exited 1 because the project is red, not because the release gate lost execution proof.

- Required package: `github.com/thebtf/engram/internal/grpcserver`.
- Each repeat: expected=12, observed=12, executed=12, passed=10, failed=2, skipped=0, missing=0, duplicate=0, incomplete=0, proof verdict `PASS`.
- The two executed product failures were `TestGetSessionStartContext_HappyPath` and `TestGetSessionStartContext_RuleRouterEnabledPacketShape`.
- Fresh identities were `engram_prc_rg_test_fc4f603aeda0760d_r1`, `_r2`, and `_r3`.
- Tests observed: 3,516 / 3,516 / 3,516.
- Passed: 3,472 / 3,471 / 3,473.
- Failed: 30 / 29 / 30. Twenty-nine were stable; `internal/bulkops::TestRollback_Conflict_EC_F3` additionally failed in repeats 1 and 3.
- Unexpected skips: 13 / 13 / 13.
- General incomplete terminal records: 1 / 3 / 0; their exact identities are retained in `fail.json`.
- Overall coverage: 53.33% / 53.35% / 53.35%, below the immutable 60% floor. Seven package floors remain red.
- Cleanup exit: 0 / 0 / 0; targeted sessions before/after: 0 / 0 / 0.

Direct post-run checks found zero `engram_prc_rg_test_%` databases, zero matching PostgreSQL sessions, and zero `engram-critical-stand` containers, volumes, or networks. The shared service remained `/engram-prc-postgres|pgvector/pgvector:pg17|true`.

Canonical summary SHA256: `4B98F3D1E296440DFDC1A6023D4EDAE3B6D4B5255083DB666CC0B8CC8339BE15`. Each required-execution proof has SHA256 `0AF9EBDF4B242ED394D13CE4AB410F7A94E7A0A367B66BB0ABBF1C177DDD0316`.

## Exact-head dev stand

The lifecycle challenged clean source commit `5dd3e3c4e2f87a52a44465d8a3a63d3b68a55f65`.

- Up PASS: compose build and PostgreSQL pull completed; launch used no build/pull; all prelaunch, tag, running, and scanned IDs matched; three generated credentials were distinct, runtime-injected, and not persisted.
- Ready PASS: direct and operator-proxied liveness/readiness endpoints returned HTTP 200 with the required semantic payloads.
- Scan correctly failed on immutable local image IDs: operator-console `sha256:d701b9ace90ed8b689f45b90fbdf87ed1c9b2a81b7a237fa5d7fd909405df9f9` = 5 findings; PostgreSQL `sha256:d2ef61f42ef767baa5a1475393303cc235bcd92febd9d7014eddb48b41f3bad0` = 20; server `sha256:c75d6cd0fd5fd5a569d1c5c4eca0200e4c9de3374dbf7d316390c064af9e4bf0` = 13.
- Down PASS: residual compose resources were zero.

Lifecycle/up/ready/scan/down SHA256 values are recorded in `proof.json`.

## Other release blockers

- OpenClaw matrix exited 1 before npm execution because tracked `plugin/openclaw-engram/package-lock.json` is absent. Pre/post surfaces were clean and cleanup passed.
- Current whole-working-tree gitleaks diagnostic reports 62 findings: 48 in ignored raw runtime evidence from the two canonical diagnostic runs and 14 in existing tracked fixtures/docs/scripts. Its redacted report SHA256 is `60FC8185A4E4207129D6076AF27C2E6E58922D34720A0E19C266C6BC7A3D6F42`. This is release classification work; the revision-5 implementation diff itself has zero findings.
- Product failures, skips, incomplete records, coverage floors, image findings, OpenClaw lock, and secret classifications remain routed to their declared master-plan lanes. This maker did not repair or reclassify them.

## Evidence integrity and changed paths

`manifest.json` uses exact `git-blob-lf` bytes for both source files and the three compact evidence files. The manifest excludes itself and `SHA256SUMS`; `SHA256SUMS` hashes the finalized report, proof, fail inventory, and manifest while excluding itself. The final evidence commit cannot contain its own commit ID, so its exact SHA is supplied out of band and must have `5dd3e3c4...` as direct parent.

Changed paths relative to the challenged base:

- `.github/workflows/test.yml`
- `scripts/production-gates/run-db-suite.ps1`
- `.agent/e/rg4/r5-maker/report.md`
- `.agent/e/rg4/r5-maker/proof.json`
- `.agent/e/rg4/r5-maker/fail.json`
- `.agent/e/rg4/r5-maker/manifest.json`
- `.agent/e/rg4/r5-maker/SHA256SUMS`

Independent checker and post-review remain mandatory. This maker handoff does not claim project production readiness.
