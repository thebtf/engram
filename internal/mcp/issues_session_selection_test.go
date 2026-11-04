package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func issueSessionSelectionMCPProject(t *testing.T, store *gormdb.Store) string {
	t.Helper()
	project := uniqueIssueToolProject(t, "session-recipient")
	require.NoError(t, store.GetDB().Create(&gormdb.Project{ID: project}).Error)
	t.Cleanup(func() {
		require.NoError(t, store.GetDB().Exec("DELETE FROM issue_history_events WHERE issue_id IN (SELECT id FROM issues WHERE source_project = ? OR target_project = ?)", project, project).Error)
		require.NoError(t, store.GetDB().Exec("DELETE FROM issue_comments WHERE issue_id IN (SELECT id FROM issues WHERE source_project = ? OR target_project = ?)", project, project).Error)
		require.NoError(t, store.GetDB().Exec("DELETE FROM issues WHERE source_project = ? OR target_project = ?", project, project).Error)
		require.NoError(t, store.GetDB().Where("id = ?", project).Delete(&gormdb.Project{}).Error)
	})
	return project
}

func issueSessionSelectionMCPIDs(t *testing.T, output string) []int64 {
	t.Helper()
	var ids []int64
	for _, match := range regexp.MustCompile(`(?m)^#([0-9]+) \[`).FindAllStringSubmatch(output, -1) {
		id, err := strconv.ParseInt(match[1], 10, 64)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	return ids
}

func TestIssueSessionSelectionMCPSchema(t *testing.T) {
	schema := issuesToolSchema()
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	for _, field := range []string{"target_session", "source_session"} {
		assert.Contains(t, properties, field, "native clients need a discoverable optional recipient/creator selector")
		assert.NotContains(t, schema["required"], field)
	}
}

func TestIssueSessionSelectionMCPRoundTripAndFilters(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	project := issueSessionSelectionMCPProject(t, store)
	otherProject := issueSessionSelectionMCPProject(t, store)
	server, _ := issueAttributionServer(store)
	const first = "01a1a365-f562-75dc-a577-53db1b92693d"
	const second = "01a1a365-f562-75dc-a577-53db1b92693e"
	base := issueClientContext(project, "session-selection-owner", auth.RoleReadWrite)
	rows := []struct{ source, target, project, priority, status string }{
		{first, first, project, "low", "open"},
		{second, first, project, "critical", "open"},
		{first, second, project, "high", "open"},
		{second, second, project, "medium", "open"},
		{first, first, project, "critical", "resolved"},
		{first, first, otherProject, "critical", "open"},
		{"", "", project, "medium", "open"},
		{first, "", project, "medium", "open"},
	}
	ids := make([]int64, len(rows))
	for index, row := range rows {
		args := map[string]any{
			"action": "create", "title": fmt.Sprintf("native recipient fixture %d", index),
			"body": "verbatim user body", "target_project": row.project, "priority": row.priority,
			"client": "omp",
		}
		if row.target != "" {
			args["target_session"] = row.target
		}
		ctx := base
		if row.source != "" {
			ctx = ContextWithSession(base, row.source)
		}
		created, err := callIssueAction(t, server, ctx, args)
		require.NoError(t, err, "a recipient reference needs no live session registration")
		_, err = fmt.Sscanf(created, "Issue #%d created", &ids[index])
		require.NoError(t, err)
		if row.status != "open" {
			require.NoError(t, issueStore.UpdateIssueStatus(context.Background(), ids[index], row.status))
		}
	}

	reloaded, _ := issueAttributionServer(store)
	reader := ContextWithSession(base, second)
	for index, row := range rows {
		out, err := callIssueAction(t, reloaded, reader, map[string]any{"action": "get", "id": ids[index]})
		require.NoError(t, err)
		assert.Contains(t, out, "verbatim user body")
		if row.target != "" {
			assert.Contains(t, out, row.target)
			assert.Contains(t, out, row.source)
		}
		var persisted string
		require.NoError(t, store.GetDB().Raw("SELECT to_jsonb(issues)::text FROM issues WHERE id = ?", ids[index]).Scan(&persisted).Error)
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(persisted), &record))
		if row.target != "" {
			assert.Equal(t, row.target, record["target_session"])
		} else {
			assert.True(t, record["target_session"] == nil || record["target_session"] == "", "omission must not select the creator or reader session")
		}
		assert.Equal(t, row.source, record["created_by_session"])
		assert.Equal(t, row.project, record["target_project"])
		assert.Equal(t, project, record["source_project"])
	}

	for _, tc := range []struct {
		name   string
		filter map[string]any
		want   []int64
	}{
		{"default_project_does_not_use_reader_session", nil, []int64{ids[0], ids[1], ids[2], ids[3], ids[6], ids[7]}},
		{"recipient_only", map[string]any{"target_session": first}, []int64{ids[0], ids[1]}},
		{"creator_only", map[string]any{"source_session": first}, []int64{ids[0], ids[2], ids[7]}},
		{"full_second_recipient_not_shared_prefix", map[string]any{"target_session": second}, []int64{ids[2], ids[3]}},
		{"intersection_before_limit_and_total", map[string]any{"source_session": first, "target_session": first, "limit": 1}, []int64{ids[0]}},
		{"status_composes_with_sessions", map[string]any{"source_session": first, "target_session": first, "status": "resolved"}, []int64{ids[4]}},
		{"unknown_recipient_is_empty_not_an_error", map[string]any{"target_session": uuid.NewString()}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"action": "list", "project": project, "source_project": project, "limit": 100}
			for key, value := range tc.filter {
				args[key] = value
			}
			out, err := callIssueAction(t, reloaded, reader, args)
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.want, issueSessionSelectionMCPIDs(t, out), out)
			if len(tc.want) > 0 {
				assert.Contains(t, out, fmt.Sprintf("Issues (%d of %d)", len(tc.want), len(tc.want)), "count must use all selectors before limit")
			}
			if tc.name == "recipient_only" || tc.name == "full_second_recipient_not_shared_prefix" {
				assert.Contains(t, out, tc.filter["target_session"], "list must report persisted recipient provenance")
			}
		})
	}
}

func TestIssueSessionSelectionMCPRecipientDoesNotAuthorize(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	project := issueSessionSelectionMCPProject(t, store)
	server, _ := issueAttributionServer(store)
	session := uuid.NewString()
	owner := ContextWithSession(issueClientContext(project, "owner", auth.RoleReadWrite), session)
	created, err := callIssueAction(t, server, owner, map[string]any{
		"action": "create", "title": "recipient does not authorize", "target_project": project, "target_session": session,
	})
	require.NoError(t, err)
	var id int64
	_, err = fmt.Sscanf(created, "Issue #%d created", &id)
	require.NoError(t, err)
	before, comments, err := issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	for _, ctx := range []context.Context{
		ContextWithSession(issueClientContext(project, "owner", auth.RoleReadOnly), session),
		ContextWithSession(contextWithProject(context.Background(), project), session),
	} {
		for _, args := range []map[string]any{
			{"action": "create", "title": "denied recipient create", "target_project": project, "target_session": session},
			{"action": "comment", "id": id, "body": "denied recipient comment", "session_id": session, "target_session": session},
			{"action": "update", "id": id, "status": "resolved", "target_session": session},
		} {
			_, err := callIssueAction(t, server, ctx, args)
			require.ErrorContains(t, err, "forbidden")
		}
	}
	after, actualComments, err := issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Equal(t, comments, actualComments)
	listed, total, err := issueStore.ListIssuesEx(context.Background(), gormdb.IssueListParams{SourceProject: project})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Len(t, listed, 1)
}

func TestIssueSessionSelectionMCPOptionalProvenanceCompatibility(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	project := issueSessionSelectionMCPProject(t, store)
	server, _ := issueAttributionServer(store)
	ctx := issueClientContext(project, "compatibility-owner", auth.RoleReadWrite)
	for index, tc := range []struct{ session, client, want string }{
		{"conversation-20261104_alpha.17", "openclaw", "conversation-20261104_alpha.17"},
		{uuid.Nil.String(), "openclaw", ""},
		{"bad\nconversation", "openclaw", ""},
		{strings.Repeat("s", 16384), "openclaw", ""},
		{"", "future-host-client", ""},
	} {
		t.Run(fmt.Sprintf("metadata_%d", index), func(t *testing.T) {
			out, err := callIssueAction(t, server, ctx, map[string]any{
				"action": "create", "title": "optional metadata compatibility", "target_project": project,
				"session_id": tc.session, "client": tc.client,
			})
			require.NoError(t, err, "optional provenance must not fail an authorized create")
			var id int64
			_, err = fmt.Sscanf(out, "Issue #%d created", &id)
			require.NoError(t, err)
			issue, _, err := issueStore.GetIssue(context.Background(), id)
			require.NoError(t, err)
			assert.Equal(t, tc.want, issue.CreatedBySession)
			if tc.want == "" {
				assert.Empty(t, issue.CreatedBySessionSource)
			} else {
				assert.Equal(t, "reported", issue.CreatedBySessionSource)
			}
		})
	}
}