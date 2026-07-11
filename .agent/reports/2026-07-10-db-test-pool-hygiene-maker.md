# DB-TEST-POOL-HYGIENE maker report

Status: **READY_FOR_RECHECK — EVIDENCE REVISION 2**

This is a maker handoff, not a PASS/acceptance verdict. A fresh checker must
review the successor before integration.

## Boundary

- Exact parent: `bd68c05baf4b7250096dd84f56bebea2aa555970`
- Product/evidence candidate: `276337b3e96aa5af6d2e7dd9a0002ff957e5ffc9`
- Evidence revision branch: `work/prc-db-test-pool-hygiene-evidence-r2`
- Evidence revision worktree: `D:\Dev\engram\.agent\worktrees\dbph-evidence-r2`
- Product scope: none; only `internal/db/gorm/candidate_store_test.go` changed.
- Revision-2 scope: evidence/report/verifier bytes only. The product/test file is
  byte-identical to the candidate (`62260c1a...`).
- Forbidden canonical register/Markdown/HTML, integration, tag, remote, and
  unrelated product paths were not changed.
- Exact handoff SHA is supplied by the maker handoff after the single commit;
  a commit cannot embed its own hash without changing that hash.

## Cause and closure

`openCandidateTestDB` created a GORM/`database/sql` PostgreSQL pool but never
closed it. It is a package-wide test owner, not a two-test local helper: exact
exact-parent mechanical inventory found 83 pre-existing call sites across eight
`*_test.go` files. Sequential top-level tests therefore retained idle pools for
the lifetime of one `go test` process and exhausted PostgreSQL's
`max_connections=100` budget.

The shared owner now registers `sqlDB.Close()` with `t.Cleanup` immediately
after obtaining `*sql.DB`, before ping or migrations. Cleanup failures are
reported on the owning test. This closes the resource class for every caller,
including failure paths, without changing production code.

The permanent regression opens one parent observer and four child-owned pools.
Each child proves its pool remains queryable and visible during the subtest;
after `t.Run` returns, `pg_stat_activity` must converge to the parent's baseline
within one second. This checks both non-premature close and owner cleanup.

## TDD evidence

All PostgreSQL runs used a fresh `engram_mkr_dbph_*` database and recorded
`max_connections`, command, timestamps, exit code, and post-drop DB/session
residue.

| Evidence | Result |
|---|---|
| `01-parent-broad.summary.log` | Exact parent full package: exit 1; six pre-existing semantic failures followed by six client-exhaustion failures; nine `too many clients` records. |
| `02-parent-red.log` | RED: four child subtests accumulated sessions `1 -> 4` above baseline; exit 1. |
| `03-green-focused.log` | GREEN: each child was live at `1`, then returned to baseline `0`; exit 0. |
| `04-prove-it.log` | Final tolerant regression with only cleanup removed accumulated `1 -> 4`; exit 1. |
| `05-post-prove-green.log` | Cleanup restored; focused regression exit 0. |
| `06-repeat20.log` | Focused regression `-count=20`; exit 0. |
| `07-race.log` | Focused `-race`; exit 0. |
| `09-candidate-repeat5.log` | All candidate/helper-local tests `-count=5`; exit 0. |
| `10-candidate-race.log` | All candidate/helper-local tests under `-race`; exit 0. |
| `08-successor-broad.summary.log` | Full package: exit 1 only for the exact six unrelated semantic failures; zero client exhaustion. |

Every row above finished with `database_residue=0` and
`activity_residue=0`.

## Exact-parent comparison

The parent and successor broad commands were byte-identical:

```text
go test -p=1 ./internal/db/gorm -count=1 -v
```

Both expose the same six existing governance/migration failures:

- `TestMigration144_RuleGovernanceEscapeConstraints`
- `TestMigration144_RuleGovernanceRollbackAndReapply`
- `TestMigration144_RuleGovernanceSnapshotStatusesAcceptExtendedStates`
- `TestRuleGovernanceStore_AnnotatedCandidateWaitsUntilReviewAfter`
- `TestRuleGovernanceStore_GetLifecycleHealthAggregatesGovernanceTables`
- `TestRuleGovernanceStore_GetLifecycleHealthOmitsGlobalArbiterRunsForProjectScopedReads`

Only the parent additionally fails three TemporalTruth, two TokenStore, and one
TranscriptStore test after the pool budget is exhausted. The successor has no
new failure and zero `too many clients` occurrence. The full machine comparison
is `11-parent-successor-comparison.txt`; this broad package remains truthfully
FAIL/WARN, not green or allowlisted.

## Static gates and scope proof

- `go vet ./...`: exit 0.
- `go build ./...`: exit 0.
- `git diff --check`: exit 0 before evidence staging.
- Coverage: N/A; the only implementation is in `*_test.go`, which Go production
  coverage does not instrument.
- Changed implementation file SHA-256:
  `62260c1a2e0705b065295322dd23fcf9b17fd47cb5ebc64134630788e2d23e09`.

## Discrepancy ledger

1. The first GREEN assertion expected `pg_stat_activity` to drop synchronously.
   Subsequent immediate observations sometimes retained one row briefly. A
   bounded 10 ms poll proved convergence, so the permanent test requires the
   exact baseline within one second; the parent/prove-it leak remains stable and
   still fails at `1 -> 4`.
2. Fresh migrations emit existing non-fatal stale pattern/relation-index,
   absent `observation_vectors`, and unavailable `vectorscale` warnings. They
   were not patched or reclassified.
3. Serena diagnostics for the changed file did not return and was terminated.
   This degradation is explicit; `gofmt`, full-repo vet, full-repo build,
   focused/race/repeat, and parent-vs-successor runtime evidence are present.
4. The six unrelated broad semantic failures remain visible and block calling
   the full package green.
5. Tracked `*.log` evidence was mechanically normalized to UTF-8/LF, tabs were
   expanded, and end-of-line whitespace was removed; command text, output
   content, exit codes, and timestamps were otherwise unchanged.
6. The two full-package outputs were captured in full, then reduced to compact
   metadata/failure/package-result summaries for the tracked packet. The
   summaries retain every top-level failure, every client-exhaustion record,
   command/limit metadata, exit code, and residue result.
7. Independent checking found that the first packet omitted seven exact-parent
   call sites in the two temporal-truth test files, advertised 76/6 instead of
   83/8, and wrote a stale outer checksum for the final manifest. Revision 2
   closes those audit defects without changing product/test implementation.
8. Revision 2 uses the machine-explicit `git-blob-bytes-v1` representation.
   `MANIFEST.json` excludes itself and the outer sums file; the outer sums file
   is generated only after the final manifest and includes the manifest. This
   avoids a self-hash paradox. The permanent verifier rejects CRLF/raw-file
   substitution, stale manifest entries, and false 76/6 inventory.

## Finish state

`review-needed`: one clean evidence-only successor commit is handed to a fresh
checker. The exact revision commit is reported after commit creation because a
commit cannot embed its own hash. No
integration, release, tag, push, or self-acceptance action was taken.
