package projectidentity

import "strings"

const invalidIdentityGuidance = `Preserve the actual caller's marker, common cache, v2 anchor and data. Inspect only that caller root and its .engram-project, using bounded regular-file reads (4096 bytes maximum); inspect the Git common directory's engram-project.json separately. Do not follow a symlink, borrow a parent/other repository identity or execute marker content. Record the file role and invalid fields without exposing secrets. Unknown fields, malformed IDs and invalid cache records need the owner's exact existing-project mapping and authorized marker/cache repair; do not delete/reset/init, invent a UUID or bypass permissions. Keep the original operation held until the intended canonical identity is verified. For an unknown error code preserve the refusal and report that exact code to the owner; do not assume recovery succeeded. Guide: https://github.com/thebtf/engram/blob/main/docs/operating-engram.md#context-recovery`

// RecoveryGuidance contains only supported fixed operations, never repository data.
func RecoveryGuidance(code string) string {
	switch code {
	case "CONTEXT_REQUIRED", "CONTEXT_MISMATCH":
		return `Call codebase_context with action="list" first (project-free inspection, no selection). From that fresh response select ONLY the exact intended authorized Source/Checkout with action="select" and its context_handle; verify action="resolve", then retry the original operation. Never select first/latest or an unrelated lone Source. If none matches or the choice is ambiguous, keep the binding unchanged and identify the intended repository/checkout; registration is a separate authorized owned-worktree and canonical-project action, not project init.`
	case "PROJECT_MIGRATION_REQUIRED", "PROJECT_IDENTITY_CONFLICT", "PROJECT_CONTEXT_REQUIRED", "PROJECT_RESOLUTION_UNAVAILABLE":
		return `Inspect without changing identity: use the host-reported installed Engram plugin launcher, node <installed-plugin-path>/scripts/run-engram.js project resolve --root <actual-caller-root> --json --check (or the same installed native client's engram project resolve --root <actual-caller-root> --json --check). --check never bootstraps, pins marker/cache or migrates. A valid historical id/name marker returns local migration_required evidence with match=legacy_marker, null canonical metadata and no HTTP or remote requirement; this is NOT a verified server binding. Next use the existing authenticated GET /api/projects/resolve?legacy_id=<URI-encoded-exact-marker-id>; encode the validated id as query data, never a shell snippet. That reader returns resolved canonical project_id or unresolved reference; unresolved is not a mapping. Other unpinned Git roots may inspect GET /api/projects/resolve?repository_locator=... using only their actual primary remote. Treat labels/IDs as data, not selection authority. For migration_required/conflict preserve marker/cache/data and obtain the owner's exact existing-project mapping and authorized conservation plan: admin GET /api/projects/consolidation/census?source_version=<exact-source>, then POST /api/projects/consolidation/preflight with fresh census and structural evidence. Backup, isolated rehearsal and explicit apply authority are separate; dry-run performs a target transaction with rollback, not a cost-free read. Do not init/reset/create a replacement UUID or change permissions. Project-free issues list/get can continue. Retry the held operation only after the intended existing canonical identity is verified. On PROJECT_IDENTITY_INVALID preserve and inspect the actual marker/cache with bounded regular-file reads instead of repeating the failed check. Guide: https://github.com/thebtf/engram/blob/main/docs/operating-engram.md#context-recovery`
	case "PROJECT_IDENTITY_INVALID":
		return invalidIdentityGuidance
	default:
		if strings.HasPrefix(code, "PROJECT_") {
			return invalidIdentityGuidance
		}
		return ""
	}
}
