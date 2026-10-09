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
	"github.com/thebtf/engram/internal/projectidentity"
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

	for _, state := range []string{"absent", "neveranchored-linked", "neveranchored-packed", "neveranchored-untracked-sibling", "sibling-index-anchor", "sibling-index-error", "git-foreign-directory", "git-foreign-discovery", "git-foreign-common", "malformed", "untracked", "unreadable", "tracked-deletion", "staged-deletion", "git-index-error", "git-detached-head-error", "git-symbolic-head-error", "git-malformed-head-ref", "git-missing-head-ref", "visible-branch-anchor", "packed-branch-anchor", "linked-preanchor", "detached-sibling-anchor", "dangling-symlink", "unauthenticated"} {
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
			case "staged-deletion", "git-malformed-head-ref", "git-missing-head-ref":
				if err := os.WriteFile(anchorPath, validAnchor, 0o600); err != nil {
					t.Fatal(err)
				}
				git("add", "--", ".engram-project")
				git("-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "Tracked anchor")
				git("rm", "--quiet", "--", ".engram-project")
				if state != "staged-deletion" {
					branch, err := exec.Command("git", "-C", root, "symbolic-ref", "HEAD").Output()
					if err != nil {
						t.Fatal(err)
					}
					refPath := filepath.Join(root, ".git", filepath.FromSlash(strings.TrimSpace(string(branch))))
					if state == "git-malformed-head-ref" {
						if err := os.WriteFile(refPath, []byte("not-an-object\n"), 0o600); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Remove(refPath); err != nil {
						t.Fatal(err)
					}
				}
			case "visible-branch-anchor", "packed-branch-anchor", "linked-preanchor", "detached-sibling-anchor":
				git("branch", "pre-anchor")
				if err := os.WriteFile(anchorPath, validAnchor, 0o600); err != nil {
					t.Fatal(err)
				}
				git("add", "--", ".engram-project")
				if state == "detached-sibling-anchor" {
					tree, err := exec.Command("git", "-C", root, "write-tree").Output()
					if err != nil {
						t.Fatal(err)
					}
					commit, err := exec.Command("git", "-C", root, "-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "commit-tree", strings.TrimSpace(string(tree)), "-p", "HEAD", "-m", "Visible detached anchor").Output()
					if err != nil {
						t.Fatal(err)
					}
					git("rm", "--cached", "--quiet", "--", ".engram-project")
					if err := os.Remove(anchorPath); err != nil {
						t.Fatal(err)
					}
					git("worktree", "add", "--quiet", "--detach", filepath.Join(t.TempDir(), "anchored"), strings.TrimSpace(string(commit)))
				} else {
					git("-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "Tracked anchor")
					if state == "linked-preanchor" {
						linked := filepath.Join(t.TempDir(), "preanchor")
						git("worktree", "add", "--quiet", linked, "pre-anchor")
						root = linked
						anchorPath = filepath.Join(root, ".engram-project")
					} else {
						git("checkout", "--quiet", "pre-anchor")
						if state == "packed-branch-anchor" {
							git("pack-refs", "--all")
						}
					}
				}
			case "git-foreign-directory", "git-foreign-discovery", "git-foreign-common":
				if err := os.WriteFile(anchorPath, validAnchor, 0o600); err != nil {
					t.Fatal(err)
				}
				git("add", "--", ".engram-project")
				git("-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "Tracked anchor")
				git("rm", "--quiet", "--", ".engram-project")
				foreign := t.TempDir()
				for _, args := range [][]string{{"init", "--quiet"}, {"-c", "user.name=Anchor Test", "-c", "user.email=anchor@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "Foreign empty repository"}} {
					if out, err := exec.Command("git", append([]string{"-C", foreign}, args...)...).CombinedOutput(); err != nil {
						t.Fatalf("foreign fixture: %v: %s", err, out)
					}
				}
				if state == "git-foreign-common" {
					t.Setenv("GIT_COMMON_DIR", filepath.Join(foreign, ".git"))
				} else {
					t.Setenv("GIT_DIR", filepath.Join(foreign, ".git"))
					if state == "git-foreign-directory" {
						t.Setenv("GIT_WORK_TREE", root)
					}
				}
			case "git-index-error":
				indexPath := filepath.Join(root, ".git", "index")
				if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if err := os.Mkdir(indexPath, 0o700); err != nil {
					t.Fatal(err)
				}
			case "git-detached-head-error", "git-symbolic-head-error":
				badObject := strings.Repeat("1", 40) + "\n"
				if state == "git-detached-head-error" {
					if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte(badObject), 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					git("symbolic-ref", "HEAD", "refs/heads/broken-head")
					if err := os.WriteFile(filepath.Join(root, ".git", "refs", "heads", "broken-head"), []byte(badObject), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "neveranchored-linked":
				linked := filepath.Join(t.TempDir(), "neveranchored")
				git("worktree", "add", "--quiet", "--detach", linked, "HEAD")
				root = linked
				anchorPath = filepath.Join(root, ".engram-project")
			case "neveranchored-packed":
				git("branch", "other-neveranchored")
				git("pack-refs", "--all")
			case "sibling-index-anchor", "sibling-index-error", "neveranchored-untracked-sibling":
				linked := filepath.Join(t.TempDir(), "anchor sibling кириллица")
				git("worktree", "add", "--quiet", "--detach", linked, "HEAD")
				if err := os.WriteFile(filepath.Join(linked, ".engram-project"), validAnchor, 0o600); err != nil {
					t.Fatal(err)
				}
				if state == "sibling-index-anchor" {
					if out, err := exec.Command("git", "-C", linked, "add", "--", ".engram-project").CombinedOutput(); err != nil {
						t.Fatalf("track sibling anchor: %v: %s", err, out)
					}
					anchor, err := projectidentity.DiscoverAnchorV3(t.Context(), linked, "repository")
					if err != nil || anchor.ProjectID != "22222222-2222-4222-8222-222222222222" {
						t.Fatalf("actual producer rejected index-tracked sibling: %+v: %v", anchor, err)
					}
					t.Log("actual DiscoverAnchorV3 accepts sibling anchor after git add, before commit")
				} else if state == "sibling-index-error" {
					index, err := exec.Command("git", "-C", linked, "rev-parse", "--git-path", "index").Output()
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(strings.TrimSpace(string(index))); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(strings.TrimSpace(string(index)), 0o700); err != nil {
						t.Fatal(err)
					}
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
			indexCommand := exec.Command("git", "-C", root, "rev-parse", "--git-path", "index")
			indexCommand.Env = projectidentity.RepositoryGitEnvironmentV3()
			indexPath, err := indexCommand.Output()
			if err != nil {
				t.Fatal(err)
			}
			selectedIndex := strings.TrimSpace(string(indexPath))
			if !filepath.IsAbs(selectedIndex) {
				selectedIndex = filepath.Join(root, selectedIndex)
			}
			var before []byte
			if state != "git-index-error" {
				var err error
				before, err = os.ReadFile(selectedIndex)
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
			if state == "absent" || strings.HasPrefix(state, "neveranchored-") {
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
				after, err := os.ReadFile(selectedIndex)
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
