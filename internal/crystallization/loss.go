// Package crystallization provides session-end decision and pattern extraction.
package crystallization

// DetectLoss reports whether candidate is a strict information-subset of existing —
// i.e., candidate DROPS meaningful information that existing already contained.
//
// Rationale: MemTier 62% compaction-discontinuity warning (PRD §392-395).
// When a candidate would overwrite an existing decision but carries less information,
// the loss is flagged so the caller can route to review rather than silent overwrite.
//
// A candidate is considered lossy when ANY of the following is true:
//   - existing.Text is non-empty AND candidate.Text is empty.
//   - existing has Evidence entries AND candidate has zero Evidence entries.
//   - existing has a non-zero Confidence AND candidate's Confidence is zero.
//
// When candidate is a superset or equal, DetectLoss returns false.
func DetectLoss(existing, candidate ExtractedDecision) bool {
	// Loss condition 1: existing carries text but candidate does not.
	if existing.Text != "" && candidate.Text == "" {
		return true
	}

	// Loss condition 2: existing has evidence but candidate has none.
	if len(existing.Evidence) > 0 && len(candidate.Evidence) == 0 {
		return true
	}

	// Loss condition 3: existing carries a confidence score but candidate zeroed it out.
	if existing.Confidence != 0 && candidate.Confidence == 0 {
		return true
	}

	return false
}
