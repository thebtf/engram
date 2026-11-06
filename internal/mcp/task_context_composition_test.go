package mcp

import (
	"context"
	"testing"
	"time"

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
