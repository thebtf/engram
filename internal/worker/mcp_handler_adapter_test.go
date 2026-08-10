package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

func TestMCPHandlerAdapter_ReadOnlyMutationIsToolErrorOverGRPC(t *testing.T) {
	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "test"})
	_, grpcServer := grpcserver.New(&mcpHandlerAdapter{mcpServer: mcpServer}, nil)
	ctx := auth.WithIdentity(context.Background(), auth.Client("read-only", "keycard-read-only"))

	response, err := grpcServer.CallTool(ctx, &pb.CallToolRequest{
		ToolName:      "docs",
		ArgumentsJson: []byte(`{"action":"create","content":"api_key=must-not-appear"}`),
	})
	require.NoError(t, err)
	require.True(t, response.IsError)

	var toolError struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.ContentJson, &toolError))
	assert.Equal(t, -32000, toolError.Code)
	assert.Equal(t, "Tool error: read_only: action is not permitted", toolError.Message)
	assert.Equal(t, "read_only: action is not permitted", toolError.Data)
	assert.NotContains(t, string(response.ContentJson), "must-not-appear")
}
