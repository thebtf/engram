package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	gormlib "gorm.io/gorm"
	"github.com/thebtf/engram/internal/backfill"
	"github.com/thebtf/engram/internal/backfill/extract"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/sessions"
	"github.com/thebtf/engram/pkg/llmclient"
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

// handleBackfillIngest godoc
// @Summary Ingest backfill observations
// @Description Observation-era backfill ingest was removed in v5. The endpoint remains for compatibility and returns an explicit deprecation response.
// @Tags Backfill
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body BackfillRequest true "Backfill observations"
// @Success 200 {object} BackfillResponse
// @Failure 400 {string} string "bad request"
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

	resp := BackfillResponse{Skipped: len(req.Observations)}
	runInfo := s.backfillTracker.getOrCreate(req.RunID)
	runInfo.Stored += resp.Stored
	runInfo.Skipped += resp.Skipped
	runInfo.Errors += resp.Errors
	runInfo.Sessions++

	writeJSON(w, map[string]any{
		"stored":     resp.Stored,
		"skipped":    resp.Skipped,
		"errors":     resp.Errors,
		"deprecated": "observation backfill ingest removed in v5",
	})
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
		// Fire-and-forget vault storage (Constitution P3: Non-Blocking)
		capturedContent := req.Content
		capturedProject := project
		go func() {
			vaultCtx, vaultCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer vaultCancel()
			vaultStoreDetectedSecrets(vaultCtx, s, capturedContent, capturedProject)
		}()
	}
	for i := range sess.Exchanges {
		sess.Exchanges[i].UserText = privacy.RedactSecrets(sess.Exchanges[i].UserText)
		sess.Exchanges[i].AssistantText = privacy.RedactSecrets(sess.Exchanges[i].AssistantText)
	}

	// Initialize LLM client from server env vars.
	llmClient := llmclient.New(llmclient.DefaultConfig())
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

	// Store session metadata still supported in v5; observation/session-summary persistence was removed.
	sdkSessionID := fmt.Sprintf("backfill-%s-%s", req.RunID, req.SessionID)

	if s.sessionStore != nil {
		sessionDBID, err := s.sessionStore.CreateSDKSession(r.Context(), sdkSessionID, project, "backfill")
		if err != nil {
			log.Warn().Err(err).Str("session_id", req.SessionID).Msg("backfill: failed to create SDK session")
		}
		_ = sessionDBID
	}

	if len(result.Observations) > 0 {
		resp.Skipped = len(result.Observations)
	}

	runInfo := s.backfillTracker.getOrCreate(req.RunID)
	runInfo.Stored += resp.Stored
	runInfo.Skipped += resp.Skipped
	runInfo.Errors += resp.Errors
	runInfo.Sessions++

	writeJSON(w, map[string]any{
		"stored":                resp.Stored,
		"skipped":               resp.Skipped,
		"errors":                resp.Errors,
		"observations_extracted": resp.ObservationsExtracted,
		"metrics_report":        resp.MetricsReport,
		"deprecated":            "backfill observation/session-summary persistence removed in v5",
	})
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
	credentialStore := s.credentialStore
	s.initMu.RUnlock()
	if credentialStore == nil {
		log.Warn().Msg("backfill: credential store not available, skipping secret storage")
		return
	}

	stored := 0
	for _, secret := range secrets {
		_, err := credentialStore.Get(ctx, project, secret.Name)
		if err == nil {
			continue
		}
		if !errors.Is(err, gormlib.ErrRecordNotFound) {
			log.Warn().Err(err).Str("name", secret.Name).Msg("backfill: failed to check existing credential")
			continue
		}

		ciphertext, err := vault.Encrypt(secret.Value)
		if err != nil {
			log.Warn().Err(err).Str("name", secret.Name).Msg("backfill: failed to encrypt secret")
			continue
		}

		_, createErr := credentialStore.Create(ctx, &models.Credential{
			Project:                  project,
			Key:                      secret.Name,
			EncryptedSecret:          ciphertext,
			EncryptionKeyFingerprint: vault.Fingerprint(),
			Scope:                    string(models.ScopeProject),
			EditedBy:                 "backfill-auto-redactor",
		})
		if createErr != nil {
			log.Warn().Err(createErr).Str("name", secret.Name).Msg("backfill: failed to store auto-detected credential")
			continue
		}
		stored++
	}

	if stored > 0 {
		log.Info().Int("count", stored).Int("detected", len(secrets)).Msg("backfill: auto-detected secrets stored in vault")
	}
}

// handleImportFeedback godoc
// @Summary Import a feedback rule via LLM
// @Description Accepts raw feedback_*.md content, processes it through the server-side LLM
// to extract a structured TRIGGER→RULE→REASON observation, deduplicates, and stores as type=guidance.
// @Tags Import
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body object true "Feedback content: {content: string, source_file: string}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Failure 503 {string} string "LLM not configured"
// @Router /api/import/feedback [post]
func (s *Service) handleImportFeedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content    string `json:"content"`
		SourceFile string `json:"source_file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	// Initialize LLM client from server env vars.
	llmClient := llmclient.New(llmclient.DefaultConfig())
	if !llmClient.IsConfigured() {
		http.Error(w, "LLM not configured on server (set ENGRAM_LLM_URL + ENGRAM_LLM_API_KEY)", http.StatusServiceUnavailable)
		return
	}

	// Call LLM with feedback import prompt.
	callCtx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	response, err := llmClient.Complete(callCtx, extract.FeedbackImportSystemPrompt, req.Content)
	if err != nil {
		log.Warn().Err(err).Str("source", req.SourceFile).Msg("feedback import: LLM call failed")
		http.Error(w, "LLM extraction failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Constitution P9: RedactSecrets on LLM output before parsing
	response = privacy.RedactSecrets(response)

	// Parse single observation from LLM XML output.
	obs := extract.ParseSingleObservation(response)
	if obs == nil {
		writeJSON(w, map[string]any{
			"status": "skipped",
			"reason": "LLM returned no parseable observation",
		})
		return
	}

	s.initMu.RLock()
	behavioralRulesStore := s.behavioralRulesStore
	s.initMu.RUnlock()

	if behavioralRulesStore == nil {
		http.Error(w, "behavioral rules store not ready", http.StatusServiceUnavailable)
		return
	}

	ruleContent := strings.TrimSpace(obs.Narrative)
	if ruleContent == "" {
		ruleContent = strings.TrimSpace(obs.Title)
	}
	if ruleContent == "" {
		writeJSON(w, map[string]any{
			"status": "skipped",
			"reason": "LLM returned empty behavioral rule content",
		})
		return
	}

	createdRule, storeErr := behavioralRulesStore.Create(r.Context(), &models.BehavioralRule{
		Content:  ruleContent,
		EditedBy: fmt.Sprintf("feedback-import:%s", req.SourceFile),
		Priority: 80,
	})
	if storeErr != nil {
		log.Error().Err(storeErr).Str("title", obs.Title).Msg("feedback import: failed to store rule")
		http.Error(w, "failed to store behavioral rule: "+storeErr.Error(), http.StatusInternalServerError)
		return
	}

	log.Info().Int64("id", createdRule.ID).Str("title", obs.Title).Str("source", req.SourceFile).Msg("feedback import: stored rule")

	writeJSON(w, map[string]any{
		"status":      "imported",
		"id":          createdRule.ID,
		"title":       obs.Title,
		"source_file": req.SourceFile,
	})
}
