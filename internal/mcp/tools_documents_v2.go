package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

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
	for _, key := range []string{"path", "project", "content", "doc_type", "metadata", "author"} {
		if _, _, fieldErr := optionalStringArg(m, key); fieldErr != nil {
			return "", fmt.Errorf("doc_create: %w", fieldErr)
		}
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

	version, present, err := parseDocumentVersion(m)
	if err != nil {
		return "", err
	}
	var doc *gormpkg.VersionedDocument
	if present {
		doc, err = s.versionedDocumentStore.ReadVersion(ctx, path, project, version)
	} else {
		doc, err = s.versionedDocumentStore.ReadLatest(ctx, path, project)
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", fmt.Errorf("document not found: path=%q project=%q", path, project)
		}
		return "", fmt.Errorf("docs(action=read): %w", err)
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
	limit, err := parseDocumentListLimit(m)
	if err != nil {
		return "", err
	}

	docs, err := s.versionedDocumentStore.List(ctx, project, docType, pathPrefix, limit)
	if err != nil {
		return "", fmt.Errorf("docs(action=list): %w", err)
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
	limit, err := parseDocumentHistoryLimit(m)
	if err != nil {
		return "", err
	}

	docs, err := s.versionedDocumentStore.GetHistory(ctx, path, project, limit)
	if err != nil {
		return "", fmt.Errorf("docs(action=history): %w", err)
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

const documentQueryMaxLimit = 100

func parseDocumentVersion(m map[string]any) (int, bool, error) {
	version, present, err := optionalInt64Arg(m, "version")
	if err != nil || (present && (version < 1 || version > int64(math.MaxInt))) {
		return 0, present, fmt.Errorf("version must be a positive in-range integer")
	}
	return int(version), present, nil
}

func parseDocumentListLimit(m map[string]any) (int, error) {
	limit, present, err := optionalInt64Arg(m, "limit")
	if !present {
		return 50, nil
	}
	if err != nil || limit < 1 || limit > documentQueryMaxLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", documentQueryMaxLimit)
	}
	return int(limit), nil
}

func parseDocumentHistoryLimit(m map[string]any) (int, error) {
	limit, present, err := optionalInt64Arg(m, "limit")
	if !present {
		return 0, nil
	}
	if err != nil || limit < 1 || limit > documentQueryMaxLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", documentQueryMaxLimit)
	}
	return int(limit), nil
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

	documentID, err := requireInt64Arg(m, "document_id")
	if err != nil {
		return "", fmt.Errorf("doc_comment: %w", err)
	}
	if documentID <= 0 {
		return "", fmt.Errorf("document_id is required and must be positive")
	}
	author, authorPresent, err := optionalStringArg(m, "author")
	if err != nil {
		return "", fmt.Errorf("doc_comment: %w", err)
	}
	if !authorPresent {
		author = "agent"
	}
	content, _, err := optionalStringArg(m, "content")
	if err != nil {
		return "", fmt.Errorf("doc_comment: %w", err)
	}
	if content == "" {
		return "", fmt.Errorf("content is required")
	}

	var lineStart, lineEnd *int
	if value, present, parseErr := optionalInt64Arg(m, "line_start"); parseErr != nil {
		return "", fmt.Errorf("doc_comment: %w", parseErr)
	} else if present {
		if value <= 0 || value > int64(math.MaxInt) {
			return "", fmt.Errorf("doc_comment: line_start must be an in-range positive integer")
		}
		lineStartValue := int(value)
		lineStart = &lineStartValue
	}
	if value, present, parseErr := optionalInt64Arg(m, "line_end"); parseErr != nil {
		return "", fmt.Errorf("doc_comment: %w", parseErr)
	} else if present {
		if value <= 0 || value > int64(math.MaxInt) {
			return "", fmt.Errorf("doc_comment: line_end must be an in-range positive integer")
		}
		lineEndValue := int(value)
		lineEnd = &lineEndValue
	}

	commentID, err := s.versionedDocumentStore.AddComment(ctx, documentID, author, content, lineStart, lineEnd)
	if err != nil {
		return "", fmt.Errorf("docs(action=comment): %w", err)
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
