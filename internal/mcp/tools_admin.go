package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// adminActions is the single source of truth for valid admin tool actions.
// It is referenced by handleAdmin for validation messages and by the tool
// registration in server.go for the tool description.
var adminActions = []string{
	"stats", "search_analytics", "backfill_status", "purge_project",
}

func (s *Server) handleAdmin(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	action := coerceString(m["action"], "")
	if action == "" {
		return "", fmt.Errorf("action required for admin tool (valid: %s)", strings.Join(adminActions, ", "))
	}

	switch action {
	case "stats":
		return s.handleGetMemoryStats(ctx)
	case "search_analytics":
		return s.handleAnalyzeSearchPatterns(ctx, args)
	case "backfill_status":
		return s.handleBackfillStatus()
	case "purge_project":
		return s.handlePurgeProject(ctx, m)
	default:
		return "", fmt.Errorf("unknown admin action: %q (valid: %s)", action, strings.Join(adminActions, ", "))
	}
}

// handlePurgeProject implements the purge_project admin action.
// Double-entry confirmation: confirm must equal project to proceed.
// Credentials (credentials table) are excluded — vault concern.
// The purge receipt is returned as JSON and stored in audit_log.
func (s *Server) handlePurgeProject(ctx context.Context, m map[string]any) (string, error) {
	project := coerceString(m["project"], "")
	if project == "" {
		return "", fmt.Errorf("project required for purge_project")
	}
	confirm := coerceString(m["confirm"], "")
	if confirm == "" {
		return "", fmt.Errorf("confirmation required: set confirm=%q to confirm purge of project %q", project, project)
	}
	if confirm != project {
		return "", fmt.Errorf("confirmation required: confirm=%q does not match project=%q", confirm, project)
	}

	if s.purgeStore == nil {
		return "", fmt.Errorf("purge store not available")
	}

	receipt, err := s.purgeStore.PurgeProject(ctx, project)
	if err != nil {
		return "", fmt.Errorf("purge_project: %w", err)
	}

	return marshalJSON(receipt)
}
