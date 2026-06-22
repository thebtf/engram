package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/principalmemory"
)

func TestQueryPrincipalMemory_ToolSchemaAdvertised(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetPrincipalMemoryQueryService(&fakeMCPPrincipalMemoryQueryService{
		result: &principalmemory.PrincipalMemoryQueryResult{},
	})

	resp := srv.handleToolsList(&Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/list",
		Params:  json.RawMessage(`{"include_all":true}`),
	})
	require.Nil(t, resp.Error)

	tools := resp.Result.(map[string]any)["tools"].([]Tool)
	var tool *Tool
	for i := range tools {
		if tools[i].Name == "query_principal_memory" {
			tool = &tools[i]
			break
		}
	}
	require.NotNil(t, tool, "query_principal_memory must be advertised when the query service is wired")

	props := tool.InputSchema["properties"].(map[string]any)
	for _, field := range []string{"principal", "principal_kind", "project", "domain", "q", "query", "visibility", "include_private", "limit", "offset", "session_id"} {
		assert.Contains(t, props, field)
	}
	required := tool.InputSchema["required"].([]string)
	assert.ElementsMatch(t, []string{"principal"}, required)
	visibility := props["visibility"].(map[string]any)
	assert.ElementsMatch(t, []string{"private", "shared", "all"}, visibility["enum"])
	limit := props["limit"].(map[string]any)
	assert.Equal(t, 50, limit["default"])
	assert.Equal(t, 500, limit["maximum"])
}

func TestQueryPrincipalMemory_ResponseAndValidation(t *testing.T) {
	t.Run("returns attributed bounded principal memory response", func(t *testing.T) {
		querySvc := &fakeMCPPrincipalMemoryQueryService{
			result: &principalmemory.PrincipalMemoryQueryResult{
				Items: []principalmemory.PrincipalMemoryQueryItem{
					{
						ID:                 42,
						Project:            "project-a",
						Content:            "shared alice note",
						Tags:               []string{"semantic"},
						OwnerPrincipal:     "agent/alice",
						OwnerPrincipalKind: "human",
						AgentVisibility:    "shared",
						Domain:             "operator-console",
						Confidence:         0.8,
						CreatedAt:          time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC),
					},
				},
				Principal:     "agent/alice",
				PrincipalKind: "human",
				Project:       "project-a",
				Domain:        "operator-console",
				HiddenCount:   1,
				AuditStatus:   principalmemory.AuditStatusNotRequired,
				Audit: principalmemory.PrincipalMemoryQueryAudit{
					Action: "principal_memory_query",
				},
			},
		}
		srv := NewServer(ServerOptions{Version: "test"})
		srv.SetPrincipalMemoryQueryService(querySvc)
		id := auth.ClientWithPrincipal("read-write", "keycard-bob", "agent/bob", auth.PrincipalKindAgent)
		ctx := auth.WithIdentity(context.Background(), id)

		out, err := srv.callTool(ctx, "query_principal_memory", mustPrincipalMemoryJSON(t, map[string]any{
			"project":    "project-a",
			"principal":  "agent/alice",
			"domain":     "operator-console",
			"query":      "shared alice",
			"visibility": "all",
			"limit":      2,
			"session_id": "session-123",
		}))
		require.NoError(t, err)

		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &body))
		require.Equal(t, "agent/alice", body["principal"])
		require.Equal(t, "human", body["principal_kind"])
		require.Equal(t, "project-a", body["project"])
		require.Equal(t, "operator-console", body["domain"])
		require.Equal(t, float64(1), body["hidden_count"])
		require.Equal(t, principalmemory.AuditStatusNotRequired, body["audit_status"])
		audit := body["audit"].(map[string]any)
		require.Equal(t, "principal_memory_query", audit["action"])
		require.Equal(t, false, audit["durable"])
		items := body["items"].([]any)
		require.Len(t, items, 1)
		first := items[0].(map[string]any)
		assert.Equal(t, float64(42), first["id"])
		assert.Equal(t, "agent/alice", first["owner_principal"])
		assert.Equal(t, "human", first["owner_principal_kind"])
		assert.Equal(t, "operator-console", first["domain"])
		assert.Equal(t, []any{"semantic"}, first["tags"])
		assert.Equal(t, 0.8, first["confidence"])
		assert.Equal(t, "2026-06-22T00:00:00Z", first["created_at"])

		assert.Equal(t, "project-a", querySvc.request.Project)
		assert.Equal(t, "agent/alice", querySvc.request.OwnerPrincipal)
		assert.Equal(t, "human", querySvc.request.OwnerPrincipalKind)
		assert.Equal(t, "operator-console", querySvc.request.Domain)
		assert.Equal(t, "shared alice", querySvc.request.Query)
		assert.Empty(t, querySvc.request.AgentVisibility)
		assert.Equal(t, 2, querySvc.request.Limit)
		assert.Equal(t, "session-123", querySvc.request.SourceSessionID)
		assert.Equal(t, "agent/bob", querySvc.request.Caller.Principal)
	})

	t.Run("rejects invalid principal kind", func(t *testing.T) {
		srv := NewServer(ServerOptions{Version: "test"})
		srv.SetPrincipalMemoryQueryService(&fakeMCPPrincipalMemoryQueryService{result: &principalmemory.PrincipalMemoryQueryResult{}})

		_, err := srv.callTool(context.Background(), "query_principal_memory", mustPrincipalMemoryJSON(t, map[string]any{
			"principal":      "agent/alice",
			"principal_kind": "robot",
		}))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "principal_kind must be one of")
	})

	t.Run("rejects oversized limit clearly", func(t *testing.T) {
		querySvc := &fakeMCPPrincipalMemoryQueryService{result: &principalmemory.PrincipalMemoryQueryResult{}}
		srv := NewServer(ServerOptions{Version: "test"})
		srv.SetPrincipalMemoryQueryService(querySvc)

		_, err := srv.callTool(context.Background(), "query_principal_memory", mustPrincipalMemoryJSON(t, map[string]any{
			"principal":      "agent/alice",
			"principal_kind": "agent",
			"limit":          9999,
		}))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "limit must be between 1 and 500")
		assert.False(t, querySvc.called)
	})

	t.Run("rejects non-admin cross-principal private widening", func(t *testing.T) {
		querySvc := &fakeMCPPrincipalMemoryQueryService{err: principalmemory.ErrCrossPrincipalPrivateDenied}
		srv := NewServer(ServerOptions{Version: "test"})
		srv.SetPrincipalMemoryQueryService(querySvc)
		id := auth.ClientWithPrincipal("read-write", "keycard-bob", "agent/bob", auth.PrincipalKindAgent)
		ctx := auth.WithIdentity(context.Background(), id)

		_, err := srv.callTool(ctx, "query_principal_memory", mustPrincipalMemoryJSON(t, map[string]any{
			"principal":       "agent/alice",
			"principal_kind":  "agent",
			"include_private": true,
		}))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "include_private for another principal requires admin")
		assert.True(t, querySvc.called)
		assert.True(t, querySvc.request.IncludePrivate)
	})
}

type fakeMCPPrincipalMemoryQueryService struct {
	result  *principalmemory.PrincipalMemoryQueryResult
	err     error
	request principalmemory.PrincipalMemoryQueryRequest
	called  bool
}

func (f *fakeMCPPrincipalMemoryQueryService) Query(ctx context.Context, req principalmemory.PrincipalMemoryQueryRequest) (*principalmemory.PrincipalMemoryQueryResult, error) {
	f.called = true
	f.request = req
	if f.err != nil {
		return nil, f.err
	}
	if f.result == nil {
		return nil, errors.New("fake result not configured")
	}
	return f.result, nil
}

func mustPrincipalMemoryJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestQueryPrincipalMemory_ServiceErrorsPropagate(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetPrincipalMemoryQueryService(&fakeMCPPrincipalMemoryQueryService{err: errors.New("boom")})

	_, err := srv.callTool(context.Background(), "query_principal_memory", mustPrincipalMemoryJSON(t, map[string]any{
		"principal":      "agent/alice",
		"principal_kind": "agent",
	}))

	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "principal memory query failed"))
}
