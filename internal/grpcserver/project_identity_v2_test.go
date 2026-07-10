package grpcserver

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/auth"
	localgorm "github.com/thebtf/engram/internal/db/gorm"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gormlib "gorm.io/gorm"
)

type identityOrderHandler struct {
	steps *[]string
}

func (h identityOrderHandler) HandleToolCall(_ context.Context, _ string, _ []byte) ([]byte, bool, error) {
	*h.steps = append(*h.steps, "handler")
	return []byte(`[]`), false, nil
}
func (identityOrderHandler) ToolDefinitions() []ToolDef   { return nil }
func (identityOrderHandler) ServerInfo() (string, string) { return "test", "test" }

func TestCallTool_RegistersAndResolvesBeforeHandler(t *testing.T) {
	steps := []string{}
	srv := &Server{handler: identityOrderHandler{steps: &steps}}
	srv.identityResolver = func(_ context.Context, _ *gormlib.DB, selector string, identity *pb.ProjectIdentityV2) (string, error) {
		steps = append(steps, "resolve")
		if selector != "legacy" || identity == nil || identity.Version != 2 {
			t.Fatalf("resolver input selector=%q identity=%#v", selector, identity)
		}
		return "canonical", nil
	}

	resp, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
		ToolName: "recall",
		Project:  "legacy",
		ProjectIdentity: &pb.ProjectIdentityV2{
			Version:         2,
			LegacyProjectId: "legacy",
			GitRemote:       "https://example.invalid/acme/mono.git",
			RelativePath:    "packages/core/",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !reflect.DeepEqual(steps, []string{"resolve", "handler"}) {
		t.Fatalf("order=%v", steps)
	}
	if resp.CanonicalProject != "canonical" {
		t.Fatalf("canonical response=%q", resp.CanonicalProject)
	}
}

func TestInitialize_ResolvesBeforeReturningTools(t *testing.T) {
	srv := &Server{handler: identityOrderHandler{steps: &[]string{}}}
	srv.identityResolver = func(_ context.Context, _ *gormlib.DB, _ string, _ *pb.ProjectIdentityV2) (string, error) {
		return "canonical", nil
	}
	resp, err := srv.Initialize(context.Background(), &pb.InitializeRequest{Project: "legacy", ProjectIdentity: &pb.ProjectIdentityV2{Version: 2, GitRemote: "https://example.invalid/acme/mono.git", RelativePath: "packages/core/"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.CanonicalProject != "canonical" {
		t.Fatalf("canonical response=%q", resp.CanonicalProject)
	}
}

func TestCallTool_StableIdentityErrorsPrecedeMutation(t *testing.T) {
	srv := &Server{handler: identityOrderHandler{steps: &[]string{}}}
	srv.identityResolver = func(_ context.Context, _ *gormlib.DB, _ string, _ *pb.ProjectIdentityV2) (string, error) {
		return "", &localgorm.ProjectIdentityError{Code: localgorm.ProjectIdentityAmbiguous, UpgradeAction: localgorm.UpgradeActionSendProjectIdentityV2, Err: errors.New("ambiguous selector")}
	}
	_, err := srv.CallTool(context.Background(), &pb.CallToolRequest{ToolName: "recall", Project: "legacy"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("status=%v error=%v", status.Code(err), err)
	}
	st, _ := status.FromError(err)
	if len(st.Details()) != 1 {
		t.Fatalf("stable machine-readable detail missing: %#v", st.Details())
	}
}

func TestSelectorDoesNotBypassAuthentication(t *testing.T) {
	srv := &Server{validator: auth.NewValidator("master-secret", &stubReader{})}
	req := &pb.CallToolRequest{
		ToolName: "recall",
		Project:  "known-private-selector",
		ProjectIdentity: &pb.ProjectIdentityV2{
			Version:         2,
			LegacyProjectId: "known-private-selector",
			GitRemote:       "https://example.invalid/private.git",
		},
	}
	handler := func(_ context.Context, _ any) (any, error) {
		t.Fatal("identity metadata must not bypass the auth interceptor")
		return nil, nil
	}
	_, err := srv.authInterceptor(context.Background(), req, &grpc.UnaryServerInfo{FullMethod: pb.EngramService_CallTool_FullMethodName}, handler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("status=%v error=%v", status.Code(err), err)
	}
}

func TestProjectIdentityUnavailable_DoesNotExposeDatabaseDiagnostics(t *testing.T) {
	srv := &Server{handler: identityOrderHandler{steps: &[]string{}}}
	srv.identityResolver = func(_ context.Context, _ *gormlib.DB, _ string, _ *pb.ProjectIdentityV2) (string, error) {
		return "", &localgorm.ProjectIdentityError{
			Code:          localgorm.ProjectIdentityUnavailable,
			UpgradeAction: localgorm.UpgradeActionRetryProjectRegistration,
			Err:           errors.New("postgres internal-token-do-not-leak relation projects"),
		}
	}
	_, err := srv.CallTool(context.Background(), &pb.CallToolRequest{ToolName: "recall", Project: "legacy"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("status=%v error=%v", status.Code(err), err)
	}
	if strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), "relation projects") {
		t.Fatalf("database diagnostics leaked: %v", err)
	}
}

func TestCallTool_DefaultResolverRejectsMalformedSelectorsBeforeHandler(t *testing.T) {
	for _, selector := range []string{"a b", "../x"} {
		t.Run(selector, func(t *testing.T) {
			steps := []string{}
			srv := &Server{handler: identityOrderHandler{steps: &steps}}
			_, err := srv.CallTool(context.Background(), &pb.CallToolRequest{ToolName: "recall", Project: selector})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("status=%v error=%v, want InvalidArgument", status.Code(err), err)
			}
			st, ok := status.FromError(err)
			if !ok || len(st.Details()) != 1 {
				t.Fatalf("stable machine-readable detail missing: %#v", st.Details())
			}
			detail, ok := st.Details()[0].(*errdetails.ErrorInfo)
			if !ok {
				t.Fatalf("detail=%T, want ErrorInfo", st.Details()[0])
			}
			if detail.Reason != localgorm.ProjectIdentityInvalid || detail.Domain != "engram.project_identity" || detail.Metadata["upgrade_action"] != localgorm.UpgradeActionRegenerateProjectIdentityV2 {
				t.Fatalf("detail=%#v", detail)
			}
			if len(steps) != 0 {
				t.Fatalf("handler ran before selector rejection: %v", steps)
			}
		})
	}
}
