package worker

import (
	"database/sql"
	"testing"

	"github.com/thebtf/engram/pkg/models"
)

func makeObs(id int64, title, narrative string, facts []string) *models.Observation {
	obs := &models.Observation{
		ID:   id,
		Type: "discovery",
		Title: sql.NullString{String: title, Valid: true},
		Narrative: sql.NullString{String: narrative, Valid: narrative != ""},
		Facts: models.JSONStringArray(facts),
	}
	if narrative != "" {
		obs.Subtitle = sql.NullString{String: title, Valid: true}
	}
	return obs
}

func TestEstimateObsTokens(t *testing.T) {
	tests := []struct {
		name     string
		obs      *models.Observation
		wantMin  int
		wantMax  int
	}{
		{
			name:    "short observation",
			obs:     makeObs(1, "Short title", "", nil),
			wantMin: 10,
			wantMax: 30,
		},
		{
			name:    "observation with narrative",
			obs:     makeObs(2, "Title here", "This is a longer narrative explaining what happened in detail.", []string{"fact one", "fact two"}),
			wantMin: 25,
			wantMax: 60,
		},
		{
			name:    "empty observation",
			obs:     makeObs(3, "", "", nil),
			wantMin: 12, // just overhead
			wantMax: 15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := estimateObsTokens(tt.obs)
			if tokens < tt.wantMin || tokens > tt.wantMax {
				t.Errorf("estimateObsTokens() = %d, want between %d and %d", tokens, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestTrimToTokenBudget(t *testing.T) {
	obs := []*models.Observation{
		makeObs(1, "First observation", "Narrative one with some content.", []string{"fact1"}),
		makeObs(2, "Second observation", "Narrative two with some content.", []string{"fact2"}),
		makeObs(3, "Third observation", "Narrative three with some content.", []string{"fact3"}),
		makeObs(4, "Fourth observation", "Narrative four with some content.", []string{"fact4"}),
		makeObs(5, "Fifth observation", "Narrative five with some content.", []string{"fact5"}),
	}

	t.Run("unlimited budget", func(t *testing.T) {
		result, trimmed, _ := trimToTokenBudget(obs, 0)
		if len(result) != 5 {
			t.Errorf("expected 5 observations, got %d", len(result))
		}
		if trimmed != 0 {
			t.Errorf("expected 0 trimmed, got %d", trimmed)
		}
	})

	t.Run("tight budget trims some", func(t *testing.T) {
		// Each observation is roughly 30-40 tokens. Budget of 80 should fit ~2-3.
		result, trimmed, tokens := trimToTokenBudget(obs, 80)
		if len(result) >= 5 {
			t.Errorf("expected fewer than 5 observations with tight budget, got %d", len(result))
		}
		if trimmed == 0 {
			t.Errorf("expected some observations to be trimmed")
		}
		if tokens > 80 {
			t.Errorf("expected token estimate <= 80, got %d", tokens)
		}
	})

	t.Run("very small budget", func(t *testing.T) {
		result, trimmed, _ := trimToTokenBudget(obs, 10)
		if len(result) > 1 {
			t.Errorf("expected at most 1 observation with budget=10, got %d", len(result))
		}
		if trimmed+len(result) != 5 {
			t.Errorf("trimmed(%d) + result(%d) should equal 5", trimmed, len(result))
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		result, trimmed, tokens := trimToTokenBudget(nil, 100)
		if len(result) != 0 {
			t.Errorf("expected empty result, got %d", len(result))
		}
		if trimmed != 0 {
			t.Errorf("expected 0 trimmed, got %d", trimmed)
		}
		if tokens != 0 {
			t.Errorf("expected 0 tokens, got %d", tokens)
		}
	})
}

func TestFilterByIDs(t *testing.T) {
	obs := []*models.Observation{
		makeObs(1, "A", "", nil),
		makeObs(2, "B", "", nil),
		makeObs(3, "C", "", nil),
	}

	ids := map[int64]struct{}{1: {}, 3: {}}
	result := filterByIDs(obs, ids)

	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
	if result[0].ID != 1 || result[1].ID != 3 {
		t.Errorf("expected IDs [1, 3], got [%d, %d]", result[0].ID, result[1].ID)
	}
}

func TestCompactObservation(t *testing.T) {
	obs := makeObs(42, "My Title", "My narrative here", []string{"fact1", "fact2"})
	obs.Subtitle = sql.NullString{String: "My Subtitle", Valid: true}

	compact := compactObservation(obs)

	// Should have: id, type, title, subtitle, narrative, facts
	if compact["id"] != int64(42) {
		t.Errorf("expected id=42, got %v", compact["id"])
	}
	if compact["title"] != "My Title" {
		t.Errorf("expected title='My Title', got %v", compact["title"])
	}
	if compact["subtitle"] != "My Subtitle" {
		t.Errorf("expected subtitle='My Subtitle', got %v", compact["subtitle"])
	}
	if compact["narrative"] != "My narrative here" {
		t.Errorf("expected narrative, got %v", compact["narrative"])
	}
	facts, ok := compact["facts"].(models.JSONStringArray)
	if !ok || len(facts) != 2 {
		t.Errorf("expected 2 facts, got %v", compact["facts"])
	}

	// Should NOT have internal fields
	for _, key := range []string{"file_mtimes", "files_read", "files_modified", "created_at_epoch", "scope", "project", "concepts"} {
		if _, exists := compact[key]; exists {
			t.Errorf("compact format should not contain %q", key)
		}
	}
}

func TestCompactObservation_MinimalFields(t *testing.T) {
	obs := makeObs(1, "Title only", "", nil)

	compact := compactObservation(obs)

	if _, exists := compact["subtitle"]; exists {
		t.Error("should not include empty subtitle")
	}
	if _, exists := compact["narrative"]; exists {
		t.Error("should not include empty narrative")
	}
	if _, exists := compact["facts"]; exists {
		t.Error("should not include empty facts")
	}
}

func TestCompactObservationsWithLimit(t *testing.T) {
	obs := []*models.Observation{
		makeObs(1, "First", "Narrative one.", []string{"fact1"}),
		makeObs(2, "Second", "Narrative two.", []string{"fact2"}),
		makeObs(3, "Third", "Narrative three.", []string{"fact3"}),
	}

	t.Run("all full detail when fullCount negative", func(t *testing.T) {
		result := compactObservationsWithLimit(obs, -1)
		for i, m := range result {
			if _, ok := m["narrative"]; !ok {
				t.Errorf("obs %d should have narrative in full detail mode", i)
			}
		}
	})

	t.Run("first N full, rest condensed", func(t *testing.T) {
		result := compactObservationsWithLimit(obs, 1)
		// First should have narrative
		if _, ok := result[0]["narrative"]; !ok {
			t.Error("obs 0 should have narrative (full detail)")
		}
		if _, ok := result[0]["facts"]; !ok {
			t.Error("obs 0 should have facts (full detail)")
		}
		// Second and third should NOT have narrative or facts
		for _, idx := range []int{1, 2} {
			if _, ok := result[idx]["narrative"]; ok {
				t.Errorf("obs %d should NOT have narrative (condensed)", idx)
			}
			if _, ok := result[idx]["facts"]; ok {
				t.Errorf("obs %d should NOT have facts (condensed)", idx)
			}
			// But should still have id, type, title
			if result[idx]["id"] == nil {
				t.Errorf("obs %d should have id", idx)
			}
			if result[idx]["title"] == nil {
				t.Errorf("obs %d should have title", idx)
			}
		}
	})

	t.Run("fullCount zero means all condensed", func(t *testing.T) {
		result := compactObservationsWithLimit(obs, 0)
		for i, m := range result {
			if _, ok := m["narrative"]; ok {
				t.Errorf("obs %d should NOT have narrative when fullCount=0", i)
			}
		}
	})
}

func TestEstimateTokensWithLimit(t *testing.T) {
	obs := []*models.Observation{
		makeObs(1, "First observation", "Long narrative with many details about the system.", []string{"fact one", "fact two"}),
		makeObs(2, "Second observation", "Another long narrative with more details.", []string{"fact three"}),
		makeObs(3, "Third observation", "Yet another narrative.", nil),
	}

	allFull := estimateTokensWithLimit(obs, -1)
	mixed := estimateTokensWithLimit(obs, 1)   // only first is full
	allCondensed := estimateTokensWithLimit(obs, 0)

	if mixed >= allFull {
		t.Errorf("mixed (%d) should be less than allFull (%d)", mixed, allFull)
	}
	if allCondensed >= mixed {
		t.Errorf("allCondensed (%d) should be less than mixed (%d)", allCondensed, mixed)
	}
	if allCondensed <= 0 {
		t.Errorf("allCondensed should be positive, got %d", allCondensed)
	}
}
