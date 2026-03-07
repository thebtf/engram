package worker

import (
	"encoding/json"
	"io"
	"net/http"
	"os"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/instincts"
)

// handleInstinctsImport handles POST /api/instincts/import.
func (s *Service) handleInstinctsImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var params struct {
		Path string `json:"path"`
	}
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
