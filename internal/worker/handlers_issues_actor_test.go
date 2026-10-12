package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/gorm"
)

const issueActorTestProject = "26ebc5e4-c18b-4f41-8828-14bc946a6e10"

func TestIssueHumanMutationSourceBoundary(t *testing.T) {
	for _, identity := range []auth.Identity{
		auth.Session("admin"),
		auth.SessionForBrowserUser("admin", 41),
		auth.ClientWithPrincipal("read-write", "keycard", "alice", auth.PrincipalKindHuman),
	} {
		ctx := auth.WithIdentity(context.Background(), identity)
		for _, source := range []struct{ project, agent string }{
			{"", ""},
			{"", "human"},
			{"dashboard", "human"},
			{strings.ToUpper(issueActorTestProject), "human"},
			{" " + issueActorTestProject, "human"},
			{issueActorTestProject, "agent"},
			{issueActorTestProject, " human "},
		} {
			require.ErrorIs(t, validateIssueMutationSource(ctx, source.project, source.agent), gormdb.ErrIssueInvalidInput)
		}
		require.NoError(t, validateIssueMutationSource(ctx, issueActorTestProject, "human"))
	}
	for _, identity := range []auth.Identity{auth.Admin(), auth.AuthDisabled(), auth.Client("read-write", "keycard"), auth.ClientWithPrincipal("read-write", "service", "automation", auth.PrincipalKindService)} {
		ctx := auth.WithIdentity(context.Background(), identity)
		require.NoError(t, validateIssueMutationSource(ctx, "", ""), "global producer semantics must remain unchanged")
		require.ErrorIs(t, validateIssueMutationSource(ctx, "dashboard", "human"), gormdb.ErrIssueInvalidInput)
	}
}

func TestIssueHumanAckDeleteRejectUnboundBeforeStore(t *testing.T) {
	service := &Service{}
	for _, body := range []map[string]any{
		{"ids": []int64{1}},
		{"ids": []int64{1}, "source_project": "dashboard", "source_agent": "human"},
		{"ids": []int64{1}, "source_project": issueActorTestProject, "source_agent": "agent"},
		{"ids": []int64{1}, "source_project": issueActorTestProject, "source_agent": "human", "unexpected": true},
	} {
		w := httptest.NewRecorder()
		service.handleAcknowledgeIssues(w, issueHTTPRouteRequest(t, http.MethodPost, 0, body, auth.Session("admin")))
		require.Equal(t, http.StatusBadRequest, w.Code)
	}
	for _, query := range []string{"", "?source_project=dashboard&source_agent=human", "?source_project=" + issueActorTestProject + "&source_agent=agent", "?source_project=" + issueActorTestProject + "&source_agent=human&source_agent=human"} {
		r := issueHTTPRouteRequest(t, http.MethodDelete, 1, nil, auth.Session("admin"))
		r.URL.RawQuery = strings.TrimPrefix(query, "?")
		w := httptest.NewRecorder()
		service.handleDeleteIssue(w, r)
		require.Equal(t, http.StatusBadRequest, w.Code)
	}
}

func TestIssueMutationAuditKeepsActingSourceSeparate(t *testing.T) {
	ctx := auth.WithIdentity(context.Background(), auth.SessionForBrowserUser("admin", 41))
	entry := issueMutationAudit(ctx, "delete", issueActorTestProject, "human", []int64{7})
	require.NotNil(t, entry)
	require.Nil(t, entry.MemoryID)
	require.Equal(t, "issue.delete", entry.Action)
	var state map[string]any
	require.NoError(t, json.Unmarshal(*entry.AfterState, &state))
	require.Equal(t, issueActorTestProject, state["source_project"])
	require.Equal(t, "human", state["source_agent"])
	require.Equal(t, []any{float64(7)}, state["issue_ids"])
	require.Nil(t, issueMutationAudit(auth.WithIdentity(context.Background(), auth.Admin()), "delete", "", "", []int64{7}))
}

func TestIssueSelectionActorEnvelopeBinding(t *testing.T) {
	service := &Service{store: &gormdb.Store{}, issueStore: gormdb.NewIssueStore(nil)}
	identity := auth.SessionForBrowserUser("admin", 41)
	var scopes []gormdb.CollectionSelectionScope
	for _, project := range []string{issueActorTestProject, "401bec6c-22e8-4a60-8caf-f8e469631ada"} {
		body := map[string]any{"source_project": project, "source_agent": "human"}
		request := issueSelectionHTTPRequest(t, "/api/issues/selection/current", body, identity, "actor-session", "actor-read")
		w := httptest.NewRecorder()
		var decoded issueSelectionCurrentRequest
		_, scope, _, ok := service.issueSelectionRequestScope(w, request, &decoded)
		require.True(t, ok, w.Body.String())
		scopes = append(scopes, scope)
	}
	require.NotEqual(t, scopes[0].ContextFingerprint, scopes[1].ContextFingerprint)
	filter, err := normalizeIssueSelectionFilter(&issueSelectionFilter{SourceProject: issueActorTestProject, SourceAgent: "human"})
	require.NoError(t, err)
	rowFingerprint := filter.collectionFilter().Fingerprint
	require.NotEqual(t, issueSelectionActorFilterFingerprint(rowFingerprint, scopes[0].ContextFingerprint), issueSelectionActorFilterFingerprint(rowFingerprint, scopes[1].ContextFingerprint))
	ctx := auth.WithIdentity(context.Background(), identity)
	_, _, err = issueSelectionSource(ctx, &issueSelectionPageRequest{SourceProject: scopes[1].Domain, SourceAgent: "human"})
	require.Error(t, err)
	_, _, err = issueSelectionSource(ctx, &issueSelectionPageRequest{SourceProject: "401bec6c-22e8-4a60-8caf-f8e469631ada", SourceAgent: "human", Filter: &filter})
	require.ErrorIs(t, err, gormdb.ErrCollectionSelectionInvalid)
	for _, target := range []any{&issueSelectionCurrentRequest{}, &issueSelectionPageRequest{}, &issueSelectionSnapshotRequest{}, &issueSelectionOperationRequest{}} {
		_, _, err = issueSelectionSource(ctx, target)
		require.ErrorIs(t, err, gormdb.ErrIssueInvalidInput)
	}
	for _, field := range []string{"project", "source_project"} {
		var invalid issueSelectionFilter
		if field == "project" {
			invalid.Project = "dashboard"
		} else {
			invalid.SourceProject = "dashboard"
		}
		_, err = normalizeIssueSelectionFilter(&invalid)
		require.ErrorIs(t, err, gormdb.ErrCollectionSelectionInvalid)
	}
}

func TestIssueHumanHTTPMutationAuditAtomicity(t *testing.T) {
	service, store := newIssueHTTPTestService(t)
	service.store = store
	ctx := context.Background()
	identity := auth.SessionForBrowserUser("admin", 41)
	id := createIssueHTTPFixture(t, store, service.issueStore, "historical-original-source", "original-keycard", "open")
	commentID, err := service.issueStore.AddComment(ctx, id, &gormdb.IssueComment{AuthorProject: "historical-comment-source", AuthorAgent: "original-agent", Body: "retained history"})
	require.NoError(t, err)
	t.Cleanup(func() {
		store.GetDB().Exec("DELETE FROM audit_log WHERE action LIKE 'issue.%' AND EXISTS (SELECT 1 FROM jsonb_array_elements_text(after_state->'issue_ids') AS audit_ids(value) WHERE value::bigint = ?)", id)
	})
	before, comments, err := service.issueStore.GetIssue(ctx, id)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	secondID := createIssueHTTPFixture(t, store, service.issueStore, "historical-original-source", "original-keycard", "open")
	sessionID := fmt.Sprintf("issue-actor-%d", id)
	t.Cleanup(func() {
		store.GetDB().Exec("DELETE FROM collection_selections WHERE subject_user_id = ? AND session_id = ? AND domain = ?", 41, sessionID, issueSelectionDomain)
	})
	snapshotBody := map[string]any{"selection": map[string]any{"kind": "explicit", "targets": []map[string]any{{"id": fmt.Sprint(id)}, {"id": fmt.Sprint(secondID)}}}}
	snapshot := issueSelectionCall(t, service.HandleIssueSelectionSnapshot, "/api/issues/selection", snapshotBody, identity, sessionID, "actor-snapshot")
	require.Equal(t, http.StatusOK, snapshot.Code, snapshot.Body.String())
	var saved issueSelectionSnapshotResponse
	require.NoError(t, json.Unmarshal(snapshot.Body.Bytes(), &saved))
	bulkBody := map[string]any{
		"request_id": "actor-bulk", "action": "labels", "labels": []string{"must-roll-back"},
		"selection":      map[string]any{"kind": "explicit", "selection_version": saved.Selection.Version},
		"source_project": "401bec6c-22e8-4a60-8caf-f8e469631ada", "source_agent": "human",
	}
	replay := httptest.NewRecorder()
	service.HandleIssueSelectionOperation(replay, issueSelectionHTTPRequest(t, "/api/issues/operations", bulkBody, identity, sessionID, "actor-bulk"))
	require.Equal(t, http.StatusConflict, replay.Code, replay.Body.String())
	require.Empty(t, replay.Body.String(), "actor mismatch must disclose no selected rows")
	snapshot = issueSelectionCall(t, service.HandleIssueSelectionSnapshot, "/api/issues/selection", snapshotBody, identity, sessionID, "actor-resnapshot")
	require.Equal(t, http.StatusOK, snapshot.Code, snapshot.Body.String())
	require.NoError(t, json.Unmarshal(snapshot.Body.Bytes(), &saved))
	bulkBody["selection"] = map[string]any{"kind": "explicit", "selection_version": saved.Selection.Version}
	callback := fmt.Sprintf("issue_actor_audit_fail_%d", id)
	callbacks := store.GetDB().Callback().Create()
	require.NoError(t, callbacks.Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_log" {
			tx.AddError(errors.New("injected issue audit failure"))
		}
	}))
	t.Cleanup(func() { _ = callbacks.Remove(callback) })
	bulk := issueSelectionCall(t, service.HandleIssueSelectionOperation, "/api/issues/operations", bulkBody, identity, sessionID, "actor-bulk")
	require.Equal(t, http.StatusInternalServerError, bulk.Code, bulk.Body.String())
	require.Empty(t, bulk.Body.String(), "rolled-back bulk must not expose readback")
	for _, targetID := range []int64{id, secondID} {
		row, _, readErr := service.issueStore.GetIssue(ctx, targetID)
		require.NoError(t, readErr)
		require.NotContains(t, row.Labels, "must-roll-back", "audit failure rolls back every selected row")
	}
	actor := map[string]any{"ids": []int64{id}, "source_project": issueActorTestProject, "source_agent": "human"}
	w := httptest.NewRecorder()
	service.handleAcknowledgeIssues(w, issueHTTPRouteRequest(t, http.MethodPost, 0, actor, identity))
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	after, retained, err := service.issueStore.GetIssue(ctx, id)
	require.NoError(t, err)
	require.Equal(t, before.Status, after.Status)
	require.True(t, before.UpdatedAt.Equal(after.UpdatedAt))
	require.Equal(t, commentID, retained[0].ID)
	deleteRequest := issueHTTPRouteRequest(t, http.MethodDelete, id, nil, identity)
	deleteRequest.URL.RawQuery = "source_project=" + issueActorTestProject + "&source_agent=human"
	w = httptest.NewRecorder()
	service.handleDeleteIssue(w, deleteRequest)
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	_, retained, err = service.issueStore.GetIssue(ctx, id)
	require.NoError(t, err)
	require.Equal(t, commentID, retained[0].ID, "failed delete audit must roll back comment deletion")
	require.NoError(t, callbacks.Remove(callback))
	bulk = issueSelectionCall(t, service.HandleIssueSelectionOperation, "/api/issues/operations", bulkBody, identity, sessionID, "actor-bulk")
	require.Equal(t, http.StatusOK, bulk.Code, bulk.Body.String())
	var committed issueSelectionOperationResponse
	require.NoError(t, json.Unmarshal(bulk.Body.Bytes(), &committed))
	require.Len(t, committed.ItemResults, 2)
	require.Len(t, committed.Readback.CurrentState, 2)
	require.ElementsMatch(t, []int64{id, secondID}, []int64{committed.ItemResults[0].TargetID, committed.ItemResults[1].TargetID})
	for _, row := range committed.Readback.CurrentState {
		require.Equal(t, []string{"must-roll-back"}, row.Labels)
	}
	var bulkAudits []gormdb.AuditLogEntry
	require.NoError(t, store.GetDB().Where("action = ? AND after_state->'issue_ids' = ?::jsonb", "issue.labels", fmt.Sprintf("[%d,%d]", id, secondID)).Find(&bulkAudits).Error)
	require.Len(t, bulkAudits, 1)
	require.Equal(t, "session:human:browser-user/41", bulkAudits[0].Actor)
	w = httptest.NewRecorder()
	service.handleAcknowledgeIssues(w, issueHTTPRouteRequest(t, http.MethodPost, 0, actor, identity))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	after, retained, err = service.issueStore.GetIssue(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "acknowledged", after.Status)
	require.Equal(t, before.SourceProject, after.SourceProject)
	require.Equal(t, before.TargetProject, after.TargetProject)
	require.Equal(t, before.SourceAgent, after.SourceAgent)
	require.Equal(t, comments, retained)
	var entries []gormdb.AuditLogEntry
	require.NoError(t, store.GetDB().Where("action = ? AND after_state->'issue_ids' = ?::jsonb", "issue.acknowledge", fmt.Sprintf("[%d]", id)).Find(&entries).Error)
	require.Len(t, entries, 1)
	require.Equal(t, "session:human:browser-user/41", entries[0].Actor)
	var attribution map[string]any
	require.NoError(t, json.Unmarshal(*entries[0].AfterState, &attribution))
	require.Equal(t, issueActorTestProject, attribution["source_project"])
	require.Equal(t, "human", attribution["source_agent"])
	w = httptest.NewRecorder()
	service.handleDeleteIssue(w, deleteRequest)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	_, _, err = service.issueStore.GetIssue(ctx, id)
	require.Error(t, err)
	entries = nil
	require.NoError(t, store.GetDB().Where("action = ? AND after_state->'issue_ids' = ?::jsonb", "issue.delete", fmt.Sprintf("[%d]", id)).Find(&entries).Error)
	require.Len(t, entries, 1, "deletion attribution survives removal of original issue/comments")
}
