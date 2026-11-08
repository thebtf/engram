package issues_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/issues"
	gormlib "gorm.io/gorm"
)

func TestSharedIssueReadPagesPostgres(t *testing.T) {
	db := sharedDB(t)
	store := gormdb.NewIssueStore(db)
	application := issues.New(store, nil)
	ctx := auth.WithIdentity(t.Context(), auth.Admin())
	largeAgent := strings.Repeat("h", gormdb.IssueTaskPageBytes+257)
	encoded, err := json.Marshal(map[string]any{"title": "Synthetic history singleton", "source_agent": largeAgent})
	require.NoError(t, err)
	card, err := application.Create(ctx, issues.Scope{SourceProject: source}, encoded)
	require.NoError(t, err)
	_, err = application.Mutate(ctx, issues.Scope{SourceProject: source}, card.ID, json.RawMessage(`{"expected_revision":"1","body":"Synthetic history tail"}`))
	require.NoError(t, err)
	for range 3 {
		raw, marshalErr := json.Marshal(map[string]any{"comment": strings.Repeat("c", 1536*1024)})
		require.NoError(t, marshalErr)
		_, err = application.Mutate(ctx, issues.Scope{SourceProject: source}, card.ID, raw)
		require.NoError(t, err)
	}

	t.Run("history_intact_oversized_singleton_and_independent_continuation", func(t *testing.T) {
		params := gormdb.IssueTaskDetailParams{Paged: true, CommentsLimit: 200, HistoryLimit: 200}
		page, err := store.ReadTaskIssue(ctx, card.ID, params)
		require.NoError(t, err)
		require.Len(t, page.History, 1)
		require.NotNil(t, page.History[0].Attribution.SourceAgent)
		assert.True(t, *page.History[0].Attribution.SourceAgent == largeAgent, "historical attribution must stay byte-for-byte intact")
		assert.True(t, page.HistoryOversized)
		assert.False(t, page.CommentsOversized)
		require.NotNil(t, page.HistoryNextOffset)
		assert.Equal(t, 1, *page.HistoryNextOffset)
		require.NotNil(t, page.CommentsNextOffset)
		assert.Equal(t, 2, *page.CommentsNextOffset)
		params.HistoryOffset, params.CommentsOffset = 1, 2
		tail, err := store.ReadTaskIssue(ctx, card.ID, params)
		require.NoError(t, err)
		require.Len(t, tail.History, 1)
		assert.EqualValues(t, 2, tail.History[0].Revision)
		assert.True(t, tail.HistoryComplete)
		assert.False(t, tail.HistoryOversized)
		require.Len(t, tail.Comments, 1)
		assert.True(t, tail.CommentsComplete)
	})

	t.Run("collection_reads_are_counted_or_sql_limited", func(t *testing.T) {
		name := "issue513_bounded_reads_" + uuid.NewString()
		var unbounded int
		inspect := func(tx *gormlib.DB) {
			if tx.Statement.Table != "issue_comments" && tx.Statement.Table != "issue_changes" {
				return
			}
			sql := strings.ToUpper(tx.Statement.SQL.String())
			if strings.Contains(sql, "COUNT(") {
				return
			}
			if !strings.Contains(sql, "LIMIT ") {
				unbounded++
			}
		}
		require.NoError(t, db.Callback().Row().After("gorm:row").Register(name, inspect))
		require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, inspect))
		defer func() {
			require.NoError(t, db.Callback().Row().Remove(name))
			require.NoError(t, db.Callback().Query().Remove(name))
		}()
		_, err := store.ReadTaskIssue(ctx, card.ID, gormdb.IssueTaskDetailParams{Paged: true, CommentsLimit: 1, HistoryLimit: 1})
		require.NoError(t, err)
		assert.Zero(t, unbounded, "a normal page cannot select the full comments/history collection")
	})

	t.Run("omitted_patch_source_is_unknown_attribution_not_a_grant", func(t *testing.T) {
		result, err := application.Mutate(ctx, issues.Scope{}, card.ID, json.RawMessage(`{"expected_revision":"2","title":"Synthetic omitted source edit","comment":"Synthetic unclaimed comment"}`))
		require.NoError(t, err)
		assert.Equal(t, "3", result.Revision)
		var history gormdb.IssueChange
		require.NoError(t, db.Where("issue_id=? AND revision=3", card.ID).Take(&history).Error)
		assert.Nil(t, history.Attribution.SourceProject, "do not fabricate caller attribution from the stored card")
	})
}
