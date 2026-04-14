// Package main is the engram daemon — a muxcore-based MCP engine that
// translates MCP JSON-RPC to gRPC calls against the engram server.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/proxy"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"github.com/thebtf/mcp-mux/muxcore/engine"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

func main() {
	// Resolve project identity before engine starts.
	// The shim runs in the user's repo directory so "." is the project root.
	slug, remote, err := proxy.ResolveProjectSlug(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[engram] warning: git identity failed: %v\n", err)
	}
	if slug != "" {
		os.Setenv("ENGRAM_PROJECT", slug)
		fmt.Fprintf(os.Stderr, "[engram] project: %s\n", slug)
		if remote != "" {
			fmt.Fprintf(os.Stderr, "[engram] git remote: %s\n", safeRemoteURL(remote))
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	eng, err := engine.New(engine.Config{
		Name:       "engram",
		Persistent: true,
		Handler:    mcpHandler,
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

// mcpHandler is called by muxcore for each MCP session with per-session
// stdin/stdout. It reads config from env (forwarded by muxcore from the shim),
// dials the engram gRPC server, and runs the JSON-RPC ↔ gRPC translation loop.
func mcpHandler(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	serverURL := os.Getenv("ENGRAM_URL")
	if serverURL == "" {
		return fmt.Errorf("ENGRAM_URL not set")
	}
	token := os.Getenv("ENGRAM_API_TOKEN")
	project := os.Getenv("ENGRAM_PROJECT")

	grpcAddr, err := parseGRPCAddr(serverURL)
	if err != nil {
		return fmt.Errorf("parse ENGRAM_URL: %w", err)
	}

	conn, err := dialGRPC(grpcAddr, serverURL, token)
	if err != nil {
		return fmt.Errorf("gRPC connect: %w", err)
	}
	defer conn.Close()

	client := pb.NewEngramServiceClient(conn)

	return runTranslator(ctx, stdin, stdout, client, project)
}

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
// Translation loop
// ---------------------------------------------------------------------------

// runTranslator reads JSON-RPC lines from stdin, dispatches each to gRPC, and
// writes responses to stdout. It exits cleanly when ctx is cancelled or stdin
// reaches EOF.
func runTranslator(ctx context.Context, stdin io.Reader, stdout io.Writer, client pb.EngramServiceClient, project string) error {
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // 4 MB max line
	encoder := json.NewEncoder(stdout)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var req jsonrpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = encoder.Encode(jsonrpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &jsonrpcError{Code: -32700, Message: "Parse error"},
			})
			continue
		}

		resp := handleJSONRPC(ctx, client, &req, project)
		_ = encoder.Encode(resp)
	}

	return scanner.Err()
}

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
