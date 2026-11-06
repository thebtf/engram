package worker

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"golang.org/x/crypto/bcrypt"
	gormlib "gorm.io/gorm"
)

type blockingDocumentResponse struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
}

func (w *blockingDocumentResponse) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return w.ResponseRecorder.Write(p)
}

func TestProjectAliasAttachHTTP(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("migration186 PostgreSQL fixture DSN is not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid migration186 fixture DSN")
	}
	require.True(t, cfg.Host == "127.0.0.1" || cfg.Host == "localhost" || cfg.Host == "::1", "fixture requires a loopback PostgreSQL node")
	const prefix, suffix = "engram_migration186_scope_test_", "_source"
	require.True(t, strings.HasPrefix(cfg.Database, prefix) && strings.HasSuffix(cfg.Database, suffix) && len(cfg.Database) > len(prefix)+len(suffix), "refusing a database outside the dedicated source fixture namespace")
	require.True(t, cfg.RuntimeParams["search_path"] == "" || cfg.RuntimeParams["search_path"] == "public", "fixture requires the public schema")
	require.Empty(t, cfg.Fallbacks, "fixture DSN must not fall back to another PostgreSQL target")
	backing := openWorkerUCIContextCompositionStore(t)
	db := backing.DB
	target, a, b, foreign := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	invalidA, invalidB := uuid.NewString(), uuid.NewString()
	secondTarget, secondSource := uuid.NewString(), uuid.NewString()
	for _, id := range []string{target, a, b, foreign, invalidA, invalidB, secondTarget, secondSource, "synthetic-nonuuid-target"} {
		require.NoError(t, db.Create(&gormdb.Project{ID: id, DisplayName: sql.NullString{String: "Synthetic Same Name", Valid: true}}).Error)
	}
	records := []gormdb.Memory{
		{Project: target, Content: "alias-http current", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: a, Content: "alias-http source-a", Status: "active", PrivacyScope: "project", Version: 2},
		{Project: b, Content: "alias-http source-b", Status: "active", PrivacyScope: "project", Version: 3},
		{Project: foreign, Content: "alias-http unrelated", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: a, Content: "alias-http hidden-private", Status: "active", PrivacyScope: "private", SourceWorkstationID: "other-ws", Version: 1},
		{Project: a, Content: "alias-http conflict", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: a, Content: "alias-http unassigned", Status: "active", PrivacyScope: "project", Version: 1},
	}
	for i := range records {
		require.NoError(t, db.Create(&records[i]).Error)
	}
	secondMemory := gormdb.Memory{Project: secondSource, Content: "independent synthetic group", Status: "active", PrivacyScope: "project", Version: 1}
	require.NoError(t, db.Create(&secondMemory).Error)
	for i, entry := range []struct {
		row     int
		project *string
	}{{5, &a}, {5, &foreign}, {6, nil}} {
		require.NoError(t, db.Create(&gormdb.ConsolidationMembership{Reference: fmt.Sprintf("urn:engram:fixture:alias:control:%d", i), Table: "memories", RowKey: gormdb.JSONRaw(fmt.Sprintf(`{"id":%d}`, records[entry.row].ID)), FieldPath: gormdb.JSONRaw(`["project"]`), ProjectID: entry.project, PlanFingerprint: "synthetic-alias-control"}).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', ?, 'synthetic-invalid-self')`, foreign, foreign).Error)
	for _, id := range []string{invalidA, invalidB} {
		require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', 'synthetic-nonuuid-target', 'synthetic-invalid-target')`, id).Error)
	}
	invalidMemory := gormdb.Memory{Project: invalidB, Content: "invalid-group-foreign", Status: "active", PrivacyScope: "project", Version: 1}
	require.NoError(t, db.Create(&invalidMemory).Error)
	docs := []gormdb.VersionedDocument{}
	for i, id := range []string{target, a, b, foreign} {
		doc := gormdb.VersionedDocument{Project: id, Path: fmt.Sprintf("alias-doc-%d.md", i), Content: "synthetic document", ContentHash: fmt.Sprintf("hash-%d", i), DocType: "markdown", Author: "synthetic", Version: 1}
		require.NoError(t, db.Create(&doc).Error)
		docs = append(docs, doc)
	}
	for _, doc := range []gormdb.VersionedDocument{
		{Project: target, Path: "shared.md", Content: "target version one", ContentHash: "target-hash", DocType: "markdown", Author: "synthetic", Version: 1},
		{Project: a, Path: "shared.md", Content: "source version one", ContentHash: "source-hash-1", DocType: "markdown", Author: "synthetic", Version: 1},
		{Project: a, Path: "shared.md", Content: "source version two", ContentHash: "source-hash-2", DocType: "markdown", Author: "synthetic", Version: 2},
	} {
		require.NoError(t, db.Create(&doc).Error)
		docs = append(docs, doc)
	}
	issues := gormdb.NewIssueStore(db)
	issueID, err := issues.CreateIssue(t.Context(), &gormdb.Issue{Title: "synthetic alias issue", Body: "synthetic issue body", SourceProject: a, TargetProject: a, SourceAgent: "synthetic", Labels: []string{}})
	require.NoError(t, err)
	snapshot := func(table string, id int64) string {
		t.Helper()
		var out string
		require.NoError(t, db.Raw("SELECT row_to_json(x)::text FROM "+table+" x WHERE id = ?", id).Row().Scan(&out))
		return out
	}
	before := make([]string, len(records))
	for i := range records {
		before[i] = snapshot("memories", records[i].ID)
	}
	beforeDocs := make([]string, len(docs))
	for i := range docs {
		beforeDocs[i] = snapshot("versioned_documents", docs[i].ID)
	}
	service := &Service{store: backing, sessionStore: gormdb.NewSessionStore(backing), memoryStore: gormdb.NewMemoryStore(backing), issueStore: gormdb.NewIssueStore(db), documentStore: gormdb.NewVersionedDocumentStore(backing), vault: newTestVault(t)}
	service.documentSelectionStore = gormdb.NewCollectionSelectionStore(db)
	router := chi.NewRouter()
	base := "/api/projects/consolidation/aliases"
	router.Post(base+"/prepare", service.handleProjectAliasPrepare)
	router.Post(base+"/apply", service.handleProjectAliasApply)
	router.Get(base+"/status", service.handleProjectAliasStatus)
	router.Post(base+"/rollback", service.handleProjectAliasRollback)
	router.Get("/api/projects", service.handleGetProjects)
	router.Get("/api/projects/resolve", service.handleResolveLogicalProject)
	router.Get("/api/memories", service.handleListMemories)
	router.Get("/api/memories/{id}", service.handleGetMemoryByID)
	router.Get("/api/documents", service.handleListDocuments)
	router.Post("/api/documents", service.handleCreateDocument)
	router.Get("/api/documents/read", service.handleReadDocument)
	router.Get("/api/documents/history", service.handleDocumentHistory)
	router.Post("/api/documents/comment", service.handleAddDocumentComment)
	router.Get("/api/documents/comments", service.handleListDocumentComments)
	router.Post("/api/documents/selection", service.handleDocumentSelectionSnapshot)
	router.Post("/api/documents/selection/current", service.handleDocumentSelectionCurrent)
	router.Get("/api/documents/exports/{artifactID}", service.handleDownloadDocumentExport)
	router.Get("/api/issues", service.handleListIssues)
	router.Get("/api/issues/{id}", service.handleGetIssue)
	call := func(method, path string, body any, identity *auth.Identity) *httptest.ResponseRecorder {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, err = json.Marshal(body)
			require.NoError(t, err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		req.Header.Set(engramRequestIDHeader, "synthetic-request")
		req.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: documentSelectionTestSession})
		if identity != nil {
			req = req.WithContext(auth.WithIdentity(req.Context(), *identity))
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	admin := auth.SessionForBrowserUser("admin", 41)
	master := auth.Admin()
	nonAdmin := auth.Client("read-write", uuid.NewString())
	req := gormdb.ProjectAliasAttachRequest{Target: target, Aliases: []gormdb.ProjectAliasExpected{{Source: a}, {Source: b}}, Reason: "synthetic explicit decision"}
	unknown := call(http.MethodGet, base+"/status?idempotency_key="+uuid.NewString(), nil, &admin)
	require.Equal(t, http.StatusNotFound, unknown.Code, unknown.Body.String())
	require.Contains(t, unknown.Body.String(), "PROJECT_ALIAS_NOT_RECORDED")
	require.Contains(t, unknown.Body.String(), "may still be in flight")
	require.NotContains(t, unknown.Body.String(), "isolated")
	identityRequest := httptest.NewRequest(http.MethodGet, base+"/status", nil)
	identityRequest = identityRequest.WithContext(auth.WithIdentity(identityRequest.Context(), auth.AuthDisabled()))
	_, disabledActor := service.projectAliasStore(httptest.NewRecorder(), identityRequest)
	require.Equal(t, "auth-disabled:admin", disabledActor)
	require.Equal(t, http.StatusForbidden, call(http.MethodPost, base+"/prepare", req, &nonAdmin).Code)
	prepare := func(r gormdb.ProjectAliasAttachRequest) gormdb.ProjectAliasPlan {
		t.Helper()
		rec := call(http.MethodPost, base+"/prepare", r, &admin)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var plan gormdb.ProjectAliasPlan
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &plan))
		return plan
	}
	stalePlan := prepare(req)
	require.NoError(t, db.Model(&gormdb.Project{}).Where("id = ?", target).Update("display_name", "Changed identity label").Error)
	staleApply := gormdb.ProjectAliasApplyRequest{PlanReference: stalePlan.Reference, PlanFingerprint: stalePlan.Fingerprint, IdempotencyKey: uuid.NewString()}
	require.Equal(t, http.StatusConflict, call(http.MethodPost, base+"/apply", staleApply, &admin).Code)
	plan := prepare(req)
	apply := gormdb.ProjectAliasApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, IdempotencyKey: uuid.NewString()}
	require.Equal(t, http.StatusForbidden, call(http.MethodPost, base+"/apply", apply, &nonAdmin).Code)
	rec := call(http.MethodPost, base+"/apply", apply, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = call(http.MethodPost, base+"/apply", apply, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = call(http.MethodGet, base+"/status?idempotency_key="+apply.IdempotencyKey, nil, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "applied")
	var audit int64
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Where("action = ?", "project_alias_attach").Count(&audit).Error)
	require.EqualValues(t, 1, audit)
	var auditActor string
	require.NoError(t, db.Model(&gormdb.AuditLogEntry{}).Select("actor").Where("action = ?", "project_alias_attach").Scan(&auditActor).Error)
	require.Equal(t, "session:human:browser-user/41", auditActor)
	secondRequest := gormdb.ProjectAliasAttachRequest{Target: secondTarget, Aliases: []gormdb.ProjectAliasExpected{{Source: secondSource}}, Reason: "separate synthetic decision"}
	secondPlan := prepare(secondRequest)
	secondApply := gormdb.ProjectAliasApplyRequest{PlanReference: secondPlan.Reference, PlanFingerprint: secondPlan.Fingerprint, IdempotencyKey: apply.IdempotencyKey}
	rec = call(http.MethodPost, base+"/apply", secondApply, &master)
	require.Equal(t, http.StatusConflict, rec.Code, "a key cannot be reused for a different plan")
	secondApply.IdempotencyKey = uuid.NewString()
	rec = call(http.MethodPost, base+"/apply", secondApply, &master)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "master:admin")
	rec = call(http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", secondMemory.ID, secondTarget), nil, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = call(http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", secondMemory.ID, target), nil, &admin)
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = call(http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", records[1].ID, secondTarget), nil, &admin)
	require.Equal(t, http.StatusNotFound, rec.Code)
	wrong := req
	wrong.Target = foreign
	require.Equal(t, http.StatusConflict, call(http.MethodPost, base+"/prepare", wrong, &admin).Code)
	cycle := gormdb.ProjectAliasAttachRequest{Target: a, Aliases: []gormdb.ProjectAliasExpected{{Source: target}}, Reason: "synthetic cycle"}
	require.Equal(t, http.StatusConflict, call(http.MethodPost, base+"/prepare", cycle, &admin).Code)
	rec = call(http.MethodGet, "/api/projects", nil, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var catalog projectListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &catalog))
	require.Len(t, catalog.LogicalGroups, 2)
	groups := map[string][]string{}
	for _, group := range catalog.LogicalGroups {
		groups[group.PrimaryProjectID] = group.AliasProjectIDs
	}
	require.ElementsMatch(t, []string{a, b}, groups[target])
	require.ElementsMatch(t, []string{secondSource}, groups[secondTarget])
	var hasForeign bool
	for _, item := range catalog.Items {
		if item.ID == foreign {
			hasForeign = true
		}
	}
	require.True(t, hasForeign, "an invalid group must not hide its catalog descriptor")
	rec = call(http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", invalidMemory.ID, invalidA), nil, &admin)
	require.Equal(t, http.StatusNotFound, rec.Code, "a non-UUID target must not join otherwise distinct projects")
	for _, project := range []string{target, a, b} {
		rec = call(http.MethodGet, "/api/issues?project="+project, nil, &admin)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "synthetic alias issue")
		rec = call(http.MethodGet, fmt.Sprintf("/api/issues/%d?project=%s", issueID, project), nil, &admin)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "synthetic issue body")
	}
	rec = call(http.MethodGet, "/api/issues?project="+foreign, nil, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "synthetic alias issue")
	for _, project := range []string{target, a, b} {
		for _, family := range []string{"memories", "documents"} {
			var collected []string
			total := 3
			if family == "documents" {
				total = 5
			}
			for offset := 0; offset < total; offset += 2 {
				rec = call(http.MethodGet, fmt.Sprintf("/api/%s?project=%s&limit=2&offset=%d", family, project, offset), nil, &admin)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				var page struct {
					Total int `json:"total"`
					Items []struct {
						ID      int64  `json:"id"`
						Project string `json:"project"`
						Path    string `json:"path"`
					} `json:"items"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
				require.Equal(t, total, page.Total)
				for _, item := range page.Items {
					require.Equal(t, project, item.Project)
					if family == "memories" {
						collected = append(collected, fmt.Sprint(item.ID))
					} else {
						collected = append(collected, item.Path)
					}
				}
			}
			var want []string
			if family == "memories" {
				for i := 0; i < 3; i++ {
					want = append(want, fmt.Sprint(records[i].ID))
				}
			} else {
				for i := 0; i < 3; i++ {
					want = append(want, docs[i].Path)
				}
				want = append(want, "shared.md", "shared.md")
			}
			sort.Strings(collected)
			sort.Strings(want)
			require.Equal(t, want, collected)
		}
		for i := 0; i < 3; i++ {
			rec = call(http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", records[i].ID, project), nil, &admin)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), records[i].Content)
		}
	}
	for _, tc := range []struct {
		project           string
		documentID        int64
		version, expected string
	}{
		{target, docs[4].ID, "1", "target version one"},
		{target, docs[6].ID, "1", "source version one"},
		{a, docs[6].ID, "2", "source version two"},
		{b, docs[4].ID, "1", "target version one"},
	} {
		readURL := fmt.Sprintf("/api/documents/read?project=%s&path=shared.md&document_id=%d&version=%s", tc.project, tc.documentID, tc.version)
		rec = call(http.MethodGet, readURL, nil, &admin)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), tc.expected)
		require.Contains(t, rec.Body.String(), `"project":"`+tc.project+`"`)
	}
	rec = call(http.MethodGet, "/api/documents/read?project="+target+"&path=shared.md", nil, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "target version one")
	rec = call(http.MethodGet, "/api/documents/read?project="+a+"&path=shared.md", nil, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "source version two")
	rec = call(http.MethodGet, "/api/documents/read?project="+b+"&path=shared.md", nil, &admin)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	rec = call(http.MethodGet, fmt.Sprintf("/api/documents/read?project=%s&path=shared.md&document_id=%d", target, docs[3].ID), nil, &admin)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	rec = call(http.MethodGet, fmt.Sprintf("/api/documents/read?project=%s&path=wrong.md&document_id=%d", target, docs[6].ID), nil, &admin)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	rec = call(http.MethodGet, fmt.Sprintf("/api/documents/history?project=%s&path=shared.md&document_id=%d", target, docs[6].ID), nil, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "source-hash-1")
	require.Contains(t, rec.Body.String(), "source-hash-2")
	require.NotContains(t, rec.Body.String(), "target-hash")
	rec = call(http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", records[3].ID, target), nil, &admin)
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = call(http.MethodGet, "/api/projects/resolve?legacy_id="+a, nil, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), target)
	for i := range records {
		require.Equal(t, before[i], snapshot("memories", records[i].ID))
	}
	for i := range docs {
		require.Equal(t, beforeDocs[i], snapshot("versioned_documents", docs[i].ID))
	}
	nullEdit := map[string]any{"project": target, "path": "shared.md", "content": "must not write", "document_id": nil, "expected_version": nil}
	rec = call(http.MethodPost, "/api/documents", nullEdit, &admin)
	require.Equal(t, http.StatusBadRequest, rec.Code, "present null selectors must never fall back to a path-only write")
	edit := map[string]any{"project": target, "path": "shared.md", "content": "edited source", "document_id": docs[6].ID, "expected_version": 2}
	foreignEdit := map[string]any{"project": target, "path": docs[3].Path, "content": "must not write", "document_id": docs[3].ID, "expected_version": 1}
	rec = call(http.MethodPost, "/api/documents", foreignEdit, &admin)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	wrongPath := map[string]any{"project": target, "path": "wrong.md", "content": "must not write", "document_id": docs[6].ID, "expected_version": 2}
	rec = call(http.MethodPost, "/api/documents", wrongPath, &admin)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	staleEdit := map[string]any{"project": target, "path": "shared.md", "content": "must not write", "document_id": docs[5].ID, "expected_version": 1}
	rec = call(http.MethodPost, "/api/documents", staleEdit, &admin)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	pathOnly := map[string]any{"project": b, "path": "shared.md", "content": "must not fork"}
	rec = call(http.MethodPost, "/api/documents", pathOnly, &admin)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	rec = call(http.MethodPost, "/api/documents", edit, &admin)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	var stored gormdb.VersionedDocument
	require.NoError(t, db.First(&stored, created.ID).Error)
	require.Equal(t, a, stored.Project)
	require.Equal(t, 3, stored.Version)
	rec = call(http.MethodPost, "/api/documents", edit, &admin)
	require.Equal(t, http.StatusConflict, rec.Code, "retry must not create a second version")
	oldEdit := map[string]any{"project": a, "path": "shared.md", "content": "old selector edit", "document_id": created.ID, "expected_version": 3}
	rec = call(http.MethodPost, "/api/documents", oldEdit, &admin)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	comment := map[string]any{"project": target, "document_id": docs[5].ID, "expected_version": 1, "content": "historical version comment"}
	rec = call(http.MethodPost, "/api/documents/comment", comment, &admin)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	commentsURL := fmt.Sprintf("/api/documents/comments?project=%s&document_id=%d&expected_version=1", target, docs[5].ID)
	rec = call(http.MethodGet, commentsURL, nil, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "historical version comment")
	for _, bad := range []map[string]any{
		{"project": foreign, "document_id": docs[5].ID, "expected_version": 1, "content": "denied"},
		{"project": target, "document_id": docs[3].ID, "expected_version": 1, "content": "denied"},
		{"project": target, "document_id": docs[5].ID, "expected_version": 2, "content": "denied"},
	} {
		rec = call(http.MethodPost, "/api/documents/comment", bad, &admin)
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		require.JSONEq(t, `{"error":"document not found"}`, rec.Body.String())
	}
	rec = call(http.MethodPost, "/api/documents/comment", map[string]any{"document_id": docs[5].ID, "expected_version": nil, "content": "denied"}, &admin)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	readToken := "engram_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	hash, hashErr := bcrypt.GenerateFromPassword([]byte(readToken), bcrypt.MinCost)
	require.NoError(t, hashErr)
	gate, gateErr := NewTokenAuth("synthetic-master-only")
	require.NoError(t, gateErr)
	gate.SetValidator(auth.NewValidator("", knowledgeFixtureTokens{{ID: "fixture-readonly", Scope: "read-only", TokenHash: string(hash), TokenPrefix: readToken[len("engram_") : len("engram_")+8]}}))
	commentBytes, marshalErr := json.Marshal(comment)
	require.NoError(t, marshalErr)
	readonlyRequest := httptest.NewRequest(http.MethodPost, "/api/documents/comment", bytes.NewReader(commentBytes))
	readonlyRequest.Header.Set("Authorization", "Bearer "+readToken)
	readonlyResult := httptest.NewRecorder()
	gate.Middleware(router).ServeHTTP(readonlyResult, readonlyRequest)
	require.Equal(t, http.StatusForbidden, readonlyResult.Code, readonlyResult.Body.String())
	require.Contains(t, readonlyResult.Body.String(), "forbidden: read-only token")
	for _, invalidTarget := range []gormdb.CollectionSelectionTarget{{ID: fmt.Sprint(docs[3].ID), ExpectedVersion: 1}, {ID: fmt.Sprint(docs[5].ID), ExpectedVersion: 2}} {
		badSelection := map[string]any{"domain": documentSelectionDomain, "project": target, "selection": map[string]any{"kind": "explicit", "targets": []gormdb.CollectionSelectionTarget{invalidTarget}}}
		rec = call(http.MethodPost, "/api/documents/selection", badSelection, &admin)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	}
	selection := map[string]any{"domain": documentSelectionDomain, "project": target, "selection": map[string]any{"kind": "explicit", "targets": []gormdb.CollectionSelectionTarget{{ID: fmt.Sprint(docs[5].ID), ExpectedVersion: 1}}}}
	rec = call(http.MethodPost, "/api/documents/selection", selection, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var selected operatorCollectionSnapshotResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &selected))
	export := map[string]any{"action": "export", "project": target, "request_id": "synthetic-request", "selection": map[string]any{"kind": "explicit", "selection_version": selected.Selection.SelectionVersion}}
	rec = call(http.MethodPost, "/api/documents", export, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var exported documentOperationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &exported))
	require.NotNil(t, exported.Artifact)
	require.NotContains(t, exported.Artifact.DownloadURL, "?")
	rec = call(http.MethodGet, exported.Artifact.DownloadURL, nil, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"project":"`+target+`"`)
	otherBrowser := auth.SessionForBrowserUser("admin", 42)
	rec = call(http.MethodGet, exported.Artifact.DownloadURL, nil, &otherBrowser)
	require.Equal(t, http.StatusNotFound, rec.Code)
	wrongSession := httptest.NewRequest(http.MethodGet, exported.Artifact.DownloadURL, nil)
	wrongSession.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: "different-browser-session"})
	wrongSession = wrongSession.WithContext(auth.WithIdentity(wrongSession.Context(), admin))
	wrongSessionResult := httptest.NewRecorder()
	router.ServeHTTP(wrongSessionResult, wrongSession)
	require.Equal(t, http.StatusNotFound, wrongSessionResult.Code)
	rollback := gormdb.ProjectAliasRollbackRequest{ApplyKey: apply.IdempotencyKey, IdempotencyKey: uuid.NewString()}
	require.Equal(t, http.StatusForbidden, call(http.MethodPost, base+"/rollback", rollback, &nonAdmin).Code)
	var fingerprint string
	require.NoError(t, db.Raw(`SELECT plan_fingerprint FROM project_consolidation_redirects WHERE selector = ? AND session_id = ''`, a).Scan(&fingerprint).Error)
	require.NoError(t, db.Exec(`UPDATE project_consolidation_redirects SET plan_fingerprint = 'synthetic-drift' WHERE selector = ? AND session_id = ''`, a).Error)
	rec = call(http.MethodPost, base+"/rollback", rollback, &admin)
	require.Equal(t, http.StatusConflict, rec.Code, "rollback must reject drift atomically")
	require.NoError(t, db.Exec(`UPDATE project_consolidation_redirects SET plan_fingerprint = ? WHERE selector = ? AND session_id = ''`, fingerprint, a).Error)
	blocked := &blockingDocumentResponse{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	blockedRequest := httptest.NewRequest(http.MethodGet, exported.Artifact.DownloadURL, nil)
	blockedRequest.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: documentSelectionTestSession})
	blockedRequest = blockedRequest.WithContext(auth.WithIdentity(blockedRequest.Context(), admin))
	downloadDone := make(chan struct{})
	go func() { router.ServeHTTP(blocked, blockedRequest); close(downloadDone) }()
	select {
	case <-blocked.entered:
	case <-time.After(10 * time.Second):
		close(blocked.release)
		t.Fatal("download did not reach the response writer")
	}
	rollbackBytes, marshalErr := json.Marshal(rollback)
	require.NoError(t, marshalErr)
	rollbackRequest := httptest.NewRequest(http.MethodPost, base+"/rollback", bytes.NewReader(rollbackBytes))
	rollbackRequest = rollbackRequest.WithContext(auth.WithIdentity(rollbackRequest.Context(), admin))
	rollbackResult := httptest.NewRecorder()
	rollbackDone := make(chan struct{})
	go func() { router.ServeHTTP(rollbackResult, rollbackRequest); close(rollbackDone) }()
	select {
	case <-rollbackDone:
		close(blocked.release)
	case <-time.After(5 * time.Second):
		close(blocked.release)
		<-downloadDone
		<-rollbackDone
		t.Fatal("blocked download retained database locks needed by rollback")
	}
	<-downloadDone
	require.Equal(t, http.StatusOK, blocked.Code, blocked.Body.String())
	require.Equal(t, http.StatusOK, rollbackResult.Code, rollbackResult.Body.String())
	rec = call(http.MethodPost, "/api/documents", export, &admin)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	rec = call(http.MethodGet, exported.Artifact.DownloadURL, nil, &admin)
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = call(http.MethodGet, commentsURL, nil, &admin)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	rec = call(http.MethodPost, base+"/rollback", rollback, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = call(http.MethodGet, base+"/status?idempotency_key="+apply.IdempotencyKey, nil, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "rolled_back")
	require.Equal(t, http.StatusConflict, call(http.MethodPost, base+"/apply", apply, &admin).Code)
	rec = call(http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", records[1].ID, target), nil, &admin)
	require.Equal(t, http.StatusNotFound, rec.Code)
	secondRollback := gormdb.ProjectAliasRollbackRequest{ApplyKey: secondApply.IdempotencyKey, IdempotencyKey: uuid.NewString()}
	rec = call(http.MethodPost, base+"/rollback", secondRollback, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = call(http.MethodGet, "/api/projects", nil, &admin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var rawCatalog map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rawCatalog))
	groupsJSON, present := rawCatalog["logical_groups"]
	require.True(t, present, "logical_groups must signal capability after the final alias rollback")
	require.JSONEq(t, `[]`, string(groupsJSON))
	for i := range records {
		require.Equal(t, before[i], snapshot("memories", records[i].ID))
	}
	for i := range docs {
		require.Equal(t, beforeDocs[i], snapshot("versioned_documents", docs[i].ID))
	}
}

func TestProjectAliasAttachPreservesTargetSelfRedirect(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("migration186 PostgreSQL fixture DSN is not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid migration186 fixture DSN")
	}
	require.True(t, cfg.Host == "127.0.0.1" || cfg.Host == "localhost" || cfg.Host == "::1")
	const prefix, suffix = "engram_migration186_scope_test_", "_source"
	require.True(t, strings.HasPrefix(cfg.Database, prefix) && strings.HasSuffix(cfg.Database, suffix) && len(cfg.Database) > len(prefix)+len(suffix))
	require.True(t, cfg.RuntimeParams["search_path"] == "" || cfg.RuntimeParams["search_path"] == "public")
	require.Empty(t, cfg.Fallbacks)
	backing := openWorkerUCIContextCompositionStore(t)
	db := backing.DB
	target, source := uuid.NewString(), uuid.NewString()
	for _, id := range []string{target, source} {
		require.NoError(t, db.Create(&gormdb.Project{ID: id, DisplayName: sql.NullString{String: "alias witness", Valid: true}}).Error)
	}
	memories := []gormdb.Memory{
		{Project: target, Content: "target witness", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: source, Content: "source witness", Status: "active", PrivacyScope: "project", Version: 1},
	}
	for i := range memories {
		require.NoError(t, db.Create(&memories[i]).Error)
	}
	baseline := func() string {
		t.Helper()
		var row string
		require.NoError(t, db.Raw(`SELECT row_to_json(r)::text FROM project_consolidation_redirects r WHERE selector = ? AND session_id = ''`, target).Row().Scan(&row))
		return row
	}
	native := func(id int64) string {
		t.Helper()
		var row string
		require.NoError(t, db.Raw(`SELECT row_to_json(m)::text FROM memories m WHERE id = ?`, id).Row().Scan(&row))
		return row
	}
	service := &Service{store: backing, sessionStore: gormdb.NewSessionStore(backing), memoryStore: gormdb.NewMemoryStore(backing), vault: newTestVault(t)}
	router := chi.NewRouter()
	const base = "/api/projects/consolidation/aliases"
	router.Post(base+"/prepare", service.handleProjectAliasPrepare)
	router.Post(base+"/apply", service.handleProjectAliasApply)
	router.Get(base+"/status", service.handleProjectAliasStatus)
	router.Post(base+"/rollback", service.handleProjectAliasRollback)
	router.Get("/api/memories/{id}", service.handleGetMemoryByID)
	admin := auth.SessionForBrowserUser("admin", 41)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var payload []byte
		if body != nil {
			var err error
			payload, err = json.Marshal(body)
			require.NoError(t, err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		req = req.WithContext(auth.WithIdentity(req.Context(), admin))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	priorRequest := gormdb.ProjectAliasAttachRequest{Target: target, Aliases: []gormdb.ProjectAliasExpected{{Source: source}}, Reason: "baseline provenance fixture"}
	priorPrepare := call(http.MethodPost, base+"/prepare", priorRequest)
	require.Equal(t, http.StatusOK, priorPrepare.Code)
	var priorPlan gormdb.ProjectAliasPlan
	require.NoError(t, json.Unmarshal(priorPrepare.Body.Bytes(), &priorPlan))
	require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', ?, ?)`, target, target, priorPlan.Fingerprint).Error)
	beforeRedirect := baseline()
	beforeTarget, beforeSource := native(memories[0].ID), native(memories[1].ID)
	request := gormdb.ProjectAliasAttachRequest{Target: target, Aliases: []gormdb.ProjectAliasExpected{{Source: source}}, Reason: "existing target self redirect"}
	prepare := func() gormdb.ProjectAliasPlan {
		t.Helper()
		rec := call(http.MethodPost, base+"/prepare", request)
		if rec.Code != http.StatusOK {
			t.Fatalf("prepare must accept a pre-existing global target-self redirect: status=%d conflicting_membership=%t", rec.Code, strings.Contains(rec.Body.String(), "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP"))
		}
		var plan gormdb.ProjectAliasPlan
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &plan))
		return plan
	}
	witness := func(project string, index int, status int) {
		t.Helper()
		rec := call(http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", memories[index].ID, project), nil)
		require.Equal(t, status, rec.Code)
		if status == http.StatusOK {
			require.True(t, strings.Contains(rec.Body.String(), memories[index].Content))
		}
	}
	witness(target, 0, http.StatusOK)
	witness(source, 1, http.StatusOK)
	witness(target, 1, http.StatusNotFound)
	require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, 'session-conflict', ?, ?)`, target, target, priorPlan.Fingerprint).Error)
	require.Equal(t, http.StatusConflict, call(http.MethodPost, base+"/prepare", request).Code, "a session-scoped self redirect is not the global baseline")
	require.NoError(t, db.Exec(`DELETE FROM project_consolidation_redirects WHERE selector = ? AND session_id = 'session-conflict'`, target).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', ?, ?)`, source, source, priorPlan.Fingerprint).Error)
	require.Equal(t, http.StatusConflict, call(http.MethodPost, base+"/prepare", request).Code, "a source-self redirect must not be converted")
	require.NoError(t, db.Exec(`DELETE FROM project_consolidation_redirects WHERE selector = ? AND session_id = ''`, source).Error)
	require.Equal(t, beforeRedirect, baseline())
	stale := prepare()
	require.Equal(t, beforeRedirect, baseline())
	require.NoError(t, db.Exec(`UPDATE project_consolidation_redirects SET plan_fingerprint = ? WHERE selector = ? AND session_id = ''`, "changed-baseline", target).Error)
	staleApply := gormdb.ProjectAliasApplyRequest{PlanReference: stale.Reference, PlanFingerprint: stale.Fingerprint, IdempotencyKey: uuid.NewString()}
	require.Equal(t, http.StatusConflict, call(http.MethodPost, base+"/apply", staleApply).Code, "baseline drift must invalidate the prepared plan")
	require.NoError(t, db.Exec(`UPDATE project_consolidation_redirects SET plan_fingerprint = ? WHERE selector = ? AND session_id = ''`, priorPlan.Fingerprint, target).Error)
	require.Equal(t, beforeRedirect, baseline())
	plan := prepare()
	require.Equal(t, beforeRedirect, baseline())
	apply := gormdb.ProjectAliasApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, IdempotencyKey: uuid.NewString()}
	require.Equal(t, http.StatusOK, call(http.MethodPost, base+"/apply", apply).Code)
	require.Equal(t, beforeRedirect, baseline())
	require.Equal(t, http.StatusOK, call(http.MethodPost, base+"/apply", apply).Code, "same-key replay must reconcile")
	require.Equal(t, beforeRedirect, baseline())
	rec := call(http.MethodGet, base+"/status?idempotency_key="+apply.IdempotencyKey, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.Contains(rec.Body.String(), "applied"))
	require.Equal(t, beforeRedirect, baseline())
	witness(target, 1, http.StatusOK)
	witness(source, 0, http.StatusOK)
	witness(source, 1, http.StatusOK)
	rollback := gormdb.ProjectAliasRollbackRequest{ApplyKey: apply.IdempotencyKey, IdempotencyKey: uuid.NewString()}
	require.NoError(t, db.Exec(`UPDATE project_consolidation_redirects SET plan_fingerprint = ? WHERE selector = ? AND session_id = ''`, "active-baseline-drift", target).Error)
	require.Equal(t, http.StatusConflict, call(http.MethodPost, base+"/apply", apply).Code, "replay must reject changed baseline")
	require.Equal(t, http.StatusConflict, call(http.MethodGet, base+"/status?idempotency_key="+apply.IdempotencyKey, nil).Code, "status must reject changed baseline")
	require.Equal(t, http.StatusConflict, call(http.MethodPost, base+"/rollback", rollback).Code, "inverse must reject changed baseline")
	var sourceCount int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM project_consolidation_redirects WHERE selector = ? AND session_id = '' AND project_id = ? AND plan_fingerprint = ?`, source, target, plan.Fingerprint).Scan(&sourceCount).Error)
	require.EqualValues(t, 1, sourceCount, "refused inverse must retain the operation redirect")
	require.NoError(t, db.Exec(`UPDATE project_consolidation_redirects SET plan_fingerprint = ? WHERE selector = ? AND session_id = ''`, priorPlan.Fingerprint, target).Error)
	legacyTarget, legacySource := uuid.NewString(), uuid.NewString()
	for _, id := range []string{legacyTarget, legacySource} {
		require.NoError(t, db.Create(&gormdb.Project{ID: id}).Error)
	}
	legacyRequest := gormdb.ProjectAliasAttachRequest{Target: legacyTarget, Aliases: []gormdb.ProjectAliasExpected{{Source: legacySource}}, Reason: "legacy empty baseline"}
	legacyPrepare := call(http.MethodPost, base+"/prepare", legacyRequest)
	require.Equal(t, http.StatusOK, legacyPrepare.Code)
	var legacyPlan gormdb.ProjectAliasPlan
	require.NoError(t, json.Unmarshal(legacyPrepare.Body.Bytes(), &legacyPlan))
	legacyApply := gormdb.ProjectAliasApplyRequest{PlanReference: legacyPlan.Reference, PlanFingerprint: legacyPlan.Fingerprint, IdempotencyKey: uuid.NewString()}
	require.Equal(t, http.StatusOK, call(http.MethodPost, base+"/apply", legacyApply).Code)
	require.NoError(t, db.Exec(`UPDATE project_consolidation_operations SET receipt = receipt - 'redirect_snapshot' WHERE idempotency_key = ?`, legacyApply.IdempotencyKey).Error)
	require.Equal(t, http.StatusOK, call(http.MethodGet, base+"/status?idempotency_key="+legacyApply.IdempotencyKey, nil).Code)
	legacyRollback := gormdb.ProjectAliasRollbackRequest{ApplyKey: legacyApply.IdempotencyKey, IdempotencyKey: uuid.NewString()}
	require.Equal(t, http.StatusOK, call(http.MethodPost, base+"/rollback", legacyRollback).Code)
	require.Equal(t, beforeRedirect, baseline())
	require.Equal(t, http.StatusOK, call(http.MethodPost, base+"/rollback", rollback).Code)
	require.Equal(t, beforeRedirect, baseline())
	require.Equal(t, http.StatusOK, call(http.MethodPost, base+"/rollback", rollback).Code)
	rec = call(http.MethodGet, base+"/status?idempotency_key="+apply.IdempotencyKey, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.Contains(rec.Body.String(), "rolled_back"))
	require.Equal(t, beforeRedirect, baseline())
	witness(target, 0, http.StatusOK)
	witness(source, 1, http.StatusOK)
	witness(target, 1, http.StatusNotFound)
	require.Equal(t, beforeTarget, native(memories[0].ID))
	require.Equal(t, beforeSource, native(memories[1].ID))
}

func TestProjectAliasHTTPErrorSemantics(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{gormlib.ErrRecordNotFound, http.StatusNotFound, "PROJECT_ALIAS_NOT_RECORDED"},
		{errors.New("private database detail"), http.StatusServiceUnavailable, "PROJECT_ALIAS_UNAVAILABLE"},
		{&gormdb.ConsolidationError{Code: "PROJECT_ADOPTION_SOURCE_IDENTITY_UNAVAILABLE"}, http.StatusServiceUnavailable, "PROJECT_ADOPTION_SOURCE_IDENTITY_UNAVAILABLE"},
		{&gormdb.ConsolidationError{Code: "PROJECT_ADOPTION_STALE_PLAN"}, http.StatusConflict, "PROJECT_ADOPTION_STALE_PLAN"},
	} {
		rec := httptest.NewRecorder()
		projectAliasHTTPError(rec, tc.err)
		require.Equal(t, tc.status, rec.Code)
		require.Contains(t, rec.Body.String(), tc.code)
		require.NotContains(t, rec.Body.String(), "private database detail")
		require.NotContains(t, rec.Body.String(), "isolated-target")
	}
}
