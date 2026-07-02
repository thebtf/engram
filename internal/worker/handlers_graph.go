package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

const graphFeatureFlag = "ENGRAM_GRAPH_ENABLED"
const defaultGraphListLimit = 80
const maxGraphListLimit = 200

type graphEdgeStore interface {
	Create(ctx context.Context, e *graph.Edge) (*graph.Edge, error)
	Get(ctx context.Context, id int64) (*graph.Edge, error)
	ListByMemory(ctx context.Context, memoryID int64, dir graph.Direction, edgeType string) ([]graph.Edge, error)
	ListByNode(ctx context.Context, nodeID int64, dir graph.Direction, edgeType string) ([]graph.Edge, error)
	SoftDelete(ctx context.Context, id int64) error
	Traverse(ctx context.Context, startID int64, maxDepth int, edgeTypes []string) ([]graph.TraversalResult, error)
	FindPath(ctx context.Context, sourceID, targetID int64, maxDepth int) ([]graph.TraversalResult, error)
}

type graphNodeStore interface {
	Create(ctx context.Context, node *models.KnowledgeNode) (*models.KnowledgeNode, error)
	Get(ctx context.Context, id int64, includePrivate bool) (*models.KnowledgeNode, error)
	SoftDelete(ctx context.Context, id int64) error
}

type graphNodeLister interface {
	ListByType(ctx context.Context, nodeType, project string, includePrivate bool) ([]models.KnowledgeNode, error)
}

type graphMemoryGetter interface {
	Get(ctx context.Context, id int64) (*models.Memory, error)
}

type graphErrorResponse struct {
	Error graphError `json:"error"`
}

type graphError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type createNodeRequest struct {
	NodeType     string `json:"node_type"`
	ExternalRef  string `json:"external_ref"`
	Project      string `json:"project"`
	PrivacyScope string `json:"privacy_scope,omitempty"`
}

type createEdgeRequest struct {
	Reasoning       string  `json:"reasoning,omitempty"`
	SourceSessionID string  `json:"source_session_id,omitempty"`
	SourceType      string  `json:"source_type,omitempty"`
	TargetType      string  `json:"target_type,omitempty"`
	EdgeType        string  `json:"edge_type"`
	Weight          float64 `json:"weight,omitempty"`
	SourceID        int64   `json:"source_id,omitempty"`
	TargetID        int64   `json:"target_id,omitempty"`
	NodeSourceID    int64   `json:"node_source_id,omitempty"`
	NodeTargetID    int64   `json:"node_target_id,omitempty"`
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

type graphDeleteEdgeResponse struct {
	Deleted bool  `json:"deleted"`
	EdgeID  int64 `json:"edge_id"`
}

type graphDeleteNodeResponse struct {
	Deleted        bool    `json:"deleted"`
	Cascade        bool    `json:"cascade"`
	NodeID         int64   `json:"node_id"`
	DeletedEdgeIDs []int64 `json:"deleted_edge_ids,omitempty"`
}

func graphEnabledFromEnv() bool {
	return os.Getenv(graphFeatureFlag) == "true"
}

func normalizeGraphEndpointType(value string) string {
	normalized, err := parseGraphEndpointType(value)
	if err != nil {
		return "memory"
	}
	return normalized
}

func parseGraphEndpointType(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", "memory":
		return "memory", nil
	case "node":
		return "node", nil
	default:
		return "", fmt.Errorf("endpoint type must be 'memory' or 'node', got %q", value)
	}
}

func (s *Service) graphActive() bool {
	return s != nil && s.graphEnabled
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

func (s *Service) currentGraphMemoryStore() graphMemoryGetter {
	if s == nil {
		return nil
	}

	s.initMu.RLock()
	defer s.initMu.RUnlock()
	return s.memoryStore
}

func (s *Service) rejectGraphDisabled(w http.ResponseWriter) bool {
	if s.graphActive() {
		return false
	}
	writeGraphError(w, http.StatusForbidden, "graph_disabled", graphFeatureFlag+" must be true")
	return true
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

func parseGraphBoolQuery(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
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

func graphEdgeSourceMatches(edge graph.Edge, candidate graph.Edge) bool {
	if normalizeGraphEndpointType(edge.SourceType) != normalizeGraphEndpointType(candidate.SourceType) {
		return false
	}
	if normalizeGraphEndpointType(edge.SourceType) == "node" {
		return edge.NodeSourceID != nil && candidate.NodeSourceID != nil && *edge.NodeSourceID == *candidate.NodeSourceID
	}
	return edge.SourceID != nil && candidate.SourceID != nil && *edge.SourceID == *candidate.SourceID
}

func graphEdgeTargetMatches(edge graph.Edge, candidate graph.Edge) bool {
	if normalizeGraphEndpointType(edge.TargetType) != normalizeGraphEndpointType(candidate.TargetType) {
		return false
	}
	if normalizeGraphEndpointType(edge.TargetType) == "node" {
		return edge.NodeTargetID != nil && candidate.NodeTargetID != nil && *edge.NodeTargetID == *candidate.NodeTargetID
	}
	return edge.TargetID != nil && candidate.TargetID != nil && *edge.TargetID == *candidate.TargetID
}

func graphEdgesEqual(edge graph.Edge, candidate graph.Edge) bool {
	return edge.EdgeType == candidate.EdgeType && graphEdgeSourceMatches(edge, candidate) && graphEdgeTargetMatches(edge, candidate)
}

func graphEdgeDeleteIDs(edges []graph.Edge) []int64 {
	seen := make(map[int64]struct{}, len(edges))
	ids := make([]int64, 0, len(edges))
	for _, edge := range edges {
		if _, ok := seen[edge.ID]; ok {
			continue
		}
		seen[edge.ID] = struct{}{}
		ids = append(ids, edge.ID)
	}
	return ids
}

func (s *Service) graphEndpointExists(ctx context.Context, endpointType string, memoryID, nodeID int64) (bool, error) {
	switch endpointType {
	case "node":
		store := s.currentGraphNodeStore()
		if store == nil {
			return false, fmt.Errorf("graph node store not available")
		}
		_, err := store.Get(ctx, nodeID, true)
		if err != nil {
			if errors.Is(err, gormlib.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	default:
		store := s.currentGraphMemoryStore()
		if store == nil {
			return false, fmt.Errorf("memory store not available")
		}
		_, err := store.Get(ctx, memoryID)
		if err != nil {
			if errors.Is(err, gormlib.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
}

func (s *Service) graphEdgeAlreadyExists(ctx context.Context, store graphEdgeStore, candidate graph.Edge) (bool, error) {
	var (
		existing []graph.Edge
		err      error
	)
	if normalizeGraphEndpointType(candidate.SourceType) == "node" {
		existing, err = store.ListByNode(ctx, *candidate.NodeSourceID, graph.Outgoing, candidate.EdgeType)
	} else {
		existing, err = store.ListByMemory(ctx, *candidate.SourceID, graph.Outgoing, candidate.EdgeType)
	}
	if err != nil {
		return false, err
	}
	for _, edge := range existing {
		if graphEdgesEqual(edge, candidate) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) handleGetGraphNodes(w http.ResponseWriter, r *http.Request) {
	if s.rejectGraphDisabled(w) {
		return
	}
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

func (s *Service) handleCreateGraphNode(w http.ResponseWriter, r *http.Request) {
	if s.rejectGraphDisabled(w) {
		return
	}
	store := s.currentGraphNodeStore()
	if store == nil {
		writeGraphError(w, http.StatusServiceUnavailable, "graph_store_unavailable", "graph node store not available")
		return
	}

	var req createNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if !models.ValidNodeType(req.NodeType) {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("invalid node_type %q", req.NodeType))
		return
	}
	node, err := models.NewKnowledgeNode(req.NodeType, strings.TrimSpace(req.ExternalRef), strings.TrimSpace(req.Project))
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.PrivacyScope != "" {
		node.PrivacyScope = strings.TrimSpace(req.PrivacyScope)
	}

	unlock := graph.LockWrites()
	defer unlock()

	created, err := store.Create(r.Context(), node)
	if err != nil {
		writeGraphError(w, http.StatusConflict, "node_create_failed", err.Error())
		return
	}
	writeJSON(w, created)
}

func (s *Service) handleCreateGraphEdge(w http.ResponseWriter, r *http.Request) {
	if s.rejectGraphDisabled(w) {
		return
	}
	store := s.currentGraphEdgeStore()
	if store == nil {
		writeGraphError(w, http.StatusServiceUnavailable, "graph_store_unavailable", "graph edge store not available")
		return
	}

	var req createEdgeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	sourceType, err := parseGraphEndpointType(req.SourceType)
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", "source_type "+err.Error())
		return
	}
	targetType, err := parseGraphEndpointType(req.TargetType)
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", "target_type "+err.Error())
		return
	}
	if strings.TrimSpace(req.EdgeType) == "" {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", "edge_type is required")
		return
	}
	if !graph.ValidEdgeType(req.EdgeType) {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("invalid edge_type %q", req.EdgeType))
		return
	}
	weight := req.Weight
	if weight == 0 {
		weight = 1.0
	}

	candidate := graph.Edge{
		EdgeType:        strings.TrimSpace(req.EdgeType),
		Weight:          weight,
		Reasoning:       strings.TrimSpace(req.Reasoning),
		SourceSessionID: strings.TrimSpace(req.SourceSessionID),
		SourceType:      sourceType,
		TargetType:      targetType,
	}
	if sourceType == "node" {
		if req.NodeSourceID <= 0 {
			writeGraphError(w, http.StatusBadRequest, "invalid_request", "node_source_id required when source_type='node'")
			return
		}
		nodeSourceID := req.NodeSourceID
		candidate.NodeSourceID = &nodeSourceID
	} else {
		if req.SourceID <= 0 {
			writeGraphError(w, http.StatusBadRequest, "invalid_request", "source_id required when source_type='memory'")
			return
		}
		sourceID := req.SourceID
		candidate.SourceID = &sourceID
	}
	if targetType == "node" {
		if req.NodeTargetID <= 0 {
			writeGraphError(w, http.StatusBadRequest, "invalid_request", "node_target_id required when target_type='node'")
			return
		}
		nodeTargetID := req.NodeTargetID
		candidate.NodeTargetID = &nodeTargetID
	} else {
		if req.TargetID <= 0 {
			writeGraphError(w, http.StatusBadRequest, "invalid_request", "target_id required when target_type='memory'")
			return
		}
		targetID := req.TargetID
		candidate.TargetID = &targetID
	}

	unlock := graph.LockWrites()
	defer unlock()

	sourceExists, err := s.graphEndpointExists(r.Context(), sourceType, req.SourceID, req.NodeSourceID)
	if err != nil {
		writeGraphError(w, http.StatusServiceUnavailable, "graph_lookup_failed", err.Error())
		return
	}
	if !sourceExists {
		writeGraphError(w, http.StatusConflict, "orphan_edge", "source endpoint does not exist")
		return
	}
	targetExists, err := s.graphEndpointExists(r.Context(), targetType, req.TargetID, req.NodeTargetID)
	if err != nil {
		writeGraphError(w, http.StatusServiceUnavailable, "graph_lookup_failed", err.Error())
		return
	}
	if !targetExists {
		writeGraphError(w, http.StatusConflict, "orphan_edge", "target endpoint does not exist")
		return
	}
	duplicate, err := s.graphEdgeAlreadyExists(r.Context(), store, candidate)
	if err != nil {
		writeGraphError(w, http.StatusInternalServerError, "graph_read_failed", err.Error())
		return
	}
	if duplicate {
		writeGraphError(w, http.StatusConflict, "duplicate_edge", "an active edge with the same source, target, and type already exists")
		return
	}
	created, err := store.Create(r.Context(), &candidate)
	if err != nil {
		writeGraphError(w, http.StatusConflict, "edge_create_failed", err.Error())
		return
	}
	writeJSON(w, created)
}

func (s *Service) handleDeleteGraphEdge(w http.ResponseWriter, r *http.Request) {
	if s.rejectGraphDisabled(w) {
		return
	}
	store := s.currentGraphEdgeStore()
	if store == nil {
		writeGraphError(w, http.StatusServiceUnavailable, "graph_store_unavailable", "graph edge store not available")
		return
	}
	id, err := parseGraphIDParam(r, "id")
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	unlock := graph.LockWrites()
	defer unlock()

	if err := store.SoftDelete(r.Context(), id); err != nil {
		status := http.StatusInternalServerError
		code := "edge_delete_failed"
		if errors.Is(err, gormlib.ErrRecordNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found") {
			status = http.StatusNotFound
			code = "not_found"
		}
		writeGraphError(w, status, code, err.Error())
		return
	}
	writeJSON(w, graphDeleteEdgeResponse{Deleted: true, EdgeID: id})
}

func (s *Service) handleDeleteGraphNode(w http.ResponseWriter, r *http.Request) {
	if s.rejectGraphDisabled(w) {
		return
	}
	nodeStore := s.currentGraphNodeStore()
	edgeStore := s.currentGraphEdgeStore()
	if nodeStore == nil || edgeStore == nil {
		writeGraphError(w, http.StatusServiceUnavailable, "graph_store_unavailable", "graph stores not available")
		return
	}
	id, err := parseGraphIDParam(r, "id")
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	cascade := parseGraphBoolQuery(r.URL.Query().Get("cascade"))

	unlock := graph.LockWrites()
	defer unlock()

	if _, err := nodeStore.Get(r.Context(), id, true); err != nil {
		status := http.StatusInternalServerError
		code := "graph_lookup_failed"
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			status = http.StatusNotFound
			code = "not_found"
		}
		writeGraphError(w, status, code, err.Error())
		return
	}
	incoming, err := edgeStore.ListByNode(r.Context(), id, graph.Incoming, "")
	if err != nil {
		writeGraphError(w, http.StatusInternalServerError, "graph_read_failed", err.Error())
		return
	}
	outgoing, err := edgeStore.ListByNode(r.Context(), id, graph.Outgoing, "")
	if err != nil {
		writeGraphError(w, http.StatusInternalServerError, "graph_read_failed", err.Error())
		return
	}
	if len(incoming) > 0 && !cascade {
		writeGraphError(w, http.StatusConflict, "node_has_live_in_edges", "node has live in-edges; retry with cascade=true to remove connected edges")
		return
	}

	deleteIDs := graphEdgeDeleteIDs(outgoing)
	if cascade {
		deleteIDs = graphEdgeDeleteIDs(append(outgoing, incoming...))
	}
	for _, edgeID := range deleteIDs {
		if err := edgeStore.SoftDelete(r.Context(), edgeID); err != nil {
			writeGraphError(w, http.StatusInternalServerError, "edge_delete_failed", err.Error())
			return
		}
	}
	if err := nodeStore.SoftDelete(r.Context(), id); err != nil {
		status := http.StatusInternalServerError
		code := "node_delete_failed"
		if errors.Is(err, gormlib.ErrRecordNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found") {
			status = http.StatusNotFound
			code = "not_found"
		}
		writeGraphError(w, status, code, err.Error())
		return
	}
	writeJSON(w, graphDeleteNodeResponse{Deleted: true, Cascade: cascade, NodeID: id, DeletedEdgeIDs: deleteIDs})
}

func (s *Service) handleGetGraphEdges(w http.ResponseWriter, r *http.Request) {
	if s.rejectGraphDisabled(w) {
		return
	}
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
	resp := graphEdgesResponse{Edges: edges, Direction: string(direction), EdgeType: edgeType, Count: len(edges)}
	if memoryID != 0 {
		resp.MemoryID = &memoryID
	} else {
		resp.NodeID = &nodeID
	}
	writeJSON(w, resp)
}

func (s *Service) handleTraverseGraph(w http.ResponseWriter, r *http.Request) {
	if s.rejectGraphDisabled(w) {
		return
	}
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
	results, err := store.Traverse(r.Context(), memoryID, depth, parseGraphEdgeTypes(r.URL.Query().Get("edge_types")))
	if err != nil {
		writeGraphError(w, http.StatusInternalServerError, "graph_read_failed", err.Error())
		return
	}
	writeJSON(w, graphTraverseResponse{Results: results, MemoryID: memoryID, Depth: depth, Count: len(results)})
}

func (s *Service) handleFindGraphPath(w http.ResponseWriter, r *http.Request) {
	if s.rejectGraphDisabled(w) {
		return
	}
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
	path, err := store.FindPath(r.Context(), sourceID, targetID, maxDepth)
	if err != nil {
		writeGraphError(w, http.StatusInternalServerError, "graph_read_failed", err.Error())
		return
	}
	writeJSON(w, graphPathResponse{Path: path, SourceID: sourceID, TargetID: targetID, Found: path != nil, Hops: len(path)})
}
