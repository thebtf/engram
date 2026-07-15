package s4bsurfacing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/cognitive/s4directives"
	"github.com/thebtf/engram/pkg/cognitive"
)

func TestSubsystemImplementsOnlyCandidateProposerAndAcceptsNilMeter(t *testing.T) {
	source := &fakeSource{events: []s4directives.StoredAttentionEvent{
		directive(1, "engram", "s", "release checklist", "project", "internal", time.Unix(1700005000, 0).UTC()),
	}}
	subsystem := NewSubsystem(source)
	require.Equal(t, "engram.s4b.directives_surfacing", subsystem.Name())
	require.Equal(t, []string{"CandidateProposer"}, subsystem.Implements())
	require.NoError(t, subsystem.Start(context.Background(), cognitivecore.Dependencies{}))
	var typedNilMeter *cognitivecore.LocalMeter
	require.NoError(t, subsystem.Start(context.Background(), cognitivecore.Dependencies{Meter: typedNilMeter}))

	proposals, err := subsystem.Propose(context.Background(), cognitive.AttentionEvent{
		Type:      "user_prompt_shift",
		Project:   "engram",
		SessionID: "s",
		Payload:   map[string]interface{}{"text": "release"},
	}, 1)
	require.NoError(t, err)
	require.Len(t, proposals, 1)
	require.NoError(t, subsystem.Stop())
}

func TestSubsystemRejectsNilTypedNilSourceAndCanceledStart(t *testing.T) {
	require.ErrorIs(t, NewSubsystem(nil).Start(context.Background(), cognitivecore.Dependencies{}), ErrNoSource)
	var source *fakeSource
	require.ErrorIs(t, NewSubsystem(source).Start(context.Background(), cognitivecore.Dependencies{}), ErrNoSource)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, NewSubsystem(&fakeSource{}).Start(ctx, cognitivecore.Dependencies{}), context.Canceled)
}
