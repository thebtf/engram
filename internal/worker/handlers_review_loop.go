package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/reviewpacket"
	"github.com/thebtf/engram/pkg/models"
)

type candidateReviewLoopAtomicPromotionStore interface {
	PreserveWithMemoryAndSnapshot(ctx context.Context, snapshotStore *gormdb.SnapshotStore, candidateID int64, mem *models.Memory, snapshot *models.BulkOpSnapshot, actor string) (*models.CrystallizationCandidate, *models.Memory, *models.BulkOpSnapshot, error)
}

type candidateReviewLoopAtomicTransitionStore interface {
	TransitionToSuppressedWithSnapshot(ctx context.Context, snapshotStore *gormdb.SnapshotStore, id int64, reason string, snapshot *models.BulkOpSnapshot, actor string) (*models.CrystallizationCandidate, *models.BulkOpSnapshot, error)
}

type reviewPacketActionRequest struct {
	ActionType string `json:"action_type"`
	Reason     string `json:"reason,omitempty"`
}

func decodeReviewPacketActionRequest(r *http.Request) (reviewPacketActionRequest, error) {
	var req reviewPacketActionRequest
	if r.Body == nil {
		return req, nil
	}
	defer r.Body.Close()

	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil && !errors.Is(err, io.EOF) {
		return req, err
	}
	req.ActionType = strings.TrimSpace(req.ActionType)
	req.Reason = strings.TrimSpace(req.Reason)
	return req, nil
}

func reviewLoopStatusAndLimit(r *http.Request) (models.CandidateStatus, int, bool) {
	status := models.CandidateStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if status == "" {
		status = models.CandidateStatusPending
	}
	if !status.IsValid() {
		return "", 0, false
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > 100 {
			return "", 0, false
		}
		limit = n
	}
	return status, limit, true
}

func reviewLoopProject(r *http.Request) string {
	return normalizeCandidateProject(r.URL.Query().Get("project"))
}

func reviewLoopPacketTypeSupported(packetType string) bool {
	packetType = strings.TrimSpace(packetType)
	return packetType == "" || packetType == reviewpacket.ReviewPacketTypeUsefulnessNoise || packetType == reviewpacket.CandidatePacketKind
}

func reviewLoopPacketIDFromRequest(r *http.Request) string {
	packetID := strings.TrimSpace(chi.URLParam(r, "packetID"))
	if packetID == "" {
		packetID = strings.TrimSpace(r.URL.Query().Get("packet_id"))
	}
	return packetID
}

func writeReviewActionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, reviewpacket.ErrUnsupportedReviewAction):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, reviewpacket.ErrInvalidReviewPacket):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, reviewpacket.ErrStaleReviewPacket):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusConflict)
	}
}

func (s *Service) handleReadMemoryReviewMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.candidateQueueActive() {
		writeJSONStatus(w, http.StatusForbidden, reviewpacket.GatedReviewMetrics("candidate queue requires "+candidateQueueFlag+"=true", time.Now().UTC()))
		return
	}
	store := s.currentCandidateReviewStore()
	if store == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, reviewpacket.GatedReviewMetrics("candidate store not available", time.Now().UTC()))
		return
	}
	status, limit, ok := reviewLoopStatusAndLimit(r)
	if !ok {
		http.Error(w, "invalid review metrics status or limit", http.StatusBadRequest)
		return
	}

	candidates, err := store.ListByStatus(r.Context(), reviewLoopProject(r), status, limit)
	if err != nil {
		if writeCandidateContextError(w, err) {
			return
		}
		writeJSONStatus(w, http.StatusInternalServerError, reviewpacket.ErrorReviewMetrics(err, time.Now().UTC()))
		return
	}
	writeJSON(w, reviewpacket.BuildReviewMetrics(candidates, limit, time.Now().UTC()))
}

func (s *Service) handleReadMemoryReviewQueue(w http.ResponseWriter, r *http.Request) {
	if !s.candidateQueueActive() {
		writeJSONStatus(w, http.StatusForbidden, reviewpacket.GatedReviewQueue("candidate queue requires "+candidateQueueFlag+"=true", 0, time.Now().UTC()))
		return
	}
	status, limit, ok := reviewLoopStatusAndLimit(r)
	if !ok {
		http.Error(w, "invalid review queue status or limit", http.StatusBadRequest)
		return
	}
	if !reviewLoopPacketTypeSupported(r.URL.Query().Get("packet_type")) {
		writeJSON(w, reviewpacket.GatedReviewQueue("unsupported packet_type for CR-008 review queue", limit, time.Now().UTC()))
		return
	}
	store := s.currentCandidateReviewStore()
	if store == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, reviewpacket.GatedReviewQueue("candidate store not available", limit, time.Now().UTC()))
		return
	}

	candidates, err := store.ListByStatus(r.Context(), reviewLoopProject(r), status, limit)
	if err != nil {
		if writeCandidateContextError(w, err) {
			return
		}
		log.Error().Err(err).Str("status", string(status)).Msg("read review queue failed")
		writeJSONStatus(w, http.StatusInternalServerError, reviewpacket.ErrorReviewQueue(err, limit, time.Now().UTC()))
		return
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("risky_only")), "true") {
		metrics := reviewpacket.BuildReviewMetrics(candidates, limit, time.Now().UTC())
		if metrics.State == reviewpacket.ReviewStateSparse {
			writeJSON(w, reviewpacket.GatedReviewQueue("risky_only requires complete confidence telemetry", limit, time.Now().UTC()))
			return
		}
		candidates = filterRiskyReviewCandidates(candidates)
	}
	writeJSON(w, reviewpacket.BuildReviewQueue(candidates, status, limit, time.Now().UTC()))
}

func filterRiskyReviewCandidates(candidates []*models.CrystallizationCandidate) []*models.CrystallizationCandidate {
	filtered := make([]*models.CrystallizationCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate != nil && candidate.Confidence > 0 && candidate.Confidence < 0.5 {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func (s *Service) handleGetMemoryReviewPacketDetail(w http.ResponseWriter, r *http.Request) {
	candidate, packetID, ok := s.loadReviewPacketCandidate(w, r)
	if !ok {
		return
	}
	if reviewpacket.FromCandidate(candidate).PacketID != packetID {
		writeReviewActionError(w, fmt.Errorf("%w: current packet_id is %s", reviewpacket.ErrStaleReviewPacket, reviewpacket.FromCandidate(candidate).PacketID))
		return
	}
	writeJSON(w, reviewpacket.DetailFromCandidate(candidate, time.Now().UTC()))
}

func (s *Service) handlePreviewMemoryReviewPacketAction(w http.ResponseWriter, r *http.Request) {
	candidate, packetID, ok := s.loadReviewPacketCandidate(w, r)
	if !ok {
		return
	}
	req, err := decodeReviewPacketActionRequest(r)
	if err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	preview, err := reviewpacket.PreviewReviewAction(candidate, packetID, req.ActionType, time.Now().UTC())
	if err != nil {
		writeReviewActionError(w, err)
		return
	}
	writeJSON(w, preview)
}

func (s *Service) handleApplyMemoryReviewPacketAction(w http.ResponseWriter, r *http.Request) {
	candidate, packetID, ok := s.loadReviewPacketCandidate(w, r)
	if !ok {
		return
	}
	req, err := decodeReviewPacketActionRequest(r)
	if err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := reviewpacket.PreviewReviewAction(candidate, packetID, req.ActionType, time.Now().UTC()); err != nil {
		writeReviewActionError(w, err)
		return
	}

	switch req.ActionType {
	case reviewpacket.ReviewActionPreserve:
		s.applyPreserveReviewPacketAction(w, r, candidate, packetID)
	case reviewpacket.ReviewActionSuppress:
		s.applySuppressReviewPacketAction(w, r, candidate, packetID, req.Reason)
	default:
		writeReviewActionError(w, fmt.Errorf("%w: %s", reviewpacket.ErrUnsupportedReviewAction, req.ActionType))
	}
}

func (s *Service) loadReviewPacketCandidate(w http.ResponseWriter, r *http.Request) (*models.CrystallizationCandidate, string, bool) {
	if !s.candidateQueueActive() {
		http.Error(w, "candidate queue requires "+candidateQueueFlag+"=true", http.StatusForbidden)
		return nil, "", false
	}
	store := s.currentCandidateReviewStore()
	if store == nil {
		http.Error(w, "candidate store not available", http.StatusServiceUnavailable)
		return nil, "", false
	}
	packetID := reviewLoopPacketIDFromRequest(r)
	candidateID, err := reviewpacket.CandidateIDFromPacketID(packetID)
	if err != nil {
		writeReviewActionError(w, err)
		return nil, "", false
	}
	candidate, err := store.Get(r.Context(), candidateID)
	if err != nil {
		writeCandidateStoreError(w, "get_review_packet_candidate", candidateID, err)
		return nil, "", false
	}
	if candidate == nil {
		http.Error(w, "candidate not found", http.StatusNotFound)
		return nil, "", false
	}
	return candidate, packetID, true
}

func (s *Service) applyPreserveReviewPacketAction(w http.ResponseWriter, r *http.Request, candidate *models.CrystallizationCandidate, packetID string) {
	store := s.currentCandidateReviewStore()
	atomicStore, ok := store.(candidateReviewLoopAtomicPromotionStore)
	if !ok {
		http.Error(w, "candidate review preserve requires atomic preserve store", http.StatusServiceUnavailable)
		return
	}
	memory, err := memoryFromCandidate(candidate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	actor := candidateReviewActorFromContext(r.Context())
	snapshot, err := s.newCandidateReviewSnapshot(reviewpacket.ReviewActionPreserve, candidate, actor)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	snapshotStore, ok := s.currentCandidateReviewSnapshotStore().(*gormdb.SnapshotStore)
	if snapshot != nil && (!ok || snapshotStore == nil) {
		http.Error(w, "candidate review snapshot store is not configured", http.StatusServiceUnavailable)
		return
	}
	updated, created, createdSnapshot, err := atomicStore.PreserveWithMemoryAndSnapshot(r.Context(), snapshotStore, candidate.ID, memory, snapshot, actor)
	if err != nil {
		if strings.Contains(err.Error(), "snapshot") || strings.Contains(err.Error(), "candidate_review audit store") {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		writeCandidateStoreError(w, reviewpacket.ReviewActionPreserve, candidate.ID, err)
		return
	}
	writeJSON(w, reviewpacket.NewReviewActionReceipt(reviewpacket.ReviewActionPreserve, packetID, updated, createdSnapshot, created))
}

func (s *Service) applySuppressReviewPacketAction(w http.ResponseWriter, r *http.Request, candidate *models.CrystallizationCandidate, packetID string, reason string) {
	store := s.currentCandidateReviewStore()
	atomicStore, ok := store.(candidateReviewLoopAtomicTransitionStore)
	if !ok {
		http.Error(w, "candidate review suppress requires atomic suppress store", http.StatusServiceUnavailable)
		return
	}
	actor := candidateReviewActorFromContext(r.Context())
	snapshot, err := s.newCandidateReviewSnapshot(reviewpacket.ReviewActionSuppress, candidate, actor)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	snapshotStore, ok := s.currentCandidateReviewSnapshotStore().(*gormdb.SnapshotStore)
	if snapshot != nil && (!ok || snapshotStore == nil) {
		http.Error(w, "candidate review snapshot store is not configured", http.StatusServiceUnavailable)
		return
	}
	updated, createdSnapshot, err := atomicStore.TransitionToSuppressedWithSnapshot(r.Context(), snapshotStore, candidate.ID, reason, snapshot, actor)
	if err != nil {
		if strings.Contains(err.Error(), "snapshot") || strings.Contains(err.Error(), "candidate_review audit store") {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		writeCandidateStoreError(w, reviewpacket.ReviewActionSuppress, candidate.ID, err)
		return
	}
	writeJSON(w, reviewpacket.NewReviewActionReceipt(reviewpacket.ReviewActionSuppress, packetID, updated, createdSnapshot, nil))
}
