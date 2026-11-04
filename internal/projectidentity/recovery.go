package projectidentity

import "strings"

const invalidIdentityGuidance = `Preserve the actual caller's marker, caller-scoped local selection records, common cache, v2 anchor and data. Inspect only that caller root using bounded regular-file reads (4096 bytes maximum per identity file); inspect the Git common directory's engram-project.json separately. Do not follow a symlink, borrow another caller/root identity or execute file content. Record the file role and invalid fields without exposing secrets. Unknown fields, malformed IDs and invalid records need the owner's authorized repair; selection cannot bypass invalid identity evidence. Do not delete/reset/init, invent a UUID or change permissions. Keep the original operation held until the intended canonical identity is verified. For an unknown error code preserve the refusal and report that exact code to the owner; do not assume recovery succeeded. Guide: https://github.com/thebtf/engram/blob/main/docs/operating-engram.md#context-recovery`

// RecoveryGuidance contains only supported fixed operations, never repository data.
func RecoveryGuidance(code string) string {
	switch code {
	case "CONTEXT_REQUIRED", "CONTEXT_MISMATCH":
		return `Call codebase_context with action="list" first (project-free inspection, no selection). From that fresh response select ONLY the exact intended authorized Source/Checkout with action="select" and its context_handle; verify action="resolve", then retry the original operation. Never select first/latest or an unrelated lone Source. If none matches or the choice is ambiguous, keep the binding unchanged and identify the intended repository/checkout; registration is a separate authorized owned-worktree and canonical-project action, not project init.`
	case "PROJECT_MIGRATION_REQUIRED", "PROJECT_IDENTITY_CONFLICT", "PROJECT_CONTEXT_REQUIRED", "PROJECT_RESOLUTION_UNAVAILABLE", "PROJECT_SELECTION_INVALID", "PROJECT_SELECTION_STALE":
		return `Ordinary calls automatically recover a strict historical id/name marker when the configured server's authorized GET /api/projects/resolve?legacy_id=<encoded-exact-id> verifies one existing canonical mapping. If the intended project still needs a choice, call the local project_context tool with action="choices". Review the current authorized names, full project IDs and repository qualifiers; do not pick first/latest, by name alone or merely because one result exists. Bind the intended existing project with action="select" and its exact fresh selection_ref, then retry the original operation unchanged with its original actor/session/client and target. External clients can use the installed native client or plugin launcher: engram project choices --root <actual-caller-root> --json; engram project select --root <actual-caller-root> --selection-ref <fresh-reference> --json; ordinary engram project resolve --root <actual-caller-root> --json then revalidates the retained binding. The host must supply its actual caller root, not a guessed terminal cwd. Choices is read-only. Selection is local to that caller/root/server and preserves marker/cache/data; it creates no project, migrates no data, grants no permission and never replaces an existing different binding. Stale refs require fresh choices; conflicting or invalid existing identity records remain preserved for authorized owner repair. Authentication, denied catalog access or unavailable readers must be restored before selection; never bypass permissions or substitute a browser session for host identity. project resolve --check remains read-only local/remote diagnosis and never bootstraps, pins or selects; a strict historical marker there is only migration_required evidence, not a server binding. Project-free issues list/get can continue. Never replay a mutation after response loss or an unknown acceptance outcome. Guide: https://github.com/thebtf/engram/blob/main/docs/operating-engram.md#context-recovery`
	case "PROJECT_IDENTITY_INVALID":
		return invalidIdentityGuidance
	default:
		if strings.HasPrefix(code, "PROJECT_") {
			return invalidIdentityGuidance
		}
		return ""
	}
}
