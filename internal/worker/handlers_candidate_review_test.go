package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

type queueCandidateSelectionStoreFake struct {
	selection    gormdb.CollectionSelection
	currentErr   error
	frozenErr    error
	currentCalls int
	frozenCalls  int
	currentScope gormdb.CollectionSelectionScope
	frozenScope  gormdb.CollectionSelectionScope
	frozenToken  string
}

func (store *queueCandidateSelectionStoreFake) Current(_ context.Context, scope gormdb.CollectionSelectionScope) (gormdb.CollectionSelection, error) {
	store.currentCalls++
	store.currentScope = scope
	if store.currentErr != nil {
		return gormdb.CollectionSelection{}, store.currentErr
	}
	return store.selection, nil
}

func (store *queueCandidateSelectionStoreFake) Frozen(_ context.Context, scope gormdb.CollectionSelectionScope, token string) (gormdb.CollectionSelection, error) {
	store.frozenCalls++
	store.frozenScope = scope
	store.frozenToken = token
	if store.frozenErr != nil {
		return gormdb.CollectionSelection{}, store.frozenErr
	}
	return store.selection, nil
}

type queueCandidateScopeResolverFunc func(context.Context, auth.Identity, string, string) (gormdb.CollectionSelectionScope, error)

func (resolve queueCandidateScopeResolverFunc) ResolveOperatorCollectionScope(ctx context.Context, identity auth.Identity, sessionID, domain string) (gormdb.CollectionSelectionScope, error) {
	return resolve(ctx, identity, sessionID, domain)
}

func queueCandidateSelectionTestScope(userID int64, sessionID string) gormdb.CollectionSelectionScope {
	return gormdb.CollectionSelectionScope{
		SubjectUserID:      userID,
		SessionID:          sessionID,
		Domain:             queueCandidateSelectionDomain,
		ContextFingerprint: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		AuthorizationEpoch: 1,
		CollectionVersion:  1,
	}
}

func queueCandidateSelectionTestRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/memory/candidates/operations", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Engram-Request-ID", "queue-operation-1")
	req.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: "queue-browser-session"})
	return req.WithContext(auth.WithIdentity(req.Context(), auth.SessionForBrowserUser("operator", 41)))
}

func queueCandidateSelectionFixture(id int64, status models.CandidateStatus) *models.CrystallizationCandidate {
	return &models.CrystallizationCandidate{
		ID:                      id,
		Status:                  status,
		ProposedContent:         "queue candidate action must not disclose this content",
		ProposedTier:            "semantic",
		ProposedPromotionTarget: "semantic",
		SourceSessionID:         "queue-selection-session",
		EvidenceHandles:         []string{"session:queue-selection-session"},
		PrivacyScope:            "project",
		AffectedProjects:        []string{"queue-selection-project"},
	}
}

func newQueueCandidateSelectionTestHandler(store *fakeCandidateReviewStore, selections *queueCandidateSelectionStoreFake) *QueueCandidateSelectionHandler {
	scope := queueCandidateSelectionTestScope(41, "queue-browser-session")
	return NewQueueCandidateSelectionHandler(
		&Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store, snapshotStore: gormdb.NewSnapshotStore(nil)},
		selections,
		queueCandidateScopeResolverFunc(func(_ context.Context, _ auth.Identity, sessionID, domain string) (gormdb.CollectionSelectionScope, error) {
			if sessionID != scope.SessionID || domain != scope.Domain {
				return gormdb.CollectionSelectionScope{}, errors.New("unexpected queue selection scope")
			}
			return scope, nil
		}),
	)
}

func TestQueueCandidateSelectionHandler_ActionsReturnOnlyPermittedStatus(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name   string
		action string
		status models.CandidateStatus
	}{
		{name: "promote", action: "promote", status: models.CandidateStatusPromoted},
		{name: "reject", action: "reject", status: models.CandidateStatusRejected},
		{name: "supersede", action: "supersede", status: models.CandidateStatusSuperseded},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := queueCandidateSelectionFixture(42, models.CandidateStatusPending)
			store := &fakeCandidateReviewStore{
				getRows: map[int64]*models.CrystallizationCandidate{42: candidate},
				transitionRows: map[int64]*models.CrystallizationCandidate{
					42: {ID: 42, Status: testCase.status},
				},
			}
			selections := &queueCandidateSelectionStoreFake{selection: gormdb.CollectionSelection{
				Kind:    gormdb.CollectionSelectionExplicit,
				Version: 1,
				Targets: []gormdb.CollectionSelectionTarget{{ID: "42"}},
			}}
			handler := newQueueCandidateSelectionTestHandler(store, selections)
			recorder := httptest.NewRecorder()
			body := `{"request_id":"queue-operation-1","action":"` + testCase.action + `","selection":{"kind":"explicit","selection_version":1}}`
			handler.Handle(recorder, queueCandidateSelectionTestRequest(t, body))

			require.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, 1, selections.currentCalls, "selection must be resolved at action execution")
			assert.Equal(t, 0, selections.frozenCalls)
			assert.Equal(t, queueCandidateSelectionDomain, selections.currentScope.Domain)
			assert.NotZero(t, map[string]int64{"promote": store.promoteID, "reject": store.rejectID, "supersede": store.supersedeID}[testCase.action])

			var raw map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &raw))
			assert.NotContains(t, raw, "review_packet")
			assert.NotContains(t, raw, "proposed_content")
			assert.NotContains(t, raw, "lifecycle")
			assert.NotContains(t, raw, "ingestion")

			var response queueCandidateSelectionOperationResponse
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Len(t, response.ItemResults, 1)
			assert.Equal(t, int64(42), response.ItemResults[0].TargetID)
			assert.Equal(t, "committed", response.ItemResults[0].Outcome)
			assert.Equal(t, string(testCase.status), response.ItemResults[0].CandidateStatus)
			require.NotNil(t, response.Readback)
			assert.True(t, response.Readback.Authoritative)
			assert.Equal(t, "current", response.Readback.Kind)
		})
	}
}

func TestQueueCandidateSelectionHandler_RechecksTargetsAndReportsMixedTruth(t *testing.T) {
	t.Parallel()

	store := &fakeCandidateReviewStore{
		getRows: map[int64]*models.CrystallizationCandidate{
			42: queueCandidateSelectionFixture(42, models.CandidateStatusPending),
			43: queueCandidateSelectionFixture(43, models.CandidateStatusPromoted),
		},
		transitionRows: map[int64]*models.CrystallizationCandidate{
			42: {ID: 42, Status: models.CandidateStatusPromoted},
		},
	}
	selections := &queueCandidateSelectionStoreFake{selection: gormdb.CollectionSelection{
		Kind:    gormdb.CollectionSelectionExplicit,
		Version: 1,
		Targets: []gormdb.CollectionSelectionTarget{{ID: "42"}, {ID: "43"}},
	}}
	handler := newQueueCandidateSelectionTestHandler(store, selections)
	recorder := httptest.NewRecorder()
	body := `{"request_id":"queue-operation-1","action":"promote","selection":{"kind":"explicit","selection_version":1}}`
	handler.Handle(recorder, queueCandidateSelectionTestRequest(t, body))

	require.Equal(t, http.StatusMultiStatus, recorder.Code)
	var response queueCandidateSelectionOperationResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "partial", response.OperationState)
	require.Len(t, response.ItemResults, 2)
	assert.Equal(t, queueCandidateSelectionOperationItemResult{TargetID: 42, Outcome: "committed", CandidateStatus: "promoted"}, response.ItemResults[0])
	assert.Equal(t, queueCandidateSelectionOperationItemResult{TargetID: 43, Outcome: "conflict"}, response.ItemResults[1])
	assert.NotContains(t, recorder.Body.String(), "queue candidate action must not disclose this content")
}

func TestQueueCandidateSelectionHandler_DeniesStaleAndTokenOnlySelection(t *testing.T) {
	t.Parallel()

	t.Run("stale", func(t *testing.T) {
		store := &fakeCandidateReviewStore{}
		selections := &queueCandidateSelectionStoreFake{selection: gormdb.CollectionSelection{
			Kind:                   gormdb.CollectionSelectionExplicit,
			Version:                2,
			ReconfirmationRequired: true,
			Targets:                []gormdb.CollectionSelectionTarget{{ID: "42"}},
		}}
		handler := newQueueCandidateSelectionTestHandler(store, selections)
		recorder := httptest.NewRecorder()
		body := `{"request_id":"queue-operation-1","action":"promote","selection":{"kind":"explicit","selection_version":1}}`
		handler.Handle(recorder, queueCandidateSelectionTestRequest(t, body))

		assert.Equal(t, http.StatusPreconditionFailed, recorder.Code)
		assert.Zero(t, store.promoteID)
		assert.NotContains(t, recorder.Body.String(), "42")
	})

	t.Run("token_is_not_authority", func(t *testing.T) {
		store := &fakeCandidateReviewStore{}
		selections := &queueCandidateSelectionStoreFake{frozenErr: gormdb.ErrCollectionSelectionDenied}
		handler := newQueueCandidateSelectionTestHandler(store, selections)
		recorder := httptest.NewRecorder()
		body := `{"request_id":"queue-operation-1","action":"supersede","selection":{"kind":"frozen_filter","selection_version":1,"selection_token":"b857ebf7-c1cf-4a1f-a733-465ee492d3cb"}}`
		handler.Handle(recorder, queueCandidateSelectionTestRequest(t, body))

		assert.Equal(t, http.StatusForbidden, recorder.Code)
		assert.Equal(t, 1, selections.frozenCalls)
		assert.Zero(t, store.supersedeID)
	})
}

func TestQueueCandidateSelectionHandler_RejectsNonCandidateControls(t *testing.T) {
	t.Parallel()

	store := &fakeCandidateReviewStore{}
	selections := &queueCandidateSelectionStoreFake{}
	handler := newQueueCandidateSelectionTestHandler(store, selections)
	recorder := httptest.NewRecorder()
	body := `{"request_id":"queue-operation-1","action":"preview","selection":{"kind":"explicit","selection_version":1}}`
	handler.Handle(recorder, queueCandidateSelectionTestRequest(t, body))

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Zero(t, selections.currentCalls)
	assert.Zero(t, store.promoteID)
	assert.Zero(t, store.rejectID)
	assert.Zero(t, store.supersedeID)
	assert.NotContains(t, recorder.Body.String(), "review_packet")
}
