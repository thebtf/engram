// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/internal/projectidentity"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// VersionedDocument is the GORM model for the documents table created by migration 051.
// It stores versioned document content for AI agent collaboration workflows.
// Named VersionedDocument to avoid collision with the RAG Document model in models.go.
type VersionedDocument struct {
	ID          int64     `gorm:"primaryKey;autoIncrement"`
	Path        string    `gorm:"not null"`
	Project     string    `gorm:"not null"`
	Version     int       `gorm:"not null;default:1"`
	Content     string    `gorm:"not null"`
	ContentHash string    `gorm:"column:content_hash;not null"`
	DocType     string    `gorm:"column:doc_type;not null;default:markdown"`
	Metadata    string    `gorm:"type:jsonb;default:'{}'"`
	Author      string    `gorm:"not null"`
	CreatedAt   time.Time `gorm:"column:created_at;not null;autoCreateTime"`
}

// TableName maps VersionedDocument to the versioned_documents table.
func (VersionedDocument) TableName() string { return "versioned_documents" }

// VersionedDocumentComment is the GORM model for the document_comments table.
type VersionedDocumentComment struct {
	ID         int64     `gorm:"primaryKey;autoIncrement"`
	DocumentID int64     `gorm:"column:document_id;not null"`
	Author     string    `gorm:"not null"`
	Content    string    `gorm:"not null"`
	LineStart  *int      `gorm:"column:line_start"`
	LineEnd    *int      `gorm:"column:line_end"`
	Status     string    `gorm:"not null;default:open"`
	CreatedAt  time.Time `gorm:"column:created_at;not null;autoCreateTime"`
}

// TableName maps VersionedDocumentComment to the versioned_document_comments table.
func (VersionedDocumentComment) TableName() string { return "versioned_document_comments" }

// VersionedDocumentStore provides CRUD operations for versioned documents
// and their associated comments (migration 051 schema).
type VersionedDocumentStore struct {
	db    *gorm.DB
	vault *vaultcrypto.Vault
}

// NewVersionedDocumentStore creates a new VersionedDocumentStore backed by the given Store.
func NewVersionedDocumentStore(store *Store) *VersionedDocumentStore {
	return &VersionedDocumentStore{db: store.DB}
}

// NewVersionedDocumentStoreWithVault enables authenticated retained-version writes.
func NewVersionedDocumentStoreWithVault(store *Store, vault *vaultcrypto.Vault) *VersionedDocumentStore {
	return &VersionedDocumentStore{db: store.DB, vault: vault}
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
		if projectidentity.ValidProjectID(project) || (project != "" && tx.Dialector.Name() == "postgres") {
			if err := tx.Exec(`LOCK TABLE project_consolidation_memberships, project_consolidation_redirects IN SHARE MODE`).Error; err != nil {
				return err
			}
		}
		if project != "" && !projectidentity.ValidProjectID(project) && tx.Dialector.Name() == "postgres" {
			var effective string
			if err := tx.Raw(`SELECT engram_consolidation_scope(?)`, project).Scan(&effective).Error; err != nil {
				return err
			}
			project = effective
		}
		if err := ensureLogicalProjectForWrite(ctx, tx, project); err != nil {
			return err
		}

		// Lock all existing rows for this path+project to prevent concurrent version races.
		var existing []VersionedDocument
		if err := tx.
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("path = ? AND project = ?", path, project).
			Find(&existing).Error; err != nil {
			return fmt.Errorf("lock rows: %w", err)
		}
		if projectidentity.ValidProjectID(project) {
			scoped, err := canonicalProjectRows(ctx, tx.Model(&VersionedDocument{}), project, "versioned_documents", "d")
			if err != nil {
				return err
			}
			var visibleNative int64
			if err := scoped.Where("d.path = ? AND d.project = ?", path, project).Count(&visibleNative).Error; err != nil {
				return err
			}
			if visibleNative != int64(len(existing)) {
				return ErrKnowledgeProjectForbidden
			}
			if len(existing) != 0 {
				var retained int64
				if err := tx.Table("project_consolidation_memberships AS m").Joins("JOIN versioned_documents AS d ON m.row_key->>'id' = CAST(d.id AS text)").
					Where("m.table_name = ? AND m.field_path = ?::jsonb AND d.path = ? AND d.project = ?", "versioned_documents", `["project"]`, path, project).Count(&retained).Error; err != nil {
					return err
				}
				if retained != 0 {
					return ErrDocumentLineageAmbiguous
				}
			}
			if len(existing) == 0 {
				scoped, err = canonicalProjectRows(ctx, tx.Model(&VersionedDocument{}), project, "versioned_documents", "d")
				if err != nil {
					return err
				}
				var otherLineages int64
				if err := scoped.Where("d.path = ?", path).Count(&otherLineages).Error; err != nil {
					return err
				}
				if otherLineages != 0 {
					return ErrDocumentLineageAmbiguous
				}
			}
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

var (
	ErrDocumentVersionStale          = errors.New("document version is stale")
	ErrDocumentProvenanceUnavailable = errors.New("DOCUMENT_PROVENANCE_UNAVAILABLE")
)

// CreateVersionForLineage reauthorizes an issued row and binds the new version
// to its native UUID lineage under the same transaction as insertion.
func (s *VersionedDocumentStore) CreateVersionForLineage(ctx context.Context, path, project, content, docType, metadata, author string, anchorID int64, expectedVersion int) (int64, error) {
	if anchorID <= 0 || expectedVersion <= 0 || !projectidentity.ValidProjectID(project) {
		return 0, ErrDocumentVersionStale
	}
	if docType == "" {
		docType = "markdown"
	}
	if metadata == "" {
		metadata = "{}"
	}
	var id int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`LOCK TABLE project_consolidation_memberships IN SHARE ROW EXCLUSIVE MODE`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`LOCK TABLE project_consolidation_redirects, projects IN SHARE MODE`).Error; err != nil {
			return err
		}
		q, err := canonicalProjectRows(ctx, tx.Model(&VersionedDocument{}), project, "versioned_documents", "d")
		if err != nil {
			return err
		}
		var anchor VersionedDocument
		if err := q.Where("d.id = ? AND d.path = ?", anchorID, path).Take(&anchor).Error; err != nil {
			return err
		}
		if !projectidentity.ValidProjectID(anchor.Project) {
			return ErrDocumentVersionStale
		}
		var active int64
		if err := liveKnowledgeProjects(tx).Where("id = ?", anchor.Project).Count(&active).Error; err != nil {
			return err
		}
		if active != 1 {
			return ErrDocumentVersionStale
		}
		var native []VersionedDocument
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("path = ? AND project = ?", path, anchor.Project).Find(&native).Error; err != nil {
			return err
		}
		visible, err := canonicalProjectRows(ctx, tx.Model(&VersionedDocument{}), project, "versioned_documents", "d")
		if err != nil {
			return err
		}
		var visibleCount int64
		if err := visible.Where("d.path = ? AND d.project = ?", path, anchor.Project).Count(&visibleCount).Error; err != nil {
			return err
		}
		if visibleCount != int64(len(native)) {
			return ErrKnowledgeProjectForbidden
		}
		maxVersion := 0
		for _, row := range native {
			if row.Version > maxVersion {
				maxVersion = row.Version
			}
		}
		if anchor.Version != expectedVersion || maxVersion != expectedVersion {
			return ErrDocumentVersionStale
		}
		var parents []ConsolidationMembership
		if err := tx.Where("table_name = ? AND row_key->>'id' = ? AND field_path = ?::jsonb", "versioned_documents", fmt.Sprint(anchor.ID), `["project"]`).Find(&parents).Error; err != nil {
			return err
		}
		var membershipOwner *ProjectConsolidationStore
		if len(parents) != 0 {
			if len(parents) != 1 || parents[0].ProjectID == nil {
				return ErrKnowledgeProjectForbidden
			}
			if s.vault == nil {
				return ErrDocumentProvenanceUnavailable
			}
			membershipOwner = NewProjectConsolidationStore(tx, s.vault, nil)
			if err := membershipOwner.available(); err != nil {
				return err
			}
			if err := membershipOwner.validateMembershipEvidence(tx, parents[0]); err != nil {
				return err
			}
		}
		doc := VersionedDocument{Path: path, Project: anchor.Project, Version: maxVersion + 1, Content: content, ContentHash: versionedDocHashContent(content), DocType: docType, Metadata: metadata, Author: author}
		if err := tx.Create(&doc).Error; err != nil {
			return err
		}
		if membershipOwner != nil {
			if err := membershipOwner.writeLiveDocumentMembership(tx, parents[0], doc); err != nil {
				return err
			}
		}
		id = doc.ID
		return nil
	})
	return id, err
}

var ErrDocumentLineageAmbiguous = errors.New("document path has multiple native lineages")

// documentLineageQuery chooses only an authorized lineage; an exact row ID can
// disambiguate independent native histories without trusting a caller-supplied origin.
func (s *VersionedDocumentStore) documentLineageQuery(ctx context.Context, path, project string, anchorID int64) (*gorm.DB, error) {
	q, err := canonicalProjectRows(ctx, s.db.Model(&VersionedDocument{}), project, "versioned_documents", "d")
	if err != nil {
		return nil, err
	}
	type lineage struct{ NativeProject string }
	var rows []lineage
	selectLineages := func(nativeOnly bool) error {
		base, err := canonicalProjectRows(ctx, s.db.Model(&VersionedDocument{}), project, "versioned_documents", "d")
		if err != nil {
			return err
		}
		selected := base.Select("DISTINCT d.project AS native_project").Where("d.path = ?", path)
		if anchorID > 0 {
			selected = selected.Where("d.id = ?", anchorID)
		}
		if nativeOnly {
			selected = selected.Where("d.project = ?", project)
		}
		return selected.Limit(2).Scan(&rows).Error
	}
	if anchorID == 0 {
		if err := selectLineages(true); err != nil {
			return nil, err
		}
	}
	if anchorID > 0 || len(rows) == 0 {
		if err := selectLineages(false); err != nil {
			return nil, err
		}
	}
	if len(rows) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if len(rows) != 1 {
		return nil, ErrDocumentLineageAmbiguous
	}
	q, err = canonicalProjectRows(ctx, s.db.Model(&VersionedDocument{}), project, "versioned_documents", "d")
	if err != nil {
		return nil, err
	}
	return q.Where("d.path = ? AND d.project = ?", path, rows[0].NativeProject), nil
}

// ReadLatest returns the highest-versioned document for the given path+project.
// Returns gorm.ErrRecordNotFound if no document exists.
func (s *VersionedDocumentStore) ReadLatest(ctx context.Context, path, project string) (*VersionedDocument, error) {
	return s.ReadLatestForLineage(ctx, path, project, 0)
}

func (s *VersionedDocumentStore) ReadLatestForLineage(ctx context.Context, path, project string, anchorID int64) (*VersionedDocument, error) {
	q, err := s.documentLineageQuery(ctx, path, project, anchorID)
	if err != nil {
		return nil, err
	}
	var doc VersionedDocument
	err = q.Where("path = ?", path).
		Order("version DESC, id DESC").
		Limit(1).
		First(&doc).Error
	if err != nil {
		return nil, fmt.Errorf("versioned_document_store: read latest: %w", err)
	}
	doc.Project = project
	return &doc, nil
}

// ReadVersion returns the document at the exact version specified.
// Returns gorm.ErrRecordNotFound if no matching row exists.
func (s *VersionedDocumentStore) ReadVersion(ctx context.Context, path, project string, version int) (*VersionedDocument, error) {
	return s.ReadVersionForLineage(ctx, path, project, version, 0)
}

func (s *VersionedDocumentStore) ReadVersionForLineage(ctx context.Context, path, project string, version int, anchorID int64) (*VersionedDocument, error) {
	q, err := s.documentLineageQuery(ctx, path, project, anchorID)
	if err != nil {
		return nil, err
	}
	var doc VersionedDocument
	err = q.Where("path = ? AND version = ?", path, version).
		Order("id DESC").
		First(&doc).Error
	if err != nil {
		return nil, fmt.Errorf("versioned_document_store: read version %d: %w", version, err)
	}
	doc.Project = project
	return &doc, nil
}

// ReadByID returns one immutable document version by its database identity.
// Returns gorm.ErrRecordNotFound if the selected version no longer exists.
func (s *VersionedDocumentStore) ReadByID(ctx context.Context, id int64) (*VersionedDocument, error) {
	var doc VersionedDocument
	if err := s.db.WithContext(ctx).First(&doc, id).Error; err != nil {
		return nil, fmt.Errorf("versioned_document_store: read document %d: %w", id, err)
	}
	return &doc, nil
}

// List returns the latest version of each distinct document path in a project.
// Optional metadata filters apply after latest-version selection.
// limit <= 0 means no row limit.
func (s *VersionedDocumentStore) List(ctx context.Context, project, docType, pathPrefix string, limit int) ([]VersionedDocument, error) {
	q, err := canonicalProjectRows(ctx, s.db.Model(&VersionedDocument{}), project, "versioned_documents", "d")
	if err != nil {
		return nil, err
	}
	latest := q.Select("d.*, ROW_NUMBER() OVER (PARTITION BY d.path, d.project ORDER BY d.version DESC, d.id DESC) AS version_rank")
	q = s.db.WithContext(ctx).Table("(?) AS latest", latest).Where("version_rank = 1")
	if docType != "" {
		q = q.Where("doc_type = ?", docType)
	}
	if pathPrefix != "" {
		q = q.Where("path LIKE ? ESCAPE '\\'", escapeSQLLike(pathPrefix)+"%")
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var docs []VersionedDocument
	if err := q.Order("created_at DESC, id DESC").Find(&docs).Error; err != nil {
		return nil, err
	}
	for i := range docs {
		docs[i].Project = project
	}
	return docs, nil
}

// SearchTaskContext searches authorized current document bodies without a
// separate projection. Visibility and latest-lineage selection precede ranking
// and the result limit. The request context bounds the SQL work.
func (s *VersionedDocumentStore) SearchTaskContext(ctx context.Context, project, query string, limit int) ([]VersionedDocument, error) {
	if strings.TrimSpace(query) == "" || len(query) > 16<<10 || limit < 1 || limit > 3 {
		return nil, fmt.Errorf("invalid document task search")
	}
	scoped, err := canonicalProjectRows(ctx, s.db.Model(&VersionedDocument{}), project, "versioned_documents", "d")
	if err != nil {
		return nil, err
	}
	latest := scoped.Select("d.*, ROW_NUMBER() OVER (PARTITION BY d.path, d.project ORDER BY d.version DESC, d.id DESC) AS version_rank")
	var docs []VersionedDocument
	err = s.db.WithContext(ctx).Table("(?) AS latest", latest).
		Where("version_rank = 1").
		Where("to_tsvector('english', content) @@ COALESCE(NULLIF(websearch_to_tsquery('english', ?), ''::tsquery), plainto_tsquery('english', ?))", query, query).
		Clauses(clause.OrderBy{Expression: clause.Expr{SQL: "ts_rank_cd(to_tsvector('english', content), COALESCE(NULLIF(websearch_to_tsquery('english', ?), ''::tsquery), plainto_tsquery('english', ?))) DESC, created_at DESC, id DESC", Vars: []any{query, query}}}).Limit(limit).Find(&docs).Error
	if err != nil {
		return nil, err
	}
	for i := range docs {
		docs[i].Project = project
	}
	return docs, nil
}

// ListPage searches public metadata on the latest version of each path. Count
// and page share the same project and filter predicates; content is not loaded.
func (s *VersionedDocumentStore) ListPage(ctx context.Context, project string, docTypes []string, pathPrefix, query string, limit, offset int) ([]VersionedDocument, int64, error) {
	if project == "" || limit < 1 || limit > 500 || offset < 0 || len(docTypes) > 6 {
		return nil, 0, fmt.Errorf("invalid document page")
	}
	q, err := canonicalProjectRows(ctx, s.db.Model(&VersionedDocument{}), project, "versioned_documents", "d")
	if err != nil {
		return nil, 0, err
	}
	latest := q.Select("d.id, d.path, d.project, d.version, d.doc_type, d.author, d.created_at, ROW_NUMBER() OVER (PARTITION BY d.path, d.project ORDER BY d.version DESC, d.id DESC) AS version_rank")
	q = s.db.WithContext(ctx).Table("(?) AS latest", latest).Where("version_rank = 1")
	if len(docTypes) != 0 {
		q = q.Where("doc_type IN ?", docTypes)
	}
	if pathPrefix != "" {
		q = q.Where("path LIKE ? ESCAPE '\\'", escapeSQLLike(pathPrefix)+"%")
	}
	if query != "" {
		literal := "%" + escapeSQLLike(strings.ToLower(query)) + "%"
		q = q.Where("LOWER(path) LIKE ? ESCAPE '\\' OR LOWER(doc_type) LIKE ? ESCAPE '\\' OR LOWER(author) LIKE ? ESCAPE '\\'", literal, literal, literal)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	docs := make([]VersionedDocument, 0)
	if err := q.Select("id, path, project, version, doc_type, author, created_at").
		Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&docs).Error; err != nil {
		return nil, 0, err
	}
	for i := range docs {
		docs[i].Project = project
	}
	return docs, total, nil
}

// GetHistory returns all versions of a document for the given path+project,
// ordered by version descending (newest first).
// limit <= 0 means no row limit.
func (s *VersionedDocumentStore) GetHistory(ctx context.Context, path, project string, limit int) ([]VersionedDocument, error) {
	return s.GetHistoryForLineage(ctx, path, project, limit, 0)
}

func (s *VersionedDocumentStore) GetHistoryForLineage(ctx context.Context, path, project string, limit int, anchorID int64) ([]VersionedDocument, error) {
	query, err := s.documentLineageQuery(ctx, path, project, anchorID)
	if err != nil {
		if anchorID == 0 && errors.Is(err, gorm.ErrRecordNotFound) {
			return []VersionedDocument{}, nil
		}
		return nil, err
	}
	query = query.Where("path = ?", path).Order("version DESC, id DESC")

	if limit > 0 {
		query = query.Limit(limit)
	}

	var docs []VersionedDocument
	if err := query.Find(&docs).Error; err != nil {
		return nil, fmt.Errorf("versioned_document_store: get history: %w", err)
	}
	for i := range docs {
		docs[i].Project = project
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
