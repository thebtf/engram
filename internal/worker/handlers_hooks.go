// Package worker provides HTTP handlers for hook-triggered lifecycle events.
package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/crystallization"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/feedback"
	"github.com/thebtf/engram/pkg/models"
)

// sessionEndRequest is the JSON body expected by POST /api/hooks/session-end.
type sessionEndRequest struct {
	SessionID       string `json:"session_id"`
	Project         string `json:"project"`
	AgentOutputText string `json:"agent_output_text"`
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

	// Respond 202 immediately — citation detection runs asynchronously.
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"status": "accepted"})

	// Capture stores under initMu so the goroutine holds stable references.
	s.initMu.RLock()
	injectionStore := s.injectionStore
	memStore := s.memoryStore
	citationStore := s.citationLogStore
	feedbackUpdater := s.feedbackUpdater
	s.initMu.RUnlock()

	capturedSessionID := req.SessionID
	capturedProject := req.Project
	capturedOutput := req.AgentOutputText

	s.wg.Add(1)
	go s.processCitationsAsync(capturedSessionID, capturedProject, capturedOutput, injectionStore, memStore, citationStore, feedbackUpdater)
}

// processCitationsAsync performs citation detection for a finished session.
// It is always called from a goroutine and must not block the HTTP response path.
func (s *Service) processCitationsAsync(
	sessionID, project, agentOutput string,
	injectionStore *gormdb.InjectionStore,
	memStore *gormdb.MemoryStore,
	citationStore *gormdb.CitationLogStore,
	feedbackUpdater *feedback.Updater,
) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	if injectionStore == nil || memStore == nil || citationStore == nil {
		log.Warn().
			Str("session_id", sessionID).
			Str("project", project).
			Msg("session_end: stores not ready, skipping citation detection")
		return
	}

	// Step 1: Retrieve the memory IDs that were injected for this session.
	injections, err := injectionStore.GetInjectionsBySession(ctx, sessionID)
	if err != nil {
		log.Error().Err(err).
			Str("session_id", sessionID).
			Msg("session_end: failed to fetch injection records")
		return
	}
	if len(injections) == 0 {
		log.Debug().
			Str("session_id", sessionID).
			Msg("session_end: no injection records found, skipping citation detection")
		return
	}

	// Step 2: Load the full memory objects for each injected ID.
	// Deduplicate IDs first — the same memory can appear in multiple sections.
	seen := make(map[int64]struct{}, len(injections))
	memories := make([]*models.Memory, 0, len(injections))
	for _, inj := range injections {
		if _, already := seen[inj.ObservationID]; already {
			continue
		}
		seen[inj.ObservationID] = struct{}{}

		mem, loadErr := memStore.Get(ctx, inj.ObservationID)
		if loadErr != nil {
			// Memory may have been deleted since injection; skip silently.
			log.Debug().Err(loadErr).
				Int64("memory_id", inj.ObservationID).
				Msg("session_end: could not load injected memory, skipping")
			continue
		}
		memories = append(memories, mem)
	}

	if len(memories) == 0 {
		return
	}

	// Step 3: Detect which memories appear in the agent output.
	results := feedback.DetectCitations(agentOutput, memories)

	// Step 3.5: Detect violations for guidance-type memories.
	results = feedback.DetectViolations(agentOutput, results, memories)

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

	// Step 8: Crystallization pipeline — extract decisions from agent output and
	// store them as episodic memories. Gated by ENGRAM_CRYSTALLIZATION_ENABLED.
	// Failure here must never affect the 202 response (fire-and-forget discipline):
	// all errors are logged and execution continues.
	if isCrystallizationEnabled() {
		s.runCrystallization(ctx, sessionID, project, agentOutput, memStore)
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

// runCrystallization extracts decision patterns from agent output and persists them
// as episodic memories. It is called from processCitationsAsync and must not panic
// or propagate errors upward — all failures are logged and swallowed.
func (s *Service) runCrystallization(
	ctx context.Context,
	sessionID, project, agentOutput string,
	memStore *gormdb.MemoryStore,
) {
	if memStore == nil {
		log.Warn().
			Str("session_id", sessionID).
			Msg("crystallization: memoryStore not ready, skipping")
		return
	}

	decisions := crystallization.ExtractDecisions(agentOutput)
	if len(decisions) == 0 {
		log.Debug().
			Str("session_id", sessionID).
			Msg("crystallization: no decision patterns found")
		return
	}

	mems := crystallization.BuildMemories(decisions, sessionID, project)

	s.initMu.RLock()
	auditStore := s.auditStore
	s.initMu.RUnlock()

	storedCount := 0
	for _, mem := range mems {
		created, err := memStore.Create(ctx, mem)
		if err != nil {
			log.Error().Err(err).
				Str("session_id", sessionID).
				Msg("crystallization: failed to store decision memory")
			continue
		}
		storedCount++

		// Emit audit event when audit store is wired — mirrors the Create audit
		// path in tools_memory.go. Nil-guarded: crystallization runs even when
		// ENGRAM_VNEXT_ENABLED is off (audit store nil in that case).
		if auditStore != nil {
			afterJSON, jsonErr := json.Marshal(created)
			if jsonErr == nil {
				raw := json.RawMessage(afterJSON)
				memID := created.ID
				_ = auditStore.Log(ctx, gormdb.AuditLogEntry{
					MemoryID:        &memID,
					Action:          "create",
					Actor:           "crystallization",
					SourceSessionID: sessionID,
					AfterState:      &raw,
					Reason:          "session-end crystallization",
				})
			}
		}
	}

	log.Info().
		Str("event", "crystallization_complete").
		Str("session_id", sessionID).
		Str("project", project).
		Int("decisions_found", len(decisions)).
		Int("memories_stored", storedCount).
		Msg("crystallization: stored decisions as episodic memories")
}
