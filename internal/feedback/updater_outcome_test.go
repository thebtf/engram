package feedback

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMultiplierTable_AllOutcomesCovered(t *testing.T) {
	outcomes := []string{"success", "partial", "failure", "abandoned", ""}
	for _, outcome := range outcomes {
		mult, ok := multiplierTable[outcome]
		assert.True(t, ok, "outcome %q should be in multiplier table", outcome)
		assert.GreaterOrEqual(t, mult.citedAlpha, 0.0)
		assert.GreaterOrEqual(t, mult.uncitedBeta, 0.0)
		assert.GreaterOrEqual(t, mult.violatedBeta, 0.0)
	}
}

func TestMultiplierTable_SuccessNoUncitedPenalty(t *testing.T) {
	mult := multiplierTable["success"]
	assert.Equal(t, 0.0, mult.uncitedBeta, "success should not penalize uncited memories (they were followed)")
	assert.Equal(t, 2.0, mult.citedAlpha, "success should give extra reward to cited memories")
}

func TestMultiplierTable_FailureStrongPenalty(t *testing.T) {
	mult := multiplierTable["failure"]
	assert.Equal(t, 2.0, mult.uncitedBeta, "failure should strongly penalize uncited memories")
	assert.Equal(t, 5.0, mult.violatedBeta, "failure should very strongly penalize violated rules")
	assert.Equal(t, 0.5, mult.citedAlpha, "failure should give weak reward to cited memories")
}

func TestMultiplierTable_AbandonedSkipsAll(t *testing.T) {
	mult := multiplierTable["abandoned"]
	assert.Equal(t, 0.0, mult.citedAlpha)
	assert.Equal(t, 0.0, mult.uncitedBeta)
	assert.Equal(t, 0.0, mult.violatedBeta)
}

func TestCalculateImportance_BehaviorPreserved(t *testing.T) {
	tests := []struct {
		name          string
		currentBase   float64
		citationCount int
		expected      float64
	}{
		{"zero citations", 0.5, 0, 0.5},
		{"one citation", 0.5, 1, 0.5 * 0.6931471805599453}, // 0.5 * ln(2) ≈ 0.347 < 0.5, returns base
		{"many citations", 0.3, 10, 0.3 * 2.3978952727983707},
		{"caps at 1.0", 0.9, 100, 1.0},
		{"negative base defaults to 0.5", -0.1, 1, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calculateImportance(tt.currentBase, tt.citationCount)
			if tt.expected > 1.0 {
				assert.LessOrEqual(t, result, 1.0)
			} else if tt.name == "one citation" {
				assert.Equal(t, tt.currentBase, result, "should not decrease below current base")
			} else {
				assert.InDelta(t, tt.expected, result, 0.01)
			}
		})
	}
}
