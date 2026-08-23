package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type contextV3ResolutionStore struct {
	binding  projectidentity.AnchorBindingV3
	lookups  int
	attempts []projectidentity.ResolutionAttemptV3
}

func (s *contextV3ResolutionStore) LookupAnchorBindingV3(_ context.Context, _ projectidentity.VerifiedAuthorizationV3, _ string) (projectidentity.AnchorBindingV3, error) {
	s.lookups++
	return s.binding, nil
}

func (*contextV3ResolutionStore) RegisterAnchorBindingAndRecordAttemptV3(context.Context, projectidentity.AnchorRegistrationV3, projectidentity.RegistrationAttemptBuilderV3) error {
	return errors.New("registration is not an HTTP context retrieval path")
}

func (*contextV3ResolutionStore) LookupAdministrativeTargetV3(context.Context, projectidentity.VerifiedAuthorizationV3) (projectidentity.AnchorBindingV3, error) {
	return projectidentity.AnchorBindingV3{}, errors.New("administrative lookup is not an HTTP context retrieval path")
}

func (s *contextV3ResolutionStore) RecordResolutionAttemptV3(_ context.Context, attempt projectidentity.ResolutionAttemptV3) error {
	s.attempts = append(s.attempts, attempt)
	return nil
}

type contextV3AuthorizationVerifier struct{}

func (contextV3AuthorizationVerifier) VerifyAuthorizationV3(context.Context, projectidentity.AuthorizationVerificationRequestV3) (projectidentity.AuthorizationVerificationV3, error) {
	return projectidentity.AuthorizationVerificationV3{Authorized: true}, nil
}

type contextV3ResolverSpy struct {
	resolver projectidentity.ResolverV3
	calls    int
	request  projectidentity.ResolveProjectRequestV3
}

func (s *contextV3ResolverSpy) ResolveProjectV3(ctx context.Context, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolveProjectResultV3, error) {
	s.calls++
	s.request = request
	return s.resolver.ResolveProjectV3(ctx, request)
}

func newContextV3ResolverSpy(binding projectidentity.AnchorBindingV3) (*contextV3ResolverSpy, *contextV3ResolutionStore) {
	store := &contextV3ResolutionStore{binding: binding}
	return &contextV3ResolverSpy{resolver: projectidentity.NewResolverV3(store, contextV3AuthorizationVerifier{})}, store
}

func validContextProjectDescriptorV3() map[string]any {
	return map[string]any{
		"version":                3,
		"anchor_project_id":      "11111111-1111-4111-8111-111111111111",
		"name":                   "engram",
		"scope":                  "repository",
		"normalized_git_remotes": []string{"github.com/thebtf/engram"},
		"legacy_identifiers":     []any{},
		"client_instance_id":     "fixture-http-client",
	}
}

func v3ContextInjectRequest(t *testing.T, descriptor map[string]any) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"project":            "attacker-selected-project",
		"agent_id":           "attacker-selected-agent",
		"session_id":         "session-v3",
		"project_descriptor": descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/context/inject", bytes.NewReader(body))
	return req.WithContext(auth.WithIdentity(req.Context(), auth.Client("read-only", "keycard-v3")))
}

func TestContextInjectV3_ResolvesBeforeRetrievalAndIgnoresRawSelectors(t *testing.T) {
	const projectKey = "22222222-2222-4222-8222-222222222222"
	resolver, store := newContextV3ResolverSpy(projectidentity.AnchorBindingV3{
		State:      projectidentity.AnchorBindingActiveV3,
		ProjectKey: projectKey,
		Scope:      "repository",
	})
	service := newInjectTestService(true)
	service.projectIdentityResolverV3 = resolver

	var fallbackScopes []retrievalScope
	var retrievedProject string
	service.retrievalHooks.searchObservationsFTSFiltered = func(_ context.Context, _ string, scope retrievalScope, _ int) ([]*models.Observation, error) {
		fallbackScopes = append(fallbackScopes, scope)
		return nil, nil
	}
	service.retrievalHooks.getLastPromptBySession = func(context.Context, string, string) (*models.UserPromptWithSession, error) {
		return nil, nil
	}
	service.retrievalHooks.retrieveRelevant = func(_ context.Context, project, _ string, _ RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
		retrievedProject = project
		return nil, nil, nil
	}

	writer := httptest.NewRecorder()
	service.handleContextInject(writer, v3ContextInjectRequest(t, validContextProjectDescriptorV3()))

	if writer.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
	}
	if resolver.calls != 1 || store.lookups != 1 {
		t.Fatalf("central resolver calls=%d lookups=%d", resolver.calls, store.lookups)
	}
	if resolver.request.Intent != projectidentity.ResolveExistingIntentV3 || resolver.request.Anchor.ProjectID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("unexpected resolver request: %#v", resolver.request)
	}
	if retrievedProject != projectKey {
		t.Fatalf("retrieval project=%q, want resolved key %q", retrievedProject, projectKey)
	}
	if len(fallbackScopes) != 2 {
		t.Fatalf("fallback calls=%d, want 2", len(fallbackScopes))
	}
	for _, scope := range fallbackScopes {
		if scope.Project != projectKey || scope.AgentID != "" {
			t.Fatalf("raw V3 scope bypassed resolver: %#v", scope)
		}
	}
	var response struct {
		ProjectResolution projectIdentityV3HTTPResolution `json:"project_resolution_v3"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ProjectResolution.ProjectKey != projectKey || response.ProjectResolution.Outcome != string(projectidentity.ProjectResolvedOutcomeV3) {
		t.Fatalf("resolution response=%+v", response.ProjectResolution)
	}
}

func TestContextInjectV3_RefusalStopsRetrievalAndRedactsInputs(t *testing.T) {
	resolver, store := newContextV3ResolverSpy(projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingMissingV3})
	service := newInjectTestService(true)
	service.projectIdentityResolverV3 = resolver
	service.retrievalHooks.searchObservationsFTSFiltered = func(context.Context, string, retrievalScope, int) ([]*models.Observation, error) {
		t.Fatal("retrieval ran after V3 refusal")
		return nil, nil
	}

	writer := httptest.NewRecorder()
	service.handleContextInject(writer, v3ContextInjectRequest(t, validContextProjectDescriptorV3()))

	if writer.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
	}
	if resolver.calls != 1 || store.lookups != 1 {
		t.Fatalf("resolver calls=%d lookups=%d", resolver.calls, store.lookups)
	}
	var response map[string]string
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response) != 3 || response["code"] != string(projectidentity.ProjectOnboardingRequiredOutcomeV3) || response["upgrade_action"] != "complete_project_onboarding" {
		t.Fatalf("refusal=%v", response)
	}
	for _, forbidden := range []string{"attacker-selected-project", "attacker-selected-agent", "22222222", "candidate"} {
		if strings.Contains(writer.Body.String(), forbidden) {
			t.Fatalf("V3 refusal leaked %q: %s", forbidden, writer.Body.String())
		}
	}
}

func TestContextInjectV3_RejectsClientProjectKeyBeforeResolver(t *testing.T) {
	resolver, _ := newContextV3ResolverSpy(projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingActiveV3})
	service := newInjectTestService(true)
	service.projectIdentityResolverV3 = resolver
	descriptor := validContextProjectDescriptorV3()
	descriptor["project_key"] = "22222222-2222-4222-8222-222222222222"

	writer := httptest.NewRecorder()
	service.handleContextInject(writer, v3ContextInjectRequest(t, descriptor))

	if writer.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
	}
	if resolver.calls != 0 {
		t.Fatalf("client project key reached resolver %d times", resolver.calls)
	}
	var response map[string]string
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response) != 3 || response["code"] != string(projectidentity.ProjectKeyClientAssertionForbiddenOutcomeV3) {
		t.Fatalf("refusal=%v", response)
	}
}

func TestContextInjectV3_MissingResolverReturnsStableUnavailableRefusal(t *testing.T) {
	service := newInjectTestService(true)
	writer := httptest.NewRecorder()
	service.handleContextInject(writer, v3ContextInjectRequest(t, validContextProjectDescriptorV3()))

	if writer.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
	}
	var response map[string]string
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response) != 3 || response["code"] != "PROJECT_RESOLUTION_UNAVAILABLE" || response["upgrade_action"] != "retry_project_resolution" {
		t.Fatalf("refusal=%v", response)
	}
}

type sessionStartV3Provider struct {
	calls    int
	ctx      context.Context
	request  *pb.GetSessionStartContextRequest
	response *pb.GetSessionStartContextResponse
	err      error
}

func (s *sessionStartV3Provider) GetSessionStartContext(ctx context.Context, request *pb.GetSessionStartContextRequest) (*pb.GetSessionStartContextResponse, error) {
	s.calls++
	s.ctx = ctx
	s.request = request
	if s.err != nil {
		return nil, s.err
	}
	return s.response, nil
}

func v3SessionStartRequest(t *testing.T, descriptor map[string]any) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"project":            "attacker-selected-project",
		"agent_id":           "attacker-selected-agent",
		"project_descriptor": descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/context/session-start", bytes.NewReader(body))
	return req.WithContext(auth.WithIdentity(req.Context(), auth.Client("read-only", "keycard-v3")))
}

func TestSessionStartV3_ForwardsDescriptorAndIgnoresRawSelectors(t *testing.T) {
	projectKey := "22222222-2222-4222-8222-222222222222"
	provider := &sessionStartV3Provider{response: &pb.GetSessionStartContextResponse{
		ProjectResolutionV3: &pb.ProjectResolutionResultV3{
			Outcome:    pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED,
			ProjectKey: &projectKey,
		},
	}}
	service := &Service{grpcInternalServer: provider}

	writer := httptest.NewRecorder()
	service.handleSessionStartContextStatic(writer, v3SessionStartRequest(t, validContextProjectDescriptorV3()))

	if writer.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
	}
	if provider.calls != 1 || provider.request == nil {
		t.Fatalf("session-start calls=%d request=%#v", provider.calls, provider.request)
	}
	if provider.request.GetProject() != "" {
		t.Fatalf("raw project bypassed V3 resolver: %q", provider.request.GetProject())
	}
	identity := provider.request.GetProjectIdentityV3()
	if identity == nil || identity.GetAnchorProjectId() != "11111111-1111-4111-8111-111111111111" || identity.GetClientInstanceId() != "fixture-http-client" {
		t.Fatalf("project identity=%#v", identity)
	}
	caller, ok := auth.IdentityFrom(provider.ctx)
	if !ok || caller.KeycardID != "keycard-v3" || caller.Role != "read-only" {
		t.Fatalf("authenticated identity was not forwarded: %#v", caller)
	}
}

func sessionStartV3Refusal(t *testing.T, outcome projectidentity.ResolutionOutcomeV3) error {
	t.Helper()
	status, err := grpcstatus.New(codes.FailedPrecondition, "raw project resolution diagnostic").WithDetails(&errdetails.ErrorInfo{
		Reason:   string(outcome),
		Domain:   "engram.project_identity.v3",
		Metadata: map[string]string{"correlation": "correlation-v3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return status.Err()
}

func TestSessionStartV3_RefusalsAreTypedAndRedacted(t *testing.T) {
	for _, test := range []struct {
		name       string
		descriptor map[string]any
		err        error
		status     int
		code       string
		calls      int
	}{
		{
			name: "client key assertion",
			descriptor: func() map[string]any {
				descriptor := validContextProjectDescriptorV3()
				descriptor["project_key"] = "22222222-2222-4222-8222-222222222222"
				return descriptor
			}(),
			status: http.StatusBadRequest,
			code:   string(projectidentity.ProjectKeyClientAssertionForbiddenOutcomeV3),
			calls:  0,
		},
		{
			name:       "resolver refusal",
			descriptor: validContextProjectDescriptorV3(),
			err:        sessionStartV3Refusal(t, projectidentity.ProjectOnboardingRequiredOutcomeV3),
			status:     http.StatusConflict,
			code:       string(projectidentity.ProjectOnboardingRequiredOutcomeV3),
			calls:      1,
		},
		{
			name:       "resolver unavailable",
			descriptor: validContextProjectDescriptorV3(),
			err:        grpcstatus.Error(codes.Unavailable, "raw session-start backend diagnostic"),
			status:     http.StatusServiceUnavailable,
			code:       "PROJECT_RESOLUTION_UNAVAILABLE",
			calls:      1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &sessionStartV3Provider{err: test.err}
			service := &Service{grpcInternalServer: provider}
			writer := httptest.NewRecorder()

			service.handleSessionStartContextStatic(writer, v3SessionStartRequest(t, test.descriptor))

			if writer.Code != test.status {
				t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
			}
			if provider.calls != test.calls {
				t.Fatalf("calls=%d, want %d", provider.calls, test.calls)
			}
			var response map[string]string
			if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response) != 3 || response["code"] != test.code {
				t.Fatalf("refusal=%v", response)
			}
			for _, forbidden := range []string{"attacker-selected-project", "attacker-selected-agent", "22222222-2222", "raw project resolution diagnostic", "raw session-start backend diagnostic"} {
				if strings.Contains(writer.Body.String(), forbidden) {
					t.Fatalf("V3 refusal leaked %q: %s", forbidden, writer.Body.String())
				}
			}
		})
	}
}

func TestSessionStartV3_AbsentRetainsProjectCompatibility(t *testing.T) {
	provider := &sessionStartV3Provider{response: &pb.GetSessionStartContextResponse{}}
	service := &Service{grpcInternalServer: provider}
	writer := httptest.NewRecorder()

	service.handleSessionStartContextStatic(writer, httptest.NewRequest(http.MethodGet, "/api/context/session-start?project=legacy-project", nil))

	if writer.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
	}
	if provider.request == nil || provider.request.GetProject() != "legacy-project" || provider.request.GetProjectIdentityV3() != nil {
		t.Fatalf("compatibility request=%#v", provider.request)
	}
}
