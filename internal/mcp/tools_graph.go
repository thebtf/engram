package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
)

// nodesLister is the minimal NodesStore interface needed by filterEdgesByNodeType.
type nodesLister interface {
	ListByType(ctx context.Context, nodeType, project string, includePrivate bool) ([]models.KnowledgeNode, error)
}

// nodesStoreAPI is the interface satisfied by *graph.NodesStore that covers
// all uses of Server.nodesStore within the mcp package. Defined as an interface
// so tests can inject a fake without requiring a real database.
type nodesStoreAPI interface {
	Create(ctx context.Context, node *models.KnowledgeNode) (*models.KnowledgeNode, error)
	ListByType(ctx context.Context, nodeType, project string, includePrivate bool) ([]models.KnowledgeNode, error)
	Get(ctx context.Context, id int64, includePrivate bool) (*models.KnowledgeNode, error)
}

// graphArgs is the unified argument struct for all graph tool actions.
// Fields added for Milestone F TG2 (T014):
//   - SourceType / TargetType: discriminator for add_edge (default 'memory')
//   - NodeID / NodeType: used by add_node and get_edges node_type filter
//
// Backward compat (v6.2.x): callers omitting SourceType/TargetType get the
// 'memory' default, preserving all prior add_edge behaviour unchanged.
type graphArgs struct {
	Action    string   `json:"action"`
	SourceID  int64    `json:"source_id"`
	TargetID  int64    `json:"target_id"`
	MemoryID  int64    `json:"memory_id"`
	EdgeID    int64    `json:"edge_id"`
	EdgeType  string   `json:"edge_type"`
	Weight    float64  `json:"weight"`
	Reasoning string   `json:"reasoning"`
	Direction string   `json:"direction"`
	Depth     int      `json:"depth"`
	EdgeTypes []string `json:"edge_types"`
	MaxDepth  int      `json:"max_depth"`

	// T014: discriminator params for add_edge (Milestone F TG2).
	// Default 'memory' is applied in graphAddEdge when empty.
	SourceType string `json:"source_type,omitempty"`
	TargetType string `json:"target_type,omitempty"`

	// T014: node fields for add_node action and get_edges node_type filter.
	NodeID       int64  `json:"node_id,omitempty"`
	NodeType     string `json:"node_type,omitempty"`
	ExternalRef  string `json:"external_ref,omitempty"`
	Project      string `json:"project,omitempty"`
	PrivacyScope string `json:"privacy_scope,omitempty"`
	NodeSourceID int64  `json:"node_source_id,omitempty"` // FK for source endpoint when source_type='node'
	NodeTargetID int64  `json:"node_target_id,omitempty"` // FK for target endpoint when target_type='node'
}

func (s *Server) handleGraph(ctx context.Context, args json.RawMessage) (string, error) {
	if s.graphStore == nil {
		return "", fmt.Errorf("graph store not available")
	}

	var a graphArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parse graph args: %w", err)
	}

	switch a.Action {
	case "add_edge":
		return s.graphAddEdge(ctx, a)
	case "remove_edge":
		return s.graphRemoveEdge(ctx, a)
	case "get_edges":
		return s.graphGetEdges(ctx, a)
	case "traverse":
		return s.graphTraverse(ctx, a)
	case "find_path":
		return s.graphFindPath(ctx, a)
	case "synonyms":
		return s.graphSynonyms(ctx, a)
	case "add_node":
		// T014: add_node is a Milestone F TG2 action gated by vnextFEnabled().
		// When flag is OFF, return a clear error rather than routing to the handler
		// so existing callers never accidentally create nodes in flag-OFF deploys.
		if !vnextFEnabled() {
			return "", fmt.Errorf("add_node requires ENGRAM_VNEXT_F_ENABLED=true")
		}
		return s.graphAddNode(ctx, a)
	default:
		return "", fmt.Errorf("unknown graph action: %s", a.Action)
	}
}

func (s *Server) graphAddEdge(ctx context.Context, a graphArgs) (string, error) {
	// Resolve discriminators — default to 'memory' for v6.2.x backward compat.
	srcType := a.SourceType
	if srcType == "" {
		srcType = "memory"
	}
	tgtType := a.TargetType
	if tgtType == "" {
		tgtType = "memory"
	}

	// T014: validate discriminator values.
	if srcType != "memory" && srcType != "node" {
		return "", fmt.Errorf("invalid_node_type: source_type must be 'memory' or 'node', got %q", srcType)
	}
	if tgtType != "memory" && tgtType != "node" {
		return "", fmt.Errorf("invalid_node_type: target_type must be 'memory' or 'node', got %q", tgtType)
	}

	// Validate source endpoint.
	if srcType == "memory" {
		if a.SourceID == 0 {
			return "", fmt.Errorf("source_id required when source_type='memory'")
		}
	} else {
		if a.NodeSourceID == 0 {
			return "", fmt.Errorf("node_source_id required when source_type='node'")
		}
	}

	// Validate target endpoint.
	if tgtType == "memory" {
		if a.TargetID == 0 {
			return "", fmt.Errorf("target_id required when target_type='memory'")
		}
	} else {
		if a.NodeTargetID == 0 {
			return "", fmt.Errorf("node_target_id required when target_type='node'")
		}
	}

	if a.EdgeType == "" {
		return "", fmt.Errorf("edge_type required")
	}
	if !graph.ValidEdgeType(a.EdgeType) {
		return "", fmt.Errorf("invalid edge_type: %s", a.EdgeType)
	}
	if a.Weight == 0 {
		a.Weight = 1.0
	}

	// Build nullable FK pointers — only set when the endpoint is memory-typed.
	var sourceID *int64
	var targetID *int64
	if srcType == "memory" {
		id := a.SourceID
		sourceID = &id
	}
	if tgtType == "memory" {
		id := a.TargetID
		targetID = &id
	}
	var nodeSourceID *int64
	var nodeTargetID *int64
	if srcType == "node" {
		id := a.NodeSourceID
		nodeSourceID = &id
	}
	if tgtType == "node" {
		id := a.NodeTargetID
		nodeTargetID = &id
	}

	edge := &graph.Edge{
		SourceID:     sourceID,
		TargetID:     targetID,
		EdgeType:     a.EdgeType,
		Weight:       a.Weight,
		Reasoning:    a.Reasoning,
		SourceType:   srcType,
		TargetType:   tgtType,
		NodeSourceID: nodeSourceID,
		NodeTargetID: nodeTargetID,
	}
	created, err := s.graphStore.Create(ctx, edge)
	if err != nil {
		return "", err
	}
	// Dereference nullable source_id/target_id for the JSON response.
	// nil means node-typed endpoint (no memory ID) — emit 0 as sentinel.
	var srcIDVal, tgtIDVal int64
	if created.SourceID != nil {
		srcIDVal = *created.SourceID
	}
	if created.TargetID != nil {
		tgtIDVal = *created.TargetID
	}
	return marshalJSON(map[string]any{
		"edge_id":     created.ID,
		"source_id":   srcIDVal,
		"target_id":   tgtIDVal,
		"source_type": created.SourceType,
		"target_type": created.TargetType,
		"edge_type":   created.EdgeType,
		"message":     "edge created",
	})
}

// graphAddNode implements the add_node action (T014).
// Gated by vnextFEnabled() in the dispatch switch — this function is only
// called when the flag is ON.
//
// Anti-stub: removing NodeType validation causes
// TestGraphTool_T014_InvalidNodeTypeRejects to fail.
func (s *Server) graphAddNode(ctx context.Context, a graphArgs) (string, error) {
	if s.nodesStore == nil {
		return "", fmt.Errorf("nodes store not available")
	}
	if a.NodeType == "" {
		return "", fmt.Errorf("node_type required")
	}
	if !models.ValidNodeType(a.NodeType) {
		return "", fmt.Errorf("invalid_node_type: %q is not a valid node type", a.NodeType)
	}
	if a.ExternalRef == "" {
		return "", fmt.Errorf("external_ref required")
	}
	if a.Project == "" {
		return "", fmt.Errorf("project required")
	}
	ps := a.PrivacyScope
	if ps == "" {
		ps = "project"
	}
	node := &models.KnowledgeNode{
		NodeType:     a.NodeType,
		ExternalRef:  a.ExternalRef,
		Project:      a.Project,
		PrivacyScope: ps,
	}
	created, err := s.nodesStore.Create(ctx, node)
	if err != nil {
		return "", fmt.Errorf("create node: %w", err)
	}
	return marshalJSON(map[string]any{
		"node_id":      created.ID,
		"node_type":    created.NodeType,
		"external_ref": created.ExternalRef,
		"project":      created.Project,
		"message":      "node created",
	})
}

func (s *Server) graphRemoveEdge(ctx context.Context, a graphArgs) (string, error) {
	if a.EdgeID == 0 {
		return "", fmt.Errorf("edge_id required")
	}
	if err := s.graphStore.SoftDelete(ctx, a.EdgeID); err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"edge_id": a.EdgeID,
		"message": "edge removed",
	})
}

func (s *Server) graphGetEdges(ctx context.Context, a graphArgs) (string, error) {
	if a.MemoryID == 0 && a.NodeID == 0 {
		return "", fmt.Errorf("memory_id or node_id required")
	}
	dir := graph.Both
	switch a.Direction {
	case "outgoing":
		dir = graph.Outgoing
	case "incoming":
		dir = graph.Incoming
	}

	// T014: node_type filter — validate if provided.
	if a.NodeType != "" && !models.ValidNodeType(a.NodeType) {
		return "", fmt.Errorf("invalid_node_type: %q is not a valid node type", a.NodeType)
	}

	var edges []graph.Edge
	var err error
	if a.MemoryID != 0 {
		edges, err = s.graphStore.ListByMemory(ctx, a.MemoryID, dir, a.EdgeType)
	} else {
		// node_id-based edge lookup: list edges where the node is source or target.
		edges, err = s.graphStore.ListByNode(ctx, a.NodeID, dir, a.EdgeType)
	}
	if err != nil {
		return "", err
	}

	// T014: apply node_type filter post-query if specified.
	// Batch-fetch node IDs from knowledge_nodes filtered by node_type; include
	// only edges whose node endpoint (node_source_id or node_target_id) is in
	// the matching set. Cross-table lookup is required because node_type is
	// stored in knowledge_nodes, not in knowledge_edges.
	filtered := edges
	if a.NodeType != "" && vnextFEnabled() {
		filtered = filterEdgesByNodeType(ctx, edges, a.NodeType, s.nodesStore)
	}

	return marshalJSON(map[string]any{
		"memory_id": a.MemoryID,
		"node_id":   a.NodeID,
		"direction": a.Direction,
		"node_type": a.NodeType,
		"count":     len(filtered),
		"edges":     filtered,
	})
}

// filterEdgesByNodeType returns only edges whose node endpoint (node_source_id
// or node_target_id) belongs to a knowledge_node with the given nodeType.
//
// Algorithm:
//  1. Collect the unique set of node IDs referenced by edges.
//  2. Fetch each node via nodesStore (using Get with includePrivate=false).
//  3. Build the set of node IDs whose node_type matches nodeType.
//  4. Retain edges that have at least one endpoint in the matching set.
//
// Edges with no node endpoints (both NodeSourceID and NodeTargetID nil) are
// excluded — they are memory-only edges that cannot match a node_type filter.
// When nodesStore is nil, the filter is a no-op (returns edges unfiltered)
// with no error: the schema validation upstream already required vnextFEnabled.
//
// Anti-stub: this replaces the prior return-unfiltered implementation.
// TestGraphTool_T014_NodeTypeFilterOffline asserts correct filtering behaviour.
func filterEdgesByNodeType(ctx context.Context, edges []graph.Edge, nodeType string, ns nodesLister) []graph.Edge {
	if ns == nil {
		return edges
	}

	// Step 1: collect unique node IDs referenced by the edge set.
	nodeIDs := map[int64]struct{}{}
	for _, e := range edges {
		if e.NodeSourceID != nil {
			nodeIDs[*e.NodeSourceID] = struct{}{}
		}
		if e.NodeTargetID != nil {
			nodeIDs[*e.NodeTargetID] = struct{}{}
		}
	}
	if len(nodeIDs) == 0 {
		// No node endpoints — no edge can match a node_type filter.
		return nil
	}

	// Step 2: fetch each node and record which IDs match the requested type.
	// We use the graph.NodesStore directly via the nodesLister interface;
	// Get is called per unique ID (set is bounded by the edge slice size).
	type getterWithGet interface {
		Get(ctx context.Context, id int64, includePrivate bool) (*models.KnowledgeNode, error)
	}
	getter, hasGet := ns.(getterWithGet)

	matchingIDs := map[int64]struct{}{}
	if hasGet {
		for id := range nodeIDs {
			node, err := getter.Get(ctx, id, false)
			if err != nil {
				// Node not found or private — skip; the edge will be excluded.
				continue
			}
			if node.NodeType == nodeType {
				matchingIDs[id] = struct{}{}
			}
		}
	}

	// Step 3: filter edges by membership in matchingIDs.
	var out []graph.Edge
	for _, e := range edges {
		if e.NodeSourceID != nil {
			if _, ok := matchingIDs[*e.NodeSourceID]; ok {
				out = append(out, e)
				continue
			}
		}
		if e.NodeTargetID != nil {
			if _, ok := matchingIDs[*e.NodeTargetID]; ok {
				out = append(out, e)
			}
		}
	}
	return out
}

func (s *Server) graphTraverse(ctx context.Context, a graphArgs) (string, error) {
	if a.MemoryID == 0 {
		return "", fmt.Errorf("memory_id required")
	}
	depth := a.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > graph.MaxTraverseDepth {
		return "", fmt.Errorf("max depth is %d", graph.MaxTraverseDepth)
	}
	results, err := s.graphStore.Traverse(ctx, a.MemoryID, depth, a.EdgeTypes)
	if err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"memory_id": a.MemoryID,
		"depth":     depth,
		"count":     len(results),
		"results":   results,
	})
}

func (s *Server) graphFindPath(ctx context.Context, a graphArgs) (string, error) {
	if a.SourceID == 0 || a.TargetID == 0 {
		return "", fmt.Errorf("source_id and target_id required")
	}
	maxDepth := a.MaxDepth
	if maxDepth <= 0 {
		maxDepth = graph.MaxTraverseDepth
	}
	path, err := s.graphStore.FindPath(ctx, a.SourceID, a.TargetID, maxDepth)
	if err != nil {
		return "", err
	}
	found := path != nil
	return marshalJSON(map[string]any{
		"source_id": a.SourceID,
		"target_id": a.TargetID,
		"found":     found,
		"hops":      len(path),
		"path":      path,
	})
}

func (s *Server) graphSynonyms(ctx context.Context, a graphArgs) (string, error) {
	if a.MemoryID == 0 {
		return "", fmt.Errorf("memory_id required")
	}
	edges, err := s.graphStore.FindSynonyms(ctx, a.MemoryID)
	if err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"memory_id": a.MemoryID,
		"count":     len(edges),
		"synonyms":  edges,
	})
}
