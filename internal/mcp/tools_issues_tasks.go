package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/issues"
	issuecontract "github.com/thebtf/engram/internal/issues/contract"
	"github.com/thebtf/engram/internal/projectidentity"
)

const nativeIssueEnvelopeBytes = 16 << 20

func nativeIssueArgument(field string) error {
	failure := issues.Failure("INVALID_ARGUMENT")
	failure.Message = "Invalid " + field + "; correct the field before retrying."
	return failure
}

func validateNativeIssueFields(m map[string]any) error {
	stringsFields := []string{"action", "format", "project", "source_project", "source_agent", "source_session", "participant_session", "agent_source", "created_by_session", "session_id", "client", "title", "body", "priority", "type", "status", "comment", "request_key", "expected_revision", "detail_mode", "q"}
	otherFields := []string{"target_project", "target_session", "metadata", "comment_metadata", "labels", "owner_label", "recipient_label", "id", "limit", "offset", "comments_limit", "comments_offset", "history_limit", "history_offset", "history_after_id", "expected_actor", "resolved_since"}
	for key := range m {
		for _, canonical := range stringsFields {
			if key != canonical && strings.EqualFold(key, canonical) {
				return nativeIssueArgument(canonical)
			}
		}
		for _, canonical := range otherFields {
			if key != canonical && strings.EqualFold(key, canonical) {
				return nativeIssueArgument(canonical)
			}
		}
	}
	for _, field := range stringsFields {
		if _, _, err := optionalStringArg(m, field); err != nil {
			return nativeIssueArgument(field)
		}
	}
	if _, _, err := optionalStringArg(m, "target_project"); err != nil {
		return nativeIssueArgument("target_project")
	}
	if value, present := m["target_session"]; present {
		if value == nil {
			if m["action"] != "update" {
				return nativeIssueArgument("target_session")
			}
		} else if _, ok := value.(string); !ok {
			return nativeIssueArgument("target_session")
		}
	}
	if _, _, err := optionalStringSliceArg(m, "labels"); err != nil {
		if values, ok := m["labels"].([]any); ok {
			for index, value := range values {
				if _, ok := value.(string); !ok {
					return nativeIssueArgument(fmt.Sprintf("labels[%d]", index))
				}
			}
		}
		return nativeIssueArgument("labels")
	}
	return nil
}

func (s *Server) executeNativeIssues(ctx context.Context, raw json.RawMessage) (string, error) {
	if len(raw) > nativeIssueEnvelopeBytes {
		return "", issues.Failure("PAYLOAD_TOO_LARGE")
	}
	members, err := issues.DecodeReceivedObject(raw)
	if err != nil {
		return "", err
	}
	m, err := parseArgs(raw)
	if err != nil {
		return "", issues.Failure("INVALID_ARGUMENT")
	}
	if err := validateNativeIssueFields(m); err != nil {
		return "", err
	}
	action := "list"
	if _, present := members["action"]; present {
		if err := json.Unmarshal(members["action"], &action); err != nil {
			return "", nativeIssueArgument("action")
		}
	}
	if err := validateIssueActionParams(action, m); err != nil {
		return "", nativeIssueArgument("action requirements")
	}
	format := "text"
	if encoded, present := members["format"]; present {
		if json.Unmarshal(encoded, &format) != nil || (format != "text" && format != "json") {
			return "", nativeIssueArgument("format")
		}
	}
	// Pin the existing authenticated value before source admission. The common
	// application receives this same value, never a re-sampled outer context.
	identity, present := auth.IdentityFrom(ctx)
	if !present {
		return "", issues.Failure("UNAUTHENTICATED")
	}
	ctx = auth.WithIdentity(ctx, identity)
	actor, err := issues.CaptureActor(ctx)
	if err != nil {
		return "", err
	}
	if s.issueStore == nil {
		return "", issues.Failure("UNAVAILABLE")
	}
	if action == "list" {
		return s.listNativeIssueTasks(ctx, m, format)
	}
	if action == "get" {
		return s.getNativeIssueTask(ctx, m, format)
	}
	if !actor.CanWrite() {
		return "", issues.Failure("FORBIDDEN")
	}
	if action == "create" {
		if target, ok := m["target_project"].(string); ok && target != "" && !projectidentity.ValidProjectID(target) {
			if _, resolveErr := s.issueStore.ResolveProject(ctx, target); resolveErr != nil {
				if code := issueAdoptionRefusal(resolveErr); code != "" {
					return "", &issueRecoveryError{code: code, err: fmt.Errorf("target_project resolution failed; use an independently confirmed, authorized existing canonical target UUID, or resolve the exact verified legacy ID with authenticated GET /api/projects/resolve?legacy_id=<URI-encoded-exact-id>. Do not select by name or adopt unresolved membership: %w", resolveErr)}
				}
			}
			return "", nativeIssueArgument("target_project; use an independently confirmed authorized canonical project UUID")
		}
	}
	source := ProjectFromContext(ctx)
	if source == "" {
		return "", &projectidentity.LogicalError{Code: "PROJECT_CONTEXT_REQUIRED"}
	}
	if !projectidentity.ValidProjectID(source) {
		if _, resolveErr := s.issueStore.ResolveProject(ctx, source); resolveErr != nil {
			if code := issueAdoptionRefusal(resolveErr); code != "" {
				return "", &issueRecoveryError{code: code, err: fmt.Errorf("source_project (native caller) resolution failed; use project_context(action=\"repair\") and, if needed, project_context(action=\"choices\"). Follow its marker and caller-root hash preconditions; do not guess or adopt unresolved membership: %w", resolveErr)}
			}
		}
		return "", &projectidentity.LogicalError{Code: "PROJECT_IDENTITY_INVALID"}
	}
	// The submitted source project cannot select identity or override transport source.
	delete(members, "source_project")
	ctx = issuecontract.WithTransportSession(ctx, sessionFromContext(ctx))
	if legacy, exists := members["agent_source"]; exists {
		if _, both := members["source_agent"]; both {
			return "", nativeIssueArgument("source_agent")
		}
		members["source_agent"] = legacy
	}
	for _, field := range []string{"action", "format", "project", "agent_source", "id"} {
		delete(members, field)
	}
	application := issues.New(s.issueStore, s.redactionRules)
	var result issues.Result
	if action == "create" {
		mapped, err := issues.EncodeReceivedObject(members)
		if err != nil {
			return "", issues.Failure("INVALID_ARGUMENT")
		}
		result, err = application.Create(ctx, issues.Scope{SourceProject: source}, mapped)
		if err != nil {
			return "", err
		}
	} else {
		id, err := requireInt64Arg(m, "id")
		if err != nil || id < 1 {
			return "", nativeIssueArgument("id")
		}
		switch action {
		case "comment":
			for _, field := range []string{"title", "priority", "type", "status", "labels", "owner_label", "recipient_label", "target_session"} {
				if _, present := members[field]; present {
					return "", nativeIssueArgument(field)
				}
			}
			if _, present := members["comment"]; present {
				return "", nativeIssueArgument("comment")
			}
			members["comment"] = members["body"]
			delete(members, "body")
			if metadata, present := members["metadata"]; present {
				if _, both := members["comment_metadata"]; both {
					return "", nativeIssueArgument("comment_metadata")
				}
				members["comment_metadata"] = metadata
				delete(members, "metadata")
			}
		case "reopen", "close":
			members["status"], _ = json.Marshal(map[string]string{"reopen": "reopened", "close": "closed"}[action])
			if reason, present := members["body"]; present {
				if _, both := members["comment"]; both {
					return "", nativeIssueArgument("comment")
				}
				members["comment"] = reason
				delete(members, "body")
			}
			if metadata, present := members["metadata"]; present {
				if _, both := members["comment_metadata"]; both {
					return "", nativeIssueArgument("comment_metadata")
				}
				members["comment_metadata"] = metadata
				delete(members, "metadata")
			}
		case "update":
		default:
			return "", nativeIssueArgument("action")
		}
		mapped, err := issues.EncodeReceivedObject(members)
		if err != nil {
			return "", issues.Failure("INVALID_ARGUMENT")
		}
		result, err = application.Mutate(ctx, issues.Scope{SourceProject: source}, id, mapped)
		if err != nil {
			return "", err
		}
	}
	if format == "json" {
		return encodeNativeIssueJSON(result)
	}
	if action == "create" {
		verb := "created"
		if result.Replayed {
			verb = "replayed"
		}
		return fmt.Sprintf("Issue #%d %s; revision %s. Read current detail before deriving an edit.", result.ID, verb, result.Revision), nil
	}
	if action == "comment" {
		return fmt.Sprintf("Comment added to issue #%d (comment id: %d); revision %s unchanged.", result.ID, *result.CommentID, result.Revision), nil
	}
	return fmt.Sprintf("Issue #%d updated; revision %s; changed=%t. Read and explicitly reconcile any later conflict.", result.ID, result.Revision, result.Changed), nil
}

func encodeNativeIssueJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", issues.Failure("UNAVAILABLE")
	}
	return string(encoded), nil
}

func nativeIssueInt(m map[string]any, field string, fallback, minimum, maximum int64) (int, error) {
	value, present, err := optionalInt64Arg(m, field)
	if !present {
		return int(fallback), nil
	}
	if err != nil || value < minimum || value > maximum {
		return 0, nativeIssueArgument(field)
	}
	return int(value), nil
}

func (s *Server) listNativeIssueTasks(ctx context.Context, m map[string]any, format string) (string, error) {
	params := gormdb.IssueTaskListParams{}
	var err error
	params.Limit, err = parseIssueListLimit(m)
	if err != nil {
		failure := issues.Failure("INVALID_ARGUMENT")
		failure.Message = err.Error()
		return "", failure
	}
	params.Offset, err = nativeIssueInt(m, "offset", 0, 0, int64(^uint(0)>>1))
	if err != nil {
		return "", err
	}
	params.TargetProject, _, _ = optionalStringArg(m, "project")
	params.SourceProject, _, _ = optionalStringArg(m, "source_project")
	params.SourceAgent, _, _ = optionalStringArg(m, "source_agent")
	params.SourceSession, _, _ = optionalStringArg(m, "source_session")
	params.TargetSession, _, _ = optionalStringArg(m, "target_session")
	params.ParticipantSession, _, _ = optionalStringArg(m, "participant_session")
	for _, field := range []string{"source_session", "target_session", "participant_session"} {
		if value, present, _ := optionalStringArg(m, field); present && value == "" {
			return "", nativeIssueArgument(field)
		}
	}
	params.Query, _, _ = optionalStringArg(m, "q")
	params.Type, _, _ = optionalStringArg(m, "type")
	params.Type = strings.ToLower(strings.TrimSpace(params.Type))
	if params.Type != "" && params.Type != "task" && params.Type != "bug" && params.Type != "feature" && params.Type != "improvement" {
		return "", nativeIssueArgument("type")
	}
	if status, present, _ := optionalStringArg(m, "status"); present {
		for _, value := range strings.Split(status, ",") {
			value = strings.TrimSpace(value)
			switch value {
			case "open", "acknowledged", "resolved", "reopened", "closed", "rejected":
			default:
				return "", nativeIssueArgument("status")
			}
			params.Statuses = append(params.Statuses, value)
		}
	}
	for _, field := range []string{"owner_label", "recipient_label"} {
		if value, present, err := optionalStringArg(m, field); err != nil {
			return "", nativeIssueArgument(field)
		} else if present {
			if field == "owner_label" {
				params.OwnerLabel = &value
			} else {
				params.RecipientLabel = &value
			}
		}
	}
	if since, err := parseIssueResolvedSince(m); err != nil {
		return "", nativeIssueArgument("resolved_since")
	} else if since > 0 {
		value := time.UnixMilli(since)
		params.ResolvedSince = &value
	}
	page, err := s.issueStore.ListTaskIssues(ctx, params)
	if err != nil {
		return "", err
	}
	roles := make([]gormdb.IssueProjectRoles, len(page.Issues))
	for index, row := range page.Issues {
		roles[index] = row.IssueProjectRoles
	}
	names, err := s.issueStore.IssueTaskProjectNames(ctx, roles)
	if err != nil {
		return "", err
	}
	type summary struct {
		gormdb.IssueTaskSummary
		Origin nativeIssueOrigin `json:"project_origin"`
	}
	items := make([]summary, len(page.Issues))
	for index, row := range page.Issues {
		items[index] = summary{row, nativeIssueOrigin{row.SourceProject, row.TargetProject}}
	}
	if format == "json" {
		return encodeNativeIssueJSON(map[string]any{"issues": items, "total": page.Total, "limit": page.Limit, "offset": page.Offset, "next_offset": page.NextOffset, "project_names": names})
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Issues [%d,%d) of %d; limit=%d. Live offset page, not a snapshot.\n", page.Offset, page.Offset+len(items), page.Total, page.Limit)
	for _, row := range items {
		fmt.Fprintf(&out, "#%d [%s] [%s] %s; revision %d; comments=%d\n", row.ID, row.Priority, row.Status, strconv.Quote(row.Title), row.Revision, row.CommentCount)
	}
	if page.NextOffset != nil {
		m["offset"] = *page.NextOffset
		m["action"] = "list"
		call, err := json.Marshal(m)
		if err != nil {
			return "", issues.Failure("UNAVAILABLE")
		}
		fmt.Fprintf(&out, "Next: issues(%s)\n", call)
	} else {
		out.WriteString("Complete: no later records.\n")
	}
	return out.String(), nil
}

type nativeIssueOrigin struct {
	Source string `json:"source_project"`
	Target string `json:"target_project"`
}

func (s *Server) getNativeIssueTask(ctx context.Context, m map[string]any, format string) (string, error) {
	id, err := requireInt64Arg(m, "id")
	if err != nil || id < 1 {
		return "", nativeIssueArgument("id")
	}
	params := gormdb.IssueTaskDetailParams{}
	if mode, present, _ := optionalStringArg(m, "detail_mode"); present {
		if mode != "paged" {
			return "", nativeIssueArgument("detail_mode")
		}
		params.Paged = true
	} else {
		params.Paged = format == "text"
	}
	params.CommentsOffset, err = nativeIssueInt(m, "comments_offset", 0, 0, int64(^uint(0)>>1))
	if err != nil {
		return "", err
	}
	params.CommentsLimit, err = nativeIssueInt(m, "comments_limit", 50, 1, 200)
	if err != nil {
		return "", err
	}
	params.HistoryOffset, err = nativeIssueInt(m, "history_offset", 0, 0, int64(^uint(0)>>1))
	if err != nil {
		return "", err
	}
	params.HistoryLimit, err = nativeIssueInt(m, "history_limit", 50, 1, 200)
	if err != nil {
		return "", err
	}
	afterID, err := nativeIssueInt(m, "history_after_id", 0, 0, int64(^uint(0)>>1))
	if err != nil {
		return "", err
	}
	params.HistoryAfterID = int64(afterID)
	page, err := s.issueStore.ReadTaskIssue(ctx, id, params)
	if err != nil {
		return "", err
	}
	names, err := s.issueStore.IssueTaskProjectNames(ctx, []gormdb.IssueProjectRoles{page.Roles})
	if err != nil {
		return "", err
	}
	type card struct {
		gormdb.Issue
		gormdb.IssueProjectRoles
		Origin nativeIssueOrigin `json:"project_origin"`
	}
	data := struct {
		gormdb.IssueTaskDetail
		Issue      card   `json:"issue"`
		SourceName string `json:"source_project_display_name"`
		TargetName string `json:"target_project_display_name"`
	}{IssueTaskDetail: page, Issue: card{page.Issue, page.Roles, nativeIssueOrigin{page.Issue.SourceProject, page.Issue.TargetProject}}}
	if page.Roles.SourceProjectID != nil {
		data.SourceName = names[*page.Roles.SourceProjectID]
	}
	if page.Roles.TargetProjectID != nil {
		data.TargetName = names[*page.Roles.TargetProjectID]
	}
	if format == "json" {
		return encodeNativeIssueJSON(data)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# Issue #%d: %s\nStatus: %s | Priority: %s | Revision: %d\nBody (quoted data): %s\nMetadata (quoted data): %s\n", id, strconv.Quote(page.Issue.Title), page.Issue.Status, page.Issue.Priority, page.Issue.Revision, strconv.Quote(page.Issue.Body), strconv.Quote(string(page.Issue.Metadata)))
	fmt.Fprintf(&out, "Comments [%d,%d) of %d; complete=%t; oversized=%t\n", page.CommentsOffset, page.CommentsOffset+len(page.Comments), page.CommentCount, page.CommentsComplete, page.CommentsOversized)
	for _, comment := range page.Comments {
		encoded, err := json.Marshal(comment)
		if err != nil {
			return "", issues.Failure("UNAVAILABLE")
		}
		fmt.Fprintf(&out, "Quoted comment data: %s\n", encoded)
	}
	fmt.Fprintf(&out, "History [%d,%d) of %d; complete=%t; oversized=%t\n", page.HistoryOffset, page.HistoryOffset+len(page.History), page.HistoryCount, page.HistoryComplete, page.HistoryOversized)
	for _, change := range page.History {
		encoded, err := json.Marshal(change)
		if err != nil {
			return "", issues.Failure("UNAVAILABLE")
		}
		fmt.Fprintf(&out, "Quoted history data: %s\n", encoded)
	}
	fmt.Fprintf(&out, "Lifecycle events after %d; more=%t\n", params.HistoryAfterID, page.HistoryHasMore)
	for _, event := range page.HistoryEvents {
		encoded, err := json.Marshal(event)
		if err != nil {
			return "", issues.Failure("UNAVAILABLE")
		}
		fmt.Fprintf(&out, "Quoted lifecycle event data: %s\n", encoded)
	}
	if page.HistoryNextAfterID != nil {
		call := map[string]any{"action": "get", "id": id, "format": "text", "detail_mode": "paged", "history_after_id": *page.HistoryNextAfterID, "history_limit": page.HistoryLimit}
		encoded, _ := json.Marshal(call)
		fmt.Fprintf(&out, "Next lifecycle events: issues(%s)\n", encoded)
	}
	for _, next := range []struct {
		field  string
		offset *int
	}{{"comments_offset", page.CommentsNextOffset}, {"history_offset", page.HistoryNextOffset}} {
		if next.offset == nil {
			continue
		}
		call := map[string]any{"action": "get", "id": id, "format": "text", "detail_mode": "paged", "comments_offset": page.CommentsOffset, "comments_limit": page.CommentsLimit, "history_offset": page.HistoryOffset, "history_limit": page.HistoryLimit}
		call[next.field] = *next.offset
		encoded, _ := json.Marshal(call)
		fmt.Fprintf(&out, "Next %s: issues(%s)\n", strings.TrimSuffix(next.field, "_offset"), encoded)
	}
	return out.String(), nil
}
