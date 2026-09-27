package worker

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestAuthEnabledPublicLoginShellAndAssetsKeepProtectedRoutesClosed(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	restoreStaticFS := replaceStaticFSForTest(t, fstest.MapFS{
		"index.html":            &fstest.MapFile{Data: []byte(`<!doctype html><script type="module" src="/_nuxt/entry.js"></script><link rel="stylesheet" href="/_nuxt/entry.css">`)},
		"_nuxt/entry.js":        &fstest.MapFile{Data: []byte("export default 'login'")},
		"_nuxt/entry.css":       &fstest.MapFile{Data: []byte("body { color: black }")},
		"_fonts/entry.woff2":    &fstest.MapFile{Data: []byte("font")},
		"i18n/en.json":          &fstest.MapFile{Data: []byte(`{"login":"Sign in"}`)},
		"assets/login-icon.svg": &fstest.MapFile{Data: []byte("<svg/>")},
	})
	defer restoreStaticFS()
	guard, err := NewTokenAuth("test-admin-key")
	require.NoError(t, err)
	svc := &Service{router: chi.NewRouter(), tokenAuth: guard}
	svc.setupMiddleware()
	svc.setupRoutes()

	for _, tc := range []struct {
		path, body string
	}{
		{"/", "_nuxt/entry.js"},
		{"/login", "_nuxt/entry.js"},
		{"/_nuxt/entry.js", "export default 'login'"},
		{"/_nuxt/entry.css", "body { color: black }"},
		{"/_fonts/entry.woff2", "font"},
		{"/i18n/en.json", "Sign in"},
		{"/assets/login-icon.svg", "<svg/>"},
	} {
		rec := httptest.NewRecorder()
		svc.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		require.Equal(t, http.StatusOK, rec.Code, tc.path+": "+rec.Body.String())
		require.Contains(t, rec.Body.String(), tc.body, tc.path)
	}
	login := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/auth/user-login", strings.NewReader(`{"email":"user@example.test","password":"invalid"}`))
	request.Header.Set("Content-Type", "application/json")
	svc.router.ServeHTTP(login, request)
	require.NotEqual(t, http.StatusUnauthorized, login.Code, "public login request must reach its handler")

	for _, path := range []string{"/code", "/memory", "/admin", "/api/code/grants/choices", "/api/memories", "/api/admin/users", "/api/flags", "/api/auth/user-logout", "/api/login"} {
		rec := httptest.NewRecorder()
		svc.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusUnauthorized, rec.Code, path+": "+rec.Body.String())
		require.NotContains(t, rec.Body.String(), "_nuxt/entry.js", path)
	}
	rec := httptest.NewRecorder()
	svc.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_nuxt/../api/flags", nil))
	require.NotEqual(t, http.StatusOK, rec.Code, "asset traversal cannot serve API or shell")
}

func TestHealthNegotiatesBrowserShellWithoutChangingJSONProbes(t *testing.T) {
	restoreStaticFS := replaceStaticFSForTest(t, fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>operator health</title>")},
	})
	defer restoreStaticFS()

	svc := &Service{router: chi.NewRouter(), version: "test"}
	svc.setupRoutes()

	for _, tc := range []struct {
		name        string
		path        string
		accept      string
		contentType string
		body        string
	}{
		{name: "browser health page", path: "/health", accept: "text/html,application/xhtml+xml", contentType: "text/html; charset=utf-8", body: "operator health"},
		{name: "html refused by quality", path: "/health", accept: "text/html;q=0,application/json", contentType: "application/json", body: `"status"`},
		{name: "html accepted with quality", path: "/health", accept: "application/json;q=0.8,text/html;q=0.1", contentType: "text/html; charset=utf-8", body: "operator health"},
		{name: "lookalike is not html", path: "/health", accept: "application/text/html", contentType: "application/json", body: `"status"`},
		{name: "malformed is not html", path: "/health", accept: "text/html; q=wat", contentType: "application/json", body: `"status"`},
		{name: "no accept probe", path: "/health", contentType: "application/json", body: `"status"`},
		{name: "wildcard probe", path: "/health", accept: "*/*", contentType: "application/json", body: `"status"`},
		{name: "json probe", path: "/health", accept: "application/json", contentType: "application/json", body: `"status"`},
		{name: "api health is always json", path: "/api/health", accept: "text/html", contentType: "application/json", body: `"status"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			svc.router.ServeHTTP(rec, req)
			if got := rec.Header().Get("Content-Type"); got != tc.contentType {
				t.Fatalf("content type = %q, want %q", got, tc.contentType)
			}
			if !strings.Contains(rec.Body.String(), tc.body) {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.body)
			}
		})
	}
}

func TestHealthBrowserRouteUsesConfiguredOperatorConsoleProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><title>proxied health</title></html>"))
	}))
	defer upstream.Close()
	restoreProxy := replaceOperatorConsoleProxyForTest(t)
	defer restoreProxy()
	t.Setenv("ENGRAM_OPERATOR_CONSOLE_URL", upstream.URL)

	svc := &Service{router: chi.NewRouter(), version: "test"}
	svc.setupRoutes()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Accept", "text/html")
	svc.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "proxied health") {
		t.Fatalf("browser health proxy response = %d %q", rec.Code, rec.Body.String())
	}
}

func TestOperatorConsoleFontAssetsDoNotFallThroughToSPA(t *testing.T) {
	restoreStaticFS := replaceStaticFSForTest(t, fstest.MapFS{
		"index.html":                 &fstest.MapFile{Data: []byte("<!doctype html><title>operator</title>")},
		"_fonts/operator-font.woff2": &fstest.MapFile{Data: []byte("wOF2-test-font")},
	})
	defer restoreStaticFS()

	svc := &Service{router: chi.NewRouter()}
	svc.setupRoutes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_fonts/operator-font.woff2", nil)
	svc.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("font asset status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "font/woff2" {
		t.Fatalf("font asset content-type = %q, want font/woff2", got)
	}
	if got := rec.Body.String(); got != "wOF2-test-font" {
		t.Fatalf("font asset body = %q, want embedded font bytes", got)
	}
}

func TestOperatorConsoleMissingFontAssetReturns404NotSPAHTML(t *testing.T) {
	restoreStaticFS := replaceStaticFSForTest(t, fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>operator</title>")},
	})
	defer restoreStaticFS()

	svc := &Service{router: chi.NewRouter()}
	svc.setupRoutes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_fonts/missing.woff2", nil)
	svc.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing font status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got == "text/html; charset=utf-8" {
		t.Fatalf("missing font fell through to SPA HTML content-type")
	}
	if got := rec.Body.String(); got == "<!doctype html><title>operator</title>" {
		t.Fatalf("missing font fell through to SPA HTML body")
	}
}

func TestOperatorConsoleMissingNuxtJSChunkReturnsReloadModule(t *testing.T) {
	restoreStaticFS := replaceStaticFSForTest(t, fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>operator</title>")},
	})
	defer restoreStaticFS()

	svc := &Service{router: chi.NewRouter()}
	svc.setupRoutes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_nuxt/old-build-chunk.js", nil)
	svc.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("missing Nuxt JS chunk status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/javascript; charset=utf-8" {
		t.Fatalf("missing Nuxt JS chunk content-type = %q, want application/javascript; charset=utf-8", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache, no-store, must-revalidate" {
		t.Fatalf("missing Nuxt JS chunk cache-control = %q, want no-cache, no-store, must-revalidate", got)
	}
	if got := rec.Body.String(); strings.Contains(got, "window.location.reload()") || !strings.Contains(got, "window.location.replace(url.toString())") || !strings.Contains(got, "engram_chunk_reload") || !strings.Contains(got, "export default {}") {
		t.Fatalf("missing Nuxt JS chunk fallback does not look like a reload module: %q", got)
	}
}

func TestOperatorConsoleNuxtJSChunkReadErrorReturns500(t *testing.T) {
	restoreStaticFS := replaceStaticFSForTest(t, readErrorFS{err: errors.New("test read failure")})
	defer restoreStaticFS()

	svc := &Service{router: chi.NewRouter()}
	svc.setupRoutes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_nuxt/broken-build-chunk.js", nil)
	svc.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("broken Nuxt JS chunk status = %d, want 500; body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); strings.Contains(got, "engram_chunk_reload") || strings.Contains(got, "export default {}") {
		t.Fatalf("read error was masked as a stale chunk reload module: %q", got)
	}
}

func replaceStaticFSForTest(t *testing.T, next fs.FS) func() {
	t.Helper()

	prevFS := staticSubFS
	prevErr := staticInitErr
	staticSubFS = next
	staticInitErr = nil

	return func() {
		staticSubFS = prevFS
		staticInitErr = prevErr
	}
}

func replaceOperatorConsoleProxyForTest(t *testing.T) func() {
	t.Helper()
	prevProxy, prevTarget, prevErr := operatorConsoleProxy, operatorConsoleProxyTarget, operatorConsoleProxyErr
	operatorConsoleProxyOnce = sync.Once{}
	operatorConsoleProxy = nil
	operatorConsoleProxyTarget = ""
	operatorConsoleProxyErr = nil
	return func() {
		operatorConsoleProxyOnce = sync.Once{}
		operatorConsoleProxy = prevProxy
		operatorConsoleProxyTarget = prevTarget
		operatorConsoleProxyErr = prevErr
	}
}

type readErrorFS struct {
	err error
}

func (fsys readErrorFS) Open(name string) (fs.File, error) {
	return nil, fsys.err
}

func (fsys readErrorFS) ReadFile(name string) ([]byte, error) {
	return nil, fsys.err
}
