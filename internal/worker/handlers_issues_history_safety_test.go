package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestIssueHistoryHTTPBoundedContinuationAndReadBoundary(t *testing.T) {
	store := openWorkerUCIContextCompositionStore(t)
	service := &Service{store: store, issueStore: gormdb.NewIssueStore(store.GetDB())}
	project := seedWorkerTestProject(t, store.GetDB(), "history reader")
	foreign := seedWorkerTestProject(t, store.GetDB(), "other parent issue")
	id := createIssueHTTPFixture(t, store, service.issueStore, project, "history-owner", "open")
	otherID := createIssueHTTPFixture(t, store, service.issueStore, foreign, "other-owner", "open")
	const session = "01a1a369-dfce-7241-9330-2a0a3f697f9c"
	const commentBody = "User comment remains separate from lifecycle events."
	_, err := service.issueStore.AddComment(context.Background(), id, &gormdb.IssueComment{
		AuthorProject: project, Body: commentBody,
	})
	require.NoError(t, err)
	_, beforeComments, err := service.issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)

	// Timestamps deliberately run backwards. Continuation is exclusive by ID,
	// not by wall-clock timestamp, and foreign-parent IDs create a real gap.
	events := make([]gormdb.IssueHistoryEvent, 103)
	for index := range events {
		if index == 51 {
			require.NoError(t, store.GetDB().Create(&gormdb.IssueHistoryEvent{
				IssueID: otherID, Status: "resolved", AuthorProject: project,
				SessionID: session, SessionSource: "reported", Client: "openclaw",
				CreatedAt: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC),
			}).Error)
		}
		events[index] = gormdb.IssueHistoryEvent{
			IssueID: id, Status: "resolved", AuthorProject: project,
			SessionID: session, SessionSource: "reported", Client: "omp",
			CreatedAt: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC).Add(-time.Duration(index) * time.Second),
		}
		if index == 0 {
			events[index].AuthorProject = ""
			events[index].AuthorAgent = ""
		}
		require.NoError(t, store.GetDB().Create(&events[index]).Error)
	}

	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	const token = "synthetic-history-reader-token"
	tokenAuth, err := NewTokenAuth(token)
	require.NoError(t, err)
	router := chi.NewRouter()
	router.Use(tokenAuth.Middleware)
	router.Get("/api/issues/{id}", service.handleGetIssue)
	request := func(issueID int64, query string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/issues/%d?%s", issueID, query), nil)
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		// Reported provenance must not replace the real authentication boundary.
		r.Header.Set("X-Session-ID", session)
		r.Header.Set("X-Client", "omp")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	type page struct {
		Events       []gormdb.IssueHistoryEvent `json:"history_events"`
		Comments     []gormdb.IssueComment      `json:"comments"`
		CommentCount int                        `json:"comment_count"`
		HasMore      *bool                      `json:"history_has_more"`
		NextAfterID  *int64                     `json:"history_next_after_id"`
	}
	read := func(query string) page {
		t.Helper()
		w := request(id, "project="+project+"&"+query, true)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var result page
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		require.NotNil(t, result.HasMore, "even an empty page needs honest pagination metadata")
		require.Equal(t, beforeComments, result.Comments, "events are not synthetic user comments")
		require.Equal(t, 1, result.CommentCount)
		return result
	}
	requireHistoryEqual := func(expected, actual []gormdb.IssueHistoryEvent) {
		t.Helper()
		require.Len(t, actual, len(expected))
		for index, want := range expected {
			got := actual[index]
			require.True(t, want.CreatedAt.Equal(got.CreatedAt), "event %d CreatedAt must retain the exact instant", want.ID)
			got.CreatedAt = want.CreatedAt
			require.Equal(t, want, got, "event %d non-time fields must remain unchanged", want.ID)
		}
	}

	var got []int64
	cursor := int64(0)
	for pageIndex, size := range []int{50, 50, 3} {
		query := ""
		if pageIndex > 0 {
			query = "history_after_id=" + strconv.FormatInt(cursor, 10)
		}
		result := read(query)
		require.Len(t, result.Events, size, "default history_limit is 50")
		requireHistoryEqual(events[len(got):len(got)+size], result.Events)
		for _, event := range result.Events {
			require.Greater(t, event.ID, cursor)
			require.Equal(t, id, event.IssueID)
			got = append(got, event.ID)
		}
		cursor = result.Events[len(result.Events)-1].ID
		require.Equal(t, pageIndex < 2, *result.HasMore)
		if *result.HasMore {
			require.NotNil(t, result.NextAfterID)
			require.Equal(t, cursor, *result.NextAfterID)
		} else {
			require.Nil(t, result.NextAfterID)
		}
	}
	want := make([]int64, len(events))
	for index := range events {
		want[index] = events[index].ID
	}
	require.Equal(t, want, got, "every parent event appears once, with no missing IDs")
	first := read("history_limit=1")
	requireHistoryEqual(events[:1], first.Events)
	require.Empty(t, first.Events[0].AuthorProject, "the reader must not backfill an unknown author")
	require.Equal(t, session, first.Events[0].SessionID, "JSON session_id survives the author_session storage rename")
	require.Equal(t, "reported", first.Events[0].SessionSource)
	require.Equal(t, "omp", first.Events[0].Client)
	maximum := read("history_limit=100")
	requireHistoryEqual(events[:100], maximum.Events)
	require.True(t, *maximum.HasMore)
	require.Equal(t, events[99].ID, *maximum.NextAfterID)
	empty := read("history_after_id=" + strconv.FormatInt(cursor, 10))
	require.Empty(t, empty.Events)
	require.False(t, *empty.HasMore)
	require.Nil(t, empty.NextAfterID)

	for _, query := range []string{
		"history_limit=0", "history_limit=101", "history_limit=1.5",
		"history_after_id=-1", "history_after_id=1.5", "history_after_id=9223372036854775808",
	} {
		w := request(id, "project="+project+"&"+query, true)
		require.Equal(t, http.StatusBadRequest, w.Code, query+": "+w.Body.String())
		require.NotContains(t, w.Body.String(), commentBody)
	}
	for _, issueID := range []int64{id, otherID} {
		w := request(issueID, "history_limit=1", false)
		require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		require.NotContains(t, w.Body.String(), "history_events")
		require.NotContains(t, w.Body.String(), commentBody)
	}
	// The foreign event names this reader's project/session, but only its parent
	// issue's real project roles may satisfy a scoped detail request.
	denied := request(otherID, "project="+project+"&history_limit=1", true)
	require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, denied.Code, denied.Body.String())
	require.NotContains(t, denied.Body.String(), "history_events")
	require.NotContains(t, denied.Body.String(), session)
	allowed := request(otherID, "project="+foreign+"&history_limit=1", true)
	require.Equal(t, http.StatusOK, allowed.Code, allowed.Body.String())

	// Read-only remains a reader: no new lifecycle event or comment is authored.
	readonly := issueHTTPRouteRequest(t, http.MethodGet, id, nil, auth.Client("read-only", "history-reader"))
	readonly.URL.RawQuery = "project=" + project + "&history_limit=1"
	w := httptest.NewRecorder()
	service.handleGetIssue(w, readonly)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var retained []gormdb.IssueHistoryEvent
	require.NoError(t, store.GetDB().Where("issue_id = ?", id).Order("id").Find(&retained).Error)
	requireHistoryEqual(events, retained)
}
