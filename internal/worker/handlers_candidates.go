package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/reviewpacket"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

const candidateQueueFlag = "ENGRAM_VNEXT_F_ENABLED"
const candidateQueueAllProjects = "all"
const statusClientClosedRequest = 499

type candidateReviewStore interface {
	ListByStatus(ctx context.Context, project string, status models.CandidateStatus, limit int) ([]*models.CrystallizationCandidate, error)
	Get(ctx context.Context, id int64) (*models.CrystallizationCandidate, error)
	PromoteWithMemory(ctx context.Context, candidateID int64, mem *models.Memory) (*models.CrystallizationCandidate, *models.Memory, error)
	TransitionToRejected(ctx context.Context, id int64, reason string) (*models.CrystallizationCandidate, error)
	TransitionToSuperseded(ctx context.Context, id int64) (*models.CrystallizationCandidate, error)
}

type candidateListResponse struct {
	Candidates []candidateReviewItem `json:"candidates"`
	Count      int                   `json:"count"`
	Project    string                `json:"project"`
	Status     string                `json:"status"`
	Limit      int                   `json:"limit"`
}

type candidateReviewItem struct {
	ReviewPacket            reviewpacket.CandidateReviewPacket `json:"review_packet"`
	ReviewAfter             *string                            `json:"review_after,omitempty"`
	PromotedMemoryID        *int64                             `json:"promoted_memory_id,omitempty"`
	ID                      int64                              `json:"id"`
	Status                  string                             `json:"status"`
	ProposedContent         string                             `json:"proposed_content"`
	ProposedPromotionTarget string                             `json:"proposed_promotion_target"`
	ProposedTier            string                             `json:"proposed_tier"`
	ProposedEpistemicType   string                             `json:"proposed_epistemic_type"`
	SourceSessionID         string                             `json:"source_session_id"`
	Fingerprint             string                             `json:"fingerprint,omitempty"`
	PrivacyScope            string                             `json:"privacy_scope,omitempty"`
	CreatedAt               string                             `json:"created_at"`
	UpdatedAt               string                             `json:"updated_at"`
	EvidenceHandles         []string                           `json:"evidence_handles"`
	AffectedProjects        []string                           `json:"affected_projects"`
	Confidence              float32                            `json:"confidence"`
	RecurrenceCount         int                                `json:"recurrence_count"`
}

type candidateActionReceipt struct {
	CandidateID      int64  `json:"candidate_id"`
	CandidateStatus  string `json:"candidate_status"`
	MemoryID         int64  `json:"memory_id,omitempty"`
	PromotedMemoryID *int64 `json:"promoted_memory_id,omitempty"`
	Action           string `json:"action"`
}

type rejectCandidateRequest struct {
	Reason string `json:"reason,omitempty"`
}

func candidateQueueEnabledFromEnv() bool {
	return os.Getenv(candidateQueueFlag) == "true"
}

func (s *Service) candidateQueueActive() bool {
	return s != nil && s.candidateQueueEnabled
}

func (s *Service) currentCandidateReviewStore() candidateReviewStore {
	if s.candidateReviewStoreSeam != nil {
		return s.candidateReviewStoreSeam
	}

	s.initMu.RLock()
	defer s.initMu.RUnlock()
	if s.candidateStore == nil {
		return nil
	}
	return s.candidateStore
}

func candidateRFC3339(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func candidateRFC3339Ptr(value *time.Time) *string {
	if value == nil || value.IsZero() {
		return nil
	}
	formatted := candidateRFC3339(*value)
	return &formatted
}

func normalizeCandidateProject(project string) string {
	project = strings.TrimSpace(project)
	if strings.EqualFold(project, candidateQueueAllProjects) || project == "*" {
		return ""
	}
	return project
}

func candidateReviewItemFromDomain(candidate *models.CrystallizationCandidate) candidateReviewItem {
	if candidate == nil {
		return candidateReviewItem{}
	}

	return candidateReviewItem{
		ReviewPacket:            reviewpacket.FromCandidate(candidate),
		ID:                      candidate.ID,
		Status:                  string(candidate.Status),
		ProposedContent:         candidate.ProposedContent,
		ProposedPromotionTarget: candidate.ProposedPromotionTarget,
		ProposedTier:            candidate.ProposedTier,
		ProposedEpistemicType:   candidate.ProposedEpistemicType,
		SourceSessionID:         candidate.SourceSessionID,
		Confidence:              candidate.Confidence,
		RecurrenceCount:         candidate.RecurrenceCount,
		Fingerprint:             candidate.Fingerprint,
		CreatedAt:               candidateRFC3339(candidate.CreatedAt),
		UpdatedAt:               candidateRFC3339(candidate.UpdatedAt),
		ReviewAfter:             candidateRFC3339Ptr(candidate.ReviewAfter),
		EvidenceHandles:         append([]string(nil), candidate.EvidenceHandles...),
		AffectedProjects:        append([]string(nil), candidate.AffectedProjects...),
		PrivacyScope:            candidate.PrivacyScope,
		PromotedMemoryID:        candidate.PromotedMemoryID,
	}
}

func writeCandidateStoreError(w http.ResponseWriter, action string, id int64, err error) {
	if writeCandidateContextError(w, err) {
		return
	}
	if errors.Is(err, gormlib.ErrRecordNotFound) {
		http.Error(w, "candidate not found", http.StatusNotFound)
		return
	}
	if errors.Is(err, gormdb.ErrInvalidTransition) {
		http.Error(w, "invalid candidate transition", http.StatusConflict)
		return
	}
	log.Error().Err(err).Str("action", action).Int64("candidate_id", id).Msg("candidate action failed")
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func writeCandidateContextError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, context.Canceled) {
		http.Error(w, "client closed request", statusClientClosedRequest)
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		http.Error(w, "candidate request deadline exceeded", http.StatusGatewayTimeout)
		return true
	}
	return false
}

func candidateIDFromRequest(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func decodeRejectCandidateRequest(r *http.Request) (rejectCandidateRequest, error) {
	var req rejectCandidateRequest
	if r.Body == nil {
		return req, nil
	}
	defer r.Body.Close()

	err := json.NewDecoder(r.Body).Decode(&req)
	if err == nil || errors.Is(err, io.EOF) {
		req.Reason = strings.TrimSpace(req.Reason)
		return req, nil
	}
	return req, err
}

func memoryFromCandidate(candidate *models.CrystallizationCandidate) (*models.Memory, error) {
	if candidate == nil {
		return nil, errors.New("candidate is required")
	}
	project := ""
	for _, affectedProject := range candidate.AffectedProjects {
		project = strings.TrimSpace(affectedProject)
		if project != "" {
			break
		}
	}
	if project == "" {
		return nil, errors.New("candidate has no affected project")
	}
	return &models.Memory{
		Content:       candidate.ProposedContent,
		Project:       project,
		Tier:          candidate.ProposedTier,
		EpistemicType: "decision",
		Tags:          []string{fmt.Sprintf("candidate:%d", candidate.ID), "crystallized"},
		SourceAgent:   "crystallization",
	}, nil
}

// handleListMemoryCandidates godoc
// @Summary List crystallization candidates for operator review
// @Description Returns vNext-F crystallization candidates filtered by project and status.
// @Tags Memories
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Project identifier or 'all' for unscoped/all-project candidates"
// @Param status query string false "Candidate status (default pending)"
// @Param limit query int false "Maximum number of results (default 20, max 100)"
// @Success 200 {object} candidateListResponse
// @Failure 400 {string} string "bad request"
// @Failure 403 {string} string "feature flag required"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal error"
// @Router /api/memory/candidates [get]
func (s *Service) handleListMemoryCandidates(w http.ResponseWriter, r *http.Request) {
	if !s.candidateQueueActive() {
		http.Error(w, "candidate queue requires ENGRAM_VNEXT_F_ENABLED=true", http.StatusForbidden)
		return
	}

	store := s.currentCandidateReviewStore()
	if store == nil {
		http.Error(w, "candidate store not available", http.StatusServiceUnavailable)
		return
	}

	projectParam := strings.TrimSpace(r.URL.Query().Get("project"))
	listProject := normalizeCandidateProject(projectParam)
	responseProject := projectParam
	if responseProject == "" || listProject == "" {
		responseProject = candidateQueueAllProjects
	}

	status := models.CandidateStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if status == "" {
		status = models.CandidateStatusPending
	}
	if !status.IsValid() {
		http.Error(w, "invalid candidate status", http.StatusBadRequest)
		return
	}

	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		if n > 100 {
			http.Error(w, "limit must not exceed 100", http.StatusBadRequest)
			return
		}
		limit = n
	}

	candidates, err := store.ListByStatus(r.Context(), listProject, status, limit)
	if err != nil {
		if writeCandidateContextError(w, err) {
			return
		}
		log.Error().Err(err).Str("project", responseProject).Str("status", string(status)).Msg("list candidates failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := make([]candidateReviewItem, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		items = append(items, candidateReviewItemFromDomain(candidate))
	}

	writeJSON(w, candidateListResponse{
		Candidates: items,
		Count:      len(items),
		Project:    responseProject,
		Status:     string(status),
		Limit:      limit,
	})
}

// handleGetMemoryCandidate godoc
// @Summary Read one crystallization candidate review packet
// @Description Returns one vNext-F crystallization candidate with its bounded review_packet projection.
// @Tags Memories
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Candidate ID"
// @Success 200 {object} candidateReviewItem
// @Failure 400 {string} string "invalid id"
// @Failure 403 {string} string "feature flag required"
// @Failure 404 {string} string "candidate not found"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal error"
// @Router /api/memory/candidates/{id} [get]
func (s *Service) handleGetMemoryCandidate(w http.ResponseWriter, r *http.Request) {
	if !s.candidateQueueActive() {
		http.Error(w, "candidate queue requires ENGRAM_VNEXT_F_ENABLED=true", http.StatusForbidden)
		return
	}
	store := s.currentCandidateReviewStore()
	if store == nil {
		http.Error(w, "candidate store not available", http.StatusServiceUnavailable)
		return
	}
	id, ok := candidateIDFromRequest(r)
	if !ok {
		http.Error(w, "invalid candidate id", http.StatusBadRequest)
		return
	}

	candidate, err := store.Get(r.Context(), id)
	if err != nil {
		writeCandidateStoreError(w, "get_candidate", id, err)
		return
	}
	if candidate == nil {
		http.Error(w, "candidate not found", http.StatusNotFound)
		return
	}

	writeJSON(w, candidateReviewItemFromDomain(candidate))
}

func (s *Service) handlePromoteMemoryCandidate(w http.ResponseWriter, r *http.Request) {
	if !s.candidateQueueActive() {
		http.Error(w, "candidate queue requires ENGRAM_VNEXT_F_ENABLED=true", http.StatusForbidden)
		return
	}
	store := s.currentCandidateReviewStore()
	if store == nil {
		http.Error(w, "candidate store not available", http.StatusServiceUnavailable)
		return
	}
	id, ok := candidateIDFromRequest(r)
	if !ok {
		http.Error(w, "invalid candidate id", http.StatusBadRequest)
		return
	}

	candidate, err := store.Get(r.Context(), id)
	if err != nil {
		writeCandidateStoreError(w, "get_candidate_for_promotion", id, err)
		return
	}
	if candidate == nil {
		http.Error(w, "candidate not found", http.StatusNotFound)
		return
	}
	if candidate.Status != models.CandidateStatusPending {
		http.Error(w, "candidate is not pending", http.StatusConflict)
		return
	}

	if err := reviewpacket.ValidateCandidateMutation(candidate); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	memory, err := memoryFromCandidate(candidate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	updated, created, err := store.PromoteWithMemory(r.Context(), id, memory)
	if err != nil {
		writeCandidateStoreError(w, "promote_candidate", id, err)
		return
	}
	if updated == nil || created == nil {
		log.Error().Int64("candidate_id", id).Msg("candidate promotion returned nil payload")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, candidateActionReceipt{
		Action:           "promote",
		CandidateID:      updated.ID,
		CandidateStatus:  string(updated.Status),
		MemoryID:         created.ID,
		PromotedMemoryID: updated.PromotedMemoryID,
	})
}

func (s *Service) handleRejectMemoryCandidate(w http.ResponseWriter, r *http.Request) {
	if !s.candidateQueueActive() {
		http.Error(w, "candidate queue requires ENGRAM_VNEXT_F_ENABLED=true", http.StatusForbidden)
		return
	}
	store := s.currentCandidateReviewStore()
	if store == nil {
		http.Error(w, "candidate store not available", http.StatusServiceUnavailable)
		return
	}
	id, ok := candidateIDFromRequest(r)
	if !ok {
		http.Error(w, "invalid candidate id", http.StatusBadRequest)
		return
	}

	req, err := decodeRejectCandidateRequest(r)
	if err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	candidate, err := store.Get(r.Context(), id)
	if err != nil {
		writeCandidateStoreError(w, "get_candidate_for_rejection", id, err)
		return
	}
	if candidate == nil {
		http.Error(w, "candidate not found", http.StatusNotFound)
		return
	}
	if err := reviewpacket.ValidateCandidateMutation(candidate); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	updated, err := store.TransitionToRejected(r.Context(), id, req.Reason)
	if err != nil {
		writeCandidateStoreError(w, "reject_candidate", id, err)
		return
	}
	if updated == nil {
		log.Error().Int64("candidate_id", id).Msg("candidate rejection returned nil payload")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, candidateActionReceipt{
		Action:          "reject",
		CandidateID:     updated.ID,
		CandidateStatus: string(updated.Status),
	})
}

func (s *Service) handleSupersedeMemoryCandidate(w http.ResponseWriter, r *http.Request) {
	if !s.candidateQueueActive() {
		http.Error(w, "candidate queue requires ENGRAM_VNEXT_F_ENABLED=true", http.StatusForbidden)
		return
	}
	store := s.currentCandidateReviewStore()
	if store == nil {
		http.Error(w, "candidate store not available", http.StatusServiceUnavailable)
		return
	}
	id, ok := candidateIDFromRequest(r)
	if !ok {
		http.Error(w, "invalid candidate id", http.StatusBadRequest)
		return
	}

	candidate, err := store.Get(r.Context(), id)
	if err != nil {
		writeCandidateStoreError(w, "get_candidate_for_supersede", id, err)
		return
	}
	if candidate == nil {
		http.Error(w, "candidate not found", http.StatusNotFound)
		return
	}
	if err := reviewpacket.ValidateCandidateMutation(candidate); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	updated, err := store.TransitionToSuperseded(r.Context(), id)
	if err != nil {
		writeCandidateStoreError(w, "supersede_candidate", id, err)
		return
	}
	if updated == nil {
		log.Error().Int64("candidate_id", id).Msg("candidate supersede returned nil payload")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, candidateActionReceipt{
		Action:          "supersede",
		CandidateID:     updated.ID,
		CandidateStatus: string(updated.Status),
	})
}
