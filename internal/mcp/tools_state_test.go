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

func (f *fakeStatePlane) WriteSessionState(_ context.Context, _ string, state cognitive.SessionStateSlots) error {
	f.session = state
	return nil
}

func (f *fakeStatePlane) WriteProjectState(_ context.Context, _ string, state cognitive.ProjectStateRecord) error {
	if state.UpdatedBy == "" {
		state.UpdatedBy = "agent"
	}
	f.project = state
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
	if f.packet.Source != "" {
		return f.packet, nil
	}
	nextAction := ""
	if f.session.Execution != nil {
		if value, ok := f.session.Execution["next_action"].(string); ok {
			nextAction = value
		}
	}
	return cognitive.ResumePacket{
		Source:           cognitive.StatePacketSourceNative,
		Freshness:        cognitive.StateFreshnessFresh,
		Drift:            cognitive.StateDrift{Kind: cognitive.StateDriftNone, CheckedAt: time.Now().UTC()},
		NextAction:       cognitive.StateAction{Kind: cognitive.StateActionCommand, Description: nextAction},
		NextVerification: cognitive.StateVerification{Kind: cognitive.StateVerificationCommand, Description: "verify native state"},
		GeneratedAt:      time.Now().UTC(),
		Project:          request.Project,
		Principal:        request.Principal,
		SessionID:        request.SessionID,
		GoalID:           request.GoalID,
		TaskID:           request.TaskID,
		Scopes:           request.Scopes,
	}, nil
}

func callStateToolViaServer(t *testing.T, srv *Server, name string, args map[string]any) string {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	require.NoError(t, err)
	resp := srv.HandleRequest(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)
	result, ok := resp.Result.(map[string]any)
	require.True(t, ok)
	content, ok := result["content"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	text, ok := content[0]["text"].(string)
	require.True(t, ok)
	return text
}

func TestStateToolsAdvertisedOnlyWhenNativeStoreIsReachable(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	require.NotContains(t, buildToolsList(srv), "get_state")
	require.NotContains(t, buildToolsList(srv), "set_state")

	_, err := srv.callTool(context.Background(), "set_state", json.RawMessage(`{"action":"session","session_id":"session-1","state":{"focus":{},"execution":{},"horizons":{}}}`))
	require.Error(t, err)
	require.ErrorContains(t, err, "state store not available")

	srv.SetStateStore(&fakeStatePlane{})
	names := buildToolsList(srv)
	require.Contains(t, names, "get_state")
	require.Contains(t, names, "set_state")

	readTool := stateTool()
	readSchema := readTool.InputSchema["properties"].(map[string]any)
	require.NotContains(t, readSchema, "allow_filesystem_fallback")
	require.Contains(t, readSchema, "principal")
	readRequired := readTool.InputSchema["required"].([]string)
	require.ElementsMatch(t, []string{"action"}, readRequired)
	readConditionals := readTool.InputSchema["allOf"].([]any)
	require.NotEmpty(t, readConditionals)
	resumeThen := readConditionals[0].(map[string]any)["then"].(map[string]any)
	require.ElementsMatch(t, []string{"principal", "session_id"}, resumeThen["required"].([]string))

	writeTool := setStateTool()
	writeSchema := writeTool.InputSchema["properties"].(map[string]any)
	require.Contains(t, writeSchema, "state")
	writeRequired := writeTool.InputSchema["required"].([]string)
	require.ElementsMatch(t, []string{"action", "state"}, writeRequired)
}

func TestSetStateToolWritesNativeSessionAndProjectState(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetStateStore(&fakeStatePlane{})

	result, err := srv.callTool(context.Background(), "set_state", json.RawMessage(`{"action":"session","session_id":"session-1","state":{"focus":{"topic":"native seam"},"execution":{"next_action":"resume from write"},"horizons":{"risk":"low"}}}`))
	require.NoError(t, err)
	var sessionAck struct {
		Source    cognitive.StatePacketSource `json:"source"`
		Action    string                      `json:"action"`
		SessionID string                      `json:"session_id"`
		Status    string                      `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(result), &sessionAck))
	require.Equal(t, cognitive.StatePacketSourceNative, sessionAck.Source)
	require.Equal(t, "session", sessionAck.Action)
	require.Equal(t, "session-1", sessionAck.SessionID)
	require.Equal(t, "updated", sessionAck.Status)

	result, err = srv.callTool(context.Background(), "get_state", json.RawMessage(`{"action":"session","session_id":"session-1"}`))
	require.NoError(t, err)
	var sessionRead struct {
		State cognitive.SessionStateSlots `json:"state"`
	}
	require.NoError(t, json.Unmarshal([]byte(result), &sessionRead))
	require.Equal(t, "native seam", sessionRead.State.Focus["topic"])
	require.Equal(t, "resume from write", sessionRead.State.Execution["next_action"])
	require.Equal(t, "low", sessionRead.State.Horizons["risk"])

	result, err = srv.callTool(context.Background(), "set_state", json.RawMessage(`{"action":"project","project":"engram","state":{"phase":"implementation","deadline_date":null,"pressure":"normal","updated_by":"agent"}}`))
	require.NoError(t, err)
	var projectAck struct {
		Source  cognitive.StatePacketSource `json:"source"`
		Action  string                      `json:"action"`
		Project string                      `json:"project"`
		Status  string                      `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(result), &projectAck))
	require.Equal(t, cognitive.StatePacketSourceNative, projectAck.Source)
	require.Equal(t, "project", projectAck.Action)
	require.Equal(t, "engram", projectAck.Project)
	require.Equal(t, "updated", projectAck.Status)

	result, err = srv.callTool(context.Background(), "get_state", json.RawMessage(`{"action":"project","project":"engram"}`))
	require.NoError(t, err)
	var projectRead struct {
		State cognitive.ProjectStateRecord `json:"state"`
	}
	require.NoError(t, json.Unmarshal([]byte(result), &projectRead))
	require.Equal(t, "implementation", projectRead.State.Phase)
	require.Equal(t, "normal", projectRead.State.Pressure)
	require.Equal(t, "agent", projectRead.State.UpdatedBy)
}

func TestSetStateThenGetStateResumeUsesServerCallPath(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	fakeStore := &fakeStatePlane{}
	srv.SetStateStore(fakeStore)

	callStateToolViaServer(t, srv, "set_state", map[string]any{
		"action":     "session",
		"session_id": "session-1",
		"state": map[string]any{
			"focus":     map[string]any{"topic": "server call seam"},
			"execution": map[string]any{"next_action": "continue through server call"},
			"horizons":  map[string]any{"risk": "low"},
		},
	})
	callStateToolViaServer(t, srv, "set_state", map[string]any{
		"action":  "project",
		"project": "engram",
		"state": map[string]any{
			"phase":         "implementation",
			"deadline_date": nil,
			"pressure":      "normal",
			"updated_by":    "agent",
		},
	})

	result := callStateToolViaServer(t, srv, "get_state", map[string]any{
		"action":     "resume",
		"project":    "engram",
		"principal":  "agent:developer",
		"session_id": "session-1",
		"goal_id":    "goal-1",
		"task_id":    "task-1",
	})

	var packet cognitive.ResumePacket
	require.NoError(t, json.Unmarshal([]byte(result), &packet))
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
	require.Equal(t, "continue through server call", packet.NextAction.Description)
	require.Equal(t, "engram", packet.Project)
	require.Equal(t, "agent:developer", packet.Principal)
	require.Equal(t, "session-1", packet.SessionID)
	require.ElementsMatch(t, []cognitive.StateScopeKind{cognitive.StateScopeSession, cognitive.StateScopeProject, cognitive.StateScopeGoal, cognitive.StateScopeTask}, packet.Scopes)
	require.Equal(t, "agent:developer", fakeStore.lastRequest.Principal)
	require.Equal(t, "engram", fakeStore.lastRequest.Project)
	require.Equal(t, "session-1", fakeStore.lastRequest.SessionID)
	require.ElementsMatch(t, []cognitive.StateScopeKind{cognitive.StateScopeSession, cognitive.StateScopeProject, cognitive.StateScopeGoal, cognitive.StateScopeTask}, fakeStore.lastRequest.Scopes)
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

	result, err := srv.callTool(context.Background(), "get_state", json.RawMessage(`{"action":"resume","project":"engram","principal":"agent:developer","session_id":"session-1","goal_id":"goal-1","task_id":"task-1"}`))
	require.NoError(t, err)

	var packet cognitive.ResumePacket
	require.NoError(t, json.Unmarshal([]byte(result), &packet))
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
	require.Equal(t, "run focused tests", packet.NextAction.Description)
	require.Empty(t, packet.FallbackPath)
	require.False(t, fakeStore.lastRequest.AllowFilesystemFallback)
	require.Equal(t, "agent:developer", fakeStore.lastRequest.Principal)
	require.Equal(t, "engram", fakeStore.lastRequest.Project)
	require.Equal(t, "session-1", fakeStore.lastRequest.SessionID)
	require.Equal(t, "goal-1", fakeStore.lastRequest.GoalID)
	require.Equal(t, "task-1", fakeStore.lastRequest.TaskID)
	require.ElementsMatch(t, []cognitive.StateScopeKind{cognitive.StateScopeSession, cognitive.StateScopeProject, cognitive.StateScopeGoal, cognitive.StateScopeTask}, fakeStore.lastRequest.Scopes)
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
	require.ElementsMatch(t, []cognitive.StateScopeKind{cognitive.StateScopeSession}, fakeStore.lastRequest.Scopes)
}
