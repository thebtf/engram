package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/db/sqlitetest"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/internal/worker/sse"
	"github.com/thebtf/engram/pkg/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
	"modernc.org/sqlite"
)

// This fixture is independent of the lifecycle RED added after 528328415.
// SQLite executes real authorization, handlers, transactions and persistence;
// only PostgreSQL syntax is lowered. The PostgreSQL tests below own lock proof.
type (
	memoryMutationSQLiteDriver struct{ driver.Driver }
	memoryMutationSQLiteConn   struct{ driver.Conn }
)

func (d memoryMutationSQLiteDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.Driver.Open(name)
	if err != nil {
		return nil, err
	}
	return memoryMutationSQLiteConn{conn}, nil
}

var (
	memoryMutationAliasSQL   = regexp.MustCompile(`COALESCE\(legacy_ids, ARRAY\[\]::TEXT\[\]\) @> ARRAY\[(\$[0-9]+)\]::TEXT\[\]`)
	memoryMutationSessionSQL = regexp.MustCompile(`(\$[0-9]+) = ANY\(m\.source_sessions\)`)
)

func (c memoryMutationSQLiteConn) Prepare(query string) (driver.Stmt, error) {
	// SQLite cannot certify relation locks; only these production statements
	// are lowered. The isolated PostgreSQL cases own their serialization proof.
	switch query {
	case `LOCK TABLE "audit_log" IN ROW EXCLUSIVE MODE`, `LOCK TABLE "memories" IN ROW SHARE MODE`, `LOCK TABLE "project_consolidation_memberships" IN SHARE MODE`:
		query = "SELECT 1"
	}
	query = strings.TrimSuffix(strings.TrimSuffix(query, " FOR UPDATE"), " FOR SHARE")
	query = memoryMutationAliasSQL.ReplaceAllString(query, `memory_mutation_has_session(legacy_ids, $1)`)
	query = memoryMutationSessionSQL.ReplaceAllString(query, `memory_mutation_has_session(m.source_sessions, $1)`)
	query = strings.ReplaceAll(query, "btrim(", "memory_mutation_btrim(")
	query = strings.ReplaceAll(query, "now()", "memory_mutation_now()")
	// Current-validity readers use uppercase NOW(); preserve the same clock
	// semantics as the lowercase PostgreSQL function already adapted above.
	query = strings.ReplaceAll(query, "NOW()", "memory_mutation_now()")
	query = strings.ReplaceAll(query, scope.MemoryContentDigestSQL, "memory_mutation_content_digest(m.content)")
	// Preserve literal content/string-tag matching in this test-only dialect seam.
	// PostgreSQL regressions separately own JSONB scalar safety and real SQL proof.
	query = strings.ReplaceAll(query, "jsonb_array_elements_text(", "json_each(")
	query = strings.ReplaceAll(query, "jsonb_typeof(tags)", "json_type(tags)")
	query = strings.ReplaceAll(query, "'[]'::jsonb", "'[]'")
	query = strings.ReplaceAll(query, "AS literal_tag(value)", "AS literal_tag")
	query, err := sqlitetest.MembershipSQL(query)
	if err != nil {
		return nil, err
	}
	return c.Conn.Prepare(query)
}

var memoryMutationSQLiteOnce sync.Once

func memoryMutationSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	memoryMutationSQLiteOnce.Do(func() {
		// RegisterScalarFunction updates SQLite's registered driver instance;
		// a separate zero-value Driver would open connections without its UDFs.
		registered, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		sql.Register("memory-mutation-security-sqlite", memoryMutationSQLiteDriver{Driver: registered.Driver()})
		require.NoError(t, registered.Close())
		require.NoError(t, sqlite.RegisterScalarFunction("memory_mutation_now", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			return time.Now().UTC().Format("2006-01-02 15:04:05"), nil
		}))
		require.NoError(t, sqlite.RegisterScalarFunction("memory_mutation_btrim", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			if args[0] == nil {
				return nil, nil
			}
			return strings.TrimSpace(args[0].(string)), nil
		}))
		require.NoError(t, sqlite.RegisterScalarFunction("memory_mutation_has_session", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			var values pq.StringArray
			if err := values.Scan(args[0]); err != nil {
				return nil, err
			}
			for _, value := range values {
				if value == args[1] {
					return int64(1), nil
				}
			}
			return int64(0), nil
		}))
		require.NoError(t, sqlite.RegisterScalarFunction("memory_mutation_content_digest", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			if args[0] == nil {
				return nil, nil
			}
			content, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("memory mutation fixture digest requires text")
			}
			digest := sha256.Sum256([]byte(content))
			return hex.EncodeToString(digest[:]), nil
		}))
	})
	sqlDB, err := sql.Open("memory-mutation-security-sqlite", ":memory:")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	for _, model := range []any{gormdb.Project{}, gormdb.Memory{}, gormdb.ContinuitySlot{}, gormdb.AuditLogEntry{}, gormdb.ConsolidationMembership{}} {
		parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		require.NoError(t, err)
		var columns []string
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
	require.NoError(t, db.Exec(sqlitetest.RedirectSchema).Error)
	return db
}

func TestMemoryMutationSQLiteSearchMaterialization(t *testing.T) {
	db := memoryMutationSQLiteDB(t)
	for _, test := range []struct {
		name    string
		content any
	}{
		{"empty", ""},
		{"ascii", "selected body"},
		{"utf8", "selected body — память 😀\n"},
		{"null", nil},
	} {
		t.Run("digest/"+test.name, func(t *testing.T) {
			var actual sql.NullString
			err := db.Raw("SELECT "+scope.MemoryContentDigestSQL+" FROM (SELECT ? AS content) AS m", test.content).Row().Scan(&actual)
			require.NoError(t, err)
			if test.content == nil {
				require.False(t, actual.Valid, "SQL NULL must not become the digest of an empty body")
				return
			}
			digest := sha256.Sum256([]byte(test.content.(string)))
			require.True(t, actual.Valid)
			require.Equal(t, hex.EncodeToString(digest[:]), actual.String)
		})
	}

	f := newMemoryMutationSecurityFixture(t, db)
	memory := f.seed(t, f.tokenA, "project", "private", "")
	const selectedBody = "selected body — память 😀\n"
	require.NoError(t, db.Model(&gormdb.Memory{}).Where("id = ?", memory.ID).Update("content", selectedBody).Error)
	digest := sha256.Sum256([]byte(selectedBody))
	ref := scope.MemoryRef{ID: memory.ID, Version: memory.Version, ContentDigest: hex.EncodeToString(digest[:])}
	access := scope.MemoryReadAccess{
		Caller: scope.KeycardContext{
			Principal: memory.OwnerPrincipal, PrincipalKind: memory.OwnerPrincipalKind,
			WorkstationID: memory.SourceWorkstationID,
		},
		Visibility: scope.MemoryVisibilityOptions{ApplyPrivacyScope: true},
	}
	store := gormdb.NewMemoryStore(&gormdb.Store{DB: db})
	materialize := func(t *testing.T, selected scope.MemoryRef, expected int) {
		t.Helper()
		rows, err := store.GetByRefsForSearch(t.Context(), f.project, []scope.MemoryRef{selected}, access)
		require.NoError(t, err)
		require.Len(t, rows, expected)
	}
	t.Run("unchanged_body", func(t *testing.T) { materialize(t, ref, 1) })
	t.Run("wrong_digest", func(t *testing.T) {
		wrong := ref
		wrong.ContentDigest = strings.Repeat("0", 64)
		materialize(t, wrong, 0)
	})
	t.Run("wrong_version", func(t *testing.T) {
		wrong := ref
		wrong.Version++
		materialize(t, wrong, 0)
	})
	t.Run("same_version_changed_body", func(t *testing.T) {
		require.NoError(t, db.Model(&gormdb.Memory{}).Where("id = ?", memory.ID).Update("content", "replacement body").Error)
		materialize(t, ref, 0)
	})
	digest = sha256.Sum256([]byte("replacement body"))
	ref.ContentDigest = hex.EncodeToString(digest[:])
	t.Run("fresh_body_reference", func(t *testing.T) { materialize(t, ref, 1) })
	t.Run("current_acl", func(t *testing.T) {
		require.NoError(t, db.Model(&gormdb.Memory{}).Where("id = ?", memory.ID).Update("owner_principal", "agent/other").Error)
		materialize(t, ref, 0)
	})
	t.Run("current_lifecycle", func(t *testing.T) {
		require.NoError(t, db.Model(&gormdb.Memory{}).Where("id = ?", memory.ID).Updates(map[string]any{
			"owner_principal": memory.OwnerPrincipal, "status": "flagged",
		}).Error)
		materialize(t, ref, 0)
	})
}

type memoryMutationSecurityFixture struct {
	db        *gorm.DB
	service   *Service
	tokenAuth *TokenAuth
	project   string
	other     string
	tokenA    string
	tokenA2   string
	tokenB    string
}

func newMemoryMutationSecurityFixture(t *testing.T, db *gorm.DB) *memoryMutationSecurityFixture {
	t.Helper()
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	f := &memoryMutationSecurityFixture{db: db, project: uuid.NewString(), other: uuid.NewString()}
	for _, project := range []string{f.project, f.other} {
		require.NoError(t, db.Create(&gormdb.Project{ID: project}).Error)
	}
	// The lookup is synthetic; the real middleware parses and bcrypt-validates
	// each bearer and derives its effective principal/workstation identity.
	tokens := knowledgeFixtureTokens{}
	newToken := func(card, principal string) string {
		var secret [16]byte
		_, err := rand.Read(secret[:])
		require.NoError(t, err)
		raw := auth.TokenRawPrefix + hex.EncodeToString(secret[:])
		hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
		require.NoError(t, err)
		tokens = append(tokens, gormdb.APIToken{
			ID: card, TokenPrefix: raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen],
			TokenHash: string(hash), Scope: "read-write", Principal: principal, PrincipalKind: "agent",
		})
		return raw
	}
	f.tokenA = newToken("synthetic-security-card-a", "agent/synthetic-security-a")
	f.tokenA2 = newToken("synthetic-security-card-a-second", "agent/synthetic-security-a")
	f.tokenB = newToken("synthetic-security-card-b", "agent/synthetic-security-b")
	tokenAuth, err := NewTokenAuth(rand.Text())
	require.NoError(t, err)
	require.True(t, tokenAuth.IsEnabled())
	tokenAuth.SetValidator(auth.NewValidator("", tokens))
	f.tokenAuth = tokenAuth
	store := &gormdb.Store{DB: db}
	f.service = &Service{
		router: chi.NewRouter(), store: store,
		memoryStore: gormdb.NewMemoryStore(store), sessionStore: gormdb.NewSessionStore(store),
		auditStore: gormdb.NewAuditStore(db), mcpHealth: mcp.NewMCPHealth(), sseBroadcaster: sse.NewBroadcaster(),
	}
	f.service.router.Use(tokenAuth.Middleware)
	f.service.ready.Store(true)
	f.service.setupRoutes()
	return f
}

type memoryMutationHTTPResult struct {
	status int
	body   string
}

func (f *memoryMutationSecurityFixture) request(t *testing.T, ctx context.Context, method, route, token string, body any) *http.Request {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, route, bytes.NewReader(encoded)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func (f *memoryMutationSecurityFixture) serve(req *http.Request) memoryMutationHTTPResult {
	response := httptest.NewRecorder()
	f.service.router.ServeHTTP(response, req)
	return memoryMutationHTTPResult{status: response.Code, body: response.Body.String()}
}

func (f *memoryMutationSecurityFixture) seed(t *testing.T, token, privacy, visibility, domain string) gormdb.Memory {
	t.Helper()
	result := f.serve(f.request(t, context.Background(), http.MethodPost, "/api/memories", token, map[string]any{
		"project": f.project, "content": "Synthetic mutation security note " + uuid.NewString(),
		"source_agent": "synthetic-security", "session_id": "synthetic-security-session",
		"privacy_scope": privacy, "agent_visibility": visibility, "domain": domain,
	}))
	require.Equal(t, http.StatusCreated, result.status, result.body)
	var memory models.Memory
	require.NoError(t, json.Unmarshal([]byte(result.body), &memory))
	var row gormdb.Memory
	require.NoError(t, f.db.First(&row, memory.ID).Error)
	require.NotEmpty(t, row.OwnerPrincipal)
	require.NotEmpty(t, row.SourceWorkstationID)
	require.Equal(t, visibility, row.AgentVisibility)
	return row
}

func TestLegacyPrincipalQueryPrivateWorkstationSQLite(t *testing.T) {
	legacyPrincipalQueryPrivateWorkstation(t, memoryMutationSQLiteDB(t))
}

func TestLegacyPrincipalQueryPrivateWorkstationPostgres(t *testing.T) {
	legacyPrincipalQueryPrivateWorkstation(t, memoryMutationPostgresDB(t))
}

func legacyPrincipalQueryPrivateWorkstation(t *testing.T, db *gorm.DB) {
	t.Helper()
	f := newMemoryMutationSecurityFixture(t, db)
	f.service.principalMemoryQueryService = principalmemory.NewPrincipalMemoryQueryService(f.service.memoryStore, f.service.auditStore)
	private := f.seed(t, f.tokenA, "private", "shared", "")
	server := httptest.NewServer(f.service.router)
	t.Cleanup(server.Close)
	query := func(token string, values url.Values) principalMemoryQueryResponse {
		t.Helper()
		values.Set("principal", "agent/synthetic-security-a")
		values.Set("principal_kind", "agent")
		req := f.request(t, context.Background(), http.MethodGet, server.URL+"/api/memories/principal?"+values.Encode(), token, nil)
		req.RequestURI = ""
		response, err := server.Client().Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode, string(body))
		var result principalMemoryQueryResponse
		require.NoError(t, json.Unmarshal(body, &result))
		return result
	}
	values := func() url.Values { return url.Values{"project": {f.project}, "q": {private.Content}} }
	owner := query(f.tokenA, values())
	require.Len(t, owner.Items, 1)
	require.Equal(t, private.ID, owner.Items[0].ID)
	for label, token := range map[string]string{"same-principal-other-workstation": f.tokenA2, "other-principal": f.tokenB} {
		for _, withProject := range []bool{true, false} {
			params := values()
			params.Set("session_id", "synthetic-security-session")
			if !withProject {
				params.Del("project")
			}
			result := query(token, params)
			if len(result.Items) != 0 {
				t.Errorf("%s with project=%v received %d workstation-private items; want none", label, withProject, len(result.Items))
			}
		}
	}
	withoutIdentity, err := f.service.principalMemoryQueryService.Query(context.Background(), principalmemory.PrincipalMemoryQueryRequest{
		Caller:         principalmemory.PrincipalRef{Principal: "agent/synthetic-security-a", PrincipalKind: "agent"},
		OwnerPrincipal: "agent/synthetic-security-a", OwnerPrincipalKind: "agent", Query: private.Content,
		SourceSessionID: "synthetic-security-session",
	})
	require.NoError(t, err)
	require.Empty(t, withoutIdentity.Items)
	for _, privacy := range []string{"project", "shared", "global"} {
		visible := f.seed(t, f.tokenA, privacy, "shared", "")
		result := query(f.tokenB, url.Values{"q": {visible.Content}})
		require.Len(t, result.Items, 1, privacy)
		require.Equal(t, visible.ID, result.Items[0].ID, privacy)
	}
	visible := f.seed(t, f.tokenA, "project", "shared", "")
	require.NoError(t, db.Exec("UPDATE memories SET content = ? WHERE id = ?", private.Content, visible.ID).Error)
	for range 52 {
		hidden := f.seed(t, f.tokenA2, "private", "shared", "")
		require.NoError(t, db.Exec("UPDATE memories SET content = ? WHERE id = ?", private.Content, hidden.ID).Error)
	}
	first := values()
	first.Set("limit", "1")
	page := query(f.tokenA, first)
	require.Len(t, page.Items, 1)
	require.Equal(t, visible.ID, page.Items[0].ID, "foreign workstation rows must not consume limit")
	second := values()
	second.Set("limit", "1")
	second.Set("offset", "1")
	page = query(f.tokenA, second)
	require.Len(t, page.Items, 1)
	require.Equal(t, private.ID, page.Items[0].ID, "offset counts only accessible rows")
	master := f.tokenAuth.Token()
	f.tokenAuth.SetValidator(auth.NewValidator(master, nil))
	adminValues := values()
	adminValues.Set("include_private", "true")
	adminValues.Set("session_id", "synthetic-security-session")
	admin := query(master, adminValues)
	for _, item := range admin.Items {
		require.NotEqual(t, private.ID, item.ID, "admin widening must not bypass workstation privacy")
	}
}

func TestLegacyPrincipalQueryPrivateMatchIndistinguishableSQLite(t *testing.T) {
	legacyPrincipalQueryPrivacy(t, memoryMutationSQLiteDB(t), true)
}

func TestLegacyPrincipalQueryPrivateMatchIndistinguishablePostgres(t *testing.T) {
	legacyPrincipalQueryPrivacy(t, memoryMutationPostgresDB(t), false)
}

func legacyPrincipalQueryPrivacy(t *testing.T, db *gorm.DB, includeInvalidVisibility bool) {
	t.Helper()
	f := newMemoryMutationSecurityFixture(t, db)
	f.service.principalMemoryQueryService = principalmemory.NewPrincipalMemoryQueryService(f.service.memoryStore, f.service.auditStore)
	server := httptest.NewServer(f.service.router)
	t.Cleanup(server.Close)
	const marker = "syntheticuniquemarigold"
	const privateID = 912345
	add := func(id int, visibility, domain string) {
		t.Helper()
		require.NoError(t, db.Exec(`INSERT INTO memories (id, project, content, tags, status, privacy_scope, source_workstation_id, source_sessions, owner_principal, owner_principal_kind, agent_visibility, domain, created_at, updated_at, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?)`,
			id, f.project, marker, "[]", "active", "project", "synthetic-workstation-a", "{}", "agent/synthetic-security-a", "agent", visibility, domain, 1).Error)
	}
	add(privateID, "private", "")
	query := func(token, term string, widen bool, offset ...string) memoryMutationHTTPResult {
		t.Helper()
		values := url.Values{"principal": {"agent/synthetic-security-a"}, "principal_kind": {"agent"}, "q": {term}, "limit": {"1"}}
		if widen {
			values.Set("include_private", "true")
		}
		if len(offset) > 0 {
			values.Set("offset", offset[0])
		}
		req := f.request(t, context.Background(), http.MethodGet, server.URL+"/api/memories/principal?"+values.Encode(), token, nil)
		req.RequestURI = ""
		response, err := server.Client().Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return memoryMutationHTTPResult{status: response.StatusCode, body: string(body)}
	}
	decode := func(response memoryMutationHTTPResult) (principalMemoryQueryResponse, map[string]any) {
		t.Helper()
		require.Equal(t, http.StatusOK, response.status, response.body)
		var result principalMemoryQueryResponse
		require.NoError(t, json.Unmarshal([]byte(response.body), &result))
		var wire map[string]any
		require.NoError(t, json.Unmarshal([]byte(response.body), &wire))
		require.Contains(t, wire, "hidden_count")
		return result, wire
	}
	owner, _ := decode(query(f.tokenA, marker, false))
	require.Len(t, owner.Items, 1)
	require.EqualValues(t, privateID, owner.Items[0].ID)
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, query("", marker, false).status)
	matchResponse := query(f.tokenB, marker, false)
	missResponse := query(f.tokenB, "syntheticuniquenotpresent", false)
	match, matchWire := decode(matchResponse)
	miss, missWire := decode(missResponse)
	require.Empty(t, match.Items)
	require.Empty(t, miss.Items)
	require.NotContains(t, matchResponse.body, marker)
	require.NotContains(t, matchResponse.body, "synthetic-workstation-a")
	require.Equal(t, http.StatusForbidden, query(f.tokenB, marker, true).status)
	require.Nil(t, matchWire["hidden_count"])
	require.Nil(t, missWire["hidden_count"])
	require.Equal(t, missWire["hidden_count"], matchWire["hidden_count"], "inaccessible matches must not affect hidden_count")
	require.JSONEq(t, missResponse.body, matchResponse.body, "inaccessible matches must not affect response metadata")
	const sharedID = privateID + 1
	add(sharedID, "shared", "")
	add(sharedID+1, "shared", "")
	add(sharedID+2, "shared", "foreign-domain")
	if includeInvalidVisibility {
		add(sharedID+55, "invalid", "")
	}
	for i := range 52 {
		add(sharedID+3+i, "private", "")
	}
	page, _ := decode(query(f.tokenB, marker, false))
	require.Len(t, page.Items, 1)
	require.EqualValues(t, sharedID+1, page.Items[0].ID, "private and foreign-domain rows must not consume the SQL limit")
	secondPage, _ := decode(query(f.tokenB, marker, false, "1"))
	require.Len(t, secondPage.Items, 1)
	require.EqualValues(t, sharedID, secondPage.Items[0].ID, "offset must count only visible rows")
	master := f.tokenAuth.Token()
	f.tokenAuth.SetValidator(auth.NewValidator(master, nil))
	admin, _ := decode(query(master, marker, true))
	require.Len(t, admin.Items, 1)
	require.Equal(t, "written", admin.AuditStatus)
	require.True(t, admin.Audit.Durable)
	var audits int64
	require.NoError(t, db.Table("audit_log").Where("memory_id = ? AND action = ?", admin.Items[0].ID, "principal_memory_private_read").Count(&audits).Error)
	require.EqualValues(t, 1, audits)
	f.service.principalMemoryQueryService = principalmemory.NewPrincipalMemoryQueryService(f.service.memoryStore, nil)
	failed := query(master, marker, true)
	require.Equal(t, http.StatusInternalServerError, failed.status)
	require.NotContains(t, failed.body, marker)
}

func memoryMutationSlot(target gormdb.Memory) gormdb.ContinuitySlot {
	return gormdb.ContinuitySlot{
		Project: target.Project, MemoryID: target.ID, ExpiresAt: time.Now().UTC().Add(time.Hour),
		AuthorityDomain: "synthetic-security-slot", AuthorityOwnerPrincipal: target.OwnerPrincipal,
		AuthorityOwnerPrincipalKind: target.OwnerPrincipalKind,
	}
}

type memoryMutationSecurityState struct {
	memories []gormdb.Memory
	slots    []gormdb.ContinuitySlot
	audit    []gormdb.AuditLogEntry
}

func memoryMutationSnapshot(t *testing.T, db *gorm.DB) memoryMutationSecurityState {
	t.Helper()
	var state memoryMutationSecurityState
	require.NoError(t, db.Unscoped().Order("id").Find(&state.memories).Error)
	require.NoError(t, db.Order("project").Find(&state.slots).Error)
	require.NoError(t, db.Order("id").Find(&state.audit).Error)
	return state
}

var memoryMutationSecurityActions = []string{"delete", "suppress", "bulk_suppress"}

func memoryMutationRequest(action, project string, ids ...int64) (string, string, any) {
	method, route, body := http.MethodDelete, fmt.Sprintf("/api/memories/%d", ids[0]), any(nil)
	switch action {
	case "suppress":
		method, route = http.MethodPost, route+"/suppress"
		body = map[string]any{"reason": "synthetic authorization regression"}
	case "bulk_suppress":
		method, route = http.MethodPost, "/api/memories/suppress"
		body = map[string]any{"ids": ids, "reason": "synthetic authorization regression"}
	}
	if project != "" {
		route += "?project=" + project
	}
	return method, route, body
}

func TestMemoryMutationAccessSecurityDeniedSQLite(t *testing.T) {
	for _, action := range memoryMutationSecurityActions {
		for _, scenario := range []string{
			"foreign_principal", "foreign_principal_without_project", "same_principal_other_workstation",
			"foreign_domain", "wrong_project", "removed_project_without_selector", "unknown_id_in_bulk",
			"unresolved_membership_without_selector", "ambiguous_membership_without_selector",
		} {
			t.Run(action+"/"+scenario, func(t *testing.T) {
				if scenario == "unknown_id_in_bulk" && action != "bulk_suppress" {
					t.Skip("bulk atomicity is exercised by the bulk route")
				}
				f := newMemoryMutationSecurityFixture(t, memoryMutationSQLiteDB(t))
				privacy, visibility, domain := "project", "private", ""
				caller, project := f.tokenB, f.project
				switch scenario {
				case "foreign_principal_without_project":
					project = ""
				case "same_principal_other_workstation":
					privacy, visibility, caller = "private", "shared", f.tokenA2
				case "foreign_domain":
					visibility, domain = "shared", "synthetic-protected-domain"
				case "wrong_project":
					caller, project = f.tokenA, f.other
				case "removed_project_without_selector", "unresolved_membership_without_selector", "ambiguous_membership_without_selector":
					caller, project = f.tokenA, ""
				case "unknown_id_in_bulk":
					caller = f.tokenA
				}
				target := f.seed(t, f.tokenA, privacy, visibility, domain)
				witness := f.seed(t, caller, "project", "private", "")
				require.NoError(t, gormdb.NewContinuitySlotStore(f.db).Upsert(context.Background(), memoryMutationSlot(target)))
				if scenario == "removed_project_without_selector" {
					require.NoError(t, f.db.Model(&gormdb.Project{}).Where("id = ?", f.project).Update("removed_at", time.Now().UTC()).Error)
				}
				if scenario == "unresolved_membership_without_selector" || scenario == "ambiguous_membership_without_selector" {
					key := gormdb.JSONRaw(fmt.Sprintf(`{"id":%d}`, target.ID))
					member := gormdb.ConsolidationMembership{
						Reference: "synthetic-security-membership", Table: "memories", RowKey: key,
						FieldPath: gormdb.JSONRaw(`["project"]`), UnresolvedReference: "synthetic-unresolved",
						PlanFingerprint: "synthetic-security",
					}
					if scenario == "ambiguous_membership_without_selector" {
						member.ProjectID = &f.project
						member.UnresolvedReference = ""
					}
					require.NoError(t, f.db.Create(&member).Error)
					if scenario == "ambiguous_membership_without_selector" {
						member.Reference, member.ProjectID = "synthetic-security-conflicting-membership", &f.other
						require.NoError(t, f.db.Create(&member).Error)
					}
				}
				if scenario == "foreign_principal" || scenario == "foreign_principal_without_project" || scenario == "same_principal_other_workstation" || scenario == "foreign_domain" || scenario == "wrong_project" {
					readProject := f.project
					if scenario == "wrong_project" {
						readProject = f.other
					}
					read := f.serve(f.request(t, context.Background(), http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", target.ID, readProject), caller, nil))
					require.Equal(t, http.StatusNotFound, read.status, read.body)
					require.NotContains(t, read.body, target.Content)
					require.NotContains(t, read.body, target.OwnerPrincipal)
				}
				before := memoryMutationSnapshot(t, f.db)
				ids := []int64{target.ID}
				if action == "bulk_suppress" {
					// Put an authorized row first: denial must roll back the whole batch.
					ids = []int64{witness.ID, target.ID, witness.ID}
				}
				if scenario == "unknown_id_in_bulk" {
					ids = []int64{target.ID, witness.ID + 1000000}
				}
				method, route, body := memoryMutationRequest(action, project, ids...)
				result := f.serve(f.request(t, context.Background(), method, route, caller, body))
				require.Equal(t, http.StatusNotFound, result.status, result.body)
				require.NotContains(t, result.body, target.Content)
				require.NotContains(t, result.body, target.OwnerPrincipal)
				require.Equal(t, before, memoryMutationSnapshot(t, f.db), "denial must preserve every row, version, continuity slot and audit entry")
			})
		}
	}
}

func TestMemoryMutationAccessSecurityAllowedSQLite(t *testing.T) {
	for _, action := range memoryMutationSecurityActions {
		for _, scenario := range []string{"owner", "owner_domain", "same_principal_other_keycard", "shared_foreign_principal", "private_workstation_owner", "owner_without_project"} {
			t.Run(action+"/"+scenario, func(t *testing.T) {
				f := newMemoryMutationSecurityFixture(t, memoryMutationSQLiteDB(t))
				privacy, visibility, domain := "project", "private", ""
				caller, project := f.tokenA, f.project
				switch scenario {
				case "owner_domain":
					domain = "synthetic-owned-domain"
				case "same_principal_other_keycard":
					caller = f.tokenA2
				case "shared_foreign_principal":
					visibility, caller = "shared", f.tokenB
				case "private_workstation_owner":
					privacy = "private"
				case "owner_without_project":
					project = ""
				}
				target := f.seed(t, f.tokenA, privacy, visibility, domain)
				witness := f.seed(t, f.tokenB, "project", "private", "")
				require.NoError(t, gormdb.NewContinuitySlotStore(f.db).Upsert(context.Background(), memoryMutationSlot(target)))
				read := f.serve(f.request(t, context.Background(), http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", target.ID, f.project), caller, nil))
				require.Equal(t, http.StatusOK, read.status, read.body)
				require.Contains(t, read.body, target.Content)
				method, route, body := memoryMutationRequest(action, project, target.ID, target.ID)
				result := f.serve(f.request(t, context.Background(), method, route, caller, body))
				require.Equal(t, http.StatusOK, result.status, result.body)
				var after, witnessAfter gormdb.Memory
				require.NoError(t, f.db.First(&after, target.ID).Error)
				require.NotNil(t, after.DeletedAt)
				require.Greater(t, after.Version, target.Version)
				require.NoError(t, f.db.First(&witnessAfter, witness.ID).Error)
				require.Equal(t, witness, witnessAfter)
				state := memoryMutationSnapshot(t, f.db)
				require.Empty(t, state.slots)
				require.Len(t, state.audit, 1, "the real lifecycle transaction must clear and audit its continuity slot")
				require.Equal(t, &target.ID, state.audit[0].MemoryID)
				require.Contains(t, state.audit[0].Reason, "invalidation_action=delete")
				require.NotContains(t, state.audit[0].Reason, target.Content)
				read = f.serve(f.request(t, context.Background(), http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", target.ID, f.project), caller, nil))
				require.Equal(t, http.StatusNotFound, read.status, read.body)
				require.NotContains(t, read.body, target.Content)
			})
		}
	}
}

// Opt in only with a dedicated disposable PostgreSQL/pgvector URL. An inherited
// DATABASE_DSN is deliberately insufficient. The existing fixture creates and
// migrates a random schema, closes its pools and drops only that schema.
func memoryMutationPostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("ENGRAM_MEMORY_MUTATION_TEST_DSN")
	if dsn == "" {
		t.Skip("requires isolated disposable PostgreSQL/pgvector: set ENGRAM_MEMORY_MUTATION_TEST_DSN; DATABASE_DSN alone is never used")
	}
	t.Setenv("DATABASE_DSN", dsn)
	store := openWorkerUCIContextCompositionStore(t)
	sqlDB, err := store.DB.DB()
	require.NoError(t, err)
	// Holder, real route transaction, observer and NOWAIT control are independent.
	sqlDB.SetMaxOpenConns(4)
	return store.DB
}

func memoryMutationBeginLock(t *testing.T, db *gorm.DB, id int64) (*gorm.DB, int) {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { _ = tx.Rollback().Error })
	var pid int
	require.NoError(t, tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error)
	require.NoError(t, tx.Exec("SELECT id FROM memories WHERE id = ? FOR UPDATE", id).Error)
	return tx, pid
}

func memoryMutationAssertLockHeld(t *testing.T, db *gorm.DB, id int64) {
	t.Helper()
	probe := db.Begin()
	require.NoError(t, probe.Error)
	err := probe.Exec("SELECT id FROM memories WHERE id = ? FOR UPDATE NOWAIT", id).Error
	require.NoError(t, probe.Rollback().Error)
	var pgError *pgconn.PgError
	require.ErrorAs(t, err, &pgError, "the foreign holder must still own the row lock after the denied request returned")
	require.Equal(t, "55P03", pgError.Code)
}

func memoryMutationStartRequest(t *testing.T, f *memoryMutationSecurityFixture, method, route, token string, body any) <-chan memoryMutationHTTPResult {
	t.Helper()
	return memoryMutationStartHTTPRequest(t, f, f.request(t, context.Background(), method, route, token, body))
}

func memoryMutationStartHTTPRequest(t *testing.T, f *memoryMutationSecurityFixture, request *http.Request) <-chan memoryMutationHTTPResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	req := request.WithContext(ctx)
	results := make(chan memoryMutationHTTPResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		results <- f.serve(req)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("synthetic request did not stop after cancellation")
		}
	})
	return results
}

func memoryMutationAwaitResult(t *testing.T, results <-chan memoryMutationHTTPResult) memoryMutationHTTPResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish while the foreign row lock remained held")
		return memoryMutationHTTPResult{}
	}
}

func memoryMutationAwaitBlocked(t *testing.T, db *gorm.DB, holderPID int) {
	t.Helper()
	var probeErr error
	require.Eventually(t, func() bool {
		var count int64
		probeErr = db.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> ?
			AND ? = ANY(pg_blocking_pids(pid)) AND wait_event_type = 'Lock'`, holderPID, holderPID).Scan(&count).Error
		return probeErr == nil && count > 0
	}, 5*time.Second, 10*time.Millisecond, "real route must reach a PostgreSQL lock wait before the concurrent transaction commits")
	require.NoError(t, probeErr)
}

func TestMemoryMutationAccessSecurityDeniedDoesNotWaitPostgres(t *testing.T) {
	db := memoryMutationPostgresDB(t)
	for _, action := range memoryMutationSecurityActions {
		for _, scenario := range []string{"foreign_principal", "foreign_domain", "same_principal_other_workstation", "wrong_project"} {
			t.Run(action+"/"+scenario, func(t *testing.T) {
				f := newMemoryMutationSecurityFixture(t, db)
				privacy, visibility, domain, caller, project := "project", "private", "", f.tokenB, f.project
				switch scenario {
				case "foreign_domain":
					visibility, domain = "shared", "synthetic-foreign-domain"
				case "same_principal_other_workstation":
					privacy, visibility, caller = "private", "shared", f.tokenA2
				case "wrong_project":
					caller, project = f.tokenA, f.other
				}
				target := f.seed(t, f.tokenA, privacy, visibility, domain)
				witness := f.seed(t, caller, "project", "private", "")
				require.NoError(t, gormdb.NewContinuitySlotStore(db).Upsert(context.Background(), memoryMutationSlot(target)))
				before := memoryMutationSnapshot(t, db)
				holder, _ := memoryMutationBeginLock(t, db, target.ID)
				defer func() { _ = holder.Rollback().Error }()
				ids := []int64{target.ID}
				if action == "bulk_suppress" {
					ids = []int64{witness.ID, target.ID}
				}
				method, route, body := memoryMutationRequest(action, project, ids...)
				result := memoryMutationAwaitResult(t, memoryMutationStartRequest(t, f, method, route, caller, body))
				require.Equal(t, http.StatusNotFound, result.status, result.body)
				require.NotContains(t, result.body, target.Content)
				require.NotContains(t, result.body, target.OwnerPrincipal)
				memoryMutationAssertLockHeld(t, db, target.ID)
				require.Equal(t, before, memoryMutationSnapshot(t, db))
			})
		}
	}
}

func TestMemoryMutationAccessSecurityVisibilityRecheckedPostgres(t *testing.T) {
	db := memoryMutationPostgresDB(t)
	for _, action := range memoryMutationSecurityActions {
		for _, scenario := range []string{"principal_private", "workstation_private", "foreign_domain", "removed_project"} {
			t.Run(action+"/"+scenario, func(t *testing.T) {
				f := newMemoryMutationSecurityFixture(t, db)
				target := f.seed(t, f.tokenA, "project", "shared", "")
				witness := f.seed(t, f.tokenB, "project", "private", "")
				require.NoError(t, gormdb.NewContinuitySlotStore(db).Upsert(context.Background(), memoryMutationSlot(target)))
				holder, pid := memoryMutationBeginLock(t, db, target.ID)
				defer func() { _ = holder.Rollback().Error }()
				updates := map[string]any{"agent_visibility": "private"}
				switch scenario {
				case "workstation_private":
					updates = map[string]any{"privacy_scope": "private"}
				case "foreign_domain":
					updates = map[string]any{"domain": "synthetic-revoked-domain"}
				}
				if scenario == "removed_project" {
					require.NoError(t, holder.Model(&gormdb.Project{}).Where("id = ?", f.project).UpdateColumn("removed_at", time.Now().UTC()).Error)
				} else {
					require.NoError(t, holder.Model(&gormdb.Memory{}).Where("id = ?", target.ID).UpdateColumns(updates).Error)
				}
				expected := memoryMutationSnapshot(t, holder)
				ids := []int64{target.ID}
				if action == "bulk_suppress" {
					ids = []int64{witness.ID, target.ID}
				}
				method, route, body := memoryMutationRequest(action, f.project, ids...)
				results := memoryMutationStartRequest(t, f, method, route, f.tokenB, body)
				// MVCC preflight sees the old shared row. Observe the actual DB wait,
				// not a sleep or an implementation-specific callback, then revoke.
				memoryMutationAwaitBlocked(t, db, pid)
				require.NoError(t, holder.Commit().Error)
				result := memoryMutationAwaitResult(t, results)
				require.Equal(t, http.StatusNotFound, result.status, result.body)
				require.NotContains(t, result.body, target.Content)
				require.NotContains(t, result.body, target.OwnerPrincipal)
				require.Equal(t, expected, memoryMutationSnapshot(t, db), "only the concurrent visibility edit may persist; denied batch and continuity/audit must remain untouched")
			})
		}
	}
}

func TestMemoryMutationAccessSecurityMemoryBeforeSlotPostgres(t *testing.T) {
	db := memoryMutationPostgresDB(t)
	for _, action := range memoryMutationSecurityActions {
		t.Run(action, func(t *testing.T) {
			f := newMemoryMutationSecurityFixture(t, db)
			target := f.seed(t, f.tokenA, "project", "private", "")
			witness := f.seed(t, f.tokenB, "project", "private", "")
			before := memoryMutationSnapshot(t, db)
			holder, pid := memoryMutationBeginLock(t, db, target.ID)
			defer func() { _ = holder.Rollback().Error }()
			method, route, body := memoryMutationRequest(action, f.project, target.ID)
			results := memoryMutationStartRequest(t, f, method, route, f.tokenA, body)
			memoryMutationAwaitBlocked(t, db, pid)
			// Set the slot under the already-held memory lock. The public route
			// must lock memory first, then discover and clear this committed slot.
			require.NoError(t, gormdb.NewContinuitySlotStore(db).UpsertTx(context.Background(), holder, memoryMutationSlot(target)))
			require.NoError(t, holder.Commit().Error)
			result := memoryMutationAwaitResult(t, results)
			require.Equal(t, http.StatusOK, result.status, result.body)
			var after, witnessAfter gormdb.Memory
			require.NoError(t, db.First(&after, target.ID).Error)
			require.NotNil(t, after.DeletedAt)
			require.Greater(t, after.Version, target.Version)
			require.NoError(t, db.First(&witnessAfter, witness.ID).Error)
			require.Equal(t, witness, witnessAfter)
			state := memoryMutationSnapshot(t, db)
			for _, slot := range state.slots {
				require.NotEqual(t, target.ID, slot.MemoryID)
			}
			require.Len(t, state.audit, len(before.audit)+1)
			entry := state.audit[len(state.audit)-1]
			require.Equal(t, &target.ID, entry.MemoryID)
			require.Contains(t, entry.Reason, "invalidation_action=delete")
			require.NotContains(t, entry.Reason, target.Content)
			require.Nil(t, entry.BeforeState)
			require.Nil(t, entry.AfterState)
		})
	}
}

func memoryMutationBrowserSessions(t *testing.T, f *memoryMutationSecurityFixture) (string, string, string) {
	t.Helper()
	users, sessions := gormdb.NewUserStore(f.db), gormdb.NewAuthSessionStore(f.db)
	f.tokenAuth.SetAuthStores(users, sessions)
	user, err := users.CreateUser(uuid.NewString()+"@synthetic.invalid", "", gormdb.DashboardRoleOperator)
	require.NoError(t, err)
	other, err := users.CreateUser(uuid.NewString()+"@synthetic.invalid", "", gormdb.DashboardRoleOperator)
	require.NoError(t, err)
	newSession := func(userID int64) string {
		session, err := sessions.CreateSession(userID, time.Hour, "synthetic-memory-security", "192.0.2.1")
		require.NoError(t, err)
		return session.ID
	}
	return newSession(user.ID), newSession(user.ID), newSession(other.ID)
}

func memoryMutationBrowserRequest(t *testing.T, f *memoryMutationSecurityFixture, route, sessionID, requestID string, body any) *http.Request {
	t.Helper()
	req := f.request(t, context.Background(), http.MethodPost, route, "", body)
	req.Header.Del("Authorization")
	req.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: sessionID})
	if requestID != "" {
		req.Header.Set("X-Engram-Request-ID", requestID)
	}
	return req
}

func memoryMutationBrowserPreview(t *testing.T, f *memoryMutationSecurityFixture, sessionID, kind string, target gormdb.Memory) memoryCollectionSelectionResponse {
	t.Helper()
	selection := map[string]any{"kind": kind, "project": f.project}
	if kind == "explicit" {
		selection["targets"] = []map[string]any{{"id": fmt.Sprint(target.ID), "expected_version": target.Version}}
	}
	result := f.serve(memoryMutationBrowserRequest(t, f, "/api/memories/selection", sessionID, "", map[string]any{"selection": selection}))
	require.Equal(t, http.StatusOK, result.status, result.body)
	var response struct {
		Selection memoryCollectionSelectionResponse `json:"selection"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.body), &response))
	require.Greater(t, response.Selection.Version, int64(0))
	return response.Selection
}

func memoryMutationBrowserOperation(t *testing.T, f *memoryMutationSecurityFixture, sessionID, action string, selection memoryCollectionSelectionResponse) *http.Request {
	t.Helper()
	const requestID = "synthetic-memory-security-operation"
	return memoryMutationBrowserRequest(t, f, "/api/memories/operations", sessionID, requestID, map[string]any{
		"request_id": requestID, "action": action,
		"selection": map[string]any{
			"kind": selection.Kind, "selection_version": selection.Version, "selection_token": selection.Token,
		},
	})
}

func TestMemoryMutationAccessSecurityBrowserSessionBoundPostgres(t *testing.T) {
	db := memoryMutationPostgresDB(t)
	f := newMemoryMutationSecurityFixture(t, db)
	session, sameUserOtherSession, foreignUserSession := memoryMutationBrowserSessions(t, f)
	target := f.seed(t, f.tokenA, "project", "shared", "")
	require.NoError(t, gormdb.NewContinuitySlotStore(db).Upsert(context.Background(), memoryMutationSlot(target)))
	selection := memoryMutationBrowserPreview(t, f, session, "frozen_filter", target)
	require.NotEmpty(t, selection.Token)
	before := memoryMutationSnapshot(t, db)
	for _, caller := range []struct{ name, session string }{
		{"same_user_other_session", sameUserOtherSession}, {"foreign_user", foreignUserSession},
	} {
		t.Run(caller.name, func(t *testing.T) {
			result := f.serve(memoryMutationBrowserOperation(t, f, caller.session, "archive", selection))
			require.Equal(t, http.StatusForbidden, result.status, result.body)
			require.NotContains(t, result.body, target.Content)
			require.NotContains(t, result.body, target.OwnerPrincipal)
			require.Equal(t, before, memoryMutationSnapshot(t, db), "another authenticated session must not reuse a frozen memory selection")
		})
	}
	result := f.serve(memoryMutationBrowserOperation(t, f, session, "archive", selection))
	require.Equal(t, http.StatusOK, result.status, result.body)
	var after gormdb.Memory
	require.NoError(t, db.First(&after, target.ID).Error)
	require.Equal(t, "archived", after.Status)
	require.Nil(t, after.DeletedAt, "browser collection actions retain their status transition contract, not legacy DeleteTx semantics")
	require.Greater(t, after.Version, target.Version)
	state := memoryMutationSnapshot(t, db)
	require.Equal(t, before.slots, state.slots)
	require.Equal(t, before.audit, state.audit)
}

func TestMemoryMutationAccessSecurityBrowserVisibilityRecheckedPostgres(t *testing.T) {
	db := memoryMutationPostgresDB(t)
	for _, action := range []string{"suppress", "unsuppress", "archive"} {
		for _, scenario := range []string{"principal_private", "workstation_private", "foreign_domain", "removed_project"} {
			t.Run(action+"/"+scenario, func(t *testing.T) {
				f := newMemoryMutationSecurityFixture(t, db)
				session, _, _ := memoryMutationBrowserSessions(t, f)
				target := f.seed(t, f.tokenA, "project", "shared", "")
				if action == "unsuppress" {
					require.NoError(t, db.Model(&gormdb.Memory{}).Where("id = ?", target.ID).UpdateColumn("status", "flagged").Error)
					require.NoError(t, db.First(&target, target.ID).Error)
				}
				require.NoError(t, gormdb.NewContinuitySlotStore(db).Upsert(context.Background(), memoryMutationSlot(target)))
				selection := memoryMutationBrowserPreview(t, f, session, "explicit", target)
				holder, pid := memoryMutationBeginLock(t, db, target.ID)
				defer func() { _ = holder.Rollback().Error }()
				updates := map[string]any{"agent_visibility": "private"}
				switch scenario {
				case "workstation_private":
					updates = map[string]any{"privacy_scope": "private"}
				case "foreign_domain":
					updates = map[string]any{"domain": "synthetic-browser-revoked-domain"}
				}
				if scenario == "removed_project" {
					require.NoError(t, holder.Model(&gormdb.Project{}).Where("id = ?", f.project).UpdateColumn("removed_at", time.Now().UTC()).Error)
				} else {
					require.NoError(t, holder.Model(&gormdb.Memory{}).Where("id = ?", target.ID).UpdateColumns(updates).Error)
				}
				var changed gormdb.Memory
				require.NoError(t, holder.First(&changed, target.ID).Error)
				require.Equal(t, target.Version, changed.Version, "the fixture must revoke visibility without changing the CAS version")
				expected := memoryMutationSnapshot(t, holder)
				results := memoryMutationStartHTTPRequest(t, f, memoryMutationBrowserOperation(t, f, session, action, selection))
				memoryMutationAwaitBlocked(t, db, pid)
				require.NoError(t, holder.Commit().Error)
				result := memoryMutationAwaitResult(t, results)
				require.Equal(t, http.StatusMultiStatus, result.status, result.body)
				var response memoryCollectionSelectionOperationResponse
				require.NoError(t, json.Unmarshal([]byte(result.body), &response))
				require.Equal(t, "partial", response.OperationState)
				require.Len(t, response.ItemResults, 1)
				require.Equal(t, "denied", response.ItemResults[0].Outcome)
				require.Equal(t, "redacted", response.ItemResults[0].TargetID)
				require.Nil(t, response.ItemResults[0].ObservedVersion)
				require.Nil(t, response.Readback)
				require.NotContains(t, result.body, target.Content)
				require.NotContains(t, result.body, target.OwnerPrincipal)
				require.Equal(t, expected, memoryMutationSnapshot(t, db), "version/status CAS must not authorize a now-invisible row or change its continuity/audit")
			})
		}
	}
}

// The trigger only delays the real UPDATE; it returns NEW without changing any
// value or authorization decision. Reaching it proves the ordinary reads and
// checks preceding the effect have already run. No production hook is needed.
func memoryMutationInstallEffectGate(t *testing.T, db *gorm.DB, targetID int64) (*gorm.DB, int) {
	t.Helper()
	name := "memory_mutation_authority_gate_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	var key int64
	require.NoError(t, db.Raw("SELECT hashtextextended(current_schema() || ?, 0)", name).Scan(&key).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(%d); RETURN NEW; END; $$`, name, key)).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Exec("DROP TRIGGER IF EXISTS "+name+" ON memories").Error)
		require.NoError(t, db.Exec("DROP FUNCTION IF EXISTS "+name+"()").Error)
	})
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE OF deleted_at ON memories
		FOR EACH ROW WHEN (OLD.id = %d AND OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL)
		EXECUTE FUNCTION %s()`, name, targetID, name)).Error)
	gate := db.Begin()
	require.NoError(t, gate.Error)
	t.Cleanup(func() { _ = gate.Rollback().Error })
	var pid int
	require.NoError(t, gate.Raw("SELECT pg_backend_pid()").Scan(&pid).Error)
	require.NoError(t, gate.Exec("SELECT pg_advisory_xact_lock(?)", key).Error)
	return gate, pid
}

func memoryMutationAwaitEffectGate(t *testing.T, db *gorm.DB, gatePID int) int {
	t.Helper()
	var pids []int
	var probeErr error
	require.Eventually(t, func() bool {
		probeErr = db.Raw(`SELECT pid FROM pg_stat_activity WHERE datname = current_database()
			AND ? = ANY(pg_blocking_pids(pid)) AND wait_event_type = 'Lock'`, gatePID).Scan(&pids).Error
		return probeErr == nil && len(pids) == 1
	}, 5*time.Second, 10*time.Millisecond, "the real mutation must reach the test-only BEFORE UPDATE gate")
	require.NoError(t, probeErr)
	return pids[0]
}

func TestMemoryMutationAccessSecurityLateAuthorityChangePostgres(t *testing.T) {
	db := memoryMutationPostgresDB(t)
	for _, action := range memoryMutationSecurityActions {
		for _, change := range []string{"project_removed", "membership_reassigned", "membership_conflict_inserted"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				f := newMemoryMutationSecurityFixture(t, db)
				target := f.seed(t, f.tokenA, "project", "private", "")
				witness := f.seed(t, f.tokenB, "project", "private", "")
				require.NoError(t, gormdb.NewContinuitySlotStore(db).Upsert(context.Background(), memoryMutationSlot(target)))
				member := gormdb.ConsolidationMembership{
					Reference: "synthetic-late-authority-" + uuid.NewString(), Table: "memories",
					RowKey: gormdb.JSONRaw(fmt.Sprintf(`{"id":%d}`, target.ID)), FieldPath: gormdb.JSONRaw(`["project"]`),
					ProjectID: &f.project, PlanFingerprint: "synthetic-late-authority",
				}
				if change != "project_removed" {
					require.NoError(t, db.Create(&member).Error)
				}
				before := memoryMutationSnapshot(t, db)
				gate, gatePID := memoryMutationInstallEffectGate(t, db, target.ID)
				defer func() { _ = gate.Rollback().Error }()
				method, route, body := memoryMutationRequest(action, f.project, target.ID)
				mutationResults := memoryMutationStartRequest(t, f, method, route, f.tokenA, body)
				mutationPID := memoryMutationAwaitEffectGate(t, db, gatePID)

				// Start authority invalidation only after the mutation reached its
				// effect boundary. Keep a fourth connection available for observers.
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				changer := db.WithContext(ctx).Begin()
				require.NoError(t, changer.Error)
				var changerPID int
				require.NoError(t, changer.Raw("SELECT pg_backend_pid()").Scan(&changerPID).Error)
				authorityResults := make(chan error, 1)
				authorityFinished := make(chan struct{})
				changedReference := member.Reference
				if change == "membership_conflict_inserted" {
					changedReference += "-conflict"
				}
				go func() {
					defer close(authorityFinished)
					var result *gorm.DB
					switch change {
					case "project_removed":
						result = changer.Model(&gormdb.Project{}).Where("id = ?", f.project).UpdateColumn("removed_at", time.Now().UTC())
					case "membership_reassigned":
						result = changer.Model(&gormdb.ConsolidationMembership{}).Where("reference = ?", member.Reference).UpdateColumn("project_id", f.other)
					case "membership_conflict_inserted":
						conflict := member
						conflict.Reference, conflict.ProjectID = changedReference, &f.other
						result = changer.Create(&conflict)
					}
					if result.Error != nil {
						_ = changer.Rollback().Error
						authorityResults <- result.Error
						return
					}
					if result.RowsAffected != 1 {
						_ = changer.Rollback().Error
						authorityResults <- fmt.Errorf("synthetic authority change affected %d rows", result.RowsAffected)
						return
					}
					authorityResults <- changer.Commit().Error
				}()
				t.Cleanup(func() {
					cancel()
					select {
					case <-authorityFinished:
					case <-time.After(5 * time.Second):
						t.Error("synthetic authority transaction did not stop after cancellation")
					}
				})

				// Accept either legal order. A committed invalidation while the
				// effect is gated requires a later denied/rolled-back mutation.
				// Otherwise observe the real invalidation waiting on the mutation's
				// retained authority lock, then release the effect so it commits first.
				authorityFirst := false
				var authorityErr, probeErr error
				require.Eventually(t, func() bool {
					select {
					case authorityErr = <-authorityResults:
						authorityFirst = true
						return true
					default:
					}
					var waitsForMutation bool
					probeErr = db.Raw("SELECT ? = ANY(pg_blocking_pids(?))", mutationPID, changerPID).Scan(&waitsForMutation).Error
					return probeErr == nil && waitsForMutation
				}, 5*time.Second, 10*time.Millisecond, "authority invalidation must either commit or wait on the in-flight mutation")
				require.NoError(t, probeErr)
				require.NoError(t, authorityErr)
				require.NoError(t, gate.Rollback().Error)
				mutation := memoryMutationAwaitResult(t, mutationResults)
				if !authorityFirst {
					select {
					case authorityErr = <-authorityResults:
					case <-ctx.Done():
						t.Fatal("authority invalidation did not finish after the mutation released its locks")
					}
					require.NoError(t, authorityErr)
				}

				if change == "project_removed" {
					var project gormdb.Project
					require.NoError(t, db.First(&project, "id = ?", f.project).Error)
					require.NotNil(t, project.RemovedAt)
				} else {
					var changed gormdb.ConsolidationMembership
					require.NoError(t, db.First(&changed, "reference = ?", changedReference).Error)
					require.Equal(t, &f.other, changed.ProjectID)
				}
				require.NotContains(t, mutation.body, target.Content)
				require.NotContains(t, mutation.body, target.OwnerPrincipal)
				state := memoryMutationSnapshot(t, db)
				if authorityFirst {
					require.Equal(t, http.StatusNotFound, mutation.status, mutation.body)
					require.Equal(t, before, state, "authority committed before effect: the denied mutation must roll back memory/version/slot/audit")
					return
				}
				require.Equal(t, http.StatusOK, mutation.status, mutation.body)
				var after, witnessAfter gormdb.Memory
				require.NoError(t, db.First(&after, target.ID).Error)
				require.NotNil(t, after.DeletedAt)
				require.Greater(t, after.Version, target.Version)
				expected := target
				expected.DeletedAt, expected.UpdatedAt, expected.Version = after.DeletedAt, after.UpdatedAt, after.Version
				require.Equal(t, expected, after)
				require.NoError(t, db.First(&witnessAfter, witness.ID).Error)
				require.Equal(t, witness, witnessAfter)
				require.Len(t, state.memories, len(before.memories))
				require.Len(t, state.slots, len(before.slots)-1)
				for _, slot := range state.slots {
					require.NotEqual(t, target.ID, slot.MemoryID)
				}
				require.Len(t, state.audit, len(before.audit)+1)
				require.Equal(t, before.audit, state.audit[:len(before.audit)])
				entry := state.audit[len(state.audit)-1]
				require.Equal(t, &target.ID, entry.MemoryID)
				require.Contains(t, entry.Reason, "invalidation_action=delete")
				require.NotContains(t, entry.Reason, target.Content)
				require.Nil(t, entry.BeforeState)
				require.Nil(t, entry.AfterState)
			})
		}
	}
}

type memoryMutationDeadlockRecorder struct {
	logger.Interface
	mu        sync.Mutex
	deadlocks int
}

func (r *memoryMutationDeadlockRecorder) Trace(ctx context.Context, begin time.Time, query func() (string, int64), err error) {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "40P01" {
		r.mu.Lock()
		r.deadlocks++
		r.mu.Unlock()
	}
	r.Interface.Trace(ctx, begin, query, err)
}

func (r *memoryMutationDeadlockRecorder) deadlockCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.deadlocks
}

func memoryMutationInstallOverlapGate(t *testing.T, db *gorm.DB, firstID, secondID int64) (*gorm.DB, int) {
	t.Helper()
	// Run before the native engram_consolidation_scope_write trigger without
	// replacing it. Shared advisory acquisition lets both effects proceed once
	// the exclusive holder releases the gate; it does not serialize the callers.
	name := "aaa_memory_mutation_overlap_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	var key int64
	require.NoError(t, db.Raw("SELECT hashtextextended(current_schema() || ?, 0)", name).Scan(&key).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock_shared(%d); RETURN NEW; END; $$`, name, key)).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Exec("DROP TRIGGER IF EXISTS "+name+" ON memories").Error)
		require.NoError(t, db.Exec("DROP FUNCTION IF EXISTS "+name+"()").Error)
	})
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE OF deleted_at ON memories
		FOR EACH ROW WHEN (OLD.id IN (%d, %d) AND OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL)
		EXECUTE FUNCTION %s()`, name, firstID, secondID, name)).Error)
	gate := db.Begin()
	require.NoError(t, gate.Error)
	t.Cleanup(func() { _ = gate.Rollback().Error })
	var pid int
	require.NoError(t, gate.Raw("SELECT pg_backend_pid()").Scan(&pid).Error)
	require.NoError(t, gate.Exec("SELECT pg_advisory_xact_lock(?)", key).Error)
	return gate, pid
}

func TestMemoryMutationAccessSecurityConcurrentAllowedMutationsPostgres(t *testing.T) {
	db := memoryMutationPostgresDB(t)
	recorder := &memoryMutationDeadlockRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	requestDB := db.Session(&gorm.Session{Logger: recorder})
	first := newMemoryMutationSecurityFixture(t, requestDB)
	second := newMemoryMutationSecurityFixture(t, requestDB)
	firstTarget := first.seed(t, first.tokenA, "project", "private", "")
	secondTarget := second.seed(t, second.tokenA, "project", "private", "")
	witness := first.seed(t, first.tokenB, "project", "private", "")
	for _, target := range []gormdb.Memory{firstTarget, secondTarget} {
		require.NoError(t, gormdb.NewContinuitySlotStore(db).Upsert(context.Background(), memoryMutationSlot(target)))
		project := target.Project
		require.NoError(t, db.Create(&gormdb.ConsolidationMembership{
			Reference: "synthetic-concurrent-mutation-" + uuid.NewString(), Table: "memories",
			RowKey: gormdb.JSONRaw(fmt.Sprintf(`{"id":%d}`, target.ID)), FieldPath: gormdb.JSONRaw(`["project"]`),
			ProjectID: &project, PlanFingerprint: "synthetic-concurrent-mutation",
		}).Error)
	}
	var nativeGuards int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_trigger WHERE tgrelid = 'memories'::regclass
		AND tgname = 'engram_consolidation_scope_write' AND NOT tgisinternal AND tgenabled IN ('O', 'A')`).Scan(&nativeGuards).Error)
	require.Equal(t, int64(1), nativeGuards, "the real consolidation trigger must remain enabled")
	before := memoryMutationSnapshot(t, db)
	var membershipsBefore []gormdb.ConsolidationMembership
	require.NoError(t, db.Order("reference").Find(&membershipsBefore).Error)
	gate, gatePID := memoryMutationInstallOverlapGate(t, db, firstTarget.ID, secondTarget.ID)
	defer func() { _ = gate.Rollback().Error }()
	firstMethod, firstRoute, firstBody := memoryMutationRequest("delete", first.project, firstTarget.ID)
	secondMethod, secondRoute, secondBody := memoryMutationRequest("suppress", second.project, secondTarget.ID)
	firstResults := memoryMutationStartRequest(t, first, firstMethod, firstRoute, first.tokenA, firstBody)
	secondResults := memoryMutationStartRequest(t, second, secondMethod, secondRoute, second.tokenA, secondBody)

	var holders []int
	var probeErr error
	require.Eventually(t, func() bool {
		probeErr = db.Raw(`SELECT DISTINCT activity.pid FROM pg_stat_activity activity
			JOIN pg_locks held ON held.pid = activity.pid
			WHERE activity.datname = current_database() AND ? = ANY(pg_blocking_pids(activity.pid))
			AND activity.wait_event_type = 'Lock' AND held.locktype = 'relation'
			AND held.relation = 'project_consolidation_memberships'::regclass
			AND held.mode = 'ShareLock' AND held.granted ORDER BY activity.pid`, gatePID).Scan(&holders).Error
		return probeErr == nil && len(holders) == 2
	}, 5*time.Second, 10*time.Millisecond, "both real mutations must overlap before UPDATE while each holds membership SHARE")
	require.NoError(t, probeErr)
	require.NoError(t, gate.Rollback().Error)
	firstResult := memoryMutationAwaitResult(t, firstResults)
	secondResult := memoryMutationAwaitResult(t, secondResults)
	require.Zero(t, recorder.deadlockCount(), "the native mutation/trigger path must not raise PostgreSQL 40P01")
	require.Equal(t, http.StatusOK, firstResult.status, firstResult.body)
	require.Equal(t, http.StatusOK, secondResult.status, secondResult.body)
	for _, target := range []gormdb.Memory{firstTarget, secondTarget} {
		var after gormdb.Memory
		require.NoError(t, db.First(&after, target.ID).Error)
		require.NotNil(t, after.DeletedAt)
		require.Greater(t, after.Version, target.Version)
		expected := target
		expected.DeletedAt, expected.UpdatedAt, expected.Version = after.DeletedAt, after.UpdatedAt, after.Version
		require.Equal(t, expected, after)
	}
	var witnessAfter gormdb.Memory
	require.NoError(t, db.First(&witnessAfter, witness.ID).Error)
	require.Equal(t, witness, witnessAfter)
	state := memoryMutationSnapshot(t, db)
	require.Len(t, state.memories, len(before.memories))
	require.Empty(t, state.slots)
	require.Len(t, state.audit, len(before.audit)+2)
	seen := make(map[int64]bool, 2)
	for _, entry := range state.audit[len(before.audit):] {
		require.NotNil(t, entry.MemoryID)
		require.Contains(t, []int64{firstTarget.ID, secondTarget.ID}, *entry.MemoryID)
		require.False(t, seen[*entry.MemoryID], "each continuity slot must be cleared and audited exactly once")
		seen[*entry.MemoryID] = true
		require.Contains(t, entry.Reason, "invalidation_action=delete")
		require.NotContains(t, entry.Reason, firstTarget.Content)
		require.NotContains(t, entry.Reason, secondTarget.Content)
		require.Nil(t, entry.BeforeState)
		require.Nil(t, entry.AfterState)
	}
	var membershipsAfter []gormdb.ConsolidationMembership
	require.NoError(t, db.Order("reference").Find(&membershipsAfter).Error)
	require.Equal(t, membershipsBefore, membershipsAfter, "ordinary mutations must preserve the unchanged canonical membership evidence")
}
