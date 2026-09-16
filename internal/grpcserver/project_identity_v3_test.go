package grpcserver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/projectidentity"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
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

type grpcV3ReadFilterCase struct {
	name      string
	toolName  string
	arguments string
	fields    []string
}

func TestCallTool_V3ReadFiltersAuthorizeBeforeDispatch(t *testing.T) {
	for _, test := range []grpcV3ReadFilterCase{
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
			assertV3ReadFilterAuthorizedBeforeDispatch(t, test)
		})
	}
}

func assertV3ReadFilterAuthorizedBeforeDispatch(t *testing.T, test grpcV3ReadFilterCase) {
	t.Helper()
	steps := []string{}
	var captured []byte
	resolverCalls := 0
	response, err := grpcV3ReadFilterServer(t, &steps, &captured, &resolverCalls).CallTool(context.Background(), &pb.CallToolRequest{ToolName: test.toolName, ProjectIdentityV3: grpcV3Identity(), ArgumentsJson: []byte(test.arguments)})
	if err != nil {
		t.Fatal(err)
	}
	if response.CanonicalProject != grpcV3ProjectKey || resolverCalls != 1 || !reflect.DeepEqual(steps, []string{"handler"}) {
		t.Fatalf("response=%#v resolver_calls=%d steps=%v", response, resolverCalls, steps)
	}
	assertV3ReadFilterArguments(t, captured, test.fields)
}

func grpcV3ReadFilterServer(t *testing.T, steps *[]string, captured *[]byte, resolverCalls *int) *Server {
	srv := &Server{handler: identityOrderHandler{steps: steps, arguments: captured}}
	srv.identityResolverV3 = func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		(*resolverCalls)++
		if request.Intent != projectidentity.ReadFilterIntentV3 || request.ReadFilter == nil || request.ReadFilter.Authorization() == "" || request.ReadFilter.Correlation() != request.Correlation {
			t.Fatalf("request=%#v", request)
		}
		return grpcV3Result(t, request.Intent, projectidentity.ProjectResolvedOutcomeV3)
	}
	return srv
}

func assertV3ReadFilterArguments(t *testing.T, captured []byte, fields []string) {
	t.Helper()
	const rawProject = "foreign-project-must-not-reach-handler"
	if strings.Contains(string(captured), rawProject) {
		t.Fatalf("handler received raw V3 filter: %s", captured)
	}
	var delivered map[string]json.RawMessage
	if err := json.Unmarshal(captured, &delivered); err != nil {
		t.Fatalf("decode handler args: %v", err)
	}
	for _, field := range fields {
		var project string
		if err := json.Unmarshal(delivered[field], &project); err != nil || project != grpcV3ProjectKey {
			t.Fatalf("field %q=%q err=%v, want %q", field, project, err, grpcV3ProjectKey)
		}
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

type grpcRegistrationStoreV3 struct {
	binding           projectidentity.AnchorBindingV3
	registrationCalls int
	creates           int
	attempts          []projectidentity.ResolutionAttemptV3
}

func (store *grpcRegistrationStoreV3) LookupAnchorBindingV3(_ context.Context, _ projectidentity.VerifiedAuthorizationV3, _ string) (projectidentity.AnchorBindingV3, error) {
	return store.binding, nil
}

func (store *grpcRegistrationStoreV3) RegisterAnchorBindingAndRecordAttemptV3(_ context.Context, registration projectidentity.AnchorRegistrationV3, buildAttempt projectidentity.RegistrationAttemptBuilderV3) error {
	store.registrationCalls++
	previous := store.binding
	if store.binding.State == projectidentity.AnchorBindingMissingV3 {
		store.creates++
		store.binding = projectidentity.AnchorBindingV3{
			State:      projectidentity.AnchorBindingActiveV3,
			ProjectKey: grpcV3ProjectKey,
			Scope:      string(registration.Scope()),
		}
	}
	attempt, err := buildAttempt(store.binding)
	if err != nil {
		store.binding = previous
		store.creates--
		return err
	}
	store.attempts = append(store.attempts, attempt)
	return nil
}

func (store *grpcRegistrationStoreV3) LookupAdministrativeTargetV3(_ context.Context, _ projectidentity.VerifiedAuthorizationV3) (projectidentity.AnchorBindingV3, error) {
	return projectidentity.AnchorBindingV3{}, nil
}

func (store *grpcRegistrationStoreV3) RecordResolutionAttemptV3(_ context.Context, attempt projectidentity.ResolutionAttemptV3) error {
	store.attempts = append(store.attempts, attempt)
	return nil
}

func grpcRegistrationResolverV3(t *testing.T, store *grpcRegistrationStoreV3) func(context.Context, *gormlib.DB, projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
	t.Helper()
	return func(ctx context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		_, ok := auth.IdentityFrom(ctx)
		if !ok {
			t.Fatal("registration resolver received no authenticated identity")
		}
		resolver := projectidentity.NewResolverV3(store, grpcV3AuthorizationVerifier{authorization: request.RegistrationAuthorization})
		resolved, err := resolver.ResolveProjectV3(ctx, request)
		return resolved.Resolution(), err
	}
}

func newRegistrationV3Client(t *testing.T, validator *auth.Validator, resolver func(context.Context, *gormlib.DB, projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error)) (pb.EngramServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(bufSize)
	server, internal := New(nil, validator)
	internal.identityResolverV3 = resolver
	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.NewClient(
		"passthrough:///registration-v3",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial registration gRPC server: %v", err)
	}
	return pb.NewEngramServiceClient(conn), func() {
		_ = conn.Close()
		server.Stop()
		_ = listener.Close()
	}
}

func bearerOutgoingContext(token string) context.Context {
	if token == "" {
		return context.Background()
	}
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
}

func TestRegisterProjectIdentityV3ProtoIsAdditive(t *testing.T) {
	request := (&pb.RegisterProjectIdentityV3Request{}).ProtoReflect().Descriptor()
	if request.Fields().Len() != 2 {
		t.Fatalf("registration request fields=%d, want 2", request.Fields().Len())
	}
	field := request.Fields().ByName("project_identity_v3")
	if field == nil || field.Number() != 1 || field.Message().FullName() != "engram.v1.ProjectIdentityV3" {
		t.Fatalf("registration request descriptor=%v", request)
	}
	field = request.Fields().ByName("relay_revision")
	if field == nil || field.Number() != 2 || field.Kind().String() != "string" {
		t.Fatalf("registration relay revision descriptor=%v", request)
	}
	response := (&pb.RegisterProjectIdentityV3Response{}).ProtoReflect().Descriptor()
	if response.Fields().Len() != 1 {
		t.Fatalf("registration response fields=%d, want 1", response.Fields().Len())
	}
	field = response.Fields().ByName("project_resolution_v3")
	if field == nil || field.Number() != 1 || field.Message().FullName() != "engram.v1.ProjectResolutionResultV3" {
		t.Fatalf("registration response descriptor=%v", response)
	}
	method := request.ParentFile().Services().ByName("EngramService").Methods().ByName("RegisterProjectIdentityV3")
	if method == nil || method.Input().FullName() != request.FullName() || method.Output().FullName() != response.FullName() {
		t.Fatalf("registration method descriptor=%v", method)
	}
}

func TestRegisterProjectIdentityV3MasterRegistersOnce(t *testing.T) {
	store := &grpcRegistrationStoreV3{binding: projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingMissingV3}}
	client, stop := newRegistrationV3Client(t, auth.NewValidator("master-secret", &stubReader{}), grpcRegistrationResolverV3(t, store))
	defer stop()

	var responses []*pb.RegisterProjectIdentityV3Response
	for range 2 {
		response, err := client.RegisterProjectIdentityV3(bearerOutgoingContext("master-secret"), &pb.RegisterProjectIdentityV3Request{ProjectIdentityV3: grpcV3Identity()})
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
	}
	for _, response := range responses {
		resolution := response.GetProjectResolutionV3()
		if resolution.GetOutcome() != pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED || resolution.GetProjectKey() != grpcV3ProjectKey || resolution.GetCorrelation() == "" {
			t.Fatalf("registration response=%#v", response)
		}
	}
	if store.registrationCalls != 2 || store.creates != 1 || len(store.attempts) != 2 {
		t.Fatalf("registration store=%#v", store)
	}
	for _, attempt := range store.attempts {
		if !attempt.Valid() || attempt.Intent() != projectidentity.RegisterAnchorIntentV3 || attempt.Outcome() != projectidentity.ProjectResolvedOutcomeV3 || attempt.DescriptorVersion() != 3 || attempt.Correlation() == "" {
			t.Fatalf("redacted registration attempt=%#v", attempt)
		}
	}
}

func TestRegisterProjectIdentityV3RejectsUnauthorizedAdmissions(t *testing.T) {
	readOnlyToken := "engram_aaaa111100000000000000000000beef"
	readOnlyValidator := auth.NewValidator("master-secret", &stubReader{rows: map[string][]gormdb.APIToken{
		"aaaa1111": {makeKeycardRow(t, "read-only", readOnlyToken, "read-only")},
	}})
	for _, test := range []struct {
		name      string
		validator *auth.Validator
		token     string
		code      codes.Code
	}{
		{name: "missing bearer", validator: auth.NewValidator("master-secret", &stubReader{}), code: codes.Unauthenticated},
		{name: "invalid bearer", validator: auth.NewValidator("master-secret", &stubReader{}), token: "wrong-secret", code: codes.Unauthenticated},
		{name: "auth disabled", validator: nil, code: codes.PermissionDenied},
		{name: "read-only client", validator: readOnlyValidator, token: readOnlyToken, code: codes.PermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &grpcRegistrationStoreV3{binding: projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingMissingV3}}
			client, stop := newRegistrationV3Client(t, test.validator, grpcRegistrationResolverV3(t, store))
			defer stop()

			response, err := client.RegisterProjectIdentityV3(bearerOutgoingContext(test.token), &pb.RegisterProjectIdentityV3Request{ProjectIdentityV3: grpcV3Identity()})
			if response != nil || status.Code(err) != test.code {
				t.Fatalf("response=%#v status=%v error=%v", response, status.Code(err), err)
			}
			if store.registrationCalls != 0 || store.creates != 0 || len(store.attempts) != 0 {
				t.Fatalf("unauthorized registration wrote state: %#v", store)
			}
		})
	}
}

func TestRegisterProjectIdentityV3RejectsSessionIdentity(t *testing.T) {
	store := &grpcRegistrationStoreV3{binding: projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingMissingV3}}
	srv := &Server{identityResolverV3: grpcRegistrationResolverV3(t, store)}
	response, err := srv.RegisterProjectIdentityV3(auth.WithIdentity(context.Background(), auth.Session("admin")), &pb.RegisterProjectIdentityV3Request{ProjectIdentityV3: grpcV3Identity()})
	if response != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("response=%#v status=%v error=%v", response, status.Code(err), err)
	}
	if store.registrationCalls != 0 || store.creates != 0 || len(store.attempts) != 0 {
		t.Fatalf("session identity wrote state: %#v", store)
	}
}

func TestRegisterProjectIdentityV3RefusesInvalidOrExtraDescriptorWithoutBinding(t *testing.T) {
	for _, test := range []struct {
		name         string
		identity     func() *pb.ProjectIdentityV3
		attemptCount int
	}{
		{
			name: "malformed descriptor",
			identity: func() *pb.ProjectIdentityV3 {
				identity := grpcV3Identity()
				identity.ClientInstanceId = "private client instance"
				return identity
			},
			attemptCount: 1,
		},
		{
			name: "unknown descriptor field",
			identity: func() *pb.ProjectIdentityV3 {
				identity := grpcV3Identity()
				identity.ProtoReflect().SetUnknown([]byte{0x98, 0x06, 0x01})
				return identity
			},
			attemptCount: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &grpcRegistrationStoreV3{binding: projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingMissingV3}}
			client, stop := newRegistrationV3Client(t, auth.NewValidator("master-secret", &stubReader{}), grpcRegistrationResolverV3(t, store))
			defer stop()

			response, err := client.RegisterProjectIdentityV3(bearerOutgoingContext("master-secret"), &pb.RegisterProjectIdentityV3Request{ProjectIdentityV3: test.identity()})
			if response != nil {
				t.Fatalf("refusal returned response=%#v", response)
			}
			assertV3Refusal(t, err, projectidentity.ProjectDescriptorInvalidOutcomeV3, codes.InvalidArgument)
			if strings.Contains(err.Error(), "private client instance") || store.registrationCalls != 0 || store.creates != 0 || len(store.attempts) != test.attemptCount {
				t.Fatalf("refusal leaked or bound state: error=%v store=%#v", err, store)
			}
		})
	}
}

func TestInitializeAndCallToolV3NeverRegister(t *testing.T) {
	steps := []string{}
	intents := []projectidentity.ResolutionIntentV3{}
	srv := &Server{handler: identityOrderHandler{steps: &steps}}
	srv.identityResolverV3 = func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		intents = append(intents, request.Intent)
		return grpcV3Result(t, request.Intent, projectidentity.ProjectResolvedOutcomeV3)
	}
	if _, err := srv.Initialize(context.Background(), &pb.InitializeRequest{ProjectIdentityV3: grpcV3Identity()}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.CallTool(context.Background(), &pb.CallToolRequest{ToolName: "recall", ProjectIdentityV3: grpcV3Identity()}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(intents, []projectidentity.ResolutionIntentV3{projectidentity.ResolveExistingIntentV3, projectidentity.ResolveExistingIntentV3}) {
		t.Fatalf("normal V3 paths selected intents=%v", intents)
	}
}

func TestResolveProjectIdentityV3RecordsNormalComparisonsWithoutChangingResponses(t *testing.T) {
	observer := &grpcComparisonObserverV3{outcome: projectidentity.LegacyComparisonResolvedV2}
	store := &grpcComparisonStoreV3{}
	intents := []projectidentity.ResolutionIntentV3{}
	srv := &Server{handler: identityOrderHandler{steps: &[]string{}}, comparisonObserverV3: observer, comparisonStoreV3: store}
	srv.identityResolverV3 = func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		intents = append(intents, request.Intent)
		return grpcV3Result(t, request.Intent, projectidentity.ProjectResolvedOutcomeV3)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-request-id", "grpc-comparison-request-17", "x-engram-project-identity-adapter", "daemon"))
	response, err := srv.Initialize(ctx, &pb.InitializeRequest{ProjectIdentityV3: grpcV3Identity()})
	if err != nil || response.GetProjectResolutionV3().GetOutcome() != pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED {
		t.Fatalf("success response=%#v err=%v", response, err)
	}
	if _, err := srv.CallTool(ctx, &pb.CallToolRequest{ToolName: "issues", ProjectIdentityV3: grpcV3Identity(), ArgumentsJson: []byte(`{"action":"list","project":"raw"}`)}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(intents, []projectidentity.ResolutionIntentV3{projectidentity.ResolveExistingIntentV3, projectidentity.ReadFilterIntentV3}) || observer.calls != 2 || len(store.observations) != 2 {
		t.Fatalf("intents=%v observer_calls=%d observations=%#v", intents, observer.calls, store.observations)
	}
	for _, observation := range store.observations {
		if observation.Transport != projectidentity.ComparisonTransportDaemonV3 || observation.LegacyOutcome != projectidentity.LegacyComparisonResolvedV2 || !observation.Valid() {
			t.Fatalf("observation=%#v", observation)
		}
	}

	refusedObserver := &grpcComparisonObserverV3{outcome: projectidentity.LegacyComparisonRefusalV2}
	refusedStore := &grpcComparisonStoreV3{}
	refusedServer := &Server{comparisonObserverV3: refusedObserver, comparisonStoreV3: refusedStore}
	refusedServer.identityResolverV3 = func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		return grpcV3Result(t, request.Intent, projectidentity.ProjectOnboardingRequiredOutcomeV3)
	}
	response, err = refusedServer.Initialize(ctx, &pb.InitializeRequest{ProjectIdentityV3: grpcV3Identity()})
	if response != nil {
		t.Fatalf("refusal response=%#v", response)
	}
	assertV3Refusal(t, err, projectidentity.ProjectOnboardingRequiredOutcomeV3, codes.FailedPrecondition)
	if refusedObserver.calls != 1 || len(refusedStore.observations) != 1 || refusedStore.observations[0].Classification() != projectidentity.ComparisonRefusalV3 {
		t.Fatalf("refusal comparison observer_calls=%d observations=%#v", refusedObserver.calls, refusedStore.observations)
	}

	failingObserver := &grpcComparisonObserverV3{err: errors.New("telemetry unavailable")}
	failingStore := &grpcComparisonStoreV3{}
	failingServer := &Server{handler: identityOrderHandler{steps: &[]string{}}, comparisonObserverV3: failingObserver, comparisonStoreV3: failingStore}
	failingServer.identityResolverV3 = func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		return grpcV3Result(t, request.Intent, projectidentity.ProjectResolvedOutcomeV3)
	}
	response, err = failingServer.Initialize(ctx, &pb.InitializeRequest{ProjectIdentityV3: grpcV3Identity()})
	if err != nil || response.GetProjectResolutionV3().GetOutcome() != pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED || failingObserver.calls != 1 || len(failingStore.observations) != 1 || failingStore.observations[0].LegacyOutcome != projectidentity.LegacyComparisonUnavailableV2 || failingStore.observations[0].Classification() != projectidentity.ComparisonUnavailableV3 {
		t.Fatalf("observer failure changed response=%#v err=%v observer_calls=%d observations=%#v", response, err, failingObserver.calls, failingStore.observations)
	}

	registrationObserver := &grpcComparisonObserverV3{outcome: projectidentity.LegacyComparisonResolvedV2}
	registrationStore := &grpcComparisonStoreV3{}
	registrationServer := &Server{comparisonObserverV3: registrationObserver, comparisonStoreV3: registrationStore}
	registrationServer.identityResolverV3 = func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		return grpcV3Result(t, request.Intent, projectidentity.ProjectResolvedOutcomeV3)
	}
	registrationContext := auth.WithIdentity(ctx, auth.Admin())
	if response, err := registrationServer.RegisterProjectIdentityV3(registrationContext, &pb.RegisterProjectIdentityV3Request{ProjectIdentityV3: grpcV3Identity()}); err != nil || response.GetProjectResolutionV3().GetOutcome() != pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED || registrationObserver.calls != 0 || len(registrationStore.observations) != 0 {
		t.Fatalf("registration response=%#v err=%v observer_calls=%d observations=%#v", response, err, registrationObserver.calls, registrationStore.observations)
	}
}

func TestResolveProjectIdentityV3RecordsChannelValidatedOrigins(t *testing.T) {
	for _, test := range []struct {
		name      string
		ctx       context.Context
		transport projectidentity.ComparisonTransportV3
		observed  bool
	}{
		{name: "direct grpc", ctx: metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-request-id", "grpc-attempt-17")), transport: projectidentity.ComparisonTransportGRPCV3, observed: true},
		{name: "daemon grpc", ctx: metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-request-id", "daemon-attempt-17", "x-engram-project-identity-adapter", "daemon")), transport: projectidentity.ComparisonTransportDaemonV3, observed: true},
		{name: "direct http", ctx: projectidentity.WithHTTPComparisonOriginV3(context.Background(), "http", "http-attempt-17"), transport: projectidentity.ComparisonTransportHTTPV3, observed: true},
		{name: "hook http", ctx: projectidentity.WithHTTPComparisonOriginV3(context.Background(), "hook", "hook-attempt-17"), transport: projectidentity.ComparisonTransportHookV3, observed: true},
		{name: "openclaw http", ctx: projectidentity.WithHTTPComparisonOriginV3(context.Background(), "openclaw", "openclaw-attempt-17"), transport: projectidentity.ComparisonTransportOpenClawV3, observed: true},
		{name: "http rejects cross channel", ctx: projectidentity.WithHTTPComparisonOriginV3(context.Background(), "daemon", "http-cross-channel-17"), transport: projectidentity.ComparisonTransportHTTPV3, observed: true},
		{name: "grpc rejects cross channel", ctx: metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-request-id", "grpc-cross-channel-17", "x-engram-project-identity-adapter", "hook")), transport: projectidentity.ComparisonTransportGRPCV3, observed: true},
		{name: "unsafe request id skips telemetry", ctx: projectidentity.WithHTTPComparisonOriginV3(context.Background(), "hook", "https://fixture-user:fixture-credential@example.invalid/private/request"), observed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			observer := &grpcComparisonObserverV3{outcome: projectidentity.LegacyComparisonResolvedV2}
			store := &grpcComparisonStoreV3{}
			response, err := grpcV3ChannelObservationServer(t, observer, store).Initialize(test.ctx, &pb.InitializeRequest{ProjectIdentityV3: grpcV3Identity()})
			assertV3ChannelObservation(t, response, err, observer, store, test.transport, test.observed)
		})
	}
}

func grpcV3ChannelObservationServer(t *testing.T, observer *grpcComparisonObserverV3, store *grpcComparisonStoreV3) *Server {
	srv := &Server{handler: identityOrderHandler{steps: &[]string{}}, comparisonObserverV3: observer, comparisonStoreV3: store}
	srv.identityResolverV3 = func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		return grpcV3Result(t, request.Intent, projectidentity.ProjectResolvedOutcomeV3)
	}
	return srv
}

func assertV3ChannelObservation(t *testing.T, response *pb.InitializeResponse, err error, observer *grpcComparisonObserverV3, store *grpcComparisonStoreV3, transport projectidentity.ComparisonTransportV3, observed bool) {
	t.Helper()
	if err != nil || response.GetProjectResolutionV3().GetOutcome() != pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if !observed {
		if observer.calls != 0 || len(store.observations) != 0 {
			t.Fatalf("unsafe telemetry observer_calls=%d observations=%#v", observer.calls, store.observations)
		}
		return
	}
	if observer.calls != 1 || len(store.observations) != 1 || store.observations[0].Transport != transport {
		t.Fatalf("transport=%q observer_calls=%d observations=%#v", transport, observer.calls, store.observations)
	}
}

func TestResolveProjectIdentityV3ReplaysComparisonUnderResponseCorrelation(t *testing.T) {
	observer := &grpcComparisonObserverV3{outcome: projectidentity.LegacyComparisonResolvedV2}
	store := &grpcReplayComparisonStoreV3{}
	srv := &Server{handler: identityOrderHandler{steps: &[]string{}}, comparisonObserverV3: observer, comparisonStoreV3: store}
	srv.identityResolverV3 = func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		return grpcV3ResultWithCorrelation(t, request.Intent, projectidentity.ProjectResolvedOutcomeV3, request.Correlation)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-request-id", "grpc-comparison-replay-17", "x-engram-project-identity-adapter", "daemon"))
	first, err := srv.Initialize(ctx, &pb.InitializeRequest{ProjectIdentityV3: grpcV3Identity()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := srv.Initialize(ctx, &pb.InitializeRequest{ProjectIdentityV3: grpcV3Identity()})
	if err != nil {
		t.Fatal(err)
	}
	correlation := first.GetProjectResolutionV3().GetCorrelation()
	if correlation == "" || second.GetProjectResolutionV3().GetCorrelation() != correlation {
		t.Fatalf("replayed responses must share comparison correlation: first=%#v second=%#v", first, second)
	}
	observations := store.observationsFor(projectidentity.CorrelationV3(correlation))
	if len(observations) != 1 || string(observations[0].Correlation) != correlation || observer.calls != 2 {
		t.Fatalf("correlation=%q observations=%#v observer_calls=%d", correlation, observations, observer.calls)
	}
}

func TestResolveProjectIdentityV3SkipsComparisonWithoutStableRequestIdentity(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "missing request id", ctx: context.Background()},
		{name: "unsafe request id", ctx: metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-request-id", "https://fixture-user:fixture-credential@example.invalid/private/request"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			observer := &grpcComparisonObserverV3{outcome: projectidentity.LegacyComparisonResolvedV2}
			store := &grpcComparisonStoreV3{}
			srv := &Server{handler: identityOrderHandler{steps: &[]string{}}, comparisonObserverV3: observer, comparisonStoreV3: store}
			srv.identityResolverV3 = func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
				return grpcV3Result(t, request.Intent, projectidentity.ProjectResolvedOutcomeV3)
			}
			response, err := srv.Initialize(test.ctx, &pb.InitializeRequest{ProjectIdentityV3: grpcV3Identity()})
			if err != nil || response.GetProjectResolutionV3().GetOutcome() != pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED || observer.calls != 0 || len(store.observations) != 0 {
				t.Fatalf("response=%#v err=%v observer_calls=%d observations=%#v", response, err, observer.calls, store.observations)
			}
		})
	}
}

type grpcComparisonObserverV3 struct {
	calls   int
	outcome projectidentity.LegacyComparisonOutcomeV2
	err     error
}

func (observer *grpcComparisonObserverV3) ObserveLegacyOutcomeV2(_ context.Context, _ projectidentity.LegacyComparisonDescriptorV3) (projectidentity.LegacyComparisonOutcomeV2, error) {
	observer.calls++
	return observer.outcome, observer.err
}

type grpcComparisonStoreV3 struct {
	observations []projectidentity.ComparisonObservationV3
	err          error
}

func (store *grpcComparisonStoreV3) RecordComparisonV3(_ context.Context, observation projectidentity.ComparisonObservationV3) (projectidentity.ComparisonReceiptV3, error) {
	store.observations = append(store.observations, observation)
	return projectidentity.ComparisonReceiptV3{Correlation: observation.Correlation}, store.err
}

type grpcReplayComparisonStoreV3 struct {
	observations []projectidentity.ComparisonObservationV3
}

func (store *grpcReplayComparisonStoreV3) RecordComparisonV3(_ context.Context, observation projectidentity.ComparisonObservationV3) (projectidentity.ComparisonReceiptV3, error) {
	for _, persisted := range store.observations {
		if persisted.IdempotencyKey != observation.IdempotencyKey {
			continue
		}
		if !reflect.DeepEqual(persisted, observation) {
			return projectidentity.ComparisonReceiptV3{}, errors.New("conflicting comparison replay")
		}
		return projectidentity.ComparisonReceiptV3{IdempotencyKey: persisted.IdempotencyKey, Correlation: persisted.Correlation}, nil
	}
	store.observations = append(store.observations, observation)
	return projectidentity.ComparisonReceiptV3{IdempotencyKey: observation.IdempotencyKey, Correlation: observation.Correlation}, nil
}

func (store *grpcReplayComparisonStoreV3) observationsFor(correlation projectidentity.CorrelationV3) []projectidentity.ComparisonObservationV3 {
	var observations []projectidentity.ComparisonObservationV3
	for _, observation := range store.observations {
		if observation.Correlation == correlation {
			observations = append(observations, observation)
		}
	}
	return observations
}
