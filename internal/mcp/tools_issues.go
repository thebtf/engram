package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/projectidentity"
)

// issuesToolSchema returns the flat JSON Schema for the issues tool.
// Anthropic API does NOT support oneOf/allOf/anyOf at the top level of input_schema,
// so we use a flat schema with per-action requirements encoded in each property's
// description (via REQUIRED_FOR: prefix) and enforced server-side by
// validateIssueActionParams() with clear error messages showing the full signature.
func issuesToolSchema() map[string]any {
	schema := map[string]any{
		"x-engram-caller-context": "issues/v1",
		"type":                    "object",
		"required":                []string{"action"},
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"create", "list", "get", "update", "comment", "reopen", "close"},
				"description": "Action to perform. list/get are project-free authorized reads and remain available during a project migration hold. Mutations require the native caller's canonical identity; changing a project argument cannot establish it. PROJECT_MIGRATION_REQUIRED: " + projectidentity.RecoveryGuidance("PROJECT_MIGRATION_REQUIRED"),
			},
			"project": map[string]any{
				"type":        "string",
				"description": "Optional compatibility argument on mutations; it never establishes actor identity, which comes from the native caller's verified canonical project. For list: exact target-project filter, not name resolution; zero legacy-label results do not prove no canonical issues. get needs only id.",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "REQUIRED_FOR: create. Short issue title.",
			},
			"target_project": map[string]any{
				"type":        "string",
				"description": "REQUIRED_FOR: create. Existing canonical project UUID for the issue's target. Confirm the authorized target independently; do not choose by display name.",
			},
			"id": map[string]any{
				"type":        "integer",
				"description": "REQUIRED_FOR: get|update|comment|reopen|close. Issue ID returned by create or list.",
			},
			"body": map[string]any{
				"type":        "string",
				"description": "REQUIRED_FOR: comment. Comment text; create/update accept body, reopen/close accept an optional reason.",
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
				"description": "OPTIONAL_FOR: create or protected material update. Default: medium.",
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
				"description": "OPTIONAL_FOR: create, update, or list. Default: task.",
			},
			"source_project": map[string]any{
				"type":        "string",
				"description": "OPTIONAL_FOR: list. Exact source-project filter, not name resolution; zero legacy-label results do not prove no canonical issues.",
			},
			"source_agent": map[string]any{
				"type":        "string",
				"description": "OPTIONAL_FOR: list. Exact actor label filter, independent of source_project.",
			},
			"comment": map[string]any{
				"type":        "string",
				"description": "Optional atomic comment for update, reopen, or close.",
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
	properties := schema["properties"].(map[string]any)
	properties["format"] = map[string]any{"type": "string", "enum": []string{"text", "json"}, "description": "Text shows bounded pages and continuations; json returns the admitted DTO in one MCP text block."}
	properties["request_key"] = map[string]any{"type": "string", "minLength": 1, "description": "Retain the original actor, source, key and payload for retry."}
	properties["expected_actor"] = map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"enum": []string{"client", "operator", "unknown"}}, "reference": map[string]any{"type": []string{"string", "null"}}}}
	properties["expected_revision"] = map[string]any{"type": "string", "pattern": "^[1-9][0-9]*$", "description": "Required for material edits; reconcile stale revisions explicitly."}
	for _, field := range []string{"metadata", "comment_metadata"} {
		properties[field] = map[string]any{"type": "object", "description": "Admitted JSON object; recognized secrets are rejected, never silently rewritten."}
	}
	for _, field := range []string{"owner_label", "recipient_label"} {
		properties[field] = map[string]any{"type": []string{"string", "null"}, "description": "Non-authoritative label; null unassigns."}
	}
	properties["detail_mode"] = map[string]any{"type": "string", "enum": []string{"paged"}}
	for _, field := range []string{"comments_limit", "history_limit"} {
		properties[field] = map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 50}
	}
	for _, field := range []string{"offset", "comments_offset", "history_offset"} {
		properties[field] = map[string]any{"type": "integer", "minimum": 0, "default": 0}
	}
	properties["q"] = map[string]any{"type": "string", "description": "Literal title/body search."}
	properties["agent_source"] = map[string]any{"type": "string", "description": "Compatibility attribution, not authority."}
	properties["source_session"] = map[string]any{"type": "string", "description": "List filter: exact issue creator session, not authentication or authority."}
	properties["target_session"] = map[string]any{"type": []string{"string", "null"}, "description": "Create/update: nonempty addressee session; update may use null to clear it. List: exact nonempty addressee session filter; null is invalid for create and list. Session is provenance, not authentication or authority."}
	properties["participant_session"] = map[string]any{"type": "string", "description": "List filter: exact recorded creator, comment, lifecycle event or committed material revision session attribution; provenance only, not authentication or authority."}
	properties["session_id"] = map[string]any{"type": "string", "description": "Reported host conversation session; provenance only."}
	properties["client"] = map[string]any{"type": "string", "description": "Public client label; provenance only."}
	properties["history_after_id"] = map[string]any{"type": "integer", "minimum": 0, "description": "Exclusive lifecycle event cursor; separate from revision history_offset."}
	properties["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 20}
	properties["status"] = map[string]any{"type": "string", "description": "List states or update status; rejection requires a comment and existing authority applies."}
	properties["target_project"].(map[string]any)["description"] = "Existing canonical project UUID for the authorized target; only omission defaults to the verified source. Do not choose by display name."
	properties["source_project"].(map[string]any)["description"] = "Exact canonical source-project filter, not name resolution; zero legacy-label results do not prove no canonical issues. It is not an authority grant."
	return schema
}

// actionRequirements describes required parameters for each issues action.
// Used for upfront validation and consistent error messages.
var actionRequirements = map[string]struct {
	required []string
	full     string
}{
	"create":  {required: []string{"title"}, full: "action, title [optional canonical target_project]"},
	"list":    {required: []string{}, full: "action  [optional: project, source_project, source_agent, status, resolved_since, limit]"},
	"get":     {required: []string{"id"}, full: "action, id"},
	"update":  {required: []string{"id"}, full: "action, id, expected_revision and material fields or comment"},
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
	return s.executeNativeIssues(ctx, args)
}

const issueListMaxLimit = 200

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

type issueRecoveryError struct {
	err  error
	code string
}

func (e *issueRecoveryError) Error() string { return e.err.Error() }
func (e *issueRecoveryError) Unwrap() error { return e.err }

func issueAdoptionRefusal(err error) string {
	var hold *gormdb.ConsolidationError
	var pgErr *pgconn.PgError
	var pqErr *pq.Error
	code := ""
	switch {
	case errors.As(err, &hold):
		code = hold.Code
	case errors.As(err, &pgErr) && pgErr.Code == "P0001":
		code = pgErr.Message
	case errors.As(err, &pqErr) && pqErr.Code == "P0001":
		code = pqErr.Message
	}
	if code == "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP" || code == "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP" {
		return code
	}
	return ""
}
