// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"

	pgvec "github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DocumentStore provides document and chunk persistence for content-addressable storage.
type DocumentStore struct {
	db    *gorm.DB
	rawDB *sql.DB
}

// NewDocumentStore creates a new document store.
func NewDocumentStore(store *Store) *DocumentStore {
	return &DocumentStore{
		db:    store.DB,
		rawDB: store.GetRawDB(),
	}
}

// UpsertDocument stores the document body in content and upserts the document metadata.
func (s *DocumentStore) UpsertDocument(ctx context.Context, collection, path, title, contentBody string) (*Document, error) {
	hashBytes := sha256.Sum256([]byte(contentBody))
	hash := hex.EncodeToString(hashBytes[:])

	if err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&Content{Hash: hash, Doc: contentBody}).
		Error; err != nil {
		return nil, fmt.Errorf("upsert content: %w", err)
	}

	upsertQuery := `
		INSERT INTO documents (collection, path, title, hash, active)
		VALUES ($1, $2, $3, $4, true)
		ON CONFLICT (collection, path)
		DO UPDATE SET
			hash = EXCLUDED.hash,
			title = EXCLUDED.title,
			active = true,
			updated_at = NOW()
	`

	if _, err := s.rawDB.ExecContext(ctx, upsertQuery, collection, path, nullString(title), hash); err != nil {
		return nil, fmt.Errorf("upsert document: %w", err)
	}

	var doc Document
	if err := s.db.WithContext(ctx).Where("collection = ? AND path = ?", collection, path).First(&doc).Error; err != nil {
		return nil, fmt.Errorf("fetch upserted document: %w", err)
	}

	return &doc, nil
}

// GetDocument returns the active document for the collection and path.
func (s *DocumentStore) GetDocument(ctx context.Context, collection, path string) (*Document, error) {
	var doc Document
	if err := s.db.WithContext(ctx).
		Where("collection = ? AND path = ? AND active = true", collection, path).
		First(&doc).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("get document: %w", err)
	}

	return &doc, nil
}

// GetContent fetches content by hash.
func (s *DocumentStore) GetContent(ctx context.Context, hash string) (*Content, error) {
	var content Content
	if err := s.db.WithContext(ctx).First(&content, "hash = ?", hash).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("get content: %w", err)
	}

	return &content, nil
}

// ListDocuments lists documents in a collection, optionally filtered to active-only.
func (s *DocumentStore) ListDocuments(ctx context.Context, collection string, activeOnly bool) ([]Document, error) {
	query := s.db.WithContext(ctx).Where("collection = ?", collection)
	if activeOnly {
		query = query.Where("active = true")
	}

	var docs []Document
	if err := query.Order("path ASC").Find(&docs).Error; err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}

	return docs, nil
}

// UpsertChunks replaces existing chunks for a content hash with new chunk rows.
// Uses raw SQL to include the pgvector embedding column which is excluded from GORM mapping.
func (s *DocumentStore) UpsertChunks(ctx context.Context, hash string, chunks []ContentChunk) error {
	tx, err := s.rawDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, "DELETE FROM content_chunks WHERE hash = $1", hash); err != nil {
		return fmt.Errorf("delete existing chunks: %w", err)
	}

	if len(chunks) == 0 {
		return tx.Commit()
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO content_chunks (hash, seq, text, pos, model, embedding)
		 VALUES ($1, $2, $3, $4, $5, $6)`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, c := range chunks {
		if len(c.Embedding.Slice()) == 0 {
			return fmt.Errorf("empty embedding for chunk seq %d (hash %s): chunks without embeddings cannot be searched", c.Seq, c.Hash)
		}
		if _, err := stmt.ExecContext(ctx, c.Hash, c.Seq, c.Text, c.Pos, c.Model, c.Embedding); err != nil {
			return fmt.Errorf("insert chunk %d: %w", c.Seq, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chunks: %w", err)
	}

	return nil
}

// SearchChunks performs vector similarity search across content chunks.
// If collection is empty, searches all active documents.
func (s *DocumentStore) SearchChunks(ctx context.Context, embedding []float32, collection string, limit int) ([]ContentChunk, error) {
	var query string
	var args []any

	if collection != "" {
		query = `
			SELECT cc.hash, cc.seq, cc.text, cc.pos, cc.model, cc.created_at
			FROM content_chunks cc
			JOIN documents d ON d.hash = cc.hash
			WHERE d.collection = $2
			  AND d.active = true
			ORDER BY cc.embedding <=> $1
			LIMIT $3
		`
		args = []any{pgvec.NewVector(embedding), collection, limit}
	} else {
		query = `
			SELECT cc.hash, cc.seq, cc.text, cc.pos, cc.model, cc.created_at
			FROM content_chunks cc
			JOIN documents d ON d.hash = cc.hash
			WHERE d.active = true
			ORDER BY cc.embedding <=> $1
			LIMIT $2
		`
		args = []any{pgvec.NewVector(embedding), limit}
	}

	rows, err := s.rawDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search chunks: %w", err)
	}
	defer rows.Close()

	chunks := make([]ContentChunk, 0)
	for rows.Next() {
		var chunk ContentChunk
		if err := rows.Scan(&chunk.Hash, &chunk.Seq, &chunk.Text, &chunk.Pos, &chunk.Model, &chunk.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan chunk row: %w", err)
		}

		chunks = append(chunks, chunk)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunk rows: %w", err)
	}

	return chunks, nil
}

// ChunksExist checks if any chunks exist for a given content hash.
func (s *DocumentStore) ChunksExist(ctx context.Context, hash string) (bool, error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&ContentChunk{}).Where("hash = ?", hash).Limit(1).Count(&count).Error; err != nil {
		return false, fmt.Errorf("check chunks existence: %w", err)
	}
	return count > 0, nil
}

// DeactivateDocument marks a document as inactive.
func (s *DocumentStore) DeactivateDocument(ctx context.Context, collection, path string) error {
	if err := s.db.WithContext(ctx).
		Model(&Document{}).
		Where("collection = ? AND path = ?", collection, path).
		Update("active", false).
		Error; err != nil {
		return fmt.Errorf("deactivate document: %w", err)
	}

	return nil
}

// CollectionDocCounts returns active document counts by collection.
func (s *DocumentStore) CollectionDocCounts(ctx context.Context) (map[string]int64, error) {
	query := `
		SELECT collection, COUNT(*)
		FROM documents
		WHERE active = true
		GROUP BY collection
	`

	rows, err := s.rawDB.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query collection document counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int64)
	for rows.Next() {
		var collection string
		var count int64
		if err := rows.Scan(&collection, &count); err != nil {
			return nil, fmt.Errorf("scan collection doc count row: %w", err)
		}
		counts[collection] = count
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate collection doc counts: %w", err)
	}

	return counts, nil
}
