// Package models contains domain models for engram.
package models

import "time"

const (
	// AgentVisibilityPrivate means only the owning principal should see the
	// memory once principal-aware recall filtering is enabled.
	AgentVisibilityPrivate = "private"
	// AgentVisibilityShared preserves the current team-readable default while
	// still attributing the writer.
	AgentVisibilityShared = "shared"
)

// IsValidAgentVisibility reports whether v is an accepted principal-memory
// visibility value. Empty is handled by callers as "not specified".
func IsValidAgentVisibility(v string) bool {
	switch v {
	case AgentVisibilityPrivate, AgentVisibilityShared:
		return true
	default:
		return false
	}
}

// Memory represents a user-facing persistent note stored in the memories table.
// Memories are project-scoped and support full-text search via a GENERATED tsvector column
// (search_vector) that is NOT exposed here — it is a read-only computed column managed by
// the database and not part of the domain interface.
//
// Migration 088 creates this table; migration 105 adds lifecycle columns for vNext Milestone A;
// migration 110 adds lifecycle metadata for Milestone B; migration 125 adds privacy_scope
// + source_sessions[] for vNext Milestone F (TG1 / T001+T002).
type Memory struct {
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
	LastRetrievedAt *time.Time `json:"last_retrieved_at,omitempty"`
	LastConfirmed   *time.Time `json:"last_confirmed,omitempty"`
	ReviewAfter     *time.Time `json:"review_after,omitempty"`
	ValidFrom       *time.Time `json:"valid_from,omitempty"`
	ValidUntil      *time.Time `json:"valid_until,omitempty"`
	Project         string     `json:"project"`
	Content         string     `json:"content"`
	SourceAgent     string     `json:"source_agent,omitempty"`
	EditedBy        string     `json:"edited_by,omitempty"`
	Status          string     `json:"status,omitempty"`
	Tier            string     `json:"tier,omitempty"`
	EpistemicType   string     `json:"epistemic_type,omitempty"`
	Defeasibility   string     `json:"defeasibility,omitempty"`
	PromotionTarget string     `json:"promotion_target,omitempty"`
	// PrivacyScope is one of 'private' | 'project' | 'shared' | 'global'.
	// Added by migration 125 (TG1 / T001+T002). DEFAULT 'project' for legacy rows.
	// Backward-compat: see spec.md §FR-F1 REVISE — the legacy `scope:*` tag + MCP
	// response synthesis surface remains active for 2 minor versions (until v6.7.0)
	// per RI-F2.
	PrivacyScope string   `json:"privacy_scope,omitempty"`
	Tags         []string `json:"tags"`
	// SourceSessions lists every session_id that wrote or confirmed this memory.
	// Added by migration 125 (TG1 / T001+T002) as TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[].
	// Consumed by internal/scope/filter.Resolve for private-scope visibility checks
	// per spec §FR-F1 CHK005-ADDED keycard identity invariant.
	SourceSessions []string `json:"source_sessions,omitempty"`
	// SourceWorkstationID is the writing keycard's workstation identifier
	// (api_tokens.id for SourceClient writers). Added by migration 130 (TG1 /
	// T001b + T003b, AMEND 2026-05-25). Empty string is the "unknown
	// workstation" sentinel — pre-existing rows + flag-OFF writes carry empty.
	// Consumed by scope.Resolve ScopePrivate for the keycard identity
	// invariant per spec §FR-F1 AMEND 2026-05-25 decision tree.
	SourceWorkstationID string `json:"source_workstation_id,omitempty"`
	// OwnerPrincipal is server-derived from the authenticated identity, never
	// from caller-supplied payload fields.
	OwnerPrincipal           string  `json:"owner_principal,omitempty"`
	OwnerPrincipalKind       string  `json:"owner_principal_kind,omitempty"`
	AgentVisibility          string  `json:"agent_visibility,omitempty"`
	Domain                   string  `json:"domain,omitempty"`
	ID                       int64   `json:"id"`
	SupersedesID             *int64  `json:"supersedes_id,omitempty"`
	SupersededBy             *int64  `json:"superseded_by,omitempty"`
	ImportanceBase           float64 `json:"importance_base,omitempty"`
	TsAlpha                  float64 `json:"ts_alpha,omitempty"`
	TsBeta                   float64 `json:"ts_beta,omitempty"`
	Confidence               float64 `json:"confidence,omitempty"`
	Stability                float64 `json:"stability,omitempty"`
	Retrievability           float64 `json:"retrievability,omitempty"`
	Version                  int     `json:"version"`
	CitationCount            int     `json:"citation_count,omitempty"`
	InjectionCount           int     `json:"injection_count,omitempty"`
	AccessCount              int     `json:"access_count,omitempty"`
	RecurrenceCount          int     `json:"recurrence_count,omitempty"`
	ConsecutiveCitationCount int     `json:"consecutive_citation_count,omitempty"`
}
