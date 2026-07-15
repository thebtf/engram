package worker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
)

// TestEmbeddedHealthRouteCollidesWithMachineHandler proves the DOC-R3 ledger
// claim in docs/arch/current-surface.json: the embedded server's /health
// route resolves to the machine health handler (registered ahead of the SPA
// catch-all), not the promoted console's pages/health.vue, even though that
// page file exists in the Nuxt source. Do not delete this test without also
// updating the ledger's "Embedded /health direct-load collision" entry.
func TestEmbeddedHealthRouteCollidesWithMachineHandler(t *testing.T) {
	restore := replaceStaticFSForTest(t, fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>operator console shell</title>")},
	})
	defer restore()

	svc := &Service{router: chi.NewRouter(), version: "test"}
	svc.setupRoutes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	svc.router.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "operator console shell") {
		t.Fatalf("expected /health to hit the machine health handler, got SPA index: %q", body)
	}
	if !strings.Contains(body, `"status"`) {
		t.Fatalf("expected machine health JSON body, got: %q", body)
	}
}
