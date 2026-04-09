package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// handleIssues dispatches issue actions: create, list, get, update, comment, reopen.
func (s *Server) handleIssues(ctx context.Context, args json.RawMessage) (string, error) {
	if s.issueStore == nil {
		return "", fmt.Errorf("issue store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", fmt.Errorf("issues: %w", err)
	}

	action := coerceString(m["action"], "list")

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
	default:
		return "", fmt.Errorf("unknown issues action: %q (valid: create, list, get, update, comment, reopen)", action)
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
	statusParam := coerceString(m["status"], "open,reopened")
	limit := coerceInt(m["limit"], 20)

	var statuses []string
	for _, s := range strings.Split(statusParam, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			statuses = append(statuses, s)
		}
	}

	issues, total, err := s.issueStore.ListIssues(ctx, project, statuses, limit, 0)
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
