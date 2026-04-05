// Package maintenance provides scheduled maintenance tasks for engram.
package maintenance

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/vector"
)

// NearDuplicateFinder finds and merges near-duplicate observations during maintenance.
// Observations with cosine similarity >= threshold (same project and type) are candidates
// for merging: the lower-importance one is marked as superseded.
type NearDuplicateFinder struct {
	observationStore *gorm.ObservationStore
	vectorClient     vector.Client
	threshold        float64
	logger           zerolog.Logger
}

// NewNearDuplicateFinder creates a NearDuplicateFinder with the given dependencies.
// threshold is the cosine similarity above which two observations are considered near-duplicates.
func NewNearDuplicateFinder(
	observationStore *gorm.ObservationStore,
	vectorClient vector.Client,
	threshold float64,
	logger zerolog.Logger,
) *NearDuplicateFinder {
	if threshold <= 0 {
		threshold = 0.95
	}
	return &NearDuplicateFinder{
		observationStore: observationStore,
		vectorClient:     vectorClient,
		threshold:        threshold,
		logger:           logger.With().Str("component", "near_dedup").Logger(),
	}
}

// FindAndMerge scans recent observations and merges near-duplicate pairs.
// For each candidate, it performs a vector search; if a similar observation with the
// same project and type is found above the threshold, the lower-importance one is
// marked superseded. Returns the number of pairs merged.
func (f *NearDuplicateFinder) FindAndMerge(ctx context.Context) (int, error) {
	if f.vectorClient == nil || !f.vectorClient.IsConnected() {
		f.logger.Debug().Msg("near_dedup: vector client not connected, skipping")
		return 0, nil
	}

	// Fetch recent observations (up to 100) across all projects.
	observations, err := f.observationStore.GetAllRecentObservations(ctx, 100)
	if err != nil {
		return 0, err
	}

	// Track already-processed superseded IDs to avoid double-processing.
	superseded := make(map[int64]bool, len(observations))
	merged := 0

	for _, obs := range observations {
		if obs == nil {
			continue
		}
		if superseded[obs.ID] {
			continue
		}
		if obs.IsSuperseded {
			continue
		}

		// Build a project-scoped filter (not global) so we only merge within the same project.
		project := obs.Project
		where := vector.BuildWhereFilter(vector.DocTypeObservation, project, false)

		if !obs.Narrative.Valid || obs.Narrative.String == "" {
			continue
		}
		narrative := obs.Narrative.String

		similar, err := f.vectorClient.Query(ctx, narrative, 5, where)
		if err != nil {
			f.logger.Warn().Err(err).Int64("obs_id", obs.ID).Msg("near_dedup: vector query failed, skipping observation")
			continue
		}

		for _, result := range similar {
			if result.Similarity < f.threshold {
				continue
			}

			candidateID := vector.ExtractRowID(result.Metadata)
			if candidateID <= 0 || candidateID == obs.ID {
				continue
			}
			if superseded[candidateID] {
				continue
			}

			// Load the candidate to verify type and project match.
			candidate, err := f.observationStore.GetObservationByID(ctx, candidateID)
			if err != nil || candidate == nil {
				continue
			}
			if candidate.IsSuperseded {
				continue
			}
			// Only merge same-type observations within the same project.
			if candidate.Type != obs.Type || candidate.Project != project {
				continue
			}

			// Keep the higher-importance observation; mark the other superseded.
			// Ties go to the newer observation (higher ID = more recent).
			keepID, dropID := obs.ID, candidateID
			if candidate.ImportanceScore > obs.ImportanceScore ||
				(candidate.ImportanceScore == obs.ImportanceScore && candidateID > obs.ID) {
				keepID, dropID = candidateID, obs.ID
			}

			if err := f.observationStore.MarkAsSuperseded(ctx, dropID); err != nil {
				f.logger.Warn().
					Err(err).
					Int64("drop_id", dropID).
					Int64("keep_id", keepID).
					Msg("near_dedup: failed to mark observation superseded")
				continue
			}

			superseded[dropID] = true
			merged++

			f.logger.Info().
				Int64("keep_id", keepID).
				Int64("drop_id", dropID).
				Float64("similarity", result.Similarity).
				Str("type", string(obs.Type)).
				Str("project", project).
				Msg("near_dedup: merged near-duplicate observations")

			// If the observation we're iterating over was just dropped, stop looking for more pairs.
			if dropID == obs.ID {
				superseded[obs.ID] = true
				break
			}
		}
	}

	return merged, nil
}
