// Package pattern provides pattern detection and recognition functionality.
package pattern

import "math"

// QualityScore computes a pattern's quality baseline score.
// Formula: Q_base = 0.3*log_freq + 0.5*sigmoid_conf + 0.2*diversity
//
// Components:
//   - log_freq: log2(frequency+1) / log2(maxFrequency+1), normalized 0-1.
//   - sigmoid_conf: 1 / (1 + exp(-10*(confidence-0.5))), centered at 0.5.
//   - diversity: min(projectCount, 5) / 5.0, normalized 0-1.
func QualityScore(frequency int, confidence float64, projectCount int, maxFrequency int) float64 {
	// Normalized log2 frequency (0-1)
	logFreq := 0.0
	if maxFrequency > 0 {
		logFreq = math.Log2(float64(frequency)+1) / math.Log2(float64(maxFrequency)+1)
	}

	// Sigmoid confidence centered at 0.5
	sigmoidConf := 1.0 / (1.0 + math.Exp(-10*(confidence-0.5)))

	// Project diversity capped at 5 projects (0-1)
	diversity := math.Min(float64(projectCount), 5.0) / 5.0

	return 0.3*logFreq + 0.5*sigmoidConf + 0.2*diversity
}

// TemporalDecay computes the decay multiplier based on days since last seen.
// Three phases:
//   - Grace period (0-7 days): returns 1.0, no decay.
//   - Gaussian decay (7-60 days): smooth decay from 1.0 to ~exp(-3) ≈ 0.05.
//   - Linear terminal (60-90 days): linear ramp from Gaussian end value to 0.
func TemporalDecay(daysSinceLastSeen float64) float64 {
	if daysSinceLastSeen <= 7 {
		return 1.0
	}
	if daysSinceLastSeen <= 60 {
		// Gaussian decay: normalize [7,60] to [0,1] then apply exp(-3*t²).
		t := (daysSinceLastSeen - 7) / 53.0
		return math.Exp(-3.0 * t * t)
	}
	if daysSinceLastSeen <= 90 {
		// Linear terminal from Gaussian end value to 0 over 30 days.
		gaussianEnd := math.Exp(-3.0) // ≈ 0.0498
		t := (daysSinceLastSeen - 60) / 30.0
		return gaussianEnd * (1.0 - t)
	}
	return 0.0
}

// DynamicQuality computes the final quality score with temporal decay applied.
// Q_dynamic = QualityScore(...) * TemporalDecay(daysSinceLastSeen)
func DynamicQuality(frequency int, confidence float64, projectCount int, maxFrequency int, daysSinceLastSeen float64) float64 {
	base := QualityScore(frequency, confidence, projectCount, maxFrequency)
	decay := TemporalDecay(daysSinceLastSeen)
	return base * decay
}

// Lifecycle thresholds for pattern quality management.
const (
	// DeprecateThreshold marks a pattern for soft-deprecation when Q_dynamic drops below this value.
	DeprecateThreshold = 0.10
	// DeleteThreshold marks a pattern as eligible for hard deletion when Q_dynamic reaches exactly 0.
	DeleteThreshold = 0.0
)
