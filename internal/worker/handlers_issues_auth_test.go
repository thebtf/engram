package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/gorm"
)

func newIssueHTTPTestService(t *testing.T) (*Service, *gormdb.Store) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping issue HTTP integration test")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return &Service{issueStore: gormdb.NewIssueStore(store.GetDB())}, store
}

func issueHTTPRouteRequest(t *testing.T, method string, id int64, body any, identity auth.Identity) *http.Request {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(method, "/api/issues/"+strconv.FormatInt(id, 10), bytes.NewReader(payload))
	ctx := auth.WithIdentity(req.Context(), identity)
	if id > 0 {
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", strconv.FormatInt(id, 10))
		ctx = context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
	}
	return req.WithContext(ctx)
}

func createIssueHTTPFixture(t *testing.T, store *gormdb.Store, issueStore *gormdb.IssueStore, project, keycard, status string) int64 {
	t.Helper()
	id, err := issueStore.CreateIssue(context.Background(), &gormdb.Issue{
		Title: "HTTP issue auth", SourceProject: project, TargetProject: project,
		Status: status, Type: "bug", Priority: "medium", CreatorKeycardID: keycard,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		store.GetDB().Exec(`DELETE FROM issue_comments WHERE issue_id = ?`, id)
		store.GetDB().Exec(`DELETE FROM issues WHERE id = ?`, id)
	})
	return id
}

func TestIssueHTTPKeycardOwnershipIgnoresClaimedProject(t *testing.T) {
	service, store := newIssueHTTPTestService(t)
	project := fmt.Sprintf("zz-http-issue-%d", time.Now().UnixNano())
	owner := auth.Client("read-write", "keycard-owner")
	create := issueHTTPRouteRequest(t, http.MethodPost, 0, map[string]any{
		"title": "bound", "source_project": project, "target_project": project,
	}, owner)
	createRec := httptest.NewRecorder()
	service.handleCreateIssue(createRec, create)
	require.Equal(t, http.StatusCreated, createRec.Code, createRec.Body.String())
	var created struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &created))
	t.Cleanup(func() {
		store.GetDB().Exec(`DELETE FROM issue_comments WHERE issue_id = ?`, created.ID)
		store.GetDB().Exec(`DELETE FROM issues WHERE id = ?`, created.ID)
	})

	issue, _, err := service.issueStore.GetIssue(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "keycard-owner", issue.CreatorKeycardID)

	foreign := issueHTTPRouteRequest(t, http.MethodPatch, created.ID, map[string]any{
		"status": "resolved", "source_project": project, "source_agent": "spoofed",
	}, auth.Client("read-write", "keycard-second"))
	foreignRec := httptest.NewRecorder()
	service.handleUpdateIssue(foreignRec, foreign)
	require.Equal(t, http.StatusForbidden, foreignRec.Code, foreignRec.Body.String())

	ownerReq := issueHTTPRouteRequest(t, http.MethodPatch, created.ID, map[string]any{"status": "resolved"}, owner)
	ownerRec := httptest.NewRecorder()
	service.handleUpdateIssue(ownerRec, ownerReq)
	require.Equal(t, http.StatusOK, ownerRec.Code, ownerRec.Body.String())
}

func TestIssueHTTPOperatorOnlyRoutesAndAtomicAcknowledge(t *testing.T) {
	service, store := newIssueHTTPTestService(t)
	project := fmt.Sprintf("zz-http-issue-operator-%d", time.Now().UnixNano())
	id := createIssueHTTPFixture(t, store, service.issueStore, project, "keycard-owner", "open")
	clientAdmin := auth.Identity{Role: auth.RoleAdmin, Source: auth.SourceClient, KeycardID: "keycard-owner"}

	for _, tc := range []struct {
		name   string
		method string
		body   any
		call   func(http.ResponseWriter, *http.Request)
	}{
		{"forced acknowledged", http.MethodPatch, map[string]any{"status": "acknowledged"}, service.handleUpdateIssue},
		{"reject", http.MethodPatch, map[string]any{"status": "rejected", "comment": "no"}, service.handleUpdateIssue},
		{"acknowledge", http.MethodPost, map[string]any{"ids": []int64{id}}, service.handleAcknowledgeIssues},
		{"delete", http.MethodDelete, map[string]any{}, service.handleDeleteIssue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, issueHTTPRouteRequest(t, tc.method, id, tc.body, clientAdmin))
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		})
	}

	operator := auth.Admin()
	invalidAck := httptest.NewRecorder()
	service.handleAcknowledgeIssues(invalidAck, issueHTTPRouteRequest(t, http.MethodPost, 0, map[string]any{"ids": []int64{id, 0}}, operator))
	require.Equal(t, http.StatusBadRequest, invalidAck.Code, invalidAck.Body.String())
	issue, _, err := service.issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "open", issue.Status, "invalid bulk acknowledgement must leave every issue unchanged")

	ack := httptest.NewRecorder()
	service.handleAcknowledgeIssues(ack, issueHTTPRouteRequest(t, http.MethodPost, 0, map[string]any{"ids": []int64{id}}, operator))
	require.Equal(t, http.StatusOK, ack.Code, ack.Body.String())
}

func TestIssueHTTPCloseWithCommentStorageFailureReturnsServerError(t *testing.T) {
	service, store := newIssueHTTPTestService(t)
	project := fmt.Sprintf("zz-http-issue-close-%d", time.Now().UnixNano())
	id := createIssueHTTPFixture(t, store, service.issueStore, project, "keycard-owner", "resolved")
	triggerName := fmt.Sprintf("engram_test_issue_comment_fail_%d", id)
	db := store.GetDB()
	t.Cleanup(func() {
		db.Exec("DROP TRIGGER IF EXISTS " + triggerName + " ON issue_comments")
		db.Exec("DROP FUNCTION IF EXISTS " + triggerName + "()")
	})
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.issue_id = %d THEN RAISE EXCEPTION 'injected issue comment failure'; END IF; RETURN NEW; END; $$`, triggerName, id)).Error)
	require.NoError(t, db.Exec("CREATE TRIGGER "+triggerName+" BEFORE INSERT ON issue_comments FOR EACH ROW EXECUTE FUNCTION "+triggerName+"()").Error)

	rec := httptest.NewRecorder()
	service.handleUpdateIssue(rec, issueHTTPRouteRequest(t, http.MethodPatch, id, map[string]any{
		"status": "closed", "comment": "must be atomic", "title": "must roll back", "source_project": project, "source_agent": "agent",
	}, auth.Admin()))
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	issue, comments, err := service.issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "resolved", issue.Status)
	require.Equal(t, "HTTP issue auth", issue.Title)
	require.Empty(t, comments)
}

func TestIssueHTTPCloseInvalidTransitionReturnsClientError(t *testing.T) {
	service, store := newIssueHTTPTestService(t)
	project := fmt.Sprintf("zz-http-issue-transition-%d", time.Now().UnixNano())
	id := createIssueHTTPFixture(t, store, service.issueStore, project, "keycard-owner", "open")
	rec := httptest.NewRecorder()
	service.handleUpdateIssue(rec, issueHTTPRouteRequest(t, http.MethodPatch, id, map[string]any{
		"status": "closed", "comment": "not allowed", "source_project": project,
	}, auth.Client("read-write", "keycard-owner")))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	issue, comments, err := service.issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "open", issue.Status)
	require.Empty(t, comments)
}

func TestIssueHTTPInvalidStatusRollsBackFieldEdits(t *testing.T) {
	service, store := newIssueHTTPTestService(t)
	project := fmt.Sprintf("zz-http-issue-invalid-status-%d", time.Now().UnixNano())
	id := createIssueHTTPFixture(t, store, service.issueStore, project, "keycard-owner", "open")
	rec := httptest.NewRecorder()
	service.handleUpdateIssue(rec, issueHTTPRouteRequest(t, http.MethodPatch, id, map[string]any{
		"status": "invalid", "title": "must roll back", "source_project": project,
	}, auth.Client("read-write", "keycard-owner")))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	issue, _, err := service.issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "HTTP issue auth", issue.Title)
	require.Equal(t, "open", issue.Status)
}

func TestIssueHTTPOwnerCannotForceOperatorStatuses(t *testing.T) {
	service, store := newIssueHTTPTestService(t)
	project := fmt.Sprintf("zz-http-issue-operator-status-%d", time.Now().UnixNano())
	id := createIssueHTTPFixture(t, store, service.issueStore, project, "keycard-owner", "open")
	owner := auth.Client("read-write", "keycard-owner")
	for _, status := range []string{"open", "acknowledged"} {
		rec := httptest.NewRecorder()
		service.handleUpdateIssue(rec, issueHTTPRouteRequest(t, http.MethodPatch, id, map[string]any{"status": status}, owner))
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestIssueHTTPAcknowledgeStorageFailureReturnsServerError(t *testing.T) {
	service, store := newIssueHTTPTestService(t)
	project := fmt.Sprintf("zz-http-issue-ack-storage-%d", time.Now().UnixNano())
	id := createIssueHTTPFixture(t, store, service.issueStore, project, "keycard-owner", "open")
	callbackName := fmt.Sprintf("engram_test_ack_query_failure_%d", id)
	queryCallbacks := store.GetDB().Callback().Query()
	require.NoError(t, queryCallbacks.Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		tx.AddError(errors.New("injected acknowledge query failure"))
	}))
	t.Cleanup(func() { _ = queryCallbacks.Remove(callbackName) })

	rec := httptest.NewRecorder()
	service.handleAcknowledgeIssues(rec, issueHTTPRouteRequest(t, http.MethodPost, 0, map[string]any{"ids": []int64{id}}, auth.Admin()))
	require.NoError(t, queryCallbacks.Remove(callbackName))
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
}
