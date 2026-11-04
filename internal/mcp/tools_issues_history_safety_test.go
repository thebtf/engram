package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// event_id and pagination lines bind the output contract agreed with the backend
// owner.
func TestIssueHistoryMCPBoundedContinuationAndReadOnly(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	project := uuid.NewString()
	require.NoError(t, store.GetDB().Create(&gormdb.Project{ID: project}).Error)
	t.Cleanup(func() { require.NoError(t, store.GetDB().Where("id = ?", project).Delete(&gormdb.Project{}).Error) })
	id := createIssueToolFixture(t, store, issueStore, project, "history-owner", "open")
	otherID := createIssueToolFixture(t, store, issueStore, project, "other-owner", "open")
	const session = "01a1a369-dfce-7241-9330-2a0a3f697f9c"
	const body = "A real user comment, not a generated lifecycle entry."
	_, err := issueStore.AddComment(context.Background(), id, &gormdb.IssueComment{AuthorProject: project, Body: body})
	require.NoError(t, err)
	_, commentsBefore, err := issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	events := make([]gormdb.IssueHistoryEvent, 103)
	var foreignEvent gormdb.IssueHistoryEvent
	for index := range events {
		if index == 51 {
			foreignEvent = gormdb.IssueHistoryEvent{IssueID: otherID, Status: "closed", AuthorProject: project, CreatedAt: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)}
			require.NoError(t, store.GetDB().Create(&foreignEvent).Error)
		}
		events[index] = gormdb.IssueHistoryEvent{
			IssueID: id, Status: "resolved", AuthorProject: project,
			SessionID: session, SessionSource: "reported", Client: "omp",
			CreatedAt: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC).Add(-time.Duration(index) * time.Second),
		}
		if index == 0 {
			events[index].AuthorProject = ""
		}
		require.NoError(t, store.GetDB().Create(&events[index]).Error)
	}
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	// Project-free reads remain available through the ordinary read-only gate,
	// including during an identity hold. A session must not become a project.
	ctx := ContextWithSession(auth.WithIdentity(context.Background(), auth.Client("read-only", "history-reader")), uuid.NewString())
	call := func(args map[string]any) (string, error) {
		t.Helper()
		raw, err := json.Marshal(args)
		require.NoError(t, err)
		return server.callTool(ctx, "issues", raw)
	}
	eventPattern := regexp.MustCompile(`\bevent_id=([0-9]+)\b`)
	metadata := func(output, field string) string {
		t.Helper()
		match := regexp.MustCompile(`(?m)^`+field+`: ([^\r\n]+)\r?$`).FindAllStringSubmatch(output, -1)
		require.Len(t, match, 1, "exactly one %s field is required in %q", field, output)
		return match[0][1]
	}
	read := func(args map[string]any, size, limit int, more bool, next int64) []int64 {
		t.Helper()
		out, err := call(args)
		require.NoError(t, err)
		require.Equal(t, strconv.Itoa(limit), metadata(out, "history_limit"))
		require.Equal(t, strconv.FormatBool(more), metadata(out, "history_has_more"))
		if more {
			require.Equal(t, strconv.FormatInt(next, 10), metadata(out, "history_next_after_id"))
			require.Contains(t, out, fmt.Sprintf(`issues(action="get", id=%d, history_limit=%d, history_after_id=%d)`, id, limit, next))
		} else {
			require.Equal(t, "null", metadata(out, "history_next_after_id"))
		}
		require.Equal(t, 1, strings.Count(out, body), "events must not duplicate the single user comment")
		require.Contains(t, out, "--- Comments (1) ---")
		matches := eventPattern.FindAllStringSubmatch(out, -1)
		require.Len(t, matches, size)
		ids := make([]int64, len(matches))
		for index, match := range matches {
			ids[index], err = strconv.ParseInt(match[1], 10, 64)
			require.NoError(t, err)
			require.NotEqual(t, foreignEvent.ID, ids[index], "history query must retain its parent issue predicate")
		}
		return ids
	}
	var got []int64
	for index, bounds := range [][2]int{{0, 50}, {50, 100}, {100, 103}} {
		args := map[string]any{"action": "get", "id": id}
		if index > 0 {
			args["history_after_id"] = got[len(got)-1]
		}
		more := index < 2
		got = append(got, read(args, bounds[1]-bounds[0], 50, more, events[bounds[1]-1].ID)...)
	}
	want := make([]int64, len(events))
	for index := range events {
		want[index] = events[index].ID
	}
	require.Equal(t, want, got, "exclusive cursor returns all event IDs exactly once despite timestamp order and ID gaps")
	require.Equal(t, want[:100], read(map[string]any{"action": "get", "id": id, "history_limit": 100}, 100, 100, true, events[99].ID))
	require.Equal(t, want[:1], read(map[string]any{"action": "get", "id": id, "history_limit": 1}, 1, 1, true, events[0].ID))
	read(map[string]any{"action": "get", "id": id, "history_after_id": events[102].ID}, 0, 50, false, 0)
	out, err := call(map[string]any{"action": "get", "id": id, "history_limit": 1})
	require.NoError(t, err)
	require.Contains(t, out, `author_project=""`, "unknown authors must not be inferred from the issue, project or reader")
	require.Contains(t, out, `host_session="`+session+`"`)
	require.Contains(t, out, `session_source="reported"`)
	require.Contains(t, out, `client="omp"`)
	require.Contains(t, out, "provenance only")
	for _, args := range []map[string]any{
		{"history_limit": 0},
		{"history_limit": 101},
		{"history_limit": 1.5},
		{"history_after_id": -1},
		{"history_after_id": 1.5},
		{"history_after_id": "1"},
	} {
		args["action"], args["id"] = "get", id
		out, err := call(args)
		require.Error(t, err)
		require.Empty(t, out, "invalid pagination must not return a partial detail page")
	}
	out, err = call(map[string]any{"action": "update", "id": id, "status": "resolved", "session_id": session, "client": "omp"})
	require.Error(t, err, "reported provenance must never elevate a read-only client")
	require.Empty(t, out)
	_, commentsAfter, err := issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, commentsBefore, commentsAfter)
	var retained []gormdb.IssueHistoryEvent
	require.NoError(t, store.GetDB().Where("issue_id = ?", id).Order("id").Find(&retained).Error)
	require.Len(t, retained, len(events))
	for index, expected := range events {
		actual := retained[index]
		require.True(t, expected.CreatedAt.Equal(actual.CreatedAt), "event %d CreatedAt must retain the exact instant", expected.ID)
		actual.CreatedAt = expected.CreatedAt
		require.Equal(t, expected, actual, "event %d non-time fields must remain unchanged", expected.ID)
	}
}

func TestIssueHistoryMCPDiscoverablePagination(t *testing.T) {
	schema := issuesToolSchema()
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	for _, name := range []string{"history_limit", "history_after_id"} {
		require.Contains(t, properties, name)
		require.NotContains(t, schema["required"], name)
	}
	server := NewServer(ServerOptions{})
	var description string
	for _, tool := range server.primaryTools() {
		if tool.Name == "issues" {
			raw, err := json.Marshal(tool.InputSchema)
			require.NoError(t, err)
			description = tool.Description + " " + string(raw)
		}
	}
	require.NotEmpty(t, description)
	for _, name := range []string{"history_limit", "history_after_id", "history_has_more", "history_next_after_id"} {
		require.Contains(t, description, name, "external clients must discover both continuation inputs and honest readback fields")
	}
}
