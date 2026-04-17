package instincts

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/db/gorm"
)

const defaultDedupThreshold = 0.85

// Sentinel values for instinct imports that have no session/project context.
// Using explicit markers instead of empty strings to satisfy NOT NULL constraints
// and ensure cleanup logic correctly handles imported observations.
const (
	instinctSessionID = "instinct-import"
	instinctProject   = "instinct-import"
)

// Import reads all instinct files from dir, deduplicates, and creates observations.
// The vectorClient parameter is ignored in v5 (vector storage removed).
func Import(ctx context.Context, dir string, vectorClient any, obsStore *gorm.ObservationStore) (*ImportResult, error) {
	instincts, parseErrors := ParseDir(dir)

	result := &ImportResult{
		Total: len(instincts) + len(parseErrors),
	}

	for _, e := range parseErrors {
		result.Errors = append(result.Errors, e.Error())
	}

	for _, inst := range instincts {
		// IsDuplicate always returns false in v5 (vector storage removed).
		// The isDup branch is currently unreachable but kept for when dedup is restored.
		isDup, err := IsDuplicate(ctx, vectorClient, inst.Trigger, defaultDedupThreshold)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("dedup check for %s: %v", inst.ID, err))
			continue
		}
		if isDup {
			result.Skipped++
			log.Debug().Str("id", inst.ID).Str("trigger", inst.Trigger).Msg("Skipping duplicate instinct")
			continue
		}

		// Convert instinct to parsed observation and store
		parsed := ConvertToObservation(inst)
		obsID, _, err := obsStore.StoreObservation(ctx, instinctSessionID, instinctProject, parsed, 0, 0)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("store observation for %s: %v", inst.ID, err))
			continue
		}

		// Update importance score from instinct confidence.
		importance := InstinctImportanceScore(inst.Confidence)
		if err := obsStore.UpdateImportanceScore(ctx, obsID, importance); err != nil {
			log.Warn().Err(err).Str("id", inst.ID).Msg("Failed to update importance score")
		}

		result.Imported++
		log.Info().Str("id", inst.ID).Str("trigger", inst.Trigger).Msg("Imported instinct")
	}

	if result.Imported == 0 && len(result.Errors) > 0 {
		return result, fmt.Errorf("no instincts imported: %d errors", len(result.Errors))
	}

	return result, nil
}
