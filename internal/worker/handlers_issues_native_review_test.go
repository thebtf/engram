package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/issues"
	"github.com/thebtf/engram/internal/mcp"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// Root-requested test-only oracle. Requests cross the actual gRPC client,
// interceptor, CallTool canonicalizer and existing MCP bridge, never a copied
// wrapper or a direct call to raw admission. All records/auth are schema-local.
func TestIssue513NativeWireReviewRed(t *testing.T) {
	fixture := newIssue513ActorFixture(t)
	service := fixture.service
	validator := auth.NewValidator("", service.tokenStore)
	identity, err := validator.Validate(t.Context(), fixture.client.bearer)
	require.NoError(t, err)
	require.Equal(t, auth.SourceClient, identity.Source)
	require.Equal(t, auth.RoleReadWrite, identity.Role)
	require.NotEmpty(t, identity.KeycardID)
	native := mcp.NewServer(mcp.ServerOptions{})
	native.SetIssueStore(service.issueStore)
	listener := bufconn.Listen(1 << 20)
	transport, _ := grpcserver.New(&mcpHandlerAdapter{mcpServer: native}, validator)
	go func() { _ = transport.Serve(listener) }()
	connection, err := grpc.NewClient("passthrough:///issue513-native-wire-review",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, connection.Close())
		transport.Stop()
		assert.NoError(t, listener.Close())
	})
	client := pb.NewEngramServiceClient(connection)
	grpcContext := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer "+fixture.client.bearer)
	mcpContext := auth.WithIdentity(mcp.ContextWithProject(t.Context(), issue513Source), identity)
	invokeGRPC := func(t *testing.T, raw []byte) (*pb.CallToolResponse, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(grpcContext, 15*time.Second)
		defer cancel()
		return client.CallTool(ctx, &pb.CallToolRequest{ToolName: "issues", Project: issue513Source, ArgumentsJson: raw})
	}
	decodeGRPC := func(t *testing.T, response *pb.CallToolResponse) map[string]json.RawMessage {
		t.Helper()
		require.NotNil(t, response)
		require.False(t, response.IsError, "valid gRPC control must reach persistence through the real bridge")
		require.Equal(t, issue513Source, response.CanonicalProject)
		var envelope struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		require.NoError(t, json.Unmarshal(response.ContentJson, &envelope))
		require.Len(t, envelope.Content, 1)
		var result map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(envelope.Content[0].Text), &result))
		return result
	}
	create := func(t *testing.T, key string) int64 {
		t.Helper()
		raw := []byte(fmt.Sprintf(`{"action":"create","title":"Synthetic native review control","request_key":%q,"format":"json"}`, key))
		response, err := invokeGRPC(t, raw)
		require.NoError(t, err)
		var id int64
		require.NoError(t, json.Unmarshal(decodeGRPC(t, response)["id"], &id))
		require.Positive(t, id)
		return id
	}
	publicMCP := func(t *testing.T, id int64, fields map[string]any) *mcp.Response {
		t.Helper()
		fields["id"] = id
		raw, err := json.Marshal(fields)
		require.NoError(t, err)
		params, err := json.Marshal(mcp.ToolCallParams{Name: "issues", Arguments: raw})
		require.NoError(t, err)
		response := native.HandleRequest(mcpContext, &mcp.Request{JSONRPC: "2.0", ID: t.Name(), Method: "tools/call", Params: params})
		require.NotNil(t, response)
		return response
	}
	assertDuplicateRefusal := func(t *testing.T, project, key string) {
		t.Helper()
		before := issue513IssueDomainCounts(t, service)
		raw := []byte(fmt.Sprintf(`{"action":"create","title":"Synthetic duplicate gRPC intent","project":%q,"request_key":%q,"request_key":%q,"format":"json"}`, project, key+"-first", key+"-last"))
		require.True(t, json.Valid(raw), "duplicate members must be valid received JSON, not syntax failure")
		response, err := invokeGRPC(t, raw)
		if err != nil {
			assert.Equal(t, codes.InvalidArgument, status.Code(err), "transport failure is not the intended duplicate rejection")
		} else {
			require.NotNil(t, response)
			assert.True(t, response.IsError, "duplicate request_key must be refused before compatibility remapping can erase it")
		}
		assert.Equal(t, before, issue513IssueDomainCounts(t, service), "duplicate refusal must leave issues/comments/history/receipts unchanged")
	}

	t.Run("grpc_valid_mismatched_compatibility_project_control", func(t *testing.T) {
		before := issue513IssueDomainCounts(t, service)
		response, err := invokeGRPC(t, []byte(fmt.Sprintf(`{"action":"create","title":"Synthetic valid gRPC remapping control","project":%q,"request_key":"valid-remap-control","format":"json"}`, issue513OtherSource)))
		require.NoError(t, err)
		var id int64
		require.NoError(t, json.Unmarshal(decodeGRPC(t, response)["id"], &id))
		row, _, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, issue513Source, row.SourceProject, "compatibility project must not become caller identity")
		assert.Equal(t, identity.KeycardID, row.CreatorKeycardID)
		after := issue513IssueDomainCounts(t, service)
		assert.Equal(t, before["issues"]+1, after["issues"])
		assert.Equal(t, before["issue_changes"]+1, after["issue_changes"])
		assert.Equal(t, before["issue_create_receipts"]+1, after["issue_create_receipts"])
	})
	t.Run("grpc_duplicate_without_remapping_control", func(t *testing.T) {
		assertDuplicateRefusal(t, issue513Source, "duplicate-same-project")
	})
	t.Run("grpc_duplicate_survives_mismatched_compatibility_project", func(t *testing.T) {
		assertDuplicateRefusal(t, issue513OtherSource, "duplicate-rewritten-project")
	})
	t.Run("grpc_ascii_metadata_control", func(t *testing.T) {
		metadata := `{"v":"` + strings.Repeat("a", 180000) + `"}`
		raw := []byte(`{"action":"create","title":"Synthetic metadata wire control","request_key":"ascii-wire-control","format":"json","metadata":` + metadata + `}`)
		response, err := invokeGRPC(t, raw)
		require.NoError(t, err)
		decodeGRPC(t, response)
	})
	t.Run("grpc_admitted_html_metadata_is_not_expanded_out_of_domain", func(t *testing.T) {
		metadata := `{"v":"` + strings.Repeat("<", 180000) + `"}`
		require.Less(t, len(metadata), issues.MetadataBytes, "raw compact metadata is inside the accepted domain")
		controlRaw := []byte(`{"title":"Synthetic direct metadata control","request_key":"html-direct-control","metadata":` + metadata + `}`)
		control, err := issues.New(service.issueStore, nil).Create(mcpContext, issues.Scope{SourceProject: issue513Source}, controlRaw)
		require.NoError(t, err, "real shared admission must accept the unmodified received metadata before testing wrappers")
		stored, _, err := service.issueStore.GetIssue(t.Context(), control.ID)
		require.NoError(t, err)
		var admitted struct {
			Value string `json:"v"`
		}
		require.NoError(t, json.Unmarshal(stored.Metadata, &admitted))
		require.True(t, admitted.Value == strings.Repeat("<", 180000), "direct control must retain all admitted characters")
		before := issue513IssueDomainCounts(t, service)
		raw := []byte(`{"action":"create","title":"Synthetic HTML metadata wire request","request_key":"html-wire-control","format":"json","metadata":` + metadata + `}`)
		require.True(t, json.Valid(raw))
		response, err := invokeGRPC(t, raw)
		require.NoError(t, err)
		result := decodeGRPC(t, response)
		var id int64
		require.NoError(t, json.Unmarshal(result["id"], &id))
		row, _, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		var actual struct {
			Value string `json:"v"`
		}
		require.NoError(t, json.Unmarshal(row.Metadata, &actual))
		assert.True(t, actual.Value == admitted.Value, "gRPC wrapping cannot silently rewrite admitted metadata")
		after := issue513IssueDomainCounts(t, service)
		assert.Equal(t, before["issues"]+1, after["issues"])
	})
	t.Run("grpc_over_limit_metadata_is_still_refused_without_writes", func(t *testing.T) {
		metadata := `{"v":"` + strings.Repeat("a", issues.MetadataBytes-len(`{"v":""}`)+1) + `"}`
		require.Equal(t, issues.MetadataBytes+1, len(metadata))
		before := issue513IssueDomainCounts(t, service)
		raw := []byte(`{"action":"create","title":"Synthetic oversized metadata wire request","request_key":"oversized-wire-control","format":"json","metadata":` + metadata + `}`)
		response, err := invokeGRPC(t, raw)
		require.NoError(t, err)
		require.NotNil(t, response)
		assert.True(t, response.IsError, "genuine over-limit metadata must not be stored")
		var failure struct {
			Data struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(response.ContentJson, &failure))
		assert.Equal(t, "PAYLOAD_TOO_LARGE", failure.Data.Error.Code)
		assert.Equal(t, before, issue513IssueDomainCounts(t, service))
	})
	t.Run("public_mcp_ordinary_comment_preserves_material_revision", func(t *testing.T) {
		id := create(t, "ordinary-comment-control")
		before, _, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		counts := issue513IssueDomainCounts(t, service)
		response := publicMCP(t, id, map[string]any{"action": "comment", "body": "Synthetic ordinary comment", "metadata": map[string]any{"extension": true}, "format": "json"})
		require.Nil(t, response.Error, "ordinary standalone comment must remain supported")
		after, comments, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		after.UpdatedAt = before.UpdatedAt // Comment-only append may update its parent timestamp.
		assert.True(t, reflect.DeepEqual(before, after), "ordinary comment changes no material field/revision/provenance")
		require.Len(t, comments, 1)
		assert.JSONEq(t, `{"extension":true}`, string(comments[0].Metadata))
		assert.Equal(t, counts["issue_changes"], issue513IssueDomainCounts(t, service)["issue_changes"])
	})
	t.Run("ordinary_mux_historical_record_has_safe_rest_guidance", func(t *testing.T) {
		issue513NativeIPCReviewCase(t, fixture, native, validator)
	})
	t.Run("grpc_absent_host_reported_comment_and_host_update", func(t *testing.T) {
		const reported = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		const hostB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		const hostA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		id := create(t, "native-absent-host-create")
		row, _, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.Empty(t, row.CreatedBySession)
		assert.Equal(t, identity.KeycardID, row.CreatorKeycardID)
		assert.Equal(t, issue513Source, row.SourceProject)
		comment, err := json.Marshal(map[string]any{"action": "comment", "id": id, "body": "Synthetic reported comment", "created_by_session": reported, "format": "json"})
		require.NoError(t, err)
		response, err := invokeGRPC(t, comment)
		require.NoError(t, err)
		decodeGRPC(t, response)
		_, comments, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		require.Len(t, comments, 1)
		assert.Equal(t, reported, comments[0].AuthorSession)
		assert.Equal(t, "reported", comments[0].AuthorSessionSource)
		assert.Equal(t, issue513Source, comments[0].AuthorProject)
		assert.Equal(t, "client:"+identity.KeycardID, issue513ActorReference(comments[0].Actor.Reference))
		update, err := json.Marshal(map[string]any{"action": "update", "id": id, "expected_revision": "1", "title": "Synthetic host B update", "created_by_session": reported, "format": "json"})
		require.NoError(t, err)
		response, err = client.CallTool(grpcContext, &pb.CallToolRequest{ToolName: "issues", Project: issue513Source, SessionId: hostB, ArgumentsJson: update})
		require.NoError(t, err)
		assert.JSONEq(t, `"2"`, string(decodeGRPC(t, response)["revision"]))
		var change gormdb.IssueChange
		require.NoError(t, service.store.GetDB().Where("issue_id = ? AND revision = 2", id).First(&change).Error)
		require.NotNil(t, change.Attribution.Session)
		assert.Equal(t, hostB, *change.Attribution.Session)
		assert.Equal(t, "client:"+identity.KeycardID, issue513ActorReference(change.Actor.Reference))
		for _, candidate := range []string{hostA, hostB, reported} {
			args, marshalErr := json.Marshal(map[string]any{"action": "list", "participant_session": candidate, "format": "json"})
			require.NoError(t, marshalErr)
			listed, callErr := invokeGRPC(t, args)
			require.NoError(t, callErr)
			var rows []struct {
				ID int64 `json:"id"`
			}
			require.NoError(t, json.Unmarshal(decodeGRPC(t, listed)["issues"], &rows))
			found := false
			for _, item := range rows {
				found = found || item.ID == id
			}
			assert.Equal(t, candidate != hostA, found, "participant filters reflect committed provenance only")
		}
	})
	t.Run("grpc_material_session_precedence_and_participant_filter", func(t *testing.T) {
		for _, tc := range []struct{ name, transport, sessionID, reported, expected string }{
			{"reported_fallback", "", "", "reported-only-513", "reported-only-513"},
			{"explicit_over_reported", "", "explicit-513", "reported-shadow-513", "explicit-513"},
			{"transport_over_explicit", "transport-513", "explicit-shadow-513", "reported-last-513", "transport-513"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				id := create(t, "native-session-"+tc.name)
				before, _, err := service.issueStore.GetIssue(t.Context(), id)
				require.NoError(t, err)
				fields := map[string]any{"action": "update", "id": id, "expected_revision": "1", "title": "native session " + tc.name, "created_by_session": tc.reported, "format": "json"}
				if tc.sessionID != "" {
					fields["session_id"] = tc.sessionID
				}
				raw, err := json.Marshal(fields)
				require.NoError(t, err)
				response, err := client.CallTool(grpcContext, &pb.CallToolRequest{ToolName: "issues", Project: issue513Source, SessionId: tc.transport, ArgumentsJson: raw})
				require.NoError(t, err)
				result := decodeGRPC(t, response)
				assert.JSONEq(t, `"2"`, string(result["revision"]))
				after, _, err := service.issueStore.GetIssue(t.Context(), id)
				require.NoError(t, err)
				assert.Equal(t, before.CreatorKeycardID, after.CreatorKeycardID, "session provenance cannot change actor authority")
				assert.Equal(t, before.SourceProject, after.SourceProject, "session provenance cannot select the source")
				var changes []gormdb.IssueChange
				require.NoError(t, service.store.GetDB().Where("issue_id = ? AND revision = 2", id).Find(&changes).Error)
				require.Len(t, changes, 1)
				if assert.NotNil(t, changes[0].Attribution.Session, "reported fallback must survive the native adapter") {
					assert.Equal(t, tc.expected, *changes[0].Attribution.Session)
				}
				for _, candidate := range []string{tc.expected, tc.reported, tc.sessionID} {
					if candidate == "" {
						continue
					}
					args, marshalErr := json.Marshal(map[string]any{"action": "list", "project": issue513Source, "participant_session": candidate, "format": "json"})
					require.NoError(t, marshalErr)
					listed, callErr := invokeGRPC(t, args)
					require.NoError(t, callErr)
					var rows []struct {
						ID int64 `json:"id"`
					}
					require.NoError(t, json.Unmarshal(decodeGRPC(t, listed)["issues"], &rows))
					found := false
					for _, row := range rows {
						if row.ID == id {
							found = true
						}
					}
					assert.Equal(t, candidate == tc.expected, found, "participant filter must use the committed attribution, not lower-precedence inputs")
				}
			})
		}
	})
	t.Run("grpc_per_call_source_agent_and_client_provenance", func(t *testing.T) {
		for _, tc := range []struct{ name, client, agentField, agent, wantAgent string }{
			{"unknown", "", "", "", ""},
			{"omp_client_only", "omp", "", "", ""},
			{"openclaw_client_only", "openclaw", "", "", ""},
			{"explicit_source_agent", "omp", "source_agent", "synthetic-explicit-agent", "synthetic-explicit-agent"},
			{"legacy_agent_source", "openclaw", "agent_source", "synthetic-legacy-agent", "synthetic-legacy-agent"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fields := map[string]any{"action": "create", "title": "native provenance " + tc.name, "request_key": "native-client-" + tc.name, "format": "json"}
				if tc.client != "" {
					fields["client"] = tc.client
				}
				if tc.agentField != "" {
					fields[tc.agentField] = tc.agent
				}
				raw, err := json.Marshal(fields)
				require.NoError(t, err)
				response, err := invokeGRPC(t, raw)
				require.NoError(t, err)
				var id int64
				require.NoError(t, json.Unmarshal(decodeGRPC(t, response)["id"], &id))
				row, _, err := service.issueStore.GetIssue(t.Context(), id)
				require.NoError(t, err)
				if tc.agentField != "" || tc.client == "" {
					assert.Equal(t, tc.wantAgent, row.SourceAgent, "explicit agent must survive and unevidenced agent must remain unknown")
				} else {
					assert.Contains(t, []string{"", tc.client}, row.SourceAgent, "client-only attribution may use the evidenced client or remain unknown, never fabricate another agent")
				}
				assert.Equal(t, tc.client, row.SourceClient, "actual submitted client must remain provenance")
				assert.Equal(t, identity.KeycardID, row.CreatorKeycardID, "attribution must never select authentication")
				assert.Equal(t, issue513Source, row.SourceProject)
				delete(fields, "request_key")
				fields["action"], fields["id"], fields["expected_revision"], fields["title"] = "update", id, "1", "native provenance updated "+tc.name
				raw, err = json.Marshal(fields)
				require.NoError(t, err)
				updated, err := invokeGRPC(t, raw)
				require.NoError(t, err)
				assert.JSONEq(t, `"2"`, string(decodeGRPC(t, updated)["revision"]))
				var change gormdb.IssueChange
				require.NoError(t, service.store.GetDB().Where("issue_id=? AND revision=2", id).First(&change).Error)
				changeAgent := issue513ActorReference(change.Attribution.SourceAgent)
				if tc.agentField != "" || tc.client == "" {
					assert.Equal(t, tc.wantAgent, changeAgent, "explicit update agent must survive and unevidenced agent must remain unknown")
				} else {
					assert.Contains(t, []string{"", tc.client}, changeAgent, "client-only update must not fabricate another agent")
				}
				assert.Equal(t, "client", change.Actor.Kind)
				assert.Equal(t, "client:"+identity.KeycardID, issue513ActorReference(change.Actor.Reference))
			})
		}
	})
	for _, test := range []struct {
		field string
		value any
	}{
		{"status", "closed"},
		{"title", "Synthetic forbidden comment title"},
		{"priority", "high"},
		{"type", "bug"},
		{"labels", []string{"unexpected-material"}},
		{"owner_label", "unexpected-owner"},
		{"recipient_label", "unexpected-recipient"},
	} {
		t.Run("public_mcp_comment_cannot_mutate_"+test.field, func(t *testing.T) {
			id := create(t, "comment-material-"+test.field)
			if test.field == "status" {
				resolved := publicMCP(t, id, map[string]any{"action": "update", "status": "resolved", "expected_revision": "1", "format": "json"})
				require.Nil(t, resolved.Error, "status test must start in a genuinely closeable state")
			}
			before, originalComments, err := service.issueStore.GetIssue(t.Context(), id)
			require.NoError(t, err)
			counts := issue513IssueDomainCounts(t, service)
			response := publicMCP(t, id, map[string]any{"action": "comment", "body": "Synthetic standalone comment with extra material", "expected_revision": fmt.Sprint(before.Revision), test.field: test.value})
			after, comments, err := service.issueStore.GetIssue(t.Context(), id)
			require.NoError(t, err)
			if response.Error != nil {
				assert.True(t, reflect.DeepEqual(before, after), "refused comment must leave the whole parent unchanged")
				assert.True(t, reflect.DeepEqual(originalComments, comments), "refused comment must not append a companion")
				assert.Equal(t, counts, issue513IssueDomainCounts(t, service))
			} else {
				after.UpdatedAt = before.UpdatedAt
				assert.True(t, reflect.DeepEqual(before, after), "comment action must not apply submitted material fields")
				assert.Equal(t, before.Revision, after.Revision, "comment action must not advance material revision")
				assert.Equal(t, counts["issue_changes"], issue513IssueDomainCounts(t, service)["issue_changes"], "comment action must not append material history")
				assert.Equal(t, len(originalComments)+1, len(comments), "accepted ordinary comment appends exactly one record")
			}
		})
	}
}
