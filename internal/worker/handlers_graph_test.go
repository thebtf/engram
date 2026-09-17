package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

type fakeGraphEdgeStore struct {
	edges []graph.Edge
}

func (f *fakeGraphEdgeStore) ListByMemory(_ context.Context, memoryID int64, dir graph.Direction, _ string) ([]graph.Edge, error) {
	return f.list(memoryID, dir, false), nil
}

func (f *fakeGraphEdgeStore) ListByNode(_ context.Context, nodeID int64, dir graph.Direction, _ string) ([]graph.Edge, error) {
	return f.list(nodeID, dir, true), nil
}

func (f *fakeGraphEdgeStore) list(id int64, dir graph.Direction, node bool) []graph.Edge {
	var result []graph.Edge
	for _, edge := range f.edges {
		var source, target *int64
		if node {
			source, target = edge.NodeSourceID, edge.NodeTargetID
		} else {
			source, target = edge.SourceID, edge.TargetID
		}
		if (dir == graph.Both || dir == graph.Outgoing) && source != nil && *source == id {
			result = append(result, edge)
			continue
		}
		if (dir == graph.Both || dir == graph.Incoming) && target != nil && *target == id {
			result = append(result, edge)
		}
	}
	return result
}

func (f *fakeGraphEdgeStore) Traverse(context.Context, int64, int, []string) ([]graph.TraversalResult, error) {
	return nil, nil
}

func (f *fakeGraphEdgeStore) FindPath(context.Context, int64, int64, int) ([]graph.TraversalResult, error) {
	return nil, nil
}

type fakeGraphNodeStore struct {
	nodes map[int64]models.KnowledgeNode
}

func (f *fakeGraphNodeStore) Get(_ context.Context, id int64, includePrivate bool) (*models.KnowledgeNode, error) {
	node, ok := f.nodes[id]
	if !ok || (!includePrivate && node.PrivacyScope == "private") {
		return nil, fmt.Errorf("node %d: %w", id, gormlib.ErrRecordNotFound)
	}
	return &node, nil
}

func (f *fakeGraphNodeStore) ListByType(_ context.Context, nodeType, project string, includePrivate bool) ([]models.KnowledgeNode, error) {
	var result []models.KnowledgeNode
	for _, node := range f.nodes {
		if node.Project == project && (nodeType == "" || node.NodeType == nodeType) && (includePrivate || node.PrivacyScope != "private") {
			result = append(result, node)
		}
	}
	return result, nil
}

func newGraphTestService(edges *fakeGraphEdgeStore, nodes *fakeGraphNodeStore) *Service {
	return &Service{graphEdgeStoreSeam: edges, graphNodeStoreSeam: nodes}
}

func graphRouter(s *Service) *chi.Mux {
	s.router = chi.NewRouter()
	s.setupRoutes()
	return s.router
}

func TestHandlersGraphPreservesReadersAfterWriterRetirement(t *testing.T) {
	visibleID, hiddenID := int64(1), int64(2)
	nodes := &fakeGraphNodeStore{nodes: map[int64]models.KnowledgeNode{
		visibleID: {ID: visibleID, NodeType: "skill", ExternalRef: "visible", Project: "engram", PrivacyScope: "project"},
		hiddenID:  {ID: hiddenID, NodeType: "skill", ExternalRef: "hidden", Project: "engram", PrivacyScope: "private"},
	}}
	edges := &fakeGraphEdgeStore{edges: []graph.Edge{
		{ID: 11, SourceType: "node", TargetType: "node", NodeSourceID: &visibleID, NodeTargetID: &visibleID, EdgeType: graph.EdgeUses},
		{ID: 12, SourceType: "node", TargetType: "node", NodeSourceID: &visibleID, NodeTargetID: &hiddenID, EdgeType: graph.EdgeUses},
	}}
	for _, legacyFlag := range []string{"", "false", "true"} {
		t.Run("legacy_flag_"+legacyFlag, func(t *testing.T) {
			t.Setenv("ENGRAM_GRAPH_ENABLED", legacyFlag)
			service := newGraphTestService(edges, nodes)
			service.ready.Store(true)
			router := graphRouter(service)
			for _, admission := range []struct {
				method string
				path   string
				status int
			}{
				{method: http.MethodPost, path: "/api/graph/nodes", status: http.StatusMethodNotAllowed},
				{method: http.MethodPost, path: "/api/graph/edges", status: http.StatusMethodNotAllowed},
				{method: http.MethodDelete, path: "/api/graph/nodes/1", status: http.StatusMethodNotAllowed},
				{method: http.MethodDelete, path: "/api/graph/edges/1", status: http.StatusMethodNotAllowed},
			} {
				writer := httptest.NewRecorder()
				router.ServeHTTP(writer, httptest.NewRequest(admission.method, admission.path, nil))
				require.Equalf(t, admission.status, writer.Code, "%s %s body=%s", admission.method, admission.path, writer.Body.String())
			}

			reader := httptest.NewRecorder()
			router.ServeHTTP(reader, httptest.NewRequest(http.MethodGet, "/api/graph/edges?node_id=1&direction=outgoing", nil))
			require.Equal(t, http.StatusOK, reader.Code, reader.Body.String())
			var payload graphEdgesResponse
			require.NoError(t, json.Unmarshal(reader.Body.Bytes(), &payload))
			require.Len(t, payload.Edges, 1)
			assert.Equal(t, int64(11), payload.Edges[0].ID)
		})
	}
	assert.Len(t, edges.edges, 2)
	assert.Len(t, nodes.nodes, 2)
}

func TestHandlersGraphListsHistoricalNodesWithoutLegacyFlag(t *testing.T) {
	nodes := &fakeGraphNodeStore{nodes: map[int64]models.KnowledgeNode{
		1: {ID: 1, NodeType: "skill", ExternalRef: "visible", Project: "engram", PrivacyScope: "project"},
		2: {ID: 2, NodeType: "skill", ExternalRef: "private", Project: "engram", PrivacyScope: "private"},
	}}
	service := newGraphTestService(&fakeGraphEdgeStore{}, nodes)
	writer := httptest.NewRecorder()
	service.handleGetGraphNodes(writer, httptest.NewRequest(http.MethodGet, "/api/graph/nodes?project=engram&node_type=skill", nil))
	require.Equal(t, http.StatusOK, writer.Code, writer.Body.String())
	var payload graphNodesResponse
	require.NoError(t, json.Unmarshal(writer.Body.Bytes(), &payload))
	require.Len(t, payload.Nodes, 1)
	assert.Equal(t, int64(1), payload.Nodes[0].ID)
}

func TestHandlersGraphRetainedPathReaderRejectsUnboundedDepth(t *testing.T) {
	service := newGraphTestService(&fakeGraphEdgeStore{}, &fakeGraphNodeStore{})
	writer := httptest.NewRecorder()
	service.handleFindGraphPath(writer, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/graph/path?source_id=1&target_id=2&max_depth=%d", graph.MaxTraverseDepth+1), nil))
	require.Equal(t, http.StatusBadRequest, writer.Code, writer.Body.String())
	assert.Contains(t, writer.Body.String(), fmt.Sprintf("max depth is %d", graph.MaxTraverseDepth))
}
