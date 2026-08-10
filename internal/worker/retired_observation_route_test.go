package worker

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestRetiredObservationRouteIsAbsentWhileEventIngestRemainsWired(t *testing.T) {
	svc := &Service{router: chi.NewRouter()}
	svc.setupRoutes()

	retired := httptest.NewRecorder()
	svc.router.ServeHTTP(retired, httptest.NewRequest(http.MethodPost, "/api/sessions/observations", nil))
	if retired.Code != http.StatusMethodNotAllowed {
		t.Fatalf("retired observation POST status=%d, want %d", retired.Code, http.StatusMethodNotAllowed)
	}

	ingest := httptest.NewRecorder()
	svc.router.ServeHTTP(ingest, httptest.NewRequest(http.MethodPost, "/api/events/ingest", nil))
	if ingest.Code == http.StatusNotFound {
		t.Fatal("event ingest route is not registered")
	}
}
