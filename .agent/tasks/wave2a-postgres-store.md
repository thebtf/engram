# Wave 2A: Rewrite internal/db/gorm/store.go for PostgreSQL

## Task
Rewrite `internal/db/gorm/store.go` to use PostgreSQL via `gorm.io/driver/postgres`.
Remove ALL SQLite-specific code (sqlite_vec, go-sqlite3, WAL pragmas, etc.).

## File to Modify
`internal/db/gorm/store.go`

## Current State (to REMOVE)
- Imports: `sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"`, `_ "github.com/mattn/go-sqlite3"`, `"gorm.io/driver/sqlite"`
- `sqlite_vec.Auto()` call in NewStore
- SQLite connection: `sql.Open("sqlite3", dsn)` with FTS5 support
- `sqlite.Dialector{Conn: sqlDB}` for GORM
- All PRAGMA statements (WAL, synchronous, cache_size, temp_store, mmap_size, page_size, busy_timeout)
- Config.Path field

## New State (to IMPLEMENT)
Replace with PostgreSQL connection using DSN from config.

### New imports
```go
import (
    "context"
    "database/sql"
    "fmt"
    "slices"
    "sync"
    "time"

    "github.com/rs/zerolog/log"
    "gorm.io/driver/postgres"
    "gorm.io/gorm"
    "gorm.io/gorm/logger"
)
```

### New Config struct
```go
// Config holds database configuration.
type Config struct {
    DSN      string          // PostgreSQL DSN (e.g. postgres://user:pass@host/db)
    MaxConns int             // Maximum number of open connections (default: 10)
    LogLevel logger.LogLevel // GORM log level (logger.Silent for production)
}
```

### New NewStore function
```go
func NewStore(cfg Config) (*Store, error) {
    // 1. Open GORM with PostgreSQL driver
    db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{
        Logger:      logger.Default.LogMode(cfg.LogLevel),
        PrepareStmt: true,
        NowFunc:     nil,
    })
    if err != nil {
        return nil, fmt.Errorf("open gorm postgres: %w", err)
    }

    // 2. Get underlying *sql.DB for pool configuration
    sqlDB, err := db.DB()
    if err != nil {
        return nil, fmt.Errorf("get sql.DB: %w", err)
    }

    // 3. Configure connection pool (PostgreSQL connections are expensive)
    maxConns := cfg.MaxConns
    if maxConns <= 0 {
        maxConns = 10
    }
    sqlDB.SetMaxOpenConns(maxConns)
    sqlDB.SetMaxIdleConns(maxConns / 2)
    sqlDB.SetConnMaxLifetime(1 * time.Hour)
    sqlDB.SetConnMaxIdleTime(10 * time.Minute)

    // 4. Verify connection
    if err := sqlDB.Ping(); err != nil {
        return nil, fmt.Errorf("ping postgres: %w", err)
    }

    store := &Store{
        DB:             db,
        sqlDB:          sqlDB,
        metrics:        NewPoolMetrics(100),
        healthCacheTTL: 5 * time.Second,
    }

    // 5. Run migrations
    if err := runMigrations(db, sqlDB); err != nil {
        return nil, fmt.Errorf("run migrations: %w", err)
    }

    // 6. Warm connection pool
    store.WarmPool(maxConns / 2)

    return store, nil
}
```

### Keep unchanged (do NOT modify)
- Store struct definition
- WarmPool method
- Close method
- Ping method
- GetRawDB method (keep the comment but update the description - remove "FTS5" and "sqlite-vec" references, say "tsvector" and "pgvector" instead)
- GetDB method
- Stats method
- Optimize method - CHANGE to use PostgreSQL ANALYZE:
  ```go
  func (s *Store) Optimize(ctx context.Context) error {
      log.Info().Msg("Starting database optimization")
      start := time.Now()
      if _, err := s.sqlDB.ExecContext(ctx, "ANALYZE"); err != nil {
          return fmt.Errorf("analyze: %w", err)
      }
      log.Info().Dur("duration", time.Since(start)).Msg("Database optimization complete")
      return nil
  }
  ```
- HealthCheck, HealthCheckForce, performHealthCheck methods (update SELECT 1 latency check - this works fine in PostgreSQL)
- HealthInfo, PoolStats type definitions
- QueryTimeout constants
- PoolMetrics, NewPoolMetrics, RecordLatency, RecordPoolStats, GetMetricsSummary methods
- MetricsSummary type definition
- GetMetrics, ResetMetrics methods
- WithTimeout method
- ExecWithTimeout method
- QueryRowWithTimeout method
- TransactionWithTimeout method

### Update GetRawDB comment
```go
// GetRawDB returns the underlying *sql.DB for operations GORM can't handle.
// Use this for:
// - tsvector full-text search queries
// - pgvector operations
// - Complex raw SQL queries
func (s *Store) GetRawDB() *sql.DB {
    return s.sqlDB
}
```

## Must NOT Do
- Do NOT remove the Store struct, PoolMetrics, HealthInfo, PoolStats, MetricsSummary types
- Do NOT remove timeout helper methods (WithTimeout, ExecWithTimeout, etc.)
- Do NOT add any SQLite imports
- Do NOT use `database/sql` Open directly - always go through GORM then db.DB()
- Do NOT set ConnMaxLifetime to 0 (PostgreSQL connections need expiry)

## LANGUAGE: All file content MUST be English. No exceptions.
