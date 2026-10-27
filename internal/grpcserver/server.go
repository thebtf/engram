package grpcserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/hostadvisor"
	"github.com/thebtf/engram/internal/intervention"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/projectidentity"
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
	handler               MCPHandler
	mu                    sync.RWMutex       // guards mutable server dependencies
	validator             *auth.Validator    // nil = auth disabled; read under mu.RLock
	db                    *gorm.DB           // injected by worker after DB is ready
	bus                   *projectevents.Bus // in-process project lifecycle event bus
	identityResolverV3    func(context.Context, *gorm.DB, projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error)
	hostAdvisorRegistry   *hostadvisor.Registry
	interventionAdvisor   intervention.Advisor
	uciTransport          UCITransport
	uciCompletionRecorder UCICompletionRecorder
}

// New creates a new gRPC server. The returned *grpc.Server has EngramService
// already registered AND has unary + streaming auth interceptors wired
// unconditionally. When the live validator is nil, the interceptors inject the
// explicit disabled-auth identity; otherwise they validate bearer credentials.
// They honour any validator installed later via SetValidator without restart.
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
		// Always register the interceptors. When the live validator is nil,
		// they inject the disabled-auth identity; when SetValidator promotes
		// the server out of bootstrap, they enforce bearer validation.
		// Conditional registration would lock the server into the
		// construction-time auth state and silently leave RPCs unprotected
		// after a nil → non-nil swap.
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

// SetHostAdvisorRegistry installs or removes the private host-advisor registry.
// A nil registry is the deliberate default-dark posture.
func (s *Server) SetHostAdvisorRegistry(registry *hostadvisor.Registry) {
	s.mu.Lock()
	s.hostAdvisorRegistry = registry
	s.mu.Unlock()
}

func (s *Server) currentHostAdvisorRegistry() *hostadvisor.Registry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hostAdvisorRegistry
}

// SetInterventionAdvisor installs or removes the private intervention runtime.
// A nil advisor is the deliberate typed-unavailable default-dark posture.
func (s *Server) SetInterventionAdvisor(advisor intervention.Advisor) {
	s.mu.Lock()
	s.interventionAdvisor = advisor
	s.mu.Unlock()
}

func (s *Server) currentInterventionAdvisor() intervention.Advisor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.interventionAdvisor
}

// SetUCITransport installs or removes the private scoped-UCI runtime. A nil
// transport deliberately leaves the UCI RPCs dark with typed Unavailable errors.
func (s *Server) SetUCITransport(transport UCITransport) {
	s.mu.Lock()
	s.uciTransport = transport
	s.mu.Unlock()
}

func (s *Server) currentUCITransport() UCITransport {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.uciTransport
}

// SetUCICompletionRecorder installs or removes the dedicated completion port.
func (s *Server) SetUCICompletionRecorder(recorder UCICompletionRecorder) {
	s.mu.Lock()
	s.uciCompletionRecorder = recorder
	s.mu.Unlock()
}

func (s *Server) currentUCICompletionRecorder() UCICompletionRecorder {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.uciCompletionRecorder
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
	if err := rejectHAPCredentialWithoutProject(ctx); err != nil {
		return nil, err
	}
	// Discovery never resolves a project. A scoped operation reports its own error.
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

	proof := initializeAuthenticatedSubjectProof(ctx)

	return &pb.InitializeResponse{
		ServerName:                      name,
		ServerVersion:                   version,
		Tools:                           tools,
		AuthenticatedSubjectProofSha256: proof,
	}, nil
}

// CallTool dispatches a single MCP tool call.
func (s *Server) CallTool(ctx context.Context, req *pb.CallToolRequest) (*pb.CallToolResponse, error) {
	if err := rejectHAPCredentialWithoutProject(ctx); err != nil {
		return nil, err
	}
	requiresCorrelation := requiresUCIRequestCorrelation(req)
	var metadataValues []string
	if incoming, found := metadata.FromIncomingContext(ctx); found {
		metadataValues = incoming.Get(auditcontext.UCIRequestCorrelationMetadataKey)
	}
	var correlation auditcontext.UCIRequestCorrelation
	correlationValid := false
	if len(metadataValues) == 1 {
		correlation, correlationValid = auditcontext.ParseUCIRequestCorrelation(metadataValues[0])
	}
	if requiresCorrelation && !correlationValid {
		if len(metadataValues) == 0 {
			return nil, status.Error(codes.FailedPrecondition, "UCI request correlation is required")
		}
		return nil, status.Error(codes.InvalidArgument, "invalid UCI request correlation")
	}
	if req.SessionId != "" {
		ctx = mcp.ContextWithSession(ctx, req.SessionId)
	}
	canonicalProject := req.GetProject()
	if canonicalProject != "" && !projectidentity.ValidProjectID(canonicalProject) {
		st := status.New(codes.InvalidArgument, "project must be a canonical UUID")
		withDetails, err := st.WithDetails(&errdetails.ErrorInfo{Reason: "PROJECT_CONTEXT_INVALID", Domain: "engram.project"})
		if err != nil {
			return nil, st.Err()
		}
		return nil, withDetails.Err()
	}
	if canonicalProject == "" && projectidentity.ToolRequiresProject(req.ToolName, req.ArgumentsJson) {
		st := status.New(codes.FailedPrecondition, "project context is required: "+projectidentity.RecoveryGuidance("PROJECT_CONTEXT_REQUIRED"))
		withDetails, err := st.WithDetails(&errdetails.ErrorInfo{Reason: "PROJECT_CONTEXT_REQUIRED", Domain: "engram.project"})
		if err != nil {
			return nil, st.Err()
		}
		return nil, withDetails.Err()
	}
	// Inject project identity using the same context key that internal/mcp reads.
	if canonicalProject != "" {
		ctx = mcp.ContextWithProject(ctx, canonicalProject)
	}
	if correlationValid {
		ctx = auditcontext.WithUCIRequestCorrelation(ctx, correlation)
	}
	if requiresCorrelation {
		ctx = auditcontext.WithUCIRequestCorrelationRequired(ctx)
	}
	if strings.HasPrefix(req.ToolName, "codebase_") {
		if identity, found := auth.IdentityFrom(ctx); found && identity.Source == auth.SourceAuthDisabled {
			instance, _, valid := noAuthUCIClientInstanceFrom(ctx)
			if !valid {
				return nil, status.Error(codes.FailedPrecondition, "noauth code client installation is required")
			}
			ctx = mcp.ContextWithCodeClientInstance(ctx, instance)
		}
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

func requiresUCIRequestCorrelation(req *pb.CallToolRequest) bool {
	if req == nil || req.GetProject() != "" {
		return false
	}
	switch req.GetToolName() {
	case "codebase_search", "codebase_graph", "codebase_read":
		return true
	default:
		return false
	}
}

// canonicalizeProjectArgument pins caller-scoped fields while preserving explicit
// administrative targets and cross-project reader filters.
func canonicalizeProjectArgument(toolName string, args []byte, canonicalProject string) ([]byte, error) {
	if canonicalProject == "" || len(bytes.TrimSpace(args)) == 0 {
		return args, nil
	}
	input, found, err := parseCanonicalProjectArguments(args)
	if err != nil {
		return nil, err
	}
	if !found || input.project == "" || preservesTargetProjectArgument(toolName, input.action) {
		return args, nil
	}
	return replaceCanonicalProjectArguments(args, input, canonicalProject)
}

type canonicalProjectArguments struct {
	values  map[string]json.RawMessage
	project string
	action  string
}

func parseCanonicalProjectArguments(args []byte) (canonicalProjectArguments, bool, error) {
	values, found, err := decodeCanonicalProjectArguments(args)
	if err != nil || !found {
		return canonicalProjectArguments{}, found, err
	}
	project, err := canonicalProjectString(values, "project")
	if err != nil {
		return canonicalProjectArguments{}, false, err
	}
	action, err := canonicalProjectAction(values, project != "")
	if err != nil {
		return canonicalProjectArguments{}, false, err
	}
	return canonicalProjectArguments{values: values, project: project, action: action}, true, nil
}

func decodeCanonicalProjectArguments(args []byte) (map[string]json.RawMessage, bool, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(args, &values); err != nil || values == nil {
		return nil, false, nil
	}
	for key := range values {
		if key != "project" && strings.EqualFold(key, "project") {
			return nil, false, errors.New(`tool arguments.project must use the lowercase "project" key`)
		}
	}
	return values, true, nil
}

func canonicalProjectString(values map[string]json.RawMessage, key string) (string, error) {
	raw, present := values[key]
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("tool arguments.%s must be a string", key)
	}
	return value, nil
}

func canonicalProjectAction(values map[string]json.RawMessage, required bool) (string, error) {
	if !required {
		return "", nil
	}
	raw, present := values["action"]
	if !present {
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", errors.New("tool arguments.action must be a string")
	}
	var action string
	if err := json.Unmarshal(raw, &action); err != nil {
		return "", errors.New("tool arguments.action must be a string")
	}
	return action, nil
}

func preservesTargetProjectArgument(toolName, action string) bool {
	if (toolName == "admin" && action == "purge_project") || (toolName == "issues" && (action == "" || action == "list")) {
		return true
	}
	switch toolName {
	case "review_metrics.read", "review_queue.read", "rule_governance_health", "rule_governance_queue", "rule_governance_snapshots", "rule_governance_usefulness":
		return true
	default:
		return false
	}
}

func replaceCanonicalProjectArguments(args []byte, input canonicalProjectArguments, canonicalProject string) ([]byte, error) {
	encodedProject, err := json.Marshal(canonicalProject)
	if err != nil {
		return nil, err
	}
	changed := false
	if input.project != "" && input.project != canonicalProject {
		input.values["project"] = encodedProject
		changed = true
	}
	if !changed {
		return args, nil
	}
	return json.Marshal(input.values)
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

	// Auth-disabled calls still need a concrete identity so downstream role
	// checks distinguish this deliberate mode from an unauthenticated request.
	if s.currentValidator() == nil {
		return handler(auth.WithIdentity(ctx, auth.AuthDisabled()), req)
	}

	id, err := s.validateBearer(ctx)
	if err != nil {
		return nil, err
	}

	ctx = auth.WithIdentity(ctx, id)
	return handler(ctx, req)
}

// streamAuthInterceptor is the streaming gRPC server interceptor. ProjectEvents
// and StageCodeIndex authenticate when their streams open. Per-event revocation
// checks apply only to the long-lived ProjectEvents emitter (see project_events.go).
func (s *Server) streamAuthInterceptor(
	srv any,
	ss grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	// Preserve the stream wrapper so downstream handlers observe the same
	// disabled-auth identity through ServerStream.Context().
	if s.currentValidator() == nil {
		wrapped := &authedStream{ServerStream: ss, ctx: auth.WithIdentity(ss.Context(), auth.AuthDisabled())}
		return handler(srv, wrapped)
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
