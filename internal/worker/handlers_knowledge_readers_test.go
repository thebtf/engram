package worker

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
	"modernc.org/sqlite"
)

func useKnowledgeTestProject(service *Service, project string) {
	service.knowledgeProjectExistsSeam = func(_ context.Context, id string) (bool, error) { return id == project, nil }
}

func decodeKnowledgeItems[T any](t *testing.T, response *httptest.ResponseRecorder) []T {
	t.Helper()
	var page struct {
		Items []T `json:"items"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.NotNil(t, page.Items)
	return page.Items
}

// SQLite executes the real handlers and row queries. Only PostgreSQL array/JSON
// containment syntax is lowered here; PostgreSQL operator behavior is NOT_PROVEN.
type (
	knowledgeSQLiteDriver struct{ driver.Driver }
	knowledgeSQLiteConn   struct{ driver.Conn }
)

func (d *knowledgeSQLiteDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.Driver.Open(name)
	if err != nil {
		return nil, err
	}
	return knowledgeSQLiteConn{conn}, nil
}

var (
	knowledgeJSONContainment   = regexp.MustCompile(`m\.tags @> (\$[0-9]+)::jsonb`)
	knowledgeAliasContainment  = regexp.MustCompile(`COALESCE\(legacy_ids, ARRAY\[\]::TEXT\[\]\) @> ARRAY\[(\$[0-9]+)\]::TEXT\[\]`)
	knowledgeSessionMembership = regexp.MustCompile(`(\$[0-9]+) = ANY\(m\.source_sessions\)`)
)

func (c knowledgeSQLiteConn) Prepare(query string) (driver.Stmt, error) {
	query = knowledgeAliasContainment.ReplaceAllString(query, `knowledge_has_alias(legacy_ids, $1)`)
	query = knowledgeJSONContainment.ReplaceAllString(query, `knowledge_has_tag(m.tags, $1)`)
	query = knowledgeSessionMembership.ReplaceAllString(query, `knowledge_has_alias(m.source_sessions, $1)`)
	return c.Conn.Prepare(query)
}

var knowledgeSQLiteFunctions sync.Once

func knowledgeSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	knowledgeSQLiteFunctions.Do(func() {
		registered, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		sql.Register("knowledge-sqlite", &knowledgeSQLiteDriver{Driver: registered.Driver()})
		require.NoError(t, registered.Close())
		require.NoError(t, sqlite.RegisterScalarFunction("knowledge_has_alias", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			var aliases pq.StringArray
			if err := aliases.Scan(args[0]); err != nil {
				return nil, err
			}
			for _, alias := range aliases {
				if alias == args[1] {
					return int64(1), nil
				}
			}
			return int64(0), nil
		}))
		require.NoError(t, sqlite.RegisterScalarFunction("knowledge_has_tag", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			var tags models.JSONStringArray
			var wanted []string
			if args[0] == nil {
				return int64(0), nil
			}
			if err := tags.Scan(args[0]); err != nil {
				return nil, err
			}
			if err := json.Unmarshal([]byte(args[1].(string)), &wanted); err != nil {
				return nil, err
			}
			for _, tag := range tags {
				if len(wanted) == 1 && tag == wanted[0] {
					return int64(1), nil
				}
			}
			return int64(0), nil
		}))
		require.NoError(t, sqlite.RegisterScalarFunction("now", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			return time.Now().UTC().Format("2006-01-02 15:04:05"), nil
		}))
		require.NoError(t, sqlite.RegisterScalarFunction("btrim", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			if args[0] == nil {
				return nil, nil
			}
			return strings.TrimSpace(args[0].(string)), nil
		}))
		// No PostgreSQL operator/session semantics are certified. Fail if an
		// unrecognized ANY expression escapes the explicit fixture lowering.
		require.NoError(t, sqlite.RegisterScalarFunction("any", 1, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			return nil, fmt.Errorf("PostgreSQL array membership is outside the SQLite fixture")
		}))
	})
	sqlDB, err := sql.Open("knowledge-sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	// ponytail: one SQLite connection; PostgreSQL locking/collation is a separate DB gate.
	sqlDB.SetMaxOpenConns(1)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, model := range []any{gormdb.Memory{}, gormdb.Project{}, gormdb.VersionedDocument{}, gormdb.AuditLogEntry{}} {
		parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		require.NoError(t, err)
		columns := make([]string, 0, len(parsed.Fields))
		for _, field := range parsed.Fields {
			kind := "TEXT"
			switch field.DataType {
			case schema.Int, schema.Uint:
				kind = "INTEGER"
			case schema.Float:
				kind = "REAL"
			case schema.Bool:
				kind = "BOOLEAN"
			case schema.Time, schema.DataType("timestamptz"):
				kind = "TIMESTAMP"
			}
			if field.PrimaryKey {
				kind += " PRIMARY KEY"
			}
			columns = append(columns, fmt.Sprintf("%q %s", field.DBName, kind))
		}
		require.NoError(t, db.Exec(fmt.Sprintf("CREATE TABLE %q (%s)", parsed.Table, strings.Join(columns, ","))).Error)
	}
	require.NoError(t, db.Exec("PRAGMA case_sensitive_like = ON").Error)
	return db
}

type knowledgeFixtureTokens []gormdb.APIToken

func (tokens knowledgeFixtureTokens) FindByPrefix(_ context.Context, prefix string) ([]gormdb.APIToken, error) {
	var candidates []gormdb.APIToken
	for _, token := range tokens {
		if token.TokenPrefix == prefix {
			candidates = append(candidates, token)
		}
	}
	return candidates, nil
}

func TestKnowledgeReadersHTTP_SQLite(t *testing.T) {
	runKnowledgeReadersHTTP(t, knowledgeSQLiteDB(t), true)
}

func TestKnowledgeReadersHTTP_Postgres(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("isolated DATABASE_DSN unavailable: PostgreSQL reader semantics NOT_PROVEN")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	tx := store.DB.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { require.NoError(t, tx.Rollback().Error) })
	runKnowledgeReadersHTTP(t, tx, false)
}

func runKnowledgeReadersHTTP(t *testing.T, db *gorm.DB, sqliteFixture bool) {
	t.Helper()
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	projectA, projectB, removed, merged, unlabelled := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	memoryOnly, documentOnly := uuid.NewString(), uuid.NewString()
	aliases := []string{"legacy-reader-alias", "ambiguous-reader-alias", projectB, removed, "dead-owner-alias"}
	otherAliases := []string{"ambiguous-reader-alias"}
	now := time.Now().UTC().Add(-time.Hour)
	for _, row := range []gormdb.Project{
		{ID: projectA, DisplayName: sql.NullString{String: "Same human label", Valid: true}, LegacyIDs: aliases},
		{ID: projectB, DisplayName: sql.NullString{String: "Same human label", Valid: true}, LegacyIDs: otherAliases},
		{ID: removed, RemovedAt: &now, LegacyIDs: []string{"dead-owner-alias"}},
		{ID: merged, IdentityStatus: sql.NullString{String: "merged", Valid: true}},
		{ID: unlabelled},
		{ID: memoryOnly, LegacyIDs: []string{"legacy-memory-only"}},
		{ID: documentOnly, LegacyIDs: []string{"legacy-document-only"}},
	} {
		require.NoError(t, db.Create(&row).Error)
	}

	memories := make([]gormdb.Memory, 501)
	for i := range memories {
		memories[i] = gormdb.Memory{Project: projectA, Content: fmt.Sprintf("Known decision %03d", i), Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}, CreatedAt: now, UpdatedAt: now, SourceAgent: "reader-fixture"}
		memories[i].EpistemicType = "fact"
		if i%2 != 0 {
			memories[i].EpistemicType = "experience"
		}
	}
	memories[0].Content = `Known decision 100%_safe\route`
	memories[0].PrivacyScope, memories[0].SourceWorkstationID = "private", "fixture-card-a"
	memories[0].AgentVisibility, memories[0].OwnerPrincipal, memories[0].OwnerPrincipalKind = "private", "agent/local-a", "agent"
	memories[0].Domain = "reader-domain"
	memories[0].Confidence, memories[0].Tags = 0.5, []string{"replace"}
	memories[0].UpdatedAt = now.Add(-31 * 24 * time.Hour)
	memories[2].UpdatedAt, memories[2].CitationCount = now.Add(-31*24*time.Hour), 3
	memories[3].UpdatedAt, memories[3].InjectionCount = now.Add(-31*24*time.Hour), 4
	memories[4].Confidence = 0.85
	memories[5].Tags = []string{"prefix replacement", "REPLACE", `quoted "replacement"`, `quoted "replacement`}
	memories[0].Project = "legacy-reader-alias"
	require.NoError(t, db.CreateInBatches(&memories, 100).Error)
	// Create applies confidence=0.5; explicitly store the intended omitted DTO values.
	require.NoError(t, db.Model(&gormdb.Memory{}).Where("project = ? AND id <> ?", projectA, memories[4].ID).UpdateColumn("confidence", 0).Error)
	require.NoError(t, db.Model(&gormdb.Memory{}).Where("id = ?", memories[3].ID).UpdateColumn("superseded_by", memories[500].ID).Error)
	require.NoError(t, db.Create(&gormdb.Memory{Project: "legacy-memory-only", Content: "memory-only registered project", Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}, CreatedAt: now, UpdatedAt: now}).Error)
	for _, selector := range []string{"ambiguous-reader-alias", removed, "dead-owner-alias", "unregistered-orphan"} {
		require.NoError(t, db.Create(&gormdb.Memory{Project: selector, Content: "decision excluded " + selector, Status: "active", Version: 1, PrivacyScope: "project", Tags: []string{}, SourceSessions: []string{}, CreatedAt: now, UpdatedAt: now}).Error)
	}
	hiddenIDs := []int64{}
	for _, hidden := range []gormdb.Memory{
		{Project: projectA, Content: "decision hidden workstation", PrivacyScope: "private", SourceWorkstationID: "fixture-card-b"},
		{Project: projectA, Content: "decision hidden principal", PrivacyScope: "project", AgentVisibility: "private", OwnerPrincipal: "agent/local-b", OwnerPrincipalKind: "agent"},
		{Project: projectA, Content: "decision hidden domain", PrivacyScope: "project", AgentVisibility: "shared", Domain: "reader-domain", OwnerPrincipal: "agent/local-b", OwnerPrincipalKind: "agent"},
		{Project: projectB, Content: "decision other project", PrivacyScope: "project"},
	} {
		hidden.Status, hidden.Version, hidden.Tags, hidden.SourceSessions = "active", 1, []string{}, []string{}
		hidden.EpistemicType = "fact"
		hidden.Tags = []string{"replace"}
		hidden.CreatedAt, hidden.UpdatedAt = now.Add(time.Minute), now.Add(time.Minute)
		require.NoError(t, db.Create(&hidden).Error)
		hiddenIDs = append(hiddenIDs, hidden.ID)
	}
	for _, hidden := range []gormdb.Memory{
		{Project: projectA, Content: "decision deleted", Status: "active", DeletedAt: &now},
		{Project: projectA, Content: "decision expired", Status: "active", ValidUntil: &now},
	} {
		hidden.Version, hidden.PrivacyScope, hidden.Tags, hidden.SourceSessions = 1, "project", []string{}, []string{}
		hidden.CreatedAt, hidden.UpdatedAt = now, now
		require.NoError(t, db.Create(&hidden).Error)
	}

	documents := make([]gormdb.VersionedDocument, 501)
	for i := range documents {
		documents[i] = gormdb.VersionedDocument{Project: projectA, Path: fmt.Sprintf("notes/decision-%03d.md", i), Version: 1, DocType: "markdown", Author: "reader-fixture", Content: "body-only-secret-needle", Metadata: `{"private":"metadata-only-secret-needle"}`, ContentHash: "fixture-hash", CreatedAt: now}
	}
	documents[0].Path = `notes/100%_safe\route.md`
	documents[1].Version, documents[1].DocType = 2, "text"
	documents[0].Project, documents[0].Version = "legacy-reader-alias", 2
	require.NoError(t, db.CreateInBatches(&documents, 100).Error)
	require.NoError(t, db.Create(&gormdb.VersionedDocument{Project: projectA, Path: documents[1].Path, Version: 1, DocType: "markdown", Author: "history-only-needle", Content: "old", ContentHash: "old", Metadata: "{}", CreatedAt: now.Add(-time.Hour)}).Error)
	require.NoError(t, db.Create(&gormdb.VersionedDocument{Project: projectA, Path: documents[0].Path, Version: 1, DocType: "text", Author: "old-alias-history", Content: "old alias version", ContentHash: "old", Metadata: "{}", CreatedAt: now.Add(-time.Hour)}).Error)
	require.NoError(t, db.Create(&gormdb.VersionedDocument{Project: projectB, Path: "notes/other.md", Version: 1, DocType: "markdown", Author: "reader-fixture", Content: "other", ContentHash: "other", Metadata: "{}", CreatedAt: now}).Error)
	require.NoError(t, db.Create(&gormdb.VersionedDocument{Project: "legacy-document-only", Path: "only-document.md", Version: 1, DocType: "markdown", Author: "fixture", Content: "document-only registered project", ContentHash: "fixture", Metadata: "{}", CreatedAt: now}).Error)
	for _, selector := range []string{"ambiguous-reader-alias", removed, "dead-owner-alias", "unregistered-orphan"} {
		require.NoError(t, db.Create(&gormdb.VersionedDocument{Project: selector, Path: "notes/excluded.md", Version: 1, DocType: "markdown", Author: "fixture", Content: "unreadable " + selector, ContentHash: "fixture", Metadata: "{}", CreatedAt: now}).Error)
	}

	// Fresh private nonces exercise real bcrypt/keycard auth only in this fixture.
	newToken := func() string {
		var secret [16]byte
		_, err := rand.Read(secret[:])
		require.NoError(t, err)
		return fmt.Sprintf("engram_%x", secret[:])
	}
	tokenA, tokenB, tokenHAP := newToken(), newToken(), newToken()
	tokens := knowledgeFixtureTokens{}
	for _, input := range []struct{ raw, id, principal, kind string }{
		{tokenA, "fixture-card-a", "agent/local-a", "agent"},
		{tokenB, "fixture-card-b", "agent/local-b", "agent"},
		{tokenHAP, "fixture-card-hap", auth.ProjectServicePrincipal(projectA), "service"},
	} {
		hash, err := bcrypt.GenerateFromPassword([]byte(input.raw), bcrypt.MinCost)
		require.NoError(t, err)
		tokens = append(tokens, gormdb.APIToken{ID: input.id, TokenPrefix: input.raw[len("engram_") : len("engram_")+8], TokenHash: string(hash), Scope: "read-only", Principal: input.principal, PrincipalKind: input.kind})
	}
	ta, err := NewTokenAuth(rand.Text())
	require.NoError(t, err)
	require.True(t, ta.IsEnabled(), "fixture must exercise bearer authentication")
	ta.SetValidator(auth.NewValidator("", tokens))
	store := &gormdb.Store{DB: db}
	service := &Service{store: store, memoryStore: gormdb.NewMemoryStore(store), documentStore: gormdb.NewVersionedDocumentStore(store), sessionStore: gormdb.NewSessionStore(store)}
	router := chi.NewRouter()
	router.Use(ta.Middleware)
	router.Get("/api/projects", service.handleGetProjects)
	router.Get("/api/memories", service.handleListMemories)
	router.Get("/api/memories/{id}", service.handleGetMemoryByID)
	router.Get("/api/documents", service.handleListDocuments)
	router.Get("/api/documents/read", service.handleReadDocument)
	router.Get("/api/documents/history", service.handleDocumentHistory)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	get := func(path, token string, status int, result any) string {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		require.NoError(t, err)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := server.Client().Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, status, response.StatusCode, "%s: %s", path, body)
		if result != nil {
			require.NoError(t, json.Unmarshal(body, result), "%s: %s", path, body)
		}
		return string(body)
	}
	var catalog projectListResponse
	get("/api/projects", tokenA, 200, &catalog)
	require.Contains(t, catalog.Items, gormdb.ProjectDescriptor{ID: projectA, DisplayName: "Same human label"})
	require.Contains(t, catalog.Items, gormdb.ProjectDescriptor{ID: projectB, DisplayName: "Same human label"})
	require.Contains(t, catalog.Items, gormdb.ProjectDescriptor{ID: unlabelled, DisplayName: unlabelled})
	require.Contains(t, catalog.Items, gormdb.ProjectDescriptor{ID: memoryOnly, DisplayName: memoryOnly})
	require.Contains(t, catalog.Items, gormdb.ProjectDescriptor{ID: documentOnly, DisplayName: documentOnly})
	for _, item := range catalog.Items {
		require.NotContains(t, []string{removed, merged, "legacy-reader-alias"}, item.ID)
	}

	var memoryPage memoryListResponse
	memoryURL := "/api/memories?project=" + projectA
	body := get(memoryURL+"&limit=500", tokenA, 200, &memoryPage)
	require.EqualValues(t, 501, memoryPage.Total)
	require.Len(t, memoryPage.Items, 500)
	require.True(t, memoryPage.HasMore)
	require.Equal(t, memories[500].ID, memoryPage.Items[0].ID)
	require.Equal(t, memories[1].ID, memoryPage.Items[499].ID)
	require.NotContains(t, body, "hidden")
	var rawMemoryPage struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &rawMemoryPage))
	require.NotContains(t, rawMemoryPage.Items[0], "confidence", "stored zero is actually omitted from the HTTP DTO")
	get(memoryURL+"&offset=500&limit=500", tokenA, 200, &memoryPage)
	require.EqualValues(t, 501, memoryPage.Total)
	require.Len(t, memoryPage.Items, 1)
	require.False(t, memoryPage.HasMore)
	require.Equal(t, memories[0].ID, memoryPage.Items[0].ID)
	require.Equal(t, "active", memoryPage.Items[0].Status)
	require.Equal(t, "reader-fixture", memoryPage.Items[0].SourceAgent)
	require.Equal(t, "agent/local-a", memoryPage.Items[0].OwnerPrincipal)
	require.Equal(t, projectA, memoryPage.Items[0].Project)
	var exactMemory models.Memory
	get(memoryURL, tokenA, 200, &memoryPage)
	require.Equal(t, 50, memoryPage.Limit)
	require.Zero(t, memoryPage.Offset)
	require.Len(t, memoryPage.Items, 50)
	require.EqualValues(t, 501, memoryPage.Total)
	detailURL := fmt.Sprintf("/api/memories/%d?project=", memories[0].ID)
	get(detailURL+projectA, tokenA, 200, &exactMemory)
	require.Equal(t, projectA, exactMemory.Project)
	require.Equal(t, memories[0].Content, exactMemory.Content)
	get(fmt.Sprintf("/api/memories/%d", memories[0].ID), tokenA, 400, nil)
	notFound := get(detailURL+projectB, tokenA, 404, nil)
	require.Equal(t, notFound, get(detailURL+projectA, tokenB, 404, nil))
	for _, hiddenID := range hiddenIDs {
		require.Equal(t, notFound, get(fmt.Sprintf("/api/memories/%d?project=%s", hiddenID, projectA), tokenA, 404, nil))
	}
	require.Equal(t, notFound, get("/api/memories/999999999?project="+projectA, tokenA, 404, nil))
	for _, id := range []string{"invalid", "0", "-1"} {
		get("/api/memories/"+id+"?project="+projectA, tokenA, 400, nil)
	}
	for _, project := range []string{removed, merged, "legacy-reader-alias", "unregistered-orphan"} {
		get(detailURL+project, tokenA, 403, nil)
	}
	get(detailURL+projectA, tokenHAP, 403, nil)
	get(memoryURL+"&query="+url.QueryEscape(`DECISION 100%_SAFE\route`), tokenA, 200, &memoryPage)
	require.EqualValues(t, 1, memoryPage.Total)
	require.Equal(t, memories[0].ID, memoryPage.Items[0].ID)
	get(memoryURL+"&query="+url.QueryEscape(`DECISION 100%_SAFE\route`), tokenB, 200, &memoryPage)
	require.Zero(t, memoryPage.Total)
	require.Empty(t, memoryPage.Items)
	for _, query := range []string{"hidden principal", "hidden domain", "hidden workstation", "excluded", "absent", "' OR 1=1 --"} {
		get(memoryURL+"&query="+url.QueryEscape(query), tokenA, 200, &memoryPage)
		require.Zero(t, memoryPage.Total, query)
		require.Empty(t, memoryPage.Items)
	}
	get(memoryURL+"&offset=501", tokenA, 200, &memoryPage)
	require.EqualValues(t, 501, memoryPage.Total)
	require.Empty(t, memoryPage.Items)
	require.False(t, memoryPage.HasMore)
	for _, filter := range []string{"active", "all"} {
		get(memoryURL+"&filter="+filter+"&limit=500", tokenA, 200, &memoryPage)
		require.EqualValues(t, 501, memoryPage.Total)
	}
	get(memoryURL+"&filter=low&limit=1", tokenA, 200, &memoryPage)
	require.EqualValues(t, 1, memoryPage.Total, "omitted confidence must not become a low rating")
	require.Equal(t, memories[0].ID, memoryPage.Items[0].ID, "filtered older row lies beyond the unfiltered first 500")
	get(memoryURL+"&filter=stale&offset=1&limit=1", tokenA, 200, &memoryPage)
	require.EqualValues(t, 2, memoryPage.Total, "old cited row is not stale; unknown or zero citation row is stale")
	require.Equal(t, memories[0].ID, memoryPage.Items[0].ID)
	require.False(t, memoryPage.HasMore)
	get(memoryURL+"&filter=chain&limit=500", tokenA, 200, &memoryPage)
	require.EqualValues(t, 2, memoryPage.Total, "only exact tags or supersession links match")
	require.Len(t, memoryPage.Items, 2)
	get(memoryURL+"&filter=flagged", tokenA, 200, &memoryPage)
	require.Zero(t, memoryPage.Total, "filters do not widen the existing active-only corpus")
	get(memoryURL+"&filter=unknown", tokenA, 400, nil)
	for _, filters := range []string{"filter=low&filter=chain", "filter=chain&filter=low", "filter=all&filter=low&filter=chain", "filter=low&filter=all&filter=chain&filter=low"} {
		get(memoryURL+"&"+filters+"&limit=1", tokenA, 200, &memoryPage)
		require.EqualValues(t, 1, memoryPage.Total, "selected chips intersect before count/page: %s", filters)
		require.Len(t, memoryPage.Items, 1)
		require.Equal(t, memories[0].ID, memoryPage.Items[0].ID, "intersection finds the older canonical alias beyond the unfiltered first 500")
	}
	get(memoryURL+"&filter=all&filter=chain", tokenA, 200, &memoryPage)
	require.EqualValues(t, 2, memoryPage.Total, "all is neutral, not an exclusive reset")
	get(memoryURL+strings.Repeat("&filter=chain", 6), tokenA, 200, &memoryPage)
	require.EqualValues(t, 2, memoryPage.Total, "six incoming valid duplicates are deduplicated")
	for _, filters := range []string{"filter=active&filter=flagged", "filter=flagged&filter=active", "filter=all&filter=active&filter=flagged&filter=stale&filter=low&filter=chain"} {
		get(memoryURL+"&"+filters, tokenA, 200, &memoryPage)
		require.Zero(t, memoryPage.Total, "active/flagged AND preserves the active-only corpus")
		require.Empty(t, memoryPage.Items)
	}
	get(memoryURL+"&filter=chain&filter=low&query=hidden", tokenA, 200, &memoryPage)
	require.Zero(t, memoryPage.Total, "hidden low+chain rows must affect neither total nor page")
	require.Empty(t, memoryPage.Items)
	get(memoryURL+"&filter=chain&filter=low&query="+url.QueryEscape(memories[0].Content), tokenB, 200, &memoryPage)
	require.Zero(t, memoryPage.Total, "combined filtering retains private workstation/principal denial")
	for _, filters := range []string{"filter=", "filter=low&filter=unknown", "filter=chain&filter=", "filter=chain&filter=low,chain", "filter=chain&filter=LOW", strings.TrimPrefix(strings.Repeat("&filter=chain", 7), "&")} {
		get(memoryURL+"&"+filters, tokenA, 400, nil)
	}
	for _, types := range []string{"epistemic_type=fact&epistemic_type=experience", "epistemic_type=experience&epistemic_type=fact&epistemic_type=fact"} {
		body := get(memoryURL+"&"+types+"&limit=500", tokenA, 200, &memoryPage)
		require.EqualValues(t, 501, memoryPage.Total, "type OR precedes count/page")
		require.Len(t, memoryPage.Items, 500)
		require.True(t, memoryPage.HasMore)
		require.NotContains(t, body, "hidden")
		get(memoryURL+"&"+types+"&offset=500&limit=500", tokenA, 200, &memoryPage)
		require.EqualValues(t, 501, memoryPage.Total)
		require.Len(t, memoryPage.Items, 1)
		require.Equal(t, memories[0].ID, memoryPage.Items[0].ID)
		require.False(t, memoryPage.HasMore)
		get(memoryURL+"&"+types+"&filter=low&filter=chain&query=decision&limit=1", tokenA, 200, &memoryPage)
		require.EqualValues(t, 1, memoryPage.Total, "type OR intersects query and quality/status filters")
		require.Equal(t, memories[0].ID, memoryPage.Items[0].ID)
		get(memoryURL+"&"+types+"&query=hidden", tokenA, 200, &memoryPage)
		require.Zero(t, memoryPage.Total, "hidden selected types affect neither count nor page")
		require.Empty(t, memoryPage.Items)
		get(memoryURL+"&"+types+"&query="+url.QueryEscape(memories[0].Content), tokenB, 200, &memoryPage)
		require.Zero(t, memoryPage.Total, "type filter cannot bypass workstation/principal/domain privacy")
	}
	get(memoryURL+"&epistemic_type=fact&limit=500", tokenA, 200, &memoryPage)
	require.EqualValues(t, 251, memoryPage.Total)
	get(memoryURL+strings.Repeat("&epistemic_type=fact", 6), tokenA, 200, &memoryPage)
	require.EqualValues(t, 251, memoryPage.Total, "six raw duplicates deduplicate without broadening")
	for _, types := range []string{"epistemic_type=opinion", "epistemic_type=observation"} {
		get(memoryURL+"&"+types, tokenA, 200, &memoryPage)
		require.Zero(t, memoryPage.Total, "valid absent types do not fall back")
	}
	for _, types := range []string{"epistemic_type=", "epistemic_type=fact&epistemic_type=decision", "epistemic_type=fact&epistemic_type=", "epistemic_type=fact,experience", "epistemic_type=FACT", "epistemic_type=%20fact", "epistemic_type=fact&epistemic_type=%00", "epistemic_type=fact&epistemic_type=%FF", "epistemic_type=fact&epistemic_type=%", strings.TrimPrefix(strings.Repeat("&epistemic_type=fact", 7), "&")} {
		get(memoryURL+"&"+types, tokenA, 400, nil)
	}
	get("/api/memories?project="+memoryOnly, tokenA, 200, &memoryPage)
	require.EqualValues(t, 1, memoryPage.Total)
	require.Equal(t, memoryOnly, memoryPage.Items[0].Project)

	var documentPage documentListResponse
	documentURL := "/api/documents?project=" + projectA + "&path_prefix=notes%2F"
	get(documentURL+"&limit=500", tokenA, 200, &documentPage)
	require.EqualValues(t, 501, documentPage.Total)
	require.Len(t, documentPage.Items, 500)
	require.True(t, documentPage.HasMore)
	require.Equal(t, documents[500].ID, documentPage.Items[0].ID)
	get(documentURL+"&limit=500&offset=500", tokenA, 200, &documentPage)
	require.EqualValues(t, 501, documentPage.Total)
	require.Equal(t, projectA, documentPage.Items[0].Project)
	require.Len(t, documentPage.Items, 1)
	require.Equal(t, documents[0].ID, documentPage.Items[0].ID)
	require.False(t, documentPage.HasMore)
	get(documentURL+"&offset=501", tokenA, 200, &documentPage)
	require.EqualValues(t, 501, documentPage.Total)
	require.Empty(t, documentPage.Items)
	require.NotNil(t, documentPage.Items)
	require.False(t, documentPage.HasMore)
	get(documentURL, tokenA, 200, &documentPage)
	require.Equal(t, 50, documentPage.Limit)
	require.Len(t, documentPage.Items, 50)
	get(documentURL+"&doc_type=markdown&limit=500", tokenA, 200, &documentPage)
	require.EqualValues(t, 500, documentPage.Total)
	require.Len(t, documentPage.Items, 500)
	for _, item := range documentPage.Items {
		require.NotEqual(t, documents[1].ID, item.ID)
	}
	for _, types := range []string{"doc_type=markdown&doc_type=text", "doc_type=text&doc_type=markdown&doc_type=text"} {
		get(documentURL+"&"+types+"&limit=500", tokenA, 200, &documentPage)
		require.EqualValues(t, 501, documentPage.Total, "latest-type OR before count/page")
		require.Len(t, documentPage.Items, 500)
		require.True(t, documentPage.HasMore)
		get(documentURL+"&"+types+"&limit=500&offset=500", tokenA, 200, &documentPage)
		require.Len(t, documentPage.Items, 1)
		require.EqualValues(t, 501, documentPage.Total)
		require.False(t, documentPage.HasMore)
		get(documentURL+"&"+types+"&query="+url.QueryEscape(`100%_SAFE\route`), tokenA, 200, &documentPage)
		require.EqualValues(t, 1, documentPage.Total, "type OR AND literal query/path prefix")
		get(documentURL+"&"+types+"&query=history-only-needle", tokenA, 200, &documentPage)
		require.Zero(t, documentPage.Total, "matching old version is never admitted by type OR")
	}
	get(documentURL+"&doc_type=text", tokenA, 200, &documentPage)
	require.EqualValues(t, 1, documentPage.Total)
	require.Equal(t, documents[1].ID, documentPage.Items[0].ID)
	get(documentURL+strings.Repeat("&doc_type=markdown", 6), tokenA, 200, &documentPage)
	require.EqualValues(t, 500, documentPage.Total)
	get(documentURL+"&doc_type="+url.QueryEscape("\u2003text\u00a0")+"&doc_type=text", tokenA, 200, &documentPage)
	require.EqualValues(t, 1, documentPage.Total, "Unicode trimming precedes valid-value dedup")
	for _, types := range []string{"doc_type=", "doc_type=markdown&doc_type=", "doc_type=text&doc_type=%20", "doc_type=text&doc_type=%00", "doc_type=text&doc_type=%FF", "doc_type=text&doc_type=%", "doc_type=text&doc_type=" + strings.Repeat("x", 129), "doc_type=" + url.QueryEscape(strings.Repeat("я", 65)), strings.TrimPrefix(strings.Repeat("&doc_type=text", 7), "&")} {
		get(documentURL+"&"+types, tokenA, 400, nil)
	}
	literalTypes := []string{"markdown,text", "я", "%FF", `a%b_c\d`, strings.Repeat("я", 64)}
	for i, docType := range literalTypes {
		row := gormdb.VersionedDocument{Project: projectA, Path: fmt.Sprintf("types/literal-%d", i), Version: 1, DocType: docType, Author: "type-fixture", Content: "secret-type-body", Metadata: "{}", ContentHash: "fixture", CreatedAt: now}
		require.NoError(t, db.Create(&row).Error)
		body := get("/api/documents?project="+projectA+"&path_prefix=types/&doc_type="+url.QueryEscape(docType), tokenA, 200, &documentPage)
		require.EqualValues(t, 1, documentPage.Total, "literal document type is not split or decoded again")
		require.Equal(t, docType, documentPage.Items[0].DocType)
		require.NotContains(t, body, "secret-type-body")
	}
	for _, row := range []gormdb.VersionedDocument{
		{Project: projectA, Path: "types/latest-only", Version: 1, DocType: "markdown", Content: "old", Author: "type-fixture", Metadata: "{}", ContentHash: "old", CreatedAt: now},
		{Project: projectA, Path: "types/latest-only", Version: 2, DocType: "html", Content: "new", Author: "type-fixture", Metadata: "{}", ContentHash: "new", CreatedAt: now},
	} {
		require.NoError(t, db.Create(&row).Error)
	}
	get("/api/documents?project="+projectA+"&path_prefix=types/latest&doc_type=markdown&doc_type=text", tokenA, 200, &documentPage)
	require.Zero(t, documentPage.Total, "latest html excludes matching older markdown")
	get("/api/documents?project="+projectA+"&path_prefix=types/latest&doc_type=markdown&doc_type=html", tokenA, 200, &documentPage)
	require.EqualValues(t, 1, documentPage.Total)
	require.Equal(t, "html", documentPage.Items[0].DocType)
	get(documentURL+"&query="+url.QueryEscape(`100%_SAFE\route`), tokenA, 200, &documentPage)
	require.EqualValues(t, 1, documentPage.Total)
	require.Equal(t, documents[0].ID, documentPage.Items[0].ID)
	get("/api/documents?project="+projectA+"&path_prefix="+url.QueryEscape(`notes/100%_safe\`), tokenA, 200, &documentPage)
	require.EqualValues(t, 1, documentPage.Total)
	for _, query := range []string{"history-only-needle", "old-alias-history", "body-only-secret-needle", "metadata-only-secret-needle", "' OR 1=1 --", "excluded"} {
		get(documentURL+"&query="+url.QueryEscape(query), tokenA, 200, &documentPage)
		require.Zero(t, documentPage.Total, query)
	}
	get("/api/documents?project="+projectB, tokenA, 200, &documentPage)
	require.EqualValues(t, 1, documentPage.Total)
	require.Equal(t, projectB, documentPage.Items[0].Project)
	get("/api/documents?project="+documentOnly, tokenA, 200, &documentPage)
	require.EqualValues(t, 1, documentPage.Total)
	require.Equal(t, documentOnly, documentPage.Items[0].Project)
	var read documentReadResponse
	get("/api/documents/read?project="+projectA+"&path="+url.QueryEscape(documents[1].Path), tokenA, 200, &read)
	require.Equal(t, 2, read.Version)
	require.Equal(t, "body-only-secret-needle", read.Content)
	get("/api/documents/read?project="+projectA+"&path="+url.QueryEscape(documents[0].Path), tokenA, 200, &read)
	require.Equal(t, projectA, read.Project)
	get("/api/documents/read?project="+projectA+"&path="+url.QueryEscape(documents[0].Path)+"&version=1", tokenA, 200, &read)
	require.Equal(t, projectA, read.Project)
	require.Equal(t, "old alias version", read.Content)
	get("/api/documents/read?project="+projectA+"&path="+url.QueryEscape(documents[0].Path)+"&version=3", tokenA, 404, nil)
	get("/api/documents/read?project="+projectA+"&path="+url.QueryEscape(documents[0].Path)+"&version=0", tokenA, 400, nil)
	get("/api/documents/read?project="+projectB+"&path="+url.QueryEscape(documents[0].Path), tokenA, 404, nil)
	get("/api/documents/read?project="+projectA+"&path=notes%2Fexcluded.md", tokenA, 404, nil)
	var rawProject string
	require.NoError(t, db.Model(&gormdb.Memory{}).Where("id = ?", memories[0].ID).Pluck("project", &rawProject).Error)
	require.Equal(t, "legacy-reader-alias", rawProject, "read must not rewrite stored provenance")
	require.NoError(t, db.Model(&gormdb.VersionedDocument{}).Where("id = ?", documents[0].ID).Pluck("project", &rawProject).Error)
	require.Equal(t, "legacy-reader-alias", rawProject)
	var history documentHistoryResponse
	get("/api/documents/history?project="+projectA+"&path="+url.QueryEscape(documents[1].Path), tokenA, 200, &history)
	require.Equal(t, 2, history.Count)
	require.Equal(t, 2, history.Versions[0].Version)
	get("/api/documents/history?project="+projectA+"&path="+url.QueryEscape(documents[0].Path), tokenA, 200, &history)
	require.Equal(t, 2, history.Count)
	require.Equal(t, projectA, history.Project)
	get("/api/documents/history?project="+projectB+"&path="+url.QueryEscape(documents[0].Path), tokenA, 200, &history)
	require.Zero(t, history.Count)
	get("/api/documents/history?project="+projectA+"&path=notes%2Fexcluded.md", tokenA, 200, &history)
	require.Zero(t, history.Count)
	for _, endpoint := range []string{"/api/documents/read", "/api/documents/history"} {
		for _, selector := range []string{removed, merged, "legacy-reader-alias", "unregistered-orphan"} {
			get(endpoint+"?project="+selector+"&path="+url.QueryEscape(documents[0].Path), tokenA, 403, nil)
		}
	}

	for _, endpoint := range []string{"/api/memories", "/api/documents"} {
		for _, query := range []string{"", "project=%20", "project=" + projectA + "&limit=0", "project=" + projectA + "&limit=501", "project=" + projectA + "&offset=-1", "project=" + projectA + "&query=%00", "project=" + projectA + "&query=%FF", "project=" + projectA + "&query=" + strings.Repeat("a", 1025), "project=" + projectA + "&query=" + url.QueryEscape(strings.Repeat("я", 513))} {
			get(endpoint+"?"+query, tokenA, 400, nil)
		}
		for _, project := range []string{removed, merged, uuid.NewString(), "legacy-reader-alias", "Same human label"} {
			get(endpoint+"?project="+url.QueryEscape(project), tokenA, 403, nil)
		}
		get(endpoint+"?project="+projectA, tokenHAP, 403, nil)
		get(endpoint+"?project="+projectA, "", 401, nil)
		get(endpoint+"?project="+projectA+"&query="+strings.Repeat("a", 1024), tokenA, 200, nil)
		get(endpoint+"?project="+projectA+"&offset=bad", tokenA, 400, nil)
		get(endpoint+"?project="+projectA+"&query=%", tokenA, 400, nil)
	}
	get(documentURL+"&doc_type="+strings.Repeat("x", 129), tokenA, 400, nil)
	get("/api/documents?project="+projectA+"&path_prefix="+strings.Repeat("x", 4097), tokenA, 400, nil)
	get("/api/projects", tokenHAP, 403, nil)
	get("/api/projects", "", 401, nil)
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")
	get(memoryURL+"&limit=500", tokenA, 200, &memoryPage)
	require.EqualValues(t, 502, memoryPage.Total, "flag-off never disables principal/domain privacy")
	if sqliteFixture {
		require.NoError(t, db.Exec("ALTER TABLE memories RENAME TO reader_unavailable_memories").Error)
		require.NotContains(t, get(memoryURL, tokenA, 500, nil), "SQL")
		get(detailURL+projectA, tokenA, 500, nil)
		require.NoError(t, db.Exec("ALTER TABLE versioned_documents RENAME TO reader_unavailable_documents").Error)
		get(documentURL, tokenA, 500, nil)
		get("/api/documents/read?project="+projectA+"&path=notes", tokenA, 500, nil)
		get("/api/documents/history?project="+projectA+"&path=notes", tokenA, 500, nil)
		require.NoError(t, db.Exec("ALTER TABLE projects RENAME TO reader_unavailable_projects").Error)
		get("/api/projects", tokenA, 500, nil)
		get(memoryURL, tokenA, 500, nil)
	}
	service.memoryStore, service.documentStore, service.sessionStore = nil, nil, nil
	get(memoryURL, tokenA, 503, nil)
	get(detailURL+projectA, tokenA, 503, nil)
	get(documentURL, tokenA, 503, nil)
	get("/api/projects", tokenA, 503, nil)
	t.Log("actual HTTP: canonical/privacy; repeated type OR AND query/status/quality; visible counts/pages beyond500; strict all-value bounds/dedup; latest-version type negatives; comma/Unicode/percent literal types PASS; PostgreSQL operators remain a separate DSN gate")
}
