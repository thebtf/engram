package grpcserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	gormlib "gorm.io/gorm"
)

type uciRequestCorrelationHandler struct {
	correlation auditcontext.UCIRequestCorrelation
	found       bool
	required    bool
	calls       int
}

func (handler *uciRequestCorrelationHandler) HandleToolCall(ctx context.Context, _ string, _ []byte) ([]byte, bool, error) {
	handler.correlation, handler.found = auditcontext.UCIRequestCorrelationFromContext(ctx)
	handler.required = auditcontext.UCIRequestCorrelationRequired(ctx)
	handler.calls++
	return []byte(`[]`), false, nil
}

func (*uciRequestCorrelationHandler) ToolDefinitions() []ToolDef   { return nil }
func (*uciRequestCorrelationHandler) ServerInfo() (string, string) { return "test", "test" }

func TestCallToolCarriesOneValidatedUCIRequestCorrelation(t *testing.T) {
	for _, test := range []struct {
		name      string
		requestID json.RawMessage
	}{
		{name: "opaque string", requestID: json.RawMessage(`"daemon request/42"`)},
		{name: "numeric", requestID: json.RawMessage(`42`)},
		{name: "numeric lexical", requestID: json.RawMessage(`42.0`)},
		{name: "unicode", requestID: json.RawMessage(`"операция 42"`)},
		{name: "empty string", requestID: json.RawMessage(`""`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			correlation, ok := auditcontext.NewUCIRequestCorrelation(test.requestID)
			require.True(t, ok)

			handler := &uciRequestCorrelationHandler{}
			server := &Server{handler: handler}
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(auditcontext.UCIRequestCorrelationMetadataKey, correlation.MetadataValue()))
			_, err := server.CallTool(ctx, &pb.CallToolRequest{ToolName: "codebase_search", ArgumentsJson: []byte(`{"query":"fixture"}`)})
			require.NoError(t, err)
			require.Equal(t, 1, handler.calls)
			require.True(t, handler.found)
			require.True(t, handler.required)
			require.Equal(t, correlation.MetadataValue(), handler.correlation.MetadataValue())

			wantID, ok := correlation.JSONRPCID()
			require.True(t, ok)
			gotID, ok := handler.correlation.JSONRPCID()
			require.True(t, ok)
			require.Equal(t, string(wantID), string(gotID))
		})
	}
}

func TestCallToolRejectsMissingInvalidOrDuplicateUCIRequestCorrelation(t *testing.T) {
	valid, ok := auditcontext.NewUCIRequestCorrelation(json.RawMessage(`"outer-id-must-not-leak"`))
	require.True(t, ok)

	for _, test := range []struct {
		name   string
		values []string
		code   codes.Code
	}{
		{name: "missing", code: codes.FailedPrecondition},
		{name: "malformed", values: []string{"%%%"}, code: codes.InvalidArgument},
		{name: "duplicate", values: []string{valid.MetadataValue(), valid.MetadataValue()}, code: codes.InvalidArgument},
	} {
		for _, toolName := range []string{"codebase_search", "codebase_graph", "codebase_read"} {
			t.Run(toolName+"/"+test.name, func(t *testing.T) {
				handler := &uciRequestCorrelationHandler{}
				server := &Server{handler: handler}
				ctx := context.Background()
				if len(test.values) > 0 {
					pairs := make([]string, 0, len(test.values)*2)
					for _, value := range test.values {
						pairs = append(pairs, auditcontext.UCIRequestCorrelationMetadataKey, value)
					}
					ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(pairs...))
				}

				response, err := server.CallTool(ctx, &pb.CallToolRequest{ToolName: toolName, ArgumentsJson: []byte(`{"query":"fixture"}`)})
				require.Nil(t, response)
				require.Error(t, err)
				require.Equal(t, test.code, status.Code(err))
				require.NotContains(t, err.Error(), "outer-id-must-not-leak")
				require.NotContains(t, err.Error(), valid.MetadataValue())
				require.Equal(t, 0, handler.calls)
				require.False(t, handler.found)
			})
		}
	}
}

func TestCallToolPreservesLegacyCodebaseSearchWithoutUCIRequestCorrelation(t *testing.T) {
	handler := &uciRequestCorrelationHandler{}
	server := &Server{handler: handler}
	server.identityResolver = func(_ context.Context, _ *gormlib.DB, selector string, _ *pb.ProjectIdentityV2) (string, error) {
		require.Equal(t, "legacy", selector)
		return "canonical", nil
	}

	response, err := server.CallTool(context.Background(), &pb.CallToolRequest{
		ToolName:      "codebase_search",
		Project:       "legacy",
		ArgumentsJson: []byte(`{"query":"fixture"}`),
	})
	require.NoError(t, err)
	require.Equal(t, "canonical", response.CanonicalProject)
	require.Equal(t, 1, handler.calls)
	require.False(t, handler.found)
	require.False(t, handler.required)
}

func TestCallToolKeepsNonUCICompatibilityWhenCorrelationMetadataIsInvalid(t *testing.T) {
	handler := &uciRequestCorrelationHandler{}
	server := &Server{handler: handler}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(auditcontext.UCIRequestCorrelationMetadataKey, "%%%"))

	_, err := server.CallTool(ctx, &pb.CallToolRequest{ToolName: "recall", ArgumentsJson: []byte(`{}`)})
	require.NoError(t, err)
	require.Equal(t, 1, handler.calls)
	require.False(t, handler.found)
	require.False(t, handler.required)
}
