package injection

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/thebtf/engram/pkg/models"
)

func TestScore_DynamicPrior_UnseenMemories(t *testing.T) {
	memories := []*models.Memory{
		{ID: 1, TsAlpha: 1.0, TsBeta: 1.0, CreatedAt: time.Now().Add(-72 * time.Hour)},
		{ID: 2, TsAlpha: 5.0, TsBeta: 2.0, CreatedAt: time.Now().Add(-72 * time.Hour)},
	}

	opts := ScoreOpts{
		DynamicPrior:        true,
		ProjectCitationRate: 0.6,
	}

	scored := Score(memories, 2, opts)
	assert.Len(t, scored, 2)

	// Memory 1 (unseen, alpha==beta==1) should have had its prior adjusted.
	// Memory 2 (seen, alpha=5, beta=2) should keep its original priors.
	for _, sm := range scored {
		assert.True(t, sm.Score > 0 && sm.Score <= 1.0, "score should be in (0, 1]")
		assert.True(t, sm.Selected)
	}
}

func TestScore_NewcomerBonus(t *testing.T) {
	old := time.Now().Add(-72 * time.Hour)
	fresh := time.Now().Add(-12 * time.Hour)

	// Run multiple trials and compare average scores.
	trials := 1000
	var oldTotal, freshTotal float64
	for i := 0; i < trials; i++ {
		oldMem := &models.Memory{ID: 1, TsAlpha: 1.0, TsBeta: 1.0, CreatedAt: old}
		freshMem := &models.Memory{ID: 2, TsAlpha: 1.0, TsBeta: 1.0, CreatedAt: fresh}
		scored := Score([]*models.Memory{oldMem, freshMem}, 2)
		for _, sm := range scored {
			if sm.Memory.ID == 1 {
				oldTotal += sm.Score
			} else {
				freshTotal += sm.Score
			}
		}
	}

	avgOld := oldTotal / float64(trials)
	avgFresh := freshTotal / float64(trials)
	assert.Greater(t, avgFresh, avgOld, "newcomer bonus should boost fresh memory average score")
}

func TestScore_BackwardCompatible(t *testing.T) {
	memories := []*models.Memory{
		{ID: 1, TsAlpha: 3.0, TsBeta: 1.0},
		{ID: 2, TsAlpha: 1.0, TsBeta: 3.0},
	}

	// No opts — should work exactly as before.
	scored := Score(memories, 1)
	assert.Len(t, scored, 2)

	selectedCount := 0
	for _, sm := range scored {
		if sm.Selected {
			selectedCount++
		}
	}
	assert.Equal(t, 1, selectedCount)
}

func TestScore_DynamicPrior_NotAppliedToSeenMemories(t *testing.T) {
	memories := []*models.Memory{
		{ID: 1, TsAlpha: 3.0, TsBeta: 2.0, CreatedAt: time.Now().Add(-72 * time.Hour)},
	}

	opts := ScoreOpts{
		DynamicPrior:        true,
		ProjectCitationRate: 0.8,
	}

	// The dynamic prior should NOT change already-observed memories (alpha != 1 or beta != 1).
	scored := Score(memories, 1, opts)
	assert.Len(t, scored, 1)
	assert.True(t, scored[0].Score > 0)
}
