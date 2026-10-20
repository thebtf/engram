package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestIssueStore_ListIssuesExTreatsProjectSelectorsAsLiteralIdentities(t *testing.T) {
	db := openCandidateTestDB(t).Begin()
	require.NoError(t, db.Error)
	t.Cleanup(func() { require.NoError(t, db.Rollback().Error) })
	var schema string
	require.NoError(t, db.Raw(`SELECT current_schema()`).Scan(&schema).Error)
	// The reader consumes a historical pre-186 snapshot, not new legacy writes to the guarded base table.
	require.NoError(t, db.Exec(`CREATE TEMPORARY TABLE issues (LIKE `+pq.QuoteIdentifier(schema)+`.issues INCLUDING DEFAULTS INCLUDING GENERATED) ON COMMIT DROP`).Error)
	var guarded bool
	require.NoError(t, db.Raw(`SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = ?::regclass AND tgname = 'engram_consolidation_scope_write' AND tgenabled <> 'D')`, pq.QuoteIdentifier(schema)+`.issues`).Scan(&guarded).Error)
	require.True(t, guarded, "the current base table must retain its canonical membership fence")
	store := NewIssueStore(db)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	bare := "literal_project%" + suffix
	canonical := bare + "_a1b2c3"
	sibling := bare + "-other"

	ids := make([]int64, 0, 4)
	for _, issue := range []*Issue{
		{Title: "target canonical", TargetProject: canonical, SourceProject: "source", Type: "task"},
		{Title: "target sibling", TargetProject: sibling, SourceProject: "source", Type: "task"},
		{Title: "source canonical", TargetProject: "target", SourceProject: canonical, Type: "task"},
		{Title: "source sibling", TargetProject: "target", SourceProject: sibling, Type: "task"},
	} {
		id, err := store.CreateIssue(ctx, issue)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	t.Cleanup(func() { _ = db.Exec("DELETE FROM issues WHERE id IN ?", ids).Error })

	targetRows, targetTotal, err := store.ListIssuesEx(ctx, IssueListParams{TargetProject: canonical, Limit: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, targetTotal)
	require.Len(t, targetRows, 1)
	require.Equal(t, canonical, targetRows[0].TargetProject)

	sourceRows, sourceTotal, err := store.ListIssuesEx(ctx, IssueListParams{SourceProject: canonical, Limit: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, sourceTotal)
	require.Len(t, sourceRows, 1)
	require.Equal(t, canonical, sourceRows[0].SourceProject)
}

func TestIssueStoreProjectRolesBeyondPostgresBindLimit(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	ctx := context.Background()
	a, b, removed := "71000000-0000-4000-8000-000000000001", "71000000-0000-4000-8000-000000000002", "71000000-0000-4000-8000-000000000003"
	now := time.Now().UTC()
	seed := db.Begin()
	require.NoError(t, seed.Error)
	defer seed.Rollback()
	// Seed historical role evidence without changing the current write guards.
	require.NoError(t, seed.Exec("SET LOCAL session_replication_role=replica").Error)
	for _, project := range []Project{{ID: a}, {ID: b}, {ID: removed, RemovedAt: &now}} {
		require.NoError(t, seed.Create(&project).Error)
	}
	for _, id := range []int64{1, 2, 65535, 65536, 69999, 70000} {
		source := a
		if id == 69999 {
			source = removed
		}
		require.NoError(t, seed.Create(&Issue{ID: id, Title: "role boundary fixture", SourceProject: source, TargetProject: b}).Error)
	}
	for index, membership := range []struct {
		id      int64
		field   string
		project *string
	}{
		{2, "source_project", &b},
		{2, "target_project", &a},
		{65535, "source_project", nil},
		{65536, "source_project", &a},
		{65536, "source_project", &b},
		{65536, "target_project", &a},
		{70000, "target_project", nil},
	} {
		require.NoError(t, seed.Create(&ConsolidationMembership{
			Reference: fmt.Sprintf("urn:engram:fixture:issue-role-boundary:%d", index), Table: "issues",
			RowKey: JSONRaw(fmt.Sprintf(`{"id":%d}`, membership.id)), FieldPath: JSONRaw(fmt.Sprintf(`["%s"]`, membership.field)),
			ProjectID: membership.project, PlanFingerprint: "synthetic-issue-role-boundary",
		}).Error)
	}
	require.NoError(t, seed.Commit().Error)
	store := NewIssueStore(db)
	want := map[int64]IssueProjectRoles{
		1:     {ID: 1, SourceProjectID: &a, TargetProjectID: &b},
		2:     {ID: 2, SourceProjectID: &b, TargetProjectID: &a},
		65535: {ID: 65535, TargetProjectID: &b},
		65536: {ID: 65536, TargetProjectID: &a},
		69999: {ID: 69999, TargetProjectID: &b},
		70000: {ID: 70000, SourceProjectID: &a},
	}
	for _, count := range []int{65536, 70000} {
		t.Run(fmt.Sprintf("%d_requested_IDs", count), func(t *testing.T) {
			ids := make([]int64, count)
			for index := range ids {
				ids[index] = int64(index + 1)
			}
			ids[2] = 1
			roles, err := store.ProjectRoles(ctx, ids)
			require.NoError(t, err)
			expected := make(map[int64]IssueProjectRoles)
			for id, role := range want {
				if id <= int64(count) {
					expected[id] = role
				}
			}
			require.Equal(t, expected, roles, "include the selected tail; duplicate/missing IDs cannot invent rows or canonical roles")
		})
	}
	for _, ids := range [][]int64{nil, {}, {3, 70001}} {
		roles, err := store.ProjectRoles(ctx, ids)
		require.NoError(t, err)
		require.Empty(t, roles)
	}
}
