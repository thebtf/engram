package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/thebtf/engram/internal/auth"
)

// buildAdminTool constructs the admin tool definition, gating purge_project
// schema fields behind ENGRAM_VNEXT_ENABLED. When the flag is off, the
// description and schema are byte-identical to the pre-branch (no purge_project
// mention, no confirm field). This ensures flag-off tool/list output is
// indistinguishable from the pre-branch surface.
func buildAdminTool() Tool {
	actions := adminActionsForEnv()
	desc := "Administrative operations: bulk ops, tagging, analytics. Actions: " +
		strings.Join(actions, ", ") + ". Action required."

	props := map[string]any{
		"action":  map[string]any{"type": "string", "description": "Action to perform (required). See tool description for valid actions."},
		"project": map[string]any{"type": "string", "description": "Project name (for stats, search_analytics)"},
		"days":    map[string]any{"type": "number", "description": "Days to analyze (for search_analytics)"},
	}

	if vnextEnabled() {
		// Extend project description and add confirm field only when purge_project is active.
		props["project"] = map[string]any{"type": "string", "description": "Project name (for stats, search_analytics, purge_project)"}
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

// adminActionsBase lists the always-on admin actions (no feature flag required).
var adminActionsBase = []string{
	"stats", "search_analytics", "backfill_status",
}

// adminActionsVnext lists admin actions that require ENGRAM_VNEXT_ENABLED=true.
var adminActionsVnext = []string{
	"purge_project",
}

// adminActions is the single source of truth for valid admin tool actions
// in the current runtime configuration. It is built once at package init
// and references the current environment, so it reflects flag state at
// process start. The server.go tool schema uses the same helper
// (adminActionsForEnv) to build descriptions that match actual behaviour.
var adminActions = adminActionsForEnv()

// adminActionsForEnv returns the current set of valid admin actions based on
// ENGRAM_VNEXT_ENABLED. When the flag is "true", vnext actions (purge_project)
// are appended to the base list. Callers that need the flag-off list explicitly
// should use adminActionsBase directly.
func adminActionsForEnv() []string {
	if os.Getenv("ENGRAM_VNEXT_ENABLED") == "true" {
		all := make([]string, len(adminActionsBase)+len(adminActionsVnext))
		copy(all, adminActionsBase)
		copy(all[len(adminActionsBase):], adminActionsVnext)
		return all
	}
	return adminActionsBase
}

// vnextEnabled reports whether the vnext feature flag is active.
// Mirroring the check used elsewhere in service.go.
func vnextEnabled() bool {
	return os.Getenv("ENGRAM_VNEXT_ENABLED") == "true"
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
		return s.handleGetMemoryStats(ctx)
	case "search_analytics":
		return s.handleAnalyzeSearchPatterns(ctx, args)
	case "backfill_status":
		return s.handleBackfillStatus()
	case "purge_project":
		// purge_project is a vnext-gated action (Milestone D).
		// When flag is off, respond byte-identically to an unknown action so
		// the flag-off surface is indistinguishable from pre-branch behaviour.
		if !vnextEnabled() {
			return "", fmt.Errorf("unknown admin action: %q (valid: %s)", action, strings.Join(adminActionsBase, ", "))
		}
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
