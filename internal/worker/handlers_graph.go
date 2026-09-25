package worker

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
)

const (
	defaultGraphListLimit = 80
	maxGraphListLimit     = 200
)

type graphEdgeStore interface {
	ListByMemory(ctx context.Context, memoryID int64, dir graph.Direction, edgeType string) ([]graph.Edge, error)
	ListByNode(ctx context.Context, nodeID int64, dir graph.Direction, edgeType string) ([]graph.Edge, error)
	Traverse(ctx context.Context, startID int64, maxDepth int, edgeTypes []string) ([]graph.TraversalResult, error)
	FindPath(ctx context.Context, sourceID, targetID int64, maxDepth int) ([]graph.TraversalResult, error)
}

type graphNodeStore interface {
	Get(ctx context.Context, id int64, includePrivate bool) (*models.KnowledgeNode, error)
}

type graphNodeLister interface {
	ListByType(ctx context.Context, nodeType, project string, includePrivate bool) ([]models.KnowledgeNode, error)
}

type graphErrorResponse struct {
	Error graphError `json:"error"`
}

type graphError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type graphNodesResponse struct {
	Nodes    []models.KnowledgeNode `json:"nodes"`
	Project  string                 `json:"project"`
	NodeType string                 `json:"node_type,omitempty"`
	Count    int                    `json:"count"`
	Limit    int                    `json:"limit"`
}

type graphEdgesResponse struct {
	Edges     []graph.Edge `json:"edges"`
	Direction string       `json:"direction"`
	EdgeType  string       `json:"edge_type,omitempty"`
	Count     int          `json:"count"`
	MemoryID  *int64       `json:"memory_id,omitempty"`
	NodeID    *int64       `json:"node_id,omitempty"`
}

type graphTraverseResponse struct {
	Results  []graph.TraversalResult `json:"results"`
	MemoryID int64                   `json:"memory_id"`
	Depth    int                     `json:"depth"`
	Count    int                     `json:"count"`
}

type graphPathResponse struct {
	Path     []graph.TraversalResult `json:"path"`
	SourceID int64                   `json:"source_id"`
	TargetID int64                   `json:"target_id"`
	Found    bool                    `json:"found"`
	Hops     int                     `json:"hops"`
}

func (s *Service) currentGraphEdgeStore() graphEdgeStore {
	if s == nil {
		return nil
	}
	if s.graphEdgeStoreSeam != nil {
		return s.graphEdgeStoreSeam
	}

	s.initMu.RLock()
	defer s.initMu.RUnlock()
	return s.graphStore
}

func (s *Service) currentGraphNodeStore() graphNodeStore {
	if s == nil {
		return nil
	}
	if s.graphNodeStoreSeam != nil {
		return s.graphNodeStoreSeam
	}

	s.initMu.RLock()
	defer s.initMu.RUnlock()
	return s.graphNodeStore
}

func (s *Service) currentGraphNodeLister() graphNodeLister {
	if s == nil {
		return nil
	}
	if s.graphNodeStoreSeam != nil {
		if lister, ok := any(s.graphNodeStoreSeam).(graphNodeLister); ok {
			return lister
		}
	}

	s.initMu.RLock()
	defer s.initMu.RUnlock()
	return s.graphNodeStore
}

func writeGraphError(w http.ResponseWriter, status int, code, message string) {
	writeJSONStatus(w, status, graphErrorResponse{
		Error: graphError{Code: code, Message: message},
	})
}

func parseGraphIDParam(r *http.Request, name string) (int64, error) {
	raw := strings.TrimSpace(chi.URLParam(r, name))
	if raw == "" {
		return 0, fmt.Errorf("%s required", name)
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid %s %q", name, raw)
	}
	return id, nil
}

func parseGraphQueryID(r *http.Request, name string) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid %s %q", name, raw)
	}
	return id, nil
}

func parseGraphQueryLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return defaultGraphListLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("invalid limit %q", raw)
	}
	if limit > maxGraphListLimit {
		limit = maxGraphListLimit
	}
	return limit, nil
}

func parseGraphDirection(raw string) (graph.Direction, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(graph.Both):
		return graph.Both, nil
	case string(graph.Outgoing):
		return graph.Outgoing, nil
	case string(graph.Incoming):
		return graph.Incoming, nil
	default:
		return "", fmt.Errorf("direction must be outgoing, incoming, or both")
	}
}

func parseGraphIntQuery(raw string, field string, fallback int) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid %s %q", field, raw)
	}
	return parsed, nil
}

func parseGraphEdgeTypes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Service) handleGetGraphNodes(w http.ResponseWriter, r *http.Request) {
	store := s.currentGraphNodeLister()
	if store == nil {
		writeGraphError(w, http.StatusServiceUnavailable, "graph_store_unavailable", "graph node store not available")
		return
	}

	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", "project query parameter is required")
		return
	}
	nodeType := strings.TrimSpace(r.URL.Query().Get("node_type"))
	if nodeType != "" && !models.ValidNodeType(nodeType) {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("invalid node_type %q", nodeType))
		return
	}
	limit, err := parseGraphQueryLimit(r)
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	nodes, err := store.ListByType(r.Context(), nodeType, project, false)
	if err != nil {
		writeGraphError(w, http.StatusInternalServerError, "graph_read_failed", err.Error())
		return
	}
	if len(nodes) > limit {
		nodes = nodes[:limit]
	}
	writeJSON(w, graphNodesResponse{Nodes: nodes, Project: project, NodeType: nodeType, Count: len(nodes), Limit: limit})
}

func (s *Service) graphMemoryVisible(ctx context.Context, id int64) bool {
	if s.memoryStore == nil || id == 0 {
		return false
	}
	mem, err := s.memoryStore.Get(ctx, id)
	return err == nil && memoryVisibleREST(ctx, mem)
}

func (s *Service) graphNodeVisible(ctx context.Context, id int64) bool {
	store := s.currentGraphNodeStore()
	if store == nil || id == 0 {
		return false
	}
	_, err := store.Get(ctx, id, false)
	return err == nil
}

func (s *Service) filterVisibleGraphEdges(ctx context.Context, edges []graph.Edge) []graph.Edge {
	visible := make([]graph.Edge, 0, len(edges))
	memoryAccess, nodeAccess := map[int64]bool{}, map[int64]bool{}
	memVisible := func(id int64) bool {
		allowed, seen := memoryAccess[id]
		if !seen {
			allowed = s.graphMemoryVisible(ctx, id)
			memoryAccess[id] = allowed
		}
		return allowed
	}
	nodeVisible := func(id int64) bool {
		allowed, seen := nodeAccess[id]
		if !seen {
			allowed = s.graphNodeVisible(ctx, id)
			nodeAccess[id] = allowed
		}
		return allowed
	}
	for _, edge := range edges {
		if (edge.SourceID != nil && !memVisible(*edge.SourceID)) ||
			(edge.TargetID != nil && !memVisible(*edge.TargetID)) ||
			(edge.NodeSourceID != nil && !nodeVisible(*edge.NodeSourceID)) ||
			(edge.NodeTargetID != nil && !nodeVisible(*edge.NodeTargetID)) {
			continue
		}
		visible = append(visible, edge)
	}
	return visible
}

func (s *Service) filterVisibleGraphResults(ctx context.Context, results []graph.TraversalResult) []graph.TraversalResult {
	visible := make([]graph.TraversalResult, 0, len(results))
	access := map[int64]bool{}
	canRead := func(id int64) bool {
		allowed, seen := access[id]
		if !seen {
			allowed = s.graphMemoryVisible(ctx, id)
			access[id] = allowed
		}
		return allowed
	}
	for _, result := range results {
		if canRead(result.SourceID) && canRead(result.TargetID) {
			visible = append(visible, result)
		}
	}
	return visible
}

func (s *Service) handleGetGraphEdges(w http.ResponseWriter, r *http.Request) {
	store := s.currentGraphEdgeStore()
	if store == nil {
		writeGraphError(w, http.StatusServiceUnavailable, "graph_store_unavailable", "graph edge store not available")
		return
	}
	direction, err := parseGraphDirection(r.URL.Query().Get("direction"))
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	memoryID, err := parseGraphQueryID(r, "memory_id")
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	nodeID, err := parseGraphQueryID(r, "node_id")
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if memoryID == 0 && nodeID == 0 {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", "memory_id or node_id query parameter is required")
		return
	}
	if memoryID != 0 && nodeID != 0 {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", "memory_id and node_id are mutually exclusive")
		return
	}
	edgeType := strings.TrimSpace(r.URL.Query().Get("edge_type"))
	if (memoryID != 0 && !s.graphMemoryVisible(r.Context(), memoryID)) || (nodeID != 0 && !s.graphNodeVisible(r.Context(), nodeID)) {
		resp := graphEdgesResponse{Edges: []graph.Edge{}, Direction: string(direction), EdgeType: edgeType}
		if memoryID != 0 {
			resp.MemoryID = &memoryID
		} else {
			resp.NodeID = &nodeID
		}
		writeJSON(w, resp)
		return
	}
	var edges []graph.Edge
	if memoryID != 0 {
		edges, err = store.ListByMemory(r.Context(), memoryID, direction, edgeType)
	} else {
		edges, err = store.ListByNode(r.Context(), nodeID, direction, edgeType)
	}
	if err != nil {
		writeGraphError(w, http.StatusInternalServerError, "graph_read_failed", err.Error())
		return
	}
	edges = s.filterVisibleGraphEdges(r.Context(), edges)
	resp := graphEdgesResponse{Edges: edges, Direction: string(direction), EdgeType: edgeType, Count: len(edges)}
	if memoryID != 0 {
		resp.MemoryID = &memoryID
	} else {
		resp.NodeID = &nodeID
	}
	writeJSON(w, resp)
}

func (s *Service) handleTraverseGraph(w http.ResponseWriter, r *http.Request) {
	store := s.currentGraphEdgeStore()
	if store == nil {
		writeGraphError(w, http.StatusServiceUnavailable, "graph_store_unavailable", "graph edge store not available")
		return
	}
	memoryID, err := parseGraphQueryID(r, "memory_id")
	if err != nil || memoryID == 0 {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", "memory_id query parameter is required")
		return
	}
	depth, err := parseGraphIntQuery(r.URL.Query().Get("depth"), "depth", 1)
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if depth > graph.MaxTraverseDepth {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("max depth is %d", graph.MaxTraverseDepth))
		return
	}
	if !s.graphMemoryVisible(r.Context(), memoryID) {
		writeJSON(w, graphTraverseResponse{Results: []graph.TraversalResult{}, MemoryID: memoryID, Depth: depth})
		return
	}
	results, err := store.Traverse(r.Context(), memoryID, depth, parseGraphEdgeTypes(r.URL.Query().Get("edge_types")))
	if err != nil {
		writeGraphError(w, http.StatusInternalServerError, "graph_read_failed", err.Error())
		return
	}
	results = s.filterVisibleGraphResults(r.Context(), results)
	writeJSON(w, graphTraverseResponse{Results: results, MemoryID: memoryID, Depth: depth, Count: len(results)})
}

func (s *Service) handleFindGraphPath(w http.ResponseWriter, r *http.Request) {
	store := s.currentGraphEdgeStore()
	if store == nil {
		writeGraphError(w, http.StatusServiceUnavailable, "graph_store_unavailable", "graph edge store not available")
		return
	}
	sourceID, err := parseGraphQueryID(r, "source_id")
	if err != nil || sourceID == 0 {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", "source_id query parameter is required")
		return
	}
	targetID, err := parseGraphQueryID(r, "target_id")
	if err != nil || targetID == 0 {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", "target_id query parameter is required")
		return
	}
	maxDepth, err := parseGraphIntQuery(r.URL.Query().Get("max_depth"), "max_depth", graph.MaxTraverseDepth)
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if maxDepth > graph.MaxTraverseDepth {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("max depth is %d", graph.MaxTraverseDepth))
		return
	}
	if !s.graphMemoryVisible(r.Context(), sourceID) || !s.graphMemoryVisible(r.Context(), targetID) {
		writeJSON(w, graphPathResponse{Path: []graph.TraversalResult{}, SourceID: sourceID, TargetID: targetID})
		return
	}
	path, err := store.FindPath(r.Context(), sourceID, targetID, maxDepth)
	if err != nil {
		writeGraphError(w, http.StatusInternalServerError, "graph_read_failed", err.Error())
		return
	}
	if len(s.filterVisibleGraphResults(r.Context(), path)) != len(path) {
		path = nil
	}
	writeJSON(w, graphPathResponse{Path: path, SourceID: sourceID, TargetID: targetID, Found: path != nil, Hops: len(path)})
}
