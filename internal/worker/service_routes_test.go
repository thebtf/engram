package worker

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/internal/mcp"
)

func TestSetupRoutes_RegistersLearningHitRateRoute(t *testing.T) {
	svc := &Service{
		router:    chi.NewRouter(),
		mcpHealth: mcp.NewMCPHealth(),
	}
	svc.setupRoutes()
	svc.ready.Store(true)

	req := httptest.NewRequest(http.MethodGet, "/api/learning/hit-rate", nil)
	w := httptest.NewRecorder()
	svc.router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Fatalf("expected status 200 for GET /api/learning/hit-rate, got %d", got)
	}
}
