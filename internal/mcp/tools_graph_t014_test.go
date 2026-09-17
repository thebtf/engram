package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
)

func TestGraphToolRetiresWriterActionsDespiteLegacyFlags(t *testing.T) {
	t.Setenv("ENGRAM_GRAPH_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	server := &Server{graphStore: &graph.Store{}}

	for _, action := range RetiredGraphWriterActions {
		t.Run(string(action), func(t *testing.T) {
			_, err := server.handleGraph(context.Background(), mustMarshal(t, graphArgs{Action: string(action)}))
			if err == nil || err.Error() != fmt.Sprintf("graph writer action %q has been retired", action) {
				t.Fatalf("writer action %q error=%v", action, err)
			}
		})
	}

	if got := os.Getenv("ENGRAM_GRAPH_ENABLED"); got != "true" {
		t.Fatalf("legacy graph flag changed during test: %q", got)
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

type fakeNodeTypeLookup struct {
	nodes map[int64]models.KnowledgeNode
}

func (f fakeNodeTypeLookup) ListByType(_ context.Context, _, _ string, _ bool) ([]models.KnowledgeNode, error) {
	return nil, nil
}

func (f fakeNodeTypeLookup) Get(_ context.Context, id int64, _ bool) (*models.KnowledgeNode, error) {
	node, ok := f.nodes[id]
	if !ok {
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
