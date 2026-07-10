# DB-BULKOPS Capture/Lock Rework — Maker Report

## Outcome

The capture-before-lock TOCTOU, candidate rollback overwrite, legacy no-`After`
rollback hazard, and raw dry-run count defect are fixed and backed by permanent
PostgreSQL regressions.

Status: **READY FOR AN INDEPENDENT CHECKER AND POST-RUN CODE REVIEW**.

This is maker evidence only. The exact commit containing this report is supplied in
the handoff; this report does not replace the independent checker, PM acceptance, or
integrated release gates.

## Scope and authorized expansion

- Worktree: `D:\Dev\engram\.agent\worktrees\prc-db-bulkops`
- Branch: `work/prc-db-bulkops`
- Parent head: `2b085de663d5ba9dfa97adf9ee58de062ee0997c`
- Original DB-BULKOPS paths:
  - `internal/bulkops/facade.go`
  - `internal/bulkops/facade_test.go`
- Minimal checker-authorized expansion for exact rollback conflict detection:
  - `pkg/models/snapshot.go`
  - `internal/bulkops/rollback.go`
  - `internal/bulkops/rollback_test.go`
- `internal/db/gorm/candidate_store.go` was inspected but not changed.
- No schema migration or v5-demolished subsystem was introduced or revived.

## Capture and promotion transaction

`bulk_promote` now normalizes requested IDs, opens one outer transaction, locks all
currently existing candidate rows with `FOR UPDATE` in ascending unique ID order,
and only then reads candidate before-state. PostgreSQL `clock_timestamp()` is read
inside the same transaction after the locks are acquired.

The locked candidate object is the single input for:

1. promoted-memory construction;
2. the candidate rollback `Before` payload;
3. the exact post-promotion `After` payload;
4. the final successful-candidate snapshot membership.

Snapshot creation, candidate transition, memory creation, and promoted-memory
amendment commit or roll back together. A candidate missing when the locked capture
runs is reported as missing and is not promoted even if it is inserted before the
transaction completes. Partial success creates a snapshot containing exactly the
successful candidate and memory mutations; zero success creates no rollback snapshot.

Permanent proofs:

- `TestFacade_BulkPromote_ConcurrentCommittedUpdateIsCapturedPromotedAndRollbackable`
  proves A→B committed before lock acquisition is the captured, promoted, and restored state.
- `TestFacade_BulkPromote_CandidateInsertedAfterLockedCaptureIsNotPromoted` proves a
  missing-at-capture candidate inserted later remains pending and absent from the snapshot.
- Existing equal-ID and amendment-failure regressions remain GREEN, including exact
  post-commit audit counts and rollback retry safety.

## Exact candidate rollback conflict detection

`SnapshotEntry` now has an optional JSON `After` field. New bulk-promote snapshots
persist the database-re-read promoted candidate, including its authoritative
`updated_at`, status, and promoted-memory ID.

Rollback lock order is now:

1. snapshot row;
2. candidate rows in ascending unique ID order;
3. memory rows in ascending unique ID order.

Before any restore or delete, rollback compares each locked current candidate with
the exact persisted `After` state. Time locations and nil/empty JSON slices are
canonicalized, while all domain fields and ordering remain exact. Any mismatch or
missing candidate returns `ErrRollbackConflict`; the transaction preserves candidate
C, the promoted memory, and the committed snapshot.

`TestFacade_BulkPromote_CandidateChangedAfterExecuteConflictsAndPreservesCurrent`
holds an uncommitted B→C update, proves rollback waits on the candidate lock, commits
C, and then proves rollback reports conflict without overwriting C or deleting memory.

## Legacy snapshots fail closed

Candidate restore entries without `After` cannot establish the exact operation-owned
post-state. They are still locked deterministically but now return
`ErrRollbackConflict` instead of guessing from `snapshot.CreatedAt` and blindly
calling `RevertRawTx`.

`TestFacade_BulkPromote_LegacySnapshotWithoutAfterFailsClosedAndPreservesCandidate`
removes `After` from a real bulk-promote snapshot, applies a later candidate edit, and
proves the candidate, memory, and committed snapshot remain intact.

Compatibility retained:

- numeric legacy memory restore/delete entries still use the existing decoder;
- prefixed `candidate:<id>` and `memory:<id>` domains remain disjoint;
- modern candidate-review fixtures now persist exact `After` state and retain the
  established successful rollback and edited-memory conflict behavior.

## Normalized dry-run semantics

Dry-run now reports `WouldAffect = len(sortedUniqueIDs(candidate_ids))`, matching the
same duplicate removal and zero filtering used by execution. It intentionally remains
database-free; the regression uses two existing candidates so preview `2` equals the
actual execution count `2` for a six-element duplicate/zero input.

Permanent proof:
`TestFacade_BulkPromote_DryRunNormalizesDuplicateAndZeroIDs`.

## TDD and prove-it evidence

Evidence artifacts:

- `.agent/specs/production-ready-db-bulkops/evidence/DB-BULKOPS-CAPTURE-LOCK-REWORK.red.json`
- `.agent/specs/production-ready-db-bulkops/evidence/DB-BULKOPS-ROLLBACK-CANDIDATE-CONFLICT.red.json`
- `.agent/specs/production-ready-db-bulkops/evidence/DB-BULKOPS-DRY-RUN-NORMALIZATION.red.json`
- `.agent/specs/production-ready-db-bulkops/evidence/DB-BULKOPS-LEGACY-CANDIDATE-NO-AFTER.red.json`
- `.agent/specs/production-ready-db-bulkops/evidence/DB-BULKOPS-CAPTURE-LOCK-REWORK.tdd.json`
- `.agent/specs/production-ready-db-bulkops/evidence/DB-BULKOPS-FINAL.cover.out`

RED reproduced all four defects on fresh PostgreSQL 17 databases before their
production edits. Three temporary prove-it sentinels then reintroduced raw dry-run
counting, unconditional candidate-state acceptance, and legacy no-`After` acceptance.
Each permanent regression failed with the destructive behavior; all passed again after
the sentinels were removed.

## Final verification

Focused high-risk repeat on
`engram_prc_bulkops_final_focus_20260710_082852`:

```text
9 permanent high-risk tests x -count=20
PASS — 180/180 executions
package 41.437s
EXIT_CODE=0
```

Final full package with coverage on
`engram_prc_bulkops_final_postprove_20260710_083859`:

```text
go test ./internal/bulkops -count=1 -coverprofile DB-BULKOPS-FINAL.cover.out
PASS — package 3.901s
coverage: 76.8% of statements
EXIT_CODE=0
```

Final race run on
`engram_prc_bulkops_final_postprove_race_20260710_084223`:

```text
go test -race ./internal/bulkops -count=1
PASS — package 9.538s
EXIT_CODE=0
```

Additional gates:

```text
go test ./pkg/models -count=1
PASS

go vet ./internal/bulkops ./internal/db/gorm ./pkg/models
PASS

Serena diagnostics: no warnings/errors in all five changed files
git diff --check: PASS
```

Coverage details:

- package: 76.8% — informational WARN against the 80% default;
- `executeBulkPromote`: 80.0%;
- `lockPromoteCandidatesTx`: 81.8%;
- `Rollback`: 79.6%;
- `lockCandidateRowsForRollbackTx`: 84.2%;
- `detectCandidateConflicts`: 81.2%;
- `matchesExpectedCandidateState`: 87.5%.

The package-level threshold is not silently promoted to PASS. Every new load-bearing
branch nevertheless has deterministic database regression, repeat-20, race, and
mutation/prove-it evidence.

## Database and worktree hygiene

Every maker-owned RED/GREEN/focus/full/race/prove-it database reached zero active
sessions and was force-dropped. The orphan `engram_prc_bulkops_check` database was
separately classified as inactive, name-validated, and dropped. Final PostgreSQL proof:

```text
bulk database count: 0
bulk active session count: 0
```

The accidental generated file named `$cover` was classified as a coverage artifact,
deleted, and replaced by the intended ignored evidence file
`DB-BULKOPS-FINAL.cover.out`; `$cover` is absent from final git status.

Known fresh-migration warnings for stale pattern/relation indexes, absent
`observation_vectors`, and unavailable `vectorscale` remained non-fatal and unchanged.

## Maker disposition

The DB-BULKOPS blocking findings are closed by implementation and permanent evidence.
No push or integration was performed. The next required step is an independent checker
against the exact handoff commit, followed by separate post-run code review and the
integrated production-readiness gates.
