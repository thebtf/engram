package graph

import (
	"context"
	"fmt"
)

const MaxTraverseDepth = 3

// TraverseVisible performs a bounded BFS, checking edges before exploring their endpoints.
func (s *Store) TraverseVisible(ctx context.Context, startID int64, maxDepth int, edgeTypes []string, visible func(*Edge) bool) ([]TraversalResult, error) {
	return s.traverse(ctx, startID, maxDepth, edgeTypes, visible)
}

// Traverse performs an unfiltered bounded BFS for internal callers.
func (s *Store) Traverse(ctx context.Context, startID int64, maxDepth int, edgeTypes []string) ([]TraversalResult, error) {
	return s.traverse(ctx, startID, maxDepth, edgeTypes, nil)
}

func (s *Store) traverse(ctx context.Context, startID int64, maxDepth int, edgeTypes []string, visible func(*Edge) bool) ([]TraversalResult, error) {
	if maxDepth <= 0 || maxDepth > MaxTraverseDepth {
		maxDepth = MaxTraverseDepth
	}

	visited := map[int64]bool{startID: true}
	visitedEdges := map[int64]bool{}
	var results []TraversalResult
	frontier := []int64{startID}

	for depth := 1; depth <= maxDepth; depth++ {
		if len(frontier) == 0 {
			break
		}
		var nextFrontier []int64
		for _, nodeID := range frontier {
			edges, err := s.ListByMemory(ctx, nodeID, Both, "")
			if err != nil {
				return results, fmt.Errorf("traverse depth %d: %w", depth, err)
			}
			for _, e := range edges {
				if visitedEdges[e.ID] {
					continue
				}
				visitedEdges[e.ID] = true
				if len(edgeTypes) > 0 && !containsStr(edgeTypes, e.EdgeType) {
					continue
				}
				if visible != nil && !visible(&e) {
					continue
				}
				// Node endpoints have no memory ID; preserve their typed ID in the result.
				var srcID, tgtID int64
				if e.SourceID != nil {
					srcID = *e.SourceID
				}
				if e.TargetID != nil {
					tgtID = *e.TargetID
				}
				neighborID := tgtID
				if neighborID == nodeID {
					neighborID = srcID
				}
				results = append(results, TraversalResult{
					EdgeID: e.ID, SourceID: srcID, TargetID: tgtID,
					NodeSourceID: e.NodeSourceID, NodeTargetID: e.NodeTargetID,
					EdgeType: e.EdgeType, Weight: e.Weight, Reasoning: e.Reasoning, Depth: depth,
				})
				// A node endpoint has no memory ID to expand.
				if e.SourceID != nil && e.TargetID != nil && !visited[neighborID] {
					visited[neighborID] = true
					nextFrontier = append(nextFrontier, neighborID)
				}
			}
		}
		frontier = nextFrontier
	}
	return results, nil
}

// FindPathVisible finds the shortest memory path without crossing inaccessible edges.
func (s *Store) FindPathVisible(ctx context.Context, sourceID, targetID int64, maxDepth int, visible func(*Edge) bool) ([]TraversalResult, error) {
	return s.findPath(ctx, sourceID, targetID, maxDepth, visible)
}

// FindPath finds an unfiltered shortest memory path for internal callers.
func (s *Store) FindPath(ctx context.Context, sourceID, targetID int64, maxDepth int) ([]TraversalResult, error) {
	return s.findPath(ctx, sourceID, targetID, maxDepth, nil)
}

func (s *Store) findPath(ctx context.Context, sourceID, targetID int64, maxDepth int, visible func(*Edge) bool) ([]TraversalResult, error) {
	if maxDepth <= 0 || maxDepth > MaxTraverseDepth {
		maxDepth = MaxTraverseDepth
	}
	if sourceID == targetID {
		return nil, nil
	}

	type pathNode struct {
		id   int64
		path []TraversalResult
	}

	visited := map[int64]bool{sourceID: true}
	queue := []pathNode{{id: sourceID}}

	for depth := 1; depth <= maxDepth; depth++ {
		if len(queue) == 0 {
			break
		}
		var nextQueue []pathNode
		for _, node := range queue {
			edges, err := s.ListByMemory(ctx, node.id, Both, "")
			if err != nil {
				return nil, fmt.Errorf("find path depth %d: %w", depth, err)
			}
			for _, e := range edges {
				if e.SourceID == nil || e.TargetID == nil || (visible != nil && !visible(&e)) {
					continue
				}
				srcID, tgtID := *e.SourceID, *e.TargetID
				neighborID := tgtID
				if neighborID == node.id {
					neighborID = srcID
				}
				if visited[neighborID] {
					continue
				}
				visited[neighborID] = true
				step := TraversalResult{
					EdgeID:   e.ID,
					SourceID: srcID,
					TargetID: tgtID,
					EdgeType: e.EdgeType,
					Weight:   e.Weight,
					Depth:    depth,
				}
				newPath := make([]TraversalResult, len(node.path)+1)
				copy(newPath, node.path)
				newPath[len(node.path)] = step
				if neighborID == targetID {
					return newPath, nil
				}
				nextQueue = append(nextQueue, pathNode{id: neighborID, path: newPath})
			}
		}
		queue = nextQueue
	}
	return nil, nil
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
