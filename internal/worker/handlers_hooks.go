// Package worker provides HTTP handlers for hook-triggered lifecycle events.
package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog/log"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/feedback"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/pkg/models"
)

// sessionEndRequest is the JSON body expected by POST /api/hooks/session-end.
type sessionEndRequest struct {
	SessionID       string `json:"session_id"`
	Project         string `json:"project"`
	AgentOutputText string `json:"agent_output_text"`
}

// transcriptCreator is the minimal interface the session-end persistence
// goroutine needs from a transcript store. The concrete *gorm.TranscriptStore
// satisfies it; unit tests inject a fake to assert the real handler path
// (redact → Create) without a live database.
type transcriptCreator interface {
	Create(ctx context.Context, t *gormdb.SessionTranscript) error
}

// handleSessionEnd godoc
// @Summary Session-end hook
// @Description Receives agent output text from the stop hook and spawns async citation detection. Returns 202 Accepted immediately.
// @Tags Hooks
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body sessionEndRequest true "Session-end payload"
// @Success 202 {object} map[string]string
// @Failure 400 {string} string "session_id and project required"
// @Router /api/hooks/session-end [post]
func (s *Service) handleSessionEnd(w http.ResponseWriter, r *http.Request) {
	var req sessionEndRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.SessionID == "" || req.Project == "" {
		http.Error(w, "session_id and project required", http.StatusBadRequest)
		return
	}

	// Respond 202 immediately — all processing runs asynchronously.
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"status": "accepted"})

	// Capture stores under initMu so the goroutines hold stable references.
	s.initMu.RLock()
	injLogStore := s.injectionLogStore
	memStore := s.memoryStore
	citationStore := s.citationLogStore
	feedbackUpdater := s.feedbackUpdater
	transcriptStore := s.transcriptStore
	s.initMu.RUnlock()

	capturedSessionID := req.SessionID
	capturedProject := req.Project
	capturedOutput := req.AgentOutputText

	// Resolve the transcript creator: prefer the test seam, fall back to the
	// real store. The override is set only in unit tests; production leaves it nil
	// so the concrete *gorm.TranscriptStore is used.
	var transcriptCr transcriptCreator
	if s.transcriptCreatorOverride != nil {
		transcriptCr = s.transcriptCreatorOverride
	} else if transcriptStore != nil {
		transcriptCr = transcriptStore
	}

	// Transcript persistence: redact secrets then write to session_transcripts.
	// Gated by isCrystallizationEnabled() — reuses the existing flag so no new
	// env var is required (NFR-4: raw secrets must never reach the table).
	if isCrystallizationEnabled() && transcriptCr != nil && capturedOutput != "" {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
			defer cancel()
			redacted := privacy.RedactSecrets(capturedOutput)
			if err := transcriptCr.Create(ctx, &gormdb.SessionTranscript{
				SessionID: capturedSessionID,
				Project:   capturedProject,
				Content:   redacted,
			}); err != nil {
				log.Error().Err(err).
					Str("session_id", capturedSessionID).
					Str("project", capturedProject).
					Msg("session_end: failed to persist transcript")
			}
		}()
	}

	s.wg.Add(1)
	go s.processCitationsAsync(capturedSessionID, capturedProject, capturedOutput, injLogStore, memStore, citationStore, feedbackUpdater)
}

// processCitationsAsync performs citation detection for a finished session.
// It is always called from a goroutine and must not block the HTTP response path.
//
// CR-1 (provenance-cleanup): reads the injected memory IDs from injection_log
// (mig 106) via InjectionLogStore.GetBySession — the SOLE injection sink — instead
// of the legacy observation_injections table. GetBySession concatenates the
// memory_ids array of every matching row WITHOUT deduplicating, so a memory
// injected across multiple log rows appears more than once; this function dedups
// the IDs (Step 2 seen-map) before loading memories. Do not remove that loop.
func (s *Service) processCitationsAsync(
	sessionID, project, agentOutput string,
	injLogStore *gormdb.InjectionLogStore,
	memStore *gormdb.MemoryStore,
	citationStore *gormdb.CitationLogStore,
	feedbackUpdater *feedback.Updater,
) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	if injLogStore == nil || memStore == nil || citationStore == nil {
		log.Warn().
			Str("session_id", sessionID).
			Str("project", project).
			Msg("session_end: stores not ready, skipping citation detection")
		return
	}

	// Step 1: Retrieve the memory IDs that were injected for this session from
	// injection_log (the vNext sink — replaces the dropped observation_injections read).
	injectedIDs, err := injLogStore.GetBySession(ctx, sessionID)
	if err != nil {
		log.Error().Err(err).
			Str("session_id", sessionID).
			Msg("session_end: failed to fetch injection records")
		return
	}
	if len(injectedIDs) == 0 {
		log.Debug().
			Str("session_id", sessionID).
			Msg("session_end: no injection records found, skipping citation detection")
		return
	}

	// Step 2: Load the full memory objects for each injected ID.
	// Deduplicate IDs first — the same memory can appear across multiple log rows.
	seen := make(map[int64]struct{}, len(injectedIDs))
	memories := make([]*models.Memory, 0, len(injectedIDs))
	for _, id := range injectedIDs {
		if _, already := seen[id]; already {
			continue
		}
		seen[id] = struct{}{}

		mem, loadErr := memStore.Get(ctx, id)
		if loadErr != nil {
			// Memory may have been deleted since injection; skip silently.
			log.Debug().Err(loadErr).
				Int64("memory_id", id).
				Msg("session_end: could not load injected memory, skipping")
			continue
		}
		memories = append(memories, mem)
	}

	if len(memories) == 0 {
		return
	}

	// Step 3: Detect which memories appear in the agent output.
	// Rank-2 anti-poisoning: strip engram's OWN injected context blocks first, so an agent
	// that echoes an injected <engram-static-memories>/<engram-reinjection>/<user-behavior-rules>/
	// <open-issues> block verbatim cannot self-cite and falsely inflate citation_count. Only the
	// agent's own references (outside the injected wrappers) count toward the feedback signal.
	cleanedOutput := feedback.StripInjectedBlocks(agentOutput)
	results := feedback.DetectCitations(cleanedOutput, memories)

	// Step 3.5: Detect violations for guidance-type memories.
	// Uses the same injection-stripped output as Step 3: an echoed <user-behavior-rules> block
	// would otherwise false-flag a violation (the prohibited token from a "never X" rule appears
	// inside engram's own injected rule text, not in the agent's independent prose).
	results = feedback.DetectViolations(cleanedOutput, results, memories)

	// Step 4: Write citation results to citation_log.
	var records []gormdb.CitationRecord
	for _, res := range results {
		matchType := ""
		if res.Cited {
			matchType = "substring"
		}
		records = append(records, gormdb.CitationRecord{
			SessionID: sessionID,
			MemoryID:  res.MemoryID,
			Cited:     res.Cited,
			Violated:  res.Violated,
			MatchType: matchType,
		})
	}

	if len(records) > 0 {
		if err := citationStore.RecordBatch(ctx, records); err != nil {
			log.Error().Err(err).
				Str("session_id", sessionID).
				Msg("session_end: failed to record citation batch")
			return
		}
	}

	// Step 5: Look up session outcome and apply outcome-modulated feedback.
	var outcome string
	if s.sessionStore != nil {
		var outErr error
		outcome, outErr = s.sessionStore.GetOutcome(ctx, sessionID)
		if outErr != nil {
			log.Warn().Err(outErr).Str("session_id", sessionID).Msg("session_end: could not look up session outcome")
		}
	}

	if feedbackUpdater != nil {
		feedbackUpdater.UpdateWithOutcome(ctx, results, outcome)
	}

	// Step 6: Log summary.
	citedCount := 0
	violatedCount := 0
	for _, r := range results {
		if r.Cited {
			citedCount++
		}
		if r.Violated {
			violatedCount++
		}
	}
	// Step 7: Close open segments and evict cached embeddings for this session.
	if os.Getenv("ENGRAM_ADAPTIVE_ENABLED") == "true" {
		if s.segmentStore != nil {
			if err := s.segmentStore.CloseAllSegments(ctx, sessionID); err != nil {
				log.Warn().Err(err).Str("session_id", sessionID).Msg("session_end: close segments failed")
			}
		}
		s.clearSegmentEmbeddings(sessionID)
	}

	log.Info().
		Str("event", "session_end_processed").
		Str("session_id", sessionID).
		Str("project", project).
		Str("outcome", outcome).
		Int("cited_count", citedCount).
		Int("violated_count", violatedCount).
		Int("total_count", len(results)).
		Msg("session-end citation detection complete")
}

