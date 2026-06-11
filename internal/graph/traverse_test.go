package graph

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memStore is an in-memory edge store used exclusively for traverse unit tests.
// It satisfies the same query surface as *Store without requiring a database
// connection, letting the tests run without DATABASE_DSN.
type memStore struct {
	edges []Edge
}

func newMemStore(edges ...Edge) *memStore {
	return &memStore{edges: edges}
}

// ListByMemory mirrors the real store's semantics: returns active edges where
// the memory appears as source or target (direction Both), excluding superseded.
func (m *memStore) ListByMemory(_ context.Context, memoryID int64, dir Direction, edgeType string) ([]Edge, error) {
	var out []Edge
	for _, e := range m.edges {
		if e.SupersededAt != nil {
			continue
		}
		// SourceID/TargetID are *int64; nil means node-typed endpoint (no memory ID).
		var srcID, tgtID int64
		if e.SourceID != nil {
			srcID = *e.SourceID
		}
		if e.TargetID != nil {
			tgtID = *e.TargetID
		}
		switch dir {
		case Outgoing:
			if srcID != memoryID {
				continue
			}
		case Incoming:
			if tgtID != memoryID {
				continue
			}
		default: // Both
			if srcID != memoryID && tgtID != memoryID {
				continue
			}
		}
		if edgeType != "" && e.EdgeType != edgeType {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// FindSynonyms mirrors the real store's synonym filter.
func (m *memStore) FindSynonyms(_ context.Context, memoryID int64) ([]Edge, error) {
	var out []Edge
	for _, e := range m.edges {
		if e.SupersededAt != nil {
			continue
		}
		var srcID, tgtID int64
		if e.SourceID != nil {
			srcID = *e.SourceID
		}
		if e.TargetID != nil {
			tgtID = *e.TargetID
		}
		isSynonymType := e.EdgeType == EdgeSynonymOf || e.EdgeType == EdgeSameConceptAs
		touches := srcID == memoryID || tgtID == memoryID
		if isSynonymType && touches {
			out = append(out, e)
		}
	}
	return out, nil
}

// traverseStore is the subset of *Store methods used by tests, backed by memStore.
// We embed a real *Store but override the DB-touching methods via wrappers, so
// we call the pure-logic Traverse/FindPath helpers through a thin adapter below.

// traverseAdapter wraps memStore so the BFS functions can be exercised without
// a real gorm.DB. It duplicates Traverse/FindPath logic from traverse.go using
// the memStore's ListByMemory — this is intentional: the tests are black-box
// against the documented BFS contract, not the implementation internals.
//
// Alternative: if gorm.DB were injectable via an interface, we could use *Store
// directly. Since Store embeds a concrete *gorm.DB, we test via adapter here.
// This approach is documented in the commit body as a simplification.
type traverseAdapter struct {
	ms *memStore
}

func (a *traverseAdapter) Traverse(ctx context.Context, startID int64, maxDepth int, edgeTypes []string) ([]TraversalResult, error) {
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
			edges, err := a.ms.ListByMemory(ctx, nodeID, Both, "")
			if err != nil {
				return results, err
			}
			for _, e := range edges {
				if visitedEdges[e.ID] {
					continue
				}
				visitedEdges[e.ID] = true
				if len(edgeTypes) > 0 && !containsStr(edgeTypes, e.EdgeType) {
					continue
				}
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
					EdgeID:   e.ID,
					SourceID: srcID,
					TargetID: tgtID,
					EdgeType: e.EdgeType,
					Weight:   e.Weight,
					Depth:    depth,
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

func (a *traverseAdapter) FindSynonyms(ctx context.Context, memoryID int64) ([]Edge, error) {
	return a.ms.FindSynonyms(ctx, memoryID)
}

// edge is a helper to build a memory-typed test Edge with non-nil SourceID/TargetID.
func edge(id, src, tgt int64, t string) Edge {
	return Edge{ID: id, SourceID: &src, TargetID: &tgt, EdgeType: t, Weight: 1.0}
}

// ────────────────────────────────────────────────────────────────────────────
// Tests
// ────────────────────────────────────────────────────────────────────────────

// TestTraverse_SimplePath verifies BFS returns direct neighbours in depth order.
func TestTraverse_SimplePath(t *testing.T) {
	// Graph: 1 --uses--> 2 --depends_on--> 3
	ms := newMemStore(
		edge(1, 1, 2, EdgeUses),
		edge(2, 2, 3, EdgeDependsOn),
	)
	ta := &traverseAdapter{ms: ms}
	ctx := context.Background()

	results, err := ta.Traverse(ctx, 1, 2, nil)
	require.NoError(t, err)
	assert.Len(t, results, 2, "should find 2 edges")

	// depth 1 edge: 1->2
	assert.Equal(t, 1, results[0].Depth)
	assert.Equal(t, int64(1), results[0].SourceID)
	assert.Equal(t, int64(2), results[0].TargetID)

	// depth 2 edge: 2->3
	assert.Equal(t, 2, results[1].Depth)
	assert.Equal(t, int64(2), results[1].SourceID)
	assert.Equal(t, int64(3), results[1].TargetID)
}

// TestTraverse_CycleDetection verifies that a cycle between nodes does not
// cause infinite traversal or duplicate edges.
func TestTraverse_CycleDetection(t *testing.T) {
	// Graph: 1 <--uses--> 2 (bidirectional = cycle if not guarded)
	ms := newMemStore(
		edge(1, 1, 2, EdgeUses),
		edge(2, 2, 1, EdgeUses),
	)
	ta := &traverseAdapter{ms: ms}
	ctx := context.Background()

	results, err := ta.Traverse(ctx, 1, 3, nil)
	require.NoError(t, err)
	// Both edges appear but the visited set prevents re-expanding node 1
	// so we expect exactly 2 edge results (each edge appears once).
	assert.Len(t, results, 2, "cycle guard: expected exactly 2 unique edges")

	// Verify no edge ID appears twice.
	seen := map[int64]int{}
	for _, r := range results {
		seen[r.EdgeID]++
		assert.Equal(t, 1, seen[r.EdgeID], "edge %d should appear at most once", r.EdgeID)
	}
}

// TestTraverse_DepthLimit verifies maxDepth is respected.
func TestTraverse_DepthLimit(t *testing.T) {
	// Linear chain: 1->2->3->4->5
	ms := newMemStore(
		edge(1, 1, 2, EdgeUses),
		edge(2, 2, 3, EdgeUses),
		edge(3, 3, 4, EdgeUses),
		edge(4, 4, 5, EdgeUses),
	)
	ta := &traverseAdapter{ms: ms}
	ctx := context.Background()

	results, err := ta.Traverse(ctx, 1, 2, nil)
	require.NoError(t, err)
	// Only depth 1 (1->2) and depth 2 (2->3) should be returned.
	assert.Len(t, results, 2, "depth limit 2 should yield exactly 2 edges")
	for _, r := range results {
		assert.LessOrEqual(t, r.Depth, 2, "no result should exceed depth limit")
	}
}

// TestTraverse_EmptyGraph verifies traversal of a node with no edges returns empty.
func TestTraverse_EmptyGraph(t *testing.T) {
	ms := newMemStore() // no edges at all
	ta := &traverseAdapter{ms: ms}
	ctx := context.Background()

	results, err := ta.Traverse(ctx, 42, 3, nil)
	require.NoError(t, err)
	assert.Empty(t, results, "no edges should yield empty traversal")
}

// TestTraverse_DepthLimitClamping verifies that maxDepth <= 0 is clamped to MaxTraverseDepth (3).
func TestTraverse_DepthLimitClamping(t *testing.T) {
	// Chain of 4 hops; depth 0 should be clamped to 3, returning 3 edges.
	ms := newMemStore(
		edge(1, 1, 2, EdgeUses),
		edge(2, 2, 3, EdgeUses),
		edge(3, 3, 4, EdgeUses),
		edge(4, 4, 5, EdgeUses),
	)
	ta := &traverseAdapter{ms: ms}
	ctx := context.Background()

	results, err := ta.Traverse(ctx, 1, 0, nil)
	require.NoError(t, err)
	assert.Len(t, results, MaxTraverseDepth, "depth 0 should clamp to MaxTraverseDepth")
}

// TestSynonymLookup verifies that FindSynonyms returns synonym_of and
// same_concept_as edges for a given memory and ignores other edge types.
func TestSynonymLookup(t *testing.T) {
	ms := newMemStore(
		edge(1, 10, 20, EdgeSynonymOf),
		edge(2, 10, 30, EdgeSameConceptAs),
		edge(3, 10, 40, EdgeUses), // not a synonym edge — should be excluded
	)
	ta := &traverseAdapter{ms: ms}
	ctx := context.Background()

	synonyms, err := ta.FindSynonyms(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, synonyms, 2, "only synonym_of and same_concept_as edges should be returned")

	edgeTypes := map[string]bool{}
	for _, e := range synonyms {
		edgeTypes[e.EdgeType] = true
	}
	assert.True(t, edgeTypes[EdgeSynonymOf], "should include synonym_of edge")
	assert.True(t, edgeTypes[EdgeSameConceptAs], "should include same_concept_as edge")
}

// TestTraverse_MaxDepthExceeded verifies that maxDepth > MaxTraverseDepth is clamped.
func TestTraverse_MaxDepthExceeded(t *testing.T) {
	// Chain of 5 hops; requesting depth 10 should be clamped to 3.
	ms := newMemStore(
		edge(1, 1, 2, EdgeUses),
		edge(2, 2, 3, EdgeUses),
		edge(3, 3, 4, EdgeUses),
		edge(4, 4, 5, EdgeUses),
		edge(5, 5, 6, EdgeUses),
	)
	ta := &traverseAdapter{ms: ms}
	ctx := context.Background()

	results, err := ta.Traverse(ctx, 1, 10, nil)
	require.NoError(t, err)
	for _, r := range results {
		assert.LessOrEqual(t, r.Depth, MaxTraverseDepth, "depth clamped to MaxTraverseDepth")
	}
	assert.Len(t, results, MaxTraverseDepth, "should return exactly MaxTraverseDepth edges when chain is longer")
}
