package lifecycle

import "math"

// ComputeRetrievability returns the current recall probability given
// stability (in days) and elapsed time since last retrieval (in days).
// Formula: R = (1 + elapsed × 0.9 / stability)^(-1)
// Source: FSRS-6 / Oblivion (arXiv:2604.00131).
func ComputeRetrievability(stability, elapsedDays float64) float64 {
	if stability <= 0 {
		return 0
	}
	if elapsedDays <= 0 {
		return 1.0
	}
	r := math.Pow(1+elapsedDays*0.9/stability, -1)
	return clamp(r, 0, 1)
}

// ComputeStability calculates the effective stability for a memory based on
// its tier, epistemic type, and citation count.
// stability = base × tier_weight × epistemic_weight × (1 + citations × 0.3)
func ComputeStability(baseStability float64, tier, epistemicType string, citationCount int) float64 {
	if baseStability <= 0 {
		baseStability = 30.0
	}
	tw := TierWeight(tier)
	ew := EpistemicWeight(epistemicType)
	citationFactor := 1.0 + float64(citationCount)*0.3
	return baseStability * tw * ew * citationFactor
}

// Reconsolidate updates stability after a successful retrieval.
// stability_new = stability × (1 + max(0, retrievability - 0.5) × 1.5)
// Source: Nader 2000 reconsolidation principle.
func Reconsolidate(currentStability, retrievability float64) float64 {
	if currentStability <= 0 {
		return 30.0
	}
	boost := math.Max(0, retrievability-0.5) * 1.5
	return currentStability * (1 + boost)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
