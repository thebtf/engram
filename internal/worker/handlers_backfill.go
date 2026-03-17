package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/backfill"
	"github.com/thebtf/engram/internal/backfill/extract"
	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/internal/privacy"
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

// handleBackfillIngest godoc
// @Summary Ingest backfill observations
// @Description Stores pre-extracted observations from a backfill run. Supports semantic deduplication via vector similarity.
// @Tags Backfill
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body BackfillRequest true "Backfill observations"
// @Success 200 {object} BackfillResponse
// @Failure 400 {string} string "bad request"
// @Failure 503 {string} string "observation store not initialized"
// @Router /api/backfill [post]
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

// handleBackfillStatus godoc
// @Summary Get backfill status
// @Description Returns status of all active backfill runs including stored/skipped/error counts per run.
// @Tags Backfill
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} BackfillStatus
// @Router /api/backfill/status [get]
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

// handleBackfillSession godoc
// @Summary Backfill session with LLM extraction
// @Description Accepts raw JSONL session content, runs server-side LLM extraction, and stores resulting observations with semantic deduplication.
// @Tags Backfill
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body BackfillSessionRequest true "Session content and metadata"
// @Success 200 {object} BackfillSessionResponse
// @Failure 400 {string} string "bad request"
// @Failure 503 {string} string "LLM not configured or store not initialized"
// @Router /api/backfill/session [post]
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

	// Detect secrets in the raw content, store in vault, then redact session exchanges.
	project := sess.ProjectPath
	if req.Project != "" {
		project = req.Project
	}
	if privacy.ContainsSecrets(req.Content) {
		vaultStoreDetectedSecrets(r.Context(), s, req.Content, project)
	}
	for i := range sess.Exchanges {
		sess.Exchanges[i].UserText = privacy.RedactSecrets(sess.Exchanges[i].UserText)
		sess.Exchanges[i].AssistantText = privacy.RedactSecrets(sess.Exchanges[i].AssistantText)
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

// vaultStoreDetectedSecrets extracts secrets from text, encrypts each with the vault,
// and stores them as credential observations. All errors are non-fatal — secrets are
// still redacted from the transcript even if vault storage fails.
func vaultStoreDetectedSecrets(ctx context.Context, s *Service, text, project string) {
	secrets := privacy.ExtractSecrets(text)
	if len(secrets) == 0 {
		return
	}

	vault, err := s.getVault()
	if err != nil {
		log.Warn().Err(err).Msg("backfill: vault not available, skipping secret storage")
		return
	}

	s.initMu.RLock()
	obsStore := s.observationStore
	s.initMu.RUnlock()
	if obsStore == nil {
		log.Warn().Msg("backfill: observation store not available, skipping secret storage")
		return
	}

	stored := 0
	for _, secret := range secrets {
		// Idempotency: skip if credential with this name already exists.
		existing, err := obsStore.GetCredential(ctx, secret.Name, project)
		if err != nil {
			log.Warn().Err(err).Str("name", secret.Name).Msg("backfill: failed to check existing credential")
			continue
		}
		if existing != nil {
			continue
		}

		ciphertext, err := vault.Encrypt(secret.Value)
		if err != nil {
			log.Warn().Err(err).Str("name", secret.Name).Msg("backfill: failed to encrypt secret")
			continue
		}

		obs := &models.ParsedObservation{
			Type:                     models.ObsTypeCredential,
			SourceType:               models.SourceBackfill,
			Title:                    secret.Name,
			Narrative:                secret.Name,
			Concepts:                 []string{"auto-detected", "redactor"},
			Scope:                    models.ScopeProject,
			EncryptedSecret:          ciphertext,
			EncryptionKeyFingerprint: vault.Fingerprint(),
		}

		const vaultSessionID = "credential:auto-redactor"
		_, _, storeErr := obsStore.StoreObservation(ctx, vaultSessionID, project, obs, 0, 0)
		if storeErr != nil {
			log.Warn().Err(storeErr).Str("name", secret.Name).Msg("backfill: failed to store auto-detected credential")
			continue
		}
		stored++
	}

	if stored > 0 {
		log.Info().Int("count", stored).Int("detected", len(secrets)).Msg("backfill: auto-detected secrets stored in vault")
	}
}
