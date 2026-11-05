package gorm

import (
	"context"
	"math"
	"strconv"

	"github.com/thebtf/engram/internal/projectidentity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// withScopedDocument binds one exact version to the current authorized read
// scope while holding redirect and membership locks through the action.
func (s *VersionedDocumentStore) withScopedDocument(ctx context.Context, project string, id int64, version int, action func(*gorm.DB, *VersionedDocument) error) error {
	if !projectidentity.ValidProjectID(project) || id <= 0 || version <= 0 {
		return gorm.ErrRecordNotFound
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`LOCK TABLE project_consolidation_memberships, project_consolidation_redirects, projects IN SHARE MODE`).Error; err != nil {
			return err
		}
		query, err := canonicalProjectRows(ctx, tx.Model(&VersionedDocument{}), project, "versioned_documents", "d")
		if err != nil {
			return err
		}
		var doc VersionedDocument
		if err := query.Clauses(clause.Locking{Strength: "SHARE"}).Where("d.id = ? AND d.version = ?", id, version).Take(&doc).Error; err != nil {
			return err
		}
		doc.Project = project
		return action(tx, &doc)
	})
}

func (s *VersionedDocumentStore) ReadScopedDocument(ctx context.Context, project string, id int64, version int) (*VersionedDocument, error) {
	var result *VersionedDocument
	err := s.withScopedDocument(ctx, project, id, version, func(_ *gorm.DB, doc *VersionedDocument) error { result = doc; return nil })
	return result, err
}

func (s *VersionedDocumentStore) AddScopedComment(ctx context.Context, project string, id int64, version int, author, content string, start, end *int) (int64, error) {
	var commentID int64
	err := s.withScopedDocument(ctx, project, id, version, func(tx *gorm.DB, _ *VersionedDocument) error {
		comment := VersionedDocumentComment{DocumentID: id, Author: author, Content: content, LineStart: start, LineEnd: end, Status: "open"}
		if err := tx.Create(&comment).Error; err != nil {
			return err
		}
		commentID = comment.ID
		return nil
	})
	return commentID, err
}

func (s *VersionedDocumentStore) GetScopedComments(ctx context.Context, project string, id int64, version int) ([]VersionedDocumentComment, error) {
	var comments []VersionedDocumentComment
	err := s.withScopedDocument(ctx, project, id, version, func(tx *gorm.DB, _ *VersionedDocument) error {
		return tx.Where("document_id = ?", id).Order("created_at ASC, id ASC").Find(&comments).Error
	})
	return comments, err
}

// WithScopedDocuments holds scope locks through the caller's bounded export action.
func (s *VersionedDocumentStore) WithScopedDocuments(ctx context.Context, project string, targets []CollectionSelectionTarget, action func([]VersionedDocument) error) error {
	if !projectidentity.ValidProjectID(project) || len(targets) == 0 || len(targets) > CollectionSelectionMaxTargets {
		return gorm.ErrRecordNotFound
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`LOCK TABLE project_consolidation_memberships, project_consolidation_redirects, projects IN SHARE MODE`).Error; err != nil {
			return err
		}
		docs := make([]VersionedDocument, 0, len(targets))
		for _, target := range targets {
			id, err := strconv.ParseInt(target.ID, 10, 64)
			if err != nil || id <= 0 || target.ExpectedVersion == 0 || target.ExpectedVersion > uint64(math.MaxInt) {
				return gorm.ErrRecordNotFound
			}
			query, err := canonicalProjectRows(ctx, tx.Model(&VersionedDocument{}), project, "versioned_documents", "d")
			if err != nil {
				return err
			}
			var doc VersionedDocument
			if err := query.Clauses(clause.Locking{Strength: "SHARE"}).Where("d.id = ? AND d.version = ?", id, int(target.ExpectedVersion)).Take(&doc).Error; err != nil {
				return err
			}
			doc.Project = project
			docs = append(docs, doc)
		}
		return action(docs)
	})
}
