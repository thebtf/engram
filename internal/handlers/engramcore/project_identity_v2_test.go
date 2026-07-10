package engramcore

import (
	"context"
	"encoding/json"
	"testing"

	pb "github.com/thebtf/engram/proto/engram/v1"
)

func TestProxyHandleTool_FirstCallBeforeHookSendsProjectIdentityV2(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)

	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("first CallTool before any hook/session-connect: %v", err)
	}

	srv.mu.Lock()
	req := srv.callReq
	srv.mu.Unlock()
	if req == nil || req.ProjectIdentity == nil {
		t.Fatal("first CallTool did not carry project_identity v2")
	}
	if req.ProjectIdentity.Version != 2 {
		t.Fatalf("identity version=%d", req.ProjectIdentity.Version)
	}
	if req.ProjectIdentity.LegacyProjectId == "" {
		t.Fatal("legacy selector is required for mixed-version convergence")
	}
}

func TestProxyTools_SendsProjectIdentityV2OnInitialize(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{}}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)

	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	srv.mu.Lock()
	req := srv.initReq
	srv.mu.Unlock()
	if req == nil || req.ProjectIdentity == nil || req.ProjectIdentity.Version != 2 {
		t.Fatalf("Initialize project identity=%#v", req)
	}
}

func TestProjectIdentityV2_ProtoFieldNumbersRemainAdditive(t *testing.T) {
	callReq := (&pb.CallToolRequest{}).ProtoReflect().Descriptor().Fields()
	if got := callReq.ByName("project_identity").Number(); got != 5 {
		t.Fatalf("CallToolRequest.project_identity tag=%d, want 5", got)
	}
	callResp := (&pb.CallToolResponse{}).ProtoReflect().Descriptor().Fields()
	if got := callResp.ByName("canonical_project").Number(); got != 3 {
		t.Fatalf("CallToolResponse.canonical_project tag=%d, want 3", got)
	}
	initReq := (&pb.InitializeRequest{}).ProtoReflect().Descriptor().Fields()
	if got := initReq.ByName("project_identity").Number(); got != 4 {
		t.Fatalf("InitializeRequest.project_identity tag=%d, want 4", got)
	}
	initResp := (&pb.InitializeResponse{}).ProtoReflect().Descriptor().Fields()
	if got := initResp.ByName("canonical_project").Number(); got != 4 {
		t.Fatalf("InitializeResponse.canonical_project tag=%d, want 4", got)
	}
}
