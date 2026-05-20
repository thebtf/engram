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
// Also recalculates importance_base = base * log(1 + citation_count).
func (u *Updater) Update(ctx context.Context, results []CitationResult) {
	for _, res := range results {
		mem, err := u.memoryStore.Get(ctx, res.MemoryID)
		if err != nil {
			log.Error().Err(err).Int64("memory_id", res.MemoryID).Msg("feedback: failed to load memory")
			continue
		}

		if res.Cited {
			// Cited: increment alpha, citation_count, recalculate importance
			mem.TsAlpha += 1.0
			mem.CitationCount += 1
			mem.ImportanceBase = calculateImportance(mem.ImportanceBase, mem.CitationCount)
		} else {
			// Not cited but was injected: increment beta
			mem.TsBeta += 1.0
		}

		if err := u.memoryStore.UpdateLifecycleFields(ctx, mem.ID, map[string]any{
			"ts_alpha":        mem.TsAlpha,
			"ts_beta":         mem.TsBeta,
			"citation_count":  mem.CitationCount,
			"importance_base": mem.ImportanceBase,
		}); err != nil {
			log.Error().Err(err).Int64("memory_id", res.MemoryID).Msg("feedback: failed to update memory")
		}
	}
}

// calculateImportance applies diminishing returns: base * log(1 + citation_count).
// Capped at 1.0 to keep importance normalized.
func calculateImportance(currentBase float64, citationCount int) float64 {
	if currentBase <= 0 {
		currentBase = 0.5
	}
	scaled := currentBase * math.Log(1.0+float64(citationCount))
	if scaled > 1.0 {
		scaled = 1.0
	}
	return scaled
}
