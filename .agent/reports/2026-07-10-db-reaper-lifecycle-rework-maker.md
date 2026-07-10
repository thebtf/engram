# DB Reaper Lifecycle Rework — Maker Report

Date: 2026-07-10

Worktree: `D:/Dev/engram/.agent/worktrees/prc-db-reaper`

Branch: `work/prc-db-reaper`

Base: `dc891b2d72b1fd63b83e4a630a249241fc389151`

Rejected predecessor retained: `88f6bbb58c228b843dc30aa00bd05d3e775f3558`

Prior checker report SHA256: `45584C1A632801BDFC2F85B013FB681B4B37080CE2AA5C83D35D92E1DBB26B6E`

## Outcome

The rejected predecessor's valid removal of `t.Parallel()` from the environment-mutating test is preserved. This successor fixes the ordinary lifecycle and retention correctness gaps that predecessor did not address.

- `ENGRAM_PROJECT_RETENTION_DAYS` now has explicit deterministic behavior: unset, malformed, zero, and negative values use the documented 30-day default; positive values through `106751` days are honored exactly; larger positive decimal values skip the sweep instead of overflowing or clamping downward and deleting newer rows.
- `PurgeOnce` now returns a failed `DELETE` to its caller with query context instead of logging the error and returning `nil`.
- Reaper `Start`/`Stop` is a mutex-guarded restartable state machine. Stop-before-start is a no-op; concurrent/repeated starts own at most one loop; concurrent/repeated stops share cancellation and join the same completion; the ticker is stopped before completion is published.
- Service shutdown is once-only and nil-safe, joins partial async initialization, stops and joins the project reaper, drains other tracked goroutines, then closes the database. The project-reaper field is represented by the minimum lifecycle interface so shutdown ordering can be tested without weakening production construction.

## Why service lifecycle files changed

Live call-path tracing found `initializeAsync` created and started the reaper after readiness, while `Shutdown` cancelled the root context but neither joined initialization nor called `projectReaper.Stop()` before `store.Close()`. The reaper was also outside the service wait group. The minimum service changes are therefore required to make the requested database-close ordering true under ordinary and partial initialization.

## TDD evidence

Observed RED evidence precedes each corresponding production edit:

- `tdd/retention-config.red.json` — missing bounded parser/max-safe behavior.
- `tdd/purge-error.red.json` — closed database logged an error while `PurgeOnce` returned `nil`.
- `tdd/stop-before-start.red.json` — Stop blocked forever before Start.
- `tdd/concurrent-lifecycle.red.json` — no concurrency-safe lifecycle/ticker ownership seam.
- `tdd/service-partial-init.red.json` — partial service shutdown panicked on nil cancel.
- `tdd/service-reaper-order.red.json` — no initialization join or testable reaper-before-database contract.

GREEN and coverage are consolidated in `tdd/db-reaper-lifecycle-rework.tdd.json`. Reaper statement coverage is `87.6%`. The committed-snapshot Prove-It audit substituted the retention parser, synchronous purge, reaper stop/run lifecycle, and service shutdown/once paths; every mutation failed the intended regression tests, then the exact production files were restored from the committed snapshot and the focused suites returned green. Full mutation evidence is `tdd/prove-it.json`.

## Verification

All commands below were run from this worktree.

| Gate | Result |
| --- | --- |
| Focused retention/error/lifecycle edge matrix, `-count=20` | PASS — `internal/worker/reaper` |
| Full reaper package, `-count=3` | PASS |
| Full reaper race package, `-count=10` | PASS |
| Service shutdown tests, `-race -count=10` | PASS |
| Worker package with `DATABASE_DSN` unset | PASS |
| Worker package tree with `DATABASE_DSN` unset | PASS |
| Repository `go test ./... -count=1` with `DATABASE_DSN` unset | PASS |
| `go vet ./...` | PASS |
| `go build ./cmd/engram-server` | PASS; disposable binary SHA256 `FCF19C377C32F86071052F6C1DCBB8DA53F5E360F65C1DBF0D12BC1DB74E7760` |
| `git diff --check` | PASS |

The full command/result matrix is `evidence/production-ready/db-reaper-lifecycle-rework/verification-summary.json`.

Residue cleanup is recorded in `evidence/production-ready/db-reaper-lifecycle-rework/residue-cleanup.json`: both reaper test databases are absent, their PostgreSQL session count is zero, no worktree/test process remains, the disposable server binary is absent, and the interrupted worktree semantic index created during source routing was removed. The pre-existing PostgreSQL container was retained.

## Discrepancy surfaced

One broad DB-backed `go test ./internal/worker -count=1` run failed five tests outside this diff: two DB-AUTH lifecycle tests assigned to the separate DB-AUTH lane, and three crystallization tests that assert direct decision-memory behavior demolished in v5. No failing test touches `internal/worker/reaper/**`, `Service.Shutdown`, or the new shutdown test. This is recorded as `FAIL_OUT_OF_SCOPE` in the verification summary rather than silently presented as green. The affected DB-backed shutdown surface passes under repeat and race, and the worker/full-repository unit baselines pass with `DATABASE_DSN` unset.

## Scope

Production/test paths changed:

- `internal/worker/reaper/reaper.go`
- `internal/worker/reaper/reaper_test.go`
- `internal/worker/service.go`
- `internal/worker/service_reaper_lifecycle_test.go`

Durable report/evidence paths:

- `.agent/reports/2026-07-10-db-reaper-lifecycle-rework-maker.md`
- `.agent/reports/evidence/production-ready/db-reaper-lifecycle-rework/**`

## Handoff state

Status: `READY_FOR_INDEPENDENT_CHECK`. This maker report is not an approval and does not authorize integration.
