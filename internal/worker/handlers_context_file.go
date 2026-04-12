package worker

import (
	"net/http"
	"strconv"

	"github.com/rs/zerolog/log"
)

// handleContextByFile returns observations related to a specific file path.
// Used by PreToolUse hook to inject file-specific context before Edit/Write.
//
//	@Summary File-specific context
//	@Description Returns observations with files_modified or files_read matching the given path.
//	@Tags Context
//	@Produce json
//	@Security ApiKeyAuth
//	@Param path query string true "File path to search for"
//	@Param project query string false "Project name"
//	@Param limit query int false "Max results (default 10, max 20)"
//	@Success 200 {object} map[string]interface{}
//	@Failure 400 {string} string "bad request"
//	@Router /api/context/by-file [get]
func (s *Service) handleContextByFile(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	// Validate project name if provided
	project := r.URL.Query().Get("project")
	if project != "" {
		if err := ValidateProjectName(project); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 20 {
			limit = n
		}
	}

	observations, err := s.observationStore.GetObservationsByFile(r.Context(), filePath, limit)
	if err != nil {
		log.Debug().Err(err).Str("path", filePath).Msg("Failed to fetch file-context observations")
		// Graceful degradation: return empty, don't error (NFR-3)
		writeJSON(w, map[string]any{
			"observations": []*struct{}{},
			"total":        0,
		})
		return
	}

	writeJSON(w, map[string]any{
		"observations": observations,
		"total":        len(observations),
	})
}
