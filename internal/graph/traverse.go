package graph

import (
	"context"
	"fmt"
)

const MaxTraverseDepth = 3

// Traverse performs a BFS traversal from startID up to the given depth.
// Uses a visited set to prevent cycles. Returns all edges encountered.
func (s *Store) Traverse(ctx context.Context, startID int64, maxDepth int, edgeTypes []string) ([]TraversalResult, error) {
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
				// SourceID/TargetID are nullable (*int64); dereference safely for
				// memory-typed traverse (node-typed edges are excluded by ListByMemory
				// which filters by source_id/target_id, so nil values are not expected
				// on this code path — guard defensively).
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
					EdgeID:    e.ID,
					SourceID:  srcID,
					TargetID:  tgtID,
					EdgeType:  e.EdgeType,
					Weight:    e.Weight,
					Reasoning: e.Reasoning,
					Depth:     depth,
				})
				if !visited[neighborID] {
					visited[neighborID] = true
					nextFrontier = append(nextFrontier, neighborID)
				}
			}
		}
		frontier = nextFrontier
	}
	return results, nil
}

// FindPath returns the shortest path between source and target using BFS.
func (s *Store) FindPath(ctx context.Context, sourceID, targetID int64, maxDepth int) ([]TraversalResult, error) {
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
				var srcID, tgtID int64
				if e.SourceID != nil {
					srcID = *e.SourceID
				}
				if e.TargetID != nil {
					tgtID = *e.TargetID
				}
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
