package grpcserver

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/projectidentity"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gormlib "gorm.io/gorm"
)

const grpcV3ProjectKey = "11111111-1111-4111-8111-111111111111"

func grpcV3Identity() *pb.ProjectIdentityV3 {
	return &pb.ProjectIdentityV3{
		Version:              3,
		AnchorProjectId:      "22222222-2222-4222-8222-222222222222",
		Name:                 "grpc-v3",
		Scope:                "repository",
		NormalizedGitRemotes: []string{"example.invalid/acme/engram"},
		LegacyIdentifiers: []*pb.ProjectLegacyIdentifierV3{{
			Scheme:     "binding_v2",
			Value:      "legacy-grpc-v3",
			Provenance: "transport",
		}},
		ClientInstanceId: "grpc-client-v3",
	}
}

func grpcV3Result(t *testing.T, intent projectidentity.ResolutionIntentV3, outcome projectidentity.ResolutionOutcomeV3) (projectidentity.ResolutionResultV3, error) {
	t.Helper()
	correlation, err := projectidentity.NewCorrelationV3("grpc-correlation-v3")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.IsSuccess() {
		key, err := projectidentity.NewProjectKeyV3(grpcV3ProjectKey)
		if err != nil {
			t.Fatal(err)
		}
		return projectidentity.NewSuccessResultV3(intent, outcome, key, projectidentity.RepositoryResolvedScopeV3, "", correlation)
	}
	result, err := projectidentity.NewRefusalResultV3(intent, outcome, correlation)
	if err != nil {
		t.Fatal(err)
	}
	resolutionErr, err := projectidentity.NewResolutionErrorV3(outcome, correlation)
	if err != nil {
		t.Fatal(err)
	}
	return result, resolutionErr
}

func TestCallTool_V3ResolvesBeforeHandlerWithoutV2Fallback(t *testing.T) {
	steps := []string{}
	var captured []byte
	srv := &Server{handler: identityOrderHandler{steps: &steps, arguments: &captured}}
	srv.identityResolver = func(_ context.Context, _ *gormlib.DB, _ string, _ *pb.ProjectIdentityV2) (string, error) {
		t.Fatal("V2 resolver must not run for a V3 descriptor")
		return "", nil
	}
	srv.identityResolverV3 = func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		steps = append(steps, "resolve-v3")
		if request.Intent != projectidentity.ResolveExistingIntentV3 || request.ResolveExistingAuthorization == "" || request.RegistrationAuthorization != "" || request.ReadFilter != nil || request.AdminTarget != nil {
			t.Fatalf("request=%#v", request)
		}
		want := projectidentity.DescriptorV3{
			Version:              3,
			AnchorProjectID:      "22222222-2222-4222-8222-222222222222",
			Name:                 "grpc-v3",
			Scope:                "repository",
			NormalizedGitRemotes: []string{"example.invalid/acme/engram"},
			LegacyIdentifiers: []projectidentity.LegacyIdentifierV3{{
				Scheme:     "binding_v2",
				Value:      "legacy-grpc-v3",
				Provenance: "transport",
			}},
			ClientInstanceID: "grpc-client-v3",
		}
		if !reflect.DeepEqual(request.Descriptor, want) {
			t.Fatalf("descriptor=%#v, want %#v", request.Descriptor, want)
		}
		return grpcV3Result(t, request.Intent, projectidentity.ProjectResolvedOutcomeV3)
	}

	response, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
		ToolName:          "recall",
		Project:           "raw-private-selector-must-not-be-used",
		ProjectIdentityV3: grpcV3Identity(),
		ArgumentsJson:     []byte(`{"project":"raw-private-selector-must-not-be-used"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(steps, []string{"resolve-v3", "handler"}) {
		t.Fatalf("steps=%v", steps)
	}
	if response.GetCanonicalProject() != grpcV3ProjectKey {
		t.Fatalf("canonical project=%q", response.GetCanonicalProject())
	}
	if strings.Contains(string(captured), "raw-private-selector-must-not-be-used") {
		t.Fatalf("handler received raw V3 project: %s", captured)
	}
	var delivered map[string]string
	if err := json.Unmarshal(captured, &delivered); err != nil || delivered["project"] != grpcV3ProjectKey {
		t.Fatalf("handler args=%s err=%v", captured, err)
	}
	if result := response.GetProjectResolutionV3(); result.GetOutcome() != pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED || result.GetProjectKey() != grpcV3ProjectKey || result.GetResolvedScope() != "repository" || result.GetCorrelation() == "" {
		t.Fatalf("resolution=%#v", result)
	}
}

func TestCallTool_V3ReadFiltersAuthorizeBeforeDispatch(t *testing.T) {
	const rawProject = "foreign-project-must-not-reach-handler"
	for _, test := range []struct {
		name      string
		toolName  string
		arguments string
		fields    []string
	}{
		{name: "issues target", toolName: "issues", arguments: `{"action":"list","project":"foreign-project-must-not-reach-handler"}`, fields: []string{"project"}},
		{name: "issues source", toolName: "issues", arguments: `{"action":"list","source_project":"foreign-project-must-not-reach-handler"}`, fields: []string{"source_project"}},
		{name: "review metrics", toolName: "review_metrics.read", arguments: `{"project":"foreign-project-must-not-reach-handler"}`, fields: []string{"project"}},
		{name: "review queue", toolName: "review_queue.read", arguments: `{"project":"foreign-project-must-not-reach-handler"}`, fields: []string{"project"}},
		{name: "governance health", toolName: "rule_governance_health", arguments: `{"project":"foreign-project-must-not-reach-handler"}`, fields: []string{"project"}},
		{name: "governance queue", toolName: "rule_governance_queue", arguments: `{"project":"foreign-project-must-not-reach-handler"}`, fields: []string{"project"}},
		{name: "governance snapshots", toolName: "rule_governance_snapshots", arguments: `{"project":"foreign-project-must-not-reach-handler"}`, fields: []string{"project"}},
		{name: "governance usefulness", toolName: "rule_governance_usefulness", arguments: `{"project":"foreign-project-must-not-reach-handler"}`, fields: []string{"project"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			steps := []string{}
			var captured []byte
			resolverCalls := 0
			srv := &Server{handler: identityOrderHandler{steps: &steps, arguments: &captured}}
			srv.identityResolverV3 = func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
				resolverCalls++
				if request.Intent != projectidentity.ReadFilterIntentV3 || request.ReadFilter == nil || request.ReadFilter.Authorization() == "" || request.ReadFilter.Correlation() != request.Correlation {
					t.Fatalf("request=%#v", request)
				}
				return grpcV3Result(t, request.Intent, projectidentity.ProjectResolvedOutcomeV3)
			}

			response, err := srv.CallTool(context.Background(), &pb.CallToolRequest{ToolName: test.toolName, ProjectIdentityV3: grpcV3Identity(), ArgumentsJson: []byte(test.arguments)})
			if err != nil {
				t.Fatal(err)
			}
			if response.CanonicalProject != grpcV3ProjectKey || resolverCalls != 1 || !reflect.DeepEqual(steps, []string{"handler"}) {
				t.Fatalf("response=%#v resolver_calls=%d steps=%v", response, resolverCalls, steps)
			}
			if strings.Contains(string(captured), rawProject) {
				t.Fatalf("handler received raw V3 filter: %s", captured)
			}
			var delivered map[string]json.RawMessage
			if err := json.Unmarshal(captured, &delivered); err != nil {
				t.Fatalf("decode handler args: %v", err)
			}
			for _, field := range test.fields {
				var project string
				if err := json.Unmarshal(delivered[field], &project); err != nil || project != grpcV3ProjectKey {
					t.Fatalf("field %q=%q err=%v, want %q", field, project, err, grpcV3ProjectKey)
				}
			}
		})
	}
}

func TestCallTool_V3PurgeRejectsRawTargetBeforeMCP(t *testing.T) {
	const rawTarget = `C:\private\v3-purge-target`
	steps := []string{}
	srv := &Server{handler: identityOrderHandler{steps: &steps}}
	srv.identityResolverV3 = func(_ context.Context, _ *gormlib.DB, _ projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		t.Fatal("V3 purge without a server-issued administrative target must not resolve")
		return projectidentity.ResolutionResultV3{}, nil
	}

	response, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
		ToolName:          "admin",
		ProjectIdentityV3: grpcV3Identity(),
		ArgumentsJson:     []byte(`{"action":"purge_project","project":"C:\\private\\v3-purge-target","confirm":"C:\\private\\v3-purge-target"}`),
	})
	if response != nil {
		t.Fatalf("response=%#v, want no dispatch response", response)
	}
	assertV3Refusal(t, err, projectidentity.ProjectDescriptorInvalidOutcomeV3, codes.InvalidArgument)
	if strings.Contains(err.Error(), rawTarget) || len(steps) != 0 {
		t.Fatalf("error=%v steps=%v", err, steps)
	}
}

func TestGetSessionStartContext_V3RefusalClosesRawProjectBypassBeforeQuery(t *testing.T) {
	calls := 0
	srv := &Server{}
	srv.identityResolverV3 = func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		calls++
		if request.Intent != projectidentity.ReadFilterIntentV3 || request.ReadFilter == nil || request.ReadFilter.Authorization() == "" || request.ReadFilter.Correlation() != request.Correlation {
			t.Fatalf("request=%#v", request)
		}
		return grpcV3Result(t, request.Intent, projectidentity.ProjectOnboardingRequiredOutcomeV3)
	}

	response, err := srv.GetSessionStartContext(context.Background(), &pb.GetSessionStartContextRequest{
		Project:           "raw-private-selector-must-not-be-queried",
		ProjectIdentityV3: grpcV3Identity(),
	})
	if response != nil {
		t.Fatalf("response=%#v, want nil on refusal", response)
	}
	assertV3Refusal(t, err, projectidentity.ProjectOnboardingRequiredOutcomeV3, codes.FailedPrecondition)
	if calls != 1 {
		t.Fatalf("resolver calls=%d, want 1", calls)
	}
}

func TestV3RefusalStatusMappingIsStableAndRedacted(t *testing.T) {
	for _, test := range []struct {
		outcome projectidentity.ResolutionOutcomeV3
		code    codes.Code
	}{
		{projectidentity.ProjectOnboardingRequiredOutcomeV3, codes.FailedPrecondition},
		{projectidentity.ProjectAnchorInvalidOutcomeV3, codes.InvalidArgument},
		{projectidentity.ProjectScopeMismatchOutcomeV3, codes.InvalidArgument},
		{projectidentity.ProjectNestedRepositoryUnresolvedOutcomeV3, codes.FailedPrecondition},
		{projectidentity.ProjectAnchorDecisionRequiredOutcomeV3, codes.FailedPrecondition},
		{projectidentity.ProjectIdentityAmbiguousOutcomeV3, codes.FailedPrecondition},
		{projectidentity.ProjectDescriptorUnsupportedOutcomeV3, codes.InvalidArgument},
		{projectidentity.ProjectDescriptorInvalidOutcomeV3, codes.InvalidArgument},
		{projectidentity.ProjectKeyClientAssertionForbiddenOutcomeV3, codes.InvalidArgument},
	} {
		t.Run(string(test.outcome), func(t *testing.T) {
			result, err := grpcV3Result(t, projectidentity.ResolveExistingIntentV3, test.outcome)
			assertV3Refusal(t, projectIdentityV3Error(result, err), test.outcome, test.code)
		})
	}
}

func TestProjectIdentityV3OutcomeProtoMapsEveryTypedOutcome(t *testing.T) {
	for _, outcome := range []projectidentity.ResolutionOutcomeV3{
		projectidentity.ProjectResolvedOutcomeV3,
		projectidentity.ProjectRedirectedOutcomeV3,
		projectidentity.ProjectOnboardingRequiredOutcomeV3,
		projectidentity.ProjectAnchorInvalidOutcomeV3,
		projectidentity.ProjectScopeMismatchOutcomeV3,
		projectidentity.ProjectNestedRepositoryUnresolvedOutcomeV3,
		projectidentity.ProjectAnchorDecisionRequiredOutcomeV3,
		projectidentity.ProjectIdentityAmbiguousOutcomeV3,
		projectidentity.ProjectDescriptorUnsupportedOutcomeV3,
		projectidentity.ProjectDescriptorInvalidOutcomeV3,
		projectidentity.ProjectKeyClientAssertionForbiddenOutcomeV3,
	} {
		if got := projectIdentityV3OutcomeProto(outcome); got == pb.ProjectResolutionOutcomeV3_PROJECT_RESOLUTION_OUTCOME_V3_UNSPECIFIED || got.String() != string(outcome) {
			t.Fatalf("outcome=%q mapped to %v", outcome, got)
		}
	}
}

func assertV3Refusal(t *testing.T, err error, outcome projectidentity.ResolutionOutcomeV3, wantCode codes.Code) {
	t.Helper()
	if status.Code(err) != wantCode {
		t.Fatalf("status=%v error=%v, want %v", status.Code(err), err, wantCode)
	}
	st, ok := status.FromError(err)
	if !ok || len(st.Details()) != 1 {
		t.Fatalf("status=%#v, want one ErrorInfo", st)
	}
	detail, ok := st.Details()[0].(*errdetails.ErrorInfo)
	if !ok || detail.Domain != "engram.project_identity.v3" || detail.Reason != string(outcome) || detail.Metadata["correlation"] == "" {
		t.Fatalf("detail=%#v", detail)
	}
	if detail.Metadata["project_key"] != "" || detail.Metadata["raw_project"] != "" || detail.Metadata["candidate"] != "" {
		t.Fatalf("refusal detail leaked authority: %#v", detail)
	}
}
