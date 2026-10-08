package engramcore

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/module"
)

// This uses the production gRPC authentication/Initialize and MCP tool catalog,
// not a mock Initialize response or a helper-only anchor assertion.
func TestV3MissingAnchorToolsList(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	token := uuid.NewString()
	backend, _ := grpcserver.New(anchorDiscoveryHandler{mcp.NewServer(mcp.ServerOptions{Version: "anchor-discovery-test"})}, auth.NewValidator(token, nil))
	go func() { _ = backend.Serve(listener) }()
	t.Cleanup(backend.GracefulStop)

	for _, state := range []string{"absent", "malformed", "untracked", "unreadable", "tracked-deletion", "staged-deletion", "git-index-error", "dangling-symlink", "unauthenticated"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			git := func(args ...string) {
				t.Helper()
				command := exec.Command("git", append([]string{"-C", root}, args...)...)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, output)
				}
			}
			git("init", "--quiet")
			git("-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "Existing repository")
			anchorPath := filepath.Join(root, ".engram-project")
			validAnchor := []byte(`{"version":3,"project_id":"22222222-2222-4222-8222-222222222222","name":"private-display-name","scope":"repository"}`)
			switch state {
			case "malformed":
				if err := os.WriteFile(anchorPath, []byte(`{"version":3`), 0o600); err != nil {
					t.Fatal(err)
				}
				git("add", "--", ".engram-project")
			case "untracked", "tracked-deletion":
				if err := os.WriteFile(anchorPath, validAnchor, 0o600); err != nil {
					t.Fatal(err)
				}
				if state == "tracked-deletion" {
					git("add", "--", ".engram-project")
					if err := os.Remove(anchorPath); err != nil {
						t.Fatal(err)
					}
				}
			case "staged-deletion":
				if err := os.WriteFile(anchorPath, validAnchor, 0o600); err != nil {
					t.Fatal(err)
				}
				git("add", "--", ".engram-project")
				git("-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "Tracked anchor")
				git("rm", "--quiet", "--", ".engram-project")
			case "git-index-error":
				indexPath := filepath.Join(root, ".git", "index")
				if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if err := os.Mkdir(indexPath, 0o700); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				if err := os.Mkdir(anchorPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if _, err := os.ReadFile(anchorPath); err == nil || os.IsNotExist(err) {
					t.Fatalf("unreadable fixture did not produce a non-ENOENT read error: %v", err)
				}
			case "dangling-symlink":
				if err := os.Symlink(filepath.Join(root, "missing-target"), anchorPath); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			var before []byte
			if state != "git-index-error" {
				var err error
				before, err = os.ReadFile(filepath.Join(root, ".git", "index"))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			dispatcher, mod, project := buildV3ContractDispatcher(t, listener.Addr().String())
			project.Cwd = root
			project.Env[config.EnvWorkstationToken] = token
			if state == "unauthenticated" {
				project.Env[config.EnvWorkstationToken] = ""
			}
			response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcListReq(1))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("tools/list %s: %s", state, response)
			if state == "absent" {
				assertOnlyRegistrationTool(t, response)
				_, callErr := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`))
				var moduleErr *module.ModuleError
				if !errors.As(callErr, &moduleErr) || moduleErr.Code != "PROJECT_ANCHOR_INVALID" {
					t.Fatalf("missing-anchor scoped recall error=%v, want PROJECT_ANCHOR_INVALID", callErr)
				}
				if _, err := os.Lstat(anchorPath); !os.IsNotExist(err) {
					t.Fatalf("discovery created an anchor: %v", err)
				}
			} else {
				assertToolsListServiceUnavailable(t, response)
				if state != "unauthenticated" && !strings.Contains(string(response), "PROJECT_ANCHOR_INVALID") {
					t.Fatal("invalid anchor lost its typed refusal")
				}
			}
			for _, private := range []string{root, project.ID, "private-display-name", "22222222-2222-4222-8222-222222222222", token} {
				if strings.Contains(string(response), private) {
					t.Fatal("discovery leaked private identity or credentials")
				}
			}
			if mod.cache.HasEntry(project.ID) {
				t.Fatal("discovery retained legacy project authority")
			}
			if state != "git-index-error" {
				after, err := os.ReadFile(filepath.Join(root, ".git", "index"))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if string(before) != string(after) {
					t.Fatal("discovery mutated the repository index")
				}
			}
		})
	}
}

type anchorDiscoveryHandler struct{ server *mcp.Server }

func (h anchorDiscoveryHandler) HandleToolCall(ctx context.Context, name string, args []byte) ([]byte, bool, error) {
	params, err := json.Marshal(map[string]any{"name": name, "arguments": json.RawMessage(args)})
	if err != nil {
		return nil, false, err
	}
	response := h.server.HandleRequest(ctx, &mcp.Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})
	if response.Error != nil {
		body, err := json.Marshal(response.Error)
		return body, true, err
	}
	body, err := json.Marshal(response.Result)
	return body, false, err
}

func (h anchorDiscoveryHandler) ToolDefinitions() []grpcserver.ToolDef {
	tools := h.server.ListTools()
	defs := make([]grpcserver.ToolDef, len(tools))
	for i, tool := range tools {
		schema, _ := json.Marshal(tool.InputSchema)
		defs[i] = grpcserver.ToolDef{Name: tool.Name, Description: tool.Description, InputSchemaJSON: schema}
	}
	return defs
}

func (h anchorDiscoveryHandler) ServerInfo() (string, string) { return "engram", h.server.Version() }
