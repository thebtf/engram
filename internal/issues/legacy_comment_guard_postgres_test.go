package issues_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/issues"
)

// AddComment and addIssueCommentTx are unchanged from v7.0.16
// (aa2a840e7d1741b3ce50f641b0c042424f42ff00). This calls the retained
// transaction owner, not copied SQL, an old binary, or an HTTP/login path.
func TestLegacyIssueCommentActorGuardPostgres(t *testing.T) {
	db := sharedDB(t)
	var migrated int64
	require.NoError(t, db.Table("migrations").Where("id = ?", "193_issue_internal_tasks").Count(&migrated).Error)
	require.EqualValues(t, 1, migrated, "exercise the retained writer after the real migration chain")

	store := gormdb.NewIssueStore(db)
	application := issues.New(store, nil)
	creator := auth.WithIdentity(t.Context(), auth.Client("read-write", "synthetic-legacy-comment-creator"))
	collaborator := auth.WithIdentity(t.Context(), auth.Client("read-write", "synthetic-legacy-comment-collaborator"))
	scope := issues.Scope{SourceProject: source}
	seed := func(t *testing.T) int64 {
		t.Helper()
		created, err := application.Create(creator, scope, intent(t.Name(), `{"retained":{"n":1}}`))
		require.NoError(t, err)
		require.Positive(t, created.ID)
		require.Equal(t, "1", created.Revision)
		retained, err := application.Mutate(creator, scope, created.ID, json.RawMessage(`{"comment":"Synthetic retained authenticated comment","source_agent":"synthetic-retained-attribution","comment_metadata":{"retained":[true,null,1]}}`))
		require.NoError(t, err)
		require.NotNil(t, retained.CommentID)
		require.Positive(t, *retained.CommentID)
		require.False(t, retained.Changed)
		require.Equal(t, "1", retained.Revision)
		updated := db.Exec(`UPDATE issues SET updated_at='2000-01-01T00:00:00Z' WHERE id=?`, created.ID)
		require.NoError(t, updated.Error)
		require.EqualValues(t, 1, updated.RowsAffected)
		var history, receipts int64
		require.NoError(t, db.Table("issue_changes").Where("issue_id = ?", created.ID).Count(&history).Error)
		require.NoError(t, db.Table("issue_create_receipts").Where("issue_id = ?", created.ID).Count(&receipts).Error)
		require.EqualValues(t, 1, history)
		require.EqualValues(t, 1, receipts, "the conservation check must cover a real retained receipt")
		return created.ID
	}
	snapshot := func(t *testing.T, id int64) map[string]string {
		t.Helper()
		result := make(map[string]string)
		for _, table := range []string{"issues", "issue_comments", "issue_changes", "issue_create_receipts"} {
			key := "issue_id"
			if table == "issues" {
				key = "id"
			}
			var value string
			require.NoError(t, db.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+table+` t WHERE `+key+`=?`, id).Row().Scan(&value))
			result[table] = value
		}
		return result
	}

	t.Run("legacy_store_append_without_actor_is_refused_atomically", func(t *testing.T) {
		id := seed(t)
		before := snapshot(t, id)
		beforeCounts := domainCounts(t, db)
		beforeRow, beforeComments, err := store.GetIssue(t.Context(), id)
		require.NoError(t, err)
		require.Len(t, beforeComments, 1)

		// Supply only the fields accepted by the old comment writer. The
		// authenticated context must not be mistaken for a persisted actor.
		// No rollback sentinel: an incorrectly accepted write really commits.
		commentID, appendErr := store.AddComment(collaborator, id, &gormdb.IssueComment{
			AuthorProject: source,
			AuthorAgent:   "synthetic-legacy-supplied-attribution",
			Body:          "Synthetic incompatible legacy append",
		})

		afterRow, afterComments, err := store.GetIssue(t.Context(), id)
		require.NoError(t, err)
		after := snapshot(t, id)
		afterCounts := domainCounts(t, db)
		t.Logf("actual retained AddComment: caller_id=%d caller_error=%v counts[issues,comments,history,receipts] before=%v after=%v updated_at_changed=%t", commentID, appendErr, beforeCounts, afterCounts, !afterRow.UpdatedAt.Equal(beforeRow.UpdatedAt))
		for _, comment := range afterComments {
			if comment.ID == commentID {
				t.Logf("actual persisted legacy comment: id=%d actor_kind=%q actor_reference_present=%t metadata=%s", comment.ID, comment.Actor.Kind, comment.Actor.Reference != nil, comment.Metadata)
			}
		}
		assert.Error(t, appendErr, "an incompatible legacy append lacking the authenticated actor must return refusal, never success")
		assert.Zero(t, commentID, "refusal must not expose a successful comment ID")
		assert.Equal(t, beforeRow, afterRow, "refusal must preserve every issue field, including updated_at")
		assert.Equal(t, beforeComments, afterComments, "refusal must retain all comments without appending an unknown-actor row")
		assert.Equal(t, beforeCounts, afterCounts)
		for _, table := range []string{"issues", "issue_comments", "issue_changes", "issue_create_receipts"} {
			assert.True(t, before[table] == after[table], "%s must retain its complete canonical row snapshot", table)
		}
	})

	t.Run("current_authenticated_comment_retains_metadata_actor_and_attribution_without_revision_history", func(t *testing.T) {
		id := seed(t)
		before := snapshot(t, id)
		beforeCounts := domainCounts(t, db)
		beforeRow, beforeComments, err := store.GetIssue(t.Context(), id)
		require.NoError(t, err)
		require.Len(t, beforeComments, 1)

		result, err := application.Mutate(collaborator, scope, id, json.RawMessage(`{"comment":"Synthetic current authenticated outcome","source_agent":"synthetic-current-supplied-attribution","comment_metadata":{"outcome":{"nested":[true,null,0.1],"kept":"synthetic"}}}`))
		require.NoError(t, err)
		assert.Equal(t, id, result.ID)
		assert.False(t, result.Changed, "comment-only append is not a material issue revision")
		assert.Equal(t, "1", result.Revision)
		require.NotNil(t, result.CommentID)
		require.Positive(t, *result.CommentID)

		afterRow, afterComments, err := store.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, beforeRow.Revision, afterRow.Revision)
		assert.True(t, afterRow.UpdatedAt.After(beforeRow.UpdatedAt), "the fixed fixture timestamp avoids a timing-dependent assertion")
		comparable := *afterRow
		comparable.UpdatedAt = beforeRow.UpdatedAt
		assert.Equal(t, *beforeRow, comparable, "only updated_at may change on the issue")
		require.Len(t, afterComments, len(beforeComments)+1)
		assert.Equal(t, beforeComments, afterComments[:len(beforeComments)])
		appended := afterComments[len(beforeComments)]
		assert.Equal(t, *result.CommentID, appended.ID, "the successful ID must identify the persisted comment")
		assert.Equal(t, id, appended.IssueID)
		assert.Equal(t, "Synthetic current authenticated outcome", appended.Body)
		assert.Equal(t, source, appended.AuthorProject)
		assert.Equal(t, "synthetic-current-supplied-attribution", appended.AuthorAgent)
		assert.JSONEq(t, `{"outcome":{"nested":[true,null,0.1],"kept":"synthetic"}}`, string(appended.Metadata))
		assert.Equal(t, "client", appended.Actor.Kind)
		require.NotNil(t, appended.Actor.Reference)
		assert.Equal(t, "client:synthetic-legacy-comment-collaborator", *appended.Actor.Reference, "actor comes from authentication, not supplied attribution or the issue creator")
		assert.Equal(t, []int64{beforeCounts[0], beforeCounts[1] + 1, beforeCounts[2], beforeCounts[3]}, domainCounts(t, db))
		after := snapshot(t, id)
		for _, table := range []string{"issue_changes", "issue_create_receipts"} {
			assert.True(t, before[table] == after[table], "%s must not change for a comment-only append", table)
		}
	})
}
