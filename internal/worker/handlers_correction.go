package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/writegate"
	"github.com/thebtf/engram/pkg/models"
)

type correctionRequest struct {
	SessionID   string `json:"session_id"`
	Project     string `json:"project"`
	UserMessage string `json:"user_message"`
}

// handleCorrection processes user correction signals from the UserPromptSubmit hook.
// Finds similar memories via Jaccard and auto-supersedes the stale one.
func (s *Service) handleCorrection(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ENGRAM_ADAPTIVE_ENABLED") != "true" {
		writeJSON(w, map[string]string{"status": "disabled"})
		return
	}

	var req correctionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	req.UserMessage = strings.TrimSpace(req.UserMessage)
	if req.Project == "" || req.UserMessage == "" {
		http.Error(w, "project and user_message required", http.StatusBadRequest)
		return
	}

	s.initMu.RLock()
	memStore := s.memoryStore
	s.initMu.RUnlock()

	if memStore == nil {
		http.Error(w, "memory store not ready", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"status": "accepted"})

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.processCorrectionAsync(req)
	}()
}

func (s *Service) processCorrectionAsync(req correctionRequest) {
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()

	s.initMu.RLock()
	memStore := s.memoryStore
	s.initMu.RUnlock()

	if memStore == nil {
		return
	}

	existing, err := memStore.List(ctx, req.Project, 100)
	if err != nil {
		log.Error().Err(err).Str("project", req.Project).Msg("correction: list failed")
		return
	}

	gateResult := writegate.Check(ctx, req.UserMessage, existing)
	if gateResult.SimilarExisting == nil || gateResult.MaxJaccard < 0.3 {
		newMem := &models.Memory{
			Project:       req.Project,
			Content:       req.UserMessage,
			SourceAgent:   "user_correction",
			Tags:          []string{"user_correction"},
			EpistemicType: "guidance",
		}
		// Correction always sets EpistemicType; use CreateWithLifecycle so the
		// field is persisted without touching the plain Create path.
		if _, createErr := memStore.CreateWithLifecycle(ctx, newMem); createErr != nil {
			log.Error().Err(createErr).Msg("correction: create new memory failed")
		} else {
			log.Info().Str("project", req.Project).Msg("correction: stored as new memory (no similar found)")
		}
		return
	}

	similarID := *gateResult.SimilarExisting
	newMem := &models.Memory{
		Project:       req.Project,
		Content:       req.UserMessage,
		SourceAgent:   "user_correction",
		Tags:          []string{"user_correction", "supersedes"},
		SupersedesID:  &similarID,
		EpistemicType: "guidance",
	}
	created, createErr := memStore.CreateWithLifecycle(ctx, newMem)
	if createErr != nil {
		log.Error().Err(createErr).Int64("supersedes", similarID).Msg("correction: create superseding memory failed")
		return
	}

	now := time.Now().UTC()
	if updateErr := memStore.UpdateLifecycleFields(ctx, similarID, map[string]any{
		"superseded_by": created.ID,
		"valid_until":   now,
		"status":        "superseded",
	}); updateErr != nil {
		log.Error().Err(updateErr).
			Int64("old_memory_id", similarID).
			Int64("new_memory_id", created.ID).
			Msg("correction: mark superseded failed — new memory created but old not marked; manual reconciliation may be needed")
	}

	log.Info().
		Int64("old_id", similarID).
		Int64("new_id", created.ID).
		Float64("jaccard", gateResult.MaxJaccard).
		Str("project", req.Project).
		Msg("correction: auto-superseded stale memory")
}
