package gorm

import (
	"context"
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
