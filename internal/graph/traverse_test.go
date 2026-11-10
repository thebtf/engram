package graph

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	_ "modernc.org/sqlite"
)

// newTraversalStore uses a private SQLite database with the production GORM query path.
// It does not exercise PostgreSQL-specific behavior or the production migrations.
func newTraversalStore(t *testing.T, edges ...Edge) *Store {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })
	sqlDB.SetMaxOpenConns(1)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE knowledge_edges (
		id INTEGER PRIMARY KEY, source_id INTEGER, target_id INTEGER,
		edge_type TEXT NOT NULL, weight REAL NOT NULL, created_at DATETIME NOT NULL,
		source_type TEXT NOT NULL, target_type TEXT NOT NULL, superseded_at DATETIME
	)`).Error)
	for i, e := range edges {
		createdAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Second)
		require.NoError(t, db.Exec(`INSERT INTO knowledge_edges
			(id, source_id, target_id, edge_type, weight, created_at, source_type, target_type, superseded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.SourceID, e.TargetID, e.EdgeType, e.Weight, createdAt, "memory", "memory", e.SupersededAt).Error)
	}
	return NewStore(db, nil)
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
	ta := newTraversalStore(t,
		edge(1, 1, 2, EdgeUses),
		edge(2, 2, 3, EdgeDependsOn),
	)
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
	ta := newTraversalStore(t,
		edge(1, 1, 2, EdgeUses),
		edge(2, 2, 1, EdgeUses),
	)
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
	ta := newTraversalStore(t,
		edge(1, 1, 2, EdgeUses),
		edge(2, 2, 3, EdgeUses),
		edge(3, 3, 4, EdgeUses),
		edge(4, 4, 5, EdgeUses),
	)
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
	ta := newTraversalStore(t) // no edges at all
	ctx := context.Background()

	results, err := ta.Traverse(ctx, 42, 3, nil)
	require.NoError(t, err)
	assert.Empty(t, results, "no edges should yield empty traversal")
}

// TestTraverse_DepthLimitClamping verifies that maxDepth <= 0 is clamped to MaxTraverseDepth (3).
func TestTraverse_DepthLimitClamping(t *testing.T) {
	// Chain of 4 hops; depth 0 should be clamped to 3, returning 3 edges.
	ta := newTraversalStore(t,
		edge(1, 1, 2, EdgeUses),
		edge(2, 2, 3, EdgeUses),
		edge(3, 3, 4, EdgeUses),
		edge(4, 4, 5, EdgeUses),
	)
	ctx := context.Background()

	results, err := ta.Traverse(ctx, 1, 0, nil)
	require.NoError(t, err)
	assert.Len(t, results, MaxTraverseDepth, "depth 0 should clamp to MaxTraverseDepth")
}

// TestSynonymLookup verifies that FindSynonyms returns synonym_of and
// same_concept_as edges for a given memory and ignores other edge types.
func TestSynonymLookup(t *testing.T) {
	ta := newTraversalStore(t,
		edge(1, 10, 20, EdgeSynonymOf),
		edge(2, 10, 30, EdgeSameConceptAs),
		edge(3, 10, 40, EdgeUses), // not a synonym edge — should be excluded
	)
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
	ta := newTraversalStore(t,
		edge(1, 1, 2, EdgeUses),
		edge(2, 2, 3, EdgeUses),
		edge(3, 3, 4, EdgeUses),
		edge(4, 4, 5, EdgeUses),
		edge(5, 5, 6, EdgeUses),
	)
	ctx := context.Background()

	results, err := ta.Traverse(ctx, 1, 10, nil)
	require.NoError(t, err)
	for _, r := range results {
		assert.LessOrEqual(t, r.Depth, MaxTraverseDepth, "depth clamped to MaxTraverseDepth")
	}
	assert.Len(t, results, MaxTraverseDepth, "should return exactly MaxTraverseDepth edges when chain is longer")
}
