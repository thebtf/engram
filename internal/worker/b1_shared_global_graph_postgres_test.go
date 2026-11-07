package worker

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
)

// Low-level edges are synthetic fixture setup, not an assertion that the
// retired graph editor or a public edge-writing interface exists.
func TestB1SharedGlobalGraphPrivacyPostgres(t *testing.T) {
	f := newOwnCardContextFixture(t, false)
	owner := f.registered(t, "global-graph-owner")
	reader := f.registered(t, "global-graph-reader")
	unregistered := f.keycard(t, "agent", "agent/unregistered-global-"+uuid.NewString())
	createGlobal := func(content, visibility string) ownCardContextRecord {
		t.Helper()
		payload := ownCardContextPayload(content)
		payload["context"] = map[string]any{"kind": models.MemoryContextGlobal}
		if visibility != "" {
			payload["agent_visibility"] = visibility
		}
		return f.create(t, owner, payload)
	}
	first := createGlobal("shared global graph source", models.AgentVisibilityShared)
	second := createGlobal("shared global graph target", models.AgentVisibilityShared)
	private := createGlobal("unshared global graph target", "")
	sharedPath := "/api/memories/" + strconv.FormatInt(first.ID, 10) + "?context=global"
	sharedRead := f.call(t, http.MethodGet, sharedPath, nil, reader.caller())
	require.Equal(t, http.StatusOK, sharedRead.Code)
	require.Contains(t, sharedRead.Body.String(), first.Content)
	unsharedPath := "/api/memories/" + strconv.FormatInt(private.ID, 10) + "?context=global"
	privateRead := f.call(t, http.MethodGet, unsharedPath, nil, reader.caller())
	require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, privateRead.Code)
	require.NotContains(t, privateRead.Body.String(), private.Content)
	unregisteredRead := f.call(t, http.MethodGet, sharedPath, nil, unregistered.caller())
	require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, unregisteredRead.Code)
	f.service.graphStore = graph.NewStore(f.store.DB, nil)
	makeEdge := func(target int64, marker string) *graph.Edge {
		t.Helper()
		edge, err := f.service.graphStore.Create(t.Context(), &graph.Edge{SourceID: &first.ID, TargetID: &target, EdgeType: graph.EdgeUses, Weight: 1, Reasoning: marker})
		require.NoError(t, err)
		return edge
	}
	created := makeEdge(second.ID, "synthetic-shared-edge")
	makeEdge(private.ID, "synthetic-hidden-edge")
	var storedUTC string
	require.NoError(t, f.store.DB.Raw(`SELECT to_char(valid_until AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS') FROM knowledge_edges WHERE id = ?`, created.ID).Scan(&storedUTC).Error)
	loaded, err := f.service.graphStore.Get(t.Context(), created.ID)
	require.NoError(t, err)
	t.Logf("edge validity stored UTC=%s returned=%s returned UTC=%s", storedUTC, loaded.ValidUntil.Format("2006-01-02 15:04:05 -07:00"), loaded.ValidUntil.UTC().Format("2006-01-02 15:04:05 -07:00"))
	path := "/api/graph/edges?memory_id=" + strconv.FormatInt(first.ID, 10)
	readEdges := func(caller ownCardContextCaller) (graphEdgesResponse, string) {
		t.Helper()
		response := f.call(t, http.MethodGet, path, nil, caller)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var result graphEdgesResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
		return result, response.Body.String()
	}
	own, _ := readEdges(owner.caller())
	require.Len(t, own.Edges, 2, "owner sees both permitted endpoints")
	shared, body := readEdges(reader.caller())
	require.Len(t, shared.Edges, 1, "enrolled non-owner sees explicit shared endpoints")
	require.NotNil(t, shared.Edges[0].TargetID)
	require.Equal(t, second.ID, *shared.Edges[0].TargetID)
	require.NotContains(t, body, "synthetic-hidden-edge")
	denied, deniedBody := readEdges(unregistered.caller())
	require.Empty(t, denied.Edges)
	require.NotContains(t, deniedBody, "synthetic-shared-edge")
	query := "/api/memories/principal?" + url.Values{"principal": {owner.identity.Principal}, "principal_kind": {owner.identity.PrincipalKind}, "visibility": {"shared"}}.Encode()
	response := f.call(t, http.MethodGet, query, nil, reader.caller())
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), first.Content)
	require.Contains(t, response.Body.String(), second.Content)
	require.NotContains(t, response.Body.String(), private.Content)
}
