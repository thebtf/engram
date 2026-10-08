package engramcore

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNativeCodebaseRegistrationUsesActualGitRootWithoutProjectMutation(t *testing.T) {
	root := daemonV3Repository(t)
	markerPath := filepath.Join(root, ".engram-project")
	marker, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	selected := filepath.Join(root, "nested", "source with spaces")
	require.NoError(t, os.MkdirAll(selected, 0o755))
	server := &uciIndexAdapterGRPCServer{call: func(_ context.Context, request *pb.CallToolRequest) (*pb.CallToolResponse, error) {
		require.Equal(t, "codebase_context", request.GetToolName())
		require.Equal(t, "native-onboarding-session", request.GetSessionId())
		require.Empty(t, request.GetProject())
		require.Nil(t, request.GetProjectIdentity())
		require.Nil(t, request.GetProjectIdentityV3())
		var args nativeCodebaseRegistrationArgs
		require.NoError(t, json.Unmarshal(request.GetArgumentsJson(), &args))
		require.NotNil(t, args.SourceLabel)
		require.Equal(t, "My repository", *args.SourceLabel)
		require.Nil(t, args.SourceID)
		require.NotNil(t, args.Locator)
		physicalRoot, err := filepath.EvalSymlinks(root)
		require.NoError(t, err)
		locatorRoot := filepath.ToSlash(physicalRoot)
		if runtime.GOOS == "windows" {
			locatorRoot = "/" + locatorRoot
		}
		require.Equal(t, (&url.URL{Scheme: "file", Path: locatorRoot}).String(), *args.Locator)
		return &pb.CallToolResponse{ContentJson: []byte(`{"binding_kind":"checkout","context_handle":"server-owned-handle","context":null}`)}, nil
	}}
	serverURL := startUCIIndexAdapterGRPC(t, server)
	mod := NewModuleWithClientInstanceID("fixture-daemon-install")
	t.Cleanup(mod.pool.closeAll)
	project := uciClientTestProject(serverURL)
	project.Cwd = selected
	project.Env[config.EnvClaudeSessionID] = "host-session-is-not-authority"
	ctx := auditcontext.WithUCITransportSession(context.Background(), "native-onboarding-session")

	// A caller's Git environment must not redirect native registration.
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "foreign.git"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	response, err := mod.ProxyHandleTool(ctx, project, "codebase_context", json.RawMessage(`{"action":"register","source_label":"My repository"}`))
	require.NoError(t, err)
	require.Contains(t, string(response), "server-owned-handle")
	after, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	require.Equal(t, marker, after)
	require.Empty(t, server.bindRequestsSnapshot())
	_, incoming := server.metadataSnapshot()
	require.Equal(t, []string{"fixture-daemon-install"}, incoming.Get(uci.NoAuthCodeClientInstanceMetadataKey))
}

func TestNativeCodebaseRegistrationReusesOnlyServerBoundSource(t *testing.T) {
	for _, handle := range []string{"", "owned-handle"} {
		t.Run("handle="+handle, func(t *testing.T) {
			root := daemonV3Repository(t)
			server := &uciIndexAdapterGRPCServer{bind: func(_ context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
				require.Equal(t, handle, request.GetContextHandle())
				require.Equal(t, "native-source-session", request.GetClientSessionId())
				require.Nil(t, request.GetRequestedContext())
				response := uciClientTestBindResponse(request)
				if handle == "" {
					// Production default Bind resolves a published Context; no-View
					// checkout bindings require an explicit owned handle.
					response.Context = uciClientTestContextA()
				}
				return response, nil
			}, call: func(_ context.Context, request *pb.CallToolRequest) (*pb.CallToolResponse, error) {
				var args nativeCodebaseRegistrationArgs
				require.NoError(t, json.Unmarshal(request.GetArgumentsJson(), &args))
				require.Nil(t, args.SourceLabel)
				require.Nil(t, args.ContextHandle)
				require.NotNil(t, args.SourceID)
				require.Equal(t, uciClientTestSourceID, *args.SourceID)
				require.NotNil(t, args.Locator)
				return &pb.CallToolResponse{ContentJson: []byte(`{"binding_kind":"checkout","context_handle":"new-checkout-handle","context":null}`)}, nil
			}}
			mod := NewModuleWithClientInstanceID("fixture-daemon-install")
			t.Cleanup(mod.pool.closeAll)
			project := uciClientTestProject(startUCIIndexAdapterGRPC(t, server))
			project.Cwd = root
			ctx := auditcontext.WithUCITransportSession(context.Background(), "native-source-session")
			args := map[string]string{"action": "register"}
			if handle != "" {
				args["context_handle"] = handle
			}
			raw, err := json.Marshal(args)
			require.NoError(t, err)
			_, err = mod.ProxyHandleTool(ctx, project, "codebase_context", raw)
			require.NoError(t, err)
			require.Len(t, server.bindRequestsSnapshot(), 1)
			require.Len(t, server.callRequestsSnapshot(), 1)
		})
	}
}

func TestNativeCodebaseRegistrationRefusesUnboundForeignAndChangedHandle(t *testing.T) {
	for _, test := range []struct {
		name string
		code codes.Code
	}{
		{name: "absent selection", code: codes.FailedPrecondition},
		{name: "foreign source", code: codes.PermissionDenied},
		{name: "changed owned handle", code: codes.OK},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &uciIndexAdapterGRPCServer{bind: func(_ context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
				if test.code != codes.OK {
					return nil, status.Error(test.code, "closed server selection")
				}
				response := uciClientTestBindResponse(request)
				response.ContextHandle = "different-handle"
				return response, nil
			}}
			mod := NewModuleWithClientInstanceID("fixture-daemon-install")
			t.Cleanup(mod.pool.closeAll)
			project := uciClientTestProject(startUCIIndexAdapterGRPC(t, server))
			project.Cwd = "must-not-be-inspected-after-refusal"
			ctx := auditcontext.WithUCITransportSession(context.Background(), "native-refusal-session")
			_, err := mod.ProxyHandleTool(ctx, project, "codebase_context", json.RawMessage(`{"action":"register","context_handle":"owned-handle"}`))
			require.Error(t, err)
			require.Empty(t, server.callRequestsSnapshot())
		})
	}
}

func TestNativeCodebaseRegistrationRefusesUnavailableGitAndAmbiguousInputs(t *testing.T) {
	server := &uciIndexAdapterGRPCServer{}
	mod := NewModuleWithClientInstanceID("fixture-daemon-install")
	t.Cleanup(mod.pool.closeAll)
	project := uciClientTestProject(startUCIIndexAdapterGRPC(t, server))
	project.Cwd = t.TempDir()
	t.Setenv("PATH", t.TempDir())
	ctx := auditcontext.WithUCITransportSession(context.Background(), "native-input-session")
	for _, raw := range []string{
		`{"action":"register","source_label":"repository"}`,
		`{"action":"register","source_label":"repository","context_handle":"owned-handle"}`,
		`{"action":"register","source_label":"repository","source_id":"manufactured-source"}`,
		`{"action":"register","source_label":"repository","root":"/foreign"}`,
		`{"action":"register","context_handle":""}`,
		`{"action":"register","source_label":null}`,
		`{"action":"register","context_handle":null}`,
		`{"action":"register","source_label":"repository","locator":null}`,
	} {
		_, err := mod.ProxyHandleTool(ctx, project, "codebase_context", json.RawMessage(raw))
		require.Error(t, err, raw)
		var moduleErr *module.ModuleError
		require.ErrorAs(t, err, &moduleErr)
		require.NotContains(t, moduleErr.Message, project.Cwd)
	}
	require.Empty(t, server.bindRequestsSnapshot())
	require.Empty(t, server.callRequestsSnapshot())
}

func TestNativeCodebaseRegistrationRejectsUnboundSourceIDInAutoLocatorMode(t *testing.T) {
	server := &uciIndexAdapterGRPCServer{call: func(_ context.Context, _ *pb.CallToolRequest) (*pb.CallToolResponse, error) {
		return &pb.CallToolResponse{ContentJson: []byte(`{"binding_kind":"checkout","context_handle":"server-checkout","context":null}`)}, nil
	}}
	mod := NewModuleWithClientInstanceID("fixture-daemon-install")
	t.Cleanup(mod.pool.closeAll)
	project := uciClientTestProject(startUCIIndexAdapterGRPC(t, server))
	project.Cwd = daemonV3Repository(t)
	ctx := auditcontext.WithUCITransportSession(context.Background(), "native-unbound-source-session")
	const otherSourceID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	automatic := json.RawMessage(`{"action":"register","source_id":"` + otherSourceID + `"}`)
	_, err := mod.ProxyHandleTool(ctx, project, "codebase_context", automatic)
	var moduleErr *module.ModuleError
	require.ErrorAs(t, err, &moduleErr)
	require.Equal(t, "CONTEXT_MISMATCH", moduleErr.Code)
	require.Empty(t, server.bindRequestsSnapshot())
	require.Empty(t, server.callRequestsSnapshot(), "an otherwise valid local Git root must not authorize a caller-supplied Source")

	// Explicit locator registration keeps the existing server-owned contract.
	project.Cwd = "explicit-locator-must-not-inspect-cwd"
	explicit := json.RawMessage(`{"action":"register","source_id":"` + otherSourceID + `","locator":"file:///private/worktree"}`)
	_, err = mod.ProxyHandleTool(ctx, project, "codebase_context", explicit)
	require.NoError(t, err)
	require.Empty(t, server.bindRequestsSnapshot())
	calls := server.callRequestsSnapshot()
	require.Len(t, calls, 1)
	require.Equal(t, []byte(explicit), calls[0].GetArgumentsJson())
}
