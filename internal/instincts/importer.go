package instincts

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/db/gorm"
)

// Sentinel values for instinct imports that have no session/project context.
// Using explicit markers instead of empty strings to satisfy NOT NULL constraints
// and ensure cleanup logic correctly handles imported observations.
const (
	instinctSessionID = "instinct-import"
	instinctProject   = "instinct-import"
)

// Import reads all instinct files from dir and creates observations.
func Import(ctx context.Context, dir string, obsStore *gorm.ObservationStore) (*ImportResult, error) {
	instincts, parseErrors := ParseDir(dir)

	result := &ImportResult{
		Total: len(instincts) + len(parseErrors),
	}

	for _, e := range parseErrors {
		result.Errors = append(result.Errors, e.Error())
	}

	for _, inst := range instincts {
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
