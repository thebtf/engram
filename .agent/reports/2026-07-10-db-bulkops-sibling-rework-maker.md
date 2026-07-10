# DB-BULKOPS Sibling-Path Rework — Maker Report

## Outcome

All three findings in the independent acceptance check are closed in the bounded
rework: live candidate-review snapshots now persist exact candidate `After` state,
nil-facade MCP dry-run uses the same ID normalization as execution, and an all-row
promotion failure emits an explicit failure audit instead of a success-shaped one.

Status: **READY FOR AN INDEPENDENT CHECKER AND POST-RUN CODE REVIEW**.

This is maker evidence only. The exact commit containing this report is supplied in
the handoff because a commit cannot embed its own final SHA. No push, integration,
role/oracle update, plan mutation, or release action was performed.

## Scope and live-path classification

- Worktree: `D:\Dev\engram\.agent\worktrees\prc-db-bulkops`
- Branch: `work/prc-db-bulkops`
- Rework base: `6ea10496aa127fba7fdb194875044e770d0a1d8c`
- Product paths changed:
  - `internal/db/gorm/candidate_store.go`
  - `internal/bulkops/facade.go`
  - `internal/mcp/tools_bulkops.go`
- Regression paths changed:
  - `internal/bulkops/facade_test.go`
  - `internal/bulkops/rollback_test.go`
  - `internal/db/gorm/candidate_store_test.go`
  - `internal/mcp/tools_dryrun_test.go`
- Evidence is confined to
  `.agent/reports/evidence/production-ready/db-bulkops-sibling-rework/`.

H1 is live: public MCP/HTTP candidate-review handlers call
`NewCandidateReviewActionSnapshot` and the exported atomic `*WithSnapshot` store
methods under the live `ENGRAM_VNEXT_F_ENABLED` surface. M1 is live through
`server.callTool -> handleBulkPromote`; the nil-facade seam is explicitly supported.
M2 is live through facade execution and its operator-facing audit stream. None is a
v5-demolition tombstone, an unset-only dormant scaffold, or a removed HTTP MCP path.

## H1 — exact candidate After state on every live review action

`promoteWithMemoryAndSnapshotAction` and `transitionWithSnapshot` now call one
transactional helper after the authoritative locked mutation and database re-read.
The helper locks the snapshot row, requires a `candidate_review_action` snapshot,
requires its exact `candidate:<id>` restore entry, marshals the authoritative
candidate into `SnapshotEntry.After`, and updates the JSONB snapshot in the same
transaction before audit and commit. A missing or malformed entry is an error, so the
candidate mutation cannot commit without its rollback comparator state.

The historical rule remains unchanged: genuinely old candidate entries without
`After` still fail closed during rollback.

Permanent public-path regressions cover:

- promote and preserve, including promoted-memory deletion during rollback;
- reject, supersede, and suppress;
- exact equality between persisted `After` and the authoritative candidate returned
  by the store;
- rollback to pending without any fixture-side snapshot SQL amendment.

The pre-existing candidate-store tests had a before-only hand-built snapshot fixture.
The full-package run correctly exposed that stale test setup after the production
contract became strict. The helper now uses the real public
`reviewpacket.NewCandidateReviewActionSnapshot` constructor; all four affected store
tests pass, including the forced audit-failure rollback case. No production validation
was weakened.

## M1 — one normalization contract across MCP and facade

The facade's sorted, unique, non-zero normalization is exposed inside the package as
`NormalizeCandidateIDs`. `handleBulkPromote` applies it immediately after coercion,
before both the supported nil-facade preview branch and the wired facade branch.

`TestBulkPromote_DryRun_NilFacade` now sends `[2,0,1,2,1,0]` and requires
`would_affect=2`, pinning the exact sibling-path contract.

## M2 — explicit zero-success audit semantics

When execution has `AffectedCount=0` and row errors, the facade now writes
`action=bulk_promote_failed` with attempted, affected, and failed counts. It still
creates no rollback snapshot. Successful and partial-success operations retain the
existing `action=bulk_promote` summary.

`TestFacade_BulkPromote_AllRowsFailWritesExplicitFailureAudit` uses one rejected and
one missing candidate and proves zero affected rows, no snapshot, no success-shaped
audit, and exactly one explicit failure audit.

## TDD and prove-it evidence

Durable evidence:

- `H1-candidate-review-after.red.json`
- `M1-nil-facade-normalization.red.json`
- `M2-all-row-failure-audit.red.json`
- `DB-BULKOPS-SIBLING-REWORK.tdd.json`
- `DB-BULKOPS-SIBLING-REWORK.final.json`

Valid RED runs reproduced each checker finding before production edits. The first H1
attempt was discarded because the new test lacked an import and did not compile; it
is not presented as behavioral RED. One later fixture run was also discarded after a
PowerShell interpolation mistake produced an invalid DSN; the exact test database was
dropped and the corrected run passed.

Six temporary prove-it sentinels independently broke the two candidate-store mutation
paths, the snapshot amendment helper, the shared ID normalizer, the MCP handler, and
the facade audit path. In every case the permanent regression failed, and after an
exact restore from the interim commit the same test passed.

High-risk repeat:

```text
go test -p=1 ./internal/bulkops ./internal/mcp \
  -run '^(TestRollback_PublicCandidateReview(PromotePersistsAfterAndRestoresPending|PreservePersistsAfterAndRestoresPending|NonMemoryActionsPersistAfterAndRestorePending)|TestFacade_BulkPromote_AllRowsFailWritesExplicitFailureAudit|TestBulkPromote_DryRun_NilFacade)$' \
  -count=20
PASS — 7 scenarios x 20 = 140 executions
bulkops 27.940s; mcp 0.095s
```

## Final scoped verification

Fresh PostgreSQL 17 database
`engram_prc_bulkops_sibling_scoped_20260710_101019337`:

```text
go test -p=1 ./internal/bulkops -count=1 -coverprofile .../bulkops.cover.out
PASS — 8.527s — 78.1% statements

go test -p=1 ./internal/db/gorm -run '<six candidate-review store tests>' -count=1
PASS — 0.810s

go test -p=1 ./internal/mcp -run '<three bulk-promote surface tests>' -count=1
PASS — 0.160s

go test -p=1 ./internal/reviewpacket ./pkg/models -count=1
PASS
```

Fresh race database `engram_prc_bulkops_sibling_race_20260710_101104510`:

```text
go test -race -p=1 ./internal/bulkops -count=1
PASS — 10.915s

go test -race -p=1 ./internal/db/gorm -run '<six candidate-review store tests>' -count=1
PASS — 2.037s

go test -race -p=1 ./internal/mcp -run '<three bulk-promote surface tests>' -count=1
PASS — 1.087s
```

Additional gates:

```text
go vet ./internal/bulkops ./internal/db/gorm ./internal/mcp ./internal/reviewpacket ./pkg/models
PASS

Serena diagnostics on all seven changed Go files
PASS — no warnings or errors

git diff --check <base>
PASS
```

Coverage remains an explicit WARN, not a fabricated pass: `internal/bulkops` is
78.1%, below the informational 80% threshold. The load-bearing functions measured
`NormalizeCandidateIDs` 100.0%, `executeBulkPromote` 86.2%,
`promoteWithMemoryAndSnapshotAction` 77.5%, `transitionWithSnapshot` 85.7%,
`amendCandidateReviewAfterTx` 66.7%, and `handleBulkPromote` 52.4% in their scoped
profiles. Each new contract also has direct behavior, repeat-20, race, and prove-it
evidence.

## Full-package baseline, preserved rather than silently patched

After the fixture correction, a fresh full `internal/db/gorm` run no longer reports
any candidate-review failure. It still fails six unrelated governance/migration tests:

- `TestRuleGovernanceStore_AnnotatedCandidateWaitsUntilReviewAfter`;
- two lifecycle-health aggregate tests;
- three migration-144 rollback/reapply constraint tests.

A fresh full `internal/mcp` run still fails the unrelated
`TestHybridTG3_ConfidenceMin_FloorEnforced_T022` JSON-shape test and
`TestEC_F1_TagDerivedBackfill_T007` compatibility test. These failures existed in the
pre-fix combined matrix, are outside this checker-bounded rework, and were not patched.
The full package commands therefore remain WARN/FAIL baseline; only the scoped gates
are claimed green.

Fresh migrations also continue to log the known non-fatal stale pattern/relation
index warnings, absent `observation_vectors`, and unavailable `vectorscale` extension.

## Database and worktree hygiene

Every maker-owned RED, GREEN, fixture, repeat, scoped, race, full-gorm, full-MCP, and
prove-it database reached zero active sessions before exact-name drop. Final proof:

```text
database count matching engram_prc_bulkops_sibling_%: 0
active session count matching engram_prc_bulkops_sibling_%: 0
```

The branch contains one commit over the required base after final amendment. No push
or integration was performed.

## Maker disposition

H1, M1, and M2 are closed by implementation and permanent evidence. The required next
gate is an independent acceptance checker against the exact handoff commit, followed
by separate post-run code review and the integrated production-readiness gates.
