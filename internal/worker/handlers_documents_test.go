package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// fakeDocumentStore is a test seam implementing the versionedDocumentStore
// interface. Every call is recorded so tests can assert the REST handlers
// call the store directly (ADR-001) instead of routing through internal/mcp.
type fakeDocumentStore struct {
	createErr  error
	createID   int64
	createCall struct {
		path, project, content, docType, metadata, author string
		called                                            bool
	}

	listErr  error
	listRows []gormdb.VersionedDocument
	listCall struct {
		project, docType, pathPrefix string
		limit                        int
		called                       bool
	}

	historyErr  error
	historyRows []gormdb.VersionedDocument
	historyCall struct {
		path, project string
		limit         int
		called        bool
	}

	readLatestErr  error
	readLatestDoc  *gormdb.VersionedDocument
	readVersionErr error
	readVersionDoc *gormdb.VersionedDocument
	readCall       struct {
		path, project string
		version       int
		called        bool
	}

	commentErr  error
	commentID   int64
	commentCall struct {
		documentID         int64
		author, content    string
		lineStart, lineEnd *int
		called             bool
	}

	commentsErr  error
	commentsRows []gormdb.VersionedDocumentComment
	commentsCall struct {
		documentID int64
		called     bool
	}
}

func (f *fakeDocumentStore) Create(_ context.Context, path, project, content, docType, metadata, author string) (int64, error) {
	f.createCall.called = true
	f.createCall.path = path
	f.createCall.project = project
	f.createCall.content = content
	f.createCall.docType = docType
	f.createCall.metadata = metadata
	f.createCall.author = author
	if f.createErr != nil {
		return 0, f.createErr
	}
	return f.createID, nil
}

func (f *fakeDocumentStore) ReadLatest(_ context.Context, path, project string) (*gormdb.VersionedDocument, error) {
	f.readCall.called = true
	f.readCall.path = path
	f.readCall.project = project
	f.readCall.version = 0
	if f.readLatestErr != nil {
		return nil, f.readLatestErr
	}
	return f.readLatestDoc, nil
}

func (f *fakeDocumentStore) ReadVersion(_ context.Context, path, project string, version int) (*gormdb.VersionedDocument, error) {
	f.readCall.called = true
	f.readCall.path = path
	f.readCall.project = project
	f.readCall.version = version
	if f.readVersionErr != nil {
		return nil, f.readVersionErr
	}
	return f.readVersionDoc, nil
}

func (f *fakeDocumentStore) List(_ context.Context, project, docType, pathPrefix string, limit int) ([]gormdb.VersionedDocument, error) {
	f.listCall.called = true
	f.listCall.project = project
	f.listCall.docType = docType
	f.listCall.pathPrefix = pathPrefix
	f.listCall.limit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listRows, nil
}

func (f *fakeDocumentStore) GetHistory(_ context.Context, path, project string, limit int) ([]gormdb.VersionedDocument, error) {
	f.historyCall.called = true
	f.historyCall.path = path
	f.historyCall.project = project
	f.historyCall.limit = limit
	if f.historyErr != nil {
		return nil, f.historyErr
	}
	return f.historyRows, nil
}

func (f *fakeDocumentStore) AddComment(_ context.Context, documentID int64, author, content string, lineStart, lineEnd *int) (int64, error) {
	f.commentCall.called = true
	f.commentCall.documentID = documentID
	f.commentCall.author = author
	f.commentCall.content = content
	f.commentCall.lineStart = lineStart
	f.commentCall.lineEnd = lineEnd
	if f.commentErr != nil {
		return 0, f.commentErr
	}
	return f.commentID, nil
}

func (f *fakeDocumentStore) GetComments(_ context.Context, documentID int64) ([]gormdb.VersionedDocumentComment, error) {
	f.commentsCall.called = true
	f.commentsCall.documentID = documentID
	if f.commentsErr != nil {
		return nil, f.commentsErr
	}
	return f.commentsRows, nil
}

func documentsTestService(store versionedDocumentStore) *Service {
	return &Service{documentStore: store}
}

// TestHandlersDocuments_Create verifies POST /api/documents calls
// VersionedDocumentStore.Create directly (ADR-001) and returns the new ID.
func TestHandlersDocuments_Create(t *testing.T) {
	t.Parallel()

	fake := &fakeDocumentStore{createID: 42}
	svc := documentsTestService(fake)

	body := bytes.NewBufferString(`{"path":"notes/plan.md","project":"engram","content":"# Plan","doc_type":"markdown","author":"agent-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/documents", body)
	w := httptest.NewRecorder()

	svc.handleCreateDocument(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	require.True(t, fake.createCall.called, "handler must call VersionedDocumentStore.Create directly")
	assert.Equal(t, "notes/plan.md", fake.createCall.path)
	assert.Equal(t, "engram", fake.createCall.project)
	assert.Equal(t, "# Plan", fake.createCall.content)
	assert.Equal(t, "agent-1", fake.createCall.author)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(42), resp["id"])
}

// TestHandlersDocuments_Create_RequiresPath verifies validation rejects an
// empty path before touching the store.
func TestHandlersDocuments_Create_RequiresPath(t *testing.T) {
	t.Parallel()

	fake := &fakeDocumentStore{}
	svc := documentsTestService(fake)

	body := bytes.NewBufferString(`{"project":"engram","content":"# Plan"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/documents", body)
	w := httptest.NewRecorder()

	svc.handleCreateDocument(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, fake.createCall.called, "store must not be called when validation fails")
}

// TestHandlersDocuments_List verifies GET /api/documents calls
// VersionedDocumentStore.List directly (ADR-001) with the query filters.
func TestHandlersDocuments_List(t *testing.T) {
	t.Parallel()

	fake := &fakeDocumentStore{
		listRows: []gormdb.VersionedDocument{
			{ID: 1, Path: "notes/a.md", Project: "engram", Version: 2},
		},
	}
	svc := documentsTestService(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/documents?project=engram&doc_type=markdown&path_prefix=notes/&limit=25", nil)
	w := httptest.NewRecorder()

	svc.handleListDocuments(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, fake.listCall.called, "handler must call VersionedDocumentStore.List directly")
	assert.Equal(t, "engram", fake.listCall.project)
	assert.Equal(t, "markdown", fake.listCall.docType)
	assert.Equal(t, "notes/", fake.listCall.pathPrefix)
	assert.Equal(t, 25, fake.listCall.limit)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	docs, ok := resp["documents"].([]any)
	require.True(t, ok)
	assert.Len(t, docs, 1)
}

// TestHandlersDocuments_List_RequiresProject verifies validation rejects a
// missing project before touching the store.
func TestHandlersDocuments_List_RequiresProject(t *testing.T) {
	t.Parallel()

	fake := &fakeDocumentStore{}
	svc := documentsTestService(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/documents", nil)
	w := httptest.NewRecorder()

	svc.handleListDocuments(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, fake.listCall.called)
}

// TestHandlersDocuments_History verifies GET /api/documents/history calls
// VersionedDocumentStore.GetHistory directly (ADR-001).
func TestHandlersDocuments_History(t *testing.T) {
	t.Parallel()

	fake := &fakeDocumentStore{
		historyRows: []gormdb.VersionedDocument{
			{ID: 2, Path: "notes/a.md", Project: "engram", Version: 2},
			{ID: 1, Path: "notes/a.md", Project: "engram", Version: 1},
		},
	}
	svc := documentsTestService(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/documents/history?path=notes/a.md&project=engram&limit=10", nil)
	w := httptest.NewRecorder()

	svc.handleDocumentHistory(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, fake.historyCall.called, "handler must call VersionedDocumentStore.GetHistory directly")
	assert.Equal(t, "notes/a.md", fake.historyCall.path)
	assert.Equal(t, "engram", fake.historyCall.project)
	assert.Equal(t, 10, fake.historyCall.limit)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(2), resp["count"])
}

// TestHandlersDocuments_History_RequiresPathAndProject verifies validation.
func TestHandlersDocuments_History_RequiresPathAndProject(t *testing.T) {
	t.Parallel()

	fake := &fakeDocumentStore{}
	svc := documentsTestService(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/documents/history?project=engram", nil)
	w := httptest.NewRecorder()

	svc.handleDocumentHistory(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, fake.historyCall.called)
}

// TestHandlersDocuments_AddComment verifies POST /api/documents/comment calls
// VersionedDocumentStore.AddComment directly (ADR-001).
func TestHandlersDocuments_AddComment(t *testing.T) {
	t.Parallel()

	fake := &fakeDocumentStore{commentID: 7}
	svc := documentsTestService(fake)

	body := bytes.NewBufferString(`{"document_id":42,"author":"operator","content":"needs a rewrite","line_start":3,"line_end":5}`)
	req := httptest.NewRequest(http.MethodPost, "/api/documents/comment", body)
	w := httptest.NewRecorder()

	svc.handleAddDocumentComment(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	require.True(t, fake.commentCall.called, "handler must call VersionedDocumentStore.AddComment directly")
	assert.Equal(t, int64(42), fake.commentCall.documentID)
	assert.Equal(t, "operator", fake.commentCall.author)
	assert.Equal(t, "needs a rewrite", fake.commentCall.content)
	require.NotNil(t, fake.commentCall.lineStart)
	assert.Equal(t, 3, *fake.commentCall.lineStart)
	require.NotNil(t, fake.commentCall.lineEnd)
	assert.Equal(t, 5, *fake.commentCall.lineEnd)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(7), resp["comment_id"])
}

// TestHandlersDocuments_AddComment_RequiresDocumentID verifies validation.
func TestHandlersDocuments_AddComment_RequiresDocumentID(t *testing.T) {
	t.Parallel()

	fake := &fakeDocumentStore{}
	svc := documentsTestService(fake)

	body := bytes.NewBufferString(`{"content":"needs a rewrite"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/documents/comment", body)
	w := httptest.NewRecorder()

	svc.handleAddDocumentComment(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, fake.commentCall.called)
}

// TestHandlersDocuments_Read verifies GET /api/documents/read calls
// VersionedDocumentStore.ReadLatest directly (ADR-001) when no version is
// specified, and ReadVersion when a version query param is present. This
// endpoint backs the T010 version-compare UI (reads two versions).
func TestHandlersDocuments_Read(t *testing.T) {
	t.Parallel()

	fake := &fakeDocumentStore{
		readLatestDoc: &gormdb.VersionedDocument{ID: 5, Path: "notes/a.md", Project: "engram", Version: 3, Content: "latest"},
	}
	svc := documentsTestService(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/documents/read?path=notes/a.md&project=engram", nil)
	w := httptest.NewRecorder()

	svc.handleReadDocument(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, fake.readCall.called, "handler must call VersionedDocumentStore.ReadLatest directly")
	assert.Equal(t, "notes/a.md", fake.readCall.path)
	assert.Equal(t, "engram", fake.readCall.project)
	assert.Equal(t, 0, fake.readCall.version)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "latest", resp["content"])
}

// TestHandlersDocuments_Read_SpecificVersion verifies the version query
// param routes to ReadVersion instead of ReadLatest.
func TestHandlersDocuments_Read_SpecificVersion(t *testing.T) {
	t.Parallel()

	fake := &fakeDocumentStore{
		readVersionDoc: &gormdb.VersionedDocument{ID: 4, Path: "notes/a.md", Project: "engram", Version: 2, Content: "older"},
	}
	svc := documentsTestService(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/documents/read?path=notes/a.md&project=engram&version=2", nil)
	w := httptest.NewRecorder()

	svc.handleReadDocument(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, fake.readCall.called)
	assert.Equal(t, 2, fake.readCall.version)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "older", resp["content"])
}

// TestHandlersDocuments_Read_NotFound verifies a 404 when the store returns
// gorm.ErrRecordNotFound.
func TestHandlersDocuments_Read_NotFound(t *testing.T) {
	t.Parallel()

	fake := &fakeDocumentStore{readLatestErr: gormlib.ErrRecordNotFound}
	svc := documentsTestService(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/documents/read?path=missing.md&project=engram", nil)
	w := httptest.NewRecorder()

	svc.handleReadDocument(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestHandlersDocuments_Comments verifies GET /api/documents/comments calls
// VersionedDocumentStore.GetComments directly (ADR-001).
func TestHandlersDocuments_Comments(t *testing.T) {
	t.Parallel()

	fake := &fakeDocumentStore{
		commentsRows: []gormdb.VersionedDocumentComment{
			{ID: 1, DocumentID: 42, Author: "operator", Content: "first pass"},
		},
	}
	svc := documentsTestService(fake)

	req := httptest.NewRequest(http.MethodGet, "/api/documents/comments?document_id=42", nil)
	w := httptest.NewRecorder()

	svc.handleListDocumentComments(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, fake.commentsCall.called, "handler must call VersionedDocumentStore.GetComments directly")
	assert.Equal(t, int64(42), fake.commentsCall.documentID)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	comments, ok := resp["comments"].([]any)
	require.True(t, ok)
	assert.Len(t, comments, 1)
}

// TestHandlersDocuments_StoreUnavailable verifies 503 responses when the
// documents bridge is wired to a nil store (fresh startup / disabled build).
func TestHandlersDocuments_StoreUnavailable(t *testing.T) {
	t.Parallel()

	svc := &Service{}

	req := httptest.NewRequest(http.MethodGet, "/api/documents?project=engram", nil)
	w := httptest.NewRecorder()
	svc.handleListDocuments(w, req)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}
