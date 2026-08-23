package engramcore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/module"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const daemonV3CanonicalProject = "11111111-1111-4111-8111-111111111111"

func TestProxyV3DescriptorForwardsWithoutV2Fallback(t *testing.T) {
	srv := &mockEngramServer{
		initResp: &pb.InitializeResponse{
			Tools:               []*pb.ToolDefinition{{Name: "recall", Description: "recall"}},
			CanonicalProject:    daemonV3CanonicalProject,
			ProjectResolutionV3: resolvedV3Response(),
		},
		callResp: &pb.CallToolResponse{
			ContentJson:         []byte(`[]`),
			CanonicalProject:    daemonV3CanonicalProject,
			ProjectResolutionV3: resolvedV3Response(),
		},
	}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"

	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatalf("V3 Initialize: %v", err)
	}
	assertV3InitializeRequest(t, srv.initReq)

	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("V3 CallTool: %v", err)
	}
	assertV3CallRequest(t, srv.callReq)
}

func TestProxyV3RefusalClearsCompatibilityCacheAndExposesOnlyTypedOutcome(t *testing.T) {
	srv := &mockEngramServer{callErr: typedV3Refusal(t, "PROJECT_ONBOARDING_REQUIRED")}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-refusal")

	_, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`))
	var moduleErr *module.ModuleError
	if !errors.As(err, &moduleErr) {
		t.Fatalf("error=%v, want typed module error", err)
	}
	if moduleErr.Code != "PROJECT_ONBOARDING_REQUIRED" || moduleErr.Message != "project identity resolution refused" {
		t.Fatalf("typed refusal=%#v", moduleErr)
	}
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("V3 refusal retained a compatibility cache entry")
	}
	if strings.Contains(err.Error(), project.ID) || strings.Contains(err.Error(), project.Cwd) || strings.Contains(err.Error(), "candidate") || strings.Contains(err.Error(), "credential") {
		t.Fatalf("refusal leaked private identity material: %v", err)
	}
	assertV3CallRequest(t, srv.callReq)
}

func TestProxyV3RejectsCanonicalProjectNotIssuedByResolution(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{
		ContentJson:         []byte(`[]`),
		CanonicalProject:    "client-selected-project-must-not-enter-context",
		ProjectResolutionV3: resolvedV3Response(),
	}}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"

	_, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`))
	var moduleErr *module.ModuleError
	if !errors.As(err, &moduleErr) || moduleErr.Code != "PROJECT_RESOLUTION_UNAVAILABLE" {
		t.Fatalf("error=%v, want server-resolution-only refusal", err)
	}
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("noncanonical V3 response retained a compatibility cache entry")
	}
	assertV3CallRequest(t, srv.callReq)
}

func assertV3InitializeRequest(t *testing.T, req *pb.InitializeRequest) {
	t.Helper()
	if req == nil || req.GetProjectIdentityV3() == nil {
		t.Fatalf("Initialize request=%#v, want V3 descriptor", req)
	}
	if req.GetProject() != "" || req.GetProjectIdentity() != nil {
		t.Fatalf("Initialize used V2/raw fallback: %#v", req)
	}
	assertV3Descriptor(t, req.GetProjectIdentityV3())
}

func assertV3CallRequest(t *testing.T, req *pb.CallToolRequest) {
	t.Helper()
	if req == nil || req.GetProjectIdentityV3() == nil {
		t.Fatalf("CallTool request=%#v, want V3 descriptor", req)
	}
	if req.GetProject() != "" || req.GetProjectIdentity() != nil {
		t.Fatalf("CallTool used V2/raw fallback: %#v", req)
	}
	assertV3Descriptor(t, req.GetProjectIdentityV3())
}

func assertV3Descriptor(t *testing.T, identity *pb.ProjectIdentityV3) {
	t.Helper()
	if identity.GetVersion() != 3 || identity.GetAnchorProjectId() != "22222222-2222-4222-8222-222222222222" || identity.GetName() != "daemon-v3" || identity.GetScope() != "repository" || identity.GetClientInstanceId() != "fixture-daemon-install" {
		t.Fatalf("descriptor=%#v", identity)
	}
	if got := identity.GetNormalizedGitRemotes(); len(got) != 1 || got[0] != "git.example.test/Platform/Daemon" {
		t.Fatalf("normalized remotes=%q", got)
	}
	if len(identity.GetLegacyIdentifiers()) != 0 {
		t.Fatalf("legacy identifiers=%#v, want none", identity.GetLegacyIdentifiers())
	}
}

func resolvedV3Response() *pb.ProjectResolutionResultV3 {
	projectKey := daemonV3CanonicalProject
	scope := "repository"
	return &pb.ProjectResolutionResultV3{
		Outcome:       pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED,
		Correlation:   "daemon-v3-correlation",
		ProjectKey:    &projectKey,
		ResolvedScope: &scope,
	}
}

func typedV3Refusal(t *testing.T, outcome string) error {
	t.Helper()
	st, err := status.New(codes.FailedPrecondition, "project identity resolution refused").WithDetails(&errdetails.ErrorInfo{
		Domain:   "engram.project_identity.v3",
		Reason:   outcome,
		Metadata: map[string]string{"correlation": "daemon-v3-correlation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

func daemonV3Repository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"remote", "add", "origin", "https://git.example.test/Platform/Daemon.git"},
		{"add", ".engram-project"},
	} {
		if args[0] == "add" {
			if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte("{\"version\":3,\"project_id\":\"22222222-2222-4222-8222-222222222222\",\"name\":\"daemon-v3\",\"scope\":\"repository\"}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return root
}
