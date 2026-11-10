package gorm

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

func strPtr(s string) *string { return &s }

func openBehavioralRulesStore(t *testing.T) *Store {
	t.Helper()
	db, cleanup := openTestDB(t)
	t.Cleanup(cleanup)
	return &Store{DB: db}
}

// TestBehavioralRulesStore_CreateGetUpdateListDelete exercises the full
// Create→Get→Update→List→Delete round-trip against a real PostgreSQL database.
// Anti-stub contract: if any method body is replaced with `return nil` this test fails.
func TestBehavioralRulesStore_CreateGetUpdateListDelete(t *testing.T) {
	store := openBehavioralRulesStore(t)
	db := store.DB
	brs := NewBehavioralRulesStore(store)
	ctx := context.Background()

	proj := sessionTestProject(t, db)
	defer db.Exec(`DELETE FROM behavioral_rules WHERE project = ?`, proj)

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
	assert.True(t, created.Enabled, "Create must default rules to enabled for existing store_rule callers")
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
	assert.True(t, updated.Enabled, "content/priority updates must preserve enabled state")
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

	// --- Disable ---
	disabled, err := brs.SetEnabled(ctx, created.ID, false, strPtr("toggle-test"))
	require.NoError(t, err, "SetEnabled(false) should succeed")
	assert.False(t, disabled.Enabled)
	assert.Equal(t, "toggle-test", disabled.EditedBy)
	assert.Greater(t, disabled.Version, updated.Version)

	operatorList, err := brs.List(ctx, strPtr(proj), 100)
	require.NoError(t, err, "operator List should keep disabled rows visible")
	foundDisabledInOperatorList := false
	for _, r := range operatorList {
		if r.ID == created.ID {
			foundDisabledInOperatorList = true
			assert.False(t, r.Enabled, "List should surface disabled state")
			break
		}
	}
	assert.True(t, foundDisabledInOperatorList, "disabled rule must remain visible in operator List")

	enabledList, err := brs.ListEnabled(ctx, strPtr(proj), 100)
	require.NoError(t, err, "ListEnabled should succeed")
	for _, r := range enabledList {
		assert.NotEqual(t, created.ID, r.ID, "disabled rule must not appear in injection ListEnabled")
	}

	reenabled, err := brs.SetEnabled(ctx, created.ID, true, nil)
	require.NoError(t, err, "SetEnabled(true) should succeed")
	assert.True(t, reenabled.Enabled)
	assert.Equal(t, "toggle-test", reenabled.EditedBy, "empty editedBy must preserve existing audit actor")
	assert.Greater(t, reenabled.Version, disabled.Version)

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
	store := openBehavioralRulesStore(t)
	// No rows are inserted in this test (all creates fail), so no extra cleanup needed.
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
	store := openBehavioralRulesStore(t)
	db := store.DB
	brs := NewBehavioralRulesStore(store)
	ctx := context.Background()

	proj := sessionTestProject(t, db)
	defer db.Exec(`DELETE FROM behavioral_rules WHERE project = ? OR (project IS NULL AND content LIKE 'global rule: never hardcode%')`, proj)

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

func TestBehavioralRulesStore_ApplySelectionOperation_OverTwoHundredRules(t *testing.T) {
	store := openBehavioralRulesStore(t)
	db := store.DB
	brs := NewBehavioralRulesStore(store)
	selections := NewCollectionSelectionStore(db)
	ctx := context.Background()
	project := sessionTestProject(t, db)
	t.Cleanup(func() { require.NoError(t, db.Exec("DELETE FROM behavioral_rules WHERE project = ?", project).Error) })

	rules := make([]*models.BehavioralRule, 205)
	for index := range rules {
		created, err := brs.Create(ctx, &models.BehavioralRule{
			Project:  strPtr(project),
			Content:  fmt.Sprintf("selection fixture rule %03d", index),
			Priority: len(rules) - index,
			EditedBy: "selection-fixture",
		})
		require.NoError(t, err)
		rules[index] = created
	}

	scope := behavioralRuleSelectionScope("behavioral-rules-selection-" + strconv.FormatInt(time.Now().UnixNano(), 10))
	explicit, err := selections.Save(ctx, scope, CollectionSelection{
		Kind:    CollectionSelectionExplicit,
		Targets: behavioralRuleSelectionTargets(t, brs, rules[:2]),
	})
	require.NoError(t, err)
	disabled, err := brs.ApplySelectionOperation(ctx, scope, BehavioralRuleSelectionOperation{
		Action:           BehavioralRuleSelectionDisable,
		SelectionKind:    explicit.Kind,
		SelectionVersion: explicit.Version,
	})
	require.NoError(t, err)
	assertBehavioralRuleOperationCommitted(t, disabled, 2)
	for _, rule := range rules[:2] {
		current, getErr := brs.Get(ctx, rule.ID)
		require.NoError(t, getErr)
		assert.False(t, current.Enabled)
	}

	page, err := selections.Save(ctx, scope, CollectionSelection{
		Kind:    CollectionSelectionPage,
		Cursor:  "rules-page-2",
		Targets: behavioralRuleSelectionTargets(t, brs, rules[2:4]),
	})
	require.NoError(t, err)
	deleted, err := brs.ApplySelectionOperation(ctx, scope, BehavioralRuleSelectionOperation{
		Action:           BehavioralRuleSelectionDelete,
		SelectionKind:    page.Kind,
		SelectionVersion: page.Version,
	})
	require.NoError(t, err)
	assertBehavioralRuleOperationCommitted(t, deleted, 2)
	for _, rule := range rules[2:4] {
		_, getErr := brs.Get(ctx, rule.ID)
		require.ErrorIs(t, getErr, gorm.ErrRecordNotFound)
	}

	active := append(append([]*models.BehavioralRule(nil), rules[:2]...), rules[4:]...)
	frozen, err := selections.Save(ctx, scope, CollectionSelection{
		Kind:              CollectionSelectionFrozenFilter,
		FilterFingerprint: behavioralRuleSelectionDigest("enabled"),
		Targets:           behavioralRuleSelectionTargets(t, brs, active),
		ExcludedIDs:       []string{strconv.FormatInt(active[len(active)-1].ID, 10)},
		ExpiresAt:         time.Now().UTC().Add(30 * time.Minute),
	})
	require.NoError(t, err)
	enabled, err := brs.ApplySelectionOperation(ctx, scope, BehavioralRuleSelectionOperation{
		Action:           BehavioralRuleSelectionEnable,
		SelectionKind:    frozen.Kind,
		SelectionVersion: frozen.Version,
		SelectionToken:   frozen.Token,
	})
	require.NoError(t, err)
	assertBehavioralRuleOperationCommitted(t, enabled, len(active)-1)
	for _, rule := range rules[:2] {
		current, getErr := brs.Get(ctx, rule.ID)
		require.NoError(t, getErr)
		assert.True(t, current.Enabled)
	}

	priority := 777
	content := "selection operation updated content"
	frozen, err = selections.Save(ctx, scope, CollectionSelection{
		Kind:              CollectionSelectionFrozenFilter,
		FilterFingerprint: behavioralRuleSelectionDigest("priority"),
		Targets:           behavioralRuleSelectionTargets(t, brs, active),
		ExcludedIDs:       []string{strconv.FormatInt(active[len(active)-1].ID, 10)},
		ExpiresAt:         time.Now().UTC().Add(30 * time.Minute),
	})
	require.NoError(t, err)
	updated, err := brs.ApplySelectionOperation(ctx, scope, BehavioralRuleSelectionOperation{
		Action:           BehavioralRuleSelectionUpdate,
		SelectionKind:    frozen.Kind,
		SelectionVersion: frozen.Version,
		SelectionToken:   frozen.Token,
		Priority:         &priority,
		Content:          &content,
	})
	require.NoError(t, err)
	assertBehavioralRuleOperationCommitted(t, updated, len(active)-1)
	for _, rule := range active[:len(active)-1] {
		current, getErr := brs.Get(ctx, rule.ID)
		require.NoError(t, getErr)
		assert.Equal(t, priority, current.Priority)
		assert.Equal(t, content, current.Content)
	}
	excluded, err := brs.Get(ctx, active[len(active)-1].ID)
	require.NoError(t, err)
	assert.NotEqual(t, priority, excluded.Priority)
	assert.NotEqual(t, content, excluded.Content)
	// A stale target must reject the entire selected mutation; it may not leave
	// a later current target changed after reporting the stale revision.
	conflictSelection, err := selections.Save(ctx, scope, CollectionSelection{
		Kind:    CollectionSelectionExplicit,
		Targets: behavioralRuleSelectionTargets(t, brs, rules[:2]),
	})
	require.NoError(t, err)
	beforeCurrent, err := brs.Get(ctx, rules[1].ID)
	require.NoError(t, err)
	_, err = brs.SetEnabled(ctx, rules[0].ID, false, nil)
	require.NoError(t, err)
	_, err = brs.ApplySelectionOperation(ctx, scope, BehavioralRuleSelectionOperation{
		Action:           BehavioralRuleSelectionDisable,
		SelectionKind:    conflictSelection.Kind,
		SelectionVersion: conflictSelection.Version,
	})
	require.ErrorIs(t, err, ErrBehavioralRuleSelectionConflict)
	afterCurrent, err := brs.Get(ctx, rules[1].ID)
	require.NoError(t, err)
	assert.Equal(t, beforeCurrent.Enabled, afterCurrent.Enabled, "a stale selected rule must not partially disable another rule")
	assert.Equal(t, beforeCurrent.Version, afterCurrent.Version, "a stale selected rule must not partially bump another rule version")
}

func TestBehavioralRulesStore_ApplySelectionOperation_ReorderConflictRollsBackScope(t *testing.T) {
	store := openBehavioralRulesStore(t)
	db := store.DB
	brs := NewBehavioralRulesStore(store)
	selections := NewCollectionSelectionStore(db)
	ctx := context.Background()
	project := sessionTestProject(t, db)
	t.Cleanup(func() { require.NoError(t, db.Exec("DELETE FROM behavioral_rules WHERE project = ?", project).Error) })

	rules := make([]*models.BehavioralRule, 4)
	for index := range rules {
		created, err := brs.Create(ctx, &models.BehavioralRule{Project: strPtr(project), Content: fmt.Sprintf("reorder rule %d", index), Priority: 40 - index})
		require.NoError(t, err)
		rules[index] = created
	}
	scope := behavioralRuleSelectionScope("behavioral-rules-reorder-" + strconv.FormatInt(time.Now().UnixNano(), 10))
	selection, err := selections.Save(ctx, scope, CollectionSelection{
		Kind:    CollectionSelectionExplicit,
		Targets: behavioralRuleSelectionTargets(t, brs, rules),
	})
	require.NoError(t, err)

	reorder := make([]BehavioralRuleOrder, len(selection.Targets))
	for index := range selection.Targets {
		target := selection.Targets[len(selection.Targets)-1-index]
		ruleID, parseErr := strconv.ParseInt(target.ID, 10, 64)
		require.NoError(t, parseErr)
		reorder[index] = BehavioralRuleOrder{RuleID: ruleID, ExpectedVersion: target.ExpectedVersion}
	}

	require.NoError(t, db.Model(&BehavioralRule{}).Where("id = ?", rules[0].ID).Updates(map[string]any{"version": gorm.Expr("version + 1")}).Error)
	var before []BehavioralRule
	require.NoError(t, db.Where("project = ?", project).Order("id ASC").Find(&before).Error)

	_, err = brs.ApplySelectionOperation(ctx, scope, BehavioralRuleSelectionOperation{
		Action:           BehavioralRuleSelectionReorder,
		SelectionKind:    selection.Kind,
		SelectionVersion: selection.Version,
		Scope:            &BehavioralRuleScope{Project: strPtr(project)},
		Order:            reorder,
	})
	require.ErrorIs(t, err, ErrBehavioralRuleSelectionConflict)

	var after []BehavioralRule
	require.NoError(t, db.Where("project = ?", project).Order("id ASC").Find(&after).Error)
	require.Equal(t, before, after, "a reorder conflict must leave every row in its declared scope byte-equivalent")
}

func TestBehavioralRulesStore_ApplySelectionOperation_ReorderInjectedFailureRollsBackScope(t *testing.T) {
	store := openBehavioralRulesStore(t)
	db := store.DB
	brs := NewBehavioralRulesStore(store)
	selections := NewCollectionSelectionStore(db)
	ctx := context.Background()
	project := sessionTestProject(t, db)
	t.Cleanup(func() { require.NoError(t, db.Exec("DELETE FROM behavioral_rules WHERE project = ?", project).Error) })

	rules := make([]*models.BehavioralRule, 3)
	for index := range rules {
		created, err := brs.Create(ctx, &models.BehavioralRule{Project: strPtr(project), Content: fmt.Sprintf("reorder failure rule %d", index), Priority: 30 - index})
		require.NoError(t, err)
		rules[index] = created
	}
	scope := behavioralRuleSelectionScope("behavioral-rules-reorder-failure-" + strconv.FormatInt(time.Now().UnixNano(), 10))
	selection, err := selections.Save(ctx, scope, CollectionSelection{
		Kind:    CollectionSelectionExplicit,
		Targets: behavioralRuleSelectionTargets(t, brs, rules),
	})
	require.NoError(t, err)

	reorder := make([]BehavioralRuleOrder, len(selection.Targets))
	for index := range selection.Targets {
		target := selection.Targets[len(selection.Targets)-1-index]
		ruleID, parseErr := strconv.ParseInt(target.ID, 10, 64)
		require.NoError(t, parseErr)
		reorder[index] = BehavioralRuleOrder{RuleID: ruleID, ExpectedVersion: target.ExpectedVersion}
	}
	functionName := fmt.Sprintf("behavioral_rules_reorder_fail_%d", time.Now().UnixNano())
	triggerName := functionName + "_trigger"
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id = %d THEN RAISE EXCEPTION 'injected reorder failure'; END IF; RETURN NEW; END; $$`, functionName, rules[0].ID)).Error)
	require.NoError(t, db.Exec(fmt.Sprintf("CREATE TRIGGER %s BEFORE UPDATE ON behavioral_rules FOR EACH ROW EXECUTE FUNCTION %s()", triggerName, functionName)).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON behavioral_rules", triggerName)).Error)
		require.NoError(t, db.Exec(fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName)).Error)
	})

	var before []BehavioralRule
	require.NoError(t, db.Where("project = ?", project).Order("id ASC").Find(&before).Error)
	_, err = brs.ApplySelectionOperation(ctx, scope, BehavioralRuleSelectionOperation{
		Action:           BehavioralRuleSelectionReorder,
		SelectionKind:    selection.Kind,
		SelectionVersion: selection.Version,
		Scope:            &BehavioralRuleScope{Project: strPtr(project)},
		Order:            reorder,
	})
	require.Error(t, err)
	var after []BehavioralRule
	require.NoError(t, db.Where("project = ?", project).Order("id ASC").Find(&after).Error)
	require.Equal(t, before, after, "an injected reorder failure must roll back every update in the declared scope")
}

func TestBehavioralRulesStore_CollectionBridgePaginatesFreezesAndReconfirms(t *testing.T) {
	store := openBehavioralRulesStore(t)
	db := store.DB
	brs := NewBehavioralRulesStore(store)
	ctx := context.Background()
	project := sessionTestProject(t, db)
	t.Cleanup(func() { require.NoError(t, db.Exec("DELETE FROM behavioral_rules WHERE project = ?", project).Error) })

	rules := make([]*models.BehavioralRule, 205)
	for index := range rules {
		created, err := brs.Create(ctx, &models.BehavioralRule{
			Project:  strPtr(project),
			Content:  fmt.Sprintf("collection rule %03d", index),
			Priority: len(rules) - index,
		})
		require.NoError(t, err)
		rules[index] = created
	}

	scope := behavioralRuleSelectionScope("behavioral-rules-collection-" + strconv.FormatInt(time.Now().UnixNano(), 10))
	filter, err := brs.NormalizeCollectionFilter(ctx, scope, project)
	require.NoError(t, err)
	first, err := brs.PageCollection(ctx, scope, CollectionPageRequest{Domain: "rules", Filter: filter, Limit: CollectionPageMaxSize})
	require.NoError(t, err)
	require.Len(t, first.Targets, CollectionPageMaxSize)
	require.NotEmpty(t, first.Cursor)
	require.NotEmpty(t, first.NextCursor)
	require.NotNil(t, first.Total)
	require.EqualValues(t, len(rules), *first.Total)
	require.Equal(t, CollectionSelectionTarget{ID: strconv.FormatInt(rules[0].ID, 10), ExpectedVersion: uint64(rules[0].Version)}, first.Targets[0])

	second, err := brs.PageCollection(ctx, scope, CollectionPageRequest{Domain: "rules", Filter: filter, Cursor: first.NextCursor, Limit: CollectionPageMaxSize})
	require.NoError(t, err)
	require.Equal(t, first.NextCursor, second.Cursor)
	require.Len(t, second.Targets, 5)
	require.Empty(t, second.NextCursor)
	require.Equal(t, CollectionSelectionTarget{ID: strconv.FormatInt(rules[200].ID, 10), ExpectedVersion: uint64(rules[200].Version)}, second.Targets[0])

	frozen, err := brs.FreezeCollectionSelection(ctx, scope, filter)
	require.NoError(t, err)
	require.Len(t, frozen.Targets, len(rules))
	require.Equal(t, first.Targets[0], frozen.Targets[0])
	pageTargets, err := brs.FreezeCollectionPageSelection(ctx, scope, first.Cursor)
	require.NoError(t, err)
	require.Equal(t, first.Targets, pageTargets)
	unchanged, err := brs.Get(ctx, rules[0].ID)
	require.NoError(t, err)
	require.Equal(t, rules[0].Version, unchanged.Version, "paging and freezing are read-only")

	_, err = brs.SetEnabled(ctx, rules[3].ID, false, nil)
	require.NoError(t, err)
	_, err = brs.PageCollection(ctx, scope, CollectionPageRequest{Domain: "rules", Filter: filter, Cursor: first.NextCursor, Limit: CollectionPageMaxSize})
	require.ErrorIs(t, err, ErrCollectionSelectionReconfirmationRequired)
	global, err := brs.NormalizeCollectionFilter(ctx, scope, behavioralRuleCollectionGlobal)
	require.NoError(t, err)
	_, err = brs.PageCollection(ctx, scope, CollectionPageRequest{Domain: "rules", Filter: global, Cursor: first.NextCursor, Limit: CollectionPageMaxSize})
	require.ErrorIs(t, err, ErrCollectionSelectionInvalid)
}

func behavioralRuleSelectionScope(sessionID string) CollectionSelectionScope {
	return CollectionSelectionScope{
		SubjectUserID:      41,
		SessionID:          sessionID,
		Domain:             "rules",
		ContextFingerprint: behavioralRuleSelectionDigest("context"),
		AuthorizationEpoch: 1,
		CollectionVersion:  1,
	}
}

func behavioralRuleSelectionDigest(value string) string {
	return "sha256:" + fmt.Sprintf("%064x", len(value))
}

func behavioralRuleSelectionTargets(t *testing.T, store *BehavioralRulesStore, rules []*models.BehavioralRule) []CollectionSelectionTarget {
	t.Helper()
	targets := make([]CollectionSelectionTarget, len(rules))
	for index, rule := range rules {
		current, err := store.Get(context.Background(), rule.ID)
		require.NoError(t, err)
		targets[index] = CollectionSelectionTarget{ID: strconv.FormatInt(rule.ID, 10), ExpectedVersion: uint64(current.Version)}
	}
	return targets
}

func assertBehavioralRuleOperationCommitted(t *testing.T, result BehavioralRuleSelectionOperationResult, count int) {
	t.Helper()
	require.Len(t, result.Items, count)
	for _, item := range result.Items {
		assert.Equal(t, BehavioralRuleSelectionCommitted, item.Outcome)
	}
}

func TestRulesAliasCollectionTransaction(t *testing.T) {
	store := openBehavioralRulesStore(t)
	db := store.DB
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(3)
	rules := NewBehavioralRulesStore(store)
	ctx := context.Background()
	primary, alias, foreign := sessionTestProject(t, db), sessionTestProject(t, db), sessionTestProject(t, db)
	for _, project := range []string{primary, alias, foreign} {
		row := BehavioralRule{Project: &project, Content: "synthetic alias rule", Version: 3, Enabled: false, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		require.NoError(t, db.Create(&row).Error)
		require.NoError(t, db.Model(&BehavioralRule{}).Where("id = ?", row.ID).Update("enabled", false).Error)
	}
	ambiguous := BehavioralRule{Project: &alias, Content: "unresolved rule", Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	require.NoError(t, db.Create(&ambiguous).Error)
	membership := ConsolidationMembership{Reference: "urn:engram:test:rules:" + strconv.FormatInt(ambiguous.ID, 10), Table: "behavioral_rules", RowKey: JSONRaw(fmt.Sprintf(`{"id":%d}`, ambiguous.ID)), FieldPath: JSONRaw(`["project"]`), ProjectID: nil, PlanFingerprint: "rules-fixture"}
	require.NoError(t, db.Create(&membership).Error)
	t.Cleanup(func() {
		db.Exec("DELETE FROM project_consolidation_redirects WHERE selector = ?", alias)
		db.Exec("DELETE FROM behavioral_rules WHERE project IN ?", []string{primary, alias, foreign})
	})
	require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', ?, 'test')`, alias, primary).Error)
	scope := behavioralRuleSelectionScope("rules-alias-" + strconv.FormatInt(time.Now().UnixNano(), 10))
	mainRows, err := rules.ListOperator(ctx, &primary, 100)
	require.NoError(t, err)
	aliasRows, err := rules.ListOperator(ctx, &alias, 100)
	require.NoError(t, err)
	require.Equal(t, mainRows, aliasRows)
	require.Len(t, mainRows, 2)
	for _, row := range mainRows {
		require.False(t, row.Enabled)
		require.Equal(t, 3, row.Version)
		require.Contains(t, []string{primary, alias}, *row.Project)
	}
	// A freeze cannot observe the temporary mapping while holding its old fingerprint.
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { tx.Rollback() })
	require.NoError(t, aliasLock(tx))
	require.NoError(t, tx.Exec(`DELETE FROM project_consolidation_redirects WHERE selector = ?`, alias).Error)
	type freezeResult struct {
		selection CollectionSelection
		err       error
	}
	completed := make(chan freezeResult, 1)
	go func() {
		selection, err := rules.SaveRulesSelection(ctx, scope, primary, CollectionSelection{Kind: CollectionSelectionFrozenFilter})
		completed <- freezeResult{selection, err}
	}()
	require.Eventually(t, func() bool {
		var waiting int64
		err := db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE 'LOCK TABLE audit_log%'`).Scan(&waiting).Error
		return err == nil && waiting > 0
	}, 3*time.Second, 20*time.Millisecond, "freeze must actually wait for the alias lock")
	require.NoError(t, tx.Rollback().Error)
	outcome := <-completed
	require.NoError(t, outcome.err)
	require.Len(t, outcome.selection.Targets, 2)
	filter, err := rules.NormalizeCollectionFilter(ctx, scope, primary)
	require.NoError(t, err)
	qualified, err := RulesProjectionScope(scope, filter)
	require.NoError(t, err)
	page, err := rules.PageCollection(ctx, qualified, CollectionPageRequest{Domain: "rules", Filter: filter, Limit: 50})
	require.NoError(t, err)
	require.Len(t, page.Targets, 2)
	saved, err := rules.SaveRulesSelection(ctx, scope, primary, CollectionSelection{Kind: CollectionSelectionFrozenFilter})
	require.NoError(t, err)
	require.Equal(t, page.Targets, saved.Targets)
	order := make([]BehavioralRuleOrder, 0, len(saved.Targets))
	for _, target := range saved.Targets {
		id, err := strconv.ParseInt(target.ID, 10, 64)
		require.NoError(t, err)
		order = append(order, BehavioralRuleOrder{RuleID: id, ExpectedVersion: target.ExpectedVersion})
	}
	reordered, err := rules.ApplySelectionOperation(ctx, qualified, BehavioralRuleSelectionOperation{Action: BehavioralRuleSelectionReorder, SelectionKind: saved.Kind, SelectionVersion: saved.Version, SelectionToken: saved.Token, Scope: &BehavioralRuleScope{Project: &primary}, Order: order, ProjectionFilter: &filter})
	require.NoError(t, err)
	require.Len(t, reordered.Items, 2)
	for _, item := range reordered.Items {
		require.False(t, item.Rule.Enabled)
		require.Contains(t, []string{primary, alias}, *item.Rule.Project)
	}
	saved, err = rules.SaveRulesSelection(ctx, scope, primary, CollectionSelection{Kind: CollectionSelectionFrozenFilter})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`DELETE FROM project_consolidation_redirects WHERE selector = ?`, alias).Error)
	_, err = rules.ApplySelectionOperation(ctx, qualified, BehavioralRuleSelectionOperation{Action: BehavioralRuleSelectionDisable, SelectionKind: saved.Kind, SelectionVersion: saved.Version, SelectionToken: saved.Token, ProjectionFilter: &filter})
	require.ErrorIs(t, err, ErrCollectionSelectionReconfirmationRequired)
	_, err = rules.PageCollection(ctx, qualified, CollectionPageRequest{Domain: "rules", Filter: filter, Cursor: page.Cursor, Limit: 50})
	require.ErrorIs(t, err, ErrCollectionSelectionReconfirmationRequired)
}

func TestRulesAliasCursorCarriesServerFingerprint(t *testing.T) {
	scope := behavioralRuleSelectionScope("rules-cursor-fingerprint")
	filter := CollectionFilter{Value: "all", Fingerprint: collectionSelectionTestDigest("a")}
	qualified, err := RulesProjectionScope(scope, filter)
	require.NoError(t, err)
	require.NotEqual(t, scope.ContextFingerprint, qualified.ContextFingerprint)
	cursor, err := encodeBehavioralRuleCollectionCursor(filter, collectionSelectionTestDigest("b"), 0, 50)
	require.NoError(t, err)
	state, parsed, err := decodeBehavioralRuleCollectionCursor(cursor)
	require.NoError(t, err)
	require.Equal(t, filter.Fingerprint, state.Fingerprint)
	require.Equal(t, filter.Value, parsed.scope)
}
