package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/graph"
)

func TestB1GraphDefaultEdgeMCPPostgres(t *testing.T) {
	f := newB1LifecycleFixture(t, false)
	first := f.ownCard(t, "self_shared")
	second := f.ownCard(t, "self_shared")
	store := graph.NewStore(f.store.DB, nil)
	f.server.SetGraphStore(store)
	edge, err := store.Create(t.Context(), &graph.Edge{SourceID: &first.ID, TargetID: &second.ID, EdgeType: graph.EdgeUses, Weight: 1, Reasoning: "synthetic-default-edge"})
	require.NoError(t, err)
	var storedUTC string
	require.NoError(t, f.store.DB.Raw(`SELECT to_char(valid_until AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS') FROM knowledge_edges WHERE id = ?`, edge.ID).Scan(&storedUTC).Error)
	loaded, err := store.Get(t.Context(), edge.ID)
	require.NoError(t, err)
	t.Logf("edge validity stored UTC=%s returned=%s returned UTC=%s", storedUTC, loaded.ValidUntil.Format("2006-01-02 15:04:05 -07:00"), loaded.ValidUntil.UTC().Format("2006-01-02 15:04:05 -07:00"))
	call := func(owner bool) *Response {
		t.Helper()
		identity := f.other
		if owner {
			identity = f.owner
		}
		return f.server.HandleRequest(auth.WithIdentity(t.Context(), identity), &Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: mustJSON(t, map[string]any{"name": "graph", "arguments": map[string]any{"action": "get_edges", "memory_id": first.ID}})})
	}
	owner := call(true)
	require.NotNil(t, owner)
	body := b1LifecycleBody(t, owner)
	require.EqualValues(t, 1, body["count"])
	edges, ok := body["edges"].([]any)
	require.True(t, ok)
	require.Len(t, edges, 1)
	item, ok := edges[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "synthetic-default-edge", item["reasoning"])
	other := call(false)
	require.NotNil(t, other)
	raw, err := json.Marshal(other)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "synthetic-default-edge")
}
