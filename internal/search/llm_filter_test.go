// Package search provides unified search capabilities for engram.
package search

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/thebtf/engram/pkg/models"
)

type llmClientMock struct {
	complete func(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

func (m llmClientMock) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	return m.complete(ctx, systemPrompt, userPrompt)
}

func buildLLMFilterTestCandidates() []*models.Observation {
	return []*models.Observation{
		{
			ID:        101,
			Type:      models.ObsTypeDiscovery,
			Title:     sql.NullString{String: "First", Valid: true},
			Narrative: sql.NullString{String: "First candidate", Valid: true},
		},
		{
			ID:        202,
			Type:      models.ObsTypeBugfix,
			Title:     sql.NullString{String: "Second", Valid: true},
			Narrative: sql.NullString{String: "Second candidate", Valid: true},
		},
	}
}

func TestLLMFilterFilterByRelevance_EmptyResponseSilencesInjection(t *testing.T) {
	t.Parallel()

	filter := NewLLMFilter(llmClientMock{
		complete: func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
			return "[]", nil
		},
	}, time.Second)

	result := filter.FilterByRelevance(context.Background(), buildLLMFilterTestCandidates(), "engram", "fix silence gate")

	assert.Empty(t, result)
	assert.Len(t, result, 0)
}

func TestLLMFilterFilterByRelevance_ParseFailureFallsBackToAllCandidates(t *testing.T) {
	t.Parallel()

	filter := NewLLMFilter(llmClientMock{
		complete: func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
			return "not json", nil
		},
	}, time.Second)

	result := filter.FilterByRelevance(context.Background(), buildLLMFilterTestCandidates(), "engram", "fix silence gate")

	assert.Equal(t, []int64{101, 202}, result)
}

func TestLLMFilterFilterByRelevance_TimeoutFallsBackToAllCandidates(t *testing.T) {
	t.Parallel()

	filter := NewLLMFilter(llmClientMock{
		complete: func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
			return "", context.DeadlineExceeded
		},
	}, time.Millisecond)

	result := filter.FilterByRelevance(context.Background(), buildLLMFilterTestCandidates(), "engram", "fix silence gate")

	assert.Equal(t, []int64{101, 202}, result)
}
