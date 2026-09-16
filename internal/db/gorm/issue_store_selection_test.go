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
	labeledCurrent, _, getErr := store.GetIssue(ctx, labelTarget.ID)
	require.NoError(t, getErr)
	require.Equal(t, models.JSONStringArray(labels), labeledCurrent.Labels)
	clearedLabels := []string{}
	cleared, err := store.ApplyIssueSelectionOperation(ctx, IssueSelectionOperation{
		Action:  IssueSelectionAction{Kind: IssueSelectionLabels, Labels: &clearedLabels},
		Actor:   IssueSelectionActor{KeycardID: "issue-selection-owner"},
		Targets: issueSelectionTargets(*labeledCurrent),
	})
	require.NoError(t, err)
	require.Empty(t, cleared.Items[0].Readback.Labels)
	clearedCurrent, _, getErr := store.GetIssue(ctx, labelTarget.ID)
	require.NoError(t, getErr)
	require.Empty(t, clearedCurrent.Labels)

	directLabelTarget := issueSelectionTestRows(t, db, []int64{ids[10]})[0]
	require.NoError(t, store.UpdateIssueFields(ctx, directLabelTarget.ID, "", "", "", "", []string{}))
	directCleared, _, getErr := store.GetIssue(ctx, directLabelTarget.ID)
	require.NoError(t, getErr)
	require.Equal(t, models.JSONStringArray{}, directCleared.Labels)

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

func TestIssueSelectionValidationContracts(t *testing.T) {
	stringPtr := func(value string) *string { return &value }
	labelsPtr := func(values ...string) *[]string { return &values }
	validOperation := func(action IssueSelectionAction) IssueSelectionOperation {
		return IssueSelectionOperation{
			Action: action,
			Targets: []IssueSelectionTarget{{
				ID:                1,
				ExpectedUpdatedAt: time.Date(2026, time.January, 2, 3, 4, 5, 987654321, time.UTC),
			}},
		}
	}

	for _, store := range []*IssueStore{nil, NewIssueStore(nil)} {
		_, err := store.ApplyIssueSelectionOperation(context.Background(), IssueSelectionOperation{})
		require.EqualError(t, err, "issue store is not configured")
	}

	store := NewIssueStore(&gorm.DB{})
	_, err := store.ApplyIssueSelectionOperation(context.Background(), IssueSelectionOperation{})
	require.ErrorIs(t, err, ErrIssueInvalidInput)
	_, err = store.ApplyIssueSelectionOperation(context.Background(), validOperation(IssueSelectionAction{Kind: "unknown"}))
	require.ErrorIs(t, err, ErrIssueInvalidInput)

	targets, err := validateIssueSelectionOperation(validOperation(IssueSelectionAction{Kind: IssueSelectionAcknowledge}))
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, time.January, 2, 3, 4, 5, 987654000, time.UTC), targets[0].expectedUpdatedAt)

	overflow := make([]IssueSelectionTarget, CollectionSelectionMaxTargets+1)
	for index := range overflow {
		overflow[index] = IssueSelectionTarget{ID: int64(index + 1), ExpectedUpdatedAt: time.Now()}
	}
	for _, operation := range []IssueSelectionOperation{
		{Action: IssueSelectionAction{Kind: IssueSelectionAcknowledge}},
		{Action: IssueSelectionAction{Kind: IssueSelectionAcknowledge}, Targets: overflow},
		{Action: IssueSelectionAction{Kind: IssueSelectionAcknowledge}, Targets: []IssueSelectionTarget{{ExpectedUpdatedAt: time.Now()}}},
		{Action: IssueSelectionAction{Kind: IssueSelectionAcknowledge}, Targets: []IssueSelectionTarget{{ID: 1}}},
		{Action: IssueSelectionAction{Kind: IssueSelectionAcknowledge}, Targets: []IssueSelectionTarget{{ID: 1, ExpectedUpdatedAt: time.Now()}, {ID: 1, ExpectedUpdatedAt: time.Now()}}},
	} {
		_, err := validateIssueSelectionOperation(operation)
		require.ErrorIs(t, err, ErrIssueInvalidInput)
	}

	for _, kind := range []IssueSelectionActionKind{IssueSelectionAcknowledge, IssueSelectionDelete} {
		require.NoError(t, validateIssueSelectionAction(IssueSelectionAction{Kind: kind}))
	}
	for _, status := range []string{"open", "acknowledged", "resolved", "reopened", "closed", "rejected"} {
		action := IssueSelectionAction{Kind: IssueSelectionStatus, Status: stringPtr(status)}
		if status == "rejected" {
			action.Comment = stringPtr("required")
		}
		require.NoError(t, validateIssueSelectionAction(action))
	}
	for _, priority := range []string{"critical", "high", "medium", "low"} {
		require.NoError(t, validateIssueSelectionAction(IssueSelectionAction{Kind: IssueSelectionPriority, Priority: stringPtr(priority)}))
	}
	for _, labels := range []*[]string{labelsPtr(), labelsPtr("selected")} {
		require.NoError(t, validateIssueSelectionAction(IssueSelectionAction{Kind: IssueSelectionLabels, Labels: labels}))
	}

	invalidActions := []IssueSelectionAction{
		{Kind: "unknown"},
		{Kind: IssueSelectionAcknowledge, Status: stringPtr("open")},
		{Kind: IssueSelectionDelete, Comment: stringPtr("payload")},
		{Kind: IssueSelectionStatus},
		{Kind: IssueSelectionStatus, Status: stringPtr("open"), Priority: stringPtr("high")},
		{Kind: IssueSelectionStatus, Status: stringPtr("invalid")},
		{Kind: IssueSelectionStatus, Status: stringPtr("rejected")},
		{Kind: IssueSelectionPriority},
		{Kind: IssueSelectionPriority, Priority: stringPtr("high"), Comment: stringPtr("payload")},
		{Kind: IssueSelectionPriority, Priority: stringPtr("invalid")},
		{Kind: IssueSelectionLabels},
		{Kind: IssueSelectionLabels, Labels: labelsPtr("selected"), AuthorProject: "payload"},
	}
	for _, action := range invalidActions {
		require.ErrorIs(t, validateIssueSelectionAction(action), ErrIssueInvalidInput)
	}
}

func TestIssueSelectionAuthorizationAndTransitions(t *testing.T) {
	stringPtr := func(value string) *string { return &value }
	labels := []string{"selected"}
	row := &Issue{CreatorKeycardID: "creator"}
	operator := IssueSelectionActor{IsOperator: true}
	owner := IssueSelectionActor{KeycardID: "creator"}

	for _, action := range []IssueSelectionAction{
		{Kind: IssueSelectionAcknowledge},
		{Kind: IssueSelectionDelete},
		{Kind: IssueSelectionStatus, Status: stringPtr("open")},
		{Kind: IssueSelectionPriority, Priority: stringPtr("high")},
		{Kind: IssueSelectionLabels, Labels: &labels},
	} {
		require.NoError(t, authorizeIssueSelectionRow(row, action, operator))
	}

	require.ErrorIs(t, authorizeIssueSelectionRow(row, IssueSelectionAction{Kind: IssueSelectionPriority, Priority: stringPtr("high")}, IssueSelectionActor{}), ErrIssueForbidden)
	for _, action := range []IssueSelectionAction{
		{Kind: IssueSelectionPriority, Priority: stringPtr("high")},
		{Kind: IssueSelectionLabels, Labels: &labels},
		{Kind: IssueSelectionStatus, Status: stringPtr("reopened")},
		{Kind: IssueSelectionStatus, Status: stringPtr("closed")},
	} {
		require.ErrorIs(t, authorizeIssueSelectionRow(row, action, IssueSelectionActor{KeycardID: "other"}), ErrIssueForbidden)
	}
	require.ErrorIs(t, authorizeIssueSelectionRow(&Issue{}, IssueSelectionAction{Kind: IssueSelectionPriority, Priority: stringPtr("high")}, owner), ErrIssueForbidden)
	for _, action := range []IssueSelectionAction{
		{Kind: IssueSelectionAcknowledge},
		{Kind: IssueSelectionDelete},
		{Kind: IssueSelectionStatus, Status: stringPtr("open")},
		{Kind: IssueSelectionStatus, Status: stringPtr("acknowledged")},
		{Kind: IssueSelectionStatus, Status: stringPtr("rejected")},
	} {
		require.ErrorIs(t, authorizeIssueSelectionRow(row, action, owner), ErrIssueForbidden)
	}
	require.NoError(t, authorizeIssueSelectionRow(row, IssueSelectionAction{Kind: IssueSelectionStatus, Status: stringPtr("resolved")}, IssueSelectionActor{KeycardID: "other"}))

	for _, scenario := range []struct {
		name     string
		status   string
		action   IssueSelectionAction
		actor    IssueSelectionActor
		conflict bool
	}{
		{"acknowledge open", "open", IssueSelectionAction{Kind: IssueSelectionAcknowledge}, operator, false},
		{"acknowledge resolved", "resolved", IssueSelectionAction{Kind: IssueSelectionAcknowledge}, operator, true},
		{"reopen resolved", "resolved", IssueSelectionAction{Kind: IssueSelectionStatus, Status: stringPtr("reopened")}, owner, false},
		{"reopen open", "open", IssueSelectionAction{Kind: IssueSelectionStatus, Status: stringPtr("reopened")}, owner, true},
		{"operator closes open", "open", IssueSelectionAction{Kind: IssueSelectionStatus, Status: stringPtr("closed")}, operator, false},
		{"owner closes resolved", "resolved", IssueSelectionAction{Kind: IssueSelectionStatus, Status: stringPtr("closed")}, owner, false},
		{"owner closes reopened", "reopened", IssueSelectionAction{Kind: IssueSelectionStatus, Status: stringPtr("closed")}, owner, false},
		{"owner closes open", "open", IssueSelectionAction{Kind: IssueSelectionStatus, Status: stringPtr("closed")}, owner, true},
		{"close already closed", "closed", IssueSelectionAction{Kind: IssueSelectionStatus, Status: stringPtr("closed")}, operator, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			err := validateIssueSelectionCurrentRow(&Issue{Status: scenario.status}, scenario.action, scenario.actor)
			if scenario.conflict {
				require.ErrorIs(t, err, ErrIssueSelectionConflict)
				return
			}
			require.NoError(t, err)
		})
	}

	for _, scenario := range []struct {
		err     error
		outcome IssueSelectionOutcome
	}{
		{ErrIssueForbidden, IssueSelectionDenied},
		{ErrIssueNotFound, IssueSelectionConflict},
		{ErrIssueInvalidTransition, IssueSelectionConflict},
		{ErrIssueSelectionConflict, IssueSelectionConflict},
		{fmt.Errorf("database failure"), IssueSelectionFailed},
	} {
		require.Equal(t, scenario.outcome, issueSelectionFailureOutcome(scenario.err))
	}
}
