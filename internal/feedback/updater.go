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

// outcomeMultipliers defines how session outcome modulates the session-end memory
// updates. citedAlpha/uncitedBeta/violatedBeta scale the Thompson Sampling prior
// increments (which feed retrieval ranking via the rank-5 posterior blend).
//
// importanceFactor (rank-6) scales the importance_base citation bump, which is a
// SEPARATE surface from the Thompson priors: importance_base drives ListForInjection
// ordering (ORDER BY importance_base DESC), i.e. WHICH memories get injected at session
// start — whereas ts_alpha/ts_beta drive how injected memories are scored. Before rank-6
// the importance_base bump ran at a fixed magnitude regardless of outcome, so a memory
// cited in a failed session was promoted for future injection exactly as much as one cited
// in a successful session. importanceFactor=1.0 reproduces that prior magnitude exactly
// (backward-compatible for the "" / partial defaults); >1.0 promotes harder on success,
// <1.0 promotes less on failure, 0.0 leaves importance_base untouched. The bump remains
// monotonic-up (the store clamps it to never fall below the current value), so this adds
// outcome SENSITIVITY without a permanent decrement — true negative reinforcement on
// importance_base is deliberately deferred as a product decision (it needs a human call on
// whether "cited in a failed session" should be read as "this memory caused the failure").
type outcomeMultipliers struct {
	citedAlpha       float64
	uncitedBeta      float64
	violatedBeta     float64
	importanceFactor float64
}

var multiplierTable = map[string]outcomeMultipliers{
	"success":   {citedAlpha: 2.0, uncitedBeta: 0.0, violatedBeta: 3.0, importanceFactor: 1.5},
	"partial":   {citedAlpha: 1.0, uncitedBeta: 1.0, violatedBeta: 3.0, importanceFactor: 1.0},
	"failure":   {citedAlpha: 0.5, uncitedBeta: 2.0, violatedBeta: 5.0, importanceFactor: 0.25},
	"abandoned": {citedAlpha: 0.0, uncitedBeta: 0.0, violatedBeta: 0.0, importanceFactor: 0.0},
	"":          {citedAlpha: 1.0, uncitedBeta: 1.0, violatedBeta: 3.0, importanceFactor: 1.0},
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
		if err := u.memoryStore.BatchIncrementCitedN(ctx, citedIDs, mult.citedAlpha, mult.importanceFactor); err != nil {
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
