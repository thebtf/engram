# DB-AUTH rework maker report

Date: 2026-07-10
Parent commit: `b0c4ab4c07a4c6f512728da52b2e132bacd0289c`
Branch: `work/prc-db-auth`
Worktree: `D:\Dev\engram\.agent\worktrees\prc-db-auth`

## Outcome

Concurrent first-admin setup is now serialized by PostgreSQL across independent server processes. Exactly one setup request can create the initial administrator; a concurrent loser receives the typed `ErrInitialAdminSetupAlreadyCompleted` store error and HTTP `409 Conflict` from the real handler.

The password hash is computed before the database lock. A cheap preflight count remains in the public handler to avoid turning a completed setup endpoint into a bcrypt work amplifier, while the count performed after `pg_advisory_xact_lock` inside `CreateInitialAdmin` is authoritative.

## Implementation

- Added `UserStore.CreateInitialAdmin(ctx, email, passwordHash)`.
- Uses a transaction-scoped two-key PostgreSQL advisory lock with stable `ENGR` / `ADMI` keys.
- Uses explicit `READ COMMITTED` transaction isolation so the zero-user check observes a setup committed while this transaction waited for the lock.
- Counts users and inserts the initial admin in the same transaction.
- Returns the typed sentinel `ErrInitialAdminSetupAlreadyCompleted` after the serialized check observes an existing user.
- Relies on transaction rollback for lock cancellation and insert failures, leaving setup retryable.
- Maps the typed losing result to HTTP 409 in `handleSetup`; unrelated database failures remain HTTP 500.
- Added a test-only handler seam after bcrypt and before the store call so both real requests deterministically reach the authoritative database operation before either proceeds.
- Preserved the existing last-active-admin guard and its lifecycle tests.

## TDD evidence

### RED: store contract absent

Timestamp: `2026-07-10T03:58:49.4314543Z`

```powershell
$env:DATABASE_DSN='postgres://engram:engram@localhost:55432/engram_prc_auth_rework?sslmode=disable'; go test ./internal/db/gorm -run '^TestUserStore_CreateInitialAdmin_ConcurrentIndependentConnectionsExactlyOne$' -count=1 -v
```

Exit: `1`. The new regression test did not compile because `CreateInitialAdmin` and `ErrInitialAdminSetupAlreadyCompleted` did not exist.

### RED: real handler allowed two initial admins

Timestamp: `2026-07-10T04:02:36.5763198Z`

```powershell
$env:DATABASE_DSN='postgres://engram:engram@localhost:55432/engram_prc_auth_rework?sslmode=disable'; go test ./internal/worker -run '^TestAuthHandlersLifecycle_ConcurrentInitialAdminSetupExactlyOne$' -count=1 -v
```

Exit: `1`. Failure at iteration 0: expected one HTTP 201 response, actual two.

### GREEN

The focused store regression passed after adding the serialized store method (`PASS`, package `0.317s`). The focused real-handler regression then passed after routing setup through that method (`PASS`, test `3.60s`, package `3.698s`).

After adding the deterministic post-bcrypt barrier, both concurrent handler cases passed:

```powershell
$env:DATABASE_DSN='postgres://engram:engram@localhost:55432/engram_prc_auth_rework?sslmode=disable'; go test ./internal/worker -run '^TestAuthHandlersLifecycle_(ConcurrentInitialAdminSetupExactlyOne|ConcurrentInitialAdminSetupDuplicateEmailFailsSafely)$' -count=1 -v
```

Exit: `0`; package `4.024s`.

### Prove-It sentinel

`handleSetup` was temporarily changed back to the non-serialized `CreateUser` call while keeping the deterministic barrier. The focused regression failed immediately:

```powershell
$env:DATABASE_DSN='postgres://engram:engram@localhost:55432/engram_prc_auth_rework?sslmode=disable'; go test ./internal/worker -run '^TestAuthHandlersLifecycle_ConcurrentInitialAdminSetupExactlyOne$' -count=1 -v
```

Exit: `1`; iteration 0 expected one created response but observed two. The production call was restored to `CreateInitialAdmin`; the identical command then exited `0` (`PASS`, package `3.709s`). No sentinel change remains in the diff.

## Final verification on restored production code

Store regressions plus existing last-admin guard, repeated three times:

```powershell
$env:DATABASE_DSN='postgres://engram:engram@localhost:55432/engram_prc_auth_rework?sslmode=disable'; go test ./internal/db/gorm -run '^TestUserStore_(CreateInitialAdmin|UpdateUserWithLastAdminGuard)_' -count=3
```

Exit: `0`; package `4.052s`.

Real handler setup regressions plus existing last-admin lifecycle tests, repeated three times:

```powershell
$env:DATABASE_DSN='postgres://engram:engram@localhost:55432/engram_prc_auth_rework?sslmode=disable'; go test ./internal/worker -run '^TestAuthHandlersLifecycle_(ConcurrentInitialAdminSetup|InitialAdminSetup|LastAdminDemoteRaceLeavesOneAdmin|DisabledAdminCanBeDemotedWithoutLastAdminError)' -count=3
```

Exit: `0`; package `15.604s`.

Store race detector:

```powershell
$env:DATABASE_DSN='postgres://engram:engram@localhost:55432/engram_prc_auth_rework?sslmode=disable'; go test -race ./internal/db/gorm -run '^TestUserStore_(CreateInitialAdmin|UpdateUserWithLastAdminGuard)_' -count=1
```

Exit: `0`; package `2.676s`.

Handler race detector:

```powershell
$env:DATABASE_DSN='postgres://engram:engram@localhost:55432/engram_prc_auth_rework?sslmode=disable'; go test -race ./internal/worker -run '^TestAuthHandlersLifecycle_(ConcurrentInitialAdminSetup|InitialAdminSetup|LastAdminDemoteRaceLeavesOneAdmin|DisabledAdminCanBeDemotedWithoutLastAdminError)' -count=1
```

Exit: `0`; package `54.318s`.

Static checks:

```powershell
go vet ./internal/db/gorm ./internal/worker
git diff --check
```

Both exited `0`. Serena Go diagnostics at warning-or-higher severity returned `{}` for all four changed source/test files.

## Failure-path and residue coverage

- Two independent GORM stores and SQL pools, different emails: one success, one typed already-completed error, exactly one active admin, repeated 20 times.
- Two independent real handlers and SQL pools, different emails: one 201, one 409, exactly one user, one active admin, one setup audit event, and zero sessions, repeated 20 times.
- Concurrent duplicate email: one success and one typed/409 loser without duplicate residue.
- Context cancellation while waiting for the advisory lock: no user residue; a later setup succeeds.
- Store insert failure after acquiring the lock: transaction rollback leaves zero users; a later setup succeeds.
- Invalid request, handler insert failure, and cancelled request: no user/audit residue; a later setup succeeds.
- Setup after completion remains 409 and does not add a user or setup audit event.

## Disclosed command error

One earlier worker race command used the mistyped database name `engram_prc-db-auth` and exited `1` with `database does not exist`. This was an operator DSN typo, not a product/test failure. The command was immediately rerun with the dedicated database `engram_prc_auth_rework` and passed; the final race result above is from the corrected command and final code.

## Database hygiene

Only the dedicated database `engram_prc_auth_rework` was used. No full GORM and worker package tests were run concurrently.

Before deletion:

- leftover schemas matching `initial_admin_test_%` or `auth_setup_test_%`: `0`
- active sessions for `engram_prc_auth_rework`: `0`

Then:

```sql
DROP DATABASE IF EXISTS engram_prc_auth_rework WITH (FORCE);
```

Post-cleanup verification:

- database rows in `pg_database`: `0`
- active sessions in `pg_stat_activity`: `0`

No other test database, secret, container state, or external system was modified.

## Changed paths

- `internal/db/gorm/user_store.go`
- `internal/db/gorm/user_store_test.go`
- `internal/worker/auth_handlers.go`
- `internal/worker/auth_handlers_lifecycle_test.go`
- `.agent/reports/db-auth-rework-maker-2026-07-10.md`
