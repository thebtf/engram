package worker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func retainedDiscoveryRouter(service *Service) http.Handler {
	router := chi.NewRouter()
	router.Get("/api/projects", service.handleGetProjects)
	router.Get("/api/projects/unresolved/{reference}/content", service.handleRetainedProjectContent)
	router.Get("/api/projects/unresolved/{reference}/history", service.handleRetainedProjectHistory)
	router.Get("/api/projects/unresolved/{reference}/code", service.handleRetainedProjectCode)
	return router
}

func retainedDiscoveryGET(t *testing.T, router http.Handler, path string, authenticated bool, status int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if authenticated {
		request = request.WithContext(auth.WithIdentity(request.Context(), auth.Admin()))
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, status, response.Code, response.Body.String())
	return response
}

func retainedDiscoveryFixture(t *testing.T) (*Service, *gorm.DB, map[string]string) {
	t.Helper()
	db := knowledgeSQLiteDB(t)
	vault := newTestVault(t)
	backing := &gormdb.Store{DB: db}
	service := &Service{store: backing, vault: vault, sessionStore: gormdb.NewSessionStore(backing)}
	for _, statement := range []string{
		`CREATE TABLE sources (source_id TEXT PRIMARY KEY, display_name TEXT, project_id TEXT)`,
		`CREATE TABLE issues (id INTEGER PRIMARY KEY, title TEXT, body TEXT, source_project TEXT, target_project TEXT)`,
		`CREATE TABLE credentials (id INTEGER PRIMARY KEY, project TEXT, encrypted_secret TEXT)`,
		`CREATE TABLE sdk_sessions (id INTEGER PRIMARY KEY, project TEXT)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	const selector = "historical-unregistered-selector"
	for _, id := range []int64{101, 102} {
		require.NoError(t, db.Create(&gormdb.Memory{ID: id, Project: selector, Content: "private-memory-body-sentinel", PrivacyScope: "private", Status: "active", Version: 1, Tags: []string{}, SourceSessions: []string{}}).Error)
	}
	for _, id := range []int64{201, 202} {
		require.NoError(t, db.Create(&gormdb.VersionedDocument{ID: id, Project: selector, Path: "private-document-path-sentinel", Version: int(id - 200), Content: "document-body-sentinel", ContentHash: "synthetic", Metadata: "{}"}).Error)
	}
	require.NoError(t, db.Exec("INSERT INTO issues VALUES (301, 'issue-title-sentinel', 'issue-body-sentinel', ?, ?)", selector, selector).Error)
	require.NoError(t, db.Exec("INSERT INTO credentials VALUES (401, ?, 'ciphertext-sentinel')", selector).Error)
	require.NoError(t, db.Exec("INSERT INTO sdk_sessions VALUES (501, ?)", "historical-session-only").Error)
	sourceID := uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO sources VALUES (?, 'Retained repository', NULL)", sourceID).Error)
	for _, id := range []string{uuid.NewString(), "historical-registry"} {
		require.NoError(t, db.Exec("INSERT INTO projects (id, display_name, relative_path) VALUES (?, 'Historical notebook', 'private-locator-sentinel')", id).Error)
	}
	opaque := func(kind string, value any) string {
		key := vault.DeriveKey("project-consolidation-reference/v1")
		mac := hmac.New(sha256.New, key[:])
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = mac.Write(append([]byte(kind+"\x00"), raw...))
		require.NoError(t, err)
		return "urn:engram:migration:" + kind + ":" + hex.EncodeToString(mac.Sum(nil))
	}
	member := func(table string, key any, field, historicalSelector string) string {
		t.Helper()
		raw, err := json.Marshal(key)
		require.NoError(t, err)
		path, err := json.Marshal([]string{field})
		require.NoError(t, err)
		reference := opaque("row", struct {
			Table, Column string
			Key           gormdb.JSONRaw
			Path          []string
		}{table, field, gormdb.JSONRaw(raw), nil})
		selectorRef := opaque("selector", historicalSelector)
		if table == "sources" {
			selectorRef = ""
		}
		group := selectorRef
		if group == "" {
			group = reference
		}
		unresolved := opaque("unresolved", group)
		require.NoError(t, db.Create(&gormdb.ConsolidationMembership{Reference: reference, Table: table, RowKey: gormdb.JSONRaw(raw), SourceRowKey: gormdb.JSONRaw(raw), FieldPath: gormdb.JSONRaw(path), SelectorReference: selectorRef, UnresolvedReference: unresolved, Reason: "missing_membership_evidence", PlanFingerprint: "retained-discovery-synthetic"}).Error)
		return unresolved
	}
	refs := map[string]string{}
	for _, id := range []int64{101, 102} {
		refs["mixed"] = member("memories", map[string]int64{"id": id}, "project", selector)
	}
	for _, id := range []int64{201, 202} {
		member("versioned_documents", map[string]int64{"id": id}, "project", selector)
	}
	for _, field := range []string{"source_project", "target_project"} {
		member("issues", map[string]int64{"id": 301}, field, selector)
	}
	member("credentials", map[string]int64{"id": 401}, "project", selector)
	refs["native"] = member("sdk_sessions", map[string]int64{"id": 501}, "project", "historical-session-only")
	refs["code"] = member("sources", map[string]string{"source_id": sourceID}, "", "")
	refs["registry"] = member("projects", map[string]string{"id": "historical-registry"}, "id", "historical-registry")
	return service, db, refs
}

func TestRetainedDiscoveryCatalogHTTP(t *testing.T) {
	service, _, refs := retainedDiscoveryFixture(t)
	response := retainedDiscoveryGET(t, retainedDiscoveryRouter(service), "/api/projects", true, http.StatusOK)
	var catalog struct {
		Items      []map[string]any `json:"items"`
		Unresolved []map[string]any `json:"unresolved"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &catalog))
	require.Len(t, catalog.Items, 1)
	require.Len(t, catalog.Unresolved, len(refs))
	groups := map[string]map[string]any{}
	for _, item := range catalog.Unresolved {
		groups[item["reference"].(string)] = item
	}

	t.Run("catalog_keeps_provenance_separate_from_payload_and_owner", func(t *testing.T) {
		require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
		require.ElementsMatch(t, []any{"document", "issue", "memory"}, groups[refs["mixed"]]["reader_kinds"])
		require.Equal(t, []any{"code"}, groups[refs["code"]]["reader_kinds"])
		require.Equal(t, []any{}, groups[refs["registry"]]["reader_kinds"])
		require.Equal(t, []any{}, groups[refs["native"]]["reader_kinds"])
		require.Equal(t, "Historical notebook", groups[refs["registry"]]["name"])
		require.Equal(t, "Retained repository", groups[refs["code"]]["name"])
		for _, marker := range []string{"private-memory-body-sentinel", "private-document-path-sentinel", "document-body-sentinel", "issue-title-sentinel", "issue-body-sentinel", "ciphertext-sentinel", "private-locator-sentinel", "historical-unregistered-selector", "historical-session-only"} {
			require.NotContains(t, response.Body.String(), marker)
		}
		for _, item := range catalog.Unresolved {
			require.Empty(t, item["project_id"], "a label shared with a canonical project is provenance, not ownership")
			require.NotContains(t, item, "available")
			require.NotContains(t, item, "readable_count")
			require.NotContains(t, item, "record_count")
		}
	})

	t.Run("fallback_groups_expose_typed_membership_discovery", func(t *testing.T) {
		// Decode the wire contract independently of the production DTO.
		type family struct {
			Kind            string `json:"kind"`
			TableName       string `json:"table_name"`
			MembershipCount int64  `json:"membership_count"`
			ReaderSupport   string `json:"reader_support"`
		}
		for _, expected := range []struct {
			group    string
			families []family
		}{
			{"native", []family{{"native", "sdk_sessions", 1, "metadata_only"}}},
			{"mixed", []family{{"memory", "memories", 2, "declared"}, {"document", "versioned_documents", 2, "declared"}, {"issue", "issues", 2, "declared"}, {"credential", "credentials", 1, "separate"}}},
			{"code", []family{{"code", "sources", 1, "declared"}}},
			{"registry", []family{{"native", "projects", 1, "metadata_only"}}},
		} {
			raw, exists := groups[refs[expected.group]]["families"]
			require.True(t, exists, "retained catalog lacks typed membership discovery for %s; reader_kinds is not family/count/support metadata", expected.group)
			encoded, err := json.Marshal(raw)
			require.NoError(t, err)
			var actual []family
			require.NoError(t, json.Unmarshal(encoded, &actual))
			require.ElementsMatch(t, expected.families, actual, "counts are saved membership occurrences, not visible records, document paths, checkouts or successful reads")
		}
	})
}

func TestRetainedDiscoveryQueryDispatchHTTP(t *testing.T) {
	service, db, _ := retainedDiscoveryFixture(t)
	observer := &retainedRecordQueryObserver{Interface: logger.Default.LogMode(logger.Silent)}
	backing := &gormdb.Store{DB: db.Session(&gorm.Session{Logger: observer})}
	service.store = backing
	service.sessionStore = gormdb.NewSessionStore(backing)
	router := retainedDiscoveryRouter(service)

	catalog := retainedDiscoveryGET(t, router, "/api/projects", true, http.StatusOK)
	require.NotEmpty(t, observer.drain(), "the healthy catalog control must exercise observed storage")
	unchanged := retainedDiscoveryGET(t, router, "/api/projects?unknown=ignored", true, http.StatusOK)
	require.JSONEq(t, catalog.Body.String(), unchanged.Body.String(), "well-formed no-view queries retain ordinary catalog behavior")
	require.NotEmpty(t, observer.drain())

	for _, authenticated := range []bool{true, false} {
		for _, query := range []string{
			"view=retained_records%ZZ",
			"vi%ZZew=retained_records",
			"view=retained_records;kind=memory",
			"view=retained_records&attribution_scope=unresolved_history&kind=memory&q=%ZZ",
			"view=retained_records&view=retained_records%ZZ",
			"view=retained_records&view=retained_records&attribution_scope=unresolved_history&kind=memory",
		} {
			t.Run(fmt.Sprintf("authenticated=%t/%s", authenticated, query), func(t *testing.T) {
				request := httptest.NewRequest(http.MethodGet, "/api/projects?"+query, nil)
				if authenticated {
					request = request.WithContext(auth.WithIdentity(request.Context(), auth.Admin()))
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				queries := observer.drain()
				t.Logf("status=%d storage_queries=%d body=%s", response.Code, len(queries), response.Body.String())
				require.Empty(t, queries, "invalid query parsing must precede catalog, membership and native data work")
				require.Equal(t, http.StatusBadRequest, response.Code)
				require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
				require.JSONEq(t, `{"error":"INVALID_QUERY"}`, response.Body.String(), "parse errors precede retained authorization and disclose no catalog or payload")
			})
		}
	}

	observer.drain()
	forbidden := retainedDiscoveryGET(t, router, "/api/projects?view=retained_records&attribution_scope=unresolved_history&kind=memory", false, http.StatusForbidden)
	require.Equal(t, "private, no-store", forbidden.Header().Get("Cache-Control"))
	require.NotContains(t, forbidden.Body.String(), "sentinel")
	require.Empty(t, observer.drain(), "valid retained queries still authorize before storage")
	retainedDiscoveryGET(t, router, "/api/projects", false, http.StatusForbidden)
}

func TestRetainedDiscoveryReadRefusalsHTTP(t *testing.T) {
	service, db, refs := retainedDiscoveryFixture(t)
	router := retainedDiscoveryRouter(service)
	prefix := "/api/projects/unresolved/" + url.PathEscape(refs["native"])
	for _, path := range []string{"/api/projects", prefix + "/content?kind=memory", prefix + "/history?kind=document&id=201", prefix + "/code"} {
		retainedDiscoveryGET(t, router, path, false, http.StatusForbidden)
	}
	for _, path := range []string{prefix + "/content?kind=memory", prefix + "/history?kind=document&id=201", prefix + "/code", prefix + "/content?kind=unsupported", "/api/projects/unresolved/urn:engram:migration:unresolved:unknown/content?kind=memory"} {
		response := retainedDiscoveryGET(t, router, path, true, http.StatusNotFound)
		require.Contains(t, response.Body.String(), "PROJECT_ADOPTION_UNKNOWN_REFERENCE")
		require.NotContains(t, response.Body.String(), "sentinel")
	}
	require.NoError(t, db.Model(&gormdb.ConsolidationMembership{}).Where("table_name = ?", "memories").Update("field_path", gormdb.JSONRaw(`[]`)).Error)
	stale := retainedDiscoveryGET(t, router, "/api/projects", true, http.StatusNotFound)
	require.Contains(t, stale.Body.String(), "PROJECT_ADOPTION_STALE_REFERENCE")
	require.NotContains(t, stale.Body.String(), "sentinel")
}

func TestRetainedDiscoveryEmptyAndUnavailableHTTP(t *testing.T) {
	service, db, _ := retainedDiscoveryFixture(t)
	require.NoError(t, db.Exec("DELETE FROM project_consolidation_memberships").Error)
	response := retainedDiscoveryGET(t, retainedDiscoveryRouter(service), "/api/projects", true, http.StatusOK)
	var catalog map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &catalog))
	require.JSONEq(t, `[]`, string(catalog["unresolved"]), "an empty membership catalog is not a null/error response")
	require.NoError(t, db.Exec("ALTER TABLE project_consolidation_memberships RENAME TO unavailable_memberships").Error)
	unavailable := retainedDiscoveryGET(t, retainedDiscoveryRouter(service), "/api/projects", true, http.StatusServiceUnavailable)
	require.NotContains(t, unavailable.Body.String(), "SQL")
	require.NotContains(t, unavailable.Body.String(), "unavailable_memberships")
	for _, suffix := range []string{"/content?kind=memory", "/history?kind=issue&id=301", "/code"} {
		retainedDiscoveryGET(t, retainedDiscoveryRouter(&Service{}), "/api/projects/unresolved/opaque"+suffix, true, http.StatusServiceUnavailable)
	}
}

func TestRetainedDiscoveryContinuationBindings(t *testing.T) {
	for _, kind := range []string{"memory", "document", "issue", "code"} {
		t.Run(kind, func(t *testing.T) {
			path := "/api/projects/unresolved/reference/content?kind=" + kind + "&limit=1"
			if kind == "code" {
				path = "/api/projects/unresolved/reference/code?limit=1"
			}
			limit, offset, binding, err := retainedProjectPage(httptest.NewRequest(http.MethodGet, path, nil), "reference", "revision")
			require.NoError(t, err)
			require.Equal(t, 0, offset)
			cursor := retainedProjectNext(binding, offset, limit, 2)
			require.NotNil(t, cursor)
			next := path + "&cursor=" + url.QueryEscape(*cursor)
			_, offset, _, err = retainedProjectPage(httptest.NewRequest(http.MethodGet, next, nil), "reference", "revision")
			require.NoError(t, err)
			require.Equal(t, 1, offset)
			require.Nil(t, retainedProjectNext(binding, offset, limit, 2))
			require.Nil(t, retainedProjectNext(binding, 0, limit, 0))
			for _, changed := range []string{next + "&q=other", strings.Replace(next, "limit=1", "limit=2", 1), strings.Replace(next, "/reference/", "/foreign/", 1), strings.Replace(next, "/content?", "/history?", 1) + "&id=201"} {
				_, _, _, err = retainedProjectPage(httptest.NewRequest(http.MethodGet, changed, nil), "reference", "revision")
				require.Error(t, err, "a continuation cannot cross query/limit/reference/reader bindings")
			}
			_, _, _, err = retainedProjectPage(httptest.NewRequest(http.MethodGet, next, nil), "reference", "changed-revision")
			require.Error(t, err)
		})
	}
}

type retainedRecordQueryItem struct {
	Reference string         `json:"reference"`
	ID        int64          `json:"id"`
	Record    map[string]any `json:"record"`
}

type retainedRecordQuerySQL struct {
	statement string
	rows      int64
}

type retainedRecordQueryObserver struct {
	logger.Interface
	mu      sync.Mutex
	queries []retainedRecordQuerySQL
}

func (observer *retainedRecordQueryObserver) Trace(_ context.Context, _ time.Time, result func() (string, int64), _ error) {
	statement, rows := result()
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.queries = append(observer.queries, retainedRecordQuerySQL{statement, rows})
}

func (observer *retainedRecordQueryObserver) drain() []retainedRecordQuerySQL {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	queries := observer.queries
	observer.queries = nil
	return queries
}

func retainedRecordQueryPage(t *testing.T, response map[string]any, kind string) ([]retainedRecordQueryItem, *string) {
	t.Helper()
	require.Equal(t, "retained_records", response["view"])
	require.Equal(t, "unresolved_history", response["attribution_scope"])
	require.Equal(t, kind, response["kind"])
	for _, key := range []string{"total", "has_more", "project_key", "project_id", "unresolved"} {
		require.NotContains(t, response, key)
	}
	require.Contains(t, response, "items")
	require.Contains(t, response, "next_cursor", "missing continuation is not observed exhaustion")
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	var page struct {
		Items      []retainedRecordQueryItem `json:"items"`
		NextCursor *string                   `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(raw, &page))
	require.NotNil(t, page.Items, "empty results must be [] rather than null")
	for _, item := range page.Items {
		require.Positive(t, item.ID)
		require.True(t, strings.HasPrefix(item.Reference, "urn:engram:migration:unresolved:"))
		require.EqualValues(t, item.ID, item.Record["id"])
		require.Equal(t, "unresolved_history", item.Record["attribution_scope"])
		require.NotContains(t, item.Record, "project_key")
	}
	if page.NextCursor != nil {
		require.NotEmpty(t, *page.NextCursor)
	}
	return page.Items, page.NextCursor
}

func retainedRecordQueryBudget(t *testing.T, queries []retainedRecordQuerySQL, table string, limit, returned int) {
	t.Helper()
	require.NotEmpty(t, queries, "this is a real PostgreSQL observation, not a fake storage response")
	require.LessOrEqual(t, len(queries), 24, "fixed-family work must not grow into a query per group/record/witness")
	bodyQueries := 0
	for _, query := range queries {
		require.LessOrEqual(t, query.rows, int64(201), "native-ID/witness/validation materialization exceeds the finite window plus lookahead")
		statement := strings.ToLower(query.statement)
		from := strings.Index(statement, " from ")
		if from < 0 || (!strings.Contains(statement[from:], "from \""+table+"\"") && !strings.Contains(statement[from:], "from "+table+" ")) {
			continue
		}
		projection := statement[:from]
		if strings.Contains(projection, "count(") || (!strings.Contains(projection, "*") && !strings.Contains(projection, "content") && !strings.Contains(projection, "body")) {
			continue
		}
		bodyQueries++
		require.LessOrEqual(t, query.rows, int64(limit), "candidate selection must not load unconfirmed bodies")
	}
	require.LessOrEqual(t, bodyQueries, 1, "fetch the confirmed ID set once, not one body per record")
	if returned == 0 {
		require.Zero(t, bodyQueries, "an intermediate empty page must not fetch candidate bodies")
	}
}

// This oracle shares only the admitted PostgreSQL fixture; all wire shapes are test-owned.
func testRetainedRecordQueryPostgresHTTP(t *testing.T, ctx context.Context, db *gorm.DB, existing *Service) {
	t.Helper()
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "false")
	tx := db.WithContext(ctx).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	observer := &retainedRecordQueryObserver{Interface: logger.Default.LogMode(logger.Silent)}
	observed := tx.Session(&gorm.Session{Logger: observer})
	backing := &gormdb.Store{DB: observed}
	service := &Service{store: backing, vault: existing.vault, sessionStore: gormdb.NewSessionStore(backing), issueStore: gormdb.NewIssueStore(observed)}
	require.Nil(t, service.operatorCodeAdapter, "retained content must not require a browser chooser or code adapter")
	const selectorA, selectorB = "native-query-a", "native-query-b"
	canonical := uuid.NewString()
	require.NoError(t, tx.Create(&gormdb.Project{ID: canonical, LegacyIDs: []string{}}).Error)
	opaque := func(kind string, value any) string {
		key := service.vault.DeriveKey("project-consolidation-reference/v1")
		mac := hmac.New(sha256.New, key[:])
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = mac.Write(append([]byte(kind+"\x00"), raw...))
		require.NoError(t, err)
		return "urn:engram:migration:" + kind + ":" + hex.EncodeToString(mac.Sum(nil))
	}
	witness := func(table string, id, sourceID int64, path []string, selector string, project *string) gormdb.ConsolidationMembership {
		key, err := json.Marshal(map[string]int64{"id": id})
		require.NoError(t, err)
		sourceKey, err := json.Marshal(map[string]int64{"id": sourceID})
		require.NoError(t, err)
		fieldPath, err := json.Marshal(path)
		require.NoError(t, err)
		var nested []string
		if len(path) > 1 {
			nested = path[1:]
		}
		ref := opaque("row", struct {
			Table, Column string
			Key           gormdb.JSONRaw
			Path          []string
		}{table, path[0], gormdb.JSONRaw(sourceKey), nested})
		selectorRef := opaque("selector", selector)
		group := opaque("unresolved", selectorRef)
		if project != nil {
			group = ""
		}
		return gormdb.ConsolidationMembership{Reference: ref, Table: table, RowKey: gormdb.JSONRaw(key), SourceRowKey: gormdb.JSONRaw(sourceKey), FieldPath: gormdb.JSONRaw(fieldPath), SelectorReference: selectorRef, ProjectID: project, UnresolvedReference: group, Reason: "retained query fixture", PlanFingerprint: "native-query-plan"}
	}
	var members []gormdb.ConsolidationMembership
	add := func(table string, id int64, path []string, selector string, project *string) {
		members = append(members, witness(table, id, id, path, selector, project))
	}
	now := time.Now().UTC().Add(-time.Hour)
	memories := []gormdb.Memory{
		{ID: 1101, Project: selectorA, Content: "native-query memory A 100%_literal", Status: "active", Version: 3, PrivacyScope: "project", Tags: []string{"native-query-tag"}, SourceSessions: []string{}, SourceAgent: "fixture-agent", CreatedAt: now.Add(time.Hour), UpdatedAt: now},
		{ID: 1102, Project: selectorB, Content: "native-query memory B 100XXliteral", Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}, CreatedAt: now, UpdatedAt: now},
		{ID: 1103, Project: selectorA, Content: "native-query shared owned memory", Status: "active", Version: 1, PrivacyScope: "project", AgentVisibility: "shared", OwnerPrincipal: "query-owner", OwnerPrincipalKind: "agent", Tags: []string{}, SourceSessions: []string{}, CreatedAt: now, UpdatedAt: now},
		{ID: 1104, Project: selectorA, Content: "native-query canonical-only memory", Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}, CreatedAt: now, UpdatedAt: now},
		{ID: 1105, Project: selectorB, Content: "native-query private foreign sentinel", Status: "active", Version: 1, PrivacyScope: "private", SourceWorkstationID: "owner-device", AgentVisibility: "private", Domain: "native-private-domain", OwnerPrincipal: "query-owner", OwnerPrincipalKind: "agent", Tags: []string{}, SourceSessions: []string{}, CreatedAt: now, UpdatedAt: now},
		{ID: 1106, Project: selectorA, Content: "native-query deleted sentinel", Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}, DeletedAt: &now, CreatedAt: now, UpdatedAt: now},
	}
	require.NoError(t, tx.Create(&memories).Error)
	for _, memory := range memories {
		var project *string
		if memory.ID == 1104 {
			project = &canonical
		}
		add("memories", memory.ID, []string{"project"}, memory.Project, project)
	}
	members = append(members, witness("memories", 1103, 11103, []string{"project"}, selectorA, nil))
	documents := []gormdb.VersionedDocument{
		{ID: 2101, Project: selectorA, Path: "native-query.md", Version: 1, Content: "native-query document old", ContentHash: "old-hash", Metadata: `{"retained":true}`, DocType: "markdown", Author: "fixture-author", CreatedAt: now.Add(time.Hour)},
		{ID: 2102, Project: selectorA, Path: "native-query.md", Version: 2, Content: "native-query document new", ContentHash: "new-hash", Metadata: `{"retained":true}`, DocType: "markdown", Author: "fixture-author", CreatedAt: now},
		{ID: 2103, Project: selectorB, Path: "native-query.md", Version: 1, Content: "native-query same path foreign group", ContentHash: "other-hash", Metadata: "{}", DocType: "text", Author: "other-author", CreatedAt: now},
		{ID: 2104, Project: selectorA, Path: "native-query-other.md", Version: 1, Content: "native-query other path", ContentHash: "path-hash", Metadata: "{}", DocType: "text", Author: "fixture-author", CreatedAt: now},
		{ID: 2105, Project: selectorA, Path: "native-query-canonical.md", Version: 1, Content: "native-query canonical-only document", ContentHash: "mapped-hash", Metadata: "{}", DocType: "text", Author: "fixture-author", CreatedAt: now},
	}
	require.NoError(t, tx.Create(&documents).Error)
	for _, document := range documents {
		var project *string
		if document.ID == 2105 {
			project = &canonical
		}
		add("versioned_documents", document.ID, []string{"project"}, document.Project, project)
	}
	issues := []gormdb.Issue{
		{ID: 3101, Title: "native-query two groups", Body: "native-query issue body A", SourceProject: selectorA, TargetProject: selectorB, SourceAgent: "fixture-agent", Priority: "high", Labels: []string{"retained"}, CreatedAt: now.Add(time.Hour), UpdatedAt: now},
		{ID: 3102, Title: "native-query duplicate roles", Body: "native-query issue body B", SourceProject: selectorA, TargetProject: selectorA, Labels: []string{}, CreatedAt: now, UpdatedAt: now},
		{ID: 3103, Title: "native-query canonical-only issue", SourceProject: selectorA, TargetProject: selectorB, Labels: []string{}, CreatedAt: now, UpdatedAt: now},
		{ID: 3104, Title: "native-query mixed roles", Body: "native-query mixed body", SourceProject: selectorA, TargetProject: selectorB, Labels: []string{}, CreatedAt: now, UpdatedAt: now},
	}
	require.NoError(t, tx.Create(&issues).Error)
	for _, issue := range issues {
		for _, role := range []struct{ field, selector string }{{"source_project", issue.SourceProject}, {"target_project", issue.TargetProject}} {
			var project *string
			if issue.ID == 3103 || (issue.ID == 3104 && role.field == "source_project") {
				project = &canonical
			}
			add("issues", issue.ID, []string{role.field}, role.selector, project)
		}
	}
	require.NoError(t, tx.Create(&members).Error)
	memoryID := int64(1101)
	require.NoError(t, tx.Create(&gormdb.AuditLogEntry{MemoryID: &memoryID, Action: "created", Actor: "fixture", CreatedAt: now}).Error)
	sharedID := int64(1103)
	require.NoError(t, tx.Create(&gormdb.AuditLogEntry{MemoryID: &sharedID, Action: "created", Actor: "fixture", CreatedAt: now}).Error)
	privateID := int64(1105)
	require.NoError(t, tx.Create(&gormdb.AuditLogEntry{MemoryID: &privateID, Action: "created", Actor: "fixture", CreatedAt: now}).Error)
	require.NoError(t, tx.Create(&gormdb.IssueComment{IssueID: 3101, AuthorProject: "historical-comment-author", AuthorAgent: "fixture", Body: "native-query comment history", CreatedAt: now}).Error)
	require.NoError(t, tx.Create(&gormdb.IssueComment{IssueID: 3102, AuthorProject: "historical-comment-author", AuthorAgent: "fixture", Body: "native-query comment history", CreatedAt: now}).Error)
	firstReference := func(table string, id int64) string {
		var eligible []gormdb.ConsolidationMembership
		for _, member := range members {
			var key struct {
				ID int64 `json:"id"`
			}
			require.NoError(t, json.Unmarshal(member.RowKey, &key))
			if member.Table == table && key.ID == id && member.ProjectID == nil {
				eligible = append(eligible, member)
			}
		}
		require.NotEmpty(t, eligible)
		sort.Slice(eligible, func(i, j int) bool { return eligible[i].Reference < eligible[j].Reference })
		return eligible[0].UnresolvedReference
	}
	gate, err := NewTokenAuth("retained-query-synthetic-token")
	require.NoError(t, err)
	router := chi.NewRouter()
	router.Use(gate.Middleware)
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity := auth.ClientWithPrincipal("read-only", "owner-device", "query-owner", auth.PrincipalKindAgent)
			switch r.Header.Get("X-Fixture-Caller") {
			case "foreign":
				identity = auth.ClientWithPrincipal("read-only", "foreign-device", "foreign-reader", auth.PrincipalKindAgent)
			case "hap":
				identity = auth.ClientWithPrincipal("read-write", "hap-device", auth.ProjectServicePrincipal(canonical), auth.PrincipalKindService)
			case "noauth":
				identity = auth.AuthDisabled()
			}
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
		})
	})
	router.Mount("/", retainedDiscoveryRouter(service))
	server := httptest.NewServer(router)
	defer server.Close()
	get := func(t *testing.T, path, caller string, status int) map[string]any {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
		require.NoError(t, err)
		if caller != "anonymous" {
			request.Header.Set("Authorization", "Bearer "+gate.Token())
		}
		request.Header.Set("X-Fixture-Caller", caller)
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, status, response.StatusCode, string(raw))
		if status != http.StatusUnauthorized {
			require.Equal(t, "private, no-store", response.Header.Get("Cache-Control"), "success and handler refusal/error pages must not be cached")
		}
		result := map[string]any{}
		if json.Valid(raw) {
			require.NoError(t, json.Unmarshal(raw, &result))
		} else {
			require.GreaterOrEqual(t, status, 400, string(raw))
		}
		if status >= 400 {
			for _, marker := range []string{"native-query issue body", "native-query document", "private foreign sentinel", "SELECT ", "project_consolidation_memberships", "injected retained storage failure"} {
				require.NotContains(t, string(raw), marker)
			}
			require.Empty(t, result["items"], "an error cannot return a partial page")
		}
		return result
	}
	query := func(kind, text string, limit int) string {
		return "/api/projects?view=retained_records&attribution_scope=unresolved_history&kind=" + kind + "&q=" + url.QueryEscape(text) + "&limit=" + fmt.Sprint(limit)
	}
	tables := map[string]string{"memory": "memories", "document": "versioned_documents", "issue": "issues"}
	traverse := func(t *testing.T, kind, text, caller string, limit int) []retainedRecordQueryItem {
		t.Helper()
		base := query(kind, text, limit)
		path := base
		seenCursors := map[string]bool{}
		seenIDs := map[int64]bool{}
		var all []retainedRecordQueryItem
		for pageNumber := 0; ; pageNumber++ {
			require.Less(t, pageNumber, 32, "bounded fixture traversal must make progress")
			observer.drain()
			response := get(t, path, caller, http.StatusOK)
			items, cursor := retainedRecordQueryPage(t, response, kind)
			require.LessOrEqual(t, len(items), limit)
			retainedRecordQueryBudget(t, observer.drain(), tables[kind], limit, len(items))
			for _, item := range items {
				require.False(t, seenIDs[item.ID], "duplicate native ID across witnesses/roles/pages")
				if len(all) > 0 {
					require.Greater(t, all[len(all)-1].ID, item.ID, "native ID DESC, not created_at or group order")
				}
				seenIDs[item.ID] = true
				all = append(all, item)
			}
			if cursor == nil {
				return all
			}
			require.False(t, seenCursors[*cursor], "a continuation must advance, even on an empty page")
			seenCursors[*cursor] = true
			path = base + "&cursor=" + url.QueryEscape(*cursor)
		}
	}
	t.Run("native-payloads-unique-desc-and-no-view-catalog", func(t *testing.T) {
		before := get(t, "/api/projects", "owner", http.StatusOK)
		for _, family := range []struct {
			kind string
			ids  []int64
		}{
			{"memory", []int64{1105, 1103, 1102, 1101}},
			{"document", []int64{2104, 2103, 2102, 2101}},
			{"issue", []int64{3104, 3102, 3101}},
		} {
			t.Run(family.kind, func(t *testing.T) {
				items := traverse(t, family.kind, "native-query", "owner", 1)
				var ids []int64
				for _, item := range items {
					ids = append(ids, item.ID)
					require.Equal(t, firstReference(tables[family.kind], item.ID), item.Reference, "first valid association is not a guessed owner")
					switch family.kind {
					case "memory":
						require.NotEmpty(t, item.Record["content"])
						require.Equal(t, "active", item.Record["status"])
						require.Contains(t, item.Record, "privacy_scope")
						if item.ID == 1101 {
							require.Equal(t, memories[0].Content, item.Record["content"])
							require.EqualValues(t, 3, item.Record["version"])
							require.Equal(t, []any{"native-query-tag"}, item.Record["tags"])
							require.Equal(t, "fixture-agent", item.Record["source_agent"])
						}
					case "document":
						document := documents[item.ID-2101]
						for key, value := range map[string]any{"content": document.Content, "path": document.Path, "content_hash": document.ContentHash, "doc_type": document.DocType, "author": document.Author} {
							require.Equal(t, value, item.Record[key])
						}
						metadata, ok := item.Record["metadata"].(string)
						require.True(t, ok, "native document metadata remains a JSON string")
						// PostgreSQL JSONB preserves values, not the fixture's input whitespace.
						require.JSONEq(t, document.Metadata, metadata)
						require.EqualValues(t, document.Version, item.Record["version"])
					case "issue":
						issue := issues[item.ID-3101]
						require.Equal(t, issue.Title, item.Record["title"])
						require.Equal(t, issue.Body, item.Record["body"])
						require.Equal(t, issue.SourceProject, item.Record["source_project"])
						require.Equal(t, issue.TargetProject, item.Record["target_project"])
						require.Contains(t, item.Record, "project_origin")
						if item.ID == 3104 {
							require.Equal(t, canonical, item.Record["source_project_id"], "a canonical source role must not exclude a separate valid unresolved target role")
						}
					}
				}
				require.Equal(t, family.ids, ids)
			})
		}
		require.Equal(t, before, get(t, "/api/projects", "owner", http.StatusOK), "explicit record discovery must not replace or mutate the no-view catalog")
	})
	t.Run("literal-search-normalized-query-and-family-authority", func(t *testing.T) {
		items := traverse(t, "memory", "  100%_literal  ", "owner", 200)
		require.Len(t, items, 1, "SQL wildcard characters retain literal reader semantics")
		require.EqualValues(t, 1101, items[0].ID)
		foreign := traverse(t, "memory", "native-query", "foreign", 200)
		var ids []int64
		for _, item := range foreign {
			ids = append(ids, item.ID)
			require.NotContains(t, fmt.Sprint(item.Record), "private foreign sentinel")
		}
		require.Equal(t, []int64{1103, 1102, 1101}, ids)
		for _, kind := range []string{"document", "issue"} {
			require.NotEmpty(t, traverse(t, kind, "native-query", "foreign", 200), "ordinary TokenAuth/HAP route + exact membership is the actual authority, not an invented per-row principal ACL")
		}
		for _, caller := range []string{"owner", "noauth"} {
			require.NotEmpty(t, traverse(t, "document", "native-query", caller, 200), "no code/browser chooser dependency")
		}
	})
	t.Run("selected-body-history-and-return-binding", func(t *testing.T) {
		for _, selection := range []struct {
			kind string
			id   int64
		}{{"memory", 1103}, {"document", 2102}, {"issue", 3102}} {
			t.Run(selection.kind, func(t *testing.T) {
				search := query(selection.kind, "native-query", 1)
				first := get(t, search, "owner", http.StatusOK)
				_, savedCursor := retainedRecordQueryPage(t, first, selection.kind)
				var selected retainedRecordQueryItem
				for _, item := range traverse(t, selection.kind, "native-query", "owner", 1) {
					if item.ID == selection.id {
						selected = item
					}
				}
				require.Equal(t, selection.id, selected.ID)
				prefix := "/api/projects/unresolved/" + url.PathEscape(selected.Reference)
				args := "?attribution_scope=unresolved_history&kind=" + selection.kind + "&id=" + fmt.Sprint(selected.ID) + "&limit=1"
				body := get(t, prefix+"/content"+args, "owner", http.StatusOK)
				require.Equal(t, "unresolved_history", body["attribution_scope"])
				require.Equal(t, selected.Reference, body["reference"])
				require.Equal(t, selection.kind, body["kind"])
				require.EqualValues(t, selected.ID, body["id"])
				bodyItems, ok := body["items"].([]any)
				require.True(t, ok)
				require.Len(t, bodyItems, 1)
				require.Equal(t, selected.Record, bodyItems[0], "selected content keeps the genuine family payload and historical projection")
				historyPath := prefix + "/history" + args
				var historyIDs []int64
				for page := 0; ; page++ {
					require.Less(t, page, 4)
					history := get(t, historyPath, "owner", http.StatusOK)
					require.Equal(t, "unresolved_history", history["attribution_scope"])
					require.Equal(t, selected.Reference, history["reference"])
					require.EqualValues(t, selected.ID, history["id"])
					entries, ok := history["items"].([]any)
					require.True(t, ok)
					require.Len(t, entries, 1)
					entry := entries[0].(map[string]any)
					require.NotContains(t, entry, "project_key")
					switch selection.kind {
					case "memory":
						require.Equal(t, "created", entry["action"])
					case "issue":
						require.Equal(t, "native-query comment history", entry["body"])
					case "document":
						historyIDs = append(historyIDs, int64(entry["id"].(float64)))
					}
					cursor, exists := history["next_cursor"]
					require.True(t, exists)
					if cursor == nil {
						break
					}
					historyPath = prefix + "/history" + args + "&cursor=" + url.QueryEscape(cursor.(string))
				}
				if selection.kind == "document" {
					require.ElementsMatch(t, []int64{2101, 2102}, historyIDs, "history retains the validated group IDs/path/project, not just the selected version or another group's same path")
					firstHistory := get(t, prefix+"/history"+args, "owner", http.StatusOK)
					historyCursor, ok := firstHistory["next_cursor"].(string)
					require.True(t, ok)
					get(t, prefix+"/history"+strings.Replace(args, "id=2102", "id=2101", 1)+"&cursor="+url.QueryEscape(historyCursor), "owner", http.StatusBadRequest)
					allVersions := get(t, prefix+"/content?attribution_scope=unresolved_history&kind=document", "owner", http.StatusOK)
					require.EqualValues(t, 3, allVersions["total"], "omitting id preserves the old whole-group content behavior")
					legacyVersions := get(t, prefix+"/content?kind=document", "owner", http.StatusOK)
					require.EqualValues(t, 3, legacyVersions["total"])
				}
				returned := get(t, search, "owner", http.StatusOK)
				for _, field := range []string{"view", "attribution_scope", "kind", "items"} {
					require.Equal(t, first[field], returned[field], "opening a payload must not change the saved search/results")
				}
				if savedCursor != nil {
					continued := get(t, search+"&cursor="+url.QueryEscape(*savedCursor), "owner", http.StatusOK)
					items, _ := retainedRecordQueryPage(t, continued, selection.kind)
					require.NotEmpty(t, items, "the saved search continuation survives body/history navigation")
				}
				foreignRef := opaque("unresolved", opaque("selector", selectorB))
				if selected.Reference == foreignRef {
					foreignRef = opaque("unresolved", opaque("selector", selectorA))
				}
				if selection.kind != "issue" {
					for _, reader := range []string{"content", "history"} {
						get(t, "/api/projects/unresolved/"+url.PathEscape(foreignRef)+"/"+reader+args, "owner", http.StatusNotFound)
					}
				}
				for _, bad := range []string{strings.Replace(args, "unresolved_history", "global", 1), args + "&attribution_scope=unresolved_history", args + "&project_key=" + canonical, strings.Replace(args, "id="+fmt.Sprint(selected.ID), "id=0", 1)} {
					get(t, prefix+"/content"+bad, "owner", http.StatusBadRequest)
					get(t, prefix+"/history"+bad, "owner", http.StatusBadRequest)
				}
			})
		}
		privatePrefix := "/api/projects/unresolved/" + url.PathEscape(firstReference("memories", 1105))
		privateBody := get(t, privatePrefix+"/content?attribution_scope=unresolved_history&kind=memory&id=1105", "foreign", http.StatusOK)
		require.Empty(t, privateBody["items"])
		get(t, privatePrefix+"/history?attribution_scope=unresolved_history&kind=memory&id=1105", "foreign", http.StatusNotFound)
		privateHistory := get(t, privatePrefix+"/history?attribution_scope=unresolved_history&kind=memory&id=1105", "owner", http.StatusOK)
		require.NotEmpty(t, privateHistory["items"], "the matching owner retains the existing domain-manage history authority")
		sharedPrefix := "/api/projects/unresolved/" + url.PathEscape(firstReference("memories", 1103))
		sharedHistory := get(t, sharedPrefix+"/history?attribution_scope=unresolved_history&kind=memory&id=1103", "foreign", http.StatusOK)
		require.NotEmpty(t, sharedHistory["items"], "an empty domain preserves the actual legacy manage policy; ownership metadata alone is not a new ACL")
		for _, reader := range []string{"content", "history"} {
			groupB := opaque("unresolved", opaque("selector", selectorB))
			get(t, "/api/projects/unresolved/"+url.PathEscape(groupB)+"/"+reader+"?attribution_scope=unresolved_history&kind=issue&id=3102", "owner", http.StatusNotFound)
		}
	})
	t.Run("closed-query-and-authenticated-cursor-refusal-before-data", func(t *testing.T) {
		base := query("memory", "native-query", 1)
		first := get(t, base, "owner", http.StatusOK)
		_, cursor := retainedRecordQueryPage(t, first, "memory")
		require.NotNil(t, cursor)
		continued := base + "&cursor=" + url.QueryEscape(*cursor)
		regular, _ := retainedRecordQueryPage(t, get(t, continued, "owner", http.StatusOK), "memory")
		normalized := query("memory", " \t native-query \n ", 1) + "&cursor=" + url.QueryEscape(*cursor)
		trimmed, _ := retainedRecordQueryPage(t, get(t, normalized, "owner", http.StatusOK), "memory")
		require.Equal(t, regular, trimmed, "cursor binding uses the normalized query")
		for _, invalid := range []string{
			"/api/projects?view=retained_records&kind=memory",
			strings.Replace(base, "unresolved_history", "current", 1),
			strings.Replace(base, "retained_records", "other", 1),
			strings.Replace(base, "kind=memory", "kind=code", 1),
			strings.Replace(base, "kind=memory", "kind=credential", 1),
			strings.Replace(base, "kind=memory", "kind=native", 1),
			strings.Replace(base, "kind=memory", "kind=", 1),
			strings.Replace(base, "&kind=memory", "", 1),
			base + "&view=retained_records", base + "&attribution_scope=unresolved_history", base + "&kind=memory",
			base + "&q=again", base + "&limit=1", continued + "&cursor=" + url.QueryEscape(*cursor),
			base + "&project=" + canonical, base + "&project_id=" + canonical, base + "&project_key=" + canonical,
			base + "&reference=guessed", base + "&offset=1", base + "&unknown=value",
			query("memory", strings.Repeat("é", 513), 1),
			strings.Replace(base, "q=native-query", "q=%ff", 1),
			strings.Replace(base, "q=native-query", "q=%ZZ", 1),
			query("memory", "native-query", 0), query("memory", "native-query", -1), query("memory", "native-query", 201),
			strings.Replace(base, "limit=1", "limit=abc", 1),
		} {
			observer.drain()
			refused := get(t, invalid, "owner", http.StatusBadRequest)
			require.Equal(t, "INVALID_QUERY", refused["error"])
			require.Empty(t, observer.drain(), "closed parsing must refuse before any catalog/native/membership query")
		}
		for _, changed := range []struct{ path, caller string }{
			{strings.Replace(continued, "q=native-query", "q=other", 1), "owner"},
			{strings.Replace(continued, "limit=1", "limit=2", 1), "owner"},
			{strings.Replace(continued, "kind=memory", "kind=document", 1), "owner"},
			{continued, "foreign"},
			{continued, "noauth"},
		} {
			observer.drain()
			refused := get(t, changed.path, changed.caller, http.StatusBadRequest)
			require.Equal(t, "INVALID_CURSOR", refused["error"])
			require.Empty(t, observer.drain(), "query/caller binding is authenticated before data work")
		}
		tampered := []byte(*cursor)
		if tampered[0] == 'A' {
			tampered[0] = 'B'
		} else {
			tampered[0] = 'A'
		}
		checksum := sha256.Sum256([]byte(base))
		forged, err := json.Marshal(map[string]any{"b": hex.EncodeToString(checksum[:]), "o": 1})
		require.NoError(t, err)
		for _, token := range []string{string(tampered), base64.RawURLEncoding.EncodeToString(forged), "not-a-server-issued-cursor"} {
			observer.drain()
			refused := get(t, base+"&cursor="+url.QueryEscape(token), "owner", http.StatusBadRequest)
			require.Equal(t, "INVALID_CURSOR", refused["error"])
			require.Empty(t, observer.drain(), "a public checksum is not an authenticated continuation")
		}
		observer.drain()
		get(t, base, "hap", http.StatusForbidden)
		require.Empty(t, observer.drain())
		get(t, base, "anonymous", http.StatusUnauthorized)
		for _, kind := range []string{"memory", "document", "issue"} {
			require.Empty(t, traverse(t, kind, strings.Repeat("é", 512), "owner", 200), "1024 UTF-8 bytes is valid; null is returned only after all search work is exhausted")
			defaultPage := get(t, "/api/projects?view=retained_records&attribution_scope=unresolved_history&kind="+kind, "owner", http.StatusOK)
			items, _ := retainedRecordQueryPage(t, defaultPage, kind)
			require.LessOrEqual(t, len(items), 50)
		}
	})
	t.Run("stale-forged-witnesses-do-not-poison-valid-discovery", func(t *testing.T) {
		var invalidMemories []gormdb.Memory
		var invalidMembers []gormdb.ConsolidationMembership
		for id := int64(1201); id <= 1206; id++ {
			invalidMemories = append(invalidMemories, gormdb.Memory{ID: id, Project: selectorA, Content: "witness-only retained payload", Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}})
			member := witness("memories", id, id, []string{"project"}, selectorA, nil)
			switch id {
			case 1201:
				member.PlanFingerprint = ""
				member.Reference = "urn:engram:migration:row:" + strings.Repeat("0", 64)
			case 1202:
				member = witness("memories", id, id, []string{"project"}, selectorB, nil)
			case 1203:
				member.Reference += "0"
			case 1204:
				member.UnresolvedReference = opaque("unresolved", opaque("selector", selectorB))
			case 1205:
				member.SourceRowKey = gormdb.JSONRaw(`{"id":999999}`)
			case 1206:
				member.RowKey = gormdb.JSONRaw(`{"id":"1206"}`)
			}
			invalidMembers = append(invalidMembers, member)
		}
		valid := witness("memories", 1201, 12101, []string{"project"}, selectorA, nil)
		require.Less(t, invalidMembers[0].Reference, valid.Reference)
		invalidMembers = append(invalidMembers, valid)
		require.NoError(t, tx.Create(&invalidMemories).Error)
		require.NoError(t, tx.Create(&invalidMembers).Error)
		defer func() {
			require.NoError(t, tx.Where("table_name = ? AND (row_key->>'id')::bigint BETWEEN ? AND ?", "memories", 1201, 1206).Delete(&gormdb.ConsolidationMembership{}).Error)
			require.NoError(t, tx.Where("id BETWEEN ? AND ?", 1201, 1206).Delete(&gormdb.Memory{}).Error)
		}()
		items := traverse(t, "memory", "witness-only", "owner", 1)
		require.Len(t, items, 1)
		require.EqualValues(t, 1201, items[0].ID)
		require.Equal(t, valid.UnresolvedReference, items[0].Reference)
	})
	t.Run("selected-stale-binding-never-repairs-from-another-group", func(t *testing.T) {
		items := traverse(t, "issue", "native-query two groups", "owner", 1)
		require.Len(t, items, 1)
		selected := items[0]
		var selectedMember gormdb.ConsolidationMembership
		for _, member := range members {
			if member.Table == "issues" && member.UnresolvedReference == selected.Reference && string(member.RowKey) == `{"id":3101}` {
				selectedMember = member
				break
			}
		}
		require.NotEmpty(t, selectedMember.Reference)
		require.NoError(t, tx.Model(&gormdb.ConsolidationMembership{}).Where("reference = ?", selectedMember.Reference).Update("plan_fingerprint", "").Error)
		defer func() {
			require.NoError(t, tx.Model(&gormdb.ConsolidationMembership{}).Where("reference = ?", selectedMember.Reference).Update("plan_fingerprint", selectedMember.PlanFingerprint).Error)
		}()
		for _, reader := range []string{"content", "history"} {
			path := "/api/projects/unresolved/" + url.PathEscape(selected.Reference) + "/" + reader + "?attribution_scope=unresolved_history&kind=issue&id=3101"
			refused := get(t, path, "owner", http.StatusNotFound)
			require.Equal(t, "PROJECT_ADOPTION_STALE_REFERENCE", refused["error"])
		}
		reassociated := traverse(t, "issue", "native-query two groups", "owner", 1)
		require.Len(t, reassociated, 1)
		require.Equal(t, selected.ID, reassociated[0].ID)
		require.NotEqual(t, selected.Reference, reassociated[0].Reference, "discovery may issue the other valid association, but selected reads cannot repair the old one")
	})
	t.Run("native-window-empty-page-keeps-valid-lower-id", func(t *testing.T) {
		var rows []gormdb.Memory
		for id := int64(1300); id <= 1501; id++ {
			rows = append(rows, gormdb.Memory{ID: id, Project: selectorA, Content: "native-window-only payload", Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}})
		}
		require.NoError(t, tx.CreateInBatches(&rows, 200).Error)
		member := witness("memories", 1300, 1300, []string{"project"}, selectorA, nil)
		require.NoError(t, tx.Create(&member).Error)
		base := query("memory", "native-window-only", 1)
		observer.drain()
		first, cursor := retainedRecordQueryPage(t, get(t, base, "owner", http.StatusOK), "memory")
		require.Empty(t, first)
		require.NotNil(t, cursor, "a short/empty page is not exhaustion while a lower native window remains")
		retainedRecordQueryBudget(t, observer.drain(), "memories", 1, 0)
		var found []int64
		seen := map[string]bool{}
		for page := 0; cursor != nil; page++ {
			require.Less(t, page, 8)
			require.False(t, seen[*cursor])
			seen[*cursor] = true
			observer.drain()
			items, next := retainedRecordQueryPage(t, get(t, base+"&cursor="+url.QueryEscape(*cursor), "owner", http.StatusOK), "memory")
			retainedRecordQueryBudget(t, observer.drain(), "memories", 1, len(items))
			for _, item := range items {
				found = append(found, item.ID)
			}
			cursor = next
		}
		require.Equal(t, []int64{1300}, found)
	})
	t.Run("201-invalid-witnesses-resume-before-valid-and-lower-id", func(t *testing.T) {
		rows := []gormdb.Memory{
			{ID: 1601, Project: selectorA, Content: "witness-budget-only pending payload", Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}},
			{ID: 1600, Project: selectorB, Content: "witness-budget-only lower payload", Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}},
		}
		require.NoError(t, tx.Create(&rows).Error)
		valid := witness("memories", 1601, 1601, []string{"project"}, selectorA, nil)
		var witnesses []gormdb.ConsolidationMembership
		for sourceID := int64(20000); len(witnesses) < 201; sourceID++ {
			require.Less(t, sourceID, int64(100000), "fixture must deterministically construct 201 earlier witnesses")
			invalid := witness("memories", 1601, sourceID, []string{"project"}, selectorA, nil)
			if invalid.Reference < valid.Reference {
				invalid.PlanFingerprint = ""
				witnesses = append(witnesses, invalid)
			}
		}
		witnesses = append(witnesses, valid, witness("memories", 1600, 1600, []string{"project"}, selectorB, nil))
		require.NoError(t, tx.CreateInBatches(&witnesses, 200).Error)
		base := query("memory", "witness-budget-only", 1)
		observer.drain()
		first, cursor := retainedRecordQueryPage(t, get(t, base, "owner", http.StatusOK), "memory")
		require.Empty(t, first)
		require.NotNil(t, cursor, "unconfirmed pending native ID must survive the 200-witness budget")
		retainedRecordQueryBudget(t, observer.drain(), "memories", 1, 0)
		var ids []int64
		seen := map[string]bool{}
		for page := 0; cursor != nil; page++ {
			require.Less(t, page, 8)
			require.False(t, seen[*cursor])
			seen[*cursor] = true
			observer.drain()
			items, next := retainedRecordQueryPage(t, get(t, base+"&cursor="+url.QueryEscape(*cursor), "owner", http.StatusOK), "memory")
			retainedRecordQueryBudget(t, observer.drain(), "memories", 1, len(items))
			for _, item := range items {
				ids = append(ids, item.ID)
			}
			cursor = next
		}
		require.Equal(t, []int64{1601, 1600}, ids, "neither the valid 202nd witness nor the lower native ID may be skipped or repeated")
	})
	t.Run("201-confirmed-witnesses-keep-lower-native-id", func(t *testing.T) {
		rows := []gormdb.Memory{
			{ID: 1701, Project: selectorA, Content: "confirmed-witness-budget-only higher payload", Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}},
			{ID: 1700, Project: selectorB, Content: "confirmed-witness-budget-only lower payload", Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}},
		}
		require.NoError(t, tx.Create(&rows).Error)
		var witnesses []gormdb.ConsolidationMembership
		for sourceID := int64(50000); sourceID < 50201; sourceID++ {
			witnesses = append(witnesses, witness("memories", 1701, sourceID, []string{"project"}, selectorA, nil))
		}
		witnesses = append(witnesses, witness("memories", 1700, 1700, []string{"project"}, selectorB, nil))
		require.NoError(t, tx.CreateInBatches(&witnesses, 200).Error)
		items := traverse(t, "memory", "confirmed-witness-budget-only", "owner", 200)
		var ids []int64
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		require.Equal(t, []int64{1701, 1700}, ids, "lookahead for an already-confirmed higher ID must not turn unprocessed lower IDs into false exhaustion")
	})
	t.Run("vault-and-store-failures-are-unavailable-not-terminal-empty", func(t *testing.T) {
		base := query("memory", "native-query", 1)
		first := get(t, base, "owner", http.StatusOK)
		_, cursor := retainedRecordQueryPage(t, first, "memory")
		require.NotNil(t, cursor)
		withoutVault := &Service{store: backing, sessionStore: service.sessionStore, issueStore: service.issueStore}
		request := httptest.NewRequest(http.MethodGet, base, nil)
		request = request.WithContext(auth.WithIdentity(ctx, auth.AuthDisabled()))
		response := httptest.NewRecorder()
		retainedDiscoveryRouter(withoutVault).ServeHTTP(response, request)
		require.Equal(t, http.StatusServiceUnavailable, response.Code)
		require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
		var unavailable map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &unavailable))
		require.Equal(t, "RETAINED_RECORDS_UNAVAILABLE", unavailable["error"])
		require.Empty(t, unavailable["items"])
		name := "retained_record_query_failure_" + uuid.NewString()
		fail := func(db *gorm.DB) { db.AddError(errors.New("injected retained storage failure")) }
		queryCallbacks, rowCallbacks := observed.Callback().Query(), observed.Callback().Row()
		require.NoError(t, queryCallbacks.Before("gorm:query").Register(name, fail))
		defer queryCallbacks.Remove(name)
		require.NoError(t, rowCallbacks.Before("gorm:row").Register(name, fail))
		defer rowCallbacks.Remove(name)
		for _, path := range []string{base, base + "&cursor=" + url.QueryEscape(*cursor)} {
			failed := get(t, path, "owner", http.StatusServiceUnavailable)
			require.Equal(t, "RETAINED_RECORDS_UNAVAILABLE", failed["error"])
			require.NotContains(t, failed, "next_cursor", "an operational failure must not masquerade as exhausted traversal")
		}
	})
}
