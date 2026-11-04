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
		require.Contains(t, tool.Description, "PROJECT_MIGRATION_REQUIRED")
		require.Contains(t, tool.Description, "automatically recover a strict historical id/name marker")
		require.Contains(t, tool.Description, "authorized GET /api/projects/resolve?legacy_id=<encoded-exact-id>")
		require.Contains(t, tool.Description, "local project_context tool with action=\"choices\"")
		require.Contains(t, tool.Description, "exact fresh selection_ref")
		require.Contains(t, tool.Description, "do not pick first/latest, by name alone or merely because one result exists")
		require.Contains(t, tool.Description, "not a guessed terminal cwd")
		require.Contains(t, tool.Description, "never bypass permissions")
		require.Contains(t, tool.Description, "not a server binding")
		require.Contains(t, tool.Description, "conflicting or invalid existing identity records remain preserved for authorized owner repair")
		require.Contains(t, tool.Description, "Never replay a mutation after response loss or an unknown acceptance outcome")
		return
	}
	t.Fatal("actual tools/list omitted codebase_context")
}
