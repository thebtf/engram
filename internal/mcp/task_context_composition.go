package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskcontext"
)

// The native transport has a 30ms outer reserve; leave another bounded slice
// inside its deadline for this RPC's serialization and the remaining IPC hops.
const taskCompositionReserve = 30 * time.Millisecond

func (server *Server) handleCodebaseTaskContext(parent context.Context, raw json.RawMessage) (string, error) {
	var request taskcontext.Request
	if taskcontext.Decode(bytes.NewReader(raw), &request) != nil || request.Validate() != nil {
		encoded, err := taskcontext.Encode(taskcontext.Closed("unavailable", "INVALID_REQUEST"), taskcontext.DefaultMaxBytes)
		return string(bytes.TrimSpace(encoded)), err
	}
	ctx, cancel := request.Context(parent, time.Now())
	defer cancel()
	finish := func(response taskcontext.Response) (string, error) {
		if ctx.Err() != nil {
			response = taskcontext.Closed("deadline_exceeded", "DEADLINE_EXCEEDED")
		}
		encoded, err := taskcontext.Encode(response, request.OutputLimit())
		return string(bytes.TrimSpace(encoded)), err
	}
	if _, err := codebaseContextCallerInput(ctx); err != nil || taskcontext.NativeLocator(ctx) == "" {
		return finish(taskcontext.Closed("forbidden", "PERMISSION_DENIED"))
	}
	project := ProjectFromContext(ctx)
	if !projectidentity.ValidProjectID(project) {
		return finish(taskcontext.Closed("context_required", "PROJECT_CONTEXT_REQUIRED"))
	}
	deadline, ok := ctx.Deadline()
	if !ok || !time.Now().Before(deadline.Add(-taskCompositionReserve)) {
		return finish(taskcontext.Closed("deadline_exceeded", "DEADLINE_EXCEEDED"))
	}
	work, stopWork := context.WithDeadline(ctx, deadline.Add(-taskCompositionReserve))
	defer stopWork()
	code, knowledge := collectTaskProviders(work, func(ctx context.Context) taskcontext.Response {
		text, err := server.handleTaskCodeProvider(ctx, raw)
		var response taskcontext.Response
		if err != nil || json.Unmarshal([]byte(text), &response) != nil || response.Validate() != nil {
			return taskcontext.Closed("unavailable", "SERVER_UNAVAILABLE")
		}
		return response
	}, func(ctx context.Context) taskKnowledgeResult { return server.taskMemories(ctx, project, request.Query) },
		func(ctx context.Context) taskKnowledgeResult { return server.taskNotes(ctx, project, request.Query) },
		func(ctx context.Context) taskKnowledgeResult { return server.taskRules(ctx, project) })
	response := code
	response.CodeStatus, response.CodeReason = code.Status, code.ReasonCode
	response.Knowledge = knowledge
	if len(knowledge.Items) > 0 || code.Status == "ok" {
		response.Status, response.ReasonCode = "ok", "OK"
	} else if knowledge.MemoryStatus == "empty" && knowledge.NoteStatus == "empty" && knowledge.RuleStatus == "empty" && code.Status == "empty" {
		response.Status, response.ReasonCode = "empty", "NO_MATCH"
	} else {
		response.Status, response.ReasonCode = "unavailable", "SERVER_UNAVAILABLE"
		response.Packet = nil
	}
	if response.Validate() != nil {
		return finish(taskcontext.Closed("unavailable", "INVALID_RESPONSE"))
	}
	return finish(response)
}

func collectTaskProviders(work context.Context, codeProvider func(context.Context) taskcontext.Response, memoryProvider, noteProvider, ruleProvider func(context.Context) taskKnowledgeResult) (taskcontext.Response, *taskcontext.Knowledge) {
	codeCh := make(chan taskcontext.Response, 1)
	knowledgeCh := make(chan taskKnowledgeResult, 3)
	go func() { codeCh <- codeProvider(work) }()
	go func() { knowledgeCh <- memoryProvider(work) }()
	go func() { knowledgeCh <- noteProvider(work) }()
	go func() { knowledgeCh <- ruleProvider(work) }()

	code := taskcontext.Closed("deadline_exceeded", "DEADLINE_EXCEEDED")
	knowledge := &taskcontext.Knowledge{MemoryStatus: "deadline_exceeded", NoteStatus: "deadline_exceeded", RuleStatus: "deadline_exceeded", Items: []taskcontext.Evidence{}}
	codePending, knowledgePending := true, 3
	acceptKnowledge := func(result taskKnowledgeResult) {
		if result.kind == "memory" {
			knowledge.MemoryStatus = result.status
		} else if result.kind == "note" {
			knowledge.NoteStatus = result.status
		} else if result.kind == "rule" {
			knowledge.RuleStatus = result.status
		}
		knowledge.Items = append(knowledge.Items, result.items...)
	}
	for codePending || knowledgePending > 0 {
		select {
		case result := <-codeCh:
			code = result
			codePending = false
			codeCh = nil
		case result := <-knowledgeCh:
			acceptKnowledge(result)
			knowledgePending--
		case <-work.Done():
			if codePending {
				select {
				case code = <-codeCh:
				default:
				}
			}
			for knowledgePending > 0 {
				select {
				case result := <-knowledgeCh:
					acceptKnowledge(result)
					knowledgePending--
				default:
					knowledgePending = 0
				}
			}
			codePending = false
		}
	}
	return code, knowledge
}
