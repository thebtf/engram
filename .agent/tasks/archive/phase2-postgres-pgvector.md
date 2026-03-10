# Phase 2: PostgreSQL + pgvector Migration

**Status**: IN PROGRESS
**Priority**: T3
**Security**: S2 (DATABASE_DSN env var — credentials)

## Goal
Replace SQLite + sqlite-vec with PostgreSQL + pgvector. Eliminates ALL CGO dependencies
(sqlite-vec-go-bindings/cgo, mattn/go-sqlite3). Enables multi-workstation shared storage.

## Credentials
```
DATABASE_DSN=postgresql://mnemonic_user:Xk9m!vLp3Qr7wNz2Ygf@unleashed.lan:5432/mnemonic
pgvector version: 0.8.0
```

## Architecture After Migration

```
SQLite + sqlite-vec (CGO)       →    PostgreSQL + pgvector (pure Go)
gorm.io/driver/sqlite           →    gorm.io/driver/postgres
github.com/mattn/go-sqlite3     →    github.com/lib/pq (via GORM postgres driver)
github.com/asg017/sqlite-vec... →    github.com/pgvector/pgvector-go
internal/vector/sqlitevec/      →    internal/vector/pgvector/  (new)
FTS5 virtual tables             →    tsvector + GIN indexes
sqlite-vec VIRTUAL TABLE vec0   →    pgvector VECTOR type + HNSW index
WAL/pragmas                     →    connection pooling via DSN
```

## Files to CREATE
- `internal/vector/types.go` — shared types extracted from sqlitevec/helpers.go
- `internal/vector/pgvector/client.go` — pgvector implementation of vector.Client
- `internal/vector/pgvector/sync.go` — vector sync (parallel to sqlitevec/sync.go)

## Files to REWRITE
- `internal/vector/interface.go` — remove sqlitevec import, use local vector package types
- `internal/db/gorm/store.go` — PostgreSQL connection (DSN instead of file path)
- `internal/db/gorm/migrations.go` — PostgreSQL schema (tsvector, pgvector VECTOR columns)
- `internal/db/gorm/observation_store.go` — FTS5 MATCH → tsvector @@ plainto_tsquery
- `internal/config/config.go` — add DATABASE_DSN field + env var
- `internal/worker/service.go` — use pgvector client instead of sqlitevec
- `go.mod` + `go.sum` — swap sqlite deps for postgres deps

## Files to ADD BUILD TAGS (//go:build ignore)
- `internal/vector/sqlitevec/client.go` — CGO, no longer used
- `internal/vector/hybrid/client.go` — wraps sqlitevec, no longer used

## go.mod Changes

### Remove
```
github.com/asg017/sqlite-vec-go-bindings v0.1.6
github.com/mattn/go-sqlite3 v1.14.34
gorm.io/driver/sqlite v1.6.0
```

### Add
```
gorm.io/driver/postgres
github.com/pgvector/pgvector-go
```
Note: lib/pq is pulled in by gorm.io/driver/postgres transitively.

## Wave 1: Types Refactor + Config (PREREQUISITE, PARALLEL)

### Wave 1A: Shared Types + Interface

**File to CREATE: `internal/vector/types.go`**

Extract these types/functions from `internal/vector/sqlitevec/helpers.go` and
`internal/vector/sqlitevec/client.go` into the parent `vector` package:

```go
package vector

// DocType represents the type of document stored in the vector table.
type DocType string

const (
    DocTypeObservation    DocType = "observation"
    DocTypeSessionSummary DocType = "session_summary"
    DocTypeUserPrompt     DocType = "user_prompt"
)

// Document represents a document to store with vector embedding.
type Document struct {
    Metadata map[string]any
    ID       string
    Content  string
}

// QueryResult represents a search result from vector search.
type QueryResult struct {
    Metadata   map[string]any
    ID         string
    Distance   float64
    Similarity float64
}

// StaleVectorInfo contains information about a vector that needs rebuilding.
type StaleVectorInfo struct {
    DocID     string
    DocType   string
    // + other fields from sqlitevec.StaleVectorInfo
}

// DistanceToSimilarity converts cosine distance to similarity score.
func DistanceToSimilarity(distance float64) float64 {
    return 1.0 - (distance / 2.0)
}

// FilterByThreshold filters results above similarity threshold.
func FilterByThreshold(results []QueryResult, threshold float64, maxResults int) []QueryResult { ... }

// BuildWhereFilter creates a where filter map for vector queries.
func BuildWhereFilter(docType DocType, project string) map[string]interface{} { ... }

// ExtractIDsByDocType extracts SQLite/PG IDs from query results, grouped by doc type.
type ExtractedIDs struct {
    ObservationIDs []int64
    SummaryIDs     []int64
    PromptIDs      []int64
}

// ExtractIDsByDocType, ExtractObservationIDs, ExtractSummaryIDs, ExtractPromptIDs
// (copy from sqlitevec/helpers.go, same logic)
```

**File to REWRITE: `internal/vector/interface.go`**

Remove `import "github.com/thebtf/engram/internal/vector/sqlitevec"`.
Use local types:

```go
package vector

import "context"

type Client interface {
    AddDocuments(ctx context.Context, docs []Document) error
    DeleteDocuments(ctx context.Context, ids []string) error
    Query(ctx context.Context, query string, limit int, where map[string]any) ([]QueryResult, error)
    IsConnected() bool
    Close() error
    Count(ctx context.Context) (int64, error)
    ModelVersion() string
    NeedsRebuild(ctx context.Context) (bool, string)
    GetStaleVectors(ctx context.Context) ([]StaleVectorInfo, error)
    DeleteVectorsByDocIDs(ctx context.Context, docIDs []string) error
}
```

**Files to UPDATE (type reference changes)**

All files that use `sqlitevec.Document`, `sqlitevec.QueryResult`, `sqlitevec.StaleVectorInfo`,
`sqlitevec.DocType*`, `sqlitevec.ExtractIDsByDocType`, etc. need to be updated to use
`vector.Document`, `vector.QueryResult`, etc.

Key callers (from grep):
- `internal/vector/hybrid/client.go` (add //go:build ignore OR update)
- `internal/worker/service.go` (use pgvector types)
- `internal/worker/handlers_data.go`

### Wave 1B: Config + go.mod

**File to UPDATE: `internal/config/config.go`**

Add to Config struct:
```go
DatabaseDSN string `json:"-"` // env-only: DATABASE_DSN (never from JSON file — contains password)
DatabaseMaxConns int `json:"database_max_conns"` // default: 10
```

Add to Default():
```go
DatabaseMaxConns: 10,
```

Add to Load() after existing env overrides:
```go
if v := strings.TrimSpace(os.Getenv("DATABASE_DSN")); v != "" {
    cfg.DatabaseDSN = v
}
if v := strings.TrimSpace(os.Getenv("DATABASE_MAX_CONNS")); v != "" {
    if n, err := strconv.Atoi(v); err == nil && n > 0 {
        cfg.DatabaseMaxConns = n
    }
}
```

Add accessor function:
```go
func GetDatabaseDSN() string {
    if v := strings.TrimSpace(os.Getenv("DATABASE_DSN")); v != "" {
        return v
    }
    return Get().DatabaseDSN
}
```

**File to UPDATE: `go.mod`**

Remove:
- `github.com/asg017/sqlite-vec-go-bindings v0.1.6`
- `github.com/mattn/go-sqlite3 v1.14.34`
- `gorm.io/driver/sqlite v1.6.0`

Add:
- `gorm.io/driver/postgres` (latest)
- `github.com/pgvector/pgvector-go` (latest)

## Wave 2: Core Implementation (after Wave 1)

### Wave 2A: PostgreSQL Store

**File to REWRITE: `internal/db/gorm/store.go`**

Replace SQLite-specific connection code with PostgreSQL:

```go
// Config holds database configuration for PostgreSQL.
type Config struct {
    DSN      string          // PostgreSQL DSN: postgres://user:pass@host:5432/db?sslmode=disable
    MaxConns int             // Maximum open connections (default: 10)
    LogLevel logger.LogLevel // GORM log level
}

func NewStore(cfg Config) (*Store, error) {
    // 1. Open PostgreSQL connection
    db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{
        Logger:      logger.Default.LogMode(cfg.LogLevel),
        PrepareStmt: true,
        NowFunc:     nil,
    })
    if err != nil {
        return nil, fmt.Errorf("open gorm postgres: %w", err)
    }

    // 2. Get raw *sql.DB for connection pool config
    sqlDB, err := db.DB()
    if err != nil {
        return nil, fmt.Errorf("get raw db: %w", err)
    }

    // 3. Configure pool (PostgreSQL benefits from more connections than SQLite)
    maxConns := cfg.MaxConns
    if maxConns <= 0 {
        maxConns = 10
    }
    sqlDB.SetMaxOpenConns(maxConns)
    sqlDB.SetMaxIdleConns(maxConns / 2)
    sqlDB.SetConnMaxLifetime(time.Hour)
    sqlDB.SetConnMaxIdleTime(30 * time.Minute)

    // 4. Verify connection
    if err := sqlDB.Ping(); err != nil {
        return nil, fmt.Errorf("ping database: %w", err)
    }

    store := &Store{...}

    // 5. Run migrations
    if err := runMigrations(db, sqlDB); err != nil {
        return nil, fmt.Errorf("run migrations: %w", err)
    }

    // 6. Warm connection pool
    store.WarmPool(maxConns)
    return store, nil
}
```

Remove: `sqlite_vec.Auto()`, WAL pragmas, `busy_timeout`, SQLite-specific code.
Remove imports: `sqlite_vec`, `go-sqlite3`, `gorm.io/driver/sqlite`.
Add imports: `gorm.io/driver/postgres`.

The `Optimize()` method becomes:
```go
func (s *Store) Optimize(ctx context.Context) error {
    _, err := s.sqlDB.ExecContext(ctx, "ANALYZE")
    return err
}
```

### Wave 2B: pgvector Client

**File to CREATE: `internal/vector/pgvector/client.go`**

Implements `vector.Client` interface for PostgreSQL + pgvector.

The vectors table schema (created by migrations.go):
```sql
CREATE TABLE IF NOT EXISTS vectors (
    doc_id TEXT PRIMARY KEY,
    embedding VECTOR(384),
    sqlite_id BIGINT,         -- keep name for backward compat, stores PG row id
    doc_type TEXT,
    field_type TEXT,
    project TEXT,
    scope TEXT,
    model_version TEXT
);
CREATE INDEX IF NOT EXISTS idx_vectors_hnsw ON vectors USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);
CREATE INDEX IF NOT EXISTS idx_vectors_doc_type ON vectors(doc_type, project);
```

Client struct:
```go
type Client struct {
    embeddingGroup     singleflight.Group
    resultCache        map[string]resultCacheEntry
    db                 *sql.DB
    embedSvc           *embedding.Service
    queryCache         map[string]embeddingCacheEntry
    stopCleanup        chan struct{}
    // ... (same cache structure as sqlitevec)
    modelVersion       string
}

type Config struct {
    DB *sql.DB
}

func NewClient(cfg Config, embedSvc *embedding.Service) (*Client, error) { ... }
```

Key method — AddDocuments:
```go
func (c *Client) AddDocuments(ctx context.Context, docs []vector.Document) error {
    // For each doc:
    // 1. Compute embedding via embedSvc.Embed(doc.Content)
    // 2. INSERT INTO vectors (doc_id, embedding, sqlite_id, doc_type, field_type, project, scope, model_version)
    //    VALUES ($1, $2::vector, $3, $4, $5, $6, $7, $8)
    //    ON CONFLICT (doc_id) DO UPDATE SET embedding = EXCLUDED.embedding, model_version = EXCLUDED.model_version
    // Use pgvector.NewVector(embedding) for the VECTOR type
}
```

Key method — Query:
```go
func (c *Client) Query(ctx context.Context, query string, limit int, where map[string]any) ([]vector.QueryResult, error) {
    // 1. Embed the query: embedding, err := embedSvc.Embed(query)
    // 2. Build WHERE clause from `where` map
    // 3. SELECT doc_id, sqlite_id, doc_type, project, scope,
    //           embedding <=> $1::vector AS distance
    //    FROM vectors
    //    WHERE doc_type = $2 AND project = $3  -- if in where map
    //    ORDER BY distance ASC
    //    LIMIT $n
    // 4. Convert distance to similarity: 1.0 - distance/2.0
}
```

Other required methods from interface:
- `IsConnected()` — ping
- `Close()` — close nothing (DB managed externally)
- `Count(ctx)` — SELECT COUNT(*) FROM vectors
- `ModelVersion()` — return stored model version
- `NeedsRebuild(ctx)` — check if vectors table empty or model_version mismatch
- `GetStaleVectors(ctx)` — SELECT doc_id, doc_type, ... FROM vectors WHERE model_version != $current
- `DeleteVectorsByDocIDs(ctx, docIDs)` — DELETE FROM vectors WHERE doc_id = ANY($1)
- `DeleteDocuments(ctx, ids)` — DELETE FROM vectors WHERE doc_id = ANY($1)

Import pgvector: `github.com/pgvector/pgvector-go`
Use: `pgvector.NewVector([]float32{...})` for parameter binding.

**File to CREATE: `internal/vector/pgvector/sync.go`**

Port from `internal/vector/sqlitevec/sync.go`:
- Same `Sync` struct and `BatchSyncConfig`
- Uses `vector.Client` interface (not sqlitevec-specific)
- `DefaultBatchSyncConfig()` → same defaults
- `SyncObservations`, `SyncSummaries`, `SyncPrompts` methods

## Wave 3: Schema + FTS + Wire-up (after Wave 2)

### Wave 3A: PostgreSQL Migrations

**File to REWRITE: `internal/db/gorm/migrations.go`**

Full rewrite of ALL 16 migrations for PostgreSQL. Key changes:

Migration 001-002, 007-011: AutoMigrate unchanged (GORM handles cross-driver).
Note: add `"gorm.io/datatypes"` or ensure GORM AutoMigrate handles check constraints properly.

Migration 003 (user_prompts FTS):
```sql
-- Instead of FTS5 virtual table, add tsvector column + trigger + GIN index
ALTER TABLE user_prompts ADD COLUMN IF NOT EXISTS search_vector tsvector;
CREATE INDEX IF NOT EXISTS idx_user_prompts_fts ON user_prompts USING GIN(search_vector);
CREATE OR REPLACE FUNCTION update_user_prompts_search_vector()
RETURNS TRIGGER AS $$
BEGIN
    NEW.search_vector := to_tsvector('english', COALESCE(NEW.prompt_text, ''));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE OR REPLACE TRIGGER user_prompts_search_update
    BEFORE INSERT OR UPDATE ON user_prompts
    FOR EACH ROW EXECUTE FUNCTION update_user_prompts_search_vector();
-- Backfill existing rows
UPDATE user_prompts SET search_vector = to_tsvector('english', COALESCE(prompt_text, ''));
```

Migration 004 (observations FTS):
```sql
ALTER TABLE observations ADD COLUMN IF NOT EXISTS search_vector tsvector;
CREATE INDEX IF NOT EXISTS idx_observations_fts ON observations USING GIN(search_vector);
CREATE OR REPLACE FUNCTION update_observations_search_vector()
RETURNS TRIGGER AS $$
BEGIN
    NEW.search_vector := to_tsvector('english',
        COALESCE(NEW.title, '') || ' ' ||
        COALESCE(NEW.subtitle, '') || ' ' ||
        COALESCE(NEW.narrative, ''));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE OR REPLACE TRIGGER observations_search_update
    BEFORE INSERT OR UPDATE ON observations
    FOR EACH ROW EXECUTE FUNCTION update_observations_search_vector();
UPDATE observations SET search_vector = to_tsvector('english',
    COALESCE(title, '') || ' ' || COALESCE(subtitle, '') || ' ' || COALESCE(narrative, ''));
```

Migration 005 (session_summaries FTS) — same pattern as 003/004.

Migration 006 (vectors table - REPLACE sqlite-vec):
```sql
-- Require pgvector extension (must be pre-installed on DB)
CREATE EXTENSION IF NOT EXISTS vector;
CREATE TABLE IF NOT EXISTS vectors (
    doc_id TEXT PRIMARY KEY,
    embedding VECTOR(384),
    sqlite_id BIGINT,
    doc_type TEXT,
    field_type TEXT,
    project TEXT,
    scope TEXT,
    model_version TEXT
);
-- HNSW index for approximate nearest neighbor search
CREATE INDEX IF NOT EXISTS idx_vectors_hnsw
    ON vectors USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);
CREATE INDEX IF NOT EXISTS idx_vectors_doc_type_project ON vectors(doc_type, project);
```

Migration 010 (patterns FTS) — same pattern as 004.

Migrations 012-016 (indexes): Fix partial WHERE clause syntax for PostgreSQL:
- SQLite: `WHERE is_deprecated = 0` → PostgreSQL: `WHERE is_deprecated = false` OR keep as integer (if column stays INTEGER type)
- PostgreSQL supports partial indexes the same way, syntax is compatible for TEXT/INTEGER
- Remove SQLite-specific `OR IS NULL` patterns that don't translate to PostgreSQL as well

### Wave 3B: Observation Store FTS

**File to UPDATE: `internal/db/gorm/observation_store.go`**

Replace `SearchObservationsFTS` method:

Current (SQLite FTS5):
```go
ftsQuery := `
    SELECT o.id, ...
    FROM observations o
    JOIN observations_fts fts ON o.id = fts.rowid
    WHERE observations_fts MATCH ?
      AND (o.project = ? OR o.scope = 'global')
    ORDER BY rank, COALESCE(o.importance_score, 1.0) DESC
    LIMIT ?`
```

New (PostgreSQL tsvector):
```go
ftsQuery := `
    SELECT o.id, o.sdk_session_id, o.project, COALESCE(o.scope, 'project') as scope, o.type,
           o.title, o.subtitle, o.facts, o.narrative, o.concepts, o.files_read, o.files_modified,
           o.files_modified_at, o.file_mtimes,
           o.importance_score, o.user_feedback, o.retrieval_count, o.prompt_number,
           o.discovery_tokens, o.created_at, o.created_at_epoch, o.is_superseded,
           o.last_retrieved_at_epoch, o.score_updated_at_epoch,
           COALESCE(o.is_superseded, 0) as is_superseded
    FROM observations o
    WHERE o.search_vector @@ plainto_tsquery('english', $1)
      AND (o.project = $2 OR o.scope = 'global')
      AND (o.is_archived = 0 OR o.is_archived IS NULL)
      AND (o.is_superseded = 0 OR o.is_superseded IS NULL)
    ORDER BY ts_rank(o.search_vector, plainto_tsquery('english', $1)) DESC,
             COALESCE(o.importance_score, 1.0) DESC
    LIMIT $3`
```

Note: Change `?` placeholders to `$1, $2, $3` (PostgreSQL style).

Also update `searchObservationsLike` to use `$1` style placeholders (PostgreSQL doesn't use `?`):
```go
// LIKE fallback for PostgreSQL
query := s.db.Where("(title LIKE $1 OR subtitle LIKE $1 OR narrative LIKE $1) AND (project = $2 OR scope = 'global')", ...)
```

Wait — actually GORM handles parameter placeholders automatically through its API. Only raw SQL queries need explicit `$N`. Check which queries use rawDB.QueryContext vs GORM.

Also update prompt_store.go and pattern_store.go if they use FTS5 MATCH queries.

### Wave 3C: Wire Up service.go

**File to UPDATE: `internal/worker/service.go`**

1. Change imports: remove sqlitevec, add pgvector
2. Change struct fields:
   ```go
   vectorClient vector.Client      // interface, was *sqlitevec.Client
   vectorSync   *pgvector.Sync     // was *sqlitevec.Sync
   ```
3. Change initializeAsync():
   ```go
   // Create pgvector client using the same PostgreSQL DB connection
   pgvecClient, err := pgvector.NewClient(pgvector.Config{
       DB: store.GetRawDB(),
   }, embedSvc)
   if err != nil {
       log.Warn().Err(err).Msg("pgvector client creation failed")
   } else {
       vectorClient = pgvecClient
       vectorSync = pgvector.NewSync(pgvecClient)
   }
   ```
4. Update all sqlitevec.DefaultBatchSyncConfig() → pgvector.DefaultBatchSyncConfig()
5. Update function signatures that take `*sqlitevec.Client`, `*sqlitevec.Sync` → `vector.Client`, `*pgvector.Sync`

Also update NewStore() call to use DSN:
```go
store, err := gorm.NewStore(gorm.Config{
    DSN:      config.GetDatabaseDSN(),
    MaxConns: cfg.MaxConns,
    LogLevel: logger.Silent,
})
```

## Testing Strategy

After each wave:
1. `go build ./...` — verify compilation (will fail if deps/types wrong)
2. `go vet ./...` — check for errors
3. Connect test: verify `SELECT 1` against unleashed.lan PostgreSQL

Final validation:
```bash
DATABASE_DSN="postgresql://mnemonic_user:Xk9m!vLp3Qr7wNz2Ygf@unleashed.lan:5432/mnemonic" \
EMBEDDING_PROVIDER=builtin \
go run ./cmd/worker
```

## SQLite Compatibility Layer

Files in `internal/vector/sqlitevec/` that have CGO dependencies:
- Add `//go:build ignore` to `client.go` (has sqlite_vec CGO import)
- Add `//go:build ignore` to `sync.go` if it imports from client.go

Files in `internal/vector/hybrid/`:
- `client.go` imports sqlitevec — add `//go:build ignore`
- Keep `autotuner.go`, `config.go`, `graph_search.go` (pure Go, useful for future)
- Keep `metrics.go` (pure Go)

## Security Notes (S2)
- DATABASE_DSN must NEVER be in JSON config files (contains password)
- Use env-only pattern: same as EMBEDDING_API_KEY in Phase 1
- In config.go, use `json:"-"` tag on DatabaseDSN field
- Log DSN with password redacted when debugging (show only host:port)

## Commit Plan
- feat: wave1 - extract vector types + update interface
- feat: wave1b - add PostgreSQL config + update go.mod
- feat: wave2a - PostgreSQL GORM store
- feat: wave2b - pgvector client + sync
- feat: wave3 - PostgreSQL migrations + FTS → tsvector
- feat: wave3c - wire pgvector into worker service
- chore: ignore deprecated sqlitevec/hybrid CGO files
