// Package learning provides self-learning utilities for engram.
package learning

import (
	"context"
	"fmt"
)

// InjectionRecord represents a single observation injection event for propagation.
type InjectionRecord struct {
	ObservationID    int64
	InjectionSection string
}

// InjectionSource provides injection records for a session.
type InjectionSource interface {
	GetInjectionsBySession(ctx context.Context, sessionID string) ([]InjectionRecord, error)
}

// EffectivenessUpdater applies effectiveness stats to a single observation.
type EffectivenessUpdater interface {
	// GetUtilityScore returns the current utility_score for an observation.
	GetUtilityScore(ctx context.Context, id int64) (float64, error)
	// UpdateEffectivenessStats atomically increments effectiveness counters and sets utility_score.
	UpdateEffectivenessStats(ctx context.Context, id int64, addInjections, addSuccesses int, newUtilityScore float64) error
}

// AgentStatsUpdater applies per-agent effectiveness stats for an agent-observation pair.
type AgentStatsUpdater interface {
	// UpsertAgentStats increments injection count and optionally success count for an agent-observation pair.
	UpsertAgentStats(ctx context.Context, agentID string, observationID int64, success bool) error
}

// sectionWeight returns the position weight for an injection section.
func sectionWeight(section string) float64 {
	switch section {
	case "always_inject":
		return 1.0
	case "recent":
		return 0.8
	case "relevant":
		return 0.5
	case "mark_injected":
		return 0.3
	default:
		return 0.5
	}
}

// scoreDelta returns the base score delta for an outcome.
// Returns (delta, countSuccesses) where countSuccesses is 1 for success, 0 otherwise.
// Returns false for abandoned (no change).
func scoreDelta(outcome Outcome) (delta float64, countSuccess bool, apply bool) {
	switch outcome {
	case OutcomeSuccess:
		return 0.02, true, true
	case OutcomePartial:
		return 0.005, false, true
	case OutcomeFailure:
		return -0.01, false, true
	case OutcomeAbandoned:
		return 0, false, false
	default:
		return 0, false, false
	}
}

// clamp restricts v to [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// PropagateAgentStats updates agent-specific injection effectiveness stats for all observations
// injected during a session. It is a no-op for abandoned outcomes (returns 0, nil).
// agentID identifies the agent whose stats are being updated.
func PropagateAgentStats(
	ctx context.Context,
	injStore InjectionSource,
	agentStore AgentStatsUpdater,
	sessionID string,
	agentID string,
	outcome Outcome,
) (int, error) {
	_, isSuccess, apply := scoreDelta(outcome)
	if !apply || agentID == "" {
		return 0, nil
	}

	records, err := injStore.GetInjectionsBySession(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("fetch injections for agent stats session %s: %w", sessionID, err)
	}

	updated := 0
	for _, rec := range records {
		if err := agentStore.UpsertAgentStats(ctx, agentID, rec.ObservationID, isSuccess); err != nil {
			// Skip individual failures — do not abort the whole propagation.
			continue
		}
		updated++
	}

	return updated, nil
}

// PropagateOutcome propagates a session outcome to the utility scores of all injected observations.
// For abandoned outcomes it is a no-op (returns 0, nil).
func PropagateOutcome(
	ctx context.Context,
	injStore InjectionSource,
	obsStore EffectivenessUpdater,
	sessionID string,
	outcome Outcome,
) (int, error) {
	baseDelta, isSuccess, apply := scoreDelta(outcome)
	if !apply {
		return 0, nil
	}

	records, err := injStore.GetInjectionsBySession(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("fetch injections for session %s: %w", sessionID, err)
	}

	const maxPerSessionAdjustment = 0.05
	updated := 0

	for _, rec := range records {
		weight := sectionWeight(rec.InjectionSection)
		delta := clamp(baseDelta*weight, -maxPerSessionAdjustment, maxPerSessionAdjustment)

		currentScore, err := obsStore.GetUtilityScore(ctx, rec.ObservationID)
		if err != nil {
			// Skip individual failures — do not abort the whole propagation.
			continue
		}

		newScore := clamp(currentScore+delta, 0.0, 1.0)

		addSuccesses := 0
		if isSuccess {
			addSuccesses = 1
		}

		if err := obsStore.UpdateEffectivenessStats(ctx, rec.ObservationID, 1, addSuccesses, newScore); err != nil {
			continue
		}

		updated++
	}

	return updated, nil
}
