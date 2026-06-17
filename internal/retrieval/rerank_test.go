package retrieval

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/thebtf/engram/pkg/models"
)

// stubCrossEncoder is a deterministic in-memory CrossEncoder for testing the
// rerank stage without any HTTP call. order maps a passage substring to its desired
// rank (0 = most relevant); err, when set, is returned to exercise failure-silence.
type stubCrossEncoder struct {
	// rankByIndex returns, for the given passages, the reranked order. When nil,
	// it reverses the input order (a simple, observable permutation).
	rankByIndex func(passages []string) []RerankResult
	err         error
	called      bool
}

func (s *stubCrossEncoder) Rank(_ context.Context, _ string, passages []string) ([]RerankResult, error) {
	s.called = true
	if s.err != nil {
		return nil, s.err
	}
	if s.rankByIndex != nil {
		return s.rankByIndex(passages), nil
	}
	// Default: reverse order, descending relevance.
	out := make([]RerankResult, len(passages))
	for i := range passages {
		srcIdx := len(passages) - 1 - i
		out[i] = RerankResult{Index: srcIdx, RelevanceScore: 1.0 - float64(i)*0.1}
	}
	return out, nil
}

// makeScored builds a scored slice with descending fusion Score (id N → Score N/100)
// so the initial fusion order is id3 > id2 > id1.
func makeScored(ids ...int64) []ScoredMemory {
	out := make([]ScoredMemory, len(ids))
	for i, id := range ids {
		s := float64(id) / 100.0
		out[i] = ScoredMemory{
			Memory:      &models.Memory{ID: id, Content: fmt.Sprintf("content-%d", id), CreatedAt: time.Now()},
			Score:       s,
			orderKey:    s, // mirror Score() constructor: sort key starts equal to composite
			RerankScore: -1,
		}
	}
	return out
}

// finalOrder mirrors HybridSearch's final sort (score desc, id asc tiebreak) and
// returns the resulting id order — the order the caller actually sees.
func finalOrder(scored []ScoredMemory) []int64 {
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].orderKey != scored[j].orderKey {
			return scored[i].orderKey > scored[j].orderKey
		}
		return scored[i].Memory.ID < scored[j].Memory.ID
	})
	ids := make([]int64, len(scored))
	for i, sm := range scored {
		ids[i] = sm.Memory.ID
	}
	return ids
}

func TestRerankApplyCrossEncoder_ReordersByCrossEncoder(t *testing.T) {
	t.Parallel()
	// Fusion order (by Score) is id3 > id2 > id1. The stub reverses → id1 > id2 > id3.
	scored := makeScored(3, 2, 1)
	ce := &stubCrossEncoder{} // default: reverse

	rerankApplyCrossEncoder(context.Background(), ce, "q", scored)

	if !ce.called {
		t.Fatal("expected cross-encoder to be called")
	}
	got := finalOrder(scored)
	want := []int64{1, 2, 3} // reranked order replaces fusion order
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rerank order = %v, want %v (cross-encoder order must REPLACE fusion order)", got, want)
	}
	// RerankScore must be populated (not the -1 sentinel) on reranked candidates.
	for _, sm := range scored {
		if sm.RerankScore < 0 {
			t.Errorf("memory %d: RerankScore = %v, want >= 0 after rerank", sm.Memory.ID, sm.RerankScore)
		}
		// Codex review: the public Score field must remain the honest composite in
		// [0,1] after rerank — only the unexported orderKey carries the synthetic key.
		if sm.Score < 0 || sm.Score > 1 {
			t.Errorf("memory %d: Score = %v after rerank, want composite in [0,1] (rerank must not clobber the public score)", sm.Memory.ID, sm.Score)
		}
	}
}

func TestRerankApplyCrossEncoder_FailureSilentKeepsFusionOrder(t *testing.T) {
	t.Parallel()
	scored := makeScored(3, 2, 1)
	ce := &stubCrossEncoder{err: fmt.Errorf("rerank endpoint down")}

	rerankApplyCrossEncoder(context.Background(), ce, "q", scored)

	got := finalOrder(scored)
	want := []int64{3, 2, 1} // fusion order preserved on error
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("on rerank error, order = %v, want fusion order %v (failure must be silent)", got, want)
	}
	for _, sm := range scored {
		if sm.RerankScore != -1 {
			t.Errorf("memory %d: RerankScore = %v, want -1 sentinel after failed rerank", sm.Memory.ID, sm.RerankScore)
		}
	}
}

func TestRerankApplyCrossEncoder_EmptyResultsKeepsFusionOrder(t *testing.T) {
	t.Parallel()
	scored := makeScored(3, 2, 1)
	ce := &stubCrossEncoder{rankByIndex: func(_ []string) []RerankResult { return nil }}

	rerankApplyCrossEncoder(context.Background(), ce, "q", scored)

	got := finalOrder(scored)
	want := []int64{3, 2, 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("on empty rerank results, order = %v, want fusion order %v", got, want)
	}
}

func TestRerankApplyCrossEncoder_RerankedOutrankUnreranked(t *testing.T) {
	t.Parallel()
	// More candidates than the cap so some are NOT sent to the cross-encoder.
	// The reranked pool must always outrank the un-reranked tail.
	n := RerankMaxCandidates + 10
	ids := make([]int64, n)
	for i := 0; i < n; i++ {
		ids[i] = int64(i + 1)
	}
	scored := makeScored(ids...)

	// Stub promotes the LAST passage in its input pool to rank 0 (highest).
	ce := &stubCrossEncoder{rankByIndex: func(passages []string) []RerankResult {
		out := make([]RerankResult, len(passages))
		for i := range passages {
			srcIdx := len(passages) - 1 - i
			out[i] = RerankResult{Index: srcIdx, RelevanceScore: 1.0 - float64(i)*0.01}
		}
		return out
	}}

	rerankApplyCrossEncoder(context.Background(), ce, "q", scored)
	got := finalOrder(scored)

	// The top RerankMaxCandidates results must all carry a non-sentinel RerankScore;
	// every un-reranked candidate (sentinel -1) must sort strictly below them.
	firstUnreranked := -1
	for i, id := range got {
		var sm ScoredMemory
		for _, c := range scored {
			if c.Memory.ID == id {
				sm = c
				break
			}
		}
		if sm.RerankScore < 0 {
			firstUnreranked = i
			break
		}
	}
	if firstUnreranked < 0 {
		t.Fatal("expected some un-reranked tail candidates")
	}
	if firstUnreranked < RerankMaxCandidates {
		t.Fatalf("un-reranked candidate appeared at rank %d, before all %d reranked candidates", firstUnreranked, RerankMaxCandidates)
	}
}
