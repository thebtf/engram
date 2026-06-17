package retrieval

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/thebtf/engram/pkg/models"
)

// TestAssembleRationale_T017 validates all 6 v5-surface fields of RankingRationale.
//
// Anti-stub: replacing AssembleRationale with return RankingRationale{} causes
// at least 5 of 6 field assertions to fail (RecencyDays, Confidence,
// CitationCount, SubstringMatch, FiltersApplied all have non-zero expected values
// in the table below).
func TestAssembleRationale_T017(t *testing.T) {
	now := time.Now().UTC()

	type tc struct {
		name           string
		memory         *models.Memory
		queryText      string
		matched        bool
		filters        []string
		wantTier       string
		wantConfidence float64
		wantCitations  int
		wantMatched    bool
		// wantRecencyDaysMin and Max allow a generous window around the expected
		// computed value so tests do not flake on CI clock jitter.
		wantRecencyDaysMin float64
		wantRecencyDaysMax float64
		wantFilters        []string
	}

	cases := []tc{
		{
			name: "episodic_memory_with_match_and_filters",
			memory: &models.Memory{
				CreatedAt:     now.Add(-72 * time.Hour), // 3 days ago
				Confidence:    0.74,
				CitationCount: 5,
				Tier:          "semantic",
			},
			queryText:          "postgres",
			matched:            true,
			filters:            []string{"project=engram", "confidence_min=0.6"},
			wantTier:           "semantic",
			wantConfidence:     0.74,
			wantCitations:      5,
			wantMatched:        true,
			wantRecencyDaysMin: 2.99,
			wantRecencyDaysMax: 3.01,
			wantFilters:        []string{"project=engram", "confidence_min=0.6"},
		},
		{
			name: "no_query_no_filters_zero_confidence",
			memory: &models.Memory{
				CreatedAt:     now.Add(-24 * time.Hour),
				Confidence:    0.0,
				CitationCount: 0,
				Tier:          "",
			},
			queryText:          "",
			matched:            false,
			filters:            nil,
			wantTier:           "",
			wantConfidence:     0.0,
			wantCitations:      0,
			wantMatched:        false,
			wantRecencyDaysMin: 0.99,
			wantRecencyDaysMax: 1.01,
			wantFilters:        []string{},
		},
		{
			name: "query_present_but_not_matched",
			memory: &models.Memory{
				CreatedAt:     now,
				Confidence:    0.5,
				CitationCount: 2,
				Tier:          "episodic",
			},
			queryText:          "golang",
			matched:            false,
			filters:            []string{"include_superseded=true"},
			wantTier:           "episodic",
			wantConfidence:     0.5,
			wantCitations:      2,
			wantMatched:        false,
			wantRecencyDaysMin: 0.0,
			wantRecencyDaysMax: 0.01,
			wantFilters:        []string{"include_superseded=true"},
		},
		{
			name: "high_citation_procedural_no_filters",
			memory: &models.Memory{
				CreatedAt:     now.Add(-365 * 24 * time.Hour),
				Confidence:    0.95,
				CitationCount: 42,
				Tier:          "procedural",
			},
			queryText:          "terraform",
			matched:            true,
			filters:            []string{},
			wantTier:           "procedural",
			wantConfidence:     0.95,
			wantCitations:      42,
			wantMatched:        true,
			wantRecencyDaysMin: 364.9,
			wantRecencyDaysMax: 365.1,
			wantFilters:        []string{},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := AssembleRationale(c.memory, c.queryText, c.matched, c.filters)

			// Field 1: recency_days
			assert.GreaterOrEqual(t, r.RecencyDays, c.wantRecencyDaysMin,
				"recency_days must be ≥ %v", c.wantRecencyDaysMin)
			assert.LessOrEqual(t, r.RecencyDays, c.wantRecencyDaysMax,
				"recency_days must be ≤ %v", c.wantRecencyDaysMax)

			// Field 2: confidence
			assert.Equal(t, c.wantConfidence, r.Confidence, "confidence mismatch")

			// Field 3: citation_count
			assert.Equal(t, c.wantCitations, r.CitationCount, "citation_count mismatch")

			// Field 4: tier (omitempty — empty string is valid when unset)
			assert.Equal(t, c.wantTier, r.Tier, "tier mismatch")

			// Field 5: substring_match
			assert.Equal(t, c.wantMatched, r.SubstringMatch, "substring_match mismatch")

			// Field 6: filters_applied — must never be nil (JSON nil vs [])
			assert.NotNil(t, r.FiltersApplied, "filters_applied must never be nil")
			assert.Equal(t, c.wantFilters, r.FiltersApplied, "filters_applied mismatch")
		})
	}
}

// BenchmarkAssembleRationale measures the overhead of rationale assembly per
// result. NFR-F3 requires that include_rationale=true adds ≤5ms p95 over the
// flag-OFF baseline. With a trivially cheap implementation (arithmetic only,
// no I/O), this benchmark should run in nanoseconds — well within budget.
//
// G003 evidence: run with `go test -bench=BenchmarkAssembleRationale -benchtime=5s
// ./internal/retrieval/` and confirm ns/op << 5,000,000 (5ms ceiling).
func BenchmarkAssembleRationale(b *testing.B) {
	now := time.Now().UTC()
	mem := &models.Memory{
		CreatedAt:     now.Add(-72 * time.Hour),
		Confidence:    0.74,
		CitationCount: 5,
		Tier:          "semantic",
	}
	filters := []string{"project=engram", "confidence_min=0.6"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = AssembleRationale(mem, "postgres", true, filters)
	}
}

// TestAssembleRationale_StalenessHint covers the rank-3 staleness fields: a memory
// whose content uses relative-time language AND is older than the freshness window
// is flagged Stale with the triggering terms; fresh or absolute-dated memories are not.
func TestAssembleRationale_StalenessHint(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)
	fresh := now.Add(-2 * 24 * time.Hour)

	t.Run("relative + old → stale with terms", func(t *testing.T) {
		mem := &models.Memory{Content: "The default is currently 1536", CreatedAt: old}
		r := AssembleRationale(mem, "", false, nil)
		assert.True(t, r.Stale, "old memory with relative-time language must be flagged stale")
		assert.Contains(t, r.StaleTerms, "currently")
	})

	t.Run("relative + fresh → not stale", func(t *testing.T) {
		mem := &models.Memory{Content: "The default is currently 1536", CreatedAt: fresh}
		r := AssembleRationale(mem, "", false, nil)
		assert.False(t, r.Stale, "fresh memory must not be flagged stale even with relative-time language")
		assert.Empty(t, r.StaleTerms)
	})

	t.Run("absolute date + old → not stale", func(t *testing.T) {
		mem := &models.Memory{Content: "dim unified on 1536 in migration 142", CreatedAt: old}
		r := AssembleRationale(mem, "", false, nil)
		assert.False(t, r.Stale, "absolute-anchored content must not be flagged stale")
	})
}
