package engramcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/proxy"
	"github.com/thebtf/engram/internal/version"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	proxyToolsDiscoveryTimeout        = 30 * time.Second
	projectIdentityV3RegistrationTool = "project_identity.register_v3"
)

const (
	projectIdentityResolutionRefusedMessage     = "project identity resolution refused"
	projectIdentityResolutionUnavailableMessage = "project identity resolution unavailable"
)

var projectIdentityV3RegistrationSchema = json.RawMessage(`{"type":"object","additionalProperties":false}`)

func daemonComparisonContextV3(ctx context.Context) context.Context {
	outgoing, _ := metadata.FromOutgoingContext(ctx)
	outgoing = outgoing.Copy()
	if len(outgoing.Get("x-request-id")) != 1 {
		outgoing.Set("x-request-id", uuid.NewString())
	}
	outgoing.Set("x-engram-project-identity-adapter", "daemon")
	return metadata.NewOutgoingContext(ctx, outgoing)
}

// ProxyTools fetches the dynamic tool set from the engram server via a gRPC
// Initialize handshake. Implements module.ProxyToolProvider per FR-11a.
//
// Ported verbatim from cmd/engram/main.go handleToolsList v4.2.0 — the
// translation from pb.ToolDefinition to module.ToolDef preserves the exact
// shape the CC client expects.
//
// Missing both supported server URL variables is intentional offline mode and
// remains eligible for a static Loom-only response. Once either backend URL is
// configured, every identity, connection, or Initialize failure fails loud.
func (m *Module) ProxyTools(ctx context.Context, p muxcore.ProjectContext) ([]module.ToolDef, error) {
	serverURL, err := m.requireServerURL(p)
	if err != nil {
		// No configured server URL is the intentional offline contract: the
		// dispatcher may still return static Loom tools.
		return nil, err
	}
	token := m.envFor(p, config.EnvWorkstationToken)
	v3Identity, v3Enabled, err := m.v3Identity(p)
	if err != nil {
		return nil, &module.RequiredProxyToolsError{Cause: err}
	}

	conn, err := m.pool.getOrDialGRPC(serverURL, token)
	if err != nil {
		return nil, &module.RequiredProxyToolsError{Cause: fmt.Errorf("gRPC connect: %w", err)}
	}
	client := pb.NewEngramServiceClient(conn)
	discoveryCtx, cancel := context.WithTimeout(ctx, proxyToolsDiscoveryTimeout)
	defer cancel()

	request := &pb.InitializeRequest{ClientName: "engram-daemon", ClientVersion: daemonClientVersion}
	unscopedUCIDiscovery := v3Enabled && v3Identity == nil
	if v3Enabled && !unscopedUCIDiscovery {
		request.ProjectIdentityV3 = v3Identity
		discoveryCtx = daemonComparisonContextV3(discoveryCtx)
	} else if !v3Enabled {
		project := m.cache.Resolve(ctx, p)
		projectIdentity, identityErr := m.cache.ResolveIdentity(ctx, p)
		if identityErr != nil {
			return nil, &module.RequiredProxyToolsError{Cause: fmt.Errorf("project identity v2: %w", identityErr)}
		}
		request.Project = project
		request.ProjectIdentity = projectIdentity
	}

	resp, err := client.Initialize(discoveryCtx, request, grpc.WaitForReady(true))
	if err != nil {
		if v3Enabled {
			if isV3OnboardingRequired(err) {
				return nil, nil
			}
			return nil, &module.RequiredProxyToolsError{Cause: v3ProxyError(err)}
		}
		return nil, &module.RequiredProxyToolsError{Cause: fmt.Errorf("gRPC Initialize: %w", err)}
	}
	if v3Enabled && !unscopedUCIDiscovery {
		if err := validateV3Resolution(resp.GetProjectResolutionV3(), resp.GetCanonicalProject(), v3Identity); err != nil {
			return nil, &module.RequiredProxyToolsError{Cause: err}
		}
	}

	tools := make([]module.ToolDef, 0, len(resp.Tools))
	for _, t := range resp.Tools {
		if t.GetName() == projectIdentityV3RegistrationTool || (unscopedUCIDiscovery && !isUCIProxyTool(t.GetName())) {
			continue
		}
		tools = append(tools, module.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchemaJson,
		})
	}
	// The proof becomes usable only after the existing response validation and
	// complete tools-list projection above have succeeded. Empty is the
	// old-server-compatible case; malformed non-empty values fail closed.
	if err := m.advisorProofs.record(serverURL, token, resp.GetAuthenticatedSubjectProofSha256()); err != nil {
		return nil, &module.RequiredProxyToolsError{Cause: err}
	}

	return tools, nil
}

// Tools returns the one stable setup tool for a V3-configured daemon. V2
// instances return no extra static tool and retain their existing surface.
// V3 dispatch is static; V2 rejects the reserved name before proxy work.
func (m *Module) Tools() []module.ToolDef {
	if m.v3ClientInstanceID == "" {
		return nil
	}
	return []module.ToolDef{{
		Name:        projectIdentityV3RegistrationTool,
		Description: "Explicitly register the current repository's V3 project identity. Takes no arguments and requires a server-authenticated master/admin identity.",
		InputSchema: projectIdentityV3RegistrationSchema,
	}}
}

// HandleTool executes the descriptor-only V3 registration setup operation.
// It never constructs a V2 selector or accepts client project authority.
func (m *Module) HandleTool(ctx context.Context, p muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error) {
	if name != projectIdentityV3RegistrationTool {
		return nil, &module.ModuleError{Code: "tool_not_found", Message: "unknown tool"}
	}
	if m.v3ClientInstanceID != "" {
		m.cache.Forget(p.ID)
	}
	if !hasNoRegistrationArguments(args) {
		return nil, &module.ModuleError{Code: "tool_input_invalid", Message: "project_identity.register_v3 accepts no arguments"}
	}

	identity, v3Enabled, err := m.v3Identity(p)
	if err != nil {
		return nil, err
	}
	if !v3Enabled {
		return nil, &module.ModuleError{Code: "PROJECT_DESCRIPTOR_UNSUPPORTED", Message: projectIdentityResolutionRefusedMessage}
	}
	if identity == nil {
		return nil, &module.ModuleError{Code: "PROJECT_ANCHOR_INVALID", Message: projectIdentityResolutionRefusedMessage}
	}
	serverURL, err := m.requireServerURL(p)
	if err != nil {
		return nil, &module.ModuleError{Code: "PROJECT_RESOLUTION_UNAVAILABLE", Message: projectIdentityResolutionUnavailableMessage}
	}
	conn, err := m.pool.getOrDialGRPC(serverURL, m.envFor(p, config.EnvWorkstationToken))
	if err != nil {
		return nil, &module.ModuleError{Code: "PROJECT_RESOLUTION_UNAVAILABLE", Message: projectIdentityResolutionUnavailableMessage}
	}
	response, err := pb.NewEngramServiceClient(conn).RegisterProjectIdentityV3(daemonComparisonContextV3(ctx),
		&pb.RegisterProjectIdentityV3Request{ProjectIdentityV3: identity}, grpc.WaitForReady(true))
	if err != nil {
		return nil, v3ProxyError(err)
	}
	resolution := response.GetProjectResolutionV3()
	if err := validateV3Resolution(resolution, resolution.GetProjectKey(), identity); err != nil {
		return nil, err
	}
	result, err := protojson.Marshal(resolution)
	if err != nil {
		return nil, &module.ModuleError{Code: "PROJECT_RESOLUTION_UNAVAILABLE", Message: projectIdentityResolutionUnavailableMessage}
	}
	return result, nil
}

func hasNoRegistrationArguments(args json.RawMessage) bool {
	if len(args) == 0 {
		return true
	}
	var fields map[string]json.RawMessage
	return json.Unmarshal(args, &fields) == nil && fields != nil && len(fields) == 0
}

// ProxyHandleTool forwards a tools/call request to the engram server via
// gRPC CallTool. Implements module.ProxyToolProvider per FR-11a.
//
// Ported from cmd/engram/main.go handleToolsCall v4.2.0. The dispatcher
// wraps the inner MCP content block returned here in the standard envelope
// `{"content": [<block>], "isError": ...}` — this method produces the inner
// block only.
//
// NFR-5 byte-identical envelope handling:
//
//	v4.2.0 produced {"type":"text","text":<string of resp.ContentJson>} as
//	the inner block AND `isError: resp.IsError` at the envelope level.
//
//	In v4.3.0 the dispatcher owns the envelope wrapping:
//	  - Happy path (resp.IsError=false): return the inner block; dispatcher
//	    wraps with isError:false.
//	  - Unhappy path (resp.IsError=true): return a *module.ProxyIsError
//	    sentinel carrying the SAME inner block; the dispatcher detects the
//	    sentinel and wraps with isError:true. End result is byte-identical
//	    to v4.2.0 both in content and in the isError boolean.
func (m *Module) ProxyHandleTool(ctx context.Context, p muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error) {
	if name == projectIdentityV3RegistrationTool {
		return nil, &module.ModuleError{Code: "PROJECT_DESCRIPTOR_UNSUPPORTED", Message: projectIdentityResolutionRefusedMessage}
	}
	sessionID, uciTool, err := m.proxyToolSession(ctx, p, name)
	if err != nil {
		return nil, err
	}
	serverURL, err := m.requireServerURL(p)
	if err != nil {
		return nil, err
	}
	conn, err := m.pool.getOrDialGRPC(serverURL, m.envFor(p, config.EnvWorkstationToken))
	if err != nil {
		return nil, fmt.Errorf("gRPC connect: %w", err)
	}
	request := &pb.CallToolRequest{ToolName: name, ArgumentsJson: args, SessionId: sessionID}
	callCtx, v3Identity, v3Enabled, err := m.proxyToolCallContext(ctx, p, request, uciTool)
	if err != nil {
		return nil, err
	}
	response, err := pb.NewEngramServiceClient(conn).CallTool(callCtx, request)
	if err != nil {
		if !uciTool && v3Enabled {
			return nil, v3ProxyError(err)
		}
		return nil, fmt.Errorf("gRPC CallTool: %w", err)
	}
	if !uciTool && v3Enabled {
		if err := validateV3Resolution(response.GetProjectResolutionV3(), response.GetCanonicalProject(), v3Identity); err != nil {
			return nil, err
		}
	}
	block, mErr := buildInnerBlock(response.ContentJson)
	if mErr != nil {
		return nil, mErr
	}
	if response.IsError {
		return nil, &module.ProxyIsError{RawContent: block}
	}
	return block, nil
}

func (m *Module) proxyToolSession(ctx context.Context, project muxcore.ProjectContext, name string) (string, bool, error) {
	uciTool := isUCIProxyTool(name)
	if !uciTool {
		return m.envFor(project, config.EnvClaudeSessionID), false, nil
	}
	sessionID, err := requireUCITransportSession(ctx)
	return sessionID, true, err
}

func (m *Module) proxyToolCallContext(ctx context.Context, project muxcore.ProjectContext, request *pb.CallToolRequest, uciTool bool) (context.Context, *pb.ProjectIdentityV3, bool, error) {
	if uciTool {
		return uciClientOutgoingContext(ctx), nil, false, nil
	}
	v3Identity, v3Enabled, err := m.v3Identity(project)
	if err != nil {
		return nil, nil, false, err
	}
	if v3Enabled {
		if v3Identity == nil {
			return nil, nil, false, &module.ModuleError{Code: "PROJECT_ANCHOR_INVALID", Message: projectIdentityResolutionRefusedMessage}
		}
		request.ProjectIdentityV3 = v3Identity
		return daemonComparisonContextV3(ctx), v3Identity, true, nil
	}
	projectSlug := m.cache.Resolve(ctx, project)
	projectIdentity, err := m.cache.ResolveIdentity(ctx, project)
	if err != nil {
		return nil, nil, false, fmt.Errorf("project identity v2: %w", err)
	}
	request.Project = projectSlug
	request.ProjectIdentity = projectIdentity
	return ctx, nil, false, nil
}

func isUCIProxyTool(name string) bool {
	switch name {
	case "codebase_context", "codebase_index", "codebase_status", "codebase_search", "codebase_read", "codebase_graph":
		return true
	default:
		return false
	}
}

// v3Identity selects V3 only when wiring supplied an explicit client instance
// reference. Once selected, it never falls through to a local V2 selector.
func (m *Module) v3Identity(p muxcore.ProjectContext) (*pb.ProjectIdentityV3, bool, error) {
	if m.v3ClientInstanceID == "" {
		return nil, false, nil
	}
	identity, err := m.cache.ResolveIdentityV3(p, m.v3ClientInstanceID)
	if err == nil {
		return identity, true, nil
	}
	var inputErr *projectIdentityV3InputError
	if errors.As(err, &inputErr) {
		return nil, true, &module.ModuleError{Code: inputErr.code, Message: projectIdentityResolutionRefusedMessage}
	}
	return nil, true, &module.ModuleError{Code: "PROJECT_RESOLUTION_UNAVAILABLE", Message: projectIdentityResolutionUnavailableMessage}
}

func isV3OnboardingRequired(err error) bool {
	grpcStatus, ok := status.FromError(err)
	if !ok || grpcStatus.Code() != codes.FailedPrecondition {
		return false
	}
	details := grpcStatus.Details()
	if len(details) != 1 {
		return false
	}
	info, ok := details[0].(*errdetails.ErrorInfo)
	return ok && info.GetDomain() == "engram.project_identity.v3" && info.GetReason() == string(projectidentity.ProjectOnboardingRequiredOutcomeV3)
}

// v3ProxyError preserves only the server's typed refusal outcome. It rejects
// every other upstream diagnostic so paths, credentials, candidates, and DB
// text cannot reach an MCP client.
func v3ProxyError(err error) error {
	if grpcStatus, ok := status.FromError(err); ok {
		for _, detail := range grpcStatus.Details() {
			info, ok := detail.(*errdetails.ErrorInfo)
			if !ok || info.GetDomain() != "engram.project_identity.v3" {
				continue
			}
			outcome := projectidentity.ResolutionOutcomeV3(info.GetReason())
			if outcome.IsRefusal() {
				return &module.ModuleError{Code: info.GetReason(), Message: projectIdentityResolutionRefusedMessage}
			}
		}
	}
	return &module.ModuleError{Code: "PROJECT_RESOLUTION_UNAVAILABLE", Message: projectIdentityResolutionUnavailableMessage}
}

// validateV3Resolution admits only a complete, server-issued canonical scope
// consistent with the submitted descriptor. The daemon does not cache or reuse
// the result as client authority.
func validateV3Resolution(resolution *pb.ProjectResolutionResultV3, canonicalProject string, identity *pb.ProjectIdentityV3) error {
	if resolution == nil || identity == nil || canonicalProject != resolution.GetProjectKey() || identity.GetScope() != resolution.GetResolvedScope() {
		return &module.ModuleError{Code: "PROJECT_RESOLUTION_UNAVAILABLE", Message: projectIdentityResolutionUnavailableMessage}
	}
	projectKey, err := projectidentity.NewProjectKeyV3(resolution.GetProjectKey())
	if err != nil {
		return &module.ModuleError{Code: "PROJECT_RESOLUTION_UNAVAILABLE", Message: projectIdentityResolutionUnavailableMessage}
	}
	correlation, err := projectidentity.NewCorrelationV3(resolution.GetCorrelation())
	if err != nil {
		return &module.ModuleError{Code: "PROJECT_RESOLUTION_UNAVAILABLE", Message: projectIdentityResolutionUnavailableMessage}
	}
	if _, err := projectidentity.NewSuccessResultV3(
		projectidentity.ResolveExistingIntentV3,
		projectidentity.ResolutionOutcomeV3(resolution.GetOutcome().String()),
		projectKey,
		projectidentity.ResolvedScopeV3(resolution.GetResolvedScope()),
		projectidentity.RedirectReferenceV3(resolution.GetRedirectReference()),
		correlation,
	); err != nil {
		return &module.ModuleError{Code: "PROJECT_RESOLUTION_UNAVAILABLE", Message: projectIdentityResolutionUnavailableMessage}
	}
	return nil
}

// buildInnerBlock wraps the server-provided content bytes in the standard
// MCP text content block shape. Extracted for test coverage of the
// byte-identity contract — the block format is non-obvious and covers the
// v4.2.0 behaviour of calling string(resp.ContentJson) to embed the bytes
// as an MCP text payload.
//
// The helper returns an error only on json.Marshal failure, which should
// never happen for the shapes we produce but is surfaced for caller clarity.
func buildInnerBlock(contentJSON []byte) (json.RawMessage, error) {
	block := map[string]any{
		"type": "text",
		"text": string(contentJSON),
	}
	return json.Marshal(block)
}

// daemonClientVersion is the ClientVersion string sent in gRPC
// InitializeRequest. Bumped alongside Constitution §15 unified version.
var daemonClientVersion = version.Daemon

func resolveProjectIdentityV2(ctx context.Context, cwd string) (*pb.ProjectIdentityV2, error) {
	identity, err := proxy.ResolveProjectIdentityV2(ctx, cwd)
	if err != nil {
		return nil, err
	}
	return &pb.ProjectIdentityV2{
		Version:         identity.Version,
		LegacyProjectId: identity.LegacyProjectID,
		DisplayName:     identity.DisplayName,
		GitRemote:       identity.GitRemote,
		RelativePath:    identity.RelativePath,
		NonGitAnchor:    identity.NonGitAnchor,
		AnchorShared:    identity.AnchorShared,
	}, nil
}
