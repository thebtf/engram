package worker

import (
	"bytes"
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

// fakeGraphEdgeStore is an in-memory graphEdgeStore for handler-level tests.
// It mirrors the discriminator-aware filtering semantics of graph.Store
// (ListByMemory / ListByNode) closely enough to exercise handler validation
// logic without a live database.
type fakeGraphEdgeStore struct {
	edges     map[int64]*graph.Edge
	nextID    int64
	createErr error
}

func newFakeGraphEdgeStore() *fakeGraphEdgeStore {
	return &fakeGraphEdgeStore{edges: map[int64]*graph.Edge{}}
}

func (f *fakeGraphEdgeStore) Create(ctx context.Context, e *graph.Edge) (*graph.Edge, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	cp := *e
	cp.ID = f.nextID
	f.edges[cp.ID] = &cp
	out := cp
	return &out, nil
}

func (f *fakeGraphEdgeStore) Get(ctx context.Context, id int64) (*graph.Edge, error) {
	e, ok := f.edges[id]
	if !ok {
		return nil, gormlib.ErrRecordNotFound
	}
	return e, nil
}

func (f *fakeGraphEdgeStore) list(matchOutgoing, matchIncoming func(graph.Edge) bool, dir graph.Direction, edgeType string) []graph.Edge {
	var out []graph.Edge
	for _, e := range f.edges {
		var match bool
		switch dir {
		case graph.Outgoing:
			match = matchOutgoing(*e)
		case graph.Incoming:
			match = matchIncoming(*e)
		default:
			match = matchOutgoing(*e) || matchIncoming(*e)
		}
		if match && (edgeType == "" || e.EdgeType == edgeType) {
			out = append(out, *e)
		}
	}
	return out
}

func (f *fakeGraphEdgeStore) ListByMemory(ctx context.Context, memoryID int64, dir graph.Direction, edgeType string) ([]graph.Edge, error) {
	return f.list(
		func(e graph.Edge) bool {
			return normalizeGraphEndpointType(e.SourceType) == "memory" && e.SourceID != nil && *e.SourceID == memoryID
		},
		func(e graph.Edge) bool {
			return normalizeGraphEndpointType(e.TargetType) == "memory" && e.TargetID != nil && *e.TargetID == memoryID
		},
		dir, edgeType,
	), nil
}

func (f *fakeGraphEdgeStore) ListByNode(ctx context.Context, nodeID int64, dir graph.Direction, edgeType string) ([]graph.Edge, error) {
	return f.list(
		func(e graph.Edge) bool {
			return normalizeGraphEndpointType(e.SourceType) == "node" && e.NodeSourceID != nil && *e.NodeSourceID == nodeID
		},
		func(e graph.Edge) bool {
			return normalizeGraphEndpointType(e.TargetType) == "node" && e.NodeTargetID != nil && *e.NodeTargetID == nodeID
		},
		dir, edgeType,
	), nil
}

func (f *fakeGraphEdgeStore) SoftDelete(ctx context.Context, id int64) error {
	if _, ok := f.edges[id]; !ok {
		return fmt.Errorf("edge %d not found", id)
	}
	delete(f.edges, id)
	return nil
}

func (f *fakeGraphEdgeStore) Traverse(ctx context.Context, startID int64, maxDepth int, edgeTypes []string) ([]graph.TraversalResult, error) {
	return nil, nil
}

func (f *fakeGraphEdgeStore) FindPath(ctx context.Context, sourceID, targetID int64, maxDepth int) ([]graph.TraversalResult, error) {
	return nil, nil
}

// fakeGraphNodeStore is an in-memory graphNodeStore for handler-level tests.
type fakeGraphNodeStore struct {
	nodes  map[int64]*models.KnowledgeNode
	nextID int64
}

func newFakeGraphNodeStore() *fakeGraphNodeStore {
	return &fakeGraphNodeStore{nodes: map[int64]*models.KnowledgeNode{}}
}

func (f *fakeGraphNodeStore) Create(ctx context.Context, node *models.KnowledgeNode) (*models.KnowledgeNode, error) {
	f.nextID++
	cp := *node
	cp.ID = f.nextID
	f.nodes[cp.ID] = &cp
	out := cp
	return &out, nil
}

func (f *fakeGraphNodeStore) Get(ctx context.Context, id int64, includePrivate bool) (*models.KnowledgeNode, error) {
	n, ok := f.nodes[id]
	if !ok {
		return nil, fmt.Errorf("get knowledge_node %d: %w", id, gormlib.ErrRecordNotFound)
	}
	return n, nil
}

func (f *fakeGraphNodeStore) ListByType(ctx context.Context, nodeType, project string, includePrivate bool) ([]models.KnowledgeNode, error) {
	var out []models.KnowledgeNode
	for _, node := range f.nodes {
		if project != "" && node.Project != project {
			continue
		}
		if nodeType != "" && node.NodeType != nodeType {
			continue
		}
		if !includePrivate && node.PrivacyScope == "private" {
			continue
		}
		out = append(out, *node)
	}
	return out, nil
}

func (f *fakeGraphNodeStore) SoftDelete(ctx context.Context, id int64) error {
	if _, ok := f.nodes[id]; !ok {
		return fmt.Errorf("knowledge_node %d not found", id)
	}
	delete(f.nodes, id)
	return nil
}

func newGraphTestService(edges *fakeGraphEdgeStore, nodes *fakeGraphNodeStore) *Service {
	return &Service{
		graphEnabled:       true,
		graphEdgeStoreSeam: edges,
		graphNodeStoreSeam: nodes,
	}
}

func graphRouter(s *Service) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/api/graph/nodes", s.handleCreateGraphNode)
	r.Delete("/api/graph/nodes/{id}", s.handleDeleteGraphNode)
	r.Post("/api/graph/edges", s.handleCreateGraphEdge)
	r.Delete("/api/graph/edges/{id}", s.handleDeleteGraphEdge)
	r.Get("/api/graph/edges", s.handleGetGraphEdges)
	return r
}

func TestHandlersGraph_ListNodesFiltersPrivate(t *testing.T) {
	nodes := newFakeGraphNodeStore()
	service := &Service{graphEnabled: true, graphNodeStoreSeam: nodes}
	privateNode, err := nodes.Create(context.Background(), &models.KnowledgeNode{NodeType: "skill", ExternalRef: "private-skill", Project: "engram", PrivacyScope: "private"})
	require.NoError(t, err)
	_, err = nodes.Create(context.Background(), &models.KnowledgeNode{NodeType: "skill", ExternalRef: "shared-skill", Project: "engram", PrivacyScope: "project"})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/api/graph/nodes?project=engram&node_type=skill", nil)
	rec := httptest.NewRecorder()
	service.handleGetGraphNodes(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload graphNodesResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	for _, node := range payload.Nodes {
		if node.ID == privateNode.ID {
			t.Fatalf("private node %d must not be listed", privateNode.ID)
		}
	}
}

func mustNode(t *testing.T, nodes *fakeGraphNodeStore, nodeType, ref, project string) int64 {
	t.Helper()
	created, err := nodes.Create(context.Background(), &models.KnowledgeNode{NodeType: nodeType, ExternalRef: ref, Project: project})
	require.NoError(t, err)
	return created.ID
}

// TestHandlersGraph_CreateEdge_RejectsDuplicate is T004 AC1: duplicate-edge
// creation is rejected, not silently accepted (FR-20 rule a).
func TestHandlersGraph_CreateEdge_RejectsDuplicate(t *testing.T) {
	edges := newFakeGraphEdgeStore()
	nodes := newFakeGraphNodeStore()
	srcID := mustNode(t, nodes, "skill", "skill-a", "engram")
	tgtID := mustNode(t, nodes, "skill", "skill-b", "engram")
	svc := newGraphTestService(edges, nodes)
	router := graphRouter(svc)

	body, err := json.Marshal(createEdgeRequest{
		SourceType: "node", TargetType: "node",
		NodeSourceID: srcID, NodeTargetID: tgtID,
		EdgeType: graph.EdgeUses,
	})
	require.NoError(t, err)

	w1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPost, "/api/graph/edges", bytes.NewReader(body))
	router.ServeHTTP(w1, req1)
	require.Equal(t, http.StatusOK, w1.Code, w1.Body.String())

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/graph/edges", bytes.NewReader(body))
	router.ServeHTTP(w2, req2)

	require.Equal(t, http.StatusConflict, w2.Code, w2.Body.String())
	assert.Contains(t, w2.Body.String(), "duplicate_edge")
	assert.Len(t, edges.edges, 1, "duplicate creation must not persist a second edge")
}

// TestHandlersGraph_DeleteNode_RejectsWithoutCascadeWhenInEdgesLive is T004
// AC2: deleting a node with live in-edges without an explicit cascade flag is
// rejected; passing cascade=true allows the delete to proceed (FR-20 rule b).
func TestHandlersGraph_DeleteNode_RejectsWithoutCascadeWhenInEdgesLive(t *testing.T) {
	edges := newFakeGraphEdgeStore()
	nodes := newFakeGraphNodeStore()
	srcID := mustNode(t, nodes, "skill", "skill-a", "engram")
	tgtID := mustNode(t, nodes, "skill", "skill-b", "engram")
	src, tgt := srcID, tgtID
	_, err := edges.Create(context.Background(), &graph.Edge{
		SourceType: "node", TargetType: "node",
		NodeSourceID: &src, NodeTargetID: &tgt,
		EdgeType: graph.EdgeUses, Weight: 1.0,
	})
	require.NoError(t, err)

	svc := newGraphTestService(edges, nodes)
	router := graphRouter(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/graph/nodes/%d", tgtID), nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "node_has_live_in_edges")
	assert.Len(t, edges.edges, 1, "edge must survive a rejected delete")
	assert.Len(t, nodes.nodes, 2, "node must survive a rejected delete")

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/graph/nodes/%d?cascade=true", tgtID), nil)
	router.ServeHTTP(w2, req2)

	require.Equal(t, http.StatusOK, w2.Code, w2.Body.String())
	assert.Len(t, edges.edges, 0, "cascade delete must remove the live in-edge")
	assert.Len(t, nodes.nodes, 1, "cascade delete must remove only the target node")
}

// TestHandlersGraph_CreateEdge_RejectsOrphanTarget is T004 AC3: creating an
// edge that references a node ID that does not exist is rejected (FR-20 rule
// c, orphan-edge rejection).
func TestHandlersGraph_CreateEdge_RejectsOrphanTarget(t *testing.T) {
	edges := newFakeGraphEdgeStore()
	nodes := newFakeGraphNodeStore()
	srcID := mustNode(t, nodes, "skill", "skill-a", "engram")
	svc := newGraphTestService(edges, nodes)
	router := graphRouter(svc)

	body, err := json.Marshal(createEdgeRequest{
		SourceType: "node", TargetType: "node",
		NodeSourceID: srcID, NodeTargetID: 999999,
		EdgeType: graph.EdgeUses,
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/graph/edges", bytes.NewReader(body))
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "orphan_edge")
	assert.Len(t, edges.edges, 0, "orphan edge creation must not persist")
}

// TestHandlersGraph_CreateAndDeleteEdge_Succeeds is T004 AC4: valid
// create/delete of edges and nodes succeeds and is reflected in the store.
func TestHandlersGraph_CreateAndDeleteEdge_Succeeds(t *testing.T) {
	edges := newFakeGraphEdgeStore()
	nodes := newFakeGraphNodeStore()
	srcID := mustNode(t, nodes, "skill", "skill-a", "engram")
	tgtID := mustNode(t, nodes, "skill", "skill-b", "engram")
	svc := newGraphTestService(edges, nodes)
	router := graphRouter(svc)

	body, err := json.Marshal(createEdgeRequest{
		SourceType: "node", TargetType: "node",
		NodeSourceID: srcID, NodeTargetID: tgtID,
		EdgeType: graph.EdgeUses,
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/graph/edges", bytes.NewReader(body))
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var created graph.Edge
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.NotZero(t, created.ID)

	dw := httptest.NewRecorder()
	dreq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/graph/edges/%d", created.ID), nil)
	router.ServeHTTP(dw, dreq)
	require.Equal(t, http.StatusOK, dw.Code, dw.Body.String())
	assert.Len(t, edges.edges, 0)

	dnw := httptest.NewRecorder()
	dnreq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/graph/nodes/%d", tgtID), nil)
	router.ServeHTTP(dnw, dnreq)
	require.Equal(t, http.StatusOK, dnw.Code, dnw.Body.String())
	assert.Len(t, nodes.nodes, 1)
}

// TestHandlersGraph_GatedBeforeStore asserts write endpoints are rejected
// before ever touching the store when ENGRAM_GRAPH_ENABLED is off.
func TestHandlersGraph_GatedBeforeStore(t *testing.T) {
	edges := newFakeGraphEdgeStore()
	nodes := newFakeGraphNodeStore()
	svc := &Service{graphEdgeStoreSeam: edges, graphNodeStoreSeam: nodes} // graphEnabled left false
	router := graphRouter(svc)

	body, err := json.Marshal(createEdgeRequest{SourceType: "memory", TargetType: "memory", SourceID: 1, TargetID: 2, EdgeType: graph.EdgeUses})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/graph/edges", bytes.NewReader(body))
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, 0, len(edges.edges))
}
