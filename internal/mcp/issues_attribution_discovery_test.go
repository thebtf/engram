package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIssuesSchemaAdvertisesReportedHostProvenance(t *testing.T) {
	fixture := newUCICodebaseContextFixture(t)
	var matches []Tool
	for _, tool := range fixture.server.ListTools() {
		if tool.Name == "issues" {
			matches = append(matches, tool)
		}
	}
	require.Len(t, matches, 1, "the actual native discovery catalog must contain one issues tool")
	schema := matches[0].InputSchema
	assert.Equal(t, "issues/v1", schema["x-engram-caller-context"], "the capability opts into reported provenance, not authentication")
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	for _, name := range []string{"session_id", "client"} {
		t.Run(name, func(t *testing.T) {
			field, ok := properties[name].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "string", field["type"])
		})
	}
}
