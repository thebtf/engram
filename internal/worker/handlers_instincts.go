package worker

import (
	"encoding/json"
	"io"
	"net/http"
	"os"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/instincts"
)

// InstinctsImportRequest is the request body for instincts import.
type InstinctsImportRequest struct {
	Path string `json:"path"`
}

// handleInstinctsImport godoc
// @Summary Import instincts
// @Description Imports instinct files from a directory on the server. Validates path against allowed base directory.
// @Tags Instincts
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body InstinctsImportRequest false "Optional: {path: '/path/to/instincts'}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "invalid path"
// @Failure 404 {string} string "directory not found"
// @Failure 500 {string} string "import failed"
// @Failure 503 {string} string "observation store not initialized"
// @Router /api/instincts/import [post]
func (s *Service) handleInstinctsImport(w http.ResponseWriter, r *http.Request) {
	var params InstinctsImportRequest
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil && err != io.EOF {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
	}

	// Resolve and validate path against allowed base directory
	dir, err := instincts.ResolveDir(params.Path)
	if err != nil {
		http.Error(w, "Invalid path: "+err.Error(), http.StatusBadRequest)
		return
	}

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		http.Error(w, "Instincts directory not found: "+dir, http.StatusNotFound)
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

	result, importErr := instincts.Import(r.Context(), dir, vectorClient, obsStore)
	if importErr != nil {
		log.Error().Err(importErr).Msg("Instinct import failed")
		http.Error(w, "Import failed: "+importErr.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
