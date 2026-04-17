package gorm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/models"
)

// TestMemoryStore_CreateGetUpdateListDelete exercises the full Create→Get→Update→List→Delete
// round-trip against a real PostgreSQL database.
// Anti-stub contract: if any method body is replaced with `return nil` this test fails.
func TestMemoryStore_CreateGetUpdateListDelete(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM memories WHERE project = 'test-memory-store'`)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ctx := context.Background()

	const testProject = "test-memory-store"

	// --- Create ---
	mem := &models.Memory{
		Project:     testProject,
		Content:     "discovered that gRPC retry policy requires idempotency tokens",
		Tags:        []string{"grpc", "retry", "architecture"},
		SourceAgent: "smoke-agent",
		EditedBy:    "smoke-test",
	}
	created, err := ms.Create(ctx, mem)
	require.NoError(t, err, "Create should succeed")
	assert.Greater(t, created.ID, int64(0), "Create should return a populated ID")
	assert.False(t, created.CreatedAt.IsZero(), "Create should return a populated CreatedAt")
	assert.False(t, created.UpdatedAt.IsZero(), "Create should return a populated UpdatedAt")
	assert.Equal(t, testProject, created.Project)
	assert.Equal(t, mem.Content, created.Content)
	assert.Equal(t, []string{"grpc", "retry", "architecture"}, created.Tags)
	assert.Equal(t, "smoke-agent", created.SourceAgent)
	assert.Equal(t, 1, created.Version, "Version should be 1 on create")
	assert.Nil(t, created.DeletedAt, "active memory must have nil deleted_at")

	// Verify input was NOT mutated
	assert.Equal(t, int64(0), mem.ID, "Create must not mutate caller's input ID")

	// --- Get ---
	fetched, err := ms.Get(ctx, created.ID)
	require.NoError(t, err, "Get should return the created memory")
	assert.Equal(t, created.ID, fetched.ID)
	assert.Equal(t, testProject, fetched.Project)
	assert.Equal(t, mem.Content, fetched.Content)
	assert.Equal(t, []string{"grpc", "retry", "architecture"}, fetched.Tags)

	// --- Update ---
	updated, err := ms.Update(ctx, &models.Memory{
		ID:       created.ID,
		Content:  "updated: grpc retry requires idempotency tokens AND exponential backoff",
		Tags:     []string{"grpc", "retry", "architecture", "backoff"},
		EditedBy: "update-test",
	})
	require.NoError(t, err, "Update should succeed")
	assert.Equal(t, created.ID, updated.ID, "Update should return same ID")
	assert.Equal(t, "updated: grpc retry requires idempotency tokens AND exponential backoff", updated.Content)
	assert.Equal(t, []string{"grpc", "retry", "architecture", "backoff"}, updated.Tags)
	assert.Equal(t, "update-test", updated.EditedBy)
	assert.Equal(t, 2, updated.Version, "Version should be bumped to 2 after update")

	// --- List ---
	list, err := ms.List(ctx, testProject, 10)
	require.NoError(t, err, "List should succeed")
	require.GreaterOrEqual(t, len(list), 1, "List should return at least one memory")
	found := false
	for _, m := range list {
		if m.ID == created.ID {
			found = true
			break
		}
	}
	assert.True(t, found, "Created memory must appear in List")

	// --- Delete ---
	err = ms.Delete(ctx, created.ID)
	require.NoError(t, err, "Delete should succeed")

	// Verify hard-delete: Get should no longer find the row.
	_, err = ms.Get(ctx, created.ID)
	require.Error(t, err, "Get after Delete should return an error (row gone)")

	// List should not return the deleted row.
	listAfter, err := ms.List(ctx, testProject, 10)
	require.NoError(t, err)
	for _, m := range listAfter {
		assert.NotEqual(t, created.ID, m.ID, "Deleted memory must not appear in List")
	}

	// --- Delete non-existent ID ---
	err = ms.Delete(ctx, 99999999)
	require.Error(t, err, "Delete of non-existent ID should return an error")
}

// TestMemoryStore_Create_ValidationErrors verifies that Create rejects invalid input.
func TestMemoryStore_Create_ValidationErrors(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	// No rows are inserted in this test (all creates fail), so no extra cleanup needed.

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ctx := context.Background()

	cases := []struct {
		name string
		mem  *models.Memory
	}{
		{"nil memory", nil},
		{"empty project", &models.Memory{Content: "some content"}},
		{"empty content", &models.Memory{Project: "proj"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ms.Create(ctx, tc.mem)
			require.Error(t, err, "Create with %q should fail", tc.name)
		})
	}
}

// TestMemoryStore_List_FiltersByProject inserts 3 memories across 2 projects and confirms
// List returns only the requested project's rows.
func TestMemoryStore_List_FiltersByProject(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM memories WHERE project IN ('test-memory-list-proj1','test-memory-list-proj2')`)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ctx := context.Background()

	const proj1 = "test-memory-list-proj1"
	const proj2 = "test-memory-list-proj2"

	// Insert 2 memories for proj1 and 1 for proj2.
	_, err := ms.Create(ctx, &models.Memory{Project: proj1, Content: "proj1 memory A"})
	require.NoError(t, err)
	_, err = ms.Create(ctx, &models.Memory{Project: proj1, Content: "proj1 memory B"})
	require.NoError(t, err)
	_, err = ms.Create(ctx, &models.Memory{Project: proj2, Content: "proj2 memory A"})
	require.NoError(t, err)

	// List proj1: must return exactly 2 rows.
	list1, err := ms.List(ctx, proj1, 100)
	require.NoError(t, err)
	assert.Len(t, list1, 2, "proj1 should have exactly 2 memories")
	for _, m := range list1 {
		assert.Equal(t, proj1, m.Project, "all rows must belong to proj1")
	}

	// List proj2: must return exactly 1 row.
	list2, err := ms.List(ctx, proj2, 100)
	require.NoError(t, err)
	assert.Len(t, list2, 1, "proj2 should have exactly 1 memory")
	assert.Equal(t, proj2, list2[0].Project)
	assert.Equal(t, "proj2 memory A", list2[0].Content)
}
