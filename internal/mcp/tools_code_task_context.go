package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/thebtf/engram/internal/taskcontext"
	"github.com/thebtf/engram/internal/uci"
)

type NativeGitLookup func(context.Context, uci.ResolveContextInput, string, string) (uci.RegisteredCheckoutSelector, error)

type CodebaseTaskContextApplication interface {
	LookupNativeGit(context.Context, uci.ResolveContextInput, string, string) (uci.AuthorizedContext, error)
	TaskCodebase(context.Context, uci.AuthorizedContext, taskcontext.Request) (taskcontext.Packet, error)
}

func (application *UCIContextApplication) SetNativeGitLookup(lookup NativeGitLookup) {
	application.lookupNativeGit = lookup
}

func (application *UCIContextApplication) LookupNativeGit(ctx context.Context, caller uci.ResolveContextInput, project, locator string) (uci.AuthorizedContext, error) {
	if application.lookupNativeGit == nil {
		return uci.AuthorizedContext{}, taskcontext.Refuse("not_ready", "OPERATION_NOT_READY")
	}
	checkout, err := application.lookupNativeGit(ctx, caller, project, locator)
	if err != nil {
		return uci.AuthorizedContext{}, err
	}
	selector, err := uci.CheckoutIndexBindingSelector(checkout)
	if err != nil {
		return uci.AuthorizedContext{}, err
	}
	binding, err := application.AuthorizeIndexBinding(ctx, codebaseContextIndexInput(caller, &selector))
	if err != nil {
		return uci.AuthorizedContext{}, err
	}
	ref := binding.Binding().Context
	if ref == nil {
		return uci.AuthorizedContext{}, taskcontext.Refuse("not_ready", "OPERATION_NOT_READY")
	}
	caller.Ref = ref
	return application.Authorize(ctx, caller)
}

func codebaseTaskContextTool() Tool {
	return Tool{Name: "codebase_task_context", Description: "Read bounded task-relevant project knowledge and, when available and authorized, code from the authenticated native caller's registered actual Git checkout. No registration, indexing, semantic provider, default selection or portable handle. Canonical project evidence never grants code access.", tier: tierCore, InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"schema", "host_session_id", "query", "deadline_unix_ms", "max_bytes"}, "properties": map[string]any{"schema": map[string]any{"type": "integer", "const": 1}, "host_session_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}, "query": map[string]any{"type": "string", "minLength": 1, "maxLength": 16384}, "deadline_unix_ms": map[string]any{"type": "integer", "minimum": 1}, "max_bytes": map[string]any{"type": "integer", "minimum": 0}}}}
}

func (server *Server) hasCodebaseTaskContextApplication() bool {
	application, _, ok := server.codebaseContextApplicationSnapshot()
	if !ok {
		return false
	}
	_, ok = application.(CodebaseTaskContextApplication)
	return ok
}

func (server *Server) hasTaskContextProvider() bool {
	return server.memoryStore != nil || server.versionedDocumentStore != nil || server.behavioralRulesStore != nil || (codeIntelEnabled() && server.hasCodebaseTaskContextApplication())
}

func (server *Server) handleTaskCodeProvider(parent context.Context, raw json.RawMessage) (string, error) {
	if !codeIntelEnabled() || !server.hasCodebaseTaskContextApplication() {
		return "", errors.New("unknown tool: codebase_task_context")
	}
	var request taskcontext.Request
	if taskcontext.Decode(bytes.NewReader(raw), &request) != nil || request.Validate() != nil {
		encoded, _ := taskcontext.Encode(taskcontext.Closed("unavailable", "INVALID_REQUEST"), taskcontext.DefaultMaxBytes)
		return string(bytes.TrimSpace(encoded)), nil
	}
	ctx, cancel := request.Context(parent, time.Now())
	defer cancel()
	finish := func(response taskcontext.Response) (string, error) {
		if ctx.Err() != nil {
			response = taskcontext.Closed("deadline_exceeded", "DEADLINE_EXCEEDED")
		}
		encoded, err := taskcontext.Encode(response, request.OutputLimit())
		return string(bytes.TrimSuffix(encoded, []byte{'\n'})), err
	}
	caller, err := codebaseContextCallerInput(ctx)
	if err != nil {
		return finish(taskcontext.Closed("forbidden", "PERMISSION_DENIED"))
	}
	locator := taskcontext.NativeLocator(ctx)
	if locator == "" {
		return finish(taskcontext.Closed("forbidden", "PERMISSION_DENIED"))
	}
	application, epoch, ok := server.codebaseContextApplicationSnapshot()
	if !ok {
		return finish(taskcontext.Closed("not_ready", "OPERATION_NOT_READY"))
	}
	tasks, ok := application.(CodebaseTaskContextApplication)
	if !ok {
		return finish(taskcontext.Closed("not_ready", "OPERATION_NOT_READY"))
	}
	authorized, err := tasks.LookupNativeGit(ctx, caller, ProjectFromContext(ctx), locator)
	if err != nil {
		return finish(taskcontext.FromError(ctx, err))
	}
	packet, err := tasks.TaskCodebase(ctx, authorized, request)
	if err != nil {
		return finish(taskcontext.FromError(ctx, err))
	}
	if packet.Search.Status == uci.QueryStatusUnavailable {
		return finish(taskcontext.Closed("unavailable", "SOURCE_UNAVAILABLE"))
	}
	response := taskcontext.Closed("empty", "NO_MATCH")
	response.Packet = &packet
	if packet.Search.Items != nil && len(*packet.Search.Items) > 0 {
		if packet.Source == nil || packet.Source.Items == nil || len(*packet.Source.Items) == 0 {
			return finish(taskcontext.Closed("unavailable", "SOURCE_UNAVAILABLE"))
		}
		response.Status, response.ReasonCode = "ok", "OK"
	}
	// Reserve final inspection/framing and receipt bytes before any release.
	preflight, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	if len(preflight)+1024 > request.OutputLimit() {
		return finish(taskcontext.Closed("unavailable", "BUDGET_EXCEEDED"))
	}
	parts := []struct {
		response  *uci.QueryResponse
		operation uci.ExposureOperation
	}{{&packet.Search, uci.ExposureOperationCodeSearch}}
	if packet.Source != nil {
		parts = append(parts, struct {
			response  *uci.QueryResponse
			operation uci.ExposureOperation
		}{packet.Source, uci.ExposureOperationVersionedRead})
	}
	if packet.Calls != nil {
		parts = append(parts, struct {
			response  *uci.QueryResponse
			operation uci.ExposureOperation
		}{packet.Calls, uci.ExposureOperationCodeGraph})
	}
	for _, part := range parts {
		encoded, err := server.releaseCodebaseQueryResponse(codebaseQueryReleaseInput{ctx: ctx, epoch: epoch, authorized: authorized, operation: part.operation, response: *part.response, matches: func(candidate uci.QueryResponse) bool {
			return codebaseQueryResponseHasExactContext(candidate, authorized)
		}, tool: "codebase_task_context"})
		if err != nil {
			return finish(taskcontext.Closed("unavailable", "EXPOSURE_UNAVAILABLE"))
		}
		var released uci.QueryResponse
		if json.Unmarshal([]byte(encoded), &released) != nil || released.Exposure == nil {
			return finish(taskcontext.Closed("unavailable", "EXPOSURE_UNAVAILABLE"))
		}
		*part.response = released
	}
	if response.Validate() != nil {
		return finish(taskcontext.Closed("unavailable", "INVALID_RESPONSE"))
	}
	return finish(response)
}
