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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const proxyToolsDiscoveryTimeout = 30 * time.Second

const projectIdentityResolutionRefusedMessage = "project identity resolution refused"

type discoveredCatalog struct {
	version string
	tools   []module.ToolDef
}

var (
	offlineCatalogOnce sync.Once
	offlineCatalog     []module.ToolDef
	offlineCatalogErr  error
)

// ProxyTools returns local schemas without waiting for remote discovery.
// Authenticated remote schemas and subject proof are refreshed asynchronously.
func (m *Module) ProxyTools(ctx context.Context, p muxcore.ProjectContext) ([]module.ToolDef, error) {
	serverURL, err := m.requireServerURL(p)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, &module.RequiredProxyToolsError{Cause: err}
	}
	fallback, err := supportedOfflineTools()
	if err != nil {
		return nil, err
	}
	token := m.envFor(p, config.EnvWorkstationToken)
	key, err := advisorProofKeyFor(serverURL, token)
	if err != nil {
		return slices.Clone(fallback), nil
	}
	m.refreshMu.Lock()
	cached := m.liveCatalog[key]
	live := append([]module.ToolDef(nil), cached.tools...)
	m.refreshMu.Unlock()
	m.refreshDiscovery(serverURL, token, p.ID)
	for i := range live {
		live[i].Description = "Previously discovered remote schema; current availability and authorization are unverified. " + live[i].Description
	}
	known := make(map[string]bool, len(live))
	for _, tool := range live {
		known[tool.Name] = true
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
		catalog := mcp.SupportedToolCatalog()
		offlineCatalog = make([]module.ToolDef, 0, len(catalog))
		for _, tool := range catalog {
			schema, err := json.Marshal(tool.InputSchema)
			if err != nil {
				offlineCatalogErr = &module.RequiredProxyToolsError{Cause: err}
				return
			}
			offlineCatalog = append(offlineCatalog, module.ToolDef{
				Name:        tool.Name,
				Description: "Local schema only; remote support and authorization are unverified and must be checked by the backend. " + tool.Description,
				InputSchema: schema,
			})
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
	if m.refreshCtx == nil || m.refreshCtx.Err() != nil || m.terminalDiscovery[key] {
		m.refreshMu.Unlock()
		return
	}
	if _, exists := m.discoveryGeneration[key]; !exists {
		if len(m.discoveryGeneration) >= maxAdvisorSubjectProofCacheItems {
			m.refreshMu.Unlock()
			return
		}
		m.discoveryGeneration[key] = 0
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
	m.refreshWG.Add(1)
	ctx := m.refreshCtx
	notifier := m.notifier
	m.refreshMu.Unlock()
	go func() {
		defer m.refreshWG.Done()
		defer func() {
			m.refreshMu.Lock()
			delete(m.refreshing[key], projectID)
			if len(m.refreshing[key]) == 0 {
				delete(m.refreshing, key)
			}
			m.signalDiscoveryUpdateLocked(key)
			m.refreshMu.Unlock()
		}()
		delay := time.Second
		for {
			if ctx.Err() != nil {
				return
			}
			conn, err := m.pool.getOrDialGRPC(serverURL, token)
			if err != nil {
				m.markDiscoveryTerminal(key)
				if !waitDiscoveryRetry(ctx, &delay) {
					return
				}
				continue
			}
			m.refreshMu.Lock()
			generation := m.discoveryGeneration[key]
			m.refreshMu.Unlock()
			callCtx, cancel := context.WithTimeout(ctx, proxyToolsDiscoveryTimeout)
			resp, err := pb.NewEngramServiceClient(conn).Initialize(callCtx, &pb.InitializeRequest{ClientName: "engram-daemon", ClientVersion: daemonClientVersion}, grpc.WaitForReady(true))
			cancel()
			if err == nil {
				tools := make([]module.ToolDef, 0, len(resp.Tools))
				valid := true
				for _, tool := range resp.Tools {
					if tool == nil || tool.Name == "" || !json.Valid(tool.InputSchemaJson) {
						valid = false
						break
					}
					tools = append(tools, module.ToolDef{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchemaJson})
				}
				if !valid {
					m.markDiscoveryTerminal(key)
					if !waitDiscoveryRetry(ctx, &delay) {
						return
					}
					continue
				}
				m.refreshMu.Lock()
				if generation != m.discoveryGeneration[key] {
					m.refreshMu.Unlock()
					continue
				}
				if m.advisorProofs.record(serverURL, token, resp.GetAuthenticatedSubjectProofSha256()) != nil {
					m.refreshMu.Unlock()
					m.markDiscoveryTerminal(key)
					if !waitDiscoveryRetry(ctx, &delay) {
						return
					}
					continue
				}
				delete(m.terminalDiscovery, key)
				delay = time.Second
				if len(m.liveCatalog) < maxAdvisorSubjectProofCacheItems {
					m.liveCatalog[key] = discoveredCatalog{version: resp.GetServerVersion(), tools: tools}
				}
				m.signalDiscoveryUpdateLocked(key)
				signal := m.discoverySignals[key]
				if signal == nil {
					signal = make(chan struct{})
					m.discoverySignals[key] = signal
				}
				m.refreshMu.Unlock()
				if notifier != nil {
					_ = notifier.Notify(projectID, []byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`))
				}
				watchCtx, cancelWatch := context.WithCancel(ctx)
				go func() {
					select {
					case <-signal:
						cancelWatch()
					case <-watchCtx.Done():
					}
				}()
				for conn.GetState() == connectivity.Ready && conn.WaitForStateChange(watchCtx, connectivity.Ready) {
				}
				cancelWatch()
				if ctx.Err() != nil {
					return
				}
				m.invalidateDiscoveryGeneration(key, serverURL, token, generation)
				continue
			}
			m.refreshMu.Lock()
			m.signalDiscoveryUpdateLocked(key)
			m.refreshMu.Unlock()
			if status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded {
				m.markDiscoveryTerminal(key)
			}
			if !waitDiscoveryRetry(ctx, &delay) {
				return
			}
		}
	}()
}

func waitDiscoveryRetry(ctx context.Context, delay *time.Duration) bool {
	timer := time.NewTimer(*delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
	}
	if *delay < 8*time.Second {
		*delay *= 2
	}
	return true
}

func (m *Module) markDiscoveryTerminal(key advisorProofKey) {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	if len(m.terminalDiscovery) < maxAdvisorSubjectProofCacheItems {
		m.terminalDiscovery[key] = true
	}
	m.signalDiscoveryUpdateLocked(key)
}

func (m *Module) invalidateDiscoveryGeneration(key advisorProofKey, serverURL, token string, generation uint64) {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	if m.discoveryGeneration[key] == generation {
		m.invalidateDiscoveryLocked(key, serverURL, token)
	}
}

func (m *Module) invalidateDiscovery(key advisorProofKey, serverURL, token string) {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	m.invalidateDiscoveryLocked(key, serverURL, token)
}

func (m *Module) signalDiscoveryUpdateLocked(key advisorProofKey) {
	if signal := m.discoveryUpdates[key]; signal != nil {
		close(signal)
		delete(m.discoveryUpdates, key)
	}
}

func (m *Module) invalidateDiscoveryLocked(key advisorProofKey, serverURL, token string) {
	delete(m.liveCatalog, key)
	m.signalDiscoveryUpdateLocked(key)
	delete(m.terminalDiscovery, key)
	if signal := m.discoverySignals[key]; signal != nil {
		close(signal)
		delete(m.discoverySignals, key)
	}
	if _, ok := m.discoveryGeneration[key]; ok || len(m.discoveryGeneration) < maxAdvisorSubjectProofCacheItems {
		m.discoveryGeneration[key]++
	}
	_ = m.advisorProofs.record(serverURL, token, nil)
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
	originalArgs := args
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
	request := &pb.CallToolRequest{ToolName: name, ArgumentsJson: args, SessionId: sessionID}
	callCtx, err := m.proxyToolCallContext(ctx, p, request, uciTool)
	if err != nil {
		return nil, err
	}
	token := m.envFor(p, config.EnvWorkstationToken)
	guarded := !unconditionallySafeHealth(name, originalArgs)
	conn, err := m.pool.getOrDialGRPC(serverURL, token)
	if err != nil {
		if guarded {
			return nil, capabilityNotSent("Backend connection setup failed.")
		}
		return nil, fmt.Errorf("gRPC connect: %w", err)
	}
	if guarded {
		if err := m.checkProxyCapability(ctx, conn, serverURL, token, p.ID, name, originalArgs, args); err != nil {
			return nil, err
		}
	}
	if name == "codebase_context" && m.registrationParserAvailable {
		callCtx = metadata.AppendToOutgoingContext(callCtx, "x-engram-verified-parser-bundle", string(uci.TreeSitterSemanticContractDigest()))
	}
	if guarded && callCtx.Err() != nil {
		return nil, capabilityNotSent("The caller cancelled before dispatch.")
	}
	response, err := pb.NewEngramServiceClient(conn).CallTool(callCtx, request)
	if err != nil && (status.Code(err) == codes.Unavailable || status.Code(err) == codes.DeadlineExceeded) {
		token := m.envFor(p, config.EnvWorkstationToken)
		if key, keyErr := advisorProofKeyFor(serverURL, token); keyErr == nil {
			m.invalidateDiscovery(key, serverURL, token)
			m.refreshDiscovery(serverURL, token, p.ID)
		}
	}
	if err != nil {
		if guarded {
			return nil, &module.ModuleError{Code: "OUTCOME_UNKNOWN", Message: "The backend tool request was dispatched but its outcome is unknown. Inspect its outcome before any retry; do not replay automatically."}
		}
		return nil, fmt.Errorf("gRPC CallTool: %w", err)
	}
	token = m.envFor(p, config.EnvWorkstationToken)
	if key, keyErr := advisorProofKeyFor(serverURL, token); keyErr == nil {
		m.refreshMu.Lock()
		delete(m.terminalDiscovery, key)
		m.refreshMu.Unlock()
		m.refreshDiscovery(serverURL, token, p.ID)
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
		return project.Env[config.EnvClaudeSessionID], false, nil
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
	resolved, err := projectidentity.ResolveLogicalProjectWithRecovery(ctx, projectidentity.ProjectContextCaller{
		Cwd: project.Cwd, CallerID: m.v3ClientInstanceID, ServerURL: serverURL, Token: m.envFor(project, config.EnvWorkstationToken),
	})
	if err != nil {
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
