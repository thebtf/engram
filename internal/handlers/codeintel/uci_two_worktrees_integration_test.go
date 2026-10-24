package codeintel_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/thebtf/engram/internal/auditcontext"
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
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/uci"
	"github.com/thebtf/engram/internal/worker"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"github.com/thebtf/mcp-mux/muxcore/control"
	"github.com/thebtf/mcp-mux/muxcore/daemon"
	"github.com/thebtf/mcp-mux/muxcore/owner"
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
	requestID := any(float64(1))
	correlationRequired := auditcontext.UCIRequestCorrelationRequired(ctx)
	if correlation, found := auditcontext.UCIRequestCorrelationFromContext(ctx); found {
		if jsonID, valid := correlation.JSONRPCID(); valid {
			requestID = jsonID
		} else if correlationRequired {
			return nil, false, fmt.Errorf("UCI request correlation is required")
		}
	} else if correlationRequired {
		return nil, false, fmt.Errorf("UCI request correlation is required")
	}
	paramsJSON, err := json.Marshal(map[string]any{
		"name":      toolName,
		"arguments": json.RawMessage(argsJSON),
	})
	if err != nil {
		return nil, false, fmt.Errorf("marshal tool call params: %w", err)
	}
	resp := a.server.HandleRequest(ctx, &mcp.Request{
		JSONRPC: "2.0",
		ID:      requestID,
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
	ID     int              `json:"id"`
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

type uciT001Context struct {
	ContextHandle     string `json:"context_handle"`
	SourceID          string `json:"source_id"`
	CheckoutID        string `json:"checkout_id"`
	IncarnationID     string `json:"incarnation_id"`
	AnalysisProfileID string `json:"analysis_profile_id"`
}

type uciT001Client struct {
	t       *testing.T
	encoder *json.Encoder
	decoder *json.Decoder
	nextID  int
}

func uciT001SpawnClient(t *testing.T, controlPath, root, serverURL string) *uciT001Client {
	t.Helper()
	reply, err := control.SendWithTimeout(controlPath, control.Request{
		Cmd: "spawn", Command: "uci-t001", Mode: "global", Cwd: root,
		Env: map[string]string{
			config.EnvServerURL:       serverURL,
			config.EnvClaudeSessionID: "uci-t001-shared-host-session",
		},
	}, 5*time.Second)
	require.NoError(t, err)
	require.True(t, reply.OK, "control spawn failed: %s", reply.Message)
	require.NotEmpty(t, reply.IPCPath)
	require.NotEmpty(t, reply.ServerID)
	require.NotEmpty(t, reply.Token)
	stdin, input := io.Pipe()
	output, stdout := io.Pipe()
	done := make(chan error, 1)
	t.Cleanup(func() {
		_ = input.CloseWithError(context.Canceled)
		_ = output.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("close admitted UCI client: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("admitted UCI client did not stop before cleanup deadline")
		}
		_ = stdin.Close()
		_ = stdout.Close()
	})
	go func() {
		err := owner.RunResilientClient(owner.ResilientClientConfig{
			Stdin: stdin, Stdout: stdout, InitialIPCPath: reply.IPCPath, Token: reply.Token,
			Logger: log.New(io.Discard, "", 0),
		})
		_ = stdout.CloseWithError(err)
		done <- err
	}()
	return &uciT001Client{t: t, encoder: json.NewEncoder(input), decoder: json.NewDecoder(output)}
}

func (client *uciT001Client) request(deadline time.Time, method string, params any) uciT001RPCResponse {
	client.t.Helper()
	client.nextID++
	id := client.nextID
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	type exchange struct {
		response uciT001RPCResponse
		err      error
	}
	done := make(chan exchange, 1)
	go func() {
		var result exchange
		result.err = client.encoder.Encode(req)
		if result.err == nil {
			result.err = client.decoder.Decode(&result.response)
		}
		done <- result
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case result := <-done:
		require.NoError(client.t, result.err)
		require.Equal(client.t, id, result.response.ID)
		require.True(client.t, time.Now().Before(deadline), "UCI request exceeded its overall deadline")
		return result.response
	case <-timer.C:
		client.t.Fatalf("UCI %s request did not finish before deadline", method)
		return uciT001RPCResponse{}
	}
}

func (client *uciT001Client) initialize(name string) {
	client.t.Helper()
	resp := client.request(time.Now().Add(20*time.Second), "initialize", map[string]any{
		"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": name, "version": "t001"},
	})
	require.Nilf(client.t, resp.Error, "JSON-RPC error: %+v", resp.Error)
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	require.NoError(client.t, json.Unmarshal(resp.Result, &result))
	require.Equal(client.t, "2024-11-05", result.ProtocolVersion)
}

func (client *uciT001Client) listTools() []string {
	client.t.Helper()
	resp := client.request(time.Now().Add(20*time.Second), "tools/list", nil)
	require.Nilf(client.t, resp.Error, "JSON-RPC error: %+v", resp.Error)
	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	require.NoError(client.t, json.Unmarshal(resp.Result, &result))
	names := make([]string, len(result.Tools))
	for i, tool := range result.Tools {
		names[i] = tool.Name
	}
	return names
}

func (client *uciT001Client) callTool(deadline time.Time, name string, arguments map[string]any) uciT001ToolResult {
	client.t.Helper()
	resp := client.request(deadline, "tools/call", map[string]any{"name": name, "arguments": arguments})
	require.Nilf(client.t, resp.Error, "JSON-RPC error: %+v", resp.Error)
	require.NotEmpty(client.t, resp.Result)
	var result uciT001ToolResult
	require.NoError(client.t, json.Unmarshal(resp.Result, &result))
	require.Falsef(client.t, result.IsError, "tool %s returned isError: %s", name, string(resp.Result))
	require.Len(client.t, result.Content, 1)
	return result
}

func (client *uciT001Client) proxyPayload(name string, arguments map[string]any, payload any) {
	client.t.Helper()
	result := client.callTool(time.Now().Add(20*time.Second), name, arguments)
	var block uciT001TextBlock
	require.NoError(client.t, json.Unmarshal(result.Content[0], &block))
	require.Equal(client.t, "text", block.Type)
	var serverResult struct {
		Content []uciT001TextBlock `json:"content"`
		IsError bool               `json:"isError"`
	}
	require.NoError(client.t, json.Unmarshal([]byte(block.Text), &serverResult))
	require.False(client.t, serverResult.IsError)
	require.Len(client.t, serverResult.Content, 1)
	require.Equal(client.t, "text", serverResult.Content[0].Type)
	require.NoError(client.t, json.Unmarshal([]byte(serverResult.Content[0].Text), payload))
}

func (client *uciT001Client) register(arguments map[string]any) uciT001Context {
	client.t.Helper()
	arguments["action"] = "register"
	var registered uciT001Context
	client.proxyPayload("codebase_context", arguments, &registered)
	require.NotEmpty(client.t, registered.ContextHandle)
	for _, id := range []string{registered.SourceID, registered.CheckoutID, registered.IncarnationID, registered.AnalysisProfileID} {
		parsed, err := uuid.Parse(id)
		require.NoError(client.t, err)
		require.NotEqual(client.t, uuid.Nil, parsed)
		require.Equal(client.t, id, parsed.String())
	}
	return registered
}

func (client *uciT001Client) indexAndWait(registered uciT001Context, root string) (string, uci.QueryContextRef) {
	client.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	started := client.callTool(deadline, "codebase_index", map[string]any{"context_handle": registered.ContextHandle, "root": root})
	var run struct {
		Status string `json:"status"`
		RunID  string `json:"run_id"`
	}
	require.NoError(client.t, json.Unmarshal(started.Content[0], &run))
	require.Equal(client.t, "started", run.Status)
	require.NotEmpty(client.t, run.RunID)
	for time.Now().Before(deadline) {
		waitMS := min(int64(250), time.Until(deadline).Milliseconds())
		if waitMS < 1 {
			break
		}
		result := client.callTool(deadline, "codebase_status", map[string]any{
			"context_handle": registered.ContextHandle,
			"after_barrier":  map[string]any{"token": run.RunID, "wait_ms": waitMS},
		})
		var status struct {
			Status                string               `json:"status"`
			RunID                 string               `json:"run_id"`
			ServerCountsAvailable bool                 `json:"server_counts_available"`
			Context               *uci.QueryContextRef `json:"context"`
			Freshness             uci.QueryFreshness   `json:"freshness"`
		}
		require.NoError(client.t, json.Unmarshal(result.Content[0], &status))
		require.Equal(client.t, run.RunID, status.RunID)
		switch status.Status {
		case "idle":
			require.True(client.t, status.ServerCountsAvailable)
			require.NotNil(client.t, status.Context)
			require.Equal(client.t, registered.SourceID, status.Context.SourceID)
			require.Equal(client.t, registered.CheckoutID, status.Context.CheckoutID)
			require.Equal(client.t, registered.AnalysisProfileID, status.Context.ProfileID)
			require.NotEmpty(client.t, status.Context.ViewID)
			require.Positive(client.t, status.Context.Generation)
			require.NotNil(client.t, status.Freshness.Barrier)
			require.Equal(client.t, uci.QueryBarrierSatisfied, status.Freshness.Barrier.State)
			return run.RunID, *status.Context
		case "running":
			time.Sleep(min(25*time.Millisecond, time.Until(deadline)))
		default:
			client.t.Fatalf("codebase index %s failed: %s", run.RunID, string(result.Content[0]))
		}
	}
	client.t.Fatalf("codebase index %s did not reach idle before 20-second deadline", run.RunID)
	return "", uci.QueryContextRef{}
}

func (client *uciT001Client) query(name string, arguments map[string]any) uci.QueryResponse {
	client.t.Helper()
	var payload uci.QueryResponse
	client.proxyPayload(name, arguments, &payload)
	require.NoError(client.t, payload.Validate())
	return payload
}

func (client *uciT001Client) search(handle, query string) uci.QueryResponse {
	client.t.Helper()
	return client.query("codebase_search", map[string]any{"context_handle": handle, "query": query, "limit": 10})
}

func (client *uciT001Client) requireDenied(name string, arguments map[string]any, reason string) {
	client.t.Helper()
	resp := client.request(time.Now().Add(20*time.Second), "tools/call", map[string]any{"name": name, "arguments": arguments})
	if resp.Error == nil {
		var result uciT001ToolResult
		require.NoError(client.t, json.Unmarshal(resp.Result, &result))
		require.Truef(client.t, result.IsError, "tool %s must be denied: %s", name, resp.Result)
		require.Len(client.t, result.Content, 1)
		var block uciT001TextBlock
		require.NoError(client.t, json.Unmarshal(result.Content[0], &block))
		require.Contains(client.t, block.Text, reason)
		require.NotContains(client.t, block.Text, uciT001AlphaMarker)
		require.NotContains(client.t, block.Text, uciT001BetaMarker)
		return
	}
	require.NotNil(client.t, resp.Error)
	require.Equal(client.t, -32603, resp.Error.Code)
	require.Contains(client.t, resp.Error.Message, reason)
	require.Empty(client.t, resp.Result)
	require.NotContains(client.t, resp.Error.Message, uciT001AlphaMarker)
	require.NotContains(client.t, resp.Error.Message, uciT001BetaMarker)
}

func uciT001RequireContextDenial(t *testing.T, response uci.QueryResponse) {
	t.Helper()
	require.Equal(t, uci.QueryStatusContextRequired, response.Status)
	require.NotNil(t, response.Error)
	require.Equal(t, uci.QueryErrorContextMismatch, response.Error.Code)
	require.Nil(t, response.Contexts)
	require.Nil(t, response.Items)
	require.Nil(t, response.Graph)
	require.Nil(t, response.Exposure)
	require.Nil(t, response.Freshness)
	require.Nil(t, response.Retrieval)
	require.Nil(t, response.Coverage)
	require.Nil(t, response.Warnings)
	require.Nil(t, response.Continuation)
	require.Nil(t, response.Truncated)
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
	project, err := projectidentity.InitLogicalProject(t.Context(), fixture.primary, "Two-worktree fixture")
	require.NoError(t, err)
	require.NoError(t, store.DB.Exec("INSERT INTO projects (id, display_name) VALUES (?, ?)", project.ProjectID, project.Name).Error)
	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "uci-t001"})
	contexts := dbgorm.NewUCIContextStore(store.DB)
	authorizer := dbgorm.NewUCIContextAuthorizer(contexts)
	resolver := uci.NewContextResolver(contexts, authorizer, contexts)
	contextApplication, err := mcp.NewUCIContextApplication(resolver, contexts)
	require.NoError(t, err)
	contextApplication.SetLocalGitRegistration(func(ctx context.Context, caller uci.ResolveContextInput, sourceID, label, locator string, parserBundle *bool) (uci.RegisteredCheckoutSelector, error) {
		registered, err := contexts.RegisterLocalGit(ctx, dbgorm.RegisterLocalGitInput{
			ProjectID: mcp.ProjectFromContext(ctx),
			AuthRealm: caller.AuthRealm, Principal: caller.Principal, WorkstationID: caller.WorkstationID,
			SourceID: sourceID, SourceLabel: label, Locator: locator, ParserBundle: parserBundle,
		})
		if err != nil {
			return uci.RegisteredCheckoutSelector{}, err
		}
		return uci.RegisteredCheckoutSelector{
			Scope:     uci.IndexScope{SourceID: registered.SourceID, CheckoutID: registered.CheckoutID, IncarnationID: registered.IncarnationID},
			ProfileID: registered.ProfileID,
		}, nil
	})
	projections := dbgorm.NewUCIProjectionStore(store.DB)
	intents := dbgorm.NewUCIIndexIntentStore(store.DB)
	publisher, err := projections.Publisher(authorizer, uci.IndexPublicationConfig{Limits: uci.DefaultIndexPublicationLimits()})
	require.NoError(t, err)
	indexTargets := grpcserver.NewIndexIntentTargetRegistry()
	indexRuntime, err := grpcserver.NewContextAwareUCIRuntimeWithIndexTargets(contexts, projections, publisher, intents, indexTargets)
	require.NoError(t, err)
	handlePort, err := mcp.NewUCIContextHandlePort(mcpServer, contexts, authorizer)
	require.NoError(t, err)
	aliases := uci.NewAliasResolver(contexts.LookupLegacyAliasRecords)
	application, err := worker.NewUCIApplication(contextApplication, aliases, uci.NewQueryService(projections), nil,
		uci.NewGraphService(projections), uci.NewVersionedReadService(projections), uci.NewIndexStatusService(projections, nil))
	require.NoError(t, err)
	mcpServer.SetCodebaseContextApplication(application)
	mcpServer.SetUCIExposureRecorder(uci.NewExposureRecorder(dbgorm.NewUCIExposureStore(store.DB), nil))
	grpcServer, internalServer := grpcserver.New(&uciT001MCPAdapter{server: mcpServer}, nil)
	internalServer.SetDB(store.DB)
	internalServer.SetUCITransport(grpcserver.NewContextAwareUCITransport(resolver, aliases, indexRuntime, handlePort))
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
	clientInstanceID := uuid.NewString()
	core := engramcore.NewModuleWithClientInstanceID(clientInstanceID)
	goProfile := uci.GoExtractionProfile{ProfileKey: "go-structure-v1", ParserKey: "go-parser-v1"}
	artifactProfile, err := uci.GoIndexAdmissionArtifactProfile(goProfile)
	require.NoError(t, err)
	codeModule, err := codeintel.NewModuleWithRuntimeConfig(core, codeintel.UCIRuntimeConfig{
		ClientInstanceID: clientInstanceID, GoProfile: goProfile, ParserBundleDigest: artifactProfile.ExtractionProfileDigest,
	})
	require.NoError(t, err)
	require.NoError(t, reg.Register(core))
	require.NoError(t, reg.Register(codeModule))
	reg.Freeze()

	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	pipeline := lifecycle.New(reg, logger)
	moduleStorage := t.TempDir()
	t.Cleanup(func() {
		cancelDaemon()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := pipeline.ShutdownAll(shutdownCtx); err != nil {
			t.Errorf("close UCI modules: %v", err)
		}
	})
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
	controlPath := filepath.Join(t.TempDir(), "uci.ctl.sock")
	nativeDaemon, err := daemon.New(daemon.Config{
		Name: "uci-t001", Namespace: "uci-t001-" + uuid.NewString(), ControlPath: controlPath,
		SkipSnapshot: true, Persistent: true, Registry: nil,
		SessionHandler: dispatcher.New(reg, logger), Logger: log.New(io.Discard, "", 0),
		AuthorizeSession: func(context.Context, muxcore.ConnInfo, muxcore.ProjectContext) muxcore.SessionAuth {
			var entropy [32]byte
			if _, err := rand.Read(entropy[:]); err != nil {
				return muxcore.SessionAuth{Decision: muxcore.AuthDeny, Reason: "uci_transport_session_unavailable"}
			}
			tag := base64.RawURLEncoding.EncodeToString(entropy[:])
			if !auditcontext.ValidUCITransportSession(tag) {
				return muxcore.SessionAuth{Decision: muxcore.AuthDeny, Reason: "uci_transport_session_unavailable"}
			}
			return muxcore.SessionAuth{Decision: muxcore.AuthAllow, TenantID: tag}
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		nativeDaemon.Shutdown()
		require.Zero(t, nativeDaemon.OwnerCount(), "owned daemon must retire every admitted owner")
	})

	unboundRoot := t.TempDir()
	uciT001RunGit(t, unboundRoot, "init", "--quiet", "--template=")
	unbound := uciT001SpawnClient(t, controlPath, unboundRoot, serverURL)
	unbound.initialize("uci-t001-unbound")
	var listed struct {
		Contexts []json.RawMessage `json:"contexts"`
	}
	unbound.proxyPayload("codebase_context", map[string]any{"action": "list"}, &listed)
	require.Empty(t, listed.Contexts)
	unbound.requireDenied("codebase_context", map[string]any{"action": "resolve"}, "CONTEXT_REQUIRED")
	unbound.requireDenied("codebase_context", map[string]any{"action": "select", "context_handle": "unowned-selection"}, "CONTEXT_MISMATCH")
	for _, path := range []string{filepath.Join(unboundRoot, ".engram-project"), filepath.Join(unboundRoot, ".git", "engram-project.json")} {
		_, err := os.Lstat(path)
		require.True(t, os.IsNotExist(err), "project-free query must not create %s", path)
	}
	t.Log("unbound native list/resolve reached real UCI without creating project identity")

	clientA := uciT001SpawnClient(t, controlPath, fixture.primary, serverURL)
	clientA.initialize("uci-t001-a")
	registeredA := clientA.register(map[string]any{"source_label": "uci-t001"})
	var storedSource dbgorm.UCISource
	require.NoError(t, store.DB.First(&storedSource, "source_id = ?", registeredA.SourceID).Error)
	require.NotNil(t, storedSource.ProjectID)
	require.Equal(t, project.ProjectID, *storedSource.ProjectID, "native registration must persist its canonical Project-to-Source binding")
	var storedCheckout dbgorm.UCICheckout
	require.NoError(t, store.DB.First(&storedCheckout, "checkout_id = ?", registeredA.CheckoutID).Error)
	require.Equal(t, registeredA.SourceID, storedCheckout.SourceID)
	require.Equal(t, uciT001Locator(t, fixture.primary), storedCheckout.LocatorRef, "omitted locator must select the admitted native caller checkout")
	require.Equal(t, uci.NoAuthCodePrincipal, storedCheckout.OwnerPrincipal)
	require.Nil(t, storedCheckout.CurrentViewID, "registration must not invent a View")
	clientB := uciT001SpawnClient(t, controlPath, fixture.linked, serverURL)
	clientB.initialize("uci-t001-b")
	registeredB := clientB.register(map[string]any{"source_id": registeredA.SourceID, "locator": uciT001Locator(t, fixture.linked)})
	require.Equal(t, registeredA.SourceID, registeredB.SourceID)
	require.NotEqual(t, registeredA.CheckoutID, registeredB.CheckoutID)
	require.NotEqual(t, registeredA.ContextHandle, registeredB.ContextHandle)
	for _, client := range []*uciT001Client{clientA, clientB} {
		tools := client.listTools()
		for _, name := range []string{"codebase_context", "codebase_index", "codebase_status", "codebase_search", "codebase_read"} {
			require.Contains(t, tools, name)
		}
	}
	barrierA, viewA := clientA.indexAndWait(registeredA, fixture.primary)
	proofA := uciT001RequireSearchResult(t, clientA, registeredA.ContextHandle, viewA, uciT001AlphaMarker, "AlphaCallee", uciT001BetaMarker)
	_, viewB := clientB.indexAndWait(registeredB, fixture.linked)
	require.NotEqual(t, viewA.ViewID, viewB.ViewID)
	proofB := uciT001RequireSearchResult(t, clientB, registeredB.ContextHandle, viewB, uciT001BetaMarker, "BetaCallee", uciT001AlphaMarker)
	require.Equal(t, proofA, uciT001RequireSearchResult(t, clientA, registeredA.ContextHandle, viewA, uciT001AlphaMarker, "AlphaCallee", uciT001BetaMarker), "B publication must not rewrite A citations or body")

	clientC := uciT001SpawnClient(t, controlPath, fixture.primary, serverURL)
	clientC.initialize("uci-t001-c")
	require.Contains(t, clientC.listTools(), "codebase_search")
	registeredC := clientC.register(map[string]any{"source_id": registeredA.SourceID, "locator": uciT001Locator(t, fixture.primary)})
	require.Equal(t, registeredA.SourceID, registeredC.SourceID)
	require.Equal(t, registeredA.CheckoutID, registeredC.CheckoutID)
	require.NotEqual(t, registeredA.ContextHandle, registeredC.ContextHandle)
	require.Equal(t, proofA, uciT001RequireSearchResult(t, clientC, registeredC.ContextHandle, viewA, uciT001AlphaMarker, "AlphaCallee", uciT001BetaMarker))
	for _, client := range []*uciT001Client{clientB, clientC} {
		client.requireDenied("codebase_index", map[string]any{"context_handle": registeredA.ContextHandle}, "CONTEXT_MISMATCH")
		client.requireDenied("codebase_status", map[string]any{
			"context_handle": registeredA.ContextHandle, "after_barrier": map[string]any{"token": barrierA, "wait_ms": 1},
		}, "CONTEXT_MISMATCH")
		uciT001RequireContextDenial(t, client.search(registeredA.ContextHandle, uciT001Query))
		uciT001RequireContextDenial(t, client.query("codebase_read", uciT001ReadArgs(registeredA.ContextHandle, proofA.Callee)))
	}
	clientB.requireDenied("codebase_status", map[string]any{
		"context_handle": registeredB.ContextHandle, "after_barrier": map[string]any{"token": barrierA, "wait_ms": 1},
	}, "codebase_status: after_barrier token is stale for this target")
	require.Equal(t, proofA, uciT001RequireSearchResult(t, clientA, registeredA.ContextHandle, viewA, uciT001AlphaMarker, "AlphaCallee", uciT001BetaMarker), "C admission and denied foreign requests must not rewrite A")
	require.Equal(t, proofB, uciT001RequireSearchResult(t, clientB, registeredB.ContextHandle, viewB, uciT001BetaMarker, "BetaCallee", uciT001AlphaMarker), "C admission and denied foreign requests must not rewrite B")

	// Replacing the application invalidates the registry epoch; handles have no clock TTL.
	mcpServer.SetCodebaseContextApplication(application)
	clientA.requireDenied("codebase_status", map[string]any{
		"context_handle": registeredA.ContextHandle, "after_barrier": map[string]any{"token": barrierA, "wait_ms": 1},
	}, "CONTEXT_MISMATCH")
	uciT001RequireContextDenial(t, clientA.search(registeredA.ContextHandle, uciT001Query))
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
	parsedDSN, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, parsedDSN.Scheme, "DATABASE_DSN must be a PostgreSQL URL")
	adminDB, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	adminSQL, err := adminDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := adminSQL.Close(); err != nil {
			t.Errorf("close UCI admin pool: %v", err)
		}
	})
	require.NoError(t, adminDB.Exec(`CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public`).Error)
	schema := "uci_t001_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, adminDB.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		if err := adminDB.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop isolated UCI schema: %v", err)
		}
	})
	query := parsedDSN.Query()
	query.Set("search_path", schema+", public")
	parsedDSN.RawQuery = query.Encode()
	store, err := dbgorm.NewStore(dbgorm.Config{DSN: parsedDSN.String(), MaxConns: 4, LogLevel: gormlogger.Silent})
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close isolated UCI store: %v", err)
		}
	})
	var actualSchema string
	require.NoError(t, store.DB.Raw(`SELECT current_schema()`).Scan(&actualSchema).Error)
	require.Equal(t, schema, actualSchema)
	return store
}

type uciT001GitFixture struct {
	primary string
	linked  string
}

func uciT001NewGitFixture(t *testing.T) uciT001GitFixture {
	t.Helper()
	root := t.TempDir()
	primary := filepath.Join(root, "primary repo")
	linked := filepath.Join(root, "linked repo")
	require.NoError(t, os.MkdirAll(filepath.Join(primary, "pkg"), 0o755))

	uciT001WriteFile(t, filepath.Join(primary, uciT001RelativePath), `package fixture

func SharedTarget() string { return BaselineCallee() }
func BaselineCallee() string { return "BASELINE_BODY_MARKER" }
`)

	uciT001RunGit(t, primary, "init")
	uciT001RunGit(t, primary, "config", "user.email", "uci-t001@example.test")
	uciT001RunGit(t, primary, "config", "user.name", "UCI T001")
	remote := "https://example.invalid/engram/uci-t001-" + uuid.NewString() + ".git"
	uciT001RunGit(t, primary, "remote", "add", "origin", remote)
	uciT001RunGit(t, primary, "add", uciT001RelativePath)
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

	require.Equal(t, remote, uciT001RunGit(t, primary, "remote", "get-url", "origin"))
	require.Equal(t, remote, uciT001RunGit(t, linked, "remote", "get-url", "origin"))
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

	return uciT001GitFixture{primary: primary, linked: linked}
}

func uciT001Locator(t *testing.T, root string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	canonical, err = filepath.Abs(canonical)
	require.NoError(t, err)
	path := filepath.ToSlash(canonical)
	if runtime.GOOS == "windows" {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

type uciT001SearchProof struct {
	Target uci.QueryItem
	Callee uci.QueryItem
}

func uciT001ReadArgs(handle string, item uci.QueryItem) map[string]any {
	return map[string]any{
		"context_handle": handle, "ref": item.Ref, "membership_id": item.MembershipID,
		"span": item.Span, "content_digest": item.ContentDigest,
	}
}

func uciT001RequireOwnView(t *testing.T, payload uci.QueryResponse, view uci.QueryContextRef) {
	t.Helper()
	require.NotNil(t, payload.Contexts)
	require.Equal(t, uci.QueryContexts{view}, *payload.Contexts)
	require.NotNil(t, payload.Items)
	for _, item := range *payload.Items {
		require.Equal(t, view.SourceID, item.Ref.SourceID)
		require.Equal(t, view.ViewID, item.Ref.ViewID)
		require.Equal(t, filepath.ToSlash(uciT001RelativePath), item.Path)
		require.NotEmpty(t, item.Ref.EntityKey)
		require.NotEmpty(t, item.MembershipID)
		require.NotEmpty(t, item.ContentDigest)
		require.NoError(t, item.Span.Validate())
	}
}

func uciT001ReadItem(t *testing.T, client *uciT001Client, handle string, view uci.QueryContextRef, item uci.QueryItem) uci.QueryItem {
	t.Helper()
	read := client.query("codebase_read", uciT001ReadArgs(handle, item))
	if read.Status != uci.QueryStatusOK {
		diagnostic, err := json.Marshal(map[string]any{
			"status": read.Status, "warnings": read.Warnings, "retrieval": read.Retrieval,
			"coverage": read.Coverage, "error": read.Error, "response_contexts": read.Contexts,
			"request_view": view, "request_ref": item.Ref, "membership_id": item.MembershipID,
			"span": item.Span, "content_digest": item.ContentDigest,
		})
		require.NoError(t, err)
		t.Logf("UCI_READ_DIAGNOSTIC %s", diagnostic)
	}
	require.Equal(t, uci.QueryStatusOK, read.Status)
	uciT001RequireOwnView(t, read, view)
	require.Len(t, *read.Items, 1)
	body := (*read.Items)[0]
	require.Equal(t, item.Ref, body.Ref)
	require.Equal(t, item.MembershipID, body.MembershipID)
	require.Equal(t, item.Path, body.Path)
	require.Equal(t, item.Span, body.Span)
	require.Equal(t, item.ContentDigest, body.ContentDigest)
	require.Equal(t, body.Span.ByteEnd-body.Span.ByteStart, int64(len(body.Excerpt)))
	require.Equal(t, item.Excerpt, body.Excerpt)
	return body
}

func uciT001RequireSearchResult(t *testing.T, client *uciT001Client, handle string, view uci.QueryContextRef, marker, callee, forbiddenMarker string) uciT001SearchProof {
	t.Helper()
	payload := client.search(handle, uciT001Query)
	require.Equal(t, uci.QueryStatusOK, payload.Status)
	uciT001RequireOwnView(t, payload, view)
	var targets []uci.QueryItem
	var fullFiles []uci.QueryItem
	for _, item := range *payload.Items {
		require.NotContains(t, item.Excerpt, forbiddenMarker)
		if item.Span.ByteStart == 0 {
			require.Equal(t, uci.QueryItemCode, item.Kind)
			require.Equal(t, "go", item.Language)
			require.Contains(t, item.MatchSources, uci.QueryMatchFTS)
			fullFiles = append(fullFiles, item)
		}
		if item.Ref.EntityKey == "go:fixture/func:SharedTarget" {
			require.Equal(t, uci.QueryItemCode, item.Kind)
			require.Equal(t, "go", item.Language)
			targets = append(targets, item)
		}
	}
	require.Len(t, targets, 1, "search must return the unique SharedTarget function entity")
	require.Len(t, fullFiles, 1, "search must return one full-file FTS citation")
	fullFile := uciT001ReadItem(t, client, handle, view, fullFiles[0])
	require.Zero(t, fullFile.Span.ByteStart)
	digest := sha256.Sum256([]byte(fullFile.Excerpt))
	require.Equal(t, string(fullFile.ContentDigest), hex.EncodeToString(digest[:]))
	require.Contains(t, fullFile.Excerpt, marker)
	require.NotContains(t, fullFile.Excerpt, forbiddenMarker)
	target := targets[0]
	require.Contains(t, target.Excerpt, "func SharedTarget()")
	require.Contains(t, target.Excerpt, callee)
	require.NotContains(t, target.Excerpt, forbiddenMarker)
	target = uciT001ReadItem(t, client, handle, view, target)
	require.Equal(t, fullFile.ContentDigest, target.ContentDigest)
	require.LessOrEqual(t, target.Span.ByteEnd, int64(len(fullFile.Excerpt)))
	require.Equal(t, fullFile.Excerpt[target.Span.ByteStart:target.Span.ByteEnd], target.Excerpt)
	require.Contains(t, target.Excerpt, callee)
	require.NotContains(t, target.Excerpt, forbiddenMarker)

	callees := client.search(handle, callee)
	require.Equal(t, uci.QueryStatusOK, callees.Status)
	uciT001RequireOwnView(t, callees, view)
	var matches []uci.QueryItem
	for _, item := range *callees.Items {
		require.NotContains(t, item.Excerpt, forbiddenMarker)
		if item.Ref.EntityKey == "go:fixture/func:"+callee {
			require.Equal(t, uci.QueryItemCode, item.Kind)
			require.Equal(t, "go", item.Language)
			matches = append(matches, item)
		}
	}
	require.Len(t, matches, 1, "callee search must return its own exact indexed span")
	body := uciT001ReadItem(t, client, handle, view, matches[0])
	require.Equal(t, fullFile.ContentDigest, body.ContentDigest)
	require.LessOrEqual(t, body.Span.ByteEnd, int64(len(fullFile.Excerpt)))
	require.Equal(t, fullFile.Excerpt[body.Span.ByteStart:body.Span.ByteEnd], body.Excerpt)
	require.Contains(t, body.Excerpt, marker)
	require.Contains(t, body.Excerpt, callee)
	require.NotContains(t, body.Excerpt, forbiddenMarker)
	forbidden := client.search(handle, forbiddenMarker)
	require.Equal(t, uci.QueryStatusEmpty, forbidden.Status)
	uciT001RequireOwnView(t, forbidden, view)
	require.Empty(t, *forbidden.Items, "foreign body marker must not be searchable in this View")
	return uciT001SearchProof{Target: target, Callee: body}
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
