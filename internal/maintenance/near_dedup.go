// Package maintenance provides scheduled maintenance tasks for engram.
package maintenance

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/thebtf/engram/internal/db/gorm"
)

// NearDuplicateFinder finds and merges near-duplicate observations during maintenance.
// Vector-based dedup removed in v5; FindAndMerge is a no-op.
type NearDuplicateFinder struct {
	observationStore *gorm.ObservationStore
	threshold        float64
	logger           zerolog.Logger
}

// NewNearDuplicateFinder creates a NearDuplicateFinder.
// The vectorClient parameter is accepted for call-site compatibility but ignored in v5.
func NewNearDuplicateFinder(
	observationStore *gorm.ObservationStore,
	vectorClient any,
	threshold float64,
	logger zerolog.Logger,
) *NearDuplicateFinder {
	if threshold <= 0 {
		threshold = 0.95
	}
	return &NearDuplicateFinder{
		observationStore: observationStore,
		threshold:        threshold,
		logger:           logger.With().Str("component", "near_dedup").Logger(),
	}
}

// FindAndMerge is a no-op in v5 (vector storage removed). Returns 0, nil.
func (f *NearDuplicateFinder) FindAndMerge(ctx context.Context) (int, error) {
	_ = ctx
	f.logger.Debug().Msg("near_dedup: vector storage removed in v5, skipping")
	return 0, nil
}
