package search

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/thebtf/engram/pkg/models"
)

type mockLLMClient struct {
	complete func(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

func (c mockLLMClient) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	return c.complete(ctx, systemPrompt, userPrompt)
}

func TestLLMFilterReturnsEmptySliceWhenLLMSilences(t *testing.T) {
	t.Parallel()

	filter := NewLLMFilter(mockLLMClient{
		complete: func(_ context.Context, _, _ string) (string, error) {
			return "[]", nil
		},
	}, time.Second)

	relevantIDs := filter.FilterByRelevance(context.Background(), testObservations(), "engram", "find silent memories")

	if len(relevantIDs) != 0 {
		t.Fatalf("expected empty slice, got %v", relevantIDs)
	}
}

func TestLLMFilterReturnsAllCandidateIDsOnParseError(t *testing.T) {
	t.Parallel()

	candidates := testObservations()
	filter := NewLLMFilter(mockLLMClient{
		complete: func(_ context.Context, _, _ string) (string, error) {
			return "not-json", nil
		},
	}, time.Second)

	relevantIDs := filter.FilterByRelevance(context.Background(), candidates, "engram", "parse failure")

	assertIDsEqual(t, relevantIDs, []int64{101, 202, 303})
}

func TestLLMFilterReturnsAllCandidateIDsOnTimeout(t *testing.T) {
	t.Parallel()

	candidates := testObservations()
	filter := NewLLMFilter(mockLLMClient{
		complete: func(ctx context.Context, _, _ string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}, 5*time.Millisecond)

	relevantIDs := filter.FilterByRelevance(context.Background(), candidates, "engram", "timeout fallback")

	assertIDsEqual(t, relevantIDs, []int64{101, 202, 303})
}

func testObservations() []*models.Observation {
	return []*models.Observation{
		{
			ID:        101,
			Type:      models.ObsTypeDecision,
			Title:     sql.NullString{String: "Decision", Valid: true},
			Narrative: sql.NullString{String: "Relevant implementation detail", Valid: true},
		},
		{
			ID:        202,
			Type:      models.ObsTypeBugfix,
			Title:     sql.NullString{String: "Bugfix", Valid: true},
			Narrative: sql.NullString{String: "Historical fix context", Valid: true},
		},
		{
			ID:        303,
			Type:      models.ObsTypeGuidance,
			Title:     sql.NullString{String: "Guidance", Valid: true},
			Narrative: sql.NullString{String: "Operator preference", Valid: true},
		},
	}
}

func assertIDsEqual(t *testing.T, got, want []int64) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("unexpected ID count: got %d want %d (%v vs %v)", len(got), len(want), got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected ID at index %d: got %d want %d", i, got[i], want[i])
		}
	}
}

// Anti-stub verification: replacing the empty-set branch with `return candidates`
// would make TestLLMFilterReturnsEmptySliceWhenLLMSilences fail.
