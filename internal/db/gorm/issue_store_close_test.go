package gorm

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	issues "github.com/thebtf/engram/internal/issues/contract"
	gormlib "gorm.io/gorm"
)

func TestIssueStorePersistsCreatorKeycardID(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := NewIssueStore(db)
	project := sessionTestProject(t, db)

	created, err := store.CreateTaskIssue(context.Background(), issueSelectionTestActor{keycard: "keycard-creator"}, issues.Scope{SourceProject: project}, issues.CreateCommand{Title: "credential owner persistence", TargetProject: project, Type: "bug", Priority: "medium", Labels: []string{}, Metadata: json.RawMessage(`{}`)})
	require.NoError(t, err)
	id := created.ID
	t.Cleanup(func() {
		db.Exec(`DELETE FROM issue_comments WHERE issue_id = ?`, id)
		db.Exec(`DELETE FROM issues WHERE id = ?`, id)
	})

	issue, _, err := store.GetIssue(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "keycard-creator", issue.CreatorKeycardID)
}

func TestCloseIssueWithCommentRollsBackWhenCommentInsertFails(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := NewIssueStore(db)
	project := sessionTestProject(t, db)
	actor := issueSelectionTestActor{keycard: "close-owner"}
	created, err := store.CreateTaskIssue(context.Background(), actor, issues.Scope{SourceProject: project}, issues.CreateCommand{Title: "close rollback", TargetProject: project, Type: "bug", Priority: "medium", Labels: []string{}, Metadata: json.RawMessage(`{}`)})
	require.NoError(t, err)
	id := created.ID
	status, revision := "resolved", created.Revision
	resolved, err := store.MutateTaskIssue(context.Background(), actor, issues.Scope{SourceProject: project}, id, issues.MutationCommand{ExpectedRevision: &revision, Status: &status})
	require.NoError(t, err)
	triggerName := fmt.Sprintf("engram_test_issue_comment_fail_%d", id)
	t.Cleanup(func() {
		db.Exec("DROP TRIGGER IF EXISTS " + triggerName + " ON issue_comments")
		db.Exec("DROP FUNCTION IF EXISTS " + triggerName + "()")
		db.Exec(`DELETE FROM issue_comments WHERE issue_id = ?`, id)
		db.Exec(`DELETE FROM issues WHERE id = ?`, id)
	})
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.issue_id = %d THEN RAISE EXCEPTION 'injected issue comment failure'; END IF; RETURN NEW; END; $$`, triggerName, id)).Error)
	require.NoError(t, db.Exec("CREATE TRIGGER "+triggerName+" BEFORE INSERT ON issue_comments FOR EACH ROW EXECUTE FUNCTION "+triggerName+"()").Error)
	attempted := false
	callbackName := triggerName + "_probe"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gormlib.DB) {
		if tx.Statement.Table == "issue_comments" {
			attempted = true
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	status, revision = "closed", resolved.Revision
	comment := "must roll back"
	_, err = store.MutateTaskIssue(context.Background(), actor, issues.Scope{SourceProject: project}, id, issues.MutationCommand{ExpectedRevision: &revision, Status: &status, Comment: &comment, CommentMetadata: json.RawMessage(`{}`)})
	require.Error(t, err)
	require.True(t, attempted, "the injected comment insert failure must be reached")
	issue, comments, err := store.GetIssue(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "resolved", issue.Status)
	require.Nil(t, issue.ClosedAt)
	require.Empty(t, comments)
}

func TestAcknowledgeIssuesAtomicallyTypesInvalidInput(t *testing.T) {
	store := NewIssueStore(nil)
	for _, ids := range [][]int64{nil, {0}, {1, 1}} {
		_, err := store.AcknowledgeIssuesAtomically(context.Background(), ids, nil, IssueChangeAttribution{})
		require.ErrorIs(t, err, ErrIssueInvalidInput)
	}
}
