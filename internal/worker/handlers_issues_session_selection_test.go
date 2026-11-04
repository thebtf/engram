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

func issueSessionSelectionHTTPService(t *testing.T) (*Service, *gormdb.Store, string, string) {
	t.Helper()
	store := openWorkerUCIContextCompositionStore(t)
	project := seedWorkerTestProject(t, store.GetDB(), "session recipient source")
	other := seedWorkerTestProject(t, store.GetDB(), "session recipient other target")
	t.Cleanup(func() {
		require.NoError(t, store.GetDB().Exec("DELETE FROM issue_history_events WHERE issue_id IN (SELECT id FROM issues WHERE source_project = ?)", project).Error)
		require.NoError(t, store.GetDB().Exec("DELETE FROM issue_comments WHERE issue_id IN (SELECT id FROM issues WHERE source_project = ?)", project).Error)
		require.NoError(t, store.GetDB().Exec("DELETE FROM issues WHERE source_project = ?", project).Error)
	})
	return &Service{store: store, issueStore: gormdb.NewIssueStore(store.GetDB())}, store, project, other
}

func TestIssueSessionSelectionHTTPRoundTripAndFilters(t *testing.T) {
	service, store, project, other := issueSessionSelectionHTTPService(t)
	identity := auth.Client("read-write", "session-selection-owner")
	const first = "01a1a365-f562-75dc-a577-53db1b92693d"
	const second = "01a1a365-f562-75dc-a577-53db1b92693e"
	rows := []struct{ source, target, project, priority, status string }{
		{first, first, project, "low", "open"},
		{second, first, project, "critical", "open"},
		{first, second, project, "high", "open"},
		{second, second, project, "medium", "open"},
		{first, first, project, "critical", "resolved"},
		{first, first, other, "critical", "open"},
		{"", "", project, "medium", "open"},
		{first, "", project, "medium", "open"},
	}
	ids := make([]int64, len(rows))
	for index, row := range rows {
		body := map[string]any{
			"title": fmt.Sprintf("HTTP recipient fixture %d", index), "body": "unchanged HTTP user body",
			"source_project": project, "target_project": row.project, "priority": row.priority,
			"created_by_session": row.source, "client": "omp",
		}
		if row.target != "" {
			body["target_session"] = row.target
		}
		created := httptest.NewRecorder()
		service.handleCreateIssue(created, issueHTTPRouteRequest(t, http.MethodPost, 0, body, identity))
		require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
		var response struct {
			ID int64 `json:"id"`
		}
		require.NoError(t, json.Unmarshal(created.Body.Bytes(), &response))
		require.Positive(t, response.ID)
		ids[index] = response.ID
		if row.status != "open" {
			require.NoError(t, service.issueStore.UpdateIssueStatus(context.Background(), response.ID, row.status))
		}
	}

	// Replace the service object so an input echo cannot stand in for persistence.
	reloaded := &Service{store: store, issueStore: gormdb.NewIssueStore(store.GetDB())}
	for index, row := range rows {
		read := httptest.NewRecorder()
		reloaded.handleGetIssue(read, issueHTTPRouteRequest(t, http.MethodGet, ids[index], nil, identity))
		require.Equal(t, http.StatusOK, read.Code, read.Body.String())
		var response struct {
			Issue map[string]any `json:"issue"`
		}
		require.NoError(t, json.Unmarshal(read.Body.Bytes(), &response))
		assert.Equal(t, "unchanged HTTP user body", response.Issue["body"])
		assert.Equal(t, row.source, response.Issue["created_by_session"])
		assert.Equal(t, row.project, response.Issue["target_project"])
		if row.target != "" {
			assert.Equal(t, row.target, response.Issue["target_session"])
		} else {
			assert.True(t, response.Issue["target_session"] == nil || response.Issue["target_session"] == "")
		}
	}

	for _, tc := range []struct {
		name   string
		filter map[string]string
		want   []int64
		total  int64
	}{
		{"default_project_keeps_every_session", nil, []int64{ids[0], ids[1], ids[2], ids[3], ids[4], ids[6], ids[7]}, 7},
		{"recipient_only", map[string]string{"target_session": first, "status": "open"}, []int64{ids[0], ids[1]}, 2},
		{"creator_only", map[string]string{"source_session": first, "status": "open"}, []int64{ids[0], ids[2], ids[7]}, 3},
		{"full_second_recipient_not_shared_prefix", map[string]string{"target_session": second, "status": "open"}, []int64{ids[2], ids[3]}, 2},
		{"intersection_before_pagination", map[string]string{"target_session": first, "source_session": first, "status": "open", "limit": "1"}, []int64{ids[0]}, 1},
		{"offset_after_intersection", map[string]string{"target_session": first, "source_session": first, "status": "open", "limit": "1", "offset": "1"}, nil, 1},
		{"status_composes_with_sessions", map[string]string{"target_session": first, "source_session": first, "status": "resolved"}, []int64{ids[4]}, 1},
		{"unknown_recipient_does_not_require_liveness", map[string]string{"target_session": uuid.NewString()}, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := url.Values{"project": {project}, "source_project": {project}, "limit": {"100"}}
			for key, value := range tc.filter {
				query.Set(key, value)
			}
			request := issueHTTPRouteRequest(t, http.MethodGet, 0, nil, identity)
			request.URL.RawQuery = query.Encode()
			read := httptest.NewRecorder()
			reloaded.handleListIssues(read, request)
			require.Equal(t, http.StatusOK, read.Code, read.Body.String())
			var response struct {
				Issues []map[string]json.RawMessage `json:"issues"`
				Total  int64                        `json:"total"`
			}
			require.NoError(t, json.Unmarshal(read.Body.Bytes(), &response))
			assert.Equal(t, tc.total, response.Total, "count must compose project, status and session filters before pagination")
			var actual []int64
			for _, issue := range response.Issues {
				var id int64
				require.NoError(t, json.Unmarshal(issue["id"], &id))
				actual = append(actual, id)
				if recipient := tc.filter["target_session"]; recipient != "" {
					var reported string
					require.NoError(t, json.Unmarshal(issue["target_session"], &reported))
					assert.Equal(t, recipient, reported)
				}
			}
			assert.ElementsMatch(t, tc.want, actual)
		})
	}
}

func TestIssueSessionSelectionHTTPRecipientDoesNotAuthorize(t *testing.T) {
	service, _, project, _ := issueSessionSelectionHTTPService(t)
	session := uuid.NewString()
	owner := auth.Client("read-write", "session-selection-owner")
	created := httptest.NewRecorder()
	service.handleCreateIssue(created, issueHTTPRouteRequest(t, http.MethodPost, 0, map[string]any{
		"title": "recipient is not authority", "source_project": project, "target_project": project,
		"target_session": session, "created_by_session": session,
	}, owner))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var response struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &response))
	before, comments, err := service.issueStore.GetIssue(context.Background(), response.ID)
	require.NoError(t, err)
	foreignEdit := httptest.NewRecorder()
	service.handleUpdateIssue(foreignEdit, issueHTTPRouteRequest(t, http.MethodPatch, response.ID, map[string]any{
		"title": "recipient must not acquire creator authority", "source_project": project,
		"session_id": session, "target_session": session,
	}, auth.Client("read-write", "different-recipient-keycard")))
	require.Equal(t, http.StatusForbidden, foreignEdit.Code, foreignEdit.Body.String())
	for _, authenticated := range []bool{false, true} {
		request := issueHTTPRouteRequest(t, http.MethodPatch, response.ID, map[string]any{
			"status": "resolved", "comment": "must not be committed", "source_project": project,
			"session_id": session, "target_session": session,
		}, auth.Client("read-only", "session-selection-owner"))
		if !authenticated {
			route := chi.NewRouteContext()
			route.URLParams.Add("id", fmt.Sprint(response.ID))
			request = request.WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, route))
		}
		denied := httptest.NewRecorder()
		service.handleUpdateIssue(denied, request)
		require.Equal(t, http.StatusForbidden, denied.Code, denied.Body.String())

		createRequest := issueHTTPRouteRequest(t, http.MethodPost, 0, map[string]any{
			"title": "denied recipient create", "source_project": project, "target_project": project,
			"target_session": session, "created_by_session": session,
		}, auth.Client("read-only", "session-selection-owner"))
		if !authenticated {
			createRequest = createRequest.WithContext(context.Background())
		}
		deniedCreate := httptest.NewRecorder()
		service.handleCreateIssue(deniedCreate, createRequest)
		require.Equal(t, http.StatusForbidden, deniedCreate.Code, deniedCreate.Body.String())
	}
	after, actualComments, err := service.issueStore.GetIssue(context.Background(), response.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Equal(t, comments, actualComments)
	issues, total, err := service.issueStore.ListIssuesEx(context.Background(), gormdb.IssueListParams{SourceProject: project})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Len(t, issues, 1)
}

func TestIssueSessionSelectionHTTPOptionalProvenanceCompatibility(t *testing.T) {
	service, _, project, _ := issueSessionSelectionHTTPService(t)
	for index, tc := range []struct{ session, client, want string }{
		{"conversation-20261104_alpha.17", "openclaw", "conversation-20261104_alpha.17"},
		{uuid.Nil.String(), "openclaw", ""},
		{"bad\nconversation", "openclaw", ""},
		{strings.Repeat("s", 16384), "openclaw", ""},
		{"", "future-host-client", ""},
	} {
		t.Run(fmt.Sprintf("metadata_%d", index), func(t *testing.T) {
			created := httptest.NewRecorder()
			service.handleCreateIssue(created, issueHTTPRouteRequest(t, http.MethodPost, 0, map[string]any{
				"title": "optional provenance", "source_project": project, "target_project": project,
				"created_by_session": tc.session, "client": tc.client,
			}, auth.Client("read-write", "compatibility-owner")))
			require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
			var response struct {
				ID int64 `json:"id"`
			}
			require.NoError(t, json.Unmarshal(created.Body.Bytes(), &response))
			read := httptest.NewRecorder()
			service.handleGetIssue(read, issueHTTPRouteRequest(t, http.MethodGet, response.ID, nil, auth.Client("read-only", "reader")))
			require.Equal(t, http.StatusOK, read.Code, read.Body.String())
			var detail struct {
				Issue struct {
					Session string `json:"created_by_session"`
					Source  string `json:"created_by_session_source"`
				} `json:"issue"`
			}
			require.NoError(t, json.Unmarshal(read.Body.Bytes(), &detail))
			assert.Equal(t, tc.want, detail.Issue.Session)
			if tc.want == "" {
				assert.Empty(t, detail.Issue.Source)
			} else {
				assert.Equal(t, "reported", detail.Issue.Source)
			}
		})
	}
}