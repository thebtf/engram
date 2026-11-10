package gorm_test

import (
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
)

func TestStatsWithCoverage_OwnedDatabase(t *testing.T) {
	db := dbgorm.OpenEmbeddingStatsFixture(t)
	store := embedding.NewStore(db)
	ctx := db.Statement.Context

	t.Run("empty", func(t *testing.T) {
		stats, err := store.StatsWithCoverage(ctx)
		require.NoError(t, err)
		require.Equal(t, embedding.CoverageStats{}, stats)
	})

	t.Run("one of two active memories", func(t *testing.T) {
		const project = "15c8f723-a6db-4d35-879c-0a0b43e0121f"
		require.NoError(t, db.Exec(`INSERT INTO projects (id) VALUES (?)`, project).Error)
		var memoryID int64
		require.NoError(t, db.Raw(`INSERT INTO memories (project, content, status) VALUES (?, 'memory with chunk', 'active') RETURNING id`, project).Scan(&memoryID).Error)
		require.NotZero(t, memoryID)
		require.NoError(t, db.Exec(`INSERT INTO memories (project, content, status) VALUES (?, 'memory without chunk', 'active')`, project).Error)
		values := make([]float32, embedding.EmbeddingDim)
		values[0], values[1] = 0.1, 0.2
		require.NoError(t, db.Exec(`INSERT INTO content_chunks (memory_id, seq, text, embedding, model) VALUES (?, 0, 'chunk text', ?::vector, 'test-model')`, memoryID, pgvector.NewVector(values)).Error)

		stats, err := store.StatsWithCoverage(ctx)
		require.NoError(t, err)
		require.Equal(t, int64(2), stats.ActiveMemoryCount)
		require.Equal(t, int64(1), stats.ChunkCount)
		require.Equal(t, int64(1), stats.MemoriesWithChunks)
		require.Equal(t, 0.5, stats.EmbeddingCoverage)
		require.Equal(t, embedding.EmbeddingDim, stats.Dimension)
		require.Equal(t, "test-model", stats.Model)
		require.NotNil(t, stats.LastChunkAt)
	})
}
