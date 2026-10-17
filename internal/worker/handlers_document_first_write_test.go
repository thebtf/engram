package worker

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/worker/sse"
)

func TestDocumentFirstWriteHTTPPostgres(t *testing.T) {
	store := openWorkerUCIContextCompositionStore(t)
	db := store.GetDB()
	var migrationCount int64
	require.NoError(t, db.Table("migrations").Where("id IN ?", []string{"186_project_consolidation", "187_logical_source_project"}).Count(&migrationCount).Error)
	require.EqualValues(t, 2, migrationCount, "requires the actual migration186/187 chain")
	var guardMode, replicationRole string
	require.NoError(t, db.Raw(`SELECT t.tgenabled::text FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
		JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema()
		AND c.relname='versioned_documents' AND t.tgname='engram_consolidation_scope_write' AND NOT t.tgisinternal`).Scan(&guardMode).Error)
	require.Contains(t, []string{"O", "A"}, guardMode, "mandatory SQL write guard must fire for ordinary writes")
	require.NoError(t, db.Raw("SHOW session_replication_role").Scan(&replicationRole).Error)
	require.Equal(t, "origin", replicationRole)

	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	token := rand.Text()
	tokenAuth, err := NewTokenAuth(token)
	require.NoError(t, err)
	require.True(t, tokenAuth.IsEnabled())
	service := &Service{
		router:         chi.NewRouter(),
		tokenAuth:      tokenAuth,
		mcpHealth:      mcp.NewMCPHealth(),
		sseBroadcaster: sse.NewBroadcaster(),
		store:          store,
		vault:          newTestVault(t),
		documentStore:  gormdb.NewVersionedDocumentStore(store),
		sessionStore:   gormdb.NewSessionStore(store),
	}
	service.ready.Store(true)
	service.setupMiddleware()
	service.setupRoutes()
	server := httptest.NewServer(service.router)
	t.Cleanup(server.Close)
	call := func(t *testing.T, method, path string, body any, status int, result any) {
		t.Helper()
		var payload []byte
		if body != nil {
			var err error
			payload, err = json.Marshal(body)
			require.NoError(t, err)
		}
		request, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, bytes.NewReader(payload))
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, status, response.StatusCode, "%s %s: %s", method, path, raw)
		if result != nil {
			require.NoError(t, json.Unmarshal(raw, result))
		}
	}

	project := uuid.NewString()
	const path = "decisions/document-first-write.md"
	const content = "# First decision\n\nПринято: exact first-write bytes.\n"
	const author = "document-first-write-test"
	const metadata = `{"fixture":"document-first-write"}`
	digest := sha256.Sum256([]byte(content))
	contentHash := hex.EncodeToString(digest[:])
	var projectCount, documentCount int64
	require.NoError(t, db.Model(&gormdb.Project{}).Where("id = ?", project).Count(&projectCount).Error)
	require.Zero(t, projectCount, "never pre-register the happy-path UUID")
	require.NoError(t, db.Model(&gormdb.VersionedDocument{}).Where("project = ?", project).Count(&documentCount).Error)
	require.Zero(t, documentCount)
	unrelatedCounts := make(map[string]int64)
	for _, table := range []string{"memories", "sources"} {
		var n int64
		require.NoError(t, db.Table(table).Count(&n).Error)
		unrelatedCounts[table] = n
	}
	var before projectListResponse
	call(t, http.MethodGet, "/api/projects", nil, http.StatusOK, &before)
	for _, item := range before.Items {
		require.NotEqual(t, project, item.ID)
	}
	call(t, http.MethodGet, "/api/documents?project="+project, nil, http.StatusForbidden, nil)
	require.NoError(t, db.Model(&gormdb.Project{}).Where("id = ?", project).Count(&projectCount).Error)
	require.Zero(t, projectCount, "ordinary readers must not register the UUID")

	var created struct {
		ID      int64  `json:"id"`
		Path    string `json:"path"`
		Project string `json:"project"`
	}
	call(t, http.MethodPost, "/api/documents", documentCreateRequest{
		Path: path, Project: project, Content: content, DocType: "markdown", Metadata: metadata, Author: author,
	}, http.StatusCreated, &created)
	require.Positive(t, created.ID)
	require.Equal(t, path, created.Path)
	require.Equal(t, project, created.Project)
	var persisted gormdb.VersionedDocument
	require.NoError(t, db.First(&persisted, created.ID).Error)
	require.Equal(t, project, persisted.Project)
	require.Equal(t, path, persisted.Path)
	require.Equal(t, 1, persisted.Version)
	require.Equal(t, content, persisted.Content)
	require.Equal(t, contentHash, persisted.ContentHash)

	t.Run("ordinary_list_reopens_first_document", func(t *testing.T) {
		var page documentListResponse
		call(t, http.MethodGet, "/api/documents?project="+project, nil, http.StatusOK, &page)
		require.EqualValues(t, 1, page.Total)
		require.Len(t, page.Items, 1)
		require.Equal(t, created.ID, page.Items[0].ID)
		require.Equal(t, project, page.Items[0].Project)
		require.Equal(t, path, page.Items[0].Path)
		require.Equal(t, 1, page.Items[0].Version)
	})
	query := url.Values{"project": {project}, "path": {path}}.Encode()
	for _, suffix := range []string{"", "&version=1"} {
		t.Run("ordinary_read"+suffix, func(t *testing.T) {
			var read documentReadResponse
			call(t, http.MethodGet, "/api/documents/read?"+query+suffix, nil, http.StatusOK, &read)
			require.Equal(t, created.ID, read.ID)
			require.Equal(t, project, read.Project)
			require.Equal(t, path, read.Path)
			require.Equal(t, 1, read.Version)
			require.Equal(t, content, read.Content)
			require.Equal(t, contentHash, read.ContentHash)
			require.Equal(t, "markdown", read.DocType)
			require.Equal(t, author, read.Author)
			require.JSONEq(t, metadata, read.Metadata)
		})
	}
	t.Run("ordinary_history_reopens_exact_version", func(t *testing.T) {
		var history documentHistoryResponse
		call(t, http.MethodGet, "/api/documents/history?"+query, nil, http.StatusOK, &history)
		require.Equal(t, path, history.Path)
		require.Equal(t, project, history.Project)
		require.Equal(t, 1, history.Count)
		require.Len(t, history.Versions, 1)
		require.Equal(t, created.ID, history.Versions[0].ID)
		require.Equal(t, 1, history.Versions[0].Version)
		require.Equal(t, contentHash, history.Versions[0].ContentHash)
		require.Equal(t, author, history.Versions[0].Author)
	})
	t.Run("ordinary_catalog_contains_exact_project", func(t *testing.T) {
		var catalog projectListResponse
		call(t, http.MethodGet, "/api/projects", nil, http.StatusOK, &catalog)
		require.Contains(t, catalog.Items, gormdb.ProjectDescriptor{ID: project, DisplayName: "Registered project"})
		require.Empty(t, catalog.Unresolved)
	})

	require.NoError(t, db.Model(&gormdb.Project{}).Where("id = ?", project).Count(&projectCount).Error)
	require.EqualValues(t, 1, projectCount)
	exists, err := service.sessionStore.CanonicalProjectExists(t.Context(), project)
	require.NoError(t, err)
	require.True(t, exists)
	for table, before := range unrelatedCounts {
		var after int64
		require.NoError(t, db.Table(table).Count(&after).Error)
		require.Equal(t, before, after, "document first write must not need a %s write", table)
	}
}
