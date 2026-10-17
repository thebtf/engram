package engramcore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thebtf/engram/internal/config"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

const (
	contractProjectID      = "22222222-2222-4222-8222-222222222222"
	otherContractProjectID = "33333333-3333-4333-8333-333333333333"
)

func writeContractProjectMarker(t *testing.T, root, projectID string) {
	t.Helper()
	raw := []byte(`{"project_id":"` + projectID + `","name":"private-display-name"}`)
	if err := os.WriteFile(filepath.Join(root, ".engram-project"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProxyHandleTool_FirstCallBeforeHookUsesLogicalProject(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	writeContractProjectMarker(t, project.Cwd, contractProjectID)

	if _, err := mod.ProxyHandleTool(t.Context(), project, "recall", json.RawMessage(`{"query":"saved decision"}`)); err != nil {
		t.Fatalf("first CallTool before any hook/session-connect: %v", err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	request := srv.callReq
	if request == nil || request.GetProject() != contractProjectID || request.GetToolName() != "recall" {
		t.Fatalf("first project-scoped request=%#v, want canonical UUID", request)
	}
	if srv.initCalls != 0 {
		t.Fatal("normal project resolution required a discovery handshake")
	}
}

func TestProxyHandleTool_ReReadsMarkerInsteadOfSessionOrLegacyCache(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	project.Env[config.EnvClaudeSessionID] = "same-host-session"
	mod.cache.ForceCacheEntry(project, "obsolete-selector-is-not-authority")

	for _, projectID := range []string{contractProjectID, otherContractProjectID} {
		writeContractProjectMarker(t, project.Cwd, projectID)
		if _, err := mod.ProxyHandleTool(t.Context(), project, "recall", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		srv.mu.Lock()
		request := srv.callReq
		srv.mu.Unlock()
		if request == nil || request.GetProject() != projectID || request.GetSessionId() != "same-host-session" {
			t.Fatalf("marker %s was replaced by session/cache authority: %#v", projectID, request)
		}
	}
	mod.OnProjectRemoved(project.ID)
	if _, err := mod.ProxyHandleTool(t.Context(), project, "recall", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("removing transient host state invalidated the portable marker: %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.callReq.GetProject() != otherContractProjectID {
		t.Fatal("project removal changed the portable UUID")
	}
}

func TestProxyTools_InitializeCarriesNoProjectAuthority(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{
		{Name: "recall"}, {Name: "codebase_context"},
	}}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	project.Cwd = filepath.Join(t.TempDir(), "missing-client-directory")
	project.Env[config.EnvClaudeSessionID] = "private-host-session"

	tools, err := mod.ProxyTools(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "recall" || tools[1].Name != "codebase_context" {
		t.Fatalf("unscoped discovery filtered the backend catalog: %#v", tools)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	request := srv.initReq
	if request == nil || request.GetProject() != "" {
		t.Fatalf("Initialize carried project authority: %#v", request)
	}
	if request.GetClientName() != "engram-daemon" || request.GetClientVersion() != daemonClientVersion || srv.initCalls != 1 {
		t.Fatalf("unexpected discovery handshake: %#v, calls=%d", request, srv.initCalls)
	}
}

func TestProxyHandleTool_SameHostProjectDifferentCWDResolvesIndependently(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	first, second := project, project
	second.Cwd = t.TempDir()
	writeContractProjectMarker(t, first.Cwd, contractProjectID)
	writeContractProjectMarker(t, second.Cwd, otherContractProjectID)

	for _, selected := range []struct {
		cwd, projectID string
	}{
		{first.Cwd, contractProjectID},
		{second.Cwd, otherContractProjectID},
		{first.Cwd, contractProjectID},
	} {
		project.Cwd = selected.cwd
		if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		srv.mu.Lock()
		request := srv.callReq
		srv.mu.Unlock()
		if request == nil || request.GetProject() != selected.projectID {
			t.Fatalf("cwd %s borrowed another client's project: %#v", selected.cwd, request)
		}
	}
}
