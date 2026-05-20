package lifecycle

import (
	"math"
	"testing"
)

func TestComputeRetrievability(t *testing.T) {
	tests := []struct {
		name        string
		stability   float64
		elapsedDays float64
		wantMin     float64
		wantMax     float64
	}{
		{"new memory", 30, 0, 0.99, 1.0},
		{"30-day observation with stability 15", 15, 30, 0.3, 0.4},
		{"same-day recall", 30, 1, 0.95, 1.0},
		{"very old", 30, 365, 0.05, 0.15},
		{"zero stability", 0, 10, 0, 0},
		{"negative elapsed", 30, -1, 0.99, 1.0},
		{"high stability procedural", 60, 30, 0.6, 0.75},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeRetrievability(tt.stability, tt.elapsedDays)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("ComputeRetrievability(%v, %v) = %v, want [%v, %v]",
					tt.stability, tt.elapsedDays, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestComputeStability(t *testing.T) {
	base := 30.0
	semantic := ComputeStability(base, TierSemantic, EpistemicFact, 0)
	if math.Abs(semantic-45.0) > 0.01 {
		t.Errorf("semantic fact stability = %v, want 45 (30×1.0×1.5)", semantic)
	}

	procedural := ComputeStability(base, TierProcedural, EpistemicFact, 3)
	expected := base * 2.0 * 1.5 * (1 + 3*0.3)
	if math.Abs(procedural-expected) > 0.01 {
		t.Errorf("procedural fact with 3 citations = %v, want %v", procedural, expected)
	}

	working := ComputeStability(base, TierWorking, EpistemicObservation, 0)
	if working > 5 {
		t.Errorf("working observation stability = %v, should be very low", working)
	}
}

func TestReconsolidate(t *testing.T) {
	fresh := Reconsolidate(30, 0.9)
	if fresh <= 30 {
		t.Errorf("reconsolidate with high retrievability should increase stability: got %v", fresh)
	}

	stale := Reconsolidate(30, 0.3)
	if stale != 30 {
		t.Errorf("reconsolidate with low retrievability should not change stability: got %v", stale)
	}

	zero := Reconsolidate(0, 0.9)
	if zero != 30.0 {
		t.Errorf("reconsolidate with zero stability should return default: got %v", zero)
	}
}
