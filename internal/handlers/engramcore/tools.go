package engramcore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/uci"
	"github.com/thebtf/engram/internal/version"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/metadata"
)

const proxyToolsDiscoveryTimeout = 30 * time.Second

const projectIdentityResolutionRefusedMessage = "project identity resolution refused"

var (
	offlineCatalogOnce sync.Once
	offlineCatalog     []module.ToolDef
	offlineCatalogErr  error
)

// ProxyTools returns local schemas without waiting for authenticated discovery.
func (m *Module) ProxyTools(ctx context.Context, p muxcore.ProjectContext) ([]module.ToolDef, error) {
	if err := ctx.Err(); err != nil {
		return nil, &module.RequiredProxyToolsError{Cause: err}
	}
	fallback, err := supportedOfflineTools()
	if err != nil {
		return nil, err
	}
	serverURL, err := m.requireServerURL(p)
	if err != nil {
		return slices.Clone(fallback), nil
	}
	token := m.envFor(p, config.EnvWorkstationToken)
	key, err := advisorProofKeyFor(serverURL, token)
	if err != nil {
		return slices.Clone(fallback), nil
	}
	m.refreshMu.Lock()
	live := slices.Clone(m.liveCatalog[key])
	m.refreshMu.Unlock()
	m.refreshDiscovery(serverURL, token, p.ID)
	known := make(map[string]bool, len(live))
	for i := range live {
		live[i].Description = "Previously discovered remote schema; current availability and authorization are unverified. " + live[i].Description
		known[live[i].Name] = true
	}
	for _, tool := range fallback {
		if !known[tool.Name] {
			live = append(live, tool)
		}
	}
	return live, nil
}

func supportedOfflineTools() ([]module.ToolDef, error) {
	offlineCatalogOnce.Do(func() {
		for _, tool := range mcp.SupportedToolCatalog() {
			schema, err := json.Marshal(tool.InputSchema)
			if err != nil {
				offlineCatalogErr = &module.RequiredProxyToolsError{Cause: err}
				return
			}
			offlineCatalog = append(offlineCatalog, module.ToolDef{Name: tool.Name, Description: "Local schema only; remote support and authorization are unverified and must be checked by the backend. " + tool.Description, InputSchema: schema})
		}
	})
	return offlineCatalog, offlineCatalogErr
}

func (m *Module) refreshDiscovery(serverURL, token, projectID string) {
	key, err := advisorProofKeyFor(serverURL, token)
	if err != nil {
		return
	}
	m.refreshMu.Lock()
	if m.refreshCtx == nil || m.refreshCtx.Err() != nil {
		m.refreshMu.Unlock()
		return
	}
	projects := m.refreshing[key]
	if projects == nil {
		if len(m.refreshing) >= maxAdvisorSubjectProofCacheItems {
			m.refreshMu.Unlock()
			return
		}
		projects = make(map[string]bool)
		m.refreshing[key] = projects
	}
	if projects[projectID] || len(projects) >= maxAdvisorSubjectProofCacheItems {
		m.refreshMu.Unlock()
		return
	}
	projects[projectID] = true
	ctx := m.refreshCtx
	m.refreshWG.Add(1)
	m.refreshMu.Unlock()
	go func() {
		defer m.refreshWG.Done()
		defer func() {
			m.refreshMu.Lock()
			delete(m.refreshing[key], projectID)
			if len(m.refreshing[key]) == 0 {
				delete(m.refreshing, key)
			}
			m.refreshMu.Unlock()
		}()
		delay := time.Second
		for ctx.Err() == nil {
			conn, err := m.pool.getOrDialGRPC(serverURL, token)
			if err == nil {
				callCtx, cancel := context.WithTimeout(ctx, proxyToolsDiscoveryTimeout)
				resp, callErr := pb.NewEngramServiceClient(conn).Initialize(callCtx, &pb.InitializeRequest{ClientName: "engram-daemon", ClientVersion: daemonClientVersion}, grpc.WaitForReady(true))
				cancel()
				if callErr == nil {
					tools := make([]module.ToolDef, 0, len(resp.Tools))
					valid := true
					for _, tool := range resp.Tools {
						if tool == nil || tool.Name == "" || !json.Valid(tool.InputSchemaJson) {
							valid = false
							break
						}
						tools = append(tools, module.ToolDef{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchemaJson})
					}
					if valid && m.advisorProofs.record(serverURL, token, resp.GetAuthenticatedSubjectProofSha256()) == nil {
						m.refreshMu.Lock()
						if _, exists := m.liveCatalog[key]; exists || len(m.liveCatalog) < maxAdvisorSubjectProofCacheItems {
							m.liveCatalog[key] = tools
						}
						notifier := m.notifier
						m.refreshMu.Unlock()
						if notifier != nil {
							_ = notifier.Notify(projectID, []byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`))
						}
						delay = time.Second
						for conn.GetState() == connectivity.Ready && conn.WaitForStateChange(ctx, connectivity.Ready) {
						}
					}
				}
			}
			_ = m.advisorProofs.record(serverURL, token, nil)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if delay < 8*time.Second {
				delay *= 2
			}
		}
	}()
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
		if code := projectidentity.ProjectErrorCode(err); request.ToolName == "recall_memory" && (code == "PROJECT_IDENTITY_CONFLICT" || code == "PROJECT_MIGRATION_REQUIRED") {
			id, bindingErr := m.conflictSafeRead(ctx, project, serverURL, m.envFor(project, config.EnvWorkstationToken), request.ArgumentsJson)
			if bindingErr != nil {
				return nil, projectIdentityRefusal(bindingErr, project.Cwd)
			}
			if id != "" {
				request.Project = id
				return ctx, nil
			}
		}
		return nil, projectIdentityRefusal(err, project.Cwd)
	}
	request.Project = resolved.ProjectID
	return ctx, nil
}

func projectIdentityRefusal(err error, cwd string) *module.ModuleError {
	code := projectidentity.ProjectErrorCode(err)
	guidance := projectidentity.RecoveryGuidance(code)
	if guidance == "" {
		guidance = projectidentity.RecoveryGuidance("PROJECT_RESOLUTION_UNAVAILABLE")
	}
	return &module.ModuleError{Code: code, Message: fmt.Sprintf("%s: %s caller_root_sha256=%x; SHA-256 of exact UTF-8 Cwd bytes, path withheld. Compare only known own caller roots locally; a match grants no selection or migration authority.", code, guidance, sha256.Sum256([]byte(cwd)))}
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
