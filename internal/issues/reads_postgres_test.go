package issues_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/issuefixture"
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

func TestIssueResolvedSinceExtremePostgres(t *testing.T) {
	db := sharedDB(t)
	store := gormdb.NewIssueStore(db)
	resolved := time.Date(2026, time.January, 2, 3, 4, 5, 123_000_000, time.UTC)
	finite := gormdb.Issue{Title: "Synthetic finite resolution", Status: "resolved", SourceProject: source, TargetProject: source, CreatedBySession: "finite-since", ResolvedAt: &resolved}
	unresolved := gormdb.Issue{Title: "Synthetic null resolution", SourceProject: source, TargetProject: source, CreatedBySession: "finite-since"}
	negativeSubsecond := time.UnixMilli(-500)
	beforeEpoch := gormdb.Issue{Title: "Synthetic pre-epoch resolution", Status: "resolved", SourceProject: source, TargetProject: source, CreatedBySession: "subsecond-since", ResolvedAt: &negativeSubsecond}
	hidden := gormdb.Issue{Title: "Synthetic other project resolution", Status: "resolved", SourceProject: hiddenSource, TargetProject: hiddenSource, CreatedBySession: "finite-since", ResolvedAt: &resolved}
	require.NoError(t, issuefixture.SeedHistorical(db, func(tx *gormlib.DB) error {
		for _, row := range []*gormdb.Issue{&finite, &unresolved, &hidden, &beforeEpoch} {
			if err := tx.Create(row).Error; err != nil {
				return err
			}
		}
		for _, value := range []string{"infinity", "-infinity"} {
			if err := tx.Exec(`INSERT INTO issues (title,status,source_project,target_project,created_by_session,resolved_at) VALUES ('Synthetic infinite resolution','resolved',?,?,?,?::text::timestamptz)`, source, source, "infinite-since", value).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(`INSERT INTO issues (title,status,source_project,target_project,created_by_session,resolved_at) VALUES ('Synthetic distant finite resolution','resolved',?,?,?,?::text::timestamptz)`, source, source, "distant-since", "260000-01-02 03:04:05.123+00").Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO issues (title,status,source_project,target_project,created_by_session,resolved_at) VALUES ('Synthetic earliest finite resolution','resolved',?,?,?,TIMESTAMPTZ 'epoch' - INTERVAL '210866803200 seconds')`, source, source, "minimum-since").Error; err != nil {
			return err
		}
		return nil
	}))
	for _, tc := range []struct {
		name   string
		millis int64
		want   bool
	}{
		{"max_int64", math.MaxInt64, false},
		{"min_int64", math.MinInt64, true},
		{"ordinary_past", resolved.UnixMilli() - 1, true},
		{"inclusive_equal", resolved.UnixMilli(), true},
		{"ordinary_future", resolved.UnixMilli() + 1, false},
		{"positive_binary_boundary", 8_000_000_000_000_000, false},
		{"positive_numeric_boundary", 8_000_000_000_001_000, false},
		{"negative_binary_boundary", -8_000_000_000_000_000, true},
		{"negative_numeric_boundary", -8_000_000_000_001_000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			since := time.UnixMilli(tc.millis)
			params := gormdb.IssueTaskListParams{ReadProject: source, IssueListParams: gormdb.IssueListParams{SourceSession: "finite-since", ResolvedSince: &since, Limit: 10}}
			result, err := store.ListTaskIssues(t.Context(), params)
			require.NoError(t, err)
			if tc.want {
				require.Len(t, result.Issues, 1)
				assert.Equal(t, finite.ID, result.Issues[0].ID, "NULL and other-project rows remain excluded")
				assert.EqualValues(t, 1, result.Total)
			} else {
				assert.Empty(t, result.Issues)
				assert.Zero(t, result.Total)
			}
		})
	}
	for _, group := range []struct {
		session string
		at      int64
	}{
		{"minimum-since", -210_866_803_200_000},
		{"subsecond-since", -500},
	} {
		for _, tc := range []struct{ millis, want int64 }{{group.at - 1, 1}, {group.at, 1}, {group.at + 1, 0}} {
			since := time.UnixMilli(tc.millis)
			params := gormdb.IssueTaskListParams{ReadProject: source, IssueListParams: gormdb.IssueListParams{SourceSession: group.session, ResolvedSince: &since, Limit: 10, Offset: 100}}
			result, err := store.ListTaskIssues(t.Context(), params)
			require.NoError(t, err, "%s at %d", group.session, tc.millis)
			assert.Equal(t, tc.want, result.Total, "%s at %d", group.session, tc.millis)
		}
	}
	distant := time.Date(260000, time.January, 2, 3, 4, 5, 123_000_000, time.UTC).UnixMilli()
	for _, tc := range []struct{ millis, want int64 }{{distant - 1, 1}, {distant, 1}, {distant + 1, 0}} {
		since := time.UnixMilli(tc.millis)
		params := gormdb.IssueTaskListParams{ReadProject: source, IssueListParams: gormdb.IssueListParams{SourceSession: "distant-since", ResolvedSince: &since, Limit: 10, Offset: 100}}
		result, err := store.ListTaskIssues(t.Context(), params)
		require.NoError(t, err)
		assert.Equal(t, tc.want, result.Total, "distant finite timestamps retain millisecond-inclusive ordering")
	}
	for _, ms := range []int64{math.MinInt64, math.MaxInt64, resolved.UnixMilli()} {
		since := time.UnixMilli(ms)
		// Count without scanning PostgreSQL infinity into Go's finite time.Time.
		params := gormdb.IssueTaskListParams{ReadProject: source, IssueListParams: gormdb.IssueListParams{SourceSession: "infinite-since", ResolvedSince: &since, Limit: 10, Offset: 100}}
		result, err := store.ListTaskIssues(t.Context(), params)
		require.NoError(t, err)
		assert.EqualValues(t, 1, result.Total, "positive infinity matches and negative infinity does not")
		assert.Empty(t, result.Issues)
	}
}
