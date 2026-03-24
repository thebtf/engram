// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// VersionedDocument is the GORM model for the documents table created by migration 051.
// It stores versioned document content for AI agent collaboration workflows.
// Named VersionedDocument to avoid collision with the RAG Document model in models.go.
type VersionedDocument struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	Path        string `gorm:"not null"`
	Project     string `gorm:"not null"`
	Version     int    `gorm:"not null;default:1"`
	Content     string `gorm:"not null"`
	ContentHash string `gorm:"column:content_hash;not null"`
	DocType     string `gorm:"column:doc_type;not null;default:markdown"`
	Metadata    string `gorm:"type:jsonb;default:'{}'"`
	Author      string `gorm:"not null"`
	CreatedAt   time.Time `gorm:"column:created_at;not null;autoCreateTime"`
}

// TableName maps VersionedDocument to the versioned_documents table.
func (VersionedDocument) TableName() string { return "versioned_documents" }

// VersionedDocumentComment is the GORM model for the document_comments table.
type VersionedDocumentComment struct {
	ID         int64  `gorm:"primaryKey;autoIncrement"`
	DocumentID int64  `gorm:"column:document_id;not null"`
	Author     string `gorm:"not null"`
	Content    string `gorm:"not null"`
	LineStart  *int   `gorm:"column:line_start"`
	LineEnd    *int   `gorm:"column:line_end"`
	Status     string `gorm:"not null;default:open"`
	CreatedAt  time.Time `gorm:"column:created_at;not null;autoCreateTime"`
}

// TableName maps VersionedDocumentComment to the versioned_document_comments table.
func (VersionedDocumentComment) TableName() string { return "versioned_document_comments" }

// VersionedDocumentStore provides CRUD operations for versioned documents
// and their associated comments (migration 051 schema).
type VersionedDocumentStore struct {
	db *gorm.DB
}

// NewVersionedDocumentStore creates a new VersionedDocumentStore backed by the given Store.
func NewVersionedDocumentStore(store *Store) *VersionedDocumentStore {
	return &VersionedDocumentStore{db: store.DB}
}

// versionedDocHashContent returns the SHA-256 hex digest of the given string.
func versionedDocHashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Create inserts a new versioned document for the given path+project.
// It computes the SHA-256 content hash, determines the next version number
// (max existing version + 1), and returns the ID of the newly created row.
func (s *VersionedDocumentStore) Create(
	ctx context.Context,
	path, project, content, docType, metadata, author string,
) (int64, error) {
	contentHash := versionedDocHashContent(content)

	if docType == "" {
		docType = "markdown"
	}
	if metadata == "" {
		metadata = "{}"
	}

	var docID int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock all existing rows for this path+project to prevent concurrent version races.
		var existing []VersionedDocument
		if err := tx.
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("path = ? AND project = ?", path, project).
			Find(&existing).Error; err != nil {
			return fmt.Errorf("lock rows: %w", err)
		}

		// Determine next version atomically within the transaction.
		var maxVersion int
		if err := tx.
			Model(&VersionedDocument{}).
			Where("path = ? AND project = ?", path, project).
			Select("COALESCE(MAX(version), 0)").
			Scan(&maxVersion).Error; err != nil {
			return fmt.Errorf("query max version: %w", err)
		}

		doc := VersionedDocument{
			Path:        path,
			Project:     project,
			Version:     maxVersion + 1,
			Content:     content,
			ContentHash: contentHash,
			DocType:     docType,
			Metadata:    metadata,
			Author:      author,
		}

		if err := tx.Create(&doc).Error; err != nil {
			return fmt.Errorf("insert document: %w", err)
		}
		docID = doc.ID
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("versioned_document_store: create: %w", err)
	}
	return docID, nil
}

// ReadLatest returns the highest-versioned document for the given path+project.
// Returns gorm.ErrRecordNotFound if no document exists.
func (s *VersionedDocumentStore) ReadLatest(ctx context.Context, path, project string) (*VersionedDocument, error) {
	var doc VersionedDocument
	err := s.db.WithContext(ctx).
		Where("path = ? AND project = ?", path, project).
		Order("version DESC").
		Limit(1).
		First(&doc).Error
	if err != nil {
		return nil, fmt.Errorf("versioned_document_store: read latest: %w", err)
	}
	return &doc, nil
}

// ReadVersion returns the document at the exact version specified.
// Returns gorm.ErrRecordNotFound if no matching row exists.
func (s *VersionedDocumentStore) ReadVersion(ctx context.Context, path, project string, version int) (*VersionedDocument, error) {
	var doc VersionedDocument
	err := s.db.WithContext(ctx).
		Where("path = ? AND project = ? AND version = ?", path, project, version).
		First(&doc).Error
	if err != nil {
		return nil, fmt.Errorf("versioned_document_store: read version %d: %w", version, err)
	}
	return &doc, nil
}

// List returns the latest version of each distinct document path in a project.
// Optional filters: docType (exact match), pathPrefix (LIKE prefix match).
// limit <= 0 means no row limit.
// Uses DISTINCT ON (path) ORDER BY path, version DESC to return only the latest per path.
func (s *VersionedDocumentStore) List(ctx context.Context, project, docType, pathPrefix string, limit int) ([]VersionedDocument, error) {
	extraClauses, args := versionedDocBuildListFilters(project, docType, pathPrefix)
	rawSQL := `SELECT DISTINCT ON (path)
	              id, path, project, version, content, content_hash, doc_type, metadata, author, created_at
	           FROM versioned_documents
	           WHERE project = ?` + extraClauses + `
	           ORDER BY path, version DESC`

	if limit > 0 {
		rawSQL += fmt.Sprintf(" LIMIT %d", limit)
	}

	var docs []VersionedDocument
	if err := s.db.WithContext(ctx).Raw(rawSQL, args...).Scan(&docs).Error; err != nil {
		return nil, fmt.Errorf("versioned_document_store: list: %w", err)
	}
	return docs, nil
}

// versionedDocBuildListFilters returns the extra WHERE clause string and positional args
// for the List raw query. The project arg is always first and already in the query template.
func versionedDocBuildListFilters(project, docType, pathPrefix string) (string, []interface{}) {
	args := []interface{}{project}
	var clauses []string

	if docType != "" {
		clauses = append(clauses, "doc_type = ?")
		args = append(args, docType)
	}
	if pathPrefix != "" {
		clauses = append(clauses, "path LIKE ?")
		args = append(args, pathPrefix+"%")
	}

	if len(clauses) == 0 {
		return "", args
	}
	return " AND " + strings.Join(clauses, " AND "), args
}

// GetHistory returns all versions of a document for the given path+project,
// ordered by version descending (newest first).
// limit <= 0 means no row limit.
func (s *VersionedDocumentStore) GetHistory(ctx context.Context, path, project string, limit int) ([]VersionedDocument, error) {
	query := s.db.WithContext(ctx).
		Where("path = ? AND project = ?", path, project).
		Order("version DESC")

	if limit > 0 {
		query = query.Limit(limit)
	}

	var docs []VersionedDocument
	if err := query.Find(&docs).Error; err != nil {
		return nil, fmt.Errorf("versioned_document_store: get history: %w", err)
	}
	return docs, nil
}

// AddComment inserts a comment associated with the given document ID.
// lineStart and lineEnd are optional (nil = not line-anchored).
// Returns the ID of the newly created comment.
func (s *VersionedDocumentStore) AddComment(
	ctx context.Context,
	documentID int64,
	author, content string,
	lineStart, lineEnd *int,
) (int64, error) {
	comment := VersionedDocumentComment{
		DocumentID: documentID,
		Author:     author,
		Content:    content,
		LineStart:  lineStart,
		LineEnd:    lineEnd,
		Status:     "open",
	}
	if err := s.db.WithContext(ctx).Create(&comment).Error; err != nil {
		return 0, fmt.Errorf("versioned_document_store: add comment: %w", err)
	}
	return comment.ID, nil
}

// GetComments returns all comments for the given document ID, ordered by creation time ascending.
func (s *VersionedDocumentStore) GetComments(ctx context.Context, documentID int64) ([]VersionedDocumentComment, error) {
	var comments []VersionedDocumentComment
	if err := s.db.WithContext(ctx).
		Where("document_id = ?", documentID).
		Order("created_at ASC").
		Find(&comments).Error; err != nil {
		return nil, fmt.Errorf("versioned_document_store: get comments: %w", err)
	}
	return comments, nil
}
