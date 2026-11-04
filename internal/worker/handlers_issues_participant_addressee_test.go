package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func issueParticipantHTTPService(t *testing.T) (*Service, *gormdb.Store, string, string) {
	t.Helper()
	store := openWorkerUCIContextCompositionStore(t)
	project := seedWorkerTestProject(t, store.GetDB(), "participant source")
	other := seedWorkerTestProject(t, store.GetDB(), "participant other project")
	t.Cleanup(func() {
		require.NoError(t, store.GetDB().Exec("DELETE FROM issue_history_events WHERE issue_id IN (SELECT id FROM issues WHERE source_project = ? OR target_project = ?)", project, project).Error)
		require.NoError(t, store.GetDB().Exec("DELETE FROM issue_comments WHERE issue_id IN (SELECT id FROM issues WHERE source_project = ? OR target_project = ?)", project, project).Error)
		require.NoError(t, store.GetDB().Exec("DELETE FROM issues WHERE source_project = ? OR target_project = ?", project, project).Error)
	})
	return &Service{store: store, issueStore: gormdb.NewIssueStore(store.GetDB())}, store, project, other
}

func issueParticipantHTTPCreate(t *testing.T, service *Service, from, to, sourceSession, recipient, title, priority string) int64 {
	t.Helper()
	body := map[string]any{
		"title": title, "body": "participant fixture user body", "source_project": from, "target_project": to,
		"created_by_session": sourceSession, "client": "omp", "priority": priority,
	}
	if recipient != "" {
		body["target_session"] = recipient
	}
	created := httptest.NewRecorder()
	service.handleCreateIssue(created, issueHTTPRouteRequest(t, http.MethodPost, 0, body, auth.Client("read-write", "recipient-creator")))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var response struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &response))
	require.Positive(t, response.ID)
	return response.ID
}

func issueParticipantHTTPRecipient(t *testing.T, service *Service, id int64) string {
	t.Helper()
	read := httptest.NewRecorder()
	service.handleGetIssue(read, issueHTTPRouteRequest(t, http.MethodGet, id, nil, auth.Client("read-only", "unrelated-reader")))
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	var response struct {
		Issue map[string]any `json:"issue"`
	}
	require.NoError(t, json.Unmarshal(read.Body.Bytes(), &response))
	if response.Issue["target_session"] == nil {
		return ""
	}
	value, ok := response.Issue["target_session"].(string)
	require.True(t, ok)
	return value
}

func TestIssueParticipantHTTPRecipientUpdateAuthorityAndPresence(t *testing.T) {
	service, store, project, _ := issueParticipantHTTPService(t)
	creatorSession, first, second := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, actor := range []struct {
		name     string
		identity auth.Identity
	}{{"creator", auth.Client("read-write", "recipient-creator")}, {"operator", auth.Admin()}} {
		t.Run(actor.name, func(t *testing.T) {
			id := issueParticipantHTTPCreate(t, service, project, project, creatorSession, "", "recipient update fixture", "medium")
			before, comments, err := service.issueStore.GetIssue(context.Background(), id)
			require.NoError(t, err)
			for _, step := range []struct {
				name         string
				body         map[string]any
				want, status string
			}{
				{"set_without_status", map[string]any{"target_session": first}, first, "open"},
				{"omitted_preserves", map[string]any{"status": "resolved"}, first, "resolved"},
				{"change_without_status", map[string]any{"target_session": second}, second, "resolved"},
				{"explicit_empty_clears", map[string]any{"target_session": ""}, "", "resolved"},
			} {
				t.Run(step.name, func(t *testing.T) {
					updated := httptest.NewRecorder()
					service.handleUpdateIssue(updated, issueHTTPRouteRequest(t, http.MethodPatch, id, step.body, actor.identity))
					require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
					reloaded := &Service{store: store, issueStore: gormdb.NewIssueStore(store.GetDB())}
					assert.Equal(t, step.want, issueParticipantHTTPRecipient(t, reloaded, id))
					after, actualComments, err := reloaded.issueStore.GetIssue(context.Background(), id)
					require.NoError(t, err)
					assert.Equal(t, step.status, after.Status)
					assert.Equal(t, before.Title, after.Title)
					assert.Equal(t, before.Body, after.Body)
					assert.Equal(t, before.SourceProject, after.SourceProject)
					assert.Equal(t, before.TargetProject, after.TargetProject)
					assert.Equal(t, before.CreatorKeycardID, after.CreatorKeycardID)
					assert.Equal(t, creatorSession, after.CreatedBySession)
					assert.Equal(t, comments, actualComments)
				})
			}
		})
	}
}

func TestIssueParticipantHTTPRecipientUpdateDenialIsAtomic(t *testing.T) {
	service, _, project, _ := issueParticipantHTTPService(t)
	session, original, replacement := uuid.NewString(), uuid.NewString(), uuid.NewString()
	id := issueParticipantHTTPCreate(t, service, project, project, session, original, "recipient authority fixture", "medium")
	before, comments, err := service.issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	history := make([]gormdb.IssueHistoryEvent, 0)
	err = service.store.GetDB().Where("issue_id = ?", id).Order("created_at ASC, id ASC").Find(&history).Error
	require.NoError(t, err)
	for _, actor := range []struct {
		name     string
		identity auth.Identity
		missing  bool
	}{
		{"foreign_rw", auth.Client("read-write", "different-keycard"), false},
		{"readonly_creator", auth.Client("read-only", "recipient-creator"), false},
		{"missing_identity", auth.Identity{}, true},
	} {
		for index, body := range []map[string]any{
			{"target_session": replacement},
			{"target_session": ""},
			{"status": "resolved", "target_session": replacement, "comment": "must roll back"},
			{"status": "resolved", "target_session": "", "comment": "must roll back"},
		} {
			t.Run(fmt.Sprintf("%s_%d", actor.name, index), func(t *testing.T) {
				body["session_id"] = session
				body["participant_session"] = session
				request := issueHTTPRouteRequest(t, http.MethodPatch, id, body, actor.identity)
				if actor.missing {
					route := chi.NewRouteContext()
					route.URLParams.Add("id", fmt.Sprint(id))
					request = request.WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, route))
				}
				denied := httptest.NewRecorder()
				service.handleUpdateIssue(denied, request)
				require.Equal(t, http.StatusForbidden, denied.Code, denied.Body.String())
				assert.Equal(t, original, issueParticipantHTTPRecipient(t, service, id))
				after, actualComments, err := service.issueStore.GetIssue(context.Background(), id)
				require.NoError(t, err)
				actualHistory := make([]gormdb.IssueHistoryEvent, 0)
				err = service.store.GetDB().Where("issue_id = ?", id).Order("created_at ASC, id ASC").Find(&actualHistory).Error
				require.NoError(t, err)
				assert.Equal(t, before, after)
				assert.Equal(t, comments, actualComments)
				assert.Equal(t, history, actualHistory)
			})
		}
	}
}

func TestIssueParticipantHTTPFilterUnionAndPagination(t *testing.T) {
	service, _, project, other := issueParticipantHTTPService(t)
	const first = "b73a77bc-10a5-4d70-9f31-111111111111"
	const second = "b73a77bc-10a5-4d70-9f31-111111111112"
	recipient, otherRecipient := uuid.NewString(), uuid.NewString()
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
		ids[index] = issueParticipantHTTPCreate(t, service, row.from, row.to, row.source, row.target, fmt.Sprintf("HTTP participant corpus %d", index), row.priority)
	}
	for index := range 2 {
		comment := httptest.NewRecorder()
		service.handleUpdateIssue(comment, issueHTTPRouteRequest(t, http.MethodPatch, ids[1], map[string]any{
			"comment": fmt.Sprintf("authored comment %d", index), "source_project": project, "session_id": first, "client": "omp",
		}, auth.Client("read-write", "commenting-participant")))
		require.Equal(t, http.StatusOK, comment.Code, comment.Body.String())
	}
	comment := httptest.NewRecorder()
	service.handleUpdateIssue(comment, issueHTTPRouteRequest(t, http.MethodPatch, ids[9], map[string]any{
		"comment": "different full session", "source_project": project, "session_id": second, "client": "omp",
	}, auth.Client("read-write", "commenting-participant")))
	require.Equal(t, http.StatusOK, comment.Code, comment.Body.String())
	for _, step := range []struct {
		id              int64
		status, session string
	}{
		{ids[2], "resolved", first}, {ids[2], "reopened", second}, {ids[7], "resolved", second},
	} {
		updated := httptest.NewRecorder()
		service.handleUpdateIssue(updated, issueHTTPRouteRequest(t, http.MethodPatch, step.id, map[string]any{
			"status": step.status, "source_project": project, "session_id": step.session, "client": "omp",
		}, auth.Client("read-write", "lifecycle-participant")))
		require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
	}
	_, lifecycleComments, err := service.issueStore.GetIssue(context.Background(), ids[2])
	require.NoError(t, err)
	require.Empty(t, lifecycleComments)
	events := make([]gormdb.IssueHistoryEvent, 0)
	err = service.store.GetDB().Where("issue_id = ?", ids[2]).Order("created_at ASC, id ASC").Find(&events).Error
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, first, events[0].SessionID)

	for _, tc := range []struct {
		name   string
		filter map[string]string
		want   []int64
		total  int64
	}{
		{"creator_or_comment_or_history_once_each", nil, []int64{ids[0], ids[1], ids[2]}, 3},
		{"and_creator_session", map[string]string{"source_session": second}, []int64{ids[1], ids[2]}, 2},
		{"and_exact_creator_session", map[string]string{"source_session": first}, []int64{ids[0]}, 1},
		{"count_before_page", map[string]string{"limit": "1"}, []int64{ids[2]}, 3},
		{"offset_after_filter", map[string]string{"limit": "1", "offset": "1"}, []int64{ids[1]}, 3},
		{"exhausted_page_keeps_count", map[string]string{"limit": "1", "offset": "3"}, nil, 3},
		{"recipient_is_not_participation", map[string]string{"target_session": ""}, []int64{ids[0], ids[1], ids[2], ids[5]}, 4},
		{"exact_second_uuid", map[string]string{"participant_session": second}, []int64{ids[1], ids[2], ids[4], ids[9]}, 4},
		{"unknown_participant_without_liveness", map[string]string{"participant_session": uuid.NewString()}, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := url.Values{
				"project": {project}, "source_project": {project}, "status": {"open,reopened"},
				"participant_session": {first}, "target_session": {recipient}, "limit": {"100"},
			}
			for key, value := range tc.filter {
				if key == "target_session" && value == "" {
					query.Del(key)
				} else {
					query.Set(key, value)
				}
			}
			request := issueHTTPRouteRequest(t, http.MethodGet, 0, nil, auth.Client("read-only", "unrelated-reader"))
			request.URL.RawQuery = query.Encode()
			read := httptest.NewRecorder()
			service.handleListIssues(read, request)
			require.Equal(t, http.StatusOK, read.Code, read.Body.String())
			var response struct {
				Issues []struct {
					ID int64 `json:"id"`
				} `json:"issues"`
				Total int64 `json:"total"`
			}
			require.NoError(t, json.Unmarshal(read.Body.Bytes(), &response))
			var actual []int64
			for _, issue := range response.Issues {
				actual = append(actual, issue.ID)
			}
			assert.Equal(t, tc.total, response.Total, "OR participation must be counted once per issue before pagination")
			assert.ElementsMatch(t, tc.want, actual)
		})
	}
}

func TestIssueParticipantHTTPReadSelectorValidation(t *testing.T) {
	service, _, project, _ := issueParticipantHTTPService(t)
	issueParticipantHTTPCreate(t, service, project, project, uuid.NewString(), "", "must not leak from invalid selector", "medium")
	for _, field := range []string{"participant_session", "source_session", "target_session"} {
		for index, value := range []string{" ", "bad\nselector", "bad\x00selector", "bad\x7fselector", uuid.Nil.String(), strings.Repeat("s", 16384)} {
			t.Run(fmt.Sprintf("%s_invalid_%d", field, index), func(t *testing.T) {
				query := url.Values{"project": {project}, field: {value}}
				request := issueHTTPRouteRequest(t, http.MethodGet, 0, nil, auth.Client("read-only", "reader"))
				request.URL.RawQuery = query.Encode()
				read := httptest.NewRecorder()
				service.handleListIssues(read, request)
				require.Equal(t, http.StatusBadRequest, read.Code, "invalid selectors must fail rather than broaden: %s", read.Body.String())
				assert.NotContains(t, read.Body.String(), "must not leak from invalid selector")
			})
		}
	}
}
