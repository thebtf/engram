# T016 — books_jobs migration checkpoint

## Scope quoted
- Add migration for books job table in `internal/db/gorm/migration_books.go`.
- Register it in `internal/db/gorm/migrations.go`.
- Keep it additive-only.

## Implemented
- Added `booksJobsMigration155()` in `internal/db/gorm/migration_books.go`.
- Registered `booksJobsMigration155()` in the migration chain in `internal/db/gorm/migrations.go`.
- Table shape includes `id`, `status`, `source_ref`, `error`, `created_at`, `updated_at`, plus additive indexes.

## Verification
- `go run ./tools/gen-data-model`
- `go test ./internal/db/gorm/...`
- `go build ./...`
- `git diff --check`

## Notes
- `go run ./tools/gen-data-model` completed, but `docs/arch/DATA_MODEL.md` had no textual books_jobs diff in this worktree. This remains a visible generator/parser discrepancy tracked in the developer oracle and should not be treated as books lane completion.
- This checkpoint does not close G005 or authorize books pipeline work by itself.
