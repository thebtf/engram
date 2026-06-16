package gorm

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
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

// TestCodeChunkStore_UpdateEmbedding_SetsVector verifies that UpdateEmbedding
// persists a vector on an existing NULL-embedding row and that the row is no
// longer returned by ListUnembedded after the update.
func TestCodeChunkStore_UpdateEmbedding_SetsVector(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("proj-updemb-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})

	chunk := testChunk(proj, "embed_me.go", "emb-001")
	require.NoError(t, store.Upsert(ctx, chunk), "upsert chunk without embedding")

	// Row must appear in ListUnembedded before the embedding is set.
	unembedded, err := store.ListUnembedded(ctx, 10)
	require.NoError(t, err)
	var found bool
	var targetID int64
	for _, c := range unembedded {
		if c.ProjectID == proj {
			found = true
			targetID = c.ID
		}
	}
	require.True(t, found, "newly-inserted chunk must appear in ListUnembedded")
	require.NotZero(t, targetID, "chunk ID must be non-zero after upsert")

	// UpdateEmbedding must not error.
	vec := pgvector.NewVector(make([]float32, 1536))
	require.NoError(t, store.UpdateEmbedding(ctx, targetID, vec), "UpdateEmbedding must succeed")

	// After the update the row must have a non-NULL embedding in the DB.
	var embeddingNullCount int
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM code_chunks WHERE id = ? AND embedding IS NULL", targetID,
	).Scan(&embeddingNullCount).Error)
	require.Equal(t, 0, embeddingNullCount, "embedding must not be NULL after UpdateEmbedding")

	// The row must no longer appear in ListUnembedded.
	unembedded2, err := store.ListUnembedded(ctx, 10)
	require.NoError(t, err)
	for _, c := range unembedded2 {
		if c.ID == targetID {
			t.Fatalf("chunk id=%d still appears in ListUnembedded after UpdateEmbedding", targetID)
		}
	}
}

// TestCodeChunkStore_UpdateEmbedding_InvalidID asserts that id <= 0 is rejected.
func TestCodeChunkStore_UpdateEmbedding_InvalidID(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	vec := pgvector.NewVector(make([]float32, 1536))
	err := store.UpdateEmbedding(ctx, 0, vec)
	require.Error(t, err, "UpdateEmbedding with id=0 must return an error")

	err = store.UpdateEmbedding(ctx, -1, vec)
	require.Error(t, err, "UpdateEmbedding with id=-1 must return an error")
}

// TestCodeChunkStore_UpdateEmbedding_MissingRow_IsNotError asserts that
// UpdateEmbedding for a non-existent row (RowsAffected == 0) does not return an
// error — the row may have been swept concurrently.
func TestCodeChunkStore_UpdateEmbedding_MissingRow_IsNotError(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	// Use an ID that is extremely unlikely to exist.
	const ghostID = int64(999999999)
	vec := pgvector.NewVector(make([]float32, 1536))
	require.NoError(t, store.UpdateEmbedding(ctx, ghostID, vec),
		"UpdateEmbedding on a non-existent row must return nil (concurrent sweep case)")
}

// TestCodeChunkStore_ListUnembedded_ReturnsOnlyNullEmbedding verifies that
// ListUnembedded returns only rows with NULL embedding and respects the limit.
func TestCodeChunkStore_ListUnembedded_ReturnsOnlyNullEmbedding(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("proj-listunb-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})

	// Insert three chunks without embeddings.
	for i := 0; i < 3; i++ {
		require.NoError(t, store.Upsert(ctx, testChunk(proj, fmt.Sprintf("f%d.go", i), fmt.Sprintf("s%d", i))))
	}

	// All three must appear in ListUnembedded with a generous limit.
	all, err := store.ListUnembedded(ctx, 100)
	require.NoError(t, err)
	var projRows []*CodeChunk
	for _, c := range all {
		if c.ProjectID == proj {
			projRows = append(projRows, c)
		}
	}
	require.Len(t, projRows, 3, "all three unembedded rows must be returned")

	// Embed the first row.
	vec := pgvector.NewVector(make([]float32, 1536))
	require.NoError(t, store.UpdateEmbedding(ctx, projRows[0].ID, vec))

	// Now only two rows for this project should be unembedded.
	remaining, err := store.ListUnembedded(ctx, 100)
	require.NoError(t, err)
	var remainingProj []*CodeChunk
	for _, c := range remaining {
		if c.ProjectID == proj {
			remainingProj = append(remainingProj, c)
		}
	}
	require.Len(t, remainingProj, 2, "two unembedded rows must remain after one is embedded")
}

// TestCodeChunkStore_ListUnembedded_LimitRespected verifies that the limit
// parameter restricts the number of returned rows.
func TestCodeChunkStore_ListUnembedded_LimitRespected(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("proj-listlimit-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})

	// Insert five chunks.
	for i := 0; i < 5; i++ {
		require.NoError(t, store.Upsert(ctx, testChunk(proj, fmt.Sprintf("lim%d.go", i), fmt.Sprintf("l%d", i))))
	}

	// Request only 2.
	rows, err := store.ListUnembedded(ctx, 2)
	require.NoError(t, err)

	// Count rows belonging to this project (other tests' rows may also be unembedded).
	var projCount int
	for _, c := range rows {
		if c.ProjectID == proj {
			projCount++
		}
	}
	// We can only assert total returned <= limit, since other unembedded rows may exist.
	require.LessOrEqual(t, len(rows), 2, "ListUnembedded must not exceed the requested limit")
	_ = projCount
}

// TestCodeChunkStore_ListUnembedded_OrderByIDAsc verifies that rows are returned
// in ascending id order (deterministic batching).
func TestCodeChunkStore_ListUnembedded_OrderByIDAsc(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("proj-listord-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})

	for i := 0; i < 3; i++ {
		require.NoError(t, store.Upsert(ctx, testChunk(proj, fmt.Sprintf("ord%d.go", i), fmt.Sprintf("o%d", i))))
	}

	rows, err := store.ListUnembedded(ctx, 100)
	require.NoError(t, err)

	// Extract rows for this project in returned order.
	var projRows []*CodeChunk
	for _, c := range rows {
		if c.ProjectID == proj {
			projRows = append(projRows, c)
		}
	}
	require.Len(t, projRows, 3)
	for i := 1; i < len(projRows); i++ {
		require.Less(t, projRows[i-1].ID, projRows[i].ID,
			"rows must be returned in ascending id order")
	}
}

// TestCodeChunkStore_CountEmbeddedByProject verifies that CountEmbeddedByProject
// returns only chunks where embedding IS NOT NULL.
func TestCodeChunkStore_CountEmbeddedByProject(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("proj-count-emb-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})

	// Insert one chunk without embedding.
	require.NoError(t, store.Upsert(ctx, testChunk(proj, "a.go", "emb-1")))

	// Insert one chunk WITH embedding.
	c2 := testChunk(proj, "b.go", "emb-2")
	vec := pgvector.NewVector(make([]float32, 1536))
	c2.Embedding = &vec
	require.NoError(t, store.Upsert(ctx, c2))

	total, err := store.CountByProject(ctx, proj)
	require.NoError(t, err)
	require.Equal(t, int64(2), total, "total must be 2")

	embedded, err := store.CountEmbeddedByProject(ctx, proj)
	require.NoError(t, err)
	require.Equal(t, int64(1), embedded, "only 1 chunk has an embedding")
}

// TestCodeChunkStore_CountEmbeddedByProject_EmptyProject verifies the zero case:
// a project with no chunks returns 0 (not an error).
func TestCodeChunkStore_CountEmbeddedByProject_EmptyProject(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	proj := fmt.Sprintf("proj-emb-empty-%d", time.Now().UnixNano())
	// No chunks inserted.

	count, err := store.CountEmbeddedByProject(ctx, proj)
	require.NoError(t, err)
	require.Equal(t, int64(0), count)
}

// TestCodeChunkStore_MaxUpdatedAtByProject verifies that MaxUpdatedAtByProject
// returns a valid timestamp when rows exist and (false, nil) when none exist.
func TestCodeChunkStore_MaxUpdatedAtByProject(t *testing.T) {
	db := openCodeChunkTestDB(t)
	store := NewCodeChunkStore(db)
	ctx := context.Background()

	// Empty project — expect false.
	proj := fmt.Sprintf("proj-maxat-%d", time.Now().UnixNano())
	_, ok, err := store.MaxUpdatedAtByProject(ctx, proj)
	require.NoError(t, err)
	require.False(t, ok, "empty project must return ok=false")

	// Insert a chunk.
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM code_chunks WHERE project_id = ?", proj).Error
	})
	require.NoError(t, store.Upsert(ctx, testChunk(proj, "a.go", "maxat-1")))

	ts, ok2, err2 := store.MaxUpdatedAtByProject(ctx, proj)
	require.NoError(t, err2)
	require.True(t, ok2, "non-empty project must return ok=true")
	require.False(t, ts.IsZero(), "returned timestamp must not be zero")
}
