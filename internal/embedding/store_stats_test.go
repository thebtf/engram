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

// build4096Vec returns a pgvector.Vector with 4096 dimensions.
// All entries are zero except index 0 (0.1) and index 1 (0.2), which ensures
// vector_dims returns 4096 and the vector is non-trivial for sanity.
func build4096Vec() pgvector.Vector {
	f := make([]float32, 4096)
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
	// Dimension is 0 when the table is empty or 4096 when rows exist; both valid.
	if stats.Dimension != 0 && stats.Dimension != 4096 {
		t.Errorf("Dimension = %d, want 0 or 4096", stats.Dimension)
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
//   - The vector dimension is 4096 to match the production column type
//     (migration 108: vector(4096)).
func TestStoreStats_Populated(t *testing.T) {
	db, closeDB := openEmbeddingTestDB(t)
	defer closeDB()

	tx, rollback := openTestTx(t, db)
	defer rollback()

	// Insert a minimal parent memory row.
	// Only `project` and `content` are required; all other columns have DB defaults.
	if err := tx.Exec(
		`INSERT INTO memories (project, content) VALUES ('__test__', 'stats test memory')`,
	).Error; err != nil {
		t.Fatalf("insert parent memory: %v", err)
	}

	// Retrieve the auto-assigned ID so the FK on content_chunks is satisfied.
	var memoryID int64
	if err := tx.Raw(`SELECT lastval()`).Scan(&memoryID).Error; err != nil {
		t.Fatalf("get lastval for memory id: %v", err)
	}

	// Insert 2 chunks with a 4096-dim vector matching the production schema.
	vec := build4096Vec()
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
	if stats.Dimension != 4096 {
		t.Errorf("Dimension = %d, want 4096", stats.Dimension)
	}
	if stats.Model == "" {
		t.Error("Model is empty, want non-empty")
	}
}
