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
	u.UpdateWithOutcome(ctx, results, "")
}

// outcomeMultipliers defines how session outcome modulates Thompson Sampling
// updates. Keys: outcome string. Values: alpha increment for cited, beta
// increment for uncited, beta increment for violated.
type outcomeMultipliers struct {
	citedAlpha   float64
	uncitedBeta  float64
	violatedBeta float64
}

var multiplierTable = map[string]outcomeMultipliers{
	"success":   {citedAlpha: 2.0, uncitedBeta: 0.0, violatedBeta: 3.0},
	"partial":   {citedAlpha: 1.0, uncitedBeta: 1.0, violatedBeta: 3.0},
	"failure":   {citedAlpha: 0.5, uncitedBeta: 2.0, violatedBeta: 5.0},
	"abandoned": {citedAlpha: 0.0, uncitedBeta: 0.0, violatedBeta: 0.0},
	"":          {citedAlpha: 1.0, uncitedBeta: 1.0, violatedBeta: 3.0},
}

// UpdateWithOutcome processes citation results with outcome-dependent modulation.
// When outcome is empty, uses default multipliers (backward-compatible).
// "abandoned" sessions skip all updates. "success" sessions don't penalize
// uncited memories (they were likely followed without being literally cited).
func (u *Updater) UpdateWithOutcome(ctx context.Context, results []CitationResult, outcome string) {
	mult, ok := multiplierTable[outcome]
	if !ok {
		mult = multiplierTable[""]
	}

	var citedIDs, uncitedIDs, violatedIDs []int64
	for _, res := range results {
		switch {
		case res.Violated:
			violatedIDs = append(violatedIDs, res.MemoryID)
		case res.Cited:
			citedIDs = append(citedIDs, res.MemoryID)
		default:
			uncitedIDs = append(uncitedIDs, res.MemoryID)
		}
	}

	if len(citedIDs) > 0 && mult.citedAlpha > 0 {
		if err := u.memoryStore.BatchIncrementCitedN(ctx, citedIDs, mult.citedAlpha); err != nil {
			log.Error().Err(err).Msg("feedback: batch increment cited failed")
		}
	}
	if len(uncitedIDs) > 0 && mult.uncitedBeta > 0 {
		if err := u.memoryStore.BatchIncrementUncitedN(ctx, uncitedIDs, mult.uncitedBeta); err != nil {
			log.Error().Err(err).Msg("feedback: batch increment uncited failed")
		}
	}
	if len(violatedIDs) > 0 && mult.violatedBeta > 0 {
		if err := u.memoryStore.BatchIncrementViolated(ctx, violatedIDs, mult.violatedBeta); err != nil {
			log.Error().Err(err).Msg("feedback: batch increment violated failed")
		}
	}

	log.Info().
		Str("outcome", outcome).
		Int("cited", len(citedIDs)).
		Int("uncited", len(uncitedIDs)).
		Int("violated", len(violatedIDs)).
		Msg("feedback: outcome-modulated update applied")
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

