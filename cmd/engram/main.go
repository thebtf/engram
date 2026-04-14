// Package main is the engram daemon — a muxcore-based MCP engine that
// translates MCP JSON-RPC to gRPC calls against the engram server.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thebtf/engram/internal/proxy"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"github.com/thebtf/mcp-mux/muxcore/engine"
	"github.com/thebtf/mcp-mux/muxcore/upgrade"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// engramHandler implements muxcore.SessionHandler and muxcore.ProjectLifecycle.
// It holds a pool of gRPC connections (keyed by address+TLS mode) and a cache
// of resolved project slugs (keyed by ProjectContext.ID).
type engramHandler struct {
	grpcConns sync.Map // connKey → *grpc.ClientConn
	slugCache sync.Map // ProjectContext.ID → string (resolved slug)
}

// connKey is the cache key for gRPC connections.
type connKey struct {
	addr    string
	tlsMode string // "custom-ca", "system-tls", "plaintext"
}

func main() {
	// Clean stale binaries from previous upgrades (.old.* files).
	if exePath, err := os.Executable(); err == nil {
		if cleaned := upgrade.CleanStale(exePath); cleaned > 0 {
			fmt.Fprintf(os.Stderr, "[engram] cleaned %d stale binary file(s)\n", cleaned)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	eng, err := engine.New(engine.Config{
		Name:           "engram",
		Persistent:     true,
		SessionHandler: &engramHandler{},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[engram] engine error: %v\n", err)
		os.Exit(1)
	}

	if err := eng.Run(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "[engram] fatal: %v\n", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// SessionHandler implementation
// ---------------------------------------------------------------------------

// HandleRequest processes one MCP JSON-RPC request per session.
// Implements muxcore.SessionHandler.
func (h *engramHandler) HandleRequest(ctx context.Context, p muxcore.ProjectContext, request []byte) ([]byte, error) {
	serverURL := envOrDefault(p.Env, "ENGRAM_URL")
	if serverURL == "" {
		return nil, fmt.Errorf("ENGRAM_URL not set")
	}
	token := envOrDefault(p.Env, "ENGRAM_API_TOKEN")
	project := h.resolveProject(p)

	conn, err := h.getOrDialGRPC(serverURL, token)
	if err != nil {
		return nil, fmt.Errorf("gRPC connect: %w", err)
	}

	client := pb.NewEngramServiceClient(conn)

	var req jsonrpcRequest
	if err := json.Unmarshal(request, &req); err != nil {
		resp := jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonrpcError{Code: -32700, Message: "Parse error"},
		}
		return json.Marshal(resp)
	}

	resp := handleJSONRPC(ctx, client, &req, project)
	return json.Marshal(resp)
}

// OnProjectConnect is called when a CC session connects.
// Implements muxcore.ProjectLifecycle.
func (h *engramHandler) OnProjectConnect(p muxcore.ProjectContext) {
	id, displayName, _, _ := proxy.ResolveProjectSlug(p.Cwd)
	if id == "" {
		id = p.ID
		displayName = filepath.Base(p.Cwd)
	}
	fmt.Fprintf(os.Stderr, "[engram] session connected: project=%s (%s), cwd=%s\n", displayName, id, p.Cwd)
}

// OnProjectDisconnect is called when a CC session disconnects.
// Implements muxcore.ProjectLifecycle.
func (h *engramHandler) OnProjectDisconnect(projectID string) {
	fmt.Fprintf(os.Stderr, "[engram] session disconnected: project=%s\n", projectID)
}

// ---------------------------------------------------------------------------
// Helper methods
// ---------------------------------------------------------------------------

// envOrDefault returns the value from the session env map if present,
// falling back to os.Getenv. Session env takes priority (per-project config).
func envOrDefault(env map[string]string, key string) string {
	if env != nil {
		if v, ok := env[key]; ok && v != "" {
			return v
		}
	}
	return os.Getenv(key)
}

// resolveProject returns the engram project ID for the given session.
// Result is cached per ProjectContext.ID to avoid repeated git operations.
func (h *engramHandler) resolveProject(p muxcore.ProjectContext) string {
	if cached, ok := h.slugCache.Load(p.ID); ok {
		return cached.(string)
	}
	id, displayName, remote, err := proxy.ResolveProjectSlug(p.Cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[engram] warning: project identity failed for %s: %v\n", p.Cwd, err)
		id = p.ID
		displayName = filepath.Base(p.Cwd)
	}
	if remote != "" {
		fmt.Fprintf(os.Stderr, "[engram] project: %s (%s, remote: %s)\n", displayName, id, safeRemoteURL(remote))
	} else {
		fmt.Fprintf(os.Stderr, "[engram] project: %s (%s)\n", displayName, id)
	}
	h.slugCache.Store(p.ID, id)
	return id
}

// getOrDialGRPC returns a pooled gRPC connection for the given server URL.
// Connections are created on first use and shared across sessions.
func (h *engramHandler) getOrDialGRPC(serverURL, token string) (*grpc.ClientConn, error) {
	grpcAddr, err := parseGRPCAddr(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}

	tlsMode := "plaintext"
	if os.Getenv("ENGRAM_TLS_CA") != "" {
		tlsMode = "custom-ca"
	} else if strings.HasPrefix(serverURL, "https") {
		tlsMode = "system-tls"
	}

	key := connKey{addr: grpcAddr, tlsMode: tlsMode}
	if existing, ok := h.grpcConns.Load(key); ok {
		return existing.(*grpc.ClientConn), nil
	}

	conn, err := dialGRPC(grpcAddr, serverURL, token)
	if err != nil {
		return nil, err
	}

	actual, loaded := h.grpcConns.LoadOrStore(key, conn)
	if loaded {
		// Another goroutine created the connection first — close ours.
		conn.Close()
		return actual.(*grpc.ClientConn), nil
	}
	return conn, nil
}

// ---------------------------------------------------------------------------
// gRPC transport helpers
// ---------------------------------------------------------------------------

// parseGRPCAddr extracts host:port from a URL.
// Example: "http://unleashed.lan:37777" → "unleashed.lan:37777".
func parseGRPCAddr(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		default:
			port = "37777" // default engram gRPC port
		}
	}
	return host + ":" + port, nil
}

// dialGRPC creates a gRPC client connection with keepalive and TLS settings.
//
// TLS is determined by the URL scheme:
//  1. ENGRAM_TLS_CA set → TLS with custom CA file (overrides scheme)
//  2. https:// → TLS with system CA pool
//  3. http:// or no scheme → plaintext (no TLS)
func dialGRPC(addr, serverURL, token string) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(16<<20), // 16 MB
			grpc.MaxCallSendMsgSize(16<<20),
		),
	}

	tlsCA := os.Getenv("ENGRAM_TLS_CA")

	switch {
	case tlsCA != "":
		creds, err := credentials.NewClientTLSFromFile(tlsCA, "")
		if err != nil {
			return nil, fmt.Errorf("load TLS CA: %w", err)
		}
		opts = append(opts, grpc.WithTransportCredentials(creds))
		fmt.Fprintf(os.Stderr, "[engram] gRPC: TLS with custom CA\n")

	case strings.HasPrefix(serverURL, "https"):
		creds := credentials.NewClientTLSFromCert(nil, "")
		opts = append(opts, grpc.WithTransportCredentials(creds))
		fmt.Fprintf(os.Stderr, "[engram] gRPC: TLS with system CA\n")

	default:
		// http:// or no scheme → plaintext
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		fmt.Fprintf(os.Stderr, "[engram] gRPC: plaintext\n")
	}

	if token != "" {
		opts = append(opts, grpc.WithUnaryInterceptor(tokenInterceptor(token)))
	}

	return grpc.NewClient(addr, opts...)
}

// tokenInterceptor injects the Bearer token into every outgoing RPC.
func tokenInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// ---------------------------------------------------------------------------
// MCP JSON-RPC types
// ---------------------------------------------------------------------------

// jsonrpcRequest is a minimal MCP JSON-RPC 2.0 request envelope.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonrpcResponse is a minimal MCP JSON-RPC 2.0 response envelope.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

// jsonrpcError is the standard JSON-RPC error object.
type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// JSON-RPC dispatch
// ---------------------------------------------------------------------------

// handleJSONRPC dispatches a single JSON-RPC request to the appropriate handler.
func handleJSONRPC(ctx context.Context, client pb.EngramServiceClient, req *jsonrpcRequest, project string) jsonrpcResponse {
	switch req.Method {
	case "initialize":
		return handleInitialize(ctx, client, req, project)
	case "tools/list":
		return handleToolsList(ctx, client, req, project)
	case "tools/call":
		return handleToolsCall(ctx, client, req, project)
	case "ping":
		return handlePing(ctx, client, req)
	case "notifications/initialized":
		// Client notification — acknowledge with empty result.
		return jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{}`)}
	default:
		return jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonrpcError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}
}

// handleInitialize handles the MCP initialize request by calling the gRPC
// Initialize RPC and returning the MCP capability negotiation response.
func handleInitialize(ctx context.Context, client pb.EngramServiceClient, req *jsonrpcRequest, project string) jsonrpcResponse {
	resp, err := client.Initialize(ctx, &pb.InitializeRequest{
		ClientName:    "engram-daemon",
		ClientVersion: "1.0.0",
		Project:       project,
	})
	if err != nil {
		return grpcErrorResponse(req.ID, err)
	}

	result := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    resp.ServerName,
			"version": resp.ServerVersion,
		},
	}
	resultJSON, _ := json.Marshal(result)
	return jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultJSON}
}

// handleToolsList handles the MCP tools/list request. It reuses the Initialize
// RPC since that returns the tool list alongside server info.
func handleToolsList(ctx context.Context, client pb.EngramServiceClient, req *jsonrpcRequest, project string) jsonrpcResponse {
	resp, err := client.Initialize(ctx, &pb.InitializeRequest{Project: project})
	if err != nil {
		return grpcErrorResponse(req.ID, err)
	}

	tools := make([]map[string]any, len(resp.Tools))
	for i, t := range resp.Tools {
		tool := map[string]any{
			"name":        t.Name,
			"description": t.Description,
		}
		if len(t.InputSchemaJson) > 0 {
			var schema any
			_ = json.Unmarshal(t.InputSchemaJson, &schema)
			tool["inputSchema"] = schema
		}
		tools[i] = tool
	}

	result := map[string]any{"tools": tools}
	resultJSON, _ := json.Marshal(result)
	return jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultJSON}
}

// handleToolsCall handles the MCP tools/call request by invoking the gRPC
// CallTool RPC and wrapping the result in the MCP content envelope.
func handleToolsCall(ctx context.Context, client pb.EngramServiceClient, req *jsonrpcRequest, project string) jsonrpcResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonrpcError{Code: -32602, Message: "invalid params"},
		}
	}

	resp, err := client.CallTool(ctx, &pb.CallToolRequest{
		ToolName:      params.Name,
		ArgumentsJson: params.Arguments,
		Project:       project,
	})
	if err != nil {
		return grpcErrorResponse(req.ID, err)
	}

	result := map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(resp.ContentJson)},
		},
		"isError": resp.IsError,
	}
	resultJSON, _ := json.Marshal(result)
	return jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: resultJSON}
}

// handlePing handles the MCP ping request via the gRPC Ping RPC.
func handlePing(ctx context.Context, client pb.EngramServiceClient, req *jsonrpcRequest) jsonrpcResponse {
	_, err := client.Ping(ctx, &pb.PingRequest{})
	if err != nil {
		return grpcErrorResponse(req.ID, err)
	}
	return jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{}`)}
}

// grpcErrorResponse wraps a gRPC error as a JSON-RPC internal error response.
func grpcErrorResponse(id json.RawMessage, err error) jsonrpcResponse {
	return jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonrpcError{Code: -32603, Message: err.Error()},
	}
}

// safeRemoteURL strips any embedded userinfo (e.g. tokens in
// https://token@host/path) before the URL is written to logs.
func safeRemoteURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.User = nil
	return u.String()
}
