package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

func TestIssueStoreApplySelectionOperationPostgres(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewIssueStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("issue-selection-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		require.NoError(t, db.Where("source_project = ?", project).Delete(&Issue{}).Error)
	})

	ids := make([]int64, 101)
	for index := range ids {
		id, err := store.CreateIssue(ctx, &Issue{
			Title:            fmt.Sprintf("selection fixture %03d", index),
			Body:             "must not appear in selection results",
			SourceProject:    project,
			TargetProject:    project,
			CreatorKeycardID: "issue-selection-owner",
			Type:             "task",
			Labels:           models.JSONStringArray{"selection"},
		})
		require.NoError(t, err)
		ids[index] = id
	}

	rows := issueSelectionTestRows(t, db, ids)
	priority := "high"
	updated, err := store.ApplyIssueSelectionOperation(ctx, IssueSelectionOperation{
		Action:  IssueSelectionAction{Kind: IssueSelectionPriority, Priority: &priority},
		Actor:   IssueSelectionActor{KeycardID: "issue-selection-owner"},
		Targets: issueSelectionTargets(rows...),
	})
	require.NoError(t, err)
	require.Len(t, updated.Items, len(rows), "the operation must not inherit the legacy 100-row limit")
	for _, item := range updated.Items {
		require.Equal(t, IssueSelectionCommitted, item.Outcome)
		require.NotNil(t, item.Readback)
		require.Equal(t, priority, item.Readback.Priority)
		current, _, getErr := store.GetIssue(ctx, item.TargetID)
		require.NoError(t, getErr)
		require.True(t, current.UpdatedAt.Equal(item.Readback.UpdatedAt), "result must return the exact persisted updated_at")
	}

	rows = issueSelectionTestRows(t, db, ids)
	beforeUntouched := rows[1]
	stale := rows[0]
	require.NoError(t, db.Model(&Issue{}).Where("id = ?", stale.ID).Update("updated_at", time.Now().UTC().Add(time.Second)).Error)
	clearLabels := []string{}
	staleResult, err := store.ApplyIssueSelectionOperation(ctx, IssueSelectionOperation{
		Action:  IssueSelectionAction{Kind: IssueSelectionLabels, Labels: &clearLabels},
		Actor:   IssueSelectionActor{KeycardID: "issue-selection-owner"},
		Targets: issueSelectionTargets(stale, beforeUntouched),
	})
	require.ErrorIs(t, err, ErrIssueSelectionConflict)
	require.Equal(t, []IssueSelectionOperationItem{{TargetID: stale.ID, Outcome: IssueSelectionConflict}}, staleResult.Items)
	unchanged, _, getErr := store.GetIssue(ctx, beforeUntouched.ID)
	require.NoError(t, getErr)
	require.Equal(t, beforeUntouched.Labels, unchanged.Labels)
	require.True(t, beforeUntouched.UpdatedAt.Equal(unchanged.UpdatedAt), "a stale target must abort the whole operation before any mutation")

	rows = issueSelectionTestRows(t, db, ids)
	permissionResult, err := store.ApplyIssueSelectionOperation(ctx, IssueSelectionOperation{
		Action:  IssueSelectionAction{Kind: IssueSelectionPriority, Priority: &priority},
		Actor:   IssueSelectionActor{KeycardID: "other-keycard"},
		Targets: issueSelectionTargets(rows[2], rows[3]),
	})
	require.ErrorIs(t, err, ErrIssueForbidden)
	require.Equal(t, []IssueSelectionOperationItem{{TargetID: rows[2].ID, Outcome: IssueSelectionDenied}}, permissionResult.Items)
	require.Nil(t, permissionResult.Items[0].Readback, "access loss must not expose the current issue row")
	unchanged, _, getErr = store.GetIssue(ctx, rows[3].ID)
	require.NoError(t, getErr)
	require.Equal(t, priority, unchanged.Priority, "a permission conflict must leave all selected rows unchanged")

	resolved := rows[4]
	require.NoError(t, store.UpdateIssueStatus(ctx, resolved.ID, "resolved"))
	resolvedCurrent, _, getErr := store.GetIssue(ctx, resolved.ID)
	require.NoError(t, getErr)
	resolved = *resolvedCurrent
	open := rows[5]
	acknowledgeResult, err := store.ApplyIssueSelectionOperation(ctx, IssueSelectionOperation{
		Action:  IssueSelectionAction{Kind: IssueSelectionAcknowledge},
		Actor:   IssueSelectionActor{IsOperator: true},
		Targets: issueSelectionTargets(resolved, open),
	})
	require.ErrorIs(t, err, ErrIssueSelectionConflict)
	require.Equal(t, []IssueSelectionOperationItem{{TargetID: resolved.ID, Outcome: IssueSelectionConflict}}, acknowledgeResult.Items)
	unchanged, _, getErr = store.GetIssue(ctx, open.ID)
	require.NoError(t, getErr)
	require.Equal(t, "open", unchanged.Status, "a status conflict must leave all selected rows unchanged")

	openCurrent, _, getErr := store.GetIssue(ctx, open.ID)
	require.NoError(t, getErr)
	open = *openCurrent
	acknowledged, err := store.ApplyIssueSelectionOperation(ctx, IssueSelectionOperation{
		Action:  IssueSelectionAction{Kind: IssueSelectionAcknowledge},
		Actor:   IssueSelectionActor{IsOperator: true},
		Targets: issueSelectionTargets(open),
	})
	require.NoError(t, err)
	require.Equal(t, "acknowledged", acknowledged.Items[0].Readback.Status)

	labelTarget := issueSelectionTestRows(t, db, []int64{ids[8]})[0]
	labels := []string{"current", "selected"}
	labeled, err := store.ApplyIssueSelectionOperation(ctx, IssueSelectionOperation{
		Action:  IssueSelectionAction{Kind: IssueSelectionLabels, Labels: &labels},
		Actor:   IssueSelectionActor{KeycardID: "issue-selection-owner"},
		Targets: issueSelectionTargets(labelTarget),
	})
	require.NoError(t, err)
	require.Equal(t, labels, labeled.Items[0].Readback.Labels)

	statusTarget := issueSelectionTestRows(t, db, []int64{ids[9]})[0]
	status := "resolved"
	resolvedResult, err := store.ApplyIssueSelectionOperation(ctx, IssueSelectionOperation{
		Action:  IssueSelectionAction{Kind: IssueSelectionStatus, Status: &status},
		Actor:   IssueSelectionActor{KeycardID: "issue-selection-owner"},
		Targets: issueSelectionTargets(statusTarget),
	})
	require.NoError(t, err)
	require.Equal(t, status, resolvedResult.Items[0].Readback.Status)

	deleteOne, deleteTwo := issueSelectionTestRows(t, db, []int64{ids[6], ids[7]})[0], issueSelectionTestRows(t, db, []int64{ids[6], ids[7]})[1]
	deleted, err := store.ApplyIssueSelectionOperation(ctx, IssueSelectionOperation{
		Action:  IssueSelectionAction{Kind: IssueSelectionDelete},
		Actor:   IssueSelectionActor{IsOperator: true},
		Targets: issueSelectionTargets(deleteOne, deleteTwo),
	})
	require.NoError(t, err)
	require.Len(t, deleted.Items, 2)
	for _, item := range deleted.Items {
		require.Equal(t, IssueSelectionCommitted, item.Outcome)
		require.True(t, item.Deleted)
		require.Nil(t, item.Readback, "delete result must not disclose the deleted row")
		var count int64
		require.NoError(t, db.Model(&Issue{}).Where("id = ?", item.TargetID).Count(&count).Error)
		require.Zero(t, count)
	}
}

func issueSelectionTestRows(t *testing.T, db *gorm.DB, ids []int64) []Issue {
	t.Helper()
	rows := make([]Issue, 0, len(ids))
	require.NoError(t, db.Where("id IN ?", ids).Order("id ASC").Find(&rows).Error)
	require.Len(t, rows, len(ids))
	return rows
}

func issueSelectionTargets(rows ...Issue) []IssueSelectionTarget {
	targets := make([]IssueSelectionTarget, len(rows))
	for index, row := range rows {
		targets[index] = IssueSelectionTarget{
			ID:                row.ID,
			ExpectedUpdatedAt: row.UpdatedAt.UTC().Truncate(time.Microsecond),
		}
	}
	return targets
}
