// Package learning provides self-learning utilities for engram.
package learning

import (
	"context"

	"github.com/thebtf/engram/pkg/models"
)

// Outcome represents the result of a session.
type Outcome string

const (
	OutcomeSuccess   Outcome = "success"
	OutcomePartial   Outcome = "partial"
	OutcomeFailure   Outcome = "failure"
	OutcomeAbandoned Outcome = "abandoned"
)

// ValidOutcomes is the set of valid outcome values.
var ValidOutcomes = map[Outcome]struct{}{
	OutcomeSuccess:   {},
	OutcomePartial:   {},
	OutcomeFailure:   {},
	OutcomeAbandoned: {},
}

// IsValidOutcome reports whether o is a recognised outcome value.
func IsValidOutcome(o Outcome) bool {
	_, ok := ValidOutcomes[o]
	return ok
}

// SessionOutcomeStore provides session data for outcome determination.
type SessionOutcomeStore interface {
	GetObservationsBySession(ctx context.Context, sessionID string) ([]*models.Observation, error)
}

// DetermineSessionOutcome heuristically determines the outcome of a session.
// Rules (from spec FR-1, clarification C1):
//   - success: session has ≥1 observation with type bugfix or feature
//   - partial: session has observations but none are bugfix/feature type
//   - failure: (reserved for hook-detected consecutive errors — not determinable server-side)
//   - abandoned: session has no observations
func DetermineSessionOutcome(ctx context.Context, store SessionOutcomeStore, sessionID string) (Outcome, string) {
	observations, err := store.GetObservationsBySession(ctx, sessionID)
	if err != nil || len(observations) == 0 {
		return OutcomeAbandoned, "no observations stored during session"
	}

	for _, obs := range observations {
		if obs.Type == models.ObsTypeBugfix || obs.Type == models.ObsTypeFeature {
			return OutcomeSuccess, "session produced bugfix or feature observations"
		}
	}

	return OutcomePartial, "session has observations but no bugfix/feature activity"
}
