package config

// Canonical env-var names. Single source of truth (Plan ADR-002): every reader
// across the codebase imports the constant rather than hard-coding the literal.
// Renames are caught at compile time. Two distinct names enforce the
// admin-vs-client tier separation (FR-1 + FR-3 + ADR-006):
//
//   - EnvAdminToken       — server-host-only operator key. NEVER read by
//     daemon, plugin, or hook code.
//   - EnvWorkstationToken — client-side keycard issued via the dashboard
//     /tokens UI. Read ONLY by the daemon and bridge.
//
// EnvServerURL / EnvServerURLAlt resolve the historical split between
// ENGRAM_URL (plugin docs / hooks) and ENGRAM_SERVER_URL (server-events
// bridge); readers SHOULD check both.
const (
	// EnvServerURL is the canonical workstation-side URL of the engram
	// server (e.g. "http://unleashed.lan:37777"). Read by run-engram.js,
	// hooks, and the daemon.
	EnvServerURL = "ENGRAM_URL"

	// EnvServerURLAlt is the bridge-side alias retained for plugin docs
	// that still document ENGRAM_SERVER_URL. Readers SHOULD prefer
	// EnvServerURL but fall back to EnvServerURLAlt before declaring
	// "no URL configured".
	EnvServerURLAlt = "ENGRAM_SERVER_URL"

	// EnvAdminToken is the operator-grade master key used to authenticate
	// admin-only RPCs and as the master compare arm of the auth.Validator.
	// MUST live ONLY in the server-host environment (Docker / compose).
	// Reading this from a workstation context is a contract violation.
	EnvAdminToken     = "ENGRAM_AUTH_ADMIN_TOKEN"
	EnvAdminTokenFile = "ENGRAM_AUTH_ADMIN_TOKEN_FILE"

	EnvDatabaseDSN     = "DATABASE_DSN"
	EnvDatabaseDSNFile = "DATABASE_DSN_FILE"

	// EnvWorkstationToken is the per-workstation client api token (keycard)
	// issued through the dashboard /tokens page. The daemon and the
	// serverevents bridge read it; nothing else does. Empty value at
	// daemon startup with a configured server URL is fatal (FR-4).
	EnvWorkstationToken = "ENGRAM_TOKEN"

	// EnvClaudeSessionID is the Claude Code session identifier injected by the
	// Claude Code harness into the daemon process environment on every session.
	// The daemon reads it from p.Env (per-session override) or os.Getenv
	// (process-level) and propagates it as SessionId in pb.CallToolRequest so
	// the server-side audit helpers can record the correct SourceSessionID.
	EnvClaudeSessionID = "CLAUDE_SESSION_ID"
)
