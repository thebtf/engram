// Package worker provides HTTP handlers for hook-triggered lifecycle events.
package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	// Respond 202 immediately — all processing runs asynchronously.
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"status": "accepted"})

	// Capture stores under initMu so the goroutines hold stable references.
	s.initMu.RLock()
	injectionStore := s.injectionStore
	memStore := s.memoryStore
	citationStore := s.citationLogStore
	feedbackUpdater := s.feedbackUpdater
	s.initMu.RUnlock()

	capturedSessionID := req.SessionID
	capturedProject := req.Project
	capturedOutput := req.AgentOutputText

	// Crystallization runs as an independent goroutine — it only needs memStore,
	// sessionID, project, and non-empty agent output. It must NOT be gated by the
	// citation pipeline's prerequisite checks (nil injectionStore/citationStore,
	// zero injection records, or citation insert failure).
	if isCrystallizationEnabled() && memStore != nil && capturedOutput != "" {
		crystallize := s.runCrystallization
		if s.crystallizeFunc != nil {
			crystallize = s.crystallizeFunc
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
			defer cancel()
			crystallize(ctx, capturedSessionID, capturedProject, capturedOutput, memStore)
		}()
	}

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

// crystallizationFingerprint returns a stable hex fingerprint for a decision memory
// used to detect duplicates across double-fire or replay scenarios.
// The fingerprint is sha256(sessionID + ":" + content), truncated to 16 hex chars.
func crystallizationFingerprint(sessionID, content string) string {
	h := sha256.Sum256([]byte(sessionID + ":" + content))
	return hex.EncodeToString(h[:])[:16]
}

// runCrystallizationAuditAsync emits a single audit event in a fire-and-forget
// goroutine with a 10-second detached context, panic recovery, and structured
// error logging. This mirrors the runAuditAsync pattern from internal/mcp/audit_helpers.go.
//
// Only called when ENGRAM_VNEXT_ENABLED=true (auditStore is non-nil in that case).
func runCrystallizationAuditAsync(auditStore *gormdb.AuditStore, sessionID string, created *models.Memory) {
	memID := created.ID
	afterJSON, jsonErr := json.Marshal(created)
	if jsonErr != nil {
		log.Error().Err(jsonErr).Int64("memory_id", memID).Msg("crystallization: failed to marshal audit state")
		return
	}
	raw := json.RawMessage(afterJSON)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error().
					Str("audit_label", "crystallization").
					Int64("memory_id", memID).
					Interface("panic", r).
					Msg("audit: goroutine panic recovered")
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := auditStore.Log(ctx, gormdb.AuditLogEntry{
			MemoryID:        &memID,
			Action:          "create",
			Actor:           "crystallization",
			SourceSessionID: sessionID,
			AfterState:      &raw,
			Reason:          "session-end crystallization",
		}); err != nil {
			log.Error().
				Err(err).
				Str("audit_label", "crystallization").
				Int64("memory_id", memID).
				Msg("audit: async write failed")
		}
	}()
}

// runCrystallization extracts decision patterns from agent output and persists them.
//
// Routing (T025 / B9 resolution):
//   - ENGRAM_VNEXT_F_ENABLED=true AND candidateStore ready → extracted decisions land in
//     crystallization_candidates (status='pending') for explicit operator promotion.
//     This IS the B9 resolution: no auto-promotion; the candidate path is the gated
//     promotion surface (operator decision #2766).
//   - Otherwise → legacy path: extracted decisions written to memories as episodic
//     via CreateWithLifecycleIfTagAbsent (W2-TG3 idempotency preserved).
//
// Independence contract: this function requires only memStore, sessionID, project, and
// agentOutput. It is NOT gated by citation pipeline prerequisites.
//
// Idempotency:
//   - Legacy path: sha256 fingerprint stored as tag "fp:<hex>" with partial unique check.
//   - Candidate path: sha256 fingerprint stored in the fingerprint column with a partial
//     unique index on status='pending' (migration 132).
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

	s.initMu.RLock()
	auditStore := s.auditStore
	candidateStore := s.candidateStore
	vnextEnabled := os.Getenv("ENGRAM_VNEXT_ENABLED") == "true"
	s.initMu.RUnlock()

	// ENGRAM_VNEXT_F_ENABLED=true: route to candidate store (B9 resolution).
	if crystallization.VnextFEnabled() && candidateStore != nil {
		s.runCrystallizationCandidatePath(ctx, decisions, sessionID, project, candidateStore)
		return
	}

	// Legacy path: write directly to memories table.
	mems := crystallization.BuildMemories(decisions, sessionID, project)

	existingFPs := make(map[string]struct{})

	storedCount := 0
	skippedCount := 0
	for i, mem := range mems {
		fp := crystallizationFingerprint(sessionID, decisions[i].Text)
		if _, dup := existingFPs[fp]; dup {
			skippedCount++
			log.Debug().
				Str("session_id", sessionID).
				Str("fingerprint", fp).
				Msg("crystallization: skipping duplicate decision memory")
			continue
		}

		// Embed the fingerprint tag so future runs can detect this decision.
		fpTag := "fp:" + fp
		mem.Tags = append(mem.Tags, fpTag)

		// Crystallization memories carry lifecycle fields (Tier="episodic",
		// EpistemicType="decision"); use CreateWithLifecycle so they are persisted
		// without touching the plain Create path (flag-gated: ENGRAM_CRYSTALLIZATION_ENABLED).
		created, duplicate, err := memStore.CreateWithLifecycleIfTagAbsent(ctx, mem, fpTag)
		if err != nil {
			log.Error().Err(err).
				Str("session_id", sessionID).
				Msg("crystallization: failed to store decision memory")
			continue
		}
		if duplicate {
			existingFPs[fp] = struct{}{}
			skippedCount++
			log.Debug().
				Str("session_id", sessionID).
				Str("fingerprint", fp).
				Msg("crystallization: skipping duplicate decision memory")
			continue
		}
		existingFPs[fp] = struct{}{}
		storedCount++

		// Emit audit event asynchronously — matches the runAuditAsync pattern from
		// internal/mcp/audit_helpers.go. Gated on ENGRAM_VNEXT_ENABLED (audit store
		// is nil when the flag is off).
		if vnextEnabled && auditStore != nil {
			runCrystallizationAuditAsync(auditStore, sessionID, created)
		}
	}

	log.Info().
		Str("event", "crystallization_complete").
		Str("session_id", sessionID).
		Str("project", project).
		Int("decisions_found", len(decisions)).
		Int("memories_stored", storedCount).
		Int("memories_skipped_dup", skippedCount).
		Msg("crystallization: stored decisions as episodic memories")
}

// runCrystallizationCandidatePath handles the ENGRAM_VNEXT_F_ENABLED candidate routing path.
// Each extracted decision is routed via crystallization.RouteDecision which enforces
// fingerprint-based idempotency and writes to crystallization_candidates.
// Satisfies B9: no auto-promotion to memories; operator must call promote_candidate explicitly.
func (s *Service) runCrystallizationCandidatePath(
	ctx context.Context,
	decisions []crystallization.ExtractedDecision,
	sessionID, project string,
	candidateStore *gormdb.CandidateStore,
) {
	createdCount := 0
	dupCount := 0
	for _, decision := range decisions {
		result, err := crystallization.RouteDecision(ctx, decision, sessionID, project, candidateStore)
		if err != nil {
			log.Error().Err(err).
				Str("session_id", sessionID).
				Msg("crystallization: candidate route failed")
			continue
		}
		if result == nil {
			// Flag flipped to off between check and here — fall through to caller's return.
			continue
		}
		if result.Duplicate {
			dupCount++
			log.Debug().
				Str("session_id", sessionID).
				Msg("crystallization: skipping duplicate candidate (fingerprint exists)")
		} else {
			createdCount++
			log.Debug().
				Str("session_id", sessionID).
				Int64("candidate_id", result.CandidateID).
				Msg("crystallization: created pending candidate")
		}
	}

	log.Info().
		Str("event", "crystallization_candidates_complete").
		Str("session_id", sessionID).
		Str("project", project).
		Int("decisions_found", len(decisions)).
		Int("candidates_created", createdCount).
		Int("candidates_skipped_dup", dupCount).
		Msg("crystallization: decisions routed to pending candidates (vnext-f)")
}
