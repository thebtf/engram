package worker

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	gormlib "gorm.io/gorm"
)

// handleDeleteBehavioralRule godoc
// @Summary Delete a behavioral rule by ID
// @Description Soft-deletes a behavioral rule by its numeric ID (sets deleted_at = now()).
// @Description Returns 404 if the rule does not exist or has already been deleted.
// @Tags Rules
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Success 200 {object} map[string]int64
// @Failure 400 {string} string "invalid rule id"
// @Failure 404 {string} string "rule not found"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal server error"
// @Router /api/rules/{id} [delete]
func (s *Service) handleDeleteBehavioralRule(w http.ResponseWriter, r *http.Request) {
	if s.behavioralRulesStore == nil {
		http.Error(w, "behavioral rules store not available", http.StatusServiceUnavailable)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid rule id", http.StatusBadRequest)
		return
	}

	if err := s.behavioralRulesStore.Delete(r.Context(), id); err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "rule not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("delete behavioral rule failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]int64{"deleted": id})
}

// updateBehavioralRuleRequest is the JSON body for PATCH /api/rules/{id}.
// All fields are pointers so the handler can perform a true partial update:
// an omitted field (nil) is left untouched, distinct from an explicit zero
// value. This matters because priority IS the rule's injection order — a
// content-only edit must not silently reset priority to 0, and a priority-only
// reorder must not wipe content. project is NOT editable — changing a rule's
// scope is a design-time concern (the store's Update deliberately excludes it),
// so the field is absent here.
type updateBehavioralRuleRequest struct {
	Content  *string `json:"content,omitempty"`
	EditedBy *string `json:"edited_by,omitempty"`
	Priority *int    `json:"priority,omitempty"`
}

// handleUpdateBehavioralRule godoc
// @Summary Edit a behavioral rule's content and priority
// @Description Updates the content, priority, and edited_by of an existing rule
// @Description by numeric ID and bumps its version. A rule's project scope is
// @Description immutable and cannot be changed here. Returns 404 if the rule does
// @Description not exist or has been deleted. Closes the must-build edit-rule gap:
// @Description the store Update method was wired and tested but had no REST/MCP
// @Description caller, forcing operators to delete-and-recreate to change a rule.
// @Tags Rules
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Param rule body updateBehavioralRuleRequest true "Updated rule fields"
// @Success 200 {object} models.BehavioralRule
// @Failure 400 {string} string "invalid rule id or body"
// @Failure 404 {string} string "rule not found"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal server error"
// @Router /api/rules/{id} [patch]
func (s *Service) handleUpdateBehavioralRule(w http.ResponseWriter, r *http.Request) {
	if s.behavioralRulesStore == nil {
		http.Error(w, "behavioral rules store not available", http.StatusServiceUnavailable)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid rule id", http.StatusBadRequest)
		return
	}

	var req updateBehavioralRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Partial-update semantics: fetch the current row, then apply only the fields
	// the caller explicitly provided. Without the fetch-merge step, the store's
	// Update (which writes content/priority/edited_by unconditionally) would
	// overwrite any omitted field with its zero value — silently wiping a rule's
	// content on a priority-only reorder, or resetting injection priority to 0 on
	// a content-only edit.
	// No-op guard: an empty body ({}) or one with no recognized fields would
	// otherwise fetch + Update, bumping version and updated_at with no real
	// change. Return 400 so the caller knows nothing was applied.
	if req.Content == nil && req.Priority == nil && req.EditedBy == nil {
		http.Error(w, "no updatable fields provided (content, priority, or edited_by)", http.StatusBadRequest)
		return
	}

	existing, err := s.behavioralRulesStore.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "rule not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("get behavioral rule for update failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if req.Content != nil {
		if *req.Content == "" {
			http.Error(w, "content must not be empty", http.StatusBadRequest)
			return
		}
		existing.Content = *req.Content
	}
	if req.Priority != nil {
		existing.Priority = *req.Priority
	}
	if req.EditedBy != nil {
		existing.EditedBy = *req.EditedBy
	}

	updated, err := s.behavioralRulesStore.Update(r.Context(), existing)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "rule not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("update behavioral rule failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, updated)
}
