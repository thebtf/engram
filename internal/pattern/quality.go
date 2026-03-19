// Package pattern provides pattern detection and recognition functionality.
package pattern

import "math"

// QualityScore computes a pattern's quality based on frequency, confidence, and diversity.
// Returns a value between 0.0 and 1.0.
//
// Components:
//   - Frequency (30% weight): Log-scaled frequency normalized against the maximum.
//   - Confidence (50% weight): Sigmoid-transformed confidence centered at 0.5.
//   - Diversity (20% weight): Project count normalized to a maximum of 10.
func QualityScore(frequency int, confidence float64, projectCount int, maxFrequency int) float64 {
	// Log-scaled frequency (30% weight)
	maxF := float64(maxFrequency)
	if maxF < 1 {
		maxF = 1
	}
	sFreq := math.Log10(1+float64(frequency)) / math.Log10(1+maxF)

	// Sigmoid confidence centered at 0.5 (50% weight)
	k := 10.0
	sConf := 1.0 / (1.0 + math.Exp(-k*(confidence-0.5)))

	// Diversity: project count normalized to max 10 (20% weight)
	sDiv := math.Min(float64(projectCount)/10.0, 1.0)

	return 0.3*sFreq + 0.5*sConf + 0.2*sDiv
}
