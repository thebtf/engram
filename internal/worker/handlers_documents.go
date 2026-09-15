package worker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	gormlib "gorm.io/gorm"
)

const (
	defaultDocumentListLimit       = 50
	maxDocumentListLimit           = 200
	documentSelectionInvalidMessage = "invalid document selection"
	engramRequestIDHeader           = "X-Engram-Request-ID"
)

type versionedDocumentStore interface {
	Create(ctx context.Context, path, project, content, docType, metadata, author string) (int64, error)
	ReadLatest(ctx context.Context, path, project string) (*gormdb.VersionedDocument, error)
	ReadVersion(ctx context.Context, path, project string, version int) (*gormdb.VersionedDocument, error)
	ReadByID(ctx context.Context, id int64) (*gormdb.VersionedDocument, error)
	List(ctx context.Context, project, docType, pathPrefix string, limit int) ([]gormdb.VersionedDocument, error)
	GetHistory(ctx context.Context, path, project string, limit int) ([]gormdb.VersionedDocument, error)
	AddComment(ctx context.Context, documentID int64, author, content string, lineStart, lineEnd *int) (int64, error)
	GetComments(ctx context.Context, documentID int64) ([]gormdb.VersionedDocumentComment, error)
}

type documentSelectionStore interface {
	Save(context.Context, gormdb.CollectionSelectionScope, gormdb.CollectionSelection) (gormdb.CollectionSelection, error)
	Current(context.Context, gormdb.CollectionSelectionScope) (gormdb.CollectionSelection, error)
	Frozen(context.Context, gormdb.CollectionSelectionScope, string) (gormdb.CollectionSelection, error)
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

type documentCreateRequest struct {
	Path      string                      `json:"path"`
	Project   string                      `json:"project"`
	Content   string                      `json:"content"`
	DocType   string                      `json:"doc_type"`
	Metadata  string                      `json:"metadata"`
	Author    string                      `json:"author"`
	RequestID string                      `json:"request_id,omitempty"`
	Action    string                      `json:"action,omitempty"`
	Selection *documentOperationSelection `json:"selection,omitempty"`
}

type documentOperationSelection struct {
	Kind    gormdb.CollectionSelectionKind `json:"kind"`
	Version int64                          `json:"selection_version"`
	Token   string                         `json:"selection_token,omitempty"`
}

type documentOperationResponse struct {
	RequestID      string                           `json:"request_id"`
	OperationID    string                           `json:"operation_id"`
	OperationState string                           `json:"operation_state"`
	ItemResults    []documentOperationItemResult    `json:"item_results,omitempty"`
	Readback       *documentOperationReadback       `json:"readback,omitempty"`
	Artifact       *documentExportArtifactReference `json:"artifact,omitempty"`
}

type documentOperationItemResult struct {
	TargetID        int64  `json:"target_id"`
	Outcome         string `json:"outcome"`
	ObservedVersion *int   `json:"observed_version,omitempty"`
}

type documentOperationReadback struct {
	Authoritative   bool   `json:"authoritative"`
	Kind            string `json:"kind"`
	OperationStatus string `json:"operation_status,omitempty"`
}

type documentExportArtifactReference struct {
	DownloadURL string `json:"download_url"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	ByteLength  int    `json:"byte_length"`
}

type documentExportArtifact struct {
	bytes       []byte
	filename    string
	contentType string
	scope       gormdb.CollectionSelectionScope
	expiresAt   time.Time
}

type documentExportPayload struct {
	SchemaVersion string                   `json:"schema_version"`
	Documents     []documentExportDocument `json:"documents"`
}

type documentExportDocument struct {
	ID          int64  `json:"id"`
	Path        string `json:"path"`
	Project     string `json:"project"`
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
}

type documentSelectionPageCursor struct {
	Project  string `json:"p"`
	Revision string `json:"r"`
	Offset   int    `json:"o"`
	Limit    int    `json:"l"`
}

type documentSelectionPageRequest struct {
	Project string `json:"project"`
	Cursor  string `json:"cursor,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

var errDocumentSelectionConflict = errors.New("document selection conflicts with current state")

func (s *Service) currentDocumentSelectionStore() documentSelectionStore {
	if s == nil {
		return nil
	}
	s.initMu.RLock()
	defer s.initMu.RUnlock()
	return s.documentSelectionStore
}

func documentSelectionScope(r *http.Request) (gormdb.CollectionSelectionScope, error) {
	if r == nil {
		return gormdb.CollectionSelectionScope{}, gormdb.ErrCollectionSelectionDenied
	}
	identity, found := auth.IdentityFrom(r.Context())
	if !found {
		return gormdb.CollectionSelectionScope{}, gormdb.ErrCollectionSelectionDenied
	}
	subject, found := identity.SessionBrowserSubject()
	if !found {
		return gormdb.CollectionSelectionScope{}, gormdb.ErrCollectionSelectionDenied
	}
	sessionID, found := operatorCodeSessionID(r)
	if !found {
		return gormdb.CollectionSelectionScope{}, gormdb.ErrCollectionSelectionDenied
	}
	scope, err := (operatorCollectionScopeAuthority{}).ResolveOperatorCollectionScope(r.Context(), identity, sessionID, documentSelectionDomain)
	if err != nil || scope.SubjectUserID != subject.UserID || scope.SessionID != sessionID || scope.Domain != documentSelectionDomain {
		return gormdb.CollectionSelectionScope{}, gormdb.ErrCollectionSelectionDenied
	}
	return scope, nil
}

func validDocumentSelectionProject(project string) bool {
	return operatorCodeText(project)
}

func documentSelectionFilterFingerprint(project string) string {
	digest := sha256.Sum256([]byte("documents-collection-filter/v1\x00" + project))
	return fmt.Sprintf("sha256:%x", digest)
}

func documentSelectionTargetsRevision(project string, targets []gormdb.CollectionSelectionTarget) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("documents-collection-revision/v1\x00" + project + "\x00"))
	for _, target := range targets {
		_, _ = hash.Write([]byte(target.ID))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(strconv.FormatUint(target.ExpectedVersion, 10)))
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil))
}

func (s *Service) documentSelectionTargets(ctx context.Context, project string) ([]gormdb.CollectionSelectionTarget, error) {
	if !validDocumentSelectionProject(project) {
		return nil, gormdb.ErrCollectionSelectionInvalid
	}
	store := s.currentDocumentStore()
	if store == nil {
		return nil, errors.New("versioned document store not available")
	}
	documents, err := store.List(ctx, project, "", "", gormdb.CollectionSelectionMaxTargets+1)
	if err != nil {
		return nil, err
	}
	if len(documents) == 0 {
		return nil, gormdb.ErrCollectionSelectionDenied
	}
	if len(documents) > gormdb.CollectionSelectionMaxTargets {
		return nil, gormdb.ErrCollectionSelectionInvalid
	}
	targets := make([]gormdb.CollectionSelectionTarget, 0, len(documents))
	for _, document := range documents {
		if document.ID < 1 || document.Version < 1 {
			return nil, gormdb.ErrCollectionSelectionInvalid
		}
		targets = append(targets, gormdb.CollectionSelectionTarget{ID: strconv.FormatInt(document.ID, 10), ExpectedVersion: uint64(document.Version)})
	}
	return targets, nil
}

func (s *Service) documentSelectionExplicitTargets(ctx context.Context, requested []gormdb.CollectionSelectionTarget) ([]gormdb.CollectionSelectionTarget, error) {
	if len(requested) == 0 || len(requested) > gormdb.CollectionSelectionMaxTargets {
		return nil, gormdb.ErrCollectionSelectionInvalid
	}
	store := s.currentDocumentStore()
	if store == nil {
		return nil, errors.New("versioned document store not available")
	}
	resolved := make([]gormdb.CollectionSelectionTarget, 0, len(requested))
	seen := make(map[int64]struct{}, len(requested))
	for _, target := range requested {
		id, err := strconv.ParseInt(target.ID, 10, 64)
		if err != nil || id < 1 || target.ExpectedVersion == 0 {
			return nil, gormdb.ErrCollectionSelectionInvalid
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, gormdb.ErrCollectionSelectionInvalid
		}
		seen[id] = struct{}{}
		document, err := store.ReadByID(ctx, id)
		if errors.Is(err, gormlib.ErrRecordNotFound) || document == nil {
			return nil, gormdb.ErrCollectionSelectionDenied
		}
		if err != nil {
			return nil, err
		}
		if document.Version != int(target.ExpectedVersion) {
			return nil, gormdb.ErrCollectionSelectionReconfirmationRequired
		}
		resolved = append(resolved, gormdb.CollectionSelectionTarget{ID: strconv.FormatInt(document.ID, 10), ExpectedVersion: uint64(document.Version)})
	}
	return resolved, nil
}

func documentSelectionPageCursorEncode(cursor documentSelectionPageCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	if len(encoded) > 512 {
		return "", gormdb.ErrCollectionSelectionInvalid
	}
	return encoded, nil
}

func documentSelectionPageCursorDecode(value string) (documentSelectionPageCursor, error) {
	if value == "" || len(value) > 512 {
		return documentSelectionPageCursor{}, gormdb.ErrCollectionSelectionInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return documentSelectionPageCursor{}, gormdb.ErrCollectionSelectionInvalid
	}
	var cursor documentSelectionPageCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || !validDocumentSelectionProject(cursor.Project) || cursor.Offset < 0 || cursor.Limit < 1 || cursor.Limit > gormdb.CollectionPageMaxSize || len(cursor.Revision) != len("sha256:")+64 || !strings.HasPrefix(cursor.Revision, "sha256:") {
		return documentSelectionPageCursor{}, gormdb.ErrCollectionSelectionInvalid
	}
	return cursor, nil
}

func (s *Service) documentSelectionPage(ctx context.Context, project, cursor string, limit int) (gormdb.CollectionPage, error) {
	if !validDocumentSelectionProject(project) || limit < 1 || limit > gormdb.CollectionPageMaxSize {
		return gormdb.CollectionPage{}, gormdb.ErrCollectionSelectionInvalid
	}
	targets, err := s.documentSelectionTargets(ctx, project)
	if err != nil {
		return gormdb.CollectionPage{}, err
	}
	revision := documentSelectionTargetsRevision(project, targets)
	offset := 0
	if cursor != "" {
		decoded, err := documentSelectionPageCursorDecode(cursor)
		if err != nil || decoded.Project != project || decoded.Limit != limit {
			return gormdb.CollectionPage{}, gormdb.ErrCollectionSelectionInvalid
		}
		if decoded.Revision != revision {
			return gormdb.CollectionPage{}, gormdb.ErrCollectionSelectionReconfirmationRequired
		}
		offset = decoded.Offset
	}
	if offset >= len(targets) {
		return gormdb.CollectionPage{}, gormdb.ErrCollectionSelectionInvalid
	}
	pageCursor, err := documentSelectionPageCursorEncode(documentSelectionPageCursor{Project: project, Revision: revision, Offset: offset, Limit: limit})
	if err != nil {
		return gormdb.CollectionPage{}, err
	}
	end := min(offset+limit, len(targets))
	total := int64(len(targets))
	page := gormdb.CollectionPage{Cursor: pageCursor, Targets: append([]gormdb.CollectionSelectionTarget(nil), targets[offset:end]...), Total: &total}
	if end < len(targets) {
		page.NextCursor, err = documentSelectionPageCursorEncode(documentSelectionPageCursor{Project: project, Revision: revision, Offset: end, Limit: limit})
		if err != nil {
			return gormdb.CollectionPage{}, err
		}
	}
	return page, nil
}

func writeDocumentSelectionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gormdb.ErrCollectionSelectionDenied):
		writeDocumentError(w, http.StatusForbidden, "document selection forbidden")
	case errors.Is(err, gormdb.ErrCollectionSelectionReconfirmationRequired):
		writeDocumentError(w, http.StatusPreconditionFailed, "document selection is stale")
	case errors.Is(err, gormdb.ErrCollectionSelectionInvalid):
		writeDocumentError(w, http.StatusBadRequest, documentSelectionInvalidMessage)
	default:
		writeDocumentError(w, http.StatusServiceUnavailable, "document selection unavailable")
	}
}

func (s *Service) handleDocumentSelectionSnapshot(w http.ResponseWriter, r *http.Request) {
	var request operatorCollectionSnapshotRequest
	if !operatorCodeText(r.Header.Get(engramRequestIDHeader)) || json.NewDecoder(r.Body).Decode(&request) != nil || request.Domain != documentSelectionDomain {
		writeDocumentError(w, http.StatusBadRequest, documentSelectionInvalidMessage)
		return
	}
	scope, err := documentSelectionScope(r)
	if err != nil {
		writeDocumentSelectionError(w, err)
		return
	}
	selection, err := request.selection()
	if err != nil {
		writeDocumentSelectionError(w, gormdb.ErrCollectionSelectionInvalid)
		return
	}
	switch selection.Kind {
	case gormdb.CollectionSelectionNone:
	case gormdb.CollectionSelectionExplicit:
		selection.Targets, err = s.documentSelectionExplicitTargets(r.Context(), selection.Targets)
	case gormdb.CollectionSelectionPage:
		cursor, decodeErr := documentSelectionPageCursorDecode(selection.Cursor)
		if decodeErr != nil {
			err = decodeErr
			break
		}
		page, pageErr := s.documentSelectionPage(r.Context(), cursor.Project, selection.Cursor, cursor.Limit)
		if pageErr != nil {
			err = pageErr
			break
		}
		selection.Cursor, selection.Targets = page.Cursor, page.Targets
	case gormdb.CollectionSelectionFrozenFilter:
		if request.Selection.Filter == nil {
			err = gormdb.ErrCollectionSelectionInvalid
			break
		}
		project := request.Selection.Filter.Scope
		selection.Targets, err = s.documentSelectionTargets(r.Context(), project)
		if err == nil {
			selection.FilterFingerprint = documentSelectionFilterFingerprint(project)
			selection.ExpiresAt = time.Now().UTC().Add(15 * time.Minute)
		}
	default:
		err = gormdb.ErrCollectionSelectionInvalid
	}
	if err != nil {
		writeDocumentSelectionError(w, err)
		return
	}
	if err := gormdb.ValidateCollectionSelectionInput(selection); err != nil {
		writeDocumentSelectionError(w, err)
		return
	}
	store := s.currentDocumentSelectionStore()
	if store == nil {
		writeDocumentSelectionError(w, errors.New("document selection store unavailable"))
		return
	}
	saved, err := store.Save(r.Context(), scope, selection)
	if err != nil {
		writeDocumentSelectionError(w, err)
		return
	}
	writeJSON(w, operatorCollectionSnapshotResponse{Selection: newOperatorCollectionSnapshotDTO(documentSelectionDomain, saved)})
}

func (s *Service) handleDocumentSelectionCurrent(w http.ResponseWriter, r *http.Request) {
	var request operatorCollectionCurrentRequest
	if !operatorCodeText(r.Header.Get(engramRequestIDHeader)) || json.NewDecoder(r.Body).Decode(&request) != nil || request.Domain != documentSelectionDomain {
		writeDocumentError(w, http.StatusBadRequest, documentSelectionInvalidMessage)
		return
	}
	scope, err := documentSelectionScope(r)
	if err != nil {
		writeDocumentSelectionError(w, err)
		return
	}
	store := s.currentDocumentSelectionStore()
	if store == nil {
		writeDocumentSelectionError(w, errors.New("document selection store unavailable"))
		return
	}
	selection, err := store.Current(r.Context(), scope)
	if err != nil {
		writeDocumentSelectionError(w, err)
		return
	}
	writeJSON(w, operatorCollectionSnapshotResponse{Selection: newOperatorCollectionSnapshotDTO(documentSelectionDomain, selection)})
}

func (s *Service) handleDocumentSelectionPage(w http.ResponseWriter, r *http.Request) {
	var request documentSelectionPageRequest
	if !operatorCodeText(r.Header.Get(engramRequestIDHeader)) || json.NewDecoder(r.Body).Decode(&request) != nil {
		writeDocumentError(w, http.StatusBadRequest, "invalid document selection page")
		return
	}
	if _, err := documentSelectionScope(r); err != nil {
		writeDocumentSelectionError(w, err)
		return
	}
	limit := request.Limit
	if limit == 0 {
		limit = maxDocumentListLimit
	}
	page, err := s.documentSelectionPage(r.Context(), request.Project, request.Cursor, limit)
	if err != nil {
		writeDocumentSelectionError(w, err)
		return
	}
	writeJSON(w, operatorCollectionPageResponse{FilterFingerprint: documentSelectionFilterFingerprint(request.Project), Cursor: page.Cursor, Targets: page.Targets, NextCursor: page.NextCursor, Total: page.Total})
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

	var req documentCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeDocumentError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Action != "" {
		s.handleDocumentSelectionOperation(w, r, &req)
		return
	}
	if req.RequestID != "" || req.Selection != nil {
		writeDocumentError(w, http.StatusBadRequest, "document selection operation requires an action")
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

// handleDocumentSelectionOperation resolves the server-owned selection under
// the current browser owner and only reports an export after its artifact bytes
// exist. Document contents remain outside the operation status response.
func (s *Service) handleDocumentSelectionOperation(w http.ResponseWriter, r *http.Request, request *documentCreateRequest) {
	if request == nil || !operatorCodeText(request.RequestID) || r.Header.Get(engramRequestIDHeader) != request.RequestID || !documentSelectionOperationValid(request) {
		writeDocumentOperationFailure(w, http.StatusBadRequest, "", "validation_error")
		return
	}
	selection, scope, err := s.resolveDocumentOperationSelection(r, request.Selection)
	if err != nil {
		writeDocumentSelectionOperationError(w, request.RequestID, err)
		return
	}
	targets, err := documentOperationTargets(selection)
	if err != nil {
		writeDocumentSelectionOperationError(w, request.RequestID, err)
		return
	}
	store := s.currentDocumentStore()
	if store == nil {
		writeDocumentOperationFailure(w, http.StatusServiceUnavailable, request.RequestID, "failed")
		return
	}

	items := make([]documentOperationItemResult, 0, len(targets))
	exported := make([]documentExportDocument, 0, len(targets))
	committed := 0
	conflicted := 0
	for _, target := range targets {
		id, _ := strconv.ParseInt(target.ID, 10, 64)
		document, err := store.ReadByID(r.Context(), id)
		if err != nil {
			outcome := "failed"
			if errors.Is(err, gormlib.ErrRecordNotFound) {
				outcome = "conflict"
				conflicted++
			}
			items = append(items, documentOperationItemResult{TargetID: id, Outcome: outcome})
			continue
		}
		if document == nil || document.ID != id || document.Version != int(target.ExpectedVersion) {
			items = append(items, documentOperationItemResult{TargetID: id, Outcome: "conflict"})
			conflicted++
			continue
		}
		observedVersion := document.Version
		items = append(items, documentOperationItemResult{TargetID: id, Outcome: "committed", ObservedVersion: &observedVersion})
		exported = append(exported, documentExportDocument{ID: document.ID, Path: document.Path, Project: document.Project, Version: document.Version, ContentHash: document.ContentHash})
		committed++
	}

	response := documentOperationResponse{RequestID: request.RequestID, OperationID: "documents-export:" + request.RequestID, ItemResults: items}
	if committed == len(items) {
		artifact, err := s.saveDocumentExportArtifact(scope, exported)
		if err != nil {
			writeDocumentOperationFailure(w, http.StatusServiceUnavailable, request.RequestID, "failed")
			return
		}
		response.OperationState = "completed"
		response.Readback = &documentOperationReadback{Authoritative: true, Kind: "non_disclosing", OperationStatus: "export_ready"}
		response.Artifact = &artifact
		writeJSON(w, response)
		return
	}
	if committed > 0 {
		response.OperationState = "partial"
		writeJSONStatus(w, http.StatusMultiStatus, response)
		return
	}
	response.OperationState = "failed"
	if conflicted == len(items) {
		writeJSONStatus(w, http.StatusConflict, response)
		return
	}
	writeJSONStatus(w, http.StatusInternalServerError, response)
}

func documentSelectionOperationValid(request *documentCreateRequest) bool {
	if request == nil || request.Action != "export" || request.Selection == nil || request.Selection.Version < 1 {
		return false
	}
	switch request.Selection.Kind {
	case gormdb.CollectionSelectionExplicit, gormdb.CollectionSelectionPage:
		return request.Selection.Token == ""
	case gormdb.CollectionSelectionFrozenFilter:
		return request.Selection.Token != ""
	default:
		return false
	}
}

func (s *Service) resolveDocumentOperationSelection(r *http.Request, requested *documentOperationSelection) (gormdb.CollectionSelection, gormdb.CollectionSelectionScope, error) {
	var empty gormdb.CollectionSelection
	var emptyScope gormdb.CollectionSelectionScope
	if requested == nil {
		return empty, emptyScope, gormdb.ErrCollectionSelectionInvalid
	}
	scope, err := documentSelectionScope(r)
	if err != nil {
		return empty, emptyScope, err
	}
	store := s.currentDocumentSelectionStore()
	if store == nil {
		return empty, emptyScope, errors.New("document selection store unavailable")
	}
	var selection gormdb.CollectionSelection
	switch requested.Kind {
	case gormdb.CollectionSelectionExplicit, gormdb.CollectionSelectionPage:
		if requested.Token != "" {
			return empty, emptyScope, gormdb.ErrCollectionSelectionInvalid
		}
		selection, err = store.Current(r.Context(), scope)
	case gormdb.CollectionSelectionFrozenFilter:
		selection, err = store.Frozen(r.Context(), scope, requested.Token)
	default:
		return empty, emptyScope, gormdb.ErrCollectionSelectionInvalid
	}
	if err != nil {
		return empty, emptyScope, err
	}
	if selection.ReconfirmationRequired {
		return empty, emptyScope, gormdb.ErrCollectionSelectionReconfirmationRequired
	}
	if selection.Kind != requested.Kind || selection.Version != requested.Version {
		return empty, emptyScope, errDocumentSelectionConflict
	}
	if requested.Kind == gormdb.CollectionSelectionFrozenFilter && selection.Token != requested.Token {
		return empty, emptyScope, gormdb.ErrCollectionSelectionDenied
	}
	return selection, scope, nil
}

func documentOperationTargets(selection gormdb.CollectionSelection) ([]gormdb.CollectionSelectionTarget, error) {
	excluded := make(map[string]struct{}, len(selection.ExcludedIDs))
	for _, id := range selection.ExcludedIDs {
		excluded[id] = struct{}{}
	}
	targets := make([]gormdb.CollectionSelectionTarget, 0, len(selection.Targets))
	for _, target := range selection.Targets {
		if _, skip := excluded[target.ID]; skip {
			continue
		}
		if _, err := strconv.ParseInt(target.ID, 10, 64); err != nil || target.ExpectedVersion == 0 {
			return nil, gormdb.ErrCollectionSelectionInvalid
		}
		targets = append(targets, target)
	}
	if len(targets) == 0 || len(targets) > gormdb.CollectionSelectionMaxTargets {
		return nil, gormdb.ErrCollectionSelectionInvalid
	}
	return targets, nil
}

func writeDocumentSelectionOperationError(w http.ResponseWriter, requestID string, err error) {
	switch {
	case errors.Is(err, gormdb.ErrCollectionSelectionDenied):
		writeDocumentOperationFailure(w, http.StatusForbidden, requestID, "denied")
	case errors.Is(err, gormdb.ErrCollectionSelectionReconfirmationRequired):
		writeDocumentOperationFailure(w, http.StatusPreconditionFailed, requestID, "stale")
	case errors.Is(err, errDocumentSelectionConflict):
		writeDocumentOperationFailure(w, http.StatusConflict, requestID, "failed")
	case errors.Is(err, gormdb.ErrCollectionSelectionInvalid):
		writeDocumentOperationFailure(w, http.StatusBadRequest, requestID, "validation_error")
	default:
		writeDocumentOperationFailure(w, http.StatusServiceUnavailable, requestID, "failed")
	}
}

const (
	documentExportArtifactMaxBytes = 1 << 20
	documentExportArtifactLimit    = 100
	documentExportArtifactTTL      = 15 * time.Minute
)

func (s *Service) saveDocumentExportArtifact(scope gormdb.CollectionSelectionScope, documents []documentExportDocument) (documentExportArtifactReference, error) {
	payload, err := json.Marshal(documentExportPayload{SchemaVersion: "engram.documents.export/v1", Documents: documents})
	if err != nil || len(payload) == 0 || len(payload) > documentExportArtifactMaxBytes {
		return documentExportArtifactReference{}, errors.New("document export artifact is invalid")
	}
	id := uuid.NewString()
	filename := "documents-export-" + id + ".json"
	now := time.Now().UTC()
	expiresAt := now.Add(documentExportArtifactTTL)
	s.documentExportArtifactsMu.Lock()
	defer s.documentExportArtifactsMu.Unlock()
	if s.documentExportArtifacts == nil {
		s.documentExportArtifacts = make(map[string]documentExportArtifact)
	}
	for artifactID, artifact := range s.documentExportArtifacts {
		if !artifact.expiresAt.After(now) {
			delete(s.documentExportArtifacts, artifactID)
		}
	}
	if len(s.documentExportArtifacts) >= documentExportArtifactLimit {
		var oldestID string
		var oldest time.Time
		for artifactID, artifact := range s.documentExportArtifacts {
			if oldestID == "" || artifact.expiresAt.Before(oldest) {
				oldestID, oldest = artifactID, artifact.expiresAt
			}
		}
		delete(s.documentExportArtifacts, oldestID)
	}
	s.documentExportArtifacts[id] = documentExportArtifact{bytes: payload, filename: filename, contentType: "application/json", scope: scope, expiresAt: expiresAt}
	return documentExportArtifactReference{DownloadURL: "/api/documents/exports/" + id, Filename: filename, ContentType: "application/json", ByteLength: len(payload)}, nil
}

func (s *Service) handleDownloadDocumentExport(w http.ResponseWriter, r *http.Request) {
	scope, err := documentSelectionScope(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	id := chi.URLParam(r, "artifactID")
	if parsed, err := uuid.Parse(id); err != nil || parsed == uuid.Nil || parsed.String() != id {
		http.NotFound(w, r)
		return
	}
	s.documentExportArtifactsMu.Lock()
	artifact, found := s.documentExportArtifacts[id]
	if found && !artifact.expiresAt.After(time.Now().UTC()) {
		delete(s.documentExportArtifacts, id)
		found = false
	}
	s.documentExportArtifactsMu.Unlock()
	if !found || artifact.scope.SubjectUserID != scope.SubjectUserID || artifact.scope.SessionID != scope.SessionID || artifact.scope.Domain != scope.Domain {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", artifact.contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", artifact.filename))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(artifact.bytes)
}

func writeDocumentOperationFailure(w http.ResponseWriter, status int, requestID, state string) {
	writeJSONStatus(w, status, documentOperationResponse{RequestID: requestID, OperationState: state})
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
