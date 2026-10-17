package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thebtf/engram/pkg/models"
)

func TestContextInject_PinnedUUIDDoesNotRegister(t *testing.T) {
	project := "11111111-1111-4111-8111-111111111111"
	service := newInjectTestService(true)
	service.retrievalHooks.searchObservationsFTSFiltered = func(_ context.Context, _ string, scope retrievalScope, _ int) ([]*models.Observation, error) {
		if scope.Project != project {
			t.Fatalf("retrieval project=%q", scope.Project)
		}
		return nil, nil
	}
	service.retrievalHooks.getLastPromptBySession = func(context.Context, string, string) (*models.UserPromptWithSession, error) {
		return nil, nil
	}
	service.retrievalHooks.retrieveRelevant = func(_ context.Context, selected, _ string, _ RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
		if selected != project {
			t.Fatalf("relevant project=%q", selected)
		}
		return nil, nil, nil
	}
	for range 2 {
		payload, err := json.Marshal(map[string]any{"project": project, "session_id": "uuid-session"})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		service.handleContextInject(rec, httptest.NewRequest(http.MethodPost, "/api/context/inject", bytes.NewReader(payload)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var response struct {
			Project string `json:"project"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Project != project {
			t.Fatalf("response=%+v error=%v", response, err)
		}
	}
}

func TestContextInject_RejectsRawSelectorBeforeProjectMutation(t *testing.T) {
	for _, project := range []string{"", "prc-http-no-store", "../project", "invalid selector"} {
		payload, err := json.Marshal(map[string]string{"project": project})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		(&Service{}).handleContextInject(rec, httptest.NewRequest(http.MethodPost, "/api/context/inject", bytes.NewReader(payload)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("project=%q status=%d body=%s", project, rec.Code, rec.Body.String())
		}
	}
}
