package gorm

// Benchmark and correctness tests for SearchCodeFTS and FindSimilarCode.
//
// How to run:
//
//	DATABASE_DSN="host=localhost user=engram password=... dbname=engram_test sslmode=disable" \
//	  go test ./internal/db/gorm/... -run TestCodeSearch -v
//
//	DATABASE_DSN="..." \
//	  go test ./internal/db/gorm/... -bench BenchmarkCode -benchtime=10s -v
//
// Interpreting benchmark results:
//   - BenchmarkCodeSearchFTS: exercises the GIN tsvector index. Target p50 < 50ms
//     (ADR-001 NFR: Tier-1 hybrid budget is <100ms; FTS alone should use ≤50ms).
//   - BenchmarkCodeFindSimilar: exercises the HNSW vector index. Target p50 < 50ms.
//   - BenchmarkCodeHybridCombined: reflects the total round-trip latency when both
//     legs run sequentially (the orchestrator runs them concurrently; sequential
//     is the worst-case bound). If BenchmarkCodeHybridCombined ns/op is materially
//     larger than 2× the maximum of the two individual benchmarks, the planner is
//     not reusing row caches and the DenseOnly fallback should be considered.
//
// The benchmarks do NOT fail the build on slowness — they are informational
// acceptance gates intended to be run by a human or CI pipeline that reads ns/op
// and decides whether to enable the DenseOnly fallback in CodeHybridOptions.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openBenchDB opens a test DB and runs migrations. Skips when DATABASE_DSN unset.
func openBenchDB(tb testing.TB) *gorm.DB {
	tb.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		tb.Skip("DATABASE_DSN not set, skipping code_chunk search test/bench")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		tb.Fatalf("open bench DB: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		tb.Fatalf("get sql.DB: %v", err)
	}
	tb.Cleanup(func() { _ = sqlDB.Close() })
	if err := runMigrations(db); err != nil {
		tb.Fatalf("runMigrations: %v", err)
	}
	return db
}

// seedCodeChunks inserts n code chunks with embeddings into a unique project and
// returns the projectID and a cleanup function.
func seedCodeChunks(tb testing.TB, db *gorm.DB, n int) (projectID string, cleanup func()) {
	tb.Helper()
	projectID = fmt.Sprintf("bench-proj-%d", rand.Int63())
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	// Vocabulary words used to build varied content so FTS has something to rank.
	words := []string{
		"function", "handler", "router", "middleware", "context",
		"request", "response", "error", "logger", "config",
		"database", "query", "transaction", "model", "schema",
	}

	for i := 0; i < n; i++ {
		word := words[i%len(words)]
		content := fmt.Sprintf("func %s_%d(ctx context.Context) error { return handle_%s() }", word, i, word)
		// Build a unit-length random vector that is directionally varied.
		raw := make([]float32, 1536)
		for j := range raw {
			raw[j] = rand.Float32()
		}
		vec := pgvector.NewVector(raw)
		chunk := &CodeChunk{
			ProjectID:      projectID,
			FilePath:       fmt.Sprintf("pkg/%s/%s_%d.go", word, word, i),
			ByteStart:      i * 100,
			ByteEnd:        i*100 + 80,
			Language:       "go",
			ChunkType:      "function",
			Content:        content,
			ContentSHA256:  fmt.Sprintf("sha-%d", i),
			IndexSessionID: "bench-sess",
			Embedding:      &vec,
		}
		if err := store.Upsert(ctx, chunk); err != nil {
			tb.Fatalf("seed chunk %d: %v", i, err)
		}
	}

	cleanup = func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", projectID).Error
	}
	return projectID, cleanup
}

// randomVec produces a random float32 slice of the given dimension.
func randomVec(dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = rand.Float32()
	}
	return v
}

// --- Correctness tests ---

// TestCodeChunkStore_SearchCodeFTS_FindsByKeyword seeds a chunk with known
// content and asserts SearchCodeFTS returns it when queried by a keyword from
// that content.
func TestCodeChunkStore_SearchCodeFTS_FindsByKeyword(t *testing.T) {
	db := openBenchDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("test-fts-%d", rand.Int63())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})

	// Seed one chunk containing the distinctive keyword "xyzzyroutine".
	chunk := &CodeChunk{
		ProjectID:      proj,
		FilePath:       "pkg/fts/target.go",
		ByteStart:      0,
		ByteEnd:        50,
		Language:       "go",
		ChunkType:      "function",
		Content:        "func xyzzyroutine() { fmt.Println(\"found it\") }",
		ContentSHA256:  "sha-fts-keyword",
		IndexSessionID: "test-sess",
	}
	require.NoError(t, store.Upsert(ctx, chunk))

	// Seed a second chunk that should NOT match.
	other := &CodeChunk{
		ProjectID:      proj,
		FilePath:       "pkg/fts/other.go",
		ByteStart:      0,
		ByteEnd:        30,
		Language:       "go",
		ChunkType:      "function",
		Content:        "func unrelated() {}",
		ContentSHA256:  "sha-fts-other",
		IndexSessionID: "test-sess",
	}
	require.NoError(t, store.Upsert(ctx, other))

	results, err := store.SearchCodeFTS(ctx, proj, "xyzzyroutine", 10)
	require.NoError(t, err)
	require.Len(t, results, 1, "FTS must return exactly the matching chunk")
	require.Equal(t, "pkg/fts/target.go", results[0].FilePath)
	require.Greater(t, results[0].Score, float64(0), "FTS score must be positive")
}

// TestCodeChunkStore_SearchCodeFTS_EmptyInputErrors asserts that an empty
// projectID or query returns an error.
func TestCodeChunkStore_SearchCodeFTS_EmptyInputErrors(t *testing.T) {
	db := openBenchDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	_, err := store.SearchCodeFTS(ctx, "", "query", 10)
	require.Error(t, err, "empty projectID must return error")

	_, err = store.SearchCodeFTS(ctx, "proj", "", 10)
	require.Error(t, err, "empty query must return error")
}

// TestCodeChunkStore_FindSimilarCode_OrderedBySimilarity seeds two chunks: one
// with an embedding identical to the query vector, one with a random embedding.
// Asserts that the identical-vector chunk ranks first and has similarity ≈ 1.0.
func TestCodeChunkStore_FindSimilarCode_OrderedBySimilarity(t *testing.T) {
	db := openBenchDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("test-vec-%d", rand.Int63())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})

	// Query vector: all 0.5 — unit-direction vector.
	queryRaw := make([]float32, 1536)
	for i := range queryRaw {
		queryRaw[i] = 0.5
	}

	// Chunk A: embedding == query vector → similarity should be ≈ 1.0.
	vecA := pgvector.NewVector(queryRaw)
	chunkA := &CodeChunk{
		ProjectID:      proj,
		FilePath:       "pkg/vec/a.go",
		ByteStart:      0,
		ByteEnd:        50,
		Language:       "go",
		ChunkType:      "function",
		Content:        "func A() {}",
		ContentSHA256:  "sha-vec-a",
		IndexSessionID: "test-sess",
		Embedding:      &vecA,
	}
	require.NoError(t, store.Upsert(ctx, chunkA))

	// Chunk B: random embedding — lower similarity.
	rawB := randomVec(1536)
	vecB := pgvector.NewVector(rawB)
	chunkB := &CodeChunk{
		ProjectID:      proj,
		FilePath:       "pkg/vec/b.go",
		ByteStart:      0,
		ByteEnd:        50,
		Language:       "go",
		ChunkType:      "function",
		Content:        "func B() {}",
		ContentSHA256:  "sha-vec-b",
		IndexSessionID: "test-sess",
		Embedding:      &vecB,
	}
	require.NoError(t, store.Upsert(ctx, chunkB))

	// Chunk C: NULL embedding — must be excluded from results.
	chunkC := &CodeChunk{
		ProjectID:      proj,
		FilePath:       "pkg/vec/c.go",
		ByteStart:      0,
		ByteEnd:        50,
		Language:       "go",
		ChunkType:      "function",
		Content:        "func C() {}",
		ContentSHA256:  "sha-vec-c",
		IndexSessionID: "test-sess",
		Embedding:      nil,
	}
	require.NoError(t, store.Upsert(ctx, chunkC))

	results, err := store.FindSimilarCode(ctx, proj, queryRaw, 10, 0.0)
	require.NoError(t, err)

	// Chunk C (NULL embedding) must never appear.
	for _, r := range results {
		require.NotEqual(t, "pkg/vec/c.go", r.FilePath, "NULL-embedding chunk must be excluded")
	}

	// Chunk A must rank first with similarity near 1.0.
	require.NotEmpty(t, results, "at least one result must be returned")
	require.Equal(t, "pkg/vec/a.go", results[0].FilePath, "identical-vector chunk must rank first")
	require.InDelta(t, 1.0, results[0].Score, 0.001, "similarity of identical vectors must be ≈ 1.0")
}

// TestCodeChunkStore_FindSimilarCode_EmptyInputErrors asserts that an empty
// projectID or empty queryVec returns an error.
func TestCodeChunkStore_FindSimilarCode_EmptyInputErrors(t *testing.T) {
	db := openBenchDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	_, err := store.FindSimilarCode(ctx, "", randomVec(1536), 10, 0.7)
	require.Error(t, err, "empty projectID must return error")

	_, err = store.FindSimilarCode(ctx, "proj", nil, 10, 0.7)
	require.Error(t, err, "nil queryVec must return error")

	_, err = store.FindSimilarCode(ctx, "proj", []float32{}, 10, 0.7)
	require.Error(t, err, "empty queryVec must return error")
}

// --- Benchmark gates (informational, not build-blocking) ---

// BenchmarkCodeSearchFTS measures GIN-indexed tsvector search latency.
// Target: p50 < 50ms (ADR-001 NFR; FTS alone uses half the 100ms hybrid budget).
// Run: DATABASE_DSN=... go test ./internal/db/gorm/... -bench BenchmarkCodeSearchFTS -benchtime=10s
func BenchmarkCodeSearchFTS(b *testing.B) {
	db := openBenchDB(b)
	proj, cleanup := seedCodeChunks(b, db, 500)
	b.Cleanup(cleanup)

	store := NewCodeChunkStore(db)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		words := []string{"function", "handler", "router", "middleware", "context"}
		query := words[i%len(words)]
		_, _ = store.SearchCodeFTS(ctx, proj, query, 20)
	}
}

// BenchmarkCodeFindSimilar measures HNSW vector index search latency.
// Target: p50 < 50ms (ADR-001 NFR).
// Run: DATABASE_DSN=... go test ./internal/db/gorm/... -bench BenchmarkCodeFindSimilar -benchtime=10s
func BenchmarkCodeFindSimilar(b *testing.B) {
	db := openBenchDB(b)
	proj, cleanup := seedCodeChunks(b, db, 500)
	b.Cleanup(cleanup)

	store := NewCodeChunkStore(db)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.FindSimilarCode(ctx, proj, randomVec(1536), 10, 0.0)
	}
}

// BenchmarkCodeHybridCombined measures the sequential latency of running both
// FTS and vector legs back-to-back (worst-case bound; the real orchestrator runs
// them concurrently via errgroup so wall-clock is max(fts, vec) not sum).
//
// Interpretation: if this benchmark shows ns/op materially above 100ms, set
// CodeHybridOptions.DenseOnly=true in production callers (ADR-001 risk-4 mitigation).
// Run: DATABASE_DSN=... go test ./internal/db/gorm/... -bench BenchmarkCodeHybridCombined -benchtime=10s
func BenchmarkCodeHybridCombined(b *testing.B) {
	db := openBenchDB(b)
	proj, cleanup := seedCodeChunks(b, db, 500)
	b.Cleanup(cleanup)

	store := NewCodeChunkStore(db)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		words := []string{"function", "handler", "router", "middleware", "context"}
		query := words[i%len(words)]
		_, _ = store.SearchCodeFTS(ctx, proj, query, 20)
		_, _ = store.FindSimilarCode(ctx, proj, randomVec(1536), 10, 0.0)
	}
}
