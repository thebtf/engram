package scope

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

func TestTypedContextDoesNotGrantGraphVisibility(t *testing.T) {
	owner := KeycardContext{WorkstationID: "synthetic-a", Principal: "agent/a", PrincipalKind: "agent"}
	other := KeycardContext{WorkstationID: "synthetic-b", Principal: "agent/b", PrincipalKind: "agent"}
	for _, context := range []*models.MemoryContext{
		{Kind: models.MemoryContextSelf, Principal: owner.Principal, PrincipalKind: owner.PrincipalKind},
		{Kind: models.MemoryContextGlobal},
	} {
		memory := &models.Memory{Context: context, OwnerPrincipal: owner.Principal, OwnerPrincipalKind: owner.PrincipalKind, AgentVisibility: models.AgentVisibilityShared}
		require.True(t, ResolveMemory(owner, memory, MemoryVisibilityOptions{}))
		require.False(t, ResolveMemory(other, memory, MemoryVisibilityOptions{}))
		require.False(t, ResolveMemory(KeycardContext{}, memory, MemoryVisibilityOptions{}))
	}
}
