package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
)

func TestContextRecoveryListsSelectsExactContextAndRetries(t *testing.T) {
	fixture := newUCICodebaseContextFixture(t)
	failed := callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{"action": "resolve"})
	requireUCICodebaseContextError(t, fixture, failed, "CONTEXT_REQUIRED")
	// Data remains the stable code; actionable guidance belongs in Message.
	assert.Contains(t, failed.Error.Message, "codebase_context", "missing selection must name the supported recovery tool")
	assert.Contains(t, failed.Error.Message, "list", "list is the first safe recovery action")
	assert.Contains(t, failed.Error.Message, "select", "guidance must explain how to establish the intended binding")

	listed := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{"action": "list"}))
	contexts := requireUCICodebaseContextList(t, listed, 1)
	handle := requireUCICodebaseContextPayload(t, fixture, contexts[0], "agent-a-linked", "saved-a", fixture.refA.Generation)
	_, bound := fixture.application.BoundSelector("mcp-client-a")
	require.False(t, bound, "inspection must not select even one authorized result")
	selected := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{"action": "select", "context_handle": handle}))
	requireUCICodebaseContextPayload(t, fixture, selected, "agent-a-linked", "saved-a", fixture.refA.Generation)

	retried := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{"action": "resolve"}))
	requireUCICodebaseContextPayload(t, fixture, retried, "agent-a-linked", "saved-a", fixture.refA.Generation)
	t.Log("public tools/call: CONTEXT_REQUIRED -> authorized list -> exact returned handle select -> original resolve succeeds")
}

func TestContextRecoveryInspectionDoesNotChooseWrongOrAmbiguousSource(t *testing.T) {
	for _, scenario := range []string{"wrong-lone-source", "ambiguous-sources", "empty-list"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newUCICodebaseContextFixture(t)
			other := fixture.refB
			other.SourceID = "20000000-0000-4000-8000-000000000099"
			fixture.catalog.records[other.CheckoutID] = uci.ContextRecord{Ref: other, AuthRealm: uciCodebaseContextTestRealm}
			fixture.projection.metadata[other.CheckoutID]["source"] = "unrelated-repository"
			fixture.discovery.candidates["mcp-client-a"] = []uci.ContextRef{fixture.refA, other}
			want := 1
			switch scenario {
			case "wrong-lone-source":
				fixture.authorizer.allowed["agent/a"] = map[string]bool{other.CheckoutID: true}
			case "ambiguous-sources":
				fixture.authorizer.allowed["agent/a"][other.CheckoutID] = true
				want = 2
			case "empty-list":
				fixture.authorizer.allowed["agent/a"] = map[string]bool{}
				want = 0
			}
			failed := callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{"action": "resolve"})
			requireUCICodebaseContextError(t, fixture, failed, "CONTEXT_REQUIRED")
			listed := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{"action": "list"}))
			contexts := requireUCICodebaseContextList(t, listed, want)
			if scenario == "wrong-lone-source" {
				require.Equal(t, "unrelated-repository", contexts[0]["source"])
			}
			_, bound := fixture.application.BoundSelector("mcp-client-a")
			require.False(t, bound, "listing is inspection, not consent to another repository or registration")
			requireUCICodebaseContextError(t, fixture, callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{"action": "resolve"}), "CONTEXT_REQUIRED")
			t.Logf("%s: authorized contexts=%d; no binding or implicit registration; original operation remains closed", scenario, want)
		})
	}
}

func TestContextRecoveryDescriptorNamesSafeFirstAction(t *testing.T) {
	fixture := newUCICodebaseContextFixture(t)
	tool := uciCodebaseContextTool(t, fixture.server.ListTools())
	assert.Contains(t, tool.Description, "CONTEXT_REQUIRED")
	assert.Contains(t, tool.Description, "list")
	assert.Contains(t, tool.Description, "select")
	assert.Contains(t, tool.Description, "PROJECT_MIGRATION_REQUIRED", "code binding and canonical project recovery must stay distinct")
}

func TestContextRecoveryQueryRetainsGuidanceAndResumesExactSearch(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	arguments := map[string]any{"query": uciCodeIntelCompatibilityQuery, "path_prefix": uciCodeIntelCompatibilityPathPrefix}
	failed := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", arguments)
	requireUCICodeIntelSuppressedQueryError(t, failed, "CONTEXT_REQUIRED", fixture)
	text := uciCodeIntelToolText(t, failed)
	assert.JSONEq(t, `{"schema":"engram.code-query/1","status":"context_required","exposure":null,"error":{"code":"CONTEXT_REQUIRED"}}`, text, "old/unknown query consumers retain the exact suppressed code-only envelope")
	discovery := fixture.server.HandleRequest(fixture.clientA, &Request{JSONRPC: "2.0", ID: 20, Method: "tools/list", Params: json.RawMessage(`{"include_all":true}`)})
	require.Nil(t, discovery.Error)
	raw, err := json.Marshal(discovery.Result)
	require.NoError(t, err)
	var catalog struct { Tools []Tool `json:"tools"` }
	require.NoError(t, json.Unmarshal(raw, &catalog))
	var description string
	for _, tool := range catalog.Tools {
		if tool.Name == "codebase_search" { description = tool.Description }
	}
	assert.Contains(t, description, "CONTEXT_REQUIRED")
	assert.Contains(t, description, "codebase_context")
	assert.Contains(t, description, "list")
	assert.Contains(t, description, "select")
	require.Empty(t, fixture.application.searchCalls, "missing binding must not run a query")
	listed := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{"action": "list"}))
	contexts := requireUCICodebaseContextList(t, listed, 1)
	decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, fixture.clientA, map[string]any{"action": "select", "context_handle": contexts[0]["context_handle"]}))
	retried := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", arguments)
	requireUCICodeIntelQueryResponse(t, retried, fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
	t.Log("original codebase_search resumes with exact selected View/body, not unrelated context or empty success")
}

func TestContextRecoverySharedMapperPreservesClosedErrorCode(t *testing.T) {
	fixture := newUCICodebaseContextFixture(t)
	closed := codebaseContextClosedError(uci.ContextRequired)
	code, recognized := codebaseContextErrorCode(closed)
	assert.True(t, recognized, "the shared mapper must recognize its own closed context errors instead of generic UCI unavailable")
	assert.Equal(t, uci.ContextRequired, code)
	mapped := codebaseStatusFreshnessError(fixture.clientA, closed)
	assert.Contains(t, mapped.Error(), "CONTEXT_REQUIRED")
	assert.NotContains(t, mapped.Error(), "UCI freshness unavailable")
}

func TestContextRecoveryIssuesDescriptorDoesNotAdvertiseActorSpoofing(t *testing.T) {
	properties := issuesToolSchema()["properties"].(map[string]any)
	description := properties["project"].(map[string]any)["description"].(string)
	assert.NotContains(t, description, "current project slug", "a caller argument cannot establish mutation actor identity")
	assert.Contains(t, description, "canonical", "write identity is injected from the native caller context")
	assert.Contains(t, description, "filter", "read filters remain separate from acting identity")
}

func TestContextRecoveryQueryErrorKeepsStrictOldSchema(t *testing.T) {
	var response uci.QueryResponse
	require.NoError(t, json.Unmarshal([]byte(`{"schema":"engram.code-query/1","status":"context_required","exposure":null,"error":{"code":"CONTEXT_REQUIRED"}}`), &response))
	require.NoError(t, response.Validate())
	var queryError uci.QueryError
	require.Error(t, json.Unmarshal([]byte(`{"code":"CONTEXT_REQUIRED","guidance":"must not extend the strict business DTO"}`), &queryError), "unknown fields stay rejected; guidance belongs in shipped descriptions and existing message channels")
}
