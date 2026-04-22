// Package worker provides tag management REST handlers for the dashboard.
package worker

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// batchTagRequest is the JSON body for POST /api/observations/batch-tag.
type batchTagRequest struct {
	IDs    []int64 `json:"ids"`
	Tag    string  `json:"tag"`
	Action string  `json:"action"` // "add" or "remove"
}

// handleBatchTagObservations godoc
// @Summary Batch add or remove a tag on multiple observations
// @Description Observation tag mutation was removed in v5. Validation is preserved so clients receive stable request errors before the explicit removal payload.
// @Tags Tags
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body batchTagRequest true "Batch tag operation"
// @Success 501 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Router /api/observations/batch-tag [post]
func (s *Service) handleBatchTagObservations(w http.ResponseWriter, r *http.Request) {
	var req batchTagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		http.Error(w, "ids is required and must not be empty", http.StatusBadRequest)
		return
	}
	if req.Tag == "" {
		http.Error(w, "tag is required", http.StatusBadRequest)
		return
	}
	switch req.Action {
	case "add", "remove":
		// valid
	default:
		http.Error(w, "action must be 'add' or 'remove'", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNotImplemented)
	writeJSON(w, map[string]any{
		"error":      "removed_in_v5",
		"deprecated": true,
		"ids":        req.IDs,
		"tag":        req.Tag,
		"action":     req.Action,
		"message":    "batch observation tag mutation removed in v5; observations persistence was dropped in US3-PR-B",
	})
}

// tagCloudEntry is a single tag with its occurrence count.
type tagCloudEntry struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// handleTagCloud godoc
// @Summary Get tag cloud from observation concepts
// @Description Observation tag cloud aggregation was removed in v5.
// @Tags Tags
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param limit query int false "Max tags to return (default 20)"
// @Success 501 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Router /api/observations/tag-cloud [get]
func (s *Service) handleTagCloud(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	limit := 20
	if val := r.URL.Query().Get("limit"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}

	w.WriteHeader(http.StatusNotImplemented)
	writeJSON(w, map[string]any{
		"error":      "removed_in_v5",
		"deprecated": true,
		"project":    project,
		"limit":      limit,
		"tags":       []tagCloudEntry{},
		"message":    "observation tag cloud removed in v5; observations persistence was dropped in US3-PR-B",
	})
}

// tagObservationRequest is the JSON body for POST /api/observations/{id}/tags.
type tagObservationRequest struct {
	Action string   `json:"action"` // "add", "remove", or "set"
	Tags   []string `json:"tags"`
}

// handleTagObservation godoc
// @Summary Add, remove, or set tags on an observation
// @Description Observation tag mutation was removed in v5. Validation is preserved so clients receive stable request errors before the explicit removal payload.
// @Tags Tags
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Param body body tagObservationRequest true "Tag operation"
// @Success 501 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Router /api/observations/{id}/tags [post]
func (s *Service) handleTagObservation(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, ok := parseIDParam(w, idStr, "observation")
	if !ok {
		return
	}

	var req tagObservationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Tags) == 0 {
		http.Error(w, "tags is required", http.StatusBadRequest)
		return
	}
	switch req.Action {
	case "add", "remove", "set":
		// valid
	default:
		http.Error(w, "action must be 'add', 'remove', or 'set'", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNotImplemented)
	writeJSON(w, map[string]any{
		"error":      "removed_in_v5",
		"deprecated": true,
		"id":         id,
		"action":     req.Action,
		"tags":       req.Tags,
		"message":    "observation tag mutation removed in v5; observations persistence was dropped in US3-PR-B",
	})
}

// handleGetObservationsByTag godoc
// @Summary List observations by tag
// @Description Observation tag search was removed in v5.
// @Tags Tags
// @Produce json
// @Security ApiKeyAuth
// @Param tag path string true "Tag to search for"
// @Param project query string false "Filter by project"
// @Param limit query int false "Number of results (default 50, max 200)"
// @Param offset query int false "Pagination offset"
// @Success 501 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Router /api/observations/by-tag/{tag} [get]
func (s *Service) handleGetObservationsByTag(w http.ResponseWriter, r *http.Request) {
	tag := chi.URLParam(r, "tag")
	if tag == "" {
		http.Error(w, "tag is required", http.StatusBadRequest)
		return
	}

	project := r.URL.Query().Get("project")
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	limit := 50
	if val := r.URL.Query().Get("limit"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}

	// Tag-based search was backed by search.Manager.UnifiedSearch, dropped in v5 (US9).
	// Return 501 Not Implemented so clients can distinguish "feature removed" from
	// "no results".
	w.WriteHeader(http.StatusNotImplemented)
	writeJSON(w, map[string]any{
		"error":        "removed_in_v5",
		"deprecated":   true,
		"tag":          tag,
		"project":      project,
		"limit":        limit,
		"observations": []any{},
		"count":        0,
		"message":      "tag-based observation search removed in v5; observations persistence was dropped in US3-PR-B",
	})
}
