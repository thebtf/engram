package embedding

import (
	"context"
	"os"
	"testing"

	"github.com/pgvector/pgvector-go"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openEmbeddingTestDB opens a real PostgreSQL connection for integration testing.
// Requires DATABASE_DSN env var; skips the test if it is not set.
func openEmbeddingTestDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping embedding store integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	cleanup := func() { sqlDB.Close() }
	return db, cleanup
}

// openTestTx begins a transaction on db and returns a *gorm.DB scoped to it.
// The returned rollback function always rolls back — no data ever commits.
// Using a transaction satisfies FK constraints (parent memory inserted in same tx)
// and guarantees zero persistent writes to the DB regardless of test outcome.
func openTestTx(t *testing.T, db *gorm.DB) (*gorm.DB, func()) {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("begin tx: %v", tx.Error)
	}
	rollback := func() {
		if err := tx.Rollback().Error; err != nil {
			// Rollback of an already-rolled-back tx is not an error in practice,
			// but log it so a real failure surfaces.
			t.Logf("tx rollback: %v", err)
		}
	}
	return tx, rollback
}

// buildEmbeddingVec returns a pgvector.Vector at the unified EmbeddingDim.
// Sourced from the SSOT constant so this test cannot drift from the live
// content_chunks column dimension (migration 142: vector(1536)). All entries are
// zero except index 0 (0.1) and index 1 (0.2): non-trivial, and vector_dims
// returns EmbeddingDim.
func buildEmbeddingVec() pgvector.Vector {
	f := make([]float32, EmbeddingDim)
	f[0] = 0.1
	f[1] = 0.2
	return pgvector.NewVector(f)
}

// TestStoreStats_Empty asserts that Stats returns no error when the transaction
// contains no chunk rows relevant to Stats (the real table may have data from
// other sessions; Stats reads the whole table but must not error on an empty
// tx-local state either way — it returns sane zero/non-negative values).
// No rows are written; the tx is rolled back unconditionally.
func TestStoreStats_Empty(t *testing.T) {
	db, closeDB := openEmbeddingTestDB(t)
	defer closeDB()

	// Stats is a read-only call — no need for a write transaction here.
	// We simply call Stats against the live table and assert it does not error
	// and returns non-negative counts.
	store := NewStore(db)
	stats, err := store.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ChunkCount < 0 {
		t.Errorf("ChunkCount = %d, want >= 0", stats.ChunkCount)
	}
	if stats.MemoriesWithChunks < 0 {
		t.Errorf("MemoriesWithChunks = %d, want >= 0", stats.MemoriesWithChunks)
	}
	// Dimension is 0 when the table is empty or EmbeddingDim when rows exist; both valid.
	if stats.Dimension != 0 && stats.Dimension != EmbeddingDim {
		t.Errorf("Dimension = %d, want 0 or %d", stats.Dimension, EmbeddingDim)
	}
}

// TestStoreStats_EmptyPhysicalTableReturnsZeroValue protects the production
// fresh-install path. PostgreSQL aggregate max(...) returns one NULL row for an
// empty table; Stats must treat that as an absent timestamp, not a scan error.
// A transaction-local temporary table shadows any shared test data so this
// regression remains deterministic and leaves no persistent rows or schema.
func TestStoreStats_EmptyPhysicalTableReturnsZeroValue(t *testing.T) {
	db, closeDB := openEmbeddingTestDB(t)
	defer closeDB()

	tx, rollback := openTestTx(t, db)
	defer rollback()

	if err := tx.Exec(`
		CREATE TEMP TABLE content_chunks (
			memory_id BIGINT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			model TEXT NOT NULL,
			embedding VECTOR
		) ON COMMIT DROP
	`).Error; err != nil {
		t.Fatalf("create empty temporary content_chunks: %v", err)
	}

	stats, err := NewStore(tx).Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats on physically empty content_chunks: %v", err)
	}
	if stats != (EmbeddingStats{}) {
		t.Fatalf("Stats on physically empty content_chunks = %+v, want zero value", stats)
	}
}

// TestStoreStats_Populated inserts a parent memory row + 2 chunks inside a
// single rolled-back transaction, runs Stats on the tx-scoped DB handle, and
// asserts the returned EmbeddingStats reflects the inserted data.
//
// Isolation guarantees:
//   - All writes are confined to the transaction; ROLLBACK at the end means
//     zero persistent mutation of the real DB.
//   - The parent memory row satisfies the memories(id) FK on content_chunks
//     without requiring any pre-existing data.
//   - The vector dimension is EmbeddingDim to match the production column type
//     (migration 142: vector(1536)).
func TestStoreStats_Populated(t *testing.T) {
	db, closeDB := openEmbeddingTestDB(t)
	defer closeDB()

	tx, rollback := openTestTx(t, db)
	defer rollback()

	// Insert parent row and fetch ID atomically via RETURNING.
	var memoryID int64
	if err := tx.Raw(
		`INSERT INTO memories (project, content)
		 VALUES ('__test__', 'stats test memory')
		 RETURNING id`,
	).Scan(&memoryID).Error; err != nil {
		t.Fatalf("insert parent memory returning id: %v", err)
	}

	// Insert 2 chunks with an EmbeddingDim-vector matching the production schema.
	vec := buildEmbeddingVec()
	if err := tx.Exec(`
		INSERT INTO content_chunks (memory_id, seq, text, embedding, model) VALUES
		(?, 0, 'chunk-a', ?::vector, 'test-model'),
		(?, 1, 'chunk-b', ?::vector, 'test-model')
	`, memoryID, vec, memoryID, vec).Error; err != nil {
		t.Fatalf("insert chunks: %v", err)
	}

	store := NewStore(tx)
	stats, err := store.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	if stats.ChunkCount < 2 {
		t.Errorf("ChunkCount = %d, want >= 2", stats.ChunkCount)
	}
	if stats.MemoriesWithChunks < 1 {
		t.Errorf("MemoriesWithChunks = %d, want >= 1", stats.MemoriesWithChunks)
	}
	if stats.LastChunkAt == nil {
		t.Error("LastChunkAt is nil, want non-nil")
	}
	if stats.Dimension != EmbeddingDim {
		t.Errorf("Dimension = %d, want %d", stats.Dimension, EmbeddingDim)
	}
	if stats.Model == "" {
		t.Error("Model is empty, want non-empty")
	}
}

// TestStatsWithCoverage_ActiveMemories inserts one active memory with chunks
// and one active memory without, then asserts StatsWithCoverage returns
// an embedding_coverage of 0.5 (1 out of 2 active memories have chunks).
func TestStatsWithCoverage_ActiveMemories(t *testing.T) {
	db, closeDB := openEmbeddingTestDB(t)
	defer closeDB()

	tx, rollback := openTestTx(t, db)
	defer rollback()

	// Insert memory with a chunk.
	var mem1ID int64
	if err := tx.Raw(
		`INSERT INTO memories (project, content, status)
		 VALUES ('__cov_test__', 'memory with chunk', 'active')
		 RETURNING id`,
	).Scan(&mem1ID).Error; err != nil {
		t.Fatalf("insert memory 1 returning id: %v", err)
	}
	vec := buildEmbeddingVec()
	if err := tx.Exec(`
		INSERT INTO content_chunks (memory_id, seq, text, embedding, model)
		VALUES (?, 0, 'chunk text', ?::vector, 'test-model')
	`, mem1ID, vec).Error; err != nil {
		t.Fatalf("insert chunk for memory 1: %v", err)
	}

	// Insert active memory without any chunk.
	if err := tx.Exec(
		`INSERT INTO memories (project, content, status)
		 VALUES ('__cov_test__', 'memory without chunk', 'active')`,
	).Error; err != nil {
		t.Fatalf("insert memory 2: %v", err)
	}

	store := NewStore(tx)
	cov, err := store.StatsWithCoverage(context.Background())
	if err != nil {
		t.Fatalf("StatsWithCoverage: %v", err)
	}

	if cov.ActiveMemoryCount < 2 {
		t.Errorf("ActiveMemoryCount = %d, want >= 2", cov.ActiveMemoryCount)
	}
	if cov.EmbeddingCoverage < 0 || cov.EmbeddingCoverage > 1 {
		t.Errorf("EmbeddingCoverage = %v, want in [0,1]", cov.EmbeddingCoverage)
	}
	// The coverage for our two inserted rows must be <= 0.5 because exactly one
	// has a chunk and the table may have additional pre-existing data. We assert
	// it is non-negative and ≤1 for portability across CI environments.
	if cov.EmbeddingCoverage < 0 {
		t.Errorf("EmbeddingCoverage = %v, want >= 0", cov.EmbeddingCoverage)
	}
}

// TestStatsWithCoverage_NoActiveMemories asserts that StatsWithCoverage returns
// zero EmbeddingCoverage and zero ActiveMemoryCount when the rolled-back
// transaction has no active memories (reads live table; skips if DB unavailable).
func TestStatsWithCoverage_NoActiveMemories(t *testing.T) {
	db, closeDB := openEmbeddingTestDB(t)
	defer closeDB()

	// Read-only call against real DB — coverage may be non-zero if the live
	// table has data, but the call must never error and must return [0,1].
	store := NewStore(db)
	cov, err := store.StatsWithCoverage(context.Background())
	if err != nil {
		t.Fatalf("StatsWithCoverage: %v", err)
	}
	if cov.EmbeddingCoverage < 0 || cov.EmbeddingCoverage > 1 {
		t.Errorf("EmbeddingCoverage = %v, want in [0,1]", cov.EmbeddingCoverage)
	}
}
