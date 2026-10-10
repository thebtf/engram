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

func TestApplyPrincipalMemoryMetadata_Visibility(t *testing.T) {
	t.Parallel()
	for _, identity := range []struct {
		name  string
		ctx   context.Context
		owner string
	}{
		{"missing_identity", context.Background(), ""},
		{"auth_disabled", auth.WithIdentity(context.Background(), auth.AuthDisabled()), ""},
		{"unowned_keycard", auth.WithIdentity(context.Background(), auth.Client("read-write", "keycard-unowned")), ""},
		{"owned_keycard", auth.WithIdentity(context.Background(), auth.ClientWithPrincipal("read-write", "keycard-owned", "agent/alice", auth.PrincipalKindAgent)), "agent/alice"},
	} {
		for _, visibility := range []struct {
			name  string
			value string
		}{
			{"omitted", ""},
			{"private", models.AgentVisibilityPrivate},
			{"shared", models.AgentVisibilityShared},
		} {
			t.Run(identity.name+"/"+visibility.name, func(t *testing.T) {
				workstation := ""
				if id, ok := auth.IdentityFrom(identity.ctx); ok {
					workstation = id.WorkstationID()
				}
				mem := &models.Memory{
					Project:             "testproj",
					PrivacyScope:        "project",
					SourceWorkstationID: workstation,
					SourceSessions:      []string{"session-fixture"},
				}
				err := applyPrincipalMemoryMetadata(identity.ctx, mem, visibility.value, "")
				if identity.owner == "" && visibility.value != "" {
					require.Error(t, err)
					require.Contains(t, err.Error(), "invalid_agent_visibility:")
					require.Empty(t, mem.AgentVisibility)
				} else {
					require.NoError(t, err)
					wantVisibility := visibility.value
					if identity.owner != "" && wantVisibility == "" {
						wantVisibility = models.AgentVisibilityShared
					}
					require.Equal(t, wantVisibility, mem.AgentVisibility)
				}
				require.Equal(t, identity.owner, mem.OwnerPrincipal)
				if identity.owner == "" {
					require.Empty(t, mem.OwnerPrincipalKind)
				} else {
					require.Equal(t, "agent", mem.OwnerPrincipalKind)
				}
				require.Empty(t, mem.Domain)
				require.Equal(t, "testproj", mem.Project)
				require.Equal(t, "project", mem.PrivacyScope)
				require.Equal(t, workstation, mem.SourceWorkstationID)
				require.Equal(t, []string{"session-fixture"}, mem.SourceSessions)
			})
		}
	}
}
