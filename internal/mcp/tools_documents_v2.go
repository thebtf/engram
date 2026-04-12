package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"

	gormpkg "github.com/thebtf/engram/internal/db/gorm"
)

// handleDocCreate creates a new versioned document (or a new version of an existing one).
func (s *Server) handleDocCreate(ctx context.Context, args json.RawMessage) (string, error) {
	if s.versionedDocumentStore == nil {
		return "", fmt.Errorf("versioned document store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	path := coerceString(m["path"], "")
	project := coerceString(m["project"], "")
	content := coerceString(m["content"], "")
	docType := coerceString(m["doc_type"], "markdown")
	metadata := coerceString(m["metadata"], "{}")
	author := coerceString(m["author"], "agent")

	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if content == "" {
		return "", fmt.Errorf("content is required")
	}
	if project == "" {
		return "", fmt.Errorf("project is required")
	}

	id, err := s.versionedDocumentStore.Create(ctx, path, project, content, docType, metadata, author)
	if err != nil {
		return "", fmt.Errorf("doc_create: %w", err)
	}

	result := map[string]any{
		"id":      id,
		"path":    path,
		"project": project,
		"message": "Document created successfully",
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// handleDocRead reads the latest or a specific version of a versioned document.
func (s *Server) handleDocRead(ctx context.Context, args json.RawMessage) (string, error) {
	if s.versionedDocumentStore == nil {
		return "", fmt.Errorf("versioned document store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	path := coerceString(m["path"], "")
	project := coerceString(m["project"], "")
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if project == "" {
		return "", fmt.Errorf("project is required")
	}

	var doc *gormpkg.VersionedDocument
	if v, ok := m["version"]; ok && v != nil {
		version := int(coerceInt(m["version"], 0))
		if version <= 0 {
			return "", fmt.Errorf("version must be a positive integer")
		}
		doc, err = s.versionedDocumentStore.ReadVersion(ctx, path, project, version)
	} else {
		doc, err = s.versionedDocumentStore.ReadLatest(ctx, path, project)
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", fmt.Errorf("document not found: path=%q project=%q", path, project)
		}
		return "", fmt.Errorf("doc_read: %w", err)
	}

	result := map[string]any{
		"id":           doc.ID,
		"path":         doc.Path,
		"project":      doc.Project,
		"version":      doc.Version,
		"content":      doc.Content,
		"content_hash": doc.ContentHash,
		"doc_type":     doc.DocType,
		"metadata":     doc.Metadata,
		"author":       doc.Author,
		"created_at":   doc.CreatedAt,
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// handleDocUpdate creates a new version of an existing document (semantic alias for doc_create).
func (s *Server) handleDocUpdate(ctx context.Context, args json.RawMessage) (string, error) {
	return s.handleDocCreate(ctx, args)
}

// handleDocList lists the latest version of each document path in a project.
func (s *Server) handleDocList(ctx context.Context, args json.RawMessage) (string, error) {
	if s.versionedDocumentStore == nil {
		return "", fmt.Errorf("versioned document store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	project := coerceString(m["project"], "")
	if project == "" {
		return "", fmt.Errorf("project is required")
	}
	docType := coerceString(m["doc_type"], "")
	pathPrefix := coerceString(m["path_prefix"], "")
	limit := int(coerceInt(m["limit"], 50))

	docs, err := s.versionedDocumentStore.List(ctx, project, docType, pathPrefix, limit)
	if err != nil {
		return "", fmt.Errorf("doc_list: %w", err)
	}

	type docItem struct {
		CreatedAt string `json:"created_at"`
		Path      string `json:"path"`
		Project   string `json:"project"`
		DocType   string `json:"doc_type"`
		Author    string `json:"author"`
		ID        int64  `json:"id"`
		Version   int    `json:"version"`
	}
	items := make([]docItem, 0, len(docs))
	for _, d := range docs {
		items = append(items, docItem{
			ID:        d.ID,
			Path:      d.Path,
			Project:   d.Project,
			Version:   d.Version,
			DocType:   d.DocType,
			Author:    d.Author,
			CreatedAt: d.CreatedAt.String(),
		})
	}

	out, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// handleDocHistory returns all versions of a document for the given path+project.
func (s *Server) handleDocHistory(ctx context.Context, args json.RawMessage) (string, error) {
	if s.versionedDocumentStore == nil {
		return "", fmt.Errorf("versioned document store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	path := coerceString(m["path"], "")
	project := coerceString(m["project"], "")
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if project == "" {
		return "", fmt.Errorf("project is required")
	}
	limit := int(coerceInt(m["limit"], 0))

	docs, err := s.versionedDocumentStore.GetHistory(ctx, path, project, limit)
	if err != nil {
		return "", fmt.Errorf("doc_history: %w", err)
	}

	type historyItem struct {
		CreatedAt   string `json:"created_at"`
		ContentHash string `json:"content_hash"`
		Author      string `json:"author"`
		ID          int64  `json:"id"`
		Version     int    `json:"version"`
	}
	items := make([]historyItem, 0, len(docs))
	for _, d := range docs {
		items = append(items, historyItem{
			ID:          d.ID,
			Version:     d.Version,
			ContentHash: d.ContentHash,
			Author:      d.Author,
			CreatedAt:   d.CreatedAt.String(),
		})
	}

	result := map[string]any{
		"path":     path,
		"project":  project,
		"versions": items,
		"count":    len(items),
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// handleDocComment adds a comment to a versioned document identified by its document ID.
func (s *Server) handleDocComment(ctx context.Context, args json.RawMessage) (string, error) {
	if s.versionedDocumentStore == nil {
		return "", fmt.Errorf("versioned document store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	documentID := coerceInt64(m["document_id"], 0)
	if documentID <= 0 {
		return "", fmt.Errorf("document_id is required and must be positive")
	}
	author := coerceString(m["author"], "agent")
	content := coerceString(m["content"], "")
	if content == "" {
		return "", fmt.Errorf("content is required")
	}

	var lineStart, lineEnd *int
	if v, ok := m["line_start"]; ok && v != nil {
		ls := int(coerceInt(m["line_start"], 0))
		if ls > 0 {
			lineStart = &ls
		}
	}
	if v, ok := m["line_end"]; ok && v != nil {
		le := int(coerceInt(m["line_end"], 0))
		if le > 0 {
			lineEnd = &le
		}
	}

	commentID, err := s.versionedDocumentStore.AddComment(ctx, documentID, author, content, lineStart, lineEnd)
	if err != nil {
		return "", fmt.Errorf("doc_comment: %w", err)
	}

	result := map[string]any{
		"comment_id":  commentID,
		"document_id": documentID,
		"author":      author,
		"message":     "Comment added successfully",
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}
