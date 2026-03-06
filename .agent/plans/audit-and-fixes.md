# engram — Audit & Fixes Plan

**Audited:** 2026-02-27
**Source:** Comprehensive architectural audit by Codex (session a35579e)
**Docs:** `docs/arch/` (8 files, 1805 lines)

---

## Summary

6 bugs found during architectural audit. Ranked by severity and fix risk.

---

## Bug 1 — Embedding Dimension Mismatch (CRITICAL)

**Severity:** P0 — Silent data corruption on fresh deploy
**File:** `internal/embedding/service.go` (hardcoded `384`)
**Also:** `internal/memory/repository.go`, `internal/storage/postgres.go`

**Problem:**
The embedding dimension is hardcoded to `384` in multiple places, but the actual
dimension depends on the configured embedding model:
- `text-embedding-3-small` → 1536 dims
- `text-embedding-3-large` → 3072 dims
- `nomic-embed-text` → 768 dims

Result: if the model is not the assumed one, all embeddings are stored/queried at
wrong dimension → silent search failures or Postgres pgvector errors.

**Fix:**
1. Remove all hardcoded `384` constants
2. Load dimension from config at startup: `cfg.Embedding.Dimension` (already exists in config)
3. Validate: at startup, query `pg_catalog.pg_attribute` for the existing `embedding` column
   dimension and compare to configured dimension. Fail fast with clear error if mismatch.
4. Add `validateEmbeddingDimension(ctx context.Context, db *pgxpool.Pool, cfg Config) error`
   called in `main.go` before accepting requests.

**Test:**
- `TestEmbeddingDimensionMismatch_FailsFast` — start with wrong dimension, expect startup error
- `TestEmbeddingDimensionMatch_Succeeds` — correct dimension, no error

---

## Bug 2 — Lazy Embedding (No Vector Until 5+ Accesses)

**Severity:** P1 — New memories invisible to similarity search until "warmed up"
**File:** `internal/memory/service.go` (access count threshold)

**Problem:**
Memories are stored without a vector embedding until they have been accessed 5+ times
(access count threshold). Until then, `embedding IS NULL` in the database.
All vector similarity search queries use `ORDER BY embedding <=> $1` which skips NULL
embeddings entirely. Net effect: newly stored memories are invisible to semantic search.

**Fix Option A (recommended):** Embed immediately on store, remove the threshold entirely.
The threshold was presumably added for performance reasons (avoid embedding every minor note),
but it creates a correctness gap. Accept the cost and embed on write.

**Fix Option B (if performance matters):** Keep the lazy approach but use a proper background
job with a queue. Store → enqueue for embedding → background worker embeds → mark ready.
During the "pending embedding" window, exclude these memories from semantic search explicitly
(add `WHERE embedding IS NOT NULL` or use a `embedding_status` column).

**Recommendation:** Fix Option A (embed on write) — simpler, no gap.

**Test:**
- `TestNewMemory_ImmediatelySearchable` — store memory, search by similar text, find it

---

## Bug 3 — BM25 Short-Circuit Skips Semantic Search

**Severity:** P2 — Incorrect search behavior when keyword match is strong
**File:** `internal/memory/repository.go` (hybrid search logic)

**Problem:**
In the hybrid search pipeline (BM25 FTS + vector similarity → RRF fusion), when BM25
produces a result with score >= 0.85, the code short-circuits and skips the vector search
entirely, returning only the BM25 result.

This breaks the fundamental RRF contract: fusion is only meaningful when both signals
contribute. The short-circuit means:
- High keyword-match memories always win, regardless of semantic relevance
- Vector-only memories (no exact keyword match) are never surfaced in "strong keyword" queries
- RRF score is not computed, defeating the purpose of hybrid search

**Fix:**
Remove the short-circuit entirely. Always run both BM25 and vector search in parallel,
then fuse with RRF. The performance cost is minimal (parallel execution, Postgres optimized for both).

If a performance optimization is needed, add a cache for BM25-heavy queries instead.

**Test:**
- `TestHybridSearch_AlwaysRunsBothSignals` — verify both BM25 and vector results contribute
- `TestHybridSearch_NoShortCircuit` — BM25 score 0.9 does NOT skip vector search

---

## Bug 4 — Consolidation Only in Background Worker, Not MCP Standalone

**Severity:** P2 — Memory consolidation does not run in MCP-only deployments
**File:** `internal/worker/consolidator.go`, `cmd/server/main.go`

**Problem:**
Memory consolidation (merging similar memories, updating importance scores, pruning outdated
memories) runs only inside the background worker goroutine started by the full server binary.
When engram is deployed as an MCP server only (without the worker running),
consolidation never happens → memories accumulate indefinitely, degrading search quality over time.

The consolidation logic is correct; the deployment architecture is incomplete.

**Fix Option A:** Add a consolidation MCP tool (`memory_consolidate`) that agents can call explicitly.
**Fix Option B:** Run consolidation as an inline maintenance step after N writes (e.g., every 100 stores).
**Fix Option C:** Add a lightweight cron-style ticker inside the MCP server process.

**Recommendation:** Fix Option A (explicit MCP tool) — most controllable, agents can decide when.

**Test:**
- `TestConsolidation_AvailableViaMCP` — consolidate tool exists in MCP tool list
- `TestConsolidation_RunsInMCPMode` — consolidate works without background worker

---

## Bug 5 — go.mod Module Name Unchanged from Upstream Fork

**Severity:** P3 — Module identity conflict with upstream
**File:** `go.mod` (line 1)

**Problem:**
The `go.mod` module name still matches the upstream `github.com/original-author/engram`
(or similar). This means that if both this fork and the original are used in the same Go workspace,
or if anyone imports this fork as a dependency, there will be module identity conflicts.

**Fix:**
Update `go.mod` module name to `github.com/thebtf/engram` (or the actual fork owner).
Also update all internal imports from the old module path to the new one.

**Command:**
```bash
go mod edit -module github.com/thebtf/engram
find . -name "*.go" -exec sed -i 's|github.com/old-owner/engram|github.com/thebtf/engram|g' {} +
go mod tidy
```

**Test:**
- `go build ./...` succeeds after rename
- No stale import paths remain

---

## Bug 6 — Patterns Migration Field Name Mismatch

**Severity:** P3 — Migration history data partially unreadable
**File:** `internal/storage/migrations/` (specific migration file TBD)
**Also:** `internal/pattern/repository.go`

**Problem:**
A database migration renames (or adds) the `status` field on the `patterns` table,
but the Go code still reads `is_deprecated` (the old boolean field). The migration ran
successfully but the application code was not updated in sync.

Result: pattern deprecation status is silently ignored → deprecated patterns continue
to appear in search results.

**Fix:**
1. Identify which migration introduced `status` (grep for `ALTER TABLE patterns`)
2. Update `pattern/repository.go` to use `status = 'deprecated'` filter instead of `is_deprecated = true`
3. Add a migration to backfill `status` from `is_deprecated` if both columns coexist

**Test:**
- `TestPattern_DeprecatedNotReturned` — deprecated patterns excluded from search results

---

## Priority Order

| # | Bug | Severity | Fix Risk | Action |
|---|-----|----------|----------|--------|
| 1 | Embedding dimension mismatch | P0 | Low (config read) | Fix immediately |
| 2 | Lazy embedding gap | P1 | Low (remove threshold) | Fix before prod |
| 3 | BM25 short-circuit | P2 | Low (remove condition) | Fix before prod |
| 4 | Consolidation in worker only | P2 | Medium (new MCP tool) | Fix before prod |
| 5 | go.mod module name | P3 | Low (rename + sed) | Fix before sharing |
| 6 | Patterns field mismatch | P3 | Low (SQL fix) | Fix before prod |

---

## Files to Modify

| Bug | File | Change |
|-----|------|--------|
| 1 | `internal/embedding/service.go` | Remove hardcoded `384`, use `cfg.Embedding.Dimension` |
| 1 | `internal/memory/repository.go` | Same |
| 1 | `internal/storage/postgres.go` | Add `validateEmbeddingDimension()` |
| 1 | `cmd/server/main.go` | Call `validateEmbeddingDimension()` at startup |
| 2 | `internal/memory/service.go` | Remove access count threshold, embed on write |
| 3 | `internal/memory/repository.go` | Remove BM25 short-circuit |
| 4 | `internal/worker/consolidator.go` | Extract consolidation to a reusable function |
| 4 | `internal/mcp/tools.go` (or similar) | Add `memory_consolidate` MCP tool |
| 5 | `go.mod` | Update module name |
| 5 | All `*.go` files | Update import paths |
| 6 | `internal/pattern/repository.go` | Use `status = 'deprecated'` instead of `is_deprecated` |
| 6 | New migration | Backfill `status` from `is_deprecated` |

---

## Related Docs

- `docs/arch/INDEX.md` — Architecture overview
- `docs/arch/GOTCHAS.md` — All known gotchas including these bugs
- `docs/arch/COMPONENTS.md` — Component internals
- `docs/arch/DATA_MODEL.md` — PostgreSQL schema with DDL
