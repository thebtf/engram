package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

func TestStoreMemoryDomainPolicy_EmptyDomainLegacyCompatible(t *testing.T) {
	t.Parallel()
	mem := &models.Memory{}

	err := applyPrincipalMemoryMetadata(context.Background(), mem, "", "  ")

	require.NoError(t, err)
	require.Empty(t, mem.Domain)
	require.Empty(t, mem.OwnerPrincipal)
	require.Empty(t, mem.OwnerPrincipalKind)
	require.Empty(t, mem.AgentVisibility)
}

func TestStoreMemoryDomainPolicy_NonEmptyDomainRequiresPrincipal(t *testing.T) {
	t.Parallel()
	mem := &models.Memory{}

	err := applyPrincipalMemoryMetadata(context.Background(), mem, "", "memory-lab")

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_domain")
	require.Empty(t, mem.Domain, "denied domain writes must not leave partial metadata on the memory")
}

func TestStoreMemoryDomainPolicy_NonEmptyDomainAllowsPrincipalIdentity(t *testing.T) {
	t.Parallel()
	mem := &models.Memory{}
	ctx := auth.WithIdentity(context.Background(),
		auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))

	err := applyPrincipalMemoryMetadata(ctx, mem, "", "memory-lab")

	require.NoError(t, err)
	require.Equal(t, "memory-lab", mem.Domain)
	require.Equal(t, "agent/alice", mem.OwnerPrincipal)
	require.Equal(t, "agent", mem.OwnerPrincipalKind)
	require.Equal(t, models.AgentVisibilityShared, mem.AgentVisibility)
}

func TestStoreMemoryDomainPolicy_NonEmptyDomainRejectsInvalidPrincipalKind(t *testing.T) {
	t.Parallel()
	mem := &models.Memory{}
	ctx := auth.WithIdentity(context.Background(), auth.Identity{
		Principal:     "agent/alice",
		PrincipalKind: auth.PrincipalKind("bogus"),
	})

	err := applyPrincipalMemoryMetadata(ctx, mem, "", "memory-lab")

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_domain")
	require.Empty(t, mem.Domain)
}

func TestWriteLintDomainPolicy_DomainOwnedCandidateHidden(t *testing.T) {
	t.Parallel()
	ms := newStubWLMemStore(&models.Memory{
		ID:                 42,
		Project:            "testproj",
		Content:            t035DupContent,
		OwnerPrincipal:     "agent/bob",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Domain:             "memory-lab",
	})
	caller := scope.KeycardContext{Principal: "agent/alice", PrincipalKind: "agent"}
	scopedStore := newScopedWriteLintMemoryStore(ms, caller, scope.MemoryVisibilityOptions{})

	candidates, err := scopedStore.List(context.Background(), "testproj", 10)

	require.NoError(t, err)
	require.Empty(t, candidates, "cross-principal domain-owned candidate must not influence write-lint")
}

func TestWriteLintDomainPolicy_DomainOwnedTargetHidden(t *testing.T) {
	t.Parallel()
	ms := newStubWLMemStore(&models.Memory{
		ID:                 43,
		Project:            "testproj",
		Content:            "hidden domain target",
		OwnerPrincipal:     "agent/bob",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Domain:             "memory-lab",
	})
	caller := scope.KeycardContext{Principal: "agent/alice", PrincipalKind: "agent"}
	scopedStore := newScopedWriteLintMemoryStore(ms, caller, scope.MemoryVisibilityOptions{})

	target, err := scopedStore.Get(context.Background(), 43)

	require.NoError(t, err)
	require.Nil(t, target, "cross-principal domain-owned target must be treated as hidden")
}

func TestRecallMemoryDomainPolicy_DomainOwnedRowHiddenFromMismatchedPrincipal(t *testing.T) {
	t.Parallel()
	caller := scope.KeycardContext{Principal: "agent/alice", PrincipalKind: "agent"}
	mem := &models.Memory{
		Content:            "domain memory",
		Tags:               []string{"type:fact"},
		OwnerPrincipal:     "agent/bob",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Domain:             "memory-lab",
	}

	visible := keepRecallMemory(mem, "domain memory", "", nil, caller, scope.MemoryVisibilityOptions{}, nil, false, 0)

	require.False(t, visible, "cross-principal domain-owned row must be absent from recall/list predicates")
}

func TestRecallMemoryDomainPolicy_DomainOwnedRowVisibleToOwner(t *testing.T) {
	t.Parallel()
	caller := scope.KeycardContext{Principal: "agent/alice", PrincipalKind: "agent"}
	mem := &models.Memory{
		Content:            "domain memory",
		Tags:               []string{"type:fact"},
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Domain:             "memory-lab",
	}

	visible := keepRecallMemory(mem, "domain memory", "", nil, caller, scope.MemoryVisibilityOptions{}, nil, false, 0)

	require.True(t, visible, "domain owner must remain visible through recall/list predicates")
}
