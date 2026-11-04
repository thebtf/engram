package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/projectidentity"
)

// issuesToolSchema returns the flat JSON Schema for the issues tool.
// Anthropic API does NOT support oneOf/allOf/anyOf at the top level of input_schema,
// so we use a flat schema with per-action requirements encoded in each property's
// description (via REQUIRED_FOR: prefix) and enforced server-side by
// validateIssueActionParams() with clear error messages showing the full signature.
func issuesToolSchema() map[string]any {
	return map[string]any{
		"type":                    "object",
		"required":                []string{"action"},
		"x-engram-caller-context": "issues/v1",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"create", "list", "get", "update", "comment", "reopen", "close"},
				"description": "Action to perform. Authenticated read-write participants may resolve, close, or reopen resolved/closed issues; resolved awaits verification and closed records the actor's checked-completion claim, not independently verified evidence. list/get are project-free authorized reads and remain available during a project migration hold. Mutations require the native caller's canonical identity; changing a project argument cannot establish it. PROJECT_MIGRATION_REQUIRED: " + projectidentity.RecoveryGuidance("PROJECT_MIGRATION_REQUIRED"),
			},
			"project": map[string]any{
				"type":        "string",
				"description": "Optional compatibility argument on mutations; it never establishes actor identity, which comes from the native caller's verified canonical project. For list: optional target-project filter; get needs only id. Never change this argument to bypass an identity hold.",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "REQUIRED_FOR: create. Short issue title.",
			},
			"target_project": map[string]any{
				"type":        "string",
				"description": "REQUIRED_FOR: create. Which project the issue is FOR (where it will be injected at session start).",
			},
			"target_session": map[string]any{
				"type":        []string{"string", "null"},
				"description": "OPTIONAL_FOR: create|update|list. Explicit recipient session reference, not an assignee, permission or liveness claim. Create defaults unknown. Update omission preserves; empty/null clears and requires creator/operator field authority. List exact-matches stored recipient and composes with other filters.",
			},
			"source_session": map[string]any{
				"type":        "string",
				"description": "OPTIONAL_FOR: list. Exact creator session filter on created_by_session only; not comments or lifecycle authors. Read selection only; no implicit project or session default.",
			},
			"participant_session": map[string]any{
				"type":        "string",
				"description": "OPTIONAL_FOR: list. Exact full session ID matching the creator OR a comment author OR a lifecycle-event author. ANDed with project, source_session, target_session and other filters before count/pagination. Never permission, identity, ownership or liveness.",
			},
			"history_limit": map[string]any{
				"type": "integer", "minimum": 1, "maximum": gormdb.IssueHistoryMaxLimit,
				"description": "OPTIONAL_FOR: get. Lifecycle event page size, default 50, maximum 100. Readback includes history_has_more and history_next_after_id; comments are separate.",
			},
			"history_after_id": map[string]any{
				"type": "integer", "minimum": 0,
				"description": "OPTIONAL_FOR: get. Exclusive event ID cursor, default 0. Continue with history_next_after_id when history_has_more=true. Each page reuses issue read authorization; no stable snapshot is implied.",
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
				"description": "For list: comma-separated statuses such as open,acknowledged,reopened,resolved,closed,rejected; empty selects all. For update: only resolved is allowed, and status may be omitted only when target_session is supplied. Use close/reopen for other transitions.",
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
			"type": map[string]any{
				"type":        "string",
				"enum":        []string{"bug", "feature", "improvement", "task"},
				"default":     "task",
				"description": "OPTIONAL_FOR: create. Issue type. Default: task.",
			},
			"source_project": map[string]any{
				"type":        "string",
				"description": "OPTIONAL_FOR: list. Filter to show only issues YOU created.",
			},
			"source_agent": map[string]any{
				"type":        "string",
				"description": "OPTIONAL_FOR: list. Exact actor label filter, independent of source_project.",
			},
			"agent_source": map[string]any{
				"type":        "string",
				"description": "OPTIONAL_FOR: create|update|comment|reopen|close. Actual author agent label when known. Omitted stays unknown; separate from client and session.",
			},
			"session_id": map[string]any{
				"type":        "string",
				"description": "OPTIONAL_FOR: create|update|comment|reopen|close. Caller-reported host conversation ID, including UUIDs or opaque IDs (maximum 256 UTF-8 bytes, no controls or surrounding whitespace). Safe request-transport attribution takes precedence. Nil UUID/unsafe values stay unknown without failing the write. Provenance only; never project, lane, keycard, browser/auth session or connection tag.",
			},
			"client": map[string]any{
				"type":        "string",
				"description": "OPTIONAL_FOR: create|update|comment|reopen|close. Actual public per-call client label, independent of agent_source. Stored labels: omp, openclaw, claude-code, codex, cursor, gemini-cli, human. Missing/unrecognized labels stay unknown without failing the write; never copied from auth context.",
			},
			"comment": map[string]any{
				"type":        "string",
				"description": "OPTIONAL_FOR: update|reopen|close. User-authored comment when changing status. Lifecycle actor history is recorded even without a comment.",
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
	"create":  {required: []string{"title", "target_project"}, full: "action, title, target_project"},
	"list":    {required: []string{}, full: "action  [optional: project, source_project, source_agent, source_session, target_session, participant_session, status, resolved_since, limit]"},
	"get":     {required: []string{"id"}, full: "action, id  [optional: history_limit, history_after_id]"},
	"update":  {required: []string{"id"}, full: "action, id, status=resolved OR target_session"},
	"comment": {required: []string{"id", "body"}, full: "action, id, body"},
	"reopen":  {required: []string{"id"}, full: "action, id"},
	"close":   {required: []string{"id"}, full: "action, id"},
}

// validateIssueActionParams checks that all required params for the given action are present.
// Returns an error listing ALL missing params and the full required signature, so the caller
// can see the complete picture in one error.
func validateIssueActionParams(action string, m map[string]any) error {
	spec, ok := actionRequirements[action]
	if !ok {
		return fmt.Errorf("unknown issues action: %q (valid: create, list, get, update, comment, reopen, close)", action)
	}
	if action != "list" && action != "get" {
		if _, _, err := optionalStringArg(m, "project"); err != nil {
			return fmt.Errorf("issues %q: %w", action, err)
		}
	}

	var missing []string
	for _, param := range spec.required {
		switch param {
		case "id":
			id, err := requireInt64Arg(m, "id")
			if err != nil || id <= 0 {
				missing = append(missing, "id (integer)")
			}
		default:
			value, present, err := optionalStringArg(m, param)
			if err != nil {
				return fmt.Errorf("issues %q: %w", action, err)
			}
			if !present || value == "" {
				if param == "status" {
					missing = append(missing, `status="resolved"`)
				} else {
					missing = append(missing, param)
				}
			}
		}
	}

	if action == "update" {
		if _, targetPresent := m["target_session"]; !targetPresent {
			status, present, err := optionalStringArg(m, "status")
			if err != nil {
				return fmt.Errorf("issues %q: %w", action, err)
			}
			if !present || status == "" {
				missing = append(missing, `status="resolved" or target_session`)
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

// issueSourceProject returns transport-derived attribution only. It is never
// used to authorize an issue mutation.
func (s *Server) issueSourceProject(ctx context.Context) (string, error) {
	if ProjectFromContext(ctx) == "" {
		return "", &projectidentity.LogicalError{Code: "PROJECT_CONTEXT_REQUIRED"}
	}
	return s.issueStore.ResolveProject(ctx, ProjectFromContext(ctx))
}

// issueMutationActor returns a trusted read-write keycard or non-client
// operator. Project and agent request fields are attribution only.
func issueMutationActor(ctx context.Context) (keycardID string, isOperator bool, err error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return "", false, fmt.Errorf("issue mutation forbidden: authenticated identity is required")
	}
	if id.IsAdmin() && id.Source != auth.SourceClient {
		return "", true, nil
	}
	if id.Source != auth.SourceClient || id.KeycardID == "" || id.Role != auth.RoleReadWrite {
		return "", false, fmt.Errorf("issue mutation forbidden: authenticated read-write client keycard is required")
	}
	return id.KeycardID, false, nil
}

func (s *Server) authorizeIssueProgression(ctx context.Context) (bool, error) {
	keycardID, isOperator, err := issueMutationActor(ctx)
	if err != nil {
		return false, err
	}
	if err := s.issueStore.AuthorizeIssueProgressionMutation(ctx, keycardID, isOperator); err != nil {
		return false, err
	}
	return isOperator, nil
}

func (s *Server) handleIssueCreate(ctx context.Context, m map[string]any) (string, error) {
	title, present, err := optionalStringArg(m, "title")
	if err != nil {
		return "", err
	}
	if !present || title == "" {
		return "", fmt.Errorf("title is required for issues create")
	}
	body, _, err := optionalStringArg(m, "body")
	if err != nil {
		return "", err
	}
	priority, present, err := optionalStringArg(m, "priority")
	if err != nil {
		return "", err
	}
	if !present {
		priority = "medium"
	}
	issueType, present, err := optionalStringArg(m, "type")
	if err != nil {
		return "", err
	}
	if !present {
		issueType = "task"
	}
	targetProject, _, err := optionalStringArg(m, "target_project")
	if err != nil {
		return "", err
	}
	labels, _, err := optionalStringSliceArg(m, "labels")
	if err != nil {
		return "", err
	}
	sourceAgent, _, err := optionalStringArg(m, "agent_source")
	if err != nil {
		return "", err
	}
	attribution, err := issueCallAttribution(ctx, m)
	if err != nil {
		return "", err
	}
	if targetProject != "" {
		targetProject, err = s.issueStore.ResolveProject(ctx, targetProject)
		if err != nil {
			return "", err
		}
	}
	keycardID, isOperator, err := issueMutationActor(ctx)
	if err != nil {
		return "", err
	}
	sourceProject, err := s.issueSourceProject(ctx)
	if err != nil {
		return "", err
	}
	if isOperator {
		keycardID = ""
	}
	if targetProject == "" {
		targetProject = sourceProject
	}
	if targetProject == "" {
		return "", fmt.Errorf("target_project is required (or set project for current project)")
	}

	targetSession, err := issueTargetSessionArg(m)
	if err != nil {
		return "", err
	}
	issue := &gormdb.Issue{
		Title:                  title,
		Body:                   body,
		Priority:               priority,
		Type:                   issueType,
		SourceProject:          sourceProject,
		TargetProject:          targetProject,
		SourceAgent:            sourceAgent,
		CreatedBySession:       attribution.SessionID,
		CreatedBySessionSource: attribution.SessionSource,
		SourceClient:           attribution.Client,
		TargetSession:          issueTargetSessionValue(targetSession),
		CreatorKeycardID:       keycardID,
		Labels:                 labels,
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
	sourceAgent, _, err := optionalStringArg(m, "source_agent")
	if err != nil {
		return "", err
	}
	sourceSession, err := issueSessionSelectorArg(m, "source_session")
	if err != nil {
		return "", err
	}
	targetSession, err := issueSessionSelectorArg(m, "target_session")
	if err != nil {
		return "", err
	}
	participantSession, err := issueSessionSelectorArg(m, "participant_session")
	if err != nil {
		return "", err
	}
	statusParam := coerceString(m["status"], "open,reopened")
	limit, err := parseIssueListLimit(m)
	if err != nil {
		return "", err
	}
	resolvedSinceMs, err := parseIssueResolvedSince(m)
	if err != nil {
		return "", err
	}
	var statuses []string
	for _, s := range strings.Split(statusParam, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			statuses = append(statuses, s)
		}
	}

	params := gormdb.IssueListParams{
		TargetProject:      project,
		SourceProject:      sourceProject,
		SourceAgent:        sourceAgent,
		SourceSession:      sourceSession,
		TargetSession:      targetSession,
		ParticipantSession: participantSession,
		Statuses:           statuses,
		Limit:              limit,
	}
	if resolvedSinceMs > 0 {
		t := time.UnixMilli(resolvedSinceMs)
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
		sb.WriteString(fmt.Sprintf("#%d [%s] [%s] %s\n  %s → %s%s\n  source_session=%q target_session=%q (reported references only)\n\n",
			issue.ID, strings.ToUpper(issue.Priority), status,
			issue.Title, issue.SourceProject, issue.TargetProject, comments, issue.CreatedBySession, issue.TargetSession))
	}

	return sb.String(), nil
}

const issueListMaxLimit = 100

func parseIssueListLimit(m map[string]any) (int, error) {
	limit, present, err := optionalInt64Arg(m, "limit")
	if !present {
		return 20, nil
	}
	if err != nil || limit < 1 || limit > issueListMaxLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", issueListMaxLimit)
	}
	return int(limit), nil
}

func parseIssueResolvedSince(m map[string]any) (int64, error) {
	resolvedSince, present, err := optionalInt64Arg(m, "resolved_since")
	if !present {
		return 0, nil
	}
	if err != nil || resolvedSince < 1 {
		return 0, fmt.Errorf("resolved_since must be a positive in-range integer")
	}
	return resolvedSince, nil
}

func (s *Server) handleIssueGet(ctx context.Context, m map[string]any) (string, error) {
	id, err := requireInt64Arg(m, "id")
	if err != nil || id <= 0 {
		return "", fmt.Errorf("id is required for issues get")
	}

	historyLimit, historyAfterID, err := parseIssueHistoryParams(m)
	if err != nil {
		return "", err
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
	if issue.CreatedBySession != "" || issue.SourceClient != "" || issue.SourceAgent != "" {
		sb.WriteString(fmt.Sprintf("Creator: agent=%q client=%q host_session=%q session_source=%q (provenance only)\n\n",
			issue.SourceAgent, issue.SourceClient, issue.CreatedBySession, issue.CreatedBySessionSource))
	}
	if issue.TargetSession != "" {
		sb.WriteString(fmt.Sprintf("Recipient session: %q (reported reference only; no authority or liveness)\n\n", issue.TargetSession))
	}

	if issue.Body != "" {
		sb.WriteString(issue.Body)
		sb.WriteString("\n\n")
	}

	if len(comments) > 0 {
		sb.WriteString(fmt.Sprintf("--- Comments (%d) ---\n\n", len(comments)))
		for _, c := range comments {
			sb.WriteString(fmt.Sprintf("[%s] %s (%s), client=%q host_session=%q session_source=%q (provenance only):\n%s\n\n",
				c.CreatedAt.Format("2006-01-02 15:04"), c.AuthorProject, c.AuthorAgent, c.AuthorClient, c.AuthorSession, c.AuthorSessionSource, c.Body))
		}
	}

	page, err := s.issueStore.GetIssueHistoryPage(ctx, id, historyLimit, historyAfterID)
	if err != nil {
		return "", err
	}
	if len(page.Events) > 0 {
		sb.WriteString("--- Lifecycle history (actor claims, not independent verification) ---\n\n")
		for _, event := range page.Events {
			sb.WriteString(fmt.Sprintf("[%s] event_id=%d status=%s author_project=%q agent=%q client=%q host_session=%q session_source=%q (provenance only)\n",
				event.CreatedAt.Format("2006-01-02 15:04"), event.ID, event.Status, event.AuthorProject, event.AuthorAgent, event.Client, event.SessionID, event.SessionSource))
		}
	}
	sb.WriteString(fmt.Sprintf("history_limit: %d\nhistory_has_more: %t\n", historyLimit, page.HasMore))
	if page.NextAfterID == nil {
		sb.WriteString("history_next_after_id: null\n")
	} else {
		sb.WriteString(fmt.Sprintf("history_next_after_id: %d\n", *page.NextAfterID))
		sb.WriteString(fmt.Sprintf("Continue: issues(action=\"get\", id=%d, history_limit=%d, history_after_id=%d)\n", id, historyLimit, *page.NextAfterID))
	}
	return sb.String(), nil
}

func (s *Server) handleIssueUpdate(ctx context.Context, m map[string]any) (string, error) {
	id, err := requireInt64Arg(m, "id")
	if err != nil || id <= 0 {
		return "", fmt.Errorf("id is required for issues update")
	}
	status, present, err := optionalStringArg(m, "status")
	if err != nil {
		return "", err
	}
	if present && status != "resolved" {
		return "", fmt.Errorf("status can only be set to 'resolved' via update (use close/reopen actions for other transitions)")
	}
	targetSession, err := issueTargetSessionArg(m)
	if err != nil {
		return "", err
	}
	if !present && targetSession == nil {
		return "", fmt.Errorf("status=resolved or target_session is required for issues update")
	}
	comment, _, err := optionalStringArg(m, "comment")
	if err != nil {
		return "", err
	}
	sourceAgent, _, err := optionalStringArg(m, "agent_source")
	if err != nil {
		return "", err
	}
	attribution, err := issueCallAttribution(ctx, m)
	if err != nil {
		return "", err
	}
	keycardID, isOperator, err := issueMutationActor(ctx)
	if err != nil {
		return "", err
	}
	if targetSession != nil {
		err = s.issueStore.AuthorizeIssueSourceMutation(ctx, id, keycardID, isOperator)
	} else {
		err = s.issueStore.AuthorizeIssueProgressionMutation(ctx, keycardID, isOperator)
	}
	if err != nil {
		return "", err
	}
	sourceProject, err := s.issueSourceProject(ctx)
	if err != nil {
		return "", err
	}

	if err := s.issueStore.UpdateIssueAtomically(ctx, id, gormdb.IssueUpdate{
		Status: status, Comment: comment, AuthorProject: sourceProject, AuthorAgent: sourceAgent,
		Attribution: attribution, TargetSession: targetSession,
	}, isOperator); err != nil {
		return "", err
	}

	if status == "resolved" {
		return fmt.Sprintf("Issue #%d resolved.", id), nil
	}
	return fmt.Sprintf("Issue #%d recipient session updated.", id), nil
}

func (s *Server) handleIssueComment(ctx context.Context, m map[string]any) (string, error) {
	id, err := requireInt64Arg(m, "id")
	if err != nil || id <= 0 {
		return "", fmt.Errorf("id is required for issues comment")
	}
	body, present, err := optionalStringArg(m, "body")
	if err != nil {
		return "", err
	}
	if !present || body == "" {
		return "", fmt.Errorf("body is required for issues comment")
	}
	sourceAgent, _, err := optionalStringArg(m, "agent_source")
	if err != nil {
		return "", err
	}
	attribution, err := issueCallAttribution(ctx, m)
	if err != nil {
		return "", err
	}
	if _, err := s.authorizeIssueProgression(ctx); err != nil {
		return "", err
	}
	sourceProject, err := s.issueSourceProject(ctx)
	if err != nil {
		return "", err
	}

	commentID, err := s.issueStore.AddComment(ctx, id, &gormdb.IssueComment{
		AuthorProject:       sourceProject,
		AuthorAgent:         sourceAgent,
		AuthorSession:       attribution.SessionID,
		AuthorSessionSource: attribution.SessionSource,
		AuthorClient:        attribution.Client,
		Body:                body,
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Comment added to issue #%d (comment id: %d).", id, commentID), nil
}

func (s *Server) handleIssueReopen(ctx context.Context, m map[string]any) (string, error) {
	id, err := requireInt64Arg(m, "id")
	if err != nil || id <= 0 {
		return "", fmt.Errorf("id is required for issues reopen")
	}
	comment, _, err := optionalStringArg(m, "comment")
	if err != nil {
		return "", err
	}
	sourceAgent, _, err := optionalStringArg(m, "agent_source")
	if err != nil {
		return "", err
	}
	attribution, err := issueCallAttribution(ctx, m)
	if err != nil {
		return "", err
	}
	if _, err := s.authorizeIssueProgression(ctx); err != nil {
		return "", err
	}
	sourceProject, err := s.issueSourceProject(ctx)
	if err != nil {
		return "", err
	}

	if err := s.issueStore.UpdateIssueAtomically(ctx, id, gormdb.IssueUpdate{
		Status: "reopened", Comment: comment, AuthorProject: sourceProject, AuthorAgent: sourceAgent, Attribution: attribution,
	}, false); err != nil {
		return "", err
	}

	return fmt.Sprintf("Issue #%d reopened.", id), nil
}

func (s *Server) handleIssueClose(ctx context.Context, m map[string]any) (string, error) {
	id, err := requireInt64Arg(m, "id")
	if err != nil || id <= 0 {
		return "", fmt.Errorf("id is required for issues close")
	}

	isOperator, err := s.authorizeIssueProgression(ctx)
	if err != nil {
		return "", err
	}
	sourceProject, err := s.issueSourceProject(ctx)
	if err != nil {
		return "", err
	}
	sourceAgent, _, err := optionalStringArg(m, "agent_source")
	if err != nil {
		return "", err
	}
	comment, _, err := optionalStringArg(m, "comment")
	if err != nil {
		return "", err
	}
	attribution, err := issueCallAttribution(ctx, m)
	if err != nil {
		return "", err
	}
	if err := s.issueStore.UpdateIssueAtomically(ctx, id, gormdb.IssueUpdate{
		Status: "closed", Comment: comment, AuthorProject: sourceProject, AuthorAgent: sourceAgent, Attribution: attribution,
	}, isOperator); err != nil {
		return "", err
	}

	return fmt.Sprintf("Issue #%d closed. The issue will no longer appear in any session injection.", id), nil
}

func issueCallAttribution(ctx context.Context, args map[string]any) (gormdb.IssueAttribution, error) {
	var session, client string
	var err error
	if args["session_id"] != nil {
		session, _, err = optionalStringArg(args, "session_id")
		if err != nil {
			return gormdb.IssueAttribution{}, err
		}
	}
	if args["client"] != nil {
		client, _, err = optionalStringArg(args, "client")
		if err != nil {
			return gormdb.IssueAttribution{}, err
		}
	}
	return gormdb.NewIssueAttribution(sessionFromContext(ctx), session, client)
}

func issueTargetSessionArg(args map[string]any) (*string, error) {
	raw, present := args["target_session"]
	if !present {
		return nil, nil
	}
	value := ""
	if raw != nil {
		var ok bool
		value, ok = raw.(string)
		if !ok {
			return nil, fmt.Errorf("target_session must be a string or null")
		}
	}
	if err := gormdb.ValidateIssueSessionSelector(value); err != nil {
		return nil, err
	}
	return &value, nil
}

func issueTargetSessionValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func issueSessionSelectorArg(args map[string]any, key string) (string, error) {
	value, present, err := optionalStringArg(args, key)
	if err != nil {
		return "", err
	}
	if present && (value == "" || gormdb.ValidateIssueSessionSelector(value) != nil) {
		return "", fmt.Errorf("%s must be a nonempty bounded session identifier", key)
	}
	return value, nil
}

func parseIssueHistoryParams(args map[string]any) (int, int64, error) {
	limit, present, err := optionalInt64Arg(args, "history_limit")
	if !present {
		limit = gormdb.IssueHistoryDefaultLimit
	}
	if err != nil || limit < 1 || limit > gormdb.IssueHistoryMaxLimit {
		return 0, 0, fmt.Errorf("history_limit must be an integer between 1 and %d", gormdb.IssueHistoryMaxLimit)
	}
	after, _, err := optionalInt64Arg(args, "history_after_id")
	if err != nil || after < 0 {
		return 0, 0, fmt.Errorf("history_after_id must be a nonnegative integer")
	}
	return int(limit), after, nil
}
