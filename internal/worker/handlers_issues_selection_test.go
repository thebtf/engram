package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func newIssueSelectionHTTPTestService(t *testing.T) (*Service, *gormdb.Store) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	require.NotEmpty(t, dsn, "DATABASE_DSN is required for Issues PostgreSQL tests")
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return &Service{store: store, issueStore: gormdb.NewIssueStore(store.GetDB())}, store
}

func issueSelectionHTTPRequest(t *testing.T, path string, body any, identity auth.Identity, sessionID, requestID string) *http.Request {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Engram-Request-ID", requestID)
	request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: sessionID})
	return request.WithContext(auth.WithIdentity(request.Context(), identity))
}

func issueSelectionCall(t *testing.T, handler func(http.ResponseWriter, *http.Request), path string, body any, identity auth.Identity, sessionID, requestID string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler(recorder, issueSelectionHTTPRequest(t, path, body, identity, sessionID, requestID))
	return recorder
}

func TestIssueSelectionHandlers_OverOneHundredIssuesRemainBoundedAndTruthful(t *testing.T) {
	service, store := newIssueSelectionHTTPTestService(t)
	ctx := context.Background()
	project := fmt.Sprintf("issues-selection-handler-%d", time.Now().UnixNano())
	const sessionID = "issues-selection-handler-session"
	identity := auth.SessionForBrowserUser("admin", 41)

	ids := make([]int64, 101)
	for index := range ids {
		id, err := service.issueStore.CreateIssue(ctx, &gormdb.Issue{
			Title:            fmt.Sprintf("selection handler %03d", index),
			Body:             "must-not-appear-in-operation-result",
			SourceProject:    project,
			TargetProject:    project,
			CreatorKeycardID: "issues-selection-owner",
			Priority:         "medium",
			Type:             "task",
			Labels:           []string{"before"},
		})
		require.NoError(t, err)
		ids[index] = id
	}
	t.Cleanup(func() {
		require.NoError(t, store.GetDB().Exec("DELETE FROM collection_selections WHERE subject_user_id = ? AND session_id = ? AND domain = ?", 41, sessionID, issueSelectionDomain).Error)
		require.NoError(t, store.GetDB().Exec("DELETE FROM issue_comments WHERE issue_id IN ?", ids).Error)
		require.NoError(t, store.GetDB().Exec("DELETE FROM issues WHERE id IN ?", ids).Error)
	})

	pageOne := issueSelectionCall(t, service.HandleIssueSelectionPage, "/api/issues/selection/page", map[string]any{
		"filter": map[string]any{"project": project},
		"limit":  50,
	}, identity, sessionID, "issues-page-one")
	require.Equal(t, http.StatusOK, pageOne.Code, pageOne.Body.String())
	var firstPage issueSelectionPageResponse
	require.NoError(t, json.Unmarshal(pageOne.Body.Bytes(), &firstPage))
	require.Len(t, firstPage.Targets, 50)
	require.NotEmpty(t, firstPage.Cursor)
	require.NotEmpty(t, firstPage.NextCursor)
	require.EqualValues(t, 101, firstPage.Total)

	pageTwo := issueSelectionCall(t, service.HandleIssueSelectionPage, "/api/issues/selection/page", map[string]any{
		"cursor": firstPage.NextCursor,
		"limit":  50,
	}, identity, sessionID, "issues-page-two")
	require.Equal(t, http.StatusOK, pageTwo.Code, pageTwo.Body.String())
	var secondPage issueSelectionPageResponse
	require.NoError(t, json.Unmarshal(pageTwo.Body.Bytes(), &secondPage))
	require.Len(t, secondPage.Targets, 50)
	require.NotEqual(t, firstPage.Targets[0].ID, secondPage.Targets[0].ID)

	pageSnapshot := issueSelectionCall(t, service.HandleIssueSelectionSnapshot, "/api/issues/selection", map[string]any{
		"selection": map[string]any{"kind": "page", "cursor": firstPage.Cursor},
	}, identity, sessionID, "issues-page-snapshot")
	require.Equal(t, http.StatusOK, pageSnapshot.Code, pageSnapshot.Body.String())
	var pageSelection issueSelectionSnapshotResponse
	require.NoError(t, json.Unmarshal(pageSnapshot.Body.Bytes(), &pageSelection))
	require.Equal(t, gormdb.CollectionSelectionPage, pageSelection.Selection.Kind)
	require.Len(t, pageSelection.Selection.Targets, 50)

	pageCurrent := issueSelectionCall(t, service.HandleIssueSelectionCurrent, "/api/issues/selection/current", map[string]any{}, identity, sessionID, "issues-current-page")
	require.Equal(t, http.StatusOK, pageCurrent.Code, pageCurrent.Body.String())
	var currentSelection issueSelectionSnapshotResponse
	require.NoError(t, json.Unmarshal(pageCurrent.Body.Bytes(), &currentSelection))
	require.Equal(t, pageSelection.Selection.Version, currentSelection.Selection.Version)
	require.Equal(t, pageSelection.Selection.Targets, currentSelection.Selection.Targets)

	priority := "high"
	pageOperation := issueSelectionCall(t, service.HandleIssueSelectionOperation, "/api/issues/operations", map[string]any{
		"request_id": "issues-page-priority",
		"action":     "priority",
		"priority":   priority,
		"selection": map[string]any{
			"kind":              "page",
			"selection_version": pageSelection.Selection.Version,
		},
	}, identity, sessionID, "issues-page-priority")
	require.Equal(t, http.StatusOK, pageOperation.Code, pageOperation.Body.String())
	require.NotContains(t, pageOperation.Body.String(), "must-not-appear-in-operation-result")
	var pageResult issueSelectionOperationResponse
	require.NoError(t, json.Unmarshal(pageOperation.Body.Bytes(), &pageResult))
	require.Equal(t, "completed", pageResult.OperationState)
	require.Len(t, pageResult.ItemResults, 50)
	require.Len(t, pageResult.Readback.CurrentState, 50)
	for _, readback := range pageResult.Readback.CurrentState {
		require.Equal(t, priority, readback.Priority)
	}

	explicitID := secondPage.Targets[0].ID
	explicitSnapshot := issueSelectionCall(t, service.HandleIssueSelectionSnapshot, "/api/issues/selection", map[string]any{
		"selection": map[string]any{
			"kind":    "explicit",
			"targets": []map[string]any{{"id": explicitID}},
		},
	}, identity, sessionID, "issues-explicit-snapshot")
	require.Equal(t, http.StatusOK, explicitSnapshot.Code, explicitSnapshot.Body.String())
	var explicitSelection issueSelectionSnapshotResponse
	require.NoError(t, json.Unmarshal(explicitSnapshot.Body.Bytes(), &explicitSelection))
	require.Equal(t, gormdb.CollectionSelectionExplicit, explicitSelection.Selection.Kind)
	require.Len(t, explicitSelection.Selection.Targets, 1)
	require.NotZero(t, explicitSelection.Selection.Targets[0].ExpectedVersion)

	statusOperation := issueSelectionCall(t, service.HandleIssueSelectionOperation, "/api/issues/operations", map[string]any{
		"request_id": "issues-explicit-status",
		"action":     "status",
		"status":     "resolved",
		"selection": map[string]any{
			"kind":              "explicit",
			"selection_version": explicitSelection.Selection.Version,
		},
	}, identity, sessionID, "issues-explicit-status")
	require.Equal(t, http.StatusOK, statusOperation.Code, statusOperation.Body.String())

	frozenSnapshot := issueSelectionCall(t, service.HandleIssueSelectionSnapshot, "/api/issues/selection", map[string]any{
		"selection": map[string]any{
			"kind":         "frozen_filter",
			"filter":       map[string]any{"project": project},
			"excluded_ids": []string{explicitID},
		},
	}, identity, sessionID, "issues-frozen-snapshot")
	require.Equal(t, http.StatusOK, frozenSnapshot.Code, frozenSnapshot.Body.String())
	var frozenSelection issueSelectionSnapshotResponse
	require.NoError(t, json.Unmarshal(frozenSnapshot.Body.Bytes(), &frozenSelection))
	require.Equal(t, gormdb.CollectionSelectionFrozenFilter, frozenSelection.Selection.Kind)
	require.Equal(t, 100, frozenSelection.Selection.TargetCount)
	require.NotEmpty(t, frozenSelection.Selection.Token)

	labels := []string{"selected", "verified"}
	frozenOperation := issueSelectionCall(t, service.HandleIssueSelectionOperation, "/api/issues/operations", map[string]any{
		"request_id": "issues-frozen-labels",
		"action":     "labels",
		"labels":     labels,
		"selection": map[string]any{
			"kind":              "frozen_filter",
			"selection_version": frozenSelection.Selection.Version,
			"selection_token":   frozenSelection.Selection.Token,
		},
	}, identity, sessionID, "issues-frozen-labels")
	require.Equal(t, http.StatusOK, frozenOperation.Code, frozenOperation.Body.String())
	var frozenResult issueSelectionOperationResponse
	require.NoError(t, json.Unmarshal(frozenOperation.Body.Bytes(), &frozenResult))
	require.Len(t, frozenResult.ItemResults, 100)
	require.Len(t, frozenResult.Readback.CurrentState, 100)

	conflictSnapshot := issueSelectionCall(t, service.HandleIssueSelectionSnapshot, "/api/issues/selection", map[string]any{
		"selection": map[string]any{
			"kind":    "explicit",
			"targets": []map[string]any{{"id": firstPage.Targets[0].ID}, {"id": firstPage.Targets[1].ID}},
		},
	}, identity, sessionID, "issues-conflict-snapshot")
	require.Equal(t, http.StatusOK, conflictSnapshot.Code, conflictSnapshot.Body.String())
	var conflictSelection issueSelectionSnapshotResponse
	require.NoError(t, json.Unmarshal(conflictSnapshot.Body.Bytes(), &conflictSelection))
	secondConflictID, err := strconv.ParseInt(firstPage.Targets[1].ID, 10, 64)
	require.NoError(t, err)
	beforeSecond, _, err := service.issueStore.GetIssue(ctx, secondConflictID)
	require.NoError(t, err)
	firstConflictID, err := strconv.ParseInt(firstPage.Targets[0].ID, 10, 64)
	require.NoError(t, err)
	require.NoError(t, store.GetDB().Model(&gormdb.Issue{}).Where("id = ?", firstConflictID).Update("updated_at", time.Now().UTC().Add(time.Second)).Error)
	conflict := issueSelectionCall(t, service.HandleIssueSelectionOperation, "/api/issues/operations", map[string]any{
		"request_id": "issues-version-conflict",
		"action":     "labels",
		"labels":     []string{"must-not-commit"},
		"selection": map[string]any{
			"kind":              "explicit",
			"selection_version": conflictSelection.Selection.Version,
		},
	}, identity, sessionID, "issues-version-conflict")
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	require.Empty(t, strings.TrimSpace(conflict.Body.String()), "a rejected selection must not disclose targets or rows")
	afterSecond, _, err := service.issueStore.GetIssue(ctx, secondConflictID)
	require.NoError(t, err)
	require.Equal(t, beforeSecond.Labels, afterSecond.Labels)
	require.True(t, beforeSecond.UpdatedAt.Equal(afterSecond.UpdatedAt), "a stale target must roll back the entire selected operation")

	expiringSnapshot := issueSelectionCall(t, service.HandleIssueSelectionSnapshot, "/api/issues/selection", map[string]any{
		"selection": map[string]any{"kind": "frozen_filter", "filter": map[string]any{"project": project}},
	}, identity, sessionID, "issues-expiring-snapshot")
	require.Equal(t, http.StatusOK, expiringSnapshot.Code, expiringSnapshot.Body.String())
	var expiringSelection issueSelectionSnapshotResponse
	require.NoError(t, json.Unmarshal(expiringSnapshot.Body.Bytes(), &expiringSelection))
	require.NoError(t, store.GetDB().Exec("UPDATE collection_selections SET created_at = ?, frozen_expires_at = ?, reconfirmation_required = TRUE, reconfirmation_reason = ?, selection_version = selection_version + 1 WHERE subject_user_id = ? AND session_id = ? AND domain = ?", time.Now().UTC().Add(-2*time.Minute), time.Now().UTC().Add(-time.Minute), gormdb.CollectionSelectionReconfirmExpired, 41, sessionID, issueSelectionDomain).Error)
	expired := issueSelectionCall(t, service.HandleIssueSelectionOperation, "/api/issues/operations", map[string]any{
		"request_id": "issues-expired",
		"action":     "priority",
		"priority":   "low",
		"selection": map[string]any{
			"kind":              "frozen_filter",
			"selection_version": expiringSelection.Selection.Version,
			"selection_token":   expiringSelection.Selection.Token,
		},
	}, identity, sessionID, "issues-expired")
	require.Equal(t, http.StatusPreconditionFailed, expired.Code, expired.Body.String())
	require.Empty(t, strings.TrimSpace(expired.Body.String()))

	deniedSnapshot := issueSelectionCall(t, service.HandleIssueSelectionSnapshot, "/api/issues/selection", map[string]any{
		"selection": map[string]any{"kind": "explicit", "targets": []map[string]any{{"id": firstPage.Targets[2].ID}}},
	}, identity, sessionID, "issues-denied-snapshot")
	require.Equal(t, http.StatusOK, deniedSnapshot.Code, deniedSnapshot.Body.String())
	var deniedSelection issueSelectionSnapshotResponse
	require.NoError(t, json.Unmarshal(deniedSnapshot.Body.Bytes(), &deniedSelection))
	denied := issueSelectionCall(t, service.HandleIssueSelectionOperation, "/api/issues/operations", map[string]any{
		"request_id": "issues-access-lost",
		"action":     "priority",
		"priority":   "low",
		"selection": map[string]any{
			"kind":              "explicit",
			"selection_version": deniedSelection.Selection.Version,
		},
	}, auth.SessionForBrowserUser("member", 41), sessionID, "issues-access-lost")
	require.Equal(t, http.StatusForbidden, denied.Code, denied.Body.String())
	require.Empty(t, strings.TrimSpace(denied.Body.String()), "lost access must disclose no selected row")

	none := issueSelectionCall(t, service.HandleIssueSelectionSnapshot, "/api/issues/selection", map[string]any{
		"selection": map[string]any{"kind": "none"},
	}, identity, sessionID, "issues-none-snapshot")
	require.Equal(t, http.StatusOK, none.Code, none.Body.String())
	var noneSelection issueSelectionSnapshotResponse
	require.NoError(t, json.Unmarshal(none.Body.Bytes(), &noneSelection))
	require.Equal(t, gormdb.CollectionSelectionNone, noneSelection.Selection.Kind)

	unsupported := issueSelectionCall(t, service.HandleIssueSelectionOperation, "/api/issues/operations", map[string]any{
		"request_id": "issues-delete-forbidden",
		"action":     "delete",
		"selection":  map[string]any{"kind": "explicit", "selection_version": 1},
	}, identity, sessionID, "issues-delete-forbidden")
	require.Equal(t, http.StatusBadRequest, unsupported.Code, unsupported.Body.String())
}
