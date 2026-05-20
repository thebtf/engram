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
				if len(edgeTypes) > 0 && !containsStr(edgeTypes, e.EdgeType) {
					continue
				}
				neighborID := e.TargetID
				if neighborID == nodeID {
					neighborID = e.SourceID
				}
				results = append(results, TraversalResult{
					EdgeID:    e.ID,
					SourceID:  e.SourceID,
					TargetID:  e.TargetID,
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
				neighborID := e.TargetID
				if neighborID == node.id {
					neighborID = e.SourceID
				}
				if visited[neighborID] {
					continue
				}
				visited[neighborID] = true
				step := TraversalResult{
					EdgeID:   e.ID,
					SourceID: e.SourceID,
					TargetID: e.TargetID,
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
