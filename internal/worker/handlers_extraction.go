package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/pkg/models"
)

type codeExtractionRequest struct {
	SessionID string           `json:"session_id"`
	Project   string           `json:"project"`
	Changes   []codeChangeItem `json:"changes"`
}

type codeChangeItem struct {
	FilePath      string `json:"file_path"`
	Action        string `json:"action"`
	CommitMessage string `json:"commit_message"`
	Context       string `json:"context"`
}

// maxExtractedPerRequest caps the number of memories stored per individual
// extraction request. Per-session enforcement across multiple requests would
// require a persistent counter and is tracked separately.
const maxExtractedPerRequest = 5

// handleCodeExtraction processes coding decision extractions from the stop hook.
func (s *Service) handleCodeExtraction(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ENGRAM_ADAPTIVE_ENABLED") != "true" {
		writeJSON(w, map[string]string{"status": "disabled"})
		return
	}

	var req codeExtractionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Project == "" || len(req.Changes) == 0 {
		http.Error(w, "project and changes required", http.StatusBadRequest)
		return
	}
	// Cap incoming slice at the store cap to avoid spinning the loop over
	// unbounded payloads (security review S2 — CPU/memory amplifier).
	if len(req.Changes) > maxExtractedPerRequest {
		req.Changes = req.Changes[:maxExtractedPerRequest]
	}

	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"status": "accepted"})

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.processCodeExtractionAsync(req)
	}()
}

func (s *Service) processCodeExtractionAsync(req codeExtractionRequest) {
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	s.initMu.RLock()
	memStore := s.memoryStore
	s.initMu.RUnlock()

	if memStore == nil {
		return
	}

	stored := 0
	for _, change := range req.Changes {
		if stored >= maxExtractedPerRequest {
			break
		}
		if change.Context == "" && change.CommitMessage == "" {
			continue
		}

		content := formatCodeDecision(change)
		if content == "" {
			continue
		}

		mem := &models.Memory{
			Project:       req.Project,
			Content:       content,
			SourceAgent:   "code_extraction",
			Tags:          []string{"code_event", "procedural"},
			EpistemicType: "procedural",
			Tier:          "episodic",
		}

		// Extraction always sets EpistemicType/Tier; use CreateWithLifecycle so
		// these fields are persisted without touching the plain Create path.
		if _, err := memStore.CreateWithLifecycle(ctx, mem); err != nil {
			log.Error().Err(err).Str("file", change.FilePath).Msg("code-extraction: store failed")
			continue
		}
		stored++
	}

	log.Info().
		Str("session_id", req.SessionID).
		Str("project", req.Project).
		Int("changes", len(req.Changes)).
		Int("stored", stored).
		Msg("code-extraction: complete")
}

func formatCodeDecision(change codeChangeItem) string {
	if change.Context != "" {
		return change.Context
	}
	if change.CommitMessage != "" {
		if change.FilePath != "" {
			return change.FilePath + ": " + change.CommitMessage
		}
		return change.CommitMessage
	}
	return ""
}
