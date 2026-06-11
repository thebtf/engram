// Package auth provides token validation that recognises both server-host operator
// keys (tier 1) and dashboard-issued client keycards (tier 2). It is the single
// source of truth for token-based authentication across HTTP and gRPC transports.
package auth

// Source identifies the authentication path that produced an Identity. It is set
// by the validator (for header/Bearer paths) or by middleware directly (for
// session-cookie / forward-auth paths).
type Source string

const (
	// SourceMaster is set when the bearer matched the operator key. The
	// operator key is admin-grade and lives only in the server-host
	// environment (ENGRAM_AUTH_ADMIN_TOKEN). It MUST NOT be issued to
	// workstation processes.
	SourceMaster Source = "master"

	// SourceClient is set when the bearer matched a non-revoked entry in the
	// api_tokens table (issued via the dashboard /tokens page). The
	// per-token Scope drives Identity.Role.
	SourceClient Source = "client"

	// SourceSession is NOT produced by the validator. Middleware sets it
	// directly when authenticating via the engram_session HMAC cookie or the
	// engram_auth DB-backed session cookie (browser logins). Issuance and
	// revocation endpoints accept SourceSession only.
	SourceSession Source = "session"
)

// Role is the role string carried in the request context. Existing call sites
// compare against the literal strings "admin", "read-write", "read-only".
type Role string

const (
	// RoleAdmin is granted to operator-key bearers and to admin users on the
	// session-cookie path.
	RoleAdmin Role = "admin"

	// RoleReadWrite mirrors the api_tokens.scope value "read-write".
	RoleReadWrite Role = "read-write"

	// RoleReadOnly mirrors the api_tokens.scope value "read-only".
	RoleReadOnly Role = "read-only"
)

// Identity is the immutable result of a successful token validation. It is a
// value type — passed by value, never mutated after construction.
type Identity struct {
	// Role is the authorization role the bearer carries.
	Role Role

	// Source identifies the auth path. Issuance endpoints (FR-6 / C4)
	// require Source == SourceSession; bearer-based callers (master OR
	// client) are explicitly rejected.
	Source Source

	// KeycardID is the api_tokens.id (UUID) when Source == SourceClient.
	// Empty string for SourceMaster and SourceSession.
	KeycardID string
}

// Admin returns an Identity for a successful master-token match.
func Admin() Identity {
	return Identity{Role: RoleAdmin, Source: SourceMaster}
}

// Client returns an Identity for a successful client-keycard match. scope is
// the api_tokens.scope value ("read-write" or "read-only"); keycardID is the
// api_tokens.id used by audit logs and revocation lookup.
func Client(scope string, keycardID string) Identity {
	return Identity{Role: Role(scope), Source: SourceClient, KeycardID: keycardID}
}

// Session returns an Identity for a successful session-cookie authentication.
// role is the resolved role string ("admin" for HMAC cookie, or the user role
// for engram_auth cookie).
func Session(role string) Identity {
	return Identity{Role: Role(role), Source: SourceSession}
}

// IsAdmin reports whether the bearer holds admin role regardless of source.
// Issuance endpoints MUST additionally check Source == SourceSession (use
// IsSessionAdmin), not this method.
func (i Identity) IsAdmin() bool {
	return i.Role == RoleAdmin
}

// IsSessionAdmin is the gate for issuance/revocation endpoints (FR-6 / C4).
// True only when both Role is admin AND Source is a browser session.
func (i Identity) IsSessionAdmin() bool {
	return i.Role == RoleAdmin && i.Source == SourceSession
}

// WorkstationID returns the stable workstation marker for this identity.
//
// For SourceClient keycard bearers, this is api_tokens.id — each workstation
// is provisioned its own keycard in v6 (per CONTINUITY: per-workstation
// API tokens replaced the shared admin token in v6 BREAKING), so the
// keycard UUID functions as a stable per-workstation identifier.
//
// For SourceMaster (the operator key bearer) and SourceSession (browser
// cookie sessions) returns the empty string — neither maps to a single
// workstation: master is a system-wide bearer used by the server itself,
// session cookies are user-bound rather than workstation-bound.
//
// Consumers (engram vNext Milestone F TG1 / T003b / spec FR-F1 AMEND
// 2026-05-25): the empty return value is treated as fail-closed by
// scope.Resolve ScopePrivate — a memory with privacy_scope='private'
// cannot be made visible to a caller whose workstation identity is
// unknown. Private-scope writes therefore require a SourceClient keycard
// bearer.
func (i Identity) WorkstationID() string {
	if i.Source == SourceClient {
		return i.KeycardID
	}
	return ""
}
