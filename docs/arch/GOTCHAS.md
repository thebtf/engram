# Gotchas and Integration Notes

This document captures non-obvious behaviors, operational risks, and integration quirks discovered during source code analysis. Read before deploying.

---

## Critical: Embedding Dimension Mismatch

**Severity: DATA CORRUPTION**

The `vectors` and `content_chunks` tables are created with `vector(384)` in migration `006` and `017` respectively. This dimension is hardcoded as DDL — GORM cannot change it without dropping and recreating the table.

- OpenAI `text-embedding-3-small` (default): produces **1536-dim** vectors — **incompatible** with the hardcoded `vector(384)` schema.

If you switch `EMBEDDING_PROVIDER=openai` after initial setup without recreating the tables, all vector upserts will fail with a pgvector dimension error.

**Workaround:**
1. Use a 384-dim OpenAI model (set `EMBEDDING_MODEL_NAME` to a 384-dim model and `EMBEDDING_DIMENSIONS=384`), or
2. Before switching providers, run:
   ```sql
   DROP TABLE vectors CASCADE;
   DROP TABLE content_chunks CASCADE;
   -- Then restart the service — migrations will recreate with correct dimensions.
   -- NOTE: you must edit migration 006/017 DDL to vector(1536) first.
   ```
   There is no automated migration path for dimension changes.

---

## Hub Storage: New Observations Are Not Vector-Searchable

**Severity: BEHAVIORAL SURPRISE**

With the default `ENGRAM_VECTOR_STORAGE_STRATEGY=hub`, embeddings are only stored in the `vectors` table after an observation has been accessed `ENGRAM_HUB_THRESHOLD` times (default: 5).

**Effect:** Observations created in the current or recent sessions will not appear in vector similarity searches (they are only searchable via FTS/tsvector). This is intentional as a storage optimization but can cause new information to be missed by semantic search.

**Workaround:** Set `ENGRAM_HUB_THRESHOLD=1` to store embeddings immediately, at the cost of more storage.

---

## Module Name Unchanged From Upstream

**Severity: LOW (development concern)**

The module name in `go.mod` is `github.com/thebtf/engram` — identical to the upstream repo. This fork did not rename the module. All internal import paths use the upstream module name.

**Effect:** If you try to use both the upstream and this fork simultaneously (e.g., `go get` both), they will conflict. Not an issue for normal use — just import the fork directly.

---

## maintenanceService and consolidationScheduler Are Nil in MCP Server

**Severity: MEDIUM (feature dependency)**

The MCP server (`cmd/mcp/main.go`) passes `nil` for both `maintenanceService` and `consolidationScheduler` when creating the MCP server:
```go
server := mcp.NewServer(
    ...
    nil, // maintenanceService - handled by worker
    ...
    nil, // consolidationScheduler - not available in standalone MCP mode
)
```

**Effect:** MCP tools `trigger_maintenance`, `get_maintenance_stats`, and `run_consolidation` will return errors or no-ops when the worker is not running. The consolidation lifecycle (decay/associations/forgetting) only runs when the worker process is active.

**Implication:** If you run only the MCP server without the worker, memory consolidation does not happen.

---

## Config File Watcher Calls os.Exit(0)

**Severity: INFORMATIONAL (intentional)**

The MCP server watches `~/.engram/settings.json` via `fsnotify`. On any file change event, it logs "Config file changed, exiting for restart..." and calls `os.Exit(0)` after a 100ms flush delay.

**Why:** Claude Code is expected to restart the MCP server process. This is a deliberate restart mechanism — simple but crude.

**Side effect:** If settings.json is written by multiple processes simultaneously (e.g., `jq` + shell redirect), spurious restarts can occur.

---

## Session Indexer Runs in Background at MCP Startup

**Severity: LOW (timing)**

The session JSONL indexer starts as a background goroutine during MCP server initialization:
```go
go func() {
    count, err := sessionIndexer.IndexAll(ctx)
    ...
}()
```

**Effect:** During the first few queries after MCP startup, the `indexed_sessions` table may not be fully populated. `search_sessions` and `list_sessions` tools may return incomplete results until indexing completes. Indexing is incremental (mtime-based) so subsequent startups are fast.

---

## DATABASE_DSN Is Strictly Environment-Only

**Severity: OPERATIONAL PITFALL**

The `DatabaseDSN` field in the `Config` struct has `json:"-"`:
```go
DatabaseDSN string `json:"-"` // env-only: DATABASE_DSN (contains password, never JSON)
```

Even if you add `"database_dsn": "postgres://..."` to `settings.json`, it will not be loaded. The JSON unmarshaling skips this field entirely.

**Correct approach:** Always set `DATABASE_DSN` as an environment variable or in a `.env` file sourced before starting the service.

---

## WorkerHost Default Inconsistency

**Severity: LOW (confusing)**

The `Config` struct sets `WorkerHost: "0.0.0.0"` as its zero-value field default in `Default()`. However, `GetWorkerHost()` returns `"127.0.0.1"` when the env var is unset and the config field is empty:

```go
func GetWorkerHost() string {
    host := strings.TrimSpace(os.Getenv("ENGRAM_WORKER_HOST"))
    if host != "" {
        return host
    }
    if cfgHost := strings.TrimSpace(Get().WorkerHost); cfgHost != "" {
        return cfgHost
    }
    return "127.0.0.1"  // <-- effective default
}
```

**Net effect:** The worker binds to `127.0.0.1` (localhost only) by default. Set `ENGRAM_WORKER_HOST=0.0.0.0` to expose it on the network (required for multi-workstation setup).

---

## BM25 Short-Circuit Can Skip Vector Search

**Severity: BEHAVIORAL SURPRISE**

The hybrid search has a short-circuit optimization: if the top BM25 score is >= 0.85 AND the gap between rank 1 and rank 2 is >= 0.15, vector search is skipped entirely:

```go
if len(ftsList) >= 2 &&
    ftsList[0].Score >= 0.85 &&
    (ftsList[0].Score-ftsList[1].Score) >= 0.15 {
    return m.buildResultFromFTS(ftsResultsCache, params)
}
```

**Effect:** For queries with very high-confidence keyword matches (e.g., exact function names), semantically related observations that don't contain those keywords may be missed.

**When this matters:** Searching for "authentication" when the relevant observation says "session management" — if BM25 gives exact matches a score >= 0.85, the semantic matches are skipped.

---

## fts5 Build Tag Is Vestigial

**Severity: INFORMATIONAL**

The Makefile builds with `-tags "fts5"` and `CGO_ENABLED=1`. These were required for SQLite FTS5 in the upstream version. The PostgreSQL fork still uses these flags because `internal/db/gorm/sqlite_build.go` conditionally compiles some SQLite-related code.

**Effect:** The build still uses these flags for compatibility with test files that carry the `fts5` build tag. CGO is required for tests but not for the main build.

---

## patterns Table Index References Non-Existent Column

**Severity: LOW (index silently skipped)**

Migration `012` creates an index with `WHERE is_deprecated = 0` on the `patterns` table:
```sql
CREATE INDEX IF NOT EXISTS idx_patterns_frequency
ON patterns(frequency DESC, last_seen_at_epoch DESC)
WHERE is_deprecated = 0
```

However, the `Pattern` model uses a `status` field (`active|deprecated|merged`), not an `is_deprecated` boolean. This index definition references a non-existent column. Since migrations 012-016 use non-fatal error handling (`continue` on error), this index creation silently fails.

**Effect:** The patterns frequency index does not exist. Pattern queries do not use this optimization. No data corruption.

---

## pgvector Extension Must Pre-Exist or User Must Have SUPERUSER

**Severity: DEPLOYMENT RISK**

Migration `006` runs:
```sql
CREATE EXTENSION IF NOT EXISTS vector
```

This requires either SUPERUSER privilege or the `vector` extension to be pre-installed and trusted. On managed PostgreSQL services (RDS, Cloud SQL, Supabase), you may need to enable pgvector through the control plane before running the service.

**Workaround:** Run `CREATE EXTENSION IF NOT EXISTS vector` as a superuser before starting the service, or ensure the database user has `SUPERUSER` or extension creation rights.

---

## Injection Silence Is Now Correct Behavior

**Severity: BEHAVIORAL CHANGE**

Since learning-memory-v4, `InjectionFloor` defaults to `0` and the inject path no longer force-fills empty result sets with top-importance observations. If no candidate survives relevance filtering, Engram returns silence.

**Effect:** An empty relevant-memory section is no longer a bug by itself. It often means the retrieval path correctly rejected noise.

**Migration path:**
- Keep the v4 behavior (recommended): leave `ENGRAM_INJECTION_FLOOR` unset or set it to `0`
- Restore legacy always-fill behavior temporarily: set `ENGRAM_INJECTION_FLOOR=3`

---

## Inject Uses Unified Retrieval By Default

**Severity: BEHAVIORAL CHANGE**

`ENGRAM_INJECT_UNIFIED` now defaults to `true`, which means inject uses the same retrieval path as search. This unifies score thresholds, freshness filtering, typed lanes, file filters, BFS expansion, and later retrieval improvements under one code path.

**Effect:** Inject behavior may change immediately when search-path ranking changes — by design. This reduces drift between “what search finds” and “what inject surfaces”.

**Rollback path:** Set `ENGRAM_INJECT_UNIFIED=false` only as an emergency kill-switch to temporarily reactivate the legacy inject path.

---

## Hooks Communicate Via HTTP (Worker Must Be Running)

**Severity: OPERATIONAL**

All Claude Code lifecycle hooks (session-start, user-prompt, post-tool-use, subagent-stop, stop, statusline) make HTTP requests to the worker on port 37777. If the worker is not running:

- `session-start`: context injection silently fails (returns empty string — no error to Claude Code)
- Other hooks: silently fail (logged to stderr)

**There is no queuing or retry.** Hook events are fire-and-forget. Observations from sessions where the worker was down are permanently lost.

---

## Stop Hook Reads Transcript File Directly

**Severity: INFORMATIONAL**

The `stop` hook reads the Claude Code transcript JSONL file directly from disk (`transcript_path` from hook input) to extract the last user and assistant messages for session summarization. This assumes the transcript file is accessible from the hook process (same machine).

In a remote/containerized setup where Claude Code and the hooks run on different filesystems, this will silently fail and the summary will be generated without transcript context.

---

## Multi-Workstation: Workers Still Need Local PostgreSQL Access

**Severity: ARCHITECTURAL**

The worker serves MCP over both SSE (`/sse`) and Streamable HTTP (`/mcp`) transports, allowing remote Claude Code workstations to connect. However, the worker still needs direct PostgreSQL access. The transport layer is an adapter — it does not add a separate data tier.

**Typical multi-workstation setup:**
- PostgreSQL server accessible to all nodes
- One or more worker processes on a central host
- Remote workstations use `mcp-stdio-proxy` to forward MCP to the central worker
- All workstations set `DATABASE_DSN` pointing to the shared PostgreSQL instance

---

## Embedding Provider Is OpenAI-Only

**Severity: INFORMATIONAL**

The ONNX/builtin embedding provider has been removed. Only the OpenAI-compatible REST API provider (`EMBEDDING_PROVIDER=openai`) is available. The `ENGRAM_EMBEDDING_MODEL` config field and `DefaultEmbeddingModel` constant are legacy remnants and are not used by the embedding service.
