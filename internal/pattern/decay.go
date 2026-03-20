// Package pattern provides pattern detection and recognition functionality.
package pattern

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/db/gorm"
)

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
		var lastSeen time.Time
		if p.LastSeenAt != "" {
			lastSeen, _ = time.Parse(time.RFC3339, p.LastSeenAt)
		}
		if lastSeen.IsZero() && p.CreatedAt != "" {
			lastSeen, _ = time.Parse(time.RFC3339, p.CreatedAt)
		}

		daysSince := time.Since(lastSeen).Hours() / 24
		dynamicQuality := DynamicQuality(p.Frequency, p.Confidence, len(p.Projects), maxFreq, daysSince)

		if dynamicQuality < DeprecateThreshold {
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
