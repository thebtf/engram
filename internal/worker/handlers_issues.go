package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// issueMutationActor accepts only read-write SourceClient keycards or
// authenticated non-client admins. Project and agent request fields are
// attribution only.
func issueMutationActor(ctx context.Context) (keycardID string, isOperator bool, err error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return "", false, fmt.Errorf("%w: issue mutation forbidden: authenticated identity is required", gormdb.ErrIssueForbidden)
	}
	if id.IsAdmin() && id.Source != auth.SourceClient {
		return "", true, nil
	}
	if id.Source != auth.SourceClient || id.KeycardID == "" || id.Role != auth.RoleReadWrite {
		return "", false, fmt.Errorf("%w: issue mutation forbidden: authenticated read-write client keycard is required", gormdb.ErrIssueForbidden)
	}
	return id.KeycardID, false, nil
}

func (s *Service) authorizeIssueProgression(ctx context.Context) (bool, error) {
	keycardID, isOperator, err := issueMutationActor(ctx)
	if err != nil {
		return false, err
	}
	if err := s.issueStore.AuthorizeIssueProgressionMutation(ctx, keycardID, isOperator); err != nil {
		return false, err
	}
	return isOperator, nil
}

func (s *Service) authorizeIssueSourceMutation(ctx context.Context, id int64) (bool, error) {
	keycardID, isOperator, err := issueMutationActor(ctx)
	if err != nil {
		return false, err
	}
	if err := s.issueStore.AuthorizeIssueSourceMutation(ctx, id, keycardID, isOperator); err != nil {
		return false, err
	}
	return isOperator, nil
}

func requireIssueOperator(ctx context.Context) error {
	_, isOperator, err := issueMutationActor(ctx)
	if err != nil {
		return err
	}
	if !isOperator {
		return fmt.Errorf("%w: issue mutation forbidden: operator identity is required", gormdb.ErrIssueForbidden)
	}
	return nil
}

func writeIssueAuthorizationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gormdb.ErrIssueNotFound):
		http.Error(w, `{"error": "issue not found"}`, http.StatusNotFound)
	case errors.Is(err, gormdb.ErrIssueForbidden):
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusForbidden)
	default:
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
	}
}

func writeIssueStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gormdb.ErrIssueNotFound):
		http.Error(w, `{"error": "issue not found"}`, http.StatusNotFound)
	case errors.Is(err, gormdb.ErrIssueForbidden):
		writeIssueAuthorizationError(w, err)
	case errors.Is(err, gormdb.ErrIssueInvalidInput), errors.Is(err, gormdb.ErrIssueInvalidTransition):
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusBadRequest)
	default:
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
	}
}

// handleListIssues handles GET /api/issues with optional filters.
func (s *Service) handleListIssues(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	sourceProject := r.URL.Query().Get("source_project")
	statusParam := r.URL.Query().Get("status")
	typeParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	resolvedSinceStr := r.URL.Query().Get("resolved_since")

	var statuses []string
	if statusParam != "" {
		for _, s := range strings.Split(statusParam, ",") {
			if s = strings.TrimSpace(s); s != "" {
				statuses = append(statuses, s)
			}
		}
	}

	limit := 50
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
		limit = v
	}
	offset := 0
	if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
		offset = v
	}

	params := gormdb.IssueListParams{
		TargetProject: project,
		SourceProject: sourceProject,
		Statuses:      statuses,
		Type:          typeParam,
		Limit:         limit,
		Offset:        offset,
	}
	if resolvedSinceStr != "" {
		if ms, err := strconv.ParseInt(resolvedSinceStr, 10, 64); err == nil {
			t := time.UnixMilli(ms)
			params.ResolvedSince = &t
		}
	}

	issues, total, err := s.issueStore.ListIssuesEx(r.Context(), params)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// Collect unique project IDs to avoid redundant lookups (check map before querying).
	projectNames := make(map[string]string)
	for _, iss := range issues {
		if iss.SourceProject != "" {
			if _, ok := projectNames[iss.SourceProject]; !ok {
				projectNames[iss.SourceProject] = s.getProjectDisplayName(r.Context(), iss.SourceProject)
			}
		}
		if iss.TargetProject != "" {
			if _, ok := projectNames[iss.TargetProject]; !ok {
				projectNames[iss.TargetProject] = s.getProjectDisplayName(r.Context(), iss.TargetProject)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"issues":        issues,
		"total":         total,
		"project_names": projectNames,
	})
}

// handleGetIssue handles GET /api/issues/{id}.
func (s *Service) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, `{"error": "invalid issue id"}`, http.StatusBadRequest)
		return
	}

	issue, comments, err := s.issueStore.GetIssue(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, `{"error": "issue not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"issue":                       issue,
		"comments":                    comments,
		"comment_count":               len(comments),
		"source_project_display_name": s.getProjectDisplayName(r.Context(), issue.SourceProject),
		"target_project_display_name": s.getProjectDisplayName(r.Context(), issue.TargetProject),
	})
}

// handleCreateIssue handles POST /api/issues.
func (s *Service) handleCreateIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title            string   `json:"title"`
		Body             string   `json:"body"`
		Priority         string   `json:"priority"`
		Type             string   `json:"type"`
		SourceProject    string   `json:"source_project"`
		TargetProject    string   `json:"target_project"`
		SourceAgent      string   `json:"source_agent"`
		CreatedBySession string   `json:"created_by_session"`
		Labels           []string `json:"labels"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	if req.Title == "" {
		http.Error(w, `{"error": "title is required"}`, http.StatusBadRequest)
		return
	}
	req.Type = strings.ToLower(strings.TrimSpace(req.Type))
	if req.TargetProject == "" && req.SourceProject != "" {
		req.TargetProject = req.SourceProject
	}
	if req.TargetProject == "" {
		http.Error(w, `{"error": "target_project is required"}`, http.StatusBadRequest)
		return
	}

	keycardID, isOperator, err := issueMutationActor(r.Context())
	if err != nil {
		writeIssueAuthorizationError(w, err)
		return
	}
	if isOperator {
		keycardID = ""
	}
	issue := &gormdb.Issue{
		Title:            req.Title,
		Body:             req.Body,
		Priority:         req.Priority,
		Type:             req.Type,
		SourceProject:    req.SourceProject,
		TargetProject:    req.TargetProject,
		SourceAgent:      req.SourceAgent,
		CreatedBySession: req.CreatedBySession,
		CreatorKeycardID: keycardID,
		Labels:           req.Labels,
	}

	id, err := s.issueStore.CreateIssue(r.Context(), issue)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	log.Info().
		Int64("issue_id", id).
		Str("title", req.Title).
		Str("source", req.SourceProject).
		Str("target", req.TargetProject).
		Msg("Issue created")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":      id,
		"message": "issue created",
	})
}

// handleUpdateIssue handles PATCH /api/issues/{id}.
func (s *Service) handleUpdateIssue(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, `{"error": "invalid issue id"}`, http.StatusBadRequest)
		return
	}

	var req struct {
		Status        string   `json:"status"`
		Comment       string   `json:"comment"`
		SourceProject string   `json:"source_project"`
		SourceAgent   string   `json:"source_agent"`
		Title         string   `json:"title"`
		Body          string   `json:"body"`
		Priority      string   `json:"priority"`
		Type          string   `json:"type"`
		Labels        []string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	// Normalize type before validation and storage.
	req.Type = strings.ToLower(strings.TrimSpace(req.Type))

	update := gormdb.IssueUpdate{
		Status:        req.Status,
		Comment:       req.Comment,
		AuthorProject: req.SourceProject,
		AuthorAgent:   req.SourceAgent,
		Title:         req.Title,
		Body:          req.Body,
		Priority:      req.Priority,
		Type:          req.Type,
		Labels:        req.Labels,
	}
	// Field edits and source-terminal actions require the creator keycard or an
	// operator. Comment-only and resolved progression require only a RW keycard.
	var isOperator bool
	if update.HasSourceAuthorityAction() {
		isOperator, err = s.authorizeIssueSourceMutation(r.Context(), id)
	} else {
		isOperator, err = s.authorizeIssueProgression(r.Context())
	}
	if err != nil {
		writeIssueAuthorizationError(w, err)
		return
	}

	if err := s.issueStore.UpdateIssueAtomically(r.Context(), id, update, isOperator); err != nil {
		writeIssueStoreError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"message": "issue updated",
	})
}

// handleAcknowledgeIssues handles POST /api/issues/acknowledge.
func (s *Service) handleAcknowledgeIssues(w http.ResponseWriter, r *http.Request) {
	if err := requireIssueOperator(r.Context()); err != nil {
		writeIssueAuthorizationError(w, err)
		return
	}
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	acknowledged, err := s.issueStore.AcknowledgeIssuesAtomically(r.Context(), req.IDs)
	if err != nil {
		writeIssueStoreError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"acknowledged": acknowledged,
	})
}

// handleTrackedProjects handles GET /api/issues/tracked-projects.
// Returns the set of projects that use engram's issue system, so agents can
// tell "is this project in engram?" — if not, they should use GitHub/Linear/etc.
func (s *Service) handleTrackedProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.issueStore.GetTrackedProjects(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if projects == nil {
		projects = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"projects": projects,
		"count":    len(projects),
	})
}

// handleDeleteIssue handles DELETE /api/issues/{id}. Hard delete — intended for dashboard operators.
func (s *Service) handleDeleteIssue(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, `{"error": "invalid issue id"}`, http.StatusBadRequest)
		return
	}
	if err := requireIssueOperator(r.Context()); err != nil {
		writeIssueAuthorizationError(w, err)
		return
	}
	if err := s.issueStore.DeleteIssue(r.Context(), id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, `{"error": "issue not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	log.Info().Int64("issue_id", id).Msg("Issue deleted by operator")
	w.WriteHeader(http.StatusNoContent)
}
