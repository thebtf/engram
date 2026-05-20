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
func (s *Store) FindSimilar(ctx context.Context, queryVec []float32, limit int, threshold float64) ([]SimilarResult, error) {
	if limit <= 0 {
		limit = 10
	}
	if threshold <= 0 {
		threshold = 0.7
	}

	vec := pgvector.NewVector(queryVec)
	var results []SimilarResult
	err := s.db.WithContext(ctx).Raw(`
        SELECT memory_id,
               1 - (embedding <=> ?::vector) as similarity,
               text
        FROM content_chunks
        WHERE 1 - (embedding <=> ?::vector) >= ?
        ORDER BY embedding <=> ?::vector
        LIMIT ?
    `, vec, vec, threshold, vec, limit).Scan(&results).Error
	if err != nil {
		return nil, fmt.Errorf("find similar: %w", err)
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
