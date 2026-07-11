package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIssueStore_ListIssuesExTreatsProjectSelectorsAsLiteralIdentities(t *testing.T) {
	db := openCandidateTestDB(t)
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
