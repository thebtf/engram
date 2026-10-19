package graph

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	engramgorm "github.com/thebtf/engram/internal/db/gorm"
)

// TestDangling_T016_DanglingEdgeReturnsFlag verifies EC-F7:
// When an edge references a soft-deleted memory,
// Resolve must return ErrDangling (NOT a generic error), and the
// return signature is (source, nil, ErrDangling).
//
// The test creates real memory endpoints and an edge, then soft-deletes the
// target through MemoryStore. The FK stays intact, but Resolve cannot see the
// deleted target — this is the "dangling" condition.
//
// DSN-gated: skips when DATABASE_DSN is not set.
//
// Anti-stub: replacing `return source, nil, ErrDangling` in resolveEndpoint
// with `return source, nil, nil` causes this test to fail because
// errors.Is(err, ErrDangling) returns false.
//
// Engram vNext Milestone F TG2 / T016.
func TestDangling_T016_DanglingEdgeReturnsFlag(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping T016 dangling acceptance test")
	}

	store := openGraphIntegrationStore(t, dsn)
	db := store.DB

	ctx := context.Background()
	ns := NewNodesStore(db)
	gs := NewStore(db, ns)
	ms := engramgorm.NewMemoryStore(store)
	project := newGraphIntegrationProject(t, db)
	t.Cleanup(func() {
		assert.NoError(t, db.Exec(`DELETE FROM knowledge_edges WHERE source_session_id = ?`, project).Error)
		assert.NoError(t, db.Exec(`DELETE FROM memories WHERE project = ?`, project).Error)
	})

	var targetMemID, srcMemID int64
	require.NoError(t, db.Raw(
		`INSERT INTO memories (project, content) VALUES (?, 'dangling-target') RETURNING id`, project,
	).Row().Scan(&targetMemID))
	require.NoError(t, db.Raw(
		`INSERT INTO memories (project, content) VALUES (?, 'dangling-source') RETURNING id`, project,
	).Row().Scan(&srcMemID))

	edge, err := gs.Create(ctx, &Edge{
		SourceID:        &srcMemID,
		TargetID:        &targetMemID,
		SourceType:      "memory",
		TargetType:      "memory",
		EdgeType:        EdgeUses,
		Weight:          1.0,
		SourceSessionID: project,
	})
	require.NoError(t, err)
	require.NoError(t, ms.Delete(ctx, targetMemID), "soft-delete target without breaking the edge FK")

	fetchedEdge, err := gs.Get(ctx, edge.ID)
	require.NoError(t, err)
	edges, err := gs.ListByMemory(ctx, srcMemID, Outgoing, EdgeUses)
	require.NoError(t, err)
	require.Len(t, edges, 1, "dangling edge must remain visible in the edge list")
	assert.Equal(t, edge.ID, edges[0].ID)

	src, tgt, resolveErr := gs.Resolve(ctx, fetchedEdge)
	require.Nil(t, tgt, "soft-deleted target must not resolve")
	require.Equal(t, srcMemID, src, "source must still resolve to its memory ID")
	require.ErrorIs(t, resolveErr, ErrDangling, "Resolve must flag a dangling target")

	t.Logf("EC-F7 PASS: Resolve returned ErrDangling for edge %d (target memory %d soft-deleted)", edge.ID, targetMemID)
}
