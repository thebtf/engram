package graph

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	ctx := context.Background()
	ns := NewNodesStore(db)
	gs := NewStore(db, ns)

	// Insert a test memory to use as edge target.
	var memID int64
	require.NoError(t, db.Raw(
		`INSERT INTO memories (project, content) VALUES ('t015-test', 'roundtrip target') RETURNING id`,
	).Row().Scan(&memID))
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM memories WHERE project = 't015-test'`).Error
		_ = db.Exec(`DELETE FROM knowledge_nodes WHERE project = 't015-test'`).Error
		_ = db.Exec(`DELETE FROM knowledge_edges WHERE source_session_id = 't015-test'`).Error
	})

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
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping T015 integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

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
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM memories WHERE project = 't015b-test'`).Error
		_ = db.Exec(`DELETE FROM knowledge_nodes WHERE project = 't015b-test'`).Error
		_ = db.Exec(`DELETE FROM knowledge_edges WHERE source_session_id = 't015b-test'`).Error
	})

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
			EdgeType:        "references",
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
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping T015 integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	ctx := context.Background()
	ns := NewNodesStore(db)

	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM knowledge_nodes WHERE project = 't015c-test'`).Error
	})

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

// TestNodesStore_MetadataDefaults_Create verifies that optional metadata is
// persisted as an empty JSON object, while non-empty JSON is unchanged.
func TestNodesStore_MetadataDefaults_Create(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping metadata integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	const project = "graph-metadata-create-test"
	t.Cleanup(func() { _ = db.Exec("DELETE FROM knowledge_nodes WHERE project = ?", project).Error })
	ns := NewNodesStore(db)

	for _, tc := range []struct {
		name     string
		metadata []byte
		expected string
		preserve bool
	}{
		{name: "nil", expected: "{}"},
		{name: "empty", metadata: []byte{}, expected: "{}"},
		{name: "whitespace", metadata: []byte(" \t\n"), expected: "{}"},
		{name: "null", metadata: []byte("null"), expected: "{}"},
		{name: "whitespace-null", metadata: []byte(" \tnull\n"), expected: "{}"},
		{name: "object", metadata: []byte(`{"source":"test"}`), expected: `{"source":"test"}`, preserve: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, err := ns.Create(context.Background(), &models.KnowledgeNode{
				NodeType: models.NodeTypeSkill, ExternalRef: "create-" + tc.name, Project: project, Metadata: tc.metadata,
			})
			require.NoError(t, err)
			assert.JSONEq(t, tc.expected, string(node.Metadata))
			if tc.preserve {
				assert.Equal(t, tc.metadata, node.Metadata)
			}

			stored, err := ns.Get(context.Background(), node.ID, true)
			require.NoError(t, err)
			assert.JSONEq(t, tc.expected, string(stored.Metadata))
		})
	}
}

// TestNodesStore_MetadataDefaults_Update verifies that optional metadata is
// normalized on updates as well as creates.
func TestNodesStore_MetadataDefaults_Update(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping metadata integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	const project = "graph-metadata-update-test"
	t.Cleanup(func() { _ = db.Exec("DELETE FROM knowledge_nodes WHERE project = ?", project).Error })
	ns := NewNodesStore(db)

	for _, tc := range []struct {
		name     string
		metadata []byte
		expected string
		preserve bool
	}{
		{name: "nil", expected: "{}"},
		{name: "empty", metadata: []byte{}, expected: "{}"},
		{name: "whitespace", metadata: []byte(" \t\n"), expected: "{}"},
		{name: "null", metadata: []byte("null"), expected: "{}"},
		{name: "whitespace-null", metadata: []byte(" \tnull\n"), expected: "{}"},
		{name: "object", metadata: []byte(`{"source":"test"}`), expected: `{"source":"test"}`, preserve: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, err := ns.Create(context.Background(), &models.KnowledgeNode{
				NodeType: models.NodeTypeSkill, ExternalRef: "update-" + tc.name, Project: project, Metadata: []byte(`{"initial":true}`),
			})
			require.NoError(t, err)

			node.Metadata = tc.metadata
			_, err = ns.Update(context.Background(), node)
			require.NoError(t, err)
			assert.JSONEq(t, tc.expected, string(node.Metadata))
			if tc.preserve {
				assert.Equal(t, tc.metadata, node.Metadata)
			}

			stored, err := ns.Get(context.Background(), node.ID, true)
			require.NoError(t, err)
			assert.JSONEq(t, tc.expected, string(stored.Metadata))
		})
	}
}
