package contract

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIssueSessionCreateBindingCompatibility(t *testing.T) {
	command := CreateCommand{Title: "task", TargetProject: "project", Type: "task", Priority: "medium", Metadata: json.RawMessage(`{}`)}
	old, err := CanonicalCreateV1(command)
	require.NoError(t, err)
	version, empty, err := CanonicalCreateBinding(command)
	require.NoError(t, err)
	require.Equal(t, 1, version)
	require.Equal(t, old, empty)
	command.TargetSession = "session-one"
	version, first, err := CanonicalCreateBinding(command)
	require.NoError(t, err)
	require.Equal(t, 2, version)
	require.NotEqual(t, empty, first)
	command.TargetSession = "session-two"
	_, second, err := CanonicalCreateBinding(command)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
}

func TestIssueSessionBindingCanonicalIntent(t *testing.T) {
	base := CreateCommand{Title: "task", Body: "body", TargetProject: "project", Type: "task", Priority: "medium", Labels: []string{"b", "a", "a"}, Metadata: json.RawMessage(`{"n":1,"other":true}`)}
	version, v1, err := CanonicalCreateBinding(base)
	require.NoError(t, err)
	require.Equal(t, 1, version)
	require.Equal(t, `{"version":1,"title":"task","body":"body","priority":"medium","type":"task","target_project":"project","labels":["a","b"],"metadata":{"n":1,"other":true},"owner_label":null,"recipient_label":null}`, string(v1))

	equivalent := base
	equivalent.Labels = []string{"a", "b"}
	equivalent.Metadata = json.RawMessage(`{"other":true,"n":1.0}`)
	equivalent.SessionID = "reported-provenance"
	equivalent.TransportSession = "transport-provenance"
	_, sameV1, err := CanonicalCreateBinding(equivalent)
	require.NoError(t, err)
	require.Equal(t, v1, sameV1)

	base.TargetSession, equivalent.TargetSession = "session-one", "session-one"
	version, v2, err := CanonicalCreateBinding(base)
	require.NoError(t, err)
	require.Equal(t, 2, version)
	_, sameV2, err := CanonicalCreateBinding(equivalent)
	require.NoError(t, err)
	require.Equal(t, v2, sameV2)
	equivalent.TargetSession = "session-two"
	_, different, err := CanonicalCreateBinding(equivalent)
	require.NoError(t, err)
	require.NotEqual(t, v2, different)
}
