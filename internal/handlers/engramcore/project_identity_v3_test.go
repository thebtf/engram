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
	"github.com/thebtf/engram/internal/projectidentity"
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

func TestV3OnboardingKeepsStaticRegistrationVisibleAndResumesProxy(t *testing.T) {
	srv := &mockEngramServer{
		initErr:      typedV3Refusal(t, "PROJECT_ONBOARDING_REQUIRED"),
		registerResp: &pb.RegisterProjectIdentityV3Response{ProjectResolutionV3: resolvedV3Response()},
		callResp: &pb.CallToolResponse{
			ContentJson:         []byte(`[]`),
			CanonicalProject:    daemonV3CanonicalProject,
			ProjectResolutionV3: resolvedV3Response(),
		},
	}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-refusal")

	before, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("onboarding tools/list: %v", err)
	}
	assertOnlyRegistrationTool(t, before)
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("onboarding refusal retained a compatibility cache entry")
	}
	if strings.Contains(string(before), project.ID) || strings.Contains(string(before), project.Cwd) {
		t.Fatalf("onboarding tools/list leaked local identity material: %s", before)
	}

	for id := 2; id <= 3; id++ {
		registration, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcCallReq(id, "project_identity.register_v3"))
		if err != nil {
			t.Fatalf("registration %d: %v", id, err)
		}
		assertRegistrationResolved(t, registration)
	}

	srv.mu.Lock()
	srv.initErr = nil
	srv.initResp = &pb.InitializeResponse{
		Tools:               []*pb.ToolDefinition{{Name: "recall", Description: "recall"}},
		CanonicalProject:    daemonV3CanonicalProject,
		ProjectResolutionV3: resolvedV3Response(),
	}
	registerCalls := srv.registerCalls
	registerReq := srv.registerReq
	initCalls := srv.initCalls
	callReq := srv.callReq
	srv.mu.Unlock()
	if initCalls != 1 || callReq != nil {
		t.Fatalf("registration bypassed static dispatch: Initialize calls=%d CallTool request=%#v", initCalls, callReq)
	}
	if registerCalls != 2 {
		t.Fatalf("registration calls=%d, want 2 to preserve server idempotency", registerCalls)
	}
	if registerReq == nil || registerReq.GetProjectIdentityV3() == nil {
		t.Fatalf("registration request=%#v, want descriptor-only request", registerReq)
	}
	assertV3Descriptor(t, registerReq.GetProjectIdentityV3())
	if strings.Contains(registerReq.String(), project.ID) || strings.Contains(registerReq.String(), project.Cwd) {
		t.Fatalf("registration request leaked raw mux identity: %s", registerReq)
	}

	after, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcListReq(4))
	if err != nil {
		t.Fatalf("post-registration tools/list: %v", err)
	}
	assertRegistrationAndProxyTool(t, after, "recall")
	if _, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcCallReq(5, "recall")); err != nil {
		t.Fatalf("post-registration V3 tool: %v", err)
	}
	assertV3InitializeRequest(t, srv.initReq)
	assertV3CallRequest(t, srv.callReq)
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("V3 registration or normal proxy route populated a compatibility cache entry")
	}
}

func TestV3RegistrationRejectsNonEmptyOrNullArgumentsWithoutCallingGRPC(t *testing.T) {
	srv := &mockEngramServer{}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-reach-registration")

	for _, args := range []json.RawMessage{
		json.RawMessage(`{"project_key":"must-not-be-accepted"}`),
		json.RawMessage(`null`),
	} {
		_, err := mod.HandleTool(context.Background(), project, projectIdentityV3RegistrationTool, args)
		var moduleErr *module.ModuleError
		if !errors.As(err, &moduleErr) || moduleErr.Code != "tool_input_invalid" {
			t.Fatalf("registration input error=%v, want safe typed refusal", err)
		}
		if strings.Contains(err.Error(), project.ID) || strings.Contains(err.Error(), project.Cwd) || strings.Contains(err.Error(), "must-not-be-accepted") {
			t.Fatalf("registration argument refusal leaked private input: %v", err)
		}
	}
	srv.mu.Lock()
	registerCalls := srv.registerCalls
	srv.mu.Unlock()
	if registerCalls != 0 {
		t.Fatalf("registration RPC calls=%d, want 0", registerCalls)
	}
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("registration argument refusal retained a compatibility cache entry")
	}
}

func TestV3NonOnboardingRefusalFailsClosedWithoutProxyTools(t *testing.T) {
	srv := &mockEngramServer{initErr: typedV3Refusal(t, "PROJECT_SCOPE_MISMATCH")}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-refusal")

	response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("non-onboarding tools/list: %v", err)
	}
	assertToolsListServiceUnavailable(t, response)
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("non-onboarding V3 refusal retained a compatibility cache entry")
	}
}

func TestV3MixedOnboardingRefusalFailsClosedWithoutProxyTools(t *testing.T) {
	st, err := status.New(codes.FailedPrecondition, "project identity resolution refused").WithDetails(
		&errdetails.ErrorInfo{
			Domain: "engram.project_identity.v3",
			Reason: string(projectidentity.ProjectOnboardingRequiredOutcomeV3),
		},
		&errdetails.ErrorInfo{
			Domain: "engram.project_identity.v3",
			Reason: string(projectidentity.ProjectScopeMismatchOutcomeV3),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	srv := &mockEngramServer{initErr: st.Err()}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-refusal")

	response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("mixed onboarding tools/list: %v", err)
	}
	assertToolsListServiceUnavailable(t, response)
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("mixed V3 refusal retained a compatibility cache entry")
	}
}

func TestV2DoesNotExposeRegistrationOrFallBackToV2(t *testing.T) {
	srv := &mockEngramServer{}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)

	if tools := mod.Tools(); len(tools) != 0 {
		t.Fatalf("V2 static tools=%v, want unchanged surface", tools)
	}
	_, err := mod.HandleTool(context.Background(), project, projectIdentityV3RegistrationTool, json.RawMessage(`{}`))
	var moduleErr *module.ModuleError
	if !errors.As(err, &moduleErr) || moduleErr.Code != "PROJECT_DESCRIPTOR_UNSUPPORTED" {
		t.Fatalf("V2 registration error=%v, want V3-only refusal", err)
	}
	srv.mu.Lock()
	registerCalls := srv.registerCalls
	srv.mu.Unlock()
	if registerCalls != 0 {
		t.Fatalf("V2 registration RPC calls=%d, want 0", registerCalls)
	}
}

func TestV2ReservedRegistrationNameDoesNotReachProxy(t *testing.T) {
	srv := &mockEngramServer{}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildContractDispatcher(t, grpcAddr)
	mod.cache.ForceCacheEntry(project, "cached-v2-selector")

	response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcCallReq(1, projectIdentityV3RegistrationTool))
	if err != nil {
		t.Fatalf("reserved V2 tools/call: %v", err)
	}
	if !strings.Contains(string(response), "PROJECT_DESCRIPTOR_UNSUPPORTED") {
		t.Fatalf("reserved V2 tools/call=%s, want safe V3-only refusal", response)
	}
	srv.mu.Lock()
	callReq := srv.callReq
	srv.mu.Unlock()
	if callReq != nil {
		t.Fatalf("reserved V2 tool reached backend CallTool: %#v", callReq)
	}
	if !mod.cache.HasEntry(project.ID) {
		t.Fatal("reserved V2 tool mutated the compatibility selector cache")
	}
}

func assertOnlyRegistrationTool(t *testing.T, response []byte) {
	t.Helper()
	var got struct {
		Error  any `json:"error"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &got); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	if got.Error != nil || len(got.Result.Tools) != 1 || got.Result.Tools[0].Name != "project_identity.register_v3" {
		t.Fatalf("onboarding tools/list=%s, want static registration tool only", response)
	}
}

func assertRegistrationAndProxyTool(t *testing.T, response []byte, proxyName string) {
	t.Helper()
	var got struct {
		Error  any `json:"error"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &got); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	if got.Error != nil || len(got.Result.Tools) != 2 || got.Result.Tools[0].Name != "project_identity.register_v3" || got.Result.Tools[1].Name != proxyName {
		t.Fatalf("post-registration tools/list=%s, want static registration and proxy tool", response)
	}
}

func assertRegistrationResolved(t *testing.T, response []byte) {
	t.Helper()
	if !strings.Contains(string(response), `"isError":false`) || !strings.Contains(string(response), `"PROJECT_RESOLVED"`) {
		t.Fatalf("registration response=%s, want typed resolved outcome", response)
	}
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
