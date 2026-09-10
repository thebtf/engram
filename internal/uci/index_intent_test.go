package uci

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestIndexIntentLifecycleTransitions(t *testing.T) {
	states := []IndexIntentState{
		IndexIntentSubmitted,
		IndexIntentQueued,
		IndexIntentAcknowledged,
		IndexIntentRunning,
		IndexIntentCompleted,
		IndexIntentUnavailable,
		IndexIntentFailed,
	}
	allowed := map[IndexIntentState]map[IndexIntentState]bool{
		IndexIntentSubmitted: {
			IndexIntentQueued:      true,
			IndexIntentUnavailable: true,
		},
		IndexIntentQueued: {
			IndexIntentAcknowledged: true,
			IndexIntentUnavailable:  true,
		},
		IndexIntentAcknowledged: {
			IndexIntentRunning: true,
			IndexIntentFailed:  true,
		},
		IndexIntentRunning: {
			IndexIntentCompleted: true,
			IndexIntentFailed:    true,
		},
		IndexIntentUnavailable: {
			IndexIntentQueued: true,
		},
	}

	for _, from := range states {
		for _, to := range states {
			require.Equalf(t, allowed[from][to], from.CanTransitionTo(to), "%s -> %s", from, to)
		}
	}
}

func TestIndexIntentResultRequiresNewScopedView(t *testing.T) {
	input, previous := newIndexIntentTestInput()
	intent := IndexIntent{
		ID:           uuid.NewString(),
		RequestRef:   input.RequestRef,
		Kind:         input.Kind,
		Scope:        input.Scope,
		ProfileID:    input.ProfileID,
		PreviousView: &previous,
		State:        IndexIntentSubmitted,
	}
	require.NoError(t, intent.Validate())

	require.ErrorIs(t, ValidateIndexIntentResult(intent, ContextRef{}), ErrIndexIntentResultInvalid)
	require.ErrorIs(t, ValidateIndexIntentResult(intent, previous), ErrIndexIntentResultInvalid)

	next := previous.Clone()
	next.ViewID = uuid.NewString()
	next.Generation++
	require.NoError(t, ValidateIndexIntentResult(intent, next))

	clone := input.Clone()
	clone.PreviousView.ViewID = uuid.NewString()
	require.Equal(t, previous.ViewID, input.PreviousView.ViewID, "cloning an input must not alter caller-owned view identity")
}

func newIndexIntentTestInput() (IndexIntentInput, ContextRef) {
	scope := IndexScope{
		SourceID:      uuid.NewString(),
		CheckoutID:    uuid.NewString(),
		IncarnationID: uuid.NewString(),
	}
	previous := ContextRef{
		SourceID:          scope.SourceID,
		CheckoutID:        scope.CheckoutID,
		ViewID:            uuid.NewString(),
		AnalysisProfileID: uuid.NewString(),
		Generation:        1,
	}
	return IndexIntentInput{
		RequestRef:   "index-intent-request-" + uuid.NewString(),
		Kind:         IndexIntentReindex,
		Scope:        scope,
		ProfileID:    previous.AnalysisProfileID,
		PreviousView: &previous,
	}, previous
}
