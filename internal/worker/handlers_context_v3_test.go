package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

func TestSearchByPrompt_PreservesCanonicalProjectAndAgentAttribution(t *testing.T) {
	service := newInjectTestService(true)
	var retrievedProject, retrievedAgent string
	service.retrievalHooks.retrieveRelevant = func(ctx context.Context, project, _ string, _ RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
		retrievedProject = project
		state, ok := ctx.Value(retrievalContextKey{}).(retrievalContextState)
		if !ok {
			t.Fatal("retrieval context missing")
		}
		retrievedAgent = state.agentID
		return []*models.Observation{{ID: 1}}, map[int64]float64{1: 0.9}, nil
	}

	writer := httptest.NewRecorder()
	service.handleSearchByPrompt(writer, httptest.NewRequest(http.MethodGet, "/api/context/search?project=11111111-1111-4111-8111-111111111111&agent_id=legacy-agent&query=legacy-query", nil))

	if writer.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
	}
	if retrievedProject != "11111111-1111-4111-8111-111111111111" || retrievedAgent != "legacy-agent" {
		t.Fatalf("legacy retrieval scope project=%q agent=%q", retrievedProject, retrievedAgent)
	}
}

func TestSessionStart_ForwardsCanonicalProject(t *testing.T) {
	provider := &stubSessionStartContextServer{resp: &pb.GetSessionStartContextResponse{}}
	service := &Service{grpcInternalServer: provider}
	writer := httptest.NewRecorder()

	service.handleSessionStartContextStatic(writer, httptest.NewRequest(http.MethodGet, "/api/context/session-start?project=11111111-1111-4111-8111-111111111111", nil))

	if writer.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
	}
	if provider.req == nil || provider.req.GetProject() != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("session-start request=%#v", provider.req)
	}
}
