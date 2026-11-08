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
