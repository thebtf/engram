// Package worker provides tag management REST handlers for the dashboard.
package worker

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/search"
)

// batchTagRequest is the JSON body for POST /api/observations/batch-tag.
type batchTagRequest struct {
	IDs    []int64 `json:"ids"`
	Tag    string  `json:"tag"`
	Action string  `json:"action"` // "add" or "remove"
}

// handleBatchTagObservations godoc
// @Summary Batch add or remove a tag on multiple observations
// @Description Adds or removes a single tag across a list of observation IDs.
// @Tags Tags
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body batchTagRequest true "Batch tag operation"
// @Success 200 {object} object
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Router /api/observations/batch-tag [post]
func (s *Service) handleBatchTagObservations(w http.ResponseWriter, r *http.Request) {
	if s.observationStore == nil {
		http.Error(w, "observation store not available", http.StatusServiceUnavailable)
		return
	}

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

	ctx := r.Context()
	var updated int64

	for _, id := range req.IDs {
		obs, err := s.observationStore.GetObservationByID(ctx, id)
		if err != nil {
			log.Error().Err(err).Int64("id", id).Msg("batch-tag: get observation failed")
			continue
		}
		if obs == nil {
			continue
		}

		var newTags []string
		switch req.Action {
		case "add":
			tagSet := make(map[string]bool)
			for _, t := range obs.Concepts {
				tagSet[t] = true
				newTags = append(newTags, t)
			}
			if !tagSet[req.Tag] {
				newTags = append(newTags, req.Tag)
			}
		case "remove":
			for _, t := range obs.Concepts {
				if t != req.Tag {
					newTags = append(newTags, t)
				}
			}
		}

		update := &gorm.ObservationUpdate{Concepts: &newTags}
		if _, err := s.observationStore.UpdateObservation(ctx, id, update); err != nil {
			log.Error().Err(err).Int64("id", id).Msg("batch-tag: update observation failed")
			continue
		}
		updated++
	}

	writeJSON(w, map[string]any{
		"updated": updated,
		"tag":     req.Tag,
		"action":  req.Action,
	})
}

// tagCloudEntry is a single tag with its occurrence count.
type tagCloudEntry struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// handleTagCloud godoc
// @Summary Get tag cloud from observation concepts
// @Description Returns aggregated concept tags with occurrence counts.
// @Tags Tags
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param limit query int false "Max tags to return (default 20)"
// @Success 200 {array} tagCloudEntry
// @Failure 500 {string} string "internal error"
// @Router /api/observations/tag-cloud [get]
func (s *Service) handleTagCloud(w http.ResponseWriter, r *http.Request) {
	if s.observationStore == nil {
		http.Error(w, "observation store not available", http.StatusServiceUnavailable)
		return
	}

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

	db := s.observationStore.GetDB()

	var query string
	var args []any

	if project != "" {
		query = `
			SELECT tag, COUNT(*) AS count
			FROM observations, jsonb_array_elements_text(concepts) AS tag
			WHERE COALESCE(is_superseded, 0) = 0
			  AND project = ?
			GROUP BY tag
			ORDER BY count DESC
			LIMIT ?`
		args = []any{project, limit}
	} else {
		query = `
			SELECT tag, COUNT(*) AS count
			FROM observations, jsonb_array_elements_text(concepts) AS tag
			WHERE COALESCE(is_superseded, 0) = 0
			GROUP BY tag
			ORDER BY count DESC
			LIMIT ?`
		args = []any{limit}
	}

	type row struct {
		Tag   string `gorm:"column:tag"`
		Count int    `gorm:"column:count"`
	}

	var rows []row
	if err := db.WithContext(r.Context()).Raw(query, args...).Scan(&rows).Error; err != nil {
		log.Error().Err(err).Msg("tag-cloud: query failed")
		http.Error(w, "query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	entries := make([]tagCloudEntry, len(rows))
	for i, row := range rows {
		entries[i] = tagCloudEntry{Tag: row.Tag, Count: row.Count}
	}

	writeJSON(w, entries)
}

// tagObservationRequest is the JSON body for POST /api/observations/{id}/tags.
type tagObservationRequest struct {
	Action string   `json:"action"` // "add", "remove", or "set"
	Tags   []string `json:"tags"`
}

// handleTagObservation godoc
// @Summary Add, remove, or set tags on an observation
// @Description Modifies the concept tags on an observation. Action must be "add", "remove", or "set".
// @Tags Tags
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Param body body tagObservationRequest true "Tag operation"
// @Success 200 {object} object
// @Failure 400 {string} string "bad request"
// @Failure 404 {string} string "not found"
// @Failure 500 {string} string "internal error"
// @Router /api/observations/{id}/tags [post]
func (s *Service) handleTagObservation(w http.ResponseWriter, r *http.Request) {
	if s.observationStore == nil {
		http.Error(w, "observation store not available", http.StatusServiceUnavailable)
		return
	}

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

	// Get current observation
	obs, err := s.observationStore.GetObservationByID(r.Context(), id)
	if err != nil {
		log.Error().Err(err).Int64("id", id).Msg("get observation for tagging failed")
		http.Error(w, "get observation: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if obs == nil {
		http.Error(w, "observation not found", http.StatusNotFound)
		return
	}

	// Compute new tags
	var newTags []string
	switch req.Action {
	case "set":
		newTags = req.Tags
	case "add":
		tagSet := make(map[string]bool)
		for _, t := range obs.Concepts {
			tagSet[t] = true
			newTags = append(newTags, t)
		}
		for _, t := range req.Tags {
			if !tagSet[t] {
				tagSet[t] = true
				newTags = append(newTags, t)
			}
		}
	case "remove":
		removeSet := make(map[string]bool)
		for _, t := range req.Tags {
			removeSet[t] = true
		}
		for _, t := range obs.Concepts {
			if !removeSet[t] {
				newTags = append(newTags, t)
			}
		}
	}

	update := &gorm.ObservationUpdate{
		Concepts: &newTags,
	}
	updatedObs, err := s.observationStore.UpdateObservation(r.Context(), id, update)
	if err != nil {
		log.Error().Err(err).Int64("id", id).Msg("update observation tags failed")
		http.Error(w, "update observation: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"id":           id,
		"action":       req.Action,
		"tags_applied": req.Tags,
		"current_tags": updatedObs.Concepts,
	})
}

// handleGetObservationsByTag godoc
// @Summary List observations by tag
// @Description Returns observations that have the specified concept tag.
// @Tags Tags
// @Produce json
// @Security ApiKeyAuth
// @Param tag path string true "Tag to search for"
// @Param project query string false "Filter by project"
// @Param limit query int false "Number of results (default 50, max 200)"
// @Param offset query int false "Pagination offset"
// @Success 200 {object} object
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Router /api/observations/by-tag/{tag} [get]
func (s *Service) handleGetObservationsByTag(w http.ResponseWriter, r *http.Request) {
	if s.observationStore == nil {
		http.Error(w, "observation store not available", http.StatusServiceUnavailable)
		return
	}

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

	if s.searchMgr == nil {
		http.Error(w, "search manager not available", http.StatusServiceUnavailable)
		return
	}

	searchParams := search.SearchParams{
		Query:    tag,
		Type:     "observations",
		Project:  project,
		Limit:    limit,
		Concepts: tag,
	}

	result, err := s.searchMgr.UnifiedSearch(r.Context(), searchParams)
	if err != nil {
		log.Error().Err(err).Str("tag", tag).Msg("search by tag failed")
		http.Error(w, "search: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Filter results to only include observations with the exact tag
	var filtered []search.SearchResult
	for _, res := range result.Results {
		if res.Type != "observation" {
			continue
		}
		if concepts, ok := res.Metadata["concepts"].([]any); ok {
			for _, c := range concepts {
				if cs, ok := c.(string); ok && cs == tag {
					filtered = append(filtered, res)
					break
				}
			}
		}
	}

	writeJSON(w, map[string]any{
		"tag":          tag,
		"observations": filtered,
		"count":        len(filtered),
	})
}
