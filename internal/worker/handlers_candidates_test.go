package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

type fakeCandidateReviewStore struct {
	listRows       []*models.CrystallizationCandidate
	getRows        map[int64]*models.CrystallizationCandidate
	transitionRows map[int64]*models.CrystallizationCandidate
	listErr        error
	getErr         error
	promoteErr     error
	rejectErr      error
	supersedeErr   error
	promotedMemory *models.Memory
	promoteInput   *models.Memory
	rejectReason   string
	listProject    string
	listStatus     models.CandidateStatus
	listLimit      int
	listCalls      int
	promoteID      int64
	rejectID       int64
	supersedeID    int64
}

func (f *fakeCandidateReviewStore) ListByStatus(ctx context.Context, project string, status models.CandidateStatus, limit int) ([]*models.CrystallizationCandidate, error) {
	f.listCalls++
	f.listProject = project
	f.listStatus = status
	f.listLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listRows, nil
}

func (f *fakeCandidateReviewStore) Get(ctx context.Context, id int64) (*models.CrystallizationCandidate, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getRows != nil {
		return f.getRows[id], nil
	}
	return nil, nil
}

func (f *fakeCandidateReviewStore) PromoteWithMemory(ctx context.Context, candidateID int64, mem *models.Memory) (*models.CrystallizationCandidate, *models.Memory, error) {
	f.promoteID = candidateID
	f.promoteInput = mem
	if f.promoteErr != nil {
		return nil, nil, f.promoteErr
	}
	updated := f.transitionRows[candidateID]
	if updated == nil {
		updated = &models.CrystallizationCandidate{ID: candidateID, Status: models.CandidateStatusPromoted}
	}
	created := f.promotedMemory
	if created == nil {
		created = &models.Memory{ID: 9001}
	}
	return updated, created, nil
}

func (f *fakeCandidateReviewStore) TransitionToRejected(ctx context.Context, id int64, reason string) (*models.CrystallizationCandidate, error) {
	f.rejectID = id
	f.rejectReason = reason
	if f.rejectErr != nil {
		return nil, f.rejectErr
	}
	updated := f.transitionRows[id]
	if updated == nil {
		updated = &models.CrystallizationCandidate{ID: id, Status: models.CandidateStatusRejected}
	}
	return updated, nil
}

func (f *fakeCandidateReviewStore) TransitionToSuperseded(ctx context.Context, id int64) (*models.CrystallizationCandidate, error) {
	f.supersedeID = id
	if f.supersedeErr != nil {
		return nil, f.supersedeErr
	}
	updated := f.transitionRows[id]
	if updated == nil {
		updated = &models.CrystallizationCandidate{ID: id, Status: models.CandidateStatusSuperseded}
	}
	return updated, nil
}

func candidateActionRequest(method, path string, body []byte) (*httptest.ResponseRecorder, *http.Request, *chi.Mux) {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return httptest.NewRecorder(), req, chi.NewRouter()
}

func TestHandleListMemoryCandidates_GatedBeforeStore(t *testing.T) {
	store := &fakeCandidateReviewStore{}
	service := &Service{candidateReviewStoreSeam: store}
	req := httptest.NewRequest(http.MethodGet, "/api/memory/candidates?project=engram", nil)
	w := httptest.NewRecorder()

	service.handleListMemoryCandidates(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), candidateQueueFlag)
	assert.Equal(t, 0, store.listCalls, "flag-off page must gate before touching the candidate store")
}

func TestHandleListMemoryCandidates_ReturnsProjectScopedPayload(t *testing.T) {
	reviewAfter := time.Date(2026, time.June, 24, 12, 0, 0, 0, time.UTC)
	store := &fakeCandidateReviewStore{
		listRows: []*models.CrystallizationCandidate{{
			ID:                      42,
			Status:                  models.CandidateStatusPending,
			ProposedContent:         "operator console queue should be live",
			ProposedPromotionTarget: "semantic",
			ProposedTier:            "semantic",
			ProposedEpistemicType:   "decision",
			SourceSessionID:         "sess-42",
			Confidence:              0.86,
			RecurrenceCount:         3,
			Fingerprint:             "abc123",
			CreatedAt:               time.Date(2026, time.June, 23, 10, 0, 0, 0, time.UTC),
			UpdatedAt:               time.Date(2026, time.June, 23, 10, 5, 0, 0, time.UTC),
			ReviewAfter:             &reviewAfter,
			EvidenceHandles:         []string{"session:sess-42"},
			AffectedProjects:        []string{"engram"},
			PrivacyScope:            "project",
		}},
	}
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
	req := httptest.NewRequest(http.MethodGet, "/api/memory/candidates?project=engram&status=pending&limit=7", nil)
	w := httptest.NewRecorder()

	service.handleListMemoryCandidates(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Equal(t, "engram", store.listProject)
	assert.Equal(t, models.CandidateStatusPending, store.listStatus)
	assert.Equal(t, 7, store.listLimit)

	var response candidateListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, 1, response.Count)
	require.Len(t, response.Candidates, 1)
	assert.Equal(t, int64(42), response.Candidates[0].ID)
	assert.Equal(t, "operator console queue should be live", response.Candidates[0].ProposedContent)
	assert.Equal(t, []string{"engram"}, response.Candidates[0].AffectedProjects)
	require.NotNil(t, response.Candidates[0].ReviewAfter)
	assert.Equal(t, "2026-06-24T12:00:00Z", *response.Candidates[0].ReviewAfter)
}

func TestHandlePromoteMemoryCandidate_BuildsDecisionMemory(t *testing.T) {
	promotedMemoryID := int64(77)
	store := &fakeCandidateReviewStore{
		getRows: map[int64]*models.CrystallizationCandidate{
			42: {
				ID:                      42,
				Status:                  models.CandidateStatusPending,
				ProposedContent:         "ship the operator queue",
				ProposedTier:            "semantic",
				AffectedProjects:        []string{"engram"},
				ProposedPromotionTarget: "semantic",
			},
		},
		transitionRows: map[int64]*models.CrystallizationCandidate{
			42: {ID: 42, Status: models.CandidateStatusPromoted, PromotedMemoryID: &promotedMemoryID},
		},
		promotedMemory: &models.Memory{ID: promotedMemoryID},
	}
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
	w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/candidates/42/promote", nil)
	router.Post("/api/memory/candidates/{id}/promote", service.handlePromoteMemoryCandidate)

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, store.promoteInput)
	assert.Equal(t, int64(42), store.promoteID)
	assert.Equal(t, "ship the operator queue", store.promoteInput.Content)
	assert.Equal(t, "engram", store.promoteInput.Project)
	assert.Equal(t, "semantic", store.promoteInput.Tier)
	assert.Equal(t, "decision", store.promoteInput.EpistemicType)
	assert.Equal(t, "crystallization", store.promoteInput.SourceAgent)
	assert.ElementsMatch(t, []string{"candidate:42", "crystallized"}, store.promoteInput.Tags)

	var receipt candidateActionReceipt
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &receipt))
	assert.Equal(t, "promote", receipt.Action)
	assert.Equal(t, int64(42), receipt.CandidateID)
	assert.Equal(t, "promoted", receipt.CandidateStatus)
	assert.Equal(t, promotedMemoryID, receipt.MemoryID)
}

func TestHandleRejectMemoryCandidate_RejectsInvalidTransitionAsConflict(t *testing.T) {
	store := &fakeCandidateReviewStore{rejectErr: fmt.Errorf("%w: promoted -> rejected", gormdb.ErrInvalidTransition)}
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
	body := []byte(`{"reason":"not durable enough"}`)
	w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/candidates/42/reject", body)
	router.Post("/api/memory/candidates/{id}/reject", service.handleRejectMemoryCandidate)

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, int64(42), store.rejectID)
	assert.Equal(t, "not durable enough", store.rejectReason)
}

func TestHandleRejectMemoryCandidate_ContextCanceledAsClientClosed(t *testing.T) {
	store := &fakeCandidateReviewStore{rejectErr: context.Canceled}
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
	w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/candidates/42/reject", nil)
	router.Post("/api/memory/candidates/{id}/reject", service.handleRejectMemoryCandidate)

	router.ServeHTTP(w, req)

	require.Equal(t, statusClientClosedRequest, w.Code)
	assert.Contains(t, w.Body.String(), "client closed request")
}

func TestHandleRejectMemoryCandidate_DeadlineExceededAsGatewayTimeout(t *testing.T) {
	store := &fakeCandidateReviewStore{rejectErr: context.DeadlineExceeded}
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
	w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/candidates/42/reject", nil)
	router.Post("/api/memory/candidates/{id}/reject", service.handleRejectMemoryCandidate)

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusGatewayTimeout, w.Code)
	assert.Contains(t, w.Body.String(), "candidate request deadline exceeded")
}
