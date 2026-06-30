package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/principalmemory"
)

func TestGetMemoryBrief_PrincipalScopeSchemaAdvertised(t *testing.T) {
	t.Setenv("ENGRAM_ADAPTIVE_ENABLED", "true")
	s := NewServer(ServerOptions{Version: "test"})
	s.memoryStore = nonNilMemoryStore()

	props := findToolProperties(t, s.ListTools(), "get_memory_brief")

	for _, field := range []string{"principal", "principal_kind", "domain", "visibility", "include_private", "session_id"} {
		require.Contains(t, props, field)
	}
	kind := props["principal_kind"].(map[string]any)
	require.ElementsMatch(t, []string{"human", "agent", "service"}, kind["enum"])
	visibility := props["visibility"].(map[string]any)
	require.ElementsMatch(t, []string{"private", "shared", "all"}, visibility["enum"])
}

func TestGetMemoryBrief_PrincipalScopedResponseAndRequest(t *testing.T) {
	t.Setenv("ENGRAM_ADAPTIVE_ENABLED", "true")
	querySvc := &fakeMCPPrincipalMemoryQueryService{
		result: &principalmemory.PrincipalMemoryQueryResult{
			Principal:     "agent/alice",
			PrincipalKind: "agent",
			Project:       "project-a",
			Domain:        "memory-lab",
			HiddenCount:   1,
			AuditStatus:   principalmemory.AuditStatusNotRequired,
			Audit: principalmemory.PrincipalMemoryQueryAudit{
				Action: principalmemory.AuditActionQuery,
			},
			Items: []principalmemory.PrincipalMemoryQueryItem{
				{
					ID:                 42,
					Project:            "project-a",
					Content:            "principal brief scoped memory",
					Tags:               []string{"type:fact"},
					OwnerPrincipal:     "agent/alice",
					OwnerPrincipalKind: "agent",
					AgentVisibility:    "shared",
					Domain:             "memory-lab",
					CreatedAt:          time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC),
				},
			},
		},
	}
	s := NewServer(ServerOptions{Version: "test"})
	s.memoryStore = nonNilMemoryStore()
	s.SetPrincipalMemoryQueryService(querySvc)
	ctx := auth.WithIdentity(context.Background(),
		auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))

	out, err := s.handleGetMemoryBrief(ctx, mustPrincipalMemoryJSON(t, map[string]any{
		"topic":          "scoped memory",
		"project":        "project-a",
		"principal":      "agent/alice",
		"principal_kind": "agent",
		"domain":         "memory-lab",
		"visibility":     "all",
		"limit":          2,
	}))
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &body))
	require.Equal(t, "project-a", body["project"])
	require.Equal(t, "scoped memory", body["topic"])
	require.Equal(t, "agent/alice", body["principal"])
	require.Equal(t, "agent", body["principal_kind"])
	require.Equal(t, "memory-lab", body["domain"])
	require.Equal(t, "principal_query", body["source"])
	require.Equal(t, "live", body["freshness"])

	scope := body["scope"].(map[string]any)
	require.Equal(t, "project-a", scope["project"])
	require.Equal(t, "agent/alice", scope["principal"])
	require.Equal(t, "agent", scope["principal_kind"])
	require.Equal(t, "memory-lab", scope["domain"])
	require.Equal(t, "principal_query", scope["source"])
	require.Equal(t, "live", scope["freshness"])
	require.Equal(t, float64(1), scope["hidden_count"])
	require.Equal(t, principalmemory.AuditStatusNotRequired, scope["audit_status"])

	memories := body["memories"].([]any)
	require.Len(t, memories, 1)
	first := memories[0].(map[string]any)
	require.Equal(t, float64(42), first["id"])
	require.Equal(t, "principal brief scoped memory", first["content"])
	require.Equal(t, []any{"type:fact"}, first["tags"])
	require.Equal(t, "agent/alice", first["owner_principal"])
	require.Equal(t, "agent", first["owner_principal_kind"])
	require.Equal(t, "shared", first["agent_visibility"])
	require.Equal(t, "memory-lab", first["domain"])

	require.True(t, querySvc.called)
	require.Equal(t, "project-a", querySvc.request.Project)
	require.Equal(t, "agent/alice", querySvc.request.OwnerPrincipal)
	require.Equal(t, "agent", querySvc.request.OwnerPrincipalKind)
	require.Equal(t, "memory-lab", querySvc.request.Domain)
	require.Equal(t, "scoped memory", querySvc.request.Query)
	require.Empty(t, querySvc.request.AgentVisibility)
	require.Equal(t, 2, querySvc.request.Limit)
	require.Equal(t, "agent/alice", querySvc.request.Caller.Principal)
}

func TestGetMemoryBrief_PrincipalScopeRequiresQueryService(t *testing.T) {
	t.Setenv("ENGRAM_ADAPTIVE_ENABLED", "true")
	s := NewServer(ServerOptions{Version: "test"})
	s.memoryStore = nonNilMemoryStore()

	_, err := s.handleGetMemoryBrief(context.Background(), mustPrincipalMemoryJSON(t, map[string]any{
		"project":   "project-a",
		"principal": "agent/alice",
	}))

	require.Error(t, err)
	require.Contains(t, err.Error(), "principal memory query service not available")
}
