package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// issuesToolSchema returns the flat JSON Schema for the issues tool.
// Anthropic API does NOT support oneOf/allOf/anyOf at the top level of input_schema,
// so we use a flat schema with per-action requirements encoded in each property's
// description (via REQUIRED_FOR: prefix) and enforced server-side by
// validateIssueActionParams() with clear error messages showing the full signature.
func issuesToolSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"action"},
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"create", "list", "get", "update", "comment", "reopen", "close"},
				"description": "Action to perform.",
			},
			"project": map[string]any{
				"type":        "string",
				"description": "REQUIRED_FOR: create|update|comment|reopen|close. YOUR current project slug (identifies who is acting — audit trail). For list: optional filter by target_project.",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "REQUIRED_FOR: create. Short issue title.",
			},
			"target_project": map[string]any{
				"type":        "string",
				"description": "REQUIRED_FOR: create. Which project the issue is FOR (where it will be injected at session start).",
			},
			"id": map[string]any{
				"type":        "integer",
				"description": "REQUIRED_FOR: get|update|comment|reopen|close. Issue ID returned by create or list.",
			},
			"body": map[string]any{
				"type":        "string",
				"description": "REQUIRED_FOR: comment. Comment text. For create: optional body/description. For reopen: optional reason.",
			},
			"status": map[string]any{
				"type":        "string",
				"enum":        []string{"resolved"},
				"description": "REQUIRED_FOR: update. Only 'resolved' is allowed. Use close/reopen actions for other transitions.",
			},
			"priority": map[string]any{
				"type":        "string",
				"enum":        []string{"critical", "high", "medium", "low"},
				"default":     "medium",
				"description": "OPTIONAL_FOR: create. Default: medium.",
			},
			"labels": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "OPTIONAL_FOR: create. Tags like 'bug', 'feature', 'reliability'.",
			},
			"source_project": map[string]any{
				"type":        "string",
				"description": "OPTIONAL_FOR: list. Filter to show only issues YOU created.",
			},
			"comment": map[string]any{
				"type":        "string",
				"description": "OPTIONAL_FOR: update|reopen. Add a comment when changing status.",
			},
			"resolved_since": map[string]any{
				"type":        "integer",
				"description": "OPTIONAL_FOR: list. Filter to issues resolved after this epoch-ms timestamp.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "OPTIONAL_FOR: list. Max results (default 20).",
			},
		},
	}
}

// actionRequirements describes required parameters for each issues action.
// Used for upfront validation and consistent error messages.
var actionRequirements = map[string]struct {
	required []string
	full     string
}{
	"create":  {required: []string{"project", "title", "target_project"}, full: "action, project, title, target_project"},
	"list":    {required: []string{}, full: "action  [optional: project, source_project, status, resolved_since, limit]"},
	"get":     {required: []string{"id"}, full: "action, id"},
	"update":  {required: []string{"project", "id", "status"}, full: "action, project, id, status=resolved"},
	"comment": {required: []string{"project", "id", "body"}, full: "action, project, id, body"},
	"reopen":  {required: []string{"project", "id"}, full: "action, project, id"},
	"close":   {required: []string{"project", "id"}, full: "action, project, id"},
}

// validateIssueActionParams checks that all required params for the given action are present.
// Returns an error listing ALL missing params and the full required signature, so the caller
// can see the complete picture in one error.
func validateIssueActionParams(action string, m map[string]any) error {
	spec, ok := actionRequirements[action]
	if !ok {
		return fmt.Errorf("unknown issues action: %q (valid: create, list, get, update, comment, reopen, close)", action)
	}

	var missing []string
	for _, param := range spec.required {
		switch param {
		case "id":
			if int64(coerceInt(m["id"], 0)) <= 0 {
				missing = append(missing, "id (integer)")
			}
		case "status":
			if coerceString(m["status"], "") == "" {
				missing = append(missing, `status="resolved"`)
			}
		default:
			if coerceString(m[param], "") == "" {
				missing = append(missing, param)
			}
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf(
			"issues %q: missing required param(s): %s.\nFull signature: issues(%s)",
			action, strings.Join(missing, ", "), spec.full,
		)
	}
	return nil
}

// handleIssues dispatches issue actions: create, list, get, update, comment, reopen, close.
func (s *Server) handleIssues(ctx context.Context, args json.RawMessage) (string, error) {
	if s.issueStore == nil {
		return "", fmt.Errorf("issue store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", fmt.Errorf("issues: %w", err)
	}

	action := coerceString(m["action"], "list")

	// Per-action required parameter validation with helpful error messages.
	// Returns the full required list for the action when any param is missing.
	if err := validateIssueActionParams(action, m); err != nil {
		return "", err
	}

	switch action {
	case "create":
		return s.handleIssueCreate(ctx, m)
	case "list":
		return s.handleIssueList(ctx, m)
	case "get":
		return s.handleIssueGet(ctx, m)
	case "update":
		return s.handleIssueUpdate(ctx, m)
	case "comment":
		return s.handleIssueComment(ctx, m)
	case "reopen":
		return s.handleIssueReopen(ctx, m)
	case "close":
		return s.handleIssueClose(ctx, m)
	default:
		return "", fmt.Errorf("unknown issues action: %q (valid: create, list, get, update, comment, reopen, close)", action)
	}
}

func (s *Server) handleIssueCreate(ctx context.Context, m map[string]any) (string, error) {
	title := coerceString(m["title"], "")
	if title == "" {
		return "", fmt.Errorf("title is required for issues create")
	}

	body := coerceString(m["body"], "")
	priority := coerceString(m["priority"], "medium")
	targetProject := coerceString(m["target_project"], "")
	labels := coerceStringSlice(m["labels"])

	// Auto-fill from session context
	sourceProject := coerceString(m["project"], "")
	sourceAgent := coerceString(m["agent_source"], "claude-code")

	if targetProject == "" {
		targetProject = sourceProject
	}
	if targetProject == "" {
		return "", fmt.Errorf("target_project is required (or set project for current project)")
	}

	issue := &gormdb.Issue{
		Title:         title,
		Body:          body,
		Priority:      priority,
		SourceProject: sourceProject,
		TargetProject: targetProject,
		SourceAgent:   sourceAgent,
		Labels:        labels,
	}

	id, err := s.issueStore.CreateIssue(ctx, issue)
	if err != nil {
		return "", fmt.Errorf("create issue: %w", err)
	}

	return fmt.Sprintf("Issue #%d created: %s\nTarget: %s | Priority: %s | From: %s", id, title, targetProject, priority, sourceProject), nil
}

func (s *Server) handleIssueList(ctx context.Context, m map[string]any) (string, error) {
	project := coerceString(m["project"], "")
	sourceProject := coerceString(m["source_project"], "")
	statusParam := coerceString(m["status"], "open,reopened")
	limit := coerceInt(m["limit"], 20)
	resolvedSinceMs := int64(coerceInt(m["resolved_since"], 0))

	var statuses []string
	for _, s := range strings.Split(statusParam, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			statuses = append(statuses, s)
		}
	}

	params := gormdb.IssueListParams{
		TargetProject: project,
		SourceProject: sourceProject,
		Statuses:      statuses,
		Limit:         limit,
	}
	if resolvedSinceMs > 0 {
		t := time.Unix(0, resolvedSinceMs*int64(time.Millisecond))
		params.ResolvedSince = &t
	}

	issues, total, err := s.issueStore.ListIssuesEx(ctx, params)
	if err != nil {
		return "", fmt.Errorf("list issues: %w", err)
	}

	if len(issues) == 0 {
		if project != "" {
			return fmt.Sprintf("No issues found for project %q with status %s.", project, statusParam), nil
		}
		return fmt.Sprintf("No issues found with status %s.", statusParam), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Issues (%d of %d):\n\n", len(issues), total))

	for _, issue := range issues {
		status := issue.Status
		comments := ""
		if issue.CommentCount > 0 {
			comments = fmt.Sprintf(" · %d comments", issue.CommentCount)
		}
		sb.WriteString(fmt.Sprintf("#%d [%s] [%s] %s\n  %s → %s%s\n\n",
			issue.ID, strings.ToUpper(issue.Priority), status,
			issue.Title, issue.SourceProject, issue.TargetProject, comments))
	}

	return sb.String(), nil
}

func (s *Server) handleIssueGet(ctx context.Context, m map[string]any) (string, error) {
	id := int64(coerceInt(m["id"], 0))
	if id <= 0 {
		return "", fmt.Errorf("id is required for issues get")
	}

	issue, comments, err := s.issueStore.GetIssue(ctx, id)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Issue #%d: %s\n", issue.ID, issue.Title))
	sb.WriteString(fmt.Sprintf("Status: %s | Priority: %s\n", issue.Status, issue.Priority))
	sb.WriteString(fmt.Sprintf("From: %s → %s\n", issue.SourceProject, issue.TargetProject))
	sb.WriteString(fmt.Sprintf("Created: %s\n\n", issue.CreatedAt.Format("2006-01-02 15:04")))

	if issue.Body != "" {
		sb.WriteString(issue.Body)
		sb.WriteString("\n\n")
	}

	if len(comments) > 0 {
		sb.WriteString(fmt.Sprintf("--- Comments (%d) ---\n\n", len(comments)))
		for _, c := range comments {
			sb.WriteString(fmt.Sprintf("[%s] %s (%s):\n%s\n\n",
				c.CreatedAt.Format("2006-01-02 15:04"), c.AuthorProject, c.AuthorAgent, c.Body))
		}
	}

	return sb.String(), nil
}

func (s *Server) handleIssueUpdate(ctx context.Context, m map[string]any) (string, error) {
	id := int64(coerceInt(m["id"], 0))
	if id <= 0 {
		return "", fmt.Errorf("id is required for issues update")
	}

	status := coerceString(m["status"], "")
	comment := coerceString(m["comment"], "")

	if status != "" {
		if status != "resolved" {
			return "", fmt.Errorf("status can only be set to 'resolved' via update (use reopen action to reopen)")
		}
		if err := s.issueStore.UpdateIssueStatus(ctx, id, status); err != nil {
			return "", err
		}
	}

	if comment != "" {
		sourceProject := coerceString(m["project"], "")
		sourceAgent := coerceString(m["agent_source"], "claude-code")
		_, err := s.issueStore.AddComment(ctx, id, &gormdb.IssueComment{
			AuthorProject: sourceProject,
			AuthorAgent:   sourceAgent,
			Body:          comment,
		})
		if err != nil {
			return "", err
		}
	}

	action := "updated"
	if status == "resolved" {
		action = "resolved"
	}
	return fmt.Sprintf("Issue #%d %s.", id, action), nil
}

func (s *Server) handleIssueComment(ctx context.Context, m map[string]any) (string, error) {
	id := int64(coerceInt(m["id"], 0))
	if id <= 0 {
		return "", fmt.Errorf("id is required for issues comment")
	}

	body := coerceString(m["body"], "")
	if body == "" {
		return "", fmt.Errorf("body is required for issues comment")
	}

	sourceProject := coerceString(m["project"], "")
	sourceAgent := coerceString(m["agent_source"], "claude-code")

	commentID, err := s.issueStore.AddComment(ctx, id, &gormdb.IssueComment{
		AuthorProject: sourceProject,
		AuthorAgent:   sourceAgent,
		Body:          body,
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Comment added to issue #%d (comment id: %d).", id, commentID), nil
}

func (s *Server) handleIssueReopen(ctx context.Context, m map[string]any) (string, error) {
	id := int64(coerceInt(m["id"], 0))
	if id <= 0 {
		return "", fmt.Errorf("id is required for issues reopen")
	}

	comment := coerceString(m["comment"], "")
	sourceProject := coerceString(m["project"], "")
	sourceAgent := coerceString(m["agent_source"], "claude-code")

	if err := s.issueStore.ReopenIssue(ctx, id, comment, sourceProject, sourceAgent); err != nil {
		return "", err
	}

	return fmt.Sprintf("Issue #%d reopened.", id), nil
}

func (s *Server) handleIssueClose(ctx context.Context, m map[string]any) (string, error) {
	id := int64(coerceInt(m["id"], 0))
	if id <= 0 {
		return "", fmt.Errorf("id is required for issues close")
	}

	sourceProject := coerceString(m["project"], "")

	if err := s.issueStore.CloseIssue(ctx, id, sourceProject); err != nil {
		return "", err
	}

	return fmt.Sprintf("Issue #%d closed. The issue will no longer appear in any session injection.", id), nil
}
