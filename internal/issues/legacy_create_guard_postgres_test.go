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

// v7.0.16 (aa2a840e7d1741b3ce50f641b0c042424f42ff00) handleCreateIssue
// calls IssueStore.CreateIssue. Its function body is identical to the retained
// current method (LF SHA-256 8315f6814525c91372e5a91a76e9e8bc04b0a994635fb1354a8a7fd13793043c).
// This exercises that actual store method, not the old binary or HTTP POST.
func TestLegacyIssueCreateHistoryGuardPostgres(t *testing.T) {
	db := sharedDB(t)

	store := gormdb.NewIssueStore(db)
	application := issues.New(store, nil)
	ctx := auth.WithIdentity(t.Context(), auth.Client("read-write", "synthetic-legacy-create-control"))
	scope := issues.Scope{SourceProject: source}

	assertCreated := func(t *testing.T, result issues.Result, title string) {
		t.Helper()
		require.Positive(t, result.ID)
		assert.Equal(t, "1", result.Revision)
		row, comments, err := store.GetIssue(ctx, result.ID)
		require.NoError(t, err)
		assert.Equal(t, title, row.Title)
		assert.Equal(t, source, row.SourceProject)
		assert.Equal(t, source, row.TargetProject)
		assert.EqualValues(t, 1, row.Revision)
		assert.Equal(t, "synthetic-legacy-create-control", row.CreatorKeycardID)
		assert.Empty(t, comments)
		var history []gormdb.IssueChange
		require.NoError(t, db.Where("issue_id = ?", result.ID).Order("revision").Find(&history).Error)
		require.Len(t, history, 1, "each successful create must commit exactly one initial history row")
		assert.Equal(t, result.ID, history[0].IssueID)
		assert.EqualValues(t, 1, history[0].Revision)
		assert.Nil(t, history[0].PreviousRevision)
		assert.Equal(t, "created", history[0].Action)
		assert.Empty(t, history[0].ChangedFields)
		assert.JSONEq(t, `{}`, string(history[0].Changes))
		assert.Equal(t, "client", history[0].Actor.Kind)
		require.NotNil(t, history[0].Actor.Reference)
		assert.Equal(t, "client:synthetic-legacy-create-control", *history[0].Actor.Reference)
		require.NotNil(t, history[0].Attribution.SourceProject)
		assert.Equal(t, source, *history[0].Attribution.SourceProject)
	}

	t.Run("legacy_store_create_without_history_is_refused_atomically", func(t *testing.T) {
		before := domainCounts(t, db)
		const title = "Synthetic legacy create missing revision history"
		id, err := store.CreateIssue(ctx, &gormdb.Issue{
			Title:            title,
			Body:             "Synthetic old-writer payload",
			SourceProject:    source,
			TargetProject:    source,
			SourceAgent:      "synthetic-legacy-agent",
			CreatedBySession: "synthetic-legacy-session",
			CreatorKeycardID: "synthetic-legacy-create-control",
		})
		after := domainCounts(t, db)
		var persisted []gormdb.Issue
		require.NoError(t, db.Where("title = ?", title).Find(&persisted).Error)
		t.Logf("actual retained CreateIssue: caller_id=%d caller_error=%v counts[issues,comments,history,receipts] before=%v after=%v matching_issues=%d", id, err, before, after, len(persisted))
		if len(persisted) == 1 {
			var linkedHistory int64
			require.NoError(t, db.Model(&gormdb.IssueChange{}).Where("issue_id = ?", persisted[0].ID).Count(&linkedHistory).Error)
			t.Logf("actual persisted legacy issue: id=%d revision=%d linked_history=%d", persisted[0].ID, persisted[0].Revision, linkedHistory)
		}
		assert.Error(t, err, "an incompatible legacy create lacking revision-linked history must return refusal, never success")
		assert.Zero(t, id, "refused legacy create must not expose a successful issue ID")
		assert.Empty(t, persisted, "refused legacy create must not leave an issue row")
		assert.Equal(t, before, after, "refusal must atomically leave issue/comment/history/receipt counts unchanged")
	})

	t.Run("authenticated_current_unkeyed_create_keeps_history_and_single_shot_semantics", func(t *testing.T) {
		before := domainCounts(t, db)
		const title = "Synthetic current unkeyed create with history"
		raw := json.RawMessage(`{"title":"Synthetic current unkeyed create with history"}`)
		first, err := application.Create(ctx, scope, raw)
		require.NoError(t, err)
		assert.False(t, first.Replayed)
		assertCreated(t, first, title)
		assert.Equal(t, []int64{before[0] + 1, before[1], before[2] + 1, before[3]}, domainCounts(t, db))
		second, err := application.Create(ctx, scope, raw)
		require.NoError(t, err)
		assert.False(t, second.Replayed)
		assert.NotEqual(t, first.ID, second.ID, "omitting request_key is ordinary single-shot create, not idempotent replay")
		assertCreated(t, second, title)
		assert.Equal(t, []int64{before[0] + 2, before[1], before[2] + 2, before[3]}, domainCounts(t, db), "unkeyed creates must not acquire receipts")
	})

	t.Run("authenticated_current_keyed_create_replays_without_duplicate_history", func(t *testing.T) {
		before := domainCounts(t, db)
		const title = "Synthetic current keyed create control"
		raw := json.RawMessage(`{"title":"Synthetic current keyed create control","request_key":"synthetic-legacy-create-keyed-control"}`)
		first, err := application.Create(ctx, scope, raw)
		require.NoError(t, err)
		assert.False(t, first.Replayed)
		assertCreated(t, first, title)
		after := domainCounts(t, db)
		assert.Equal(t, []int64{before[0] + 1, before[1], before[2] + 1, before[3] + 1}, after)
		var receipts int64
		require.NoError(t, db.Table("issue_create_receipts").Where("issue_id = ?", first.ID).Count(&receipts).Error)
		assert.EqualValues(t, 1, receipts)
		replayed, err := application.Create(ctx, scope, raw)
		require.NoError(t, err)
		assert.True(t, replayed.Replayed)
		assert.Equal(t, first.ID, replayed.ID)
		assert.Equal(t, first.Revision, replayed.Revision)
		assertCreated(t, replayed, title)
		assert.Equal(t, after, domainCounts(t, db), "keyed replay must not append another issue, history row or receipt")
	})
}
