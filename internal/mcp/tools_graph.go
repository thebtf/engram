package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

// nodesLister is the minimal NodesStore interface needed by filterEdgesByNodeType.
type nodesLister interface {
	ListByType(ctx context.Context, nodeType, project string, includePrivate bool) ([]models.KnowledgeNode, error)
}

// nodesStoreAPI is the retained reader contract used by graph filtering.
type nodesStoreAPI interface {
	ListByType(ctx context.Context, nodeType, project string, includePrivate bool) ([]models.KnowledgeNode, error)
	Get(ctx context.Context, id int64, includePrivate bool) (*models.KnowledgeNode, error)
}

type graphArgs struct {
	Action    string   `json:"action"`
	SourceID  int64    `json:"source_id"`
	TargetID  int64    `json:"target_id"`
	MemoryID  int64    `json:"memory_id"`
	EdgeType  string   `json:"edge_type"`
	Direction string   `json:"direction"`
	Depth     int      `json:"depth"`
	EdgeTypes []string `json:"edge_types"`
	MaxDepth  int      `json:"max_depth"`
	NodeID    int64    `json:"node_id,omitempty"`
	NodeType  string   `json:"node_type,omitempty"`
}

func (s *Server) handleGraph(ctx context.Context, args json.RawMessage) (string, error) {
	if s.graphStore == nil {
		return "", fmt.Errorf("graph store not available")
	}

	var a graphArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parse graph args: %w", err)
	}

	switch a.Action {
	case "get_edges":
		return s.graphGetEdges(ctx, a)
	case "traverse":
		return s.graphTraverse(ctx, a)
	case "find_path":
		return s.graphFindPath(ctx, a)
	case "synonyms":
		return s.graphSynonyms(ctx, a)
	default:
		return "", fmt.Errorf("unknown graph action: %s", a.Action)
	}
}

func (s *Server) graphGetEdges(ctx context.Context, a graphArgs) (string, error) {
	if a.MemoryID == 0 && a.NodeID == 0 {
		return "", fmt.Errorf("memory_id or node_id required")
	}
	dir := graph.Both
	switch a.Direction {
	case "outgoing":
		dir = graph.Outgoing
	case "incoming":
		dir = graph.Incoming
	}

	// T014: node_type filter — validate if provided.
	if a.NodeType != "" && !models.ValidNodeType(a.NodeType) {
		return "", fmt.Errorf("invalid_node_type: %q is not a valid node type", a.NodeType)
	}

	if a.NodeType != "" && s.nodesStore == nil {
		return "", fmt.Errorf("node_type filter unavailable: nodes store not configured")
	}
	if a.MemoryID != 0 && !s.graphMemoryVisible(ctx, a.MemoryID) {
		return marshalJSON(map[string]any{"memory_id": a.MemoryID, "node_id": a.NodeID, "direction": a.Direction, "node_type": a.NodeType, "count": 0, "edges": []graph.Edge{}})
	}
	if a.NodeID != 0 && !s.graphNodeVisible(ctx, a.NodeID) {
		return marshalJSON(map[string]any{"memory_id": a.MemoryID, "node_id": a.NodeID, "direction": a.Direction, "node_type": a.NodeType, "count": 0, "edges": []graph.Edge{}})
	}

	var edges []graph.Edge
	var err error
	if a.MemoryID != 0 {
		edges, err = s.graphStore.ListByMemory(ctx, a.MemoryID, dir, a.EdgeType)
	} else {
		// node_id-based edge lookup: list edges where the node is source or target.
		edges, err = s.graphStore.ListByNode(ctx, a.NodeID, dir, a.EdgeType)
	}
	if err != nil {
		return "", err
	}

	filtered := s.filterVisibleGraphEdges(ctx, edges)
	if a.NodeType != "" {
		filtered = filterEdgesByNodeType(ctx, filtered, a.NodeType, s.nodesStore)
	}

	return marshalJSON(map[string]any{
		"memory_id": a.MemoryID,
		"node_id":   a.NodeID,
		"direction": a.Direction,
		"node_type": a.NodeType,
		"count":     len(filtered),
		"edges":     filtered,
	})
}

func (s *Server) graphMemoryVisible(ctx context.Context, id int64) bool {
	if s.memoryStore == nil || id == 0 {
		return false
	}
	mem, err := s.memoryStore.Get(ctx, id)
	return err == nil && scope.ResolveMemory(writeLintVisibilityCaller(ctx, ""), mem, writeLintVisibilityOptions())
}

func (s *Server) graphNodeVisible(ctx context.Context, id int64) bool {
	if s.nodesStore == nil || id == 0 {
		return false
	}
	_, err := s.nodesStore.Get(ctx, id, false)
	return err == nil
}

func (s *Server) filterVisibleGraphEdges(ctx context.Context, edges []graph.Edge) []graph.Edge {
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

func (s *Server) graphTraversalEdgeVisible(ctx context.Context, edge *graph.Edge) bool {
	return (edge.SourceID == nil || s.graphMemoryVisible(ctx, *edge.SourceID)) &&
		(edge.TargetID == nil || s.graphMemoryVisible(ctx, *edge.TargetID)) &&
		(edge.NodeSourceID == nil || s.graphNodeVisible(ctx, *edge.NodeSourceID)) &&
		(edge.NodeTargetID == nil || s.graphNodeVisible(ctx, *edge.NodeTargetID))
}

// filterEdgesByNodeType returns only edges whose node endpoint (node_source_id
// or node_target_id) belongs to a knowledge_node with the given nodeType.
//
// Algorithm:
//  1. Collect the unique set of node IDs referenced by edges.
//  2. Fetch each node via nodesStore (using Get with includePrivate=false).
//  3. Build the set of node IDs whose node_type matches nodeType.
//  4. Retain edges that have at least one endpoint in the matching set.
//
// Edges with no node endpoints (both NodeSourceID and NodeTargetID nil) are
// excluded — they are memory-only edges that cannot match a node_type filter.
// graphGetEdges rejects requests without a configured node store before calling
// this helper, so a requested filter never falls back to unfiltered edges.
// Anti-stub: this replaces the prior return-unfiltered implementation.
// TestGraphTool_T014_NodeTypeFilterOffline asserts correct filtering behaviour.
func filterEdgesByNodeType(ctx context.Context, edges []graph.Edge, nodeType string, ns nodesLister) []graph.Edge {
	if ns == nil {
		return edges
	}

	// Step 1: collect unique node IDs referenced by the edge set.
	nodeIDs := map[int64]struct{}{}
	for _, e := range edges {
		if e.NodeSourceID != nil {
			nodeIDs[*e.NodeSourceID] = struct{}{}
		}
		if e.NodeTargetID != nil {
			nodeIDs[*e.NodeTargetID] = struct{}{}
		}
	}
	if len(nodeIDs) == 0 {
		// No node endpoints — no edge can match a node_type filter.
		return nil
	}

	// Step 2: fetch each node and record which IDs match the requested type.
	// We use the graph.NodesStore directly via the nodesLister interface;
	// Get is called per unique ID (set is bounded by the edge slice size).
	type getterWithGet interface {
		Get(ctx context.Context, id int64, includePrivate bool) (*models.KnowledgeNode, error)
	}
	getter, hasGet := ns.(getterWithGet)

	matchingIDs := map[int64]struct{}{}
	if hasGet {
		for id := range nodeIDs {
			node, err := getter.Get(ctx, id, false)
			if err != nil {
				// Node not found or private — skip; the edge will be excluded.
				continue
			}
			if node.NodeType == nodeType {
				matchingIDs[id] = struct{}{}
			}
		}
	}

	// Step 3: filter edges by membership in matchingIDs.
	var out []graph.Edge
	for _, e := range edges {
		if e.NodeSourceID != nil {
			if _, ok := matchingIDs[*e.NodeSourceID]; ok {
				out = append(out, e)
				continue
			}
		}
		if e.NodeTargetID != nil {
			if _, ok := matchingIDs[*e.NodeTargetID]; ok {
				out = append(out, e)
			}
		}
	}
	return out
}

func (s *Server) graphTraverse(ctx context.Context, a graphArgs) (string, error) {
	if a.MemoryID == 0 {
		return "", fmt.Errorf("memory_id required")
	}
	depth := a.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > graph.MaxTraverseDepth {
		return "", fmt.Errorf("max depth is %d", graph.MaxTraverseDepth)
	}
	if !s.graphMemoryVisible(ctx, a.MemoryID) {
		return marshalJSON(map[string]any{"memory_id": a.MemoryID, "depth": depth, "count": 0, "results": []graph.TraversalResult{}})
	}
	results, err := s.graphStore.TraverseVisible(ctx, a.MemoryID, depth, a.EdgeTypes, func(edge *graph.Edge) bool {
		return s.graphTraversalEdgeVisible(ctx, edge)
	})
	if err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"memory_id": a.MemoryID,
		"depth":     depth,
		"count":     len(results),
		"results":   results,
	})
}

func (s *Server) graphFindPath(ctx context.Context, a graphArgs) (string, error) {
	if a.SourceID == 0 || a.TargetID == 0 {
		return "", fmt.Errorf("source_id and target_id required")
	}
	if !s.graphMemoryVisible(ctx, a.SourceID) || !s.graphMemoryVisible(ctx, a.TargetID) {
		return marshalJSON(map[string]any{"source_id": a.SourceID, "target_id": a.TargetID, "found": false, "hops": 0, "path": []graph.TraversalResult{}})
	}
	maxDepth := a.MaxDepth
	if maxDepth <= 0 {
		maxDepth = graph.MaxTraverseDepth
	}
	path, err := s.graphStore.FindPathVisible(ctx, a.SourceID, a.TargetID, maxDepth, func(edge *graph.Edge) bool {
		return s.graphTraversalEdgeVisible(ctx, edge)
	})
	if err != nil {
		return "", err
	}
	found := path != nil
	return marshalJSON(map[string]any{
		"source_id": a.SourceID,
		"target_id": a.TargetID,
		"found":     found,
		"hops":      len(path),
		"path":      path,
	})
}

func (s *Server) graphSynonyms(ctx context.Context, a graphArgs) (string, error) {
	if a.MemoryID == 0 {
		return "", fmt.Errorf("memory_id required")
	}
	if !s.graphMemoryVisible(ctx, a.MemoryID) {
		return marshalJSON(map[string]any{"memory_id": a.MemoryID, "count": 0, "synonyms": []graph.Edge{}})
	}
	edges, err := s.graphStore.FindSynonyms(ctx, a.MemoryID)
	if err != nil {
		return "", err
	}
	edges = s.filterVisibleGraphEdges(ctx, edges)
	return marshalJSON(map[string]any{
		"memory_id": a.MemoryID,
		"count":     len(edges),
		"synonyms":  edges,
	})
}
