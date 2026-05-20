package retrieval

import (
	"testing"
	"time"

	"github.com/thebtf/engram/pkg/models"
)

func TestScore_NewMemory(t *testing.T) {
	now := time.Now()
	m := &models.Memory{
		CreatedAt:      now.Add(-1 * time.Hour),
		ImportanceBase: 0.5,
	}
	result := Score(m, 0.8, now)
	if result.Score <= 0 {
		t.Errorf("new memory should have positive score, got %v", result.Score)
	}
	if result.Recency < 0.9 {
		t.Errorf("1-hour-old memory should have high recency, got %v", result.Recency)
	}
}

func TestScore_OldMemory(t *testing.T) {
	now := time.Now()
	m := &models.Memory{
		CreatedAt:      now.Add(-720 * time.Hour),
		ImportanceBase: 0.5,
	}
	result := Score(m, 0.8, now)
	if result.Recency > 0.3 {
		t.Errorf("30-day-old memory should have low recency, got %v", result.Recency)
	}
}

func TestScore_HighCitation(t *testing.T) {
	now := time.Now()
	cited := &models.Memory{
		CreatedAt:      now.Add(-24 * time.Hour),
		ImportanceBase: 0.5,
		CitationCount:  10,
		InjectionCount: 10,
	}
	uncited := &models.Memory{
		CreatedAt:      now.Add(-24 * time.Hour),
		ImportanceBase: 0.5,
		CitationCount:  0,
		InjectionCount: 10,
	}
	scoreCited := Score(cited, 0.5, now)
	scoreUncited := Score(uncited, 0.5, now)
	if scoreCited.Score <= scoreUncited.Score {
		t.Errorf("cited memory should score higher: cited=%v, uncited=%v", scoreCited.Score, scoreUncited.Score)
	}
}

func TestRRF(t *testing.T) {
	a := []int64{1, 2, 3}
	b := []int64{2, 3, 4}
	merged := RRF(a, b, 60)
	if len(merged) != 4 {
		t.Fatalf("expected 4 unique IDs, got %d", len(merged))
	}
	if merged[0] != 2 {
		t.Errorf("ID 2 should rank first (appears in both lists), got %d", merged[0])
	}
}
