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
	require.Contains(t, schema, "principal")
	required := tool.InputSchema["required"].([]string)
	require.ElementsMatch(t, []string{"action"}, required)
	conditionals := tool.InputSchema["allOf"].([]any)
	require.NotEmpty(t, conditionals)
	resumeThen := conditionals[0].(map[string]any)["then"].(map[string]any)
	require.ElementsMatch(t, []string{"principal"}, resumeThen["required"].([]string))
}

func TestGetStateToolSessionDoesNotRequirePrincipal(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetStateStore(&fakeStatePlane{session: cognitive.SessionStateSlots{Focus: map[string]interface{}{"topic": "contracts"}}})

	result, err := srv.callTool(context.Background(), "get_state", json.RawMessage(`{"action":"session","session_id":"session-1"}`))
	require.NoError(t, err)

	var response struct {
		Source    cognitive.StatePacketSource `json:"source"`
		SessionID string                      `json:"session_id"`
		State     cognitive.SessionStateSlots `json:"state"`
	}
	require.NoError(t, json.Unmarshal([]byte(result), &response))
	require.Equal(t, cognitive.StatePacketSourceNative, response.Source)
	require.Equal(t, "session-1", response.SessionID)
	require.Equal(t, "contracts", response.State.Focus["topic"])
}

func TestGetStateToolProjectDoesNotRequirePrincipal(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetStateStore(&fakeStatePlane{project: cognitive.ProjectStateRecord{UpdatedBy: "agent"}})

	result, err := srv.callTool(context.Background(), "get_state", json.RawMessage(`{"action":"project","project":"engram"}`))
	require.NoError(t, err)

	var response struct {
		Source  cognitive.StatePacketSource  `json:"source"`
		Project string                       `json:"project"`
		State   cognitive.ProjectStateRecord `json:"state"`
	}
	require.NoError(t, json.Unmarshal([]byte(result), &response))
	require.Equal(t, cognitive.StatePacketSourceNative, response.Source)
	require.Equal(t, "engram", response.Project)
	require.Equal(t, "agent", response.State.UpdatedBy)
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
			Principal:        "agent:developer",
			SessionID:        "session-1",
		},
	}
	srv.SetStateStore(fakeStore)

	result, err := srv.callTool(context.Background(), "get_state", json.RawMessage(`{"action":"resume","project":"engram","principal":"agent:developer","session_id":"session-1"}`))
	require.NoError(t, err)

	var packet cognitive.ResumePacket
	require.NoError(t, json.Unmarshal([]byte(result), &packet))
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
	require.Equal(t, "run focused tests", packet.NextAction.Description)
	require.Empty(t, packet.FallbackPath)
	require.False(t, fakeStore.lastRequest.AllowFilesystemFallback)
	require.Equal(t, "agent:developer", fakeStore.lastRequest.Principal)
}

func TestGetStateToolRejectsFilesystemFallbackOption(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetStateStore(&fakeStatePlane{})

	_, err := srv.callTool(context.Background(), "get_state", json.RawMessage(`{"action":"resume","project":"engram","principal":"agent:developer","session_id":"session-1","allow_filesystem_fallback":true}`))
	require.Error(t, err)
	require.ErrorContains(t, err, "allow_filesystem_fallback is not supported")
}

func TestGetStateToolResumeRequiresPrincipal(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetStateStore(&fakeStatePlane{})

	_, err := srv.callTool(context.Background(), "get_state", json.RawMessage(`{"action":"resume","project":"engram","session_id":"session-1"}`))
	require.Error(t, err)
	require.ErrorContains(t, err, "principal required")
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

	_, err := srv.callTool(ctx, "get_state", json.RawMessage(`{"action":"resume","principal":"agent:developer","session_id":"session-1"}`))
	require.NoError(t, err)
	require.Empty(t, fakeStore.lastRequest.Project)
	require.Equal(t, "agent:developer", fakeStore.lastRequest.Principal)
}
