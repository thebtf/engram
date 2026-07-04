package s4directives

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/cognitive"
)

func TestSubsystemDelegatesAttentionWriterAndDistillerToService(t *testing.T) {
	store := &recordingDirectiveStore{}
	svc := NewService(store)
	svc.distill = func(_ context.Context, raw cognitive.RawSignal) (cognitive.Distilled, error) {
		require.Equal(t, "remember typed decisions", raw.Text)
		return cognitive.Distilled{Intent: "remember typed decisions", Horizon: "project", Privacy: "internal", Confidence: 1}, nil
	}
	subsystem := NewSubsystem(svc)

	err := subsystem.WriteAttentionEvent(context.Background(), cognitive.AttentionEventRecord{
		Project:        "engram",
		SessionID:      "session-1",
		SourceTurnHash: hashSourceMaterial("", "remember typed decisions"),
		DerivedIntent:  "remember typed decisions",
		AgentConfirmed: true,
		Horizon:        "project",
		PrivacyClass:   "internal",
	})
	require.NoError(t, err)
	require.Len(t, store.records, 1)
	require.Equal(t, "remember typed decisions", store.records[0].DerivedIntent)

	distilled, err := subsystem.Distill(context.Background(), cognitive.RawSignal{Text: " remember typed decisions "})
	require.NoError(t, err)
	require.Equal(t, "remember typed decisions", distilled.Intent)
	require.Equal(t, "project", distilled.Horizon)
	require.Equal(t, "internal", distilled.Privacy)
}

func TestSubsystemFailsClosedWhenServiceMissing(t *testing.T) {
	for _, subsystem := range []*Subsystem{nil, NewSubsystem(nil)} {
		err := subsystem.WriteAttentionEvent(context.Background(), cognitive.AttentionEventRecord{
			Project:        "engram",
			SessionID:      "session-1",
			SourceTurnHash: hashSourceMaterial("", "remember typed decisions"),
			DerivedIntent:  "remember typed decisions",
			AgentConfirmed: true,
			Horizon:        "project",
			PrivacyClass:   "internal",
		})
		require.ErrorIs(t, err, ErrNoService)

		distilled, err := subsystem.Distill(context.Background(), cognitive.RawSignal{Text: "remember typed decisions"})
		require.ErrorIs(t, err, ErrNoService)
		require.Equal(t, cognitive.Distilled{}, distilled)
	}
}
