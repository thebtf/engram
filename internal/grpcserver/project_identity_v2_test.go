package grpcserver

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/mcp"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const grpcLogicalProjectID = "33333333-3333-4333-8333-333333333333"

type identityOrderHandler struct {
	steps     *[]string
	arguments *[]byte
	project   *string
	tools     []ToolDef
}

func (h identityOrderHandler) HandleToolCall(ctx context.Context, _ string, args []byte) ([]byte, bool, error) {
	*h.steps = append(*h.steps, "handler")
	if h.arguments != nil {
		*h.arguments = append((*h.arguments)[:0], args...)
	}
	if h.project != nil {
		*h.project = mcp.ProjectFromContext(ctx)
	}
	return []byte(`[]`), false, nil
}
func (h identityOrderHandler) ToolDefinitions() []ToolDef { return h.tools }
func (identityOrderHandler) ServerInfo() (string, string) { return "test", "test" }

func TestCallTool_UsesPortableProjectWithoutRegistration(t *testing.T) {
	steps := []string{}
	var project string
	srv := &Server{handler: identityOrderHandler{steps: &steps, project: &project}}

	resp, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
		ToolName: "recall",
		Project:  grpcLogicalProjectID,
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !reflect.DeepEqual(steps, []string{"handler"}) {
		t.Fatalf("order=%v", steps)
	}
	if resp.CanonicalProject != grpcLogicalProjectID || project != grpcLogicalProjectID {
		t.Fatalf("response=%v handler project=%q", resp, project)
	}
}

func TestCallTool_RewritesProjectArgumentToCanonicalBeforeHandler(t *testing.T) {
	steps := []string{}
	var captured []byte
	srv := &Server{handler: identityOrderHandler{steps: &steps, arguments: &captured}}

	resp, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
		ToolName:      "recall",
		Project:       grpcLogicalProjectID,
		ArgumentsJson: []byte(`{"query":"needle","project":"other","limit":7}`),
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if resp.CanonicalProject != grpcLogicalProjectID {
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
	if args.Project != grpcLogicalProjectID || args.Query != "needle" || args.Limit != 7 {
		t.Fatalf("handler args=%+v", args)
	}
	if !reflect.DeepEqual(steps, []string{"handler"}) {
		t.Fatalf("steps=%v", steps)
	}
}

func TestCallTool_CodeContextRegistrationKeepsProjectOuterOnly(t *testing.T) {
	steps := []string{}
	var captured []byte
	var project string
	srv := &Server{handler: identityOrderHandler{steps: &steps, arguments: &captured, project: &project}}
	arguments := []byte(`{"action":"register","source_label":"native-fixture","locator":"file:///private/worktree"}`)
	response, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
		ToolName: "codebase_context", Project: grpcLogicalProjectID, ArgumentsJson: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	if project != grpcLogicalProjectID || response.CanonicalProject != grpcLogicalProjectID || !reflect.DeepEqual(captured, arguments) {
		t.Fatalf("registration must preserve outer-only project: context=%q response=%q arguments=%s", project, response.CanonicalProject, captured)
	}
	steps = nil
	_, err = srv.CallTool(context.Background(), &pb.CallToolRequest{ToolName: "codebase_context", ArgumentsJson: arguments})
	if status.Code(err) != codes.FailedPrecondition || len(steps) != 0 {
		t.Fatalf("registration without canonical project reached handler: error=%v steps=%v", err, steps)
	}
}

func TestCallTool_PreservesDocumentedUnscopedReviewSelector(t *testing.T) {
	for _, toolName := range []string{"review_metrics.read", "review_queue.read"} {
		t.Run(toolName, func(t *testing.T) {
			steps := []string{}
			var captured []byte
			srv := &Server{handler: identityOrderHandler{steps: &steps, arguments: &captured}}

			_, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
				ToolName:      toolName,
				Project:       grpcLogicalProjectID,
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

func TestCallTool_WithoutOuterProjectSkipsIdentityResolution(t *testing.T) {
	steps := []string{}
	var captured []byte
	srv := &Server{handler: identityOrderHandler{steps: &steps, arguments: &captured}}

	arguments := []byte(`{"project":"all","limit":5}`)
	resp, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
		ToolName:      "review_queue.read",
		ArgumentsJson: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.CanonicalProject != "" || !reflect.DeepEqual(captured, arguments) || !reflect.DeepEqual(steps, []string{"handler"}) {
		t.Fatalf("response=%#v args=%s steps=%v", resp, captured, steps)
	}
}

func TestCallTool_PreservesExplicitTargetProjectFilters(t *testing.T) {
	tests := []struct {
		name        string
		toolName    string
		arguments   string
		wantProject string
	}{
		{name: "issues list target", toolName: "issues", arguments: `{"action":"list","project":"target-project","limit":5}`, wantProject: "target-project"},
		{name: "issues create actor", toolName: "issues", arguments: `{"action":"create","project":"other","target_project":"target-project"}`, wantProject: grpcLogicalProjectID},
		{name: "issues default list target", toolName: "issues", arguments: `{"project":"target-project","limit":5}`, wantProject: "target-project"},
		{name: "review metrics filter", toolName: "review_metrics.read", arguments: `{"project":"target-project","limit":5}`, wantProject: "target-project"},
		{name: "review queue filter", toolName: "review_queue.read", arguments: `{"project":"target-project","limit":5}`, wantProject: "target-project"},
		{name: "governance health filter", toolName: "rule_governance_health", arguments: `{"project":"target-project","limit":5}`, wantProject: "target-project"},
		{name: "governance queue filter", toolName: "rule_governance_queue", arguments: `{"project":"target-project","limit":5}`, wantProject: "target-project"},
		{name: "governance snapshots filter", toolName: "rule_governance_snapshots", arguments: `{"project":"target-project","limit":5}`, wantProject: "target-project"},
		{name: "governance usefulness filter", toolName: "rule_governance_usefulness", arguments: `{"project":"target-project","limit":5}`, wantProject: "target-project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := []string{}
			var captured []byte
			srv := &Server{handler: identityOrderHandler{steps: &steps, arguments: &captured}}

			_, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
				ToolName:      tt.toolName,
				Project:       grpcLogicalProjectID,
				ArgumentsJson: []byte(tt.arguments),
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
			if args.Project != tt.wantProject {
				t.Fatalf("project=%q, want %q", args.Project, tt.wantProject)
			}
		})
	}
}

func TestCallTool_RejectsMalformedActionBeforeProjectExemption(t *testing.T) {
	for _, action := range []string{`123`, `{}`, `[]`, `null`} {
		t.Run(action, func(t *testing.T) {
			steps := []string{}
			srv := &Server{handler: identityOrderHandler{steps: &steps}}

			_, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
				ToolName:      "issues",
				Project:       grpcLogicalProjectID,
				ArgumentsJson: []byte(`{"action":` + action + `,"project":"target-project"}`),
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("status=%v error=%v, want InvalidArgument", status.Code(err), err)
			}
			if len(steps) != 0 {
				t.Fatalf("handler ran with malformed action: %v", steps)
			}
		})
	}
}

func TestCallTool_PreservesAdminPurgeProjectTarget(t *testing.T) {
	steps := []string{}
	var captured []byte
	srv := &Server{handler: identityOrderHandler{steps: &steps, arguments: &captured}}

	resp, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
		ToolName:      "admin",
		Project:       grpcLogicalProjectID,
		ArgumentsJson: []byte(`{"action":"purge_project","project":"target-project","confirm":"target-project"}`),
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if resp.CanonicalProject != grpcLogicalProjectID {
		t.Fatalf("canonical response=%q", resp.CanonicalProject)
	}
	var args struct {
		Action  string `json:"action"`
		Project string `json:"project"`
		Confirm string `json:"confirm"`
	}
	if err := json.Unmarshal(captured, &args); err != nil {
		t.Fatalf("decode handler args: %v", err)
	}
	if args.Action != "purge_project" || args.Project != "target-project" || args.Confirm != "target-project" {
		t.Fatalf("handler args=%+v", args)
	}
}

func TestCallTool_RejectsNonStringProjectArgumentBeforeHandler(t *testing.T) {
	steps := []string{}
	srv := &Server{handler: identityOrderHandler{steps: &steps}}

	_, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
		ToolName:      "recall",
		Project:       grpcLogicalProjectID,
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

			_, err := srv.CallTool(context.Background(), &pb.CallToolRequest{
				ToolName:      "recall",
				Project:       grpcLogicalProjectID,
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

func TestInitialize_ReturnsCompleteUnscopedCatalog(t *testing.T) {
	definitions := []ToolDef{
		{Name: "recall", Description: "read project memory", InputSchemaJSON: []byte(`{"type":"object"}`)},
		{Name: "codebase_context", Description: "select authorized checkout", InputSchemaJSON: []byte(`{"type":"object"}`)},
		{Name: "codebase_search", Description: "search authorized view", InputSchemaJSON: []byte(`{"type":"object"}`)},
	}
	srv := &Server{handler: identityOrderHandler{steps: &[]string{}, tools: definitions}}

	resp, err := srv.Initialize(context.Background(), &pb.InitializeRequest{ClientName: "portable-project-client", ClientVersion: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ServerName != "test" || resp.ServerVersion != "test" || resp.CanonicalProject != "" || len(resp.Tools) != len(definitions) {
		t.Fatalf("response=%#v", resp)
	}
	for index, definition := range definitions {
		tool := resp.Tools[index]
		if tool.Name != definition.Name || tool.Description != definition.Description || !reflect.DeepEqual(tool.InputSchemaJson, definition.InputSchemaJSON) {
			t.Fatalf("catalog entry %d=%v want %+v", index, tool, definition)
		}
	}
}

func TestInitialize_DoesNotAdmitProjectOperations(t *testing.T) {
	steps := []string{}
	srv := &Server{handler: identityOrderHandler{steps: &steps}}
	if _, err := srv.Initialize(context.Background(), &pb.InitializeRequest{}); err != nil {
		t.Fatal(err)
	}
	response, err := srv.CallTool(context.Background(), &pb.CallToolRequest{ToolName: "recall", ArgumentsJson: []byte(`{"project":"` + grpcLogicalProjectID + `"}`)})
	if status.Code(err) != codes.FailedPrecondition || response != nil || len(steps) != 0 {
		t.Fatalf("unbound project call: response=%v error=%v steps=%v", response, err, steps)
	}
	st, _ := status.FromError(err)
	if len(st.Details()) != 1 {
		t.Fatalf("stable project-context detail missing: %#v", st.Details())
	}
	detail, ok := st.Details()[0].(*errdetails.ErrorInfo)
	if !ok || detail.Reason != "PROJECT_CONTEXT_REQUIRED" || detail.Domain != "engram.project" {
		t.Fatalf("project-context detail=%v", st.Details())
	}
}

func TestProjectIDDoesNotBypassAuthentication(t *testing.T) {
	srv := &Server{validator: auth.NewValidator("master-secret", &stubReader{})}
	req := &pb.CallToolRequest{
		ToolName: "recall",
		Project:  grpcLogicalProjectID,
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
			if detail.Reason != "PROJECT_CONTEXT_INVALID" || detail.Domain != "engram.project" {
				t.Fatalf("detail=%#v", detail)
			}
			if len(steps) != 0 {
				t.Fatalf("handler ran before selector rejection: %v", steps)
			}
		})
	}
}
