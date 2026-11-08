package mcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/thebtf/engram/internal/issues"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/protobuf/proto"
)

func safeNativeIssueError(err error) error {
	var failure *issues.Error
	if errors.As(err, &failure) {
		copy := *failure
		if copy.Code == "ISSUE_NOT_FOUND" {
			copy.Message += ` Use issues(action="list") for authorized project-free discovery.`
		}
		return &copy
	}
	var projectError *projectidentity.LogicalError
	var contextError *uci.ContextError
	if errors.As(err, &projectError) {
		return &issues.Error{Code: projectError.Code, Message: projectError.Code + ": " + projectidentity.RecoveryGuidance(projectError.Code)}
	}
	if errors.As(err, &contextError) {
		code := string(contextError.Code())
		return &issues.Error{Code: code, Message: code + ": " + projectidentity.RecoveryGuidance(code)}
	}
	return issues.Failure("UNAVAILABLE")
}

// Measure the actual escaped MCP content and protobuf reply fields, rather than
// a raw body or a 4 MiB page estimate. The five-byte gRPC frame is included
// conservatively; no partial record is released or silently retried over REST.
func guardNativeIssueEnvelope(ctx context.Context, response *Response) *Response {
	content, err := json.Marshal(response.Result)
	whole, wholeErr := json.Marshal(response)
	if err == nil && wholeErr == nil {
		grpcBytes := proto.Size(&pb.CallToolResponse{ContentJson: content, CanonicalProject: ProjectFromContext(ctx)}) + 5
		if grpcBytes <= nativeIssueEnvelopeBytes && len(whole)+1 <= nativeIssueEnvelopeBytes {
			return response
		}
	}
	failure := issues.Failure("TRANSPORT_RECORD_TOO_LARGE")
	return &Response{JSONRPC: "2.0", ID: response.ID, Error: &Error{Code: -32000, Message: failure.Error(), Data: map[string]any{"error": failure}}}
}
