// Package models — write-lint domain types.
// T032 (engram vNext Milestone F TG5): LintSignal, LintSignalType enum,
// ResolutionOption, ResolutionToken, WriteResolutionPhase1Response,
// WriteResolutionPhase2Request/Response per spec §FR-F5.
package models

// LintSignalType enumerates the six quality-signal categories emitted by
// the write-lint orchestrator. JSON values are used directly in MCP responses.
type LintSignalType string

const (
	// LintSignalPossibleDuplicate fires when Jaccard+cosine similarity >= 0.85.
	LintSignalPossibleDuplicate LintSignalType = "possible_duplicate"

	// LintSignalPossibleConflict fires when DetectConflict finds a contradicting
	// existing memory via CorrectionPatterns or opposing-change detection.
	LintSignalPossibleConflict LintSignalType = "possible_conflict"

	// LintSignalSupersessionCandidate fires when concept overlap + file overlap
	// suggests the new memory supersedes an older one.
	LintSignalSupersessionCandidate LintSignalType = "supersession_candidate"

	// LintSignalMissingProvenance fires when no source_agent or session context
	// is supplied and the content has no fact-basis signals.
	LintSignalMissingProvenance LintSignalType = "missing_provenance"

	// LintSignalLowConfidenceWithoutBasis fires when confidence < 0.3 and no
	// supporting evidence is provided.
	LintSignalLowConfidenceWithoutBasis LintSignalType = "low_confidence_without_basis"

	// LintSignalPrivateDataRisk fires when the redaction layer matched a rule
	// (content was modified), indicating PII/credential risk.
	LintSignalPrivateDataRisk LintSignalType = "private_data_risk"
)

// LintSignal carries a single quality signal from Phase 1.
// Fields are spec §FR-F5 response schema verbatim.
type LintSignal struct {
	Type             LintSignalType `json:"type"`
	SimilarMemoryID  *int64         `json:"similar_memory_id,omitempty"`
	SimilarityScore  float64        `json:"similarity_score,omitempty"`
	SimilarityMethod string         `json:"similarity_method,omitempty"`
	// Conflict fields
	ConflictingMemoryID *int64 `json:"conflicting_memory_id,omitempty"`
	ConflictType        string `json:"conflict_type,omitempty"`
	Reason              string `json:"reason,omitempty"`
	// Supersession fields
	OlderMemoryID *int64 `json:"older_memory_id,omitempty"`
	Evidence      string `json:"evidence,omitempty"`
}

// ResolutionOption is a single actionable choice presented in Phase 1.
// Per spec §FR-F5 response schema.
type ResolutionOption struct {
	Option   string `json:"option"`
	MemoryID *int64 `json:"memory_id,omitempty"`
	Result   string `json:"result"`
}

// ResolutionToken wraps the string token minted by Phase 1, plus metadata.
// The token prefix "wlrt_" is the canonical write-lint resolution token prefix.
type ResolutionToken struct {
	Token   string `json:"token"`
	TTLSecs int    `json:"ttl_secs,omitempty"`
}

// WriteResolutionPhase1Response is the complete Phase 1 response shape per
// spec §FR-F5. Returned when lint signals fire on store_memory.
// When no signals fire, the orchestrator commits immediately and returns
// Stored=true with MemoryID populated — callers receive the same id/storage/
// scope surface as the legacy store_memory response (NFR-F1, finding 6 fix).
type WriteResolutionPhase1Response struct {
	// Stored is false when signals fired and a token was minted;
	// true when no signals fired and the memory was committed immediately.
	Stored bool `json:"stored"`
	// MemoryID is populated when Stored=true (no-signal immediate-commit path).
	// Zero when Stored=false (token path).
	MemoryID int64 `json:"memory_id,omitempty"`
	// StorageID mirrors MemoryID for legacy-shape compatibility (NFR-F1).
	StorageID int64 `json:"storage_id,omitempty"`
	// LintSignals lists all quality signals detected (populated when Stored=false).
	LintSignals []LintSignal `json:"lint_signals,omitempty"`
	// ResolutionOptions lists the actionable choices the caller can take.
	ResolutionOptions []ResolutionOption `json:"resolution_options,omitempty"`
	// ResolutionToken is the opaque token bound to this dry-run state.
	// 10-minute default TTL per ADR-F-002. Empty when Stored=true.
	ResolutionToken string `json:"resolution_token,omitempty"`
}

// WriteResolutionPhase2Request carries the caller-chosen resolution for Phase 2.
// Passed via the store_memory MCP tool parameters.
type WriteResolutionPhase2Request struct {
	// ResolutionToken is the token returned by Phase 1. Required.
	ResolutionToken string `json:"resolution_token"`
	// Option is the chosen resolution option key (e.g., "merge_with", "abort").
	Option string `json:"option"`
	// TargetMemoryID is required for option="merge_with", "supersede",
	// "link_contradiction"; ignored for "ignore_signals", "abort",
	// "mark_candidate".
	TargetMemoryID *int64 `json:"target_memory_id,omitempty"`
}

// WriteResolutionPhase2Response is the Phase 2 commit result per spec §FR-F5.
type WriteResolutionPhase2Response struct {
	// Stored is true when the memory was committed (not for abort).
	Stored bool `json:"stored"`
	// MemoryID is the ID of the committed or updated memory.
	MemoryID int64 `json:"memory_id,omitempty"`
	// ActionTaken is the audit_log.action value written per spec §FR-F5 enum.
	ActionTaken string `json:"action_taken"`
	// AuditLogID is the ID of the created audit_log entry.
	AuditLogID int64 `json:"audit_log_id,omitempty"`
}
