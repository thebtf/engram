package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/cognitive"
)

type fakeStatePlane struct {
	session cognitive.SessionStateSlots
	project cognitive.ProjectStateRecord
	packet  cognitive.ResumePacket
}

func (f fakeStatePlane) WriteSessionState(context.Context, string, cognitive.SessionStateSlots) error {
	return nil
}

func (f fakeStatePlane) WriteProjectState(context.Context, string, cognitive.ProjectStateRecord) error {
	return nil
}

func (f fakeStatePlane) ReadSessionState(context.Context, string) (cognitive.SessionStateSlots, error) {
	return f.session, nil
}

func (f fakeStatePlane) ReadProjectState(context.Context, string) (cognitive.ProjectStateRecord, error) {
	return f.project, nil
}

func (f fakeStatePlane) ReadResumePacket(context.Context, cognitive.ResumePacketRequest) (cognitive.ResumePacket, error) {
	return f.packet, nil
}

func TestStateToolAdvertisedOnlyWhenStoreWired(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	require.NotContains(t, buildToolsList(srv), "get_state")

	srv.SetStateStore(fakeStatePlane{})
	require.Contains(t, buildToolsList(srv), "get_state")
}

func TestGetStateToolResumeReturnsNativePacket(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetStateStore(fakeStatePlane{
		packet: cognitive.ResumePacket{
			Source:           cognitive.StatePacketSourceNative,
			Freshness:        cognitive.StateFreshnessFresh,
			Drift:            cognitive.StateDrift{Kind: cognitive.StateDriftNone, CheckedAt: time.Now().UTC()},
			NextAction:       cognitive.StateAction{Kind: cognitive.StateActionCommand, Description: "run focused tests", Command: "go test ./internal/mcp"},
			NextVerification: cognitive.StateVerification{Kind: cognitive.StateVerificationCommand, Description: "run full suite", Command: "go test ./..."},
			GeneratedAt:      time.Now().UTC(),
			Project:          "engram",
			SessionID:        "session-1",
		},
	})

	result, err := srv.callTool(context.Background(), "get_state", json.RawMessage(`{"action":"resume","project":"engram","session_id":"session-1"}`))
	require.NoError(t, err)

	var packet cognitive.ResumePacket
	require.NoError(t, json.Unmarshal([]byte(result), &packet))
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
	require.Equal(t, "run focused tests", packet.NextAction.Description)
	require.Empty(t, packet.FallbackPath)
}
