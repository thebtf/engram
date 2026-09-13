package grpcserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	engramgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/hostadvisor"
	"github.com/thebtf/engram/internal/intervention"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/worker/ambientcore"
	"github.com/thebtf/engram/internal/worker/projectevents"
	"github.com/thebtf/engram/internal/worker/sessioncompat"
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
	ambientDependencies   ambientcore.Dependencies
	sessionStartCommitter sessioncompat.DeliveryCommitter
	identityResolver      func(context.Context, *gorm.DB, string, *pb.ProjectIdentityV2) (string, error)
	identityResolverV3    func(context.Context, *gorm.DB, projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error)
	comparisonObserverV3  projectidentity.LegacyComparisonObserverV2
	comparisonStoreV3     projectidentity.ComparisonStoreV3
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

// SetAmbientDependencies wires the worker-owned, bounded ambient core into
// the private bridge facade. The zero value intentionally fails open.
func (s *Server) SetAmbientDependencies(dependencies ambientcore.Dependencies) {
	s.mu.Lock()
	s.ambientDependencies = dependencies
	s.mu.Unlock()
}

func (s *Server) currentAmbientDependencies() ambientcore.Dependencies {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ambientDependencies
}

// SetSessionStartDeliveryCommitter wires attempted-delivery recording for
// relay session-start responses. The response is committed before transport
// acknowledgement, preserving existing delivery semantics.
func (s *Server) SetSessionStartDeliveryCommitter(committer sessioncompat.DeliveryCommitter) {
	s.mu.Lock()
	s.sessionStartCommitter = committer
	s.mu.Unlock()
}

func (s *Server) commitRelaySessionStartDelivery(hostSessionRef, canonicalProject string, memories []*pb.SessionStartMemory) {
	s.mu.RLock()
	committer := s.sessionStartCommitter
	s.mu.RUnlock()
	if committer != nil {
		committer.CommitSessionStartDelivery(hostSessionRef, canonicalProject, memories)
	}
}

// Ping is a lightweight health check. Auth is intentionally skipped for Ping.
func (s *Server) Ping(_ context.Context, _ *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{Status: "ok"}, nil
}

// RegisterProjectIdentityV3 explicitly establishes a V3 anchor binding for a
// server-authenticated master administrator. Request credentials, canonical
// project authority, and registration authorization are never client inputs.
func (s *Server) RegisterProjectIdentityV3(ctx context.Context, req *pb.RegisterProjectIdentityV3Request) (*pb.RegisterProjectIdentityV3Response, error) {
	if err := s.authorizeRegistrationIdentity(ctx, req); err != nil {
		return nil, err
	}
	if req == nil || len(req.ProtoReflect().GetUnknown()) != 0 {
		return nil, v3DescriptorInvalid()
	}
	projectIdentity := req.GetProjectIdentityV3()
	if projectIdentity == nil || len(projectIdentity.ProtoReflect().GetUnknown()) != 0 {
		return nil, v3DescriptorInvalid()
	}
	if req.GetRelayRevision() != "" {
		ctx = withHAPRelayRegistration(ctx)
	}
	resolution, err := s.resolveProjectIdentityV3(ctx, projectIdentity, projectidentity.RegisterAnchorIntentV3)
	if err != nil {
		return nil, err
	}
	return &pb.RegisterProjectIdentityV3Response{ProjectResolutionV3: projectIdentityV3Proto(resolution)}, nil
}

// Initialize returns server info and the complete list of available tools.
func (s *Server) Initialize(ctx context.Context, req *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	if err := rejectHAPCredentialWithoutProject(ctx); err != nil {
		return nil, err
	}
	canonicalProject := ""
	var resolutionV3 *pb.ProjectResolutionResultV3
	if identity := req.GetProjectIdentityV3(); identity != nil {
		resolution, err := s.resolveProjectIdentityV3(ctx, identity, projectidentity.ResolveExistingIntentV3)
		if err != nil {
			return nil, err
		}
		canonicalProject = string(resolution.CanonicalProjectKey())
		resolutionV3 = projectIdentityV3Proto(resolution)
	} else if req.GetProject() != "" || req.GetProjectIdentity() != nil {
		var err error
		canonicalProject, err = s.resolveProjectIdentity(ctx, req.GetProject(), req.GetProjectIdentity())
		if err != nil {
			return nil, err
		}
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

	proof := initializeAuthenticatedSubjectProof(ctx)

	return &pb.InitializeResponse{
		ServerName:                      name,
		ServerVersion:                   version,
		Tools:                           tools,
		CanonicalProject:                canonicalProject,
		ProjectResolutionV3:             resolutionV3,
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
	canonicalProject := ""
	var resolutionV3 *pb.ProjectResolutionResultV3
	if identity := req.GetProjectIdentityV3(); identity != nil {
		intent, adminPurge := v3CallToolIntent(req.ToolName, req.ArgumentsJson)
		if adminPurge {
			return nil, v3DescriptorInvalid()
		}
		resolution, err := s.resolveProjectIdentityV3(ctx, identity, intent)
		if err != nil {
			return nil, err
		}
		canonicalProject = string(resolution.CanonicalProjectKey())
		resolutionV3 = projectIdentityV3Proto(resolution)
	} else if req.GetProject() != "" || req.GetProjectIdentity() != nil {
		var err error
		canonicalProject, err = s.resolveProjectIdentity(ctx, req.GetProject(), req.GetProjectIdentity())
		if err != nil {
			return nil, err
		}
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
	if correlationValid {
		ctx = auditcontext.WithUCIRequestCorrelation(ctx, correlation)
	}
	if requiresCorrelation {
		ctx = auditcontext.WithUCIRequestCorrelationRequired(ctx)
	}

	argumentsJSON, err := canonicalizeProjectArgument(req.ToolName, req.ArgumentsJson, canonicalProject, req.GetProjectIdentityV3() == nil)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	resultJSON, isError, err := s.handler.HandleToolCall(ctx, req.ToolName, argumentsJSON)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "tool call failed: %v", err)
	}

	return &pb.CallToolResponse{
		IsError:             isError,
		ContentJson:         resultJSON,
		CanonicalProject:    canonicalProject,
		ProjectResolutionV3: resolutionV3,
	}, nil
}

func requiresUCIRequestCorrelation(req *pb.CallToolRequest) bool {
	if req == nil || req.GetProject() != "" || req.GetProjectIdentity() != nil || req.GetProjectIdentityV3() != nil {
		return false
	}
	switch req.GetToolName() {
	case "codebase_search", "codebase_graph", "codebase_read":
		return true
	default:
		return false
	}
}

func v3CallToolIntent(toolName string, args []byte) (projectidentity.ResolutionIntentV3, bool) {
	var values map[string]json.RawMessage
	if len(bytes.TrimSpace(args)) == 0 || json.Unmarshal(args, &values) != nil || values == nil {
		return projectidentity.ResolveExistingIntentV3, false
	}
	action := ""
	if rawAction, ok := values["action"]; ok && !bytes.Equal(bytes.TrimSpace(rawAction), []byte("null")) {
		if json.Unmarshal(rawAction, &action) != nil {
			return projectidentity.ResolveExistingIntentV3, false
		}
	}
	if toolName == "admin" && action == "purge_project" {
		return projectidentity.ResolveExistingIntentV3, true
	}
	rawProject, hasProject := values["project"]
	hasProject = hasProject && !bytes.Equal(bytes.TrimSpace(rawProject), []byte("null"))
	if toolName == "issues" {
		rawSourceProject, hasSourceProject := values["source_project"]
		hasSourceProject = hasSourceProject && !bytes.Equal(bytes.TrimSpace(rawSourceProject), []byte("null"))
		if (action == "" || action == "list") && (hasProject || hasSourceProject) {
			return projectidentity.ReadFilterIntentV3, false
		}
		return projectidentity.ResolveExistingIntentV3, false
	}
	if hasProject {
		switch toolName {
		case "review_metrics.read", "review_queue.read",
			"rule_governance_health", "rule_governance_queue", "rule_governance_snapshots", "rule_governance_usefulness":
			return projectidentity.ReadFilterIntentV3, false
		}
	}
	return projectidentity.ResolveExistingIntentV3, false
}

const projectIdentityResolutionUnavailableMessage = "project identity resolution unavailable"

func v3DescriptorInvalid() error {
	correlation, err := projectidentity.NewCorrelationV3(uuid.NewString())
	if err != nil {
		return status.Error(codes.Unavailable, projectIdentityResolutionUnavailableMessage)
	}
	return projectIdentityV3RefusalStatus(projectidentity.ProjectDescriptorInvalidOutcomeV3, correlation)
}

// canonicalizeProjectArgument makes the identity-resolved project authoritative
// for caller-scoped project fields. V2 keeps its documented target/filter
// exceptions; V3 replaces every supported filter with server-resolved scope.
func canonicalizeProjectArgument(toolName string, args []byte, canonicalProject string, preserveV2Target bool) ([]byte, error) {
	if canonicalProject == "" || len(bytes.TrimSpace(args)) == 0 {
		return args, nil
	}

	var values map[string]json.RawMessage
	if err := json.Unmarshal(args, &values); err != nil || values == nil {
		return args, nil
	}
	for key := range values {
		if key != "project" && strings.EqualFold(key, "project") {
			return nil, errors.New(`tool arguments.project must use the lowercase "project" key`)
		}
	}
	rawProject, hasProject := values["project"]
	project := ""
	if hasProject && !bytes.Equal(bytes.TrimSpace(rawProject), []byte("null")) {
		if err := json.Unmarshal(rawProject, &project); err != nil {
			return nil, errors.New("tool arguments.project must be a string")
		}
	}
	rawSourceProject, hasSourceProject := values["source_project"]
	parseAction := project != "" || (!preserveV2Target && toolName == "issues" && hasSourceProject && !bytes.Equal(bytes.TrimSpace(rawSourceProject), []byte("null")))
	var action string
	if rawAction, ok := values["action"]; parseAction && ok {
		if bytes.Equal(bytes.TrimSpace(rawAction), []byte("null")) || json.Unmarshal(rawAction, &action) != nil {
			return nil, errors.New("tool arguments.action must be a string")
		}
	}
	sourceProject := ""
	if !preserveV2Target && toolName == "issues" && (action == "" || action == "list") && hasSourceProject && !bytes.Equal(bytes.TrimSpace(rawSourceProject), []byte("null")) {
		if err := json.Unmarshal(rawSourceProject, &sourceProject); err != nil {
			return nil, errors.New("tool arguments.source_project must be a string")
		}
	}
	if project == "" && sourceProject == "" {
		return args, nil
	}
	if preserveV2Target {
		if (toolName == "admin" && action == "purge_project") ||
			(toolName == "issues" && (action == "" || action == "list")) {
			return args, nil
		}
		switch toolName {
		case "review_metrics.read", "review_queue.read",
			"rule_governance_health", "rule_governance_queue", "rule_governance_snapshots", "rule_governance_usefulness":
			return args, nil
		}
	}

	encodedProject, err := json.Marshal(canonicalProject)
	if err != nil {
		return nil, err
	}
	changed := false
	if project != "" && project != canonicalProject {
		values["project"] = encodedProject
		changed = true
	}
	if sourceProject != "" && sourceProject != canonicalProject {
		values["source_project"] = encodedProject
		changed = true
	}
	if !changed {
		return args, nil
	}
	return json.Marshal(values)
}

// grpcV3AuthorizationVerifier admits only the opaque reference generated in
// this transport after gRPC authentication has established an identity.
type grpcV3AuthorizationVerifier struct {
	authorization projectidentity.AuthorizationReferenceV3
}

func (verifier grpcV3AuthorizationVerifier) VerifyAuthorizationV3(ctx context.Context, request projectidentity.AuthorizationVerificationRequestV3) (projectidentity.AuthorizationVerificationV3, error) {
	if request.Authorization() != verifier.authorization {
		return projectidentity.AuthorizationVerificationV3{}, nil
	}
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return projectidentity.AuthorizationVerificationV3{}, nil
	}
	switch request.Intent() {
	case projectidentity.ResolveExistingIntentV3, projectidentity.ReadFilterIntentV3:
		return projectidentity.AuthorizationVerificationV3{Authorized: true}, nil
	case projectidentity.RegisterAnchorIntentV3:
		if (identity.Source == auth.SourceMaster && identity.Role == auth.RoleAdmin) ||
			(isHAPRelayRegistration(ctx) && identity.IsHAPRegistrationService()) {
			return projectidentity.AuthorizationVerificationV3{Authorized: true}, nil
		}
	}
	return projectidentity.AuthorizationVerificationV3{}, nil
}

func grpcProjectIdentityV3Request(identity *pb.ProjectIdentityV3, intent projectidentity.ResolutionIntentV3, correlation projectidentity.CorrelationV3) (projectidentity.ResolveProjectRequestV3, error) {
	if correlation == "" {
		generated, err := projectidentity.NewCorrelationV3(uuid.NewString())
		if err != nil {
			return projectidentity.ResolveProjectRequestV3{}, err
		}
		correlation = generated
	} else if _, err := projectidentity.NewCorrelationV3(string(correlation)); err != nil {
		return projectidentity.ResolveProjectRequestV3{}, err
	}
	authorization, err := projectidentity.NewAuthorizationReferenceV3(uuid.NewString())
	if err != nil {
		return projectidentity.ResolveProjectRequestV3{}, err
	}
	anchor, descriptor := grpcProjectIdentityV3Evidence(identity)
	request := projectidentity.ResolveProjectRequestV3{
		Intent:      intent,
		Anchor:      anchor,
		Descriptor:  descriptor,
		Correlation: correlation,
	}
	switch intent {
	case projectidentity.ResolveExistingIntentV3:
		request.ResolveExistingAuthorization = authorization
	case projectidentity.RegisterAnchorIntentV3:
		request.RegistrationAuthorization = authorization
	case projectidentity.ReadFilterIntentV3:
		readFilter, err := projectidentity.NewReadFilterRequirementV3(authorization, correlation)
		if err != nil {
			return projectidentity.ResolveProjectRequestV3{}, err
		}
		request.ReadFilter = &readFilter
	default:
		return projectidentity.ResolveProjectRequestV3{}, errors.New("unsupported gRPC V3 resolution intent")
	}
	return request, nil
}

func grpcProjectIdentityV3Evidence(identity *pb.ProjectIdentityV3) (projectidentity.AnchorV3, projectidentity.DescriptorV3) {
	legacy := make([]projectidentity.LegacyIdentifierV3, len(identity.GetLegacyIdentifiers()))
	for index, identifier := range identity.GetLegacyIdentifiers() {
		legacy[index] = projectidentity.LegacyIdentifierV3{
			Scheme:     projectidentity.LegacyIdentifierSchemeV3(identifier.GetScheme()),
			Value:      identifier.GetValue(),
			Provenance: projectidentity.LegacyIdentifierProvenanceV3(identifier.GetProvenance()),
		}
	}
	anchor := projectidentity.AnchorV3{
		Version:   int(identity.GetVersion()),
		ProjectID: identity.GetAnchorProjectId(),
		Name:      identity.GetName(),
		Scope:     identity.GetScope(),
	}
	return anchor, projectidentity.DescriptorV3{
		Version:              int(identity.GetVersion()),
		AnchorProjectID:      identity.GetAnchorProjectId(),
		Name:                 identity.GetName(),
		Scope:                identity.GetScope(),
		NormalizedGitRemotes: identity.GetNormalizedGitRemotes(),
		LegacyIdentifiers:    legacy,
		ClientInstanceID:     identity.GetClientInstanceId(),
	}
}

func (s *Server) resolveProjectIdentityV3(ctx context.Context, identity *pb.ProjectIdentityV3, intent projectidentity.ResolutionIntentV3) (projectidentity.ResolutionResultV3, error) {
	var origin projectidentity.ComparisonOriginV3
	var references projectidentity.ComparisonReferencesV3
	comparisonEnabled := false
	correlation := projectidentity.CorrelationV3("")
	if intent == projectidentity.ResolveExistingIntentV3 || intent == projectidentity.ReadFilterIntentV3 {
		origin = grpcComparisonOriginV3(ctx)
		anchor, descriptor := grpcProjectIdentityV3Evidence(identity)
		if derived, err := origin.DeriveComparisonReferencesV3(anchor, descriptor, intent); err == nil {
			references = derived
			correlation = references.Correlation
			comparisonEnabled = true
		}
	}
	request, err := grpcProjectIdentityV3Request(identity, intent, correlation)
	if err != nil {
		return projectidentity.ResolutionResultV3{}, status.Error(codes.Internal, projectIdentityResolutionUnavailableMessage)
	}
	resolver := s.identityResolverV3
	if resolver == nil {
		resolver = func(ctx context.Context, db *gorm.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
			authorization := request.ResolveExistingAuthorization
			if request.RegistrationAuthorization != "" {
				authorization = request.RegistrationAuthorization
			} else if request.ReadFilter != nil {
				authorization = request.ReadFilter.Authorization()
			}
			resolved, err := projectidentity.NewResolverV3(&engramgorm.Store{DB: db}, grpcV3AuthorizationVerifier{authorization: authorization}).ResolveProjectV3(ctx, request)
			return resolved.Resolution(), err
		}
	}
	resolution, err := resolver(ctx, s.db, request)
	if comparisonEnabled && resolution.Outcome().Valid() {
		s.observeProjectIdentityComparisonV3(ctx, request, resolution, origin, references)
	}
	if err := projectIdentityV3Error(resolution, err); err != nil {
		return projectidentity.ResolutionResultV3{}, err
	}
	return resolution, nil
}

func (s *Server) observeProjectIdentityComparisonV3(ctx context.Context, request projectidentity.ResolveProjectRequestV3, resolution projectidentity.ResolutionResultV3, origin projectidentity.ComparisonOriginV3, references projectidentity.ComparisonReferencesV3) {
	descriptor, err := projectidentity.NewLegacyComparisonDescriptorV3(request.Anchor, request.Descriptor)
	if err != nil {
		log.Print("project identity comparison skipped: invalid descriptor")
		return
	}
	observer := s.comparisonObserverV3
	if observer == nil {
		observer = &engramgorm.Store{DB: s.db}
	}
	legacyOutcome, err := observer.ObserveLegacyOutcomeV2(ctx, descriptor)
	if err != nil {
		log.Print("project identity comparison observer unavailable")
		legacyOutcome = projectidentity.LegacyComparisonUnavailableV2
	}
	scope := projectidentity.ComparisonRepositoryScopeV3
	if request.Descriptor.Scope == "directory" {
		scope = projectidentity.ComparisonDirectoryScopeV3
	}
	observation, err := projectidentity.NewComparisonObservationV3(
		references.IdempotencyKey,
		resolution.Correlation(),
		resolution.Outcome(),
		legacyOutcome,
		request.Descriptor.ClientInstanceID,
		origin.Transport(),
		scope,
		projectidentity.ComparisonUnknownV3,
		references.EvidenceFingerprint,
	)
	if err != nil {
		log.Print("project identity comparison skipped: invalid redacted observation")
		return
	}
	store := s.comparisonStoreV3
	if store == nil {
		store = &engramgorm.Store{DB: s.db}
	}
	if _, err := projectidentity.RecordComparisonV3(ctx, store, observation); err != nil {
		log.Print("project identity comparison store unavailable")
	}
}

func grpcComparisonOriginV3(ctx context.Context) projectidentity.ComparisonOriginV3 {
	if origin, ok := projectidentity.ComparisonOriginFromContextV3(ctx); ok {
		return origin
	}
	var claim, requestID string
	if metadata, ok := metadata.FromIncomingContext(ctx); ok {
		claim = singleGRPCMetadataValueV3(metadata.Get("x-engram-project-identity-adapter"))
		requestID = singleGRPCMetadataValueV3(metadata.Get("x-request-id"))
	}
	return projectidentity.NewComparisonOriginV3(projectidentity.ComparisonTransportGRPCV3, claim, requestID)
}

func singleGRPCMetadataValueV3(values []string) string {
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func projectIdentityV3Error(resolution projectidentity.ResolutionResultV3, resolverErr error) error {
	if resolution.IsRefusal() {
		return projectIdentityV3RefusalStatus(resolution.Outcome(), resolution.Correlation())
	}
	var refusal projectidentity.ResolutionErrorV3
	if errors.As(resolverErr, &refusal) {
		return projectIdentityV3RefusalStatus(refusal.Outcome(), refusal.Correlation())
	}
	if resolverErr != nil || !resolution.Outcome().IsSuccess() {
		return status.Error(codes.Unavailable, projectIdentityResolutionUnavailableMessage)
	}
	return nil
}

func projectIdentityV3RefusalStatus(outcome projectidentity.ResolutionOutcomeV3, correlation projectidentity.CorrelationV3) error {
	code := codes.FailedPrecondition
	switch outcome {
	case projectidentity.ProjectAnchorInvalidOutcomeV3,
		projectidentity.ProjectScopeMismatchOutcomeV3,
		projectidentity.ProjectDescriptorUnsupportedOutcomeV3,
		projectidentity.ProjectDescriptorInvalidOutcomeV3,
		projectidentity.ProjectKeyClientAssertionForbiddenOutcomeV3:
		code = codes.InvalidArgument
	}
	st := status.New(code, "project identity resolution refused")
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   string(outcome),
		Domain:   "engram.project_identity.v3",
		Metadata: map[string]string{"correlation": string(correlation)},
	})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

func projectIdentityV3Proto(resolution projectidentity.ResolutionResultV3) *pb.ProjectResolutionResultV3 {
	result := &pb.ProjectResolutionResultV3{
		Outcome:     projectIdentityV3OutcomeProto(resolution.Outcome()),
		Correlation: string(resolution.Correlation()),
	}
	if resolution.Outcome().IsSuccess() {
		projectKey := string(resolution.CanonicalProjectKey())
		resolvedScope := string(resolution.ResolvedScope())
		result.ProjectKey = &projectKey
		result.ResolvedScope = &resolvedScope
		if redirect := string(resolution.RedirectReference()); redirect != "" {
			result.RedirectReference = &redirect
		}
	}
	return result
}

func projectIdentityV3OutcomeProto(outcome projectidentity.ResolutionOutcomeV3) pb.ProjectResolutionOutcomeV3 {
	switch outcome {
	case projectidentity.ProjectResolvedOutcomeV3:
		return pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED
	case projectidentity.ProjectRedirectedOutcomeV3:
		return pb.ProjectResolutionOutcomeV3_PROJECT_REDIRECTED
	case projectidentity.ProjectOnboardingRequiredOutcomeV3:
		return pb.ProjectResolutionOutcomeV3_PROJECT_ONBOARDING_REQUIRED
	case projectidentity.ProjectAnchorInvalidOutcomeV3:
		return pb.ProjectResolutionOutcomeV3_PROJECT_ANCHOR_INVALID
	case projectidentity.ProjectScopeMismatchOutcomeV3:
		return pb.ProjectResolutionOutcomeV3_PROJECT_SCOPE_MISMATCH
	case projectidentity.ProjectNestedRepositoryUnresolvedOutcomeV3:
		return pb.ProjectResolutionOutcomeV3_PROJECT_NESTED_REPOSITORY_UNRESOLVED
	case projectidentity.ProjectAnchorDecisionRequiredOutcomeV3:
		return pb.ProjectResolutionOutcomeV3_PROJECT_ANCHOR_DECISION_REQUIRED
	case projectidentity.ProjectIdentityAmbiguousOutcomeV3:
		return pb.ProjectResolutionOutcomeV3_PROJECT_IDENTITY_AMBIGUOUS
	case projectidentity.ProjectDescriptorUnsupportedOutcomeV3:
		return pb.ProjectResolutionOutcomeV3_PROJECT_DESCRIPTOR_UNSUPPORTED
	case projectidentity.ProjectDescriptorInvalidOutcomeV3:
		return pb.ProjectResolutionOutcomeV3_PROJECT_DESCRIPTOR_INVALID
	case projectidentity.ProjectKeyClientAssertionForbiddenOutcomeV3:
		return pb.ProjectResolutionOutcomeV3_PROJECT_KEY_CLIENT_ASSERTION_FORBIDDEN
	default:
		return pb.ProjectResolutionOutcomeV3_PROJECT_RESOLUTION_OUTCOME_V3_UNSPECIFIED
	}
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
	if !errors.As(err, &identityErr) || identityErr == nil {
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
