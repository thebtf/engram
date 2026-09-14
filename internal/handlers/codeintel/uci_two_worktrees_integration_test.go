package codeintel_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	gormlogger "gorm.io/gorm/logger"

	"github.com/thebtf/engram/internal/config"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/handlers/codeintel"
	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/dispatcher"
	"github.com/thebtf/engram/internal/module/lifecycle"
	"github.com/thebtf/engram/internal/module/registry"
	"github.com/thebtf/engram/internal/proxy"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

const (
	uciT001RelativePath = "pkg/target.go"
	uciT001Query        = "SharedTarget"
	uciT001AlphaMarker  = "ALPHA_BODY_MARKER"
	uciT001BetaMarker   = "BETA_BODY_MARKER"
)

type uciT001MCPAdapter struct {
	server *mcp.Server
}

func (a *uciT001MCPAdapter) HandleToolCall(ctx context.Context, toolName string, argsJSON []byte) ([]byte, bool, error) {
	paramsJSON, err := json.Marshal(map[string]any{
		"name":      toolName,
		"arguments": json.RawMessage(argsJSON),
	})
	if err != nil {
		return nil, false, fmt.Errorf("marshal tool call params: %w", err)
	}
	resp := a.server.HandleRequest(ctx, &mcp.Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  paramsJSON,
	})
	if resp == nil {
		return nil, false, fmt.Errorf("MCP server returned no response")
	}
	if resp.Error != nil {
		errJSON, marshalErr := json.Marshal(resp.Error)
		if marshalErr != nil {
			return nil, false, fmt.Errorf("marshal MCP error: %w", marshalErr)
		}
		return errJSON, true, nil
	}
	resultJSON, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, false, fmt.Errorf("marshal MCP result: %w", err)
	}
	return resultJSON, false, nil
}

func (a *uciT001MCPAdapter) ToolDefinitions() []grpcserver.ToolDef {
	tools := a.server.ListTools()
	defs := make([]grpcserver.ToolDef, len(tools))
	for i, tool := range tools {
		schemaJSON, _ := json.Marshal(tool.InputSchema)
		defs[i] = grpcserver.ToolDef{
			Name:            tool.Name,
			Description:     tool.Description,
			InputSchemaJSON: schemaJSON,
		}
	}
	return defs
}

func (a *uciT001MCPAdapter) ServerInfo() (string, string) {
	return "engram", a.server.Version()
}

type uciT001RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type uciT001RPCResponse struct {
	Result json.RawMessage  `json:"result"`
	Error  *uciT001RPCError `json:"error"`
}

type uciT001ToolResult struct {
	Content []json.RawMessage `json:"content"`
	IsError bool              `json:"isError"`
}

type uciT001TextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type uciT001SearchPayload struct {
	Results []struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	} `json:"results"`
	Count   int    `json:"count"`
	Query   string `json:"query"`
	Project string `json:"project"`
}

type uciT001DispatcherHarness struct {
	t          *testing.T
	dispatcher *dispatcher.Dispatcher
	nextID     int
}

func (h *uciT001DispatcherHarness) request(project muxcore.ProjectContext, method string, params any) uciT001RPCResponse {
	h.t.Helper()
	h.nextID++
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      h.nextID,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	reqJSON, err := json.Marshal(req)
	require.NoError(h.t, err)
	respJSON, err := h.dispatcher.HandleRequest(context.Background(), project, reqJSON)
	require.NoError(h.t, err)
	require.NotEmpty(h.t, respJSON)
	var resp uciT001RPCResponse
	require.NoError(h.t, json.Unmarshal(respJSON, &resp))
	require.Nilf(h.t, resp.Error, "JSON-RPC error: %+v", resp.Error)
	require.NotEmpty(h.t, resp.Result)
	return resp
}

func (h *uciT001DispatcherHarness) initialize(project muxcore.ProjectContext, clientName string) {
	h.t.Helper()
	resp := h.request(project, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    clientName,
			"version": "t001",
		},
	})
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	require.NoError(h.t, json.Unmarshal(resp.Result, &result))
	require.Equal(h.t, "2024-11-05", result.ProtocolVersion)
}

func (h *uciT001DispatcherHarness) listTools(project muxcore.ProjectContext) []string {
	h.t.Helper()
	resp := h.request(project, "tools/list", nil)
	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	require.NoError(h.t, json.Unmarshal(resp.Result, &result))
	names := make([]string, len(result.Tools))
	for i, tool := range result.Tools {
		names[i] = tool.Name
	}
	return names
}

func (h *uciT001DispatcherHarness) callTool(project muxcore.ProjectContext, name string, arguments map[string]any) uciT001ToolResult {
	h.t.Helper()
	resp := h.request(project, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	var result uciT001ToolResult
	require.NoError(h.t, json.Unmarshal(resp.Result, &result))
	require.Falsef(h.t, result.IsError, "tool %s returned isError: %s", name, string(resp.Result))
	require.Len(h.t, result.Content, 1)
	return result
}

func (h *uciT001DispatcherHarness) callStatic(project muxcore.ProjectContext, name string, arguments map[string]any) map[string]any {
	h.t.Helper()
	result := h.callTool(project, name, arguments)
	var payload map[string]any
	require.NoError(h.t, json.Unmarshal(result.Content[0], &payload), "decode static %s response: %s", name, string(result.Content[0]))
	return payload
}

func (h *uciT001DispatcherHarness) indexAndWait(project muxcore.ProjectContext, root string) {
	h.t.Helper()
	started := h.callStatic(project, "codebase_index", map[string]any{"root": root})
	require.Equal(h.t, "started", started["status"])
	runID, ok := started["run_id"].(string)
	require.True(h.t, ok)
	require.NotEmpty(h.t, runID)

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		status := h.callStatic(project, "codebase_status", map[string]any{"project": project.ID})
		switch status["status"] {
		case "idle":
			require.Equal(h.t, runID, status["run_id"])
			return
		case "error":
			h.t.Fatalf("codebase index %s failed: %+v", runID, status)
		}
		time.Sleep(25 * time.Millisecond)
	}
	h.t.Fatalf("codebase index %s did not reach idle before deadline", runID)
}

func (h *uciT001DispatcherHarness) search(project muxcore.ProjectContext) uciT001SearchPayload {
	h.t.Helper()
	result := h.callTool(project, "codebase_search", map[string]any{
		"project": project.ID,
		"query":   uciT001Query,
		"limit":   10,
	})
	var proxyBlock uciT001TextBlock
	require.NoError(h.t, json.Unmarshal(result.Content[0], &proxyBlock))
	require.Equal(h.t, "text", proxyBlock.Type)
	require.NotEmpty(h.t, proxyBlock.Text)

	var serverResult struct {
		Content []uciT001TextBlock `json:"content"`
		IsError bool               `json:"isError"`
	}
	require.NoError(h.t, json.Unmarshal([]byte(proxyBlock.Text), &serverResult), "decode proxied MCP result: %s", proxyBlock.Text)
	require.False(h.t, serverResult.IsError)
	require.Len(h.t, serverResult.Content, 1)
	require.Equal(h.t, "text", serverResult.Content[0].Type)

	var payload uciT001SearchPayload
	require.NoError(h.t, json.Unmarshal([]byte(serverResult.Content[0].Text), &payload), "decode search payload: %s", serverResult.Content[0].Text)
	require.Equal(h.t, uciT001Query, payload.Query)
	require.Equal(h.t, project.ID, payload.Project)
	return payload
}

func TestUCITwoRealWorktreesRemainIsolated(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	t.Setenv(config.EnvServerURL, "")
	t.Setenv(config.EnvServerURLAlt, "")
	t.Setenv(config.EnvWorkstationToken, "")
	t.Setenv(config.EnvClientInstanceID, "")
	t.Setenv("ENGRAM_TLS_CA", "")

	store := uciT001OpenStore(t)
	fixture := uciT001NewGitFixture(t)
	uciT001CleanProjectRows(t, store, fixture.slug)
	require.NoError(t, dbgorm.UpsertProject(context.Background(), store.DB, fixture.slug, "", fixture.remote, "", "uci-t001"))
	t.Cleanup(func() {
		uciT001CleanProjectRows(t, store, fixture.slug)
	})

	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "uci-t001"})
	mcpServer.SetLegacyUnscopedCodeChunkStore(dbgorm.NewCodeChunkStore(store.DB))
	grpcServer, internalServer := grpcserver.New(&uciT001MCPAdapter{server: mcpServer}, nil)
	internalServer.SetDB(store.DB)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.GracefulStop()
		_ = listener.Close()
		select {
		case err := <-serveErr:
			if err != nil && err != grpc.ErrServerStopped {
				t.Logf("gRPC server stopped with error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Log("gRPC server did not report shutdown before cleanup deadline")
		}
	})
	serverURL := "http://" + listener.Addr().String()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := registry.New()
	core := engramcore.NewModule()
	require.NoError(t, reg.Register(core))
	require.NoError(t, reg.Register(codeintel.NewModule(core)))
	reg.Freeze()

	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	pipeline := lifecycle.New(reg, logger)
	moduleStorage := t.TempDir()
	require.NoError(t, pipeline.Start(context.Background(), func(name string) module.ModuleDeps {
		storageDir := filepath.Join(moduleStorage, name)
		require.NoError(t, os.MkdirAll(storageDir, 0o700))
		return module.ModuleDeps{
			Logger:     logger.With("module", name),
			DaemonCtx:  daemonCtx,
			StorageDir: storageDir,
			Lookup:     reg,
		}
	}))
	t.Cleanup(func() {
		cancelDaemon()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		require.NoError(t, pipeline.ShutdownAll(shutdownCtx))
	})

	dispatchHarness := &uciT001DispatcherHarness{
		t:          t,
		dispatcher: dispatcher.New(reg, logger),
	}
	projectEnv := func(sessionID string) map[string]string {
		return map[string]string{
			config.EnvServerURL:       serverURL,
			config.EnvClaudeSessionID: sessionID,
		}
	}
	projectA := muxcore.ProjectContext{ID: fixture.slug, Cwd: fixture.primary, Env: projectEnv("uci-t001-a")}
	projectB := muxcore.ProjectContext{ID: fixture.slug, Cwd: fixture.linked, Env: projectEnv("uci-t001-b")}
	projectC := muxcore.ProjectContext{ID: fixture.slug, Cwd: fixture.primary, Env: projectEnv("uci-t001-c")}

	dispatchHarness.initialize(projectA, "uci-t001-a")
	dispatchHarness.initialize(projectB, "uci-t001-b")
	for _, project := range []muxcore.ProjectContext{projectA, projectB} {
		tools := dispatchHarness.listTools(project)
		require.Contains(t, tools, "codebase_index")
		require.Contains(t, tools, "codebase_status")
		require.Contains(t, tools, "codebase_search")
	}

	dispatchHarness.indexAndWait(projectA, fixture.primary)
	uciT001RequireSearchResult(t, dispatchHarness.search(projectA), uciT001AlphaMarker, "AlphaCallee", uciT001BetaMarker)

	dispatchHarness.indexAndWait(projectB, fixture.linked)
	uciT001RequireSearchResult(t, dispatchHarness.search(projectB), uciT001BetaMarker, "BetaCallee", uciT001AlphaMarker)
	resultAAfterB := dispatchHarness.search(projectA)

	dispatchHarness.initialize(projectC, "uci-t001-c")
	toolsC := dispatchHarness.listTools(projectC)
	require.Contains(t, toolsC, "codebase_search")
	uciT001RequireSearchResult(t, dispatchHarness.search(projectB), uciT001BetaMarker, "BetaCallee", uciT001AlphaMarker)
	resultAAfterC := dispatchHarness.search(projectA)
	require.Equal(t, resultAAfterB, resultAAfterC, "attaching client C must not rewrite either existing client result")

	uciT001RequireSearchResult(t, resultAAfterC, uciT001AlphaMarker, "AlphaCallee", uciT001BetaMarker)
}

func uciT001OpenStore(t *testing.T) *dbgorm.Store {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("DATABASE_DSN"))
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping UCI real-worktree integration test")
	}
	lowerDSN := strings.ToLower(dsn)
	if !strings.Contains(lowerDSN, "test") || strings.Contains(lowerDSN, "prod") || strings.Contains(lowerDSN, "production") || strings.Contains(lowerDSN, "staging") {
		t.Skip("DATABASE_DSN does not identify a dedicated test database")
	}
	store, err := dbgorm.NewStore(dbgorm.Config{
		DSN:      dsn,
		MaxConns: 4,
		LogLevel: gormlogger.Silent,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	return store
}

func uciT001CleanProjectRows(t *testing.T, store *dbgorm.Store, projectID string) {
	t.Helper()
	require.NoError(t, store.DB.Exec("DELETE FROM code_chunks WHERE project_id = ?", projectID).Error)
	require.NoError(t, store.DB.Exec("DELETE FROM code_index_sessions WHERE project_id = ?", projectID).Error)
	require.NoError(t, store.DB.Exec("DELETE FROM projects WHERE id = ?", projectID).Error)
}

type uciT001GitFixture struct {
	primary string
	linked  string
	slug    string
	remote  string
}

func uciT001NewGitFixture(t *testing.T) uciT001GitFixture {
	t.Helper()
	root := t.TempDir()
	primary := filepath.Join(root, "primary repo")
	linked := filepath.Join(root, "linked repo")
	require.NoError(t, os.MkdirAll(filepath.Join(primary, "pkg"), 0o755))

	uciT001WriteFile(t, filepath.Join(primary, ".engram-project"), "{\"name\":\"uci-t001\"}\n")
	uciT001WriteFile(t, filepath.Join(primary, ".gitignore"), ".git\n")
	uciT001WriteFile(t, filepath.Join(primary, uciT001RelativePath), `package fixture

func SharedTarget() string { return BaselineCallee() }
func BaselineCallee() string { return "BASELINE_BODY_MARKER" }
`)

	uciT001RunGit(t, primary, "init")
	uciT001RunGit(t, primary, "config", "user.email", "uci-t001@example.test")
	uciT001RunGit(t, primary, "config", "user.name", "UCI T001")
	remote := "https://example.invalid/engram/uci-t001-" + uuid.NewString() + ".git"
	uciT001RunGit(t, primary, "remote", "add", "origin", remote)
	uciT001RunGit(t, primary, "add", ".engram-project", ".gitignore", uciT001RelativePath)
	uciT001RunGit(t, primary, "commit", "-m", "add shared UCI fixture")
	uciT001RunGit(t, primary, "worktree", "add", "--detach", linked, "HEAD")
	t.Cleanup(func() {
		cmd := exec.Command("git", "-C", primary, "worktree", "remove", "--force", linked)
		_ = cmd.Run()
	})

	primaryHead := uciT001RunGit(t, primary, "rev-parse", "HEAD")
	linkedHead := uciT001RunGit(t, linked, "rev-parse", "HEAD")
	require.Equal(t, primaryHead, linkedHead)
	linkedGit, err := os.Stat(filepath.Join(linked, ".git"))
	require.NoError(t, err)
	require.False(t, linkedGit.IsDir(), "linked worktree .git must be a pointer file")

	slugA, _, remoteA, err := proxy.ResolveProjectSlug(context.Background(), primary)
	require.NoError(t, err)
	slugB, _, remoteB, err := proxy.ResolveProjectSlug(context.Background(), linked)
	require.NoError(t, err)
	require.Equal(t, slugA, slugB)
	require.Equal(t, remoteA, remoteB)
	require.Equal(t, remote, remoteA)
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{8}$`), slugA)
	require.Empty(t, uciT001RunGit(t, primary, "rev-parse", "--show-prefix"))
	require.Empty(t, uciT001RunGit(t, linked, "rev-parse", "--show-prefix"))

	alphaBody := `package fixture

func SharedTarget() string { return AlphaCallee() }
func AlphaCallee() string { return "ALPHA_BODY_MARKER" }
`
	betaBody := `package fixture

func SharedTarget() string { return BetaCallee() }
func BetaCallee() string { return "BETA_BODY_MARKER" }
`
	uciT001WriteFile(t, filepath.Join(primary, uciT001RelativePath), alphaBody)
	uciT001WriteFile(t, filepath.Join(linked, uciT001RelativePath), betaBody)
	require.Contains(t, uciT001RunGit(t, primary, "status", "--short", "--", uciT001RelativePath), uciT001RelativePath)
	require.Contains(t, uciT001RunGit(t, linked, "status", "--short", "--", uciT001RelativePath), uciT001RelativePath)
	primaryBytes, err := os.ReadFile(filepath.Join(primary, uciT001RelativePath))
	require.NoError(t, err)
	linkedBytes, err := os.ReadFile(filepath.Join(linked, uciT001RelativePath))
	require.NoError(t, err)
	require.Contains(t, string(primaryBytes), uciT001AlphaMarker)
	require.Contains(t, string(linkedBytes), uciT001BetaMarker)
	require.NotEqual(t, string(primaryBytes), string(linkedBytes))

	return uciT001GitFixture{primary: primary, linked: linked, slug: slugA, remote: remoteA}
}

func uciT001RequireSearchResult(t *testing.T, payload uciT001SearchPayload, marker, callee, forbiddenMarker string) {
	t.Helper()
	require.Equal(t, 1, payload.Count)
	require.Len(t, payload.Results, 1)
	require.Equal(t, filepath.ToSlash(uciT001RelativePath), filepath.ToSlash(payload.Results[0].FilePath))
	require.Contains(t, payload.Results[0].Content, marker)
	require.Contains(t, payload.Results[0].Content, callee)
	require.NotContains(t, payload.Results[0].Content, forbiddenMarker)
}

func uciT001WriteFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func uciT001RunGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", root}, args...)
	cmd := exec.Command("git", cmdArgs...)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	return strings.TrimSpace(string(output))
}
