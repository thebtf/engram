package gorm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/models"
)

func strPtr(s string) *string { return &s }

// TestBehavioralRulesStore_CreateGetUpdateListDelete exercises the full
// Create→Get→Update→List→Delete round-trip against a real PostgreSQL database.
// Anti-stub contract: if any method body is replaced with `return nil` this test fails.
func TestBehavioralRulesStore_CreateGetUpdateListDelete(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM behavioral_rules WHERE project = 'test-brules-store'`)

	store := &Store{DB: db}
	brs := NewBehavioralRulesStore(store)
	ctx := context.Background()

	proj := "test-brules-store"

	// --- Create project-scoped rule ---
	rule := &models.BehavioralRule{
		Project:  strPtr(proj),
		Content:  "always validate input at every system boundary",
		Priority: 10,
		EditedBy: "smoke-test",
	}
	created, err := brs.Create(ctx, rule)
	require.NoError(t, err, "Create should succeed")
	assert.Greater(t, created.ID, int64(0), "Create should return a populated ID")
	assert.NotNil(t, created.CreatedAt, "Create should return a populated CreatedAt")
	assert.NotNil(t, created.UpdatedAt, "Create should return a populated UpdatedAt")
	assert.Equal(t, proj, *created.Project)
	assert.Equal(t, rule.Content, created.Content)
	assert.Equal(t, 10, created.Priority)
	assert.Equal(t, 1, created.Version, "Version should be 1 on create")
	assert.Nil(t, created.DeletedAt, "active rule must have nil deleted_at")

	// Verify input was NOT mutated
	assert.Equal(t, int64(0), rule.ID, "Create must not mutate caller's input ID")

	// --- Get ---
	fetched, err := brs.Get(ctx, created.ID)
	require.NoError(t, err, "Get should return the created rule")
	assert.Equal(t, created.ID, fetched.ID)
	assert.Equal(t, rule.Content, fetched.Content)
	assert.Equal(t, 10, fetched.Priority)

	// --- Update ---
	updated, err := brs.Update(ctx, &models.BehavioralRule{
		ID:       created.ID,
		Content:  "always validate input at every boundary AND use schema-based validation",
		Priority: 20,
		EditedBy: "update-test",
	})
	require.NoError(t, err, "Update should succeed")
	assert.Equal(t, created.ID, updated.ID)
	assert.Equal(t, "always validate input at every boundary AND use schema-based validation", updated.Content)
	assert.Equal(t, 20, updated.Priority)
	assert.Equal(t, "update-test", updated.EditedBy)
	assert.Equal(t, 2, updated.Version, "Version should be bumped to 2 after update")

	// --- List ---
	list, err := brs.List(ctx, strPtr(proj), 100)
	require.NoError(t, err, "List should succeed")
	found := false
	for _, r := range list {
		if r.ID == created.ID {
			found = true
			break
		}
	}
	assert.True(t, found, "Created rule must appear in List")

	// --- Delete ---
	err = brs.Delete(ctx, created.ID)
	require.NoError(t, err, "Delete should succeed")

	// Verify hard-delete: Get should no longer find the row.
	_, err = brs.Get(ctx, created.ID)
	require.Error(t, err, "Get after Delete should return an error (row gone)")

	// List should not return the deleted row.
	listAfter, err := brs.List(ctx, strPtr(proj), 100)
	require.NoError(t, err)
	for _, r := range listAfter {
		assert.NotEqual(t, created.ID, r.ID, "Deleted rule must not appear in List")
	}

	// --- Delete non-existent ID ---
	err = brs.Delete(ctx, 99999999)
	require.Error(t, err, "Delete of non-existent ID should return an error")
}

// TestBehavioralRulesStore_Create_ValidationErrors verifies that Create rejects invalid input.
func TestBehavioralRulesStore_Create_ValidationErrors(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	// No rows are inserted in this test (all creates fail), so no extra cleanup needed.

	store := &Store{DB: db}
	brs := NewBehavioralRulesStore(store)
	ctx := context.Background()

	cases := []struct {
		name string
		rule *models.BehavioralRule
	}{
		{"nil rule", nil},
		{"empty content", &models.BehavioralRule{Project: strPtr("proj"), Content: ""}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := brs.Create(ctx, tc.rule)
			require.Error(t, err, "Create with %q should fail", tc.name)
		})
	}
}

// TestBehavioralRulesStore_List_GlobalRulesAlwaysIncluded verifies the scoping rules:
//   - List(project="p1") returns both the project-scoped rule AND the global rule.
//   - List(project=nil)  returns only the global rule (not the project-scoped one).
func TestBehavioralRulesStore_List_GlobalRulesAlwaysIncluded(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM behavioral_rules WHERE project = 'test-brules-global' OR (project IS NULL AND content LIKE 'global rule: never hardcode%')`)

	store := &Store{DB: db}
	brs := NewBehavioralRulesStore(store)
	ctx := context.Background()

	const proj = "test-brules-global"

	// Create a global rule (project = nil).
	globalRule, err := brs.Create(ctx, &models.BehavioralRule{
		Project:  nil,
		Content:  "global rule: never hardcode secrets",
		Priority: 100,
	})
	require.NoError(t, err, "Create global rule should succeed")

	// Create a project-scoped rule.
	projRule, err := brs.Create(ctx, &models.BehavioralRule{
		Project:  strPtr(proj),
		Content:  "project rule: always run safety-gate before push",
		Priority: 50,
	})
	require.NoError(t, err, "Create project rule should succeed")

	// List(project="p1") must return BOTH the project rule and the global rule.
	listProj, err := brs.List(ctx, strPtr(proj), 100)
	require.NoError(t, err)
	ids := make(map[int64]bool)
	for _, r := range listProj {
		ids[r.ID] = true
	}
	assert.True(t, ids[globalRule.ID], "List(project) must include the global rule")
	assert.True(t, ids[projRule.ID], "List(project) must include the project-scoped rule")

	// List(project=nil) must return ONLY global rules — NOT the project-scoped rule.
	listGlobal, err := brs.List(ctx, nil, 100)
	require.NoError(t, err)
	globalIDs := make(map[int64]bool)
	for _, r := range listGlobal {
		globalIDs[r.ID] = true
	}
	assert.True(t, globalIDs[globalRule.ID], "List(nil) must include the global rule")
	assert.False(t, globalIDs[projRule.ID], "List(nil) must NOT include project-scoped rules")
}
