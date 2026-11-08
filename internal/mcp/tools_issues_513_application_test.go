package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/issuefixture"
	gormlib "gorm.io/gorm"
)

func TestIssues513NativeApplication(t *testing.T) {
	store, issueStore, project := openIssues513NativeStore(t)
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	owner := issueClientContext(project, "synthetic-native-owner", auth.RoleReadWrite)
	other := issueClientContext(project, "synthetic-native-other", auth.RoleReadWrite)
	invoke := func(t *testing.T, args map[string]any) map[string]json.RawMessage {
		t.Helper()
		args["format"] = "json"
		response := callIssues513Native(t, server, owner, args)
		require.Nil(t, response.Error, "valid native request must reach the shared application")
		return issues513NativeJSON(t, response)
	}
	var id int64
	t.Run("create_equal_retry_and_original_binding_after_edit", func(t *testing.T) {
		intent := map[string]any{"action": "create", "title": "Synthetic native task", "request_key": "native-original", "metadata": map[string]any{"unknown": []any{true, nil, 0.1}}, "labels": []string{"b", "a", "a"}}
		first := invoke(t, intent)
		require.NoError(t, json.Unmarshal(first["id"], &id))
		require.Positive(t, id)
		assert.JSONEq(t, `"1"`, string(first["revision"]))
		retry := invoke(t, intent)
		assert.JSONEq(t, string(first["id"]), string(retry["id"]))
		assert.JSONEq(t, `true`, string(retry["replayed"]))
		edited := invoke(t, map[string]any{"action": "update", "id": id, "expected_revision": "1", "body": "", "owner_label": "docs", "comment": "Synthetic committed outcome", "comment_metadata": map[string]any{"extension": 1}})
		assert.JSONEq(t, `"2"`, string(edited["revision"]))
		retry = invoke(t, intent)
		assert.JSONEq(t, string(first["id"]), string(retry["id"]))
		assert.JSONEq(t, `"2"`, string(retry["revision"]))
		intent["title"] = "Changed synthetic intent"
		failure := issues513NativeJSON(t, callIssues513Native(t, server, owner, intent))
		var errorObject struct {
			Code string `json:"code"`
		}
		require.NoError(t, json.Unmarshal(failure["error"], &errorObject))
		assert.Equal(t, "IDEMPOTENCY_CONFLICT", errorObject.Code)
	})
	t.Run("stale_compound_and_foreign_field_edit_do_not_commit", func(t *testing.T) {
		require.Positive(t, id)
		before, comments, err := issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		for _, test := range []struct {
			foreign        bool
			revision, code string
		}{{false, "1", "REVISION_CONFLICT"}, {true, "2", "FORBIDDEN"}} {
			ctx := owner
			if test.foreign {
				ctx = other
			}
			response := callIssues513Native(t, server, ctx, map[string]any{"action": "update", "id": id, "expected_revision": test.revision, "title": "Synthetic losing draft", "comment": "Synthetic losing companion", "format": "json"})
			failure := issues513NativeJSON(t, response)
			var errorObject struct {
				Code string `json:"code"`
			}
			require.NoError(t, json.Unmarshal(failure["error"], &errorObject))
			assert.Equal(t, test.code, errorObject.Code)
		}
		after, actualComments, err := issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, before, after)
		assert.Equal(t, comments, actualComments)
	})
	t.Run("comment_progression_and_source_terminal_revision_chain", func(t *testing.T) {
		comment := callIssues513Native(t, server, other, map[string]any{"action": "comment", "id": id, "body": "Synthetic collaborator outcome", "metadata": map[string]any{"outcome": "partial"}, "format": "json"})
		require.Nil(t, comment.Error)
		assert.JSONEq(t, `"2"`, string(issues513NativeJSON(t, comment)["revision"]))
		resolved := invoke(t, map[string]any{"action": "update", "id": id, "expected_revision": "2", "status": "resolved"})
		assert.JSONEq(t, `"3"`, string(resolved["revision"]))
		reopened := invoke(t, map[string]any{"action": "reopen", "id": id, "expected_revision": "3", "body": "Synthetic reason", "metadata": map[string]any{"reason": "retained"}})
		assert.JSONEq(t, `"4"`, string(reopened["revision"]))
		closed := invoke(t, map[string]any{"action": "close", "id": id, "expected_revision": "4"})
		assert.JSONEq(t, `"5"`, string(closed["revision"]))
		detail := invoke(t, map[string]any{"action": "get", "id": id, "detail_mode": "paged"})
		var row gormdb.Issue
		require.NoError(t, json.Unmarshal(detail["issue"], &row))
		assert.Equal(t, "closed", row.Status)
		assert.Equal(t, "", row.Body)
		assert.Equal(t, "client", row.Actor.Kind)
		var comments []gormdb.IssueComment
		require.NoError(t, json.Unmarshal(detail["comments"], &comments))
		require.Len(t, comments, 3)
		assert.JSONEq(t, `{"outcome":"partial"}`, string(comments[1].Metadata))
		assert.JSONEq(t, `{"reason":"retained"}`, string(comments[2].Metadata))
		var history []gormdb.IssueChange
		require.NoError(t, json.Unmarshal(detail["history"], &history))
		require.Len(t, history, 5)
		for _, change := range history {
			assert.JSONEq(t, `{}`, string(change.Changes))
		}
	})
	t.Run("list_json_uses_shared_filters_and_effective_bounds", func(t *testing.T) {
		page := invoke(t, map[string]any{"action": "list", "project": project, "q": "native task", "owner_label": "docs", "limit": 200, "offset": 0})
		assert.JSONEq(t, `200`, string(page["limit"]))
		assert.JSONEq(t, `null`, string(page["next_offset"]))
		assert.JSONEq(t, `1`, string(page["total"]))
		var rows []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(page["issues"], &rows))
		require.Len(t, rows, 1)
		for _, field := range []string{"body", "metadata", "creator_keycard_id"} {
			assert.NotContains(t, rows[0], field)
		}
		defaultPage := invoke(t, map[string]any{"action": "list"})
		assert.JSONEq(t, `20`, string(defaultPage["limit"]))
	})
	t.Run("raw_duplicate_numeric_and_privacy_refusals_leave_no_issue", func(t *testing.T) {
		var before int64
		require.NoError(t, store.GetDB().Model(&gormdb.Issue{}).Count(&before).Error)
		for _, raw := range []string{`{"action":"create","title":"Synthetic raw","request_key":"raw","REQUEST_KEY":null}`, `{"action":"create","title":"Synthetic raw","metadata":{"x":1,"x":2}}`, `{"action":"create","title":"Synthetic raw","metadata":{"x":0.10000000000000001}}`, fmt.Sprintf(`{"action":"create","title":"Synthetic raw","body":%q}`, "api_key="+strings.Repeat("x", 32))} {
			response := server.HandleRequest(owner, &Request{JSONRPC: "2.0", ID: t.Name(), Method: "tools/call", Params: mustJSON(t, ToolCallParams{Name: "issues", Arguments: json.RawMessage(raw)})})
			require.NotNil(t, response.Error)
			var wire bytes.Buffer
			server.stdout = &wire
			server.sendResponse(response)
			assert.Less(t, wire.Len(), 2048, "refusal is body-free, not an echoed payload")
		}
		var after int64
		require.NoError(t, store.GetDB().Model(&gormdb.Issue{}).Count(&after).Error)
		assert.Equal(t, before, after)
	})
	t.Run("missing_revision_and_changed_actor_fail_without_new_intent", func(t *testing.T) {
		for _, args := range []map[string]any{{"action": "close", "id": id, "format": "json"}, {"action": "create", "title": "Synthetic actor guard", "request_key": "guarded", "expected_actor": map[string]any{"kind": "operator", "reference": "operator:master"}, "format": "json"}} {
			response := callIssues513Native(t, server, owner, args)
			require.NotNil(t, response.Error)
			failure := issues513NativeJSON(t, response)
			var object struct {
				Code string `json:"code"`
			}
			require.NoError(t, json.Unmarshal(failure["error"], &object))
			assert.Contains(t, []string{"REVISION_REQUIRED", "ACTOR_CHANGED"}, object.Code)
		}
	})
	t.Run("actual_serialized_reply_bound_refuses_intact_historical_record", func(t *testing.T) {
		var historical int64
		err := issuefixture.SeedHistorical(store.GetDB(), func(tx *gormlib.DB) error {
			var err error
			historical, err = gormdb.NewIssueStore(tx).CreateIssue(owner, &gormdb.Issue{Title: "Synthetic native envelope record", Body: strings.Repeat("x", nativeIssueEnvelopeBytes), SourceProject: project, TargetProject: project})
			return err
		})
		require.NoError(t, err)
		response := callIssues513Native(t, server, owner, map[string]any{"action": "get", "id": historical, "format": "json", "detail_mode": "paged"})
		require.NotNil(t, response.Error)
		failure := issues513NativeJSON(t, response)
		var object struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		require.NoError(t, json.Unmarshal(failure["error"], &object))
		assert.Equal(t, "TRANSPORT_RECORD_TOO_LARGE", object.Code)
		assert.Contains(t, object.Message, "REST")
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		assert.Less(t, len(encoded), 2048, "no partial content may escape")
		row, _, err := issueStore.GetIssue(t.Context(), historical)
		require.NoError(t, err)
		assert.Equal(t, nativeIssueEnvelopeBytes, len(row.Body), "transport refusal cannot rewrite historical bytes")
	})
}
