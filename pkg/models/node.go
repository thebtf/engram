package models

import (
	"fmt"
	"time"
)

// NodeType enumerates the 13 typed node kinds supported by the knowledge graph
// (spec §FR-F2, ADR-F-001 Path C). Nodes reside in the knowledge_nodes table
// (migration 126); Memory rows continue to be the only nodes accessible via
// the legacy source_id/target_id FKs on knowledge_edges.
type NodeType = string

// The 13 valid node types per spec §FR-F2.
const (
	NodeTypeProject  NodeType = "project"
	NodeTypeRepo     NodeType = "repo"
	NodeTypeSkill    NodeType = "skill"
	NodeTypeAgent    NodeType = "agent"
	NodeTypeRule     NodeType = "rule"
	NodeTypeHook     NodeType = "hook"
	NodeTypeSession  NodeType = "session"
	NodeTypeFile     NodeType = "file"
	NodeTypeConsumer NodeType = "consumer"
	NodeTypeDecision NodeType = "decision"
	NodeTypeClaim    NodeType = "claim"
	NodeTypeBug      NodeType = "bug"
	NodeTypeFeature  NodeType = "feature"
)

// validNodeTypes is the canonical set of accepted node_type values.
// Must match the CHECK constraint in migration 126_knowledge_nodes_table.
var validNodeTypes = map[NodeType]bool{
	NodeTypeProject:  true,
	NodeTypeRepo:     true,
	NodeTypeSkill:    true,
	NodeTypeAgent:    true,
	NodeTypeRule:     true,
	NodeTypeHook:     true,
	NodeTypeSession:  true,
	NodeTypeFile:     true,
	NodeTypeConsumer: true,
	NodeTypeDecision: true,
	NodeTypeClaim:    true,
	NodeTypeBug:      true,
	NodeTypeFeature:  true,
}

// ValidNodeType returns true iff t is one of the 13 accepted node types.
func ValidNodeType(t NodeType) bool {
	return validNodeTypes[t]
}

// KnowledgeNode is the domain model for a typed non-memory graph node stored in
// the knowledge_nodes table (migration 126). Nodes are referenced by knowledge_edges
// via node_source_id / node_target_id FK columns (migration 127) and resolved
// through the source_type / target_type discriminator.
//
// Unlike Memory rows, KnowledgeNodes do not carry content or lifecycle metadata;
// they represent external entities (skills, files, agents, rules, …) that
// participate in the knowledge graph.
type KnowledgeNode struct {
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
	NodeType     NodeType   `json:"node_type"`
	ExternalRef  string     `json:"external_ref"`  // e.g. skill name, file path, session_id
	Project      string     `json:"project"`       // owning project slug
	PrivacyScope string     `json:"privacy_scope"` // NOT NULL DEFAULT 'project'
	ID           int64      `json:"id"`
	Metadata     []byte     `json:"metadata,omitempty"` // JSONB column, optional
}

// NewKnowledgeNode constructs a KnowledgeNode after validating required fields.
// Returns an error when node_type is unknown, external_ref is empty, or project
// is empty — anti-stub: replacing with return &KnowledgeNode{} skips validation
// and causes fixture tests (TestKnowledgeNode_T011_RequiredFieldsValidated) to fail.
func NewKnowledgeNode(nodeType NodeType, externalRef, project string) (*KnowledgeNode, error) {
	if !ValidNodeType(nodeType) {
		return nil, fmt.Errorf("invalid node_type %q: must be one of project, repo, skill, agent, rule, hook, session, file, consumer, decision, claim, bug, feature", nodeType)
	}
	if externalRef == "" {
		return nil, fmt.Errorf("external_ref is required")
	}
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	now := time.Now().UTC()
	return &KnowledgeNode{
		NodeType:     nodeType,
		ExternalRef:  externalRef,
		Project:      project,
		PrivacyScope: "project",
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}
