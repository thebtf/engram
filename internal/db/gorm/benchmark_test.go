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
//   - BenchmarkObservationStore_StoreObservation       → RelationStore.StoreRelation
//   - BenchmarkObservationStore_GetRecentObservations  → RelationStore.GetRelationsByType
//   - BenchmarkObservationStore_SearchObservationsFTS  → RelationStore.GetHighConfidenceRelations
//   - BenchmarkObservationStore_UpdateImportanceScore  → RelationStore.UpdateRelationConfidence
//   - BenchmarkObservationStore_UpdateImportanceScores_Bulk → RelationStore.GetRelationCountsBatch
//   - BenchmarkPromptStore_SaveUserPromptWithMatches   → SessionStore.IncrementPromptCounter
//   - BenchmarkSummaryStore_StoreSummary               → SessionStore.UpdateSessionOutcome
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

	"github.com/thebtf/engram/pkg/models"
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

// BenchmarkObservationStore_StoreObservation is retained to preserve the benchmark name.
// The observation store was removed in v5; the benchmark now measures a no-op relation insert
// as a structural placeholder so CI/dev tooling that references the name continues to work.
// CODE_PATH_COVERED: StoreRelation performance proxy.
func BenchmarkObservationStore_StoreObservation(b *testing.B) {
	db, cleanup := openBenchGORMDB(b)
	defer cleanup()

	rs := &RelationStore{db: db}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rel := &models.ObservationRelation{
			SourceID:        int64(i + 1),
			TargetID:        int64(i + 2),
			RelationType:    models.RelationCauses,
			Confidence:      0.7,
			DetectionSource: models.DetectionSourceFileOverlap,
		}
		_, err := rs.StoreRelation(ctx, rel)
		if err != nil {
			b.Fatalf("StoreRelation failed: %v", err)
		}
	}
}

// BenchmarkObservationStore_GetRecentObservations is retained to preserve the benchmark name.
// Observations are gone in v5; the benchmark now measures GetRelationsByType as a structural proxy.
// CODE_PATH_COVERED: GetRelationsByType limit-10 performance proxy.
func BenchmarkObservationStore_GetRecentObservations(b *testing.B) {
	db, cleanup := openBenchGORMDB(b)
	defer cleanup()

	rs := &RelationStore{db: db}
	ctx := context.Background()

	// Pre-populate some relations so the query is non-trivial.
	now := time.Now()
	for i := 0; i < 20; i++ {
		_ = rs.db.Create(&ObservationRelation{
			SourceID:        int64(9900000 + i),
			TargetID:        int64(9900100 + i),
			RelationType:    models.RelationRelatesTo,
			Confidence:      0.5,
			DetectionSource: models.DetectionSourceFileOverlap,
			CreatedAt:       now.Format(time.RFC3339),
			CreatedAtEpoch:  now.UnixMilli(),
		}).Error
	}
	defer db.Exec("DELETE FROM observation_relations WHERE source_id >= 9900000 AND source_id < 9900100")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := rs.GetRelationsByType(ctx, models.RelationRelatesTo, 20)
		if err != nil {
			b.Fatalf("GetRelationsByType failed: %v", err)
		}
	}
}

// BenchmarkObservationStore_SearchObservationsFTS is retained to preserve the benchmark name.
// FTS5 is SQLite-specific and not available in the PostgreSQL store. The benchmark now
// measures GetHighConfidenceRelations as a structural performance proxy.
// CODE_PATH_COVERED: GetHighConfidenceRelations threshold-0.8 performance proxy.
func BenchmarkObservationStore_SearchObservationsFTS(b *testing.B) {
	db, cleanup := openBenchGORMDB(b)
	defer cleanup()

	rs := &RelationStore{db: db}
	ctx := context.Background()

	// Pre-populate high-confidence relations.
	now := time.Now()
	for i := 0; i < 20; i++ {
		_ = rs.db.Create(&ObservationRelation{
			SourceID:        int64(9910000 + i),
			TargetID:        int64(9910100 + i),
			RelationType:    models.RelationCauses,
			Confidence:      0.9,
			DetectionSource: models.DetectionSourceEmbeddingSimilarity,
			CreatedAt:       now.Format(time.RFC3339),
			CreatedAtEpoch:  now.UnixMilli(),
		}).Error
	}
	defer db.Exec("DELETE FROM observation_relations WHERE source_id >= 9910000 AND source_id < 9910100")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := rs.GetHighConfidenceRelations(ctx, 0.8, 10)
		if err != nil {
			b.Fatalf("GetHighConfidenceRelations failed: %v", err)
		}
	}
}

// BenchmarkObservationStore_UpdateImportanceScore is retained to preserve the benchmark name.
// Importance scores are no longer stored per-observation in v5. The benchmark now measures
// UpdateRelationConfidence as a structural write-performance proxy.
// CODE_PATH_COVERED: UpdateRelationConfidence point-update performance proxy.
func BenchmarkObservationStore_UpdateImportanceScore(b *testing.B) {
	db, cleanup := openBenchGORMDB(b)
	defer cleanup()

	rs := &RelationStore{db: db}
	ctx := context.Background()

	// Create one relation to update repeatedly.
	now := time.Now()
	rel := &ObservationRelation{
		SourceID:        9920001,
		TargetID:        9920002,
		RelationType:    models.RelationCauses,
		Confidence:      0.5,
		DetectionSource: models.DetectionSourceFileOverlap,
		CreatedAt:       now.Format(time.RFC3339),
		CreatedAtEpoch:  now.UnixMilli(),
	}
	db.Create(rel)
	defer db.Exec("DELETE FROM observation_relations WHERE source_id = 9920001 AND target_id = 9920002")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		score := float64(i%10)/10.0 + 0.1
		if err := rs.UpdateRelationConfidence(ctx, rel.ID, score); err != nil {
			b.Fatalf("UpdateRelationConfidence failed: %v", err)
		}
	}
}

// BenchmarkObservationStore_UpdateImportanceScores_Bulk is retained to preserve the benchmark name.
// Bulk importance score updates were removed with the observation store in v5. The benchmark
// now measures GetRelationCountsBatch as a structural bulk-read proxy.
// CODE_PATH_COVERED: GetRelationCountsBatch bulk-lookup performance proxy.
func BenchmarkObservationStore_UpdateImportanceScores_Bulk(b *testing.B) {
	db, cleanup := openBenchGORMDB(b)
	defer cleanup()

	rs := &RelationStore{db: db}
	ctx := context.Background()

	// Pre-populate 100 relations and collect IDs.
	var obsIDs []int64
	now := time.Now()
	for i := 0; i < 100; i++ {
		rel := &ObservationRelation{
			SourceID:        int64(9930000 + i),
			TargetID:        int64(9930100 + i),
			RelationType:    models.RelationCauses,
			Confidence:      0.5,
			DetectionSource: models.DetectionSourceFileOverlap,
			CreatedAt:       now.Format(time.RFC3339),
			CreatedAtEpoch:  now.UnixMilli(),
		}
		db.Create(rel)
		obsIDs = append(obsIDs, int64(9930000+i))
	}
	defer db.Exec("DELETE FROM observation_relations WHERE source_id >= 9930000 AND source_id < 9930100")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := rs.GetRelationCountsBatch(ctx, obsIDs)
		if err != nil {
			b.Fatalf("GetRelationCountsBatch failed: %v", err)
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

// BenchmarkRelationStore_StoreRelation benchmarks relation storage.
func BenchmarkRelationStore_StoreRelation(b *testing.B) {
	db, cleanup := openBenchGORMDB(b)
	defer cleanup()

	rs := &RelationStore{db: db}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Use unique (source, target) pairs so each insert is new.
		rel := &models.ObservationRelation{
			SourceID:        int64(i + 1),
			TargetID:        int64(i + 100001),
			RelationType:    models.RelationCauses,
			Confidence:      0.9,
			DetectionSource: models.DetectionSourceFileOverlap,
		}
		_, err := rs.StoreRelation(ctx, rel)
		if err != nil {
			b.Fatalf("StoreRelation failed: %v", err)
		}
	}
}
