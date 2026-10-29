package engramcore

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
	"github.com/thebtf/engram/internal/worker"
)

// Exercise production authentication, Initialize and the complete MCP catalog.
func TestToolsList_ProjectStateDoesNotFilterAuthenticatedCatalog(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	contexts := gormstore.NewUCIContextStore(nil)
	contextApplication, err := mcp.NewUCIContextApplication(
		uci.NewContextResolver(contexts, gormstore.NewUCIContextAuthorizer(contexts), contexts), contexts)
	if err != nil {
		t.Fatal(err)
	}
	projections := gormstore.NewUCIProjectionStore(nil)
	application, err := worker.NewUCIApplication(contextApplication,
		uci.NewAliasResolver(contexts.LookupLegacyAliasRecords), uci.NewQueryService(projections), nil,
		uci.NewGraphService(projections), uci.NewVersionedReadService(projections), uci.NewIndexStatusService(projections, nil))
	if err != nil {
		t.Fatal(err)
	}
	catalog := mcp.NewServer(mcp.ServerOptions{Version: daemonClientVersion})
	catalog.SetCodebaseContextApplication(application)
	expected := make(map[string]mcp.Tool)
	for _, tool := range catalog.ListTools() {
		expected[tool.Name] = tool
	}
	for _, required := range []string{"store", "recall", "codebase_context", "codebase_search", "codebase_read", "codebase_graph"} {
		if _, ok := expected[required]; !ok {
			t.Fatalf("production catalog fixture omitted %s", required)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	token := uuid.NewString()
	backend, _ := grpcserver.New(anchorDiscoveryHandler{catalog}, auth.NewValidator(token, nil))
	go func() { _ = backend.Serve(listener) }()
	t.Cleanup(backend.GracefulStop)

	for _, state := range []string{"absent", "portable", "malformed", "directory", "git-index-error", "missing-cwd", "unauthenticated"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, ".engram-project")
			var beforeMarker []byte
			switch state {
			case "portable", "unauthenticated":
				writeContractProjectMarker(t, root, contractProjectID)
			case "malformed":
				if err := os.WriteFile(marker, []byte(`{"project_id":"private-invalid-uuid"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(marker, 0o700); err != nil {
					t.Fatal(err)
				}
			case "git-index-error":
				if output, err := exec.Command("git", "-C", root, "init", "--quiet").CombinedOutput(); err != nil {
					t.Fatalf("fixture git: %v: %s", err, output)
				}
				if err := os.Mkdir(filepath.Join(root, ".git", "index"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "missing-cwd":
				root = filepath.Join(root, "missing")
				marker = filepath.Join(root, ".engram-project")
			}
			if state == "portable" || state == "malformed" || state == "unauthenticated" {
				beforeMarker, err = os.ReadFile(marker)
				if err != nil {
					t.Fatal(err)
				}
			}
			dispatcher, _, project := buildV3ContractDispatcher(t, listener.Addr().String())
			project.Cwd = root
			project.Env[config.EnvWorkstationToken] = token
			if state == "unauthenticated" {
				project.Env[config.EnvWorkstationToken] = ""
			}
			response, err := dispatcher.HandleRequest(t.Context(), project, jsonrpcListReq(1))
			if err != nil {
				t.Fatal(err)
			}
			if state == "unauthenticated" {
				assertToolsListServiceUnavailable(t, response)
			} else {
				var got struct {
					Error  any `json:"error"`
					Result struct {
						Tools []struct {
							Name        string          `json:"name"`
							Description string          `json:"description"`
							InputSchema json.RawMessage `json:"inputSchema"`
						} `json:"tools"`
					} `json:"result"`
				}
				if err := json.Unmarshal(response, &got); err != nil {
					t.Fatal(err)
				}
				if got.Error != nil || len(got.Result.Tools) != len(expected) {
					t.Fatalf("%s tools/list=%s, want the complete production catalog", state, response)
				}
				seen := make(map[string]bool)
				for _, tool := range got.Result.Tools {
					definition, ok := expected[tool.Name]
					if !ok || seen[tool.Name] || tool.Description != definition.Description {
						t.Fatalf("catalog descriptor changed or duplicated: %q", tool.Name)
					}
					schema, err := json.Marshal(definition.InputSchema)
					if err != nil {
						t.Fatal(err)
					}
					assertJSONEqual(t, tool.Name+" schema", tool.InputSchema, schema)
					seen[tool.Name] = true
				}
			}
			for _, private := range []string{root, project.ID, "private-display-name", "private-invalid-uuid", contractProjectID, token} {
				if strings.Contains(string(response), private) {
					t.Fatal("discovery leaked private project context or credentials")
				}
			}
			if beforeMarker != nil {
				after, err := os.ReadFile(marker)
				if err != nil || string(after) != string(beforeMarker) {
					t.Fatal("discovery changed a portable marker")
				}
			} else if state == "directory" {
				if info, err := os.Lstat(marker); err != nil || !info.IsDir() {
					t.Fatal("discovery changed the nonregular marker")
				}
			} else if _, err := os.Lstat(marker); !os.IsNotExist(err) {
				t.Fatalf("discovery created a portable marker: %v", err)
			}
			if state == "git-index-error" {
				if info, err := os.Lstat(filepath.Join(root, ".git", "index")); err != nil || !info.IsDir() {
					t.Fatal("discovery changed the repository index")
				}
				if _, err := os.Lstat(filepath.Join(root, ".git", "engram-project.json")); !os.IsNotExist(err) {
					t.Fatalf("catalog discovery created a project cache: %v", err)
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
