package grpcserver

import (
	"context"
	"testing"

	"github.com/thebtf/engram/internal/projectidentity"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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
	return grpcV3ResultWithCorrelation(t, intent, outcome, correlation)
}

func grpcV3ResultWithCorrelation(t *testing.T, intent projectidentity.ResolutionIntentV3, outcome projectidentity.ResolutionOutcomeV3, correlation projectidentity.CorrelationV3) (projectidentity.ResolutionResultV3, error) {
	t.Helper()
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

func bearerOutgoingContext(token string) context.Context {
	if token == "" {
		return context.Background()
	}
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
}
