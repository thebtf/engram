package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
)

func TestServiceRouterPreservesRealIPAndRateLimit(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	guard, err := NewTokenAuth("test-token")
	require.NoError(t, err)
	svc := &Service{router: chi.NewRouter(), tokenAuth: guard, rateLimiter: NewPerClientRateLimiter(0, 1)}
	svc.setupMiddleware()
	svc.router.Get("/peer", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.RemoteAddr))
	})

	request := func(forwardedIP string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/peer", nil)
		req.RemoteAddr = "198.51.100.2:443"
		req.Header.Set("X-Real-IP", forwardedIP)
		req.Header.Set("X-Auth-Token", "test-token")
		rec := httptest.NewRecorder()
		svc.router.ServeHTTP(rec, req)
		return rec
	}

	first := request("203.0.113.1")
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "203.0.113.1", first.Body.String())
	limited := request("203.0.113.1")
	require.Equal(t, http.StatusTooManyRequests, limited.Code)
	require.Equal(t, "DENY", limited.Header().Get("X-Frame-Options"))
	otherClient := request("203.0.113.2")
	require.Equal(t, http.StatusOK, otherClient.Code)
	require.Equal(t, "203.0.113.2", otherClient.Body.String())
}

func TestServiceRouterAuthentikTrustsOnlyOriginalPeer(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	env := openAuthLifecycleEnv(t)
	email := fmt.Sprintf("zz-authentik-peer-%d@example.com", time.Now().UnixNano())
	user, err := env.users.CreateUser(email, "hash", gormdb.DashboardRoleOperator)
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.store.DB.Delete(user).Error })
	session, err := env.sessions.CreateSession(user.ID, time.Hour, "test-agent", "127.0.0.1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.store.DB.Where("id = ?", session.ID).Delete(&gormdb.AuthSession{}).Error })

	guard, err := NewTokenAuth("test-token")
	require.NoError(t, err)
	guard.SetAuthStores(env.users, env.sessions)
	guard.SetAuthentikConfig(true, false, []string{"192.0.2.1"})
	svc := &Service{router: chi.NewRouter(), tokenAuth: guard, authHandlers: env.handlers}
	svc.setupMiddleware()
	svc.ready.Store(true)
	svc.setupRoutes()

	for _, headers := range []map[string]string{
		{"True-Client-IP": "192.0.2.1"},
		{"X-Real-IP": "192.0.2.1"},
		{"X-Forwarded-For": "192.0.2.1"},
		{"True-Client-IP": "192.0.2.1", "X-Real-IP": "192.0.2.1", "X-Forwarded-For": "192.0.2.1"},
	} {
		for _, path := range []string{"/api/auth/me", "/api/code/grants/choices"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "198.51.100.2:443"
			req.Header.Set("X-Authentik-Email", email)
			for key, value := range headers {
				req.Header.Set(key, value)
			}
			rec := httptest.NewRecorder()
			svc.router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusUnauthorized, rec.Code, "%s, headers: %v, body: %s", path, headers, rec.Body.String())
			require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
		}
	}

	for _, tc := range []struct {
		name       string
		remoteAddr string
		cookie     bool
	}{
		{name: "trusted proxy", remoteAddr: "192.0.2.1:443"},
		{name: "local password cookie", remoteAddr: "198.51.100.2:443", cookie: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Authentik-Email", email)
			req.Header.Set("True-Client-IP", "203.0.113.9")
			if tc.cookie {
				req.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: session.ID})
			}
			rec := httptest.NewRecorder()
			svc.router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, true, body["authenticated"])
			require.Equal(t, map[string]any{"id": float64(user.ID), "email": email, "role": gormdb.DashboardRoleOperator}, body["user"])
		})
	}
}

func TestAuthMeUsesProtectedRouteIdentity(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	env := openAuthLifecycleEnv(t)
	email := fmt.Sprintf("zz-auth-me-%d@example.com", time.Now().UnixNano())
	user, err := env.users.CreateUser(email, "hash", gormdb.DashboardRoleOperator)
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.store.DB.Delete(user).Error })
	session, err := env.sessions.CreateSession(user.ID, time.Hour, "test-agent", "127.0.0.1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.store.DB.Where("id = ?", session.ID).Delete(&gormdb.AuthSession{}).Error })

	guard, err := NewTokenAuth("test-token")
	require.NoError(t, err)
	guard.SetAuthStores(env.users, env.sessions)
	guard.SetAuthentikConfig(true, false, []string{"192.0.2.1"})
	svc := &Service{tokenAuth: guard, authHandlers: env.handlers}
	handler := captureOriginalPeer(guard.Middleware(http.HandlerFunc(svc.handleAuthMe)))

	t.Run("direct middleware without captured peer fails closed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		req.RemoteAddr = "192.0.2.1:443"
		req.Header.Set("X-Authentik-Email", email)
		rec := httptest.NewRecorder()
		guard.Middleware(http.HandlerFunc(svc.handleAuthMe)).ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	})

	for _, tc := range []struct {
		name       string
		remoteAddr string
		email      string
		cookie     bool
		wantStatus int
	}{
		{name: "trusted Authentik without cookie", remoteAddr: "192.0.2.1:443", email: email, wantStatus: http.StatusOK},
		{name: "untrusted spoofed header", remoteAddr: "198.51.100.2:443", email: email, wantStatus: http.StatusUnauthorized},
		{name: "anonymous", wantStatus: http.StatusUnauthorized},
		{name: "local session cookie", cookie: true, wantStatus: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
			if tc.remoteAddr != "" {
				req.RemoteAddr = tc.remoteAddr
			}
			if tc.email != "" {
				req.Header.Set("X-Authentik-Email", tc.email)
			}
			if tc.cookie {
				req.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: session.ID})
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			if tc.wantStatus == http.StatusOK {
				require.Equal(t, true, body["authenticated"])
				require.Equal(t, gormdb.DashboardRoleOperator, body["role"])
				require.Equal(t, map[string]any{"id": float64(user.ID), "email": email, "role": gormdb.DashboardRoleOperator}, body["user"])
			} else {
				require.Equal(t, false, body["authenticated"])
			}
		})
	}

	t.Run("master bearer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, true, body["authenticated"])
		require.Equal(t, "admin", body["role"])
		require.NotContains(t, body, "user")
	})

	t.Run("auth disabled synthetic admin", func(t *testing.T) {
		t.Setenv("ENGRAM_AUTH_DISABLED", "true")
		disabledGuard, err := NewTokenAuth("")
		require.NoError(t, err)
		disabledSvc := &Service{tokenAuth: disabledGuard}
		rec := httptest.NewRecorder()
		disabledGuard.Middleware(http.HandlerFunc(disabledSvc.handleAuthMe)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, true, body["authenticated"])
		require.Equal(t, true, body["auth_disabled"])
		require.Equal(t, true, body["synthetic"])
		require.Equal(t, "admin", body["role"])
	})
}

func TestTokenAuth_AuthentikProvisioningRequiresInitialAdminSetup(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping Authentik provisioning integration test")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	var existing int64
	require.NoError(t, store.DB.Model(&gormdb.User{}).Count(&existing).Error)
	require.Zero(t, existing, "Authentik provisioning regression requires an empty test database")

	prefix := fmt.Sprintf("zz-middleware-authentik-%d", time.Now().UnixNano())
	adminEmail := prefix + "-admin@example.com"
	operatorEmail := prefix + "-operator@example.com"
	t.Cleanup(func() {
		_ = store.DB.Exec(`DELETE FROM audit_log WHERE action = 'auth_setup_completed' AND actor = ?`, adminEmail).Error
		_ = store.DB.Exec(`DELETE FROM users WHERE email IN (?, ?)`, adminEmail, operatorEmail).Error
	})

	tokenAuth, err := NewTokenAuth("test-token")
	require.NoError(t, err)
	tokenAuth.SetAuthStores(gormdb.NewUserStore(store.DB), gormdb.NewAuthSessionStore(store.DB))
	tokenAuth.SetAuthentikConfig(true, true, []string{"192.0.2.1"})

	var role string
	handler := captureOriginalPeer(tokenAuth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role = getAuthRole(r)
		w.WriteHeader(http.StatusNoContent)
	})))
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
		req.RemoteAddr = "192.0.2.1:443"
		req.Header.Set("X-Authentik-Email", operatorEmail)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	require.Equal(t, http.StatusUnauthorized, request().Code)
	var stranded int64
	require.NoError(t, store.DB.Model(&gormdb.User{}).Count(&stranded).Error)
	require.Zero(t, stranded, "pre-setup rejection must not persist an operator")

	_, err = gormdb.NewUserStore(store.DB).CreateInitialAdmin(t.Context(), adminEmail, "hash", gormdb.NewDomainOwnerStore(store))
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, request().Code)
	require.Equal(t, gormdb.DashboardRoleOperator, role)

	var operatorCount, auditCount int64
	require.NoError(t, store.DB.Model(&gormdb.User{}).Where("email = ?", operatorEmail).Count(&operatorCount).Error)
	require.Equal(t, int64(1), operatorCount)
	require.NoError(t, store.DB.Model(&gormdb.AuditLogEntry{}).Where("action = ? AND actor = ?", "auth_setup_completed", adminEmail).Count(&auditCount).Error)
	require.Equal(t, int64(1), auditCount)
}

func TestTokenAuth_AuthentikCodeExplorerUsesTrustedBrowserSession(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping Authentik Code Explorer integration test")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	email := fmt.Sprintf("zz-authentik-code-%d@example.com", time.Now().UnixNano())
	user, err := gormdb.NewUserStore(store.DB).CreateUser(email, "", gormdb.DashboardRoleOperator)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DB.Delete(&gormdb.User{}, user.ID).Error })

	adapter, fixture := newOperatorCodeHTTPTestAdapter(t)
	fixture.grants.current.SubjectUserID = user.ID
	fixture.binding.expectedUserID = user.ID
	fixture.binding.expectedSessionID = fmt.Sprintf("authentik/%d", user.ID)
	fixture.app.search = operatorCodeHTTPTestQueryResponse(t, fixture.ref, uci.QueryRetrievalLexical)

	tokenAuth, err := NewTokenAuth("test-token")
	require.NoError(t, err)
	tokenAuth.SetAuthStores(gormdb.NewUserStore(store.DB), gormdb.NewAuthSessionStore(store.DB))
	tokenAuth.SetAuthentikConfig(true, false, []string{"192.0.2.1"})
	handler := captureOriginalPeer(tokenAuth.Middleware(http.HandlerFunc(adapter.HandleSearch)))
	body := `{"tab_binding_id":"` + operatorCodeHTTPTestBindingID + `","document_proof":"proof-current","query":"Fixture"}`

	trusted := httptest.NewRequest(http.MethodPost, "/api/code/search", bytes.NewBufferString(body))
	trusted.RemoteAddr = "192.0.2.1:443"
	trusted.Header.Set("X-Authentik-Email", email)
	trusted.Header.Set("X-Engram-Request-ID", "authentik-code-request")
	trusted.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: "attacker-supplied-cookie"})
	trustedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(trustedRecorder, trusted)
	require.Equal(t, http.StatusOK, trustedRecorder.Code, trustedRecorder.Body.String())
	require.Equal(t, []string{fmt.Sprintf("authentik/%d", user.ID)}, fixture.app.sourceSessions)

	spoofed := httptest.NewRequest(http.MethodPost, "/api/code/search", bytes.NewBufferString(body))
	spoofed.RemoteAddr = "198.51.100.2:443"
	spoofed.Header.Set("X-Authentik-Email", email)
	spoofed.Header.Set("X-Engram-Request-ID", "spoofed-code-request")
	spoofedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(spoofedRecorder, spoofed)
	require.Equal(t, http.StatusUnauthorized, spoofedRecorder.Code, spoofedRecorder.Body.String())

	unauthenticated := httptest.NewRequest(http.MethodPost, "/api/code/search", bytes.NewBufferString(body))
	unauthenticated.Header.Set("X-Engram-Request-ID", "unauthenticated-code-request")
	unauthenticatedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedRecorder, unauthenticated)
	require.Equal(t, http.StatusUnauthorized, unauthenticatedRecorder.Code, unauthenticatedRecorder.Body.String())
	require.Equal(t, 1, fixture.app.searchCalls, "denied requests must not reach Code Explorer")
}
