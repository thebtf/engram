package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/thebtf/engram/internal/auth"
)

// buildAdminTool advertises ordinary admin operations; mutations remain admin-authorized.
func buildAdminTool() Tool {
	actions := adminActionsForEnv()
	desc := "Administrative operations. Actions: " +
		strings.Join(actions, ", ") + ". Action required."

	props := map[string]any{
		"action": map[string]any{"type": "string", "enum": actions, "description": "Action to perform (required)."},
	}

	{
		props["project"] = map[string]any{"type": "string", "description": "Project name (required for purge_project)"}
		props["confirm"] = map[string]any{"type": "string", "description": "Double-entry confirmation: must equal project name (for purge_project)"}
	}

	return Tool{
		Name:        "admin",
		Description: desc,
		tier:        tierUseful,
		InputSchema: map[string]any{
			"type":       "object",
			"required":   []string{"action"},
			"properties": props,
		},
	}
}

// adminActionsBase lists the always-on admin actions.
var adminActionsBase = []string{"stats"}

// adminActionsVnext retains the existing purge action.
var adminActionsVnext = []string{
	"purge_project",
}

func adminActionsForEnv() []string {
	all := make([]string, len(adminActionsBase)+len(adminActionsVnext))
	copy(all, adminActionsBase)
	copy(all[len(adminActionsBase):], adminActionsVnext)
	return all
}

func (s *Server) handleAdmin(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	action := coerceString(m["action"], "")
	if action == "" {
		return "", fmt.Errorf("action required for admin tool (valid: %s)", strings.Join(adminActionsForEnv(), ", "))
	}

	switch action {
	case "stats":
		return s.handleAdminStats(ctx)
	case "purge_project":
		return s.handlePurgeProject(ctx, m)
	default:
		return "", fmt.Errorf("unknown admin action: %q (valid: %s)", action, strings.Join(adminActionsForEnv(), ", "))
	}
}

// handlePurgeProject implements the purge_project admin action.
// Requires admin-level identity (RoleAdmin): the operator key (ENGRAM_AUTH_ADMIN_TOKEN)
// and session-admin path both satisfy this; read-write/read-only keycards do not.
// Double-entry confirmation: confirm must equal project to proceed.
// Credentials (credentials table) are excluded — vault concern.
// The purge receipt is returned as JSON and stored in audit_log.
func (s *Server) handlePurgeProject(ctx context.Context, m map[string]any) (string, error) {
	// --- CRIT: admin authorization gate ---
	// Identity is injected into ctx by the gRPC auth interceptor (auth.WithIdentity)
	// and by the HTTP middleware (same path). When auth is disabled, no Identity is
	// present — fail closed for this destructive action.
	id, ok := auth.IdentityFrom(ctx)
	if !ok || !id.IsAdmin() {
		return "", fmt.Errorf("admin authorization required for purge_project")
	}

	project := strings.TrimSpace(coerceString(m["project"], ""))
	if project == "" {
		return "", fmt.Errorf("project required for purge_project")
	}
	confirm := strings.TrimSpace(coerceString(m["confirm"], ""))
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
