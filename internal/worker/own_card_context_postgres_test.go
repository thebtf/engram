package worker

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/pkg/models"
)

// Feature009 AU-02/AU-06/AU-08 and B1/B3/B4, limited to enrollment,
// create/read, privacy and request-key persistence. The parent runs this only
// against ENGRAM_SEARCH_PRIVACY_TEST_DSN, never DATABASE_DSN or a mirror.
// openContextSearchPrivacyStore creates and drops a generated private schema;
// the disposable database must already have pgvector installed in public.
func TestOwnCardContextPostgres(t *testing.T) {
	// Current main deliberately uses a synthetic identity when auth is disabled;
	// this suite proves only the authenticated keycard path.
	for _, disabled := range []bool{false} {
		name := "auth_enabled"
		t.Run(name, func(t *testing.T) {
			f := newOwnCardContextFixture(t, disabled)
			t.Run("enrollment_requires_a_valid_browser_admin", f.testEnrollmentAuthorization)
			t.Run("enrollment_validates_the_existing_canonical_pair", f.testEnrollmentValidation)
			t.Run("enrollment_is_deliberate_idempotent_and_not_backfilled", f.testDeliberateEnrollment)
			t.Run("omitted_selector_uses_self_and_authenticated_author", f.testSelfCreateRead)
			t.Run("concurrent_revision_real_http", f.testConcurrentRevisionHTTP)
			t.Run("revision_history_failure_is_atomic", f.testRevisionHistoryFailure)
			t.Run("revision_audit_failure_is_atomic", f.testRevisionAuditFailure)
			t.Run("default_shared_self_context_isolates_canonical_owner", f.testDefaultSharedSelfIsolation)
			t.Run("untrusted_callers_cannot_invent_self", f.testUntrustedSelf)
			t.Run("private_self_access_follows_principal_not_card", f.testPrivateSelfReplacement)
			t.Run("shared_default_and_principal_private_are_distinct", f.testProjectVisibility)
			t.Run("workstation_private_remains_a_separate_restriction", f.testWorkstationPrivacy)
			t.Run("invalid_selectors_have_no_memory_or_project_effect", f.testInvalidSelectors)
			t.Run("valid_legacy_project_without_identity_header_still_works", f.testLegacyProject)
			t.Run("request_key_replays_survive_service_and_card_replacement", f.testPersistedReplay)
			t.Run("request_key_conflicts_do_not_mutate_the_card", f.testReplayConflicts)
			t.Run("concurrent_equivalent_requests_commit_one_card", f.testConcurrentReplay)
			t.Run("request_keys_are_scoped_by_context_and_canonical_pair", f.testReplayScopes)
			t.Run("enrollment_can_precede_keycard_issuance", f.testEnrollmentBeforeIssuance)
			t.Run("retired_or_missing_request_key_target_is_not_recreated", f.testRetiredReplay)
			t.Run("ledger_insert_failure_rolls_back_body_and_allows_retry", f.testFailedLedgerInsert)
			t.Run("snapshot_restore_preserves_typed_ownership", f.testContextSnapshotRestore)
		})
	}
}

type ownCardContextIdentity struct {
	PrincipalKind string `json:"principal_kind"`
	Principal     string `json:"principal"`
}

type ownCardContextDescriptor struct {
	Kind          string          `json:"kind"`
	PrincipalKind string          `json:"principal_kind,omitempty"`
	Principal     string          `json:"principal,omitempty"`
	Project       json.RawMessage `json:"project,omitempty"`
}

type ownCardContextRecord struct {
	models.Memory
	Context *ownCardContextDescriptor `json:"context"`
	Author  *ownCardContextIdentity   `json:"author"`
}

type ownCardContextCredential struct {
	identity ownCardContextIdentity
	token    string
	id       string
}

type ownCardContextCaller struct {
	token    string
	cookie   *http.Cookie
	explicit bool
}

func (card ownCardContextCredential) caller() ownCardContextCaller {
	return ownCardContextCaller{token: card.token, explicit: true}
}

type ownCardContextFixture struct {
	store    *gormdb.Store
	tokens   *gormdb.TokenStore
	users    *gormdb.UserStore
	sessions *gormdb.AuthSessionStore
	service  *Service
	admin    ownCardContextCaller
	operator ownCardContextCaller
	master   string
	disabled bool
}

func newOwnCardContextFixture(t *testing.T, disabled bool) *ownCardContextFixture {
	t.Helper()
	dsn := os.Getenv("ENGRAM_SEARCH_PRIVACY_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_SEARCH_PRIVACY_TEST_DSN not set; parent must select a disposable loopback PostgreSQL database with pgvector")
	}
	// Validate without formatting the URL or parse error, which can contain a
	// password. The tracked fixture performs its own target/schema checks too.
	parsed, err := url.Parse(dsn)
	if err != nil || parsed == nil {
		t.Fatal("disposable fixture DSN must be a valid PostgreSQL URL; value withheld")
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		t.Fatal("disposable fixture DSN must use postgres or postgresql")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() || parsed.Port() == "" || strings.Trim(parsed.Path, "/") == "" {
		t.Fatal("disposable fixture DSN must explicitly select a loopback IP, port and database")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		t.Fatal("disposable fixture DSN has invalid options; values withheld")
	}
	for key := range query {
		switch strings.ToLower(key) {
		case "sslmode", "connect_timeout", "application_name":
		default:
			t.Fatal("disposable fixture DSN has an unsupported option; target and schema overrides are forbidden")
		}
	}
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSERVICEFILE", "")
	t.Setenv("PGOPTIONS", "")
	t.Setenv("ENGRAM_AUTH_DISABLED", strconv.FormatBool(disabled))
	t.Setenv("ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT", "false")
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "false")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	store := openContextSearchPrivacyStore(t)
	f := &ownCardContextFixture{
		store: store, tokens: gormdb.NewTokenStore(store),
		users: gormdb.NewUserStore(store.DB), sessions: gormdb.NewAuthSessionStore(store.DB),
		master: uuid.NewString(), disabled: disabled,
	}
	f.admin = f.browser(t, gormdb.DashboardRoleAdmin, time.Hour, false, false)
	f.operator = f.browser(t, gormdb.DashboardRoleOperator, time.Hour, false, false)
	f.reopen(t)
	return f
}

// Reconstruct all HTTP/auth/memory-store objects while retaining only the SQL
// store. A replay after this boundary cannot depend on a Service-local cache.
func (f *ownCardContextFixture) reopen(t *testing.T) {
	t.Helper()
	ta, err := NewTokenAuth(f.master)
	require.NoError(t, err)
	ta.SetTokenStore(f.tokens)
	ta.SetValidator(auth.NewValidator(f.master, f.tokens))
	ta.SetAuthStores(f.users, f.sessions)
	f.service = &Service{
		router: chi.NewRouter(), tokenAuth: ta, config: config.Default(), store: f.store,
		tokenStore: f.tokens, memoryStore: gormdb.NewMemoryStore(f.store),
		sessionStore: gormdb.NewSessionStore(f.store), auditStore: gormdb.NewAuditStore(f.store.DB),
		mcpHealth: mcp.NewMCPHealth(), retrievalStats: make(map[string]*RetrievalStats),
		authHandlers: NewAuthHandlers(f.users, gormdb.NewInvitationStore(f.store.DB), f.sessions, gormdb.NewDomainOwnerStore(f.store)),
	}
	f.service.principalMemoryQueryService = principalmemory.NewPrincipalMemoryQueryService(f.service.memoryStore, f.service.auditStore)
	f.service.ready.Store(true)
	f.service.setupMiddleware()
	f.service.setupRoutes()
}

func (f *ownCardContextFixture) browser(t *testing.T, role string, lifetime time.Duration, revoked, disabled bool) ownCardContextCaller {
	t.Helper()
	user, err := f.users.CreateUser("own-card-"+uuid.NewString()+"@example.invalid", "synthetic-unused-password-hash", role)
	require.NoError(t, err)
	session, err := f.sessions.CreateSession(user.ID, lifetime, "own-card-context-test", "127.0.0.1")
	require.NoError(t, err)
	if revoked {
		changed, err := f.sessions.RevokeSession(session.ID, nil, "synthetic test revocation")
		require.NoError(t, err)
		require.True(t, changed)
	}
	if disabled {
		require.NoError(t, f.store.DB.Model(&gormdb.User{}).Where("id = ?", user.ID).Update("disabled", true).Error)
	}
	return ownCardContextCaller{cookie: &http.Cookie{Name: authSessionCookieName, Value: session.ID}}
}

func (f *ownCardContextFixture) keycard(t *testing.T, kind, principal string) ownCardContextCredential {
	t.Helper()
	raw := auth.TokenRawPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
	require.NoError(t, err)
	card, err := f.tokens.CreateWithPrincipal(t.Context(), gormdb.TokenCreatePrincipalInput{
		Name: "own-card-" + uuid.NewString(), TokenHash: string(hash),
		TokenPrefix: raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen],
		Scope:       "read-write", Principal: principal, PrincipalKind: kind,
	})
	require.NoError(t, err)
	return ownCardContextCredential{
		identity: ownCardContextIdentity{PrincipalKind: kind, Principal: principal}, token: raw, id: card.ID,
	}
}

func (f *ownCardContextFixture) registered(t *testing.T, label string) ownCardContextCredential {
	t.Helper()
	card := f.keycard(t, "agent", "agent/own-card-"+label+"-"+uuid.NewString())
	f.enroll(t, card.identity)
	return card
}

func (f *ownCardContextFixture) enroll(t *testing.T, identity ownCardContextIdentity, statuses ...int) {
	t.Helper()
	if len(statuses) == 0 {
		statuses = []int{http.StatusOK, http.StatusCreated}
	}
	rec := f.call(t, http.MethodPost, "/api/auth/principals", identity, f.admin)
	require.Contains(t, statuses, rec.Code, "browser-admin enrollment must return the expected new/existing status; response withheld")
	var enrolled ownCardContextIdentity
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &enrolled))
	require.Equal(t, identity, enrolled, "enrollment must return the normalized existing canonical pair")
}

func (f *ownCardContextFixture) publicKeycard(t *testing.T, identity ownCardContextIdentity) ownCardContextCredential {
	t.Helper()
	rec := f.call(t, http.MethodPost, "/api/auth/tokens", map[string]any{
		"name": "own-card-replacement-" + uuid.NewString(), "scope": "read-write",
		"principal_kind": identity.PrincipalKind, "principal": identity.Principal,
	}, f.admin)
	require.Equal(t, http.StatusOK, rec.Code, "ordinary keycard issuance must remain available; response withheld")
	var issued struct {
		ID            string `json:"id"`
		Token         string `json:"token"`
		PrincipalKind string `json:"principal_kind"`
		Principal     string `json:"principal"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &issued))
	require.NotEmpty(t, issued.ID)
	if issued.Token == "" {
		t.Fatal("ordinary keycard issuance omitted its one-time credential")
	}
	require.Equal(t, identity.PrincipalKind, issued.PrincipalKind)
	require.Equal(t, identity.Principal, issued.Principal)
	return ownCardContextCredential{identity: identity, token: issued.Token, id: issued.ID}
}

func ownCardContextRequest(t *testing.T, method, path string, payload any, caller ownCardContextCaller) *http.Request {
	t.Helper()
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1:39999"+path, bytes.NewReader(body))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "http://127.0.0.1:39999")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if caller.explicit {
		req.Header.Set("X-Engram-Identity", "keycard")
	}
	if caller.token != "" {
		req.Header.Set("Authorization", "Bearer "+caller.token)
	}
	if caller.cookie != nil {
		req.AddCookie(caller.cookie)
	}
	return req
}

func (f *ownCardContextFixture) call(t *testing.T, method, path string, payload any, caller ownCardContextCaller) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.service.router.ServeHTTP(rec, ownCardContextRequest(t, method, path, payload, caller))
	return rec
}

func ownCardContextPayload(content string) map[string]any {
	return map[string]any{
		"content": content, "tags": []string{"type:fact", "synthetic-own-card"},
		"source_agent": "client-supplied-source-not-the-authenticated-author",
	}
}

func ownCardContextDecode(t *testing.T, rec *httptest.ResponseRecorder, statuses ...int) ownCardContextRecord {
	t.Helper()
	require.Contains(t, statuses, rec.Code, "ordinary memory route status; response withheld")
	var record ownCardContextRecord
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &record))
	require.Positive(t, record.ID, "memory identity must be a stable positive numeric ID")
	return record
}

func (f *ownCardContextFixture) create(t *testing.T, card ownCardContextCredential, payload map[string]any) ownCardContextRecord {
	t.Helper()
	return ownCardContextDecode(t, f.call(t, http.MethodPost, "/api/memories", payload, card.caller()), http.StatusCreated)
}

func (f *ownCardContextFixture) assertTyped(t *testing.T, record ownCardContextRecord, identity ownCardContextIdentity, kind, project string) {
	t.Helper()
	require.NotNil(t, record.Context, "new typed records must disclose their resolved owning context")
	want := ownCardContextDescriptor{Kind: kind}
	if kind == "self" {
		want.PrincipalKind, want.Principal = identity.PrincipalKind, identity.Principal
	}
	assert.Equal(t, want, *record.Context)
	assert.Equal(t, project, record.Project, "self/global must not acquire a fake project")
	require.NotNil(t, record.Author, "authenticated author is independent of source_agent")
	assert.Equal(t, identity, *record.Author)
	assert.Equal(t, identity.Principal, record.OwnerPrincipal)
	assert.Equal(t, identity.PrincipalKind, record.OwnerPrincipalKind)
	var stored struct {
		ID                  int64
		Project             string
		Content             string
		SourceAgent         string
		OwnerPrincipal      string
		OwnerPrincipalKind  string
		AgentVisibility     string
		PrivacyScope        string
		SourceWorkstationID string
	}
	require.NoError(t, f.store.DB.Table("memories").Where("id = ?", record.ID).Take(&stored).Error)
	assert.Equal(t, record.ID, stored.ID)
	assert.Equal(t, project, stored.Project)
	assert.Equal(t, record.Content, stored.Content, "the existing memories table remains the body owner")
	assert.Equal(t, record.SourceAgent, stored.SourceAgent)
	assert.Equal(t, identity.Principal, stored.OwnerPrincipal)
	assert.Equal(t, identity.PrincipalKind, stored.OwnerPrincipalKind)
	assert.Equal(t, record.AgentVisibility, stored.AgentVisibility)
	assert.Equal(t, record.PrivacyScope, stored.PrivacyScope)
	assert.Equal(t, record.SourceWorkstationID, stored.SourceWorkstationID)
}

type ownCardContextState struct {
	memories string
	projects string
	users    int64
	tokens   int64
}

func (f *ownCardContextFixture) state(t *testing.T) ownCardContextState {
	t.Helper()
	var state ownCardContextState
	require.NoError(t, f.store.DB.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(m) ORDER BY m.id), '[]'::jsonb)::text FROM memories m`).Scan(&state.memories).Error)
	require.NoError(t, f.store.DB.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY p.id), '[]'::jsonb)::text FROM projects p`).Scan(&state.projects).Error)
	require.NoError(t, f.store.DB.Table("users").Count(&state.users).Error)
	require.NoError(t, f.store.DB.Table("api_tokens").Count(&state.tokens).Error)
	return state
}

func (f *ownCardContextFixture) memoryCount(t *testing.T) int64 {
	t.Helper()
	var count int64
	require.NoError(t, f.store.DB.Table("memories").Count(&count).Error)
	return count
}

func (f *ownCardContextFixture) project(t *testing.T) string {
	t.Helper()
	project := uuid.NewString()
	require.NoError(t, f.store.DB.Create(&gormdb.Project{ID: project}).Error)
	return project
}

func ownCardContextDenied(t *testing.T, rec *httptest.ResponseRecorder, hidden ownCardContextRecord, statuses ...int) {
	t.Helper()
	require.Contains(t, statuses, rec.Code, "denied request must fail closed; response withheld")
	assert.NotContains(t, rec.Body.String(), hidden.Content, "denial must not expose memory content")
	if hidden.OwnerPrincipal != "" {
		assert.NotContains(t, rec.Body.String(), hidden.OwnerPrincipal, "denial must not expose private owner metadata")
	}
}

func (f *ownCardContextFixture) testEnrollmentAuthorization(t *testing.T) {
	login := f.call(t, http.MethodPost, "/api/auth/login", map[string]any{"token": f.master}, ownCardContextCaller{})
	require.Equal(t, http.StatusOK, login.Code, "public master-token login must issue a genuine HMAC cookie; response withheld")
	loginResponse := login.Result()
	var hmacCookie *http.Cookie
	for _, cookie := range loginResponse.Cookies() {
		if cookie.Name == sessionCookieName && cookie.Value != "" {
			hmacCookie = cookie
			break
		}
	}
	require.NoError(t, loginResponse.Body.Close())
	require.NotNil(t, hmacCookie, "public login must return its signed session cookie; response withheld")
	cardCaller := f.keycard(t, "agent", "agent/own-card-enrollment-caller-"+uuid.NewString())
	callers := []struct {
		name   string
		caller ownCardContextCaller
	}{
		{"missing_session_or_synthetic_admin", ownCardContextCaller{}},
		{"invalid_session", ownCardContextCaller{cookie: &http.Cookie{Name: authSessionCookieName, Value: "invalid-synthetic-session"}}},
		{"non_admin_session", f.operator},
		{"master_bearer_is_not_a_browser_session", ownCardContextCaller{token: f.master}},
		{"hmac_session_is_not_a_persisted_browser_admin", ownCardContextCaller{cookie: hmacCookie}},
		{"keycard_cannot_self_enroll", cardCaller.caller()},
		{"expired_session", f.browser(t, gormdb.DashboardRoleAdmin, -time.Hour, false, false)},
		{"revoked_session", f.browser(t, gormdb.DashboardRoleAdmin, time.Hour, true, false)},
		{"disabled_admin_session", f.browser(t, gormdb.DashboardRoleAdmin, time.Hour, false, true)},
	}
	for _, tc := range callers {
		t.Run(tc.name, func(t *testing.T) {
			candidate := cardCaller
			if tc.name != "keycard_cannot_self_enroll" {
				candidate = f.keycard(t, "agent", "agent/own-card-denied-enrollment-"+uuid.NewString())
			}
			before := f.state(t)
			rec := f.call(t, http.MethodPost, "/api/auth/principals", candidate.identity, tc.caller)
			require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code, "only a validated browser admin may enroll; response withheld")
			if tc.name == "hmac_session_is_not_a_persisted_browser_admin" {
				hmacSelf := f.call(t, http.MethodPost, "/api/memories", ownCardContextPayload("HMAC admin session must not invent a canonical self owner"), tc.caller)
				require.Contains(t, []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden}, hmacSelf.Code, "HMAC session must not acquire self ownership")
			}
			rec = f.call(t, http.MethodPost, "/api/memories", ownCardContextPayload("denied enrollment must not grant self access"), candidate.caller())
			require.Contains(t, []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden}, rec.Code)
			assert.Equal(t, before, f.state(t), "denied enrollment must not create memories, projects, users or keycards")
		})
	}
}

func (f *ownCardContextFixture) testEnrollmentValidation(t *testing.T) {
	for _, payload := range []any{
		map[string]any{},
		map[string]any{"principal_kind": "agent"},
		map[string]any{"principal_kind": "daemon", "principal": "agent/own-card-invalid-kind"},
		map[string]any{"principal_kind": "agent", "principal": " \t "},
	} {
		before := f.state(t)
		rec := f.call(t, http.MethodPost, "/api/auth/principals", payload, f.admin)
		require.Equal(t, http.StatusBadRequest, rec.Code, "enrollment requires a nonempty principal and a valid kind when supplied; response withheld")
		assert.Equal(t, before, f.state(t))
	}
	t.Run("omitted_kind_uses_existing_human_default", func(t *testing.T) {
		card := f.keycard(t, "human", "operator/own-card-human-default-"+uuid.NewString())
		before := f.state(t)
		rec := f.call(t, http.MethodPost, "/api/auth/principals", map[string]any{"principal": card.identity.Principal}, f.admin)
		require.Equal(t, http.StatusCreated, rec.Code, "omitted-kind enrollment of a new pair must return 201; response withheld")
		var enrolled ownCardContextIdentity
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &enrolled))
		require.Equal(t, "human", enrolled.PrincipalKind, "omitted kind must return the existing human default")
		require.Equal(t, card.identity.Principal, enrolled.Principal)
		assert.Equal(t, before, f.state(t), "default-kind enrollment must not create an account, credential, project or body")
		created := f.create(t, card, ownCardContextPayload("human-default enrollment permits the corresponding canonical keycard to use self"))
		f.assertTyped(t, created, card.identity, "self", "")
		f.reopen(t)
		read := ownCardContextDecode(t, f.call(t, http.MethodGet, "/api/memories/"+strconv.FormatInt(created.ID, 10), nil, card.caller()), http.StatusOK)
		assert.Equal(t, created.ID, read.ID)
		assert.Equal(t, created.Content, read.Content)
		f.assertTyped(t, read, card.identity, "self", "")
	})
}

func (f *ownCardContextFixture) testDeliberateEnrollment(t *testing.T) {
	principal := "agent/own-card-deliberate-" + uuid.NewString()
	selected := f.keycard(t, "agent", principal)
	unselected := f.keycard(t, "agent", "agent/own-card-not-backfilled-"+uuid.NewString())
	otherKind := f.keycard(t, "human", principal)
	for _, card := range []ownCardContextCredential{selected, unselected, otherKind} {
		rec := f.call(t, http.MethodPost, "/api/memories", ownCardContextPayload("issuing a named card is not self enrollment"), card.caller())
		require.Contains(t, []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden}, rec.Code)
	}
	before := f.state(t)
	f.enroll(t, selected.identity, http.StatusCreated)
	f.enroll(t, selected.identity, http.StatusOK)
	assert.Equal(t, before, f.state(t), "enrollment binds the existing pair; it creates no account, credential, project or body")
	payload := ownCardContextPayload("deliberately enrolled canonical agent owns this card")
	created := f.create(t, selected, payload)
	f.assertTyped(t, created, selected.identity, "self", "")
	for _, card := range []ownCardContextCredential{unselected, otherKind} {
		before := f.state(t)
		rec := f.call(t, http.MethodPost, "/api/memories", ownCardContextPayload("another existing keycard must not be automatically enrolled"), card.caller())
		require.Contains(t, []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden}, rec.Code)
		assert.Equal(t, before, f.state(t))
	}
	f.enroll(t, selected.identity, http.StatusOK)
	read := ownCardContextDecode(t, f.call(t, http.MethodGet, "/api/memories/"+strconv.FormatInt(created.ID, 10), nil, selected.caller()), http.StatusOK)
	assert.Equal(t, created.ID, read.ID, "re-enrollment must preserve the existing card")
}

func (f *ownCardContextFixture) testSelfCreateRead(t *testing.T) {
	card := f.registered(t, "self")
	before := f.state(t)
	payload := ownCardContextPayload("Собственная запись: не переносить приватные сведения в общий проект.")
	created := f.create(t, card, payload)
	f.assertTyped(t, created, card.identity, "self", "")
	assert.Equal(t, models.AgentVisibilityShared, created.AgentVisibility, "self ownership does not silently change the shared visibility default")
	assert.Equal(t, payload["source_agent"], created.SourceAgent)
	assert.NotEqual(t, created.SourceAgent, created.Author.Principal)
	assert.Equal(t, card.id, created.SourceWorkstationID)
	assert.Equal(t, before.projects, f.state(t).projects, "project-free save must not synthesize a project")
	f.reopen(t)
	read := ownCardContextDecode(t, f.call(t, http.MethodGet, "/api/memories/"+strconv.FormatInt(created.ID, 10), nil, card.caller()), http.StatusOK)
	assert.Equal(t, created.ID, read.ID)
	assert.Equal(t, created.Content, read.Content)
	assert.Equal(t, created.Tags, read.Tags)
	assert.Equal(t, created.SourceAgent, read.SourceAgent)
	f.assertTyped(t, read, card.identity, "self", "")
	assert.Equal(t, before.projects, f.state(t).projects)
	path := "/api/memories/" + strconv.FormatInt(created.ID, 10)
	other := f.registered(t, "revision-other")
	denied := f.call(t, http.MethodPatch, path, map[string]any{"version": created.Version, "content": "forbidden revision"}, other.caller())
	require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, denied.Code)
	denied = f.call(t, http.MethodGet, path+"/revisions", nil, other.caller())
	require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, denied.Code)
	replacement := f.publicKeycard(t, card.identity)
	updated := ownCardContextDecode(t, f.call(t, http.MethodPatch, path, map[string]any{"version": created.Version, "content": "revised synthetic card"}, replacement.caller()), http.StatusOK)
	assert.Equal(t, created.ID, updated.ID)
	assert.Equal(t, created.Version+1, updated.Version)
	assert.Equal(t, "revised synthetic card", updated.Content)
	var auditCount int64
	require.NoError(t, f.store.DB.Table("audit_log").Where("memory_id = ? AND action = ? AND actor = ?", created.ID, "update", card.identity.Principal).Count(&auditCount).Error)
	require.EqualValues(t, 1, auditCount, "a successful revision retains its authenticated actor")
	stale := f.call(t, http.MethodPatch, path, map[string]any{"version": created.Version, "content": "revised synthetic card"}, replacement.caller())
	require.Equal(t, http.StatusConflict, stale.Code)
	history := f.call(t, http.MethodGet, path+"/revisions", nil, replacement.caller())
	require.Equal(t, http.StatusOK, history.Code)
	var revisions struct {
		Revisions []struct {
			Version int    `json:"version"`
			Content string `json:"content"`
		} `json:"revisions"`
	}
	require.NoError(t, json.Unmarshal(history.Body.Bytes(), &revisions))
	require.Len(t, revisions.Revisions, 1)
	assert.Equal(t, created.Version, revisions.Revisions[0].Version)
	assert.Equal(t, created.Content, revisions.Revisions[0].Content)
	page := f.call(t, http.MethodGet, path+"/revisions?after_version="+strconv.Itoa(created.Version)+"&limit=1", nil, replacement.caller())
	require.Equal(t, http.StatusOK, page.Code)
	var next struct {
		Revisions []struct {
			Version int `json:"version"`
		} `json:"revisions"`
	}
	require.NoError(t, json.Unmarshal(page.Body.Bytes(), &next))
	require.Empty(t, next.Revisions)
	// A later restriction applies to old revision bodies too.
	require.NoError(t, f.store.DB.Model(&gormdb.Memory{}).Where("id = ?", created.ID).Update("privacy_scope", "private").Error)
	denied = f.call(t, http.MethodGet, path+"/revisions", nil, replacement.caller())
	require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, denied.Code)
	assert.NotContains(t, denied.Body.String(), created.Content)
	require.Equal(t, http.StatusOK, f.call(t, http.MethodGet, path+"/revisions", nil, card.caller()).Code)
}

func (f *ownCardContextFixture) testDefaultSharedSelfIsolation(t *testing.T) {
	owner := f.registered(t, "shared-self-owner")
	other := f.registered(t, "shared-self-other")
	otherKind := f.keycard(t, "human", owner.identity.Principal)
	f.enroll(t, otherKind.identity)
	payload := ownCardContextPayload("default-shared self card has owner-only synthetic content " + uuid.NewString())
	source := "self-owner-source-" + uuid.NewString()
	tag := "self-owner-tag-" + uuid.NewString()
	payload["source_agent"], payload["tags"] = source, []string{"type:fact", tag}
	beforeCreate := f.state(t)
	created := f.create(t, owner, payload)
	require.Equal(t, payload["content"], created.Content)
	require.Equal(t, models.AgentVisibilityShared, created.AgentVisibility, "self isolation must not be simulated by changing the shared default to private")
	require.Equal(t, source, created.SourceAgent)
	require.Equal(t, []string{"type:fact", tag}, created.Tags)
	f.assertTyped(t, created, owner.identity, "self", "")
	assert.Equal(t, beforeCreate.projects, f.state(t).projects, "default-shared self creation must not synthesize a project")
	path := "/api/memories/" + strconv.FormatInt(created.ID, 10)
	read := ownCardContextDecode(t, f.call(t, http.MethodGet, path, nil, owner.caller()), http.StatusOK)
	assert.Equal(t, created.ID, read.ID)
	assert.Equal(t, created.Content, read.Content)
	assert.Equal(t, created.SourceAgent, read.SourceAgent)
	assert.Equal(t, created.Tags, read.Tags)
	f.assertTyped(t, read, owner.identity, "self", "")
	queryPath := "/api/memories/principal?" + url.Values{
		"principal": {owner.identity.Principal}, "principal_kind": {owner.identity.PrincipalKind},
		"visibility": {"shared"}, "limit": {"50"},
	}.Encode()
	ownerQuery := f.call(t, http.MethodGet, queryPath, nil, owner.caller())
	require.Equal(t, http.StatusOK, ownerQuery.Code, "real project-free principal query must retain owner access; response withheld")
	var ownerQueryResult principalMemoryQueryResponse
	require.NoError(t, json.Unmarshal(ownerQuery.Body.Bytes(), &ownerQueryResult))
	require.Len(t, ownerQueryResult.Items, 1, "owner control must find the self record through the existing principal reader")
	assert.Equal(t, created.ID, ownerQueryResult.Items[0].ID)
	assert.Equal(t, created.Content, ownerQueryResult.Items[0].Content)
	assert.Equal(t, created.Tags, ownerQueryResult.Items[0].Tags)
	assert.Equal(t, owner.identity.Principal, ownerQueryResult.Items[0].OwnerPrincipal)
	assert.Equal(t, owner.identity.PrincipalKind, ownerQueryResult.Items[0].OwnerPrincipalKind)
	assert.Nil(t, ownerQueryResult.HiddenCount)
	for _, tc := range []struct {
		name string
		card ownCardContextCredential
	}{
		{"different_enrolled_principal", other},
		{"same_principal_different_enrolled_kind", otherKind},
	} {
		t.Run(tc.name, func(t *testing.T) {
			own := f.create(t, tc.card, ownCardContextPayload("comparison principal has a usable independent self context: "+tc.name))
			f.assertTyped(t, own, tc.card.identity, "self", "")
			ownRead := ownCardContextDecode(t, f.call(t, http.MethodGet, "/api/memories/"+strconv.FormatInt(own.ID, 10), nil, tc.card.caller()), http.StatusOK)
			assert.Equal(t, own.ID, ownRead.ID)
			assert.Equal(t, own.Content, ownRead.Content)
			f.assertTyped(t, ownRead, tc.card.identity, "self", "")
			t.Run("principal_query", func(t *testing.T) {
				beforeQuery := f.state(t)
				queried := f.call(t, http.MethodGet, queryPath, nil, tc.card.caller())
				assert.Equal(t, beforeQuery, f.state(t), "foreign principal query must not mutate memories, projects, users or keycard counts")
				require.Contains(t, []int{http.StatusOK, http.StatusForbidden, http.StatusNotFound}, queried.Code, "existing principal reader must fail closed or return a nondisclosing empty result; response withheld")
				for _, hidden := range []string{created.Content, source, tag, owner.id} {
					assert.NotContains(t, queried.Body.String(), hidden, "principal query must not expose another self context's body or identifying record metadata")
				}
				if queried.Code == http.StatusOK {
					var result principalMemoryQueryResponse
					require.NoError(t, json.Unmarshal(queried.Body.Bytes(), &result))
					assert.Empty(t, result.Items, "principal query must not return self record IDs or metadata across canonical owners")
					assert.Nil(t, result.HiddenCount, "inaccessible self record counts are unknown")
				}
			})
			beforeDenied := f.state(t)
			denied := f.call(t, http.MethodGet, path, nil, tc.card.caller())
			assert.Equal(t, beforeDenied, f.state(t), "cross-context ID lookup must not mutate memories, projects, users or keycard counts")
			ownCardContextDenied(t, denied, created, http.StatusForbidden, http.StatusNotFound)
			for _, hidden := range []string{source, tag, created.SourceWorkstationID} {
				if hidden != "" {
					assert.NotContains(t, denied.Body.String(), hidden, "cross-context denial must not expose the owner's source, tags or workstation metadata")
				}
			}
		})
	}
	beforeReplacement := f.state(t)
	memoryCount := f.memoryCount(t)
	replacement := f.publicKeycard(t, owner.identity)
	require.NotEqual(t, owner.id, replacement.id)
	expected := beforeReplacement
	expected.tokens++
	assert.Equal(t, expected, f.state(t), "replacement issuance adds only its keycard, without moving or copying self records")
	f.reopen(t)
	read = ownCardContextDecode(t, f.call(t, http.MethodGet, path, nil, replacement.caller()), http.StatusOK)
	assert.Equal(t, created.ID, read.ID)
	assert.Equal(t, created.Content, read.Content)
	assert.Equal(t, created.SourceAgent, read.SourceAgent)
	assert.Equal(t, created.Tags, read.Tags)
	assert.Equal(t, models.AgentVisibilityShared, read.AgentVisibility)
	assert.Equal(t, owner.id, read.SourceWorkstationID, "canonical-owner access must not rewrite the original workstation provenance")
	f.assertTyped(t, read, owner.identity, "self", "")
	assert.Equal(t, memoryCount, f.memoryCount(t))
}

func (f *ownCardContextFixture) testUntrustedSelf(t *testing.T) {
	unregistered := f.keycard(t, "agent", "agent/own-card-unregistered-"+uuid.NewString())
	unbound := f.keycard(t, "", "")
	for _, tc := range []struct {
		name   string
		caller ownCardContextCaller
	}{
		{"unregistered_principal", unregistered.caller()},
		{"unbound_keycard", unbound.caller()},
		{"master_without_opt_in", ownCardContextCaller{token: f.master}},
		{"master_with_opt_in", ownCardContextCaller{token: f.master, explicit: true}},
		{"bare_or_synthetic_admin", ownCardContextCaller{}},
		{"keycard_mode_without_bearer", ownCardContextCaller{explicit: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, explicitSelf := range []bool{false, true} {
				payload := ownCardContextPayload("untrusted caller must not invent an owning self context")
				if explicitSelf {
					payload["context"] = map[string]any{"kind": "self"}
				}
				before := f.state(t)
				rec := f.call(t, http.MethodPost, "/api/memories", payload, tc.caller)
				require.Contains(t, []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden}, rec.Code)
				assert.Equal(t, before, f.state(t))
			}
		})
	}
}

func (f *ownCardContextFixture) testPrivateSelfReplacement(t *testing.T) {
	owner := f.registered(t, "private-owner")
	other := f.registered(t, "private-other")
	unregistered := f.keycard(t, "agent", "agent/own-card-private-unregistered-"+uuid.NewString())
	unbound := f.keycard(t, "", "")
	otherKind := f.keycard(t, "human", owner.identity.Principal)
	f.enroll(t, otherKind.identity)
	payload := ownCardContextPayload("private self card: synthetic cobalt-only owner secret")
	payload["agent_visibility"] = models.AgentVisibilityPrivate
	created := f.create(t, owner, payload)
	f.assertTyped(t, created, owner.identity, "self", "")
	assert.Equal(t, models.AgentVisibilityPrivate, created.AgentVisibility)
	path := "/api/memories/" + strconv.FormatInt(created.ID, 10)
	for _, caller := range []ownCardContextCaller{
		other.caller(), otherKind.caller(), unregistered.caller(), unbound.caller(),
		{},
		{token: f.master},
		{token: owner.token},
	} {
		if !f.disabled && caller.token == owner.token {
			continue // Enabled-auth legacy bearer is the same validated owner.
		}
		ownCardContextDenied(t, f.call(t, http.MethodGet, path, nil, caller), created,
			http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound)
	}
	before := f.memoryCount(t)
	replacement := f.publicKeycard(t, owner.identity)
	require.NotEqual(t, owner.id, replacement.id)
	rec := f.call(t, http.MethodDelete, "/api/auth/tokens/"+owner.id, nil, f.admin)
	require.Equal(t, http.StatusOK, rec.Code)
	f.reopen(t)
	read := ownCardContextDecode(t, f.call(t, http.MethodGet, path, nil, replacement.caller()), http.StatusOK)
	assert.Equal(t, created.ID, read.ID)
	assert.Equal(t, created.Content, read.Content)
	assert.Equal(t, owner.id, read.SourceWorkstationID, "credential replacement must not move or rewrite the original card")
	f.assertTyped(t, read, owner.identity, "self", "")
	assert.Equal(t, before, f.memoryCount(t))
	ownCardContextDenied(t, f.call(t, http.MethodGet, path, nil, owner.caller()), created, http.StatusUnauthorized)
}

func (f *ownCardContextFixture) testProjectVisibility(t *testing.T) {
	owner := f.registered(t, "project-owner")
	other := f.registered(t, "project-reader")
	unregistered := f.keycard(t, "agent", "agent/own-card-project-unregistered-"+uuid.NewString())
	unbound := f.keycard(t, "", "")
	otherKind := f.keycard(t, "human", owner.identity.Principal)
	f.enroll(t, otherKind.identity)
	project := f.project(t)
	sharedPayload := ownCardContextPayload("existing project shared-default synthetic knowledge")
	sharedPayload["project"] = project
	sharedPayload["context"] = map[string]any{"kind": "project"}
	shared := f.create(t, owner, sharedPayload)
	f.assertTyped(t, shared, owner.identity, "project", project)
	assert.Equal(t, models.AgentVisibilityShared, shared.AgentVisibility)
	sharedPath := "/api/memories/" + strconv.FormatInt(shared.ID, 10) + "?project=" + project
	read := ownCardContextDecode(t, f.call(t, http.MethodGet, sharedPath, nil, other.caller()), http.StatusOK)
	assert.Equal(t, shared.Content, read.Content, "default project sharing remains a separate positive consumer path")
	f.assertTyped(t, read, owner.identity, "project", project)
	privatePayload := ownCardContextPayload("principal-private synthetic project knowledge must stay hidden")
	privatePayload["project"], privatePayload["agent_visibility"] = project, models.AgentVisibilityPrivate
	privatePayload["context"] = map[string]any{"kind": "project"}
	private := f.create(t, owner, privatePayload)
	f.assertTyped(t, private, owner.identity, "project", project)
	assert.Equal(t, models.AgentVisibilityPrivate, private.AgentVisibility)
	privatePath := "/api/memories/" + strconv.FormatInt(private.ID, 10) + "?project=" + project
	own := ownCardContextDecode(t, f.call(t, http.MethodGet, privatePath, nil, owner.caller()), http.StatusOK)
	assert.Equal(t, private.Content, own.Content)
	for _, caller := range []ownCardContextCaller{
		other.caller(), otherKind.caller(), unregistered.caller(), unbound.caller(),
		{},
		{token: f.master},
	} {
		ownCardContextDenied(t, f.call(t, http.MethodGet, privatePath, nil, caller), private,
			http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound)
	}
}

func (f *ownCardContextFixture) testWorkstationPrivacy(t *testing.T) {
	owner := f.registered(t, "workstation-owner")
	replacement := f.publicKeycard(t, owner.identity)
	project := f.project(t)
	payload := ownCardContextPayload("workstation-private remains bound to its original card")
	payload["project"], payload["privacy_scope"], payload["agent_visibility"] = project, "private", models.AgentVisibilityShared
	created := f.create(t, owner, payload)
	assert.Equal(t, "private", created.PrivacyScope)
	assert.Equal(t, models.AgentVisibilityShared, created.AgentVisibility)
	assert.Equal(t, owner.id, created.SourceWorkstationID)
	path := "/api/memories/" + strconv.FormatInt(created.ID, 10) + "?project=" + project
	read := ownCardContextDecode(t, f.call(t, http.MethodGet, path, nil, owner.caller()), http.StatusOK)
	assert.Equal(t, created.Content, read.Content)
	ownCardContextDenied(t, f.call(t, http.MethodGet, path, nil, replacement.caller()), created, http.StatusForbidden, http.StatusNotFound)
}

func (f *ownCardContextFixture) testInvalidSelectors(t *testing.T) {
	card := f.registered(t, "invalid-selector")
	project := f.project(t)
	for _, tc := range []struct {
		name   string
		fields map[string]any
	}{
		{"empty_project", map[string]any{"project": ""}},
		{"blank_project", map[string]any{"project": " \t "}},
		{"null_project", map[string]any{"project": nil}},
		{"malformed_project", map[string]any{"project": "not-a-canonical-project"}},
		{"unknown_project", map[string]any{"project": uuid.NewString()}},
		{"empty_context", map[string]any{"context": map[string]any{}}},
		{"null_context", map[string]any{"context": nil}},
		{"string_context", map[string]any{"context": "self"}},
		{"empty_context_kind", map[string]any{"context": map[string]any{"kind": ""}}},
		{"unknown_context_kind", map[string]any{"context": map[string]any{"kind": "assistant-session"}}},
		{"project_context_without_project", map[string]any{"context": map[string]any{"kind": "project"}}},
		{"project_context_with_empty_project", map[string]any{"project": "", "context": map[string]any{"kind": "project"}}},
		{"self_context_conflicts_with_project", map[string]any{"project": project, "context": map[string]any{"kind": "self"}}},
		{"global_context_conflicts_with_project", map[string]any{"project": project, "context": map[string]any{"kind": "global"}}},
		{"explicit_empty_project_is_not_omission_for_self", map[string]any{"project": "", "context": map[string]any{"kind": "self"}}},
		{"explicit_empty_project_is_not_omission_for_global", map[string]any{"project": "", "context": map[string]any{"kind": "global"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := ownCardContextPayload("invalid selector must not create a memory or fake project")
			for key, value := range tc.fields {
				payload[key] = value
			}
			before := f.state(t)
			rec := f.call(t, http.MethodPost, "/api/memories", payload, card.caller())
			require.Contains(t, []int{http.StatusBadRequest, http.StatusNotFound}, rec.Code, "invalid selectors must fail before mutation")
			assert.Equal(t, before, f.state(t))
		})
	}
	created := f.create(t, card, ownCardContextPayload("self read must not accept invalid explicit project fallback"))
	for _, query := range []string{"project=", "project=%20", "project=invalid", "project=" + uuid.NewString(), "project=" + project + "&project=" + uuid.NewString()} {
		before := f.state(t)
		rec := f.call(t, http.MethodGet, "/api/memories/"+strconv.FormatInt(created.ID, 10)+"?"+query, nil, card.caller())
		ownCardContextDenied(t, rec, created, http.StatusBadRequest, http.StatusNotFound)
		assert.Equal(t, before, f.state(t))
	}
}

func (f *ownCardContextFixture) testLegacyProject(t *testing.T) {
	legacy := f.keycard(t, "", "")
	project := f.project(t)
	payload := ownCardContextPayload("legacy actual project save needs no new identity header or enrollment")
	payload["project"] = project
	caller := ownCardContextCaller{token: legacy.token}
	created := ownCardContextDecode(t, f.call(t, http.MethodPost, "/api/memories", payload, caller), http.StatusCreated)
	assert.Equal(t, project, created.Project)
	assert.Equal(t, payload["content"], created.Content)
	read := ownCardContextDecode(t, f.call(t, http.MethodGet, "/api/memories/"+strconv.FormatInt(created.ID, 10)+"?project="+project, nil, caller), http.StatusOK)
	assert.Equal(t, created.ID, read.ID)
	assert.Equal(t, created.Content, read.Content)
	assert.Equal(t, project, read.Project)
	assert.Equal(t, created.SourceAgent, read.SourceAgent)
}

func (f *ownCardContextFixture) testPersistedReplay(t *testing.T) {
	owner := f.registered(t, "persisted-retry")
	payload := ownCardContextPayload("uncertain save reconciles through persisted canonical request identity")
	payload["agent_visibility"], payload["request_key"] = models.AgentVisibilityPrivate, uuid.NewString()
	before := f.memoryCount(t)
	created := f.create(t, owner, payload)
	f.assertTyped(t, created, owner.identity, "self", "")
	f.reopen(t)
	replayed := ownCardContextDecode(t, f.call(t, http.MethodPost, "/api/memories", payload, owner.caller()), http.StatusOK)
	assert.Equal(t, created.ID, replayed.ID)
	assert.Equal(t, created.Content, replayed.Content)
	replacement := f.publicKeycard(t, owner.identity)
	require.Equal(t, http.StatusOK, f.call(t, http.MethodDelete, "/api/auth/tokens/"+owner.id, nil, f.admin).Code)
	f.reopen(t)
	replayed = ownCardContextDecode(t, f.call(t, http.MethodPost, "/api/memories", payload, replacement.caller()), http.StatusOK)
	assert.Equal(t, created.ID, replayed.ID, "request identity follows canonical principal, not the issuing card")
	assert.Equal(t, owner.id, replayed.SourceWorkstationID)
	f.assertTyped(t, replayed, owner.identity, "self", "")
	assert.Equal(t, before+1, f.memoryCount(t))
	read := ownCardContextDecode(t, f.call(t, http.MethodGet, "/api/memories/"+strconv.FormatInt(created.ID, 10), nil, replacement.caller()), http.StatusOK)
	assert.Equal(t, created.ID, read.ID)
	assert.Equal(t, created.Content, read.Content)
}

func (f *ownCardContextFixture) testReplayConflicts(t *testing.T) {
	owner := f.registered(t, "retry-conflict")
	payload := ownCardContextPayload("first committed request payload must remain authoritative")
	payload["request_key"] = uuid.NewString()
	created := f.create(t, owner, payload)
	for _, tc := range []struct {
		name  string
		key   string
		value any
	}{
		{"content", "content", "conflicting retry must not overwrite original content"},
		{"tags", "tags", []string{"type:fact", "changed-provenance"}},
		{"source", "source_agent", "different-client-supplied-source"},
		{"visibility", "agent_visibility", models.AgentVisibilityPrivate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := make(map[string]any, len(payload)+1)
			for key, value := range payload {
				changed[key] = value
			}
			changed[tc.key] = tc.value
			before := f.state(t)
			rec := f.call(t, http.MethodPost, "/api/memories", changed, owner.caller())
			require.Equal(t, http.StatusConflict, rec.Code, "same scoped key with changed payload must conflict")
			assert.Equal(t, before, f.state(t), "conflict must not update or duplicate the existing card")
		})
	}
	read := ownCardContextDecode(t, f.call(t, http.MethodGet, "/api/memories/"+strconv.FormatInt(created.ID, 10), nil, owner.caller()), http.StatusOK)
	assert.Equal(t, created.Content, read.Content)
	assert.Equal(t, created.Tags, read.Tags)
	assert.Equal(t, created.SourceAgent, read.SourceAgent)
	assert.Equal(t, created.AgentVisibility, read.AgentVisibility)
}

func (f *ownCardContextFixture) testConcurrentReplay(t *testing.T) {
	owner := f.registered(t, "concurrent-retry")
	payload := ownCardContextPayload("concurrent equivalent submissions must produce one canonical card")
	payload["request_key"] = uuid.NewString()
	const writers = 8
	requests := make([]*http.Request, writers)
	recorders := make([]*httptest.ResponseRecorder, writers)
	for i := range writers {
		requests[i] = ownCardContextRequest(t, http.MethodPost, "/api/memories", payload, owner.caller())
		recorders[i] = httptest.NewRecorder()
	}
	before := f.memoryCount(t)
	router := f.service.router
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			router.ServeHTTP(recorders[index], requests[index])
		}(i)
	}
	close(start)
	wg.Wait()
	var id int64
	for _, rec := range recorders {
		record := ownCardContextDecode(t, rec, http.StatusOK, http.StatusCreated)
		if id == 0 {
			id = record.ID
		}
		assert.Equal(t, id, record.ID, "all successful equivalents must identify the single committed card")
		assert.Equal(t, payload["content"], record.Content)
	}
	assert.Equal(t, before+1, f.memoryCount(t))
	f.reopen(t)
	replayed := ownCardContextDecode(t, f.call(t, http.MethodPost, "/api/memories", payload, owner.caller()), http.StatusOK, http.StatusCreated)
	assert.Equal(t, id, replayed.ID)
	assert.Equal(t, before+1, f.memoryCount(t))
}

func (f *ownCardContextFixture) testReplayScopes(t *testing.T) {
	owner := f.registered(t, "retry-context-owner")
	other := f.registered(t, "retry-context-other")
	otherKind := f.keycard(t, "human", owner.identity.Principal)
	f.enroll(t, otherKind.identity)
	project, secondProject := f.project(t), f.project(t)
	key := uuid.NewString()
	before := f.memoryCount(t)
	projectsBefore := f.state(t).projects
	seen := make(map[int64]bool)
	for _, tc := range []struct {
		name    string
		card    ownCardContextCredential
		kind    string
		project string
	}{
		{"self", owner, "self", ""},
		{"first_project", owner, "project", project},
		{"second_project", owner, "project", secondProject},
		{"explicit_global", owner, "global", ""},
		{"other_principal_self", other, "self", ""},
		{"same_name_different_principal_kind", otherKind, "self", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := ownCardContextPayload("same payload and request key in distinct canonical contexts")
			payload["request_key"], payload["context"] = key, map[string]any{"kind": tc.kind}
			if tc.project != "" {
				payload["project"] = tc.project
			}
			created := f.create(t, tc.card, payload)
			require.False(t, seen[created.ID], "different canonical principal/context scopes must not collide")
			seen[created.ID] = true
			f.assertTyped(t, created, tc.card.identity, tc.kind, tc.project)
			f.reopen(t)
			replayed := ownCardContextDecode(t, f.call(t, http.MethodPost, "/api/memories", payload, tc.card.caller()), http.StatusOK)
			assert.Equal(t, created.ID, replayed.ID)
			f.assertTyped(t, replayed, tc.card.identity, tc.kind, tc.project)
		})
	}
	assert.Len(t, seen, 6)
	assert.Equal(t, before+6, f.memoryCount(t))
	assert.Equal(t, projectsBefore, f.state(t).projects, "self/global and their retries must not synthesize projects")
}

func (f *ownCardContextFixture) testEnrollmentBeforeIssuance(t *testing.T) {
	identity := ownCardContextIdentity{PrincipalKind: "service", Principal: "service/own-card-before-key-" + uuid.NewString()}
	before := f.state(t)
	f.enroll(t, identity, http.StatusCreated)
	assert.Equal(t, before, f.state(t), "enrollment before issuance creates only the canonical registry entry")
	card := f.publicKeycard(t, identity)
	created := f.create(t, card, ownCardContextPayload("a deliberately enrolled pair can receive its first keycard later"))
	f.assertTyped(t, created, identity, "self", "")
}

func (f *ownCardContextFixture) testRetiredReplay(t *testing.T) {
	card := f.registered(t, "retired-retry")
	payload := ownCardContextPayload("retirement cannot turn an old request key into another body")
	key := "retry / заметка " + uuid.NewString()
	payload["request_key"] = key
	created := f.create(t, card, payload)
	replayed := ownCardContextDecode(t, f.call(t, http.MethodPost, "/api/memories", payload, card.caller()), http.StatusOK)
	require.Equal(t, created.ID, replayed.ID, "request keys are not limited to UUID syntax")
	require.NoError(t, f.store.DB.Table("memories").Where("id = ?", created.ID).Update("status", "archived").Error)
	before := f.state(t)
	require.Equal(t, http.StatusConflict, f.call(t, http.MethodPost, "/api/memories", payload, card.caller()).Code)
	assert.Equal(t, before, f.state(t))
	require.NoError(t, f.store.DB.Exec("DELETE FROM memories WHERE id = ?", created.ID).Error)
	before = f.state(t)
	require.Equal(t, http.StatusConflict, f.call(t, http.MethodPost, "/api/memories", payload, card.caller()).Code)
	assert.Equal(t, before, f.state(t), "a retained retry tombstone must not recreate a deleted body")
	var receipts int64
	require.NoError(t, f.store.DB.Table("memory_request_keys").Where("principal_kind = ? AND principal = ? AND request_key = ? AND memory_id IS NULL", card.identity.PrincipalKind, card.identity.Principal, key).Count(&receipts).Error)
	assert.Equal(t, int64(1), receipts)
}

func (f *ownCardContextFixture) testFailedLedgerInsert(t *testing.T) {
	card := f.registered(t, "atomic-retry")
	payload := ownCardContextPayload("a failed receipt insert must roll back the preceding body insert")
	key := "atomic retry " + uuid.NewString()
	payload["request_key"] = key
	name := "own_card_fail_ledger_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	cleanup := func() {
		assert.NoError(t, f.store.DB.Exec("DROP TRIGGER IF EXISTS "+name+" ON memory_request_keys").Error)
		assert.NoError(t, f.store.DB.Exec("DROP FUNCTION IF EXISTS "+name+"()").Error)
	}
	t.Cleanup(cleanup)
	require.NoError(t, f.store.DB.Exec("CREATE FUNCTION "+name+"() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic ledger insertion failure'; END; $$").Error)
	require.NoError(t, f.store.DB.Exec("CREATE TRIGGER "+name+" BEFORE INSERT ON memory_request_keys FOR EACH ROW EXECUTE FUNCTION "+name+"()").Error)
	before := f.state(t)
	var receiptsBefore int64
	require.NoError(t, f.store.DB.Table("memory_request_keys").Count(&receiptsBefore).Error)
	require.Equal(t, http.StatusInternalServerError, f.call(t, http.MethodPost, "/api/memories", payload, card.caller()).Code)
	assert.Equal(t, before, f.state(t), "body insertion and retry receipt must fail atomically")
	var receiptsAfter int64
	require.NoError(t, f.store.DB.Table("memory_request_keys").Count(&receiptsAfter).Error)
	assert.Equal(t, receiptsBefore, receiptsAfter)
	cleanup()
	created := f.create(t, card, payload)
	replayed := ownCardContextDecode(t, f.call(t, http.MethodPost, "/api/memories", payload, card.caller()), http.StatusOK, http.StatusCreated)
	assert.Equal(t, created.ID, replayed.ID)
	require.NoError(t, f.store.DB.Table("memory_request_keys").Where("principal_kind = ? AND principal = ? AND request_key = ?", card.identity.PrincipalKind, card.identity.Principal, key).Count(&receiptsAfter).Error)
	assert.Equal(t, int64(1), receiptsAfter)
}

func (f *ownCardContextFixture) testContextSnapshotRestore(t *testing.T) {
	card := f.registered(t, "context-snapshot")
	created := f.create(t, card, ownCardContextPayload("snapshot body retains its self context and original authenticated writer"))
	snapshot, err := f.service.memoryStore.GetForSnapshot(t.Context(), created.ID)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Context)
	require.NotNil(t, snapshot.Author)
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	var captured models.Memory
	require.NoError(t, json.Unmarshal(encoded, &captured))
	require.Equal(t, snapshot.Context, captured.Context)
	require.Equal(t, snapshot.Author, captured.Author)
	require.NoError(t, f.store.DB.Table("memories").Where("id = ?", created.ID).Update("status", "superseded").Error)
	require.NoError(t, f.service.memoryStore.RestoreRaw(t.Context(), &captured))
	read := ownCardContextDecode(t, f.call(t, http.MethodGet, "/api/memories/"+strconv.FormatInt(created.ID, 10), nil, card.caller()), http.StatusOK)
	assert.Equal(t, created.Content, read.Content)
	assert.Equal(t, captured.Status, read.Status)
	f.assertTyped(t, read, card.identity, "self", "")
	downgraded := captured
	downgraded.Context, downgraded.Author = nil, nil
	before := f.state(t)
	require.Error(t, f.service.memoryStore.RestoreRaw(t.Context(), &downgraded), "a legacy snapshot cannot erase typed owning-context authority")
	assert.Equal(t, before, f.state(t))
}

func (f *ownCardContextFixture) testConcurrentRevisionHTTP(t *testing.T) {
	owner := f.registered(t, "concurrent-revision")
	created := f.create(t, owner, ownCardContextPayload("concurrent revision original"))
	server := httptest.NewServer(f.service.router)
	t.Cleanup(server.Close)
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	requests := make([]*http.Request, 2)
	for i, body := range []string{"concurrent revision one", "concurrent revision two"} {
		encoded, err := json.Marshal(map[string]any{"version": created.Version, "content": body})
		require.NoError(t, err)
		requests[i], err = http.NewRequest(http.MethodPatch, server.URL+"/api/memories/"+strconv.FormatInt(created.ID, 10), bytes.NewReader(encoded))
		require.NoError(t, err)
		requests[i].Header.Set("Content-Type", "application/json")
		requests[i].Header.Set("Authorization", "Bearer "+owner.token)
	}
	type outcome struct {
		status int
		err    error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for _, request := range requests {
		go func(req *http.Request) {
			<-start
			response, err := client.Do(req)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			response.Body.Close()
			results <- outcome{status: response.StatusCode}
		}(request)
	}
	close(start)
	statuses := make([]int, 0, 2)
	for range requests {
		result := <-results
		require.NoError(t, result.err)
		statuses = append(statuses, result.status)
	}
	require.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, statuses)
	path := "/api/memories/" + strconv.FormatInt(created.ID, 10)
	current := ownCardContextDecode(t, f.call(t, http.MethodGet, path, nil, owner.caller()), http.StatusOK)
	require.Equal(t, created.Version+1, current.Version)
	require.Contains(t, []string{"concurrent revision one", "concurrent revision two"}, current.Content)
	history := f.call(t, http.MethodGet, path+"/revisions", nil, owner.caller())
	require.Equal(t, http.StatusOK, history.Code)
	var result struct {
		Revisions []struct {
			Version int    `json:"version"`
			Content string `json:"content"`
		} `json:"revisions"`
	}
	require.NoError(t, json.Unmarshal(history.Body.Bytes(), &result))
	require.Len(t, result.Revisions, 1)
	require.Equal(t, created.Version, result.Revisions[0].Version)
	require.Equal(t, created.Content, result.Revisions[0].Content)
}

func (f *ownCardContextFixture) testRevisionHistoryFailure(t *testing.T) {
	owner := f.registered(t, "revision-rollback")
	created := f.create(t, owner, ownCardContextPayload("revision rollback original"))
	name := "revision_reject_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	cleanup := func() {
		assert.NoError(t, f.store.DB.Exec("DROP TRIGGER IF EXISTS "+name+" ON memory_revisions").Error)
		assert.NoError(t, f.store.DB.Exec("DROP FUNCTION IF EXISTS "+name+"()").Error)
	}
	t.Cleanup(cleanup)
	require.NoError(t, f.store.DB.Exec("CREATE FUNCTION "+name+"() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic revision failure'; END; $$").Error)
	require.NoError(t, f.store.DB.Exec("CREATE TRIGGER "+name+" BEFORE INSERT ON memory_revisions FOR EACH ROW EXECUTE FUNCTION "+name+"()").Error)
	path := "/api/memories/" + strconv.FormatInt(created.ID, 10)
	failed := f.call(t, http.MethodPatch, path, map[string]any{"version": created.Version, "content": "must not commit"}, owner.caller())
	require.Equal(t, http.StatusInternalServerError, failed.Code)
	current := ownCardContextDecode(t, f.call(t, http.MethodGet, path, nil, owner.caller()), http.StatusOK)
	require.Equal(t, created.Content, current.Content)
	require.Equal(t, created.Version, current.Version)
	var count int64
	require.NoError(t, f.store.DB.Table("memory_revisions").Where("memory_id = ?", created.ID).Count(&count).Error)
	require.Zero(t, count)
}

func (f *ownCardContextFixture) testRevisionAuditFailure(t *testing.T) {
	owner := f.registered(t, "revision-audit-rollback")
	created := f.create(t, owner, ownCardContextPayload("audit rollback original"))
	name := "revision_audit_reject_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	t.Cleanup(func() {
		assert.NoError(t, f.store.DB.Exec("DROP TRIGGER IF EXISTS "+name+" ON audit_log").Error)
		assert.NoError(t, f.store.DB.Exec("DROP FUNCTION IF EXISTS "+name+"()").Error)
	})
	require.NoError(t, f.store.DB.Exec("CREATE FUNCTION "+name+"() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END; $$").Error)
	require.NoError(t, f.store.DB.Exec("CREATE TRIGGER "+name+" BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION "+name+"()").Error)
	path := "/api/memories/" + strconv.FormatInt(created.ID, 10)
	failed := f.call(t, http.MethodPatch, path, map[string]any{"version": created.Version, "content": "audit must rollback body"}, owner.caller())
	require.Equal(t, http.StatusInternalServerError, failed.Code)
	current := ownCardContextDecode(t, f.call(t, http.MethodGet, path, nil, owner.caller()), http.StatusOK)
	require.Equal(t, created.Content, current.Content)
	require.Equal(t, created.Version, current.Version)
	var count int64
	require.NoError(t, f.store.DB.Table("memory_revisions").Where("memory_id = ?", created.ID).Count(&count).Error)
	require.Zero(t, count, "audit failure must also roll back the history insert")
}
