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

func TestPostApplyRetainedDocumentPathOnlyPostgres(t *testing.T) {
	source := reviewDeltaStore(t)
	sourceCfg, err := pgx.ParseConfig(os.Getenv("DATABASE_DSN"))
	require.NoError(t, err)
	restoreDSN := os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN")
	if restoreDSN == "" {
		t.Skip("paired disposable restore fixture required")
	}
	restoreCfg, err := pgx.ParseConfig(restoreDSN)
	require.NoError(t, err)
	require.Equal(t, sourceCfg.Host, restoreCfg.Host)
	require.Equal(t, sourceCfg.Port, restoreCfg.Port)
	require.Equal(t, strings.TrimSuffix(sourceCfg.Database, "_source")+"_restore", restoreCfg.Database)
	require.True(t, restoreCfg.RuntimeParams["search_path"] == "" || restoreCfg.RuntimeParams["search_path"] == "public")
	require.Empty(t, restoreCfg.Fallbacks)
	var schema string
	require.NoError(t, source.DB.Raw(`SELECT current_schema()`).Scan(&schema).Error)
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
	restoreDB, err := gormlib.Open(postgres.Open(parsed.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	restoreSQL, err := restoreDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, restoreSQL.Close()) })
	a, b := uuid.NewString(), uuid.NewString()
	for _, id := range []string{a, b} {
		require.NoError(t, source.DB.Create(&gormdb.Project{ID: id}).Error)
	}
	docs := []gormdb.VersionedDocument{
		{Project: a, Path: "post-apply-http.md", Content: "retained http", ContentHash: "http-hash", DocType: "markdown", Author: "fixture", Version: 1},
		{Project: a, Path: "post-apply-mcp.md", Content: "retained mcp", ContentHash: "mcp-hash", DocType: "markdown", Author: "fixture", Version: 1},
		{Project: a, Path: "post-apply-explicit.md", Content: "retained explicit", ContentHash: "explicit-hash", DocType: "markdown", Author: "fixture", Version: 1},
	}
	for i := range docs {
		require.NoError(t, source.DB.Create(&docs[i]).Error)
	}
	vault := newTestVault(t)
	engine := gormdb.NewProjectConsolidationStore(source.DB, vault, restoreDB)
	census, err := engine.Census(t.Context(), "synthetic-document-path-only")
	require.NoError(t, err)
	var refs []string
	for _, row := range census.Rows {
		if row.Family == "versioned_documents" && row.Role == "project" {
			refs = append(refs, row.Reference)
		}
	}
	require.Len(t, refs, len(docs))
	plan, err := engine.Preflight(t.Context(), gormdb.ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint, Evidence: []gormdb.ConsolidationEvidence{{Reference: "urn:engram:fixture:path-only", Kind: "portable_marker", Group: "urn:engram:fixture:path-only-group", ProjectID: b, Rows: refs}}})
	require.NoError(t, err)
	var backup bytes.Buffer
	require.NoError(t, engine.Backup(t.Context(), plan.Reference, plan.Fingerprint, &backup))
	backupRef, err := engine.VerifyBackup(t.Context(), plan.Reference, plan.Fingerprint, bytes.NewReader(backup.Bytes()))
	require.NoError(t, err)
	_, err = engine.Apply(t.Context(), gormdb.ConsolidationApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, BackupReference: backupRef, IdempotencyKey: uuid.NewString()})
	require.NoError(t, err)
	db := target.DB
	for _, doc := range docs {
		var row gormdb.VersionedDocument
		require.NoError(t, db.First(&row, doc.ID).Error)
		require.Equal(t, b, row.Project)
		var memberships int64
		require.NoError(t, db.Model(&gormdb.ConsolidationMembership{}).Where("table_name = ? AND row_key->>'id' = ? AND project_id = ?", "versioned_documents", fmt.Sprint(doc.ID), b).Count(&memberships).Error)
		require.EqualValues(t, 1, memberships)
	}
	svc := &Service{store: target, sessionStore: gormdb.NewSessionStore(target), documentStore: gormdb.NewVersionedDocumentStoreWithVault(target, vault)}
	router := chi.NewRouter()
	router.Post("/api/documents", svc.handleCreateDocument)
	admin := auth.SessionForBrowserUser("admin", 41)
	httpCreate := func(body any) *httptest.ResponseRecorder {
		t.Helper()
		data, e := json.Marshal(body)
		require.NoError(t, e)
		req := httptest.NewRequest(http.MethodPost, "/api/documents", bytes.NewReader(data)).WithContext(auth.WithIdentity(t.Context(), admin))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "synthetic-post-apply"})
	mcpServer.SetVersionedDocumentStore(gormdb.NewVersionedDocumentStoreWithVault(target, vault))
	counts := func(path string) (int64, int64) {
		t.Helper()
		var versions, members int64
		require.NoError(t, db.Model(&gormdb.VersionedDocument{}).Where("path = ?", path).Count(&versions).Error)
		require.NoError(t, db.Table("project_consolidation_memberships m").Joins("JOIN versioned_documents d ON m.row_key->>'id' = CAST(d.id AS text)").Where("m.table_name = ? AND d.path = ?", "versioned_documents", path).Count(&members).Error)
		return versions, members
	}
	t.Run("http_path_only_refused", func(t *testing.T) {
		v, m := counts(docs[0].Path)
		rec := httpCreate(map[string]any{"project": b, "path": docs[0].Path, "content": "must not insert"})
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "document lineage or version conflict")
		assert.Contains(t, rec.Body.String(), "document_id and expected_version")
		t.Logf("HTTP refusal status=%d body=%s", rec.Code, rec.Body.String())
		v2, m2 := counts(docs[0].Path)
		assert.Equal(t, v, v2)
		assert.Equal(t, m, m2)
	})
	t.Run("mcp_path_only_refused", func(t *testing.T) {
		v, m := counts(docs[1].Path)
		params, e := json.Marshal(map[string]any{"name": "docs", "arguments": map[string]any{"action": "create", "project": b, "path": docs[1].Path, "content": "must not insert"}})
		require.NoError(t, e)
		response := mcpServer.HandleRequest(auth.WithIdentity(t.Context(), admin), &mcp.Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})
		require.NotNil(t, response)
		if assert.NotNil(t, response.Error) {
			assert.Equal(t, -32000, response.Error.Code)
			assert.Contains(t, response.Error.Message, "document lineage or version conflict")
			assert.Contains(t, response.Error.Message, "document_id and expected_version")
			t.Logf("MCP refusal code=%d message=%s", response.Error.Code, response.Error.Message)
		}
		v2, m2 := counts(docs[1].Path)
		assert.Equal(t, v, v2)
		assert.Equal(t, m, m2)
	})
	t.Run("ordinary_path_only_and_explicit_lineage", func(t *testing.T) {
		fresh := httpCreate(map[string]any{"project": b, "path": "fresh.md", "content": "fresh"})
		require.Equal(t, http.StatusCreated, fresh.Code, fresh.Body.String())
		ordinary := httpCreate(map[string]any{"project": b, "path": "fresh.md", "content": "ordinary update"})
		require.Equal(t, http.StatusCreated, ordinary.Code, ordinary.Body.String())
		explicit := httpCreate(map[string]any{"project": b, "path": docs[2].Path, "content": "authenticated successor", "document_id": docs[2].ID, "expected_version": 1})
		require.Equal(t, http.StatusCreated, explicit.Code, explicit.Body.String())
		v, m := counts(docs[2].Path)
		assert.EqualValues(t, 2, v)
		var successor gormdb.VersionedDocument
		require.NoError(t, db.Where("path = ? AND version = ?", docs[2].Path, 2).Take(&successor).Error)
		assert.Equal(t, b, successor.Project)
		assert.Equal(t, "authenticated successor", successor.Content)
		t.Logf("explicit successor project=%s version=%d, versions=%d memberships=%d", successor.Project, successor.Version, v, m)
		assert.EqualValues(t, 2, m)
	})
}
