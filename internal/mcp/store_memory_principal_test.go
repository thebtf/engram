package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/pkg/models"
)

func TestStoreMemory_PrincipalOwnerDerivedFromIdentity(t *testing.T) {
	project := "test-mcp-memory-principal-" + uuid.NewString()
	env := newMemoryServerForT007(t, project)

	args := mustJSON(t, map[string]any{
		"content":          "MCP principal-owned memory",
		"project":          project,
		"owner_principal":  "agent/spoofed",
		"agent_visibility": "private",
		"domain":           "memory-lab",
	})
	id := auth.ClientWithPrincipal("read-write", "keycard-mcp-principal", "agent/jeeves", auth.PrincipalKindAgent)
	ctx := auth.WithIdentity(context.Background(), id)

	out, err := env.srv.handleStoreMemory(ctx, args)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.Equal(t, "agent/jeeves", resp["owner_principal"])
	require.Equal(t, "agent", resp["owner_principal_kind"])
	require.Equal(t, models.AgentVisibilityPrivate, resp["agent_visibility"])
	require.Equal(t, "memory-lab", resp["domain"])
	require.NotEqual(t, "agent/spoofed", resp["owner_principal"])

	createdID, ok := resp["id"].(float64)
	require.True(t, ok)
	got, err := env.srv.memoryStore.Get(context.Background(), int64(createdID))
	require.NoError(t, err)
	require.Equal(t, "agent/jeeves", got.OwnerPrincipal)
	require.Equal(t, "agent", got.OwnerPrincipalKind)
	require.Equal(t, models.AgentVisibilityPrivate, got.AgentVisibility)
	require.Equal(t, "memory-lab", got.Domain)
}
