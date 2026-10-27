package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInvalidIdentityRecoveryShippedDiscovery(t *testing.T) {
	fixture := newUCICodebaseContextFixture(t)
	response := fixture.server.HandleRequest(fixture.clientA, &Request{JSONRPC: "2.0", ID: 508, Method: "tools/list", Params: json.RawMessage(`{"include_all":true}`)})
	require.Nil(t, response.Error)
	raw, err := json.Marshal(response.Result)
	require.NoError(t, err)
	var catalog struct {
		Tools []Tool `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(raw, &catalog))
	for _, tool := range catalog.Tools {
		if tool.Name != "codebase_context" {
			continue
		}
		require.Contains(t, tool.Description, "legacy_marker")
		require.Contains(t, tool.Description, "GET /api/projects/resolve?legacy_id=")
		require.Contains(t, tool.Description, "URI-encoded")
		require.Contains(t, tool.Description, "PROJECT_IDENTITY_INVALID")
		require.Contains(t, tool.Description, "not a mapping")
		return
	}
	t.Fatal("actual tools/list omitted codebase_context")
}
