package retrieval

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
)

// ── golden-value score test ──────────────────────────────────────────────────

func TestScore_GoldenValues(t *testing.T) {
	// Fixed reference time for deterministic decay.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("zero hours ago, full importance", func(t *testing.T) {
		m := &models.Memory{
			CreatedAt:      now,
			ImportanceBase: 1.0,
		}
		sm := Score(m, 1.0, now)
		// recency = 0.995^0 = 1.0
		// importance = 1.0
		// score = 0.4*1.0 + 0.3*1.0 + 0.3*1.0 = 1.0
		assertClose(t, "recency", 1.0, sm.Recency, 1e-9)
		assertClose(t, "importance", 1.0, sm.Importance, 1e-9)
		assertClose(t, "score", 1.0, sm.Score, 1e-9)
	})

	t.Run("24 hours ago, mid importance, no citation", func(t *testing.T) {
		m := &models.Memory{
			CreatedAt:      now.Add(-24 * time.Hour),
			ImportanceBase: 0.5,
		}
		sm := Score(m, 0.8, now)
		wantRecency := math.Pow(0.995, 24)
		wantImportance := 0.5
		wantScore := 0.4*0.8 + 0.3*wantRecency + 0.3*wantImportance
		assertClose(t, "recency", wantRecency, sm.Recency, 1e-9)
		assertClose(t, "importance", wantImportance, sm.Importance, 1e-9)
		assertClose(t, "score", wantScore, sm.Score, 1e-9)
	})

	t.Run("100% citation rate boosts importance", func(t *testing.T) {
		m := &models.Memory{
			CreatedAt:      now.Add(-1 * time.Hour),
			ImportanceBase: 0.3,
			CitationCount:  5,
			InjectionCount: 5,
		}
		sm := Score(m, 0.5, now)
		// citationRate = 1.0; importance = 0.3*0.7 + 1.0*0.3 = 0.51
		wantImportance := 0.3*0.7 + 1.0*0.3
		assertClose(t, "importance", wantImportance, sm.Importance, 1e-9)
	})

	t.Run("importance capped at 1.0", func(t *testing.T) {
		m := &models.Memory{
			CreatedAt:      now,
			ImportanceBase: 1.0,
			Confidence:     1.0,
			CitationCount:  100,
			InjectionCount: 1, // citationRate = 100 → capped
		}
		sm := Score(m, 1.0, now)
		if sm.Importance > 1.0 {
			t.Errorf("importance must be ≤ 1.0, got %v", sm.Importance)
		}
	})
}

// ── RRF determinism ──────────────────────────────────────────────────────────

func TestRRF_Determinism(t *testing.T) {
	a := []int64{1, 2, 3, 4, 5}
	b := []int64{3, 2, 1, 5, 6}

	first := RRF(a, b, 60)
	second := RRF(a, b, 60)

	if len(first) != len(second) {
		t.Fatalf("RRF results differ in length: %d vs %d", len(first), len(second))
	}
	// Determinism: two identical calls must produce identical order.
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("non-deterministic at position %d: %d vs %d", i, first[i], second[i])
		}
	}
	// Completeness: all 6 unique IDs must appear.
	if len(first) != 6 {
		t.Fatalf("expected 6 unique IDs, got %d: %v", len(first), first)
	}
	// IDs appearing in both lists (1,2,3,5) must all appear before single-list IDs (4,6).
	// RRF score for a both-list id is strictly greater than a single-list id
	// whose positions are no better (this holds for a=[1,2,3,4,5] b=[3,2,1,5,6]):
	//   worst both-list:  id=5 → pos4 in A + pos3 in B → 1/64 + 1/63 > 1/65 (best single-list)
	bothSet := map[int64]bool{1: true, 2: true, 3: true, 5: true}
	reachedSingleList := false
	for _, id := range first {
		if !bothSet[id] {
			reachedSingleList = true
		}
		if reachedSingleList && bothSet[id] {
			t.Errorf("id %d (in both lists) appears after a single-list id in %v", id, first)
		}
	}
}

// ── mock implementations ─────────────────────────────────────────────────────

type mockMemStore struct {
	ftsResults  []*models.Memory
	byIDResults []*models.Memory
	listResults []*models.Memory
	ftsErr      error
}

func (m *mockMemStore) SearchFTS(_ context.Context, _, _ string, _ int) ([]*models.Memory, error) {
	return m.ftsResults, m.ftsErr
}
func (m *mockMemStore) GetByIDs(_ context.Context, ids []int64) ([]*models.Memory, error) {
	result := make([]*models.Memory, 0)
	for _, r := range m.byIDResults {
		for _, id := range ids {
			if r.ID == id {
				result = append(result, r)
				break
			}
		}
	}
	return result, nil
}
func (m *mockMemStore) List(_ context.Context, _ string, _ int) ([]*models.Memory, error) {
	return m.listResults, nil
}

type mockEmbStore struct {
	results []embedding.SimilarResult
	err     error
}

func (m *mockEmbStore) FindSimilar(_ context.Context, _ []float32, _ int, _ float64) ([]embedding.SimilarResult, error) {
	return m.results, m.err
}

type mockGraphStore struct {
	results []graph.TraversalResult
	err     error
}

func (m *mockGraphStore) Traverse(_ context.Context, _ int64, _ int, _ []string) ([]graph.TraversalResult, error) {
	return m.results, m.err
}

// helper to build a minimal memory.
func mem(id int64, content string) *models.Memory {
	return &models.Memory{
		ID:             id,
		Content:        content,
		CreatedAt:      time.Now().Add(-time.Hour),
		ImportanceBase: 0.5,
	}
}

// ── Tier0 exact match ────────────────────────────────────────────────────────

func TestHybridSearch_Tier0ExactMatch(t *testing.T) {
	ctx := context.Background()
	query := "exact content to match"
	candidate := mem(42, query)

	store := &mockMemStore{
		listResults: []*models.Memory{candidate},
		byIDResults: []*models.Memory{candidate},
		ftsResults:  []*models.Memory{},
	}

	scored, _, err := HybridSearch(ctx, "proj", query, 10, store, nil, nil, HybridOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scored) != 1 {
		t.Fatalf("expected 1 result, got %d", len(scored))
	}
	if scored[0].Memory.ID != 42 {
		t.Errorf("expected id=42, got %d", scored[0].Memory.ID)
	}
	if scored[0].Relevance != 1.0 {
		t.Errorf("Tier0 relevance must be 1.0, got %v", scored[0].Relevance)
	}
}

// ── FTS-only degradation (no embedding service) ──────────────────────────────

func TestHybridSearch_FTSOnlyDegradation(t *testing.T) {
	ctx := context.Background()

	m1 := mem(1, "foo bar")
	m2 := mem(2, "baz qux")
	store := &mockMemStore{
		ftsResults:  []*models.Memory{m1, m2},
		byIDResults: []*models.Memory{m1, m2},
		listResults: []*models.Memory{},
	}

	// embStore = nil → pure FTS.
	scored, _, err := HybridSearch(ctx, "proj", "foo", 10, store, nil, nil, HybridOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scored) == 0 {
		t.Fatal("expected results from FTS degradation path, got none")
	}
}

// ── Flag-OFF byte-identity via flag check ────────────────────────────────────
// This test verifies the flag-gate at the HybridSearch call site is
// structurally reachable; the actual byte-identity of the List path is
// tested via the MCP handler test below.

func TestHybridSearch_MinConfidenceFiltersResults(t *testing.T) {
	ctx := context.Background()

	// One memory with very low importance/recency: score will be near 0.
	old := &models.Memory{
		ID:             10,
		Content:        "old stuff",
		CreatedAt:      time.Now().Add(-8760 * time.Hour), // 1 year old
		ImportanceBase: 0.0,
	}
	store := &mockMemStore{
		ftsResults:  []*models.Memory{old},
		byIDResults: []*models.Memory{old},
		listResults: []*models.Memory{},
	}

	// MinConfidence near 1 should drop the low-scoring memory.
	scored, _, err := HybridSearch(ctx, "proj", "old", 10, store, nil, nil, HybridOptions{
		MinConfidence: 0.99,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scored) != 0 {
		t.Errorf("expected 0 results above MinConfidence=0.99, got %d", len(scored))
	}
}

// ── explain flag populates RankingExplanation ────────────────────────────────

func TestHybridSearch_ExplainPopulated(t *testing.T) {
	ctx := context.Background()

	m1 := mem(1, "some content")
	store := &mockMemStore{
		ftsResults:  []*models.Memory{m1},
		byIDResults: []*models.Memory{m1},
		listResults: []*models.Memory{},
	}

	scored, explanations, err := HybridSearch(ctx, "proj", "content", 10, store, nil, nil, HybridOptions{
		Explain: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scored) == 0 {
		t.Fatal("expected at least one result")
	}
	if len(explanations) != len(scored) {
		t.Errorf("explanations count %d != scored count %d", len(explanations), len(scored))
	}
	e := explanations[0]
	if e.SourceTier == "" {
		t.Error("explanation source_tier must be set")
	}
	if e.FusedScore <= 0 {
		t.Error("explanation fused_score must be positive")
	}
}

// ── Tier2 graph expansion ────────────────────────────────────────────────────

func TestHybridSearch_Tier2GraphExpansion(t *testing.T) {
	ctx := context.Background()

	m1 := mem(1, "primary")
	m2 := mem(2, "graph neighbour")

	store := &mockMemStore{
		ftsResults:  []*models.Memory{m1},
		byIDResults: []*models.Memory{m1, m2},
		listResults: []*models.Memory{},
	}
	gStore := &mockGraphStore{
		results: []graph.TraversalResult{
			{SourceID: 1, TargetID: 2, EdgeType: "related_to", Depth: 1},
		},
	}

	scored, _, err := HybridSearch(ctx, "proj", "primary", 10, store, nil, gStore, HybridOptions{
		ExpandGraph: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Both primary and neighbour should appear.
	ids := make(map[int64]bool)
	for _, sm := range scored {
		ids[sm.Memory.ID] = true
	}
	if !ids[1] {
		t.Error("primary memory (id=1) missing from graph expansion results")
	}
	if !ids[2] {
		t.Error("graph neighbour (id=2) missing from graph expansion results")
	}
	// Primary must score higher than neighbour (no penalty vs 0.85 penalty).
	var scoreM1, scoreM2 float64
	for _, sm := range scored {
		if sm.Memory.ID == 1 {
			scoreM1 = sm.Score
		}
		if sm.Memory.ID == 2 {
			scoreM2 = sm.Score
		}
	}
	if scoreM1 <= scoreM2 {
		t.Errorf("primary score %v should exceed neighbour score %v (graph penalty=0.15)", scoreM1, scoreM2)
	}
}

// ── concurrent FTS+vector (errgroup) ─────────────────────────────────────────

func TestHybridSearch_ConcurrentFTSAndVector(t *testing.T) {
	ctx := context.Background()

	m1 := mem(1, "fts hit")
	m2 := mem(2, "vector hit")
	m3 := mem(3, "both hit")

	store := &mockMemStore{
		ftsResults:  []*models.Memory{m3, m1},
		byIDResults: []*models.Memory{m1, m2, m3},
		listResults: []*models.Memory{},
	}
	embStore := &mockEmbStore{
		results: []embedding.SimilarResult{
			{MemoryID: 3, Similarity: 0.9},
			{MemoryID: 2, Similarity: 0.7},
		},
	}

	scored, _, err := HybridSearch(ctx, "proj", "hit", 10, store, embStore, nil, HybridOptions{
		QueryVec: []float32{0.1, 0.2, 0.3},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scored) == 0 {
		t.Fatal("expected results from FTS+vector fusion")
	}
	// m3 (id=3) appears in both lists — must rank first.
	if scored[0].Memory.ID != 3 {
		t.Errorf("id=3 should rank first (in both FTS and vector), got id=%d", scored[0].Memory.ID)
	}
}

// ── tier filter restricts to specified tiers ─────────────────────────────────

func TestHybridSearch_TierFilter_VectorOnly(t *testing.T) {
	ctx := context.Background()

	m1 := mem(1, "fts-only result")
	m2 := mem(2, "vector result")

	store := &mockMemStore{
		ftsResults:  []*models.Memory{m1},
		byIDResults: []*models.Memory{m1, m2},
		listResults: []*models.Memory{},
	}
	embStore := &mockEmbStore{
		results: []embedding.SimilarResult{
			{MemoryID: 2, Similarity: 0.8},
		},
	}

	// Filter to tier1_vector only — FTS should be skipped.
	scored, _, err := HybridSearch(ctx, "proj", "result", 10, store, embStore, nil, HybridOptions{
		QueryVec:   []float32{0.1},
		TierFilter: []string{"tier1_vector"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, sm := range scored {
		if sm.Memory.ID == 1 {
			t.Error("id=1 (FTS-only) must not appear when tier_filter=tier1_vector")
		}
	}
}

// ── helper ───────────────────────────────────────────────────────────────────

func assertClose(t *testing.T, name string, want, got, tol float64) {
	t.Helper()
	if math.Abs(want-got) > tol {
		t.Errorf("%s: want %.10f, got %.10f (diff %.2e)", name, want, got, math.Abs(want-got))
	}
}
