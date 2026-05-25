// Package scope implements 4-tier privacy_scope visibility resolution for
// engram vNext Milestone F (TG1 / CR-F1 / T003).
//
// See spec.md §FR-F1 schema delta + §FR-F1 CHK005-ADDED "private scope keycard
// identity invariant". Architecture: ADR-F-001 + plan.md Phase 1.
//
// The 4 scope tiers, in widening visibility order:
//
//   - 'private': only the writing workstation/session
//   - 'project': all sessions tagged with the same project slug (upstream filter)
//   - 'shared':  cross-project for the same workstation owner (currently same
//     surface as 'global' for single-workstation deployments — see
//     .agent/specs/engram-vnext-milestone-f/implementation-notes.html
//     Needs Amend entry on multi-workstation expansion)
//   - 'global':  visible across the entire engram server
//
// Resolve is the central visibility decision point. The project boundary check
// is enforced UPSTREAM in the existing recall path (`WHERE project = ?` in
// MemoryStore.List); Resolve trusts that filter and does not re-check project.
package scope

// PrivacyScope values recognised by Resolve. Mirror the CHECK constraint on
// memories.privacy_scope added by migration 125 (T001).
const (
	ScopePrivate string = "private"
	ScopeProject string = "project"
	ScopeShared  string = "shared"
	ScopeGlobal  string = "global"
)

// KeycardContext identifies the caller's keycard identity for visibility checks.
//
// WorkstationID is reserved for the spec FR-F1 keycard identity invariant. The
// existing `internal/auth/identity.go Identity` struct does not yet expose a
// stable WorkstationID — and the memories table does not yet store
// source_workstation_id. See .agent/specs/engram-vnext-milestone-f/
// implementation-notes.html Needs Amend entry. Until the spec amendment lands,
// workstation-only matching is NOT supported; Resolve requires SessionID
// non-empty for private-scope visibility.
type KeycardContext struct {
	WorkstationID string // reserved — Needs Amend; not consulted in TG1
	SessionID     string
}

// SourceMeta identifies the memory's source for visibility checks. Sessions
// mirrors Memory.SourceSessions per spec FR-F1 schema delta (T002 / migration
// 125).
type SourceMeta struct {
	Sessions []string
}

// Resolve returns true iff the caller may see a memory with the given
// privacy_scope value. The project boundary check is enforced upstream in the
// recall path (MemoryStore.List `WHERE project = ?`); Resolve trusts that
// filter and does not re-check project.
//
// Decision table:
//
//	ScopeGlobal  → always true (server-wide visibility)
//	ScopeShared  → always true (cross-project; same surface as global in TG1)
//	ScopeProject → always true (project filter is upstream)
//	ScopePrivate → true iff caller.SessionID non-empty AND in memorySource.Sessions
//	unknown      → false (fail-closed against unrecognised scope strings)
//
// Anti-stub: a `return true` body would pass the project/shared/global cases
// (12 of 16 in the TestResolve table) but would fail the 4 private cases that
// expect FALSE (caller-session-mismatch, caller-without-session, empty
// memory-sessions, scope-but-no-session-data). Exactly 4 false-positives —
// matches the AC anti-stub bound.
func Resolve(caller KeycardContext, memoryScope string, memorySource SourceMeta) bool {
	switch memoryScope {
	case ScopeGlobal, ScopeShared, ScopeProject:
		return true
	case ScopePrivate:
		if caller.SessionID == "" {
			return false
		}
		for _, s := range memorySource.Sessions {
			if s == caller.SessionID {
				return true
			}
		}
		return false
	default:
		return false
	}
}
