package scope

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/thebtf/engram/pkg/models"
)

// MemoryReadAccess is caller-derived policy, never a project or principal grant.
// Project and current lifecycle predicates remain the responsibility of each reader.
type MemoryReadAccess struct {
	Caller     KeycardContext
	Visibility MemoryVisibilityOptions
}

// MemoryRef pins the selected version and exact UTF-8 body digest. Rollback can
// restore an older version number, so version equality alone is not body identity.
type MemoryRef struct {
	ID            int64
	Version       int
	ContentDigest string
}

// MemoryContentDigestSQL is the database equivalent of MemoryContentDigest for
// memories alias m. PostgreSQL's built-in SHA-256 needs no extension or schema.
const MemoryContentDigestSQL = "encode(sha256(convert_to(m.content, 'UTF8')), 'hex')"

func MemoryContentDigest(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

func MemoryReference(memory *models.Memory) MemoryRef {
	return MemoryRef{ID: memory.ID, Version: memory.Version, ContentDigest: MemoryContentDigest(memory.Content)}
}

func (ref MemoryRef) Matches(memory *models.Memory) bool {
	return memory != nil && ref.ID == memory.ID && ref.Version == memory.Version &&
		ref.ContentDigest == MemoryContentDigest(memory.Content)
}

// MemoryPrivacyAccessSQL projects Resolve's legacy privacy rules for alias m.
const MemoryPrivacyAccessSQL = `(
		CASE COALESCE(NULLIF(m.privacy_scope, ''), 'project')
			WHEN 'project' THEN TRUE
			WHEN 'shared' THEN TRUE
			WHEN 'global' THEN TRUE
			WHEN 'private' THEN
				? <> ''
				AND m.source_workstation_id <> ''
				AND m.source_workstation_id = ?
				AND (? = '' OR ? = ANY(m.source_sessions))
			ELSE FALSE
		END
	)`

// MemoryPrincipalAccessSQL projects ResolvePrincipal and domain read ownership
// for alias m. It is shared by task, retained and ordinary search readers.
const MemoryPrincipalAccessSQL = `(
		btrim(m.agent_visibility) = ''
		OR btrim(m.agent_visibility) = 'shared'
		OR (
			btrim(m.agent_visibility) = 'private'
			AND btrim(m.owner_principal) <> ''
			AND btrim(m.owner_principal_kind) <> ''
			AND ? <> ''
			AND ? <> ''
			AND btrim(m.owner_principal) = ?
			AND btrim(m.owner_principal_kind) = ?
		)
	)
	AND (
		btrim(m.domain) = ''
		OR (
			btrim(m.owner_principal) <> ''
			AND btrim(m.owner_principal_kind) <> ''
			AND ? <> ''
			AND ? IN ('human', 'agent', 'service')
			AND btrim(m.owner_principal) = ?
			AND btrim(m.owner_principal_kind) = ?
		)
	)
`

// MemoryReadAccessSQL projects ResolveMemory before ORDER BY/LIMIT or body
// materialization. All column references use the fixed memories alias m.
func MemoryReadAccessSQL(access MemoryReadAccess) (string, []any) {
	caller, opts := access.Caller, access.Visibility
	principal, kind := strings.TrimSpace(caller.Principal), strings.TrimSpace(caller.PrincipalKind)
	predicate := "(" + MemoryPrincipalAccessSQL + ")"
	args := []any{principal, kind, principal, kind, principal, kind, principal, kind}
	if opts.ApplyPrivacyScope {
		predicate += " AND " + MemoryPrivacyAccessSQL
		args = append(args, caller.WorkstationID, caller.WorkstationID, caller.SessionID, caller.SessionID)
		if len(opts.IncludeScopes) > 0 {
			// Fixed ordering keeps the projection deterministic; unknown or false-only
			// selections deny all, just as ResolveMemory does.
			var placeholders []string
			for _, value := range []string{ScopePrivate, ScopeProject, ScopeShared, ScopeGlobal} {
				if opts.IncludeScopes[value] {
					placeholders = append(placeholders, "?")
					args = append(args, value)
				}
			}
			if len(placeholders) == 0 {
				predicate += " AND FALSE"
			} else {
				predicate += " AND COALESCE(NULLIF(m.privacy_scope, ''), 'project') IN (" + strings.Join(placeholders, ",") + ")"
			}
		}
	}
	return predicate, args
}
