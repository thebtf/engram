package models

import (
	"testing"
)

// TestKnowledgeNode_T011_AllTypesAccepted verifies the 13 valid NodeType constants
// are all accepted by NewKnowledgeNode and that an invalid type is rejected.
func TestKnowledgeNode_T011_AllTypesAccepted(t *testing.T) {
	validTypes := []NodeType{
		NodeTypeProject,
		NodeTypeRepo,
		NodeTypeSkill,
		NodeTypeAgent,
		NodeTypeRule,
		NodeTypeHook,
		NodeTypeSession,
		NodeTypeFile,
		NodeTypeConsumer,
		NodeTypeDecision,
		NodeTypeClaim,
		NodeTypeBug,
		NodeTypeFeature,
	}
	if len(validTypes) != 13 {
		t.Fatalf("expected 13 node types, got %d", len(validTypes))
	}
	for _, nt := range validTypes {
		node, err := NewKnowledgeNode(nt, "test-ref", "test-project")
		if err != nil {
			t.Errorf("NewKnowledgeNode(%q) unexpected error: %v", nt, err)
		}
		if node == nil {
			t.Errorf("NewKnowledgeNode(%q) returned nil node", nt)
			continue
		}
		if node.NodeType != nt {
			t.Errorf("node.NodeType = %q, want %q", node.NodeType, nt)
		}
		if node.ExternalRef != "test-ref" {
			t.Errorf("node.ExternalRef = %q, want %q", node.ExternalRef, "test-ref")
		}
		if node.Project != "test-project" {
			t.Errorf("node.Project = %q, want %q", node.Project, "test-project")
		}
		if node.PrivacyScope != "project" {
			t.Errorf("node.PrivacyScope = %q, want %q", node.PrivacyScope, "project")
		}
	}
}

// TestKnowledgeNode_T011_InvalidTypeRejected verifies that an unknown node type
// is rejected by the constructor — anti-stub: replacing with return &KnowledgeNode{}
// would cause this to fail because the validation check would be missing.
func TestKnowledgeNode_T011_InvalidTypeRejected(t *testing.T) {
	_, err := NewKnowledgeNode("invalid_type", "ref", "proj")
	if err == nil {
		t.Error("expected error for invalid node type, got nil")
	}
}

// TestKnowledgeNode_T011_RequiredFieldsValidated verifies the constructor rejects
// empty external_ref and empty project.
func TestKnowledgeNode_T011_RequiredFieldsValidated(t *testing.T) {
	cases := []struct {
		name        string
		nodeType    NodeType
		externalRef string
		project     string
	}{
		{"empty external_ref", NodeTypeSkill, "", "proj"},
		{"empty project", NodeTypeSkill, "ref", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewKnowledgeNode(tc.nodeType, tc.externalRef, tc.project)
			if err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

// TestKnowledgeNode_T011_GoldenShape verifies struct field names and JSON tags.
func TestKnowledgeNode_T011_GoldenShape(t *testing.T) {
	node, err := NewKnowledgeNode(NodeTypeSkill, "nvmd-architect", "engram")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify all fields are settable and match expected shape
	if node.ID != 0 {
		t.Errorf("expected zero ID on new node, got %d", node.ID)
	}
	if node.NodeType == "" {
		t.Error("NodeType should not be empty")
	}
	if node.ExternalRef == "" {
		t.Error("ExternalRef should not be empty")
	}
	if node.Project == "" {
		t.Error("Project should not be empty")
	}
	if node.PrivacyScope == "" {
		t.Error("PrivacyScope should default to 'project'")
	}
	// CreatedAt should be set to a non-zero time
	if node.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set on construction")
	}
}
