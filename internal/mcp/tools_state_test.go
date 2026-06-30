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
	session     cognitive.SessionStateSlots
	project     cognitive.ProjectStateRecord
	packet      cognitive.ResumePacket
	lastRequest cognitive.ResumePacketRequest
}

func (f *fakeStatePlane) WriteSessionState(context.Context, string, cognitive.SessionStateSlots) error {
	return nil
}

func (f *fakeStatePlane) WriteProjectState(context.Context, string, cognitive.ProjectStateRecord) error {
	return nil
}

func (f *fakeStatePlane) ReadSessionState(context.Context, string) (cognitive.SessionStateSlots, error) {
	return f.session, nil
}

func (f *fakeStatePlane) ReadProjectState(context.Context, string) (cognitive.ProjectStateRecord, error) {
	return f.project, nil
}

func (f *fakeStatePlane) ReadResumePacket(_ context.Context, request cognitive.ResumePacketRequest) (cognitive.ResumePacket, error) {
	f.lastRequest = request
	return f.packet, nil
}

func TestStateToolHiddenUntilNativeWritePathIsReachable(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	require.NotContains(t, buildToolsList(srv), "get_state")

	srv.SetStateStore(&fakeStatePlane{})
	require.NotContains(t, buildToolsList(srv), "get_state")

	tool := stateTool()
	schema := tool.InputSchema["properties"].(map[string]any)
	require.NotContains(t, schema, "allow_filesystem_fallback")
}

func TestGetStateToolResumeReturnsNativePacket(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	fakeStore := &fakeStatePlane{
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
	}
	srv.SetStateStore(fakeStore)

	result, err := srv.callTool(context.Background(), "get_state", json.RawMessage(`{"action":"resume","project":"engram","session_id":"session-1"}`))
	require.NoError(t, err)

	var packet cognitive.ResumePacket
	require.NoError(t, json.Unmarshal([]byte(result), &packet))
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
	require.Equal(t, "run focused tests", packet.NextAction.Description)
	require.Empty(t, packet.FallbackPath)
	require.False(t, fakeStore.lastRequest.AllowFilesystemFallback)
}

func TestGetStateToolRejectsFilesystemFallbackOption(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetStateStore(&fakeStatePlane{})

	_, err := srv.callTool(context.Background(), "get_state", json.RawMessage(`{"action":"resume","project":"engram","session_id":"session-1","allow_filesystem_fallback":true}`))
	require.Error(t, err)
	require.ErrorContains(t, err, "allow_filesystem_fallback is not supported")
}

func TestGetStateToolResumeDoesNotInjectContextProjectWhenOmitted(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	fakeStore := &fakeStatePlane{
		packet: cognitive.ResumePacket{
			Source:           cognitive.StatePacketSourceNative,
			Freshness:        cognitive.StateFreshnessFresh,
			Drift:            cognitive.StateDrift{Kind: cognitive.StateDriftNone, CheckedAt: time.Now().UTC()},
			NextAction:       cognitive.StateAction{Kind: cognitive.StateActionCommand, Description: "continue session", Command: "go test ./internal/mcp"},
			NextVerification: cognitive.StateVerification{Kind: cognitive.StateVerificationCommand, Description: "run focused tests", Command: "go test ./internal/mcp"},
			GeneratedAt:      time.Now().UTC(),
			SessionID:        "session-1",
		},
	}
	srv.SetStateStore(fakeStore)
	ctx := contextWithProject(context.Background(), "context-project")

	_, err := srv.callTool(ctx, "get_state", json.RawMessage(`{"action":"resume","session_id":"session-1"}`))
	require.NoError(t, err)
	require.Empty(t, fakeStore.lastRequest.Project)
}
