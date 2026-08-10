package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestIssuesRejectWrongStructuredFieldTypesBeforeMutation(t *testing.T) {
	server := NewServer(ServerOptions{Version: "strict-issues"})
	// A zero-value store makes an accidental durable call fail the test instead of
	// accepting a coercion. Every malformed request must fail during validation.
	server.SetIssueStore(&gormdb.IssueStore{})

	for _, tc := range []struct {
		name  string
		raw   string
		field string
	}{
		{"create title", `{"action":"create","project":"source","title":1,"target_project":"target"}`, "title"},
		{"create body", `{"action":"create","project":"source","title":"title","body":1,"target_project":"target"}`, "body"},
		{"create priority", `{"action":"create","project":"source","title":"title","target_project":"target","priority":1}`, "priority"},
		{"create type", `{"action":"create","project":"source","title":"title","target_project":"target","type":1}`, "type"},
		{"create agent source", `{"action":"create","project":"source","title":"title","target_project":"target","agent_source":1}`, "agent_source"},
		{"create labels", `{"action":"create","project":"source","title":"title","target_project":"target","labels":"valid"}`, "labels"},
		{"create label item", `{"action":"create","project":"source","title":"title","target_project":"target","labels":["valid",1]}`, "labels[1]"},
		{"update status", `{"action":"update","project":"source","id":1,"status":1}`, "status"},
		{"update comment", `{"action":"update","project":"source","id":1,"status":"resolved","comment":1}`, "comment"},
		{"update agent source", `{"action":"update","project":"source","id":1,"status":"resolved","agent_source":1}`, "agent_source"},
		{"comment body", `{"action":"comment","project":"source","id":1,"body":1}`, "body"},
		{"comment agent source", `{"action":"comment","project":"source","id":1,"body":"comment","agent_source":1}`, "agent_source"},
		{"reopen comment", `{"action":"reopen","project":"source","id":1,"comment":1}`, "comment"},
		{"reopen agent source", `{"action":"reopen","project":"source","id":1,"agent_source":1}`, "agent_source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := server.handleIssues(context.Background(), json.RawMessage(tc.raw))
			require.ErrorContains(t, err, tc.field)
			require.Empty(t, out)
		})
	}
}

func TestIssuesGetSelectorIsStrictAndLossless(t *testing.T) {
	server := NewServer(ServerOptions{Version: "strict-issues"})
	server.SetIssueStore(&gormdb.IssueStore{})

	valid, err := parseArgs(json.RawMessage(`{"action":"get","id":9007199254740993}`))
	require.NoError(t, err)
	require.NoError(t, validateIssueActionParams("get", valid))
	id, err := requireInt64Arg(valid, "id")
	require.NoError(t, err)
	require.EqualValues(t, 9007199254740993, id)

	for _, raw := range []string{
		`{"action":"get","id":"1"}`,
		`{"action":"get","id":1.5}`,
		`{"action":"get","id":1e3}`,
		`{"action":"get","id":-1}`,
		`{"action":"get","id":9223372036854775808}`,
		`{"action":"get","id":0}`,
	} {
		out, err := server.handleIssues(context.Background(), json.RawMessage(raw))
		require.Error(t, err, raw)
		require.Empty(t, out, raw)
	}
}
