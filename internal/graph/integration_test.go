package graph

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	engramgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm/logger"
)

func openGraphIntegrationStore(t *testing.T, dsn string) *engramgorm.Store {
	t.Helper()
	store, err := engramgorm.NewStore(engramgorm.Config{
		DSN:      dsn,
		MaxConns: 2,
		LogLevel: logger.Silent,
	})
	require.NoError(t, err, "initialize graph integration DB")
	t.Cleanup(func() { assert.NoError(t, store.Close(), "close graph integration DB") })
	return store
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
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping T015 integration test")
	}

	db := openGraphIntegrationStore(t, dsn).DB

	ctx := context.Background()
	ns := NewNodesStore(db)
	gs := NewStore(db, ns)

	project := "t015-test-" + uuid.NewString()
	t.Cleanup(func() {
		assert.NoError(t, db.Exec(`DELETE FROM knowledge_edges WHERE source_session_id = ?`, project).Error)
		assert.NoError(t, db.Exec(`DELETE FROM memories WHERE project = ?`, project).Error)
		assert.NoError(t, db.Exec(`DELETE FROM knowledge_nodes WHERE project = ?`, project).Error)
	})

	// Insert a test memory to use as edge target.
	var memID int64
	require.NoError(t, db.Raw(
		`INSERT INTO memories (project, content) VALUES (?, 'roundtrip target') RETURNING id`, project,
	).Row().Scan(&memID))

	// Step 1: Create a skill node.
	node, err := ns.Create(ctx, &models.KnowledgeNode{
		NodeType:    models.NodeTypeSkill,
		ExternalRef: "nvmd-architect",
		Project:     project,
	})
	require.NoError(t, err)
	require.NotZero(t, node.ID)
	assert.Equal(t, models.NodeTypeSkill, node.NodeType)
	assert.Equal(t, "project", node.PrivacyScope) // default

	// Step 2: Create edge skill→memory (source_type='node', target_type='memory').
	// TargetID is *int64 (nullable); set via pointer for memory-typed endpoint.
	edge := &Edge{
		SourceType:      "node",
		TargetType:      "memory",
		TargetID:        &memID,
		NodeSourceID:    &node.ID,
		EdgeType:        EdgeUses,
		Weight:          1.0,
		SourceSessionID: project,
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
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping T015 integration test")
	}

	db := openGraphIntegrationStore(t, dsn).DB

	ctx := context.Background()
	ns := NewNodesStore(db)
	gs := NewStore(db, ns)

	project := "t015b-test-" + uuid.NewString()
	t.Cleanup(func() {
		assert.NoError(t, db.Exec(`DELETE FROM knowledge_edges WHERE source_session_id = ?`, project).Error)
		assert.NoError(t, db.Exec(`DELETE FROM memories WHERE project = ?`, project).Error)
		assert.NoError(t, db.Exec(`DELETE FROM knowledge_nodes WHERE project = ?`, project).Error)
	})

	// Setup: two memories, one node.
	var mem1, mem2 int64
	require.NoError(t, db.Raw(
		`INSERT INTO memories (project, content) VALUES (?, 'mem1') RETURNING id`, project,
	).Row().Scan(&mem1))
	require.NoError(t, db.Raw(
		`INSERT INTO memories (project, content) VALUES (?, 'mem2') RETURNING id`, project,
	).Row().Scan(&mem2))

	node, err := ns.Create(ctx, &models.KnowledgeNode{
		NodeType:    models.NodeTypeAgent,
		ExternalRef: "engram-agent-v2",
		Project:     project,
	})
	require.NoError(t, err)

	// Create two outgoing edges with distinct allowed code edge types.
	fixtures := []struct {
		memoryID int64
		edgeType string
	}{
		{memoryID: mem1, edgeType: EdgeUses},
		{memoryID: mem2, edgeType: EdgeImports},
	}
	for _, fixture := range fixtures {
		id := fixture.memoryID
		_, err := gs.Create(ctx, &Edge{
			SourceType:      "node",
			TargetType:      "memory",
			TargetID:        &id,
			NodeSourceID:    &node.ID,
			EdgeType:        fixture.edgeType,
			Weight:          1.0,
			SourceSessionID: project,
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

	for _, fixture := range fixtures {
		filtered, err := gs.ListByNode(ctx, node.ID, Outgoing, fixture.edgeType)
		require.NoError(t, err)
		require.Len(t, filtered, 1, "edge-type filter must select only its own target")
		assert.Equal(t, fixture.edgeType, filtered[0].EdgeType)
		require.NotNil(t, filtered[0].TargetID)
		assert.Equal(t, fixture.memoryID, *filtered[0].TargetID)
	}
	absent, err := gs.ListByNode(ctx, node.ID, Outgoing, EdgeDependsOn)
	require.NoError(t, err)
	assert.Empty(t, absent, "an allowed but absent edge type must not match")

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
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping T015 integration test")
	}

	db := openGraphIntegrationStore(t, dsn).DB

	ctx := context.Background()
	ns := NewNodesStore(db)

	project := "t015c-test-" + uuid.NewString()
	t.Cleanup(func() {
		assert.NoError(t, db.Exec(`DELETE FROM knowledge_nodes WHERE project = ?`, project).Error)
	})

	before := time.Now().UTC().Add(-time.Second)
	node, err := ns.Create(ctx, &models.KnowledgeNode{
		NodeType:    models.NodeTypeRule,
		ExternalRef: "my-rule-001",
		Project:     project,
	})
	after := time.Now().UTC().Add(time.Second)

	require.NoError(t, err)
	assert.True(t, node.CreatedAt.After(before), "CreatedAt must be after %v, got %v", before, node.CreatedAt)
	assert.True(t, node.CreatedAt.Before(after), "CreatedAt must be before %v, got %v", after, node.CreatedAt)
}

// TestNodesStore_MetadataDefaults_Create verifies that metadata persists in canonical JSON form.
func TestNodesStore_MetadataDefaults_Create(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping metadata integration test")
	}

	db := openGraphIntegrationStore(t, dsn).DB

	project := "graph-metadata-create-test-" + uuid.NewString()
	t.Cleanup(func() { assert.NoError(t, db.Exec("DELETE FROM knowledge_nodes WHERE project = ?", project).Error) })
	ns := NewNodesStore(db)

	for _, tc := range []struct {
		name     string
		metadata []byte
		expected string
	}{
		{name: "nil", expected: "{}"},
		{name: "empty", metadata: []byte{}, expected: "{}"},
		{name: "whitespace", metadata: []byte(" \t\n"), expected: "{}"},
		{name: "null", metadata: []byte("null"), expected: "{}"},
		{name: "whitespace-null", metadata: []byte(" \tnull\n"), expected: "{}"},
		{name: "canonical-object", metadata: []byte(`{"source":"test"}`), expected: `{"source":"test"}`},
		{name: "spaced-object", metadata: []byte(" { \"z\" : [ true, false ], \"a\" : { \"nested\" : 1 } } "), expected: `{"a":{"nested":1},"z":[true,false]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, err := ns.Create(context.Background(), &models.KnowledgeNode{
				NodeType: models.NodeTypeSkill, ExternalRef: "create-" + tc.name, Project: project, Metadata: tc.metadata,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.expected, string(node.Metadata))

			stored, err := ns.Get(context.Background(), node.ID, true)
			require.NoError(t, err)
			assert.JSONEq(t, tc.expected, string(stored.Metadata))
		})
	}
}

// TestNodesStore_MetadataDefaults_Update verifies that metadata persists in canonical JSON form on updates.
func TestNodesStore_MetadataDefaults_Update(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping metadata integration test")
	}

	db := openGraphIntegrationStore(t, dsn).DB

	project := "graph-metadata-update-test-" + uuid.NewString()
	t.Cleanup(func() { assert.NoError(t, db.Exec("DELETE FROM knowledge_nodes WHERE project = ?", project).Error) })
	ns := NewNodesStore(db)

	for _, tc := range []struct {
		name     string
		metadata []byte
		expected string
	}{
		{name: "nil", expected: "{}"},
		{name: "empty", metadata: []byte{}, expected: "{}"},
		{name: "whitespace", metadata: []byte(" \t\n"), expected: "{}"},
		{name: "null", metadata: []byte("null"), expected: "{}"},
		{name: "whitespace-null", metadata: []byte(" \tnull\n"), expected: "{}"},
		{name: "canonical-object", metadata: []byte(`{"source":"test"}`), expected: `{"source":"test"}`},
		{name: "spaced-object", metadata: []byte(" { \"z\" : [ true, false ], \"a\" : { \"nested\" : 1 } } "), expected: `{"a":{"nested":1},"z":[true,false]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, err := ns.Create(context.Background(), &models.KnowledgeNode{
				NodeType: models.NodeTypeSkill, ExternalRef: "update-" + tc.name, Project: project, Metadata: []byte(`{"initial":true}`),
			})
			require.NoError(t, err)

			node.Metadata = tc.metadata
			_, err = ns.Update(context.Background(), node)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, string(node.Metadata))

			stored, err := ns.Get(context.Background(), node.ID, true)
			require.NoError(t, err)
			assert.JSONEq(t, tc.expected, string(stored.Metadata))
		})
	}
}
