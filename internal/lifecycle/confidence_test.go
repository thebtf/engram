package lifecycle

import (
	"testing"
	"time"
)

func TestComputeConfidence_Defaults(t *testing.T) {
	got := ComputeConfidence(ConfidenceInputs{})
	if got != 0.5 {
		t.Errorf("empty inputs should produce base confidence 0.5, got %v", got)
	}
}

func TestComputeConfidence_AllMax(t *testing.T) {
	now := time.Now()
	got := ComputeConfidence(ConfidenceInputs{
		RecurrenceCount: 10,
		CitationCount:   50,
		InjectionCount:  50,
		LastConfirmed:   &now,
		UserRatingDelta: 0.2,
	})
	if got > 1.0 {
		t.Errorf("confidence should be capped at 1.0, got %v", got)
	}
	if got < 0.9 {
		t.Errorf("all-max inputs should produce high confidence, got %v", got)
	}
}

func TestComputeConfidence_NegativeRating(t *testing.T) {
	got := ComputeConfidence(ConfidenceInputs{
		UserRatingDelta: -0.2,
	})
	if got > 0.35 || got < 0.25 {
		t.Errorf("negative rating should reduce confidence: got %v, want ~0.3", got)
	}
}

func TestComputeConfidence_Freshness(t *testing.T) {
	recent := time.Now().Add(-24 * time.Hour)
	old := time.Now().Add(-30 * 24 * time.Hour)

	fresh := ComputeConfidence(ConfidenceInputs{LastConfirmed: &recent})
	stale := ComputeConfidence(ConfidenceInputs{LastConfirmed: &old})

	if fresh <= stale {
		t.Errorf("recently confirmed should have higher confidence: fresh=%v, stale=%v", fresh, stale)
	}
}

func TestComputeConfidence_CitationRate(t *testing.T) {
	high := ComputeConfidence(ConfidenceInputs{CitationCount: 10, InjectionCount: 10})
	low := ComputeConfidence(ConfidenceInputs{CitationCount: 1, InjectionCount: 10})

	if high <= low {
		t.Errorf("high citation rate should produce higher confidence: high=%v, low=%v", high, low)
	}
}
