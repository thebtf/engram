package injection

import (
	"testing"

	"github.com/thebtf/engram/pkg/models"
)

// helpers

func makeMemory(id int64, alpha, beta float64) *models.Memory {
	return &models.Memory{
		ID:      id,
		Content: "memory content",
		TsAlpha: alpha,
		TsBeta:  beta,
	}
}

func countSelected(scored []ScoredMemory) int {
	n := 0
	for _, s := range scored {
		if s.Selected {
			n++
		}
	}
	return n
}

// --- Scenario 3: top-K boundary ---
// Tests that the count of Selected entries matches expectations for boundary cases.
// This is deterministic and should pass on first implementation.

func TestScore_TopKBoundary(t *testing.T) {
	t.Run("exactly topK selected", func(t *testing.T) {
		mems := make([]*models.Memory, 30)
		for i := range mems {
			mems[i] = makeMemory(int64(i+1), 5, 1)
		}
		scored := Score(mems, 15)
		if scored == nil {
			t.Fatal("Score returned nil for non-nil input")
		}
		if len(scored) != 30 {
			t.Fatalf("expected 30 results, got %d", len(scored))
		}
		sel := countSelected(scored)
		if sel != 15 {
			t.Errorf("expected exactly 15 selected, got %d", sel)
		}
	})

	t.Run("topK zero means all selected", func(t *testing.T) {
		mems := make([]*models.Memory, 10)
		for i := range mems {
			mems[i] = makeMemory(int64(i+1), 5, 1)
		}
		scored := Score(mems, 0)
		if scored == nil {
			t.Fatal("Score returned nil for non-nil input")
		}
		sel := countSelected(scored)
		if sel != 10 {
			t.Errorf("topK=0: expected all 10 selected, got %d", sel)
		}
	})

	t.Run("topK greater than len selects all", func(t *testing.T) {
		mems := make([]*models.Memory, 5)
		for i := range mems {
			mems[i] = makeMemory(int64(i+1), 5, 1)
		}
		scored := Score(mems, 15)
		sel := countSelected(scored)
		if sel != 5 {
			t.Errorf("topK>len: expected all 5 selected, got %d", sel)
		}
	})

	t.Run("nil input returns nil", func(t *testing.T) {
		scored := Score(nil, 10)
		if scored != nil {
			t.Errorf("expected nil for nil input, got %v", scored)
		}
	})

	t.Run("empty input returns empty", func(t *testing.T) {
		scored := Score([]*models.Memory{}, 10)
		if len(scored) != 0 {
			t.Errorf("expected empty slice for empty input, got %d entries", len(scored))
		}
	})
}

// --- Scenario 1: Exploration — new memories get a chance (FR-A6 bullet 3) ---
// New memories (α=1, β=1) have a wide Beta distribution and must be sampled
// at least once across 100 independent Score calls.
// A deterministic top-K scorer would never pick new memories over proven ones.

func TestScore_ThompsonExploration(t *testing.T) {
	const runs = 100
	const proven = 10
	const novel = 10
	const topK = 10

	// proven memories: high α, low β → most mass near 1.0
	// novel  memories: α=1, β=1 → uniform, wide variance
	mems := make([]*models.Memory, proven+novel)
	for i := 0; i < proven; i++ {
		mems[i] = makeMemory(int64(i+1), 10, 1)
		mems[i].Content = "proven memory"
	}
	for i := 0; i < novel; i++ {
		mems[proven+i] = makeMemory(int64(proven+i+1), 1, 1)
		mems[proven+i].Content = "novel memory"
	}

	// track whether each novel memory was selected at least once
	novelEverSelected := make(map[int64]bool)
	for i := proven; i < proven+novel; i++ {
		novelEverSelected[mems[i].ID] = false
	}

	for run := 0; run < runs; run++ {
		scored := Score(mems, topK)
		for _, s := range scored {
			if s.Selected {
				if _, ok := novelEverSelected[s.Memory.ID]; ok {
					novelEverSelected[s.Memory.ID] = true
				}
			}
		}
	}

	neverSelected := 0
	for id, seen := range novelEverSelected {
		if !seen {
			t.Errorf("novel memory ID=%d was never selected in %d runs (exploration failure)", id, runs)
			neverSelected++
		}
	}
	if neverSelected > 0 {
		t.Logf("FR-A6: %d/%d novel memories were never selected — Thompson Sampling exploration not working", neverSelected, novel)
	}
}

// --- Scenario 2: Exploitation — proven memories dominate (FR-A6 bullet 4) ---
// Proven memories (high α, low β) should be selected in the majority of runs.

func TestScore_ThompsonExploitation(t *testing.T) {
	const runs = 100
	const proven = 5
	const unproven = 5
	const topK = 5
	const minSelectionRate = 0.70

	mems := make([]*models.Memory, proven+unproven)
	for i := 0; i < proven; i++ {
		mems[i] = makeMemory(int64(i+1), 20, 1)
		mems[i].Content = "proven"
	}
	for i := 0; i < unproven; i++ {
		mems[proven+i] = makeMemory(int64(proven+i+1), 1, 20)
		mems[proven+i].Content = "unproven"
	}

	selectionCount := make(map[int64]int)

	for run := 0; run < runs; run++ {
		scored := Score(mems, topK)
		for _, s := range scored {
			if s.Selected {
				selectionCount[s.Memory.ID]++
			}
		}
	}

	for i := 0; i < proven; i++ {
		id := mems[i].ID
		rate := float64(selectionCount[id]) / float64(runs)
		if rate < minSelectionRate {
			t.Errorf("proven memory ID=%d selected in only %.0f%% of runs (want >%.0f%%) — exploitation not working",
				id, rate*100, minSelectionRate*100)
		}
	}
}

// --- Scenario 4: ExplorationRatio ---

func TestExplorationRatio(t *testing.T) {
	t.Run("all exploratory selected returns 1.0", func(t *testing.T) {
		scored := []ScoredMemory{
			{Memory: makeMemory(1, 1, 1), Score: 0.5, Selected: true},
			{Memory: makeMemory(2, 1, 1), Score: 0.6, Selected: true},
			{Memory: makeMemory(3, 1, 1), Score: 0.4, Selected: true},
		}
		ratio := ExplorationRatio(scored)
		if ratio != 1.0 {
			t.Errorf("all exploratory selected: expected ratio 1.0, got %f", ratio)
		}
	})

	t.Run("all proven selected returns 0.0", func(t *testing.T) {
		scored := []ScoredMemory{
			{Memory: makeMemory(1, 20, 1), Score: 0.9, Selected: true},
			{Memory: makeMemory(2, 15, 2), Score: 0.8, Selected: true},
		}
		ratio := ExplorationRatio(scored)
		if ratio != 0.0 {
			t.Errorf("all proven selected: expected ratio 0.0, got %f", ratio)
		}
	})

	t.Run("none selected returns 0.0", func(t *testing.T) {
		scored := []ScoredMemory{
			{Memory: makeMemory(1, 1, 1), Score: 0.5, Selected: false},
			{Memory: makeMemory(2, 1, 1), Score: 0.6, Selected: false},
		}
		ratio := ExplorationRatio(scored)
		if ratio != 0.0 {
			t.Errorf("none selected: expected ratio 0.0, got %f", ratio)
		}
	})

	t.Run("mixed: half exploratory returns 0.5", func(t *testing.T) {
		scored := []ScoredMemory{
			{Memory: makeMemory(1, 1, 1), Score: 0.5, Selected: true},
			{Memory: makeMemory(2, 20, 1), Score: 0.9, Selected: true},
		}
		ratio := ExplorationRatio(scored)
		if ratio != 0.5 {
			t.Errorf("mixed: expected ratio 0.5, got %f", ratio)
		}
	})

	t.Run("empty slice returns 0.0", func(t *testing.T) {
		ratio := ExplorationRatio(nil)
		if ratio != 0.0 {
			t.Errorf("nil input: expected 0.0, got %f", ratio)
		}
	})
}
