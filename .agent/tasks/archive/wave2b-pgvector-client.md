# Wave 2B: Create internal/vector/pgvector/ package

## Task
Create TWO new files implementing pgvector-based vector storage:
1. `internal/vector/pgvector/client.go` — implements `vector.Client` interface
2. `internal/vector/pgvector/sync.go` — ports sqlitevec/sync.go to pgvector

## Module path
`github.com/thebtf/engram` (see go.mod)

## Files to CREATE

---

### File 1: internal/vector/pgvector/client.go

```go
// Package pgvector provides PostgreSQL+pgvector based vector storage for engram.
package pgvector
```

#### Imports needed
```go
import (
    "context"
    "database/sql"
    "fmt"
    "strings"
    "sync"

    "github.com/thebtf/engram/internal/embedding"
    "github.com/thebtf/engram/internal/vector"
    pgvec "github.com/pgvector/pgvector-go"
    "github.com/rs/zerolog/log"
    "gorm.io/gorm"
    "gorm.io/gorm/clause"
)
```

#### vectorRecord struct (GORM model for vectors table)
```go
// vectorRecord is the GORM model for the vectors table (created by migrations).
type vectorRecord struct {
    DocID        string      `gorm:"primaryKey;column:doc_id"`
    Embedding    pgvec.Vector `gorm:"type:vector(384);column:embedding"`
    SQLiteID     int64       `gorm:"column:sqlite_id"`
    DocType      string      `gorm:"column:doc_type"`
    FieldType    string      `gorm:"column:field_type"`
    Project      string      `gorm:"column:project"`
    Scope        string      `gorm:"column:scope"`
    ModelVersion string      `gorm:"column:model_version"`
}

func (vectorRecord) TableName() string { return "vectors" }
```

#### Config struct
```go
// Config holds configuration for the pgvector client.
type Config struct {
    DB       *gorm.DB           // PostgreSQL GORM connection (required)
    EmbedSvc *embedding.Service // Embedding service (required)
}
```

#### Client struct
```go
// Client provides vector operations via PostgreSQL+pgvector.
type Client struct {
    db           *gorm.DB
    sqlDB        *sql.DB
    embedSvc     *embedding.Service
    modelVersion string
    mu           sync.RWMutex
}
```

#### NewClient
```go
// NewClient creates a new pgvector client.
func NewClient(cfg Config) (*Client, error) {
    if cfg.DB == nil {
        return nil, fmt.Errorf("DB is required")
    }
    if cfg.EmbedSvc == nil {
        return nil, fmt.Errorf("EmbedSvc is required")
    }

    sqlDB, err := cfg.DB.DB()
    if err != nil {
        return nil, fmt.Errorf("get sql.DB: %w", err)
    }

    return &Client{
        db:           cfg.DB,
        sqlDB:        sqlDB,
        embedSvc:     cfg.EmbedSvc,
        modelVersion: cfg.EmbedSvc.Model().Version(),
    }, nil
}
```

#### AddDocuments
```go
func (c *Client) AddDocuments(ctx context.Context, docs []vector.Document) error {
    if len(docs) == 0 {
        return nil
    }

    texts := make([]string, len(docs))
    for i, doc := range docs {
        texts[i] = doc.Content
    }

    embeddings, err := c.embedSvc.EmbedBatch(ctx, texts)
    if err != nil {
        return fmt.Errorf("embed batch: %w", err)
    }

    records := make([]vectorRecord, 0, len(docs))
    for i, doc := range docs {
        if len(embeddings[i]) == 0 {
            continue
        }
        meta := doc.Metadata
        rec := vectorRecord{
            DocID:        doc.ID,
            Embedding:    pgvec.NewVector(embeddings[i]),
            SQLiteID:     extractInt64(meta["sqlite_id"]),
            DocType:      extractString(meta["doc_type"]),
            FieldType:    extractString(meta["field_type"]),
            Project:      extractString(meta["project"]),
            Scope:        extractString(meta["scope"]),
            ModelVersion: c.modelVersion,
        }
        records = append(records, rec)
    }

    if len(records) == 0 {
        return nil
    }

    // Upsert: INSERT ... ON CONFLICT (doc_id) DO UPDATE SET ...
    return c.db.WithContext(ctx).
        Clauses(clause.OnConflict{
            Columns:   []clause.Column{{Name: "doc_id"}},
            DoUpdates: clause.AssignmentColumns([]string{
                "embedding", "sqlite_id", "doc_type", "field_type",
                "project", "scope", "model_version",
            }),
        }).
        Create(&records).Error
}
```

#### DeleteDocuments
```go
func (c *Client) DeleteDocuments(ctx context.Context, ids []string) error {
    if len(ids) == 0 {
        return nil
    }
    return c.db.WithContext(ctx).
        Where("doc_id = ANY(?)", stringSliceToArray(ids)).
        Delete(&vectorRecord{}).Error
}
```

#### Query
```go
func (c *Client) Query(ctx context.Context, query string, limit int, where map[string]any) ([]vector.QueryResult, error) {
    if limit <= 0 {
        limit = 10
    }

    embedding, err := c.embedSvc.Embed(ctx, query)
    if err != nil {
        return nil, fmt.Errorf("embed query: %w", err)
    }
    if len(embedding) == 0 {
        return nil, nil
    }

    queryVec := pgvec.NewVector(embedding)

    // Build raw SQL for cosine distance query
    var args []any
    args = append(args, queryVec)
    argIdx := 2

    var whereClauses []string
    for k, v := range where {
        whereClauses = append(whereClauses, fmt.Sprintf("%s = $%d", k, argIdx))
        args = append(args, v)
        argIdx++
    }
    args = append(args, limit)

    sql := fmt.Sprintf(`
        SELECT doc_id, sqlite_id, doc_type, field_type, project, scope, model_version,
               embedding <=> $1 AS distance
        FROM vectors
        %s
        ORDER BY distance
        LIMIT $%d`,
        buildWhereClause(whereClauses),
        argIdx,
    )

    type row struct {
        DocID        string
        SQLiteID     int64   `db:"sqlite_id"`
        DocType      string  `db:"doc_type"`
        FieldType    string  `db:"field_type"`
        Project      string
        Scope        string
        ModelVersion string  `db:"model_version"`
        Distance     float64
    }

    rows, err := c.sqlDB.QueryContext(ctx, sql, args...)
    if err != nil {
        return nil, fmt.Errorf("query vectors: %w", err)
    }
    defer rows.Close()

    var results []vector.QueryResult
    for rows.Next() {
        var r row
        if err := rows.Scan(
            &r.DocID, &r.SQLiteID, &r.DocType, &r.FieldType,
            &r.Project, &r.Scope, &r.ModelVersion, &r.Distance,
        ); err != nil {
            return nil, fmt.Errorf("scan row: %w", err)
        }
        results = append(results, vector.QueryResult{
            ID:         r.DocID,
            Distance:   r.Distance,
            Similarity: vector.DistanceToSimilarity(r.Distance),
            Metadata: map[string]any{
                "sqlite_id":  r.SQLiteID,
                "doc_type":   r.DocType,
                "field_type": r.FieldType,
                "project":    r.Project,
                "scope":      r.Scope,
            },
        })
    }
    return results, rows.Err()
}
```

#### Remaining interface methods
```go
func (c *Client) IsConnected() bool {
    return c.sqlDB.Ping() == nil
}

func (c *Client) Close() error {
    return c.sqlDB.Close()
}

func (c *Client) Count(ctx context.Context) (int64, error) {
    var count int64
    err := c.db.WithContext(ctx).Model(&vectorRecord{}).Count(&count).Error
    return count, err
}

func (c *Client) ModelVersion() string {
    return c.modelVersion
}

func (c *Client) NeedsRebuild(ctx context.Context) (bool, string) {
    var stale int64
    err := c.db.WithContext(ctx).Model(&vectorRecord{}).
        Where("model_version IS NULL OR model_version != ?", c.modelVersion).
        Count(&stale).Error
    if err != nil {
        log.Warn().Err(err).Msg("Failed to check stale vectors")
        return false, ""
    }
    if stale > 0 {
        return true, fmt.Sprintf("%d vectors have stale model version", stale)
    }
    return false, ""
}

func (c *Client) GetStaleVectors(ctx context.Context) ([]vector.StaleVectorInfo, error) {
    var records []vectorRecord
    err := c.db.WithContext(ctx).
        Where("model_version IS NULL OR model_version != ?", c.modelVersion).
        Find(&records).Error
    if err != nil {
        return nil, fmt.Errorf("get stale vectors: %w", err)
    }

    infos := make([]vector.StaleVectorInfo, len(records))
    for i, r := range records {
        infos[i] = vector.StaleVectorInfo{
            DocID:    r.DocID,
            DocType:  r.DocType,
            FieldType: r.FieldType,
            Project:  r.Project,
            Scope:    r.Scope,
            SQLiteID: r.SQLiteID,
        }
    }
    return infos, nil
}

func (c *Client) DeleteVectorsByDocIDs(ctx context.Context, docIDs []string) error {
    if len(docIDs) == 0 {
        return nil
    }
    // docIDs here are string representations of int64 IDs — match by sqlite_id
    return c.db.WithContext(ctx).
        Where("doc_id = ANY(?)", stringSliceToArray(docIDs)).
        Delete(&vectorRecord{}).Error
}
```

#### Helper functions (unexported)
```go
func extractInt64(v any) int64 {
    switch x := v.(type) {
    case int64:
        return x
    case float64:
        return int64(x)
    case int:
        return int64(x)
    }
    return 0
}

func extractString(v any) string {
    if s, ok := v.(string); ok {
        return s
    }
    return ""
}

func stringSliceToArray(s []string) interface{} {
    // For PostgreSQL ANY($1) we need to pass as pq.Array or use a subquery.
    // Use a simple format: pass as string literal array.
    // Actually, use the pgx or lib/pq array. Since we're using gorm with pgx,
    // we can just pass a Go slice and gorm will handle it.
    // Use raw SQL with string building as fallback.
    return s  // gorm postgres driver handles []string as ANY
}

func buildWhereClause(clauses []string) string {
    if len(clauses) == 0 {
        return ""
    }
    return "WHERE " + strings.Join(clauses, " AND ")
}
```

**IMPORTANT NOTE on stringSliceToArray**: For PostgreSQL `ANY($1)` with GORM, the lib/pq driver needs `pq.Array(s)`. But the project uses jackc/pgx. Check if pgx handles `[]string` natively for ANY. If not, build a placeholder list like `($1, $2, ...)` instead.

**SAFER DELETE implementation** (avoids driver compatibility issues):
```go
func (c *Client) DeleteDocuments(ctx context.Context, ids []string) error {
    if len(ids) == 0 {
        return nil
    }
    // Use GORM's In clause which generates proper SQL
    return c.db.WithContext(ctx).
        Where("doc_id IN ?", ids).
        Delete(&vectorRecord{}).Error
}
```

---

### File 2: internal/vector/pgvector/sync.go

Port `internal/vector/sqlitevec/sync.go` directly, replacing:
- Package name: `sqlitevec` → `pgvector`
- Type `*Client` → `*Client` (same package)
- Type `Document` → `vector.Document` (need import `"github.com/thebtf/engram/internal/vector"`)
- Log messages: replace "sqlite-vec" → "pgvector"
- Keep ALL methods: SyncObservation, SyncSummary, SyncUserPrompt, DeleteObservations, DeleteUserPrompts, SyncPattern, DeletePatterns, BatchSyncObservations, BatchSyncSummaries, BatchSyncPrompts
- Internal helpers: formatObservationDocs, formatSummaryDocs, formatPatternDocs
- Keep BatchSyncConfig, DefaultBatchSyncConfig

The sync.go file uses helper functions: `joinStrings`, `copyMetadata`, `copyMetadataMulti`
These should call `vector.JoinStrings`, `vector.CopyMetadata`, `vector.CopyMetadataMulti` from the parent vector package.

Use `vector.Document` type (not sqlitevec.Document) since we import vector package directly.

## Compile Verification
After implementation, these should be true:
- `internal/vector/pgvector` package has no sqlite imports
- `pgvector.Client` satisfies `vector.Client` interface
- `pgvector.Sync` has same public methods as `sqlitevec.Sync`

## LANGUAGE: All file content MUST be English. No exceptions.
