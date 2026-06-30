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
	authpkg "github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/reviewpacket"
	"github.com/thebtf/engram/pkg/models"
)

type fakeCandidateReviewStore struct {
	listRows               []*models.CrystallizationCandidate
	getRows                map[int64]*models.CrystallizationCandidate
	transitionRows         map[int64]*models.CrystallizationCandidate
	listErr                error
	getErr                 error
	promoteErr             error
	rejectErr              error
	supersedeErr           error
	promotedMemory         *models.Memory
	promoteSnapshot        *models.BulkOpSnapshot
	promoteSnapshotStore   *gormdb.SnapshotStore
	promoteActor           string
	rejectSnapshot         *models.BulkOpSnapshot
	rejectSnapshotStore    *gormdb.SnapshotStore
	rejectActor            string
	supersedeSnapshot      *models.BulkOpSnapshot
	supersedeSnapshotStore *gormdb.SnapshotStore
	supersedeActor         string
	promoteInput           *models.Memory
	rejectReason           string
	listProject            string
	listStatus             models.CandidateStatus
	listLimit              int
	listCalls              int
	promoteID              int64
	rejectID               int64
	supersedeID            int64
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

func (f *fakeCandidateReviewStore) PromoteWithMemoryAndSnapshot(ctx context.Context, snapshotStore *gormdb.SnapshotStore, candidateID int64, mem *models.Memory, snapshot *models.BulkOpSnapshot, actor string) (*models.CrystallizationCandidate, *models.Memory, *models.BulkOpSnapshot, error) {
	f.promoteSnapshotStore = snapshotStore
	f.promoteSnapshot = snapshot
	f.promoteActor = actor
	updated, created, err := f.PromoteWithMemory(ctx, candidateID, mem)
	if err != nil {
		return nil, nil, nil, err
	}
	return updated, created, snapshot, nil
}

func (f *fakeCandidateReviewStore) PreserveWithMemoryAndSnapshot(ctx context.Context, snapshotStore *gormdb.SnapshotStore, candidateID int64, mem *models.Memory, snapshot *models.BulkOpSnapshot, actor string) (*models.CrystallizationCandidate, *models.Memory, *models.BulkOpSnapshot, error) {
	return f.PromoteWithMemoryAndSnapshot(ctx, snapshotStore, candidateID, mem, snapshot, actor)
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

func (f *fakeCandidateReviewStore) TransitionToRejectedWithSnapshot(ctx context.Context, snapshotStore *gormdb.SnapshotStore, id int64, reason string, snapshot *models.BulkOpSnapshot, actor string) (*models.CrystallizationCandidate, *models.BulkOpSnapshot, error) {
	f.rejectSnapshotStore = snapshotStore
	f.rejectSnapshot = snapshot
	f.rejectActor = actor
	updated, err := f.TransitionToRejected(ctx, id, reason)
	if err != nil {
		return nil, nil, err
	}
	return updated, snapshot, nil
}

func (f *fakeCandidateReviewStore) TransitionToSuppressedWithSnapshot(ctx context.Context, snapshotStore *gormdb.SnapshotStore, id int64, reason string, snapshot *models.BulkOpSnapshot, actor string) (*models.CrystallizationCandidate, *models.BulkOpSnapshot, error) {
	return f.TransitionToRejectedWithSnapshot(ctx, snapshotStore, id, reason, snapshot, actor)
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

func (f *fakeCandidateReviewStore) TransitionToSupersededWithSnapshot(ctx context.Context, snapshotStore *gormdb.SnapshotStore, id int64, snapshot *models.BulkOpSnapshot, actor string) (*models.CrystallizationCandidate, *models.BulkOpSnapshot, error) {
	f.supersedeSnapshotStore = snapshotStore
	f.supersedeSnapshot = snapshot
	f.supersedeActor = actor
	updated, err := f.TransitionToSuperseded(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return updated, snapshot, nil
}

type fakeCandidateReviewSnapshotStore struct {
	snapshots                []*models.BulkOpSnapshot
	amendedSnapshotID        string
	amendedPromotedMemoryIDs []int64
	err                      error
	amendErr                 error
}

func (f *fakeCandidateReviewSnapshotStore) Create(ctx context.Context, snap *models.BulkOpSnapshot) (*models.BulkOpSnapshot, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.snapshots = append(f.snapshots, snap)
	return snap, nil
}

func (f *fakeCandidateReviewSnapshotStore) AmendPromoteEntries(ctx context.Context, snapshotID string, promotedMemoryIDs []int64) error {
	if f.amendErr != nil {
		return f.amendErr
	}
	f.amendedSnapshotID = snapshotID
	f.amendedPromotedMemoryIDs = append([]int64(nil), promotedMemoryIDs...)
	return nil
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

	packet := response.Candidates[0].ReviewPacket
	assert.Equal(t, "candidate:42:abc123", packet.PacketID)
	assert.Equal(t, int64(42), packet.CandidateID)
	assert.Equal(t, "candidate_review", packet.Kind)
	assert.Equal(t, []string{"promote", "reject", "supersede"}, packet.Decision.AllowedActions)
	assert.Equal(t, "semantic", packet.Decision.PromotionTarget)
	assert.Equal(t, []string{"engram"}, packet.Scope.Projects)
	assert.Equal(t, "project", packet.Scope.PrivacyScope)
	require.Len(t, packet.Evidence, 1)
	assert.Equal(t, "session:sess-42", packet.Evidence[0].Handle)
	assert.Equal(t, "session", packet.Evidence[0].Kind)
	assert.True(t, packet.Snapshot.Required)
	assert.Equal(t, "bulk_op_snapshots", packet.Snapshot.Store)
	assert.Equal(t, "pre_action_required", packet.Snapshot.Status)
	assert.Equal(t, "audit_log", packet.Audit.Store)
	assert.Equal(t, "pending_on_action", packet.Audit.Status)
}

func TestHandleListMemoryCandidates_AllProjectListsUnscopedQueue(t *testing.T) {
	store := &fakeCandidateReviewStore{
		listRows: []*models.CrystallizationCandidate{{
			ID:              43,
			Status:          models.CandidateStatusPending,
			ProposedContent: "unscoped candidate should remain visible to the operator",
			CreatedAt:       time.Date(2026, time.June, 23, 11, 0, 0, 0, time.UTC),
			UpdatedAt:       time.Date(2026, time.June, 23, 11, 0, 0, 0, time.UTC),
		}},
	}
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
	req := httptest.NewRequest(http.MethodGet, "/api/memory/candidates?project=all&status=pending", nil)
	w := httptest.NewRecorder()

	service.handleListMemoryCandidates(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "", store.listProject)

	var response candidateListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	assert.Equal(t, candidateQueueAllProjects, response.Project)
	require.Len(t, response.Candidates, 1)
	assert.Empty(t, response.Candidates[0].AffectedProjects)
}

func TestHandleGetMemoryCandidate_ReturnsReviewPacket(t *testing.T) {
	store := &fakeCandidateReviewStore{
		getRows: map[int64]*models.CrystallizationCandidate{
			42: {
				ID:                      42,
				Status:                  models.CandidateStatusPending,
				ProposedContent:         "read path should return a packet",
				ProposedPromotionTarget: "semantic",
				ProposedTier:            "semantic",
				ProposedEpistemicType:   "decision",
				SourceSessionID:         "sess-42",
				Fingerprint:             "abc123",
				EvidenceHandles:         []string{"session:sess-42"},
				AffectedProjects:        []string{"engram"},
				PrivacyScope:            "project",
				CreatedAt:               time.Date(2026, time.June, 23, 10, 0, 0, 0, time.UTC),
				UpdatedAt:               time.Date(2026, time.June, 23, 10, 5, 0, 0, time.UTC),
			},
		},
	}
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
	req := httptest.NewRequest(http.MethodGet, "/api/memory/candidates/42", nil)
	w := httptest.NewRecorder()
	router := chi.NewRouter()
	router.Get("/api/memory/candidates/{id}", service.handleGetMemoryCandidate)

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var response candidateReviewItem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	assert.Equal(t, int64(42), response.ID)
	assert.Equal(t, "read path should return a packet", response.ProposedContent)
	assert.Equal(t, "candidate:42:abc123", response.ReviewPacket.PacketID)
	assert.Equal(t, []string{"promote", "reject", "supersede"}, response.ReviewPacket.Decision.AllowedActions)
	assert.Equal(t, "bulk_op_snapshots", response.ReviewPacket.Snapshot.Store)
	assert.Equal(t, "audit_log", response.ReviewPacket.Audit.Store)
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
				SourceSessionID:         "sess-42",
				EvidenceHandles:         []string{"session:sess-42"},
				PrivacyScope:            "project",
				ProposedPromotionTarget: "semantic",
			},
		},
		transitionRows: map[int64]*models.CrystallizationCandidate{
			42: {ID: 42, Status: models.CandidateStatusPromoted, PromotedMemoryID: &promotedMemoryID},
		},
		promotedMemory: &models.Memory{ID: promotedMemoryID},
	}
	snapshotStore := gormdb.NewSnapshotStore(nil)
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store, snapshotStore: snapshotStore}
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
	require.NotNil(t, store.promoteSnapshot)
	assert.True(t, store.promoteSnapshotStore == snapshotStore)
	assert.Equal(t, models.SnapshotOpCandidateReviewAction, store.promoteSnapshot.OpType)

	var receipt candidateActionReceipt
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &receipt))
	assert.Equal(t, "promote", receipt.Action)
	assert.Equal(t, int64(42), receipt.CandidateID)
	assert.Equal(t, "promoted", receipt.CandidateStatus)
	assert.Equal(t, promotedMemoryID, receipt.MemoryID)
	assert.Equal(t, "system", store.promoteActor)
}

func TestHandleRejectMemoryCandidate_UsesContextPrincipalForSnapshotActor(t *testing.T) {
	store := &fakeCandidateReviewStore{
		getRows: map[int64]*models.CrystallizationCandidate{
			42: {
				ID:               42,
				Status:           models.CandidateStatusPending,
				SourceSessionID:  "sess-42",
				EvidenceHandles:  []string{"session:sess-42"},
				PrivacyScope:     "project",
				AffectedProjects: []string{"engram"},
			},
		},
		transitionRows: map[int64]*models.CrystallizationCandidate{
			42: {ID: 42, Status: models.CandidateStatusRejected, SourceSessionID: "sess-42"},
		},
	}
	snapshotStore := gormdb.NewSnapshotStore(nil)
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store, snapshotStore: snapshotStore}
	body := []byte(`{"reason":"not durable enough"}`)
	w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/candidates/42/reject", body)
	req = req.WithContext(authpkg.WithIdentity(req.Context(), authpkg.ClientWithPrincipal("read-write", "key-1", "agent/codex", authpkg.PrincipalKindAgent)))
	router.Post("/api/memory/candidates/{id}/reject", service.handleRejectMemoryCandidate)

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, int64(42), store.rejectID)
	assert.Equal(t, "not durable enough", store.rejectReason)
	require.NotNil(t, store.rejectSnapshot)
	assert.True(t, store.rejectSnapshotStore == snapshotStore)
	assert.Equal(t, "agent/codex", store.rejectSnapshot.Actor)
	assert.Equal(t, "agent/codex", store.rejectActor)
}

func TestHandleSupersedeMemoryCandidate_UsesAtomicSnapshotTransition(t *testing.T) {
	store := &fakeCandidateReviewStore{
		getRows: map[int64]*models.CrystallizationCandidate{
			42: {
				ID:               42,
				Status:           models.CandidateStatusPending,
				SourceSessionID:  "sess-42",
				EvidenceHandles:  []string{"session:sess-42"},
				PrivacyScope:     "project",
				AffectedProjects: []string{"engram"},
			},
		},
		transitionRows: map[int64]*models.CrystallizationCandidate{
			42: {ID: 42, Status: models.CandidateStatusSuperseded, SourceSessionID: "sess-42"},
		},
	}
	snapshotStore := gormdb.NewSnapshotStore(nil)
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store, snapshotStore: snapshotStore}
	w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/candidates/42/supersede", nil)
	router.Post("/api/memory/candidates/{id}/supersede", service.handleSupersedeMemoryCandidate)

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, int64(42), store.supersedeID)
	require.NotNil(t, store.supersedeSnapshot)
	assert.True(t, store.supersedeSnapshotStore == snapshotStore)
	assert.Equal(t, "system", store.supersedeActor)
}

func TestHandlePromoteMemoryCandidate_RejectsUnscopedPromotion(t *testing.T) {
	store := &fakeCandidateReviewStore{
		getRows: map[int64]*models.CrystallizationCandidate{
			42: {
				ID:              42,
				Status:          models.CandidateStatusPending,
				ProposedContent: "unscoped candidate must not create a projectless memory",
				ProposedTier:    "semantic",
				SourceSessionID: "sess-42",
				EvidenceHandles: []string{"session:sess-42"},
				PrivacyScope:    "project",
			},
		},
	}
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
	w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/candidates/42/promote", nil)
	router.Post("/api/memory/candidates/{id}/promote", service.handlePromoteMemoryCandidate)

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "candidate has no affected project")
	assert.Equal(t, int64(0), store.promoteID)
}

func TestHandlePromoteMemoryCandidate_RejectsMissingPrivacyScopeBeforeMutation(t *testing.T) {
	store := &fakeCandidateReviewStore{
		getRows: map[int64]*models.CrystallizationCandidate{
			42: {
				ID:               42,
				Status:           models.CandidateStatusPending,
				ProposedContent:  "privacy scope must be validated before mutation",
				ProposedTier:     "semantic",
				AffectedProjects: []string{"engram"},
			},
		},
	}
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
	w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/candidates/42/promote", nil)
	router.Post("/api/memory/candidates/{id}/promote", service.handlePromoteMemoryCandidate)

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "privacy_scope")
	assert.Equal(t, int64(0), store.promoteID)
}

func TestHandleRejectMemoryCandidate_RejectsInvalidTransitionAsConflict(t *testing.T) {
	store := &fakeCandidateReviewStore{
		getRows: map[int64]*models.CrystallizationCandidate{
			42: {ID: 42, Status: models.CandidateStatusPending, PrivacyScope: "project"},
		},
		rejectErr: fmt.Errorf("%w: promoted -> rejected", gormdb.ErrInvalidTransition),
	}
	snapshotStore := gormdb.NewSnapshotStore(nil)
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store, snapshotStore: snapshotStore}
	body := []byte(`{"reason":"not durable enough"}`)
	w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/candidates/42/reject", body)
	router.Post("/api/memory/candidates/{id}/reject", service.handleRejectMemoryCandidate)

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, int64(42), store.rejectID)
	assert.Equal(t, "not durable enough", store.rejectReason)
}

func TestHandleRejectMemoryCandidate_ContextCanceledAsClientClosed(t *testing.T) {
	store := &fakeCandidateReviewStore{
		getRows: map[int64]*models.CrystallizationCandidate{
			42: {ID: 42, Status: models.CandidateStatusPending, PrivacyScope: "project"},
		},
		rejectErr: context.Canceled,
	}
	snapshotStore := gormdb.NewSnapshotStore(nil)
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store, snapshotStore: snapshotStore}
	w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/candidates/42/reject", nil)
	router.Post("/api/memory/candidates/{id}/reject", service.handleRejectMemoryCandidate)

	router.ServeHTTP(w, req)

	require.Equal(t, statusClientClosedRequest, w.Code)
	assert.Contains(t, w.Body.String(), "client closed request")
}

func TestHandleRejectMemoryCandidate_DeadlineExceededAsGatewayTimeout(t *testing.T) {
	store := &fakeCandidateReviewStore{
		getRows: map[int64]*models.CrystallizationCandidate{
			42: {ID: 42, Status: models.CandidateStatusPending, PrivacyScope: "project"},
		},
		rejectErr: context.DeadlineExceeded,
	}
	snapshotStore := gormdb.NewSnapshotStore(nil)
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store, snapshotStore: snapshotStore}
	w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/candidates/42/reject", nil)
	router.Post("/api/memory/candidates/{id}/reject", service.handleRejectMemoryCandidate)

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusGatewayTimeout, w.Code)
	assert.Contains(t, w.Body.String(), "candidate request deadline exceeded")
}

func TestHandleReadMemoryReviewQueue_ReturnsPacketCentricQueueAndSparseMetrics(t *testing.T) {
	store := &fakeCandidateReviewStore{
		listRows: []*models.CrystallizationCandidate{{
			ID:               42,
			Status:           models.CandidateStatusPending,
			SourceSessionID:  "sess-42",
			EvidenceHandles:  []string{"session:sess-42"},
			AffectedProjects: []string{"engram"},
			PrivacyScope:     "project",
			Fingerprint:      "abc123",
		}},
	}
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
	req := httptest.NewRequest(http.MethodGet, "/api/memory/review-queue?project=engram&status=pending&limit=5", nil)
	w := httptest.NewRecorder()

	service.handleReadMemoryReviewQueue(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "engram", store.listProject)
	assert.Equal(t, models.CandidateStatusPending, store.listStatus)
	assert.Equal(t, 5, store.listLimit)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	require.Contains(t, raw, "packets")
	require.NotContains(t, raw, "candidates", "CR-008 queue read must not be a raw candidate row dump")

	var response reviewpacket.ReviewQueueRead
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Len(t, response.Packets, 1)
	assert.Equal(t, "candidate:42:abc123", response.Packets[0].PacketID)
	assert.Equal(t, reviewpacket.ReviewPacketTypeUsefulnessNoise, response.Packets[0].PacketType)
	assert.GreaterOrEqual(t, response.Packets[0].ProvenanceCount, 4)
	assert.Equal(t, reviewpacket.ReviewStateSparse, response.Metrics.State)
	assert.Contains(t, response.Metrics.SparseReason, "confidence telemetry")
}

func TestHandleReadMemoryReviewQueue_RiskyOnlyKeepsUnfilteredMetricsAndBacklog(t *testing.T) {
	store := &fakeCandidateReviewStore{
		listRows: []*models.CrystallizationCandidate{
			{ID: 42, Status: models.CandidateStatusPending, SourceSessionID: "sess-42", AffectedProjects: []string{"engram"}, PrivacyScope: "project", Fingerprint: "abc123", Confidence: 0.4},
			{ID: 43, Status: models.CandidateStatusPending, SourceSessionID: "sess-43", AffectedProjects: []string{"engram"}, PrivacyScope: "project", Fingerprint: "def456", Confidence: 0.8},
		},
	}
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
	req := httptest.NewRequest(http.MethodGet, "/api/memory/review-queue?project=engram&status=pending&limit=5&risky_only=true", nil)
	w := httptest.NewRecorder()

	service.handleReadMemoryReviewQueue(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var response reviewpacket.ReviewQueueRead
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Len(t, response.Packets, 1)
	assert.Equal(t, "candidate:42:abc123", response.Packets[0].PacketID)
	assert.Equal(t, 2, response.Metrics.BacklogTotal)
	assert.Equal(t, 2, response.Metrics.ReadyCount)
	assert.Equal(t, 1, response.Metrics.RiskyCount)
	assert.Equal(t, reviewpacket.ReviewStateLive, response.Metrics.State)
	assert.Equal(t, 2, response.Backlog.BoundedTotal)
	assert.Equal(t, 2, response.Backlog.ReadyCount)
}

func TestHandlePreviewMemoryReviewPacketAction_DoesNotMutate(t *testing.T) {
	store := &fakeCandidateReviewStore{
		getRows: map[int64]*models.CrystallizationCandidate{
			42: {ID: 42, Status: models.CandidateStatusPending, PrivacyScope: "project", AffectedProjects: []string{"engram"}, Fingerprint: "abc123"},
		},
	}
	service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
	body := []byte(`{"action_type":"suppress"}`)
	w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/review-packets/candidate:42:abc123/preview", body)
	router.Post("/api/memory/review-packets/{packetID}/preview", service.handlePreviewMemoryReviewPacketAction)

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, int64(0), store.rejectID)
	assert.Equal(t, int64(0), store.promoteID)
	var preview reviewpacket.ReviewActionPreview
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &preview))
	assert.Equal(t, reviewpacket.ReviewActionSuppress, preview.ActionType)
	assert.Equal(t, "reject", preview.CandidateAction)
	assert.Equal(t, "candidate:42:abc123", preview.StaleMarker)
}

func TestHandleApplyMemoryReviewPacketAction_PreserveAndSuppressUseSnapshotBackedTransitions(t *testing.T) {
	t.Run("preserve", func(t *testing.T) {
		promotedMemoryID := int64(77)
		store := &fakeCandidateReviewStore{
			getRows: map[int64]*models.CrystallizationCandidate{
				42: {ID: 42, Status: models.CandidateStatusPending, ProposedContent: "preserve useful memory", ProposedTier: "semantic", SourceSessionID: "sess-42", PrivacyScope: "project", AffectedProjects: []string{"engram"}, Fingerprint: "abc123"},
			},
			transitionRows: map[int64]*models.CrystallizationCandidate{
				42: {ID: 42, Status: models.CandidateStatusPromoted, PromotedMemoryID: &promotedMemoryID, Fingerprint: "abc123"},
			},
			promotedMemory: &models.Memory{ID: promotedMemoryID},
		}
		snapshotStore := gormdb.NewSnapshotStore(nil)
		service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store, snapshotStore: snapshotStore}
		body := []byte(`{"action_type":"preserve"}`)
		w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/review-packets/candidate:42:abc123/apply", body)
		router.Post("/api/memory/review-packets/{packetID}/apply", service.handleApplyMemoryReviewPacketAction)

		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, int64(42), store.promoteID)
		require.NotNil(t, store.promoteSnapshot)
		assert.True(t, store.promoteSnapshotStore == snapshotStore)
		assert.Contains(t, string(store.promoteSnapshot.Parameters), `"action":"preserve"`)
		var receipt reviewpacket.ReviewActionReceipt
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &receipt))
		assert.Equal(t, reviewpacket.ReviewActionPreserve, receipt.ActionType)
		assert.Equal(t, "promote", receipt.CandidateAction)
		assert.Equal(t, "promoted", receipt.UpdatedPacketStatus)
		assert.NotEmpty(t, receipt.SnapshotID)
		assert.Equal(t, promotedMemoryID, receipt.MemoryID)
	})

	t.Run("suppress", func(t *testing.T) {
		store := &fakeCandidateReviewStore{
			getRows: map[int64]*models.CrystallizationCandidate{
				43: {ID: 43, Status: models.CandidateStatusPending, SourceSessionID: "sess-43", PrivacyScope: "project", AffectedProjects: []string{"engram"}, Fingerprint: "def456"},
			},
			transitionRows: map[int64]*models.CrystallizationCandidate{
				43: {ID: 43, Status: models.CandidateStatusRejected, Fingerprint: "def456"},
			},
		}
		snapshotStore := gormdb.NewSnapshotStore(nil)
		service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store, snapshotStore: snapshotStore}
		body := []byte(`{"action_type":"suppress","reason":"noise"}`)
		w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/review-packets/candidate:43:def456/apply", body)
		router.Post("/api/memory/review-packets/{packetID}/apply", service.handleApplyMemoryReviewPacketAction)

		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, int64(43), store.rejectID)
		assert.Equal(t, "noise", store.rejectReason)
		require.NotNil(t, store.rejectSnapshot)
		assert.True(t, store.rejectSnapshotStore == snapshotStore)
		assert.Contains(t, string(store.rejectSnapshot.Parameters), `"action":"suppress"`)
		var receipt reviewpacket.ReviewActionReceipt
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &receipt))
		assert.Equal(t, reviewpacket.ReviewActionSuppress, receipt.ActionType)
		assert.Equal(t, "reject", receipt.CandidateAction)
		assert.Equal(t, "rejected", receipt.UpdatedPacketStatus)
		assert.NotEmpty(t, receipt.SnapshotID)
	})
}

func TestHandleApplyMemoryReviewPacketAction_RejectsUnsupportedAndStalePackets(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		store := &fakeCandidateReviewStore{
			getRows: map[int64]*models.CrystallizationCandidate{
				42: {ID: 42, Status: models.CandidateStatusPending, PrivacyScope: "project", AffectedProjects: []string{"engram"}, Fingerprint: "abc123"},
			},
		}
		service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
		body := []byte(`{"action_type":"destroy"}`)
		w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/review-packets/candidate:42:abc123/apply", body)
		router.Post("/api/memory/review-packets/{packetID}/apply", service.handleApplyMemoryReviewPacketAction)

		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "unsupported_review_action")
		assert.Equal(t, int64(0), store.rejectID)
		assert.Equal(t, int64(0), store.promoteID)
	})

	t.Run("stale", func(t *testing.T) {
		store := &fakeCandidateReviewStore{
			getRows: map[int64]*models.CrystallizationCandidate{
				42: {ID: 42, Status: models.CandidateStatusPending, PrivacyScope: "project", AffectedProjects: []string{"engram"}, Fingerprint: "new456"},
			},
		}
		service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: store}
		body := []byte(`{"action_type":"suppress"}`)
		w, req, router := candidateActionRequest(http.MethodPost, "/api/memory/review-packets/candidate:42:old123/apply", body)
		router.Post("/api/memory/review-packets/{packetID}/apply", service.handleApplyMemoryReviewPacketAction)

		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusConflict, w.Code)
		assert.Contains(t, w.Body.String(), "stale_review_packet")
		assert.Equal(t, int64(0), store.rejectID)
	})
}

func TestHandleReadMemoryReviewMetrics_ReturnsGatedAndErrorStatesHonestly(t *testing.T) {
	t.Run("gated", func(t *testing.T) {
		service := &Service{candidateReviewStoreSeam: &fakeCandidateReviewStore{}}
		req := httptest.NewRequest(http.MethodGet, "/api/memory/review-metrics", nil)
		w := httptest.NewRecorder()

		service.handleReadMemoryReviewMetrics(w, req)

		require.Equal(t, http.StatusForbidden, w.Code)
		var metrics reviewpacket.ReviewMetrics
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &metrics))
		assert.Equal(t, reviewpacket.ReviewStateGated, metrics.State)
		assert.Contains(t, metrics.SparseReason, candidateQueueFlag)
	})

	t.Run("error", func(t *testing.T) {
		service := &Service{candidateQueueEnabled: true, candidateReviewStoreSeam: &fakeCandidateReviewStore{listErr: fmt.Errorf("db unavailable")}}
		req := httptest.NewRequest(http.MethodGet, "/api/memory/review-metrics", nil)
		w := httptest.NewRecorder()

		service.handleReadMemoryReviewMetrics(w, req)

		require.Equal(t, http.StatusInternalServerError, w.Code)
		var metrics reviewpacket.ReviewMetrics
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &metrics))
		assert.Equal(t, reviewpacket.ReviewStateError, metrics.State)
		assert.Contains(t, metrics.SparseReason, "db unavailable")
	})
}
