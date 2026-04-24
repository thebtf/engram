// Package worker provides the main worker service for engram.
// This file contains shared handler utilities and health/status endpoints.
// Domain-specific handlers are split into:
//   - handlers_sessions.go: Session lifecycle (init, start, observation, summarize)
//   - handlers_context.go: Context/search (search by prompt, file context, inject)
//   - handlers_data.go: Data retrieval (observations, summaries, prompts, stats)
//   - handlers_update.go: Updates and self-check (update check/apply, self-check)
//   - handlers_import_export.go: Import/export/archive operations
package worker

import (
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog/log"
)

// Handler configuration constants
const (
	// DefaultObservationsLimit is the default number of observations to return.
	DefaultObservationsLimit = 100

	// DefaultSearchLimit is the default number of search results to return.
	DefaultSearchLimit = 50

	// DefaultContextLimit is the default number of context observations to return.
	DefaultContextLimit = 50
)

// ObservationTypes is the canonical list of observation types.
// Used by both Go backend and served to frontend.
var ObservationTypes = []string{
	"bugfix",
	"feature",
	"refactor",
	"discovery",
	"decision",
	"change",
}

// observationTypeSet is a pre-computed map for O(1) type validation.
// Initialized at package load time.
var observationTypeSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(ObservationTypes))
	for _, t := range ObservationTypes {
		m[t] = struct{}{}
	}
	return m
}()

// IsValidObservationType returns true if the type is valid (O(1) lookup).
func IsValidObservationType(t string) bool {
	_, ok := observationTypeSet[t]
	return ok
}

// ConceptTypes is the canonical list of valid concept types.
// Used by both Go backend and served to frontend.
var ConceptTypes = []string{
	// Semantic concepts
	"how-it-works",
	"why-it-exists",
	"what-changed",
	"problem-solution",
	"gotcha",
	"pattern",
	"trade-off",
	// Globalizable concepts (from models.GlobalizableConcepts)
	"best-practice",
	"anti-pattern",
	"architecture",
	"security",
	"performance",
	"testing",
	"debugging",
	"workflow",
	"tooling",
	// Additional useful concepts
	"refactoring",
	"api",
	"database",
	"configuration",
	"error-handling",
	"caching",
	"logging",
	"auth",
	"validation",
}

// conceptTypeSet is a pre-computed map for O(1) concept validation.
var conceptTypeSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(ConceptTypes))
	for _, t := range ConceptTypes {
		m[t] = struct{}{}
	}
	return m
}()

// IsValidConceptType returns true if the concept type is valid (O(1) lookup).
func IsValidConceptType(t string) bool {
	_, ok := conceptTypeSet[t]
	return ok
}

// writeJSON writes a JSON response with proper error handling.
func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Error().Err(err).Msg("Failed to encode JSON response")
	}
}

// handleHealth godoc
// @Summary Health check
// @Description Returns 200 OK immediately (even during init) so hooks can connect quickly. Use /api/ready for full readiness check.
// @Tags System
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/health [get]
func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "starting"
	if s.ready.Load() {
		status = "ready"
	} else if err := s.GetInitError(); err != nil {
		status = "error"
	}
	writeJSON(w, map[string]any{
		"status":  status,
		"version": s.version,
	})
}

// handleVersion godoc
// @Summary Get worker version
// @Description Returns the worker version string for version checking.
// @Tags System
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/version [get]
func (s *Service) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{
		"version": s.version,
	})
}

// handleReady godoc
// @Summary Readiness check
// @Description Returns 200 only when fully initialized, 503 otherwise.
// @Tags System
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 500 {string} string "init error"
// @Failure 503 {string} string "service initializing"
// @Router /api/ready [get]
func (s *Service) handleReady(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		if err := s.GetInitError(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Error(w, "service initializing", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]string{"status": "ready"})
}

// requireReady is middleware that returns 503 if service isn't ready.
func (s *Service) requireReady(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.ready.Load() {
			if err := s.GetInitError(); err != nil {
				http.Error(w, "service initialization failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
			http.Error(w, "service initializing", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleListModels godoc
// @Summary Compatibility: empty models list (no embeddings in v5)
// @Description OpenAI-compatibility shim. Embedding pipeline removed in v5; always returns {"object":"list","data":[]}.
// @Tags System
// @Produce json
// @Success 200 {object} map[string]interface{} "Always returns empty data array; no real model entries"
// @Router /v1/models [get]
func (s *Service) handleListModels(w http.ResponseWriter, _ *http.Request) {
	type response struct {
		Object string `json:"object"`
		Data   []any  `json:"data"`
	}
	writeJSON(w, response{Object: "list", Data: []any{}})
}
