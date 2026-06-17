package retrieval

import (
	"time"

	"github.com/thebtf/engram/internal/staleness"
	"github.com/thebtf/engram/pkg/models"
)

// RankingRationale carries the per-result explanation emitted when a caller
// passes include_rationale=true to recall_memory or recall(action=search).
//
// All 7 fields are sourced from existing Memory struct fields that were shipped
// in Milestones A and B. There is NO vector cosine, NO graph_distance, NO LLM
// rerank — those depend on infrastructure removed in v5 and are out of
// Milestone F scope per spec §FR-F3 REVISE 2026-05-25.
//
// T017 (engram vNext Milestone F TG3): anti-stub — replacing AssembleRationale
// with return RankingRationale{} fails 5 of 6 field assertions in the test suite.
type RankingRationale struct {
	// RecencyDays is (now - memory.CreatedAt) in fractional days.
	// Sourced from the existing CreatedAt column (Migration 088).
	RecencyDays float64 `json:"recency_days"`
	// Confidence is the memory's Confidence value (shipped Milestone B,
	// migration 110). Range [0,1]; 0.0 when not set.
	Confidence float64 `json:"confidence"`
	// CitationCount is the cumulative citation count (shipped Milestone A,
	// migration 105). Integer ≥ 0.
	CitationCount int `json:"citation_count"`
	// InjectionCount is the cumulative number of times this memory was injected
	// into a session-start context (shipped Milestone A, migration 105; written by
	// BatchIncrementInjected). Integer ≥ 0. It is the DENOMINATOR for citation rate:
	// the caller reads citation_count/injection_count to judge whether an injected
	// memory was actually used, not a bare cited count. (CR-001: revive feedback loop.)
	InjectionCount int `json:"injection_count"`
	// Tier is the memory's lifecycle tier (shipped Milestone B, migration 110).
	// One of working/episodic/semantic/procedural; empty string when not set.
	Tier string `json:"tier,omitempty"`
	// SubstringMatch reports whether queryText was found as a case-insensitive
	// substring of memory.Content. True only when queryText is non-empty and
	// the match was confirmed by the handler.
	SubstringMatch bool `json:"substring_match"`
	// FiltersApplied lists the active filter descriptors in the form
	// "key=value", e.g. ["project=engram","confidence_min=0.6"]. Empty slice
	// when no non-default filters were in effect.
	FiltersApplied []string `json:"filters_applied"`
	// Stale flags this result as a stale candidate (rank-3): its content carries
	// relative-time language ("currently", "recently", "last week", …) AND it is
	// older than the freshness window, so the fact it asserts may have drifted. This
	// is a HINT to re-verify before trusting, never a hide/drop signal. Derived from
	// live fields only (content + created_at) — NOT the dormant lifecycle columns.
	Stale bool `json:"stale,omitempty"`
	// StaleTerms lists the relative-time phrases that triggered Stale (lower-cased,
	// first-seen order). Empty/omitted when Stale is false.
	StaleTerms []string `json:"stale_terms,omitempty"`
}

// AssembleRationale builds a RankingRationale from the fields available on
// the live v5 Memory model. Called per-result when include_rationale=true.
//
// Parameters:
//   - memory: the retrieved Memory row (must not be nil).
//   - queryText: the recall query string used; may be empty.
//   - matched: true when the caller confirmed queryText was found in content.
//     The caller (handler) is responsible for this check so AssembleRationale
//     remains a pure function with no string scanning.
//   - filters: descriptors of active non-default filters, e.g.
//     ["confidence_min=0.7","include_superseded=true"]. Nil is normalised to
//     an empty slice so JSON output is always [] rather than null.
//
// NFR-F3: this function performs only arithmetic on already-loaded fields.
// Measured overhead is well within the ≤5ms p95 budget. See benchmark in
// rationale_test.go (BenchmarkAssembleRationale).
func AssembleRationale(memory *models.Memory, queryText string, matched bool, filters []string) RankingRationale {
	if memory == nil {
		return RankingRationale{}
	}
	recencyDays := time.Since(memory.CreatedAt).Hours() / 24
	if recencyDays < 0 {
		recencyDays = 0
	}

	// Normalise nil filters to empty slice so JSON marshals as [] not null.
	appliedFilters := filters
	if appliedFilters == nil {
		appliedFilters = []string{}
	}

	// Rank-3 staleness hint: flag a result whose content uses relative-time language
	// AND is older than the freshness window, so the agent re-verifies before trusting.
	// One regex scan over content — well within the ≤5ms NFR-F3 budget. Live fields only.
	now := time.Now()
	var stale bool
	var staleTerms []string
	if staleness.IsStaleCandidate(memory.Content, memory.CreatedAt, now, 0) {
		stale = true
		staleTerms = staleness.DetectRelativeTime(memory.Content)
	}

	return RankingRationale{
		RecencyDays:    recencyDays,
		Confidence:     memory.Confidence,
		CitationCount:  memory.CitationCount,
		InjectionCount: memory.InjectionCount,
		Tier:           memory.Tier,
		SubstringMatch: queryText != "" && matched,
		FiltersApplied: appliedFilters,
		Stale:          stale,
		StaleTerms:     staleTerms,
	}
}
