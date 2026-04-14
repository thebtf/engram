package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

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

	// Collect unique project IDs to avoid redundant lookups.
	projectNames := make(map[string]string)
	for _, iss := range issues {
		if iss.SourceProject != "" {
			projectNames[iss.SourceProject] = s.getProjectDisplayName(iss.SourceProject)
		}
		if iss.TargetProject != "" {
			projectNames[iss.TargetProject] = s.getProjectDisplayName(iss.TargetProject)
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
		"issue":                        issue,
		"comments":                     comments,
		"comment_count":                len(comments),
		"source_project_display_name":  s.getProjectDisplayName(issue.SourceProject),
		"target_project_display_name":  s.getProjectDisplayName(issue.TargetProject),
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

	issue := &gormdb.Issue{
		Title:            req.Title,
		Body:             req.Body,
		Priority:         req.Priority,
		Type:             req.Type,
		SourceProject:    req.SourceProject,
		TargetProject:    req.TargetProject,
		SourceAgent:      req.SourceAgent,
		CreatedBySession: req.CreatedBySession,
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

	// Field edits (dashboard inline editing)
	if req.Title != "" || req.Body != "" || req.Priority != "" || req.Type != "" || req.Labels != nil {
		if err := s.issueStore.UpdateIssueFields(r.Context(), id, req.Title, req.Body, req.Priority, req.Type, req.Labels); err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, `{"error": "issue not found"}`, http.StatusNotFound)
				return
			}
			http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusBadRequest)
			return
		}
	}

	// Status transitions
	if req.Status != "" {
		var statusErr error
		switch req.Status {
		case "resolved":
			statusErr = s.issueStore.UpdateIssueStatus(r.Context(), id, req.Status)
		case "reopened":
			statusErr = s.issueStore.ReopenIssue(r.Context(), id, req.Comment, req.SourceProject, req.SourceAgent)
			req.Comment = "" // ReopenIssue already adds comment
		case "closed":
			statusErr = s.issueStore.CloseIssue(r.Context(), id, req.SourceProject)
		case "rejected":
			statusErr = s.issueStore.RejectIssue(r.Context(), id, req.Comment, req.SourceProject, req.SourceAgent)
			req.Comment = "" // RejectIssue already adds comment
		case "open", "acknowledged":
			// Force status (operator override) — no lifecycle validation
			statusErr = s.issueStore.UpdateIssueStatus(r.Context(), id, req.Status)
		default:
			http.Error(w, `{"error": "invalid status"}`, http.StatusBadRequest)
			return
		}
		if statusErr != nil {
			if strings.Contains(statusErr.Error(), "not found") {
				http.Error(w, `{"error": "issue not found"}`, http.StatusNotFound)
				return
			}
			http.Error(w, fmt.Sprintf(`{"error": %q}`, statusErr.Error()), http.StatusBadRequest)
			return
		}
	}

	if req.Comment != "" {
		_, err := s.issueStore.AddComment(r.Context(), id, &gormdb.IssueComment{
			AuthorProject: req.SourceProject,
			AuthorAgent:   req.SourceAgent,
			Body:          req.Comment,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"message": "issue updated",
	})
}

// handleAcknowledgeIssues handles POST /api/issues/acknowledge.
func (s *Service) handleAcknowledgeIssues(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	acknowledged, err := s.issueStore.AcknowledgeIssues(r.Context(), req.IDs)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
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

// formatIssuesForInjection formats issues into the <open-issues> XML block for context injection.
func formatIssuesForInjection(issues []gormdb.IssueWithCount, project string) string {
	if len(issues) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<open-issues count=\"%d\" project=\"%s\">\n", len(issues), escapeXML(project)))

	for _, issue := range issues {
		priority := strings.ToUpper(issue.Priority)
		sb.WriteString(fmt.Sprintf("#%d [%s] [from: %s] %s\n", issue.ID, priority, escapeXML(issue.SourceProject), escapeXML(issue.Title)))

		if issue.CommentCount > 0 {
			// Fetch latest comment preview — simplified: just show count
			ago := time.Since(issue.UpdatedAt).Truncate(time.Minute)
			sb.WriteString(fmt.Sprintf("  └─ %d comment(s), updated %s ago\n", issue.CommentCount, formatDuration(ago)))
		}
	}

	sb.WriteString("</open-issues>")
	return sb.String()
}

// escapeXML escapes special characters for safe inclusion in XML-like injection blocks.
func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// formatDuration returns a human-readable duration string.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
