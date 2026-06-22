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

import (
	"strings"

	"github.com/thebtf/engram/pkg/models"
)

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
// WorkstationID is populated by handler-level wiring from
// `auth.Identity.WorkstationID()` (added in T003b per spec FR-F1 AMEND
// 2026-05-25). For `auth.SourceClient` bearers this is the api_tokens.id
// (one keycard per workstation); empty for `SourceMaster` + `SourceSession`.
//
// SessionID is the per-session identifier when available (handler-derived).
type KeycardContext struct {
	WorkstationID string
	SessionID     string
	Principal     string
	PrincipalKind string
}

// SourceMeta identifies the memory's source for visibility checks.
//
// WorkstationID mirrors `Memory.SourceWorkstationID` (added by migration 130
// in T001b). Sessions mirrors `Memory.SourceSessions` (added by migration
// 125 in T001 / T002).
type SourceMeta struct {
	WorkstationID string
	Sessions      []string
}

// MemoryVisibilityOptions controls which legacy visibility layers apply to a
// memory read. Principal-private filtering is always applied by ResolveMemory;
// ApplyPrivacyScope controls only the legacy privacy_scope layer.
type MemoryVisibilityOptions struct {
	IncludeScopes     map[string]bool
	ApplyPrivacyScope bool
}

// ResolveMemory composes legacy privacy_scope visibility with principal-owned
// memory visibility.
//
// ApplyPrivacyScope=false preserves old flag-off behavior for legacy unowned
// rows, but never disables principal-private filtering for owned rows.
func ResolveMemory(caller KeycardContext, mem *models.Memory, opts MemoryVisibilityOptions) bool {
	if mem == nil {
		return false
	}
	if opts.ApplyPrivacyScope {
		memScope := NormalizePrivacyScope(mem.PrivacyScope)
		if len(opts.IncludeScopes) > 0 && !opts.IncludeScopes[memScope] {
			return false
		}
		meta := SourceMeta{
			WorkstationID: mem.SourceWorkstationID,
			Sessions:      mem.SourceSessions,
		}
		if !Resolve(caller, memScope, meta) {
			return false
		}
	}
	return ResolvePrincipal(caller, mem)
}

// ResolvePrincipal returns whether principal ownership metadata permits caller
// visibility. Empty agent_visibility is legacy/team-visible; private rows fail
// closed unless the caller principal exactly matches the owner.
func ResolvePrincipal(caller KeycardContext, mem *models.Memory) bool {
	if mem == nil {
		return false
	}
	visibility := strings.TrimSpace(mem.AgentVisibility)
	switch visibility {
	case "":
		return true
	case models.AgentVisibilityShared:
		return true
	case models.AgentVisibilityPrivate:
		owner := strings.TrimSpace(mem.OwnerPrincipal)
		ownerKind := strings.TrimSpace(mem.OwnerPrincipalKind)
		principal := strings.TrimSpace(caller.Principal)
		principalKind := strings.TrimSpace(caller.PrincipalKind)
		return owner != "" &&
			ownerKind != "" &&
			principal != "" &&
			principalKind != "" &&
			owner == principal &&
			ownerKind == principalKind
	default:
		return false
	}
}

// NormalizePrivacyScope applies the DB default used for legacy rows.
func NormalizePrivacyScope(memoryScope string) string {
	if memoryScope == "" {
		return ScopeProject
	}
	return memoryScope
}

// FilterMemories returns the subset of mems that the caller may see per
// scope.Resolve. It is a pure helper for read-path callers that have already
// fetched a raw slice from the store and need to remove private-scope rows
// the caller cannot access.
//
// Callers build the KeycardContext from auth.IdentityFrom(ctx) and pass it
// here so this package stays free of auth and context dependencies.
//
// When memoryScope is empty, the defaulted value "project" is used (matching
// the DB column DEFAULT 'project').
//
// Anti-stub: returning mems unchanged fails private-scope cross-workstation
// cases that expect the private row to be absent.
func FilterMemories(caller KeycardContext, mems []*models.Memory) []*models.Memory {
	return FilterMemoriesWithOptions(caller, mems, MemoryVisibilityOptions{ApplyPrivacyScope: true})
}

// FilterMemoriesWithOptions returns the subset of mems visible under
// ResolveMemory and the supplied visibility options.
func FilterMemoriesWithOptions(caller KeycardContext, mems []*models.Memory, opts MemoryVisibilityOptions) []*models.Memory {
	out := make([]*models.Memory, 0, len(mems))
	for _, mem := range mems {
		if ResolveMemory(caller, mem, opts) {
			out = append(out, mem)
		}
	}
	return out
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
//	ScopePrivate → per the keycard identity invariant (FR-F1 AMEND 2026-05-25):
//	   1. caller.WorkstationID empty               → false (caller unknown)
//	   2. memorySource.WorkstationID empty         → false (memory unknown)
//	   3. WorkstationID mismatch                   → false (different workstations)
//	   4. caller.SessionID non-empty (session-required branch):
//	        require caller.SessionID in memorySource.Sessions
//	   5. caller.SessionID empty (workstation-only-suffices branch):
//	        workstation match alone is enough
//	unknown      → false (fail-closed against unrecognised scope strings)
//
// The workstation-only-suffices branch is the spec text "When the writing
// keycard exposes only `workstation_id` (no session), workstation match alone
// suffices". T003 shipped a narrow interpretation that disabled this branch;
// T003b restores it per spec FR-F1 AMEND 2026-05-25.
//
// Anti-stub: a `return true` body fails private-scope cases that expect FALSE
// (workstation mismatch, empty workstation, session mismatch when session
// required).
func Resolve(caller KeycardContext, memoryScope string, memorySource SourceMeta) bool {
	switch memoryScope {
	case ScopeGlobal, ScopeShared, ScopeProject:
		return true
	case ScopePrivate:
		if caller.WorkstationID == "" || memorySource.WorkstationID == "" {
			return false
		}
		if caller.WorkstationID != memorySource.WorkstationID {
			return false
		}
		if caller.SessionID == "" {
			// Workstation-only-suffices branch: workstation match without
			// caller session_id is enough per spec FR-F1 AMEND 2026-05-25.
			return true
		}
		// Session-required branch: session_id MUST appear in the memory's
		// recorded sessions.
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
