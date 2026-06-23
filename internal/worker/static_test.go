package worker

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
)

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
	if got := rec.Body.String(); !strings.Contains(got, "window.location.reload()") || !strings.Contains(got, "window.location.replace(url.toString())") || !strings.Contains(got, "engram_chunk_reload") || !strings.Contains(got, "export default {}") {
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

type readErrorFS struct {
	err error
}

func (fsys readErrorFS) Open(name string) (fs.File, error) {
	return nil, fsys.err
}

func (fsys readErrorFS) ReadFile(name string) ([]byte, error) {
	return nil, fsys.err
}
