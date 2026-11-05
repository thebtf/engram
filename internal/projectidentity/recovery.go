package projectidentity

import "strings"

const invalidIdentityGuidance = `Preserve the actual caller's marker, common cache, v2 anchor and data. Inspect only that caller root and its .engram-project, using bounded regular-file reads (4096 bytes maximum); inspect the Git common directory's engram-project.json separately. Do not follow a symlink, borrow a parent/other repository identity or execute marker content. Record the file role and invalid fields without exposing secrets. Unknown fields, malformed IDs and invalid cache records need the owner's exact existing-project mapping and authorized marker/cache repair; do not delete/reset/init, invent a UUID or bypass permissions. Keep the original operation held until the intended canonical identity is verified. For an unknown error code preserve the refusal and report that exact code to the owner; do not assume recovery succeeded. Guide: https://github.com/thebtf/engram/blob/main/docs/operating-engram.md#context-recovery`

// RecoveryGuidance contains only supported fixed operations, never repository data.
func RecoveryGuidance(code string) string {
	switch code {
	case "CONTEXT_REQUIRED", "CONTEXT_MISMATCH":
		return `Call codebase_context with action="list" first (project-free inspection, no selection). From that fresh response select ONLY the exact intended authorized Source/Checkout with action="select" and its context_handle; verify action="resolve", then retry the original operation. Never select first/latest or an unrelated lone Source. If none matches or the choice is ambiguous, keep the binding unchanged and identify the intended repository/checkout; registration is a separate authorized owned-worktree and canonical-project action, not project init.`
	case "PROJECT_AUTH_REQUIRED":
		return `The configured backend rejected authentication. In this host conversation ask for the existing authorized keycard for this exact server to be configured through the host's supported private credential route; do not print or pass credentials in tool arguments. Preserve marker, cache and data; do not bypass authentication. After access is restored, retry only the original safe read.`
	case "PROJECT_ACCESS_DENIED":
		return `The configured backend denied access. In this host conversation ask the project owner to grant the existing keycard access to the intended existing project on this server, using the supported private credential route. Do not disclose credentials, bypass authorization, or select another project to evade denial. Preserve marker, cache and data; after access is restored retry only the original safe read.`
	case "PROJECT_MIGRATION_REQUIRED", "PROJECT_IDENTITY_CONFLICT", "PROJECT_CONTEXT_REQUIRED", "PROJECT_RESOLUTION_UNAVAILABLE":
		return `Use the local project_context tool. For a server-confirmed historical or primary-origin mapping call action="repair"; when neither identifies an existing project call action="choices" and explicitly select the intended authorized project with its fresh selection_ref, then call action="repair". For conflicting evidence preserve both files, ask the indicated question in this host conversation, and use only the selected fresh authorized choice; a portable-marker/cache conflict can resume only the original safe recall in the same verified transport session, without changing either file. The host resolves any returned marker target relative to the actual caller Cwd, verifies both root fingerprints and all preconditions, preserves the old bytes, and applies the action only through its filesystem guard. The tool does not write a marker, migrate data or authorize access. On denial request write access to that exact target, never bypass the guard; on stale input obtain fresh evidence. Retry only the original safe read; never replay an unknown-outcome mutation. For read-only diagnostics the installed client supports project resolve --root <actual-caller-root> --json --check and GET /api/projects/resolve?legacy_id=<URI-encoded-exact-marker-id>, subject to the backend's authorization policy. Unresolved historical membership is not a mapping. Preserve marker, cache and data; never init/reset or invent a replacement UUID. Guide: https://github.com/thebtf/engram/blob/main/docs/operating-engram.md#context-recovery`
	case "PROJECT_IDENTITY_INVALID":
		return invalidIdentityGuidance
	default:
		if strings.HasPrefix(code, "PROJECT_") {
			return invalidIdentityGuidance
		}
		return ""
	}
}
