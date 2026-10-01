package worker

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestAuthEnabledLoginReachesProtectedHomeWithLocalAdmin(t *testing.T) {
	dsn := os.Getenv("ENGRAM_LOGIN_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_LOGIN_TEST_DATABASE_DSN not set; isolated PostgreSQL fixture required")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", parsed.Hostname(), "only disposable loopback PostgreSQL is allowed")
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")

	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn})
	require.NoError(t, err)
	defer store.Close()
	users := gormdb.NewUserStore(store.DB)
	sessions := gormdb.NewAuthSessionStore(store.DB)
	email := fmt.Sprintf("local-login-smoke-%d@example.test", time.Now().UnixNano())
	const password = "disposable-fixture-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	user, err := users.CreateUser(email, string(hash), gormdb.DashboardRoleAdmin)
	require.NoError(t, err)
	defer func() {
		_ = store.DB.Where("user_id = ?", user.ID).Delete(&gormdb.AuthSession{}).Error
		_ = store.DB.Delete(user).Error
	}()

	restore := replaceStaticFSForTest(t, fstest.MapFS{
		"index.html":     &fstest.MapFile{Data: []byte(`<!doctype html><script type="module" src="/_nuxt/login.js"></script>`)},
		"_nuxt/login.js": &fstest.MapFile{Data: []byte("export default 'login'")},
	})
	defer restore()
	guard, err := NewTokenAuth("disposable-master-key")
	require.NoError(t, err)
	guard.SetAuthStores(users, sessions)
	svc := &Service{router: chi.NewRouter(), tokenAuth: guard, authHandlers: NewAuthHandlers(users, nil, sessions, nil)}
	svc.setupMiddleware()
	svc.ready.Store(true)
	svc.setupRoutes()
	server := httptest.NewServer(svc.router)
	defer server.Close()
	client := server.Client()

	for _, path := range []string{"/login", "/_nuxt/login.js"} {
		response, err := client.Get(server.URL + path)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode, path)
		if path == "/login" {
			require.Empty(t, response.Cookies(), "anonymous shell must not create a session")
		}
		_ = response.Body.Close()
	}
	for _, path := range []string{"/code", "/api/code/grants/choices", "/api/memories", "/api/admin/users"} {
		response, err := client.Get(server.URL + path)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, response.StatusCode, path)
		_ = response.Body.Close()
	}
	response, err := client.Post(server.URL+"/api/auth/user-login", "application/json", strings.NewReader(`{"email":"`+email+`","password":"`+password+`"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	cookies := response.Cookies()
	_ = response.Body.Close()
	require.NotEmpty(t, cookies)
	for _, path := range []string{"/", "/api/auth/me"} {
		request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		require.NoError(t, err)
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		response, err := client.Do(request)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode, path)
		_ = response.Body.Close()
	}
	var authCookie *http.Cookie
	for _, cookie := range cookies {
		if cookie.Name == authSessionCookieName {
			authCookie = cookie
		}
	}
	require.NotNil(t, authCookie)
	legacyLogin, err := client.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"token":"disposable-master-key"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, legacyLogin.StatusCode)
	legacyCookies := legacyLogin.Cookies()
	_ = legacyLogin.Body.Close()
	require.Len(t, legacyCookies, 1)
	require.Equal(t, sessionCookieName, legacyCookies[0].Name)

	logoutRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/auth/logout", nil)
	require.NoError(t, err)
	logoutRequest.Header.Set("Origin", server.URL)
	logoutRequest.AddCookie(authCookie)
	logoutRequest.AddCookie(legacyCookies[0])
	svc.authHandlers.sessions = nil
	failedLogout, err := client.Do(logoutRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, failedLogout.StatusCode)
	require.Empty(t, failedLogout.Cookies(), "failed revocation must not claim logout or clear cookies")
	_ = failedLogout.Body.Close()
	svc.authHandlers.sessions = sessions
	_, err = sessions.GetSession(authCookie.Value)
	require.NoError(t, err)

	logoutRequest, err = http.NewRequest(http.MethodPost, server.URL+"/api/auth/logout", nil)
	require.NoError(t, err)
	logoutRequest.Header.Set("Origin", server.URL)
	logoutRequest.AddCookie(authCookie)
	logoutRequest.AddCookie(legacyCookies[0])
	logout, err := client.Do(logoutRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, logout.StatusCode)
	cleared := make(map[string]*http.Cookie)
	for _, cookie := range logout.Cookies() {
		cleared[cookie.Name] = cookie
	}
	_ = logout.Body.Close()
	meRequest, err := http.NewRequest(http.MethodGet, server.URL+"/api/auth/me", nil)
	require.NoError(t, err)
	meRequest.AddCookie(authCookie)
	me, err := client.Do(meRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, me.StatusCode)
	_ = me.Body.Close()
	for _, name := range []string{sessionCookieName, authSessionCookieName} {
		require.Contains(t, cleared, name)
		require.Less(t, cleared[name].MaxAge, 0)
		require.Equal(t, "/", cleared[name].Path)
	}
	_, err = sessions.GetSession(authCookie.Value)
	require.ErrorIs(t, err, gormdb.ErrAuthSessionRevoked)
}

func TestLogoutRejectsCrossOriginBeforeRevokingSession(t *testing.T) {
	dsn := os.Getenv("ENGRAM_LOGIN_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_LOGIN_TEST_DATABASE_DSN not set; isolated PostgreSQL fixture required")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", parsed.Hostname(), "only disposable loopback PostgreSQL is allowed")
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn})
	require.NoError(t, err)
	defer store.Close()
	users := gormdb.NewUserStore(store.DB)
	sessions := gormdb.NewAuthSessionStore(store.DB)
	user, err := users.CreateUser(fmt.Sprintf("logout-origin-%d@example.test", time.Now().UnixNano()), "hash", gormdb.DashboardRoleAdmin)
	require.NoError(t, err)
	defer func() {
		_ = store.DB.Where("user_id = ?", user.ID).Delete(&gormdb.AuthSession{}).Error
		_ = store.DB.Delete(user).Error
	}()
	guard, err := NewTokenAuth("disposable-master-key")
	require.NoError(t, err)
	guard.SetAuthStores(users, sessions)
	svc := &Service{router: chi.NewRouter(), tokenAuth: guard, authHandlers: NewAuthHandlers(users, nil, sessions, nil)}
	svc.setupMiddleware()
	svc.ready.Store(true)
	svc.setupRoutes()
	guard.SetAuthentikConfig(false, false, []string{"192.0.2.5"})
	cases := []struct {
		name, target, origin, site, contentType, peer, forwardedHost, forwardedProto string
		want                                                                         int
	}{
		{name: "wrong same-site port", target: "http://localhost:37777", origin: "http://localhost:5173", want: http.StatusForbidden},
		{name: "cross-site browser", target: "http://localhost:37777", origin: "http://malicious.test", site: "cross-site", want: http.StatusForbidden},
		{name: "spoofed fetch metadata", target: "http://localhost:37777", origin: "http://localhost:37777", site: "same-site", want: http.StatusForbidden},
		{name: "bodyless without origin", target: "http://localhost:37777", want: http.StatusForbidden},
		{name: "cross-site metadata without origin", target: "http://localhost:37777", site: "cross-site", contentType: "application/json", want: http.StatusForbidden},
		{name: "same-site metadata without origin", target: "http://localhost:37777", site: "same-site", contentType: "application/json", want: http.StatusForbidden},
		{name: "same-origin metadata without origin", target: "http://localhost:37777", site: "same-origin", want: http.StatusOK},
		{name: "non-browser JSON without origin", target: "http://localhost:37777", contentType: "application/json", want: http.StatusOK},
		{name: "same-origin form", target: "http://localhost:37777", origin: "http://localhost:37777", want: http.StatusOK},
		{name: "same-origin fetch", target: "http://localhost:37777", origin: "http://localhost:37777", site: "same-origin", contentType: "application/json", want: http.StatusOK},
		{name: "direct HTTPS", target: "https://app.example.test", origin: "https://app.example.test:443", want: http.StatusOK},
		{name: "untrusted forwarded host", target: "http://internal:8080", origin: "https://app.example.test", forwardedHost: "app.example.test", forwardedProto: "https", want: http.StatusForbidden},
		{name: "trusted HTTPS proxy", target: "http://internal:8080", origin: "https://app.example.test", peer: "192.0.2.5:9000", forwardedHost: "app.example.test", forwardedProto: "https", want: http.StatusOK},
		{name: "trusted HTTP proxy", target: "http://internal:8080", origin: "http://app.example.test:8081", peer: "192.0.2.5:9000", forwardedHost: "app.example.test:8081", forwardedProto: "http", want: http.StatusOK},
		{name: "trusted proxy wrong port", target: "http://internal:8080", origin: "https://app.example.test:444", peer: "192.0.2.5:9000", forwardedHost: "app.example.test", forwardedProto: "https", want: http.StatusForbidden},
	}
	for _, route := range []string{"/api/auth/logout", "/api/auth/user-logout"} {
		for _, tc := range cases {
			t.Run(route+" "+tc.name, func(t *testing.T) {
				session, err := sessions.CreateSession(user.ID, time.Hour, "fixture", "127.0.0.1")
				require.NoError(t, err)
				request := httptest.NewRequest(http.MethodPost, tc.target+route, nil)
				if tc.origin != "" {
					request.Header.Set("Origin", tc.origin)
				}
				if tc.site != "" {
					request.Header.Set("Sec-Fetch-Site", tc.site)
				}
				if tc.contentType != "" {
					request.Header.Set("Content-Type", tc.contentType)
				}
				if tc.peer != "" {
					request.RemoteAddr = tc.peer
				}
				if tc.forwardedHost != "" {
					request.Header.Set("X-Forwarded-Host", tc.forwardedHost)
				}
				if tc.forwardedProto != "" {
					request.Header.Set("X-Forwarded-Proto", tc.forwardedProto)
				}
				request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: session.ID})
				request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "legacy-cookie"})
				response := httptest.NewRecorder()
				svc.router.ServeHTTP(response, request)
				require.Equal(t, tc.want, response.Code, response.Body.String())
				_, err = sessions.GetSession(session.ID)
				if tc.want == http.StatusForbidden {
					require.Empty(t, response.Result().Cookies(), "rejected logout must not clear cookies")
					require.NoError(t, err, "rejected logout must not revoke session")
				} else {
					require.ErrorIs(t, err, gormdb.ErrAuthSessionRevoked)
				}
			})
		}
	}
	for _, route := range []string{"/api/auth/logout", "/api/auth/user-logout"} {
		t.Run(route+" store unavailable", func(t *testing.T) {
			session, err := sessions.CreateSession(user.ID, time.Hour, "fixture", "127.0.0.1")
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "http://localhost:37777"+route, nil)
			request.Header.Set("Origin", "http://localhost:37777")
			request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: session.ID})
			svc.authHandlers.sessions = nil
			response := httptest.NewRecorder()
			svc.router.ServeHTTP(response, request)
			svc.authHandlers.sessions = sessions
			require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
			require.Empty(t, response.Result().Cookies())
			_, err = sessions.GetSession(session.ID)
			require.NoError(t, err)
		})
	}
	login := httptest.NewRequest(http.MethodPost, "http://localhost:37777/api/auth/login", strings.NewReader(`{"token":"disposable-master-key"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	svc.router.ServeHTTP(loginResponse, login)
	require.Equal(t, http.StatusOK, loginResponse.Code, loginResponse.Body.String())
	require.Len(t, loginResponse.Result().Cookies(), 1)
	legacyOnly := httptest.NewRequest(http.MethodPost, "http://localhost:37777/api/auth/logout", nil)
	legacyOnly.Header.Set("Content-Type", "application/json")
	legacyOnly.AddCookie(loginResponse.Result().Cookies()[0])
	legacyResponse := httptest.NewRecorder()
	svc.router.ServeHTTP(legacyResponse, legacyOnly)
	require.Equal(t, http.StatusOK, legacyResponse.Code, legacyResponse.Body.String())
	clearedLegacy := false
	for _, cookie := range legacyResponse.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.MaxAge < 0 {
			clearedLegacy = true
		}
	}
	require.True(t, clearedLegacy, "legacy HMAC-only logout must still clear its cookie")
}
