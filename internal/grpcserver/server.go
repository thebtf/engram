package grpcserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/thebtf/engram/internal/auth"
	engramgorm "github.com/thebtf/engram/internal/db/gorm"
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
//
// Authentication is delegated to *auth.Validator (FR-2 / Plan ADR-002): the
// same validation chain runs on HTTP and gRPC, so a bearer that authenticates
// over `/api/...` MUST authenticate equivalently over gRPC. The validator is
// nil ONLY when ENGRAM_AUTH_DISABLED=true is the operator's deliberate choice.
type Server struct {
	pb.UnimplementedEngramServiceServer
	handler          MCPHandler
	mu               sync.RWMutex       // guards validator pointer swaps
	validator        *auth.Validator    // nil = auth disabled; read under mu.RLock
	db               *gorm.DB           // injected by worker after DB is ready
	bus              *projectevents.Bus // in-process project lifecycle event bus
	identityResolver func(context.Context, *gorm.DB, string, *pb.ProjectIdentityV2) (string, error)
}

// New creates a new gRPC server. The returned *grpc.Server has EngramService
// already registered AND has unary + streaming auth interceptors wired
// unconditionally. The interceptors bypass auth when the live validator is
// nil — used by tests and by ENGRAM_AUTH_DISABLED deployments — but they
// honour any validator installed later via SetValidator without restart.
//
// Pass validator = nil to start with auth disabled; SetValidator(v) at any
// later time re-enables it. Production callers SHOULD pass a non-nil
// validator at New time.
func New(handler MCPHandler, validator *auth.Validator) (*grpc.Server, *Server) {
	srv := &Server{
		handler:   handler,
		validator: validator,
	}

	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(16 << 20), // 16 MB
		grpc.MaxSendMsgSize(16 << 20),
		// Always register the interceptors. They are runtime-no-op when the
		// live validator is nil (auth disabled), and runtime-enforce when
		// SetValidator promotes the server out of bootstrap. Conditional
		// registration would lock the server into the construction-time
		// auth state and silently leave RPCs unprotected after a
		// nil → non-nil swap.
		grpc.UnaryInterceptor(srv.authInterceptor),
		grpc.StreamInterceptor(srv.streamAuthInterceptor),
	}

	gs := grpc.NewServer(opts...)
	pb.RegisterEngramServiceServer(gs, srv)
	return gs, srv
}

// SetValidator swaps the validator after construction. Used in tests and as
// a hook point for future operator-key rotation. Production wiring already
// receives the validator at New time; this setter exists for symmetry with
// SetDB / SetBus.
//
// Concurrent reads from the auth interceptors are serialised through s.mu
// — every validateBearer call takes RLock, so SetValidator's Lock/Unlock
// is the only writer. Without the mutex the pointer swap races with reads.
func (s *Server) SetValidator(v *auth.Validator) {
	s.mu.Lock()
	s.validator = v
	s.mu.Unlock()
}

// currentValidator returns the live validator under read lock.
func (s *Server) currentValidator() *auth.Validator {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.validator
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
func (s *Server) Initialize(ctx context.Context, req *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	canonicalProject, err := s.resolveProjectIdentity(ctx, req.GetProject(), req.GetProjectIdentity())
	if err != nil {
		return nil, err
	}
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
		ServerName:       name,
		ServerVersion:    version,
		Tools:            tools,
		CanonicalProject: canonicalProject,
	}, nil
}

// CallTool dispatches a single MCP tool call.
func (s *Server) CallTool(ctx context.Context, req *pb.CallToolRequest) (*pb.CallToolResponse, error) {
	canonicalProject, err := s.resolveProjectIdentity(ctx, req.GetProject(), req.GetProjectIdentity())
	if err != nil {
		return nil, err
	}
	// Inject project identity using the same context key that internal/mcp reads.
	if canonicalProject != "" {
		ctx = mcp.ContextWithProject(ctx, canonicalProject)
	}
	// Finding 3: inject session identity so audit helpers can record the correct
	// SourceSessionID. Only set when the proto field is non-empty.
	if req.SessionId != "" {
		ctx = mcp.ContextWithSession(ctx, req.SessionId)
	}

	argumentsJSON, err := canonicalizeProjectArgument(req.ToolName, req.ArgumentsJson, canonicalProject)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	resultJSON, isError, err := s.handler.HandleToolCall(ctx, req.ToolName, argumentsJSON)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "tool call failed: %v", err)
	}

	return &pb.CallToolResponse{
		IsError:          isError,
		ContentJson:      resultJSON,
		CanonicalProject: canonicalProject,
	}, nil
}

// canonicalizeProjectArgument makes the identity-resolved project authoritative
// for any explicitly project-scoped tool call. Empty/omitted project fields keep
// their existing global/default semantics. The review queue's documented all/*
// sentinels are also preserved.
func canonicalizeProjectArgument(toolName string, args []byte, canonicalProject string) ([]byte, error) {
	if canonicalProject == "" || len(bytes.TrimSpace(args)) == 0 {
		return args, nil
	}

	var values map[string]json.RawMessage
	if err := json.Unmarshal(args, &values); err != nil || values == nil {
		return args, nil
	}
	rawProject, ok := values["project"]
	if !ok || bytes.Equal(bytes.TrimSpace(rawProject), []byte("null")) {
		return args, nil
	}

	var project string
	if err := json.Unmarshal(rawProject, &project); err != nil {
		return nil, errors.New("tool arguments.project must be a string")
	}
	if project == "" {
		return args, nil
	}
	if (toolName == "review_metrics.read" || toolName == "review_queue.read") &&
		(strings.EqualFold(project, "all") || project == "*") {
		return args, nil
	}
	if project == canonicalProject {
		return args, nil
	}

	encodedProject, err := json.Marshal(canonicalProject)
	if err != nil {
		return nil, err
	}
	values["project"] = encodedProject
	return json.Marshal(values)
}

func (s *Server) resolveProjectIdentity(ctx context.Context, selector string, identity *pb.ProjectIdentityV2) (string, error) {
	resolver := s.identityResolver
	if resolver == nil {
		resolver = func(ctx context.Context, db *gorm.DB, selector string, wire *pb.ProjectIdentityV2) (string, error) {
			var metadata *engramgorm.ProjectIdentityV2
			if wire != nil {
				metadata = &engramgorm.ProjectIdentityV2{
					Version:         wire.GetVersion(),
					LegacyProjectID: wire.GetLegacyProjectId(),
					DisplayName:     wire.GetDisplayName(),
					GitRemote:       wire.GetGitRemote(),
					RelativePath:    wire.GetRelativePath(),
					NonGitAnchor:    wire.GetNonGitAnchor(),
					AnchorShared:    wire.AnchorShared,
				}
			}
			resolved, err := engramgorm.RegisterAndResolve(ctx, db, selector, metadata)
			return resolved.CanonicalProjectID, err
		}
	}
	canonical, err := resolver(ctx, s.db, selector, identity)
	if err == nil {
		return canonical, nil
	}
	var identityErr *engramgorm.ProjectIdentityError
	if !errors.As(err, &identityErr) {
		return "", status.Error(codes.Unavailable, engramgorm.ProjectIdentityPublicMessage(err))
	}
	code := codes.Unavailable
	switch identityErr.Code {
	case engramgorm.ProjectIdentityInvalid:
		code = codes.InvalidArgument
	case engramgorm.ProjectIdentityAmbiguous:
		code = codes.FailedPrecondition
	}
	st := status.New(code, engramgorm.ProjectIdentityPublicMessage(identityErr))
	withDetails, detailsErr := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   identityErr.Code,
		Domain:   "engram.project_identity",
		Metadata: map[string]string{"upgrade_action": identityErr.UpgradeAction},
	})
	if detailsErr != nil {
		return "", st.Err()
	}
	return "", withDetails.Err()
}

// extractBearer pulls the bearer token from gRPC metadata, stripping the
// optional "Bearer " prefix. Returns empty string when no authorization
// header is present (caller decides whether that's an error).
func extractBearer(md metadata.MD) string {
	values := md.Get("authorization")
	if len(values) == 0 {
		return ""
	}
	return strings.TrimPrefix(values[0], "Bearer ")
}

// validateBearer runs the validator and maps the outcome to a gRPC status
// error. Returns (Identity, nil) on success.
//
// Error mapping follows FR-2 + spec §5.2 Error Path Table:
//
//   - missing metadata     → Unauthenticated "missing metadata"
//   - missing header       → Unauthenticated "missing authorization header"
//   - empty token after strip → Unauthenticated "missing authorization header"
//   - invalid credentials  → Unauthenticated "invalid token"
//   - revoked              → Unauthenticated "token revoked"
//   - other (DB error)     → Internal "auth: store unavailable"
func (s *Server) validateBearer(ctx context.Context) (auth.Identity, error) {
	v := s.currentValidator()
	if v == nil {
		// Auth disabled deployments skip the interceptor entirely; if we
		// reach here without a validator, fail closed.
		return auth.Identity{}, status.Error(codes.Internal, "auth: validator not configured")
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return auth.Identity{}, status.Error(codes.Unauthenticated, "missing metadata")
	}
	raw := extractBearer(md)
	if raw == "" {
		return auth.Identity{}, status.Error(codes.Unauthenticated, "missing authorization header")
	}

	id, err := v.Validate(ctx, raw)
	switch {
	case err == nil:
		return id, nil
	case errors.Is(err, auth.ErrEmptyToken):
		return auth.Identity{}, status.Error(codes.Unauthenticated, "missing authorization header")
	case errors.Is(err, auth.ErrInvalidCredentials):
		return auth.Identity{}, status.Error(codes.Unauthenticated, "invalid token")
	case errors.Is(err, auth.ErrRevoked):
		// Currently unreachable: gormdb.TokenStore.FindByPrefix already
		// filters revoked rows at the SQL layer ("AND NOT revoked"), so
		// the validator never observes a revoked candidate. Kept as the
		// explicit mapping for the day FindByPrefix changes contract OR
		// a different TokenStoreReader implementation surfaces revoked
		// rows for audit logging.
		return auth.Identity{}, status.Error(codes.Unauthenticated, "token revoked")
	default:
		// DB error or unexpected bcrypt failure. Surface as Internal so
		// monitoring distinguishes auth-rejected (Unauthenticated) from
		// auth-broken (Internal).
		return auth.Identity{}, status.Error(codes.Internal, "auth: store unavailable")
	}
}

// authInterceptor is the unary gRPC server interceptor. Ping is always allowed
// through regardless of credentials. All other RPCs are validated through the
// shared *auth.Validator. Successful identities are stored in the request
// context via auth.WithIdentity so downstream handlers can read role/source.
func (s *Server) authInterceptor(
	ctx context.Context,
	req any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	if info.FullMethod == pb.EngramService_Ping_FullMethodName {
		return handler(ctx, req)
	}

	// Auth disabled (no validator installed) — bypass entirely. This is the
	// runtime check that lets SetValidator(v) flip the server from bootstrap
	// to enforced without recreating the gRPC server.
	if s.currentValidator() == nil {
		return handler(ctx, req)
	}

	id, err := s.validateBearer(ctx)
	if err != nil {
		return nil, err
	}

	ctx = auth.WithIdentity(ctx, id)
	return handler(ctx, req)
}

// streamAuthInterceptor is the streaming gRPC server interceptor. Ping is not
// streaming; SyncProjectState is unary; ProjectEvents is the only streaming
// method on the engram surface. The interceptor validates the bearer at stream
// open. Per-event re-validation (FR-6 revocation honour mid-stream) lives in
// the ProjectEvents emitter (see project_events.go).
func (s *Server) streamAuthInterceptor(
	srv any,
	ss grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	// Auth disabled (no validator installed) — bypass entirely.
	if s.currentValidator() == nil {
		return handler(srv, ss)
	}

	id, err := s.validateBearer(ss.Context())
	if err != nil {
		return err
	}

	wrapped := &authedStream{ServerStream: ss, ctx: auth.WithIdentity(ss.Context(), id)}
	return handler(srv, wrapped)
}

// authedStream overrides Context() so handlers downstream of the interceptor
// see the auth-enriched context.
type authedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (a *authedStream) Context() context.Context { return a.ctx }
