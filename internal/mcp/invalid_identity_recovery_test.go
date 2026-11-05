package mcp

import (
	"encoding/json"
	"testing"

	"github.com/thebtf/engram/internal/projectidentity"

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
		require.Contains(t, tool.Description, "project_context")
		require.Contains(t, tool.Description, `action="repair"`)
		require.Contains(t, tool.Description, `action="choices"`)
		require.Contains(t, tool.Description, "not a mapping")
		require.Contains(t, tool.Description, "never replay an unknown-outcome mutation")
		invalid := projectidentity.RecoveryGuidance("PROJECT_IDENTITY_INVALID")
		require.Contains(t, invalid, "malformed IDs")
		require.Contains(t, invalid, "do not delete/reset/init")
		return
	}
	t.Fatal("actual tools/list omitted codebase_context")
}
