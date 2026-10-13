package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestHumanVaultMissingContext(t *testing.T) {
	service := &Service{credentialStore: gormdb.NewCredentialStore(&gormdb.Store{})}
	for _, query := range []string{"", "?scope=project", "?project=dashboard", "?scope=global&project=", "?scope=global&project=" + uuid.NewString(), "?scope=other"} {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			r := httptest.NewRequest(method, "/api/vault/credentials/same"+query, nil)
			route := chi.NewRouteContext()
			route.URLParams.Add("name", "same")
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
			w := httptest.NewRecorder()
			if method == http.MethodGet {
				service.handleGetCredential(w, r)
			} else {
				service.handleDeleteCredential(w, r)
			}
			require.Equal(t, http.StatusBadRequest, w.Code)
		}
	}
	project, err := credentialRequestProject(httptest.NewRequest(http.MethodGet, "/?scope=global", nil))
	require.NoError(t, err)
	require.Empty(t, project)
	require.Nil(t, credentialProjectID(project))
	selected := uuid.NewString()
	project, err = credentialRequestProject(httptest.NewRequest(http.MethodGet, "/?project="+selected, nil))
	require.NoError(t, err)
	require.Equal(t, selected, project)
}

func TestHumanVaultIssueSelectionActorBinding(t *testing.T) {
	base, err := normalizeIssueSelectionFilter(&issueSelectionFilter{SourceProject: uuid.NewString()})
	require.NoError(t, err)
	human := base
	human.SourceAgent = "human"
	require.False(t, sameIssueSelectionFilter(base, human))
	require.NotEqual(t, base.collectionFilter().Fingerprint, human.collectionFilter().Fingerprint)
	_, err = normalizeIssueSelectionFilter(&issueSelectionFilter{SourceAgent: " human "})
	require.Error(t, err)
}

func TestHumanVaultHTTPStoreIsolation(t *testing.T) {
	dsnFile := os.Getenv("ENGRAM_IDENTITY_TEST_DSN_FILE")
	deadline, err := time.Parse(time.RFC3339, os.Getenv("ENGRAM_HUMAN_VAULT_FIXTURE_DEADLINE"))
	if dsnFile == "" || err != nil || time.Until(deadline) < 15*time.Second {
		t.Skip("requires private assigned fixture DSN and an active bounded runtime admission")
	}
	dsn, err := os.ReadFile(dsnFile)
	if err != nil {
		t.Fatal("private fixture DSN unavailable")
	}
	u, err := url.Parse(strings.TrimSpace(string(dsn)))
	if err != nil || u.Host != "127.0.0.1:22366" || u.Path != "/identity_fixture" {
		t.Fatal("refusing unassigned database")
	}
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	admin, err := gormlib.Open(postgres.Open(u.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("assigned fixture connection failed")
	}
	adminPool, err := admin.DB()
	require.NoError(t, err)
	defer adminPool.Close()
	schema := "human_vault_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.WithContext(ctx).Exec("CREATE SCHEMA "+schema).Error)
	defer func() {
		if time.Now().Before(deadline) {
			require.NoError(t, admin.WithContext(ctx).Exec("DROP SCHEMA "+schema+" CASCADE").Error)
		} else {
			t.Errorf("fixture deadline reached; owned schema retained: %s", schema)
		}
	}()
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := gormlib.Open(postgres.Open(u.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("assigned schema connection failed")
	}
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	pool.SetMaxOpenConns(2)
	require.NoError(t, db.WithContext(ctx).AutoMigrate(&gormdb.Credential{}, &gormdb.Issue{}, &gormdb.IssueComment{}, &gormdb.ConsolidationMembership{}))
	private := t.TempDir()
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	keyFile := filepath.Join(private, "vault.key")
	require.NoError(t, os.WriteFile(keyFile, []byte(hex.EncodeToString(key)), 0o600))
	service := &Service{
		store:           &gormdb.Store{DB: db},
		config:          &config.Config{EncryptionKeyFile: keyFile},
		credentialStore: gormdb.NewCredentialStore(&gormdb.Store{DB: db}),
		issueStore:      gormdb.NewIssueStore(db),
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), auth.Admin())))
		})
	})
	router.Get("/api/vault/credentials", service.handleListCredentials)
	router.Post("/api/vault/credentials", service.handleStoreCredential)
	router.Get("/api/vault/credentials/{name}", service.handleGetCredential)
	router.Delete("/api/vault/credentials/{name}", service.handleDeleteCredential)
	router.Get("/api/issues", service.handleListIssues)
	server := httptest.NewServer(router)
	defer server.Close()
	call := func(method, path string, body any, expected int) map[string]any {
		t.Helper()
		var reader io.Reader
		if body != nil {
			payload, marshalErr := json.Marshal(body)
			require.NoError(t, marshalErr)
			reader = bytes.NewReader(payload)
		}
		request, requestErr := http.NewRequestWithContext(ctx, method, server.URL+path, reader)
		require.NoError(t, requestErr)
		response, requestErr := server.Client().Do(request)
		require.NoError(t, requestErr)
		defer response.Body.Close()
		require.Equal(t, expected, response.StatusCode)
		if expected >= 400 {
			return nil
		}
		var result map[string]any
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
		return result
	}
	projectA, projectB := uuid.NewString(), uuid.NewString()
	name := "same-name"
	ids := make([]float64, 3)
	values := make([]string, 3)
	for i, project := range []string{projectA, projectB, ""} {
		secret := make([]byte, 24)
		_, err = rand.Read(secret)
		require.NoError(t, err)
		values[i] = hex.EncodeToString(secret)
		require.NoError(t, os.WriteFile(filepath.Join(private, "value-"+string(rune('a'+i))), []byte(values[i]), 0o600))
		scope := "project"
		if project == "" {
			scope = "global"
		}
		created := call(http.MethodPost, "/api/vault/credentials", map[string]any{"name": name, "value": values[i], "scope": scope, "project": project}, http.StatusCreated)
		state := created["current_state"].(map[string]any)
		ids[i] = state["id"].(float64)
		require.Equal(t, credentialProjectID(project), state["project_id"])
		require.Equal(t, scope, state["scope"])
		require.NotContains(t, state, "value")
		require.NotContains(t, created, "value")
		selector := "?project=" + project
		if project == "" {
			selector = "?scope=global"
		}
		revealed := call(http.MethodGet, "/api/vault/credentials/"+name+selector, nil, http.StatusOK)
		require.Equal(t, ids[i], revealed["id"])
		require.Equal(t, credentialProjectID(project), revealed["project_id"])
		require.True(t, revealed["value"] == values[i], "exact namespace must decrypt its own value")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/vault/credentials?project="+projectA, nil)
	require.NoError(t, err)
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	var metadata []map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&metadata))
	require.NoError(t, response.Body.Close())
	require.Len(t, metadata, 2)
	for _, item := range metadata {
		require.NotContains(t, item, "value")
		require.True(t, item["project_id"] == projectA || item["project_id"] == nil)
	}
	require.NotEqual(t, ids[0], ids[1])
	require.NotEqual(t, ids[1], ids[2])
	vault, err := service.getVault()
	require.NoError(t, err)
	consolidation := gormdb.NewProjectConsolidationStore(db, vault, nil)
	census, err := consolidation.Census(ctx, "retained-vault-fixture")
	require.NoError(t, err)
	plan, err := consolidation.Preflight(ctx, gormdb.ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint})
	require.NoError(t, err)
	nativeKey, err := json.Marshal(map[string]int64{"id": int64(ids[1])})
	require.NoError(t, err)
	address, err := json.Marshal(struct {
		Table, Column string
		Key           gormdb.JSONRaw
		Path          []string
	}{"credentials", "project", gormdb.JSONRaw(nativeKey), nil})
	require.NoError(t, err)
	referenceKey := vault.DeriveKey("project-consolidation-reference/v1")
	mac := hmac.New(sha256.New, referenceKey[:])
	_, _ = mac.Write([]byte("row\x00"))
	_, _ = mac.Write(address)
	nativeReference := "urn:engram:migration:row:" + hex.EncodeToString(mac.Sum(nil))
	var membership *gormdb.ConsolidationMapping
	for i := range plan.Mapping {
		if plan.Mapping[i].Reference == nativeReference {
			membership = &plan.Mapping[i]
		}
	}
	require.NotNil(t, membership)
	require.NotEmpty(t, membership.UnresolvedReference)
	require.NoError(t, db.Create(&gormdb.ConsolidationMembership{Reference: nativeReference, UnresolvedReference: membership.UnresolvedReference, Table: "credentials", RowKey: gormdb.JSONRaw(nativeKey), SourceRowKey: gormdb.JSONRaw(nativeKey), FieldPath: gormdb.JSONRaw(`["project"]`), SelectorReference: membership.SelectorReference, PlanFingerprint: plan.Fingerprint}).Error)
	retainedQuery := "?id=" + strconv.FormatInt(int64(ids[1]), 10) + "&unresolved_reference=" + url.QueryEscape(membership.UnresolvedReference)
	retained := call(http.MethodGet, "/api/vault/credentials/"+name+retainedQuery, nil, http.StatusOK)
	require.Nil(t, retained["project_id"])
	require.Equal(t, membership.UnresolvedReference, retained["unresolved_reference"])
	require.True(t, retained["value"] == values[1], "retained exact ID must decrypt only its own credential")
	call(http.MethodGet, "/api/vault/credentials/wrong-name"+retainedQuery, nil, http.StatusNotFound)
	call(http.MethodGet, "/api/vault/credentials/"+name+"?id="+strconv.FormatInt(int64(ids[0]), 10)+"&unresolved_reference="+url.QueryEscape(membership.UnresolvedReference), nil, http.StatusNotFound)
	call(http.MethodGet, "/api/vault/credentials/"+name+retainedQuery+"&scope=global", nil, http.StatusBadRequest)
	deletedAt := time.Now().UTC()
	require.NoError(t, db.Model(&gormdb.Credential{}).Where("id = ?", int64(ids[1])).Update("deleted_at", deletedAt).Error)
	call(http.MethodGet, "/api/vault/credentials/"+name+retainedQuery, nil, http.StatusNotFound)
	require.NoError(t, db.Model(&gormdb.Credential{}).Where("id = ?", int64(ids[1])).Update("deleted_at", nil).Error)
	require.NoError(t, db.Model(&gormdb.Credential{}).Where("id = ?", int64(ids[1])).Update("encryption_key_fingerprint", "fixture-mismatched-key").Error)
	call(http.MethodGet, "/api/vault/credentials/"+name+retainedQuery, nil, http.StatusConflict)
	require.NoError(t, db.Model(&gormdb.Credential{}).Where("id = ?", int64(ids[1])).Update("encryption_key_fingerprint", vault.Fingerprint()).Error)
	require.NoError(t, db.Model(&gormdb.Credential{}).Where("id = ?", int64(ids[1])).Update("project", uuid.NewString()).Error)
	call(http.MethodGet, "/api/vault/credentials/"+name+retainedQuery, nil, http.StatusNotFound)
	require.NoError(t, db.Model(&gormdb.Credential{}).Where("id = ?", int64(ids[1])).Update("project", projectB).Error)
	call(http.MethodGet, "/api/vault/credentials/"+name, nil, http.StatusBadRequest)
	call(http.MethodDelete, "/api/vault/credentials/"+name, nil, http.StatusBadRequest)
	call(http.MethodGet, "/api/vault/credentials/"+name+"?project="+uuid.NewString(), nil, http.StatusNotFound)
	deleted := call(http.MethodDelete, "/api/vault/credentials/"+name+"?project="+projectA, nil, http.StatusOK)
	require.Equal(t, projectA, deleted["current_state"].(map[string]any)["project_id"])
	require.NotContains(t, deleted["current_state"].(map[string]any), "value")
	call(http.MethodGet, "/api/vault/credentials/"+name+"?project="+projectA, nil, http.StatusNotFound)
	revealed := call(http.MethodGet, "/api/vault/credentials/"+name+"?project="+projectB, nil, http.StatusOK)
	require.True(t, revealed["value"] == values[1], "other project's same-name credential must survive")
	deleted = call(http.MethodDelete, "/api/vault/credentials/"+name+"?scope=global", nil, http.StatusOK)
	require.Nil(t, deleted["current_state"].(map[string]any)["project_id"])
	require.Equal(t, "global", deleted["current_state"].(map[string]any)["scope"])
	call(http.MethodGet, "/api/vault/credentials/"+name+"?scope=global", nil, http.StatusNotFound)
	revealed = call(http.MethodGet, "/api/vault/credentials/"+name+"?project="+projectB, nil, http.StatusOK)
	require.True(t, revealed["value"] == values[1])

	for _, issue := range []*gormdb.Issue{
		{Title: "human A high", SourceProject: projectA, TargetProject: projectB, SourceAgent: "human", Priority: "high"},
		{Title: "human A low", SourceProject: projectA, TargetProject: projectB, SourceAgent: "human", Priority: "low"},
		{Title: "bot A", SourceProject: projectA, TargetProject: projectB, SourceAgent: "bot", Priority: "critical"},
		{Title: "human B", SourceProject: projectB, TargetProject: projectB, SourceAgent: "human", Priority: "critical"},
		{Title: "legacy prefix", SourceProject: projectA + "_a1b2c3", TargetProject: projectB, SourceAgent: "human", Priority: "critical"},
		{Title: "dashboard provenance", SourceProject: "dashboard", TargetProject: projectB, SourceAgent: "human", Priority: "critical"},
		{Title: "global producer", SourceProject: "", TargetProject: "", SourceAgent: "service", Priority: "critical"},
	} {
		_, err := service.issueStore.CreateIssue(ctx, issue)
		require.NoError(t, err)
	}
	rows, total, err := service.issueStore.ListIssuesEx(ctx, gormdb.IssueListParams{SourceProject: projectA, SourceAgent: "human", Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Len(t, rows, 1)
	require.Equal(t, "human A low", rows[0].Title)
	page := call(http.MethodGet, "/api/issues?source_project="+projectA+"&source_agent=human&limit=1&offset=1", nil, http.StatusOK)
	require.Equal(t, float64(2), page["total"])
	items := page["issues"].([]any)
	require.Len(t, items, 1)
	require.Equal(t, "human A low", items[0].(map[string]any)["title"])
	all, total, err := service.issueStore.ListIssuesEx(ctx, gormdb.IssueListParams{Limit: 20})
	require.NoError(t, err)
	require.Len(t, all, 7)
	require.EqualValues(t, 7, total)
	filtered, err := service.issueSelectionRows(ctx, issueSelectionFilter{SourceProject: projectA, SourceAgent: "human"})
	require.NoError(t, err)
	require.Len(t, filtered, 2)
	t.Log("PASS real HTTP/AES-GCM/PostgreSQL: A/B/global same-name namespaces; scoped metadata; missing-context refusal; actor+project filters before count/offset; unscoped producer preserved")
}

func TestHumanVaultRetainedSelection(t *testing.T) {
	reference := "urn:engram:migration:unresolved:" + strings.Repeat("a", 64)
	for _, query := range []string{"?id=1", "?unresolved_reference=" + reference, "?id=0&unresolved_reference=" + reference, "?id=01&unresolved_reference=" + reference, "?id=1&id=2&unresolved_reference=" + reference, "?id=1&unresolved_reference=project-name", "?id=1&unresolved_reference=" + reference + "&project=" + uuid.NewString(), "?id=1&unresolved_reference=" + reference + "&scope=global"} {
		_, _, _, err := credentialReadSelection(httptest.NewRequest(http.MethodGet, "/"+query, nil))
		require.Error(t, err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/vault/credentials/same?id=1&unresolved_reference="+reference, nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("name", "same")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	service := &Service{credentialStore: gormdb.NewCredentialStore(&gormdb.Store{})}
	response := httptest.NewRecorder()
	service.handleGetCredential(response, request)
	require.Equal(t, http.StatusForbidden, response.Code)
	_, id, got, err := credentialReadSelection(request)
	require.NoError(t, err)
	require.EqualValues(t, 1, id)
	require.Equal(t, reference, got)
	future := time.Now().Add(time.Minute)
	legacy := auth.ClientWithPrincipalExpiry("read-write", "fixture-keycard", auth.LegacyDirectPrincipal(uuid.NewString()), auth.PrincipalKindAgent, &future)
	response = httptest.NewRecorder()
	service.handleGetCredential(response, request.WithContext(auth.WithIdentity(request.Context(), legacy)))
	require.Equal(t, http.StatusForbidden, response.Code)
	for _, identity := range []auth.Identity{auth.Admin(), auth.Session("admin"), auth.Client("read-only", "fixture-readonly"), auth.Client("read-write", "fixture-readwrite")} {
		require.True(t, hapHTTPRouteAllowed(identity, http.MethodGet, request.URL.Path), "retained read must keep the existing Vault GET actor policy")
	}
}
