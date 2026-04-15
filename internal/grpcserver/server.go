package grpcserver

import (
	"context"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/worker/projectevents"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

// MCPHandler handles MCP JSON-RPC requests.
// Implement this interface with a thin adapter over mcp.Server to avoid
// direct coupling between grpcserver and the MCP request/response types.
type MCPHandler interface {
	// HandleToolCall processes a tool call and returns the JSON result.
	HandleToolCall(ctx context.Context, toolName string, argsJSON []byte) (resultJSON []byte, isError bool, err error)
	// ToolDefinitions returns the list of available tools.
	ToolDefinitions() []ToolDef
	// ServerInfo returns the server name and version.
	ServerInfo() (name, version string)
}

// ToolDef describes a single tool for the Initialize response.
type ToolDef struct {
	Name            string
	Description     string
	InputSchemaJSON []byte
}

// Server implements the EngramService gRPC server.
type Server struct {
	pb.UnimplementedEngramServiceServer
	handler MCPHandler
	token   string             // from ENGRAM_API_TOKEN; empty means auth disabled
	db      *gorm.DB           // injected by worker after DB is ready
	bus     *projectevents.Bus // in-process project lifecycle event bus
}

// New creates a new gRPC server with an optional auth interceptor.
// The returned *grpc.Server has EngramService already registered.
func New(handler MCPHandler) (*grpc.Server, *Server) {
	token := os.Getenv("ENGRAM_API_TOKEN")

	srv := &Server{
		handler: handler,
		token:   token,
	}

	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(16 << 20), // 16 MB
		grpc.MaxSendMsgSize(16 << 20),
	}

	if token != "" {
		opts = append(opts, grpc.UnaryInterceptor(srv.authInterceptor))
	}

	gs := grpc.NewServer(opts...)
	pb.RegisterEngramServiceServer(gs, srv)
	return gs, srv
}

// SetDB wires the database connection into the gRPC server after async initialization
// completes. It is safe to call from a different goroutine than New, but callers must
// ensure SetDB is called before SyncProjectState can be reached by clients.
func (s *Server) SetDB(db *gorm.DB) {
	s.db = db
}

// SetBus wires the in-process project event bus so that the ProjectEvents stream
// handler can forward lifecycle events to connected daemons.
func (s *Server) SetBus(bus *projectevents.Bus) {
	s.bus = bus
}

// Ping is a lightweight health check. Auth is intentionally skipped for Ping.
func (s *Server) Ping(_ context.Context, _ *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{Status: "ok"}, nil
}

// Initialize returns server info and the complete list of available tools.
func (s *Server) Initialize(_ context.Context, _ *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	name, version := s.handler.ServerInfo()

	defs := s.handler.ToolDefinitions()
	tools := make([]*pb.ToolDefinition, len(defs))
	for i, d := range defs {
		tools[i] = &pb.ToolDefinition{
			Name:            d.Name,
			Description:     d.Description,
			InputSchemaJson: d.InputSchemaJSON,
		}
	}

	return &pb.InitializeResponse{
		ServerName:    name,
		ServerVersion: version,
		Tools:         tools,
	}, nil
}

// CallTool dispatches a single MCP tool call.
func (s *Server) CallTool(ctx context.Context, req *pb.CallToolRequest) (*pb.CallToolResponse, error) {
	// Inject project identity using the same context key that internal/mcp reads.
	if req.Project != "" {
		ctx = mcp.ContextWithProject(ctx, req.Project)
	}

	resultJSON, isError, err := s.handler.HandleToolCall(ctx, req.ToolName, req.ArgumentsJson)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "tool call failed: %v", err)
	}

	return &pb.CallToolResponse{
		IsError:     isError,
		ContentJson: resultJSON,
	}, nil
}

// authInterceptor validates the Bearer token from gRPC metadata.
// Ping is always allowed through regardless of token.
func (s *Server) authInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	// Ping is a health-check — skip auth so monitoring tools work without credentials.
	if info.FullMethod == pb.EngramService_Ping_FullMethodName {
		return handler(ctx, req)
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}

	values := md.Get("authorization")
	if len(values) == 0 {
		return nil, status.Error(codes.Unauthenticated, "missing authorization header")
	}

	token := values[0]
	// Accept both "Bearer <token>" and raw token.
	if len(token) > 7 && token[:7] == "Bearer " {
		token = token[7:]
	}

	if token != s.token {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}

	return handler(ctx, req)
}
