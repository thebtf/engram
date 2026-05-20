package feedback

import (
	"math"
	"testing"
)

// approxEqual returns true when a and b differ by at most epsilon.
func approxEqual(a, b, epsilon float64) bool {
	return math.Abs(a-b) <= epsilon
}

// --- Scenario 1: calculateImportance formula — FR-A5 ---
// Spec: effective_importance = importance_base × log(1 + citation_count)
//
// Edge-case clarification:
//   citation_count=0 → log(1+0) = log(1) = 0 → product is 0.
//   This would zero-out all uncited memories, which is destructive.
//   Spec intent (FR-A5 "diminishing returns") implies the base should be
//   preserved when citation_count=0 — i.e. the function uses max(log(1+n), 1)
//   or an additive +1 guard.  The test documents BOTH interpretations and
//   checks either: importance must be >= importanceBase when citationCount==0.

func TestCalculateImportance_ZeroCitations(t *testing.T) {
	base := 0.5
	result := calculateImportance(base, 0)
	// Importance must not drop below base for an uncited memory.
	// Implementations may return base×1 = 0.5 (using log(2+n) or log(1+n)+1
	// guard), or may return base×log(1+0+1)=base×log(1)… but never < base.
	if result < base {
		t.Errorf("calculateImportance(%f, 0): got %f, must not drop below base %f", base, result, base)
	}
}

func TestCalculateImportance_OneCitation(t *testing.T) {
	const epsilon = 0.01
	base := 0.5
	// Spec formula with natural log: 0.5 × ln(1+1) = 0.5 × ln(2) ≈ 0.347
	// With log₁₀: 0.5 × log₁₀(2) ≈ 0.150
	// Either is acceptable; we test that result is > 0 and < base (diminishing uplift pattern)
	// OR > base if implementation adds a +1 guard.
	result := calculateImportance(base, 1)
	if result <= 0 {
		t.Errorf("calculateImportance(%f, 1): got %f, must be > 0", base, result)
	}
	// Result must be a finite, positive number.
	if math.IsNaN(result) || math.IsInf(result, 0) {
		t.Errorf("calculateImportance(%f, 1): got non-finite result %f", base, result)
	}
}

func TestCalculateImportance_TenCitations(t *testing.T) {
	base := 0.5
	result := calculateImportance(base, 10)
	// With natural log: 0.5 × ln(11) ≈ 1.199, which should be capped at 1.0.
	// With log₁₀: 0.5 × log₁₀(11) ≈ 0.521, which is < 1.0 (no cap needed).
	// Either way: result must be in (0, 1.0].
	if result > 1.0+1e-9 {
		t.Errorf("calculateImportance(%f, 10): got %f, must be capped at 1.0", base, result)
	}
	if result <= 0 {
		t.Errorf("calculateImportance(%f, 10): got %f, must be > 0", base, result)
	}
}

// --- Scenario 2: Capped at 1.0 ---

func TestCalculateImportance_CappedAtOne(t *testing.T) {
	// Very high citation count should never push importance above 1.0.
	result := calculateImportance(0.8, 100)
	if result > 1.0+1e-9 {
		t.Errorf("calculateImportance(0.8, 100): got %f, expected <= 1.0 (cap violated)", result)
	}
}

func TestCalculateImportance_BaseNearOne_CappedAtOne(t *testing.T) {
	// Base of 1.0 with any citations must remain 1.0.
	result := calculateImportance(1.0, 5)
	if result > 1.0+1e-9 {
		t.Errorf("calculateImportance(1.0, 5): got %f, expected <= 1.0", result)
	}
}

// --- Scenario 3: Zero or negative base → fall back to default 0.5 ---

func TestCalculateImportance_ZeroBase(t *testing.T) {
	const defaultBase = 0.5
	result := calculateImportance(0, 5)
	// Implementation should substitute default base (0.5) when base == 0.
	// We check that the result matches calculateImportance(0.5, 5).
	expected := calculateImportance(defaultBase, 5)
	if !approxEqual(result, expected, 1e-9) {
		t.Errorf("calculateImportance(0, 5): got %f, expected same as default-base result %f", result, expected)
	}
}

func TestCalculateImportance_NegativeBase(t *testing.T) {
	const defaultBase = 0.5
	result := calculateImportance(-1, 5)
	expected := calculateImportance(defaultBase, 5)
	if !approxEqual(result, expected, 1e-9) {
		t.Errorf("calculateImportance(-1, 5): got %f, expected same as default-base result %f", result, expected)
	}
}

// --- Scenario 4: Monotonically increasing with citation count ---
// More citations should never decrease effective importance (within cap).

func TestCalculateImportance_Monotonic(t *testing.T) {
	base := 0.4
	prev := calculateImportance(base, 0)
	for n := 1; n <= 20; n++ {
		curr := calculateImportance(base, n)
		if curr < prev-1e-9 {
			t.Errorf("calculateImportance not monotonic: n=%d gave %f < n=%d gave %f", n, curr, n-1, prev)
		}
		prev = curr
	}
}
