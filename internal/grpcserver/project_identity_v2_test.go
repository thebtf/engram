package grpcserver

import (
	"context"
	"encoding/json"
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
	steps     *[]string
	arguments *[]byte
}

func (h identityOrderHandler) HandleToolCall(_ context.Context, _ string, args []byte) ([]byte, bool, error) {
	*h.steps = append(*h.steps, "handler")
	if h.arguments != nil {
		*h.arguments = append((*h.arguments)[:0], args...)
	}
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

func TestCallTool_RewritesProjectArgumentToCanonicalBeforeHandler(t *testing.T) {
	steps := []string{}
	var captured []byte
	srv := &Server{handler: identityOrderHandler{steps: &steps, arguments: &captured}}
	srv.identityResolver = func(_ context.Context, _ *gormlib.DB, _ string, _ *pb.ProjectIdentityV2) (string, error) {
		return "canonical", nil
	}

	resp, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
		ToolName:      "codebase_search",
		Project:       "legacy",
		ArgumentsJson: []byte(`{"query":"needle","project":"other","limit":7}`),
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if resp.CanonicalProject != "canonical" {
		t.Fatalf("canonical response=%q", resp.CanonicalProject)
	}
	var args struct {
		Query   string `json:"query"`
		Project string `json:"project"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(captured, &args); err != nil {
		t.Fatalf("decode handler args: %v", err)
	}
	if args.Project != "canonical" || args.Query != "needle" || args.Limit != 7 {
		t.Fatalf("handler args=%+v", args)
	}
	if !reflect.DeepEqual(steps, []string{"handler"}) {
		t.Fatalf("steps=%v", steps)
	}
}

func TestCallTool_PreservesDocumentedUnscopedReviewSelector(t *testing.T) {
	for _, toolName := range []string{"review_metrics.read", "review_queue.read"} {
		t.Run(toolName, func(t *testing.T) {
			steps := []string{}
			var captured []byte
			srv := &Server{handler: identityOrderHandler{steps: &steps, arguments: &captured}}
			srv.identityResolver = func(_ context.Context, _ *gormlib.DB, _ string, _ *pb.ProjectIdentityV2) (string, error) {
				return "canonical", nil
			}

			_, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
				ToolName:      toolName,
				Project:       "legacy",
				ArgumentsJson: []byte(`{"project":"all","limit":5}`),
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			var args struct {
				Project string `json:"project"`
			}
			if err := json.Unmarshal(captured, &args); err != nil {
				t.Fatalf("decode handler args: %v", err)
			}
			if args.Project != "all" {
				t.Fatalf("project=%q, want all", args.Project)
			}
		})
	}
}

func TestCallTool_RejectsNonStringProjectArgumentBeforeHandler(t *testing.T) {
	steps := []string{}
	srv := &Server{handler: identityOrderHandler{steps: &steps}}
	srv.identityResolver = func(_ context.Context, _ *gormlib.DB, _ string, _ *pb.ProjectIdentityV2) (string, error) {
		return "canonical", nil
	}

	_, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
		ToolName:      "codebase_search",
		Project:       "legacy",
		ArgumentsJson: []byte(`{"query":"needle","project":42}`),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status=%v error=%v, want InvalidArgument", status.Code(err), err)
	}
	if len(steps) != 0 {
		t.Fatalf("handler ran with malformed project argument: %v", steps)
	}
}

func TestCallTool_RejectsCaseVariantProjectArgumentBeforeHandler(t *testing.T) {
	tests := []struct {
		name string
		args []byte
	}{
		{name: "title case", args: []byte(`{"query":"needle","Project":"other"}`)},
		{name: "upper case", args: []byte(`{"query":"needle","PROJECT":"other"}`)},
		{name: "mixed case", args: []byte(`{"query":"needle","pRoJeCt":"other"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := []string{}
			srv := &Server{handler: identityOrderHandler{steps: &steps}}
			srv.identityResolver = func(_ context.Context, _ *gormlib.DB, _ string, _ *pb.ProjectIdentityV2) (string, error) {
				return "canonical", nil
			}

			_, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
				ToolName:      "codebase_search",
				Project:       "legacy",
				ArgumentsJson: tt.args,
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("status=%v error=%v, want InvalidArgument", status.Code(err), err)
			}
			if len(steps) != 0 {
				t.Fatalf("handler ran with case-variant project argument: %v", steps)
			}
		})
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
