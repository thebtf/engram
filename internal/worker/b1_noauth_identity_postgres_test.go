package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestB1AuthDisabledDoesNotInventSelfPostgres(t *testing.T) {
	f := newOwnCardContextFixture(t, true)
	before := f.state(t)
	response := f.call(t, http.MethodPost, "/api/memories", ownCardContextPayload("synthetic no-auth must not create self"), ownCardContextCaller{})
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, response.Code)
	require.Equal(t, before, f.state(t))
}

type b1UnavailableTokenStore struct{}

func (b1UnavailableTokenStore) FindByPrefix(context.Context, string) ([]gormdb.APIToken, error) {
	return nil, errors.New("synthetic token store unavailable")
}

func TestB1AuthDisabledVerifiedIdentityPostgres(t *testing.T) {
	f := newOwnCardContextFixture(t, false)
	identity := ownCardContextIdentity{PrincipalKind: "agent", Principal: "agent/noauth-regression-" + uuid.NewString()}
	f.enroll(t, identity, http.StatusCreated)
	card := f.publicKeycard(t, identity)
	baseline := f.create(t, card, ownCardContextPayload("synthetic authenticated baseline"))
	path := "/api/memories/" + strconv.FormatInt(baseline.ID, 10)
	revoked := f.publicKeycard(t, identity)
	require.NoError(t, f.tokens.Revoke(t.Context(), revoked.id))
	expired := f.publicKeycard(t, identity)
	require.NoError(t, f.store.DB.Table("api_tokens").Where("id = ?", expired.id).Update("expires_at", time.Now().UTC().Add(-time.Hour)).Error)
	readonly := f.publicKeycard(t, identity)
	require.NoError(t, f.store.DB.Table("api_tokens").Where("id = ?", readonly.id).Update("scope", "read-only").Error)
	hap := f.keycard(t, "service", auth.ProjectServicePrincipal(uuid.NewString()))

	password := "synthetic-login-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	email := "b1-login-" + uuid.NewString() + "@example.invalid"
	_, err = f.users.CreateUser(email, string(hash), gormdb.DashboardRoleAdmin)
	require.NoError(t, err)
	hmacLogin := f.call(t, http.MethodPost, "/api/auth/login", map[string]any{"token": f.master}, ownCardContextCaller{})
	require.Equal(t, http.StatusOK, hmacLogin.Code)
	var hmacCookie *http.Cookie
	for _, cookie := range hmacLogin.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			hmacCookie = cookie
		}
	}
	require.NotNil(t, hmacCookie)
	nonAdmin := f.operator
	revokedSession := f.browser(t, gormdb.DashboardRoleAdmin, time.Hour, true, false)
	expiredSession := f.browser(t, gormdb.DashboardRoleAdmin, -time.Hour, false, false)
	disabledAdmin := f.browser(t, gormdb.DashboardRoleAdmin, time.Hour, false, true)
	t.Setenv("ENGRAM_AUTH_DISABLED", "true")
	f.reopen(t) // Test fixture wires the validator explicitly; production wiring requires separate proof.
	login := f.call(t, http.MethodPost, "/api/auth/user-login", map[string]any{"email": email, "password": password}, ownCardContextCaller{})
	require.Equal(t, http.StatusOK, login.Code, "synthetic local admin login in auth-disabled mode")
	var adminCookie *http.Cookie
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == authSessionCookieName {
			adminCookie = cookie
		}
	}
	require.NotNil(t, adminCookie)

	t.Run("excluded_routes_keep_synthetic_identity", func(t *testing.T) {
		routes := []struct{ method, path string }{
			{http.MethodGet, "/api/memories/principal"},
			{http.MethodPost, "/api/memories/suppress"},
			{http.MethodGet, path + "/audit"},
			{http.MethodPatch, path + "/revisions"},
			{http.MethodDelete, path},
			{http.MethodGet, "/api/auth/tokens"},
			{http.MethodGet, "/api/auth/me"},
		}
		callers := []struct {
			name   string
			caller ownCardContextCaller
		}{
			{"valid_bearer", card.caller()},
			{"invalid_bearer", ownCardContextCaller{token: "invalid-synthetic-key"}},
			{"persisted_admin_cookie", ownCardContextCaller{cookie: adminCookie}},
		}
		for _, route := range routes {
			for _, caller := range callers {
				t.Run(route.method+" "+route.path+"/"+caller.name, func(t *testing.T) {
					called := false
					handler := f.service.tokenAuth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						called = true
						id, ok := auth.IdentityFrom(r.Context())
						require.True(t, ok)
						require.Equal(t, auth.SourceAuthDisabled, id.Source)
						require.Empty(t, id.KeycardID)
						require.Empty(t, id.Principal)
						_, browser := id.SessionBrowserSubject()
						require.False(t, browser)
						_, hasSession := authenticatedBrowserSessionID(r.Context())
						require.False(t, hasSession)
						require.False(t, id.IsSessionAdmin())
						w.WriteHeader(http.StatusNoContent)
					}))
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, ownCardContextRequest(t, route.method, route.path, nil, caller.caller))
					require.True(t, called)
					require.Equal(t, http.StatusNoContent, rec.Code)
				})
			}
		}
		rec := f.call(t, http.MethodGet, "/api/auth/tokens", nil, ownCardContextCaller{cookie: adminCookie})
		require.Equal(t, http.StatusForbidden, rec.Code)
	})
	t.Run("x_auth_token_precedence", func(t *testing.T) {
		req := ownCardContextRequest(t, http.MethodGet, path, nil, ownCardContextCaller{token: card.token, cookie: adminCookie, explicit: true})
		req.Header.Set("X-Auth-Token", "invalid-synthetic-key")
		rec := httptest.NewRecorder()
		f.service.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code)

		req = ownCardContextRequest(t, http.MethodGet, path, nil, ownCardContextCaller{token: "invalid-synthetic-key", cookie: adminCookie, explicit: true})
		req.Header.Set("X-Auth-Token", card.token)
		rec = httptest.NewRecorder()
		f.service.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		req = ownCardContextRequest(t, http.MethodPost, "/api/auth/principals", ownCardContextIdentity{PrincipalKind: "agent", Principal: "agent/denied-" + uuid.NewString()}, ownCardContextCaller{cookie: adminCookie})
		req.Header.Set("X-Auth-Token", "invalid-synthetic-key")
		rec = httptest.NewRecorder()
		f.service.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code)

		req = ownCardContextRequest(t, http.MethodPost, "/api/auth/tokens", map[string]any{"name": "synthetic-denied", "scope": "read-write"}, ownCardContextCaller{cookie: adminCookie})
		req.Header.Set("X-Auth-Token", "invalid-synthetic-key")
		rec = httptest.NewRecorder()
		f.service.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("valid_key_self_create_read_revision", func(t *testing.T) {
		created := f.create(t, card, ownCardContextPayload("synthetic disabled own card"))
		f.assertTyped(t, created, identity, "self", "")
		selfPath := "/api/memories/" + strconv.FormatInt(created.ID, 10)
		read := ownCardContextDecode(t, f.call(t, http.MethodGet, selfPath, nil, card.caller()), http.StatusOK)
		require.Equal(t, created.ID, read.ID)
		f.assertTyped(t, read, identity, "self", "")
		updated := ownCardContextDecode(t, f.call(t, http.MethodPatch, selfPath, map[string]any{"version": created.Version, "content": "synthetic revised"}, card.caller()), http.StatusOK)
		require.Equal(t, created.Version+1, updated.Version)
		require.Equal(t, http.StatusOK, f.call(t, http.MethodGet, selfPath+"/revisions", nil, card.caller()).Code)
	})
	t.Run("unkeyed_preserves_synthetic_behavior", func(t *testing.T) {
		before := f.state(t)
		for _, caller := range []ownCardContextCaller{{}, {explicit: true}} {
			require.Equal(t, http.StatusForbidden, f.call(t, http.MethodPost, "/api/memories", ownCardContextPayload("unkeyed synthetic"), caller).Code)
		}
		require.Equal(t, before, f.state(t))
		me := f.call(t, http.MethodGet, "/api/auth/me", nil, ownCardContextCaller{})
		require.Equal(t, http.StatusOK, me.Code)
		require.JSONEq(t, `{"authenticated":true,"role":"admin","auth_disabled":true,"source":"auth-disabled","synthetic":true}`, me.Body.String())
	})
	for _, tc := range []struct {
		name   string
		caller ownCardContextCaller
	}{{"invalid_key", ownCardContextCaller{token: "invalid-synthetic-key", explicit: true}}, {"revoked_key", revoked.caller()}, {"expired_key", expired.caller()}} {
		t.Run(tc.name, func(t *testing.T) {
			before := f.state(t)
			require.Equal(t, http.StatusUnauthorized, f.call(t, http.MethodPost, "/api/memories", ownCardContextPayload("rejected key"), tc.caller).Code)
			require.Equal(t, http.StatusUnauthorized, f.call(t, http.MethodGet, path, nil, tc.caller).Code)
			require.Equal(t, before, f.state(t))
		})
	}
	t.Run("token_store_unavailable_fails_closed", func(t *testing.T) {
		f.service.tokenAuth.SetValidator(auth.NewValidator("", b1UnavailableTokenStore{}))
		defer f.service.tokenAuth.SetValidator(auth.NewValidator(f.master, f.tokens))
		require.Equal(t, http.StatusInternalServerError, f.call(t, http.MethodPost, "/api/memories", ownCardContextPayload("unavailable store"), card.caller()).Code)
	})
	t.Run("validator_unavailable_fails_closed", func(t *testing.T) {
		f.service.tokenAuth.SetValidator(nil)
		defer f.service.tokenAuth.SetValidator(auth.NewValidator(f.master, f.tokens))
		require.Equal(t, http.StatusUnauthorized, f.call(t, http.MethodPost, "/api/memories", ownCardContextPayload("unavailable validator"), card.caller()).Code)
	})
	t.Run("readonly_restrictions", func(t *testing.T) {
		require.Equal(t, http.StatusForbidden, f.call(t, http.MethodPost, "/api/memories", ownCardContextPayload("read-only denied"), readonly.caller()).Code)
		require.Equal(t, http.StatusForbidden, f.call(t, http.MethodPatch, path, map[string]any{"version": baseline.Version, "content": "read-only denied"}, readonly.caller()).Code)
		require.Equal(t, http.StatusOK, f.call(t, http.MethodGet, path, nil, readonly.caller()).Code)
	})
	t.Run("hap_restrictions", func(t *testing.T) {
		require.Equal(t, http.StatusForbidden, f.call(t, http.MethodGet, path, nil, hap.caller()).Code)
	})
	t.Run("browser_admin_enrollment_and_issuance", func(t *testing.T) {
		f.admin = ownCardContextCaller{cookie: adminCookie}
		newIdentity := ownCardContextIdentity{PrincipalKind: "agent", Principal: "agent/noauth-new-" + uuid.NewString()}
		f.enroll(t, newIdentity, http.StatusCreated)
		newCard := f.publicKeycard(t, newIdentity)
		created := f.create(t, newCard, ownCardContextPayload("newly enrolled synthetic"))
		f.assertTyped(t, created, newIdentity, "self", "")
	})
	for _, tc := range []struct {
		name   string
		caller ownCardContextCaller
	}{
		{"absent", ownCardContextCaller{}},
		{"invalid_cookie", ownCardContextCaller{cookie: &http.Cookie{Name: authSessionCookieName, Value: "invalid-synthetic"}}},
		{"revoked_cookie", revokedSession},
		{"expired_cookie", expiredSession},
		{"disabled_admin", disabledAdmin},
		{"non_admin", nonAdmin},
		{"hmac", ownCardContextCaller{cookie: hmacCookie}},
		{"master", ownCardContextCaller{token: f.master}},
		{"keycard", card.caller()},
		{"invalid_bearer_with_admin_cookie", ownCardContextCaller{token: "invalid-synthetic-key", cookie: adminCookie}},
	} {
		t.Run("enrollment_denied_"+tc.name, func(t *testing.T) {
			candidate := ownCardContextIdentity{PrincipalKind: "agent", Principal: "agent/denied-" + uuid.NewString()}
			before := f.state(t)
			var principalsBefore, tokensBefore int64
			require.NoError(t, f.store.DB.Table("memory_principals").Count(&principalsBefore).Error)
			require.NoError(t, f.store.DB.Table("api_tokens").Count(&tokensBefore).Error)
			t.Cleanup(func() {
				var principalsAfter, tokensAfter int64
				require.NoError(t, f.store.DB.Table("memory_principals").Count(&principalsAfter).Error)
				require.NoError(t, f.store.DB.Table("api_tokens").Count(&tokensAfter).Error)
				require.Equal(t, principalsBefore, principalsAfter, "denied request must not enroll any principal")
				require.Equal(t, tokensBefore, tokensAfter, "denied request must not issue a keycard")
			})
			rec := f.call(t, http.MethodPost, "/api/auth/principals", candidate, tc.caller)
			if tc.name == "invalid_bearer_with_admin_cookie" {
				require.Equal(t, http.StatusUnauthorized, rec.Code)
			} else {
				require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code)
			}
			issuance := f.call(t, http.MethodPost, "/api/auth/tokens", map[string]any{"name": "synthetic-denied", "scope": "read-write", "principal_kind": candidate.PrincipalKind, "principal": candidate.Principal}, tc.caller)
			if tc.name == "invalid_bearer_with_admin_cookie" {
				require.Equal(t, http.StatusUnauthorized, issuance.Code)
			} else {
				require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, issuance.Code)
			}
			require.Equal(t, before, f.state(t))
		})
	}
}
