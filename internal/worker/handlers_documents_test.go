package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

type documentReadVersionKey struct {
	path    string
	project string
	version int
}

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

	readLatestErr   error
	readLatestDoc   *gormdb.VersionedDocument
	readVersionErr  error
	readVersionDoc  *gormdb.VersionedDocument
	readVersionDocs map[documentReadVersionKey]*gormdb.VersionedDocument
	readByIDErr     error
	readByIDDocs    map[int64]*gormdb.VersionedDocument
	readCall        struct {
		path, project string
		version       int
		called        bool
	}
	readByIDCall struct {
		id     int64
		called bool
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
	if f.readVersionDocs != nil {
		document, found := f.readVersionDocs[documentReadVersionKey{path: path, project: project, version: version}]
		if !found {
			return nil, gormlib.ErrRecordNotFound
		}
		return document, nil
	}
	return f.readVersionDoc, nil
}

func (f *fakeDocumentStore) ReadByID(_ context.Context, id int64) (*gormdb.VersionedDocument, error) {
	f.readByIDCall.called = true
	f.readByIDCall.id = id
	if f.readByIDErr != nil {
		return nil, f.readByIDErr
	}
	document, found := f.readByIDDocs[id]
	if !found {
		return nil, gormlib.ErrRecordNotFound
	}
	return document, nil
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

const documentSelectionTestSession = "documents-browser-session"

type documentSelectionStoreFake struct {
	selection     gormdb.CollectionSelection
	currentErr    error
	frozenErr     error
	expectedScope *gormdb.CollectionSelectionScope
	currentCalls  int
	frozenCalls   int
}

func (store *documentSelectionStoreFake) Save(_ context.Context, _ gormdb.CollectionSelectionScope, selection gormdb.CollectionSelection) (gormdb.CollectionSelection, error) {
	store.selection = selection
	return selection, nil
}

func (store *documentSelectionStoreFake) Current(_ context.Context, scope gormdb.CollectionSelectionScope) (gormdb.CollectionSelection, error) {
	store.currentCalls++
	if store.expectedScope != nil && scope != *store.expectedScope {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionDenied
	}
	if store.currentErr != nil {
		return gormdb.CollectionSelection{}, store.currentErr
	}
	return store.selection, nil
}

func (store *documentSelectionStoreFake) Frozen(_ context.Context, scope gormdb.CollectionSelectionScope, token string) (gormdb.CollectionSelection, error) {
	store.frozenCalls++
	if store.expectedScope != nil && scope != *store.expectedScope {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionDenied
	}
	if store.frozenErr != nil {
		return gormdb.CollectionSelection{}, store.frozenErr
	}
	if store.selection.Token != token {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionDenied
	}
	return store.selection, nil
}

func documentsSelectionTestService(store versionedDocumentStore, selections documentSelectionStore) *Service {
	service := documentsTestService(store)
	service.documentSelectionStore = selections
	return service
}

func documentSelectionTestScope(t *testing.T, identity auth.Identity) gormdb.CollectionSelectionScope {
	t.Helper()
	scope, err := (operatorCollectionScopeAuthority{}).ResolveOperatorCollectionScope(context.Background(), identity, documentSelectionTestSession, documentSelectionDomain)
	require.NoError(t, err)
	return scope
}

func callDocumentSelectionOperation(t *testing.T, service *Service, request documentCreateRequest, identity auth.Identity) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	require.NoError(t, err)
	httpRequest := httptest.NewRequest(http.MethodPost, "/api/documents", bytes.NewReader(body))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("X-Engram-Request-ID", request.RequestID)
	httpRequest.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: documentSelectionTestSession})
	httpRequest = httpRequest.WithContext(auth.WithIdentity(httpRequest.Context(), identity))
	recorder := httptest.NewRecorder()
	service.handleCreateDocument(recorder, httpRequest)
	return recorder
}

func documentExportRequest(requestID string, kind gormdb.CollectionSelectionKind, version int64, token string) documentCreateRequest {
	return documentCreateRequest{RequestID: requestID, Action: "export", Selection: &documentOperationSelection{Kind: kind, Version: version, Token: token}}
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

func TestHandlersDocuments_SelectionExportUsesAuthoritativeSelectionAndProducesDownload(t *testing.T) {
	t.Parallel()
	identity := auth.SessionForBrowserUser("operator", 41)
	document := gormdb.VersionedDocument{ID: 42, Path: "safe/export.md", Project: "engram", Version: 3, Content: "must not appear in export operation status or artifact", ContentHash: "content-sha", Metadata: `{"private":"must not appear"}`}
	target := gormdb.CollectionSelectionTarget{ID: "42", ExpectedVersion: 3}
	scope := documentSelectionTestScope(t, identity)

	for _, selection := range []struct {
		name  string
		kind  gormdb.CollectionSelectionKind
		token string
	}{
		{name: "explicit", kind: gormdb.CollectionSelectionExplicit},
		{name: "page", kind: gormdb.CollectionSelectionPage},
		{name: "frozen filter", kind: gormdb.CollectionSelectionFrozenFilter, token: "b857ebf7-c1cf-4a1f-a733-465ee492d3cb"},
	} {
		t.Run(selection.name, func(t *testing.T) {
			fake := &fakeDocumentStore{readByIDDocs: map[int64]*gormdb.VersionedDocument{document.ID: &document}}
			selections := &documentSelectionStoreFake{selection: gormdb.CollectionSelection{Kind: selection.kind, Version: 1, Token: selection.token, Targets: []gormdb.CollectionSelectionTarget{target}}, expectedScope: &scope}
			service := documentsSelectionTestService(fake, selections)
			recorder := callDocumentSelectionOperation(t, service, documentExportRequest("documents-export-"+string(selection.kind), selection.kind, 1, selection.token), identity)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.True(t, fake.readByIDCall.called, "export must resolve the server-owned document ID")
			assert.Equal(t, document.ID, fake.readByIDCall.id)
			assert.NotContains(t, recorder.Body.String(), document.Content)
			assert.NotContains(t, recorder.Body.String(), document.Metadata)

			var response documentOperationResponse
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.NotNil(t, response.Artifact)
			assert.Equal(t, "completed", response.OperationState)
			assert.Equal(t, []documentOperationItemResult{{TargetID: document.ID, Outcome: "committed", ObservedVersion: &document.Version}}, response.ItemResults)
			assert.Equal(t, documentOperationReadback{Authoritative: true, Kind: "non_disclosing", OperationStatus: "export_ready"}, *response.Readback)

			artifactID := strings.TrimPrefix(response.Artifact.DownloadURL, "/api/documents/exports/")
			download := httptest.NewRequest(http.MethodGet, response.Artifact.DownloadURL, nil)
			download.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: documentSelectionTestSession})
			route := chi.NewRouteContext()
			route.URLParams.Add("artifactID", artifactID)
			download = download.WithContext(auth.WithIdentity(context.WithValue(download.Context(), chi.RouteCtxKey, route), identity))
			artifactRecorder := httptest.NewRecorder()
			service.handleDownloadDocumentExport(artifactRecorder, download)

			require.Equal(t, http.StatusOK, artifactRecorder.Code)
			assert.Equal(t, "application/json", artifactRecorder.Header().Get("Content-Type"))
			assert.Equal(t, `attachment; filename="`+response.Artifact.Filename+`"`, artifactRecorder.Header().Get("Content-Disposition"))
			assert.Equal(t, response.Artifact.ByteLength, artifactRecorder.Body.Len())
			var payload documentExportPayload
			require.NoError(t, json.Unmarshal(artifactRecorder.Body.Bytes(), &payload))
			assert.Equal(t, documentExportPayload{SchemaVersion: "engram.documents.export/v1", Documents: []documentExportDocument{{ID: document.ID, Path: document.Path, Project: document.Project, Version: document.Version, ContentHash: document.ContentHash}}}, payload)
			assert.NotContains(t, artifactRecorder.Body.String(), document.Content)
			assert.NotContains(t, artifactRecorder.Body.String(), document.Metadata)
		})
	}
}

func TestHandlersDocuments_SelectionExportRejectsFabricatedExpiredStaleAndMemberMismatchSelections(t *testing.T) {
	t.Parallel()
	identity := auth.SessionForBrowserUser("operator", 41)
	target := gormdb.CollectionSelectionTarget{ID: "42", ExpectedVersion: 3}
	scope := documentSelectionTestScope(t, identity)

	for _, testCase := range []struct {
		name       string
		request    documentCreateRequest
		selection  gormdb.CollectionSelection
		frozenErr  error
		identity   auth.Identity
		wantStatus int
	}{
		{name: "fabricated version", request: documentExportRequest("documents-fabricated", gormdb.CollectionSelectionExplicit, 1, ""), selection: gormdb.CollectionSelection{Kind: gormdb.CollectionSelectionExplicit, Version: 2, Targets: []gormdb.CollectionSelectionTarget{target}}, identity: identity, wantStatus: http.StatusConflict},
		{name: "expired frozen selection", request: documentExportRequest("documents-expired", gormdb.CollectionSelectionFrozenFilter, 1, "b857ebf7-c1cf-4a1f-a733-465ee492d3cb"), frozenErr: gormdb.ErrCollectionSelectionReconfirmationRequired, identity: identity, wantStatus: http.StatusPreconditionFailed},
		{name: "stale current selection", request: documentExportRequest("documents-stale", gormdb.CollectionSelectionPage, 1, ""), selection: gormdb.CollectionSelection{Kind: gormdb.CollectionSelectionPage, Version: 1, Targets: []gormdb.CollectionSelectionTarget{target}, ReconfirmationRequired: true, ReconfirmationReason: gormdb.CollectionSelectionReconfirmExpired}, identity: identity, wantStatus: http.StatusPreconditionFailed},
		{name: "member mismatch", request: documentExportRequest("documents-member-mismatch", gormdb.CollectionSelectionExplicit, 1, ""), selection: gormdb.CollectionSelection{Kind: gormdb.CollectionSelectionExplicit, Version: 1, Targets: []gormdb.CollectionSelectionTarget{target}}, identity: auth.SessionForBrowserUser("other", 42), wantStatus: http.StatusForbidden},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fake := &fakeDocumentStore{readByIDDocs: map[int64]*gormdb.VersionedDocument{42: {ID: 42, Version: 3}}}
			selections := &documentSelectionStoreFake{selection: testCase.selection, frozenErr: testCase.frozenErr, expectedScope: &scope}
			recorder := callDocumentSelectionOperation(t, documentsSelectionTestService(fake, selections), testCase.request, testCase.identity)
			assert.Equal(t, testCase.wantStatus, recorder.Code, recorder.Body.String())
			assert.False(t, fake.readByIDCall.called, "rejected selections must not touch targets")
		})
	}
}

func TestHandlersDocuments_SelectionExportKeepsPartialTruthWithoutAnArtifact(t *testing.T) {
	t.Parallel()
	identity := auth.SessionForBrowserUser("operator", 41)
	scope := documentSelectionTestScope(t, identity)
	allowed := gormdb.VersionedDocument{ID: 51, Path: "safe/allowed.md", Project: "engram", Version: 2, Content: "selected document body must remain private"}
	fake := &fakeDocumentStore{readByIDDocs: map[int64]*gormdb.VersionedDocument{allowed.ID: &allowed}}
	selection := &documentSelectionStoreFake{selection: gormdb.CollectionSelection{Kind: gormdb.CollectionSelectionExplicit, Version: 1, Targets: []gormdb.CollectionSelectionTarget{{ID: "51", ExpectedVersion: 2}, {ID: "52", ExpectedVersion: 4}}}, expectedScope: &scope}
	recorder := callDocumentSelectionOperation(t, documentsSelectionTestService(fake, selection), documentExportRequest("documents-partial", gormdb.CollectionSelectionExplicit, 1, ""), identity)
	require.Equal(t, http.StatusMultiStatus, recorder.Code, recorder.Body.String())
	var response documentOperationResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "partial", response.OperationState)
	assert.Nil(t, response.Artifact)
	assert.Equal(t, []documentOperationItemResult{{TargetID: 51, Outcome: "committed", ObservedVersion: &allowed.Version}, {TargetID: 52, Outcome: "conflict"}}, response.ItemResults)
	assert.NotContains(t, recorder.Body.String(), allowed.Content)
}
