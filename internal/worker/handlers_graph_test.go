package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	_ "modernc.org/sqlite"
)

type fakeGraphEdgeStore struct {
	edges []graph.Edge
	store *graph.Store
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

func (f *fakeGraphEdgeStore) TraverseVisible(ctx context.Context, startID int64, depth int, types []string, visible func(*graph.Edge) bool) ([]graph.TraversalResult, error) {
	if f.store != nil {
		return f.store.TraverseVisible(ctx, startID, depth, types, visible)
	}
	return nil, nil
}

func (f *fakeGraphEdgeStore) FindPathVisible(ctx context.Context, sourceID, targetID int64, depth int, visible func(*graph.Edge) bool) ([]graph.TraversalResult, error) {
	if f.store != nil {
		return f.store.FindPathVisible(ctx, sourceID, targetID, depth, visible)
	}
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

func TestHandlersGraphTraversalPrunesPrivateIntermediariesAndKeepsPublicNodes(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	sqlDB, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(1)
	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gormlib.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE memories (id INTEGER PRIMARY KEY, project TEXT, content TEXT, privacy_scope TEXT, source_workstation_id TEXT, deleted_at DATETIME)`,
		`INSERT INTO memories (id, project, content, privacy_scope, source_workstation_id) VALUES (1, 'graph', 'start', 'project', ''), (2, 'graph', 'hidden', 'private', 'other'), (3, 'graph', 'target', 'project', ''), (4, 'graph', 'via', 'project', ''), (5, 'graph', 'via two', 'project', ''), (6, 'graph', 'descendant', 'project', ''), (7, 'graph', 'descendant two', 'project', '')`,
		`CREATE TABLE knowledge_edges (id INTEGER PRIMARY KEY, source_id INTEGER, target_id INTEGER, node_source_id INTEGER, node_target_id INTEGER, source_type TEXT, target_type TEXT, edge_type TEXT, weight REAL, reasoning TEXT, source_session_id TEXT, valid_from DATETIME, valid_until DATETIME, created_at DATETIME, superseded_at DATETIME)`,
		`INSERT INTO knowledge_edges (id, source_id, target_id, node_target_id, source_type, target_type, edge_type, weight, created_at) VALUES
		(1, 1, 2, NULL, 'memory', 'memory', 'uses', 1, CURRENT_TIMESTAMP),
		(2, 2, 3, NULL, 'memory', 'memory', 'uses', 1, CURRENT_TIMESTAMP),
		(3, 1, 4, NULL, 'memory', 'memory', 'uses', 1, CURRENT_TIMESTAMP),
		(4, 4, 5, NULL, 'memory', 'memory', 'uses', 1, CURRENT_TIMESTAMP),
		(5, 5, 3, NULL, 'memory', 'memory', 'uses', 1, CURRENT_TIMESTAMP),
		(6, 2, 6, NULL, 'memory', 'memory', 'uses', 1, CURRENT_TIMESTAMP),
		(7, 6, 7, NULL, 'memory', 'memory', 'uses', 1, CURRENT_TIMESTAMP),
		(8, 1, NULL, 10, 'memory', 'node', 'uses', 1, CURRENT_TIMESTAMP),
		(9, 1, NULL, 20, 'memory', 'node', 'uses', 1, CURRENT_TIMESTAMP)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	service := newGraphTestService(&fakeGraphEdgeStore{store: graph.NewStore(db, nil)}, &fakeGraphNodeStore{nodes: map[int64]models.KnowledgeNode{
		10: {ID: 10, PrivacyScope: "project"}, 20: {ID: 20, PrivacyScope: "private"},
	}})
	service.memoryStore = gormdb.NewMemoryStore(&gormdb.Store{DB: db})
	for _, tc := range []struct {
		path string
		want []int64
	}{
		{"/api/graph/traverse?memory_id=1&depth=3", []int64{3, 4, 5, 8}},
		{"/api/graph/path?source_id=1&target_id=3&max_depth=3", []int64{3, 4, 5}},
		{"/api/graph/path?source_id=1&target_id=7&max_depth=3", nil},
	} {
		writer := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if strings.HasPrefix(tc.path, "/api/graph/traverse") {
			service.handleTraverseGraph(writer, request)
		} else {
			service.handleFindGraphPath(writer, request)
		}
		require.Equal(t, http.StatusOK, writer.Code, writer.Body.String())
		var response struct {
			Results []graph.TraversalResult `json:"results"`
			Path    []graph.TraversalResult `json:"path"`
		}
		require.NoError(t, json.Unmarshal(writer.Body.Bytes(), &response))
		steps := response.Results
		if response.Path != nil {
			steps = response.Path
		}
		var ids []int64
		for _, step := range steps {
			ids = append(ids, step.EdgeID)
			if step.EdgeID == 8 {
				require.NotNil(t, step.NodeTargetID)
				require.Equal(t, int64(10), *step.NodeTargetID)
			}
		}
		assert.ElementsMatch(t, tc.want, ids, writer.Body.String())
	}
}
