package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
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

func TestLegacyWriteGateDomainPolicy_DomainOwnedCandidateHidden(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	ctx := auth.WithIdentity(context.Background(),
		auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))
	candidates := []*models.Memory{{
		ID:                 44,
		Project:            "testproj",
		Content:            t035DupContent,
		OwnerPrincipal:     "agent/bob",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Domain:             "memory-lab",
	}, {
		ID:                 45,
		Project:            "testproj",
		Content:            "visible own domain row",
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Domain:             "memory-lab",
	}}

	visible := filterVisibleWriteGateCandidates(ctx, "", candidates)

	require.Len(t, visible, 1)
	require.Equal(t, int64(45), visible[0].ID)
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

func TestIngestWriteGateRespectsExistingMemoryVisibility(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set; actual ingest store privacy proof requires PostgreSQL")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	const content = "Synthetic ingestion duplicate content about preserving canonical project identity and enforcing scoped memory visibility for every reader and writer."
	for _, tc := range []struct {
		name    string
		memory  *models.Memory
		session string
		flagged int
	}{
		{name: "no existing row"},
		{name: "foreign principal hidden", memory: &models.Memory{OwnerPrincipal: "agent/bob", OwnerPrincipalKind: "agent", AgentVisibility: models.AgentVisibilityPrivate}},
		{name: "foreign domain hidden", memory: &models.Memory{OwnerPrincipal: "agent/bob", OwnerPrincipalKind: "agent", AgentVisibility: models.AgentVisibilityShared, Domain: "memory-lab"}},
		{name: "foreign workstation hidden", memory: &models.Memory{PrivacyScope: "private", SourceWorkstationID: "keycard-bob"}},
		{name: "visible project duplicate", memory: &models.Memory{PrivacyScope: "project"}, flagged: 1},
		{name: "owner private duplicate", memory: &models.Memory{OwnerPrincipal: "agent/alice", OwnerPrincipalKind: "agent", AgentVisibility: models.AgentVisibilityPrivate}, flagged: 1},
		{name: "same workstation other session hidden", memory: &models.Memory{PrivacyScope: "private", SourceWorkstationID: "keycard-alice", SourceSessions: []string{"session-other"}}, session: "session-alice"},
		{name: "same workstation same session duplicate", memory: &models.Memory{PrivacyScope: "private", SourceWorkstationID: "keycard-alice", SourceSessions: []string{"session-alice"}}, session: "session-alice", flagged: 1},
		{name: "no session workstation fallback duplicate", memory: &models.Memory{PrivacyScope: "private", SourceWorkstationID: "keycard-alice", SourceSessions: []string{"session-other"}}, flagged: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := uuid.NewString()
			require.NoError(t, store.DB.Create(&gormdb.Project{ID: project}).Error)
			t.Cleanup(func() {
				require.NoError(t, store.DB.Exec("DELETE FROM memories WHERE project = ?", project).Error)
				require.NoError(t, store.DB.Where("id = ?", project).Delete(&gormdb.Project{}).Error)
			})
			memoryStore := gormdb.NewMemoryStore(store)
			if tc.memory != nil {
				tc.memory.Project, tc.memory.Content = project, content
				tc.memory.SourceAgent = "synthetic-existing"
				_, err := memoryStore.CreateWithLifecycle(context.Background(), tc.memory)
				require.NoError(t, err)
			}
			srv := NewServer(ServerOptions{Version: "ingest-privacy-test"})
			srv.SetMemoryStore(memoryStore)
			ctx := auth.WithIdentity(context.Background(), auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))
			ctx = ContextWithProject(ctx, project)
			if tc.session != "" {
				ctx = ContextWithSession(ctx, tc.session)
			}
			result := srv.HandleRequest(ctx, &Request{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params:  mustJSON(t, map[string]any{"name": "ingest", "arguments": map[string]any{"action": "ingest", "project": project, "content": content, "source_title": "synthetic-fixture"}}),
			})
			require.NotNil(t, result)
			require.Nil(t, result.Error)
			var toolResult struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			require.NoError(t, json.Unmarshal(mustJSON(t, result.Result), &toolResult))
			require.Len(t, toolResult.Content, 1)
			var response struct {
				Stored  int `json:"stored"`
				Flagged int `json:"flagged"`
			}
			require.NoError(t, json.Unmarshal([]byte(toolResult.Content[0].Text), &response))
			require.Equal(t, 1, response.Stored)
			require.Equal(t, tc.flagged, response.Flagged, "a hidden matching row must not change the caller-visible write-gate result")
			var created gormdb.Memory
			require.NoError(t, store.DB.Where("project = ? AND source_agent = ?", project, "ingestion").Take(&created).Error)
			if tc.flagged == 1 {
				require.Equal(t, "flagged", created.Status)
			} else {
				require.Equal(t, "active", created.Status)
			}
		})
	}
}
