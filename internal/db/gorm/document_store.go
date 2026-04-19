// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrChunkStorageUnsupported is returned by chunk methods when content_chunks
// table has been dropped in v5. Callers should treat this as a graceful
// "feature removed" signal and degrade accordingly.
var ErrChunkStorageUnsupported = errors.New("chunk storage unsupported: content_chunks table dropped in v5")

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

	if _, err := s.rawDB.ExecContext(ctx, upsertQuery, collection, path, sqlNullString(title), hash); err != nil {
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

// UpsertChunks returns ErrChunkStorageUnsupported in v5.
// content_chunks was dropped in migration 085; callers should degrade gracefully.
func (s *DocumentStore) UpsertChunks(_ context.Context, _ string, _ []ContentChunk) error {
	return ErrChunkStorageUnsupported
}

// SearchChunks returns ErrChunkStorageUnsupported in v5.
// content_chunks was dropped in migration 085; callers should treat this as
// "feature removed", not as "zero results found".
func (s *DocumentStore) SearchChunks(_ context.Context, _ []float32, _ string, _ int) ([]ContentChunk, error) {
	return nil, ErrChunkStorageUnsupported
}

// ChunksExist returns ErrChunkStorageUnsupported in v5.
// content_chunks was dropped in migration 085.
func (s *DocumentStore) ChunksExist(_ context.Context, _ string) (bool, error) {
	return false, ErrChunkStorageUnsupported
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
