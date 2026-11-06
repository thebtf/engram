package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRetainedDocumentVersionPreservesEffectiveProjectHTTP(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("dedicated PostgreSQL source fixture DSN is not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	require.True(t, cfg.Host == "127.0.0.1" || cfg.Host == "localhost" || cfg.Host == "::1")
	const prefix, suffix = "engram_migration186_scope_test_", "_source"
	require.True(t, strings.HasPrefix(cfg.Database, prefix) && strings.HasSuffix(cfg.Database, suffix) && len(cfg.Database) > len(prefix)+len(suffix))
	require.True(t, cfg.RuntimeParams["search_path"] == "" || cfg.RuntimeParams["search_path"] == "public")
	require.Empty(t, cfg.Fallbacks)
	backing := openWorkerUCIContextCompositionStore(t)
	db := backing.DB
	a, b, foreign := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{a, b, foreign} {
		require.NoError(t, db.Create(&gormdb.Project{ID: id}).Error)
	}
	var schema string
	require.NoError(t, db.Raw(`SELECT current_schema()`).Scan(&schema).Error)
	restoreDSN := os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN")
	if restoreDSN == "" {
		t.Skip("paired disposable restore fixture DSN is not set")
	}
	restoreConfig, err := pgx.ParseConfig(restoreDSN)
	require.NoError(t, err)
	require.Equal(t, cfg.Host, restoreConfig.Host)
	require.Equal(t, cfg.Port, restoreConfig.Port)
	require.Equal(t, strings.TrimSuffix(cfg.Database, "_source")+"_restore", restoreConfig.Database)
	require.True(t, restoreConfig.RuntimeParams["search_path"] == "" || restoreConfig.RuntimeParams["search_path"] == "public")
	require.Empty(t, restoreConfig.Fallbacks)
	adminDB, err := gormlib.Open(postgres.Open(restoreDSN), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	adminSQL, err := adminDB.DB()
	require.NoError(t, err)
	require.NoError(t, adminDB.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		assert.NoError(t, adminDB.Exec("DROP SCHEMA "+schema+" CASCADE").Error)
		assert.NoError(t, adminSQL.Close())
	})
	parsed, err := url.Parse(restoreDSN)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	target, err := gormdb.NewStore(gormdb.Config{DSN: parsed.String(), MaxConns: 2, LogLevel: logger.Silent})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, target.Close()) })
	// The consolidation stream executes a multi-statement temporary-table setup;
	// use its ordinary non-prepared fixture connection rather than the worker pool.
	restoreDB, err := gormlib.Open(postgres.Open(parsed.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	restoreSQL, err := restoreDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, restoreSQL.Close()) })
	docs := []gormdb.VersionedDocument{
		{Project: a, Path: "retained-membership.md", Version: 1, Content: "original retained body", ContentHash: "original-hash", DocType: "markdown", Author: "fixture"},
		{Project: a, Path: "conflicting-membership.md", Version: 1, Content: "conflicting body", ContentHash: "conflict-hash", DocType: "markdown", Author: "fixture"},
		{Project: a, Path: "unassigned-membership.md", Version: 1, Content: "unassigned body", ContentHash: "unassigned-hash", DocType: "markdown", Author: "fixture"},
		{Project: foreign, Path: "unrelated.md", Version: 1, Content: "unrelated body", ContentHash: "unrelated-hash", DocType: "markdown", Author: "fixture"},
		{Project: a, Path: "invalid-evidence.md", Version: 1, Content: "invalid evidence body", ContentHash: "invalid-hash", DocType: "markdown", Author: "fixture"},
	}
	require.NoError(t, db.Create(&docs[0]).Error)
	blocker := gormdb.Memory{Project: a, Content: "synthetic unassigned redirect blocker", Status: "active", PrivacyScope: "project", Version: 1}
	require.NoError(t, db.Create(&blocker).Error)
	vault := newTestVault(t)
	engine := gormdb.NewProjectConsolidationStore(db, vault, restoreDB)
	census, err := engine.Census(t.Context(), "synthetic-http-document-parent")
	require.NoError(t, err)
	var docRefs []string
	for _, row := range census.Rows {
		if row.Family == "versioned_documents" && row.Role == "project" {
			docRefs = append(docRefs, row.Reference)
		}
	}
	require.Len(t, docRefs, 1)
	plan, err := engine.Preflight(t.Context(), gormdb.ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint, Evidence: []gormdb.ConsolidationEvidence{{Reference: "urn:engram:fixture:http-marker", Kind: "portable_marker", Group: "urn:engram:fixture:http-group", ProjectID: b, Rows: docRefs}}})
	require.NoError(t, err)
	var backup bytes.Buffer
	require.NoError(t, engine.Backup(t.Context(), plan.Reference, plan.Fingerprint, &backup))
	backupRef, err := engine.VerifyBackup(t.Context(), plan.Reference, plan.Fingerprint, bytes.NewReader(backup.Bytes()))
	require.NoError(t, err)
	_, err = engine.Apply(t.Context(), gormdb.ConsolidationApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, BackupReference: backupRef, IdempotencyKey: uuid.NewString()})
	require.NoError(t, err)
	backing, db = target, target.DB
	var mapped gormdb.VersionedDocument
	require.NoError(t, db.First(&mapped, docs[0].ID).Error)
	require.Equal(t, b, mapped.Project)
	// Test-only post-Apply restoration models the reported A-native/B-membership shape.
	require.NoError(t, db.Model(&gormdb.VersionedDocument{}).Where("id = ?", docs[0].ID).Update("project", a).Error)
	var redirects int64
	require.NoError(t, db.Table("project_consolidation_redirects").Where("selector IN ? OR project_id IN ?", []string{a, b}, []string{a, b}).Count(&redirects).Error)
	require.Zero(t, redirects, "the fixture must not rely on an A/B redirect")
	for i := 1; i < len(docs); i++ {
		require.NoError(t, db.Create(&docs[i]).Error)
	}
	member := func(id int64, selected *string) {
		t.Helper()
		require.NoError(t, db.Create(&gormdb.ConsolidationMembership{Reference: "urn:fixture:document-membership:" + uuid.NewString(), Table: "versioned_documents", RowKey: gormdb.JSONRaw(fmt.Sprintf(`{"id":%d}`, id)), FieldPath: gormdb.JSONRaw(`["project"]`), ProjectID: selected}).Error)
	}
	member(docs[1].ID, &b)
	member(docs[1].ID, &foreign)
	member(docs[2].ID, nil)
	member(docs[4].ID, &b)
	rowJSON := func(id int64) string {
		t.Helper()
		var value string
		require.NoError(t, db.Raw(`SELECT row_to_json(d)::text FROM versioned_documents d WHERE id = ?`, id).Row().Scan(&value))
		return value
	}
	before := make([]string, len(docs))
	for i := range docs {
		before[i] = rowJSON(docs[i].ID)
	}
	svc := &Service{store: backing, sessionStore: gormdb.NewSessionStore(backing), documentStore: gormdb.NewVersionedDocumentStoreWithVault(backing, vault)}
	router := chi.NewRouter()
	router.Get("/api/documents", svc.handleListDocuments)
	router.Post("/api/documents", svc.handleCreateDocument)
	router.Get("/api/documents/read", svc.handleReadDocument)
	router.Get("/api/documents/history", svc.handleDocumentHistory)
	admin := auth.SessionForBrowserUser("admin", 41)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		req = req.WithContext(auth.WithIdentity(req.Context(), admin))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	readURL := func(project string, id int64, path string) string {
		return fmt.Sprintf("/api/documents/read?project=%s&path=%s&document_id=%d", project, path, id)
	}
	rec := call(http.MethodGet, readURL(b, docs[0].ID, docs[0].Path), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "original retained body")
	rec = call(http.MethodGet, readURL(a, docs[0].ID, docs[0].Path), nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	write := func(project string, doc gormdb.VersionedDocument, expected int) *httptest.ResponseRecorder {
		return call(http.MethodPost, "/api/documents", map[string]any{"project": project, "path": doc.Path, "content": "new retained body", "document_id": doc.ID, "expected_version": expected})
	}
	for _, tc := range []struct {
		project         string
		doc             gormdb.VersionedDocument
		version, status int
	}{
		{b, docs[0], 2, http.StatusConflict},
		{a, docs[0], 1, http.StatusNotFound},
		{foreign, docs[0], 1, http.StatusNotFound},
		{b, docs[1], 1, http.StatusNotFound},
		{b, docs[2], 1, http.StatusNotFound},
		{b, docs[4], 1, http.StatusConflict},
	} {
		rec = write(tc.project, tc.doc, tc.version)
		require.Equal(t, tc.status, rec.Code, rec.Body.String())
	}
	rec = write(b, docs[4], 1)
	require.JSONEq(t, `{"error":"DOCUMENT_PROVENANCE_INVALID"}`, rec.Body.String())
	svc.documentStore = gormdb.NewVersionedDocumentStore(backing)
	rec = write(b, docs[0], 1)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"error":"DOCUMENT_PROVENANCE_UNAVAILABLE"}`, rec.Body.String())
	svc.documentStore = gormdb.NewVersionedDocumentStoreWithVault(backing, vault)
	mcpRefusal := func(doc gormdb.VersionedDocument, store *gormdb.VersionedDocumentStore, code string) {
		t.Helper()
		server := mcp.NewServer(mcp.ServerOptions{Version: "synthetic-document-membership"})
		server.SetVersionedDocumentStore(store)
		params, marshalErr := json.Marshal(map[string]any{"name": "docs", "arguments": map[string]any{"action": "create", "project": b, "path": doc.Path, "content": "must not insert", "document_id": doc.ID, "expected_version": 1}})
		require.NoError(t, marshalErr)
		response := server.HandleRequest(auth.WithIdentity(t.Context(), admin), &mcp.Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})
		require.NotNil(t, response)
		require.NotNil(t, response.Error)
		require.Equal(t, -32000, response.Error.Code)
		require.Contains(t, response.Error.Message, code)
	}
	mcpRefusal(docs[4], gormdb.NewVersionedDocumentStoreWithVault(backing, vault), "DOCUMENT_PROVENANCE_INVALID")
	mcpRefusal(docs[0], gormdb.NewVersionedDocumentStore(backing), "DOCUMENT_PROVENANCE_UNAVAILABLE")
	var count int64
	require.NoError(t, db.Model(&gormdb.VersionedDocument{}).Count(&count).Error)
	require.EqualValues(t, len(docs), count, "rejected writes must not insert a version")
	for i := range docs {
		require.Equal(t, before[i], rowJSON(docs[i].ID), "rejected writes must preserve existing rows")
	}
	rec = write(b, docs[0], 1)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.Positive(t, created.ID)
	var stored gormdb.VersionedDocument
	require.NoError(t, db.First(&stored, created.ID).Error)
	assert.Equal(t, a, stored.Project, "the native lineage must remain unchanged")
	assert.Equal(t, 2, stored.Version)
	assert.Equal(t, "new retained body", stored.Content)
	rec = call(http.MethodGet, "/api/documents?project="+b, nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), fmt.Sprintf(`"id":%d`, created.ID), "effective project B must list the new version")
	rec = call(http.MethodGet, readURL(b, created.ID, docs[0].Path), nil)
	assert.Equal(t, http.StatusOK, rec.Code, "effective project B must read the new version: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "new retained body")
	rec = call(http.MethodGet, fmt.Sprintf("/api/documents/history?project=%s&path=%s&document_id=%d", b, docs[0].Path, docs[0].ID), nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "original-hash", "original history remains visible")
	assert.Contains(t, rec.Body.String(), fmt.Sprintf(`"id":%d`, created.ID), "effective project B must see the new version in history")
	rec = call(http.MethodGet, readURL(a, created.ID, docs[0].Path), nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, "native project A must not gain access: %s", rec.Body.String())
	rec = call(http.MethodGet, "/api/documents?project="+a, nil)
	assert.NotContains(t, rec.Body.String(), fmt.Sprintf(`"id":%d`, created.ID), "native project A must not list the new version")
	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "synthetic-document-membership"})
	mcpServer.SetVersionedDocumentStore(gormdb.NewVersionedDocumentStoreWithVault(backing, vault))
	params, err := json.Marshal(map[string]any{"name": "docs", "arguments": map[string]any{"action": "create", "project": b, "path": docs[0].Path, "content": "mcp retained version", "document_id": created.ID, "expected_version": 2}})
	require.NoError(t, err)
	ctx := auth.WithIdentity(t.Context(), admin)
	response := mcpServer.HandleRequest(ctx, &mcp.Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})
	require.NotNil(t, response)
	require.Nil(t, response.Error)
	var newest gormdb.VersionedDocument
	require.NoError(t, db.Where("path = ? AND version = ?", docs[0].Path, 3).Take(&newest).Error)
	assert.Equal(t, a, newest.Project)
	rec = call(http.MethodGet, readURL(b, newest.ID, docs[0].Path), nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "mcp retained version")
	rec = call(http.MethodGet, readURL(a, newest.ID, docs[0].Path), nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	for i := range docs {
		assert.Equal(t, before[i], rowJSON(docs[i].ID), "version write must preserve prior and unrelated rows")
	}
}
