// Package maintenance provides scheduled maintenance tasks for engram.
package maintenance

import (
	"context"
	"errors"

	"github.com/rs/zerolog"
	"github.com/thebtf/engram/internal/db/gorm"
)

// ErrNearDedupUnsupported is returned by FindAndMerge when the near-dedup
// feature is unavailable because the vector storage pipeline was removed in v5.
// Callers should treat this as a "skipped" condition, not a failure.
var ErrNearDedupUnsupported = errors.New("near deduplication unsupported in v5: vector storage removed")

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

// FindAndMerge returns ErrNearDedupUnsupported in v5 (vector storage removed).
// Callers should treat this error as a "skipped" signal rather than a failure.
func (f *NearDuplicateFinder) FindAndMerge(ctx context.Context) (int, error) {
	_ = ctx
	f.logger.Debug().Msg("near_dedup: vector storage removed in v5, skipping")
	return 0, ErrNearDedupUnsupported
}
