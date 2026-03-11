package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/backfill"
	"github.com/thebtf/engram/internal/backfill/extract"
	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/internal/sessions"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/pkg/models"
)

// dedupThreshold is the cosine similarity threshold for semantic deduplication.
// Observations with similarity above this value are considered duplicates and skipped.
const dedupThreshold = 0.92

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
	vectorClient := s.vectorClient
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

		// Semantic dedup: check if a very similar observation already exists
		if vectorClient != nil && vectorClient.IsConnected() {
			searchText := obs.Title + " " + obs.Narrative
			results, qErr := vectorClient.Query(r.Context(), searchText, 1, vector.WhereFilter{})
			if qErr == nil && len(results) > 0 && results[0].Similarity > dedupThreshold {
				log.Debug().
					Str("title", bo.Title).
					Float64("similarity", results[0].Similarity).
					Msg("backfill: skipping near-duplicate observation")
				resp.Skipped++
				continue
			}
		}

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

// BackfillSessionRequest is the request body for POST /api/backfill/session.
// The server parses the raw JSONL content, extracts observations via LLM, and stores them.
type BackfillSessionRequest struct {
	// SessionID identifies the source session (e.g. UUID from filename).
	SessionID string `json:"session_id"`
	// Project overrides the project path from session metadata. Empty = use parsed value.
	Project string `json:"project"`
	// RunID groups observations from the same backfill run.
	RunID string `json:"run_id"`
	// Content is the raw JSONL session data.
	Content string `json:"content"`
}

// BackfillSessionResponse is the response for POST /api/backfill/session.
type BackfillSessionResponse struct {
	Stored               int    `json:"stored"`
	Skipped              int    `json:"skipped"`
	Errors               int    `json:"errors"`
	ObservationsExtracted int   `json:"observations_extracted"`
	MetricsReport        string `json:"metrics_report,omitempty"`
}

// handleBackfillSession handles POST /api/backfill/session — accepts raw JSONL content,
// runs server-side LLM extraction, and stores the resulting observations.
func (s *Service) handleBackfillSession(w http.ResponseWriter, r *http.Request) {
	var req BackfillSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.RunID == "" {
		http.Error(w, "run_id is required", http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	// Parse session from raw JSONL content.
	sess, err := sessions.ParseSessionReader(strings.NewReader(req.Content))
	if err != nil {
		http.Error(w, "Failed to parse session: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Override project if provided by CLI.
	if req.Project != "" {
		sess.ProjectPath = req.Project
	}
	if req.SessionID != "" {
		sess.SessionID = req.SessionID
	}

	// Initialize LLM client from server env vars.
	llmCfg := learning.DefaultOpenAIConfig()
	llmClient := learning.NewOpenAIClient(llmCfg)
	if !llmClient.IsConfigured() {
		http.Error(w, "LLM not configured on server (set ENGRAM_LLM_URL + ENGRAM_LLM_API_KEY)", http.StatusServiceUnavailable)
		return
	}

	// Run extraction pipeline.
	runner := backfill.NewRunner(llmClient, backfill.DefaultConfig())
	result, _ := runner.ProcessSession(r.Context(), sess)

	resp := BackfillSessionResponse{
		ObservationsExtracted: len(result.Observations),
		MetricsReport:         result.Metrics.Report(),
	}

	if len(result.Observations) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Store observations with semantic dedup (same logic as handleBackfillIngest).
	s.initMu.RLock()
	obsStore := s.observationStore
	vectorClient := s.vectorClient
	s.initMu.RUnlock()

	if obsStore == nil {
		http.Error(w, "Observation store not initialized", http.StatusServiceUnavailable)
		return
	}

	runInfo := s.backfillTracker.getOrCreate(req.RunID)

	for _, eo := range result.Observations {
		obs := eo.Observation

		// Semantic dedup: check if a very similar observation already exists.
		if vectorClient != nil && vectorClient.IsConnected() {
			searchText := obs.Title + " " + obs.Narrative
			results, qErr := vectorClient.Query(r.Context(), searchText, 1, vector.WhereFilter{})
			if qErr == nil && len(results) > 0 && results[0].Similarity > dedupThreshold {
				log.Debug().
					Str("title", obs.Title).
					Float64("similarity", results[0].Similarity).
					Msg("backfill-session: skipping near-duplicate observation")
				resp.Skipped++
				continue
			}
		}

		// Add backfill metadata.
		obs.Scope = models.ScopeProject

		project := sess.ProjectPath
		if eo.Project != "" {
			project = eo.Project
		}

		sdkSessionID := fmt.Sprintf("backfill-%s-%s", req.RunID, req.SessionID)
		_, _, storeErr := obsStore.StoreObservation(r.Context(), sdkSessionID, project, obs, 0, 0)
		if storeErr != nil {
			log.Error().Err(storeErr).Str("title", obs.Title).Msg("backfill-session: failed to store observation")
			resp.Errors++
			continue
		}

		resp.Stored++
	}

	// Update run tracker.
	runInfo.Stored += resp.Stored
	runInfo.Skipped += resp.Skipped
	runInfo.Errors += resp.Errors
	runInfo.Sessions++

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
