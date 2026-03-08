package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/backfill/extract"
	"github.com/thebtf/engram/pkg/models"
)

// BackfillRequest is the request body for POST /api/backfill.
type BackfillRequest struct {
	// SessionID is a unique identifier for the source session (e.g. filename hash).
	SessionID string `json:"session_id"`
	// Project is the project path from session metadata.
	Project string `json:"project"`
	// RunID groups observations from the same backfill run (for rollback).
	RunID string `json:"run_id"`
	// Observations are the extracted observations to store.
	Observations []BackfillObservation `json:"observations"`
}

// BackfillObservation is a single observation from a backfill extraction.
type BackfillObservation struct {
	Type      string   `json:"type"`
	Outcome   string   `json:"outcome"`
	Title     string   `json:"title"`
	Narrative string   `json:"narrative"`
	Concepts  []string `json:"concepts"`
	Files     []string `json:"files"`
}

// BackfillResponse is the response for POST /api/backfill.
type BackfillResponse struct {
	Stored  int `json:"stored"`
	Skipped int `json:"skipped"`
	Errors  int `json:"errors"`
}

// BackfillStatus holds status information for GET /api/backfill/status.
type BackfillStatus struct {
	TotalRuns        int                 `json:"total_runs"`
	ActiveRuns       map[string]*RunInfo `json:"active_runs"`
	TotalObservations int               `json:"total_observations"`
}

// RunInfo tracks per-run statistics.
type RunInfo struct {
	RunID    string `json:"run_id"`
	Stored   int    `json:"stored"`
	Skipped  int    `json:"skipped"`
	Errors   int    `json:"errors"`
	Sessions int    `json:"sessions"`
}

// backfillTracker tracks active backfill runs in memory.
type backfillTracker struct {
	mu   sync.RWMutex
	runs map[string]*RunInfo
}

func newBackfillTracker() *backfillTracker {
	return &backfillTracker{runs: make(map[string]*RunInfo)}
}

func (t *backfillTracker) getOrCreate(runID string) *RunInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ri, ok := t.runs[runID]; ok {
		return ri
	}
	ri := &RunInfo{RunID: runID}
	t.runs[runID] = ri
	return ri
}

func (t *backfillTracker) snapshot() map[string]*RunInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()
	cp := make(map[string]*RunInfo, len(t.runs))
	for k, v := range t.runs {
		vcopy := *v
		cp[k] = &vcopy
	}
	return cp
}

// handleBackfillIngest handles POST /api/backfill — stores extracted observations.
func (s *Service) handleBackfillIngest(w http.ResponseWriter, r *http.Request) {
	var req BackfillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.RunID == "" {
		http.Error(w, "run_id is required", http.StatusBadRequest)
		return
	}
	if len(req.Observations) == 0 {
		json.NewEncoder(w).Encode(BackfillResponse{})
		return
	}

	s.initMu.RLock()
	obsStore := s.observationStore
	s.initMu.RUnlock()

	if obsStore == nil {
		http.Error(w, "Observation store not initialized", http.StatusServiceUnavailable)
		return
	}

	resp := BackfillResponse{}
	runInfo := s.backfillTracker.getOrCreate(req.RunID)

	for _, bo := range req.Observations {
		// Convert to XMLObservation for validation
		xo := extract.XMLObservation{
			Type:      bo.Type,
			Outcome:   bo.Outcome,
			Title:     bo.Title,
			Narrative: bo.Narrative,
		}

		// Validate type and outcome
		if !extract.ValidTypes[bo.Type] {
			log.Warn().Str("type", bo.Type).Str("title", bo.Title).Msg("backfill: invalid observation type")
			resp.Errors++
			continue
		}
		if !extract.ValidOutcomes[bo.Outcome] {
			log.Warn().Str("outcome", bo.Outcome).Str("title", bo.Title).Msg("backfill: invalid outcome")
			resp.Errors++
			continue
		}

		obs := extract.ConvertToObservation(xo, req.Project)
		obs.Concepts = bo.Concepts
		obs.FilesRead = bo.Files

		// Add backfill metadata
		obs.Scope = models.ScopeProject

		sdkSessionID := fmt.Sprintf("backfill-%s-%s", req.RunID, req.SessionID)
		_, _, err := obsStore.StoreObservation(r.Context(), sdkSessionID, req.Project, obs, 0, 0)
		if err != nil {
			log.Error().Err(err).Str("title", bo.Title).Msg("backfill: failed to store observation")
			resp.Errors++
			continue
		}

		resp.Stored++
	}

	// Update run tracker
	runInfo.Stored += resp.Stored
	runInfo.Skipped += resp.Skipped
	runInfo.Errors += resp.Errors
	runInfo.Sessions++

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleBackfillStatus handles GET /api/backfill/status.
func (s *Service) handleBackfillStatus(w http.ResponseWriter, r *http.Request) {
	runs := s.backfillTracker.snapshot()
	totalObs := 0
	for _, ri := range runs {
		totalObs += ri.Stored
	}

	status := BackfillStatus{
		TotalRuns:         len(runs),
		ActiveRuns:        runs,
		TotalObservations: totalObs,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}
