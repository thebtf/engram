# DB-EMBEDDING-STATS Maker Report

Date: 2026-07-10

Worktree: `D:/Dev/engram/.agent/worktrees/prc-db-embedding-stats`

Branch: `work/prc-db-embedding-stats`

Base: `origin/main@dc891b2d72b1fd63b83e4a630a249241fc389151`

Finish state: `READY_FOR_INDEPENDENT_CHECK`

## Classification and root cause

The failure is a live production defect in `embedding.Store.Stats`, reached by fresh installs and by installations with no embedding chunks. PostgreSQL aggregate `max(created_at)` returns one row containing SQL NULL for an empty table. The current GORM path attempted to scan that NULL into `*time.Time` and returned:

```text
unsupported Scan, storing driver.Value type <nil> into type *time.Time
```

The existing `TestStoreStats_Empty` was not deterministic evidence because it read the shared physical table and accepted populated state. The new regression shadows the shared relation with a transaction-local empty temporary `content_chunks` table, so the exact fresh-empty condition is proven without persistent schema or rows.

## Change

- Scan the nullable aggregate into `sql.NullTime`.
- Populate `LastChunkAt` only when the aggregate is valid.
- Add `TestStoreStats_EmptyPhysicalTableReturnsZeroValue`, which requires the entire `EmbeddingStats` result to be its zero value.

No demolished graph, rerank, scoring, SDK extraction, or HTTP MCP path was restored.

## TDD evidence

- RED: the new regression failed on the unmodified production code with the exact NULL scan error.
- GREEN: focused test passed; repeat-20 passed; full `internal/embedding` package passed three times against a full-schema PostgreSQL database.
- Race: focused race repeat-3 and final full-package race passed.
- Vet: `go vet ./internal/embedding` passed.
- Prove-It: temporarily replacing `Store.Stats` with `panic("not implemented")` failed the regression; the controlled sentinel was removed and the test returned to GREEN.
- Coverage: package 47.8% WARN under the informational 80% default; touched `Stats` function 76.2%. No threshold was reduced.

Canonical phase evidence is in `.agent/specs/db-embedding-stats/evidence/DB-EMBEDDING-STATS.tdd.json`.

## Environment and cleanup

- PostgreSQL 17 test container: `engram-prc-postgres`.
- Dedicated database: `engram_prc_embedding_stats`, with pgvector enabled only for the RED/GREEN fresh-empty proof.
- Final active sessions for that database: `0`.
- The dedicated database was dropped with forced cleanup; final database count: `0`.
- The full package gates used the existing full-schema `engram_prc_crystallization` database read/write transaction fixtures and left their normal rollback boundaries intact.

## Required next action

A different native agent must create a fresh detached checker worktree at the exact candidate commit, reproduce the empty-table behavior independently, inspect NULL/non-NULL timestamp semantics, run focused/repeat/race/vet gates, verify no shared-data dependency or residue, and issue PASS or FAIL. A separate root post-run code review remains mandatory before integration.
