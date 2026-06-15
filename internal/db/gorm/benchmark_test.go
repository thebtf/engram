// Package gorm provides GORM-based database operations for engram.
//
// # Benchmark naming contract
//
// Several benchmarks below carry legacy names from v4 stores that were removed in v5
// (ObservationStore, PromptStore, SummaryStore). The names are preserved verbatim so
// that CI baseline tooling and historical trend graphs continue to resolve them without
// configuration changes. Each such benchmark body is replaced by a structural proxy
// that exercises a comparable v5 write or read path.
//
// The mapping is:
//
//   - BenchmarkPromptStore_SaveUserPromptWithMatches   → SessionStore.IncrementPromptCounter
//   - BenchmarkSummaryStore_StoreSummary               → SessionStore.UpdateSessionOutcome
//
// (The RelationStore proxy benchmarks were removed in CR-2a of provenance-cleanup
// when the relation store was deleted.)
//
// Any CI tool that compares these benchmarks by name across v4 and v5 baselines will
// measure a different operation post-v5. Baseline keys must be reset at the v5 migration
// boundary. See the CODE_PATH_COVERED annotations on each benchmark for details.
package gorm

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupBenchStore opens a PostgreSQL Store for benchmarking.
// Skips the benchmark when DATABASE_DSN is not set.
func setupBenchStore(b *testing.B) (*Store, func()) {
	b.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		b.Skip("DATABASE_DSN not set, skipping benchmark")
	}

	cfg := Config{
		DSN:      dsn,
		MaxConns: 4,
		LogLevel: logger.Silent,
	}
	store, err := NewStore(cfg)
	if err != nil {
		b.Fatalf("NewStore failed: %v", err)
	}

	cleanup := func() { store.Close() }
	return store, cleanup
}

// openBenchGORMDB opens a raw GORM DB for benchmarks that construct their own stores.
func openBenchGORMDB(b *testing.B) (*gorm.DB, func()) {
	b.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		b.Skip("DATABASE_DSN not set, skipping benchmark")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		b.Fatalf("gorm.Open failed: %v", err)
	}
	if err := runMigrations(db); err != nil {
		b.Fatalf("runMigrations failed: %v", err)
	}

	sqlDB, _ := db.DB()
	return db, func() { sqlDB.Close() }
}

// benchSessionID returns a per-iteration unique session ID string.
func benchSessionID(i int) string {
	return fmt.Sprintf("bench-session-%d-%d", time.Now().UnixNano(), i)
}

// BenchmarkSessionStore_CreateSDKSession benchmarks session creation (most frequent operation).
func BenchmarkSessionStore_CreateSDKSession(b *testing.B) {
	db, cleanup := openBenchGORMDB(b)
	defer cleanup()

	ss := &SessionStore{db: db}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sid := benchSessionID(i)
		_, err := ss.CreateSDKSession(ctx, sid, "bench-project", "test prompt")
		if err != nil {
			b.Fatalf("CreateSDKSession failed: %v", err)
		}
	}
}

// BenchmarkSessionStore_CreateSDKSession_Idempotent benchmarks idempotent session creation (INSERT OR IGNORE).
func BenchmarkSessionStore_CreateSDKSession_Idempotent(b *testing.B) {
	db, cleanup := openBenchGORMDB(b)
	defer cleanup()

	ss := &SessionStore{db: db}
	ctx := context.Background()

	// Pre-create session so all b.N iterations hit the conflict path.
	const sid = "bench-idempotent-session"
	_, _ = ss.CreateSDKSession(ctx, sid, "bench-project", "test prompt")
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = ?", sid)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ss.CreateSDKSession(ctx, sid, "bench-project", "updated prompt")
		if err != nil {
			b.Fatalf("CreateSDKSession (idempotent) failed: %v", err)
		}
	}
}

// BenchmarkPromptStore_SaveUserPromptWithMatches is retained to preserve the benchmark name.
// The prompt store was removed in v5 (user_prompts table dropped). The benchmark now
// measures SessionStore.IncrementPromptCounter as a structural proxy for prompt tracking.
// CODE_PATH_COVERED: IncrementPromptCounter performance proxy.
func BenchmarkPromptStore_SaveUserPromptWithMatches(b *testing.B) {
	db, cleanup := openBenchGORMDB(b)
	defer cleanup()

	ss := &SessionStore{db: db}
	ctx := context.Background()

	sid := fmt.Sprintf("bench-prompt-session-%d", time.Now().UnixNano())
	id, err := ss.CreateSDKSession(ctx, sid, "bench-project", "")
	if err != nil {
		b.Fatalf("CreateSDKSession failed: %v", err)
	}
	defer db.Exec("DELETE FROM sdk_sessions WHERE claude_session_id = ?", sid)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ss.IncrementPromptCounter(ctx, id)
		if err != nil {
			b.Fatalf("IncrementPromptCounter failed: %v", err)
		}
	}
}

// BenchmarkSummaryStore_StoreSummary is retained to preserve the benchmark name.
// The summary store was removed in v5 (session_summaries table dropped). The benchmark now
// measures SessionStore.UpdateSessionOutcome as a structural proxy for session completion recording.
// CODE_PATH_COVERED: UpdateSessionOutcome performance proxy.
func BenchmarkSummaryStore_StoreSummary(b *testing.B) {
	db, cleanup := openBenchGORMDB(b)
	defer cleanup()

	ss := &SessionStore{db: db}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sid := fmt.Sprintf("bench-summary-session-%d-%d", time.Now().UnixNano(), i)
		_, err := ss.CreateSDKSession(ctx, sid, "bench-project", "")
		if err != nil {
			b.Fatalf("CreateSDKSession failed: %v", err)
		}
		err = ss.UpdateSessionOutcome(ctx, sid, "success", "benchmark")
		if err != nil {
			b.Fatalf("UpdateSessionOutcome failed: %v", err)
		}
	}
}

