package scope

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

func principalMemory(id int64, owner, visibility string) *models.Memory {
	mem := makeMemory(id, ScopeProject, "ws-owner", nil)
	mem.OwnerPrincipal = owner
	mem.OwnerPrincipalKind = "agent"
	mem.AgentVisibility = visibility
	return mem
}

func TestResolveMemory_PrincipalPrivateRequiresOwner(t *testing.T) {
	t.Parallel()
	mem := principalMemory(1, "agent/alice", models.AgentVisibilityPrivate)

	require.True(t, ResolveMemory(
		KeycardContext{Principal: "agent/alice", PrincipalKind: "agent"},
		mem,
		MemoryVisibilityOptions{},
	), "same principal must see own private memory")

	require.False(t, ResolveMemory(
		KeycardContext{Principal: "agent/bob", PrincipalKind: "agent"},
		mem,
		MemoryVisibilityOptions{},
	), "cross-principal caller must not see private memory")

	require.False(t, ResolveMemory(
		KeycardContext{},
		mem,
		MemoryVisibilityOptions{},
	), "empty caller principal must fail closed for private memory")
}

func TestResolveMemory_PrincipalSharedPreservesLegacyVisibility(t *testing.T) {
	t.Parallel()
	mem := principalMemory(2, "agent/alice", models.AgentVisibilityShared)

	require.True(t, ResolveMemory(
		KeycardContext{Principal: "agent/bob", PrincipalKind: "agent"},
		mem,
		MemoryVisibilityOptions{},
	), "shared owned memory must remain team-readable")
}

func TestResolveMemory_PrincipalPrivateWithoutOwnerFailsClosed(t *testing.T) {
	t.Parallel()
	mem := principalMemory(3, "", models.AgentVisibilityPrivate)

	require.False(t, ResolveMemory(
		KeycardContext{Principal: "agent/alice", PrincipalKind: "agent"},
		mem,
		MemoryVisibilityOptions{},
	), "private visibility without owner is malformed and must fail closed")
}

func TestResolveMemory_UnknownAgentVisibilityFailsClosed(t *testing.T) {
	t.Parallel()
	mem := principalMemory(4, "agent/alice", "admins-only")

	require.False(t, ResolveMemory(
		KeycardContext{Principal: "agent/alice", PrincipalKind: "agent"},
		mem,
		MemoryVisibilityOptions{},
	), "unknown principal visibility must fail closed")
}

func TestResolveMemory_PrincipalKindMustMatchForPrivate(t *testing.T) {
	t.Parallel()
	mem := principalMemory(9, "alice", models.AgentVisibilityPrivate)
	mem.OwnerPrincipalKind = "human"

	require.True(t, ResolveMemory(
		KeycardContext{Principal: "alice", PrincipalKind: "human"},
		mem,
		MemoryVisibilityOptions{},
	), "same principal and same kind must see private memory")

	require.False(t, ResolveMemory(
		KeycardContext{Principal: "alice", PrincipalKind: "agent"},
		mem,
		MemoryVisibilityOptions{},
	), "same principal string with different kind is a different actor")
}

func TestResolveMemory_PrivateWithoutOwnerKindFailsClosed(t *testing.T) {
	t.Parallel()
	mem := principalMemory(10, "agent/alice", models.AgentVisibilityPrivate)
	mem.OwnerPrincipalKind = ""

	require.False(t, ResolveMemory(
		KeycardContext{Principal: "agent/alice", PrincipalKind: "agent"},
		mem,
		MemoryVisibilityOptions{},
	), "private visibility with missing owner kind is malformed and must fail closed")
}

func TestResolveMemory_FlagOffIgnoresLegacyPrivacyButKeepsPrincipalPrivate(t *testing.T) {
	t.Parallel()
	legacyPrivate := makeMemory(5, ScopePrivate, "ws-other", nil)
	ownedPrivate := principalMemory(6, "agent/alice", models.AgentVisibilityPrivate)
	ownedPrivate.PrivacyScope = ScopePrivate
	ownedPrivate.SourceWorkstationID = "ws-other"

	opts := MemoryVisibilityOptions{ApplyPrivacyScope: false}
	caller := KeycardContext{WorkstationID: "ws-caller", Principal: "agent/bob", PrincipalKind: "agent"}

	require.True(t, ResolveMemory(caller, legacyPrivate, opts),
		"flag-off mode must preserve legacy unowned privacy_scope behavior")
	require.False(t, ResolveMemory(caller, ownedPrivate, opts),
		"flag-off mode must still filter another principal's private memory")
}

func TestResolveMemory_ComposesPrivacyScopeAndPrincipalVisibility(t *testing.T) {
	t.Parallel()
	mem := principalMemory(7, "agent/alice", models.AgentVisibilityPrivate)
	mem.PrivacyScope = ScopePrivate
	mem.SourceWorkstationID = "ws-alice"
	mem.SourceSessions = []string{"sess-alice"}
	opts := MemoryVisibilityOptions{ApplyPrivacyScope: true}

	require.True(t, ResolveMemory(
		KeycardContext{WorkstationID: "ws-alice", SessionID: "sess-alice", Principal: "agent/alice", PrincipalKind: "agent"},
		mem,
		opts,
	), "both legacy private scope and principal-private checks pass")

	require.False(t, ResolveMemory(
		KeycardContext{WorkstationID: "ws-other", SessionID: "sess-alice", Principal: "agent/alice", PrincipalKind: "agent"},
		mem,
		opts,
	), "matching principal is insufficient when legacy private scope fails")

	require.False(t, ResolveMemory(
		KeycardContext{WorkstationID: "ws-alice", SessionID: "sess-alice", Principal: "agent/bob", PrincipalKind: "agent"},
		mem,
		opts,
	), "matching legacy private scope is insufficient when principal-private fails")
}

func TestResolveMemory_IncludeScopesAppliesOnlyWithPrivacyScope(t *testing.T) {
	t.Parallel()
	mem := makeMemory(8, ScopeProject, "ws-owner", nil)
	opts := MemoryVisibilityOptions{
		ApplyPrivacyScope: true,
		IncludeScopes:     map[string]bool{ScopeGlobal: true},
	}

	require.False(t, ResolveMemory(KeycardContext{}, mem, opts))

	opts.ApplyPrivacyScope = false
	require.True(t, ResolveMemory(KeycardContext{}, mem, opts),
		"include_scopes belongs to legacy privacy_scope and is ignored when that layer is disabled")
}
