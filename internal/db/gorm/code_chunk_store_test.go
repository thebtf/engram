package gorm

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openCodeChunkTestDB opens a test PostgreSQL connection and applies all
// migrations. Skips the test when DATABASE_DSN is not set.
func openCodeChunkTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping code_chunk_store integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	require.NoError(t, err, "open test DB")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, runMigrations(db), "runMigrations for code_chunk_store tests")
	return db
}

// testChunk builds a CodeChunk fixture for the given project/file with a
// unique content_sha256 suffix to prevent cross-test key collisions.
func testChunk(projectID, filePath, suffix string) *CodeChunk {
	return &CodeChunk{
		ProjectID:      projectID,
		FilePath:       filePath,
		ByteStart:      0,
		ByteEnd:        100,
		Language:       "go",
		ChunkType:      "function",
		Content:        "func Hello() { fmt.Println(\"hello\") }",
		ContentSHA256:  "sha256-" + suffix,
		IndexSessionID: "sess-" + suffix,
	}
}

// TestCodeChunkStore_Upsert_Inserts verifies that Upsert creates a new row.
func TestCodeChunkStore_Upsert_Inserts(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("proj-upsert-insert-%d", time.Now().UnixNano())
	chunk := testChunk(proj, "pkg/foo/foo.go", "aaa")

	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})

	require.NoError(t, store.Upsert(ctx, chunk))

	count, err := store.CountByProject(ctx, proj)
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "one chunk must exist after Upsert")
}

// TestCodeChunkStore_Upsert_Idempotent verifies that upserting the same key
// twice is a no-op (DO NOTHING on conflict).
func TestCodeChunkStore_Upsert_Idempotent(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("proj-upsert-idem-%d", time.Now().UnixNano())
	chunk := testChunk(proj, "pkg/bar/bar.go", "bbb")

	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})

	require.NoError(t, store.Upsert(ctx, chunk), "first upsert")
	require.NoError(t, store.Upsert(ctx, chunk), "second upsert (idempotent)")

	count, err := store.CountByProject(ctx, proj)
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "count must still be 1 after idempotent upsert")
}

// TestCodeChunkStore_DeleteByProjectFile verifies that chunks for a specific
// (project, file) pair are removed and others are unaffected.
func TestCodeChunkStore_DeleteByProjectFile(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("proj-del-file-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})

	// Insert two chunks for different files.
	require.NoError(t, store.Upsert(ctx, testChunk(proj, "a.go", "c1")))
	require.NoError(t, store.Upsert(ctx, testChunk(proj, "b.go", "c2")))

	count, err := store.CountByProject(ctx, proj)
	require.NoError(t, err)
	require.Equal(t, int64(2), count, "two chunks before delete")

	// Delete only a.go.
	require.NoError(t, store.DeleteByProjectFile(ctx, proj, "a.go"))

	count, err = store.CountByProject(ctx, proj)
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "one chunk must remain after DeleteByProjectFile")

	// Verify the surviving chunk is b.go.
	chunks, err := store.ListByProject(ctx, proj, 10)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.Equal(t, "b.go", chunks[0].FilePath)
}

// TestCodeChunkStore_DeleteStaleForProject_RemovesStale verifies that
// DeleteStaleForProject removes chunks whose composite key is absent from the
// keep-set while leaving chunks in the keep-set intact.
func TestCodeChunkStore_DeleteStaleForProject_RemovesStale(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("proj-stale-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})

	keep := testChunk(proj, "keep.go", "sha-keep")
	stale := testChunk(proj, "stale.go", "sha-stale")

	require.NoError(t, store.Upsert(ctx, keep))
	require.NoError(t, store.Upsert(ctx, stale))

	count, _ := store.CountByProject(ctx, proj)
	require.Equal(t, int64(2), count, "two chunks before stale delete")

	// Keep only the "keep" chunk.
	keepKeys := []string{StaleKey(keep.FilePath, keep.ByteStart, keep.ContentSHA256)}
	require.NoError(t, store.DeleteStaleForProject(ctx, proj, keepKeys))

	count, err := store.CountByProject(ctx, proj)
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "one chunk must remain after DeleteStaleForProject")

	chunks, err := store.ListByProject(ctx, proj, 10)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.Equal(t, "keep.go", chunks[0].FilePath)
}

// TestCodeChunkStore_DeleteStaleForProject_EmptyKeepSet_IsNoop verifies that
// DeleteStaleForProject with an empty keep-set is a safety no-op.
func TestCodeChunkStore_DeleteStaleForProject_EmptyKeepSet_IsNoop(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("proj-stale-noop-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})

	require.NoError(t, store.Upsert(ctx, testChunk(proj, "file.go", "sha-noop")))

	// Empty keep-set must not delete anything.
	require.NoError(t, store.DeleteStaleForProject(ctx, proj, nil))

	count, err := store.CountByProject(ctx, proj)
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "chunk must survive empty keep-set delete (safety guard)")
}

// TestCodeChunkStore_CountByProject verifies CountByProject across zero and
// non-zero states.
func TestCodeChunkStore_CountByProject(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("proj-count-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})

	count, err := store.CountByProject(ctx, proj)
	require.NoError(t, err)
	require.Equal(t, int64(0), count, "count must be 0 before any inserts")

	require.NoError(t, store.Upsert(ctx, testChunk(proj, "one.go", "sha-one")))
	require.NoError(t, store.Upsert(ctx, testChunk(proj, "two.go", "sha-two")))

	count, err = store.CountByProject(ctx, proj)
	require.NoError(t, err)
	require.Equal(t, int64(2), count, "count must reflect inserted chunks")
}

// TestMigration139_CodeChunksTable verifies that migration 139 creates the
// code_chunks table with the expected columns, UNIQUE constraint, and indexes.
//
// Anti-stub: replacing the Migrate body with `return nil` causes the
// table-existence assertion to fail.
func TestMigration139_CodeChunksTable(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping migration 139 integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, sqlDB.Ping())

	require.NoError(t, runMigrations(db), "full migration chain must include 139")

	// Assert table exists.
	var tableCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'code_chunks'
	`).Scan(&tableCount).Error)
	require.Equal(t, 1, tableCount, "code_chunks table must exist after migration 139")

	// Assert all required columns exist.
	expectedCols := []string{
		"id", "project_id", "file_path", "byte_start", "byte_end",
		"language", "chunk_type", "content", "content_sha256",
		"embedding", "content_tsv", "index_session_id", "created_at", "updated_at",
	}
	for _, col := range expectedCols {
		var count int
		require.NoError(t, db.Raw(`
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'code_chunks' AND column_name = ?
		`, col).Scan(&count).Error)
		require.Equal(t, 1, count, "column %q must exist in code_chunks", col)
	}

	// Assert embedding is nullable (chunks may be stored before embedding is computed).
	var embeddingNullable string
	require.NoError(t, db.Raw(`
		SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'code_chunks' AND column_name = 'embedding'
	`).Row().Scan(&embeddingNullable))
	require.Equal(t, "YES", embeddingNullable, "embedding must be nullable (computed async by CR-004)")

	// Assert UNIQUE constraint exists on (project_id, file_path, byte_start, content_sha256).
	var uniqueCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) FROM information_schema.table_constraints
		WHERE table_schema = 'public'
		  AND table_name   = 'code_chunks'
		  AND constraint_type = 'UNIQUE'
	`).Scan(&uniqueCount).Error)
	require.GreaterOrEqual(t, uniqueCount, 1, "UNIQUE constraint must exist on code_chunks")

	// Assert idempotent re-index: inserting the same row twice must not error.
	proj := fmt.Sprintf("mig139-test-%d", time.Now().UnixNano())
	defer func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	}()
	insertSQL := `
		INSERT INTO code_chunks
			(project_id, file_path, byte_start, byte_end, language,
			 chunk_type, content, content_sha256, index_session_id)
		VALUES (?, 'main.go', 0, 50, 'go', 'function', 'func main(){}', 'sha-mig139', 'sess-mig139')
		ON CONFLICT (project_id, file_path, byte_start, content_sha256) DO NOTHING
	`
	require.NoError(t, db.Exec(insertSQL, proj).Error, "first insert must succeed")
	require.NoError(t, db.Exec(insertSQL, proj).Error, "second insert (ON CONFLICT DO NOTHING) must be a no-op")

	var rowCount int
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM code_chunks WHERE project_id = ?", proj,
	).Scan(&rowCount).Error)
	require.Equal(t, 1, rowCount, "idempotent upsert must produce exactly one row")

	// Assert GIN index on content_tsv exists.
	var ginCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) FROM pg_indexes
		WHERE tablename = 'code_chunks' AND indexname = 'idx_code_chunks_tsv'
	`).Scan(&ginCount).Error)
	require.Equal(t, 1, ginCount, "idx_code_chunks_tsv GIN index must exist")

	// Assert HNSW index on embedding exists (native pgvector, no vectorscale).
	var hnswCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) FROM pg_indexes
		WHERE tablename = 'code_chunks' AND indexname = 'idx_code_chunks_hnsw'
	`).Scan(&hnswCount).Error)
	require.Equal(t, 1, hnswCount, "idx_code_chunks_hnsw HNSW index must exist")
}
