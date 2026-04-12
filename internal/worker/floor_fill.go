package worker

import (
	"context"

	"github.com/thebtf/engram/pkg/models"
)

// fillToFloor extends existing with top-importance observations until len(existing) >= floor.
// When floor == 0 (v4 default, FR-1), the silence path is active and no fill is performed.
// When floor > 0, fetch is called with limit=floor to get candidates; duplicates are skipped
// using existingIDs (nil is safe — a fresh map is built from existing in that case).
// The original existing slice is never mutated; a new slice is returned on fill.
func fillToFloor(
	ctx context.Context,
	floor int,
	existing []*models.Observation,
	existingIDs map[int64]struct{},
	fetch func(ctx context.Context, limit int) ([]*models.Observation, error),
) []*models.Observation {
	if floor <= 0 || len(existing) >= floor {
		return existing
	}
	fillObs, err := fetch(ctx, floor)
	if err != nil {
		return existing
	}
	ids := existingIDs
	if ids == nil {
		ids = make(map[int64]struct{}, len(existing))
		for _, o := range existing {
			ids[o.ID] = struct{}{}
		}
	}
	result := make([]*models.Observation, len(existing), floor)
	copy(result, existing)
	needed := floor - len(existing)
	for _, obs := range fillObs {
		if needed == 0 {
			break
		}
		if _, already := ids[obs.ID]; !already {
			result = append(result, obs)
			ids[obs.ID] = struct{}{}
			needed--
		}
	}
	return result
}
