package feedback

import "testing"

// The rank-6 outcome→importance_base policy lives in multiplierTable.importanceFactor.
// The SQL that applies it is covered by the gorm integration tests (DATABASE_DSN-gated);
// these unit tests pin the POLICY: the ordering and the backward-compat invariants that a
// future edit could silently break.

func TestMultiplierTable_ImportanceFactorOrdering(t *testing.T) {
	success := multiplierTable["success"].importanceFactor
	partial := multiplierTable["partial"].importanceFactor
	failure := multiplierTable["failure"].importanceFactor
	abandoned := multiplierTable["abandoned"].importanceFactor

	// A successful session must promote a cited memory for future injection at least as hard
	// as a partial one, which in turn must promote at least as hard as a failure.
	if !(success >= partial && partial > failure && failure > abandoned) {
		t.Errorf("importanceFactor must be ordered success(%v) >= partial(%v) > failure(%v) > abandoned(%v)",
			success, partial, failure, abandoned)
	}
	// Abandoned must be a no-op on importance_base.
	if abandoned != 0.0 {
		t.Errorf("abandoned importanceFactor must be 0 (no-op), got %v", abandoned)
	}
	// Success must promote strictly harder than the neutral baseline (the whole point of rank-6).
	if success <= partial {
		t.Errorf("success importanceFactor (%v) must exceed partial/neutral (%v) for outcome to matter", success, partial)
	}
}

// Backward-compat invariant: the neutral default ("") and "partial" must keep importanceFactor
// == 1.0, because the store SQL reduces to the exact historical importance_base formula only at
// factor 1.0. If either drifts off 1.0, every outcome-less session (the common case) silently
// changes its importance_base growth — a regression the SQL math test cannot catch on its own.
func TestMultiplierTable_NeutralFactorIsExactlyOne(t *testing.T) {
	if got := multiplierTable[""].importanceFactor; got != 1.0 {
		t.Errorf(`default "" importanceFactor must be exactly 1.0 (historical-formula parity), got %v`, got)
	}
	if got := multiplierTable["partial"].importanceFactor; got != 1.0 {
		t.Errorf("partial importanceFactor must be exactly 1.0, got %v", got)
	}
}

// Unknown outcomes must fall back to the neutral "" row (UpdateWithOutcome does this), so an
// unrecognised outcome string can never accidentally pick a non-neutral importanceFactor.
func TestMultiplierTable_UnknownOutcomeFallsBackToNeutral(t *testing.T) {
	_, ok := multiplierTable["totally-unknown-outcome"]
	if ok {
		t.Fatal("precondition: unknown outcome should not be a table key")
	}
	// Mirror UpdateWithOutcome's fallback and confirm it lands on neutral factor 1.0.
	mult, ok := multiplierTable["totally-unknown-outcome"]
	if !ok {
		mult = multiplierTable[""]
	}
	if mult.importanceFactor != 1.0 {
		t.Errorf("unknown-outcome fallback importanceFactor must be neutral 1.0, got %v", mult.importanceFactor)
	}
}

// Guard the failure row: it must still FIRE the cited path (citedAlpha > 0), otherwise the
// reduced importanceFactor would never be applied (the dispatch is gated on citedAlpha > 0).
// This pins the interaction between the two fields that makes failure-sensitivity actually reach
// importance_base.
func TestMultiplierTable_FailureStillFiresCitedPath(t *testing.T) {
	failure := multiplierTable["failure"]
	if failure.citedAlpha <= 0 {
		t.Errorf("failure citedAlpha must be > 0 so the cited path fires and importanceFactor %v is applied; got citedAlpha=%v",
			failure.importanceFactor, failure.citedAlpha)
	}
	if failure.importanceFactor >= multiplierTable[""].importanceFactor {
		t.Errorf("failure importanceFactor (%v) must be below neutral (%v) for failure to dampen promotion",
			failure.importanceFactor, multiplierTable[""].importanceFactor)
	}
}
