package worker

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"golang.org/x/crypto/bcrypt"
	gormlib "gorm.io/gorm"
)

type issue513ActorCredential struct {
	bearer string
	cookie *http.Cookie
}

type issue513ActorDescription struct {
	Kind      string  `json:"kind"`
	Reference *string `json:"reference"`
}

type issue513ActorFixture struct {
	service       *Service
	master        issue513ActorCredential
	hmac          issue513ActorCredential
	browser       issue513ActorCredential
	browserAgain  issue513ActorCredential
	client        issue513ActorCredential
	otherClient   issue513ActorCredential
	readOnly      issue513ActorCredential
	browserUserID int64
	privateValues []string
	capturedLogs  bytes.Buffer
}

func newIssue513ActorFixture(t *testing.T) *issue513ActorFixture {
	t.Helper()
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	service, _ := newIssue513HTTPFixture(t)
	master := rand.Text()
	tokenAuth, err := NewTokenAuth(master)
	require.NoError(t, err)
	users := gormdb.NewUserStore(service.store.GetDB())
	sessions := gormdb.NewAuthSessionStore(service.store.GetDB())
	tokens := gormdb.NewTokenStore(service.store)
	tokenAuth.SetAuthStores(users, sessions)
	tokenAuth.SetValidator(auth.NewValidator(master, tokens))
	service.router = chi.NewRouter()
	service.tokenAuth = tokenAuth
	service.tokenStore = tokens
	service.authHandlers = NewAuthHandlers(users, nil, sessions, nil)
	service.config = &config.Config{}
	service.ready.Store(true)
	service.setupMiddleware()
	service.setupRoutes()
	fixture := &issue513ActorFixture{
		service: service,
		master:  issue513ActorCredential{bearer: master},
		hmac:    issue513ActorCredential{cookie: authBoundaryCookie(t, tokenAuth, time.Now().Add(time.Hour))},
	}
	user, err := users.CreateUser("synthetic-513-actor@example.test", "", gormdb.DashboardRoleAdmin)
	require.NoError(t, err)
	fixture.browserUserID = user.ID
	for _, credential := range []*issue513ActorCredential{&fixture.browser, &fixture.browserAgain} {
		session, err := sessions.CreateSession(user.ID, time.Hour, "synthetic actor contract", "192.0.2.51")
		require.NoError(t, err)
		credential.cookie = &http.Cookie{Name: authSessionCookieName, Value: session.ID}
	}
	for index, credential := range []*issue513ActorCredential{&fixture.client, &fixture.otherClient, &fixture.readOnly} {
		var entropy [16]byte
		_, err := rand.Read(entropy[:])
		require.NoError(t, err)
		raw := auth.TokenRawPrefix + hex.EncodeToString(entropy[:])
		hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
		require.NoError(t, err)
		scope := string(auth.RoleReadWrite)
		if index == 2 {
			scope = string(auth.RoleReadOnly)
		}
		_, err = tokens.Create(t.Context(), "synthetic-513-actor-"+uuid.NewString(), string(hash), raw[len(auth.TokenRawPrefix):len(auth.TokenRawPrefix)+auth.TokenPrefixLen], scope)
		require.NoError(t, err)
		credential.bearer = raw
		fixture.privateValues = append(fixture.privateValues, raw, string(hash))
	}
	fixture.privateValues = append(fixture.privateValues, master, fixture.hmac.cookie.Value, fixture.browser.cookie.Value, fixture.browserAgain.cookie.Value)
	previousLogger := log.Logger
	log.Logger = zerolog.New(&fixture.capturedLogs)
	t.Cleanup(func() {
		log.Logger = previousLogger
		for _, value := range fixture.privateValues {
			assert.False(t, strings.Contains(fixture.capturedLogs.String(), value), "operational logs must not contain synthetic credential/cookie/hash bytes")
		}
	})
	return fixture
}

func (fixture *issue513ActorFixture) call(t *testing.T, credential issue513ActorCredential, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://localhost:37777"+path, strings.NewReader(body))
	request.RemoteAddr = "192.0.2.51:43500"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:37777")
	if credential.bearer != "" {
		request.Header.Set("Authorization", "Bearer "+credential.bearer)
	}
	if credential.cookie != nil {
		request.AddCookie(credential.cookie)
	}
	response := httptest.NewRecorder()
	fixture.service.router.ServeHTTP(response, request)
	for _, value := range fixture.privateValues {
		assert.False(t, strings.Contains(response.Body.String(), value), "HTTP response must not contain synthetic credential/cookie/hash bytes")
	}
	for _, forbidden := range []string{`"token_hash"`, `"password_hash"`, `"cookie"`, `"receipt_payload"`, `"receipt_digest"`} {
		assert.NotContains(t, response.Body.String(), forbidden)
	}
	return response
}

func issue513ActorFromMe(t *testing.T, response *httptest.ResponseRecorder) (map[string]json.RawMessage, issue513ActorDescription) {
	t.Helper()
	require.Equal(t, http.StatusOK, response.Code)
	body := issue513JSON(t, response)
	var actor issue513ActorDescription
	if assert.NotEmpty(t, body["actor"], "authenticated auth-me must expose the trusted actor, not only the legacy role") {
		require.NoError(t, json.Unmarshal(body["actor"], &actor))
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(body["actor"], &fields))
		assert.Len(t, fields, 2, "actor projection contains only kind and non-secret reference")
		assert.Contains(t, fields, "kind")
		assert.Contains(t, fields, "reference")
	}
	return body, actor
}

func issue513ActorReference(reference *string) string {
	if reference == nil {
		return ""
	}
	return *reference
}

func issue513WithExpectedActor(t *testing.T, body string, expected json.RawMessage) string {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(body), &fields))
	fields["expected_actor"] = expected
	raw, err := json.Marshal(fields)
	require.NoError(t, err)
	return string(raw)
}

func issue513IssueDomainCounts(t *testing.T, service *Service) map[string]int64 {
	t.Helper()
	var tables []string
	require.NoError(t, service.store.GetDB().Raw(`SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' AND table_name LIKE 'issue%' ORDER BY table_name`).Scan(&tables).Error)
	require.Contains(t, tables, "issues")
	require.Contains(t, tables, "issue_comments")
	counts := make(map[string]int64, len(tables))
	for _, table := range tables {
		var count int64
		require.NoError(t, service.store.GetDB().Table(table).Count(&count).Error)
		counts[table] = count
	}
	return counts
}

// Observe the real IssueStore/GORM boundary without mocking receipts, changing
// results, or requiring a future receipt model/table to exist on the RED source.
func issue513ObserveDomainAccess(t *testing.T, service *Service, request func() *httptest.ResponseRecorder) (*httptest.ResponseRecorder, int64) {
	t.Helper()
	db := service.store.GetDB()
	name := "synthetic_issue513_actor_guard_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	domain := regexp.MustCompile(`(?i)\bissues\b|\bissue_[a-z0-9_]+\b`)
	var accesses atomic.Int64
	observe := func(tx *gormlib.DB) {
		if domain.MatchString(tx.Statement.Table) || domain.MatchString(tx.Statement.SQL.String()) {
			accesses.Add(1)
		}
	}
	registrations := []struct {
		register func(string, func(*gormlib.DB)) error
		remove   func(string) error
	}{
		{db.Callback().Query().After("gorm:query").Register, db.Callback().Query().Remove},
		{db.Callback().Row().After("gorm:row").Register, db.Callback().Row().Remove},
		{db.Callback().Raw().After("gorm:raw").Register, db.Callback().Raw().Remove},
		{db.Callback().Create().After("gorm:create").Register, db.Callback().Create().Remove},
		{db.Callback().Update().After("gorm:update").Register, db.Callback().Update().Remove},
		{db.Callback().Delete().After("gorm:delete").Register, db.Callback().Delete().Remove},
	}
	for _, registration := range registrations {
		require.NoError(t, registration.register(name, observe))
		defer func(remove func(string) error) { require.NoError(t, remove(name)) }(registration.remove)
	}
	response := request()
	return response, accesses.Load()
}

func issue513AssertActorChanged(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, http.StatusConflict, response.Code, "a changed/non-comparable actor must not create or replay a card")
	var result struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	assert.Equal(t, "ACTOR_CHANGED", result.Error.Code)
	for _, forbidden := range []string{`"id"`, `"actor"`, `"reference"`, `"payload"`, `"digest"`, `"metadata"`, "operator:master", "operator:session", "Synthetic guarded intent"} {
		assert.NotContains(t, response.Body.String(), forbidden, "actor-change error must not disclose identity or pending intent")
	}
}

func TestIssueHTTPActorIdentityAndGuard(t *testing.T) {
	fixture := newIssue513ActorFixture(t)

	t.Run("domain_observer_detects_real_issue_store_writes_and_reads", func(t *testing.T) {
		body := issue513CreateBody(issue513Source, "actor-observer-control", "Synthetic observer control", `{}`)
		created, writes := issue513ObserveDomainAccess(t, fixture.service, func() *httptest.ResponseRecorder {
			return fixture.call(t, fixture.master, http.MethodPost, "/api/issues", body)
		})
		id := issue513CreatedID(t, created)
		assert.Positive(t, writes, "zero-access negatives require a working real persistence observer")
		read, reads := issue513ObserveDomainAccess(t, fixture.service, func() *httptest.ResponseRecorder {
			return fixture.call(t, fixture.master, http.MethodGet, "/api/issues/"+strconv.FormatInt(id, 10), "")
		})
		require.Equal(t, http.StatusOK, read.Code)
		assert.Positive(t, reads, "the observer must see reads as well as writes")
	})

	t.Run("auth_me_distinguishes_master_and_hmac_with_identical_legacy_fields", func(t *testing.T) {
		masterBody, master := issue513ActorFromMe(t, fixture.call(t, fixture.master, http.MethodGet, "/api/auth/me", ""))
		hmacBody, hmac := issue513ActorFromMe(t, fixture.call(t, fixture.hmac, http.MethodGet, "/api/auth/me", ""))
		assert.Equal(t, "operator", master.Kind)
		assert.Equal(t, "operator", hmac.Kind)
		assert.Equal(t, "operator:master", issue513ActorReference(master.Reference))
		assert.Equal(t, "operator:session", issue513ActorReference(hmac.Reference))
		delete(masterBody, "actor")
		delete(hmacBody, "actor")
		assert.Equal(t, masterBody, hmacBody, "identical legacy admin fields cannot identify the receipt actor")
		assert.JSONEq(t, `true`, string(masterBody["authenticated"]))
		assert.JSONEq(t, `"admin"`, string(masterBody["role"]))
		assert.JSONEq(t, `false`, string(masterBody["auth_disabled"]))
	})

	t.Run("auth_me_uses_validated_persisted_browser_subject", func(t *testing.T) {
		body, first := issue513ActorFromMe(t, fixture.call(t, fixture.browser, http.MethodGet, "/api/auth/me", ""))
		_, again := issue513ActorFromMe(t, fixture.call(t, fixture.browserAgain, http.MethodGet, "/api/auth/me", ""))
		assert.Equal(t, "operator", first.Kind)
		assert.NotEmpty(t, issue513ActorReference(first.Reference))
		assert.NotEqual(t, "operator:session", issue513ActorReference(first.Reference), "persisted browser user is not the HMAC singleton")
		assert.NotEqual(t, "operator:master", issue513ActorReference(first.Reference))
		assert.Equal(t, first, again, "two validated sessions for one persisted user describe one actor")
		var user struct {
			ID    int64  `json:"id"`
			Email string `json:"email"`
			Role  string `json:"role"`
		}
		require.NoError(t, json.Unmarshal(body["user"], &user))
		assert.Equal(t, fixture.browserUserID, user.ID)
		assert.Equal(t, "synthetic-513-actor@example.test", user.Email)
		assert.Equal(t, gormdb.DashboardRoleAdmin, user.Role)
	})

	t.Run("auth_me_uses_verified_keycard_not_an_untrusted_agent_label", func(t *testing.T) {
		body, first := issue513ActorFromMe(t, fixture.call(t, fixture.client, http.MethodGet, "/api/auth/me?source_agent=operator:master", ""))
		_, again := issue513ActorFromMe(t, fixture.call(t, fixture.client, http.MethodGet, "/api/auth/me", ""))
		_, other := issue513ActorFromMe(t, fixture.call(t, fixture.otherClient, http.MethodGet, "/api/auth/me", ""))
		assert.Equal(t, "client", first.Kind)
		assert.NotEmpty(t, issue513ActorReference(first.Reference))
		assert.Equal(t, first, again)
		assert.NotEqual(t, issue513ActorReference(first.Reference), issue513ActorReference(other.Reference), "different verified keycards must not share a self reference")
		assert.JSONEq(t, `"read-write"`, string(body["role"]))
		assert.NotContains(t, body, "user")
	})

	t.Run("auth_disabled_is_a_distinct_honestly_synthetic_operator", func(t *testing.T) {
		t.Setenv("ENGRAM_AUTH_DISABLED", "true")
		tokenAuth, err := NewTokenAuth("")
		require.NoError(t, err)
		service := &Service{router: chi.NewRouter(), tokenAuth: tokenAuth, config: &config.Config{}}
		service.ready.Store(true)
		service.setupMiddleware()
		service.setupRoutes()
		response := httptest.NewRecorder()
		service.router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
		body, actor := issue513ActorFromMe(t, response)
		assert.Equal(t, "operator", actor.Kind)
		assert.Equal(t, "operator:auth-disabled", issue513ActorReference(actor.Reference))
		assert.JSONEq(t, `true`, string(body["authenticated"]))
		assert.JSONEq(t, `true`, string(body["auth_disabled"]))
		assert.JSONEq(t, `true`, string(body["synthetic"]))
		assert.JSONEq(t, `"auth-disabled"`, string(body["source"]))
		assert.JSONEq(t, `"admin"`, string(body["role"]))
	})

	// The golden self-description comes from the accepted operator:master
	// contract, not injected auth context. Keep POST assertions executable on
	// old production even when auth-me's independently asserted field is absent.
	masterExpectation := json.RawMessage(`{"kind":"operator","reference":"operator:master"}`)
	t.Run("actor_switch_between_auth_me_and_keyed_post_reaches_no_issue_or_receipt_access", func(t *testing.T) {
		me := fixture.call(t, fixture.master, http.MethodGet, "/api/auth/me", "")
		require.Equal(t, http.StatusOK, me.Code)
		assert.JSONEq(t, string(masterExpectation), string(issue513JSON(t, me)["actor"]))
		body := issue513WithExpectedActor(t, issue513CreateBody(issue513Source, "actor-switch-intent", "Synthetic guarded intent", `{}`), masterExpectation)
		before := issue513IssueDomainCounts(t, fixture.service)
		response, accesses := issue513ObserveDomainAccess(t, fixture.service, func() *httptest.ResponseRecorder {
			return fixture.call(t, fixture.hmac, http.MethodPost, "/api/issues", body)
		})
		issue513AssertActorChanged(t, response)
		assert.Zero(t, accesses, "actor precondition must run before receipt lookup/insert or issue-domain access")
		assert.Equal(t, before, issue513IssueDomainCounts(t, fixture.service))
	})

	t.Run("unchanged_actor_succeeds_retries_and_guard_does_not_change_logical_binding", func(t *testing.T) {
		me := fixture.call(t, fixture.master, http.MethodGet, "/api/auth/me", "")
		require.Equal(t, http.StatusOK, me.Code)
		assert.JSONEq(t, string(masterExpectation), string(issue513JSON(t, me)["actor"]))
		plain := issue513CreateBody(issue513Source, "stable-actor-intent", "Synthetic guarded intent", `{}`)
		guarded := issue513WithExpectedActor(t, plain, masterExpectation)
		before := issue513Count(t, fixture.service)
		id := issue513CreatedID(t, fixture.call(t, fixture.master, http.MethodPost, "/api/issues", guarded))
		for _, body := range []string{guarded, plain} {
			response := fixture.call(t, fixture.master, http.MethodPost, "/api/issues", body)
			var result struct {
				ID       int64 `json:"id"`
				Replayed bool  `json:"replayed"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			assert.Equal(t, http.StatusOK, response.Code)
			assert.Equal(t, id, result.ID)
			assert.True(t, result.Replayed)
		}
		assert.Equal(t, before+1, issue513Count(t, fixture.service))
	})

	for _, expected := range []json.RawMessage{
		json.RawMessage(`{"kind":"unknown","reference":null}`),
		json.RawMessage(`{"kind":"operator","reference":null}`),
		json.RawMessage(`{"kind":"unknown","reference":"operator:master"}`),
	} {
		t.Run("noncomparable_expectation_"+string(expected), func(t *testing.T) {
			body := issue513WithExpectedActor(t, issue513CreateBody(issue513Source, "noncomparable-"+string(expected), "Synthetic guarded intent", `{}`), expected)
			before := issue513IssueDomainCounts(t, fixture.service)
			response, accesses := issue513ObserveDomainAccess(t, fixture.service, func() *httptest.ResponseRecorder {
				return fixture.call(t, fixture.master, http.MethodPost, "/api/issues", body)
			})
			issue513AssertActorChanged(t, response)
			assert.Zero(t, accesses)
			assert.Equal(t, before, issue513IssueDomainCounts(t, fixture.service))
		})
	}

	t.Run("authentication_and_readonly_authorization_precede_actor_comparison", func(t *testing.T) {
		body := issue513WithExpectedActor(t, issue513CreateBody(issue513Source, "denied-actor-intent", "Synthetic guarded intent", `{}`), json.RawMessage(`{"kind":"system","reference":17}`))
		for _, test := range []struct {
			credential issue513ActorCredential
			status     int
		}{
			{credential: issue513ActorCredential{}, status: http.StatusUnauthorized},
			{credential: fixture.readOnly, status: http.StatusForbidden},
		} {
			before := issue513IssueDomainCounts(t, fixture.service)
			response, accesses := issue513ObserveDomainAccess(t, fixture.service, func() *httptest.ResponseRecorder {
				return fixture.call(t, test.credential, http.MethodPost, "/api/issues", body)
			})
			assert.Equal(t, test.status, response.Code)
			assert.NotContains(t, response.Body.String(), "ACTOR_CHANGED")
			assert.Zero(t, accesses)
			assert.Equal(t, before, issue513IssueDomainCounts(t, fixture.service))
		}
	})

	t.Run("active_canonical_source_admission_precedes_actor_comparison", func(t *testing.T) {
		require.NoError(t, fixture.service.store.GetDB().Exec(`UPDATE projects SET removed_at = ? WHERE id = ?`, time.Now(), issue513OtherSource).Error)
		defer func() {
			require.NoError(t, fixture.service.store.GetDB().Exec(`UPDATE projects SET removed_at = NULL WHERE id = ?`, issue513OtherSource).Error)
		}()
		body := issue513WithExpectedActor(t, issue513CreateBody(issue513OtherSource, "inactive-actor-intent", "Synthetic guarded intent", `{}`), masterExpectation)
		before := issue513IssueDomainCounts(t, fixture.service)
		response, accesses := issue513ObserveDomainAccess(t, fixture.service, func() *httptest.ResponseRecorder {
			return fixture.call(t, fixture.hmac, http.MethodPost, "/api/issues", body)
		})
		assert.Contains(t, []int{http.StatusNotFound, http.StatusForbidden}, response.Code)
		assert.NotContains(t, response.Body.String(), "ACTOR_CHANGED")
		assert.Zero(t, accesses)
		assert.Equal(t, before, issue513IssueDomainCounts(t, fixture.service))
	})

	for _, expected := range []json.RawMessage{
		json.RawMessage(`{"kind":"system","reference":"system"}`),
		json.RawMessage(`{"kind":"operator","reference":17}`),
	} {
		t.Run("malformed_authenticated_subset_"+string(expected), func(t *testing.T) {
			body := issue513WithExpectedActor(t, issue513CreateBody(issue513Source, "malformed-actor-"+string(expected), "Synthetic guarded intent", `{}`), expected)
			before := issue513IssueDomainCounts(t, fixture.service)
			response, accesses := issue513ObserveDomainAccess(t, fixture.service, func() *httptest.ResponseRecorder {
				return fixture.call(t, fixture.master, http.MethodPost, "/api/issues", body)
			})
			assert.Equal(t, http.StatusBadRequest, response.Code)
			var result struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			assert.Equal(t, "INVALID_ARGUMENT", result.Error.Code)
			assert.Zero(t, accesses)
			assert.Equal(t, before, issue513IssueDomainCounts(t, fixture.service))
		})
	}

	t.Run("omitted_guard_keeps_keyed_retry_and_legacy_unkeyed_create", func(t *testing.T) {
		plain := issue513CreateBody(issue513Source, "unguarded-actor-intent", "Synthetic unguarded intent", `{}`)
		id := issue513CreatedID(t, fixture.call(t, fixture.master, http.MethodPost, "/api/issues", plain))
		retry := fixture.call(t, fixture.master, http.MethodPost, "/api/issues", plain)
		var result struct {
			ID int64 `json:"id"`
		}
		require.NoError(t, json.Unmarshal(retry.Body.Bytes(), &result))
		assert.Equal(t, http.StatusOK, retry.Code)
		assert.Equal(t, id, result.ID)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(plain), &fields))
		delete(fields, "request_key")
		raw, err := json.Marshal(fields)
		require.NoError(t, err)
		before := issue513Count(t, fixture.service)
		first := issue513CreatedID(t, fixture.call(t, fixture.master, http.MethodPost, "/api/issues", string(raw)))
		second := issue513CreatedID(t, fixture.call(t, fixture.master, http.MethodPost, "/api/issues", string(raw)))
		assert.NotEqual(t, first, second, "legacy unkeyed creation has no deduplication promise")
		assert.Equal(t, before+2, issue513Count(t, fixture.service))
	})
}
