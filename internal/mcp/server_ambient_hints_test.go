package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
)

func TestHandleRequest_GetAmbientHintsDispatchesThroughToolsCall(t *testing.T) {
	setS3AmbientFlags(t, true, true)
	queue := cognitivecore.NewHintQueue()
	seedAmbientHintQueue(t, queue, "session-s3-dispatch",
		cognitivecore.HintProposalPayload{ID: "dispatch-1", Title: "Dispatch should reach get_ambient_hints", Reason: "tag:dispatch", Score: 0.91, Source: "s2.meta_index", CreatedAt: time.Now().UTC()},
	)

	srv := NewServer(ServerOptions{Version: "s3-red-test"})
	srv.SetHintQueue(queue)

	resp := srv.HandleRequest(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params: mustJSON(t, map[string]any{
			"name": "get_ambient_hints",
			"arguments": map[string]any{
				"session_id": "session-s3-dispatch",
				"limit":      3,
			},
		}),
	})

	require.NotNil(t, resp)
	require.Nil(t, resp.Error)
	result, ok := resp.Result.(map[string]any)
	require.True(t, ok, "tools/call must return the standard MCP content envelope")
	content, ok := result["content"].([]map[string]any)
	require.True(t, ok, "MCP result must expose text content")
	require.Len(t, content, 1)
	text, ok := content[0]["text"].(string)
	require.True(t, ok)

	payload := decodeAmbientHintsToolResponse(t, text)
	require.Len(t, payload.Hints, 1)
	require.Equal(t, "dispatch-1", payload.Hints[0].ID)
	require.Equal(t, "Dispatch should reach get_ambient_hints", payload.Hints[0].Title)
}

func TestHandleRequest_GetAmbientHintsUnknownToolRegressionGuard(t *testing.T) {
	setS3AmbientFlags(t, false, false)
	srv := NewServer(ServerOptions{Version: "s3-red-test"})
	resp := srv.HandleRequest(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params: mustJSON(t, map[string]any{
			"name": "get_ambient_hints",
			"arguments": map[string]any{
				"session_id": "session-disabled",
			},
		}),
	})

	require.NotNil(t, resp)
	require.Nil(t, resp.Error, "disabled S3 fallback must fail open to an empty JSON payload, not an unknown-tool transport error")
	result := resp.Result.(map[string]any)
	content := result["content"].([]map[string]any)
	text := content[0]["text"].(string)
	var payload ambientHintsToolResponse
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.Empty(t, payload.Hints)
}
