package feedback

import (
	"context"
	"math"

	"github.com/rs/zerolog/log"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// Updater adjusts memory importance and Thompson Sampling priors based on
// citation evidence from DetectCitations.
type Updater struct {
	memoryStore *gormdb.MemoryStore
}

// NewUpdater creates a feedback Updater.
func NewUpdater(memStore *gormdb.MemoryStore) *Updater {
	return &Updater{memoryStore: memStore}
}

// Update processes citation results: for each result, updates the memory's
// ts_alpha (if cited) or ts_beta (if not cited but was injected).
// Uses batch SQL updates to avoid N+1 queries.
func (u *Updater) Update(ctx context.Context, results []CitationResult) {
	var citedIDs, uncitedIDs []int64
	for _, res := range results {
		if res.Cited {
			citedIDs = append(citedIDs, res.MemoryID)
		} else {
			uncitedIDs = append(uncitedIDs, res.MemoryID)
		}
	}
	if len(citedIDs) > 0 {
		if err := u.memoryStore.BatchIncrementCited(ctx, citedIDs); err != nil {
			log.Error().Err(err).Msg("feedback: batch increment cited failed")
		}
	}
	if len(uncitedIDs) > 0 {
		if err := u.memoryStore.BatchIncrementUncited(ctx, uncitedIDs); err != nil {
			log.Error().Err(err).Msg("feedback: batch increment uncited failed")
		}
	}
}

// calculateImportance applies diminishing returns: base * log(1 + citation_count).
// Capped at 1.0 to keep importance normalized.
// Retained for unit testing; production updates use BatchIncrementCited SQL.
func calculateImportance(currentBase float64, citationCount int) float64 {
	if currentBase <= 0 {
		currentBase = 0.5
	}
	if citationCount <= 0 {
		return currentBase
	}
	scaled := currentBase * math.Log(1.0+float64(citationCount))
	if scaled > 1.0 {
		scaled = 1.0
	}
	if scaled < currentBase {
		return currentBase
	}
	return scaled
}

