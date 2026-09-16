package worker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
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

const (
	issueSelectionDomain      = "issues"
	issueSelectionCursorV1    = 1
	issueSelectionFilterLimit = 64
)

type issueSelectionFilter struct {
	Project       string   `json:"project,omitempty"`
	SourceProject string   `json:"source_project,omitempty"`
	Statuses      []string `json:"statuses,omitempty"`
	Type          string   `json:"type,omitempty"`
}

type issueSelectionCursor struct {
	Fingerprint string               `json:"f"`
	Revision    string               `json:"r"`
	Filter      issueSelectionFilter `json:"q"`
	Offset      int                  `json:"o"`
	Limit       int                  `json:"l"`
	Version     int                  `json:"v"`
}

type issueSelectionSnapshotRequest struct {
	Selection issueSelectionRequest `json:"selection"`
}

type issueSelectionRequest struct {
	Kind        gormdb.CollectionSelectionKind     `json:"kind"`
	Targets     []gormdb.CollectionSelectionTarget `json:"targets,omitempty"`
	Cursor      string                             `json:"cursor,omitempty"`
	Filter      *issueSelectionFilter              `json:"filter,omitempty"`
	ExcludedIDs []string                           `json:"excluded_ids,omitempty"`
}

type issueSelectionCurrentRequest struct{}

type issueSelectionPageRequest struct {
	Filter *issueSelectionFilter `json:"filter"`
	Cursor string                `json:"cursor,omitempty"`
	Limit  int                   `json:"limit"`
}

type issueSelectionOperationRequest struct {
	RequestID string                    `json:"request_id"`
	Action    string                    `json:"action"`
	Selection issueSelectionActionScope `json:"selection"`
	Status    *string                   `json:"status,omitempty"`
	Priority  *string                   `json:"priority,omitempty"`
	Labels    *[]string                 `json:"labels,omitempty"`
}

type issueSelectionActionScope struct {
	Kind    gormdb.CollectionSelectionKind `json:"kind"`
	Version int64                          `json:"selection_version"`
	Token   string                         `json:"selection_token,omitempty"`
}

type issueSelectionSnapshotResponse struct {
	Selection issueSelectionSnapshot `json:"selection"`
}

type issueSelectionSnapshot struct {
	Domain                 string                             `json:"domain"`
	Kind                   gormdb.CollectionSelectionKind     `json:"kind"`
	Version                int64                              `json:"selection_version"`
	Targets                []gormdb.CollectionSelectionTarget `json:"targets,omitempty"`
	Cursor                 string                             `json:"cursor,omitempty"`
	FilterFingerprint      string                             `json:"filter_fingerprint,omitempty"`
	TargetCount            int                                `json:"target_count,omitempty"`
	ExcludedIDs            []string                           `json:"excluded_ids,omitempty"`
	Token                  string                             `json:"selection_token,omitempty"`
	ExpiresAt              string                             `json:"expires_at,omitempty"`
	ReconfirmationRequired bool                               `json:"reconfirmation_required"`
	ReconfirmationReason   string                             `json:"reconfirmation_reason,omitempty"`
}

type issueSelectionPageResponse struct {
	FilterFingerprint string                             `json:"filter_fingerprint"`
	Cursor            string                             `json:"cursor"`
	Targets           []gormdb.CollectionSelectionTarget `json:"targets"`
	NextCursor        string                             `json:"next_cursor"`
	Total             int64                              `json:"total"`
}

type issueSelectionOperationResponse struct {
	RequestID      string                          `json:"request_id"`
	OperationState string                          `json:"operation_state"`
	ItemResults    []issueSelectionOperationItem   `json:"item_results"`
	Readback       issueSelectionOperationReadback `json:"readback"`
}

type issueSelectionOperationItem struct {
	TargetID        int64  `json:"target_id"`
	Outcome         string `json:"outcome"`
	ObservedVersion uint64 `json:"observed_version,omitempty"`
}

type issueSelectionOperationReadback struct {
	Authoritative bool                         `json:"authoritative"`
	Kind          string                       `json:"kind"`
	CurrentState  []issueSelectionCurrentState `json:"current_state"`
}

type issueSelectionCurrentState struct {
	Status    string   `json:"status"`
	Priority  string   `json:"priority"`
	Labels    []string `json:"labels"`
	UpdatedAt string   `json:"updated_at"`
}

// HandleIssueSelectionSnapshot saves one browser-scoped Issues selection.
// T031 owns route registration; this handler owns only the Issues boundary.
func (s *Service) HandleIssueSelectionSnapshot(w http.ResponseWriter, r *http.Request) {
	var request issueSelectionSnapshotRequest
	_, scope, selections, ok := s.issueSelectionRequestScope(w, r, &request)
	if !ok {
		return
	}

	selection, err := s.issueSelectionSnapshot(r.Context(), request.Selection)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	if selection.Kind == gormdb.CollectionSelectionFrozenFilter {
		filter, filterErr := normalizeIssueSelectionFilter(request.Selection.Filter)
		if filterErr != nil {
			writeIssueSelectionError(w, filterErr)
			return
		}
		rows, rowsErr := s.issueSelectionRows(r.Context(), filter)
		if rowsErr != nil {
			writeIssueSelectionError(w, rowsErr)
			return
		}
		selection.FilterFingerprint = filter.collectionFilter().Fingerprint
		selection.Targets = issueSelectionTargets(rows)
		selection.ExpiresAt = time.Now().UTC().Add(time.Hour)
	} else if selection.Kind == gormdb.CollectionSelectionPage {
		page, pageErr := s.issueSelectionPage(r.Context(), nil, selection.Cursor, 0)
		if pageErr != nil {
			writeIssueSelectionError(w, pageErr)
			return
		}
		selection.Targets = page.Targets
	} else if selection.Kind == gormdb.CollectionSelectionExplicit {
		selection.Targets, err = s.issueSelectionExplicitTargets(r.Context(), selection.Targets)
		if err != nil {
			writeIssueSelectionError(w, err)
			return
		}
	}
	if err := gormdb.ValidateCollectionSelectionInput(selection); err != nil {
		writeIssueSelectionError(w, err)
		return
	}

	saved, err := selections.Save(r.Context(), scope, selection)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	writeJSON(w, issueSelectionSnapshotResponse{Selection: newIssueSelectionSnapshot(saved)})
}

// HandleIssueSelectionCurrent returns the saved selection only for its exact browser scope.
func (s *Service) HandleIssueSelectionCurrent(w http.ResponseWriter, r *http.Request) {
	var request issueSelectionCurrentRequest
	_, scope, selections, ok := s.issueSelectionRequestScope(w, r, &request)
	if !ok {
		return
	}
	selection, err := selections.Current(r.Context(), scope)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	writeJSON(w, issueSelectionSnapshotResponse{Selection: newIssueSelectionSnapshot(selection)})
}

// HandleIssueSelectionPage returns one bounded, cursor-validated Issues page.
func (s *Service) HandleIssueSelectionPage(w http.ResponseWriter, r *http.Request) {
	var request issueSelectionPageRequest
	_, _, _, ok := s.issueSelectionRequestScope(w, r, &request)
	if !ok {
		return
	}
	page, err := s.issueSelectionPage(r.Context(), request.Filter, request.Cursor, request.Limit)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	writeJSON(w, issueSelectionPageResponse{
		FilterFingerprint: page.filter.Fingerprint,
		Cursor:            page.Cursor,
		Targets:           page.Targets,
		NextCursor:        page.NextCursor,
		Total:             page.Total,
	})
}

// HandleIssueSelectionOperation resolves the current saved selection and delegates
// all locking, permission rechecks, revision checks, and mutation to IssueStore.
func (s *Service) HandleIssueSelectionOperation(w http.ResponseWriter, r *http.Request) {
	var request issueSelectionOperationRequest
	_, scope, selections, ok := s.issueSelectionRequestScope(w, r, &request)
	if !ok {
		return
	}
	if !operatorCodeText(request.RequestID) || r.Header.Get("X-Engram-Request-ID") != request.RequestID {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return
	}

	action, err := issueSelectionAction(request)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	selection, err := issueOperationSelection(r.Context(), selections, scope, request.Selection)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	targets, err := issueSelectionOperationTargets(selection)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	keycardID, isOperator, err := issueMutationActor(r.Context())
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	result, err := s.issueStore.ApplyIssueSelectionOperation(r.Context(), gormdb.IssueSelectionOperation{
		Action:  action,
		Actor:   gormdb.IssueSelectionActor{KeycardID: keycardID, IsOperator: isOperator},
		Targets: targets,
	})
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}

	items := make([]issueSelectionOperationItem, 0, len(result.Items))
	current := make([]issueSelectionCurrentState, 0, len(result.Items))
	for _, item := range result.Items {
		response := issueSelectionOperationItem{TargetID: item.TargetID, Outcome: string(item.Outcome)}
		if item.Readback != nil {
			response.ObservedVersion = issueSelectionRevision(item.Readback.UpdatedAt)
			current = append(current, issueSelectionCurrentState{
				Status:    item.Readback.Status,
				Priority:  item.Readback.Priority,
				Labels:    append([]string(nil), item.Readback.Labels...),
				UpdatedAt: item.Readback.UpdatedAt.UTC().Format(time.RFC3339Nano),
			})
		}
		items = append(items, response)
	}
	writeJSON(w, issueSelectionOperationResponse{
		RequestID:      request.RequestID,
		OperationState: "completed",
		ItemResults:    items,
		Readback: issueSelectionOperationReadback{
			Authoritative: true,
			Kind:          "current",
			CurrentState:  current,
		},
	})
}

type issueSelectionPage struct {
	filter     gormdb.CollectionFilter
	Cursor     string
	Targets    []gormdb.CollectionSelectionTarget
	NextCursor string
	Total      int64
}

const issueSelectionDigestPrefix = "sha256:"

func (s *Service) issueSelectionRequestScope(w http.ResponseWriter, r *http.Request, target any) (auth.Identity, gormdb.CollectionSelectionScope, *gormdb.CollectionSelectionStore, bool) {
	if s == nil || s.store == nil || s.issueStore == nil || r == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return auth.Identity{}, gormdb.CollectionSelectionScope{}, nil, false
	}
	identity, found := auth.IdentityFrom(r.Context())
	if !found {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return auth.Identity{}, gormdb.CollectionSelectionScope{}, nil, false
	}
	subject, found := identity.SessionBrowserSubject()
	if !found {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return auth.Identity{}, gormdb.CollectionSelectionScope{}, nil, false
	}
	sessionID, found := operatorCodeSessionID(r)
	if !found {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return auth.Identity{}, gormdb.CollectionSelectionScope{}, nil, false
	}
	if !operatorCodeText(r.Header.Get("X-Engram-Request-ID")) {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return auth.Identity{}, gormdb.CollectionSelectionScope{}, nil, false
	}
	if _, err := operatorCodeReadJSON(r, target); err != nil {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return auth.Identity{}, gormdb.CollectionSelectionScope{}, nil, false
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("issues-collection-scope/v1\x00%d\x00%s\x00%s", subject.UserID, sessionID, issueSelectionDomain)))
	return identity, gormdb.CollectionSelectionScope{
		SubjectUserID:      subject.UserID,
		SessionID:          sessionID,
		Domain:             issueSelectionDomain,
		ContextFingerprint: issueSelectionDigestPrefix + hex.EncodeToString(digest[:]),
		AuthorizationEpoch: 1,
		CollectionVersion:  1,
	}, gormdb.NewCollectionSelectionStore(s.store.GetDB()), true
}

func (s *Service) issueSelectionSnapshot(ctx context.Context, request issueSelectionRequest) (gormdb.CollectionSelection, error) {
	selection := gormdb.CollectionSelection{
		Kind:        request.Kind,
		Targets:     append([]gormdb.CollectionSelectionTarget(nil), request.Targets...),
		Cursor:      request.Cursor,
		ExcludedIDs: append([]string(nil), request.ExcludedIDs...),
	}
	switch selection.Kind {
	case gormdb.CollectionSelectionNone:
		if len(selection.Targets) != 0 || selection.Cursor != "" || request.Filter != nil || len(selection.ExcludedIDs) != 0 {
			return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
		}
	case gormdb.CollectionSelectionExplicit:
		if selection.Cursor != "" || request.Filter != nil || len(selection.ExcludedIDs) != 0 {
			return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
		}
	case gormdb.CollectionSelectionPage:
		if selection.Cursor == "" || len(selection.Targets) != 0 || request.Filter != nil || len(selection.ExcludedIDs) != 0 {
			return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
		}
	case gormdb.CollectionSelectionFrozenFilter:
		if request.Filter == nil || len(selection.Targets) != 0 || selection.Cursor != "" {
			return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
		}
	default:
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
	}
	return selection, nil
}

func (s *Service) issueSelectionExplicitTargets(ctx context.Context, targets []gormdb.CollectionSelectionTarget) ([]gormdb.CollectionSelectionTarget, error) {
	if len(targets) == 0 || len(targets) > gormdb.CollectionSelectionMaxTargets {
		return nil, gormdb.ErrCollectionSelectionInvalid
	}
	resolved := make([]gormdb.CollectionSelectionTarget, 0, len(targets))
	seen := make(map[int64]struct{}, len(targets))
	for _, target := range targets {
		id, err := strconv.ParseInt(target.ID, 10, 64)
		if err != nil || id <= 0 {
			return nil, gormdb.ErrCollectionSelectionInvalid
		}
		if _, found := seen[id]; found {
			return nil, gormdb.ErrCollectionSelectionInvalid
		}
		issue, _, err := s.issueStore.GetIssue(ctx, id)
		if err != nil {
			return nil, gormdb.ErrCollectionSelectionDenied
		}
		seen[id] = struct{}{}
		resolved = append(resolved, gormdb.CollectionSelectionTarget{ID: target.ID, ExpectedVersion: issueSelectionRevision(issue.UpdatedAt)})
	}
	return resolved, nil
}

func (s *Service) issueSelectionRows(ctx context.Context, filter issueSelectionFilter) ([]gormdb.IssueWithCount, error) {
	params := gormdb.IssueListParams{
		TargetProject: filter.Project,
		SourceProject: filter.SourceProject,
		Statuses:      append([]string(nil), filter.Statuses...),
		Type:          filter.Type,
		Limit:         gormdb.CollectionSelectionMaxTargets + 1,
	}
	rows, total, err := s.issueStore.ListIssuesEx(ctx, params)
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, gormdb.ErrCollectionSelectionDenied
	}
	if total > gormdb.CollectionSelectionMaxTargets || int64(len(rows)) != total {
		return nil, gormdb.ErrCollectionSelectionInvalid
	}
	return rows, nil
}

func (s *Service) issueSelectionPage(ctx context.Context, requestedFilter *issueSelectionFilter, cursor string, requestedLimit int) (issueSelectionPage, error) {
	filter, state, err := issueSelectionPageFilter(requestedFilter, cursor)
	if err != nil {
		return issueSelectionPage{}, err
	}
	limit := requestedLimit
	if limit == 0 && cursor != "" {
		limit = state.Limit
	}
	if limit < 1 || limit > gormdb.CollectionPageMaxSize {
		return issueSelectionPage{}, gormdb.ErrCollectionSelectionInvalid
	}
	return s.issueSelectionPageResult(ctx, filter, state, cursor, limit)
}

func issueSelectionPageFilter(requestedFilter *issueSelectionFilter, cursor string) (issueSelectionFilter, issueSelectionCursor, error) {
	if cursor == "" {
		filter, err := normalizeIssueSelectionFilter(requestedFilter)
		return filter, issueSelectionCursor{}, err
	}
	state, err := decodeIssueSelectionCursor(cursor)
	if err != nil {
		return issueSelectionFilter{}, issueSelectionCursor{}, err
	}
	if requestedFilter == nil {
		return state.Filter, state, nil
	}
	normalized, normalizeErr := normalizeIssueSelectionFilter(requestedFilter)
	if normalizeErr != nil || !sameIssueSelectionFilter(normalized, state.Filter) {
		return issueSelectionFilter{}, issueSelectionCursor{}, gormdb.ErrCollectionSelectionInvalid
	}
	return state.Filter, state, nil
}

func (s *Service) issueSelectionPageResult(ctx context.Context, filter issueSelectionFilter, state issueSelectionCursor, cursor string, limit int) (issueSelectionPage, error) {
	rows, err := s.issueSelectionRows(ctx, filter)
	if err != nil {
		return issueSelectionPage{}, err
	}
	collectionFilter := filter.collectionFilter()
	revision := issueSelectionRevisionDigest(collectionFilter, rows)
	offset := 0
	if cursor != "" {
		if state.Fingerprint != collectionFilter.Fingerprint || state.Revision != revision || state.Limit != limit || state.Offset < 0 || state.Offset >= len(rows) {
			return issueSelectionPage{}, gormdb.ErrCollectionSelectionReconfirmationRequired
		}
		offset = state.Offset
	} else {
		cursor, err = encodeIssueSelectionCursor(collectionFilter, revision, filter, 0, limit)
		if err != nil {
			return issueSelectionPage{}, err
		}
	}
	end := min(offset+limit, len(rows))
	var nextCursor string
	if end < len(rows) {
		nextCursor, err = encodeIssueSelectionCursor(collectionFilter, revision, filter, end, limit)
		if err != nil {
			return issueSelectionPage{}, err
		}
	}
	return issueSelectionPage{
		filter:     collectionFilter,
		Cursor:     cursor,
		Targets:    issueSelectionTargets(rows[offset:end]),
		NextCursor: nextCursor,
		Total:      int64(len(rows)),
	}, nil
}

func normalizeIssueSelectionFilter(request *issueSelectionFilter) (issueSelectionFilter, error) {
	if request == nil {
		return issueSelectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	filter := issueSelectionFilter{
		Project:       strings.TrimSpace(request.Project),
		SourceProject: strings.TrimSpace(request.SourceProject),
		Type:          strings.ToLower(strings.TrimSpace(request.Type)),
	}
	if filter.Project != request.Project || filter.SourceProject != request.SourceProject || filter.Type != request.Type ||
		(len(filter.Project) > issueSelectionFilterLimit || len(filter.SourceProject) > issueSelectionFilterLimit) {
		return issueSelectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	if filter.Type != "" && filter.Type != "bug" && filter.Type != "feature" && filter.Type != "improvement" && filter.Type != "task" {
		return issueSelectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	statuses := make([]string, 0, len(request.Statuses))
	seen := make(map[string]struct{}, len(request.Statuses))
	for _, status := range request.Statuses {
		normalized := strings.ToLower(strings.TrimSpace(status))
		if normalized != status || !validIssueSelectionStatus(normalized) {
			return issueSelectionFilter{}, gormdb.ErrCollectionSelectionInvalid
		}
		if _, found := seen[normalized]; found {
			return issueSelectionFilter{}, gormdb.ErrCollectionSelectionInvalid
		}
		seen[normalized] = struct{}{}
		statuses = append(statuses, normalized)
	}
	sort.Strings(statuses)
	filter.Statuses = statuses
	return filter, nil
}

func sameIssueSelectionFilter(left, right issueSelectionFilter) bool {
	if left.Project != right.Project || left.SourceProject != right.SourceProject || left.Type != right.Type || len(left.Statuses) != len(right.Statuses) {
		return false
	}
	for index := range left.Statuses {
		if left.Statuses[index] != right.Statuses[index] {
			return false
		}
	}
	return true
}

func validIssueSelectionStatus(value string) bool {
	switch value {
	case "open", "acknowledged", "reopened", "resolved", "closed", "rejected":
		return true
	default:
		return false
	}
}

func (filter issueSelectionFilter) collectionFilter() gormdb.CollectionFilter {
	digest := sha256.Sum256([]byte("issues-collection-filter/v1\x00priority:asc\x00created_at:desc\x00" + filter.Project + "\x00" + filter.SourceProject + "\x00" + strings.Join(filter.Statuses, ",") + "\x00" + filter.Type))
	return gormdb.CollectionFilter{Fingerprint: issueSelectionDigestPrefix + hex.EncodeToString(digest[:]), Value: "issues"}
}

func issueSelectionRevisionDigest(filter gormdb.CollectionFilter, rows []gormdb.IssueWithCount) string {
	hash := sha256.New()
	hash.Write([]byte("issues-collection-revision/v1\x00" + filter.Fingerprint + "\x00"))
	for _, row := range rows {
		hash.Write([]byte(strconv.FormatInt(row.ID, 10)))
		hash.Write([]byte{0})
		hash.Write([]byte(strconv.FormatUint(issueSelectionRevision(row.UpdatedAt), 10)))
		hash.Write([]byte{0})
	}
	return issueSelectionDigestPrefix + hex.EncodeToString(hash.Sum(nil))
}

func issueSelectionTargets(rows []gormdb.IssueWithCount) []gormdb.CollectionSelectionTarget {
	targets := make([]gormdb.CollectionSelectionTarget, len(rows))
	for index, row := range rows {
		targets[index] = gormdb.CollectionSelectionTarget{ID: strconv.FormatInt(row.ID, 10), ExpectedVersion: issueSelectionRevision(row.UpdatedAt)}
	}
	return targets
}

func issueSelectionRevision(updatedAt time.Time) uint64 {
	return uint64(updatedAt.UTC().Truncate(time.Microsecond).UnixMicro())
}

func issueSelectionUpdatedAt(version uint64) (time.Time, error) {
	if version == 0 || version > uint64(^uint64(0)>>1) {
		return time.Time{}, gormdb.ErrCollectionSelectionInvalid
	}
	return time.UnixMicro(int64(version)).UTC(), nil
}

func encodeIssueSelectionCursor(filter gormdb.CollectionFilter, revision string, request issueSelectionFilter, offset, limit int) (string, error) {
	if !filter.Valid() || !validIssueSelectionRevision(revision) || offset < 0 || limit < 1 || limit > gormdb.CollectionPageMaxSize {
		return "", gormdb.ErrCollectionSelectionInvalid
	}
	payload, err := json.Marshal(issueSelectionCursor{Fingerprint: filter.Fingerprint, Revision: revision, Filter: request, Offset: offset, Limit: limit, Version: issueSelectionCursorV1})
	if err != nil {
		return "", err
	}
	cursor := base64.RawURLEncoding.EncodeToString(payload)
	if len(cursor) == 0 || len(cursor) > 512 {
		return "", gormdb.ErrCollectionSelectionInvalid
	}
	return cursor, nil
}

func decodeIssueSelectionCursor(cursor string) (issueSelectionCursor, error) {
	if len(cursor) == 0 || len(cursor) > 512 {
		return issueSelectionCursor{}, gormdb.ErrCollectionSelectionInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return issueSelectionCursor{}, gormdb.ErrCollectionSelectionInvalid
	}
	var state issueSelectionCursor
	if json.Unmarshal(payload, &state) != nil || state.Version != issueSelectionCursorV1 || state.Offset < 0 || state.Limit < 1 || state.Limit > gormdb.CollectionPageMaxSize || !validIssueSelectionRevision(state.Revision) {
		return issueSelectionCursor{}, gormdb.ErrCollectionSelectionInvalid
	}
	filter, err := normalizeIssueSelectionFilter(&state.Filter)
	if err != nil || !sameIssueSelectionFilter(filter, state.Filter) || filter.collectionFilter().Fingerprint != state.Fingerprint {
		return issueSelectionCursor{}, gormdb.ErrCollectionSelectionInvalid
	}
	return state, nil
}

func validIssueSelectionRevision(value string) bool {
	if len(value) != len(issueSelectionDigestPrefix)+64 || !strings.HasPrefix(value, issueSelectionDigestPrefix) {
		return false
	}
	for _, character := range value[len(issueSelectionDigestPrefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func issueOperationSelection(ctx context.Context, store *gormdb.CollectionSelectionStore, scope gormdb.CollectionSelectionScope, request issueSelectionActionScope) (gormdb.CollectionSelection, error) {
	if request.Version < 1 {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
	}
	var (
		selection gormdb.CollectionSelection
		err       error
	)
	if request.Kind == gormdb.CollectionSelectionFrozenFilter {
		selection, err = store.Frozen(ctx, scope, request.Token)
	} else if request.Kind == gormdb.CollectionSelectionExplicit || request.Kind == gormdb.CollectionSelectionPage {
		if request.Token != "" {
			return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
		}
		selection, err = store.Current(ctx, scope)
	} else {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
	}
	if err != nil {
		return gormdb.CollectionSelection{}, err
	}
	if selection.Kind != request.Kind || selection.Version != request.Version {
		return gormdb.CollectionSelection{}, gormdb.ErrIssueSelectionConflict
	}
	if selection.ReconfirmationRequired {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionReconfirmationRequired
	}
	return selection, nil
}

func issueSelectionOperationTargets(selection gormdb.CollectionSelection) ([]gormdb.IssueSelectionTarget, error) {
	excluded := make(map[string]struct{}, len(selection.ExcludedIDs))
	for _, id := range selection.ExcludedIDs {
		excluded[id] = struct{}{}
	}
	targets := make([]gormdb.IssueSelectionTarget, 0, len(selection.Targets))
	for _, target := range selection.Targets {
		if _, found := excluded[target.ID]; found {
			continue
		}
		id, err := strconv.ParseInt(target.ID, 10, 64)
		if err != nil || id <= 0 {
			return nil, gormdb.ErrCollectionSelectionInvalid
		}
		updatedAt, err := issueSelectionUpdatedAt(target.ExpectedVersion)
		if err != nil {
			return nil, err
		}
		targets = append(targets, gormdb.IssueSelectionTarget{ID: id, ExpectedUpdatedAt: updatedAt})
	}
	if len(targets) == 0 {
		return nil, gormdb.ErrCollectionSelectionInvalid
	}
	return targets, nil
}

func issueSelectionAction(request issueSelectionOperationRequest) (gormdb.IssueSelectionAction, error) {
	action := gormdb.IssueSelectionAction{Status: request.Status, Priority: request.Priority, Labels: request.Labels}
	switch request.Action {
	case string(gormdb.IssueSelectionAcknowledge):
		action.Kind = gormdb.IssueSelectionAcknowledge
	case string(gormdb.IssueSelectionStatus):
		action.Kind = gormdb.IssueSelectionStatus
	case string(gormdb.IssueSelectionPriority):
		action.Kind = gormdb.IssueSelectionPriority
	case string(gormdb.IssueSelectionLabels):
		action.Kind = gormdb.IssueSelectionLabels
	default:
		return gormdb.IssueSelectionAction{}, gormdb.ErrIssueInvalidInput
	}
	return action, nil
}

func newIssueSelectionSnapshot(selection gormdb.CollectionSelection) issueSelectionSnapshot {
	result := issueSelectionSnapshot{
		Domain:  issueSelectionDomain,
		Kind:    selection.Kind,
		Version: selection.Version,
	}
	switch selection.Kind {
	case gormdb.CollectionSelectionExplicit:
		result.Targets = append([]gormdb.CollectionSelectionTarget(nil), selection.Targets...)
	case gormdb.CollectionSelectionPage:
		result.Cursor = selection.Cursor
		result.Targets = append([]gormdb.CollectionSelectionTarget(nil), selection.Targets...)
	case gormdb.CollectionSelectionFrozenFilter:
		result.FilterFingerprint = selection.FilterFingerprint
		result.TargetCount = len(selection.Targets) - len(selection.ExcludedIDs)
		result.ExcludedIDs = append([]string(nil), selection.ExcludedIDs...)
		result.Token = selection.Token
		result.ExpiresAt = selection.ExpiresAt.UTC().Format(time.RFC3339Nano)
		result.ReconfirmationRequired = selection.ReconfirmationRequired
		if selection.ReconfirmationRequired {
			result.ReconfirmationReason = string(selection.ReconfirmationReason)
		}
	}
	return result
}

func writeIssueSelectionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gormdb.ErrIssueForbidden), errors.Is(err, gormdb.ErrCollectionSelectionDenied):
		operatorCodeWriteBodyless(w, http.StatusForbidden)
	case errors.Is(err, gormdb.ErrIssueSelectionConflict), errors.Is(err, gormdb.ErrIssueNotFound), errors.Is(err, gormdb.ErrIssueInvalidTransition):
		operatorCodeWriteBodyless(w, http.StatusConflict)
	case errors.Is(err, gormdb.ErrCollectionSelectionReconfirmationRequired):
		operatorCodeWriteBodyless(w, http.StatusPreconditionFailed)
	case errors.Is(err, gormdb.ErrCollectionSelectionInvalid), errors.Is(err, gormdb.ErrIssueInvalidInput):
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
	default:
		operatorCodeWriteBodyless(w, http.StatusInternalServerError)
	}
}
