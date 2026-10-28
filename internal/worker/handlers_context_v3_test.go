package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

func TestSearchByPrompt_PreservesCanonicalProjectAndAgentAttribution(t *testing.T) {
	service := newInjectTestService()
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

func TestSearchByPrompt_SourceSessionIsAuditOnly(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, session := range []string{"conversation-one", "conversation-two", ""} {
			t.Run(method+"/"+session, func(t *testing.T) {
				service := newInjectTestService()
				called := false
				service.retrievalHooks.retrieveRelevant = func(ctx context.Context, project, query string, opts RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
					called = true
					if got := auditcontext.SourceSession(ctx); got != session {
						t.Errorf("audit session=%q want=%q", got, session)
					}
					if project != "11111111-1111-4111-8111-111111111111" || query != "current-query" || opts.SessionID != "" {
						t.Errorf("session must not change project-scoped search: project=%q query=%q opts=%+v", project, query, opts)
					}
					return []*models.Observation{{ID: 1}}, map[int64]float64{1: 0.9}, nil
				}
				url := "/api/context/search?project=11111111-1111-4111-8111-111111111111&query=current-query&agent_id=one-agent"
				var body *strings.Reader
				if method == http.MethodPost {
					url += "&session_id=overridden-session"
					body = strings.NewReader(`{"session_id":"` + session + `"}`)
				} else {
					url += "&session_id=" + session
					body = strings.NewReader("")
				}
				writer := httptest.NewRecorder()
				service.handleSearchByPrompt(writer, httptest.NewRequest(method, url, body))
				if writer.Code != http.StatusOK || !called {
					t.Fatalf("status=%d called=%v body=%s", writer.Code, called, writer.Body.String())
				}
			})
		}
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
