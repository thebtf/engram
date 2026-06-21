package worker

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
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
