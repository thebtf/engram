package engramcore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/thebtf/engram/internal/module"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// ProxyTools fetches the dynamic tool set from the engram server via a gRPC
// Initialize handshake. Implements module.ProxyToolProvider per FR-11a.
//
// Ported verbatim from cmd/engram/main.go handleToolsList v4.2.0 — the
// translation from pb.ToolDefinition to module.ToolDef preserves the exact
// shape the CC client expects.
//
// Graceful degradation: on backend error, return the error to the dispatcher
// which will log a warning and omit dynamic tools from the tools/list
// response. The static tool list (empty in v4.3.0) is returned regardless.
func (m *Module) ProxyTools(ctx context.Context, p muxcore.ProjectContext) ([]module.ToolDef, error) {
	serverURL, err := m.requireServerURL(p)
	if err != nil {
		return nil, err
	}
	token := m.envFor(p, "ENGRAM_AUTH_ADMIN_TOKEN")
	project := m.cache.Resolve(p)

	conn, err := m.pool.getOrDialGRPC(serverURL, token)
	if err != nil {
		return nil, fmt.Errorf("gRPC connect: %w", err)
	}
	client := pb.NewEngramServiceClient(conn)

	resp, err := client.Initialize(ctx, &pb.InitializeRequest{
		ClientName:    "engram-daemon",
		ClientVersion: daemonClientVersion,
		Project:       project,
	})
	if err != nil {
		return nil, fmt.Errorf("gRPC Initialize: %w", err)
	}

	tools := make([]module.ToolDef, len(resp.Tools))
	for i, t := range resp.Tools {
		tools[i] = module.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchemaJson,
		}
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
	serverURL, err := m.requireServerURL(p)
	if err != nil {
		return nil, err
	}
	token := m.envFor(p, "ENGRAM_AUTH_ADMIN_TOKEN")
	project := m.cache.Resolve(p)

	conn, err := m.pool.getOrDialGRPC(serverURL, token)
	if err != nil {
		return nil, fmt.Errorf("gRPC connect: %w", err)
	}
	client := pb.NewEngramServiceClient(conn)

	resp, err := client.CallTool(ctx, &pb.CallToolRequest{
		ToolName:      name,
		ArgumentsJson: args,
		Project:       project,
	})
	if err != nil {
		return nil, fmt.Errorf("gRPC CallTool: %w", err)
	}

	block, mErr := buildInnerBlock(resp.ContentJson)
	if mErr != nil {
		return nil, mErr
	}

	if resp.IsError {
		// Sentinel path: dispatcher detects *module.ProxyIsError and emits
		// the raw inner block with isError:true, preserving byte-identity
		// with v4.2.0's error envelope.
		return nil, &module.ProxyIsError{RawContent: block}
	}
	return block, nil
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
const daemonClientVersion = "v5.0.0"
