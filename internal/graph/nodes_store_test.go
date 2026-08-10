package graph

import (
	"context"
	"strings"
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
		Get(ctx context.Context, id int64, includePrivate bool) (*models.KnowledgeNode, error)
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

// TestNodesStore_MetadataValidationRejectsInvalidValues verifies that malformed
// and non-object metadata returns validation errors before either mutation reaches the DB.
func TestNodesStore_MetadataValidationRejectsInvalidValues(t *testing.T) {
	ns := &NodesStore{db: nil}
	ctx := context.Background()

	operations := []struct {
		name string
		call func([]byte) error
	}{
		{
			name: "Create",
			call: func(metadata []byte) error {
				_, err := ns.Create(ctx, &models.KnowledgeNode{
					NodeType: models.NodeTypeSkill, ExternalRef: "metadata-test", Project: "project", Metadata: metadata,
				})
				return err
			},
		},
		{
			name: "Update",
			call: func(metadata []byte) error {
				_, err := ns.Update(ctx, &models.KnowledgeNode{ID: 1, Metadata: metadata})
				return err
			},
		},
	}
	cases := []struct {
		name     string
		metadata []byte
		message  string
	}{
		{name: "malformed", metadata: []byte(`{"unterminated":`), message: "valid JSON"},
		{name: "array", metadata: []byte(`[]`), message: "JSON object"},
		{name: "string", metadata: []byte(`"value"`), message: "JSON object"},
		{name: "number", metadata: []byte(`1`), message: "JSON object"},
		{name: "boolean", metadata: []byte(`true`), message: "JSON object"},
	}

	for _, operation := range operations {
		for _, tc := range cases {
			t.Run(operation.name+"/"+tc.name, func(t *testing.T) {
				err := operation.call(tc.metadata)
				if err == nil || !strings.Contains(err.Error(), tc.message) {
					t.Fatalf("expected metadata validation error containing %q, got %v", tc.message, err)
				}
			})
		}
	}
}

// TestNodesStore_Get_IncludePrivateParam verifies that the Get method signature
// includes the includePrivate bool parameter, consistent with ListByType
// (7th-alternate-read-path bypass pattern fix, Finding 3).
//
// This is a compile-time shape test; live privacy filtering requires DATABASE_DSN.
func TestNodesStore_Get_IncludePrivateParam(t *testing.T) {
	// Verify the interface shape includes includePrivate via the getter interface
	// defined in TestNodesStore_T012_UnitShape.
	type privacyAwareGetter interface {
		Get(ctx context.Context, id int64, includePrivate bool) (*models.KnowledgeNode, error)
	}
	var _ privacyAwareGetter = (*NodesStore)(nil)
	t.Log("NodesStore.Get includePrivate parameter shape verified")
}

// TestNodesStore_SoftDelete_MethodShape verifies that SoftDelete has the
// correct method signature (id int64) → error and reaches the DB call on
// valid input (no early-return guard for non-zero IDs).
//
// WHERE-clause correctness (id = ? AND deleted_at IS NULL) and the
// "not found or already deleted" error path require a real Postgres instance.
// Those assertions live in the DSN-gated integration suite; this offline test
// only confirms the method is reachable with a valid ID.
//
// Note: SQLite is not used in this repo — offline WHERE-clause assertion is
// not feasible. The DSN integration test (TestPathC_T015_SkillNodeEdgeRoundtrip
// and TestDangling_T016_DanglingEdgeReturnsFlag) provide the live round-trip.
func TestNodesStore_SoftDelete_MethodShape(t *testing.T) {
	ns := &NodesStore{db: nil}
	ctx := context.Background()

	// Calling SoftDelete with a valid ID (non-zero) on a nil-db store reaches
	// the gorm DB call and panics on nil pointer dereference. This confirms:
	//   (a) no early-return guard intercepts a valid ID before the WHERE clause, and
	//   (b) the method signature matches the deleter interface.
	defer func() {
		if r := recover(); r == nil {
			t.Error("SoftDelete on nil db with valid ID must reach the DB call")
		}
	}()
	_ = ns.SoftDelete(ctx, 1) // reaches nil-db → panic expected
}
