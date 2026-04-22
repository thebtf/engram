package worker

import (
	"net/http"
	"strconv"
	"strings"

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
//	@Param project query string true "Project name"
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

	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		http.Error(w, "project required", http.StatusBadRequest)
		return
	}
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 20 {
			limit = n
		}
	}

	log.Debug().Str("path", filePath).Str("project", project).Int("limit", limit).Msg("Context-by-file compatibility endpoint returns empty results in v5")

	// Graceful degradation: the observation file-reference path was removed in v5.
	// Keep the HTTP contract stable and return an empty payload instead of an error.
	writeJSON(w, map[string]any{
		"observations": []*struct{}{},
		"total":        0,
		"deprecated":   true,
		"message":      "file-path observation context removed in v5; results are intentionally empty",
	})
}
