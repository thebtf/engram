package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/thebtf/engram/internal/graph"
)

type graphArgs struct {
	Action    string   `json:"action"`
	SourceID  int64    `json:"source_id"`
	TargetID  int64    `json:"target_id"`
	MemoryID  int64    `json:"memory_id"`
	EdgeID    int64    `json:"edge_id"`
	EdgeType  string   `json:"edge_type"`
	Weight    float64  `json:"weight"`
	Reasoning string   `json:"reasoning"`
	Direction string   `json:"direction"`
	Depth     int      `json:"depth"`
	EdgeTypes []string `json:"edge_types"`
	MaxDepth  int      `json:"max_depth"`
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
	case "add_edge":
		return s.graphAddEdge(ctx, a)
	case "remove_edge":
		return s.graphRemoveEdge(ctx, a)
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

func (s *Server) graphAddEdge(ctx context.Context, a graphArgs) (string, error) {
	if a.SourceID == 0 || a.TargetID == 0 {
		return "", fmt.Errorf("source_id and target_id required")
	}
	if a.EdgeType == "" {
		return "", fmt.Errorf("edge_type required")
	}
	if !graph.ValidEdgeType(a.EdgeType) {
		return "", fmt.Errorf("invalid edge_type: %s", a.EdgeType)
	}
	if a.Weight == 0 {
		a.Weight = 1.0
	}
	edge := &graph.Edge{
		SourceID:  a.SourceID,
		TargetID:  a.TargetID,
		EdgeType:  a.EdgeType,
		Weight:    a.Weight,
		Reasoning: a.Reasoning,
	}
	created, err := s.graphStore.Create(ctx, edge)
	if err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"edge_id":   created.ID,
		"source_id": created.SourceID,
		"target_id": created.TargetID,
		"edge_type": created.EdgeType,
		"message":   "edge created",
	})
}

func (s *Server) graphRemoveEdge(ctx context.Context, a graphArgs) (string, error) {
	if a.EdgeID == 0 {
		return "", fmt.Errorf("edge_id required")
	}
	if err := s.graphStore.SoftDelete(ctx, a.EdgeID); err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"edge_id": a.EdgeID,
		"message": "edge removed",
	})
}

func (s *Server) graphGetEdges(ctx context.Context, a graphArgs) (string, error) {
	if a.MemoryID == 0 {
		return "", fmt.Errorf("memory_id required")
	}
	dir := graph.Both
	switch a.Direction {
	case "outgoing":
		dir = graph.Outgoing
	case "incoming":
		dir = graph.Incoming
	}
	edges, err := s.graphStore.ListByMemory(ctx, a.MemoryID, dir, a.EdgeType)
	if err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"memory_id": a.MemoryID,
		"direction": a.Direction,
		"count":     len(edges),
		"edges":     edges,
	})
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
	results, err := s.graphStore.Traverse(ctx, a.MemoryID, depth, a.EdgeTypes)
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
	maxDepth := a.MaxDepth
	if maxDepth <= 0 {
		maxDepth = graph.MaxTraverseDepth
	}
	path, err := s.graphStore.FindPath(ctx, a.SourceID, a.TargetID, maxDepth)
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
	edges, err := s.graphStore.FindSynonyms(ctx, a.MemoryID)
	if err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"memory_id": a.MemoryID,
		"count":     len(edges),
		"synonyms":  edges,
	})
}
