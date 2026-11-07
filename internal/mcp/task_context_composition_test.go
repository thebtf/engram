package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/taskcontext"
)

func TestTaskProvidersRunConcurrentlyAndKeepReadyResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := make(chan string, 4)
	canceled := make(chan struct{})
	result := make(chan *taskcontext.Knowledge, 1)
	go func() {
		_, knowledge := collectTaskProviders(ctx,
			func(ctx context.Context) taskcontext.Response {
				started <- "code"
				<-ctx.Done()
				close(canceled)
				return taskcontext.Closed("deadline_exceeded", "DEADLINE_EXCEEDED")
			},
			func(context.Context) taskKnowledgeResult {
				started <- "memory"
				return taskKnowledgeResult{kind: "memory", status: "ok", items: []taskcontext.Evidence{{Kind: "memory", ID: 1}}}
			},
			func(context.Context) taskKnowledgeResult {
				started <- "note"
				return taskKnowledgeResult{kind: "note", status: "empty"}
			},
			func(context.Context) taskKnowledgeResult {
				started <- "rule"
				return taskKnowledgeResult{kind: "rule", status: "empty"}
			})
		result <- knowledge
	}()
	for range 4 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("providers did not start concurrently")
		}
	}
	select {
	case knowledge := <-result:
		require.Equal(t, "ok", knowledge.MemoryStatus)
		require.Equal(t, "empty", knowledge.NoteStatus)
		require.Equal(t, "empty", knowledge.RuleStatus)
		require.Len(t, knowledge.Items, 1)
	case <-time.After(time.Second):
		t.Fatal("collector did not honor cancellation")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("code provider did not receive cancellation")
	}
}

type taskProviderObservedContext struct {
	context.Context
	selecting chan struct{}
}

func (ctx taskProviderObservedContext) Done() <-chan struct{} {
	ctx.selecting <- struct{}{}
	return ctx.Context.Done()
}

func TestTaskProvidersStableEvidenceOrderAndTrim(t *testing.T) {
	run := func(first string) (*taskcontext.Knowledge, []byte, []byte) {
		t.Helper()
		firstGate, restGate := make(chan struct{}), make(chan struct{})
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		observed := taskProviderObservedContext{Context: ctx, selecting: make(chan struct{}, 8)}
		provider := func(kind string, id int64) func(context.Context) taskKnowledgeResult {
			return func(context.Context) taskKnowledgeResult {
				if kind == first {
					<-firstGate
				} else {
					<-restGate
				}
				return taskKnowledgeResult{kind: kind, status: "ok", items: []taskcontext.Evidence{
					{Kind: kind, ID: id, Content: "first ranked witness with enough content to require trimming"},
					{Kind: kind, ID: id + 1, Content: "second ranked witness with enough content to require trimming"},
				}}
			}
		}
		result := make(chan *taskcontext.Knowledge, 1)
		go func() {
			_, knowledge := collectTaskProviders(observed,
				func(context.Context) taskcontext.Response {
					<-restGate
					return taskcontext.Closed("empty", "NO_MATCH")
				},
				provider("memory", 1), provider("note", 3),
				func(context.Context) taskKnowledgeResult {
					<-restGate
					return taskKnowledgeResult{kind: "rule", status: "unavailable"}
				})
			result <- knowledge
		}()
		waitSelect := func() {
			select {
			case <-observed.selecting:
			case <-ctx.Done():
				t.Fatal("collector did not advance")
			}
		}
		waitSelect()
		close(firstGate)
		waitSelect() // Only the first provider can have advanced the collector.
		close(restGate)
		knowledge := <-result
		response := taskcontext.Response{Schema: 1, Status: "ok", ReasonCode: "OK", Knowledge: knowledge}
		full, err := taskcontext.Encode(response, 4096)
		require.NoError(t, err)
		trimmed, err := taskcontext.Encode(response, 420)
		require.NoError(t, err)
		return knowledge, full, trimmed
	}
	memoryFirst, fullMemory, trimmedMemory := run("memory")
	noteFirst, fullNote, trimmedNote := run("note")
	require.Equal(t, "unavailable", memoryFirst.RuleStatus)
	require.Len(t, memoryFirst.Items, 4)
	require.Equal(t, []int64{1, 2, 3, 4}, []int64{memoryFirst.Items[0].ID, memoryFirst.Items[1].ID, memoryFirst.Items[2].ID, memoryFirst.Items[3].ID})
	assert.Equal(t, memoryFirst, noteFirst)
	assert.Equal(t, fullMemory, fullNote)
	assert.Equal(t, trimmedMemory, trimmedNote)
	var response taskcontext.Response
	require.NoError(t, json.Unmarshal(trimmedMemory, &response))
	require.NotEmpty(t, response.Knowledge.Items)
	require.Less(t, len(response.Knowledge.Items), len(memoryFirst.Items))
	require.Equal(t, int64(1), response.Knowledge.Items[0].ID)
}

func TestTaskProvidersKeepReadyKnowledgeWhenPeerIsLate(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, knowledge := collectTaskProviders(ctx,
		func(context.Context) taskcontext.Response { return taskcontext.Closed("empty", "NO_MATCH") },
		func(context.Context) taskKnowledgeResult {
			return taskKnowledgeResult{kind: "memory", status: "ok", items: []taskcontext.Evidence{{Kind: "memory", ID: 1}}}
		},
		func(ctx context.Context) taskKnowledgeResult {
			<-ctx.Done()
			return taskKnowledgeResult{kind: "note", status: "deadline_exceeded"}
		},
		func(context.Context) taskKnowledgeResult {
			return taskKnowledgeResult{kind: "rule", status: "unavailable"}
		})
	require.Equal(t, "ok", knowledge.MemoryStatus)
	require.Equal(t, "deadline_exceeded", knowledge.NoteStatus)
	require.Equal(t, "unavailable", knowledge.RuleStatus)
	require.Equal(t, []taskcontext.Evidence{{Kind: "memory", ID: 1}}, knowledge.Items)
}
