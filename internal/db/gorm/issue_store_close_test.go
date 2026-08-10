package gorm

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIssueStorePersistsCreatorKeycardID(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := NewIssueStore(db)

	id, err := store.CreateIssue(context.Background(), &Issue{
		Title: "credential owner persistence", SourceProject: "test", TargetProject: "test",
		Type: "bug", Priority: "medium", CreatorKeycardID: "keycard-creator",
	})
	require.NoError(t, err)
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
	id, err := store.CreateIssue(context.Background(), &Issue{
		Title: "close rollback", SourceProject: "test", TargetProject: "test",
		Status: "resolved", Type: "bug", Priority: "medium",
	})
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

	err = store.CloseIssueWithComment(context.Background(), id, false, "must roll back", "test", "agent")
	require.Error(t, err)
	issue, comments, err := store.GetIssue(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "resolved", issue.Status)
	require.Nil(t, issue.ClosedAt)
	require.Empty(t, comments)
}

func TestAcknowledgeIssuesAtomicallyTypesInvalidInput(t *testing.T) {
	store := NewIssueStore(nil)
	for _, ids := range [][]int64{nil, {0}, {1, 1}} {
		_, err := store.AcknowledgeIssuesAtomically(context.Background(), ids)
		require.ErrorIs(t, err, ErrIssueInvalidInput)
	}
}
