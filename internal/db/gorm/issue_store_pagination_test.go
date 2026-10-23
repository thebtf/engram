package gorm

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	_ "modernc.org/sqlite"
)

func TestIssueStore_ListIssuesExPaginatesTiesDeterministically(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	sqlDB.SetMaxOpenConns(1)
	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gormlib.Config{SkipDefaultTransaction: true})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE issues (
			id INTEGER PRIMARY KEY,
			priority TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			source_project TEXT NOT NULL,
			target_project TEXT NOT NULL,
			source_agent TEXT NOT NULL,
			status TEXT NOT NULL,
			type TEXT NOT NULL
		)`,
		`CREATE TABLE issue_comments (id INTEGER PRIMARY KEY, issue_id INTEGER NOT NULL)`,
		`CREATE TABLE project_consolidation_memberships (
			table_name TEXT, field_path TEXT, row_key TEXT, project_id TEXT
		)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}

	createdAt := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	for id := int64(1); id <= 15; id++ {
		priority, timestamp := "high", createdAt
		sourceProject, targetProject, sourceAgent := "paging-source", "paging-target", "paging-agent"
		status, issueType := "open", "task"
		switch id {
		case 1:
			priority, timestamp = "critical", createdAt.Add(-2*time.Hour)
		case 2:
			timestamp = createdAt.Add(time.Hour)
		case 8:
			timestamp = createdAt.Add(-time.Hour)
		case 9:
			priority, timestamp = "medium", createdAt.Add(2*time.Hour)
		case 10:
			priority, timestamp = "low", createdAt.Add(3*time.Hour)
		case 11:
			sourceProject = "other-source"
		case 12:
			targetProject = "other-target"
		case 13:
			sourceAgent = "other-agent"
		case 14:
			status = "resolved"
		case 15:
			issueType = "bug"
		}
		require.NoError(t, db.Exec(`INSERT INTO issues
			(id, priority, created_at, source_project, target_project, source_agent, status, type)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, priority, timestamp, sourceProject, targetProject, sourceAgent, status, issueType,
		).Error)
	}

	store := NewIssueStore(db)
	params := IssueListParams{
		SourceProject: "paging-source",
		TargetProject: "paging-target",
		SourceAgent:   "paging-agent",
		Statuses:      []string{"open"},
		Type:          "task",
		Limit:         2,
	}
	want := []int64{1, 2, 7, 6, 5, 4, 3, 8, 9, 10}
	var allIDs []int64
	seen := make(map[int64]bool, len(want))
	for params.Offset = 0; params.Offset <= len(want); params.Offset += params.Limit {
		page, total, err := store.ListIssuesEx(context.Background(), params)
		require.NoError(t, err)
		require.EqualValues(t, len(want), total, "offset %d", params.Offset)
		pageIDs := make([]int64, len(page))
		for index, issue := range page {
			pageIDs[index] = issue.ID
			require.NotContains(t, seen, issue.ID, "duplicate issue at offset %d", params.Offset)
			seen[issue.ID] = true
		}
		end := min(params.Offset+params.Limit, len(want))
		assert.Equal(t, want[params.Offset:end], pageIDs, "offset %d", params.Offset)
		allIDs = append(allIDs, pageIDs...)
	}
	require.Len(t, seen, len(want))
	assert.Equal(t, want, allIDs)
}
