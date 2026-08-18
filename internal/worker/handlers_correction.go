package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/writegate"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm"
)

type correctionRequest struct {
	SessionID   string `json:"session_id"`
	Project     string `json:"project"`
	UserMessage string `json:"user_message"`
	actor       string
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
	req.actor = auditcontext.Actor(r.Context())
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
	ctx := auditcontext.WithSourceSession(auditcontext.WithActor(s.ctx, req.actor), req.SessionID)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
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
		newMem := &models.Memory{Project: req.Project, Content: req.UserMessage, SourceAgent: "user_correction", Tags: []string{"user_correction"}, EpistemicType: "guidance"}
		if _, err := memStore.CreateWithLifecycle(ctx, newMem); err != nil {
			log.Error().Err(err).Msg("correction: create new memory failed")
		}
		return
	}
	similarID := *gateResult.SimilarExisting
	newMem := &models.Memory{Project: req.Project, Content: req.UserMessage, SourceAgent: "user_correction", Tags: []string{"user_correction", "supersedes"}, SupersedesID: &similarID, EpistemicType: "guidance"}
	var created *models.Memory
	now := time.Now().UTC()
	err = memStore.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		created, err = memStore.CreateWithLifecycleTx(ctx, tx, newMem)
		if err != nil {
			return fmt.Errorf("create superseding memory: %w", err)
		}
		if err := memStore.MarkSupersededTx(ctx, tx, similarID, created.ID); err != nil {
			return fmt.Errorf("mark superseded memory: %w", err)
		}
		if err := memStore.UpdateLifecycleFieldsTx(ctx, tx, similarID, map[string]any{"valid_until": now}); err != nil {
			return fmt.Errorf("set superseded memory validity: %w", err)
		}
		return nil
	})
	if err != nil {
		log.Error().Err(err).Int64("old_memory_id", similarID).Msg("correction: supersede transaction failed")
		return
	}
	log.Info().Int64("old_id", similarID).Int64("new_id", created.ID).Float64("jaccard", gateResult.MaxJaccard).Str("project", req.Project).Msg("correction: auto-superseded stale memory")
}
