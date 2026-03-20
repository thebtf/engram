// Package pattern provides pattern detection and recognition functionality.
package pattern

import (
	"context"
	"math"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/db/gorm"
)

// HybridDecay computes the temporal decay multiplier for a pattern.
// Three phases:
//   - Grace period (0-7 days): no decay, returns 1.0.
//   - Gaussian decay (7-60 days): smooth exponential decay reaching ~0.5 at 37 days.
//   - Linear terminal (60-90 days): linear ramp to zero.
func HybridDecay(lastSeenAt time.Time) float64 {
	ageDays := time.Since(lastSeenAt).Hours() / 24

	const (
		offset        = 7.0
		scale         = 30.0
		decay         = 0.5
		criticalPoint = 60.0
	)

	if ageDays <= offset {
		return 1.0
	}

	variance := -(scale * scale) / (2 * math.Log(decay))

	if ageDays <= criticalPoint {
		return math.Exp(-math.Pow(ageDays-offset, 2) / (2 * variance))
	}

	// Linear terminal phase: ramp from Gaussian value at criticalPoint to 0 over 30 days.
	gaussianAtCrit := math.Exp(-math.Pow(criticalPoint-offset, 2) / (2 * variance))
	linearSlope := gaussianAtCrit / 30.0
	return math.Max(0.0, gaussianAtCrit-linearSlope*(ageDays-criticalPoint))
}

// RunDecay processes all active patterns, computing dynamic quality and deprecating low-quality ones.
// Returns the count of deprecated patterns.
func RunDecay(ctx context.Context, patternStore *gorm.PatternStore) (int, error) {
	// Retrieve all active patterns (use a high limit to get them all).
	const maxPatterns = 10000
	patterns, err := patternStore.GetActivePatterns(ctx, maxPatterns, 0, "")
	if err != nil {
		return 0, err
	}

	if len(patterns) == 0 {
		return 0, nil
	}

	// Find max frequency for normalization.
	maxFreq := 1
	for _, p := range patterns {
		if p.Frequency > maxFreq {
			maxFreq = p.Frequency
		}
	}

	deprecated := 0
	for _, p := range patterns {
		baseQuality := QualityScore(p.Frequency, p.Confidence, len(p.Projects), maxFreq)

		var lastSeen time.Time
		if p.LastSeenAt != "" {
			lastSeen, _ = time.Parse(time.RFC3339, p.LastSeenAt)
		}
		if lastSeen.IsZero() && p.CreatedAt != "" {
			lastSeen, _ = time.Parse(time.RFC3339, p.CreatedAt)
		}

		decayMultiplier := HybridDecay(lastSeen)
		dynamicQuality := baseQuality * decayMultiplier

		if dynamicQuality < 0.10 {
			if err := patternStore.MarkPatternDeprecated(ctx, p.ID); err != nil {
				log.Warn().Err(err).Int64("pattern_id", p.ID).Msg("Failed to deprecate pattern")
				continue
			}
			deprecated++
		}
	}

	log.Info().Int("deprecated", deprecated).Int("total", len(patterns)).Msg("Pattern decay complete")
	return deprecated, nil
}
