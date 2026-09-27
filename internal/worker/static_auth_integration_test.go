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
