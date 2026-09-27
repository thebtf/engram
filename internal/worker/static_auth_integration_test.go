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
}
