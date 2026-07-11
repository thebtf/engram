package graph

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openGraphTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping graph integration test")
	}

	store, err := gormdb.NewStore(gormdb.Config{
		DSN:      dsn,
		MaxConns: 2,
		LogLevel: logger.Warn,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	return store.GetDB()
}

func cleanupGraphFixture(t *testing.T, db *gorm.DB, project, session string) {
	t.Helper()
	require.NoError(t, db.Exec(`DELETE FROM knowledge_edges WHERE source_session_id = ?`, session).Error)
	require.NoError(t, db.Exec(`DELETE FROM knowledge_nodes WHERE project = ?`, project).Error)
	require.NoError(t, db.Exec(`DELETE FROM memories WHERE project = ?`, project).Error)
}

// TestPathC_T015_SkillNodeEdgeRoundtrip verifies the full TG2 Path C roundtrip:
//  1. Create a skill knowledge_node via NodesStore.
//  2. Create a 'uses' edge from the node to an existing memory.
//  3. List edges by node via Store.ListByNode.
//  4. Resolve the edge via Store.Resolve — source must be *models.KnowledgeNode,
//     target must be an int64 (memory row ID).
//
// DSN-gated: skips when DATABASE_DSN is not set.
// Anti-stub: removing NodesStore.Create or Store.Resolve causes the roundtrip
// to fail at the relevant assertion.
//
// Engram vNext Milestone F TG2 / T015.
func TestPathC_T015_SkillNodeEdgeRoundtrip(t *testing.T) {
	db := openGraphTestDB(t)
	cleanupGraphFixture(t, db, "t015-test", "t015-test")
	t.Cleanup(func() {
		cleanupGraphFixture(t, db, "t015-test", "t015-test")
	})

	ctx := context.Background()
	ns := NewNodesStore(db)
	gs := NewStore(db, ns)

	// Insert a test memory to use as edge target.
	var memID int64
	require.NoError(t, db.Raw(
		`INSERT INTO memories (project, content) VALUES ('t015-test', 'roundtrip target') RETURNING id`,
	).Row().Scan(&memID))

	// Step 1: Create a skill node.
	node, err := ns.Create(ctx, &models.KnowledgeNode{
		NodeType:    models.NodeTypeSkill,
		ExternalRef: "nvmd-architect",
		Project:     "t015-test",
	})
	require.NoError(t, err)
	require.NotZero(t, node.ID)
	assert.Equal(t, models.NodeTypeSkill, node.NodeType)
	assert.Equal(t, "project", node.PrivacyScope) // default
	require.JSONEq(t, `{}`, string(node.Metadata), "omitted metadata must persist as an empty JSON object")

	// Step 2: Create edge skill→memory (source_type='node', target_type='memory').
	// TargetID is *int64 (nullable); set via pointer for memory-typed endpoint.
	edge := &Edge{
		SourceType:      "node",
		TargetType:      "memory",
		TargetID:        &memID,
		NodeSourceID:    &node.ID,
		EdgeType:        "uses",
		Weight:          1.0,
		SourceSessionID: "t015-test",
	}
	created, err := gs.Create(ctx, edge)
	require.NoError(t, err)
	require.NotZero(t, created.ID)

	// Step 3: List edges by node — must return our edge.
	edges, err := gs.ListByNode(ctx, node.ID, Outgoing, "")
	require.NoError(t, err)
	require.Len(t, edges, 1, "expected 1 outgoing edge from skill node")
	assert.Equal(t, created.ID, edges[0].ID)

	// Step 4: Resolve the edge — source must be KnowledgeNode, target int64.
	src, tgt, resolveErr := gs.Resolve(ctx, &edges[0])
	require.NoError(t, resolveErr)
	require.NotNil(t, src)
	require.NotNil(t, tgt)

	srcNode, ok := src.(*models.KnowledgeNode)
	require.True(t, ok, "Resolve source must be *models.KnowledgeNode, got %T", src)
	assert.Equal(t, node.ID, srcNode.ID)
	assert.Equal(t, models.NodeTypeSkill, srcNode.NodeType)

	// Target is the memory row ID (int64) as the minimal resolved form.
	tgtID, ok := tgt.(int64)
	require.True(t, ok, "Resolve target must be int64 (memory row ID), got %T", tgt)
	assert.Equal(t, memID, tgtID)
}

// TestPathC_T015_NodeTypedEdgeListFilter verifies that get_edges by node_id
// returns the correct subset when multiple edges exist.
func TestPathC_T015_NodeTypedEdgeListFilter(t *testing.T) {
	db := openGraphTestDB(t)
	cleanupGraphFixture(t, db, "t015b-test", "t015b-test")
	t.Cleanup(func() {
		cleanupGraphFixture(t, db, "t015b-test", "t015b-test")
	})

	ctx := context.Background()
	ns := NewNodesStore(db)
	gs := NewStore(db, ns)

	// Setup: two memories, one node.
	var mem1, mem2 int64
	require.NoError(t, db.Raw(
		`INSERT INTO memories (project, content) VALUES ('t015b-test', 'mem1') RETURNING id`,
	).Row().Scan(&mem1))
	require.NoError(t, db.Raw(
		`INSERT INTO memories (project, content) VALUES ('t015b-test', 'mem2') RETURNING id`,
	).Row().Scan(&mem2))

	node, err := ns.Create(ctx, &models.KnowledgeNode{
		NodeType:    models.NodeTypeAgent,
		ExternalRef: "engram-agent-v2",
		Project:     "t015b-test",
	})
	require.NoError(t, err)

	// Create two outgoing edges from node.
	for _, memID := range []int64{mem1, mem2} {
		id := memID
		_, err := gs.Create(ctx, &Edge{
			SourceType:      "node",
			TargetType:      "memory",
			TargetID:        &id,
			NodeSourceID:    &node.ID,
			EdgeType:        EdgeDependsOn,
			Weight:          1.0,
			SourceSessionID: "t015b-test",
		})
		require.NoError(t, err)
	}

	// ListByNode outgoing must return exactly 2.
	edges, err := gs.ListByNode(ctx, node.ID, Outgoing, "")
	require.NoError(t, err)
	assert.Len(t, edges, 2, "expected 2 outgoing edges from agent node")

	// ListByNode incoming must return 0.
	incoming, err := gs.ListByNode(ctx, node.ID, Incoming, "")
	require.NoError(t, err)
	assert.Empty(t, incoming, "expected 0 incoming edges for agent node")

	// Resolve each edge and verify times are reasonable (non-zero).
	for _, e := range edges {
		ec := e // capture
		src, tgt, rerr := gs.Resolve(ctx, &ec)
		require.NoError(t, rerr)
		srcNode, ok := src.(*models.KnowledgeNode)
		require.True(t, ok)
		assert.Equal(t, node.ID, srcNode.ID)
		tgtID, ok := tgt.(int64)
		require.True(t, ok)
		assert.True(t, tgtID == mem1 || tgtID == mem2, "unexpected target memory ID: %d", tgtID)
		_ = srcNode
	}
}

// TestPathC_T015_NodeCreatedAtTimestamp verifies that knowledge_nodes
// get sensible timestamps after creation.
func TestPathC_T015_NodeCreatedAtTimestamp(t *testing.T) {
	db := openGraphTestDB(t)
	cleanupGraphFixture(t, db, "t015c-test", "t015c-test")
	t.Cleanup(func() {
		cleanupGraphFixture(t, db, "t015c-test", "t015c-test")
	})

	ctx := context.Background()
	ns := NewNodesStore(db)

	before := time.Now().UTC().Add(-time.Second)
	node, err := ns.Create(ctx, &models.KnowledgeNode{
		NodeType:    models.NodeTypeRule,
		ExternalRef: "my-rule-001",
		Project:     "t015c-test",
	})
	after := time.Now().UTC().Add(time.Second)

	require.NoError(t, err)
	assert.True(t, node.CreatedAt.After(before), "CreatedAt must be after %v, got %v", before, node.CreatedAt)
	assert.True(t, node.CreatedAt.Before(after), "CreatedAt must be before %v, got %v", after, node.CreatedAt)
}

// TestPathC_T015_UpdateOmittedMetadataDefaultsEmptyObject closes the same
// NOT NULL contract for the update path as for create. Callers may omit the
// optional metadata field, but the persisted JSONB value must remain valid.
func TestPathC_T015_UpdateOmittedMetadataDefaultsEmptyObject(t *testing.T) {
	db := openGraphTestDB(t)
	cleanupGraphFixture(t, db, "t015d-test", "t015d-test")
	t.Cleanup(func() {
		cleanupGraphFixture(t, db, "t015d-test", "t015d-test")
	})

	ctx := context.Background()
	ns := NewNodesStore(db)
	node, err := ns.Create(ctx, &models.KnowledgeNode{
		NodeType:    models.NodeTypeRule,
		ExternalRef: "metadata-default-create",
		Project:     "t015d-test",
		Metadata:    []byte(`{"phase":"create"}`),
	})
	require.NoError(t, err)

	node.ExternalRef = "metadata-default-update"
	node.Metadata = nil
	updated, err := ns.Update(ctx, node)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(updated.Metadata))

	reloaded, err := ns.Get(ctx, node.ID, true)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(reloaded.Metadata), "omitted update metadata must persist as an empty JSON object")
}
