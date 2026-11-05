package gorm

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/db/sqlitetest"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	_ "modernc.org/sqlite"
)

func TestIssueStore_ListIssuesExPaginatesTiesDeterministically(t *testing.T) {
	registered, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	sqlDB := sqlitetest.OpenMemory(registered.Driver())
	require.NoError(t, registered.Close())
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
		sqlitetest.RedirectSchema,
		`CREATE TABLE projects (id TEXT PRIMARY KEY, removed_at TIMESTAMP, identity_status TEXT)`,
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
	target := "11111111-1111-4111-8111-111111111111"
	alias := "22222222-2222-4222-8222-222222222222"
	other := "33333333-3333-4333-8333-333333333333"
	invalid := "not-a-uuid"
	for _, id := range []string{target, alias, other, invalid} {
		require.NoError(t, db.Exec("INSERT INTO projects(id, identity_status) VALUES (?, 'active')", id).Error)
	}
	for _, id := range []string{alias, invalid} {
		require.NoError(t, db.Exec("INSERT INTO project_consolidation_redirects(selector, session_id, project_id) VALUES (?, '', ?)", id, target).Error)
	}
	for _, row := range []struct {
		id      int
		project string
	}{{101, alias}, {102, invalid}, {103, target}, {104, target}, {105, other}, {106, target}} {
		require.NoError(t, db.Exec(`INSERT INTO issues (id, priority, created_at, source_project, target_project, source_agent, status, type) VALUES (?, 'high', ?, 'source', ?, 'agent', 'open', 'task')`, row.id, createdAt, row.project).Error)
	}
	for _, row := range []struct {
		key     string
		project any
	}{{`{"id":103}`, target}, {`{ "id" : 103 }`, other}, {`{"id":104}`, nil}, {`{"id":105}`, target}} {
		require.NoError(t, db.Exec(`INSERT INTO project_consolidation_memberships(table_name,field_path,row_key,project_id) VALUES ('issues','["target_project"]',?,?)`, row.key, row.project).Error)
	}
	page, total, err := store.ListIssuesEx(context.Background(), IssueListParams{TargetProject: target, Limit: 20})
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	var selected []int64
	for _, issue := range page {
		selected = append(selected, issue.ID)
	}
	require.ElementsMatch(t, []int64{101, 105, 106}, selected, "valid aliases and unambiguous membership only")
	require.NoError(t, db.Exec("DROP TABLE project_consolidation_redirects").Error)
	_, _, err = store.ListIssuesEx(context.Background(), IssueListParams{TargetProject: target, Limit: 20})
	require.ErrorContains(t, err, "project_consolidation_redirects")
}
