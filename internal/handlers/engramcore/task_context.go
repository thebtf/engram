package engramcore

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskcontext"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func (m *Module) proxyNativeTaskContext(parent context.Context, project muxcore.ProjectContext, raw json.RawMessage) (json.RawMessage, error) {
	var request taskcontext.Request
	if taskcontext.Decode(bytes.NewReader(raw), &request) != nil || request.Validate() != nil {
		encoded, _ := json.Marshal(taskcontext.Closed("unavailable", "INVALID_REQUEST"))
		return buildInnerBlock(encoded)
	}
	ctx, cancel := request.Context(parent, time.Now())
	defer cancel()
	var inspection *projectidentity.ProjectInspection
	finish := func(response taskcontext.Response) (json.RawMessage, error) {
		if ctx.Err() != nil {
			response = taskcontext.Closed("deadline_exceeded", "DEADLINE_EXCEEDED")
		}
		response.Project = inspection
		encoded, err := taskcontext.Encode(response, request.OutputLimit())
		if err != nil {
			return nil, err
		}
		return buildInnerBlock(bytes.TrimSuffix(encoded, []byte{'\n'}))
	}
	closed := func(state, reason string) (json.RawMessage, error) { return finish(taskcontext.Closed(state, reason)) }
	sessionID, err := requireUCITransportSession(ctx)
	if err != nil {
		return closed("forbidden", "PERMISSION_DENIED")
	}
	outgoing, err := uciClientOutgoingContext(ctx, m.v3ClientInstanceID)
	if err != nil {
		return closed("forbidden", "PERMISSION_DENIED")
	}
	ctx = outgoing
	discovered, locator, err := nativeCodebaseGitContext(ctx, project.Cwd)
	if err != nil {
		return closed("context_required", "CONTEXT_REQUIRED")
	}
	ctx = discovered
	serverURL, err := m.requireServerURL(project)
	if err != nil {
		return closed("unavailable", "SERVER_UNAVAILABLE")
	}
	checked, err := projectidentity.CheckLogicalProject(ctx, project.Cwd, serverURL, m.envFor(project, config.EnvWorkstationToken))
	if err != nil {
		return closed("context_required", "PROJECT_CONTEXT_REQUIRED")
	}
	if checked.Validate() != nil {
		return closed("context_required", "PROJECT_CONTEXT_REQUIRED")
	}
	inspection = &checked
	if checked.Status != "resolved" || checked.Binding == nil || !projectidentity.ValidProjectID(checked.Binding.ID) {
		return closed("context_required", "PROJECT_CONTEXT_REQUIRED")
	}
	ctx = metadata.AppendToOutgoingContext(ctx, taskcontext.LocatorMetadataKey, locator)
	conn, err := m.pool.getOrDialGRPC(serverURL, m.envFor(project, config.EnvWorkstationToken))
	if err != nil {
		return closed("unavailable", "SERVER_UNAVAILABLE")
	}
	response, err := pb.NewEngramServiceClient(conn).CallTool(ctx, &pb.CallToolRequest{ToolName: "codebase_task_context", ArgumentsJson: raw, SessionId: sessionID, Project: checked.Binding.ID})
	if err != nil {
		switch status.Code(err) {
		case codes.DeadlineExceeded, codes.Canceled:
			return closed("deadline_exceeded", "DEADLINE_EXCEEDED")
		case codes.PermissionDenied, codes.Unauthenticated:
			return closed("forbidden", "PERMISSION_DENIED")
		case codes.Unimplemented:
			return closed("not_ready", "OPERATION_NOT_READY")
		default:
			return closed("unavailable", "SERVER_UNAVAILABLE")
		}
	}
	if response == nil || response.IsError {
		return closed("not_ready", "OPERATION_NOT_READY")
	}
	if response.CanonicalProject != checked.Binding.ID {
		return closed("context_required", "PROJECT_CONTEXT_MISMATCH")
	}
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(response.ContentJson, &envelope) != nil || envelope.IsError || len(envelope.Content) != 1 || envelope.Content[0].Type != "text" {
		return closed("not_ready", "OPERATION_NOT_READY")
	}
	var result taskcontext.Response
	if json.Unmarshal([]byte(envelope.Content[0].Text), &result) != nil || result.Validate() != nil {
		return closed("unavailable", "INVALID_RESPONSE")
	}
	return finish(result)
}
