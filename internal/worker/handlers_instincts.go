package worker

import (
	"encoding/json"
	"io"
	"net/http"
	"os"

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
// @Failure 501 {object} map[string]interface{}
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

	// Resolve and validate path against allowed base directory so callers keep the
	// same request contract even though filesystem-based import was removed in v5.
	dir, err := instincts.ResolveDir(params.Path)
	if err != nil {
		http.Error(w, "Invalid path: "+err.Error(), http.StatusBadRequest)
		return
	}

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		http.Error(w, "Instincts directory not found: "+dir, http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNotImplemented)
	writeJSON(w, map[string]any{
		"error":      "removed_in_v5",
		"path":       dir,
		"deprecated": true,
		"message":    "filesystem instinct import no longer persists observations in v5; use the MCP import_instincts tool with 'files' content until memory-backed REST import exists",
	})
}
