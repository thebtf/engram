package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	gormlib "gorm.io/gorm"
)

const (
	defaultDocumentListLimit = 50
	maxDocumentListLimit     = 200
)

type versionedDocumentStore interface {
	Create(ctx context.Context, path, project, content, docType, metadata, author string) (int64, error)
	ReadLatest(ctx context.Context, path, project string) (*gormdb.VersionedDocument, error)
	ReadVersion(ctx context.Context, path, project string, version int) (*gormdb.VersionedDocument, error)
	List(ctx context.Context, project, docType, pathPrefix string, limit int) ([]gormdb.VersionedDocument, error)
	GetHistory(ctx context.Context, path, project string, limit int) ([]gormdb.VersionedDocument, error)
	AddComment(ctx context.Context, documentID int64, author, content string, lineStart, lineEnd *int) (int64, error)
	GetComments(ctx context.Context, documentID int64) ([]gormdb.VersionedDocumentComment, error)
}

type documentErrorResponse struct {
	Error string `json:"error"`
}

type documentListItem struct {
	CreatedAt string `json:"created_at"`
	Path      string `json:"path"`
	Project   string `json:"project"`
	DocType   string `json:"doc_type"`
	Author    string `json:"author"`
	ID        int64  `json:"id"`
	Version   int    `json:"version"`
}

type documentListResponse struct {
	Documents  []documentListItem `json:"documents"`
	Project    string             `json:"project"`
	DocType    string             `json:"doc_type,omitempty"`
	PathPrefix string             `json:"path_prefix,omitempty"`
	Count      int                `json:"count"`
	Limit      int                `json:"limit"`
}

type documentReadResponse struct {
	CreatedAt   string `json:"created_at"`
	Path        string `json:"path"`
	Project     string `json:"project"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
	DocType     string `json:"doc_type"`
	Metadata    string `json:"metadata"`
	Author      string `json:"author"`
	ID          int64  `json:"id"`
	Version     int    `json:"version"`
}

type documentHistoryItem struct {
	CreatedAt   string `json:"created_at"`
	ContentHash string `json:"content_hash"`
	Author      string `json:"author"`
	ID          int64  `json:"id"`
	Version     int    `json:"version"`
}

type documentHistoryResponse struct {
	Path     string                `json:"path"`
	Project  string                `json:"project"`
	Versions []documentHistoryItem `json:"versions"`
	Count    int                   `json:"count"`
}

type documentCommentItem struct {
	CreatedAt  string `json:"created_at"`
	Author     string `json:"author"`
	Content    string `json:"content"`
	Status     string `json:"status"`
	ID         int64  `json:"id"`
	DocumentID int64  `json:"document_id"`
	LineStart  *int   `json:"line_start,omitempty"`
	LineEnd    *int   `json:"line_end,omitempty"`
}

type documentCommentsResponse struct {
	Comments   []documentCommentItem `json:"comments"`
	Count      int                   `json:"count"`
	DocumentID int64                 `json:"document_id"`
}

func (s *Service) currentDocumentStore() versionedDocumentStore {
	if s == nil {
		return nil
	}

	s.initMu.RLock()
	defer s.initMu.RUnlock()
	return s.documentStore
}

func writeDocumentError(w http.ResponseWriter, status int, message string) {
	writeJSONStatus(w, status, documentErrorResponse{Error: message})
}

func parseDocumentRequiredQuery(r *http.Request, name string) (string, error) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return "", fmt.Errorf("%s query parameter is required", name)
	}
	return value, nil
}

func parseDocumentListLimit(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return defaultDocumentListLimit, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("invalid limit %q", raw)
	}
	if limit > maxDocumentListLimit {
		limit = maxDocumentListLimit
	}
	return limit, nil
}

func parseDocumentHistoryLimit(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("invalid limit %q", raw)
	}
	if limit > maxDocumentListLimit {
		limit = maxDocumentListLimit
	}
	return limit, nil
}

func parseDocumentPositiveInt64(raw string, field string) (int64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("%s is required", field)
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid %s %q", field, raw)
	}
	return id, nil
}

func parseDocumentPositiveInt(raw string, field string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("%s is required", field)
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid %s %q", field, raw)
	}
	return parsed, nil
}

func documentListItems(docs []gormdb.VersionedDocument) []documentListItem {
	items := make([]documentListItem, 0, len(docs))
	for _, doc := range docs {
		items = append(items, documentListItem{
			ID:        doc.ID,
			Path:      doc.Path,
			Project:   doc.Project,
			Version:   doc.Version,
			DocType:   doc.DocType,
			Author:    doc.Author,
			CreatedAt: doc.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return items
}

func documentHistoryItems(docs []gormdb.VersionedDocument) []documentHistoryItem {
	items := make([]documentHistoryItem, 0, len(docs))
	for _, doc := range docs {
		items = append(items, documentHistoryItem{
			ID:          doc.ID,
			Version:     doc.Version,
			ContentHash: doc.ContentHash,
			Author:      doc.Author,
			CreatedAt:   doc.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return items
}

func documentReadItem(doc *gormdb.VersionedDocument) documentReadResponse {
	return documentReadResponse{
		ID:          doc.ID,
		Path:        doc.Path,
		Project:     doc.Project,
		Version:     doc.Version,
		Content:     doc.Content,
		ContentHash: doc.ContentHash,
		DocType:     doc.DocType,
		Metadata:    doc.Metadata,
		Author:      doc.Author,
		CreatedAt:   doc.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func documentCommentItems(comments []gormdb.VersionedDocumentComment) []documentCommentItem {
	items := make([]documentCommentItem, 0, len(comments))
	for _, comment := range comments {
		items = append(items, documentCommentItem{
			ID:         comment.ID,
			DocumentID: comment.DocumentID,
			Author:     comment.Author,
			Content:    comment.Content,
			Status:     comment.Status,
			LineStart:  comment.LineStart,
			LineEnd:    comment.LineEnd,
			CreatedAt:  comment.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return items
}

func (s *Service) handleCreateDocument(w http.ResponseWriter, r *http.Request) {
	store := s.currentDocumentStore()
	if store == nil {
		writeDocumentError(w, http.StatusServiceUnavailable, "versioned document store not available")
		return
	}

	var req struct {
		Path     string `json:"path"`
		Project  string `json:"project"`
		Content  string `json:"content"`
		DocType  string `json:"doc_type"`
		Metadata string `json:"metadata"`
		Author   string `json:"author"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeDocumentError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	path := strings.TrimSpace(req.Path)
	project := strings.TrimSpace(req.Project)
	content := req.Content
	if path == "" {
		writeDocumentError(w, http.StatusBadRequest, "path is required")
		return
	}
	if project == "" {
		writeDocumentError(w, http.StatusBadRequest, "project is required")
		return
	}
	if strings.TrimSpace(content) == "" {
		writeDocumentError(w, http.StatusBadRequest, "content is required")
		return
	}

	docType := strings.TrimSpace(req.DocType)
	if docType == "" {
		docType = "markdown"
	}
	metadata := strings.TrimSpace(req.Metadata)
	if metadata == "" {
		metadata = "{}"
	}
	author := strings.TrimSpace(req.Author)
	if author == "" {
		author = "operator"
	}

	id, err := store.Create(r.Context(), path, project, content, docType, metadata, author)
	if err != nil {
		writeDocumentError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSONStatus(w, http.StatusCreated, map[string]any{
		"id":      id,
		"path":    path,
		"project": project,
		"message": "document created",
	})
}

func (s *Service) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	store := s.currentDocumentStore()
	if store == nil {
		writeDocumentError(w, http.StatusServiceUnavailable, "versioned document store not available")
		return
	}

	project, err := parseDocumentRequiredQuery(r, "project")
	if err != nil {
		writeDocumentError(w, http.StatusBadRequest, err.Error())
		return
	}
	docType := strings.TrimSpace(r.URL.Query().Get("doc_type"))
	pathPrefix := strings.TrimSpace(r.URL.Query().Get("path_prefix"))
	limit, err := parseDocumentListLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeDocumentError(w, http.StatusBadRequest, err.Error())
		return
	}

	docs, err := store.List(r.Context(), project, docType, pathPrefix, limit)
	if err != nil {
		writeDocumentError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := documentListItems(docs)
	writeJSON(w, documentListResponse{
		Documents:  items,
		Project:    project,
		DocType:    docType,
		PathPrefix: pathPrefix,
		Count:      len(items),
		Limit:      limit,
	})
}

func (s *Service) handleDocumentHistory(w http.ResponseWriter, r *http.Request) {
	store := s.currentDocumentStore()
	if store == nil {
		writeDocumentError(w, http.StatusServiceUnavailable, "versioned document store not available")
		return
	}

	path, err := parseDocumentRequiredQuery(r, "path")
	if err != nil {
		writeDocumentError(w, http.StatusBadRequest, err.Error())
		return
	}
	project, err := parseDocumentRequiredQuery(r, "project")
	if err != nil {
		writeDocumentError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := parseDocumentHistoryLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeDocumentError(w, http.StatusBadRequest, err.Error())
		return
	}

	docs, err := store.GetHistory(r.Context(), path, project, limit)
	if err != nil {
		writeDocumentError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := documentHistoryItems(docs)
	writeJSON(w, documentHistoryResponse{
		Path:     path,
		Project:  project,
		Versions: items,
		Count:    len(items),
	})
}

func (s *Service) handleReadDocument(w http.ResponseWriter, r *http.Request) {
	store := s.currentDocumentStore()
	if store == nil {
		writeDocumentError(w, http.StatusServiceUnavailable, "versioned document store not available")
		return
	}

	path, err := parseDocumentRequiredQuery(r, "path")
	if err != nil {
		writeDocumentError(w, http.StatusBadRequest, err.Error())
		return
	}
	project, err := parseDocumentRequiredQuery(r, "project")
	if err != nil {
		writeDocumentError(w, http.StatusBadRequest, err.Error())
		return
	}

	versionRaw := strings.TrimSpace(r.URL.Query().Get("version"))
	var doc *gormdb.VersionedDocument
	if versionRaw == "" {
		doc, err = store.ReadLatest(r.Context(), path, project)
	} else {
		version, parseErr := parseDocumentPositiveInt(versionRaw, "version")
		if parseErr != nil {
			writeDocumentError(w, http.StatusBadRequest, parseErr.Error())
			return
		}
		doc, err = store.ReadVersion(r.Context(), path, project, version)
	}
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			writeDocumentError(w, http.StatusNotFound, "document not found")
			return
		}
		writeDocumentError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, documentReadItem(doc))
}

func (s *Service) handleAddDocumentComment(w http.ResponseWriter, r *http.Request) {
	store := s.currentDocumentStore()
	if store == nil {
		writeDocumentError(w, http.StatusServiceUnavailable, "versioned document store not available")
		return
	}

	var req struct {
		DocumentID int64  `json:"document_id"`
		Author     string `json:"author"`
		Content    string `json:"content"`
		LineStart  *int   `json:"line_start"`
		LineEnd    *int   `json:"line_end"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeDocumentError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.DocumentID <= 0 {
		writeDocumentError(w, http.StatusBadRequest, "document_id is required and must be positive")
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeDocumentError(w, http.StatusBadRequest, "content is required")
		return
	}
	if req.LineStart != nil && *req.LineStart <= 0 {
		writeDocumentError(w, http.StatusBadRequest, "line_start must be positive when provided")
		return
	}
	if req.LineEnd != nil && *req.LineEnd <= 0 {
		writeDocumentError(w, http.StatusBadRequest, "line_end must be positive when provided")
		return
	}
	if req.LineStart != nil && req.LineEnd != nil && *req.LineEnd < *req.LineStart {
		writeDocumentError(w, http.StatusBadRequest, "line_end must be greater than or equal to line_start")
		return
	}
	author := strings.TrimSpace(req.Author)
	if author == "" {
		author = "operator"
	}

	commentID, err := store.AddComment(r.Context(), req.DocumentID, author, content, req.LineStart, req.LineEnd)
	if err != nil {
		writeDocumentError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSONStatus(w, http.StatusCreated, map[string]any{
		"comment_id":  commentID,
		"document_id": req.DocumentID,
		"author":      author,
		"message":     "comment added",
	})
}

func (s *Service) handleListDocumentComments(w http.ResponseWriter, r *http.Request) {
	store := s.currentDocumentStore()
	if store == nil {
		writeDocumentError(w, http.StatusServiceUnavailable, "versioned document store not available")
		return
	}

	documentID, err := parseDocumentPositiveInt64(r.URL.Query().Get("document_id"), "document_id")
	if err != nil {
		writeDocumentError(w, http.StatusBadRequest, err.Error())
		return
	}

	comments, err := store.GetComments(r.Context(), documentID)
	if err != nil {
		writeDocumentError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := documentCommentItems(comments)
	writeJSON(w, documentCommentsResponse{
		Comments:   items,
		Count:      len(items),
		DocumentID: documentID,
	})
}
