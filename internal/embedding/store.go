package embedding

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

// EmbeddingStats summarises the current state of the content_chunks table.
// All fields are zero/nil when no chunks exist (empty table is not an error).
type EmbeddingStats struct {
	ChunkCount         int64      `json:"chunk_count"`
	MemoriesWithChunks int64      `json:"memories_with_chunks"`
	LastChunkAt        *time.Time `json:"last_chunk_at"`
	Model              string     `json:"model"`
	Dimension          int        `json:"dimension"`
}

// Stats returns aggregate telemetry for the content_chunks table.
// Returns a zero-value EmbeddingStats (no error) when the table is empty.
func (s *Store) Stats(ctx context.Context) (EmbeddingStats, error) {
	var stats EmbeddingStats

	// Total chunk count.
	if err := s.db.WithContext(ctx).
		Raw(`SELECT count(*) FROM content_chunks`).
		Scan(&stats.ChunkCount).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return EmbeddingStats{}, fmt.Errorf("embedding stats: chunk count: %w", err)
	}

	// Distinct memories that have at least one chunk.
	if err := s.db.WithContext(ctx).
		Raw(`SELECT count(DISTINCT memory_id) FROM content_chunks`).
		Scan(&stats.MemoriesWithChunks).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return EmbeddingStats{}, fmt.Errorf("embedding stats: memories count: %w", err)
	}

	// Most-recent chunk timestamp (nullable — NULL when table is empty).
	var lastAt *time.Time
	if err := s.db.WithContext(ctx).
		Raw(`SELECT max(created_at) FROM content_chunks`).
		Scan(&lastAt).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return EmbeddingStats{}, fmt.Errorf("embedding stats: last chunk at: %w", err)
	}
	stats.LastChunkAt = lastAt

	// Most recently used model (empty string when table is empty).
	var model *string
	if err := s.db.WithContext(ctx).
		Raw(`SELECT model FROM content_chunks ORDER BY created_at DESC LIMIT 1`).
		Scan(&model).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return EmbeddingStats{}, fmt.Errorf("embedding stats: model: %w", err)
	}
	if model != nil {
		stats.Model = *model
	}

	// Embedding dimension via pgvector's vector_dims function (0 when table is empty).
	var dim *int
	if err := s.db.WithContext(ctx).
		Raw(`SELECT vector_dims(embedding) FROM content_chunks LIMIT 1`).
		Scan(&dim).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return EmbeddingStats{}, fmt.Errorf("embedding stats: dimension: %w", err)
	}
	if dim != nil {
		stats.Dimension = *dim
	}

	return stats, nil
}

// CoverageStats extends EmbeddingStats with a coverage ratio derived by
// comparing memories_with_chunks against the total count of active (non-deleted,
// status='active') memories. It is computed by a single query that touches both
// tables rather than a second Stats() call.
type CoverageStats struct {
	EmbeddingStats
	ActiveMemoryCount  int64   `json:"active_memory_count"`
	EmbeddingCoverage  float64 `json:"embedding_coverage"` // fraction 0..1; 0 when no active memories
}

// StatsWithCoverage returns EmbeddingStats plus the embedding coverage ratio.
// Coverage = memories_with_chunks / active_memory_count.
// Active memories are those where deleted_at IS NULL AND status = 'active'.
// Returns a zero-value CoverageStats (no error) when tables are empty.
func (s *Store) StatsWithCoverage(ctx context.Context) (CoverageStats, error) {
	base, err := s.Stats(ctx)
	if err != nil {
		return CoverageStats{}, err
	}

	var activeCount int64
	if err := s.db.WithContext(ctx).
		Raw(`SELECT count(*) FROM memories WHERE deleted_at IS NULL AND status = 'active'`).
		Scan(&activeCount).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return CoverageStats{}, fmt.Errorf("embedding coverage: active memory count: %w", err)
	}

	var coverage float64
	if activeCount > 0 {
		coverage = float64(base.MemoriesWithChunks) / float64(activeCount)
		if coverage > 1.0 {
			coverage = 1.0 // guard: chunks may exist for archived/deleted rows
		}
	}

	return CoverageStats{
		EmbeddingStats:    base,
		ActiveMemoryCount: activeCount,
		EmbeddingCoverage: coverage,
	}, nil
}

// Chunk represents a row in the content_chunks table.
type Chunk struct {
	ID        int64           `gorm:"primaryKey;autoIncrement"`
	MemoryID  int64           `gorm:"not null"`
	Seq       int             `gorm:"not null"`
	Text      string          `gorm:"type:text;not null;default:''"`
	// Dimension is the SSOT EmbeddingDim (1536). The GORM tag must be a compile-time
	// literal so it cannot reference the constant directly; the startup assert in
	// AssertEmbeddingDimensions reconciles this tag, the DDL, and EmbeddingDim against
	// the live column. The raw-SQL DDL was a demolition-phase rollback and AutoMigrate
	// may return, so this tag is load-bearing, not decorative — keep it == EmbeddingDim.
	Embedding pgvector.Vector `gorm:"type:vector(1536)"`
	Model     string          `gorm:"type:text;not null"`
	CreatedAt time.Time       `gorm:"type:timestamptz;not null;default:now()"`
}

func (Chunk) TableName() string { return "content_chunks" }

// Store handles embedding storage and similarity queries.
type Store struct {
	db *gorm.DB
}

// NewStore creates an embedding Store.
func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

// StoreChunks inserts embedding chunks for a memory.
func (s *Store) StoreChunks(ctx context.Context, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Create(&chunks).Error
}

// SimilarResult holds a memory ID and its cosine similarity score.
type SimilarResult struct {
	MemoryID   int64   `json:"memory_id"`
	Similarity float64 `json:"similarity"`
	Text       string  `json:"text"`
}

// FindSimilar queries for chunks closest to the given embedding vector.
// Uses cosine distance (1 - cosine_similarity) via pgvector operator.
// NOTE: this method does NOT filter by project; callers that need project
// isolation must use FindSimilarForProject instead.
func (s *Store) FindSimilar(ctx context.Context, queryVec []float32, limit int, threshold float64) ([]SimilarResult, error) {
	if len(queryVec) == 0 {
		return nil, fmt.Errorf("find similar: query vector must not be empty")
	}
	if limit <= 0 {
		limit = 10
	}
	if threshold <= 0 {
		threshold = 0.7
	}

	vec := pgvector.NewVector(queryVec)
	var results []SimilarResult
	err := s.db.WithContext(ctx).Raw(`
        SELECT cc.memory_id,
               1 - (cc.embedding <=> ?::vector) as similarity,
               cc.text
        FROM content_chunks cc
        WHERE 1 - (cc.embedding <=> ?::vector) >= ?
        ORDER BY cc.embedding <=> ?::vector
        LIMIT ?
    `, vec, vec, threshold, vec, limit).Scan(&results).Error
	if err != nil {
		return nil, fmt.Errorf("find similar: %w", err)
	}
	return results, nil
}

// FindSimilarForProject queries for chunks closest to the given embedding vector,
// scoped to a specific project via a JOIN to the memories table.
// This prevents cross-project leakage through the vector leg of hybrid retrieval.
func (s *Store) FindSimilarForProject(ctx context.Context, project string, queryVec []float32, limit int, threshold float64) ([]SimilarResult, error) {
	if project == "" {
		return nil, fmt.Errorf("find similar for project: project must not be empty")
	}
	if len(queryVec) == 0 {
		return nil, fmt.Errorf("find similar: query vector must not be empty")
	}
	if limit <= 0 {
		limit = 10
	}
	if threshold <= 0 {
		threshold = 0.7
	}

	vec := pgvector.NewVector(queryVec)
	var results []SimilarResult
	// JOIN to memories to enforce project scope — content_chunks has no project column.
	// The memories.status, deleted_at, and validity-window filters are applied here to
	// avoid surfacing deleted/suppressed/expired memories through the vector path.
	// valid_from/valid_until predicates match SearchFTS and GetByIDs (memory_store.go)
	// so that time-bounded memories do not consume the fixed vector limit before
	// GetByIDs post-filtering drops them.
	err := s.db.WithContext(ctx).Raw(`
        SELECT cc.memory_id,
               1 - (cc.embedding <=> ?::vector) as similarity,
               cc.text
        FROM content_chunks cc
        JOIN memories m ON m.id = cc.memory_id
        WHERE m.project    = ?
          AND m.status     = 'active'
          AND m.deleted_at IS NULL
          AND (m.valid_from  IS NULL OR m.valid_from  <= NOW())
          AND (m.valid_until IS NULL OR m.valid_until >= NOW())
          AND 1 - (cc.embedding <=> ?::vector) >= ?
        ORDER BY cc.embedding <=> ?::vector
        LIMIT ?
    `, vec, project, vec, threshold, vec, limit).Scan(&results).Error
	if err != nil {
		return nil, fmt.Errorf("find similar for project %q: %w", project, err)
	}
	return results, nil
}

// HasChunks checks if a memory already has embedding chunks.
func (s *Store) HasChunks(ctx context.Context, memoryID int64) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&Chunk{}).
		Where("memory_id = ?", memoryID).
		Count(&count).Error
	return count > 0, err
}

// DeleteByMemory removes all chunks for a memory.
func (s *Store) DeleteByMemory(ctx context.Context, memoryID int64) error {
	return s.db.WithContext(ctx).
		Where("memory_id = ?", memoryID).
		Delete(&Chunk{}).Error
}
