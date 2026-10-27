package engramcore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/uci"
	"github.com/thebtf/engram/internal/version"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const proxyToolsDiscoveryTimeout = 30 * time.Second

const projectIdentityResolutionRefusedMessage = "project identity resolution refused"

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
	discoveryCtx, cancel := context.WithTimeout(ctx, proxyToolsDiscoveryTimeout)
	defer cancel()

	conn, err := m.pool.getOrDialGRPC(serverURL, token)
	if err != nil {
		return nil, &module.RequiredProxyToolsError{Cause: fmt.Errorf("gRPC connect: %w", err)}
	}
	client := pb.NewEngramServiceClient(conn)

	// The catalogue is independent of local project state.
	request := &pb.InitializeRequest{ClientName: "engram-daemon", ClientVersion: daemonClientVersion}
	resp, err := client.Initialize(discoveryCtx, request, grpc.WaitForReady(true))
	if err != nil {
		return nil, &module.RequiredProxyToolsError{Cause: fmt.Errorf("gRPC Initialize: %w", err)}
	}

	tools := make([]module.ToolDef, 0, len(resp.Tools))
	for _, t := range resp.Tools {
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
	sessionID, uciTool, err := m.proxyToolSession(ctx, p, name)
	if err != nil {
		return nil, err
	}
	if name == "codebase_context" {
		args, err = m.prepareNativeCodebaseRegistration(ctx, p, args)
		if err != nil {
			return nil, err
		}
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
	callCtx, err := m.proxyToolCallContext(ctx, p, request, uciTool)
	if err != nil {
		return nil, err
	}
	if name == "codebase_context" && m.registrationParserAvailable {
		callCtx = metadata.AppendToOutgoingContext(callCtx, "x-engram-verified-parser-bundle", string(uci.TreeSitterSemanticContractDigest()))
	}
	response, err := pb.NewEngramServiceClient(conn).CallTool(callCtx, request)
	if err != nil {
		return nil, fmt.Errorf("gRPC CallTool: %w", err)
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

// ConfigureRegistrationParser marks a verified installed parser sibling available
// for first-use registration; selection remains server-owned.
func (m *Module) ConfigureRegistrationParser() {
	m.registrationParserAvailable = true
}

func (m *Module) proxyToolSession(ctx context.Context, project muxcore.ProjectContext, name string) (string, bool, error) {
	uciTool := isUCIProxyTool(name)
	if !uciTool {
		return m.envFor(project, config.EnvClaudeSessionID), false, nil
	}
	sessionID, err := requireUCITransportSession(ctx)
	return sessionID, true, err
}

func (m *Module) proxyToolCallContext(ctx context.Context, project muxcore.ProjectContext, request *pb.CallToolRequest, uciTool bool) (context.Context, error) {
	if uciTool {
		var err error
		ctx, err = uciClientOutgoingContext(ctx, m.v3ClientInstanceID)
		if err != nil {
			return nil, err
		}
	}
	if !projectidentity.ToolRequiresProject(request.ToolName, request.ArgumentsJson) {
		return ctx, nil
	}
	serverURL, err := m.requireServerURL(project)
	if err != nil {
		return nil, err
	}
	resolved, err := projectidentity.ResolveLogicalProject(ctx, project.Cwd,
		projectidentity.HTTPBootstrap(serverURL, m.envFor(project, config.EnvWorkstationToken)))
	if err != nil {
		return nil, &module.ModuleError{Code: projectidentity.ProjectErrorCode(err), Message: err.Error()}
	}
	request.Project = resolved.ProjectID
	return ctx, nil
}

func isUCIProxyTool(name string) bool {
	switch name {
	case "codebase_context", "codebase_index", "codebase_status", "codebase_search", "codebase_read", "codebase_graph":
		return true
	default:
		return false
	}
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
