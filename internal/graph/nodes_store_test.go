package graph

import (
	"context"
	"testing"

	"github.com/thebtf/engram/pkg/models"
)

// TestNodesStore_T012_UnitShape verifies the NodesStore API surface matches
// the T012 contract: Create, Get, ListByType, Update, SoftDelete.
// This is a compile-time shape test — no database required.
// Anti-stub: if any method is missing the file will not compile.
func TestNodesStore_T012_UnitShape(t *testing.T) {
	// Nil-pointer receiver compile checks — if any method disappears or changes
	// signature, this block will fail to compile.
	var ns *NodesStore
	ctx := context.Background()

	// These calls will panic at runtime (nil receiver + nil db) so we only
	// verify they exist at compile time via type assertions on the nil pointer.
	_ = ns
	_ = ctx

	// Explicit interface assertions via typed nil — Go checks method set at compile time.
	type creator interface {
		Create(ctx context.Context, node *models.KnowledgeNode) (*models.KnowledgeNode, error)
	}
	type getter interface {
		Get(ctx context.Context, id int64) (*models.KnowledgeNode, error)
	}
	type lister interface {
		ListByType(ctx context.Context, nodeType, project string, includePrivate bool) ([]models.KnowledgeNode, error)
	}
	type updater interface {
		Update(ctx context.Context, node *models.KnowledgeNode) (*models.KnowledgeNode, error)
	}
	type deleter interface {
		SoftDelete(ctx context.Context, id int64) error
	}

	var _ creator = (*NodesStore)(nil)
	var _ getter = (*NodesStore)(nil)
	var _ lister = (*NodesStore)(nil)
	var _ updater = (*NodesStore)(nil)
	var _ deleter = (*NodesStore)(nil)

	t.Log("NodesStore method shape verified")
}

// TestNodesStore_T012_ValidateCreate verifies that Create rejects nodes with
// invalid fields before any database call.
// Pure unit test — no DB required (nil db causes panic only on valid input
// that would reach the DB; invalid input returns early).
func TestNodesStore_T012_ValidateCreate(t *testing.T) {
	ns := &NodesStore{db: nil}
	ctx := context.Background()

	// Nil node must return an error.
	_, err := ns.Create(ctx, nil)
	if err == nil {
		t.Error("Create(nil) should return an error")
	}

	// Node with invalid type must return an error.
	bad := &models.KnowledgeNode{NodeType: "invalid", ExternalRef: "ref", Project: "proj"}
	_, err = ns.Create(ctx, bad)
	if err == nil {
		t.Error("Create with invalid NodeType should return an error")
	}

	// Node with empty external_ref must return an error.
	bad2 := &models.KnowledgeNode{NodeType: models.NodeTypeSkill, ExternalRef: "", Project: "proj"}
	_, err = ns.Create(ctx, bad2)
	if err == nil {
		t.Error("Create with empty ExternalRef should return an error")
	}

	// Node with empty project must return an error.
	bad3 := &models.KnowledgeNode{NodeType: models.NodeTypeSkill, ExternalRef: "ref", Project: ""}
	_, err = ns.Create(ctx, bad3)
	if err == nil {
		t.Error("Create with empty Project should return an error")
	}
}

// TestNodesStore_T012_ValidateListByType verifies that ListByType rejects
// an empty project and an invalid node type before any database call.
func TestNodesStore_T012_ValidateListByType(t *testing.T) {
	ns := &NodesStore{db: nil}
	ctx := context.Background()

	// Empty project must return an error.
	_, err := ns.ListByType(ctx, models.NodeTypeSkill, "", false)
	if err == nil {
		t.Error("ListByType with empty project should return an error")
	}

	// Invalid node type must return an error.
	_, err = ns.ListByType(ctx, "invalid_type", "proj", false)
	if err == nil {
		t.Error("ListByType with invalid node_type should return an error")
	}
}

// TestNodesStore_T012_ValidateUpdate verifies that Update rejects nil and
// zero-ID nodes before any database call.
func TestNodesStore_T012_ValidateUpdate(t *testing.T) {
	ns := &NodesStore{db: nil}
	ctx := context.Background()

	_, err := ns.Update(ctx, nil)
	if err == nil {
		t.Error("Update(nil) should return an error")
	}

	_, err = ns.Update(ctx, &models.KnowledgeNode{ID: 0})
	if err == nil {
		t.Error("Update with ID=0 should return an error")
	}
}
