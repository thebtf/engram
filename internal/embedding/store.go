package embedding

import (
	"context"
	"fmt"
	"time"

	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

// Chunk represents a row in the content_chunks table.
type Chunk struct {
	ID        int64           `gorm:"primaryKey;autoIncrement"`
	MemoryID  int64           `gorm:"not null"`
	Seq       int             `gorm:"not null"`
	Text      string          `gorm:"type:text;not null;default:''"`
	Embedding pgvector.Vector `gorm:"type:vector(4096)"`
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
