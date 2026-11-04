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

func issueParticipantMCPProject(t *testing.T, store *gormdb.Store) string {
	t.Helper()
	project := uniqueIssueToolProject(t, "participant-addressee")
	require.NoError(t, store.GetDB().Create(&gormdb.Project{ID: project}).Error)
	t.Cleanup(func() {
		require.NoError(t, store.GetDB().Exec("DELETE FROM issue_history_events WHERE issue_id IN (SELECT id FROM issues WHERE source_project = ? OR target_project = ?)", project, project).Error)
		require.NoError(t, store.GetDB().Exec("DELETE FROM issue_comments WHERE issue_id IN (SELECT id FROM issues WHERE source_project = ? OR target_project = ?)", project, project).Error)
		require.NoError(t, store.GetDB().Exec("DELETE FROM issues WHERE source_project = ? OR target_project = ?", project, project).Error)
		require.NoError(t, store.GetDB().Where("id = ?", project).Delete(&gormdb.Project{}).Error)
	})
	return project
}

func issueParticipantMCPCreate(t *testing.T, server *Server, ctx context.Context, targetProject, recipient, title, priority string) int64 {
	t.Helper()
	args := map[string]any{
		"action": "create", "title": title, "body": "participant fixture user body",
		"target_project": targetProject, "priority": priority, "client": "omp",
	}
	if recipient != "" {
		args["target_session"] = recipient
	}
	out, err := callIssueAction(t, server, ctx, args)
	require.NoError(t, err)
	var id int64
	_, err = fmt.Sscanf(out, "Issue #%d created", &id)
	require.NoError(t, err)
	require.Positive(t, id)
	return id
}

func issueParticipantMCPRecipient(t *testing.T, store *gormdb.Store, id int64) string {
	t.Helper()
	var persisted string
	require.NoError(t, store.GetDB().Raw("SELECT to_jsonb(issues)::text FROM issues WHERE id = ?", id).Scan(&persisted).Error)
	var row map[string]any
	require.NoError(t, json.Unmarshal([]byte(persisted), &row))
	if row["target_session"] == nil {
		return ""
	}
	value, ok := row["target_session"].(string)
	require.True(t, ok, "persisted recipient must be a session reference, not an object or project")
	return value
}

func issueParticipantMCPIDs(t *testing.T, out string) []int64 {
	t.Helper()
	var ids []int64
	for _, match := range regexp.MustCompile(`(?m)^#([0-9]+) \[`).FindAllStringSubmatch(out, -1) {
		id, err := strconv.ParseInt(match[1], 10, 64)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	return ids
}

func TestIssueParticipantMCPRecipientUpdateAuthorityAndPresence(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	project := issueParticipantMCPProject(t, store)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	creatorSession := uuid.NewString()
	owner := ContextWithSession(issueClientContext(project, "recipient-creator", auth.RoleReadWrite), creatorSession)
	operator := ContextWithSession(auth.WithIdentity(contextWithProject(context.Background(), project), auth.Admin()), uuid.NewString())
	first, second := uuid.NewString(), uuid.NewString()
	for _, actor := range []struct {
		name string
		ctx  context.Context
	}{{"creator", owner}, {"operator", operator}} {
		t.Run(actor.name, func(t *testing.T) {
			id := issueParticipantMCPCreate(t, server, owner, project, "", "recipient mutation fixture", "medium")
			before, comments, err := issueStore.GetIssue(context.Background(), id)
			require.NoError(t, err)
			for _, step := range []struct {
				name         string
				fields       map[string]any
				want, status string
			}{
				{"set_without_status", map[string]any{"target_session": first}, first, "open"},
				{"omitted_preserves", map[string]any{"status": "resolved"}, first, "resolved"},
				{"change_without_status", map[string]any{"target_session": second}, second, "resolved"},
				{"explicit_empty_clears", map[string]any{"target_session": ""}, "", "resolved"},
			} {
				t.Run(step.name, func(t *testing.T) {
					args := map[string]any{"action": "update", "id": id}
					for key, value := range step.fields {
						args[key] = value
					}
					_, err := callIssueAction(t, server, actor.ctx, args)
					require.NoError(t, err, "recipient-only updates must not require a lifecycle transition")
					assert.Equal(t, step.want, issueParticipantMCPRecipient(t, store, id))
					after, actualComments, err := issueStore.GetIssue(context.Background(), id)
					require.NoError(t, err)
					assert.Equal(t, step.status, after.Status)
					assert.Equal(t, before.Title, after.Title)
					assert.Equal(t, before.Body, after.Body)
					assert.Equal(t, before.SourceProject, after.SourceProject)
					assert.Equal(t, before.TargetProject, after.TargetProject)
					assert.Equal(t, before.CreatorKeycardID, after.CreatorKeycardID)
					assert.Equal(t, creatorSession, after.CreatedBySession)
					assert.Equal(t, comments, actualComments)
					reloaded := NewServer(ServerOptions{})
					reloaded.SetIssueStore(gormdb.NewIssueStore(store.GetDB()))
					out, err := callIssueAction(t, reloaded, owner, map[string]any{"action": "get", "id": id})
					require.NoError(t, err)
					if step.want != "" {
						assert.Contains(t, out, step.want)
					} else {
						assert.NotContains(t, out, first)
						assert.NotContains(t, out, second)
					}
				})
			}
		})
	}
}

func TestIssueParticipantMCPRecipientUpdateDenialIsAtomic(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	project := issueParticipantMCPProject(t, store)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	session, original, replacement := uuid.NewString(), uuid.NewString(), uuid.NewString()
	owner := ContextWithSession(issueClientContext(project, "recipient-creator", auth.RoleReadWrite), session)
	id := issueParticipantMCPCreate(t, server, owner, project, original, "recipient authority fixture", "medium")
	before, comments, err := issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	history := make([]gormdb.IssueHistoryEvent, 0)
	err = store.GetDB().Where("issue_id = ?", id).Order("created_at ASC, id ASC").Find(&history).Error
	require.NoError(t, err)
	for _, actor := range []struct {
		name string
		ctx  context.Context
	}{
		{"foreign_rw_claims_creator_session", ContextWithSession(issueClientContext(project, "different-keycard", auth.RoleReadWrite), session)},
		{"readonly_creator", ContextWithSession(issueClientContext(project, "recipient-creator", auth.RoleReadOnly), session)},
		{"missing_identity", ContextWithSession(contextWithProject(context.Background(), project), session)},
	} {
		for index, fields := range []map[string]any{
			{"target_session": replacement},
			{"target_session": ""},
			{"status": "resolved", "target_session": replacement, "comment": "must roll back"},
			{"status": "resolved", "target_session": "", "comment": "must roll back"},
		} {
			t.Run(fmt.Sprintf("%s_%d", actor.name, index), func(t *testing.T) {
				args := map[string]any{"action": "update", "id": id, "participant_session": session}
				for key, value := range fields {
					args[key] = value
				}
				_, err := callIssueAction(t, server, actor.ctx, args)
				require.ErrorContains(t, err, "forbidden")
				after, actualComments, err := issueStore.GetIssue(context.Background(), id)
				require.NoError(t, err)
				actualHistory := make([]gormdb.IssueHistoryEvent, 0)
				err = store.GetDB().Where("issue_id = ?", id).Order("created_at ASC, id ASC").Find(&actualHistory).Error
				require.NoError(t, err)
				assert.Equal(t, original, issueParticipantMCPRecipient(t, store, id))
				assert.Equal(t, before, after)
				assert.Equal(t, comments, actualComments)
				assert.Equal(t, history, actualHistory)
			})
		}
	}
}

func TestIssueParticipantMCPFilterUnionAndComposition(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	project := issueParticipantMCPProject(t, store)
	other := issueParticipantMCPProject(t, store)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	const first = "b73a77bc-10a5-4d70-9f31-111111111111"
	const second = "b73a77bc-10a5-4d70-9f31-111111111112"
	recipient, otherRecipient := uuid.NewString(), uuid.NewString()
	base := issueClientContext(project, "participant-fixture-owner", auth.RoleReadWrite)
	firstContext, secondContext := ContextWithSession(base, first), ContextWithSession(base, second)
	rows := []struct{ source, target, from, to, priority string }{
		{first, recipient, project, project, "low"},
		{second, recipient, project, project, "medium"},
		{second, recipient, project, project, "high"},
		{second, first, project, project, "critical"},
		{second, recipient, project, project, "critical"},
		{first, otherRecipient, project, project, "critical"},
		{first, recipient, project, other, "critical"},
		{first, recipient, project, project, "critical"},
		{first, recipient, other, project, "critical"},
		{second, recipient, project, project, "critical"},
	}
	ids := make([]int64, len(rows))
	for index, row := range rows {
		ctx := ContextWithSession(issueClientContext(row.from, "participant-fixture-owner", auth.RoleReadWrite), row.source)
		ids[index] = issueParticipantMCPCreate(t, server, ctx, row.to, row.target, fmt.Sprintf("participant corpus %d", index), row.priority)
	}
	for index := range 2 {
		_, err := callIssueAction(t, server, firstContext, map[string]any{"action": "comment", "id": ids[1], "body": fmt.Sprintf("authored comment %d", index)})
		require.NoError(t, err)
	}
	_, err := callIssueAction(t, server, secondContext, map[string]any{"action": "comment", "id": ids[9], "body": "different full session"})
	require.NoError(t, err)
	_, err = callIssueAction(t, server, firstContext, map[string]any{"action": "update", "id": ids[2], "status": "resolved"})
	require.NoError(t, err)
	_, err = callIssueAction(t, server, secondContext, map[string]any{"action": "reopen", "id": ids[2]})
	require.NoError(t, err)
	_, err = callIssueAction(t, server, secondContext, map[string]any{"action": "update", "id": ids[7], "status": "resolved"})
	require.NoError(t, err)
	_, lifecycleComments, err := issueStore.GetIssue(context.Background(), ids[2])
	require.NoError(t, err)
	require.Empty(t, lifecycleComments, "the lifecycle branch must be tested without an authored-comment match")
	events := make([]gormdb.IssueHistoryEvent, 0)
	err = store.GetDB().Where("issue_id = ?", ids[2]).Order("created_at ASC, id ASC").Find(&events).Error
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, first, events[0].SessionID)

	for _, tc := range []struct {
		name   string
		filter map[string]any
		want   []int64
		total  int
	}{
		{"creator_or_comment_or_history_once_each", nil, []int64{ids[0], ids[1], ids[2]}, 3},
		{"and_creator_session", map[string]any{"source_session": second}, []int64{ids[1], ids[2]}, 2},
		{"and_exact_creator_session", map[string]any{"source_session": first}, []int64{ids[0]}, 1},
		{"before_limit_and_count", map[string]any{"limit": 1}, []int64{ids[2]}, 3},
		{"recipient_is_not_participation", map[string]any{"target_session": ""}, []int64{ids[0], ids[1], ids[2], ids[5]}, 4},
		{"exact_second_uuid", map[string]any{"participant_session": second}, []int64{ids[1], ids[2], ids[4], ids[9]}, 4},
		{"unknown_participant_without_liveness", map[string]any{"participant_session": uuid.NewString()}, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{
				"action": "list", "project": project, "source_project": project, "status": "open,reopened",
				"participant_session": first, "target_session": recipient, "limit": 100,
			}
			for key, value := range tc.filter {
				if key == "target_session" && value == "" {
					delete(args, key)
				} else {
					args[key] = value
				}
			}
			reader := issueClientContext(project, "unrelated-readonly-reader", auth.RoleReadOnly)
			out, err := callIssueAction(t, server, ContextWithSession(reader, second), args)
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.want, issueParticipantMCPIDs(t, out), out)
			if tc.total > 0 {
				assert.Contains(t, out, fmt.Sprintf("Issues (%d of %d)", len(tc.want), tc.total))
			}
		})
	}
}

func TestIssueParticipantMCPReadSelectorValidation(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	project := issueParticipantMCPProject(t, store)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	ctx := issueClientContext(project, "selector-fixture-owner", auth.RoleReadWrite)
	issueParticipantMCPCreate(t, server, ctx, project, "", "must not leak from invalid selector", "medium")
	for _, field := range []string{"participant_session", "source_session", "target_session"} {
		for index, value := range []any{
			" ", "bad\nselector", "bad\x00selector", "bad\x7fselector", uuid.Nil.String(), strings.Repeat("s", 16384),
			42, true,
			[]string{uuid.NewString()},
			map[string]any{"id": uuid.NewString()},
		} {
			t.Run(fmt.Sprintf("%s_invalid_%d", field, index), func(t *testing.T) {
				out, err := callIssueAction(t, server, ctx, map[string]any{"action": "list", "project": project, field: value})
				require.Error(t, err, "invalid explicit selectors must fail, not fall back to an unfiltered list")
				assert.NotContains(t, out, "must not leak from invalid selector")
				assert.Empty(t, issueParticipantMCPIDs(t, out))
			})
		}
	}
	properties, ok := issuesToolSchema()["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, properties, "participant_session", "the explicit participant selector must be discoverable")
}
