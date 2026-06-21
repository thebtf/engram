package worker

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

type behavioralRuleRequest struct {
	Project  *string `json:"project"`
	Content  *string `json:"content"`
	Priority *int    `json:"priority"`
	EditedBy *string `json:"edited_by"`
}

func parseBehavioralRuleID(r *http.Request) (int64, bool) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func normalizedOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func decodeBehavioralRuleRequest(r *http.Request) (*behavioralRuleRequest, error) {
	var req behavioralRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

// handleListBehavioralRules godoc
// @Summary List behavioral rules
// @Description Returns active behavioral rules. When project is set, returns project-scoped and global rules; otherwise returns global rules only. Set all=true for the operator-console all-scope registry view.
// @Tags Rules
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Project slug (optional)"
// @Param all query bool false "Return all active rules across scopes"
// @Param limit query int false "Maximum rows (default 50)"
// @Success 200 {array} models.BehavioralRule
// @Failure 400 {string} string "invalid limit"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal server error"
// @Router /api/rules [get]
func (s *Service) handleListBehavioralRules(w http.ResponseWriter, r *http.Request) {
	if s.behavioralRulesStore == nil {
		http.Error(w, "behavioral rules store not available", http.StatusServiceUnavailable)
		return
	}

	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = parsed
	}

	var (
		rules []*models.BehavioralRule
		err   error
	)
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("all")), "true") {
		rules, err = s.behavioralRulesStore.ListAll(r.Context(), limit)
	} else {
		rules, err = s.behavioralRulesStore.List(r.Context(), normalizedOptionalString(ptrString(r.URL.Query().Get("project"))), limit)
	}
	if err != nil {
		log.Error().Err(err).Msg("list behavioral rules failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, rules)
}

// handleCreateBehavioralRule godoc
// @Summary Create a behavioral rule
// @Description Creates an always-inject behavioral rule. Omit project to create a global rule.
// @Tags Rules
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body behavioralRuleRequest true "Rule create payload"
// @Success 201 {object} models.BehavioralRule
// @Failure 400 {string} string "content is required"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal server error"
// @Router /api/rules [post]
func (s *Service) handleCreateBehavioralRule(w http.ResponseWriter, r *http.Request) {
	if s.behavioralRulesStore == nil {
		http.Error(w, "behavioral rules store not available", http.StatusServiceUnavailable)
		return
	}

	req, err := decodeBehavioralRuleRequest(r)
	if err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	content := strings.TrimSpace(stringValue(req.Content))
	if content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	priority := 0
	if req.Priority != nil {
		priority = *req.Priority
	}

	created, err := s.behavioralRulesStore.Create(r.Context(), &models.BehavioralRule{
		Project:  normalizedOptionalString(req.Project),
		Content:  content,
		Priority: priority,
		EditedBy: strings.TrimSpace(stringValue(req.EditedBy)),
	})
	if err != nil {
		log.Error().Err(err).Msg("create behavioral rule failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, created)
}

// handleUpdateBehavioralRule godoc
// @Summary Update a behavioral rule
// @Description Partially updates a behavioral rule by ID. Missing fields keep their current values.
// @Tags Rules
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Param body body behavioralRuleRequest true "Rule update payload"
// @Success 200 {object} models.BehavioralRule
// @Failure 400 {string} string "invalid rule id"
// @Failure 404 {string} string "rule not found"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal server error"
// @Router /api/rules/{id} [patch]
func (s *Service) handleUpdateBehavioralRule(w http.ResponseWriter, r *http.Request) {
	if s.behavioralRulesStore == nil {
		http.Error(w, "behavioral rules store not available", http.StatusServiceUnavailable)
		return
	}

	id, ok := parseBehavioralRuleID(r)
	if !ok {
		http.Error(w, "invalid rule id", http.StatusBadRequest)
		return
	}

	current, err := s.behavioralRulesStore.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "rule not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("load behavioral rule for update failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	req, err := decodeBehavioralRuleRequest(r)
	if err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	content := current.Content
	if req.Content != nil {
		content = strings.TrimSpace(*req.Content)
		if content == "" {
			http.Error(w, "content must not be empty", http.StatusBadRequest)
			return
		}
	}

	priority := current.Priority
	if req.Priority != nil {
		priority = *req.Priority
	}

	editedBy := current.EditedBy
	if req.EditedBy != nil {
		editedBy = strings.TrimSpace(*req.EditedBy)
	}

	updated, err := s.behavioralRulesStore.Update(r.Context(), &models.BehavioralRule{
		ID:       id,
		Content:  content,
		Priority: priority,
		EditedBy: editedBy,
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

	id, ok := parseBehavioralRuleID(r)
	if !ok {
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

func ptrString(value string) *string {
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
