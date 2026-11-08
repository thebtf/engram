package worker

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	contract "github.com/thebtf/engram/internal/issues/contract"
)

func TestIssue513SessionProvenanceAndParticipantFilterPostgres(t *testing.T) {
	f := issue513T017Fixture(t)
	store := f.service.issueStore
	ctx := auth.WithIdentity(t.Context(), auth.SessionForBrowserUser("admin", f.browserUserID))
	actor, err := contract.CaptureActor(ctx)
	require.NoError(t, err)
	create := func(name, project string) int64 {
		t.Helper()
		body := fmt.Sprintf(`{"source_project":%q,"target_project":%q,"created_by_session":"creator-session","title":%q,"metadata":{"private":"not-in-list"}}`, project, project, name)
		return issue513CreatedID(t, f.call(t, f.browser, http.MethodPost, "/api/issues", body))
	}
	material := create("material", issue513Source)
	commented := create("commented", issue513Source)
	lifecycle := create("lifecycle", issue513Source)
	hidden := create("hidden", issue513OtherSource)
	reported := create("reported", issue513Source)
	selection := create("selection", issue513Source)
	bulk := create("bulk", issue513Source)
	scope := contract.Scope{SourceProject: issue513Source}
	mutate := func(id int64, command contract.MutationCommand) {
		t.Helper()
		_, err := store.MutateTaskIssue(ctx, actor, scope, id, command)
		require.NoError(t, err)
	}
	revision := "1"
	title := "material edited"
	mutate(reported, contract.MutationCommand{ExpectedRevision: &revision, Title: &title, SessionID: "explicit-session", CreatedBySession: "older-session"})
	mutate(material, contract.MutationCommand{ExpectedRevision: &revision, Title: &title, SessionID: "reported-session", CreatedBySession: "legacy-session", TransportSession: "transport-session"})
	body := "comment"
	mutate(commented, contract.MutationCommand{Comment: &body, SessionID: "comment-session"})
	status := "resolved"
	mutate(lifecycle, contract.MutationCommand{ExpectedRevision: &revision, Status: &status, SessionID: "lifecycle-session"})
	hiddenScope := contract.Scope{SourceProject: issue513OtherSource}
	_, err = store.MutateTaskIssue(ctx, actor, hiddenScope, hidden, contract.MutationCommand{ExpectedRevision: &revision, Title: &title, SessionID: "transport-session"})
	require.NoError(t, err)

	legacySession := "selection-session"
	priority := "high"
	_, err = store.ApplyIssueSelectionOperation(ctx, gormdb.IssueSelectionOperation{
		Action: gormdb.IssueSelectionAction{Kind: gormdb.IssueSelectionPriority, Priority: &priority},
		Actor:  gormdb.IssueSelectionActor{IsOperator: true}, Targets: issue513T017StoreTargets(t, f, selection),
		Attribution: gormdb.IssueChangeAttribution{Session: &legacySession},
	})
	require.NoError(t, err)
	bulkSession := "bulk-session"
	_, err = store.AcknowledgeIssuesAtomically(ctx, []int64{bulk}, nil, gormdb.IssueChangeAttribution{Session: &bulkSession})
	require.NoError(t, err)
	var changes []gormdb.IssueChange
	require.NoError(t, f.service.store.GetDB().Where("issue_id IN ? AND revision = 2", []int64{material, reported, selection, bulk}).Order("issue_id").Find(&changes).Error)
	require.Len(t, changes, 4)
	for _, change := range changes {
		expected := map[int64]string{material: "transport-session", reported: "explicit-session", selection: legacySession, bulk: bulkSession}[change.IssueID]
		t.Run("attribution-"+expected, func(t *testing.T) {
			require.NotNil(t, change.Attribution.Session)
			require.Equal(t, expected, *change.Attribution.Session)
		})
	}
	var materialComments, materialEvents int64
	require.NoError(t, f.service.store.GetDB().Model(&gormdb.IssueComment{}).Where("issue_id = ?", material).Count(&materialComments).Error)
	require.NoError(t, f.service.store.GetDB().Model(&gormdb.IssueHistoryEvent{}).Where("issue_id = ?", material).Count(&materialEvents).Error)
	require.Zero(t, materialComments)
	require.Zero(t, materialEvents)
	var events []gormdb.IssueHistoryEvent
	require.NoError(t, f.service.store.GetDB().Where("issue_id = ?", bulk).Find(&events).Error)
	require.Len(t, events, 1)
	require.Equal(t, bulkSession, events[0].SessionID)
	require.Equal(t, "reported", events[0].SessionSource)
	for _, tc := range []struct {
		session  string
		expected []int64
	}{
		{"transport-session", []int64{material}},
		{"reported-session", nil},
		{"legacy-session", nil},
		{"transport-sessio", nil},
		{"comment-session", []int64{commented}},
		{"lifecycle-session", []int64{lifecycle}},
		{legacySession, []int64{selection}},
		{bulkSession, []int64{bulk}},
		{"explicit-session", []int64{reported}},
		{"older-session", nil},
	} {
		t.Run(tc.session, func(t *testing.T) {
			params := gormdb.IssueListParams{TargetProject: issue513Source, ParticipantSession: tc.session, Limit: 10}
			legacy, total, err := store.ListIssuesEx(ctx, params)
			require.NoError(t, err)
			require.EqualValues(t, len(tc.expected), total)
			var legacyIDs []int64
			for _, row := range legacy {
				legacyIDs = append(legacyIDs, row.ID)
			}
			require.Equal(t, tc.expected, legacyIDs)
			page, err := store.ListTaskIssues(ctx, gormdb.IssueTaskListParams{IssueListParams: params, ReadProject: issue513Source})
			require.NoError(t, err)
			require.EqualValues(t, len(tc.expected), page.Total)
			var ids []int64
			for _, row := range page.Issues {
				ids = append(ids, row.ID)
			}
			require.Equal(t, tc.expected, ids)
		})
	}
	page, err := store.ListTaskIssues(ctx, gormdb.IssueTaskListParams{IssueListParams: gormdb.IssueListParams{ParticipantSession: "creator-session", Limit: 2}, ReadProject: issue513Source})
	require.NoError(t, err)
	require.EqualValues(t, 6, page.Total)
	require.Len(t, page.Issues, 2)
	require.NotNil(t, page.NextOffset)
	legacyPage, legacyTotal, err := store.ListIssuesEx(ctx, gormdb.IssueListParams{TargetProject: issue513Source, ParticipantSession: "creator-session", Limit: 2})
	require.NoError(t, err)
	require.EqualValues(t, 6, legacyTotal)
	require.Len(t, legacyPage, 2)

	httpIssue := create("http material", issue513Source)
	updated := f.call(t, f.browser, http.MethodPatch, fmt.Sprintf("/api/issues/%d", httpIssue), `{"expected_revision":"1","title":"http material edited","session_id":"http-update-session"}`)
	require.Equal(t, http.StatusOK, updated.Code)
	var httpChange gormdb.IssueChange
	require.NoError(t, f.service.store.GetDB().Where("issue_id = ? AND revision = 2", httpIssue).First(&httpChange).Error)
	require.NotNil(t, httpChange.Attribution.Session)
	require.Equal(t, "http-update-session", *httpChange.Attribution.Session)
	httpVisible := f.call(t, f.client, http.MethodGet, "/api/issues?project="+issue513Source+"&participant_session=http-update-session", "")
	require.Equal(t, http.StatusOK, httpVisible.Code)
	require.Contains(t, httpVisible.Body.String(), "http material edited")
	wrongProject := f.call(t, f.client, http.MethodGet, "/api/issues?project="+issue513OtherSource+"&participant_session=http-update-session", "")
	require.Equal(t, http.StatusOK, wrongProject.Code)
	require.NotContains(t, wrongProject.Body.String(), "http material edited")

	response := f.call(t, issue513ActorCredential{}, http.MethodGet, "/api/issues?project="+issue513Source+"&participant_session=transport-session", "")
	denied := f.call(t, f.otherClient, http.MethodPatch, fmt.Sprintf("/api/issues/%d", material), `{"expected_revision":"2","title":"unauthorized","session_id":"transport-session"}`)
	require.Equal(t, http.StatusForbidden, denied.Code)
	current, _, err := store.GetIssue(ctx, material)
	require.NoError(t, err)
	require.Equal(t, title, current.Title)
	visible := f.call(t, f.client, http.MethodGet, "/api/issues?project="+issue513Source+"&participant_session=transport-session", "")
	require.Equal(t, http.StatusOK, visible.Code)
	require.Contains(t, visible.Body.String(), "material edited")
	require.NotContains(t, visible.Body.String(), "not-in-list")
	require.NotEqual(t, http.StatusOK, response.Code)
	require.NotContains(t, response.Body.String(), "material edited")
	require.NotContains(t, response.Body.String(), "not-in-list")
}
