package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/pkg/models"
)

// Public MCP edit may either preserve the typed card's prior version atomically
// or reject the unsupported edit without mutation. It must never silently
// overwrite a typed body without a revision.
func TestB1MCPWritePreservesTypedHistoryPostgres(t *testing.T) {
	f := newB1LifecycleFixture(t, false)
	for _, kind := range []string{models.MemoryContextProject, models.MemoryContextSelf} {
		t.Run(kind, func(t *testing.T) {
			owning := models.MemoryContext{Kind: kind}
			project := ""
			if kind == models.MemoryContextProject {
				project = f.ownerProject
			} else {
				owning.Principal = f.owner.Principal
				owning.PrincipalKind = string(f.owner.PrincipalKind)
			}
			initial := "MCP typed revision original " + kind
			created, _, err := f.memories.CreateInContext(auth.WithIdentity(t.Context(), f.owner), &models.Memory{
				Context: &owning, Project: project, Content: initial, OwnerPrincipal: f.owner.Principal,
				OwnerPrincipalKind: string(f.owner.PrincipalKind), AgentVisibility: models.AgentVisibilityPrivate,
				PrivacyScope: "project", SourceWorkstationID: f.owner.WorkstationID(),
			}, b1LifecycleCaller(f.owner), "", nil)
			require.NoError(t, err)
			ctx := f.ownerProjectContext(t)
			response := mutationPrincipalToolCall(t, f.server, ctx, "store", map[string]any{
				"action": "edit", "id": created.ID, "narrative": "MCP typed revision changed", "project": f.ownerProject,
			})
			current, err := f.memories.GetInContext(auth.WithIdentity(context.Background(), f.owner), created.ID, owning, project, b1LifecycleCaller(f.owner))
			require.NoError(t, err)
			history, err := f.memories.HistoryInContext(t.Context(), created.ID, owning, project, b1LifecycleCaller(f.owner), 0, 50)
			require.NoError(t, err)
			if response.Error != nil {
				require.Equal(t, created.Content, current.Content, "refusal must leave the body unchanged")
				require.Equal(t, created.Version, current.Version)
				require.Empty(t, history)
				return
			}
			require.Equal(t, "MCP typed revision changed", current.Content)
			require.Equal(t, created.Version+1, current.Version)
			require.Len(t, history, 1, "a successful MCP edit must retain the original version")
			require.Equal(t, created.Version, history[0].Version)
			require.Equal(t, initial, history[0].Content)
			_, err = f.memories.HistoryInContext(t.Context(), created.ID, owning, project, b1LifecycleCaller(f.other), 0, 50)
			require.Error(t, err, "another principal must not read retained bodies")
		})
	}
}

func TestB1RawWriterPreservesTypedHistoryPostgres(t *testing.T) {
	f := newB1LifecycleFixture(t, false)
	for _, kind := range []string{models.MemoryContextSelf, models.MemoryContextProject} {
		t.Run(kind, func(t *testing.T) {
			owning := models.MemoryContext{Kind: kind}
			project := ""
			if kind == models.MemoryContextProject {
				project = f.ownerProject
			} else {
				owning.Principal, owning.PrincipalKind = f.owner.Principal, string(f.owner.PrincipalKind)
			}
			created, _, err := f.memories.CreateInContext(auth.WithIdentity(t.Context(), f.owner), &models.Memory{
				Context: &owning, Project: project, Content: "raw typed writer original", OwnerPrincipal: f.owner.Principal,
				OwnerPrincipalKind: string(f.owner.PrincipalKind), AgentVisibility: models.AgentVisibilityPrivate,
				PrivacyScope: "project", SourceWorkstationID: f.owner.WorkstationID(),
			}, b1LifecycleCaller(f.owner), "", nil)
			require.NoError(t, err)
			changed := *created
			changed.Content = "raw typed writer revised"
			_, updateErr := f.memories.Update(t.Context(), &changed)
			current, err := f.memories.GetInContext(t.Context(), created.ID, owning, project, b1LifecycleCaller(f.owner))
			require.NoError(t, err)
			history, err := f.memories.HistoryInContext(t.Context(), created.ID, owning, project, b1LifecycleCaller(f.owner), 0, 50)
			require.NoError(t, err)
			if updateErr != nil {
				require.Equal(t, created.Content, current.Content)
				require.Equal(t, created.Version, current.Version)
				require.Empty(t, history)
				return
			}
			require.Equal(t, changed.Content, current.Content)
			require.Equal(t, created.Version+1, current.Version)
			require.Len(t, history, 1, "a successful raw typed update must preserve the prior version")
			require.Equal(t, created.Content, history[0].Content)
			require.Equal(t, created.Version, history[0].Version)
		})
	}
}
