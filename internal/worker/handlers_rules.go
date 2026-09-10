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

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

type behavioralRuleRequest struct {
	Project   *string                              `json:"project"`
	Content   *string                              `json:"content"`
	Priority  *int                                 `json:"priority"`
	EditedBy  *string                              `json:"edited_by"`
	RequestID string                               `json:"request_id,omitempty"`
	Action    string                               `json:"action,omitempty"`
	Selection *behavioralRuleOperationSelection    `json:"selection,omitempty"`
	Scope     *behavioralRuleOperationScope        `json:"scope,omitempty"`
	Order     []behavioralRuleOperationOrderTarget `json:"order,omitempty"`
}

type behavioralRuleOperationSelection struct {
	Kind    gormdb.CollectionSelectionKind `json:"kind"`
	Version int64                          `json:"selection_version"`
	Token   string                         `json:"selection_token,omitempty"`
}

type behavioralRuleOperationScope struct {
	Project *string `json:"project"`
}

type behavioralRuleOperationOrderTarget struct {
	RuleID          int64  `json:"rule_id"`
	ExpectedVersion uint64 `json:"expected_version"`
}

type behavioralRuleEnabledRequest struct {
	EditedBy *string `json:"edited_by"`
	Enabled  *bool   `json:"enabled"`
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

func decodeBehavioralRuleEnabledRequest(r *http.Request) (*behavioralRuleEnabledRequest, error) {
	var req behavioralRuleEnabledRequest
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
	if req.Action != "" {
		s.handleBehavioralRuleSelectionOperation(w, r, req)
		return
	}
	if req.RequestID != "" || req.Selection != nil || req.Scope != nil || len(req.Order) != 0 {
		http.Error(w, "selection operation requires an action", http.StatusBadRequest)
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

// handleSetBehavioralRuleEnabled godoc
// @Summary Enable or disable a behavioral rule
// @Description Toggles whether an active behavioral rule is injected. Disabled rules remain listed for operator review and re-enabling.
// @Tags Rules
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Param body body behavioralRuleEnabledRequest true "Rule enabled payload"
// @Success 200 {object} models.BehavioralRule
// @Failure 400 {string} string "enabled is required"
// @Failure 404 {string} string "rule not found"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal server error"
// @Router /api/rules/{id}/enabled [patch]
func (s *Service) handleSetBehavioralRuleEnabled(w http.ResponseWriter, r *http.Request) {
	if s.behavioralRulesStore == nil {
		http.Error(w, "behavioral rules store not available", http.StatusServiceUnavailable)
		return
	}

	id, ok := parseBehavioralRuleID(r)
	if !ok {
		http.Error(w, "invalid rule id", http.StatusBadRequest)
		return
	}

	req, err := decodeBehavioralRuleEnabledRequest(r)
	if err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Enabled == nil {
		http.Error(w, "enabled is required", http.StatusBadRequest)
		return
	}

	updated, err := s.behavioralRulesStore.SetEnabled(r.Context(), id, *req.Enabled, normalizedOptionalString(req.EditedBy))
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "rule not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("set behavioral rule enabled failed")
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

// handleBehavioralRuleSelectionOperation executes the closed Rules action matrix
// against a selection that is re-resolved for the current browser owner/session.
func (s *Service) handleBehavioralRuleSelectionOperation(w http.ResponseWriter, r *http.Request, request *behavioralRuleRequest) {
	if request == nil || !operatorCodeText(request.RequestID) || r.Header.Get("X-Engram-Request-ID") != request.RequestID {
		http.Error(w, "invalid rules operation request reference", http.StatusBadRequest)
		return
	}
	operation, err := request.selectionOperation()
	if err != nil {
		http.Error(w, "invalid rules operation", http.StatusBadRequest)
		return
	}
	scope, err := behavioralRuleSelectionScopeFromRequest(r)
	if err != nil {
		http.Error(w, "rules operation forbidden", http.StatusForbidden)
		return
	}
	result, err := s.behavioralRulesStore.ApplySelectionOperation(r.Context(), scope, operation)
	if err != nil {
		writeBehavioralRuleSelectionOperationError(w, err)
		return
	}
	response := newBehavioralRuleSelectionOperationResponse(request.RequestID, operation.Action, result)
	writeJSON(w, response)
}

func (request *behavioralRuleRequest) selectionOperation() (gormdb.BehavioralRuleSelectionOperation, error) {
	if request.Selection == nil {
		return gormdb.BehavioralRuleSelectionOperation{}, errors.New("selection is required")
	}
	order := make([]gormdb.BehavioralRuleOrder, len(request.Order))
	for index, target := range request.Order {
		order[index] = gormdb.BehavioralRuleOrder{RuleID: target.RuleID, ExpectedVersion: target.ExpectedVersion}
	}
	var scope *gormdb.BehavioralRuleScope
	if request.Scope != nil {
		scope = &gormdb.BehavioralRuleScope{}
		if request.Scope.Project != nil {
			project := new(string)
			*project = *request.Scope.Project
			scope.Project = project
		}
	}
	return gormdb.BehavioralRuleSelectionOperation{
		Action:           gormdb.BehavioralRuleSelectionAction(request.Action),
		SelectionKind:    request.Selection.Kind,
		SelectionVersion: request.Selection.Version,
		SelectionToken:   request.Selection.Token,
		Content:          request.Content,
		Priority:         request.Priority,
		EditedBy:         request.EditedBy,
		Scope:            scope,
		Order:            order,
	}, nil
}

func behavioralRuleSelectionScopeFromRequest(r *http.Request) (gormdb.CollectionSelectionScope, error) {
	identity, found := auth.IdentityFrom(r.Context())
	if !found {
		return gormdb.CollectionSelectionScope{}, errors.New("browser identity is required")
	}
	subject, found := identity.SessionBrowserSubject()
	if !found {
		return gormdb.CollectionSelectionScope{}, errors.New("browser subject is required")
	}
	sessionID, found := operatorCodeSessionID(r)
	if !found {
		return gormdb.CollectionSelectionScope{}, errors.New("browser session is required")
	}
	scope, err := (operatorCollectionScopeAuthority{}).ResolveOperatorCollectionScope(r.Context(), identity, sessionID, operatorCollectionSelectionDomain)
	if err != nil || scope.SubjectUserID != subject.UserID || scope.SessionID != sessionID || scope.Domain != operatorCollectionSelectionDomain {
		return gormdb.CollectionSelectionScope{}, errors.New("browser scope is not authorized")
	}
	return scope, nil
}

func writeBehavioralRuleSelectionOperationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gormdb.ErrBehavioralRuleSelectionDenied):
		http.Error(w, "rules operation forbidden", http.StatusForbidden)
	case errors.Is(err, gormdb.ErrCollectionSelectionReconfirmationRequired):
		http.Error(w, "rules selection is stale", http.StatusPreconditionFailed)
	case errors.Is(err, gormdb.ErrBehavioralRuleSelectionConflict):
		http.Error(w, "rules selection conflicts with current state", http.StatusConflict)
	case errors.Is(err, gormdb.ErrBehavioralRuleSelectionInvalid):
		http.Error(w, "invalid rules operation", http.StatusBadRequest)
	default:
		log.Error().Err(err).Msg("apply behavioral rule selection operation failed")
		http.Error(w, "rules operation failed", http.StatusInternalServerError)
	}
}

type behavioralRuleSelectionOperationResponse struct {
	RequestID      string                                            `json:"request_id"`
	OperationState string                                            `json:"operation_state"`
	ItemResults    []behavioralRuleSelectionOperationItemResponse    `json:"item_results"`
	Readback       *behavioralRuleSelectionOperationReadbackResponse `json:"readback,omitempty"`
}

type behavioralRuleSelectionOperationItemResponse struct {
	TargetID        int64                                             `json:"target_id"`
	Outcome         string                                            `json:"outcome"`
	ObservedVersion *int                                              `json:"observed_version,omitempty"`
	Readback        *behavioralRuleSelectionOperationReadbackResponse `json:"readback,omitempty"`
}

type behavioralRuleSelectionOperationReadbackResponse struct {
	Authoritative  bool   `json:"authoritative"`
	Kind           string `json:"kind"`
	CurrentState   any    `json:"current_state,omitempty"`
	CurrentVersion *int   `json:"current_version,omitempty"`
}

func newBehavioralRuleSelectionOperationResponse(requestID string, action gormdb.BehavioralRuleSelectionAction, result gormdb.BehavioralRuleSelectionOperationResult) behavioralRuleSelectionOperationResponse {
	response := behavioralRuleSelectionOperationResponse{
		RequestID:   requestID,
		ItemResults: make([]behavioralRuleSelectionOperationItemResponse, 0, len(result.Items)),
	}
	readbackPending := false
	for _, item := range result.Items {
		itemResponse := behavioralRuleSelectionOperationItemResponse{
			TargetID:        item.TargetID,
			Outcome:         string(item.Outcome),
			ObservedVersion: item.ObservedVersion,
		}
		if readback := behavioralRuleSelectionItemReadback(item); readback != nil {
			itemResponse.Readback = readback
		} else {
			readbackPending = true
		}
		response.ItemResults = append(response.ItemResults, itemResponse)
	}
	if readbackPending {
		response.OperationState = "committed"
		return response
	}

	response.OperationState = "completed"
	if action == gormdb.BehavioralRuleSelectionDelete {
		response.Readback = &behavioralRuleSelectionOperationReadbackResponse{Authoritative: true, Kind: "authorized_absence"}
		return response
	}
	states := make([]*models.BehavioralRule, 0, len(result.Items))
	for _, item := range result.Items {
		states = append(states, item.Rule)
	}
	if len(states) == 1 {
		response.Readback = &behavioralRuleSelectionOperationReadbackResponse{
			Authoritative:  true,
			Kind:           "current",
			CurrentState:   states[0],
			CurrentVersion: result.Items[0].ObservedVersion,
		}
	} else {
		response.Readback = &behavioralRuleSelectionOperationReadbackResponse{Authoritative: true, Kind: "current", CurrentState: states}
	}
	return response
}

func behavioralRuleSelectionItemReadback(item gormdb.BehavioralRuleSelectionOperationItem) *behavioralRuleSelectionOperationReadbackResponse {
	if item.ReadbackPending {
		return nil
	}
	if item.Deleted {
		return &behavioralRuleSelectionOperationReadbackResponse{Authoritative: true, Kind: "authorized_absence"}
	}
	if item.Rule == nil {
		return nil
	}
	return &behavioralRuleSelectionOperationReadbackResponse{
		Authoritative:  true,
		Kind:           "current",
		CurrentState:   item.Rule,
		CurrentVersion: item.ObservedVersion,
	}
}
