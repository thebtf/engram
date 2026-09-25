package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"

	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	_ "modernc.org/sqlite"
)

func TestGraphToolWriterActionsAreAbsentDespiteLegacyFlag(t *testing.T) {
	t.Setenv("ENGRAM_GRAPH_ENABLED", "true")
	server := &Server{graphStore: &graph.Store{}}

	for _, action := range []string{"add_edge", "remove_edge", "add_node"} {
		t.Run(action, func(t *testing.T) {
			_, err := server.handleGraph(context.Background(), mustMarshal(t, graphArgs{Action: action}))
			if err == nil || err.Error() != fmt.Sprintf("unknown graph action: %s", action) {
				t.Fatalf("writer action %q error=%v", action, err)
			}
		})
	}
}

func TestGraphToolSchemaContainsOnlyReaderActionsAndFields(t *testing.T) {
	t.Setenv("ENGRAM_GRAPH_ENABLED", "true")
	server := NewServer(ServerOptions{Version: "test"})
	server.SetGraphStore(&graph.Store{})
	tools := server.ListTools()

	var graphTool *Tool
	for i := range tools {
		if tools[i].Name == "graph" {
			graphTool = &tools[i]
			break
		}
	}
	if graphTool == nil {
		t.Fatal("graph tool absent")
	}
	properties, ok := graphTool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("graph properties=%T", graphTool.InputSchema["properties"])
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("graph action=%T", properties["action"])
	}
	actions, ok := action["enum"].([]string)
	if !ok || len(actions) != 4 {
		t.Fatalf("graph action enum=%#v", action["enum"])
	}
	for _, name := range actions {
		if !map[string]bool{"get_edges": true, "traverse": true, "find_path": true, "synonyms": true}[name] {
			t.Fatalf("writer action remains advertised: %q", name)
		}
	}
	for _, field := range []string{"edge_id", "weight", "reasoning", "source_type", "target_type", "node_source_id", "node_target_id", "external_ref", "project", "privacy_scope"} {
		if _, exists := properties[field]; exists {
			t.Fatalf("writer-only graph field remains advertised: %q", field)
		}
	}
}

func TestGraphToolRetainsNodeTypeReaderFilter(t *testing.T) {
	ctx := context.Background()
	skillNodeID, agentNodeID, targetID := int64(10), int64(20), int64(99)
	filtered := filterEdgesByNodeType(ctx, []graph.Edge{
		{ID: 1, NodeSourceID: &skillNodeID, TargetID: &targetID, SourceType: "node", TargetType: "memory"},
		{ID: 2, NodeSourceID: &agentNodeID, TargetID: &targetID, SourceType: "node", TargetType: "memory"},
	}, models.NodeTypeSkill, fakeNodeTypeLookup{nodes: map[int64]models.KnowledgeNode{
		skillNodeID: {ID: skillNodeID, NodeType: models.NodeTypeSkill},
		agentNodeID: {ID: agentNodeID, NodeType: models.NodeTypeAgent},
	}})
	if len(filtered) != 1 || filtered[0].ID != 1 {
		t.Fatalf("retained node-type reader filter=%#v", filtered)
	}
}

func TestGraphToolNodeTypeFilterWithFlagOffAndWiredStore(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "false")
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(1)
	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gormlib.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE knowledge_edges (id INTEGER PRIMARY KEY, source_id INTEGER, target_id INTEGER, node_source_id INTEGER, node_target_id INTEGER, source_type TEXT, target_type TEXT, edge_type TEXT, weight REAL, reasoning TEXT, source_session_id TEXT, valid_from DATETIME, valid_until DATETIME, created_at DATETIME, superseded_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO knowledge_edges (id, node_source_id, target_id, source_type, target_type, edge_type, weight, created_at) VALUES (1, 10, 99, 'node', 'memory', 'related_to', 1, CURRENT_TIMESTAMP), (2, 20, 99, 'node', 'memory', 'related_to', 1, CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE memories (id INTEGER PRIMARY KEY, project TEXT, content TEXT, privacy_scope TEXT, source_workstation_id TEXT, deleted_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO memories (id, project, content, privacy_scope, source_workstation_id) VALUES (99, 'graph', 'visible', 'project', '')`).Error; err != nil {
		t.Fatal(err)
	}
	server := &Server{graphStore: graph.NewStore(db, nil), memoryStore: gormdb.NewMemoryStore(&gormdb.Store{DB: db}), nodesStore: fakeNodeTypeLookup{nodes: map[int64]models.KnowledgeNode{
		10: {ID: 10, NodeType: models.NodeTypeSkill},
		20: {ID: 20, NodeType: models.NodeTypeAgent},
	}}}
	result, err := server.handleGraph(context.Background(), mustMarshal(t, graphArgs{Action: "get_edges", MemoryID: 99, NodeType: models.NodeTypeSkill}))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Count int          `json:"count"`
		Edges []graph.Edge `json:"edges"`
	}
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		t.Fatal(err)
	}
	if response.Count != 1 || len(response.Edges) != 1 || response.Edges[0].ID != 1 {
		t.Fatalf("node_type filtered edges: %s", result)
	}
}

func TestGraphToolGetEdgesHidesPrivateAndInaccessibleEndpoints(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(1)
	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gormlib.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE memories (id INTEGER PRIMARY KEY, project TEXT, content TEXT, privacy_scope TEXT, source_workstation_id TEXT, deleted_at DATETIME)`,
		`INSERT INTO memories (id, project, content, privacy_scope, source_workstation_id) VALUES (99, 'graph', 'visible', 'project', ''), (100, 'graph', 'hidden', 'private', 'other')`,
		`CREATE TABLE knowledge_edges (id INTEGER PRIMARY KEY, source_id INTEGER, target_id INTEGER, node_source_id INTEGER, node_target_id INTEGER, source_type TEXT, target_type TEXT, edge_type TEXT, weight REAL, reasoning TEXT, source_session_id TEXT, valid_from DATETIME, valid_until DATETIME, created_at DATETIME, superseded_at DATETIME)`,
		`INSERT INTO knowledge_edges (id, source_id, target_id, node_source_id, node_target_id, source_type, target_type, edge_type, weight, reasoning, created_at) VALUES
			(1, 99, 99, NULL, NULL, 'memory', 'memory', 'related_to', 1, 'public reasoning', CURRENT_TIMESTAMP),
			(2, 99, 100, NULL, NULL, 'memory', 'memory', 'related_to', 1, 'private memory reasoning', CURRENT_TIMESTAMP),
			(3, 99, NULL, NULL, 20, 'memory', 'node', 'related_to', 1, 'private node reasoning', CURRENT_TIMESTAMP),
			(4, 99, NULL, NULL, 10, 'memory', 'node', 'related_to', 1, 'public node reasoning', CURRENT_TIMESTAMP),
			(5, NULL, NULL, 10, 20, 'node', 'node', 'related_to', 1, 'private target reasoning', CURRENT_TIMESTAMP)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{graphStore: graph.NewStore(db, nil), memoryStore: gormdb.NewMemoryStore(&gormdb.Store{DB: db}), nodesStore: fakeNodeTypeLookup{nodes: map[int64]models.KnowledgeNode{
		10: {ID: 10, NodeType: models.NodeTypeSkill, PrivacyScope: "project"},
		20: {ID: 20, NodeType: models.NodeTypeSkill, PrivacyScope: "private"},
	}}}
	for _, tc := range []struct {
		args graphArgs
		ids  []int64
	}{
		{graphArgs{Action: "get_edges", MemoryID: 99}, []int64{1, 4}},
		{graphArgs{Action: "get_edges", NodeID: 10}, []int64{4}},
		{graphArgs{Action: "get_edges", NodeID: 20}, nil},
		{graphArgs{Action: "get_edges", MemoryID: 100}, nil},
		{graphArgs{Action: "get_edges", MemoryID: 99, NodeType: models.NodeTypeSkill}, []int64{4}},
	} {
		result, err := server.handleGraph(context.Background(), mustMarshal(t, tc.args))
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Count int          `json:"count"`
			Edges []graph.Edge `json:"edges"`
		}
		if err := json.Unmarshal([]byte(result), &response); err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, edge := range response.Edges {
			ids = append(ids, edge.ID)
		}
		slices.Sort(ids)
		if response.Count != len(tc.ids) || !slices.Equal(ids, tc.ids) {
			t.Fatalf("get_edges %+v: returned ids %v, want %v; response=%s", tc.args, ids, tc.ids, result)
		}
	}
}

func TestGraphToolNodeTypeFilterRejectsMissingStore(t *testing.T) {
	for _, flag := range []string{"false", "true"} {
		t.Run(flag, func(t *testing.T) {
			t.Setenv("ENGRAM_VNEXT_F_ENABLED", flag)
			server := &Server{graphStore: &graph.Store{}}
			_, err := server.handleGraph(context.Background(), mustMarshal(t, graphArgs{Action: "get_edges", NodeID: 1, NodeType: models.NodeTypeSkill}))
			if err == nil || err.Error() != "node_type filter unavailable: nodes store not configured" {
				t.Fatalf("node_type unavailable error=%v", err)
			}
		})
	}
}

type fakeNodeTypeLookup struct {
	nodes map[int64]models.KnowledgeNode
}

func (f fakeNodeTypeLookup) ListByType(_ context.Context, _, _ string, _ bool) ([]models.KnowledgeNode, error) {
	return nil, nil
}

func (f fakeNodeTypeLookup) Get(_ context.Context, id int64, includePrivate bool) (*models.KnowledgeNode, error) {
	node, ok := f.nodes[id]
	if !ok || (!includePrivate && node.PrivacyScope == "private") {
		return nil, fmt.Errorf("not found: %d", id)
	}
	return &node, nil
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal graph args: %v", err)
	}
	return encoded
}
