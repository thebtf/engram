package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestOutcomeCallbackRetirementRoutesArePreReadyAndLeaveStopIntakeGated(t *testing.T) {
	svc := &Service{router: chi.NewRouter()}
	svc.setupRoutes()

	want := map[string]string{
		"contract_version": "engram.outcome-retirement.v1",
		"code":             "OUTCOME_CALLBACK_RETIRED",
		"action":           "upgrade_outcome_adapter",
	}
	for _, path := range []string{
		"/api/sessions/claude-session/propagate-outcome",
		"/api/sessions/openclaw-session/outcome",
	} {
		rec := httptest.NewRecorder()
		svc.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusGone {
			t.Fatalf("POST %s status=%d, want %d; body=%s", path, rec.Code, http.StatusGone, rec.Body.String())
		}

		var got map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("POST %s returned invalid retirement JSON: %v; body=%s", path, err, rec.Body.String())
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("POST %s retirement diagnostic=%v, want %v", path, got, want)
		}
	}

	stop := httptest.NewRecorder()
	svc.router.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", nil))
	if stop.Code != http.StatusServiceUnavailable {
		t.Fatalf("active Stop intake status=%d, want readiness gate %d; body=%s", stop.Code, http.StatusServiceUnavailable, stop.Body.String())
	}
}
