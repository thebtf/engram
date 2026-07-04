package s1state

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/cognitive"
)

type fakeStateWriter struct {
	sessionCalls  int
	projectCalls  int
	lastSession   cognitive.SessionStateSlots
	lastProject   cognitive.ProjectStateRecord
	lastSessionID string
	lastProjectID string
}

func (f *fakeStateWriter) WriteSessionState(_ context.Context, sessionID string, slots cognitive.SessionStateSlots) error {
	f.sessionCalls++
	f.lastSessionID = sessionID
	f.lastSession = slots
	return nil
}

func (f *fakeStateWriter) WriteProjectState(_ context.Context, project string, state cognitive.ProjectStateRecord) error {
	f.projectCalls++
	f.lastProjectID = project
	f.lastProject = state
	return nil
}

func TestSubsystemIdentityAndImplementsStateWriter(t *testing.T) {
	sub := NewSubsystem(&fakeStateWriter{})
	require.Equal(t, "engram.s1.state_writer", sub.Name())
	require.Equal(t, "v1.0.0", sub.Version())
	require.Equal(t, []string{"StateWriter"}, sub.Implements())
}

func TestSubsystemDelegatesSessionAndProjectWrites(t *testing.T) {
	writer := &fakeStateWriter{}
	sub := NewSubsystem(writer)

	sessionState := cognitive.SessionStateSlots{
		Focus:     map[string]interface{}{"topic": "native resume"},
		Execution: map[string]interface{}{"next_action": "write state"},
		Horizons:  map[string]interface{}{"risk": "low"},
	}
	projectState := cognitive.ProjectStateRecord{
		Phase:     "implementation",
		Pressure:  "normal",
		UpdatedBy: "agent",
	}

	require.NoError(t, sub.WriteSessionState(context.Background(), "session-1", sessionState))
	require.NoError(t, sub.WriteProjectState(context.Background(), "engram", projectState))

	require.Equal(t, 1, writer.sessionCalls)
	require.Equal(t, 1, writer.projectCalls)
	require.Equal(t, "session-1", writer.lastSessionID)
	require.Equal(t, "engram", writer.lastProjectID)
	require.Equal(t, sessionState, writer.lastSession)
	require.Equal(t, projectState, writer.lastProject)
}

func TestSubsystemNilWriterFailsSafely(t *testing.T) {
	sub := NewSubsystem(nil)

	err := sub.WriteSessionState(context.Background(), "session-1", cognitive.SessionStateSlots{})
	require.ErrorIs(t, err, ErrNoWriter)

	err = sub.WriteProjectState(context.Background(), "engram", cognitive.ProjectStateRecord{UpdatedBy: "agent"})
	require.ErrorIs(t, err, ErrNoWriter)
}
