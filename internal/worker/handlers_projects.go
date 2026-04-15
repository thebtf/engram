// Package worker provides project lifecycle HTTP handlers.
package worker

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/thebtf/engram/internal/worker/projectevents"
)

// handleDeleteProject godoc
// @Summary Soft-delete a project
// @Description Marks a project as removed (sets removed_at). Does not hard-delete data.
// @Tags Projects
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Project ID"
// @Success 200 {object} map[string]interface{} "id and removed_at"
// @Failure 400 {string} string "malformed id"
// @Failure 404 {string} string "project not found or already deleted"
// @Failure 500 {string} string "internal error"
// @Router /api/projects/{id} [delete]
func (s *Service) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "missing project id", http.StatusBadRequest)
		return
	}

	// Validate the ID using the existing project-name validator.
	if err := ValidateProjectName(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()

	result := s.store.DB.WithContext(r.Context()).
		Exec(
			`UPDATE projects SET removed_at = ? WHERE id = ? AND removed_at IS NULL`,
			now, id,
		)
	if result.Error != nil {
		log.Error().Err(result.Error).Str("project_id", id).Msg("handleDeleteProject: db update failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if result.RowsAffected == 0 {
		http.Error(w, "project not found or already deleted", http.StatusNotFound)
		return
	}

	// Emit the lifecycle event so gRPC subscribers are notified immediately.
	if s.eventBus != nil {
		s.eventBus.Emit(projectevents.Event{
			EventType:       projectevents.EventTypeRemoved,
			ProjectID:       id,
			TimestampUnixMs: now.UnixMilli(),
			Reason:          "operator deleted via DELETE /api/projects/{id}",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"id":         id,
		"removed_at": now.Format(time.RFC3339Nano),
	}); err != nil {
		log.Warn().Err(err).Str("project_id", id).Msg("handleDeleteProject: failed to write response")
	}
}
