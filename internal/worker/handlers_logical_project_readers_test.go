package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestLogicalProjectReadersPostgresHTTP(t *testing.T) {
	dsnFile := os.Getenv("ENGRAM_IDENTITY_TEST_DSN_FILE")
	deadline, err := time.Parse(time.RFC3339, os.Getenv("ENGRAM_READERS_FIXTURE_DEADLINE"))
	if dsnFile == "" || err != nil || time.Until(deadline) < 15*time.Second {
		t.Fatal("requires current exact-target fixture admission")
	}
	raw, err := os.ReadFile(dsnFile)
	if err != nil {
		t.Fatal("private fixture DSN unavailable")
	}
	u, err := url.Parse(strings.TrimSpace(string(raw)))
	if err != nil || u.Host != "127.0.0.1:22366" || u.Path != "/identity_fixture" {
		t.Fatal("refusing unassigned database")
	}
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	admin, err := gormlib.Open(postgres.Open(u.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("fixture connection failed")
	}
	adminPool, err := admin.DB()
	require.NoError(t, err)
	defer adminPool.Close()
	schema := "logical_readers_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.WithContext(ctx).Exec("CREATE SCHEMA "+schema).Error)
	defer func() {
		if time.Now().Before(deadline) {
			require.NoError(t, admin.WithContext(ctx).Exec("DROP SCHEMA "+schema+" CASCADE").Error)
		} else {
			t.Errorf("owned schema retained after admission expiry: %s", schema)
		}
	}()
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := gormlib.Open(postgres.Open(u.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("schema connection failed")
	}
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	pool.SetMaxOpenConns(2)
	require.NoError(t, db.WithContext(ctx).AutoMigrate(&gormdb.Project{}, &gormdb.Memory{}, &gormdb.VersionedDocument{}, &gormdb.Issue{}, &gormdb.IssueComment{}, &gormdb.AuditLogEntry{}, &gormdb.ConsolidationMembership{}, &gormdb.UCISource{}, &gormdb.UCICheckout{}, &gormdb.UCIView{}))
	require.NoError(t, db.Exec("ALTER TABLE ci_checkouts ADD COLUMN display_name TEXT").Error)
	require.NoError(t, db.Exec("CREATE TABLE project_consolidation_redirects(selector TEXT,session_id TEXT,project_id TEXT,plan_fingerprint TEXT)").Error)
	vault := newTestVault(t)
	backing := &gormdb.Store{DB: db}
	service := &Service{store: backing, vault: vault, sessionStore: gormdb.NewSessionStore(backing), memoryStore: gormdb.NewMemoryStore(backing), documentStore: gormdb.NewVersionedDocumentStore(backing), issueStore: gormdb.NewIssueStore(db)}
	adapter := NewOperatorCodeHTTPAdapter(nil, NewBrowserBindingApplication(gormdb.NewBrowserTabBindingStore(db)), nil, nil, nil)
	adapter.contexts = gormdb.NewBrowserCodeContextStore(db)
	require.NoError(t, adapter.configureChoiceCipher(vault))
	service.operatorCodeAdapter = adapter
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	a, b := uuid.NewString(), uuid.NewString()
	for _, id := range []string{a, b} {
		require.NoError(t, db.Create(&gormdb.Project{ID: id, ProjectKey: sql.NullString{String: id, Valid: true}, DisplayName: sql.NullString{String: "Same readable label", Valid: true}, LegacyIDs: []string{}}).Error)
	}
	require.NoError(t, db.Create(&gormdb.Project{ID: "old-duplicate", DisplayName: sql.NullString{String: "Same readable label", Valid: true}, LegacyIDs: []string{}}).Error)
	opaque := func(kind string, value any) string {
		key := vault.DeriveKey("project-consolidation-reference/v1")
		mac := hmac.New(sha256.New, key[:])
		raw, _ := json.Marshal(value)
		mac.Write([]byte(kind + "\x00"))
		mac.Write(raw)
		return "urn:engram:migration:" + kind + ":" + hex.EncodeToString(mac.Sum(nil))
	}
	unresolved := opaque("unresolved", opaque("selector", "mixed-selector"))
	codeUnresolved := ""
	membership := func(table string, key any, field, selector string, project *string, ref string) {
		t.Helper()
		raw, _ := json.Marshal(key)
		path, _ := json.Marshal([]string{field})
		reference := opaque("row", struct {
			Table, Column string
			Key           gormdb.JSONRaw
			Path          []string
		}{table, field, gormdb.JSONRaw(raw), nil})
		selectorReference := opaque("selector", selector)
		if table == "sources" {
			selectorReference = ""
			if project == nil {
				ref = opaque("unresolved", reference)
				codeUnresolved = ref
			}
		}
		row := gormdb.ConsolidationMembership{Reference: reference, Table: table, RowKey: gormdb.JSONRaw(raw), FieldPath: gormdb.JSONRaw(path), SelectorReference: selectorReference, ProjectID: project, UnresolvedReference: ref, Reason: "unproven historical owner", PlanFingerprint: "fixture-plan"}
		require.NoError(t, db.Create(&row).Error)
	}
	now := time.Now().UTC().Add(-time.Hour)
	memoryIDs := []int64{}
	for i := range 3 {
		row := gormdb.Memory{Project: "mixed-selector", Content: fmt.Sprintf("retained memory %d", i), Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}, CreatedAt: now, UpdatedAt: now}
		require.NoError(t, db.Create(&row).Error)
		memoryIDs = append(memoryIDs, row.ID)
		membership("memories", map[string]any{"id": row.ID}, "project", row.Project, nil, unresolved)
	}
	hidden := gormdb.Memory{Project: "mixed-selector", Content: "private hidden", Status: "active", Version: 1, PrivacyScope: "private", SourceWorkstationID: "private-owner-device", Tags: []string{}, SourceSessions: []string{}, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&hidden).Error)
	membership("memories", map[string]any{"id": hidden.ID}, "project", hidden.Project, nil, unresolved)
	mapped := gormdb.Memory{Project: "mixed-selector", Content: "canonical mapped memory", Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&mapped).Error)
	membership("memories", map[string]any{"id": mapped.ID}, "project", mapped.Project, &a, "")
	require.NoError(t, db.Create(&gormdb.AuditLogEntry{MemoryID: &memoryIDs[0], Action: "created", Actor: "fixture", BeforeState: nil, CreatedAt: now}).Error)
	docs := []gormdb.VersionedDocument{}
	for i := 1; i <= 2; i++ {
		row := gormdb.VersionedDocument{Project: "mixed-selector", Path: "retained.md", Version: i, Content: fmt.Sprintf("retained document %d", i), ContentHash: strings.Repeat("a", 64), DocType: "markdown", Metadata: "{}", Author: "fixture", CreatedAt: now.Add(time.Duration(i) * time.Minute)}
		require.NoError(t, db.Create(&row).Error)
		docs = append(docs, row)
		membership("versioned_documents", map[string]any{"id": row.ID}, "project", row.Project, nil, unresolved)
	}
	canonicalDoc := gormdb.VersionedDocument{Project: "mixed-selector", Path: "mapped.md", Version: 1, Content: "canonical retained document", ContentHash: strings.Repeat("b", 64), DocType: "markdown", Metadata: "{}", Author: "fixture", CreatedAt: now}
	require.NoError(t, db.Create(&canonicalDoc).Error)
	membership("versioned_documents", map[string]any{"id": canonicalDoc.ID}, "project", canonicalDoc.Project, &a, "")
	issue := gormdb.Issue{Title: "retained issue", Body: "retained issue body", SourceProject: "mixed-selector", TargetProject: "mixed-selector", Labels: []string{}, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&issue).Error)
	membership("issues", map[string]any{"id": issue.ID}, "target_project", issue.TargetProject, nil, unresolved)
	require.NoError(t, db.Create(&gormdb.IssueComment{IssueID: issue.ID, AuthorProject: "historical", AuthorAgent: "human", Body: "retained issue history", CreatedAt: now}).Error)
	require.NoError(t, db.Create(&gormdb.Issue{Title: "canonical issue", SourceProject: a, TargetProject: a, SourceAgent: "human", Labels: []string{}, CreatedAt: now, UpdatedAt: now}).Error)
	mappedHuman := gormdb.Issue{Title: "mapped human issue", SourceProject: "mixed-selector", TargetProject: "mixed-selector", SourceAgent: "human", Priority: "low", Labels: []string{}, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&mappedHuman).Error)
	membership("issues", map[string]any{"id": mappedHuman.ID}, "source_project", mappedHuman.SourceProject, &a, "")
	membership("issues", map[string]any{"id": mappedHuman.ID}, "target_project", mappedHuman.TargetProject, &b, "")
	foreignHuman := gormdb.Issue{Title: "foreign human issue", SourceProject: "mixed-selector", TargetProject: "mixed-selector", SourceAgent: "human", Labels: []string{}, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&foreignHuman).Error)
	membership("issues", map[string]any{"id": foreignHuman.ID}, "source_project", foreignHuman.SourceProject, &b, "")
	membership("issues", map[string]any{"id": foreignHuman.ID}, "target_project", foreignHuman.TargetProject, &a, "")
	mappedAgent := gormdb.Issue{Title: "mapped agent issue", SourceProject: "mixed-selector", TargetProject: "mixed-selector", SourceAgent: "agent", Labels: []string{}, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&mappedAgent).Error)
	membership("issues", map[string]any{"id": mappedAgent.ID}, "source_project", mappedAgent.SourceProject, &a, "")
	membership("issues", map[string]any{"id": mappedAgent.ID}, "target_project", mappedAgent.TargetProject, &a, "")
	viewIDs := []string{}
	for i := range 4 {
		source, checkout, view, profile, incarnation := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
		viewIDs = append(viewIDs, view)
		var plainProject *string
		if i == 1 {
			plainProject = &a
		}
		require.NoError(t, db.Create(&gormdb.UCISource{SourceID: source, ProjectID: plainProject, AuthRealm: uci.NoAuthCodeRealm, Kind: gormdb.UCISourceGit, DisplayName: "Independent repository", State: gormdb.UCISourceActive, CreatedAt: now, UpdatedAt: now}).Error)
		require.NoError(t, db.Create(&gormdb.UCICheckout{CheckoutID: checkout, SourceID: source, IncarnationID: incarnation, WorkstationID: fmt.Sprintf("device-%d", i), Kind: gormdb.UCICheckoutWorkingTree, OwnerPrincipal: uci.NoAuthCodePrincipal, LocatorRef: fmt.Sprintf("file:///private/copy-%d", i), State: gormdb.UCICheckoutWatching, CurrentViewID: &view, CreatedAt: now, UpdatedAt: now}).Error)
		require.NoError(t, db.Exec("UPDATE ci_checkouts SET display_name=? WHERE checkout_id=?", fmt.Sprintf("Working copy %d", i), checkout).Error)
		head := fmt.Sprintf("%040d", i+1)
		label := "same-branch"
		published := now.Add(time.Duration(i) * time.Minute)
		require.NoError(t, db.Create(&gormdb.UCIView{ViewID: view, CheckoutID: checkout, SourceID: source, IncarnationID: incarnation, ProfileID: profile, Generation: 1, HeadOID: &head, RefLabel: &label, Dirty: true, State: gormdb.UCIViewPublished, CoverageJSON: "{}", PublishedAt: &published, CreatedAt: now, UpdatedAt: now}).Error)
		owner, ref := &a, ""
		if i == 2 {
			owner = &b
		}
		if i == 3 {
			owner = nil
			ref = unresolved
		}
		if plainProject == nil {
			membership("sources", map[string]any{"source_id": source}, "", "", owner, ref)
		}
	}
	require.NoError(t, db.Exec("INSERT INTO project_consolidation_redirects VALUES(?,?,?,?)", "old-a", "", a, "fixture-plan").Error)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity := auth.AuthDisabled()
			if r.Header.Get("X-Fixture-Private") == "foreign" {
				identity = auth.ClientWithPrincipal("read-only", "foreign-device", "foreign", auth.PrincipalKindAgent)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
		})
	})
	router.Get("/api/projects", service.handleGetProjects)
	router.Get("/api/projects/resolve", service.handleResolveLogicalProject)
	router.Get("/api/projects/unresolved/{reference}/content", service.handleRetainedProjectContent)
	router.Get("/api/projects/unresolved/{reference}/history", service.handleRetainedProjectHistory)
	router.Get("/api/projects/unresolved/{reference}/code", service.handleRetainedProjectCode)
	router.Get("/api/memories", service.handleListMemories)
	router.Get("/api/documents", service.handleListDocuments)
	router.Get("/api/issues", service.handleListIssues)
	router.Post("/api/code/tabs/handshake", adapter.HandleHandshake)
	router.Post("/api/code/contexts", adapter.HandleContexts)
	server := httptest.NewServer(router)
	defer server.Close()
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			reader = bytes.NewReader(raw)
		}
		request, err := http.NewRequestWithContext(ctx, method, server.URL+path, reader)
		require.NoError(t, err)
		request.Header.Set("X-Engram-Request-ID", "logical-reader-fixture")
		request.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, want, response.StatusCode, string(raw))
		result := map[string]any{}
		if want < 400 {
			require.NoError(t, json.Unmarshal(raw, &result), string(raw))
		}
		return result
	}
	catalog := call("GET", "/api/projects", nil, 200)
	require.Len(t, catalog["items"], 2)
	require.NotEmpty(t, catalog["unresolved"])
	resolved := call("GET", "/api/projects/resolve?legacy_id=old-a", nil, 200)
	require.Equal(t, a, resolved["project_id"])
	call("GET", "/api/memories?project=mixed-selector", nil, 400)
	normal := call("GET", "/api/memories?project="+a, nil, 200)
	require.EqualValues(t, 1, normal["total"])
	normalDocs := call("GET", "/api/documents?project="+a, nil, 200)
	require.EqualValues(t, 1, normalDocs["total"])
	call("GET", "/api/issues?source_project=dashboard&source_agent=human", nil, 400)
	call("GET", "/api/issues?project=mixed-selector", nil, 400)
	normalIssues := call("GET", "/api/issues?source_project="+a+"&source_agent=human&limit=1&offset=1", nil, 200)
	require.EqualValues(t, 2, normalIssues["total"])
	require.Len(t, normalIssues["issues"], 1)
	require.EqualValues(t, mappedHuman.ID, normalIssues["issues"].([]any)[0].(map[string]any)["id"])
	rolePair := call("GET", "/api/issues?source_project="+a+"&project="+b+"&source_agent=human", nil, 200)
	require.EqualValues(t, 1, rolePair["total"])
	require.EqualValues(t, mappedHuman.ID, rolePair["issues"].([]any)[0].(map[string]any)["id"])
	agentIssues := call("GET", "/api/issues?source_project="+a+"&source_agent=agent", nil, 200)
	require.EqualValues(t, 1, agentIssues["total"])
	require.EqualValues(t, mappedAgent.ID, agentIssues["issues"].([]any)[0].(map[string]any)["id"])
	prefix := "/api/projects/unresolved/" + url.PathEscape(unresolved)
	memoryPage := call("GET", prefix+"/content?kind=memory&limit=1&q=retained", nil, 200)
	require.EqualValues(t, 3, memoryPage["total"])
	require.Len(t, memoryPage["items"], 1)
	cursor := memoryPage["next_cursor"].(string)
	second := call("GET", prefix+"/content?kind=memory&limit=1&q=retained&cursor="+url.QueryEscape(cursor), nil, 200)
	require.NotEqual(t, memoryPage["items"].([]any)[0].(map[string]any)["id"], second["items"].([]any)[0].(map[string]any)["id"])
	call("GET", prefix+"/content?kind=memory&limit=1&q=other&cursor="+url.QueryEscape(cursor), nil, 400)
	privacyRequest, err := http.NewRequestWithContext(ctx, "GET", server.URL+prefix+"/content?kind=memory", nil)
	require.NoError(t, err)
	privacyRequest.Header.Set("X-Fixture-Private", "foreign")
	privacyResponse, err := server.Client().Do(privacyRequest)
	require.NoError(t, err)
	privacyRaw, err := io.ReadAll(privacyResponse.Body)
	require.NoError(t, err)
	require.NoError(t, privacyResponse.Body.Close())
	require.Equal(t, http.StatusOK, privacyResponse.StatusCode, string(privacyRaw))
	require.NotContains(t, string(privacyRaw), "private hidden")
	documents := call("GET", prefix+"/content?kind=document&limit=1", nil, 200)
	require.EqualValues(t, 2, documents["total"])
	require.NotEmpty(t, documents["items"].([]any)[0].(map[string]any)["content"])
	docHistory := call("GET", prefix+fmt.Sprintf("/history?kind=document&id=%d&limit=1", docs[1].ID), nil, 200)
	require.EqualValues(t, 2, docHistory["total"])
	require.NotNil(t, docHistory["next_cursor"])
	issuePage := call("GET", prefix+"/content?kind=issue", nil, 200)
	require.EqualValues(t, 1, issuePage["total"])
	require.Equal(t, "retained issue body", issuePage["items"].([]any)[0].(map[string]any)["body"])
	issueHistory := call("GET", prefix+fmt.Sprintf("/history?kind=issue&id=%d", issue.ID), nil, 200)
	require.EqualValues(t, 1, issueHistory["total"])
	memoryHistory := call("GET", prefix+fmt.Sprintf("/history?kind=memory&id=%d", memoryIDs[0]), nil, 200)
	require.EqualValues(t, 1, memoryHistory["total"])
	call("GET", prefix+fmt.Sprintf("/history?kind=memory&id=%d", mapped.ID), nil, 404)
	call("GET", "/api/projects/unresolved/urn:engram:migration:unresolved:unknown/content?kind=memory", nil, 404)
	transition := call("POST", "/api/code/tabs/handshake", map[string]string{"document_nonce": "reader-document"}, 200)
	proof := map[string]any{"tab_binding_id": transition["tab_binding_id"], "document_proof": transition["document_proof"]}
	allCode := call("POST", "/api/code/contexts", proof, 200)
	require.Len(t, allCode["contexts"], 4)
	selected := call("POST", "/api/code/contexts?project="+a, proof, 200)
	require.Len(t, selected["contexts"], 2)
	entries := selected["contexts"].([]any)
	first, other := entries[0].(map[string]any), entries[1].(map[string]any)
	require.Equal(t, a, first["project_id"])
	require.Equal(t, a, other["project_id"])
	require.NotEqual(t, first["source_ref"], other["source_ref"])
	require.NotEqual(t, first["checkout_ref"], other["checkout_ref"])
	require.NotEqual(t, first["view_ref"], other["view_ref"])
	require.NotEqual(t, first["indexed_snapshot"].(map[string]any)["revision"], other["indexed_snapshot"].(map[string]any)["revision"])
	codeReview := call("GET", "/api/projects/unresolved/"+url.PathEscape(codeUnresolved)+"/code?limit=1", nil, 200)
	require.EqualValues(t, 1, codeReview["total"])
	require.Nil(t, codeReview["items"].([]any)[0].(map[string]any)["project_id"])
	require.NotContains(t, codeReview["items"].([]any)[0].(map[string]any), "selection_ref")
	call("POST", "/api/code/contexts?project=mixed-selector", proof, 400)
	require.NoError(t, db.Model(&gormdb.Memory{}).Where("id=?", memoryIDs[0]).Update("project", a).Error)
	call("GET", prefix+"/content?kind=memory", nil, 404)
	var count int64
	require.NoError(t, db.Model(&gormdb.UCIView{}).Where("view_id IN ? AND dirty = true", viewIDs).Count(&count).Error)
	require.EqualValues(t, 4, count)
	t.Log("PASS real PostgreSQL/loopback HTTP: UUID catalog/legacy redirect; exact retained row membership; memory privacy SQL; nonempty docs/memory/issues and history; count/search/cursor pages; stale/unknown/foreign IDs denied; two distinct Sources/Checkouts/dirty Views share one logical UUID without changing snapshot refs")
}
