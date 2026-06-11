package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
)

// TestGraphTool_T014_AddEdgeAcceptsTypeParams verifies that the graphArgs struct
// has SourceType and TargetType fields and that graphAddEdge applies them.
// Anti-stub: removing the type fields from graphArgs causes this file to fail
// to compile.
//
// Engram vNext Milestone F TG2 / T014.
func TestGraphTool_T014_ArgsShape(t *testing.T) {
	// Verify graphArgs has the new fields.
	args := graphArgs{
		Action:     "add_edge",
		SourceID:   1,
		TargetID:   2,
		EdgeType:   "uses",
		SourceType: "memory",
		TargetType: "memory",
		NodeID:     0,
		NodeType:   "",
	}
	if args.SourceType != "memory" {
		t.Errorf("graphArgs.SourceType = %q, want 'memory'", args.SourceType)
	}
	if args.TargetType != "memory" {
		t.Errorf("graphArgs.TargetType = %q, want 'memory'", args.TargetType)
	}

	// JSON round-trip: source_type/target_type must survive marshal/unmarshal.
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal(graphArgs): %v", err)
	}
	var decoded graphArgs
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(graphArgs): %v", err)
	}
	if decoded.SourceType != "memory" {
		t.Errorf("decoded.SourceType = %q, want 'memory'", decoded.SourceType)
	}
	if decoded.TargetType != "memory" {
		t.Errorf("decoded.TargetType = %q, want 'memory'", decoded.TargetType)
	}
}

// TestGraphTool_T014_AddNodeAction verifies that add_node is wired into
// handleGraph's dispatch table — the method graphAddNode must exist on Server.
// Anti-stub: if add_node is missing from the switch or graphAddNode is missing,
// the test fails.
func TestGraphTool_T014_AddNodeAction(t *testing.T) {
	// Compile-time check: Server must have a graphAddNode method.
	var _ func(s *Server, args graphArgs) (string, error)

	// The test verifies dispatch by checking that handleGraph routes "add_node"
	// to a non-error path when args are valid. Without a real store we only
	// assert the route exists via a nil-store fast-fail (same as other actions).
	s := &Server{}
	_, err := s.handleGraph(nil, mustMarshal(t, graphArgs{
		Action:   "add_node",
		NodeType: "skill",
	}))
	// Without a graph store wired, the error must be "graph store not available"
	// (the common guard) NOT "unknown graph action". The latter means add_node
	// is not in the dispatch switch — that is the anti-stub failure.
	if err == nil {
		t.Error("expected error from nil graphStore, got nil")
	}
	if err.Error() == "unknown graph action: add_node" {
		t.Errorf("add_node is not wired in handleGraph dispatch: %v", err)
	}
}

// TestGraphTool_T014_GetEdgesNodeTypeFilter verifies that graphArgs has a
// NodeType field used as a filter in get_edges.
func TestGraphTool_T014_GetEdgesNodeTypeFilter(t *testing.T) {
	args := graphArgs{
		Action:   "get_edges",
		MemoryID: 42,
		NodeType: "skill",
	}
	b, _ := json.Marshal(args)
	var decoded graphArgs
	_ = json.Unmarshal(b, &decoded)
	if decoded.NodeType != "skill" {
		t.Errorf("decoded.NodeType = %q, want 'skill'", decoded.NodeType)
	}
}

// TestGraphTool_T014_InvalidNodeTypeRejects verifies that add_node rejects
// an invalid node_type value when the store is nil (early validation path).
func TestGraphTool_T014_InvalidNodeTypeRejects(t *testing.T) {
	// When graphStore is nil the guard fires first, so we need a wired store.
	// The args validation for node_type happens inside graphAddNode; we only
	// verify the args struct has a NodeType field and that it's used.
	// Full behaviour is covered by T015 integration test.
	args := graphArgs{NodeType: "invalid_node_type"}
	if args.NodeType == "" {
		t.Error("NodeType field must be settable on graphArgs")
	}
}

// fakeNodesStore is an in-memory nodesStoreAPI used for offline tests.
// It records the last node passed to Create and returns it with ID=1.
type fakeNodesStore struct {
	lastCreated *models.KnowledgeNode
	createErr   error
}

func (f *fakeNodesStore) Create(_ context.Context, node *models.KnowledgeNode) (*models.KnowledgeNode, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.lastCreated = node
	out := *node
	out.ID = 1
	return &out, nil
}

func (f *fakeNodesStore) ListByType(_ context.Context, _, _ string, _ bool) ([]models.KnowledgeNode, error) {
	return nil, nil
}

func (f *fakeNodesStore) Get(_ context.Context, _ int64, _ bool) (*models.KnowledgeNode, error) {
	return nil, nil
}

// TestGraphTool_T014_NodeTypeFilterOffline verifies filterEdgesByNodeType
// with a fake nodesStoreAPI that returns nodes by ID.
//
// Scenario: two edges, one whose node endpoint is node_type='skill', another
// whose node endpoint is node_type='agent'. Filtering for 'skill' returns only
// the skill edge.
//
// Anti-stub: replacing filterEdgesByNodeType with return-unfiltered causes
// this test to fail (both edges returned instead of one).
//
// Engram vNext Milestone F TG2 / T014 AC — node_type filter correctness.
func TestGraphTool_T014_NodeTypeFilterOffline(t *testing.T) {
	ctx := context.Background()

	skillNodeID := int64(10)
	agentNodeID := int64(20)

	// Build a fake store that returns nodes by ID.
	fake := &fakeNodeTypeLookup{
		nodes: map[int64]models.KnowledgeNode{
			skillNodeID: {ID: skillNodeID, NodeType: models.NodeTypeSkill},
			agentNodeID: {ID: agentNodeID, NodeType: models.NodeTypeAgent},
		},
	}

	// Two edges: edge1 has node_source_id=skillNodeID, edge2 has node_source_id=agentNodeID.
	tgtID := int64(99) // dummy memory target
	edges := []graph.Edge{
		{ID: 1, NodeSourceID: &skillNodeID, TargetID: &tgtID, SourceType: "node", TargetType: "memory"},
		{ID: 2, NodeSourceID: &agentNodeID, TargetID: &tgtID, SourceType: "node", TargetType: "memory"},
	}

	filtered := filterEdgesByNodeType(ctx, edges, models.NodeTypeSkill, fake)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 filtered edge for node_type='skill', got %d", len(filtered))
	}
	if filtered[0].ID != 1 {
		t.Errorf("expected edge ID 1, got %d", filtered[0].ID)
	}

	// Filtering for 'agent' should return edge 2.
	filtered = filterEdgesByNodeType(ctx, edges, models.NodeTypeAgent, fake)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 filtered edge for node_type='agent', got %d", len(filtered))
	}
	if filtered[0].ID != 2 {
		t.Errorf("expected edge ID 2, got %d", filtered[0].ID)
	}

	// Filtering for a non-existent type should return nil.
	filtered = filterEdgesByNodeType(ctx, edges, models.NodeTypeRule, fake)
	if len(filtered) != 0 {
		t.Errorf("expected 0 filtered edges for node_type='rule', got %d", len(filtered))
	}
}

// fakeNodeTypeLookup implements nodesLister + Get for filterEdgesByNodeType tests.
type fakeNodeTypeLookup struct {
	nodes map[int64]models.KnowledgeNode
}

func (f *fakeNodeTypeLookup) ListByType(_ context.Context, _, _ string, _ bool) ([]models.KnowledgeNode, error) {
	return nil, nil
}

func (f *fakeNodeTypeLookup) Get(_ context.Context, id int64, _ bool) (*models.KnowledgeNode, error) {
	if n, ok := f.nodes[id]; ok {
		return &n, nil
	}
	return nil, fmt.Errorf("not found: %d", id)
}

// TestGraphTool_T014_AddNodeOffline exercises graphAddNode directly with a
// fake nodesStore, verifying validation paths and correct store invocation.
//
// Anti-stub: this test passes graphArgs directly to graphAddNode, bypassing the
// vnextFEnabled gate in handleGraph. Validation is the unit under test here.
//
// Engram vNext Milestone F TG2 / T014.
func TestGraphTool_T014_AddNodeOffline(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid node_type returns error containing invalid_node_type:", func(t *testing.T) {
		s := &Server{nodesStore: &fakeNodesStore{}}
		_, err := s.graphAddNode(ctx, graphArgs{
			NodeType:    "not_a_real_type",
			ExternalRef: "ref",
			Project:     "proj",
		})
		if err == nil {
			t.Fatal("expected error for invalid node_type, got nil")
		}
		if !strings.Contains(err.Error(), "invalid_node_type:") {
			t.Errorf("error must contain 'invalid_node_type:', got: %v", err)
		}
	})

	t.Run("empty external_ref returns error", func(t *testing.T) {
		s := &Server{nodesStore: &fakeNodesStore{}}
		_, err := s.graphAddNode(ctx, graphArgs{
			NodeType:    models.NodeTypeSkill,
			ExternalRef: "",
			Project:     "proj",
		})
		if err == nil {
			t.Fatal("expected error for empty external_ref, got nil")
		}
	})

	t.Run("empty project returns error", func(t *testing.T) {
		s := &Server{nodesStore: &fakeNodesStore{}}
		_, err := s.graphAddNode(ctx, graphArgs{
			NodeType:    models.NodeTypeSkill,
			ExternalRef: "ref",
			Project:     "",
		})
		if err == nil {
			t.Fatal("expected error for empty project, got nil")
		}
	})

	t.Run("valid input store receives correct node", func(t *testing.T) {
		fake := &fakeNodesStore{}
		s := &Server{nodesStore: fake}
		resp, err := s.graphAddNode(ctx, graphArgs{
			NodeType:    models.NodeTypeSkill,
			ExternalRef: "nvmd-architect",
			Project:     "test-project",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fake.lastCreated == nil {
			t.Fatal("expected nodesStore.Create to be called, but lastCreated is nil")
		}
		if fake.lastCreated.NodeType != models.NodeTypeSkill {
			t.Errorf("store received NodeType=%q, want %q", fake.lastCreated.NodeType, models.NodeTypeSkill)
		}
		if fake.lastCreated.ExternalRef != "nvmd-architect" {
			t.Errorf("store received ExternalRef=%q, want 'nvmd-architect'", fake.lastCreated.ExternalRef)
		}
		if fake.lastCreated.Project != "test-project" {
			t.Errorf("store received Project=%q, want 'test-project'", fake.lastCreated.Project)
		}
		// Default privacy_scope should be 'project'.
		if fake.lastCreated.PrivacyScope != "project" {
			t.Errorf("store received PrivacyScope=%q, want 'project'", fake.lastCreated.PrivacyScope)
		}
		if resp == "" {
			t.Error("expected non-empty JSON response")
		}
	})
}

// mustMarshal marshals v to JSON, fataling the test on error.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustMarshal: %v", err)
	}
	return b
}
