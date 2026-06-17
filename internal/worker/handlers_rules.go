package worker

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
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
// content is required; priority defaults to 0 when omitted. project is NOT
// editable — changing a rule's scope is a design-time concern (the store's
// Update deliberately excludes it), so the field is absent here.
type updateBehavioralRuleRequest struct {
	Content  string `json:"content"`
	EditedBy string `json:"edited_by"`
	Priority int    `json:"priority"`
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
	if req.Content == "" {
		http.Error(w, "content is required and must not be empty", http.StatusBadRequest)
		return
	}

	updated, err := s.behavioralRulesStore.Update(r.Context(), &models.BehavioralRule{
		ID:       id,
		Content:  req.Content,
		Priority: req.Priority,
		EditedBy: req.EditedBy,
	})
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
