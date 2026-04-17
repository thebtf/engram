// Package dedup provides shared near-duplicate detection for observations.
// In v5, vector storage was removed so dedup always returns ActionAdd (no-op).
package dedup

// Action represents the dedup decision.
type Action string

const (
	ActionAdd    Action = "ADD"    // New observation, no similar exists
	ActionUpdate Action = "UPDATE" // Supersede existing observation
	ActionNoop   Action = "NOOP"  // Near-duplicate, skip storage
)

// Result holds the dedup check outcome.
type Result struct {
	Action          Action
	ExistingID      int64   // ID of the matched observation (0 if ADD)
	Similarity      float64 // Cosine similarity of the best match (0 if ADD)
	CrossModelBoost bool    // True when UPDATE with different agent_sources → promote to cross_model
}

// CheckCrossModelPromotion determines if a dedup UPDATE result qualifies for
// cross-model promotion. Both agent_sources must be known (non-"unknown") and
// different from each other.
//
// NOTE: In v5, CheckDuplicate always returns ActionAdd (vector dedup removed),
// so this function is currently unreachable in production code paths.
// It is retained for when dedup is restored and for unit-test coverage.
func CheckCrossModelPromotion(result *Result, newAgentSource, existingAgentSource string) bool {
	if result == nil || result.Action != ActionUpdate {
		return false
	}
	if newAgentSource == "" || newAgentSource == "unknown" {
		return false
	}
	if existingAgentSource == "" || existingAgentSource == "unknown" {
		return false
	}
	return newAgentSource != existingAgentSource
}

// DefaultNoopThreshold is the cosine similarity above which observations are considered
// near-duplicates and skipped (NOOP). Retained for call-site compatibility.
const DefaultNoopThreshold = 0.92

// DefaultUpdateThreshold is the cosine similarity above which observations supersede
// existing ones (UPDATE/EVOLVES_FROM). Retained for call-site compatibility.
const DefaultUpdateThreshold = 0.75

// CheckDuplicate always returns ActionAdd in v5 (vector storage removed).
func CheckDuplicate() (*Result, error) {
	return &Result{Action: ActionAdd}, nil
}
